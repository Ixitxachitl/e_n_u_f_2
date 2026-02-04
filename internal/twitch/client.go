package twitch

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"time"

	"twitchbot/internal/config"
	"twitchbot/internal/markov"
)

const (
	twitchIRCServer = "irc.chat.twitch.tv:6697" // SSL port - more reliable than 6667
)

// Client represents a Twitch IRC client for a single channel
type Client struct {
	channel      string
	cfg          *config.Config
	brain        *markov.Brain
	conn         net.Conn
	writer       *bufio.Writer
	running      bool
	mu           sync.Mutex
	onMessage    func(channel, username, message, color, emotes, badges string)
	onConnect    func(channel string)
	onDisconnect func(channel string)
	onCommand    func(channel, username, command string)
}

// Message represents a parsed IRC message
type Message struct {
	Raw      string
	Tags     map[string]string
	Source   string
	Command  string
	Channel  string
	Username string
	Content  string
}

// NewClient creates a new Twitch client for a channel
func NewClient(channel string, cfg *config.Config, brain *markov.Brain) *Client {
	return &Client{
		channel: strings.ToLower(channel),
		cfg:     cfg,
		brain:   brain,
	}
}

// SetCallbacks sets the callback functions
func (c *Client) SetCallbacks(onMessage func(string, string, string, string, string, string), onConnect func(string), onDisconnect func(string), onCommand func(string, string, string)) {
	c.onMessage = onMessage
	c.onConnect = onConnect
	c.onDisconnect = onDisconnect
	c.onCommand = onCommand
}

// Connect establishes connection to Twitch IRC with retry logic
func (c *Client) Connect() error {
	return c.ConnectWithRetry(3, 5*time.Second)
}

// ConnectWithRetry attempts to connect with exponential backoff
func (c *Client) ConnectWithRetry(maxRetries int, baseDelay time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	oauthToken := c.cfg.GetOAuthToken()
	botUsername := c.cfg.GetBotUsername()

	if oauthToken == "" || botUsername == "" {
		return fmt.Errorf("bot not configured: missing OAuth token or username")
	}

	var err error
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := baseDelay * time.Duration(1<<(attempt-1)) // Exponential backoff
			if delay > 60*time.Second {
				delay = 60 * time.Second // Cap at 60 seconds
			}
			log.Printf("[%s] Connection attempt %d failed, retrying in %v...", c.channel, attempt, delay)
			c.mu.Unlock()
			time.Sleep(delay)
			c.mu.Lock()
		}

		// Use TLS for secure connection to port 6697
		dialer := &net.Dialer{Timeout: 15 * time.Second}
		c.conn, err = tls.DialWithDialer(dialer, "tcp", twitchIRCServer, nil)
		if err == nil {
			break
		}
		lastErr = err
	}

	if c.conn == nil {
		return fmt.Errorf("failed to connect after %d attempts: %w", maxRetries+1, lastErr)
	}

	c.writer = bufio.NewWriter(c.conn)

	// Authenticate
	c.sendRaw("PASS " + oauthToken)
	c.sendRaw("NICK " + botUsername)

	// Request capabilities for tags
	c.sendRaw("CAP REQ :twitch.tv/tags twitch.tv/commands")

	// Join channel
	c.sendRaw("JOIN #" + c.channel)

	c.running = true

	if c.onConnect != nil {
		c.onConnect(c.channel)
	}

	return nil
}

// Run starts the message read loop
func (c *Client) Run() {
	reader := textproto.NewReader(bufio.NewReader(c.conn))

	for c.isRunning() {
		line, err := reader.ReadLine()
		if err != nil {
			if c.isRunning() {
				log.Printf("[%s] Read error: %v", c.channel, err)
			}
			break
		}

		c.handleMessage(line)
	}

	if c.onDisconnect != nil {
		c.onDisconnect(c.channel)
	}
}

// Disconnect closes the connection
func (c *Client) Disconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.running = false
	if c.conn != nil {
		c.sendRaw("PART #" + c.channel)
		c.conn.Close()
		c.conn = nil
	}
}

// SendMessage sends a chat message to the channel
func (c *Client) SendMessage(message string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil || !c.running {
		return
	}

	c.sendRaw(fmt.Sprintf("PRIVMSG #%s :%s", c.channel, message))
}

// Channel returns the channel name
func (c *Client) Channel() string {
	return c.channel
}

// IsConnected returns connection status
func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running && c.conn != nil
}

func (c *Client) isRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

func (c *Client) sendRaw(message string) {
	if c.writer == nil {
		return
	}
	c.writer.WriteString(message + "\r\n")
	c.writer.Flush()
}

func (c *Client) handleMessage(raw string) {
	// Handle PING
	if strings.HasPrefix(raw, "PING") {
		c.mu.Lock()
		c.sendRaw("PONG" + raw[4:])
		c.mu.Unlock()
		return
	}

	msg := parseMessage(raw)
	if msg == nil {
		return
	}

	switch msg.Command {
	case "PRIVMSG":
		if c.onMessage != nil {
			color := msg.Tags["color"]
			emotes := msg.Tags["emotes"]
			badges := msg.Tags["badges"]
			c.onMessage(msg.Channel, msg.Username, msg.Content, color, emotes, badges)
		}

		// Check for commands
		cmd := strings.ToLower(strings.TrimSpace(msg.Content))

		// !join and !leave only work in bot's own channel
		if strings.EqualFold(msg.Channel, c.cfg.GetBotUsername()) {
			if cmd == "!join" || cmd == "!leave" {
				if c.onCommand != nil {
					c.onCommand(msg.Channel, msg.Username, cmd)
				}
				return
			}

			// !response <number> - set per-channel message interval
			if strings.HasPrefix(cmd, "!response") {
				parts := strings.Fields(msg.Content)
				if len(parts) == 2 {
					num, err := strconv.Atoi(parts[1])
					if err != nil || num < 1 || num > 100 {
						c.SendMessage(fmt.Sprintf("@%s Please use !response <1-100> to set how many messages before I respond in your channel.", msg.Username))
						return
					}
					userChannel := strings.ToLower(msg.Username)
					c.cfg.SetChannelMessageInterval(userChannel, num)
					c.SendMessage(fmt.Sprintf("@%s I will now respond every %d messages in your channel!", msg.Username, num))
				} else {
					// Show current setting
					userChannel := strings.ToLower(msg.Username)
					current := c.cfg.GetChannelMessageInterval(userChannel)
					c.SendMessage(fmt.Sprintf("@%s Your channel is set to %d messages. Use !response <1-100> to change.", msg.Username, current))
				}
				return
			}
		}

		// !ignoreme and !listentome work in any channel
		if cmd == "!ignoreme" {
			c.cfg.AddBlacklistedUser(msg.Username)
			c.SendMessage(fmt.Sprintf("@%s I will no longer learn from your messages. Use !listentome to undo.", msg.Username))
			return
		}
		if cmd == "!listentome" {
			c.cfg.RemoveBlacklistedUser(msg.Username)
			c.SendMessage(fmt.Sprintf("@%s I will now learn from your messages again!", msg.Username))
			return
		}

		// Process with brain (if brain exists - bot's own channel has no brain)
		if c.brain != nil {
			response := c.brain.ProcessMessage(msg.Content, msg.Username, c.cfg.GetBotUsername())
			if response != "" {
				c.SendMessage(response)
			}
		}

	case "NOTICE":
		log.Printf("[%s] NOTICE: %s", c.channel, msg.Content)

	case "RECONNECT":
		log.Printf("[%s] Received RECONNECT, reconnecting...", c.channel)
		c.Disconnect()
		time.Sleep(time.Second)
		c.Connect()
		go c.Run()
	}
}

func parseMessage(raw string) *Message {
	msg := &Message{
		Raw:  raw,
		Tags: make(map[string]string),
	}

	// Parse tags if present
	if strings.HasPrefix(raw, "@") {
		parts := strings.SplitN(raw[1:], " ", 2)
		if len(parts) < 2 {
			return nil
		}

		tagPairs := strings.Split(parts[0], ";")
		for _, pair := range tagPairs {
			kv := strings.SplitN(pair, "=", 2)
			if len(kv) == 2 {
				msg.Tags[kv[0]] = kv[1]
			}
		}
		raw = parts[1]
	}

	// Parse source
	if strings.HasPrefix(raw, ":") {
		parts := strings.SplitN(raw[1:], " ", 2)
		if len(parts) < 2 {
			return nil
		}
		msg.Source = parts[0]
		raw = parts[1]
	}

	// Parse command and params
	parts := strings.SplitN(raw, " ", 3)
	if len(parts) < 1 {
		return nil
	}

	msg.Command = parts[0]

	if len(parts) >= 2 {
		// Channel
		if strings.HasPrefix(parts[1], "#") {
			msg.Channel = strings.TrimPrefix(parts[1], "#")
		}
	}

	if len(parts) >= 3 {
		// Content
		msg.Content = strings.TrimPrefix(parts[2], ":")
	}

	// Extract username from source
	if msg.Source != "" {
		if idx := strings.Index(msg.Source, "!"); idx > 0 {
			msg.Username = msg.Source[:idx]
		}
	}

	// Use display-name from tags if available
	if displayName, ok := msg.Tags["display-name"]; ok && displayName != "" {
		msg.Username = displayName
	}

	return msg
}
