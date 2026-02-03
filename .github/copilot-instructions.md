# e_n_u_f 2.0 (Go) - Raspberry Pi

A multi-channel Twitch chat bot written in Go with Markov chain text generation and web UI.

## Project Structure
- `cmd/bot/` - Main application entry point
- `internal/markov/` - Markov chain text generation engine
- `internal/twitch/` - Twitch IRC client for multi-channel support
- `internal/web/` - Web UI server and API handlers
- `internal/config/` - Configuration management
- `web/` - Static web UI files (HTML, CSS, JS)

## Key Features
- Connect to multiple Twitch channels simultaneously
- Per-channel Markov chain brain files
- Banned words filtering
- Configurable message intervals
- Web-based management interface

## Development
- Go 1.21+
- Run: `go run ./cmd/bot`
- Build: `go build -o twitchbot ./cmd/bot`
- Cross-compile for Raspberry Pi: `GOOS=linux GOARCH=arm64 go build -o twitchbot ./cmd/bot`
