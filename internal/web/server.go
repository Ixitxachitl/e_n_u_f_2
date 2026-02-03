package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"twitchbot/internal/config"
	"twitchbot/internal/database"
	"twitchbot/internal/twitch"
)

//go:embed static/*
var staticFiles embed.FS

// Server represents the web UI server
type Server struct {
	cfg      *config.Config
	manager  *twitch.Manager
	server   *http.Server
	upgrader websocket.Upgrader
	clients  map[*websocket.Conn]bool
	mu       sync.Mutex
}

// NewServer creates a new web server
func NewServer(cfg *config.Config, manager *twitch.Manager) *Server {
	s := &Server{
		cfg:     cfg,
		manager: manager,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		clients: make(map[*websocket.Conn]bool),
	}

	// Set up event handler for real-time updates
	manager.SetEventHandler(s.broadcastEvent)

	return s
}

// Start starts the web server
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// OAuth routes
	mux.HandleFunc("/auth/twitch", s.handleTwitchAuth)
	mux.HandleFunc("/auth/callback", s.handleTwitchCallback)
	mux.HandleFunc("/auth/token", s.handleTokenExchange)

	// API routes
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/channels", s.handleChannels)
	mux.HandleFunc("/api/channels/", s.handleChannelAction)
	mux.HandleFunc("/api/brains", s.handleBrains)
	mux.HandleFunc("/api/brains/", s.handleBrainAction)
	mux.HandleFunc("/api/blacklist", s.handleBlacklist)
	mux.HandleFunc("/api/blacklist/", s.handleBlacklistAction)
	mux.HandleFunc("/api/userblacklist", s.handleUserBlacklist)
	mux.HandleFunc("/api/userblacklist/", s.handleUserBlacklistAction)
	mux.HandleFunc("/api/database", s.handleDatabase)
	mux.HandleFunc("/api/logout", s.handleLogout)
	mux.HandleFunc("/ws", s.handleWebSocket)

	// Static files
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return err
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	s.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", s.cfg.GetWebPort()),
		Handler: mux,
	}

	return s.server.ListenAndServe()
}

// Stop gracefully stops the web server
func (s *Server) Stop() {
	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.server.Shutdown(ctx)
	}
}

// handleTwitchAuth redirects to Twitch OAuth
func (s *Server) handleTwitchAuth(w http.ResponseWriter, r *http.Request) {
	clientID := s.cfg.GetClientID()
	if clientID == "" {
		httpError(w, "Client ID not configured", http.StatusBadRequest)
		return
	}

	// Build redirect URI from request
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	redirectURI := fmt.Sprintf("%s://%s/auth/callback", scheme, r.Host)

	// Build Twitch OAuth URL with force_verify
	authURL := fmt.Sprintf(
		"https://id.twitch.tv/oauth2/authorize?client_id=%s&redirect_uri=%s&response_type=token&scope=chat:read+chat:edit&force_verify=true",
		clientID,
		redirectURI,
	)

	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

// handleTwitchCallback serves the callback page that extracts the token from URL fragment
func (s *Server) handleTwitchCallback(w http.ResponseWriter, r *http.Request) {
	// The token is in the URL fragment, so we need to use JavaScript to extract it
	html := `<!DOCTYPE html>
<html>
<head>
    <title>Twitch Login</title>
    <style>
        body { background: #0e0e10; color: #efeff1; font-family: sans-serif; display: flex; justify-content: center; align-items: center; height: 100vh; margin: 0; }
        .container { text-align: center; }
        .spinner { border: 4px solid #1f1f23; border-top: 4px solid #9147ff; border-radius: 50%; width: 40px; height: 40px; animation: spin 1s linear infinite; margin: 20px auto; }
        @keyframes spin { 0% { transform: rotate(0deg); } 100% { transform: rotate(360deg); } }
        .error { color: #f44336; }
        .success { color: #00c853; }
    </style>
</head>
<body>
    <div class="container">
        <div class="spinner" id="spinner"></div>
        <p id="status">Processing login...</p>
    </div>
    <script>
        const hash = window.location.hash.substring(1);
        const params = new URLSearchParams(hash);
        const accessToken = params.get('access_token');
        const error = params.get('error');
        const errorDesc = params.get('error_description');
        
        const statusEl = document.getElementById('status');
        const spinnerEl = document.getElementById('spinner');
        
        if (error) {
            spinnerEl.style.display = 'none';
            statusEl.className = 'error';
            statusEl.textContent = 'Login failed: ' + (errorDesc || error);
            setTimeout(() => window.location.href = '/', 3000);
        } else if (accessToken) {
            fetch('/auth/token', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ access_token: accessToken })
            })
            .then(res => res.json())
            .then(data => {
                spinnerEl.style.display = 'none';
                if (data.error) {
                    statusEl.className = 'error';
                    statusEl.textContent = 'Error: ' + data.error;
                } else {
                    statusEl.className = 'success';
                    statusEl.textContent = 'Logged in as ' + data.username + '! Redirecting...';
                }
                setTimeout(() => window.location.href = '/', 2000);
            })
            .catch(err => {
                spinnerEl.style.display = 'none';
                statusEl.className = 'error';
                statusEl.textContent = 'Error: ' + err.message;
                setTimeout(() => window.location.href = '/', 3000);
            });
        } else {
            spinnerEl.style.display = 'none';
            statusEl.className = 'error';
            statusEl.textContent = 'No token received';
            setTimeout(() => window.location.href = '/', 3000);
        }
    </script>
</body>
</html>`
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(html))
}

// handleTokenExchange receives the token from the callback page and validates it
func (s *Server) handleTokenExchange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if req.AccessToken == "" {
		httpError(w, "No token provided", http.StatusBadRequest)
		return
	}

	// Validate token and get user info from Twitch
	client := &http.Client{Timeout: 10 * time.Second}
	httpReq, _ := http.NewRequest("GET", "https://api.twitch.tv/helix/users", nil)
	httpReq.Header.Set("Authorization", "Bearer "+req.AccessToken)
	httpReq.Header.Set("Client-Id", s.cfg.GetClientID())

	resp, err := client.Do(httpReq)
	if err != nil {
		httpError(w, "Failed to validate token", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("Twitch API error: %s", string(body))
		httpError(w, "Invalid token", http.StatusUnauthorized)
		return
	}

	var twitchResp struct {
		Data []struct {
			ID    string `json:"id"`
			Login string `json:"login"`
			Name  string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&twitchResp); err != nil {
		httpError(w, "Failed to parse Twitch response", http.StatusInternalServerError)
		return
	}

	if len(twitchResp.Data) == 0 {
		httpError(w, "No user data returned", http.StatusInternalServerError)
		return
	}

	user := twitchResp.Data[0]

	// Save the token and username
	s.cfg.SetOAuthToken("oauth:" + req.AccessToken)
	s.cfg.SetBotUsername(user.Login)

	log.Printf("Logged in as: %s", user.Login)

	jsonResponse(w, map[string]string{
		"status":   "success",
		"username": user.Login,
		"name":     user.Name,
	})
}

// handleLogout clears the OAuth token
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.cfg.SetOAuthToken("")
	s.cfg.SetBotUsername("")

	jsonResponse(w, map[string]string{"status": "logged_out"})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]interface{}{
		"configured":    s.cfg.IsConfigured(),
		"client_id_set": s.cfg.GetClientID() != "",
		"channels":      s.manager.GetChannelStatus(),
		"database":      s.manager.GetBrainManager().GetDatabaseStats(),
	}
	jsonResponse(w, status)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// Don't expose full token, just show if it's set
		tokenSet := s.cfg.GetOAuthToken() != ""
		clientIDSet := s.cfg.GetClientID() != ""
		config := map[string]interface{}{
			"bot_username":     s.cfg.GetBotUsername(),
			"oauth_token_set":  tokenSet,
			"client_id_set":    clientIDSet,
			"message_interval": s.cfg.GetMessageInterval(),
			"web_port":         s.cfg.GetWebPort(),
		}
		jsonResponse(w, config)

	case http.MethodPut:
		var req struct {
			ClientID        *string `json:"client_id"`
			MessageInterval *int    `json:"message_interval"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "Invalid request", http.StatusBadRequest)
			return
		}

		if req.ClientID != nil {
			s.cfg.SetClientID(*req.ClientID)
		}
		if req.MessageInterval != nil {
			s.cfg.SetMessageInterval(*req.MessageInterval)
		}

		jsonResponse(w, map[string]string{"status": "updated"})

	default:
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jsonResponse(w, s.manager.GetChannelStatus())

	case http.MethodPost:
		var req struct {
			Channel string `json:"channel"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "Invalid request", http.StatusBadRequest)
			return
		}
		if req.Channel == "" {
			httpError(w, "Channel name required", http.StatusBadRequest)
			return
		}

		if !s.cfg.IsConfigured() {
			httpError(w, "Bot not configured. Please set OAuth token and username first.", http.StatusBadRequest)
			return
		}

		if err := s.manager.JoinChannel(req.Channel); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		jsonResponse(w, map[string]string{"status": "joined", "channel": req.Channel})

	default:
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleChannelAction(w http.ResponseWriter, r *http.Request) {
	channel := strings.TrimPrefix(r.URL.Path, "/api/channels/")

	// Check for /reconnect suffix
	if strings.HasSuffix(channel, "/reconnect") {
		channel = strings.TrimSuffix(channel, "/reconnect")
		if r.Method == http.MethodPost {
			if err := s.manager.ReconnectChannel(channel); err != nil {
				httpError(w, fmt.Sprintf("Failed to reconnect: %v", err), http.StatusInternalServerError)
				return
			}
			jsonResponse(w, map[string]string{"status": "reconnected", "channel": channel})
			return
		}
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if channel == "" {
		httpError(w, "Channel name required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodDelete:
		s.manager.LeaveChannel(channel)
		jsonResponse(w, map[string]string{"status": "left", "channel": channel})

	default:
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleBrains(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	brains := s.manager.GetBrainManager().ListBrains()
	jsonResponse(w, brains)
}

func (s *Server) handleBrainAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/brains/")
	parts := strings.Split(path, "/")

	if len(parts) < 1 || parts[0] == "" {
		httpError(w, "Channel name required", http.StatusBadRequest)
		return
	}

	channel := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch r.Method {
	case http.MethodGet:
		if action == "stats" {
			brain := s.manager.GetBrainManager().GetBrain(channel)
			jsonResponse(w, brain.GetStats())
		} else if action == "transitions" {
			brain := s.manager.GetBrainManager().GetBrain(channel)
			search := r.URL.Query().Get("search")
			page := 1
			pageSize := 50
			if p := r.URL.Query().Get("page"); p != "" {
				fmt.Sscanf(p, "%d", &page)
			}
			if ps := r.URL.Query().Get("pageSize"); ps != "" {
				fmt.Sscanf(ps, "%d", &pageSize)
			}
			if page < 1 {
				page = 1
			}
			if pageSize < 1 || pageSize > 100 {
				pageSize = 50
			}
			jsonResponse(w, brain.GetTransitions(search, page, pageSize))
		} else {
			httpError(w, "Unknown action", http.StatusBadRequest)
		}

	case http.MethodPost:
		if action == "clean" {
			removed := s.manager.GetBrainManager().CleanBrain(channel)
			jsonResponse(w, map[string]int{"rows_removed": removed})
		} else {
			httpError(w, "Unknown action", http.StatusBadRequest)
		}

	case http.MethodDelete:
		if action == "transition" {
			var req struct {
				Word1    string `json:"word1"`
				Word2    string `json:"word2"`
				NextWord string `json:"next_word"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				httpError(w, "Invalid request", http.StatusBadRequest)
				return
			}
			brain := s.manager.GetBrainManager().GetBrain(channel)
			if err := brain.DeleteTransition(req.Word1, req.Word2, req.NextWord); err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			jsonResponse(w, map[string]string{"status": "deleted"})
		} else if action == "" {
			if err := s.manager.GetBrainManager().DeleteBrain(channel); err != nil {
				httpError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			jsonResponse(w, map[string]string{"status": "deleted", "channel": channel})
		} else {
			httpError(w, "Unknown action", http.StatusBadRequest)
		}

	default:
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleBlacklist(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jsonResponse(w, s.cfg.GetBlacklistedWords())

	case http.MethodPost:
		var req struct {
			Word string `json:"word"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "Invalid request", http.StatusBadRequest)
			return
		}
		if req.Word == "" {
			httpError(w, "Word required", http.StatusBadRequest)
			return
		}

		s.cfg.AddBlacklistedWord(req.Word)
		jsonResponse(w, map[string]string{"status": "added", "word": req.Word})

	case http.MethodDelete:
		// Clear all blacklisted words
		s.cfg.ClearBlacklist()
		jsonResponse(w, map[string]string{"status": "cleared"})

	default:
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleBlacklistAction(w http.ResponseWriter, r *http.Request) {
	word := strings.TrimPrefix(r.URL.Path, "/api/blacklist/")
	if word == "" {
		httpError(w, "Word required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodDelete:
		s.cfg.RemoveBlacklistedWord(word)
		jsonResponse(w, map[string]string{"status": "removed", "word": word})

	default:
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleUserBlacklist(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jsonResponse(w, s.cfg.GetBlacklistedUsers())

	case http.MethodPost:
		var req struct {
			Username string `json:"username"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "Invalid request", http.StatusBadRequest)
			return
		}
		if req.Username == "" {
			httpError(w, "Username required", http.StatusBadRequest)
			return
		}
		s.cfg.AddBlacklistedUser(req.Username)
		jsonResponse(w, map[string]string{"status": "added", "username": req.Username})

	default:
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleUserBlacklistAction(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimPrefix(r.URL.Path, "/api/userblacklist/")
	if username == "" {
		httpError(w, "Username required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodDelete:
		s.cfg.RemoveBlacklistedUser(username)
		jsonResponse(w, map[string]string{"status": "removed", "username": username})

	default:
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleDatabase(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		stats := s.manager.GetBrainManager().GetDatabaseStats()
		stats["data_directory"] = database.GetDataDir()
		jsonResponse(w, stats)

	case http.MethodPost:
		// Vacuum/optimize database
		db := database.GetDB()
		if _, err := db.Exec("VACUUM"); err != nil {
			httpError(w, "Failed to optimize database", http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]string{"status": "optimized"})

	case http.MethodDelete:
		// Clean all brains
		removed := s.manager.GetBrainManager().CleanAllBrains()
		jsonResponse(w, map[string]int{"rows_removed": removed})

	default:
		httpError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	s.mu.Lock()
	s.clients[conn] = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.clients, conn)
		s.mu.Unlock()
		conn.Close()
	}()

	// Keep connection alive and read messages
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

func (s *Server) broadcastEvent(event string, data interface{}) {
	msg := map[string]interface{}{
		"event": event,
		"data":  data,
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for client := range s.clients {
		if err := client.WriteJSON(msg); err != nil {
			client.Close()
			delete(s.clients, client)
		}
	}
}

func jsonResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func httpError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
