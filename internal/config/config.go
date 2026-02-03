package config

import (
	"strconv"
	"strings"
	"sync"

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
