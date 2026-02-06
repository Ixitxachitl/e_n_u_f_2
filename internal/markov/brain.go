package markov

import (
	"database/sql"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"twitchbot/internal/config"
	"twitchbot/internal/database"
)

// Brain represents a Markov chain brain for a single channel with its own database
type Brain struct {
	Channel    string
	cfg        *config.Config
	db         *sql.DB
	mu         sync.RWMutex
	msgCounter int
	rng        *rand.Rand
}

// BrainStats holds statistics about a brain
type BrainStats struct {
	Channel      string `json:"channel"`
	UniquePairs  int    `json:"unique_pairs"`
	TotalEntries int    `json:"total_entries"`
	MessageCount int64  `json:"message_count"`
	DbSize       int64  `json:"db_size"`
}

// NewBrain creates a new brain for a channel with its own database
func NewBrain(channel string, cfg *config.Config) (*Brain, error) {
	channel = strings.ToLower(channel)

	brain := &Brain{
		Channel: channel,
		cfg:     cfg,
		rng:     rand.New(rand.NewSource(time.Now().UnixNano())),
	}

	if err := brain.initDB(); err != nil {
		return nil, err
	}

	return brain, nil
}

// initDB initializes the brain's database
func (b *Brain) initDB() error {
	brainsDir := filepath.Join(database.GetDataDir(), "brains")
	if err := os.MkdirAll(brainsDir, 0755); err != nil {
		return err
	}

	dbPath := filepath.Join(brainsDir, b.Channel+".db")
	var err error
	b.db, err = sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}

	// Create transitions table
	_, err = b.db.Exec(`
		CREATE TABLE IF NOT EXISTS transitions (
			word1 TEXT NOT NULL,
			word2 TEXT NOT NULL,
			next_word TEXT NOT NULL,
			count INTEGER DEFAULT 1,
			PRIMARY KEY (word1, word2, next_word)
		);
		CREATE INDEX IF NOT EXISTS idx_word1_word2 ON transitions(word1, word2);
		
		CREATE TABLE IF NOT EXISTS state (
			key TEXT PRIMARY KEY,
			value INTEGER DEFAULT 0,
			value_text TEXT DEFAULT ''
		);
	`)
	if err != nil {
		return err
	}

	// Load persisted message counter
	var counter int
	err = b.db.QueryRow("SELECT value FROM state WHERE key = 'msg_counter'").Scan(&counter)
	if err == nil {
		b.msgCounter = counter
	}

	return nil
}

// Close closes the brain's database connection
func (b *Brain) Close() error {
	if b.db != nil {
		return b.db.Close()
	}
	return nil
}

// GenerationResult contains details about a generation attempt
type GenerationResult struct {
	Triggered     bool   `json:"triggered"`      // Whether generation was triggered (counter reached)
	Success       bool   `json:"success"`        // Whether a message was generated
	Response      string `json:"response"`       // The generated message (if any)
	Attempts      int    `json:"attempts"`       // Number of generation attempts
	FailureReason string `json:"failure_reason"` // Why generation failed (if it did)
	Counter       int    `json:"counter"`        // Current counter value
	Interval      int    `json:"interval"`       // Channel interval setting
	UsingGlobal   bool   `json:"using_global"`   // Whether global brain was used
}

// ProcessMessage learns from a message and optionally generates a response
// If globalGenerator is provided, it will be used instead of the local Generate function
func (b *Brain) ProcessMessage(message, username, botUsername string, globalGenerator func(int) string) string {
	result := b.ProcessMessageWithInfo(message, username, botUsername, globalGenerator)
	return result.Response
}

// ProcessMessageWithInfo learns from a message and returns detailed generation info
func (b *Brain) ProcessMessageWithInfo(message, username, botUsername string, globalGenerator func(int) string) GenerationResult {
	result := GenerationResult{}

	// Skip commands
	if strings.HasPrefix(message, "!") {
		return result
	}

	// Skip bot's own messages
	if strings.EqualFold(username, botUsername) {
		return result
	}

	// Skip learning/generating in the bot's own channel
	if strings.EqualFold(b.Channel, botUsername) {
		return result
	}

	// Skip blacklisted users
	if b.cfg.IsBlacklistedUser(username) {
		return result
	}

	// Skip messages with links
	if containsLink(message) {
		return result
	}

	// Skip non-English messages
	if !isMostlyEnglish(message) {
		return result
	}

	// Skip messages with blacklisted words
	if b.containsBlacklistedWord(message) {
		return result
	}

	// Normalize smart quotes and other Unicode to ASCII before learning
	message = normalizeASCII(message)

	// Learn from the message (always local)
	b.learn(message)

	// Increment message count
	b.cfg.IncrementChannelMessages(b.Channel)

	// Check if we should respond (use per-channel interval)
	b.mu.Lock()
	b.msgCounter++
	channelInterval := b.cfg.GetChannelMessageInterval(b.Channel)
	result.Counter = b.msgCounter
	result.Interval = channelInterval
	shouldRespond := b.msgCounter >= channelInterval
	if shouldRespond {
		b.msgCounter = 0
		result.Counter = 0
	}
	// Persist counter to database
	b.saveCounter()
	b.mu.Unlock()

	if shouldRespond {
		result.Triggered = true
		result.UsingGlobal = globalGenerator != nil

		// Choose generator based on setting
		generator := b.Generate
		if globalGenerator != nil {
			generator = globalGenerator
		}

		// Try up to 5 times to generate a clean response
		for i := 0; i < 5; i++ {
			result.Attempts = i + 1
			response := generator(20)
			if response == "" {
				result.FailureReason = "empty_generation"
				continue
			}
			if b.containsBlacklistedWord(response) {
				result.FailureReason = "blacklisted_word"
				continue
			}
			// Success!
			result.Success = true
			result.Response = response
			result.FailureReason = ""
			b.saveLastMessage(response)
			return result
		}
		// All attempts failed
		if result.FailureReason == "" {
			result.FailureReason = "unknown"
		}
	}

	return result
}

// GetMessageCounter returns the current message counter
func (b *Brain) GetMessageCounter() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.msgCounter
}

// saveCounter persists the message counter to the database (must be called with lock held)
func (b *Brain) saveCounter() {
	if b.db == nil {
		return
	}
	b.db.Exec(`
		INSERT INTO state (key, value) VALUES ('msg_counter', ?)
		ON CONFLICT(key) DO UPDATE SET value = ?
	`, b.msgCounter, b.msgCounter)
}

// saveLastMessage persists the last bot message to the database
func (b *Brain) saveLastMessage(message string) {
	if b.db == nil {
		return
	}
	b.db.Exec(`
		INSERT INTO state (key, value_text) VALUES ('last_message', ?)
		ON CONFLICT(key) DO UPDATE SET value_text = ?
	`, message, message)
}

// GetLastMessage returns the last message the bot sent in this channel
func (b *Brain) GetLastMessage() string {
	if b.db == nil {
		return ""
	}
	var msg string
	err := b.db.QueryRow("SELECT value_text FROM state WHERE key = 'last_message'").Scan(&msg)
	if err != nil {
		return ""
	}
	return msg
}

// learn adds a message to the brain
func (b *Brain) learn(message string) {
	words := strings.Fields(message)
	if len(words) < 3 {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	for i := 0; i < len(words)-2; i++ {
		word1 := words[i]
		word2 := words[i+1]
		nextWord := words[i+2]

		// Skip loop transitions (all three words the same) to avoid infinite loops
		if word1 == word2 && word2 == nextWord {
			continue
		}

		// Insert or update count
		b.db.Exec(`
			INSERT INTO transitions (word1, word2, next_word, count)
			VALUES (?, ?, ?, 1)
			ON CONFLICT(word1, word2, next_word) DO UPDATE SET count = count + 1
		`, word1, word2, nextWord)
	}
}

// Generate creates a sentence using the Markov chain
func (b *Brain) Generate(maxWords int) string {
	b.mu.RLock()
	defer b.mu.RUnlock()

	// Get a random starting pair
	var word1, word2 string
	err := b.db.QueryRow(`
		SELECT word1, word2 FROM transitions 
		ORDER BY RANDOM() LIMIT 1
	`).Scan(&word1, &word2)

	if err != nil {
		return ""
	}

	result := []string{word1, word2}

	for i := 0; i < maxWords; i++ {
		// Get possible next words weighted by count
		rows, err := b.db.Query(`
			SELECT next_word, count FROM transitions
			WHERE word1 = ? AND word2 = ?
		`, word1, word2)

		if err != nil {
			break
		}

		var candidates []string
		var weights []int
		totalWeight := 0

		for rows.Next() {
			var nextWord string
			var count int
			if rows.Scan(&nextWord, &count) == nil {
				candidates = append(candidates, nextWord)
				weights = append(weights, count)
				totalWeight += count
			}
		}
		rows.Close()

		if len(candidates) == 0 {
			break
		}

		// Weighted random selection
		r := b.rng.Intn(totalWeight)
		cumulative := 0
		var nextWord string
		for i, w := range weights {
			cumulative += w
			if r < cumulative {
				nextWord = candidates[i]
				break
			}
		}

		result = append(result, nextWord)
		word1 = word2
		word2 = nextWord
	}

	return strings.Join(result, " ")
}

// GetStats returns statistics about the brain
func (b *Brain) GetStats() BrainStats {
	b.mu.RLock()
	defer b.mu.RUnlock()

	stats := BrainStats{
		Channel: b.Channel,
	}

	// Get unique pairs count
	b.db.QueryRow(`
		SELECT COUNT(DISTINCT word1 || '|' || word2) FROM transitions
	`).Scan(&stats.UniquePairs)

	// Get total entries
	b.db.QueryRow(`
		SELECT COUNT(*) FROM transitions
	`).Scan(&stats.TotalEntries)

	// Get message count from channels table
	stats.MessageCount, _, _ = b.cfg.GetChannelStats(b.Channel)

	// Get database file size
	brainsDir := filepath.Join(database.GetDataDir(), "brains")
	dbPath := filepath.Join(brainsDir, b.Channel+".db")
	if info, err := os.Stat(dbPath); err == nil {
		stats.DbSize = info.Size()
	}

	return stats
}

// Clean removes all transitions containing blacklisted words
func (b *Brain) Clean() (rowsRemoved int) {
	blacklist := b.cfg.GetBlacklistedWords()

	if len(blacklist) == 0 {
		return 0
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	for _, word := range blacklist {
		// Check if this is a multi-word phrase
		words := strings.Fields(word)

		if len(words) >= 2 {
			// Multi-word phrase: match sequential words across columns
			// For "bad word", delete where (word1="bad" AND word2="word") OR (word2="bad" AND next_word="word")
			for i := 0; i < len(words)-1; i++ {
				w1 := strings.ToLower(words[i])
				w2 := strings.ToLower(words[i+1])

				result, _ := b.db.Exec(`
					DELETE FROM transitions 
					WHERE (LOWER(word1) = ? AND LOWER(word2) = ?)
					   OR (LOWER(word2) = ? AND LOWER(next_word) = ?)
				`, w1, w2, w1, w2)

				if result != nil {
					affected, _ := result.RowsAffected()
					rowsRemoved += int(affected)
				}
			}
		} else {
			// Single word: use LIKE for partial matching
			pattern := "%" + strings.ToLower(word) + "%"
			result, _ := b.db.Exec(`
				DELETE FROM transitions 
				WHERE LOWER(word1) LIKE ? OR LOWER(word2) LIKE ? OR LOWER(next_word) LIKE ?
			`, pattern, pattern, pattern)

			if result != nil {
				affected, _ := result.RowsAffected()
				rowsRemoved += int(affected)
			}
		}
	}

	return rowsRemoved
}

// CleanNonASCII removes transitions containing non-ASCII characters (excluding emoji)
// and also removes loop transitions where all three words are the same
func (b *Brain) CleanNonASCII() (rowsRemoved int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Get all transitions
	rows, err := b.db.Query(`SELECT rowid, word1, word2, next_word FROM transitions`)
	if err != nil {
		return 0
	}
	defer rows.Close()

	type transitionToDelete struct {
		rowid              int64
		word1, word2, next string
		reason             string
	}
	var toDelete []transitionToDelete

	for rows.Next() {
		var rowid int64
		var word1, word2, nextWord string
		if err := rows.Scan(&rowid, &word1, &word2, &nextWord); err != nil {
			continue
		}

		// Check for loop (all three words are the same)
		if word1 == word2 && word2 == nextWord {
			toDelete = append(toDelete, transitionToDelete{rowid, word1, word2, nextWord, "loop"})
			continue
		}

		// Check if any word contains non-ASCII (excluding emoji)
		if containsNonASCII(word1) || containsNonASCII(word2) || containsNonASCII(nextWord) {
			toDelete = append(toDelete, transitionToDelete{rowid, word1, word2, nextWord, "non-ascii"})
		}
	}

	// Delete and log each removed transition
	for _, t := range toDelete {
		_, err := b.db.Exec(`DELETE FROM transitions WHERE rowid = ?`, t.rowid)
		if err == nil {
			if t.reason == "loop" {
				log.Printf("[%s] Removed loop transition: %q -> %q -> %q", b.Channel, t.word1, t.word2, t.next)
			} else {
				// Find and log the offending word(s)
				var badWords []string
				if containsNonASCII(t.word1) {
					badWords = append(badWords, t.word1)
				}
				if containsNonASCII(t.word2) {
					badWords = append(badWords, t.word2)
				}
				if containsNonASCII(t.next) {
					badWords = append(badWords, t.next)
				}
				log.Printf("[%s] Removed non-ASCII transition: %q -> %q -> %q (bad: %v)", b.Channel, t.word1, t.word2, t.next, badWords)
			}
			rowsRemoved++
		}
	}

	return rowsRemoved
}

// isEmoji checks if a rune is an emoji character
func isEmoji(r rune) bool {
	// Common emoji ranges
	return (r >= 0x1F300 && r <= 0x1F9FF) || // Miscellaneous Symbols and Pictographs, Emoticons, etc.
		(r >= 0x2600 && r <= 0x26FF) || // Misc symbols (☀, ⚡, etc.)
		(r >= 0x2700 && r <= 0x27BF) || // Dingbats (✂, ✓, etc.)
		(r >= 0x1F600 && r <= 0x1F64F) || // Emoticons
		(r >= 0x1F680 && r <= 0x1F6FF) || // Transport and Map
		(r >= 0x1F1E0 && r <= 0x1F1FF) || // Flags
		(r >= 0x231A && r <= 0x231B) || // Watch, Hourglass
		(r >= 0x23E9 && r <= 0x23F3) || // Media control symbols
		(r >= 0x25AA && r <= 0x25AB) || // Squares
		(r >= 0x25B6 && r <= 0x25C0) || // Play buttons
		(r >= 0x25FB && r <= 0x25FE) || // Squares
		(r >= 0x2614 && r <= 0x2615) || // Umbrella, Hot Beverage
		(r >= 0x2648 && r <= 0x2653) || // Zodiac
		(r >= 0x267F && r <= 0x267F) || // Wheelchair
		(r >= 0x2934 && r <= 0x2935) || // Arrows
		(r >= 0x2B05 && r <= 0x2B07) || // Arrows
		(r >= 0x2B1B && r <= 0x2B1C) || // Squares
		(r >= 0x2B50 && r <= 0x2B50) || // Star
		(r >= 0x2B55 && r <= 0x2B55) || // Circle
		(r >= 0x3030 && r <= 0x3030) || // Wavy dash
		(r >= 0x303D && r <= 0x303D) || // Part alternation mark
		(r >= 0x3297 && r <= 0x3299) || // Circled Ideograph
		(r >= 0xFE0F && r <= 0xFE0F) || // Variation selector
		(r >= 0x200D && r <= 0x200D) // Zero width joiner (used in compound emoji)
}

// containsNonASCII checks if a string contains any non-ASCII characters (excluding emoji)
func containsNonASCII(s string) bool {
	for _, r := range s {
		if r > 127 && !isEmoji(r) {
			return true
		}
	}
	return false
}

// normalizeASCII converts common Unicode characters to their ASCII equivalents
// Smart quotes, dashes, ellipses, etc. are converted to standard ASCII
func normalizeASCII(s string) string {
	replacements := map[rune]string{
		// Smart quotes
		'\u2018': "'",  // Left single quote
		'\u2019': "'",  // Right single quote (apostrophe)
		'\u201A': "'",  // Single low quote
		'\u201B': "'",  // Single high-reversed quote
		'\u2032': "'",  // Prime
		'\u2035': "'",  // Reversed prime
		'\u201C': "\"", // Left double quote
		'\u201D': "\"", // Right double quote
		'\u201E': "\"", // Double low quote
		'\u201F': "\"", // Double high-reversed quote
		'\u2033': "\"", // Double prime
		'\u2036': "\"", // Reversed double prime
		// Dashes
		'\u2010': "-", // Hyphen
		'\u2011': "-", // Non-breaking hyphen
		'\u2012': "-", // Figure dash
		'\u2013': "-", // En dash
		'\u2014': "-", // Em dash
		'\u2015': "-", // Horizontal bar
		// Ellipsis
		'\u2026': "...", // Horizontal ellipsis
		// Spaces
		'\u00A0': " ", // Non-breaking space
		'\u2002': " ", // En space
		'\u2003': " ", // Em space
		'\u2009': " ", // Thin space
	}

	var result strings.Builder
	for _, r := range s {
		if replacement, ok := replacements[r]; ok {
			result.WriteString(replacement)
		} else {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// Erase clears all brain data but keeps the database file
func (b *Brain) Erase() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Delete all data from tables
	_, err := b.db.Exec("DELETE FROM markov_chain")
	if err != nil {
		return err
	}
	_, err = b.db.Exec("DELETE FROM stats")
	if err != nil {
		return err
	}
	// Reset stats
	_, err = b.db.Exec("INSERT OR REPLACE INTO stats (key, value) VALUES ('message_count', '0')")
	if err != nil {
		return err
	}
	// Vacuum to reclaim space
	_, err = b.db.Exec("VACUUM")
	return err
}

// Delete removes all brain data for this channel (deletes the database file)
func (b *Brain) Delete() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Close the database connection
	if b.db != nil {
		b.db.Close()
		b.db = nil
	}

	// Delete the database file
	brainsDir := filepath.Join(database.GetDataDir(), "brains")
	dbPath := filepath.Join(brainsDir, b.Channel+".db")

	// Also delete WAL and SHM files if they exist
	os.Remove(dbPath + "-wal")
	os.Remove(dbPath + "-shm")

	return os.Remove(dbPath)
}

// Optimize runs VACUUM on the brain database
func (b *Brain) Optimize() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	_, err := b.db.Exec("VACUUM")
	return err
}

// Transition represents a single transition entry
type Transition struct {
	Word1    string `json:"word1"`
	Word2    string `json:"word2"`
	NextWord string `json:"next_word"`
	Count    int    `json:"count"`
}

// TransitionsResult contains paginated transitions
type TransitionsResult struct {
	Transitions []Transition `json:"transitions"`
	Total       int          `json:"total"`
	Page        int          `json:"page"`
	PageSize    int          `json:"page_size"`
}

// GetTransitions returns paginated transitions, optionally filtered by search
func (b *Brain) GetTransitions(search string, page, pageSize int) TransitionsResult {
	b.mu.RLock()
	defer b.mu.RUnlock()

	result := TransitionsResult{
		Transitions: []Transition{},
		Page:        page,
		PageSize:    pageSize,
	}

	offset := (page - 1) * pageSize

	// Count total
	var countQuery string
	var countArgs []interface{}
	if search != "" {
		countQuery = `SELECT COUNT(*) FROM transitions WHERE word1 LIKE ? OR word2 LIKE ? OR next_word LIKE ?`
		searchPattern := "%" + search + "%"
		countArgs = []interface{}{searchPattern, searchPattern, searchPattern}
	} else {
		countQuery = `SELECT COUNT(*) FROM transitions`
	}
	b.db.QueryRow(countQuery, countArgs...).Scan(&result.Total)

	// Fetch transitions
	var query string
	var args []interface{}
	if search != "" {
		query = `SELECT word1, word2, next_word, count FROM transitions 
			WHERE word1 LIKE ? OR word2 LIKE ? OR next_word LIKE ?
			ORDER BY count DESC LIMIT ? OFFSET ?`
		searchPattern := "%" + search + "%"
		args = []interface{}{searchPattern, searchPattern, searchPattern, pageSize, offset}
	} else {
		query = `SELECT word1, word2, next_word, count FROM transitions ORDER BY count DESC LIMIT ? OFFSET ?`
		args = []interface{}{pageSize, offset}
	}

	rows, err := b.db.Query(query, args...)
	if err != nil {
		return result
	}
	defer rows.Close()

	for rows.Next() {
		var t Transition
		if err := rows.Scan(&t.Word1, &t.Word2, &t.NextWord, &t.Count); err == nil {
			result.Transitions = append(result.Transitions, t)
		}
	}

	return result
}

// DeleteTransition removes a specific transition
func (b *Brain) DeleteTransition(word1, word2, nextWord string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	_, err := b.db.Exec(`DELETE FROM transitions WHERE word1 = ? AND word2 = ? AND next_word = ?`,
		word1, word2, nextWord)
	return err
}

// UpdateTransitionCount updates the count for a specific transition
func (b *Brain) UpdateTransitionCount(word1, word2, nextWord string, count int) error {
	if count < 1 {
		return b.DeleteTransition(word1, word2, nextWord)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	_, err := b.db.Exec(`UPDATE transitions SET count = ? WHERE word1 = ? AND word2 = ? AND next_word = ?`,
		count, word1, word2, nextWord)
	return err
}

func (b *Brain) containsBlacklistedWord(message string) bool {
	lowerMessage := strings.ToLower(message)
	words := strings.Fields(lowerMessage)
	blacklist := b.cfg.GetBlacklistedWords()

	for _, blacklisted := range blacklist {
		blacklisted = strings.ToLower(blacklisted)

		if strings.Contains(blacklisted, " ") {
			// Phrase (contains space): substring match against full message
			if strings.Contains(lowerMessage, blacklisted) {
				return true
			}
		} else {
			// Single word: exact word match
			for _, word := range words {
				if word == blacklisted {
					return true
				}
			}
		}
	}
	return false
}

func isMostlyEnglish(text string) bool {
	for _, r := range text {
		// Allow ASCII characters and emoji, reject everything else
		if r > 127 && !isEmoji(r) {
			return false
		}
	}
	return true
}

func containsLink(text string) bool {
	lower := strings.ToLower(text)
	// Check for common URL patterns
	linkPatterns := []string{
		"http://",
		"https://",
		"www.",
		".com",
		".org",
		".net",
		".tv",
		".gg",
		".io",
		".co",
		".me",
		".be",
		".ru",
		".xyz",
		".info",
		".link",
		".click",
		".site",
		".online",
		".top",
		".ly",
		".gl",
		".to",
		".live",
		".stream",
		".uk",
		".de",
		".fr",
		".shop",
		".store",
	}
	for _, pattern := range linkPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}
