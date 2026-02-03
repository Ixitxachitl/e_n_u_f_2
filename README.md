# e_n_u_f 2.0 - Raspberry Pi

A multi-channel Twitch chat bot written in Go with Markov chain text generation, SQLite database, and web UI.

## Features

- **Multi-Channel Support**: Connect to multiple Twitch channels simultaneously
- **Markov Chain Generation**: Learn from chat and generate context-aware responses
- **SQLite Database**: All data stored in a single SQLite database file
- **Word Blacklist**: Filter out unwanted words from learning and responses
- **Web UI**: Browser-based management interface for configuration
- **Raspberry Pi Ready**: Optimized for ARM64 deployment

## Quick Start

### Prerequisites

- Go 1.21 or later (with CGO enabled for SQLite)
- Twitch account with OAuth token

### Running

```bash
# Run directly
go run ./cmd/bot

# Or build and run
go build -o twitchbot ./cmd/bot
./twitchbot
```

### Cross-Compile for Raspberry Pi

```bash
# Requires CGO cross-compilation setup
CGO_ENABLED=1 CC=aarch64-linux-gnu-gcc GOOS=linux GOARCH=arm64 go build -o twitchbot ./cmd/bot
```

## Configuration

All configuration is done via the Web UI at `http://localhost:24601`.

1. Go to the **Configuration** tab
2. Enter your bot username
3. Enter your OAuth token (get one from https://twitchapps.com/tmi/)
4. Save and restart the bot

Data is stored in `~/.twitchbot/twitchbot.db`

## Web Interface

Access the web UI at `http://localhost:24601` (default port).

### Features

- **Dashboard**: View bot status, connected channels, and activity log
- **Configuration**: Set OAuth token, username, and message interval
- **Channels**: Add/remove channels to connect to
- **Database**: View statistics, clean data, optimize database
- **Blacklist**: Manage word filter list

## Project Structure

```
├── cmd/bot/           # Main application entry point
├── internal/
│   ├── config/        # Configuration management (SQLite-backed)
│   ├── database/      # SQLite database initialization
│   ├── markov/        # Markov chain text generation
│   ├── twitch/        # Twitch IRC client
│   └── web/           # Web server and API
│       └── static/    # Embedded web UI files
└── data/              # Runtime data location (~/.twitchbot/)
```

## API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/status` | Bot status and stats |
| GET | `/api/config` | Get current config |
| PUT | `/api/config` | Update config |
| GET | `/api/channels` | List connected channels |
| POST | `/api/channels` | Join a channel |
| DELETE | `/api/channels/{name}` | Leave a channel |
| GET | `/api/brains` | List brain data per channel |
| GET | `/api/brains/{channel}/stats` | Brain statistics |
| POST | `/api/brains/{channel}/clean` | Clean blacklisted words |
| DELETE | `/api/brains/{channel}` | Delete brain data |
| GET | `/api/blacklist` | List blacklisted words |
| POST | `/api/blacklist` | Add blacklisted word |
| DELETE | `/api/blacklist/{word}` | Remove blacklisted word |
| GET | `/api/database` | Database statistics |
| POST | `/api/database` | Optimize (VACUUM) database |
| DELETE | `/api/database` | Clean all brains |

## Database Schema

The SQLite database contains:
- `config`: Key-value configuration storage
- `channels`: Channel list with message counts
- `blacklist`: Blacklisted words
- `markov_transitions`: Per-channel Markov chain data

## License

MIT
