package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

// DB is the global database instance
var (
	db   *sql.DB
	once sync.Once
)

// GetDataDir returns the data directory path
func GetDataDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "data"
	}
	return filepath.Join(homeDir, ".twitchbot")
}

// Init initializes the database connection
func Init() error {
	var initErr error
	once.Do(func() {
		dataDir := GetDataDir()
		if err := os.MkdirAll(dataDir, 0755); err != nil {
			initErr = err
			return
		}

		dbPath := filepath.Join(dataDir, "twitchbot.db")
		var err error
		db, err = sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
		if err != nil {
			initErr = err
			return
		}

		// Create tables
		initErr = createTables()
	})
	return initErr
}

// GetDB returns the database instance
func GetDB() *sql.DB {
	return db
}

// Close closes the database connection
func Close() error {
	if db != nil {
		return db.Close()
	}
	return nil
}

func createTables() error {
	tables := []string{
		// Config table for bot settings
		`CREATE TABLE IF NOT EXISTS config (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,

		// Channels table
		`CREATE TABLE IF NOT EXISTS channels (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT UNIQUE NOT NULL,
			enabled INTEGER DEFAULT 1,
			message_count INTEGER DEFAULT 0,
			message_interval INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		// Blacklist table for banned words
		`CREATE TABLE IF NOT EXISTS blacklist (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			word TEXT UNIQUE NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		// User blacklist table for ignored users
		`CREATE TABLE IF NOT EXISTS user_blacklist (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT UNIQUE NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		// Twitch users table for tracking user IDs and username changes
		`CREATE TABLE IF NOT EXISTS twitch_users (
			twitch_id TEXT PRIMARY KEY,
			username TEXT NOT NULL,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		// Activity log table for recent messages
		`CREATE TABLE IF NOT EXISTS activity (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			channel TEXT NOT NULL,
			username TEXT NOT NULL,
			message TEXT NOT NULL,
			color TEXT DEFAULT '',
			emotes TEXT DEFAULT '',
			badges TEXT DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		// Sessions table for web UI authentication
		`CREATE TABLE IF NOT EXISTS sessions (
			token TEXT PRIMARY KEY,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			expires_at DATETIME NOT NULL
		)`,

		// Quotes table for bot-generated messages
		`CREATE TABLE IF NOT EXISTS quotes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			channel TEXT NOT NULL,
			message TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
	}

	for _, table := range tables {
		if _, err := db.Exec(table); err != nil {
			return err
		}
	}

	// Migration: add message_interval column if it doesn't exist
	db.Exec("ALTER TABLE channels ADD COLUMN message_interval INTEGER DEFAULT 0")

	// Migration: add use_global_brain column if it doesn't exist
	db.Exec("ALTER TABLE channels ADD COLUMN use_global_brain INTEGER DEFAULT 0")

	// Insert default config values if not exists
	defaults := map[string]string{
		"client_id":        "",
		"oauth_token":      "",
		"bot_username":     "",
		"web_port":         "24601",
		"message_interval": "35",
	}

	for key, value := range defaults {
		db.Exec("INSERT OR IGNORE INTO config (key, value) VALUES (?, ?)", key, value)
	}

	return nil
}

// Quote represents a bot-generated message
type Quote struct {
	ID        int64  `json:"id"`
	Channel   string `json:"channel"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
}

// SaveQuote saves a bot-generated message to the quotes table
func SaveQuote(channel, message string) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec("INSERT INTO quotes (channel, message) VALUES (?, ?)", channel, message)
	return err
}

// GetQuotes retrieves quotes with optional search and pagination
func GetQuotes(search string, channel string, page, pageSize int) ([]Quote, int, error) {
	if db == nil {
		return nil, 0, nil
	}

	// Build query
	baseQuery := "FROM quotes WHERE 1=1"
	args := []interface{}{}

	if search != "" {
		baseQuery += " AND message LIKE ?"
		args = append(args, "%"+search+"%")
	}
	if channel != "" {
		baseQuery += " AND channel = ?"
		args = append(args, channel)
	}

	// Get total count
	var total int
	countQuery := "SELECT COUNT(*) " + baseQuery
	if err := db.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// Get paginated results
	offset := (page - 1) * pageSize
	selectQuery := "SELECT id, channel, message, created_at " + baseQuery + " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	args = append(args, pageSize, offset)

	rows, err := db.Query(selectQuery, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var quotes []Quote
	for rows.Next() {
		var q Quote
		if err := rows.Scan(&q.ID, &q.Channel, &q.Message, &q.CreatedAt); err != nil {
			continue
		}
		quotes = append(quotes, q)
	}

	return quotes, total, nil
}

// GetQuoteChannels returns a list of all unique channels with quotes
func GetQuoteChannels() ([]string, error) {
	if db == nil {
		return nil, nil
	}

	rows, err := db.Query("SELECT DISTINCT channel FROM quotes ORDER BY channel")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []string
	for rows.Next() {
		var ch string
		if err := rows.Scan(&ch); err != nil {
			continue
		}
		channels = append(channels, ch)
	}

	return channels, nil
}
