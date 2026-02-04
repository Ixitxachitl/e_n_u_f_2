package web

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"embed"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"

	"twitchbot/internal/config"
	"twitchbot/internal/database"
	"twitchbot/internal/twitch"
)

//go:embed static/*
var staticFiles embed.FS

// Server represents the web UI server
type Server struct {
	cfg      *config.Config
	manager  *twitch.Manager
	server   *http.Server
	upgrader websocket.Upgrader
	clients  map[*websocket.Conn]bool
	mu       sync.Mutex
}

// NewServer creates a new web server
func NewServer(cfg *config.Config, manager *twitch.Manager) *Server {
	s := &Server{
		cfg:     cfg,
		manager: manager,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		clients: make(map[*websocket.Conn]bool),
	}

	// Set up event handler for real-time updates
	manager.SetEventHandler(s.broadcastEvent)

	return s
}

// isLocalhost checks if the request is from localhost
func isLocalhost(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

// getSessionToken extracts the session token from cookies
func getSessionToken(r *http.Request) string {
	cookie, err := r.Cookie("session")
	if err != nil {
		return ""
	}
	return cookie.Value
}

// authMiddleware wraps a handler and requires authentication
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Always allow localhost without auth
		if isLocalhost(r) {
			next(w, r)
			return
		}

		// Check for valid session
		token := getSessionToken(r)
		if s.cfg.ValidateSession(token) {
			next(w, r)
			return
		}

		// Not authenticated
		httpError(w, "Unauthorized", http.StatusUnauthorized)
	}
}

// Start starts the web server
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// Auth routes (always accessible)
	mux.HandleFunc("/api/auth/status", s.handleAuthStatus)
	mux.HandleFunc("/api/auth/setup", s.handleAuthSetup)
	mux.HandleFunc("/api/auth/login", s.handleAuthLogin)
	mux.HandleFunc("/api/auth/logout", s.handleAuthLogout)
	mux.HandleFunc("/api/auth/change-password", s.authMiddleware(s.handleAuthChangePassword))

	// OAuth routes (protected)
	mux.HandleFunc("/auth/twitch", s.authMiddleware(s.handleTwitchAuth))
	mux.HandleFunc("/auth/callback", s.handleTwitchCallback) // Callback must be accessible
	mux.HandleFunc("/auth/token", s.authMiddleware(s.handleTokenExchange))

	// API routes (protected)
	mux.HandleFunc("/api/status", s.authMiddleware(s.handleStatus))
	mux.HandleFunc("/api/config", s.authMiddleware(s.handleConfig))
	mux.HandleFunc("/api/channels", s.authMiddleware(s.handleChannels))
	mux.HandleFunc("/api/channels/", s.authMiddleware(s.handleChannelAction))
	mux.HandleFunc("/api/live", s.authMiddleware(s.handleLiveChannels))
	mux.HandleFunc("/api/brains", s.authMiddleware(s.handleBrains))
	mux.HandleFunc("/api/brains/", s.authMiddleware(s.handleBrainAction))
	mux.HandleFunc("/api/blacklist", s.authMiddleware(s.handleBlacklist))
	mux.HandleFunc("/api/blacklist/", s.authMiddleware(s.handleBlacklistAction))
	mux.HandleFunc("/api/userblacklist", s.authMiddleware(s.handleUserBlacklist))
	mux.HandleFunc("/api/userblacklist/", s.authMiddleware(s.handleUserBlacklistAction))
	mux.HandleFunc("/api/database", s.authMiddleware(s.handleDatabase))
	mux.HandleFunc("/api/activity", s.authMiddleware(s.handleActivity))
	mux.HandleFunc("/api/logout", s.authMiddleware(s.handleLogout))
	mux.HandleFunc("/ws", s.authMiddleware(s.handleWebSocket))

	// Static files (always accessible - login page needs to load)
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return err
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	s.server = &http.Server{
		Addr:     fmt.Sprintf(":%d", s.cfg.GetWebPort()),
		Handler:  mux,
		ErrorLog: log.New(&tlsErrorFilter{}, "", 0),
	}

	// Also start HTTP server on port+1 for embedded browser (no cert warnings)
	httpPort := s.cfg.GetWebPort() + 1
	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", httpPort),
		Handler: mux,
	}
	go func() {
		log.Printf("Starting HTTP server on port %d (for embedded browser)", httpPort)
		httpServer.ListenAndServe()
	}()

	// Try HTTPS first, fall back to HTTP
	certFile, keyFile := s.getCertPaths()
	if _, err := os.Stat(certFile); os.IsNotExist(err) {
		log.Println("Generating self-signed certificate for HTTPS...")
		if err := s.generateSelfSignedCert(certFile, keyFile); err != nil {
			log.Printf("Failed to generate certificate: %v, falling back to HTTP", err)
			return s.server.ListenAndServe()
		}
	}

	log.Printf("Starting HTTPS server on port %d", s.cfg.GetWebPort())
	return s.server.ListenAndServeTLS(certFile, keyFile)
}

// tlsErrorFilter filters out expected TLS handshake errors from self-signed certs
type tlsErrorFilter struct{}

func (f *tlsErrorFilter) Write(p []byte) (n int, err error) {
	msg := string(p)
	// Suppress expected TLS errors from browsers rejecting self-signed certs
	if strings.Contains(msg, "TLS handshake error") &&
		(strings.Contains(msg, "unknown certificate") ||
			strings.Contains(msg, "EOF") ||
			strings.Contains(msg, "certificate required")) {
		return len(p), nil
	}
	// Pass through other errors
	log.Print(msg)
	return len(p), nil
}

func (s *Server) getCertPaths() (string, string) {
	dataDir := database.GetDataDir()
	return filepath.Join(dataDir, "cert.pem"), filepath.Join(dataDir, "key.pem")
}

func (s *Server) generateSelfSignedCert(certFile, keyFile string) error {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"e_n_u_f 2.0"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour), // 1 year
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("192.168.0.1")},
	}

	// Add common local IPs
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return err
	}

	certOut, err := os.Create(certFile)
	if err != nil {
		return err
	}
	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	certOut.Close()

	keyOut, err := os.Create(keyFile)
	if err != nil {
		return err
	}
	privBytes, _ := x509.MarshalECPrivateKey(priv)
	pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes})
	keyOut.Close()

	log.Printf("Certificate generated: %s", certFile)
	return nil
}

// Stop gracefully stops the web server
func (s *Server) Stop() {
	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.server.Shutdown(ctx)
	}
}

// handleTwitchAuth redirects to Twitch OAuth
func (s *Server) handleTwitchAuth(w http.ResponseWriter, r *http.Request) {
	clientID := s.cfg.GetClientID()
	if clientID == "" {
		httpError(w, "Client ID not configured", http.StatusBadRequest)
		return
	}

	// Build redirect URI from request
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	redirectURI := fmt.Sprintf("%s://%s/auth/callback", scheme, r.Host)

	// Build Twitch OAuth URL with force_verify
	authURL := fmt.Sprintf(
		"https://id.twitch.tv/oauth2/authorize?client_id=%s&redirect_uri=%s&response_type=token&scope=chat:read+chat:edit&force_verify=true",
		clientID,
		redirectURI,
	)

	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

// handleTwitchCallback serves the callback page that extracts the token from URL fragment
func (s *Server) handleTwitchCallback(w http.ResponseWriter, r *http.Request) {
	// The token is in the URL fragment, so we need to use JavaScript to extract it
	html := `<!DOCTYPE html>
<html>
<head>
    <title>Twitch Login</title>
    <style>
        body { background: #0e0e10; color: #efeff1; font-family: sans-serif; display: flex; justify-content: center; align-items: center; height: 100vh; margin: 0; }
        .container { text-align: center; }
        .spinner { border: 4px solid #1f1f23; border-top: 4px solid #9147ff; border-radius: 50%; width: 40px; height: 40px; animation: spin 1s linear infinite; margin: 20px auto; }
        @keyframes spin { 0% { transform: rotate(0deg); } 100% { transform: rotate(360deg); } }
        .error { color: #f44336; }
        .success { color: #00c853; }
    </style>
</head>
<body>
    <div class="container">
        <div class="spinner" id="spinner"></div>
        <p id="status">Processing login...</p>
    </div>
    <script>
        const hash = window.location.hash.substring(1);
        const params = new URLSearchParams(hash);
        const accessToken = params.get('access_token');
        const error = params.get('error');
        const errorDesc = params.get('error_description');
        
        const statusEl = document.getElementById('status');
        const spinnerEl = document.getElementById('spinner');
        
        if (error) {
            spinnerEl.style.display = 'none';
            statusEl.className = 'error';
            statusEl.textContent = 'Login failed: ' + (errorDesc || error);
            setTimeout(() => window.location.href = '/', 3000);
        } else if (accessToken) {
            fetch('/auth/token', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ access_token: accessToken })
            })
            .then(res => res.json())
            .then(data => {
                spinnerEl.style.display = 'none';
                if (data.error) {
                    statusEl.className = 'error';
                    statusEl.textContent = 'Error: ' + data.error;
                } else {
                    statusEl.className = 'success';
                    statusEl.textContent = 'Logged in as ' + data.username + '! Redirecting...';
                }
                setTimeout(() => window.location.href = '/', 2000);
            })
            .catch(err => {
                spinnerEl.style.display = 'none';
                statusEl.className = 'error';
                statusEl.textContent = 'Error: ' + err.message;
                setTimeout(() => window.location.href = '/', 3000);
            });
        } else {
            spinnerEl.style.display = 'none';
            statusEl.className = 'error';
            statusEl.textContent = 'No token received';
            setTimeout(() => window.location.href = '/', 3000);
        }
    </script>
</body>
</html>`
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(html))
}

// handleTokenExchange receives the token from the callback page and validates it
func (s *Server) handleTokenExchange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if req.AccessToken == "" {
		httpError(w, "No token provided", http.StatusBadRequest)
		return
	}

	// Validate token and get user info from Twitch
	client := &http.Client{Timeout: 10 * time.Second}
	httpReq, _ := http.NewRequest("GET", "https://api.twitch.tv/helix/users", nil)
	httpReq.Header.Set("Authorization", "Bearer "+req.AccessToken)
	httpReq.Header.Set("Client-Id", s.cfg.GetClientID())

	resp, err := client.Do(httpReq)
	if err != nil {
		httpError(w, "Failed to validate token", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("Twitch API error: %s", string(body))
		httpError(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	var twitchResp struct {
		Data []struct {
			ID    string `json:"id"`
			Login string `json:"login"`
			Name  string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&twitchResp); err != nil {
		httpError(w, "Failed to parse Twitch response", http.StatusInternalServerError)
		return
	}

	if len(twitchResp.Data) == 0 {
		httpError(w, "No user data returned", http.StatusInternalServerError)
		return
	}

	user := twitchResp.Data[0]

	// Save the token and username
	s.cfg.SetOAuthToken("oauth:" + req.AccessToken)
	s.cfg.SetBotUsername(user.Login)

	log.Printf("Logged in as: %s", user.Login)

	jsonResponse(w, map[string]string{
		"status":   "success",
		"username": user.Login,
		"name":     user.Name,
	})
}

// handleLogout clears the OAuth token
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.cfg.SetOAuthToken("")
	s.cfg.SetBotUsername("")

	jsonResponse(w, map[string]string{"status": "logged_out"})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	// Get system memory stats
	var memoryData map[string]interface{}
	if vmStat, err := mem.VirtualMemory(); err == nil {
		memoryData = map[string]interface{}{
			"total_mb":     float64(vmStat.Total) / 1024 / 1024,
			"used_mb":      float64(vmStat.Used) / 1024 / 1024,
			"available_mb": float64(vmStat.Available) / 1024 / 1024,
			"used_percent": vmStat.UsedPercent,
		}
	}

	// Get app memory stats
	var appMem runtime.MemStats
	runtime.ReadMemStats(&appMem)
	appMemoryData := map[string]interface{}{
		"alloc_mb": float64(appMem.Alloc) / 1024 / 1024,
		"sys_mb":   float64(appMem.Sys) / 1024 / 1024,
	}

	// Get disk stats for the database directory
	var storageData map[string]interface{}
	dbDir := database.GetDataDir()
	if diskStat, err := disk.Usage(dbDir); err == nil {
		storageData = map[string]interface{}{
			"path":         diskStat.Path,
			"total_gb":     float64(diskStat.Total) / 1024 / 1024 / 1024,
			"used_gb":      float64(diskStat.Used) / 1024 / 1024 / 1024,
			"free_gb":      float64(diskStat.Free) / 1024 / 1024 / 1024,
			"used_percent": diskStat.UsedPercent,
		}
	}

	// Get database sizes
	dbStats := s.manager.GetBrainManager().GetDatabaseStats()

	status := map[string]interface{}{
		"configured":    s.cfg.IsConfigured(),
		"client_id_set": s.cfg.GetClientID() != "",
		"channels":      s.manager.GetChannelStatus(),
		"database":      dbStats,
		"memory":        memoryData,
		"app_memory":    appMemoryData,
		"storage":       storageData,
	}
	jsonResponse(w, status)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// Don't expose full token, just show if it's set
		tokenSet := s.cfg.GetOAuthToken() != ""
		clientIDSet := s.cfg.GetClientID() != ""

		// Get bot's profile image
		botUsername := s.cfg.GetBotUsername()
		var botProfileImage string
		if botUsername != "" && clientIDSet && tokenSet {
			profiles := s.getUserProfiles([]string{botUsername}, s.cfg.GetClientID(), s.cfg.GetOAuthToken())
			botProfileImage = profiles[strings.ToLower(botUsername)]
		}

		config := map[string]interface{}{
			"bot_username":      s.cfg.GetBotUsername(),
			"oauth_token_set":   tokenSet,
			"client_id_set":     clientIDSet,
			"message_interval":  s.cfg.GetMessageInterval(),
			"web_port":          s.cfg.GetWebPort(),
			"bot_profile_image": botProfileImage,
			"allow_self_join":   s.cfg.GetAllowSelfJoin(),
		}
		jsonResponse(w, config)

	case http.MethodPut:
		var req struct {
			ClientID        *string `json:"client_id"`
			MessageInterval *int    `json:"message_interval"`
			AllowSelfJoin   *bool   `json:"allow_self_join"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "Invalid request", http.StatusBadRequest)
			return
		}

		if req.ClientID != nil {
			s.cfg.SetClientID(*req.ClientID)
		}
		if req.MessageInterval != nil {
			s.cfg.SetMessageInterval(*req.MessageInterval)
		}
		if req.AllowSelfJoin != nil {
			s.cfg.SetAllowSelfJoin(*req.AllowSelfJoin)
		}

		jsonResponse(w, map[string]string{"status": "updated"})

	default:
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		channels := s.manager.GetChannelStatus()

		// Get profile images for all channels
		clientID := s.cfg.GetClientID()
		oauthToken := s.cfg.GetOAuthToken()
		profileImages := make(map[string]string)
		if clientID != "" && oauthToken != "" {
			channelNames := make([]string, len(channels))
			for i, ch := range channels {
				channelNames[i] = ch.Channel
			}
			profileImages = s.getUserProfiles(channelNames, clientID, oauthToken)
		}

		// Build response with profile images and user IDs
		result := make([]map[string]interface{}, len(channels))
		for i, ch := range channels {
			result[i] = map[string]interface{}{
				"channel":           ch.Channel,
				"connected":         ch.Connected,
				"messages":          ch.Messages,
				"profile_image_url": profileImages[strings.ToLower(ch.Channel)],
				"message_interval":  s.cfg.GetChannelMessageInterval(ch.Channel),
				"user_id":           s.cfg.GetUserIDByUsername(ch.Channel),
			}
		}
		jsonResponse(w, result)

	case http.MethodPost:
		var req struct {
			Channel string `json:"channel"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "Invalid request", http.StatusBadRequest)
			return
		}
		if req.Channel == "" {
			httpError(w, "Channel name required", http.StatusBadRequest)
			return
		}

		if !s.cfg.IsConfigured() {
			httpError(w, "Bot not configured. Please set OAuth token and username first.", http.StatusBadRequest)
			return
		}

		if err := s.manager.JoinChannel(req.Channel); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		jsonResponse(w, map[string]string{"status": "joined", "channel": req.Channel})

	default:
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleChannelAction(w http.ResponseWriter, r *http.Request) {
	channel := strings.TrimPrefix(r.URL.Path, "/api/channels/")

	// Check for /reconnect suffix
	if strings.HasSuffix(channel, "/reconnect") {
		channel = strings.TrimSuffix(channel, "/reconnect")
		if r.Method == http.MethodPost {
			if err := s.manager.ReconnectChannel(channel); err != nil {
				httpError(w, fmt.Sprintf("Failed to reconnect: %v", err), http.StatusInternalServerError)
				return
			}
			jsonResponse(w, map[string]string{"status": "reconnected", "channel": channel})
			return
		}
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check for /interval suffix
	if strings.HasSuffix(channel, "/interval") {
		channel = strings.TrimSuffix(channel, "/interval")
		if r.Method == http.MethodPut {
			var req struct {
				Interval int `json:"interval"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				httpError(w, "Invalid request", http.StatusBadRequest)
				return
			}
			if req.Interval < 1 || req.Interval > 100 {
				httpError(w, "Interval must be between 1 and 100", http.StatusBadRequest)
				return
			}
			s.cfg.SetChannelMessageInterval(channel, req.Interval)
			jsonResponse(w, map[string]interface{}{"status": "updated", "channel": channel, "interval": req.Interval})
			return
		}
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if channel == "" {
		httpError(w, "Channel name required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodDelete:
		s.manager.LeaveChannel(channel)
		jsonResponse(w, map[string]string{"status": "left", "channel": channel})

	default:
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleLiveChannels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get all connected channels
	channels := s.manager.GetChannelStatus()
	if len(channels) == 0 {
		jsonResponse(w, []map[string]interface{}{})
		return
	}

	// Get Client ID and OAuth token for Twitch API
	clientID := s.cfg.GetClientID()
	oauthToken := s.cfg.GetOAuthToken()
	if clientID == "" || oauthToken == "" {
		jsonResponse(w, []map[string]interface{}{})
		return
	}

	// Build list of channel names to check
	channelNames := make([]string, len(channels))
	for i, ch := range channels {
		channelNames[i] = ch.Channel
	}

	// Query Twitch API for live streams
	liveStreams := s.getLiveStreams(channelNames, clientID, oauthToken)

	// Build response with only live channels
	result := []map[string]interface{}{}
	brainMgr := s.manager.GetBrainManager()
	for _, ch := range channels {
		if stream, isLive := liveStreams[strings.ToLower(ch.Channel)]; isLive {
			countdown, interval := brainMgr.GetChannelCountdown(ch.Channel)
			lastMsg := brainMgr.GetLastMessage(ch.Channel)
			result = append(result, map[string]interface{}{
				"channel":           ch.Channel,
				"title":             stream.Title,
				"game":              stream.GameName,
				"viewers":           stream.ViewerCount,
				"started_at":        stream.StartedAt,
				"messages_until":    countdown,
				"message_interval":  interval,
				"last_message":      lastMsg,
				"profile_image_url": stream.ProfileImageURL,
			})
		}
	}

	jsonResponse(w, result)
}

type twitchStream struct {
	Title           string `json:"title"`
	GameName        string `json:"game_name"`
	ViewerCount     int    `json:"viewer_count"`
	StartedAt       string `json:"started_at"`
	ProfileImageURL string `json:"profile_image_url"`
}

func (s *Server) getLiveStreams(channels []string, clientID, oauthToken string) map[string]twitchStream {
	result := make(map[string]twitchStream)
	if len(channels) == 0 {
		return result
	}

	// Build query params
	params := "?"
	for i, ch := range channels {
		if i > 0 {
			params += "&"
		}
		params += "user_login=" + strings.ToLower(ch)
	}

	req, err := http.NewRequest("GET", "https://api.twitch.tv/helix/streams"+params, nil)
	if err != nil {
		log.Printf("Error creating Twitch API request: %v", err)
		return result
	}

	// Remove "oauth:" prefix if present
	token := strings.TrimPrefix(oauthToken, "oauth:")

	req.Header.Set("Client-ID", clientID)
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error calling Twitch API: %v", err)
		return result
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("Twitch API error %d: %s", resp.StatusCode, string(body))
		return result
	}

	var apiResp struct {
		Data []struct {
			UserLogin   string `json:"user_login"`
			Title       string `json:"title"`
			GameName    string `json:"game_name"`
			ViewerCount int    `json:"viewer_count"`
			StartedAt   string `json:"started_at"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		log.Printf("Error decoding Twitch API response: %v", err)
		return result
	}

	for _, stream := range apiResp.Data {
		result[strings.ToLower(stream.UserLogin)] = twitchStream{
			Title:       stream.Title,
			GameName:    stream.GameName,
			ViewerCount: stream.ViewerCount,
			StartedAt:   stream.StartedAt,
		}
	}

	// Fetch profile images for live channels
	if len(result) > 0 {
		liveChannels := make([]string, 0, len(result))
		for ch := range result {
			liveChannels = append(liveChannels, ch)
		}
		profileImages := s.getUserProfiles(liveChannels, clientID, oauthToken)
		for ch, stream := range result {
			stream.ProfileImageURL = profileImages[ch]
			result[ch] = stream
		}
	}

	return result
}

// getUserProfiles fetches profile images for a list of usernames
func (s *Server) getUserProfiles(usernames []string, clientID, oauthToken string) map[string]string {
	result := make(map[string]string)
	if len(usernames) == 0 {
		return result
	}

	// Build query params
	params := "?"
	for i, username := range usernames {
		if i > 0 {
			params += "&"
		}
		params += "login=" + strings.ToLower(username)
	}

	req, err := http.NewRequest("GET", "https://api.twitch.tv/helix/users"+params, nil)
	if err != nil {
		return result
	}

	token := strings.TrimPrefix(oauthToken, "oauth:")
	req.Header.Set("Client-ID", clientID)
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return result
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return result
	}

	var apiResp struct {
		Data []struct {
			Login           string `json:"login"`
			ProfileImageURL string `json:"profile_image_url"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return result
	}

	for _, user := range apiResp.Data {
		result[strings.ToLower(user.Login)] = user.ProfileImageURL
	}

	return result
}

func (s *Server) handleBrains(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	brains := s.manager.GetBrainManager().ListBrains()
	jsonResponse(w, brains)
}

func (s *Server) handleBrainAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/brains/")
	parts := strings.Split(path, "/")

	if len(parts) < 1 || parts[0] == "" {
		httpError(w, "Channel name required", http.StatusBadRequest)
		return
	}

	channel := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch r.Method {
	case http.MethodGet:
		if action == "stats" {
			brain := s.manager.GetBrainManager().GetBrain(channel)
			jsonResponse(w, brain.GetStats())
		} else if action == "transitions" {
			brain := s.manager.GetBrainManager().GetBrain(channel)
			search := r.URL.Query().Get("search")
			page := 1
			pageSize := 50
			if p := r.URL.Query().Get("page"); p != "" {
				fmt.Sscanf(p, "%d", &page)
			}
			if ps := r.URL.Query().Get("pageSize"); ps != "" {
				fmt.Sscanf(ps, "%d", &pageSize)
			}
			if page < 1 {
				page = 1
			}
			if pageSize < 1 || pageSize > 100 {
				pageSize = 50
			}
			jsonResponse(w, brain.GetTransitions(search, page, pageSize))
		} else {
			httpError(w, "Unknown action", http.StatusBadRequest)
		}

	case http.MethodPost:
		if action == "clean" {
			removed := s.manager.GetBrainManager().CleanBrain(channel)
			jsonResponse(w, map[string]int{"rows_removed": removed})
		} else {
			httpError(w, "Unknown action", http.StatusBadRequest)
		}

	case http.MethodPut:
		if action == "transition" {
			var req struct {
				Word1    string `json:"word1"`
				Word2    string `json:"word2"`
				NextWord string `json:"next_word"`
				Count    int    `json:"count"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				httpError(w, "Invalid request", http.StatusBadRequest)
				return
			}
			brain := s.manager.GetBrainManager().GetBrain(channel)
			if err := brain.UpdateTransitionCount(req.Word1, req.Word2, req.NextWord, req.Count); err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			jsonResponse(w, map[string]string{"status": "updated"})
		} else {
			httpError(w, "Unknown action", http.StatusBadRequest)
		}

	case http.MethodDelete:
		if action == "transition" {
			var req struct {
				Word1    string `json:"word1"`
				Word2    string `json:"word2"`
				NextWord string `json:"next_word"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				httpError(w, "Invalid request", http.StatusBadRequest)
				return
			}
			brain := s.manager.GetBrainManager().GetBrain(channel)
			if err := brain.DeleteTransition(req.Word1, req.Word2, req.NextWord); err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			jsonResponse(w, map[string]string{"status": "deleted"})
		} else if action == "" {
			// Erase brain data (clear but keep the database file)
			if err := s.manager.GetBrainManager().EraseBrain(channel); err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			jsonResponse(w, map[string]string{"status": "erased", "channel": channel})
		} else {
			httpError(w, "Unknown action", http.StatusBadRequest)
		}

	default:
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleBlacklist(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jsonResponse(w, s.cfg.GetBlacklistedWords())

	case http.MethodPost:
		var req struct {
			Word string `json:"word"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "Invalid request", http.StatusBadRequest)
			return
		}
		if req.Word == "" {
			httpError(w, "Word required", http.StatusBadRequest)
			return
		}

		s.cfg.AddBlacklistedWord(req.Word)
		jsonResponse(w, map[string]string{"status": "added", "word": req.Word})

	case http.MethodDelete:
		// Clear all blacklisted words
		s.cfg.ClearBlacklist()
		jsonResponse(w, map[string]string{"status": "cleared"})

	default:
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleBlacklistAction(w http.ResponseWriter, r *http.Request) {
	word := strings.TrimPrefix(r.URL.Path, "/api/blacklist/")
	if word == "" {
		httpError(w, "Word required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodDelete:
		s.cfg.RemoveBlacklistedWord(word)
		jsonResponse(w, map[string]string{"status": "removed", "word": word})

	default:
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleUserBlacklist(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jsonResponse(w, s.cfg.GetBlacklistedUsers())

	case http.MethodPost:
		var req struct {
			Username string `json:"username"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "Invalid request", http.StatusBadRequest)
			return
		}
		if req.Username == "" {
			httpError(w, "Username required", http.StatusBadRequest)
			return
		}
		s.cfg.AddBlacklistedUser(req.Username)
		jsonResponse(w, map[string]string{"status": "added", "username": req.Username})

	default:
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleUserBlacklistAction(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimPrefix(r.URL.Path, "/api/userblacklist/")
	if username == "" {
		httpError(w, "Username required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodDelete:
		s.cfg.RemoveBlacklistedUser(username)
		jsonResponse(w, map[string]string{"status": "removed", "username": username})

	default:
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleDatabase(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		stats := s.manager.GetBrainManager().GetDatabaseStats()
		stats["data_directory"] = database.GetDataDir()
		jsonResponse(w, stats)

	case http.MethodPost:
		// Vacuum/optimize database
		db := database.GetDB()
		if _, err := db.Exec("VACUUM"); err != nil {
			httpError(w, "Failed to optimize database", http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]string{"status": "optimized"})

	case http.MethodDelete:
		// Clean all brains
		removed := s.manager.GetBrainManager().CleanAllBrains()
		jsonResponse(w, map[string]int{"rows_removed": removed})

	default:
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	jsonResponse(w, s.cfg.GetRecentActivity())
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	s.mu.Lock()
	s.clients[conn] = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.clients, conn)
		s.mu.Unlock()
		conn.Close()
	}()

	// Keep connection alive and read messages
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

func (s *Server) broadcastEvent(event string, data interface{}) {
	// Save message events to activity log
	if event == "message" {
		if msgData, ok := data.(map[string]string); ok {
			s.cfg.AddActivityEntry(
				msgData["channel"],
				msgData["username"],
				msgData["message"],
				msgData["color"],
				msgData["emotes"],
				msgData["badges"],
			)
		}
	}

	msg := map[string]interface{}{
		"event": event,
		"data":  data,
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for client := range s.clients {
		if err := client.WriteJSON(msg); err != nil {
			client.Close()
			delete(s.clients, client)
		}
	}
}

func jsonResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func httpError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// Auth handlers

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	hasPassword := s.cfg.HasAdminPassword()
	isLocal := isLocalhost(r)
	isAuthenticated := isLocal || s.cfg.ValidateSession(getSessionToken(r))

	jsonResponse(w, map[string]interface{}{
		"needs_setup":   !hasPassword,
		"authenticated": isAuthenticated,
		"is_localhost":  isLocal,
	})
}

func (s *Server) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Only allow setup if no password exists
	if s.cfg.HasAdminPassword() {
		httpError(w, "Admin password already set", http.StatusForbidden)
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if len(req.Password) < 4 {
		httpError(w, "Password must be at least 4 characters", http.StatusBadRequest)
		return
	}

	if err := s.cfg.SetAdminPassword(req.Password); err != nil {
		httpError(w, "Failed to set password", http.StatusInternalServerError)
		return
	}

	// Create a session for the user
	token, err := s.cfg.CreateSession()
	if err != nil {
		httpError(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	// Set session cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400, // 24 hours
	})

	jsonResponse(w, map[string]string{"status": "ok"})
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if !s.cfg.VerifyAdminPassword(req.Password) {
		httpError(w, "Invalid password", http.StatusUnauthorized)
		return
	}

	// Create session
	token, err := s.cfg.CreateSession()
	if err != nil {
		httpError(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	// Set session cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400, // 24 hours
	})

	jsonResponse(w, map[string]string{"status": "ok"})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Delete the session
	token := getSessionToken(r)
	if token != "" {
		s.cfg.DeleteSession(token)
	}

	// Clear the cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})

	jsonResponse(w, map[string]string{"status": "ok"})
}

func (s *Server) handleAuthChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// Verify current password (unless localhost)
	if !isLocalhost(r) && !s.cfg.VerifyAdminPassword(req.CurrentPassword) {
		httpError(w, "Current password is incorrect", http.StatusUnauthorized)
		return
	}

	if len(req.NewPassword) < 4 {
		httpError(w, "New password must be at least 4 characters", http.StatusBadRequest)
		return
	}

	if err := s.cfg.SetAdminPassword(req.NewPassword); err != nil {
		httpError(w, "Failed to change password", http.StatusInternalServerError)
		return
	}

	// Invalidate all existing sessions for security
	s.cfg.DeleteAllSessions()

	// Create a new session for the current user
	token, err := s.cfg.CreateSession()
	if err != nil {
		httpError(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	// Set new session cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400,
	})

	jsonResponse(w, map[string]string{"status": "ok"})
}
