package config

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"time"

	"twitchbot/internal/database"
)

// Config holds all bot configuration
type Config struct {
	mu sync.RWMutex
}

// New creates a new config instance
func New() *Config {
	return &Config{}
}

// Load loads configuration from database
func Load() (*Config, error) {
	if err := database.Init(); err != nil {
		return nil, err
	}
	return New(), nil
}

// GetClientID returns the Twitch Client ID
func (c *Config) GetClientID() string {
	return c.getValue("client_id")
}

// SetClientID sets the Twitch Client ID
func (c *Config) SetClientID(clientID string) error {
	return c.setValue("client_id", clientID)
}

// GetOAuthToken returns the OAuth token
func (c *Config) GetOAuthToken() string {
	return c.getValue("oauth_token")
}

// SetOAuthToken sets the OAuth token
func (c *Config) SetOAuthToken(token string) error {
	return c.setValue("oauth_token", token)
}

// GetBotUsername returns the bot username
func (c *Config) GetBotUsername() string {
	return c.getValue("bot_username")
}

// SetBotUsername sets the bot username
func (c *Config) SetBotUsername(username string) error {
	return c.setValue("bot_username", username)
}

// GetWebPort returns the web port
func (c *Config) GetWebPort() int {
	val := c.getValue("web_port")
	port, _ := strconv.Atoi(val)
	if port == 0 {
		return 24601
	}
	return port
}

// SetWebPort sets the web port
func (c *Config) SetWebPort(port int) error {
	return c.setValue("web_port", strconv.Itoa(port))
}

// GetMessageInterval returns the message interval
func (c *Config) GetMessageInterval() int {
	val := c.getValue("message_interval")
	interval, _ := strconv.Atoi(val)
	if interval == 0 {
		return 35
	}
	return interval
}

// SetMessageInterval sets the message interval
func (c *Config) SetMessageInterval(interval int) error {
	return c.setValue("message_interval", strconv.Itoa(interval))
}

// GetAllowSelfJoin returns whether users can use !join command
func (c *Config) GetAllowSelfJoin() bool {
	val := c.getValue("allow_self_join")
	if val == "" {
		return true // Default to enabled
	}
	return val == "true"
}

// SetAllowSelfJoin sets whether users can use !join command
func (c *Config) SetAllowSelfJoin(allow bool) error {
	return c.setValue("allow_self_join", strconv.FormatBool(allow))
}

// IsConfigured checks if the bot is properly configured
func (c *Config) IsConfigured() bool {
	token := c.GetOAuthToken()
	username := c.GetBotUsername()
	return token != "" && username != ""
}

func (c *Config) getValue(key string) string {
	db := database.GetDB()
	var value string
	db.QueryRow("SELECT value FROM config WHERE key = ?", key).Scan(&value)
	return value
}

func (c *Config) setValue(key, value string) error {
	db := database.GetDB()
	_, err := db.Exec("INSERT OR REPLACE INTO config (key, value) VALUES (?, ?)", key, value)
	return err
}

// Channel operations

// GetChannels returns all enabled channels
func (c *Config) GetChannels() []string {
	db := database.GetDB()
	rows, err := db.Query("SELECT name FROM channels WHERE enabled = 1")
	if err != nil {
		return []string{}
	}
	defer rows.Close()

	var channels []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			channels = append(channels, name)
		}
	}
	return channels
}

// AddChannel adds a new channel
func (c *Config) AddChannel(channel string) error {
	db := database.GetDB()
	_, err := db.Exec("INSERT OR IGNORE INTO channels (name) VALUES (?)", strings.ToLower(channel))
	return err
}

// RemoveChannel removes a channel
func (c *Config) RemoveChannel(channel string) error {
	db := database.GetDB()
	_, err := db.Exec("DELETE FROM channels WHERE name = ?", strings.ToLower(channel))
	return err
}

// SetChannelEnabled enables or disables a channel
func (c *Config) SetChannelEnabled(channel string, enabled bool) error {
	db := database.GetDB()
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	_, err := db.Exec("UPDATE channels SET enabled = ? WHERE name = ?", enabledInt, strings.ToLower(channel))
	return err
}

// IncrementChannelMessages increments the message count for a channel
func (c *Config) IncrementChannelMessages(channel string) error {
	db := database.GetDB()
	_, err := db.Exec("UPDATE channels SET message_count = message_count + 1 WHERE name = ?", strings.ToLower(channel))
	return err
}

// GetChannelStats returns stats for a channel
func (c *Config) GetChannelStats(channel string) (messageCount int64, enabled bool, err error) {
	db := database.GetDB()
	var enabledInt int
	err = db.QueryRow("SELECT message_count, enabled FROM channels WHERE name = ?", strings.ToLower(channel)).Scan(&messageCount, &enabledInt)
	enabled = enabledInt == 1
	return
}

// GetChannelMessageInterval returns the per-channel message interval (0 means use global default)
func (c *Config) GetChannelMessageInterval(channel string) int {
	db := database.GetDB()
	var interval int
	err := db.QueryRow("SELECT message_interval FROM channels WHERE name = ?", strings.ToLower(channel)).Scan(&interval)
	if err != nil || interval == 0 {
		return c.GetMessageInterval() // Fall back to global default
	}
	return interval
}

// SetChannelMessageInterval sets the per-channel message interval (1-100)
func (c *Config) SetChannelMessageInterval(channel string, interval int) error {
	// Clamp to valid range
	if interval < 1 {
		interval = 1
	}
	if interval > 100 {
		interval = 100
	}
	db := database.GetDB()
	_, err := db.Exec("UPDATE channels SET message_interval = ? WHERE name = ?", interval, strings.ToLower(channel))
	return err
}

// GetChannelUseGlobalBrain returns whether a channel uses all brains for generation
func (c *Config) GetChannelUseGlobalBrain(channel string) bool {
	db := database.GetDB()
	var useGlobal int
	err := db.QueryRow("SELECT use_global_brain FROM channels WHERE name = ?", strings.ToLower(channel)).Scan(&useGlobal)
	if err != nil {
		return false
	}
	return useGlobal == 1
}

// SetChannelUseGlobalBrain sets whether a channel uses all brains for generation
func (c *Config) SetChannelUseGlobalBrain(channel string, useGlobal bool) error {
	db := database.GetDB()
	val := 0
	if useGlobal {
		val = 1
	}
	_, err := db.Exec("UPDATE channels SET use_global_brain = ? WHERE name = ?", val, strings.ToLower(channel))
	return err
}

// Blacklist operations

// GetBlacklistedWords returns all blacklisted words
func (c *Config) GetBlacklistedWords() []string {
	db := database.GetDB()
	rows, err := db.Query("SELECT word FROM blacklist ORDER BY word")
	if err != nil {
		return []string{}
	}
	defer rows.Close()

	var words []string
	for rows.Next() {
		var word string
		if err := rows.Scan(&word); err == nil {
			words = append(words, word)
		}
	}
	return words
}

// AddBlacklistedWord adds a word to the blacklist
func (c *Config) AddBlacklistedWord(word string) error {
	db := database.GetDB()
	_, err := db.Exec("INSERT OR IGNORE INTO blacklist (word) VALUES (?)", strings.ToLower(word))
	return err
}

// RemoveBlacklistedWord removes a word from the blacklist
func (c *Config) RemoveBlacklistedWord(word string) error {
	db := database.GetDB()
	_, err := db.Exec("DELETE FROM blacklist WHERE word = ?", strings.ToLower(word))
	return err
}

// ClearBlacklist removes all blacklisted words
func (c *Config) ClearBlacklist() error {
	db := database.GetDB()
	_, err := db.Exec("DELETE FROM blacklist")
	return err
}

// IsBlacklistedWord checks if a word is blacklisted
func (c *Config) IsBlacklistedWord(word string) bool {
	db := database.GetDB()
	var count int
	db.QueryRow("SELECT COUNT(*) FROM blacklist WHERE word = ?", strings.ToLower(word)).Scan(&count)
	return count > 0
}

// User Blacklist Management

// GetBlacklistedUsers returns all blacklisted usernames
func (c *Config) GetBlacklistedUsers() []string {
	db := database.GetDB()
	rows, err := db.Query("SELECT username FROM user_blacklist ORDER BY username")
	if err != nil {
		return []string{}
	}
	defer rows.Close()

	var users []string
	for rows.Next() {
		var username string
		if rows.Scan(&username) == nil {
			users = append(users, username)
		}
	}
	return users
}

// AddBlacklistedUser adds a user to the blacklist
func (c *Config) AddBlacklistedUser(username string) error {
	db := database.GetDB()
	_, err := db.Exec("INSERT OR IGNORE INTO user_blacklist (username) VALUES (?)", strings.ToLower(username))
	return err
}

// RemoveBlacklistedUser removes a user from the blacklist
func (c *Config) RemoveBlacklistedUser(username string) error {
	db := database.GetDB()
	_, err := db.Exec("DELETE FROM user_blacklist WHERE username = ?", strings.ToLower(username))
	return err
}

// IsBlacklistedUser checks if a user is blacklisted
func (c *Config) IsBlacklistedUser(username string) bool {
	db := database.GetDB()
	var count int
	db.QueryRow("SELECT COUNT(*) FROM user_blacklist WHERE username = ?", strings.ToLower(username)).Scan(&count)
	return count > 0
}

// Twitch User ID Tracking

// GetUsernameByID returns the stored username for a Twitch user ID
func (c *Config) GetUsernameByID(twitchID string) string {
	db := database.GetDB()
	var username string
	db.QueryRow("SELECT username FROM twitch_users WHERE twitch_id = ?", twitchID).Scan(&username)
	return username
}

// SetUserIDMapping stores or updates a Twitch user ID to username mapping
func (c *Config) SetUserIDMapping(twitchID, username string) error {
	db := database.GetDB()
	_, err := db.Exec(`
		INSERT INTO twitch_users (twitch_id, username, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(twitch_id) DO UPDATE SET username = ?, updated_at = CURRENT_TIMESTAMP
	`, twitchID, strings.ToLower(username), strings.ToLower(username))
	return err
}

// GetUserIDByUsername returns the Twitch ID for a username (if we have it)
func (c *Config) GetUserIDByUsername(username string) string {
	db := database.GetDB()
	var twitchID string
	db.QueryRow("SELECT twitch_id FROM twitch_users WHERE username = ?", strings.ToLower(username)).Scan(&twitchID)
	return twitchID
}

// RenameChannel updates all references when a username changes
func (c *Config) RenameChannel(oldName, newName string) error {
	db := database.GetDB()
	oldName = strings.ToLower(oldName)
	newName = strings.ToLower(newName)

	// Update channels table
	_, err := db.Exec("UPDATE channels SET name = ? WHERE name = ?", newName, oldName)
	return err
}

// ActivityEntry represents a recent activity log entry
type ActivityEntry struct {
	ID        int64  `json:"id"`
	Channel   string `json:"channel"`
	Username  string `json:"username"`
	Message   string `json:"message"`
	Color     string `json:"color"`
	Emotes    string `json:"emotes"`
	Badges    string `json:"badges"`
	CreatedAt string `json:"created_at"`
}

const maxActivityEntries = 50

// AddActivityEntry adds a new activity entry, keeping only the most recent 50
func (c *Config) AddActivityEntry(channel, username, message, color, emotes, badges string) error {
	db := database.GetDB()

	// Insert new entry
	_, err := db.Exec(`
		INSERT INTO activity (channel, username, message, color, emotes, badges)
		VALUES (?, ?, ?, ?, ?, ?)
	`, channel, username, message, color, emotes, badges)
	if err != nil {
		return err
	}

	// Delete old entries keeping only the most recent 50
	_, err = db.Exec(`
		DELETE FROM activity WHERE id NOT IN (
			SELECT id FROM activity ORDER BY id DESC LIMIT ?
		)
	`, maxActivityEntries)
	return err
}

// GetRecentActivity returns the most recent activity entries
func (c *Config) GetRecentActivity() []ActivityEntry {
	db := database.GetDB()
	rows, err := db.Query(`
		SELECT id, channel, username, message, color, emotes, badges, created_at
		FROM activity
		ORDER BY id DESC
		LIMIT ?
	`, maxActivityEntries)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var entries []ActivityEntry
	for rows.Next() {
		var e ActivityEntry
		rows.Scan(&e.ID, &e.Channel, &e.Username, &e.Message, &e.Color, &e.Emotes, &e.Badges, &e.CreatedAt)
		entries = append(entries, e)
	}
	return entries
}

// Authentication functions

// hashPassword creates a SHA-256 hash of the password with a salt
func hashPassword(password, salt string) string {
	h := sha256.New()
	h.Write([]byte(salt + password))
	return hex.EncodeToString(h.Sum(nil))
}

// generateSalt generates a random salt
func generateSalt() string {
	bytes := make([]byte, 16)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

// generateToken generates a random session token
func generateToken() string {
	bytes := make([]byte, 32)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

// HasAdminPassword checks if an admin password has been set
func (c *Config) HasAdminPassword() bool {
	hash := c.getValue("admin_password_hash")
	return hash != ""
}

// SetAdminPassword sets the admin password (first-time setup or change)
func (c *Config) SetAdminPassword(password string) error {
	salt := generateSalt()
	hash := hashPassword(password, salt)
	if err := c.setValue("admin_password_salt", salt); err != nil {
		return err
	}
	return c.setValue("admin_password_hash", hash)
}

// VerifyAdminPassword checks if the provided password is correct
func (c *Config) VerifyAdminPassword(password string) bool {
	salt := c.getValue("admin_password_salt")
	storedHash := c.getValue("admin_password_hash")
	if salt == "" || storedHash == "" {
		return false
	}
	providedHash := hashPassword(password, salt)
	return subtle.ConstantTimeCompare([]byte(storedHash), []byte(providedHash)) == 1
}

// CreateSession creates a new session and returns the token
func (c *Config) CreateSession() (string, error) {
	token := generateToken()
	expiresAt := time.Now().Add(24 * time.Hour) // 24 hour sessions

	db := database.GetDB()
	_, err := db.Exec(`
		INSERT INTO sessions (token, expires_at) VALUES (?, ?)
	`, token, expiresAt)
	if err != nil {
		return "", err
	}

	// Clean up expired sessions
	db.Exec("DELETE FROM sessions WHERE expires_at < ?", time.Now())

	return token, nil
}

// ValidateSession checks if a session token is valid
func (c *Config) ValidateSession(token string) bool {
	if token == "" {
		return false
	}

	db := database.GetDB()
	var count int
	err := db.QueryRow(`
		SELECT COUNT(*) FROM sessions WHERE token = ? AND expires_at > ?
	`, token, time.Now()).Scan(&count)
	if err != nil {
		return false
	}
	return count > 0
}

// DeleteSession removes a session
func (c *Config) DeleteSession(token string) error {
	db := database.GetDB()
	_, err := db.Exec("DELETE FROM sessions WHERE token = ?", token)
	return err
}

// DeleteAllSessions removes all sessions (logout everywhere)
func (c *Config) DeleteAllSessions() error {
	db := database.GetDB()
	_, err := db.Exec("DELETE FROM sessions")
	return err
}
