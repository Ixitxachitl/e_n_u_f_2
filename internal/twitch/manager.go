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
	cfg          *config.Config
	brainMgr     *markov.Manager
	clients      map[string]*Client
	msgCounts    map[string]int64
	mu           sync.RWMutex
	running      bool
	eventHandler func(event string, data interface{})
	stopChan     chan struct{}
}

// NewManager creates a new Twitch connection manager
func NewManager(cfg *config.Config) *Manager {
	return &Manager{
		cfg:       cfg,
		brainMgr:  markov.NewManager(cfg),
		clients:   make(map[string]*Client),
		msgCounts: make(map[string]int64),
		stopChan:  make(chan struct{}),
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
	}

	log.Printf("Joined channel: %s", channel)
	return nil
}

// LeaveChannel disconnects from a channel, removes it from config, and deletes its brain data
func (m *Manager) LeaveChannel(channel string) {
	channel = strings.ToLower(channel)

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

	// Delete the brain data for this channel
	if err := m.brainMgr.DeleteBrain(channel); err != nil {
		log.Printf("Warning: failed to delete brain for %s: %v", channel, err)
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
	m.mu.RLock()
	handler := m.eventHandler
	m.mu.RUnlock()

	if handler != nil {
		handler("disconnect", map[string]string{"channel": channel})
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

		// Check if already in that channel
		m.mu.RLock()
		_, exists := m.clients[userChannel]
		m.mu.RUnlock()

		if exists {
			if botClient != nil {
				botClient.SendMessage(fmt.Sprintf("@%s I'm already in your channel!", username))
			}
			return
		}

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
