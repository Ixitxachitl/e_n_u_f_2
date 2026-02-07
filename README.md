# e_n_u_f 2.0

A multi-channel Twitch chat bot written in Go with Markov chain text generation, per-channel SQLite databases, and a web-based management UI. Includes a Windows GUI launcher with system tray support.

## Features

### Core
- **Multi-Channel Support**: Connect to multiple Twitch channels simultaneously via TLS (port 6697)
- **Markov Chain Generation**: Learn from chat and generate context-aware responses
- **Per-Channel SQLite Databases**: Each channel has its own brain database in `~/.twitchbot/brains/`
- **Live-Only Mode**: Bot automatically joins when channels go live, leaves when offline
- **Per-Channel Message Intervals**: Each channel can have its own response frequency (1-100 messages)

### Authentication & Security
- **Admin Password Protection**: Web UI requires password authentication for remote access
- **Localhost Exception**: No password required when accessing from the same machine
- **Session Management**: Secure cookie-based sessions with 24-hour expiry
- **Twitch OAuth Integration**: Secure login via Twitch OAuth flow
- **HTTPS Support**: Self-signed certificate generation for secure OAuth callbacks
- **Word & User Blacklists**: Filter unwanted words and ignore specific users
- **Link Filtering**: Automatically skip messages containing URLs
- **Emoji Support**: Emoji are preserved in Markov chains while filtering non-English text
- **Loop Prevention**: Detects and prevents repetitive transitions (word1 == word2 == nextWord)
- **Bot Channel Isolation**: Bot's own channel doesn't learn or generate messages

### Web Interface
- **Modern Dark Theme**: Clean, responsive web UI
- **Real-Time Updates**: WebSocket connection for live activity feed
- **Auto-Refresh**: Dashboard updates every 5 seconds
- **Live Channel Dashboard**: View currently live channels with stream info, viewer count, and countdown
- **Profile Images**: Channel avatars displayed throughout the UI
- **Database Editor**: Browse and edit Markov transitions with pagination and search
- **Generation Logging**: Activity feed shows generation attempts with success/failure status

### Public Quotes Page
- **Quotes API**: Public endpoint to view all bot-generated messages
- **Search & Filter**: Search quotes by text, filter by channel
- **Sorting Options**: Sort by newest, oldest, or most +1s
- **Live Updates**: New quotes appear in real-time via WebSocket
- **Bot Stats**: Display channel count, total transitions, and quote count
- **Channel Display Names**: Shows proper Twitch display names (e.g., "xQc" not "xqc")
- **+1 Voting**: Users can upvote favorite quotes with Twitch login
- **Remote Hosting**: Can be hosted externally with API_BASE configuration

### Windows Launcher
- **Embedded Browser**: Native Windows app with embedded WebView2 browser
- **System Tray**: Minimize to system tray, restore with tray menu
- **Custom Icon**: Application icon in exe, window title bar, and tray
- **Single Package**: Just two files to distribute (e_n_u_f.exe + twitchbot.exe)

### Deployment
- **Pure Go Build**: No CGO dependencies, easy cross-compilation
- **Raspberry Pi Ready**: ARM64 build target with systemd service file
- **User ID Tracking**: Detects username changes and migrates brain data automatically

## Quick Start

### Prerequisites

- Go 1.21 or later
- Twitch account for the bot
- Twitch Developer Application (for OAuth)

### Running

```bash
# Run directly
go run ./cmd/bot

# Or build and run
go build -o twitchbot ./cmd/bot
./twitchbot
```

Access the web UI at `https://localhost:24601`

### Windows Launcher

```powershell
# Build the bot
go build -o twitchbot.exe ./cmd/bot

# Build the launcher (requires icon.ico in cmd/launcher/)
go build -ldflags "-H=windowsgui" -o e_n_u_f.exe ./cmd/launcher

# Run the launcher (starts bot automatically)
.\e_n_u_f.exe
```

### Cross-Compile for Raspberry Pi

```bash
# Pure Go - no CGO or cross-compiler needed!
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o twitchbot-linux-arm64 ./cmd/bot
```

## Raspberry Pi Deployment

1. **Build the ARM64 binary** (on your dev machine):
   ```bash
   GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o twitchbot ./cmd/bot
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

5. **Access the Web UI** at `https://<pi-ip>:24601`

### Service Commands

```bash
sudo systemctl start twitchbot    # Start the bot
sudo systemctl stop twitchbot     # Stop the bot
sudo systemctl restart twitchbot  # Restart the bot
sudo systemctl status twitchbot   # Check status
journalctl -u twitchbot -f        # View logs
```

## Configuration

All configuration is done via the Web UI at `https://localhost:24601`.

1. Create a Twitch application at https://dev.twitch.tv/console/apps
2. Set the redirect URL to `https://localhost:24601/auth/callback`
3. Go to the **Configuration** tab in the web UI
4. Enter your Twitch Application Client ID
5. Click "Login with Twitch" to authenticate

### Data Storage

- Main database: `~/.twitchbot/twitchbot.db` (config, channels, blacklists, user mappings, quotes)
- Per-channel brains: `~/.twitchbot/brains/<channel>.db`
- TLS certificates: `~/.twitchbot/cert.pem`, `~/.twitchbot/key.pem`

### Public Access (Optional)

To expose the quotes page publicly with a valid SSL certificate:

1. Set up DuckDNS or similar dynamic DNS pointing to your IP
2. Forward port 24601 on your router
3. Use certbot to get Let's Encrypt certificates:
   ```bash
   sudo certbot certonly --manual --preferred-challenges dns -d yourdomain.duckdns.org
   ```
4. Copy certificates to `~/.twitchbot/cert.pem` and `~/.twitchbot/key.pem`
5. Add your public URL to Twitch OAuth Redirect URLs for voting support

## Chat Commands

| Command | Where | Description |
|---------|-------|-------------|
| `!join` | Bot's channel | Add bot to your channel |
| `!leave` | Bot's channel | Remove bot from your channel |
| `!response` | Bot's channel | Show current message interval for your channel |
| `!response <1-100>` | Bot's channel | Set message interval for your channel |
| `!global` | Bot's channel | Use all channel brains for generating responses |
| `!local` | Bot's channel | Use only your channel's brain for generating (default) |
| `!ignoreme` | Any channel | Opt-out of bot learning from your messages |
| `!listentome` | Any channel | Opt back in to bot learning |

## Project Structure

```
├── cmd/
│   ├── bot/           # Main bot application
│   └── launcher/      # Windows GUI launcher
├── internal/
│   ├── config/        # Configuration management (SQLite-backed)
│   ├── database/      # SQLite database initialization
│   ├── markov/        # Markov chain text generation
│   ├── twitch/        # Twitch IRC client and channel manager
│   └── web/           # Web server, API, and static files
├── deploy/            # Raspberry Pi deployment scripts
└── data/              # Runtime data location (~/.twitchbot/)
```

## API Endpoints

### Authentication (no auth required)
| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/auth/status` | Check auth status (needs setup, authenticated, localhost) |
| POST | `/api/auth/setup` | Set admin password (first time only) |
| POST | `/api/auth/login` | Login with password |
| POST | `/api/auth/logout` | Logout and clear session |

### Protected Endpoints (require auth or localhost)
| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/status` | Bot status and stats |
| GET | `/api/config` | Get current config |
| PUT | `/api/config` | Update config |
| POST | `/api/logout` | Clear OAuth token |
| GET | `/api/channels` | List configured channels |
| POST | `/api/channels` | Join a channel |
| DELETE | `/api/channels/{name}` | Leave/remove a channel |
| POST | `/api/channels/{name}/reconnect` | Reconnect to a channel |
| PUT | `/api/channels/{name}/interval` | Set channel message interval |
| GET | `/api/live` | Get currently live channels |
| GET | `/api/brains` | List brain data per channel |
| GET | `/api/brains/{channel}/stats` | Brain statistics |
| GET | `/api/brains/{channel}/transitions` | Get paginated transitions |
| POST | `/api/brains/{channel}/clean` | Clean blacklisted words |
| DELETE | `/api/brains/{channel}` | Delete brain data |
| DELETE | `/api/brains/{channel}/transition` | Delete specific transition |
| PUT | `/api/brains/{channel}/transition` | Update transition count |
| GET | `/api/blacklist` | List blacklisted words |
| POST | `/api/blacklist` | Add blacklisted word |
| DELETE | `/api/blacklist/{word}` | Remove blacklisted word |
| DELETE | `/api/blacklist` | Clear all blacklisted words |
| GET | `/api/userblacklist` | List ignored users |
| POST | `/api/userblacklist` | Add ignored user |
| DELETE | `/api/userblacklist/{user}` | Remove ignored user |
| GET | `/api/database` | Database statistics |
| POST | `/api/database` | Optimize (VACUUM) database |
| DELETE | `/api/database` | Clean all brains |
| GET | `/api/activity` | Get recent activity log |

### Public Endpoints (no auth required)
| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/quotes` | List quotes with search, filter, sort, pagination |
| POST | `/api/quotes/{id}/vote` | Add +1 vote (requires Twitch user ID) |
| DELETE | `/api/quotes/{id}/vote` | Remove +1 vote |
| GET | `/api/public/client-id` | Get OAuth client ID for Twitch login |
| WS | `/ws/public` | WebSocket for live quote updates |

## Database Schema

### Main Database (`twitchbot.db`)
- `config`: Key-value configuration storage
- `channels`: Channel list with message counts, intervals, display names, and timestamps
- `blacklist`: Blacklisted words
- `user_blacklist`: Ignored users
- `twitch_users`: User ID to username mappings (for detecting name changes)
- `quotes`: Bot-generated messages log
- `quote_votes`: +1 votes on quotes (linked to Twitch user IDs)

### Per-Channel Databases (`brains/<channel>.db`)
- `transitions`: Markov chain word transitions (word1, word2, next_word, count)

## Ports

- **24601** (HTTPS): Main web UI and OAuth callbacks
- **24602** (HTTP): Used by Windows launcher embedded browser (no cert warnings)

## License

GNU General Public License v2.0 - See [LICENSE](LICENSE) for details.

## Downloads

Pre-built executables are available on the [Releases](https://github.com/Ixitxachitl/e_n_u_f_2/releases) page:
- **Windows**: `e_n_u_f.exe` (launcher) + `twitchbot.exe` (bot)
- **Raspberry Pi / Linux ARM64**: `twitchbot-linux-arm64`

> **Note**: For Windows, both `e_n_u_f.exe` and `twitchbot.exe` must be in the same directory for the launcher to work.

## Links

- **Source Code**: https://github.com/Ixitxachitl/e_n_u_f_2
- **Releases**: https://github.com/Ixitxachitl/e_n_u_f_2/releases
- **Author**: @ixitxachitl
