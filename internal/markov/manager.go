package markov

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"twitchbot/internal/config"
	"twitchbot/internal/database"
)

// Manager manages multiple channel brains, each with its own database
type Manager struct {
	brains map[string]*Brain
	cfg    *config.Config
	mu     sync.RWMutex
}

// NewManager creates a new brain manager
func NewManager(cfg *config.Config) *Manager {
	return &Manager{
		brains: make(map[string]*Brain),
		cfg:    cfg,
	}
}

// GetBrain gets or creates a brain for a channel
func (m *Manager) GetBrain(channel string) *Brain {
	channel = strings.ToLower(channel)

	m.mu.RLock()
	brain, exists := m.brains[channel]
	m.mu.RUnlock()

	if exists {
		return brain
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if brain, exists = m.brains[channel]; exists {
		return brain
	}

	var err error
	brain, err = NewBrain(channel, m.cfg)
	if err != nil {
		log.Printf("Error creating brain for %s: %v", channel, err)
		return nil
	}
	m.brains[channel] = brain
	return brain
}

// RemoveBrain removes a brain from memory and closes its database
func (m *Manager) RemoveBrain(channel string) {
	channel = strings.ToLower(channel)

	m.mu.Lock()
	defer m.mu.Unlock()

	if brain, exists := m.brains[channel]; exists {
		brain.Close()
		delete(m.brains, channel)
	}
}

// GetChannelCountdown returns the messages until next response for a channel
func (m *Manager) GetChannelCountdown(channel string) (messagesUntilResponse int, interval int) {
	channel = strings.ToLower(channel)
	interval = m.cfg.GetChannelMessageInterval(channel) // Use per-channel interval

	m.mu.RLock()
	brain, exists := m.brains[channel]
	m.mu.RUnlock()

	if !exists || brain == nil {
		return interval, interval
	}

	counter := brain.GetMessageCounter()
	messagesUntilResponse = interval - counter
	if messagesUntilResponse < 0 {
		messagesUntilResponse = 0
	}
	return messagesUntilResponse, interval
}

// GetLastMessage returns the last message the bot sent in a channel
func (m *Manager) GetLastMessage(channel string) string {
	channel = strings.ToLower(channel)

	m.mu.RLock()
	brain, exists := m.brains[channel]
	m.mu.RUnlock()

	if !exists || brain == nil {
		return ""
	}

	return brain.GetLastMessage()
}

// ListBrains returns stats for all channels with brain data
func (m *Manager) ListBrains() []BrainStats {
	brainsDir := filepath.Join(database.GetDataDir(), "brains")

	// Ensure directory exists
	if _, err := os.Stat(brainsDir); os.IsNotExist(err) {
		return []BrainStats{}
	}

	entries, err := os.ReadDir(brainsDir)
	if err != nil {
		return []BrainStats{}
	}

	var stats []BrainStats
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".db") || strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm") {
			continue
		}

		channel := strings.TrimSuffix(name, ".db")
		brain := m.GetBrain(channel)
		if brain != nil {
			stats = append(stats, brain.GetStats())
		}
	}

	return stats
}

// EraseBrain clears all brain data for a channel but keeps the database file
func (m *Manager) EraseBrain(channel string) error {
	channel = strings.ToLower(channel)
	brain := m.GetBrain(channel)
	if brain == nil {
		return nil
	}
	return brain.Erase()
}

// DeleteBrain deletes brain data for a channel (removes the database file)
func (m *Manager) DeleteBrain(channel string) error {
	channel = strings.ToLower(channel)

	m.mu.Lock()
	brain, exists := m.brains[channel]
	if exists {
		delete(m.brains, channel)
	}
	m.mu.Unlock()

	if exists {
		return brain.Delete()
	}

	// Brain not loaded, delete the file directly
	brainsDir := filepath.Join(database.GetDataDir(), "brains")
	dbPath := filepath.Join(brainsDir, channel+".db")

	os.Remove(dbPath + "-wal")
	os.Remove(dbPath + "-shm")

	return os.Remove(dbPath)
}

// CleanBrain cleans a specific brain of blacklisted words
func (m *Manager) CleanBrain(channel string) CleanResult {
	brain := m.GetBrain(channel)
	if brain == nil {
		return CleanResult{Channel: channel, Words: []CleanWordResult{}}
	}
	return brain.Clean()
}

// CleanAllBrains cleans all brains of blacklisted words
func (m *Manager) CleanAllBrains() []CleanResult {
	stats := m.ListBrains()
	results := []CleanResult{}
	for _, stat := range stats {
		brain := m.GetBrain(stat.Channel)
		if brain != nil {
			result := brain.Clean()
			if result.TotalRemoved > 0 {
				results = append(results, result)
			}
		}
	}
	return results
}

// OptimizeAll runs VACUUM on all brain databases
func (m *Manager) OptimizeAll() {
	stats := m.ListBrains()
	for _, stat := range stats {
		brain := m.GetBrain(stat.Channel)
		if brain != nil {
			brain.Optimize()
		}
	}
}

// CleanNonASCIIAll removes non-ASCII transitions from all brains
func (m *Manager) CleanNonASCIIAll() int {
	stats := m.ListBrains()
	totalRemoved := 0
	for _, stat := range stats {
		brain := m.GetBrain(stat.Channel)
		if brain != nil {
			totalRemoved += brain.CleanNonASCII()
		}
	}
	return totalRemoved
}

// Close closes all brain database connections
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, brain := range m.brains {
		brain.Close()
	}
	m.brains = make(map[string]*Brain)
}

// GenerateGlobal generates a response using transitions from all loaded brains
func (m *Manager) GenerateGlobal(maxWords int) string {
	m.mu.RLock()
	brains := make([]*Brain, 0, len(m.brains))
	for _, brain := range m.brains {
		brains = append(brains, brain)
	}
	m.mu.RUnlock()

	if len(brains) == 0 {
		return ""
	}

	// Pick a random brain to start from
	startBrain := brains[brains[0].rng.Intn(len(brains))]

	// Get a random starting pair from the starting brain
	var word1, word2 string
	err := startBrain.db.QueryRow(`
		SELECT word1, word2 FROM transitions 
		ORDER BY RANDOM() LIMIT 1
	`).Scan(&word1, &word2)

	if err != nil {
		return ""
	}

	result := []string{word1, word2}

	for i := 0; i < maxWords; i++ {
		// Collect candidates from all brains
		var allCandidates []string
		var allWeights []int
		totalWeight := 0

		for _, brain := range brains {
			rows, err := brain.db.Query(`
				SELECT next_word, count FROM transitions
				WHERE word1 = ? AND word2 = ?
			`, word1, word2)

			if err != nil {
				continue
			}

			for rows.Next() {
				var nextWord string
				var count int
				if rows.Scan(&nextWord, &count) == nil {
					allCandidates = append(allCandidates, nextWord)
					allWeights = append(allWeights, count)
					totalWeight += count
				}
			}
			rows.Close()
		}

		if len(allCandidates) == 0 {
			break
		}

		// Weighted random selection
		r := startBrain.rng.Intn(totalWeight)
		cumulative := 0
		var nextWord string
		for i, w := range allWeights {
			cumulative += w
			if r < cumulative {
				nextWord = allCandidates[i]
				break
			}
		}

		result = append(result, nextWord)
		word1 = word2
		word2 = nextWord
	}

	return strings.Join(result, " ")
}

// GetDatabaseStats returns overall database statistics
func (m *Manager) GetDatabaseStats() map[string]interface{} {
	stats := make(map[string]interface{})

	brainStats := m.ListBrains()

	totalTransitions := 0
	totalSize := int64(0)
	for _, bs := range brainStats {
		totalTransitions += bs.TotalEntries
		totalSize += bs.DbSize
	}

	stats["total_transitions"] = totalTransitions
	stats["unique_channels"] = len(brainStats)
	stats["total_size"] = totalSize
	stats["data_directory"] = filepath.Join(database.GetDataDir(), "brains")

	// Get blacklisted words count from main database
	db := database.GetDB()
	var totalBlacklisted int
	db.QueryRow("SELECT COUNT(*) FROM blacklist").Scan(&totalBlacklisted)
	stats["blacklisted_words"] = totalBlacklisted

	return stats
}
