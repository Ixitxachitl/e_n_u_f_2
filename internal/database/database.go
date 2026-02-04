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
	}

	for _, table := range tables {
		if _, err := db.Exec(table); err != nil {
			return err
		}
	}

	// Migration: add message_interval column if it doesn't exist
	db.Exec("ALTER TABLE channels ADD COLUMN message_interval INTEGER DEFAULT 0")

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
