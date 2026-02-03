package twitch

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"twitchbot/internal/config"
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
}

// NewManager creates a new Twitch connection manager
func NewManager(cfg *config.Config) *Manager {
	return &Manager{
		cfg:       cfg,
		brainMgr:  markov.NewManager(cfg),
		clients:   make(map[string]*Client),
		msgCounts: make(map[string]int64),
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
		time.Sleep(500 * time.Millisecond)
	}

	channels := m.cfg.GetChannels()
	for _, channel := range channels {
		// Skip if it's the bot's own channel (already joined)
		if strings.EqualFold(channel, botUsername) {
			continue
		}
		if err := m.JoinChannel(channel); err != nil {
			log.Printf("Failed to join channel %s: %v", channel, err)
		}
		// Small delay between connections to avoid rate limiting
		time.Sleep(500 * time.Millisecond)
	}

	return nil
}

// Stop disconnects from all channels
func (m *Manager) Stop() {
	m.mu.Lock()
	m.running = false
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
	)

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

// LeaveChannel disconnects from a channel
func (m *Manager) LeaveChannel(channel string) {
	m.mu.Lock()
	client, exists := m.clients[channel]
	if exists {
		delete(m.clients, channel)
		delete(m.msgCounts, channel)
	}
	m.mu.Unlock()

	if exists {
		client.Disconnect()
		m.cfg.RemoveChannel(channel)
		log.Printf("Left channel: %s", channel)
	}
}

// GetChannelStatus returns status for all connected channels (excluding bot's own channel)
func (m *Manager) GetChannelStatus() []ChannelStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	botUsername := strings.ToLower(m.cfg.GetBotUsername())
	status := make([]ChannelStatus, 0, len(m.clients))
	for channel, client := range m.clients {
		// Skip bot's own channel
		if channel == botUsername {
			continue
		}
		// Get persistent message count from database
		msgCount, _, _ := m.cfg.GetChannelStats(channel)
		status = append(status, ChannelStatus{
			Channel:   channel,
			Connected: client.IsConnected(),
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

func (m *Manager) onMessage(channel, username, message string) {
	m.mu.Lock()
	m.msgCounts[channel]++
	handler := m.eventHandler
	m.mu.Unlock()

	if handler != nil {
		handler("message", map[string]string{
			"channel":  channel,
			"username": username,
			"message":  message,
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

func (m *Manager) onCommand(channel, username, command string) {
	botUsername := m.cfg.GetBotUsername()

	// Get the client for the bot's channel to send responses
	m.mu.RLock()
	botClient := m.clients[strings.ToLower(botUsername)]
	m.mu.RUnlock()

	switch command {
	case "!join":
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
