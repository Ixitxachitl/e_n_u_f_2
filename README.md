# e_n_u_f 2.0

A multi-channel Twitch chat bot written in Go with Markov chain text generation, per-channel SQLite databases, and a web-based management UI.

## Features

- **Multi-Channel Support**: Connect to multiple Twitch channels simultaneously via TLS (port 6697)
- **Markov Chain Generation**: Learn from chat and generate context-aware responses
- **Per-Channel SQLite Databases**: Each channel has its own brain database in `~/.twitchbot/brains/`
- **Twitch OAuth Authentication**: Secure login via Twitch OAuth flow
- **Word & User Blacklists**: Filter unwanted words and ignore specific users
- **Link Filtering**: Automatically skip messages containing URLs
- **Web UI**: Browser-based management with auto-refresh
- **Database Editor**: Browse and edit Markov transitions via web interface
- **Reconnect & Retry**: Automatic reconnection with exponential backoff
- **Chat Commands**: `!join`, `!leave`, `!ignoreme`, `!listentome`
- **Raspberry Pi Ready**: Cross-compile for ARM64 deployment

## Quick Start

### Prerequisites

- Go 1.21 or later
- Twitch account for the bot

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
# Pure Go - no CGO or cross-compiler needed!
GOOS=linux GOARCH=arm64 go build -o twitchbot-linux-arm64 ./cmd/bot
```

## Raspberry Pi Deployment

1. **Build the ARM64 binary** (on your dev machine):
   ```bash
   GOOS=linux GOARCH=arm64 go build -o twitchbot ./cmd/bot
   ```

2. **Copy files to your Pi**:
   ```bash
   scp twitchbot deploy/twitchbot.service deploy/deploy.sh pi@<pi-ip>:~/
   ```

3. **Run the deploy script on your Pi**:
   ```bash
   ssh pi@<pi-ip>
   chmod +x deploy.sh
   sudo ./deploy.sh
   ```

4. **Start the bot**:
   ```bash
   sudo systemctl start twitchbot
   ```

5. **Access the Web UI** at `http://<pi-ip>:24601`

### Service Commands

```bash
sudo systemctl start twitchbot    # Start the bot
sudo systemctl stop twitchbot     # Stop the bot
sudo systemctl restart twitchbot  # Restart the bot
sudo systemctl status twitchbot   # Check status
journalctl -u twitchbot -f        # View logs
```

## Configuration

All configuration is done via the Web UI at `http://localhost:24601`.

1. Create a Twitch application at https://dev.twitch.tv/console/apps
2. Set the redirect URL to `http://localhost:24601/auth/callback`
3. Go to the **Configuration** tab in the web UI
4. Enter your Twitch Application Client ID
5. Click "Login with Twitch" to authenticate

### Data Storage

- Main database: `~/.twitchbot/twitchbot.db` (config, channels, blacklists)
- Per-channel brains: `~/.twitchbot/brains/<channel>.db`

## Web Interface

Access the web UI at `http://localhost:24601` (default port).

### Features

- **Dashboard**: View bot status, connected channels, and activity log
- **Configuration**: Twitch OAuth login and message interval settings
- **Channels**: Add/remove channels, reconnect disconnected channels
- **Database**: View stats, browse/edit Markov transitions, optimize database
- **Blacklist**: Manage word filters and ignored users

### Chat Commands

- `!join <channel>` - Join a channel (bot's channel only)
- `!leave <channel>` - Leave a channel (bot's channel only)
- `!ignoreme` - Add yourself to the ignored users list (any channel)
- `!listentome` - Remove yourself from the ignored users list (any channel)

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
| POST | `/api/logout` | Clear OAuth token |
| GET | `/api/channels` | List connected channels |
| POST | `/api/channels` | Join a channel |
| DELETE | `/api/channels/{name}` | Leave a channel |
| POST | `/api/channels/{name}/reconnect` | Reconnect to a channel |
| GET | `/api/brains` | List brain data per channel |
| GET | `/api/brains/{channel}/stats` | Brain statistics |
| GET | `/api/brains/{channel}/transitions` | Get paginated transitions |
| POST | `/api/brains/{channel}/clean` | Clean blacklisted words |
| DELETE | `/api/brains/{channel}` | Delete brain data |
| DELETE | `/api/brains/{channel}/transition` | Delete specific transition |
| GET | `/api/blacklist` | List blacklisted words |
| POST | `/api/blacklist` | Add blacklisted word |
| DELETE | `/api/blacklist` | Clear all blacklisted words |
| GET | `/api/userblacklist` | List ignored users |
| POST | `/api/userblacklist` | Add ignored user |
| DELETE | `/api/userblacklist/{user}` | Remove ignored user |
| GET | `/api/database` | Database statistics |
| POST | `/api/database` | Optimize (VACUUM) database |
| DELETE | `/api/database` | Clean all brains |

## Database Schema

### Main Database (`twitchbot.db`)
- `config`: Key-value configuration storage
- `channels`: Channel list with message counts and last response times
- `blacklist`: Blacklisted words
- `user_blacklist`: Ignored users

### Per-Channel Databases (`brains/<channel>.db`)
- `transitions`: Markov chain word transitions (word1, word2, next_word, count)

## License

GNU General Public License v2.0 - See [LICENSE](LICENSE) for details.
