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

		// Quote votes table for +1 system
		`CREATE TABLE IF NOT EXISTS quote_votes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			quote_id INTEGER NOT NULL,
			twitch_user_id TEXT NOT NULL,
			twitch_username TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(quote_id, twitch_user_id),
			FOREIGN KEY(quote_id) REFERENCES quotes(id) ON DELETE CASCADE
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
	Votes     int    `json:"votes"`
	UserVoted bool   `json:"user_voted,omitempty"`
}

// SaveQuote saves a bot-generated message to the quotes table
func SaveQuote(channel, message string) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec("INSERT INTO quotes (channel, message) VALUES (?, ?)", channel, message)
	return err
}

// GetQuotes retrieves quotes with optional search, sorting, and pagination
func GetQuotes(search string, channel string, page, pageSize int, sort string, userID string) ([]Quote, int, error) {
	if db == nil {
		return nil, 0, nil
	}

	// Build query
	baseQuery := "FROM quotes q WHERE 1=1"
	args := []interface{}{}

	if search != "" {
		baseQuery += " AND q.message LIKE ?"
		args = append(args, "%"+search+"%")
	}
	if channel != "" {
		baseQuery += " AND q.channel = ?"
		args = append(args, channel)
	}

	// Get total count
	var total int
	countQuery := "SELECT COUNT(*) " + baseQuery
	if err := db.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// Determine sort order
	orderBy := "q.created_at DESC" // default: newest
	switch sort {
	case "oldest":
		orderBy = "q.created_at ASC"
	case "most_votes":
		orderBy = "vote_count DESC, q.created_at DESC"
	case "newest":
		orderBy = "q.created_at DESC"
	}

	// Get paginated results with vote counts
	offset := (page - 1) * pageSize
	selectQuery := `
		SELECT q.id, q.channel, q.message, q.created_at, 
			   COALESCE((SELECT COUNT(*) FROM quote_votes WHERE quote_id = q.id), 0) as vote_count,
			   CASE WHEN EXISTS(SELECT 1 FROM quote_votes WHERE quote_id = q.id AND twitch_user_id = ?) THEN 1 ELSE 0 END as user_voted
		` + baseQuery + " ORDER BY " + orderBy + " LIMIT ? OFFSET ?"

	// Add userID for the user_voted check, then the other args, then limit/offset
	queryArgs := append([]interface{}{userID}, args...)
	queryArgs = append(queryArgs, pageSize, offset)

	rows, err := db.Query(selectQuery, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var quotes []Quote
	for rows.Next() {
		var q Quote
		var userVoted int
		if err := rows.Scan(&q.ID, &q.Channel, &q.Message, &q.CreatedAt, &q.Votes, &userVoted); err != nil {
			continue
		}
		q.UserVoted = userVoted == 1
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

// VoteQuote adds a +1 vote to a quote (returns true if new vote, false if already voted)
func VoteQuote(quoteID int64, twitchUserID, twitchUsername string) (bool, error) {
	if db == nil {
		return false, nil
	}

	result, err := db.Exec(
		"INSERT OR IGNORE INTO quote_votes (quote_id, twitch_user_id, twitch_username) VALUES (?, ?, ?)",
		quoteID, twitchUserID, twitchUsername,
	)
	if err != nil {
		return false, err
	}

	affected, _ := result.RowsAffected()
	return affected > 0, nil
}

// UnvoteQuote removes a +1 vote from a quote
func UnvoteQuote(quoteID int64, twitchUserID string) error {
	if db == nil {
		return nil
	}

	_, err := db.Exec("DELETE FROM quote_votes WHERE quote_id = ? AND twitch_user_id = ?", quoteID, twitchUserID)
	return err
}

// GetQuoteVoteCount returns the vote count for a quote
func GetQuoteVoteCount(quoteID int64) (int, error) {
	if db == nil {
		return 0, nil
	}

	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM quote_votes WHERE quote_id = ?", quoteID).Scan(&count)
	return count, err
}
