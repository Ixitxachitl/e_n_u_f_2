package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"twitchbot/internal/config"
	"twitchbot/internal/database"
	"twitchbot/internal/twitch"
	"twitchbot/internal/web"
)

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
	log.Printf("  HTTPS: https://localhost:%d", cfg.GetWebPort())
	log.Printf("  HTTP:  http://localhost:%d", cfg.GetWebPort()+1)

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down...")
	manager.Stop()
	manager.GetBrainManager().Close()
	webServer.Stop()
}
