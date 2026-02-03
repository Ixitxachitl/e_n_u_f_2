// API Helper
const api = {
    async get(url) {
        const res = await fetch(url);
        return res.json();
    },
    async post(url, data) {
        const res = await fetch(url, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(data)
        });
        return res.json();
    },
    async put(url, data) {
        const res = await fetch(url, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(data)
        });
        return res.json();
    },
    async delete(url) {
        const res = await fetch(url, { method: 'DELETE' });
        return res.json();
    }
};

// State
let ws = null;
const activityLog = [];
const MAX_LOG_ENTRIES = 100;

// DOM Elements
const elements = {};

// Auto-refresh interval
let refreshInterval = null;

// Initialize
document.addEventListener('DOMContentLoaded', () => {
    cacheElements();
    setupTabs();
    restoreSavedTab();
    setupEventListeners();
    connectWebSocket();
    loadInitialData();
    startAutoRefresh();
});

// Start auto-refresh every 5 seconds
function startAutoRefresh() {
    if (refreshInterval) clearInterval(refreshInterval);
    refreshInterval = setInterval(() => {
        loadStatus();
        loadChannels();
        loadLiveChannels();
        loadDatabaseStats();
    }, 5000);
}

// Restore the last active tab from localStorage
function restoreSavedTab() {
    const savedTab = localStorage.getItem('activeTab');
    if (savedTab) {
        const btn = document.querySelector(`.tab-btn[data-tab="${savedTab}"]`);
        if (btn) {
            document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
            document.querySelectorAll('.tab-content').forEach(c => c.classList.remove('active'));
            btn.classList.add('active');
            document.getElementById(savedTab).classList.add('active');
        }
    }
}

function cacheElements() {
    elements.statusIndicator = document.getElementById('status-indicator');
    elements.configStatus = document.getElementById('config-status');
    elements.channelCount = document.getElementById('channel-count');
    elements.transitionCount = document.getElementById('transition-count');
    elements.channelList = document.getElementById('channel-list');
    elements.channelsList = document.getElementById('channels-list');
    elements.brainsList = document.getElementById('brains-list');
    elements.blacklistWords = document.getElementById('blacklist-words');
    elements.ignoredUsers = document.getElementById('ignored-users');
    elements.activityLog = document.getElementById('activity-log');
    elements.intervalSlider = document.getElementById('interval-slider');
    elements.intervalValue = document.getElementById('interval-value');
    elements.newChannel = document.getElementById('new-channel');
    elements.newBlacklistWord = document.getElementById('new-blacklist-word');
    elements.newIgnoredUser = document.getElementById('new-ignored-user');
    elements.clientId = document.getElementById('client-id');
    elements.loggedOutView = document.getElementById('logged-out-view');
    elements.loggedInView = document.getElementById('logged-in-view');
    elements.loggedUsername = document.getElementById('logged-username');
    elements.twitchLoginBtn = document.getElementById('twitch-login-btn');
    elements.saveClientIdBtn = document.getElementById('save-client-id-btn');
    elements.redirectUrl = document.getElementById('redirect-url');
    elements.dbTransitions = document.getElementById('db-transitions');
    elements.dbChannels = document.getElementById('db-channels');
    elements.dbBlacklisted = document.getElementById('db-blacklisted');
    elements.dbDirectory = document.getElementById('db-directory');
}

// Tab Navigation
function setupTabs() {
    document.querySelectorAll('.tab-btn').forEach(btn => {
        btn.addEventListener('click', () => {
            document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
            document.querySelectorAll('.tab-content').forEach(c => c.classList.remove('active'));
            
            btn.classList.add('active');
            document.getElementById(btn.dataset.tab).classList.add('active');
            
            // Save active tab to localStorage
            localStorage.setItem('activeTab', btn.dataset.tab);
        });
    });
}

// Event Listeners
function setupEventListeners() {
    // Add channel
    document.getElementById('add-channel-btn').addEventListener('click', addChannel);
    elements.newChannel.addEventListener('keypress', e => {
        if (e.key === 'Enter') addChannel();
    });

    // OAuth login
    elements.saveClientIdBtn.addEventListener('click', saveClientId);
    elements.twitchLoginBtn.addEventListener('click', () => {
        window.location.href = '/auth/twitch';
    });
    document.getElementById('logout-btn').addEventListener('click', logout);
    
    // Client ID input enables/disables login button
    elements.clientId.addEventListener('input', updateLoginButtonState);

    // Interval slider
    elements.intervalSlider.addEventListener('input', () => {
        elements.intervalValue.textContent = elements.intervalSlider.value;
    });
    document.getElementById('save-interval-btn').addEventListener('click', saveInterval);

    // Blacklist
    document.getElementById('add-blacklist-btn').addEventListener('click', addBlacklistWord);
    elements.newBlacklistWord.addEventListener('keypress', e => {
        if (e.key === 'Enter') addBlacklistWord();
    });

    // Ignored users
    document.getElementById('add-ignored-user-btn').addEventListener('click', addIgnoredUser);
    elements.newIgnoredUser.addEventListener('keypress', e => {
        if (e.key === 'Enter') addIgnoredUser();
    });

    // Database
    document.getElementById('optimize-db-btn').addEventListener('click', optimizeDatabase);
    document.getElementById('clean-all-btn').addEventListener('click', cleanAllBrains);
    
    // Brain editor
    document.getElementById('transition-search').addEventListener('keypress', e => {
        if (e.key === 'Enter') searchTransitions();
    });
}

// WebSocket
function connectWebSocket() {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(`${protocol}//${window.location.host}/ws`);

    ws.onopen = () => {
        elements.statusIndicator.textContent = 'Connected';
        elements.statusIndicator.className = 'status-badge connected';
    };

    ws.onclose = () => {
        elements.statusIndicator.textContent = 'Disconnected';
        elements.statusIndicator.className = 'status-badge disconnected';
        setTimeout(connectWebSocket, 3000);
    };

    ws.onmessage = (event) => {
        const data = JSON.parse(event.data);
        handleWebSocketEvent(data);
    };
}

function handleWebSocketEvent(data) {
    if (data.event === 'message') {
        addLogEntry(data.data.channel, data.data.username, data.data.message);
    } else if (data.event === 'connect' || data.event === 'disconnect') {
        loadChannels();
        loadStatus();
    }
}

// Data Loading
async function loadInitialData() {
    await Promise.all([
        loadStatus(),
        loadConfig(),
        loadChannels(),
        loadLiveChannels(),
        loadBrains(),
        loadBlacklist(),
        loadIgnoredUsers(),
        loadDatabaseStats()
    ]);
}

async function loadStatus() {
    const status = await api.get('/api/status');
    
    if (status.configured) {
        elements.configStatus.textContent = 'Configured';
        elements.configStatus.style.color = 'var(--success)';
    } else {
        elements.configStatus.textContent = 'Not Configured';
        elements.configStatus.style.color = 'var(--warning)';
        elements.statusIndicator.textContent = 'Setup Required';
        elements.statusIndicator.className = 'status-badge warning';
    }
    
    elements.channelCount.textContent = status.channels ? status.channels.length : 0;
    elements.transitionCount.textContent = status.database ? status.database.total_transitions.toLocaleString() : 0;
}

async function loadConfig() {
    const config = await api.get('/api/config');
    elements.intervalSlider.value = config.message_interval;
    elements.intervalValue.textContent = config.message_interval;
    
    // Set redirect URL based on current location
    const redirectUrl = `${window.location.origin}/auth/callback`;
    elements.redirectUrl.textContent = redirectUrl;
    
    // Handle login state
    if (config.oauth_token_set && config.bot_username) {
        elements.loggedOutView.style.display = 'none';
        elements.loggedInView.style.display = 'block';
        elements.loggedUsername.textContent = config.bot_username;
    } else {
        elements.loggedOutView.style.display = 'block';
        elements.loggedInView.style.display = 'none';
    }
    
    // Update login button state
    if (config.client_id_set) {
        elements.twitchLoginBtn.disabled = false;
    }
    
    updateLoginButtonState();
}

function updateLoginButtonState() {
    const hasClientId = elements.clientId.value.trim().length > 0;
    elements.twitchLoginBtn.disabled = !hasClientId;
}

async function saveClientId() {
    const clientId = elements.clientId.value.trim();
    if (!clientId) {
        alert('Please enter a Client ID');
        return;
    }
    
    await api.put('/api/config', { client_id: clientId });
    elements.twitchLoginBtn.disabled = false;
    alert('Client ID saved! You can now login with Twitch.');
}

async function logout() {
    await api.post('/api/logout', {});
    elements.loggedOutView.style.display = 'block';
    elements.loggedInView.style.display = 'none';
    loadStatus();
}

async function loadChannels() {
    const channels = await api.get('/api/channels');
    renderChannels(channels);
}

async function loadLiveChannels() {
    const liveChannels = await api.get('/api/live');
    renderLiveChannels(liveChannels);
}

async function loadBrains() {
    const brains = await api.get('/api/brains');
    renderBrains(brains);
}

async function loadBlacklist() {
    const words = await api.get('/api/blacklist');
    renderBlacklist(words);
}

async function loadIgnoredUsers() {
    const users = await api.get('/api/userblacklist');
    renderIgnoredUsers(users);
}

async function loadDatabaseStats() {
    const stats = await api.get('/api/database');
    elements.dbTransitions.textContent = (stats.total_transitions || 0).toLocaleString();
    elements.dbChannels.textContent = (stats.unique_channels || 0).toLocaleString();
    elements.dbBlacklisted.textContent = (stats.blacklisted_words || 0).toLocaleString();
    elements.dbDirectory.textContent = stats.data_directory || '-';
}

// Rendering
function renderChannels(channels) {
    if (!channels || channels.length === 0) {
        elements.channelsList.innerHTML = '<div class="empty-state">No channels configured</div>';
        return;
    }

    // Channels tab - with actions
    const channelsHtml = channels.map(ch => `
        <div class="list-item">
            <div class="info">
                <div class="name">
                    <span class="status-dot ${ch.connected ? 'connected' : 'disconnected'}"></span>
                    <a href="https://twitch.tv/${ch.channel}" target="_blank" class="channel-link">${ch.channel}</a>
                </div>
                <div class="stats">${ch.messages.toLocaleString()} messages</div>
            </div>
            <div class="actions">
                ${!ch.connected ? `<button class="btn warning" onclick="reconnectChannel('${ch.channel}')">Reconnect</button>` : ''}
                <button class="btn danger" onclick="removeChannel('${ch.channel}')">Leave</button>
            </div>
        </div>
    `).join('');

    elements.channelsList.innerHTML = channelsHtml;
}

function renderLiveChannels(liveChannels) {
    if (!liveChannels || liveChannels.length === 0) {
        elements.channelList.innerHTML = '<div class="empty-state">No channels are live</div>';
        return;
    }

    const html = liveChannels.map(ch => {
        const countdown = ch.messages_until || 0;
        const interval = ch.message_interval || 1;
        const percentage = Math.round(((interval - countdown) / interval) * 100);
        
        return `
        <div class="list-item live-channel-item" onclick="window.open('https://twitch.tv/${ch.channel}', '_blank')">
            <div class="countdown-display">
                <div class="countdown-number">${countdown}</div>
                <div class="countdown-label">msgs</div>
            </div>
            <div class="info">
                <div class="name">
                    <span class="status-dot connected"></span>
                    ${ch.channel}
                </div>
                <div class="stats">
                    ${ch.game || 'Unknown Game'} • ${ch.viewers.toLocaleString()} viewers
                </div>
                <div class="stream-title">${escapeHtml(ch.title || '')}</div>
                <div class="countdown-bar">
                    <div class="countdown-progress" style="width: ${percentage}%"></div>
                </div>
            </div>
        </div>
    `}).join('');

    elements.channelList.innerHTML = html;
}

function renderBrains(brains) {
    if (!brains || brains.length === 0) {
        elements.brainsList.innerHTML = '<div class="empty-state">No brain data yet</div>';
        return;
    }

    elements.brainsList.innerHTML = brains.map(brain => `
        <div class="list-item clickable" onclick="openBrainEditor('${brain.channel}')">
            <div class="info">
                <div class="name">${brain.channel}</div>
                <div class="stats">
                    ${brain.unique_pairs.toLocaleString()} pairs • 
                    ${brain.total_entries.toLocaleString()} entries •
                    ${brain.message_count.toLocaleString()} messages
                </div>
            </div>
            <div class="actions" onclick="event.stopPropagation()">
                <button class="btn warning" onclick="cleanBrain('${brain.channel}')">Clean</button>
                <button class="btn danger" onclick="deleteBrain('${brain.channel}')">Delete</button>
            </div>
        </div>
    `).join('');
}

function renderBlacklist(words) {
    if (!words || words.length === 0) {
        elements.blacklistWords.innerHTML = '<div class="empty-state">No blacklisted words</div>';
        return;
    }

    elements.blacklistWords.innerHTML = words.map(word => `
        <span class="tag">
            ${escapeHtml(word)}
            <button class="remove-btn" onclick="removeBlacklistWord('${escapeHtml(word)}')">&times;</button>
        </span>
    `).join('');
}

function renderIgnoredUsers(users) {
    if (!users || users.length === 0) {
        elements.ignoredUsers.innerHTML = '<div class="empty-state">No ignored users</div>';
        return;
    }

    elements.ignoredUsers.innerHTML = users.map(user => `
        <span class="tag user-tag">
            @${escapeHtml(user)}
            <button class="remove-btn" onclick="removeIgnoredUser('${escapeHtml(user)}')">&times;</button>
        </span>
    `).join('');
}

function addLogEntry(channel, username, message) {
    const time = new Date().toLocaleTimeString();
    activityLog.unshift({ time, channel, username, message });
    
    if (activityLog.length > MAX_LOG_ENTRIES) {
        activityLog.pop();
    }

    elements.activityLog.innerHTML = activityLog.map(entry => `
        <div class="log-entry">
            <span class="time">${entry.time}</span>
            <span class="channel">#${entry.channel}</span>
            <span class="username">${entry.username}:</span>
            ${escapeHtml(entry.message)}
        </div>
    `).join('');
}

// Actions
async function saveInterval() {
    const interval = parseInt(elements.intervalSlider.value);
    await api.put('/api/config', { message_interval: interval });
    alert('Interval saved!');
}

async function addChannel() {
    const channel = elements.newChannel.value.trim().toLowerCase();
    if (!channel) return;

    const result = await api.post('/api/channels', { channel });
    if (result.error) {
        alert(result.error);
        return;
    }
    
    elements.newChannel.value = '';
    loadChannels();
    loadStatus();
}

async function removeChannel(channel) {
    if (!confirm(`Leave channel "${channel}"?`)) return;
    await api.delete(`/api/channels/${channel}`);
    loadChannels();
    loadStatus();
}

async function reconnectChannel(channel) {
    try {
        await api.post(`/api/channels/${channel}/reconnect`, {});
        loadChannels();
        loadStatus();
    } catch (err) {
        alert(`Failed to reconnect: ${err.message}`);
    }
}

async function cleanBrain(channel) {
    if (!confirm(`Clean brain for "${channel}"? This will remove all transitions containing blacklisted words.`)) return;
    const result = await api.post(`/api/brains/${channel}/clean`, {});
    alert(`Removed ${result.rows_removed} entries`);
    loadBrains();
    loadDatabaseStats();
}

async function deleteBrain(channel) {
    if (!confirm(`Delete all brain data for "${channel}"? This cannot be undone.`)) return;
    await api.delete(`/api/brains/${channel}`);
    loadBrains();
    loadDatabaseStats();
}

async function addBlacklistWord() {
    const word = elements.newBlacklistWord.value.trim().toLowerCase();
    if (!word) return;

    await api.post('/api/blacklist', { word });
    elements.newBlacklistWord.value = '';
    loadBlacklist();
    loadDatabaseStats();
}

async function removeBlacklistWord(word) {
    await api.delete(`/api/blacklist/${encodeURIComponent(word)}`);
    loadBlacklist();
    loadDatabaseStats();
}

async function clearBlacklist() {
    if (!confirm('Clear all blacklisted words?')) return;
    await api.delete('/api/blacklist');
    loadBlacklist();
    loadDatabaseStats();
}

async function addIgnoredUser() {
    const username = elements.newIgnoredUser.value.trim().toLowerCase();
    if (!username) return;

    await api.post('/api/userblacklist', { username });
    elements.newIgnoredUser.value = '';
    loadIgnoredUsers();
}

async function removeIgnoredUser(username) {
    await api.delete(`/api/userblacklist/${encodeURIComponent(username)}`);
    loadIgnoredUsers();
}

async function optimizeDatabase() {
    await api.post('/api/database', {});
    alert('Database optimized!');
    loadDatabaseStats();
}

async function cleanAllBrains() {
    if (!confirm('Clean ALL brain data? This will remove entries containing blacklisted words from all channels.')) return;
    const result = await api.delete('/api/database');
    alert(`Removed ${result.rows_removed} entries from all brains`);
    loadBrains();
    loadDatabaseStats();
}

// Brain Editor
let editorState = {
    channel: null,
    page: 1,
    pageSize: 50,
    search: '',
    total: 0
};

function openBrainEditor(channel) {
    editorState.channel = channel;
    editorState.page = 1;
    editorState.search = '';
    document.getElementById('editor-channel').textContent = channel;
    document.getElementById('transition-search').value = '';
    document.getElementById('brain-editor-modal').classList.add('active');
    loadTransitions();
}

function closeBrainEditor() {
    document.getElementById('brain-editor-modal').classList.remove('active');
    editorState.channel = null;
}

async function loadTransitions() {
    if (!editorState.channel) return;
    
    const params = new URLSearchParams({
        page: editorState.page,
        pageSize: editorState.pageSize
    });
    if (editorState.search) {
        params.set('search', editorState.search);
    }
    
    const result = await api.get(`/api/brains/${editorState.channel}/transitions?${params}`);
    editorState.total = result.total;
    renderTransitions(result);
}

function renderTransitions(result) {
    document.getElementById('editor-showing').textContent = result.transitions.length;
    document.getElementById('editor-total').textContent = result.total.toLocaleString();
    document.getElementById('current-page').textContent = editorState.page;
    
    const maxPages = Math.ceil(result.total / editorState.pageSize);
    document.getElementById('prev-page-btn').disabled = editorState.page <= 1;
    document.getElementById('next-page-btn').disabled = editorState.page >= maxPages;
    
    const list = document.getElementById('transitions-list');
    
    if (result.transitions.length === 0) {
        list.innerHTML = '<div class="empty-state">No transitions found</div>';
        return;
    }
    
    list.innerHTML = `
        <div class="transition-row header">
            <span>Word 1</span>
            <span>Word 2</span>
            <span>Next Word</span>
            <span>Count</span>
            <span></span>
        </div>
        ${result.transitions.map(t => `
            <div class="transition-row">
                <span class="word" title="${escapeHtml(t.word1)}">${escapeHtml(t.word1)}</span>
                <span class="word" title="${escapeHtml(t.word2)}">${escapeHtml(t.word2)}</span>
                <span class="word" title="${escapeHtml(t.next_word)}">${escapeHtml(t.next_word)}</span>
                <span class="count">${t.count}</span>
                <button class="delete-btn" onclick="deleteTransition('${escapeHtml(t.word1)}', '${escapeHtml(t.word2)}', '${escapeHtml(t.next_word)}')">Delete</button>
            </div>
        `).join('')}
    `;
}

function searchTransitions() {
    editorState.search = document.getElementById('transition-search').value.trim();
    editorState.page = 1;
    loadTransitions();
}

function prevPage() {
    if (editorState.page > 1) {
        editorState.page--;
        loadTransitions();
    }
}

function nextPage() {
    const maxPages = Math.ceil(editorState.total / editorState.pageSize);
    if (editorState.page < maxPages) {
        editorState.page++;
        loadTransitions();
    }
}

async function deleteTransition(word1, word2, nextWord) {
    if (!confirm(`Delete transition: "${word1}" + "${word2}" → "${nextWord}"?`)) return;
    
    await fetch(`/api/brains/${editorState.channel}/transition`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ word1, word2, next_word: nextWord })
    });
    
    loadTransitions();
    loadBrains();
    loadDatabaseStats();
}

// Utilities
function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}
