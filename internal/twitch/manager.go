package twitch

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"twitchbot/internal/config"
	"twitchbot/internal/database"
	"twitchbot/internal/markov"
)

// ChannelStatus represents the status of a channel connection
type ChannelStatus struct {
	Channel   string `json:"channel"`
	Connected bool   `json:"connected"`
	Messages  int64  `json:"messages"`
}

// Manager manages multiple Twitch channel connections
type Manager struct {
	cfg             *config.Config
	brainMgr        *markov.Manager
	clients         map[string]*Client
	msgCounts       map[string]int64
	lastActivity    map[string]time.Time // last message time per channel for inactivity timer
	timerFired      map[string]bool      // whether timer already fired since last activity
	followersOnly   map[string]bool      // channels flagged as followers-only (skip until offline)
	mu              sync.RWMutex
	running         bool
	eventHandler    func(event string, data interface{})
	stopChan        chan struct{}
	botReconnecting bool
}

// NewManager creates a new Twitch connection manager
func NewManager(cfg *config.Config) *Manager {
	return &Manager{
		cfg:           cfg,
		brainMgr:      markov.NewManager(cfg),
		clients:       make(map[string]*Client),
		msgCounts:     make(map[string]int64),
		lastActivity:  make(map[string]time.Time),
		timerFired:    make(map[string]bool),
		followersOnly: make(map[string]bool),
		stopChan:      make(chan struct{}),
	}
}

// Start initializes and connects to all configured channels
func (m *Manager) Start() error {
	m.mu.Lock()
	m.running = true
	m.mu.Unlock()

	// Always join the bot's own channel first (for !join/!leave commands)
	botUsername := m.cfg.GetBotUsername()
	if botUsername != "" {
		if err := m.JoinChannel(botUsername); err != nil {
			log.Printf("Failed to join bot's own channel %s: %v", botUsername, err)
		}
	}

	// Start the live channel monitor (checks every 60 seconds)
	go m.monitorLiveChannels()

	// Start the inactivity timer monitor (checks every 30 seconds)
	go m.monitorInactivityTimers()

	// Do an immediate check for live channels
	m.updateLiveConnections()

	return nil
}

// Stop disconnects from all channels
func (m *Manager) Stop() {
	m.mu.Lock()
	m.running = false
	close(m.stopChan)
	clients := make([]*Client, 0, len(m.clients))
	for _, client := range m.clients {
		clients = append(clients, client)
	}
	m.mu.Unlock()

	for _, client := range clients {
		client.Disconnect()
	}
}

// JoinChannel connects to a new channel
func (m *Manager) JoinChannel(channel string) error {
	channel = strings.ToLower(channel)
	botUsername := strings.ToLower(m.cfg.GetBotUsername())
	isBotChannel := channel == botUsername

	// Check for username changes via Twitch API (for non-bot channels)
	if !isBotChannel {
		channel = m.checkAndHandleUsernameChange(channel)
	}

	m.mu.Lock()

	// Check if already connected
	if _, exists := m.clients[channel]; exists {
		m.mu.Unlock()
		return nil
	}

	// Only create brain for non-bot channels
	var brain *markov.Brain
	if !isBotChannel {
		brain = m.brainMgr.GetBrain(channel)
	}
	client := NewClient(channel, m.cfg, brain)

	client.SetCallbacks(
		m.onMessage,
		m.onConnect,
		m.onDisconnect,
		m.onCommand,
		m.onBanned,
		m.onFollowersOnly,
		m.onGeneration,
	)

	// Set global generator for combined brain generation
	client.SetGlobalGenerator(m.brainMgr.GenerateGlobal)

	m.clients[channel] = client
	m.msgCounts[channel] = 0
	m.mu.Unlock()

	if err := client.Connect(); err != nil {
		m.mu.Lock()
		delete(m.clients, channel)
		delete(m.msgCounts, channel)
		m.mu.Unlock()
		return err
	}

	go client.Run()

	// Add to config (but not the bot's own channel)
	if !isBotChannel {
		m.cfg.AddChannel(channel)
		// Apply default brain mode for new channels
		if m.cfg.GetDefaultBrainMode() == "global" {
			m.cfg.SetChannelUseGlobalBrain(channel, true)
		}
		// Apply default timer settings for new channels
		if m.cfg.GetDefaultTimerEnabled() {
			m.cfg.SetChannelTimerEnabled(channel, true)
		}
		defaultTimerMin := m.cfg.GetDefaultTimerMinutes()
		if defaultTimerMin != 15 {
			m.cfg.SetChannelTimerMinutes(channel, defaultTimerMin)
		}
	}

	log.Printf("Joined channel: %s", channel)
	return nil
}

// LeaveChannel disconnects from a channel, removes it from config, and deletes its brain data
func (m *Manager) LeaveChannel(channel string) {
	channel = strings.ToLower(channel)

	// Never leave the bot's own channel
	if channel == strings.ToLower(m.cfg.GetBotUsername()) {
		log.Printf("Ignoring LeaveChannel for bot's own channel: %s", channel)
		return
	}

	m.mu.Lock()
	client, exists := m.clients[channel]
	if exists {
		delete(m.clients, channel)
		delete(m.msgCounts, channel)
	}
	m.mu.Unlock()

	if exists {
		client.Disconnect()
	}

	// Clean up timer tracking maps
	m.mu.Lock()
	delete(m.lastActivity, channel)
	delete(m.timerFired, channel)
	m.mu.Unlock()

	// Delete the brain data for this channel (bot's own channel never has a brain)
	botUsername := strings.ToLower(m.cfg.GetBotUsername())
	if channel != botUsername {
		if err := m.brainMgr.DeleteBrain(channel); err != nil {
			log.Printf("Warning: failed to delete brain for %s: %v", channel, err)
		}
	}

	// Always remove from config, even if not currently connected
	m.cfg.RemoveChannel(channel)
	log.Printf("Left channel: %s (brain data deleted)", channel)
}

// GetChannelStatus returns status for all configured channels (excluding bot's own channel)
func (m *Manager) GetChannelStatus() []ChannelStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	botUsername := strings.ToLower(m.cfg.GetBotUsername())

	// Get all configured channels from database
	configuredChannels := m.cfg.GetChannels()
	status := make([]ChannelStatus, 0, len(configuredChannels))

	for _, channel := range configuredChannels {
		channel = strings.ToLower(channel)
		// Skip bot's own channel
		if channel == botUsername {
			continue
		}

		// Check if currently connected
		connected := false
		if client, exists := m.clients[channel]; exists {
			connected = client.IsConnected()
		}

		// Get persistent message count from database
		msgCount, _, _ := m.cfg.GetChannelStats(channel)
		status = append(status, ChannelStatus{
			Channel:   channel,
			Connected: connected,
			Messages:  msgCount,
		})
	}

	return status
}

// GetBrainManager returns the brain manager
func (m *Manager) GetBrainManager() *markov.Manager {
	return m.brainMgr
}

// ReconnectChannel attempts to reconnect to a disconnected channel
func (m *Manager) ReconnectChannel(channel string) error {
	channel = strings.ToLower(channel)

	m.mu.RLock()
	client, exists := m.clients[channel]
	m.mu.RUnlock()

	if !exists {
		// Channel not in list, try joining fresh
		return m.JoinChannel(channel)
	}

	// Check if already connected
	if client.IsConnected() {
		return nil
	}

	// Disconnect old client
	client.Disconnect()

	// Remove old client
	m.mu.Lock()
	delete(m.clients, channel)
	m.mu.Unlock()

	// Rejoin the channel
	return m.JoinChannel(channel)
}

// SetEventHandler sets a callback for events
func (m *Manager) SetEventHandler(handler func(string, interface{})) {
	m.mu.Lock()
	m.eventHandler = handler
	m.mu.Unlock()
}

func (m *Manager) onMessage(channel, username, message, color, emotes, badges string) {
	m.mu.Lock()
	m.msgCounts[channel]++
	m.lastActivity[channel] = time.Now()
	m.timerFired[channel] = false
	handler := m.eventHandler
	m.mu.Unlock()

	if handler != nil {
		handler("message", map[string]string{
			"channel":  channel,
			"username": username,
			"message":  message,
			"color":    color,
			"emotes":   emotes,
			"badges":   badges,
		})
	}
}

func (m *Manager) onConnect(channel string) {
	m.mu.RLock()
	handler := m.eventHandler
	m.mu.RUnlock()

	if handler != nil {
		handler("connect", map[string]string{"channel": channel})
	}
}

func (m *Manager) onDisconnect(channel string) {
	m.mu.Lock()
	handler := m.eventHandler
	running := m.running
	alreadyReconnecting := m.botReconnecting
	m.mu.Unlock()

	if handler != nil {
		handler("disconnect", map[string]string{"channel": channel})
	}

	// Auto-reconnect the bot's own channel indefinitely (only one goroutine at a time)
	botUsername := strings.ToLower(m.cfg.GetBotUsername())
	if running && strings.ToLower(channel) == botUsername && !alreadyReconnecting {
		m.mu.Lock()
		m.botReconnecting = true
		m.mu.Unlock()
		go m.reconnectBotChannel()
	}
}

// reconnectBotChannel attempts to reconnect the bot's own channel indefinitely with exponential backoff
func (m *Manager) reconnectBotChannel() {
	defer func() {
		m.mu.Lock()
		m.botReconnecting = false
		m.mu.Unlock()
	}()

	botUsername := strings.ToLower(m.cfg.GetBotUsername())
	baseDelay := 5 * time.Second
	maxDelay := 5 * time.Minute
	attempt := 0

	for {
		// Check if the manager is still running
		m.mu.RLock()
		running := m.running
		m.mu.RUnlock()
		if !running {
			return
		}

		delay := baseDelay * time.Duration(1<<attempt)
		if delay > maxDelay {
			delay = maxDelay
		}
		log.Printf("Bot channel disconnected, reconnecting in %v (attempt %d)...", delay, attempt+1)

		select {
		case <-m.stopChan:
			return
		case <-time.After(delay):
		}

		// Clean up old client before reconnecting
		m.mu.Lock()
		if oldClient, exists := m.clients[botUsername]; exists {
			// Set running=false directly to prevent Disconnect from triggering another onDisconnect
			oldClient.mu.Lock()
			oldClient.running = false
			if oldClient.conn != nil {
				oldClient.conn.Close()
				oldClient.conn = nil
			}
			oldClient.mu.Unlock()
			delete(m.clients, botUsername)
			delete(m.msgCounts, botUsername)
		}
		m.mu.Unlock()

		err := m.JoinChannel(botUsername)
		if err == nil {
			log.Printf("Successfully reconnected to bot channel: %s", botUsername)
			return
		}
		log.Printf("Failed to reconnect bot channel: %v", err)
		attempt++
	}
}

func (m *Manager) onGeneration(channel string, result markov.GenerationResult) {
	m.mu.RLock()
	handler := m.eventHandler
	m.mu.RUnlock()

	if handler != nil {
		handler("generation", map[string]interface{}{
			"channel":        channel,
			"triggered":      result.Triggered,
			"success":        result.Success,
			"response":       result.Response,
			"attempts":       result.Attempts,
			"failure_reason": result.FailureReason,
			"counter":        result.Counter,
			"interval":       result.Interval,
			"using_global":   result.UsingGlobal,
		})
	}
}

func (m *Manager) onBanned(channel string) {
	log.Printf("Bot was banned from channel: %s - leaving channel", channel)
	m.LeaveChannel(channel)
}

func (m *Manager) onFollowersOnly(channel string) {
	channel = strings.ToLower(channel)

	// Check if bot is following the channel (allowed to chat even in followers-only)
	if m.isBotFollowing(channel) {
		log.Printf("[%s] Channel has followers-only mode but bot is following — staying", channel)
		return
	}

	// Check if already flagged to avoid duplicate whispers
	m.mu.RLock()
	alreadyFlagged := m.followersOnly[channel]
	m.mu.RUnlock()
	if alreadyFlagged {
		log.Printf("[%s] Already flagged as followers-only — disconnecting without re-whispering", channel)
		m.leaveChannelQuietly(channel)
		return
	}

	log.Printf("[%s] Followers-only mode detected via IRC — disconnecting (will retry next live check)", channel)

	// Flag the channel so we don't whisper again until they go offline
	m.mu.Lock()
	m.followersOnly[channel] = true
	m.mu.Unlock()

	whisperMsg := fmt.Sprintf("Hi! I disconnected from your channel because it's in followers-only mode, " +
		"which prevents me from chatting. I'll automatically reconnect next time you go live " +
		"if followers-only mode is disabled. Thanks!",
	)

	m.mu.RLock()
	handler := m.eventHandler
	m.mu.RUnlock()
	if handler != nil {
		handler("followers_only", map[string]string{"channel": channel, "message": whisperMsg})
	}

	go func(user, msg string) {
		if err := m.sendWhisper(user, msg); err != nil {
			log.Printf("Failed to send whisper to %s: %v", user, err)
		}
	}(channel, whisperMsg)

	m.leaveChannelQuietly(channel)
}

// sendWhisper sends a whisper (DM) to a user via the Twitch Helix API
func (m *Manager) sendWhisper(toUsername, message string) error {
	clientID := m.cfg.GetClientID()
	oauthToken := m.cfg.GetOAuthToken()
	if clientID == "" || oauthToken == "" {
		return fmt.Errorf("missing client ID or OAuth token")
	}

	token := strings.TrimPrefix(oauthToken, "oauth:")

	// Look up the bot's own user ID
	botUserID, _, _ := m.lookupTwitchUser(m.cfg.GetBotUsername(), clientID, oauthToken)
	if botUserID == "" {
		return fmt.Errorf("could not look up bot user ID")
	}

	// Look up the target user's ID
	toUserID, _, _ := m.lookupTwitchUser(toUsername, clientID, oauthToken)
	if toUserID == "" {
		return fmt.Errorf("could not look up user ID for %s", toUsername)
	}

	// POST /helix/whispers
	url := fmt.Sprintf("https://api.twitch.tv/helix/whispers?from_user_id=%s&to_user_id=%s", botUserID, toUserID)

	body := fmt.Sprintf(`{"message":%q}`, message)
	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Client-ID", clientID)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("whisper API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("whisper API returned %d: %s", resp.StatusCode, string(respBody))
	}

	log.Printf("Sent whisper to %s about followers-only mode", toUsername)
	return nil
}

func (m *Manager) onCommand(channel, username, command string) {
	botUsername := m.cfg.GetBotUsername()

	// Get the client for the bot's channel to send responses
	m.mu.RLock()
	botClient := m.clients[strings.ToLower(botUsername)]
	m.mu.RUnlock()

	switch command {
	case "!join":
		// Check if self-join is enabled
		if !m.cfg.GetAllowSelfJoin() {
			if botClient != nil {
				botClient.SendMessage(fmt.Sprintf("@%s Self-join is currently disabled.", username))
			}
			return
		}

		// Join the user's channel
		userChannel := strings.ToLower(username)

		// Check if already in that channel (connected or in config)
		m.mu.RLock()
		_, connected := m.clients[userChannel]
		m.mu.RUnlock()
		inConfig := m.cfg.ChannelExists(userChannel)

		if connected || inConfig {
			if botClient != nil {
				botClient.SendMessage(fmt.Sprintf("@%s I'm already in your channel!", username))
			}
			return
		}

		// Look up and store the user's Twitch ID
		clientID := m.cfg.GetClientID()
		oauthToken := m.cfg.GetOAuthToken()
		if clientID != "" && oauthToken != "" {
			ids := m.lookupUserIDs([]string{userChannel}, clientID, oauthToken)
			if userID, ok := ids[userChannel]; ok {
				m.cfg.SetUserIDMapping(userID, userChannel)
			}
		}

		// Check if the user is currently live
		if m.isChannelLive(userChannel) {
			// Channel is live — join immediately
			if err := m.JoinChannel(userChannel); err != nil {
				log.Printf("Failed to join channel %s via command: %v", userChannel, err)
				if botClient != nil {
					botClient.SendMessage(fmt.Sprintf("@%s Failed to join your channel: %v", username, err))
				}
			} else {
				log.Printf("Joined channel %s via !join command from %s", userChannel, username)
				if botClient != nil {
					botClient.SendMessage(fmt.Sprintf("@%s I've joined your channel! 🤖", username))
				}
			}
		} else {
			// Channel is offline — just add to config, live monitor will join when they go live
			m.cfg.AddChannel(userChannel)
			// Apply default brain mode for new channels
			if m.cfg.GetDefaultBrainMode() == "global" {
				m.cfg.SetChannelUseGlobalBrain(userChannel, true)
			}
			// Apply default timer settings for new channels
			if m.cfg.GetDefaultTimerEnabled() {
				m.cfg.SetChannelTimerEnabled(userChannel, true)
			}
			defaultTimerMin := m.cfg.GetDefaultTimerMinutes()
			if defaultTimerMin != 15 {
				m.cfg.SetChannelTimerMinutes(userChannel, defaultTimerMin)
			}
			log.Printf("Added channel %s via !join command from %s (offline, will join when live)", userChannel, username)
			if botClient != nil {
				botClient.SendMessage(fmt.Sprintf("@%s I've added your channel! I'll join when you go live. 🤖", username))
			}
		}

	case "!leave":
		// Leave the user's channel
		userChannel := strings.ToLower(username)

		// Check if in that channel
		m.mu.RLock()
		_, exists := m.clients[userChannel]
		m.mu.RUnlock()

		if !exists {
			if botClient != nil {
				botClient.SendMessage(fmt.Sprintf("@%s I'm not in your channel!", username))
			}
			return
		}

		// Don't allow leaving the bot's own channel
		if strings.EqualFold(userChannel, botUsername) {
			if botClient != nil {
				botClient.SendMessage(fmt.Sprintf("@%s I can't leave my own channel!", username))
			}
			return
		}

		m.LeaveChannel(userChannel)
		log.Printf("Left channel %s via !leave command from %s", userChannel, username)
		if botClient != nil {
			botClient.SendMessage(fmt.Sprintf("@%s I've left your channel. Goodbye! 👋", username))
		}
	}
}

// checkAndHandleUsernameChange looks up the Twitch user ID and handles username changes
func (m *Manager) checkAndHandleUsernameChange(channel string) string {
	clientID := m.cfg.GetClientID()
	oauthToken := m.cfg.GetOAuthToken()
	if clientID == "" || oauthToken == "" {
		return channel
	}

	// Look up user info from Twitch API
	userID, currentUsername, displayName := m.lookupTwitchUser(channel, clientID, oauthToken)
	if userID == "" {
		return channel
	}

	// Store the display name for this channel
	if displayName != "" {
		m.cfg.SetChannelDisplayName(currentUsername, displayName)
	}

	// Check if we have a stored username for this ID
	storedUsername := m.cfg.GetUsernameByID(userID)

	if storedUsername == "" {
		// First time seeing this user, just store the mapping
		m.cfg.SetUserIDMapping(userID, currentUsername)
		log.Printf("Stored new user mapping: %s -> %s", userID, currentUsername)
		return currentUsername
	}

	if storedUsername != currentUsername {
		// Username changed! Handle the rename
		log.Printf("Username change detected: %s -> %s (ID: %s)", storedUsername, currentUsername, userID)
		m.handleUsernameChange(storedUsername, currentUsername, userID)
		return currentUsername
	}

	return currentUsername
}

// lookupTwitchUser queries the Twitch API for user info
func (m *Manager) lookupTwitchUser(username, clientID, oauthToken string) (userID, currentUsername, displayName string) {
	req, err := http.NewRequest("GET", "https://api.twitch.tv/helix/users?login="+strings.ToLower(username), nil)
	if err != nil {
		return "", "", ""
	}

	token := strings.TrimPrefix(oauthToken, "oauth:")

	req.Header.Set("Client-ID", clientID)
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error looking up Twitch user %s: %v", username, err)
		return "", "", ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("Twitch API error looking up %s: %d - %s", username, resp.StatusCode, string(body))
		return "", "", ""
	}

	var apiResp struct {
		Data []struct {
			ID          string `json:"id"`
			Login       string `json:"login"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return "", "", ""
	}

	if len(apiResp.Data) == 0 {
		return "", "", ""
	}

	return apiResp.Data[0].ID, strings.ToLower(apiResp.Data[0].Login), apiResp.Data[0].DisplayName
}

// handleUsernameChange renames brain files and updates database references
func (m *Manager) handleUsernameChange(oldName, newName, userID string) {
	oldName = strings.ToLower(oldName)
	newName = strings.ToLower(newName)

	// Remove old brain from memory if loaded
	m.brainMgr.RemoveBrain(oldName)

	// Rename brain database file
	brainsDir := filepath.Join(database.GetDataDir(), "brains")
	oldPath := filepath.Join(brainsDir, oldName+".db")
	newPath := filepath.Join(brainsDir, newName+".db")

	// Also handle WAL and SHM files
	filesToRename := []struct{ old, new string }{
		{oldPath, newPath},
		{oldPath + "-wal", newPath + "-wal"},
		{oldPath + "-shm", newPath + "-shm"},
	}

	for _, f := range filesToRename {
		if _, err := os.Stat(f.old); err == nil {
			if err := os.Rename(f.old, f.new); err != nil {
				log.Printf("Error renaming %s to %s: %v", f.old, f.new, err)
			} else {
				log.Printf("Renamed brain file: %s -> %s", f.old, f.new)
			}
		}
	}

	// Update channel name in database
	m.cfg.RenameChannel(oldName, newName)

	// Update the user ID mapping
	m.cfg.SetUserIDMapping(userID, newName)

	log.Printf("Successfully migrated channel data from %s to %s", oldName, newName)
}

// monitorLiveChannels periodically checks which channels are live and joins/leaves accordingly
func (m *Manager) monitorLiveChannels() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopChan:
			return
		case <-ticker.C:
			m.updateLiveConnections()
		}
	}
}

// monitorInactivityTimers periodically checks for channels with inactivity timers
func (m *Manager) monitorInactivityTimers() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopChan:
			return
		case <-ticker.C:
			m.checkInactivityTimers()
		}
	}
}

// checkInactivityTimers checks all connected channels and generates a message if inactive long enough
func (m *Manager) checkInactivityTimers() {
	botUsername := strings.ToLower(m.cfg.GetBotUsername())

	m.mu.RLock()
	channels := make([]string, 0, len(m.clients))
	for ch := range m.clients {
		if ch != botUsername {
			channels = append(channels, ch)
		}
	}
	m.mu.RUnlock()

	now := time.Now()

	for _, channel := range channels {
		// Check if timer is enabled for this channel
		if !m.cfg.GetChannelTimerEnabled(channel) {
			continue
		}

		timerMinutes := m.cfg.GetChannelTimerMinutes(channel)
		threshold := time.Duration(timerMinutes) * time.Minute

		m.mu.RLock()
		lastAct, hasActivity := m.lastActivity[channel]
		alreadyFired := m.timerFired[channel]
		client, clientExists := m.clients[channel]
		m.mu.RUnlock()

		// Skip if no activity recorded yet, already fired, or client missing
		if !hasActivity || alreadyFired || !clientExists || client == nil {
			continue
		}

		// Check if enough time has passed since last activity
		if now.Sub(lastAct) >= threshold {
			// Mark as fired so we don't keep generating
			m.mu.Lock()
			m.timerFired[channel] = true
			m.mu.Unlock()

			// Generate a message
			go m.generateTimerMessage(channel, client)
		}
	}
}

// generateTimerMessage generates and sends a message due to inactivity timer
func (m *Manager) generateTimerMessage(channel string, client *Client) {
	if !client.IsConnected() {
		return
	}

	brain := m.brainMgr.GetBrain(channel)
	if brain == nil {
		return
	}

	// Choose generator based on global brain setting
	var generator func(int) string
	if m.cfg.GetChannelUseGlobalBrain(channel) {
		generator = m.brainMgr.GenerateGlobal
	}

	maxAttempts := 5
	var response string
	for i := 0; i < maxAttempts; i++ {
		if generator != nil {
			response = generator(maxAttempts)
		} else {
			response = brain.Generate(maxAttempts)
		}
		if response != "" {
			break
		}
	}

	if response != "" {
		client.SendMessage(response)
		database.SaveQuote(channel, response)
		brain.SaveLastMessage(response)
		log.Printf("[%s] Inactivity timer generated: %s", channel, response)

		// Update lastActivity so the timer can fire again after the configured
		// duration. Reset timerFired so the next check will re-evaluate.
		m.mu.Lock()
		m.lastActivity[channel] = time.Now()
		m.timerFired[channel] = false
		m.mu.Unlock()

		// Broadcast generation event
		m.mu.RLock()
		handler := m.eventHandler
		m.mu.RUnlock()

		if handler != nil {
			handler("generation", map[string]interface{}{
				"channel":        channel,
				"triggered":      true,
				"success":        true,
				"response":       response,
				"attempts":       1,
				"failure_reason": "",
				"counter":        0,
				"interval":       0,
				"using_global":   m.cfg.GetChannelUseGlobalBrain(channel),
				"timer":          true,
			})
		}
	}
}

// GetChannelTimerInfo returns timer status for a channel
func (m *Manager) GetChannelTimerInfo(channel string) (enabled bool, minutes int, lastActivity time.Time, fired bool) {
	channel = strings.ToLower(channel)
	enabled = m.cfg.GetChannelTimerEnabled(channel)
	minutes = m.cfg.GetChannelTimerMinutes(channel)

	m.mu.RLock()
	lastActivity = m.lastActivity[channel]
	fired = m.timerFired[channel]
	m.mu.RUnlock()
	return
}

// IsChannelFollowersOnly returns whether a channel is currently flagged as followers-only
func (m *Manager) IsChannelFollowersOnly(channel string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.followersOnly[strings.ToLower(channel)]
}

// updateLiveConnections joins live channels and leaves offline channels
func (m *Manager) updateLiveConnections() {
	clientID := m.cfg.GetClientID()
	oauthToken := m.cfg.GetOAuthToken()
	if clientID == "" || oauthToken == "" {
		return
	}

	botUsername := strings.ToLower(m.cfg.GetBotUsername())
	channels := m.cfg.GetChannels()

	if len(channels) == 0 {
		return
	}

	// Build a map of channel name -> user ID (look up any missing IDs)
	channelIDs := m.ensureChannelIDs(channels, clientID, oauthToken)

	// Query Twitch API for live status using user IDs
	liveChannels, usernameUpdates := m.getLiveChannelSetByID(channelIDs, clientID, oauthToken)

	// Handle any username changes detected during polling
	for oldName, newName := range usernameUpdates {
		userID := channelIDs[oldName]
		log.Printf("Username change detected during polling: %s -> %s (ID: %s)", oldName, newName, userID)
		m.handleUsernameChange(oldName, newName, userID)
		// Update our local map for the rest of this cycle
		delete(liveChannels, oldName)
		liveChannels[newName] = true
	}

	// Get currently connected channels (excluding bot's own channel)
	m.mu.RLock()
	connectedChannels := make(map[string]bool)
	for ch := range m.clients {
		if ch != botUsername {
			connectedChannels[ch] = true
		}
	}
	m.mu.RUnlock()

	// Join channels that are live but not connected
	for _, channel := range channels {
		ch := strings.ToLower(channel)
		if ch == botUsername {
			continue
		}

		isLive := liveChannels[ch]
		isConnected := connectedChannels[ch]

		if isLive && !isConnected {
			// Skip channels already flagged as followers-only
			m.mu.RLock()
			skipFollowers := m.followersOnly[ch]
			m.mu.RUnlock()
			if skipFollowers {
				continue
			}

			// Check if channel has followers-only mode before joining
			broadcasterID := channelIDs[ch]
			if broadcasterID != "" && m.isChannelFollowersOnly(broadcasterID, clientID, oauthToken) && !m.isBotFollowing(ch) {
				log.Printf("Channel %s is now live but has followers-only mode — skipping (will retry next check)", ch)

				whisperMsg := fmt.Sprintf("Hi! I couldn't join your channel because it's in followers-only mode, " +
					"which prevents me from chatting. I'll automatically join next time you go live " +
					"if followers-only mode is disabled. Thanks!",
				)

				// Flag channel so we don't whisper again until they go offline
				m.mu.Lock()
				m.followersOnly[ch] = true
				m.mu.Unlock()

				m.mu.RLock()
				handler := m.eventHandler
				m.mu.RUnlock()
				if handler != nil {
					handler("followers_only", map[string]string{"channel": ch, "message": whisperMsg})
				}

				go func(user, msg string) {
					if err := m.sendWhisper(user, msg); err != nil {
						log.Printf("Failed to send whisper to %s: %v", user, err)
					}
				}(ch, whisperMsg)

				continue
			}

			log.Printf("Channel %s is now live, joining...", ch)
			if err := m.JoinChannel(ch); err != nil {
				log.Printf("Failed to join live channel %s: %v", ch, err)
			}
			time.Sleep(500 * time.Millisecond) // Rate limit
		} else if !isLive && isConnected {
			log.Printf("Channel %s is now offline, leaving...", ch)
			m.leaveChannelQuietly(ch)
			time.Sleep(500 * time.Millisecond) // Rate limit
		}

		// Clear followers-only flag when channel goes offline so we re-check next time
		if !isLive {
			m.mu.Lock()
			delete(m.followersOnly, ch)
			m.mu.Unlock()
		}
	}
}

// ensureChannelIDs makes sure all channels have user IDs stored, returns map of channel->userID
func (m *Manager) ensureChannelIDs(channels []string, clientID, oauthToken string) map[string]string {
	result := make(map[string]string)
	var needsLookup []string

	// Check which channels already have IDs stored
	for _, ch := range channels {
		ch = strings.ToLower(ch)
		if userID := m.cfg.GetUserIDByUsername(ch); userID != "" {
			result[ch] = userID
		} else {
			needsLookup = append(needsLookup, ch)
		}
	}

	// Look up missing IDs
	if len(needsLookup) > 0 {
		newIDs := m.lookupUserIDs(needsLookup, clientID, oauthToken)
		for ch, userID := range newIDs {
			result[ch] = userID
			m.cfg.SetUserIDMapping(userID, ch)
			log.Printf("Stored user ID for %s: %s", ch, userID)
		}
	}

	return result
}

// lookupUserIDs looks up Twitch user IDs for a list of usernames
func (m *Manager) lookupUserIDs(usernames []string, clientID, oauthToken string) map[string]string {
	result := make(map[string]string)
	if len(usernames) == 0 {
		return result
	}

	// Build query params (max 100 per request)
	params := "?"
	for i, name := range usernames {
		if i > 0 {
			params += "&"
		}
		params += "login=" + strings.ToLower(name)
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
		log.Printf("Error looking up user IDs: %v", err)
		return result
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return result
	}

	var apiResp struct {
		Data []struct {
			ID    string `json:"id"`
			Login string `json:"login"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return result
	}

	for _, user := range apiResp.Data {
		result[strings.ToLower(user.Login)] = user.ID
	}

	return result
}

// getLiveChannelSetByID returns live channels and any username changes detected
func (m *Manager) getLiveChannelSetByID(channelIDs map[string]string, clientID, oauthToken string) (live map[string]bool, usernameChanges map[string]string) {
	live = make(map[string]bool)
	usernameChanges = make(map[string]string)

	if len(channelIDs) == 0 {
		return
	}

	// Build reverse map: userID -> stored username
	idToUsername := make(map[string]string)
	var userIDs []string
	for username, userID := range channelIDs {
		if userID != "" {
			idToUsername[userID] = username
			userIDs = append(userIDs, userID)
		}
	}

	if len(userIDs) == 0 {
		return
	}

	// Build query params using user IDs (max 100 per request)
	params := "?"
	for i, id := range userIDs {
		if i > 0 {
			params += "&"
		}
		params += "user_id=" + id
	}

	req, err := http.NewRequest("GET", "https://api.twitch.tv/helix/streams"+params, nil)
	if err != nil {
		return
	}

	token := strings.TrimPrefix(oauthToken, "oauth:")
	req.Header.Set("Client-ID", clientID)
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error checking live channels: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return
	}

	var apiResp struct {
		Data []struct {
			UserID    string `json:"user_id"`
			UserLogin string `json:"user_login"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return
	}

	for _, stream := range apiResp.Data {
		currentUsername := strings.ToLower(stream.UserLogin)
		storedUsername := idToUsername[stream.UserID]

		// Check for username change
		if storedUsername != "" && storedUsername != currentUsername {
			usernameChanges[storedUsername] = currentUsername
			live[currentUsername] = true
		} else if storedUsername != "" {
			live[storedUsername] = true
		} else {
			live[currentUsername] = true
		}
	}

	return
}

// isChannelFollowersOnly checks if a channel has followers-only mode enabled via the Twitch Helix API
func (m *Manager) isChannelFollowersOnly(broadcasterID, clientID, oauthToken string) bool {
	token := strings.TrimPrefix(oauthToken, "oauth:")

	url := "https://api.twitch.tv/helix/chat/settings?broadcaster_id=" + broadcasterID
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Client-ID", clientID)
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	var apiResp struct {
		Data []struct {
			FollowerMode bool `json:"follower_mode"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return false
	}

	return len(apiResp.Data) > 0 && apiResp.Data[0].FollowerMode
}

// isChannelLive checks if a single channel is currently live via the Twitch API
func (m *Manager) isChannelLive(channel string) bool {
	clientID := m.cfg.GetClientID()
	oauthToken := m.cfg.GetOAuthToken()
	if clientID == "" || oauthToken == "" {
		return false
	}

	// Try to use stored user ID first, fall back to login query
	userID := m.cfg.GetUserIDByUsername(strings.ToLower(channel))
	var url string
	if userID != "" {
		url = "https://api.twitch.tv/helix/streams?user_id=" + userID
	} else {
		url = "https://api.twitch.tv/helix/streams?user_login=" + strings.ToLower(channel)
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false
	}

	token := strings.TrimPrefix(oauthToken, "oauth:")
	req.Header.Set("Client-ID", clientID)
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error checking live status for %s: %v", channel, err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	var apiResp struct {
		Data []struct {
			UserID string `json:"user_id"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return false
	}

	return len(apiResp.Data) > 0
}

// isBotFollowing checks if the bot account is following a channel via the Twitch Helix API
func (m *Manager) isBotFollowing(channel string) bool {
	clientID := m.cfg.GetClientID()
	oauthToken := m.cfg.GetOAuthToken()
	if clientID == "" || oauthToken == "" {
		return false
	}

	token := strings.TrimPrefix(oauthToken, "oauth:")

	// Get bot user ID
	botUserID, _, _ := m.lookupTwitchUser(m.cfg.GetBotUsername(), clientID, oauthToken)
	if botUserID == "" {
		return false
	}

	// Get channel user ID
	channelUserID := m.cfg.GetUserIDByUsername(strings.ToLower(channel))
	if channelUserID == "" {
		channelUserID, _, _ = m.lookupTwitchUser(channel, clientID, oauthToken)
		if channelUserID == "" {
			return false
		}
	}

	url := fmt.Sprintf("https://api.twitch.tv/helix/channels/followed?user_id=%s&broadcaster_id=%s", botUserID, channelUserID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false
	}

	req.Header.Set("Client-ID", clientID)
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error checking follow status for %s: %v", channel, err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	var apiResp struct {
		Data []struct {
			BroadcasterID string `json:"broadcaster_id"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return false
	}

	return len(apiResp.Data) > 0
}

// leaveChannelQuietly disconnects from a channel without removing it from config
func (m *Manager) leaveChannelQuietly(channel string) {
	m.mu.Lock()
	client, exists := m.clients[channel]
	if exists {
		delete(m.clients, channel)
		delete(m.msgCounts, channel)
	}
	m.mu.Unlock()

	if exists {
		client.Disconnect()
		log.Printf("Left offline channel: %s", channel)

		// Broadcast disconnect event
		m.mu.RLock()
		handler := m.eventHandler
		m.mu.RUnlock()

		if handler != nil {
			handler("disconnect", map[string]string{"channel": channel})
		}
	}
}
