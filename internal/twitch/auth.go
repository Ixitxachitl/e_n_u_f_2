package twitch

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"twitchbot/internal/config"
)

// Twitch OAuth endpoints
const (
	twitchTokenURL    = "https://id.twitch.tv/oauth2/token"
	twitchDeviceURL   = "https://id.twitch.tv/oauth2/device"
	twitchValidateURL = "https://id.twitch.tv/oauth2/validate"

	// Scopes the bot needs (chat read/write, whispers, follows lookups).
	twitchScopes = "chat:read chat:edit user:manage:whispers user:read:follows"

	// Refresh when fewer than this many seconds remain on the access token.
	refreshThresholdSecs = 30 * 60 // 30 minutes

	// How often the background routine checks token expiry.
	refreshCheckInterval = 15 * time.Minute
)

// tokenResponse is the JSON payload returned by the Twitch token endpoint.
type tokenResponse struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	ExpiresIn    int      `json:"expires_in"`
	TokenType    string   `json:"token_type"`
	Scope        []string `json:"scope"`
}

// DeviceFlowState holds an in-progress device-code authorization. Only one
// flow is active at a time (single-admin UI, no need for sessions).
type DeviceFlowState struct {
	DeviceCode      string    `json:"-"`
	UserCode        string    `json:"user_code"`
	VerificationURI string    `json:"verification_uri"`
	Interval        int       `json:"interval"`
	ExpiresAt       time.Time `json:"expires_at"`
}

var (
	// refreshMu serializes concurrent refresh attempts.
	refreshMu sync.Mutex

	// activeDeviceFlow holds the in-progress flow (nil when none).
	deviceFlowMu     sync.Mutex
	activeDeviceFlow *DeviceFlowState
)

// StartDeviceFlow begins Twitch's Device Code Flow. The returned state contains
// the user-facing code and verification URI to display in the UI. Callers then
// poll PollDeviceFlow at the recommended interval until the user authorizes.
func StartDeviceFlow(cfg *config.Config) (*DeviceFlowState, error) {
	clientID := cfg.GetClientID()
	if clientID == "" {
		return nil, fmt.Errorf("client_id not configured")
	}

	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("scopes", twitchScopes)

	resp, err := postForm(twitchDeviceURL, form)
	if err != nil {
		return nil, fmt.Errorf("device flow init failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("twitch device init returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var dr struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}
	if err := json.Unmarshal(body, &dr); err != nil {
		return nil, fmt.Errorf("failed to parse device flow response: %w", err)
	}
	if dr.Interval <= 0 {
		dr.Interval = 5
	}

	state := &DeviceFlowState{
		DeviceCode:      dr.DeviceCode,
		UserCode:        dr.UserCode,
		VerificationURI: dr.VerificationURI,
		Interval:        dr.Interval,
		ExpiresAt:       time.Now().Add(time.Duration(dr.ExpiresIn) * time.Second),
	}

	deviceFlowMu.Lock()
	activeDeviceFlow = state
	deviceFlowMu.Unlock()

	return state, nil
}

// PollDeviceFlow polls Twitch once for the access token using the currently
// active device flow. Returns one of: "pending", "authorized", "expired",
// "denied", or "error". On "authorized", the new tokens are persisted to cfg.
func PollDeviceFlow(cfg *config.Config) (status string, err error) {
	deviceFlowMu.Lock()
	state := activeDeviceFlow
	deviceFlowMu.Unlock()

	if state == nil {
		return "error", fmt.Errorf("no device flow in progress")
	}
	if time.Now().After(state.ExpiresAt) {
		clearDeviceFlow()
		return "expired", fmt.Errorf("device code expired — start a new login")
	}

	clientID := cfg.GetClientID()
	if clientID == "" {
		return "error", fmt.Errorf("client_id not configured")
	}

	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("scopes", twitchScopes)
	form.Set("device_code", state.DeviceCode)
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

	resp, err := postForm(twitchTokenURL, form)
	if err != nil {
		return "error", fmt.Errorf("device poll request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		// Twitch returns 400 with a `message` field for pending/denied/expired.
		var errResp struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &errResp)
		msg := strings.ToLower(errResp.Message)
		switch {
		case strings.Contains(msg, "authorization_pending"), strings.Contains(msg, "pending"):
			return "pending", nil
		case strings.Contains(msg, "slow_down"):
			return "pending", nil // caller will keep polling
		case strings.Contains(msg, "expired"):
			clearDeviceFlow()
			return "expired", fmt.Errorf("device code expired — start a new login")
		case strings.Contains(msg, "denied"), strings.Contains(msg, "access_denied"):
			clearDeviceFlow()
			return "denied", fmt.Errorf("user denied authorization")
		}
		return "error", fmt.Errorf("twitch poll returned %d: %s", resp.StatusCode, errResp.Message)
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "error", fmt.Errorf("failed to parse poll response: %w", err)
	}
	if tr.AccessToken == "" {
		return "error", fmt.Errorf("missing access_token in response")
	}

	_ = cfg.SetOAuthToken("oauth:" + tr.AccessToken)
	_ = cfg.SetRefreshToken(tr.RefreshToken)
	if tr.ExpiresIn > 0 {
		_ = cfg.SetTokenExpiresAt(time.Now().Unix() + int64(tr.ExpiresIn))
	}

	clearDeviceFlow()
	log.Printf("Device-code authorization succeeded (token expires in %ds)", tr.ExpiresIn)
	return "authorized", nil
}

// CancelDeviceFlow drops any in-progress device flow.
func CancelDeviceFlow() {
	clearDeviceFlow()
}

func clearDeviceFlow() {
	deviceFlowMu.Lock()
	activeDeviceFlow = nil
	deviceFlowMu.Unlock()
}

// RefreshAccessToken exchanges the stored refresh_token for a fresh
// access/refresh token pair and persists them. Works for both confidential
// clients (client_secret stored, e.g. legacy code-flow setup) and public
// clients (Device Code Flow — no secret required).
//
// Twitch rotates the refresh token on every refresh, so the new refresh_token
// MUST be saved or future refreshes will fail.
func RefreshAccessToken(cfg *config.Config) error {
	refreshMu.Lock()
	defer refreshMu.Unlock()

	clientID := cfg.GetClientID()
	clientSecret := cfg.GetClientSecret() // optional — only set for code-flow setups
	refreshToken := cfg.GetRefreshToken()

	if clientID == "" {
		return fmt.Errorf("cannot refresh: client_id not configured")
	}
	if refreshToken == "" {
		return fmt.Errorf("cannot refresh: no refresh_token stored (re-authorize via the web UI)")
	}

	form := url.Values{}
	form.Set("client_id", clientID)
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)

	resp, err := postForm(twitchTokenURL, form)
	if err != nil {
		return fmt.Errorf("token refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		// 400 "Invalid refresh token" means the refresh token has been
		// invalidated (revoked, password changed, etc.) — re-auth required.
		return fmt.Errorf("twitch refresh returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return fmt.Errorf("failed to parse refresh response: %w", err)
	}
	if tr.AccessToken == "" {
		return fmt.Errorf("twitch refresh response missing access_token")
	}

	if err := cfg.SetOAuthToken("oauth:" + tr.AccessToken); err != nil {
		return fmt.Errorf("failed to persist new access token: %w", err)
	}
	if tr.RefreshToken != "" {
		if err := cfg.SetRefreshToken(tr.RefreshToken); err != nil {
			return fmt.Errorf("failed to persist new refresh token: %w", err)
		}
	}
	if tr.ExpiresIn > 0 {
		_ = cfg.SetTokenExpiresAt(time.Now().Unix() + int64(tr.ExpiresIn))
	}

	log.Printf("OAuth token refreshed (expires in %ds)", tr.ExpiresIn)
	return nil
}

// ValidateToken hits Twitch's /oauth2/validate to confirm the current token is
// still good and refresh the local expires_at from authoritative data.
// Returns expires_in (seconds) on success.
func ValidateToken(cfg *config.Config) (int, error) {
	token := strings.TrimPrefix(cfg.GetOAuthToken(), "oauth:")
	if token == "" {
		return 0, fmt.Errorf("no access token to validate")
	}

	req, err := http.NewRequest("GET", twitchValidateURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "OAuth "+token)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("validate returned %d", resp.StatusCode)
	}

	var v struct {
		ExpiresIn int    `json:"expires_in"`
		Login     string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return 0, err
	}

	if v.ExpiresIn > 0 {
		_ = cfg.SetTokenExpiresAt(time.Now().Unix() + int64(v.ExpiresIn))
	}
	return v.ExpiresIn, nil
}

func postForm(endpoint string, form url.Values) (*http.Response, error) {
	req, err := http.NewRequest("POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 30 * time.Second}
	return client.Do(req)
}
