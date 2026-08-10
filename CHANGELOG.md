# e_n_u_f 2.0 Release Notes

## Version 2.2.7 - August 2026

### 🔐 Authentication Overhaul

- **Device Code Flow Login**: Replaced the old OAuth redirect flow — just enter a short code on Twitch's site, no redirect URL or copy-pasted tokens needed
- **Automatic Token Refresh**: Optionally store a Client Secret so the bot refreshes its OAuth token in the background before it expires, instead of requiring a manual re-login every ~4 hours
- **bcrypt Password Hashing**: Admin password storage upgraded from salted SHA-256 to bcrypt; existing passwords are verified and transparently upgraded on next login, no action needed
- **Admin Logout Button**: Added a "Log Out" button to end the admin web session on demand

### ⏱️ Timeout Awareness

- **Bot Timeout Detection**: The bot now detects when Twitch times it out in a channel and suppresses message generation until the timeout expires, instead of silently failing to send
- **Auto-Resume**: Detects when a moderator lifts a timeout early and resumes message generation immediately, with an activity log / WebSocket event either way

### 🔄 Connection Reliability

- **Context-Based Shutdown**: Connection manager now cancels in-flight dials and reconnect backoffs via a shared context on shutdown, instead of leaking goroutines
- **Reconnect Fixes**: Only reconnects clients still registered to a channel, preventing duplicate/competing reconnect loops when multiple channels drop at once
- **Deeper Username-Change Detection**: Reverse-checks stored Twitch user IDs against current usernames so renames are caught even for channels that are currently offline

### 🧠 Brain Performance

- **Stats Caching**: Brain and channel-list stats are now cached (with automatic invalidation on changes) instead of re-querying SQLite on every dashboard refresh

### 🗄️ Database Cleanup UI

- **Live Progress Bar**: "Clean & Optimize All" now shows real-time progress (channel X of N) over WebSocket while it works through every brain, instead of appearing to hang for large installs

### 🐛 Bug Fixes

- **Fixed "Erase Brain"**: The erase-brain action always failed with a database error due to stale table names left over from a schema rename
- **Fixed a Generation Data Race**: Global-brain generation could corrupt its random output — or hit a closed database — if a channel disconnected while a message was being generated from multiple brains at once
- **Fixed a Possible Crash**: Viewing brain stats/transitions for a channel whose brain database failed to load could crash that request with a nil pointer
- **Fixed a Launcher Edge Case**: Error messages containing a single quote could break out of the desktop launcher's PowerShell error dialog

---

## Version 2.2.5 - April 2026

### ⏱️ Inactivity Timer

- **Per-Channel Inactivity Timer**: Automatically generate a message after chat has been silent for a configurable duration (1–60 minutes)
- **Default Timer Settings**: Set default timer enabled/disabled and duration for new channels in Configuration
- **Chat Command Toggle**: Streamers can enable/disable timers and set duration via `!timer` command
- **Web UI Controls**: Timer toggle and slider per channel in the Channels tab

### 🛡️ Followers-Only Mode Handling

- **Auto-Leave on Followers-Only**: Bot detects followers-only mode and automatically leaves the channel
- **Whisper Notification**: Sends a whisper to the streamer explaining why the bot left, with instructions to `!join` again

### 🔧 Configuration Enhancements

- **Command Toggles**: Enable/disable `!global`/`!local`, `!response`, and `!timer` commands from the web UI
- **Default Brain Mode**: Set whether new channels default to local or global brain mode
- **Extended Intervals**: Channel message intervals now support 1–1000 (up from 1–100)
- **Longer Sessions**: Web UI session duration extended to 30 days

### 🧠 Brain Improvements

- **Trigram & Bigram Phrase Matching**: Blacklist cleaning now supports multi-word phrase matching in transitions
- **IRC Tag Unescaping**: Properly handles Twitch IRC tag escape sequences in messages before learning
- **Username Sanitization**: Cleans display names with special characters for consistent brain data

### 🐛 Bug Fixes

- **Dynamic Bot Name**: Generation events in the activity log now use the logged-in bot's username instead of a hardcoded value (both server-side and client-side)
- **Graceful Shutdown Logging**: Improved error handling and logging during server shutdown
- **Database Connection Optimization**: Better connection handling for SQLite databases

---

## Version 2.2.3 - February 2026

### 🔄 Reliability

- **Bot Channel Auto-Reconnect**: If the bot disconnects from its own channel, it now retries indefinitely with exponential backoff (5s → 5min cap) until reconnected
- **Smart !join**: `!join` now checks if the user is live before connecting — if offline, the channel is added to config and the bot will join automatically when they go live
- **User ID Pre-Storage**: `!join` now looks up and stores the user's Twitch ID immediately, so the live monitor can track them by ID from the start

### 🔧 Bug Fixes

- **Blacklist Punctuation Stripping**: Word matching in blacklist checks now strips surrounding punctuation for more accurate filtering

### 🎨 UI Improvements

- **First/Last Page Navigation**: All paginated tabs (Channels, Database, Quotes, Public Quotes) now have First (⇤) and Last (⇥) buttons for quick navigation

---

## Version 2.2.2 - February 2026

### 🔧 Bug Fixes

- **Activity Log Highlighting**: Bot messages now stay highlighted after page refresh (uses dynamic bot username instead of hardcoded value)

### 🎨 UI Improvements

- **First/Last Page Navigation**: All paginated tabs (Channels, Database, Quotes, Public Quotes) now have First (⇤) and Last (⇥) buttons for quick navigation
- **Quotes Total Count**: Admin Quotes pagination now shows total item count matching other tabs

---

## Version 2.2.1 - February 2026

### 😀 Emoji Support

- **Emoji Preservation**: Emoji are now allowed through filters and preserved in Markov chains
- **Smart Quote Normalization**: Curly quotes (`'` `'` `"` `"`) auto-convert to ASCII before learning
- **Extended Unicode Support**: Properly handles emoji ranges while filtering non-English text

### 🔄 Loop Prevention

- **Loop Detection**: Prevents infinite loops by skipping transitions where word1 == word2 == nextWord
- **Loop Cleanup**: "Clean Non-ASCII" optimization now removes existing loop transitions
- **Learning Filter**: New messages with repetitive patterns are not learned

### 🎨 Admin UI Improvements

- **Quotes Auto-Update**: Admin quotes list refreshes automatically when new quotes arrive
- **Styled Channel Picker**: Filter dropdowns now match the dark theme styling
- **WebSocket Sync**: Real-time updates for both public and admin quote pages

---

## Version 2.2.0 - February 2026

### 📜 Public Quotes Page

- **Quotes API**: Public API endpoint to view all bot-generated messages
- **Search & Filter**: Search quotes by text, filter by channel
- **Sorting Options**: Sort by newest, oldest, or most +1s
- **Pagination**: Browse through quotes with page navigation
- **Dynamic Bot Name**: Page title shows bot's username

### 👍 Quote Voting System

- **+1 Votes**: Users can upvote their favorite quotes
- **Twitch OAuth Login**: Authenticate with Twitch to vote (no special permissions required)
- **Vote Tracking**: See which quotes you've already voted on
- **Public Client ID Endpoint**: Enables OAuth from externally hosted pages

### 📊 Generation Event Logging

- **Activity Dashboard**: Generation attempts now appear in the activity feed
- **Success/Failure Tracking**: See when generation succeeds or fails with reasons
- **Channel Context**: Each generation event shows which channel triggered it

### 🔐 SSL/HTTPS Improvements

- **Let's Encrypt Support**: Use real SSL certificates for public access
- **Configurable Cert Paths**: Certificates loaded from `~/.twitchbot/` directory
- **DuckDNS Compatible**: Works with dynamic DNS for home hosting

---

## Version 2.0 - February 2026

### 🌐 Global Brain Mode

- **Per-Channel Brain Toggle**: Choose between local (channel-only) or global (all brains) for message generation
- **!global / !local Commands**: Streamers can switch brain modes via chat
- **Web UI Toggle**: Visual switch in Channels tab to toggle brain mode
- Learning always stays local to the channel's brain

### 🔐 Web UI Password Protection

- **Password Setup Flow**: First-time setup prompts for admin password via web UI
- **Session-Based Auth**: Secure cookie-based sessions with 24-hour expiry
- **Localhost Exception**: No password required when accessing from localhost (for Windows launcher)
- **Change Password**: Security settings card in Configuration tab

### 🛡️ Improved Content Filtering

- **Hybrid Blacklist Matching**: Single words use exact match, multi-word phrases use substring match
- **Editable Transition Counts**: Database editor now allows editing transition counts directly

---

## Version 2.0.0 - February 2026

A complete rewrite of the Twitch Markov chain chat bot in Go, designed for Raspberry Pi deployment.

### 🚀 Core Features

- **Markov Chain Text Generation**: Learns from chat messages and generates contextual responses
- **Multi-Channel Support**: Connect to multiple Twitch channels simultaneously
- **Per-Channel Brain Files**: Each channel has its own SQLite database for isolated learning
- **Live-Only Mode**: Bot automatically joins channels when they go live and leaves when offline
- **Web-Based Management UI**: Configure and monitor everything through a modern dark-themed dashboard

### 🔐 Authentication

- **Twitch OAuth Integration**: Secure login via Twitch OAuth flow
- **HTTPS Support**: Self-signed certificate generation for secure OAuth callbacks
- **Persistent Sessions**: OAuth tokens stored securely in SQLite database

### 📺 Channel Management

- **!join / !leave Commands**: Streamers can add or remove the bot via chat commands in the bot's channel
- **Per-Channel Message Intervals**: Each channel can have its own response frequency (1-100 messages)
- **!response Command**: Streamers can set their channel's interval directly from chat
- **Live Channel Dashboard**: View currently live channels with stream info, viewer count, and countdown to next message
- **Profile Images**: Channel avatars displayed throughout the UI

### 🛡️ Content Filtering

- **Banned Words List**: Global word blacklist to prevent learning inappropriate content
- **Link Filtering**: Automatically skips messages containing URLs
- **User Blacklist**: Users can opt-out with `!ignoreme` / opt back in with `!listentome`
- **Bot Channel Isolation**: Bot's own channel doesn't learn or generate messages

### 🗄️ Database Features

- **SQLite Storage**: Pure Go SQLite (no CGO) for easy cross-compilation
- **Database Browser**: View and edit Markov chain transitions with pagination and live search
- **Database Optimization**: Vacuum and optimize databases from the UI
- **User ID Tracking**: Detects username changes and migrates brain data automatically

### 🖥️ Web UI Features

- **Real-Time Updates**: WebSocket connection for live activity feed
- **Auto-Refresh**: Dashboard updates every 5 seconds
- **Tab Persistence**: Remembers your last active tab
- **Connection Status**: Visual indicators for bot connection state
- **Reconnect Button**: Manual reconnection with exponential backoff

### 🛠️ Technical Improvements

- **TLS Connection**: Secure IRC connection on port 6697
- **Pure Go Build**: No CGO dependencies, easy cross-compilation
- **Raspberry Pi Ready**: ARM64 build target with systemd service file
- **Graceful Shutdown**: Proper cleanup on SIGINT/SIGTERM
- **Configurable Polling**: 60-second live status check interval

### 📦 Deployment

- **systemd Service**: Ready-to-use service file for Linux deployment
- **Deploy Script**: Automated deployment to Raspberry Pi via SSH
- **Cross-Compilation**: Single command to build for ARM64

---

## Chat Commands

| Command | Where | Description |
|---------|-------|-------------|
| `!join` | Bot's channel | Add bot to your channel |
| `!leave` | Bot's channel | Remove bot from your channel |
| `!response` | Bot's channel | Show current message interval for your channel |
| `!response <1-100>` | Bot's channel | Set message interval for your channel |
| `!global` | Bot's channel | Use all channel brains for message generation |
| `!local` | Bot's channel | Use only your channel's brain (default) |
| `!ignoreme` | Any channel | Opt-out of bot learning from your messages |
| `!listentome` | Any channel | Opt back in to bot learning |

---

## System Requirements

- Go 1.21+ (for building)
- Raspberry Pi (ARM64) or any Linux server
- Twitch Developer Application (for OAuth)

---

## Links

- **Source Code**: https://github.com/Ixitxachitl/e_n_u_f_2
- **License**: GPL-2.0
- **Author**: @ixitxachitl
