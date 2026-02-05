package main

import (
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"twitchbot/internal/config"
	"twitchbot/internal/database"
	"twitchbot/internal/twitch"
	"twitchbot/internal/web"
)

// getLocalIP returns the local IP address of the machine
func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "localhost"
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return "localhost"
}

func main() {
	log.Println("Starting e_n_u_f 2.0...")

	// Load configuration (initializes database)
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	defer database.Close()

	// Create and start the Twitch client manager
	manager := twitch.NewManager(cfg)

	// Only start connecting to channels if configured
	if cfg.IsConfigured() {
		if err := manager.Start(); err != nil {
			log.Printf("Warning: Failed to start Twitch manager: %v", err)
		}
	} else {
		log.Println("Bot not configured. Please configure via web UI.")
	}

	// Start web server
	webServer := web.NewServer(cfg, manager)
	go func() {
		if err := webServer.Start(); err != nil {
			log.Fatalf("Web server error: %v", err)
		}
	}()

	log.Printf("Web UI available at:")
	localIP := getLocalIP()
	log.Printf("  HTTPS: https://%s:%d", localIP, cfg.GetWebPort())
	log.Printf("  HTTP:  http://%s:%d", localIP, cfg.GetWebPort()+1)

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down...")
	manager.Stop()
	manager.GetBrainManager().Close()
	webServer.Stop()
}
