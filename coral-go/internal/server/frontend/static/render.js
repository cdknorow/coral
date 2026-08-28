/* Rendering functions for session lists, chat history, and status updates */

import { state } from './state.js';
import { escapeHtml, showToast, escapeAttr, dbg, showView, renderMarkdown, getAgentColor, hexToRgba } from './utils.js';
import { renderSidebarTagDots } from './tags.js';
import { getFolderTags, renderFolderTagPills } from './folder_tags.js';
import { updateSectionVisibility } from './sidebar.js';
import { syncMobileAgentList } from './mobile.js';

/* ── Helpers ────────────────────────────────────────────────────────── */

function _repoBranch(repoName, branch) {
    if (repoName && branch) return `${repoName} : ${branch}`;
    return branch || repoName || '';
}

/* ── Agent Avatars ──────────────────────────────────────────────────── */

const _roleAvatars = {
    'orchestrator': '👑', 'lead': '🔨', 'lead dev': '🔨', 'architect': '📐',
    'frontend': '🎨', 'backend': '⚙️', 'qa': '🔍', 'quality': '🔍', 'test': '🧪',
    'security': '🛡️', 'devops': '🚀', 'data': '📊', 'llm': '🧠', 'ai': '🤖',
    'design': '✏️', 'writer': '📝', 'content': '📝', 'research': '🔬',
    'marketing': '📣', 'seo': '📈', 'product': '💡', 'manager': '📋',
    'terminal': '💻', 'ops': '🔧', 'infra': '☁️',
};

function _getAvatarEmoji(name) {
    if (!name) return null;
    const lower = name.toLowerCase();
    for (const [keyword, emoji] of Object.entries(_roleAvatars)) {
        if (lower.includes(keyword)) return emoji;
    }
    return null;
}

function _getInitials(name) {
    if (!name) return '?';
    const words = name.trim().split(/\s+/);
    if (words.length >= 2) return (words[0][0] + words[1][0]).toUpperCase();
    return name.slice(0, 2).toUpperCase();
}

function _renderAvatar(s, dotClass) {
    const name = s.display_name || s.board_job_title || s.name || '';
    const color = getAgentColor(name);
    const statusDot = `<span class="avatar-status-dot ${dotClass}"></span>`;

    // Custom icon takes priority
    if (s.icon && !s.sleeping) {
        return `<div class="agent-avatar" style="background:${hexToRgba(color, 0.15)}">
            <span class="agent-avatar-emoji">${escapeHtml(s.icon)}</span>${statusDot}
        </div>`;
    }

    // Sleeping
    if (s.sleeping) {
        return `<div class="agent-avatar" style="background:rgba(255,193,7,0.1)">
            <span class="agent-avatar-emoji">🌙</span>${statusDot}
        </div>`;
    }

    // Role-based emoji
    const emoji = _getAvatarEmoji(name);
    if (emoji) {
        return `<div class="agent-avatar" style="background:${hexToRgba(color, 0.15)}">
            <span class="agent-avatar-emoji">${emoji}</span>${statusDot}
        </div>`;
    }

    // Fallback: colored initials
    const initials = _getInitials(name);
    return `<div class="agent-avatar" style="background:${hexToRgba(color, 0.2)};color:${color}">
        <span class="agent-avatar-initials">${escapeHtml(initials)}</span>${statusDot}
    </div>`;
}

function formatShortTime(isoStr) {
    const d = new Date(isoStr);
    if (isNaN(d)) return "";
    const dd = String(d.getDate()).padStart(2, "0");
    const mm = String(d.getMonth() + 1).padStart(2, "0");
    const yy = String(d.getFullYear()).slice(-2);
    return `${dd}/${mm}/${yy}`;
}

function formatDuration(sec) {
    if (sec == null || sec <= 0) return '';
    if (sec < 60) return `${sec}s`;
    if (sec < 3600) return `${Math.round(sec / 60)}m`;
    const h = Math.floor(sec / 3600);
    const m = Math.round((sec % 3600) / 60);
    return m > 0 ? `${h}h ${m}m` : `${h}h`;
}

function formatStaleness(seconds) {
    if (seconds === null || seconds === undefined) return "Unknown";
    if (seconds < 5) return "Just now";
    if (seconds < 60) return `${Math.floor(seconds)}s ago`;
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
    return `${Math.floor(seconds / 3600)}h ago`;
}

function getStateLabel(s) {
    if (s.sleeping) return "Sleeping";
    if (s.waiting_for_input) return "Needs input";
    // The agent has produced output but has never done anything. Most often a
    // prompt sitting unanswered in its terminal; sometimes just an agent that
    // was never given a task. Both mean: look at the terminal.
    if (s.not_started) return "Check terminal";
    if (s.stuck) return "Stuck";
    if (s.working) return "Working";
    if (s.done) return "Done";
    return "Idle";
}

function getMobileStatusChip(s) {
    if (s.waiting_for_input) return { label: "Needs Input", className: "needs-input" };
    if (s.not_started) return { label: "Check Terminal", className: "needs-input" };
    if (s.stuck) return { label: "Error", className: "error" };
    if (s.working) return { label: "Running", className: "running" };
    return { label: "Idle", className: "idle" };
}

function buildSessionTooltip(s) {
    const stateLabel = getStateLabel(s);
    const lastAction = formatStaleness(s.staleness_seconds);
    const goal = s.summary || "No goal set";
    const status = s.status || "No status";
    const branch = _repoBranch(s.repo_name, s.branch) || "—";
    const agent = s.agent_type || "claude";

    let rows = [
        `<tr><td class="tt-label">State</td><td class="tt-value tt-state-${stateLabel.toLowerCase().replace(/ /g, '-')}">${escapeHtml(stateLabel)}</td></tr>`,
        `<tr><td class="tt-label">Last action</td><td class="tt-value">${escapeHtml(lastAction)}</td></tr>`,
        `<tr><td class="tt-label">Goal</td><td class="tt-value">${escapeHtml(goal)}</td></tr>`,
        `<tr><td class="tt-label">Status</td><td class="tt-value">${escapeHtml(status)}</td></tr>`,
        `<tr><td class="tt-label">Branch</td><td class="tt-value">${escapeHtml(branch)}</td></tr>`,
        `<tr><td class="tt-label">Agent</td><td class="tt-value">${escapeHtml(agent)}</td></tr>`,
    ];
    if (s.changed_file_count > 0) {
        rows.push(`<tr><td class="tt-label">Files changed</td><td class="tt-value">${s.changed_file_count}</td></tr>`);
    }
    if (s.token_cost_usd > 0 || s.token_input > 0) {
        const tokIn = _formatTokens(s.token_input || 0);
        const tokOut = _formatTokens(s.token_output || 0);
        const tokCache = (s.token_cache_read || 0);
        let tokenText = `${tokIn} in / ${tokOut} out`;
        if (tokCache > 0) tokenText += ` / ${_formatTokens(tokCache)} cache`;
        if (s.token_cost_usd > 0) tokenText += ` · ${_formatCost(s.token_cost_usd)}`;
        rows.push(`<tr><td class="tt-label">Tokens</td><td class="tt-value">${tokenText}</td></tr>`);
    }
    if (s.board_project) {
        const unreadBadge = s.board_unread > 0 ? ` <span class="tt-unread">(${s.board_unread} unread)</span>` : '';
        rows.push(`<tr><td class="tt-label">Board</td><td class="tt-value">${escapeHtml(s.board_project)}${unreadBadge}</td></tr>`);
        rows.push(`<tr><td class="tt-label">Role</td><td class="tt-value">${escapeHtml(s.board_job_title)}</td></tr>`);
    }
    return `<table class="session-tooltip-table">${rows.join("")}</table>`;
}

function getDotClass(s) {
    if (s.sleeping) return "sleeping";
    if (s.waiting_for_input) return "waiting";
    if (s.not_started) return "waiting";
    if (s.stuck) return "stuck";
    if (s.working) return "working";
    if (s.done) return "done";
    return "stale";
}

// ── Board accent colors (localStorage) ────────────────────────────────

function _getBoardColor(boardName) {
    try {
        const colors = JSON.parse(localStorage.getItem('coral-board-colors') || '{}');
        return colors[boardName] || null;
    } catch { return null; }
}

function _setBoardColor(boardName, color) {
    try {
        const colors = JSON.parse(localStorage.getItem('coral-board-colors') || '{}');
        if (color) {
            colors[boardName] = color;
        } else {
            delete colors[boardName];
        }
        localStorage.setItem('coral-board-colors', JSON.stringify(colors));
    } catch { /* ignore */ }
}

const _colorSwatches = [
    '#81a1c1', '#a3be8c', '#b48ead', '#d08770', '#bf616a',
    '#88c0d0', '#ebcb8b', '#8fbcbb', '#58a6ff', '#f778ba',
    '#7ee787', '#ffa657', '#ff7b72', '#d2a8ff', '#79c0ff',
    '#e6edf3', '#8b949e', '#484f58',
];

export function setBoardAccentColor(boardName) {
    // Remove any existing picker
    const existing = document.getElementById('board-color-picker');
    if (existing) { existing.remove(); return; }

    const current = _getBoardColor(boardName) || '#58a6ff';
    const picker = document.createElement('div');
    picker.id = 'board-color-picker';
    picker.className = 'color-picker-popover';
    picker.innerHTML = `
        <div class="color-picker-swatches">
            ${_colorSwatches.map(c =>
                `<button class="color-swatch${c === current ? ' active' : ''}" style="background:${c}" data-color="${c}" title="${c}"></button>`
            ).join('')}
        </div>
        <div class="color-picker-custom">
            <input type="text" class="color-picker-hex" value="${current}" placeholder="#hex" maxlength="7">
            <button class="btn btn-small color-picker-apply">Apply</button>
            <button class="btn btn-small color-picker-reset">Reset</button>
        </div>
    `;
    document.body.appendChild(picker);

    // Position near the clicked element
    const rect = event?.target?.getBoundingClientRect();
    if (rect) {
        picker.style.top = Math.min(rect.bottom + 4, window.innerHeight - 200) + 'px';
        picker.style.left = Math.min(rect.left, window.innerWidth - 220) + 'px';
    }

    const apply = (color) => {
        _setBoardColor(boardName, color);
        renderLiveSessions(state.liveSessions);
        picker.remove();
    };

    // Swatch clicks
    picker.querySelectorAll('.color-swatch').forEach(btn => {
        btn.addEventListener('click', (e) => { e.stopPropagation(); apply(btn.dataset.color); });
    });

    // Custom hex apply
    picker.querySelector('.color-picker-apply').addEventListener('click', (e) => {
        e.stopPropagation();
        const hex = picker.querySelector('.color-picker-hex').value.trim();
        if (/^#[0-9a-fA-F]{6}$/.test(hex)) apply(hex);
    });

    // Reset
    picker.querySelector('.color-picker-reset').addEventListener('click', (e) => {
        e.stopPropagation();
        _setBoardColor(boardName, null);
        renderLiveSessions(state.liveSessions);
        picker.remove();
    });

    // Close on outside click
    setTimeout(() => {
        document.addEventListener('click', function handler(e) {
            if (!picker.contains(e.target)) { picker.remove(); document.removeEventListener('click', handler); }
        });
    }, 0);
}

// ── Board Chat (right-panel tab) ──────────────────────────────────────

let _boardChatTimer = null;
let _boardChatLastId = {};
let _activeBoardChat = null;
let _boardChatMessages = [];
let _boardChatTotal = 0;
let _boardChatOffset = 0;
const _BOARD_CHAT_PAGE = 50;
let _boardChatSelectMode = false;
let _boardChatSelectedIds = new Set();

export function showBoardChatTab(boardName) {
    _activeBoardChat = boardName;
    const panel = document.getElementById('agentic-panel-board');
    if (!panel) return;

    // Populate the board tab content (visibility managed by switchAgenticTab)
    panel.innerHTML = `
        <div class="board-chat-header">
            <a class="board-chat-title" href="#" onclick="event.preventDefault(); selectBoardProject('${escapeAttr(boardName)}')" title="Open full board view">${escapeHtml(boardName)}</a>
            <button class="btn-nav" id="board-chat-pause-btn" onclick="window._toggleBoardChatPause('${escapeAttr(boardName)}')" title="Pause Board — stops agents from receiving new messages until resumed">Pause Board</button>
            <button class="btn-nav board-select-btn" onclick="window._toggleBoardChatSelect()" title="Export — select messages to export as Markdown">Export</button>
        </div>
        <div class="board-paused-banner" id="board-chat-paused-banner" style="display:none">Board reads are paused — agents cannot see new messages</div>
        <div class="board-chat-messages" id="board-panel-msgs"></div>
        <div class="board-chat-input-pane" id="board-chat-input-pane">
            <div class="board-chat-resize-handle" id="board-chat-resize-handle"></div>
            <div class="command-pane-toolbar board-chat-toolbar">
                <div class="toolbar-group">
                    <button class="btn-nav" onclick="const i=document.getElementById('board-panel-input');if(i){i.value='@all '+i.value;i.focus()}" title="Notify all agents">@all</button>
                </div>
                <span class="toolbar-spacer"></span>
                <div class="toolbar-group">
                    <button class="btn-nav" onclick="window._sendBoardChat('${escapeAttr(boardName)}')" title="Send message">
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><line x1="2" y1="8" x2="14" y2="8"/><polyline points="9 3 14 8 9 13"/></svg>
                    </button>
                </div>
            </div>
            <div class="board-chat-editor-wrap">
                <textarea class="board-chat-textarea" id="board-panel-input" placeholder="Message the team... (@ to mention)"
                    onkeydown="if(event.key==='Enter'&&event.ctrlKey){event.preventDefault();window._sendBoardChat('${escapeAttr(boardName)}')}"
                    oninput="window._boardMentionInput(event, '${escapeAttr(boardName)}')"></textarea>
                <div class="board-mention-dropdown" id="board-mention-dropdown" style="display:none"></div>
            </div>
        </div>`;

    // Restore saved input pane height
    const savedH = localStorage.getItem('coral-boardchat-height');
    if (savedH) {
        const pane = document.getElementById('board-chat-input-pane');
        const h = parseInt(savedH, 10);
        if (pane && h >= 80) pane.style.height = h + 'px';
    }

    // Reset pagination and select state for new board
    _boardChatMessages = [];
    _boardChatTotal = 0;
    _boardChatOffset = 0;
    _boardChatLastId[boardName] = null;
    _boardChatSelectMode = false;
    _boardChatSelectedIds.clear();

    _loadBoardPanelChat(boardName);
    _checkBoardPauseState(boardName);
    // Start polling
    if (_boardChatTimer) clearInterval(_boardChatTimer);
    _boardChatTimer = setInterval(() => _loadBoardPanelChat(boardName), 3000);
}

export function hideBoardChatTab() {
    _activeBoardChat = null;
    _boardChatSelectMode = false;
    _boardChatSelectedIds.clear();
    if (_boardChatTimer) { clearInterval(_boardChatTimer); _boardChatTimer = null; }
    const panel = document.getElementById('agentic-panel-board');
    if (panel) panel.innerHTML = '';
}

function _formatTime(iso) {
    if (!iso) return '';
    const d = new Date(iso);
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

async function _loadBoardPanelChat(boardName) {
    const msgsEl = document.getElementById('board-panel-msgs');
    if (!msgsEl) return;
    try {
        // First call: get total count and load latest page
        const countResp = await fetch(`/api/board/${encodeURIComponent(boardName)}/messages/all?limit=1&offset=0&format=dashboard`);
        const countData = await countResp.json();
        const total = countData.total || (Array.isArray(countData) ? countData.length : 0);

        if (total === 0) {
            msgsEl.innerHTML = '<div class="board-chat-empty">No messages yet</div>';
            _boardChatMessages = [];
            _boardChatTotal = 0;
            return;
        }

        // Check if new messages arrived
        if (_boardChatTotal === total && _boardChatMessages.length > 0) return;

        // Load the latest page
        const startOffset = Math.max(0, total - _BOARD_CHAT_PAGE);
        const resp = await fetch(`/api/board/${encodeURIComponent(boardName)}/messages/all?limit=${_BOARD_CHAT_PAGE}&offset=${startOffset}&format=dashboard`);
        const data = await resp.json();
        const messages = Array.isArray(data) ? data : (data.messages || []);

        _boardChatMessages = messages;
        _boardChatTotal = total;
        _boardChatOffset = startOffset;

        _renderBoardPanelMessages(msgsEl, true);
    } catch { /* ignore */ }
}

async function _loadEarlierBoardChat() {
    if (!_activeBoardChat || _boardChatOffset <= 0) return;
    const msgsEl = document.getElementById('board-panel-msgs');
    if (!msgsEl) return;
    try {
        const newOffset = Math.max(0, _boardChatOffset - _BOARD_CHAT_PAGE);
        const fetchCount = _boardChatOffset - newOffset;
        const resp = await fetch(`/api/board/${encodeURIComponent(_activeBoardChat)}/messages/all?limit=${fetchCount}&offset=${newOffset}&format=dashboard`);
        const data = await resp.json();
        const messages = Array.isArray(data) ? data : (data.messages || []);

        // Save scroll height to preserve position after prepending
        const prevScrollHeight = msgsEl.scrollHeight;

        _boardChatMessages = [...messages, ..._boardChatMessages];
        _boardChatOffset = newOffset;

        _renderBoardPanelMessages(msgsEl, false);

        // Restore scroll position so user stays where they were
        msgsEl.scrollTop = msgsEl.scrollHeight - prevScrollHeight;
    } catch { /* ignore */ }
}
window._loadEarlierBoardChat = _loadEarlierBoardChat;

function _renderBoardPanelMessages(msgsEl, scrollToBottom) {
    const messages = _boardChatMessages;
    const wasAtBottom = scrollToBottom || msgsEl.scrollTop >= msgsEl.scrollHeight - msgsEl.clientHeight - 20;

    // "Load Earlier" button
    let loadEarlierHtml = '';
    if (_boardChatOffset > 0) {
        const remaining = _boardChatOffset;
        loadEarlierHtml = `<div style="text-align:center;padding:6px 0 8px">
            <button class="btn btn-small" onclick="_loadEarlierBoardChat()" style="font-size:11px;color:var(--text-muted)">
                ▲ Load ${Math.min(remaining, _BOARD_CHAT_PAGE)} earlier (${remaining} remaining)
            </button>
        </div>`;
    }

    msgsEl.innerHTML = loadEarlierHtml + messages.map((m, i) => {
        const agent = m.job_title || m.sender_name || 'Unknown';
        const color = getAgentColor(agent);
        const prevAgent = i > 0 ? (messages[i - 1].job_title || messages[i - 1].sender_name || 'Unknown') : null;
        const sameAsPrev = agent === prevAgent;
        const spacing = sameAsPrev ? 'mb-message-grouped' : 'mb-message-first';
        const isLeader = /orchestrator/i.test(agent) || m.session_id === 'dashboard';
        const alignClass = isLeader ? ' board-msg-left' : ' board-msg-right';
        const r = parseInt(color.slice(1, 3), 16), g = parseInt(color.slice(3, 5), 16), b = parseInt(color.slice(5, 7), 16);

        const selectedClass = _boardChatSelectMode && _boardChatSelectedIds.has(m.id) ? ' mb-message-selected' : '';
        const checkbox = _boardChatSelectMode ? `<input type="checkbox" class="mb-select-checkbox" data-msg-id="${m.id}" ${_boardChatSelectedIds.has(m.id) ? 'checked' : ''} onclick="window._toggleBoardChatMsgSelect(${m.id}, this.checked)">` : '';
        return `<div class="mb-message ${spacing}${alignClass}${selectedClass}" style="border-left:3px solid rgba(${r},${g},${b},0.55); border-bottom:2px solid rgba(${r},${g},${b},0.3)">
            <div class="mb-message-header">
                ${checkbox}
                <span class="mb-agent-name" style="color:${color}">${m.icon ? escapeHtml(m.icon) + ' ' : ''}${escapeHtml(agent)}</span>
                <span class="mb-message-time">${_formatTime(m.created_at)}</span>
            </div>
            <div class="mb-message-body">${renderMarkdown(m.content)}</div>
        </div>`;
    }).join('');
    if (wasAtBottom) msgsEl.scrollTop = msgsEl.scrollHeight;
    // Update select bar if in select mode
    if (_boardChatSelectMode) _updateBoardChatSelectBar();
}

function _toggleBoardChatSelect() {
    _boardChatSelectMode = !_boardChatSelectMode;
    _boardChatSelectedIds.clear();
    const btn = document.querySelector('.board-select-btn');
    if (btn) btn.classList.toggle('active', _boardChatSelectMode);
    // Hide chat input when in select mode
    const inputPane = document.getElementById('board-chat-input-pane');
    if (inputPane) inputPane.style.display = _boardChatSelectMode ? 'none' : '';
    _updateBoardChatSelectBar();
    const msgsEl = document.getElementById('board-panel-msgs');
    if (msgsEl) _renderBoardPanelMessages(msgsEl, false);
}
window._toggleBoardChatSelect = _toggleBoardChatSelect;

function _toggleBoardChatMsgSelect(msgId, checked) {
    if (checked) {
        _boardChatSelectedIds.add(msgId);
    } else {
        _boardChatSelectedIds.delete(msgId);
    }
    const cb = document.querySelector(`#board-panel-msgs .mb-select-checkbox[data-msg-id="${msgId}"]`);
    if (cb) cb.closest('.mb-message').classList.toggle('mb-message-selected', checked);
    _updateBoardChatSelectBar();
}
window._toggleBoardChatMsgSelect = _toggleBoardChatMsgSelect;

function _boardChatSelectAll() {
    _boardChatMessages.forEach(m => _boardChatSelectedIds.add(m.id));
    const msgsEl = document.getElementById('board-panel-msgs');
    if (msgsEl) _renderBoardPanelMessages(msgsEl, false);
}
window._boardChatSelectAll = _boardChatSelectAll;

function _boardChatSelectNone() {
    _boardChatSelectedIds.clear();
    const msgsEl = document.getElementById('board-panel-msgs');
    if (msgsEl) _renderBoardPanelMessages(msgsEl, false);
}
window._boardChatSelectNone = _boardChatSelectNone;

function _cancelBoardChatSelect() {
    _boardChatSelectMode = false;
    _boardChatSelectedIds.clear();
    const btn = document.querySelector('.board-select-btn');
    if (btn) btn.classList.remove('active');
    // Restore chat input
    const inputPane = document.getElementById('board-chat-input-pane');
    if (inputPane) inputPane.style.display = '';
    _updateBoardChatSelectBar();
    const msgsEl = document.getElementById('board-panel-msgs');
    if (msgsEl) _renderBoardPanelMessages(msgsEl, false);
}
window._cancelBoardChatSelect = _cancelBoardChatSelect;

function _updateBoardChatSelectBar() {
    const panel = document.getElementById('agentic-panel-board');
    if (!panel) return;
    let bar = document.getElementById('board-chat-select-bar');
    if (!_boardChatSelectMode) {
        if (bar) bar.style.display = 'none';
        return;
    }
    if (!bar) {
        bar = document.createElement('div');
        bar.id = 'board-chat-select-bar';
        bar.className = 'mb-select-bar';
        panel.appendChild(bar);
    }
    const count = _boardChatSelectedIds.size;
    bar.innerHTML = `
        <span class="mb-select-count">${count} selected</span>
        <button class="btn mb-select-action-btn" onclick="_boardChatSelectAll()">Select All</button>
        <button class="btn mb-select-action-btn" onclick="_boardChatSelectNone()">None</button>
        <button class="btn btn-primary mb-select-action-btn" onclick="window._exportBoardChatSelected()" ${count === 0 ? 'disabled' : ''}>Export Markdown</button>
        <button class="btn mb-select-action-btn" onclick="_cancelBoardChatSelect()">Cancel</button>
    `;
    bar.style.display = '';
}

async function _exportBoardChatSelected() {
    const selected = _boardChatMessages
        .filter(m => _boardChatSelectedIds.has(m.id))
        .sort((a, b) => (a.created_at || '').localeCompare(b.created_at || ''));
    if (!selected.length) return;

    const project = _activeBoardChat || 'board';
    const now = new Date().toLocaleString();
    let md = `# ${project} — Exported Chat\n\n`;
    md += `**Exported**: ${now} · **Messages**: ${selected.length}\n\n---\n\n`;
    for (const m of selected) {
        const agent = m.job_title || m.sender_name || 'Unknown';
        const time = _formatTime(m.created_at);
        md += `### ${agent} — ${time}\n${m.content}\n\n`;
    }
    md += `---\n*Exported from Coral*\n`;

    try {
        await navigator.clipboard.writeText(md);
        showToast(`Copied ${selected.length} messages to clipboard`);
    } catch { /* fallback to download only */ }

    const blob = new Blob([md], { type: 'text/markdown' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${project}-selected-export.md`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);

    _cancelBoardChatSelect();
}
window._exportBoardChatSelected = _exportBoardChatSelected;

async function _sendBoardChat(boardName) {
    const inputEl = document.getElementById('board-panel-input');
    if (!inputEl) return;
    const content = inputEl.value.trim();
    if (!content) return;
    inputEl.value = '';
    try {
        await fetch(`/api/board/${encodeURIComponent(boardName)}/messages`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ session_id: 'dashboard', content }),
        });
        _loadBoardPanelChat(boardName);
    } catch { /* ignore */ }
}
window._sendBoardChat = _sendBoardChat;

async function _toggleBoardChatPause(boardName) {
    const btn = document.getElementById('board-chat-pause-btn');
    const banner = document.getElementById('board-chat-paused-banner');
    const wasPaused = btn?.classList.contains('mb-action-danger');
    const endpoint = wasPaused ? 'resume' : 'pause';
    try {
        await fetch(`/api/board/${encodeURIComponent(boardName)}/${endpoint}`, { method: 'POST' });
        if (btn) {
            if (wasPaused) {
                btn.textContent = 'Pause Board';
                btn.classList.remove('mb-action-danger');
            } else {
                btn.textContent = 'Resume Board';
                btn.classList.add('mb-action-danger');
            }
        }
        if (banner) {
            banner.style.display = wasPaused ? 'none' : '';
        }
    } catch { /* ignore */ }
}
window._toggleBoardChatPause = _toggleBoardChatPause;

async function _toggleBoardPause(boardName) {
    const btn = document.getElementById('board-pause-btn');
    const isPaused = btn?.classList.contains('paused');
    const endpoint = isPaused ? 'resume' : 'pause';
    try {
        await fetch(`/api/board/${encodeURIComponent(boardName)}/${endpoint}`, { method: 'POST' });
        if (btn) btn.classList.toggle('paused');
        showToast(isPaused ? `Board "${boardName}" resumed` : `Board "${boardName}" paused`);
    } catch { /* ignore */ }
}
window._toggleBoardPause = _toggleBoardPause;

async function _checkBoardPauseState(boardName) {
    try {
        const resp = await fetch(`/api/board/${encodeURIComponent(boardName)}/paused`);
        const data = await resp.json();
        const btn = document.getElementById('board-chat-pause-btn');
        const banner = document.getElementById('board-chat-paused-banner');
        if (btn) {
            if (data.paused) {
                btn.textContent = 'Resume Board';
                btn.classList.add('mb-action-danger');
            } else {
                btn.textContent = 'Pause Board';
                btn.classList.remove('mb-action-danger');
            }
        }
        if (banner) {
            banner.style.display = data.paused ? '' : 'none';
        }
    } catch { /* ignore */ }
}

async function _clearBoardMessages(boardName) {
    showConfirmModal('Clear Messages', `Clear all messages from "${boardName}"? This cannot be undone.`, async () => {
        try {
            await fetch(`/api/board/${encodeURIComponent(boardName)}`, { method: 'DELETE' });
            _boardChatLastId[boardName] = null;
            _loadBoardPanelChat(boardName);
            showToast(`Cleared messages from "${boardName}"`);
        } catch { /* ignore */ }
    });
}
window._clearBoardMessages = _clearBoardMessages;

// Board chat @mention autocomplete
let _boardSubscribers = {};
async function _boardMentionInput(event, boardName) {
    const input = event.target;
    const text = input.value;
    const cursor = input.selectionStart;
    const beforeCursor = text.slice(0, cursor);
    const dropdown = document.getElementById('board-mention-dropdown');
    if (!dropdown) return;

    // Find @ trigger
    const atMatch = beforeCursor.match(/@(\w*)$/);
    if (!atMatch) { dropdown.style.display = 'none'; return; }
    const query = atMatch[1].toLowerCase();

    // Fetch subscribers if not cached
    if (!_boardSubscribers[boardName]) {
        try {
            const resp = await fetch(`/api/board/${encodeURIComponent(boardName)}/subscribers`);
            _boardSubscribers[boardName] = await resp.json();
        } catch { _boardSubscribers[boardName] = []; }
    }

    const subs = (_boardSubscribers[boardName] || [])
        .map(s => s.job_title || s.session_id || 'unknown')
        .filter(name => name.toLowerCase().includes(query));

    if (subs.length === 0) { dropdown.style.display = 'none'; return; }

    dropdown.innerHTML = subs.map(name =>
        `<div class="board-mention-item" onmousedown="event.preventDefault(); window._insertBoardMention('${escapeAttr(name)}')">${escapeHtml(name)}</div>`
    ).join('');
    dropdown.style.display = 'block';
}
window._boardMentionInput = _boardMentionInput;

function _insertBoardMention(name) {
    const input = document.getElementById('board-panel-input');
    const dropdown = document.getElementById('board-mention-dropdown');
    if (!input) return;
    const text = input.value;
    const cursor = input.selectionStart;
    const beforeCursor = text.slice(0, cursor);
    const atIdx = beforeCursor.lastIndexOf('@');
    if (atIdx === -1) return;
    const after = text.slice(cursor);
    input.value = beforeCursor.slice(0, atIdx) + '@' + name + ' ' + after;
    input.selectionStart = input.selectionEnd = atIdx + name.length + 2;
    input.focus();
    if (dropdown) dropdown.style.display = 'none';
}
window._insertBoardMention = _insertBoardMention;

// ── Session ordering ──────────────────────────────────────────────────

function _getSessionOrder() {
    try {
        return JSON.parse(state.settings.session_order || "[]");
    } catch { return []; }
}

function _sortByOrder(sessions) {
    const order = _getSessionOrder();
    if (!order.length) return sessions;
    const posMap = {};
    order.forEach((sid, i) => { posMap[sid] = i; });
    return [...sessions].sort((a, b) => {
        const pa = posMap[a.session_id] ?? 9999;
        const pb = posMap[b.session_id] ?? 9999;
        return pa - pb;
    });
}

async function _saveSessionOrder(orderedIds) {
    state.settings.session_order = JSON.stringify(orderedIds);
    try {
        await fetch("/api/settings", {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ session_order: state.settings.session_order }),
        });
    } catch (e) {
        console.error("Failed to save session order:", e);
    }
}

// ── Group ordering ───────────────────────────────────────────────────

function _getGroupOrder() {
    try {
        return JSON.parse(state.settings.group_order || "[]");
    } catch { return []; }
}

function _sortGroups(groupEntries) {
    const order = _getGroupOrder();
    if (!order.length) return groupEntries;
    const posMap = {};
    order.forEach((name, i) => { posMap[name] = i; });
    return [...groupEntries].sort((a, b) => {
        const pa = posMap[a[0]] ?? 9999;
        const pb = posMap[b[0]] ?? 9999;
        return pa - pb;
    });
}

async function _saveGroupOrder(orderedNames) {
    state.settings.group_order = JSON.stringify(orderedNames);
    try {
        await fetch("/api/settings", {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ group_order: state.settings.group_order }),
        });
    } catch (e) {
        console.error("Failed to save group order:", e);
    }
}

function _getCurrentGroupNames() {
    // Get current group names from the rendered sidebar
    const headers = document.querySelectorAll('.session-group-header[data-group-name]');
    return Array.from(headers).map(el => el.dataset.groupName);
}

export async function moveGroupUp(groupName) {
    const names = _getCurrentGroupNames();
    const idx = names.indexOf(groupName);
    if (idx <= 0) return;
    [names[idx - 1], names[idx]] = [names[idx], names[idx - 1]];
    await _saveGroupOrder(names);
    renderLiveSessions(state.liveSessions || []);
}

export async function moveGroupDown(groupName) {
    const names = _getCurrentGroupNames();
    const idx = names.indexOf(groupName);
    if (idx < 0 || idx >= names.length - 1) return;
    [names[idx], names[idx + 1]] = [names[idx + 1], names[idx]];
    await _saveGroupOrder(names);
    renderLiveSessions(state.liveSessions || []);
}

// ── Session reorder (Move Up/Down) ───────────────────────────────────

export function moveSessionUp(sessionId) {
    _moveSession(sessionId, -1);
}

export function moveSessionDown(sessionId) {
    _moveSession(sessionId, 1);
}

function _moveSession(sessionId, direction) {
    const list = document.getElementById("live-sessions-list");
    if (!list) return;
    const items = [...list.querySelectorAll(".session-group-item")];
    const ids = items.map(el => el.dataset.sessionId);
    const idx = ids.indexOf(sessionId);
    if (idx < 0) return;
    const targetIdx = idx + direction;
    if (targetIdx < 0 || targetIdx >= ids.length) return;
    [ids[idx], ids[targetIdx]] = [ids[targetIdx], ids[idx]];
    _saveSessionOrder(ids);
    renderLiveSessions(state.liveSessions);
}

// ── Group collapse state ─────────────────────────────────────────────
function _getCollapsedGroups() {
    try { return JSON.parse(localStorage.getItem('coral_collapsed_groups') || '[]'); }
    catch { return []; }
}

function _isGroupCollapsed(groupName) {
    return _getCollapsedGroups().includes(groupName);
}

export function toggleGroupCollapse(groupName) {
    const groups = _getCollapsedGroups();
    const idx = groups.indexOf(groupName);
    if (idx >= 0) groups.splice(idx, 1);
    else groups.push(groupName);
    localStorage.setItem('coral_collapsed_groups', JSON.stringify(groups));
    renderLiveSessions(state.liveSessions);
}

export function killSessionDirect(name, agentType, sessionId) {
    showConfirmModal('Kill Session', `Kill session "${name}"? This will terminate the agent.`, async () => { try {
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(name)}/kill`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ agent_type: agentType, session_id: sessionId }),
        });
        const result = await resp.json();
        if (result.error) { showToast(result.error, true); return; }
        if (result.changes_artifact_error) {
            showToast(`Killed, but changes.diff could not be saved: ${result.changes_artifact_error}`, true);
        } else {
            showToast(`Killed and saved changes.diff: ${name}`);
        }
        // Mark as done and preserve for history link instead of removing
        const killed = state.liveSessions.find(s => s.session_id === sessionId);
        if (killed) {
            killed.done = true;
            killed.working = false;
            killed.waiting_for_input = false;
            killed.stuck = false;
            killed.sleeping = false;
            state.killedSessions[sessionId] = killed;
        }
        state.liveSessions = state.liveSessions.filter(s => s.session_id !== sessionId);
        if (state.currentSession && state.currentSession.session_id === sessionId) {
            state.currentSession = null;
            showView("welcome-screen");
        }
        renderLiveSessions(state.liveSessions);
    } catch (e) {
        showToast("Failed to kill session", true);
    } });
}

export async function showInfoDirect(name, agentType, sessionId) {
    const { selectLiveSession } = await import('./sessions.js');
    await selectLiveSession(name, agentType, sessionId);
    const { showInfoModal } = await import('./modals.js');
    showInfoModal();
}

export async function attachDirect(name, agentType, sessionId) {
    try {
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(name)}/attach`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ agent_type: agentType, session_id: sessionId }),
        });
        const result = await resp.json();
        if (result.error) showToast(result.error, true);
        else showToast("Terminal opened");
    } catch (e) {
        showToast("Failed to open terminal", true);
    }
}

export async function restartDirect(name, agentType, sessionId) {
    const { selectLiveSession } = await import('./sessions.js');
    await selectLiveSession(name, agentType, sessionId);
    const { restartSession } = await import('./controls.js');
    restartSession();
}

// ── Confirm Modal ────────────────────────────────────────────────────

export function showConfirmModal(title, message, onConfirm) {
    document.getElementById("confirm-modal-title").textContent = title;
    document.getElementById("confirm-modal-message").textContent = message;
    const yesBtn = document.getElementById("confirm-modal-yes");
    const newBtn = yesBtn.cloneNode(true);
    yesBtn.parentNode.replaceChild(newBtn, yesBtn);
    newBtn.addEventListener("click", () => {
        hideConfirmModal();
        onConfirm();
    });
    document.getElementById("confirm-modal").style.display = "flex";
}

export function hideConfirmModal() {
    const modal = document.getElementById("confirm-modal");
    modal.style.display = "none";
    // Remove any extra width classes added by export modal
    const content = modal.querySelector('.modal-content');
    if (content) content.classList.remove('modal-content-wide', 'modal-content-extra-wide');
}

export function showPromptModal(title, label, defaultValue, onConfirm) {
    document.getElementById("prompt-modal-title").textContent = title;
    const labelEl = document.getElementById("prompt-modal-label");
    if (labelEl) labelEl.textContent = label || '';
    const input = document.getElementById("prompt-modal-input");
    input.value = defaultValue || '';
    const okBtn = document.getElementById("prompt-modal-ok");
    const newBtn = okBtn.cloneNode(true);
    okBtn.parentNode.replaceChild(newBtn, okBtn);
    const modal = document.getElementById("prompt-modal");
    const submit = () => {
        const val = input.value.trim();
        if (!val) { input.focus(); return; }
        hidePromptModal();
        onConfirm(val);
    };
    newBtn.addEventListener("click", submit);
    input.onkeydown = (e) => { if (e.key === 'Enter') submit(); if (e.key === 'Escape') hidePromptModal(); };
    modal.onclick = (e) => { if (e.target === modal) hidePromptModal(); };
    modal.style.display = "flex";
    setTimeout(() => input.focus(), 50);
}

export function hidePromptModal() {
    document.getElementById("prompt-modal").style.display = "none";
}

export function showAlertModal(title, message, onClose) {
    document.getElementById("alert-modal-title").textContent = title;
    document.getElementById("alert-modal-message").textContent = message;
    const okBtn = document.getElementById("alert-modal-ok");
    const newBtn = okBtn.cloneNode(true);
    okBtn.parentNode.replaceChild(newBtn, okBtn);
    const modal = document.getElementById("alert-modal");
    newBtn.addEventListener("click", () => {
        hideAlertModal();
        onClose?.();
    });
    modal.onclick = (e) => { if (e.target === modal) hideAlertModal(); };
    modal.style.display = "flex";
}

export function hideAlertModal() {
    const modal = document.getElementById("alert-modal");
    if (!modal) return;
    modal.style.display = "none";
}

export function dismissKilledSession(sessionId) {
    delete state.killedSessions[sessionId];
    state.liveSessions = state.liveSessions.filter(s => s.session_id !== sessionId);
    renderLiveSessions(state.liveSessions);
}

export function dismissBoardKilled(boardName) {
    const killedIds = Object.keys(state.killedSessions).filter(sid => {
        const s = state.killedSessions[sid];
        return s && s.board_project === boardName;
    });
    for (const sid of killedIds) {
        delete state.killedSessions[sid];
    }
    state.liveSessions = state.liveSessions.filter(s => !killedIds.includes(s.session_id));
    renderLiveSessions(state.liveSessions);
}

export function copyFolderPath(path) {
    if (!path) return;
    navigator.clipboard.writeText(path).then(() => {
        showToast("Copied path to clipboard");
    });
}

export async function killGroup(groupName) {
    const groupSessions = state.liveSessions.filter(s => (s.name || 'unknown') === groupName);
    if (!groupSessions.length) return;

    showConfirmModal(
        "Kill All Agents",
        `Kill all ${groupSessions.length} agent(s) in "${groupName}"? This will terminate them.`,
        async () => {
            for (const s of groupSessions) {
                try {
                    await fetch(`/api/sessions/live/${encodeURIComponent(s.name)}/kill`, {
                        method: "POST",
                        headers: { "Content-Type": "application/json" },
                        body: JSON.stringify({ agent_type: s.agent_type, session_id: s.session_id }),
                    });
                    // Preserve killed session for history link
                    s.done = true;
                    s.working = false;
                    s.waiting_for_input = false;
                    s.stuck = false;
                    s.sleeping = false;
                    state.killedSessions[s.session_id] = s;
                } catch (e) {
                    console.error(`Failed to kill ${s.name}:`, e);
                }
            }
            const killedIds = new Set(groupSessions.map(s => s.session_id));
            state.liveSessions = state.liveSessions.filter(s => !killedIds.has(s.session_id));
            renderLiveSessions(state.liveSessions);
        }
    );
}

export async function killBoard(boardName) {
    const boardSessions = state.liveSessions.filter(s => s.board_project === boardName);
    if (!boardSessions.length) return;

    showConfirmModal(
        "Kill Team",
        `Kill all ${boardSessions.length} agent(s) on board "${boardName}"? This will terminate them.`,
        async () => {
            for (const s of boardSessions) {
                try {
                    await fetch(`/api/sessions/live/${encodeURIComponent(s.name)}/kill`, {
                        method: "POST",
                        headers: { "Content-Type": "application/json" },
                        body: JSON.stringify({ agent_type: s.agent_type, session_id: s.session_id }),
                    });
                    s.done = true;
                    s.working = false;
                    s.waiting_for_input = false;
                    s.stuck = false;
                    s.sleeping = false;
                    state.killedSessions[s.session_id] = s;
                } catch (e) {
                    console.error(`Failed to kill ${s.name}:`, e);
                }
            }
            const killedIds = new Set(boardSessions.map(s => s.session_id));
            state.liveSessions = state.liveSessions.filter(s => !killedIds.has(s.session_id));
            renderLiveSessions(state.liveSessions);
        }
    );
}

function _buildTeamTemplateFromBoard(boardName) {
    const boardSessions = (state.liveSessions || []).filter(s => s.board_project === boardName);
    if (!boardSessions.length) return null;

    const agents = [];
    for (const s of boardSessions) {
        const agent = {
            name: s.display_name || s.board_job_title || s.name,
            prompt: s.prompt || '',
        };
        if (s.agent_type) agent.agent_type = s.agent_type;
        if (s.model) agent.model = s.model;
        if (s.capabilities) agent.capabilities = s.capabilities;
        agents.push(agent);
    }
    return { name: boardName, agents, flags: '' };
}

export async function shareAgentTeam(boardName) {
    const tmpl = _buildTeamTemplateFromBoard(boardName);
    if (!tmpl) {
        showToast("No agents found on this board", "error");
        return;
    }

    const template = {
        version: 1,
        type: "coral-team-templates",
        templates: [tmpl],
    };

    const jsonStr = JSON.stringify(template, null, 2);
    const filename = `coral-team-${boardName.replace(/[^a-zA-Z0-9-_]/g, "_")}.json`;

    // Show export modal with JSON content
    const modal = document.createElement('div');
    modal.className = 'modal';
    modal.style.display = 'flex';
    modal.innerHTML = `
        <div class="modal-content" style="width:600px">
            <div class="modal-header">
                <h3>Export Team: ${escapeHtml(boardName)}</h3>
                <p style="color:var(--text-secondary);font-size:13px;margin:6px 0 0">${tmpl.agents.length} agents</p>
            </div>
            <div class="modal-body">
                <textarea readonly style="width:100%;height:260px;font-family:monospace;font-size:12px;resize:vertical;background:var(--bg-tertiary);color:var(--text-primary);border:1px solid var(--border);border-radius:8px;padding:12px">${escapeHtml(jsonStr)}</textarea>
            </div>
            <div class="modal-actions modal-footer">
                <button class="btn" data-action="close">Close</button>
                <button class="btn" data-action="download">Download</button>
                <button class="btn btn-primary" data-action="copy">Copy to Clipboard</button>
            </div>
        </div>
    `;
    document.body.appendChild(modal);
    modal.addEventListener('click', (e) => {
        const action = e.target.dataset?.action;
        if (action === 'close' || e.target === modal) { modal.remove(); return; }
        if (action === 'copy') {
            navigator.clipboard.writeText(jsonStr).then(() => showToast('Copied to clipboard'));
            return;
        }
        if (action === 'download') {
            const blob = new Blob([jsonStr], { type: 'application/json' });
            const url = URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = url; a.download = filename; a.click();
            URL.revokeObjectURL(url);
            showToast(`Downloaded ${filename}`);
        }
    });
}

export function saveTeamFromSidebar(boardName) {
    showPromptModal('Save Team Template', 'Template name', boardName, async (templateName) => {
        const tmpl = _buildTeamTemplateFromBoard(boardName);
        if (!tmpl) {
            showToast("No agents found on this board", "error");
            return;
        }
        tmpl.name = templateName;

        // Save to user_settings via the settings API
        let existing = [];
        try {
            existing = JSON.parse(state.settings.saved_team_templates || "[]");
        } catch { /* ignore */ }
        const idx = existing.findIndex(t => t.name === templateName);
        if (idx >= 0) existing[idx] = tmpl; else existing.push(tmpl);
        state.settings.saved_team_templates = JSON.stringify(existing);
        await fetch("/api/settings", {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ saved_team_templates: state.settings.saved_team_templates }),
        });
        showToast(`Saved template "${templateName}" (${tmpl.agents.length} agents)`);
    });
}

export function toggleTeamSleep(boardName, action) {
    const doIt = async () => {
        try {
            const resp = await fetch(`/api/sessions/live/team/${encodeURIComponent(boardName)}/${action}`, { method: 'POST' });
            const data = await resp.json();
            const sleeping = !!data.sleeping;
            for (const s of state.liveSessions) {
                if (s.board_project === boardName) s.sleeping = sleeping;
            }
            renderLiveSessions(state.liveSessions);
            showToast(sleeping ? `Team "${boardName}" is now sleeping` : `Team "${boardName}" is awake`);
        } catch (e) {
            showToast('Failed to toggle team sleep', true);
        }
    };
    if (action === 'sleep') {
        showConfirmModal('Sleep Team', `Put all agents on "${boardName}" to sleep?`, doIt);
    } else {
        doIt();
    }
}

export function sleepAllAgents() {
    showConfirmModal('Sleep All', 'Put all agents to sleep?', async () => {
        try {
            const resp = await fetch('/api/sessions/live/sleep-all', { method: 'POST' });
            const data = await resp.json();
            if (data.ok) {
                for (const s of state.liveSessions) s.sleeping = true;
                renderLiveSessions(state.liveSessions);
                showToast(`All agents are now sleeping (${data.sessions_affected} affected)`);
            }
        } catch (e) {
            showToast('Failed to sleep all agents', true);
        }
    });
}

export function wakeAllAgents() {
    showConfirmModal('Wake All', 'Wake all sleeping agents?', async () => {
        try {
            const resp = await fetch('/api/sessions/live/wake-all', { method: 'POST' });
            const data = await resp.json();
            if (data.ok) {
                for (const s of state.liveSessions) s.sleeping = false;
                renderLiveSessions(state.liveSessions);
                showToast(`All agents are awake (${data.sessions_relaunched} relaunched)`);
            }
        } catch (e) {
            showToast('Failed to wake agents', true);
        }
    });
}

export function toggleAgentSleep(name, agentType, sessionId, action) {
    const doIt = async () => {
        try {
            const resp = await fetch(`/api/sessions/live/${encodeURIComponent(sessionId)}/${action}`, { method: 'POST' });
            const data = await resp.json();
            if (!data.ok) { showToast(data.error || 'Failed to toggle sleep', true); return; }
            const sleeping = !!data.sleeping;
            const s = state.liveSessions.find(s => s.session_id === sessionId);
            if (s) s.sleeping = sleeping;
            renderLiveSessions(state.liveSessions);
            showToast(sleeping ? `"${name}" is now sleeping` : `"${name}" is awake`);
        } catch (e) {
            showToast('Failed to toggle agent sleep', true);
        }
    };
    if (action === 'sleep') {
        showConfirmModal('Sleep Agent', `Put "${name}" to sleep?`, doIt);
    } else {
        doIt();
    }
}

// Drag-and-drop state
let _draggedSid = null;

function _shortPath(fullPath, segments = 2) {
    if (!fullPath) return '';
    let p = fullPath.replace(/\/+$/, '');
    // Try to replace common home dir with tilde
    p = p.replace(/^\/Users\/[^\/]+/, '~').replace(/^\/home\/[^\/]+/, '~');
    const parts = p.split('/');
    if (parts.length <= segments) return p;
    return '…/' + parts.slice(-segments).join('/');
}

function _renderSessionItem(s, groupName, isCompact, collapsed, teamDefaultDir) {
    const dotClass = getDotClass(s);
    const isActive = state.currentSession && state.currentSession.type === "live" && state.currentSession.session_id === s.session_id;

    // Spec: remove agent type from the default row layout
    const typeTag = "";

    // Directory override chip
    const hasDirOverride = teamDefaultDir && s.working_directory && s.working_directory !== teamDefaultDir;
    const dirChip = hasDirOverride ? ` <span class="agent-dir-chip" title="${escapeAttr(s.working_directory)}">${escapeHtml(_shortPath(s.working_directory, 1))}</span>` : "";

    // Branch is shown at folder level, not per agent
    const branchTag = "";
    const waitingBadge = s.waiting_for_input
        ? ' <span class="badge waiting-badge">Needs input</span>'
        : (s.not_started
            ? ' <span class="badge waiting-badge">Check terminal</span>'
            : '');
    const isTerminal = s.agent_type === "terminal";
    const sid = s.session_id ? escapeAttr(s.session_id) : "";
    const goalText = (isActive && s.summary) ? escapeHtml(s.summary) : null;
    const goal = goalText || "";
    const goalBtn = (!goalText && !isTerminal) ? `<button class="sidebar-goal-btn" onclick="event.stopPropagation(); requestGoal('${escapeAttr(s.name)}', '${escapeAttr(s.agent_type)}', '${sid}')" title="Generate Goal"><span class="material-icons" style="font-size:16px">auto_awesome</span></button>` : "";
    const isDone = !!state.killedSessions?.[s.session_id];
    const displayLabel = s.display_name || (isCompact && s.board_job_title) || (isTerminal ? "Terminal" : "Agent");
    const mobileStatus = getMobileStatusChip(s);
    const lastActivity = formatStaleness(s.staleness_seconds);
    const needsAttention = !!(s.waiting_for_input || s.stuck || s.not_started);
    const unreadBoardBadge = s.board_unread > 0
        ? `<span class="session-mobile-meta-pill">${s.board_unread} unread</span>`
        : '';
    const activityLabel = (s.waiting_for_input || s.not_started) ? "Waiting since" : "Last activity";
    const mobileAttentionBanner = s.waiting_for_input
        ? '<div class="session-mobile-banner">Waiting for your input</div>'
        : (s.not_started
            ? '<div class="session-mobile-banner">Nothing yet — open the terminal</div>'
            : (s.stuck ? '<div class="session-mobile-banner error">Session needs attention</div>' : ''));
    const avatar = _renderAvatar(s, dotClass);
    const _sleepingMenu = `
            <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); toggleAgentSleep('${escapeAttr(s.name)}', '${escapeAttr(s.agent_type)}', '${sid}', 'wake')">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3a6 6 0 1 0 0 10 5 5 0 0 1 0-10z"/></svg>
                Wake
            </button>
            <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); renameAgent('${escapeAttr(s.name)}', '${escapeAttr(s.agent_type)}', '${sid}')">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><path d="M11.5 1.5l3 3L5 14H2v-3z"/></svg>
                Rename
            </button>
            <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); setAgentIcon('${escapeAttr(s.name)}', '${escapeAttr(s.agent_type)}', '${sid}')">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="8" r="6.5"/><path d="M5.5 9.5c0 0 1 1.5 2.5 1.5s2.5-1.5 2.5-1.5"/><circle cx="6" cy="6.5" r="0.5" fill="currentColor" stroke="none"/><circle cx="10" cy="6.5" r="0.5" fill="currentColor" stroke="none"/></svg>
                Set Icon
            </button>
            <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); showInfoDirect('${escapeAttr(s.name)}', '${escapeAttr(s.agent_type)}', '${sid}')">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><circle cx="8" cy="8" r="6.5"/><line x1="8" y1="7" x2="8" y2="11"/><circle cx="8" cy="5" r="0.5" fill="currentColor" stroke="none"/></svg>
                Session Info
            </button>
            <hr class="overflow-menu-divider">
            <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); moveSessionUp('${sid}')">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M8 3v10M4 7l4-4 4 4"/></svg>
                Move Up
            </button>
            <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); moveSessionDown('${sid}')">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M8 13V3M4 9l4 4 4-4"/></svg>
                Move Down
            </button>
            <hr class="overflow-menu-divider">
            <button class="overflow-menu-item overflow-menu-danger" onclick="event.stopPropagation(); closeSidebarKebabs(); killSessionDirect('${escapeAttr(s.name)}', '${escapeAttr(s.agent_type)}', '${sid}')">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="4" y1="4" x2="12" y2="12"/><line x1="12" y1="4" x2="4" y2="12"/></svg>
                Remove Agent
            </button>`;
    const _awakeMenu = `
            <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); attachDirect('${escapeAttr(s.name)}', '${escapeAttr(s.agent_type)}', '${sid}')">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="4,4 8,8 4,12"/><line x1="9" y1="12" x2="13" y2="12"/></svg>
                Attach
            </button>
            <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); restartDirect('${escapeAttr(s.name)}', '${escapeAttr(s.agent_type)}', '${sid}')">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3.5 2v4h4"/><path d="M3.5 6A5.5 5.5 0 1 1 2.5 8"/></svg>
                Restart
            </button>
            <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); renameAgent('${escapeAttr(s.name)}', '${escapeAttr(s.agent_type)}', '${sid}')">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><path d="M11.5 1.5l3 3L5 14H2v-3z"/></svg>
                Rename
            </button>
            <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); setAgentIcon('${escapeAttr(s.name)}', '${escapeAttr(s.agent_type)}', '${sid}')">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="8" r="6.5"/><path d="M5.5 9.5c0 0 1 1.5 2.5 1.5s2.5-1.5 2.5-1.5"/><circle cx="6" cy="6.5" r="0.5" fill="currentColor" stroke="none"/><circle cx="10" cy="6.5" r="0.5" fill="currentColor" stroke="none"/></svg>
                Set Icon
            </button>
            <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); showInfoDirect('${escapeAttr(s.name)}', '${escapeAttr(s.agent_type)}', '${sid}')">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><circle cx="8" cy="8" r="6.5"/><line x1="8" y1="7" x2="8" y2="11"/><circle cx="8" cy="5" r="0.5" fill="currentColor" stroke="none"/></svg>
                Session Info
            </button>
            <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); toggleAgentSleep('${escapeAttr(s.name)}', '${escapeAttr(s.agent_type)}', '${sid}', 'sleep')">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3a6 6 0 1 0 0 10 5 5 0 0 1 0-10z"/></svg>
                Sleep
            </button>
            <hr class="overflow-menu-divider">
            <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); moveSessionUp('${sid}')">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M8 3v10M4 7l4-4 4 4"/></svg>
                Move Up
            </button>
            <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); moveSessionDown('${sid}')">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M8 13V3M4 9l4 4 4-4"/></svg>
                Move Down
            </button>
            <hr class="overflow-menu-divider">
            <button class="overflow-menu-item overflow-menu-danger" onclick="event.stopPropagation(); closeSidebarKebabs(); killSessionDirect('${escapeAttr(s.name)}', '${escapeAttr(s.agent_type)}', '${sid}')">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="4" y1="4" x2="12" y2="12"/><line x1="12" y1="4" x2="4" y2="12"/></svg>
                Kill Session
            </button>`;
    const _doneMenu = `
            <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); selectHistorySession('${sid}')">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><path d="M3 3h10v10H3z"/><path d="M6 6h4"/><path d="M6 9h4"/></svg>
                View History
            </button>
            <hr class="overflow-menu-divider">
            <button class="overflow-menu-item overflow-menu-danger" onclick="event.stopPropagation(); closeSidebarKebabs(); dismissKilledSession('${sid}')">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="4" y1="4" x2="12" y2="12"/><line x1="12" y1="4" x2="4" y2="12"/></svg>
                Dismiss
            </button>`;
    const kebabMenuContent = isDone ? _doneMenu : (s.sleeping ? _sleepingMenu : _awakeMenu);
    const kebabMenu = `<div class="sidebar-kebab-wrapper">
        <button class="sidebar-kebab-btn" onclick="event.stopPropagation(); toggleSidebarKebab(this)" title="More actions">&#x22EE;</button>
        <div class="sidebar-kebab-menu" style="display:none">${kebabMenuContent}
        </div>
    </div>`;
    const tooltip = buildSessionTooltip(s);
    const compactClass = isCompact ? ' session-compact' : '';
    const collapsedClass = collapsed ? ' group-collapsed' : '';
    const sleepingClass = s.sleeping ? ' sleeping' : '';
    const attentionClass = needsAttention ? ' needs-attention' : '';
    const doneClass = isDone ? ' session-done' : '';
    const clickHandler = isDone
        ? `selectHistorySession('${sid}')`
        : `selectLiveSession('${escapeAttr(s.name)}', '${escapeAttr(s.agent_type)}', '${sid}')`;
    return `<li class="session-group-item${isActive ? ' active' : ''}${compactClass}${collapsedClass}${sleepingClass}${attentionClass}${doneClass}"
        draggable="true"
        data-session-id="${sid}"
        data-group="${escapeAttr(groupName)}"
        onclick="${clickHandler}">
        <span class="drag-grip" title="Drag to reorder">&#x2630;</span>
        ${avatar}
        <div class="session-info">
            <div class="session-name-row">
                <span class="session-label">${escapeHtml(displayLabel)}${typeTag}${dirChip}</span>
                <span class="session-name-spacer"></span>
                ${waitingBadge}
                ${goalBtn}
                ${kebabMenu}
            </div>
            ${_renderTokenLine(s)}
            <div class="session-mobile-banner-row">
                ${mobileAttentionBanner}
            </div>
            <div class="session-mobile-meta">
                <span class="session-status-chip ${escapeAttr(mobileStatus.className)}">${escapeHtml(mobileStatus.label)}</span>
                <span class="session-activity-text" title="${escapeAttr(activityLabel)} ${escapeAttr(lastActivity)}">${escapeHtml(mobileStatus.label)}</span>
                ${unreadBoardBadge}
            </div>
            <span class="session-goal${isCompact ? ' session-goal-compact' : ''}">${goal}</span>
            ${branchTag}
            ${isActive && s.status ? `<span class="session-inline-status">${escapeHtml(s.status)}</span>` : ''}
        </div>
        <div class="session-tooltip">${tooltip}</div>
    </li>`;
}

function _renderAgentListWithSubgroups(agents, teamDefaultDir, isCompact, groupName) {
    if (!agents.length) return "";

    // Group agents by directory
    const clusters = {};
    for (const s of agents) {
        const dir = s.working_directory || teamDefaultDir || "";
        if (!clusters[dir]) clusters[dir] = [];
        clusters[dir].push(s);
    }

    const clusterKeys = Object.keys(clusters);
    const numClusters = clusterKeys.length;
    const numDiffer = agents.filter(s => {
        const d = s.working_directory || teamDefaultDir;
        return d && d !== teamDefaultDir;
    }).length;

    // Phase 3 criteria updated per review: numClusters >= 3 or (numClusters >= 2 and numDiffer >= 3)
    const shouldSubgroup = numClusters >= 3 || (numClusters >= 2 && numDiffer >= 3);

    if (!shouldSubgroup) {
        return agents.map(s => _renderSessionItem(s, groupName, isCompact, false, teamDefaultDir)).join('');
    }

    // Sort clusters: put default directory cluster first, then sort by name
    clusterKeys.sort((a, b) => {
        if (a === teamDefaultDir) return -1;
        if (b === teamDefaultDir) return 1;
        return a.localeCompare(b);
    });

    let html = "";
    for (const dir of clusterKeys) {
        const clusterAgents = clusters[dir];
        const isDefault = dir === teamDefaultDir;

        // Render subgroup header with agent count
        const shortDir = _shortPath(dir, 1);
        const fullDir = dir || "No directory";
        const countBadge = ` <span class="session-group-count">${clusterAgents.length}</span>`;
        html += `<li class="agent-subgroup-header" title="${escapeAttr(fullDir)}">
            <svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 4v8a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V6a1 1 0 0 0-1-1H8L6.5 3H3a1 1 0 0 0-1 1z"/></svg>
            <span>${escapeHtml(isDefault ? "Team Root" : (shortDir || "No Directory"))}${countBadge}</span>
        </li>`;

        for (const s of clusterAgents) {
            html += _renderSessionItem(s, groupName, isCompact, false, teamDefaultDir);
        }
    }
    return html;
}

export function toggleGroupByTeam() {
    const current = localStorage.getItem('coral-group-by-team') !== 'false';
    localStorage.setItem('coral-group-by-team', !current ? 'true' : 'false');
    const check = document.getElementById('group-by-team-check-top');
    if (check) check.style.opacity = !current ? '1' : '0.2';
    renderLiveSessions(state.liveSessions);
}

export function renderLiveSessions(sessions) {
    // Merge killed sessions back so they appear as "done" with history links.
    // Use a copy to avoid mutating state.liveSessions.
    const liveIds = new Set(sessions.map(s => s.session_id));
    sessions = [...sessions];
    for (const [sid, ks] of Object.entries(state.killedSessions)) {
        if (!liveIds.has(sid)) {
            sessions.push(ks);
        }
    }

    const list = document.getElementById("live-sessions-list");

    updateSectionVisibility('live-sessions', sessions.length);

    if (!sessions.length) {
        list.innerHTML = '<li class="empty-state">No live sessions</li>';
        return;
    }

    // Sync the Group by Team checkmark
    const groupByTeam = localStorage.getItem('coral-group-by-team') !== 'false';
    const check = document.getElementById('group-by-team-check-top');
    if (check) check.style.opacity = groupByTeam ? '1' : '0.2';

    // Helper to generate a deterministic accent color from a string
    function _boardAccentColor(name) {
        // Check for user-set accent color first
        const custom = _getBoardColor(name);
        if (custom) return custom;
        // Default: hash-based color
        let hash = 0;
        for (let i = 0; i < name.length; i++) hash = ((hash << 5) - hash + name.charCodeAt(i)) | 0;
        const hue = ((hash % 360) + 360) % 360;
        return `hsl(${hue}, 60%, 55%)`;
    }

    let html = "";

    if (groupByTeam) {
    // ── Team-first mode: teams at top level, standalone by folder below ──

    // Step 1: Separate into team (has board_project) vs workflow vs standalone
    const teamGroups = {};
    const workflowGroups = {};
    const standaloneByFolder = {};
    for (const s of sessions) {
        if (s.board_project) {
            if (!teamGroups[s.board_project]) teamGroups[s.board_project] = [];
            teamGroups[s.board_project].push(s);
        } else if (s.workflow_name) {
            const wfKey = s.workflow_name;
            if (!workflowGroups[wfKey]) workflowGroups[wfKey] = [];
            workflowGroups[wfKey].push(s);
        } else {
            const key = s.name || "unknown";
            if (!standaloneByFolder[key]) standaloneByFolder[key] = [];
            standaloneByFolder[key].push(s);
        }
    }

    // Step 2: Render team groups at top level
    // Apply saved group order first, then sort active before sleeping
    const teamEntries = _sortGroups(Object.entries(teamGroups));
    // Secondary sort: if no saved order, put sleeping teams after active
    const hasOrder = _getGroupOrder().length > 0;
    if (!hasOrder) {
        teamEntries.sort((a, b) => {
            const aAllSleeping = a[1].every(s => s.sleeping);
            const bAllSleeping = b[1].every(s => s.sleeping);
            if (aAllSleeping !== bAllSleeping) return aAllSleeping ? 1 : -1;
            return 0;
        });
    }

    for (const [boardName, boardSessions] of teamEntries) {
        const accentColor = _boardAccentColor(boardName);
        const boardCollapsed = _isGroupCollapsed(boardName);
        const bChevron = boardCollapsed ? '&#x25B8;' : '&#x25BE;';
        const boardLink = '';
        const boardWorkDir = boardSessions[0]?.working_directory || '';
        const boardBranchSession = boardSessions.find(s => s.branch);
        const boardBranch = _repoBranch(boardBranchSession?.repo_name, boardBranchSession?.branch);
        const boardIsSleeping = boardSessions.every(s => s.sleeping);
        const boardSleepIcon = boardIsSleeping ? ' <span class="agent-icon" title="Team is sleeping">🌙</span>' : '';
        const sleepLabel = boardIsSleeping ? 'Wake Team' : 'Sleep Team';
        const sleepAction = boardIsSleeping ? 'wake' : 'sleep';
        const bKebab = `<div class="sidebar-kebab-wrapper group-kebab">
            <button class="sidebar-kebab-btn group-kebab-btn" onclick="event.stopPropagation(); toggleSidebarKebab(this)" title="Group actions">&#x22EE;</button>
            <div class="sidebar-kebab-menu" style="display:none">
                <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); showAddAgentToBoard('${escapeAttr(boardName)}', '${escapeAttr(boardWorkDir)}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="8" y1="3" x2="8" y2="13"/><line x1="3" y1="8" x2="13" y2="8"/></svg>
                    Add Agent
                </button>
                <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); launchTerminalToBoard('${escapeAttr(boardName)}', '${escapeAttr(boardWorkDir)}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="12" height="10" rx="1.5"/><polyline points="5 7 7 9 5 11"/><line x1="9" y1="11" x2="11" y2="11"/></svg>
                    Add Terminal
                </button>
                <hr class="overflow-menu-divider">
                <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); saveTeamFromSidebar('${escapeAttr(boardName)}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3h8l2 2v8a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z"/><path d="M6 3v3h4V3"/><rect x="5" y="9" width="6" height="3"/></svg>
                    Save as Template
                </button>
                <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); shareAgentTeam('${escapeAttr(boardName)}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12v1a2 2 0 0 0 2 2h4a2 2 0 0 0 2-2v-1"/><polyline points="8 3 8 10"/><polyline points="5 6 8 3 11 6"/></svg>
                    Share Team
                </button>
                <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); toggleTeamSleep('${escapeAttr(boardName)}', '${sleepAction}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3a6 6 0 1 0 0 10 5 5 0 0 1 0-10z"/></svg>
                    ${sleepLabel}
                </button>
                <hr class="overflow-menu-divider">
                <button class="overflow-menu-item" onclick="event.stopPropagation(); setBoardAccentColor('${escapeAttr(boardName)}'); closeSidebarKebabs()">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="8" r="6.5"/><path d="M8 1.5v13M1.5 8h13"/></svg>
                    Set Color
                </button>
                <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); showTeamTokenUsage('${escapeAttr(boardName)}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 13V5h3v8H2zM7 13V3h3v10H7zM12 13V7h3v6h-3z"/></svg>
                    Token Usage
                </button>
                <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); moveGroupUp('${escapeAttr(boardName)}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M8 3v10M4 7l4-4 4 4"/></svg>
                    Move Up
                </button>
                <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); moveGroupDown('${escapeAttr(boardName)}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M8 13V3M4 9l4 4 4-4"/></svg>
                    Move Down
                </button>
                <hr class="overflow-menu-divider">
                <button class="overflow-menu-item overflow-menu-danger" onclick="event.stopPropagation(); closeSidebarKebabs(); resetTeam('${escapeAttr(boardName)}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M1 8a7 7 0 0 1 12.9-3.8"/><path d="M15 8a7 7 0 0 1-12.9 3.8"/><polyline points="13 1 14 4.2 10.5 4.2"/><polyline points="3 15 2 11.8 5.5 11.8"/></svg>
                    Reset Team
                </button>
                <button class="overflow-menu-item overflow-menu-danger" onclick="event.stopPropagation(); closeSidebarKebabs(); killBoard('${escapeAttr(boardName)}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="4" y1="4" x2="12" y2="12"/><line x1="12" y1="4" x2="4" y2="12"/></svg>
                    Kill All
                </button>
                <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); dismissBoardKilled('${escapeAttr(boardName)}')" title="Remove all killed agents from the sidebar">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h10M5 6V4a3 3 0 0 1 6 0v2M6 10v2M10 10v2M4 6l1 8h6l1-8"/></svg>
                    Dismiss All
                </button>
            </div>
        </div>`;
        const branchLine = boardBranch ? `<div class="board-card-branch-line"><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="5" cy="4" r="2"/><circle cx="5" cy="12" r="2"/><circle cx="12" cy="6" r="2"/><path d="M5 6v4M10.2 5.2 7 8"/></svg> <span class="branch-name">${escapeHtml(boardBranch)}</span><button class="branch-copy-btn" onclick="event.stopPropagation(); navigator.clipboard.writeText('${escapeAttr(boardBranch)}'); this.textContent='✓'; setTimeout(() => this.innerHTML='<svg width=\\'10\\' height=\\'10\\' viewBox=\\'0 0 16 16\\' fill=\\'none\\' stroke=\\'currentColor\\' stroke-width=\\'1.5\\' stroke-linecap=\\'round\\'><rect x=\\'4\\' y=\\'4\\' width=\\'9\\' height=\\'9\\' rx=\\'1\\'/><path d=\\'M4 8H3a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v1\\'/></svg>', 1000)" title="Copy branch name"><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><rect x="4" y="4" width="9" height="9" rx="1"/><path d="M4 8H3a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v1"/></svg></button></div>` : '';
        const teamDirLine = boardWorkDir ? `<div class="board-card-dir" title="${escapeAttr(boardWorkDir)}"><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 4v8a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V6a1 1 0 0 0-1-1H8L6.5 3H3a1 1 0 0 0-1 1z"/></svg> ${escapeHtml(_shortPath(boardWorkDir, 3))}</div>${branchLine}` : '';
        const teamSubline = `<div class="board-card-subline"><svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="9" cy="7" r="3"/><circle cx="17" cy="7" r="3"/><path d="M3 21v-2a4 4 0 0 1 4-4h4a4 4 0 0 1 4 4v2"/><path d="M17 11a4 4 0 0 1 4 4v2"/></svg> ${boardSessions.length} agents<span class="team-token-usage" data-board="${escapeAttr(boardName)}"></span></div>`;
        const sleepingClass = boardIsSleeping ? ' team-sleeping' : '';
        html += `<li class="session-board-card session-board-card-toplevel${sleepingClass}" style="border-left-color: ${accentColor}">
            <div class="session-group-header board-card-header" data-group-name="${escapeAttr(boardName)}" onclick="toggleGroupCollapse('${escapeAttr(boardName)}')">
                <span class="group-chevron">${bChevron}</span><div class="group-header-text"><div class="group-name-line">${escapeHtml(boardName)}${boardSleepIcon}</div>${teamDirLine}${teamSubline}</div><span class="session-name-spacer"></span>${boardLink}${bKebab}
            </div>
            <ul class="board-card-agents${boardCollapsed ? ' board-card-collapsed' : ''}">`;

        // Apply saved order, then always pin orchestrator to top
        const orderedBoard = _sortByOrder(boardSessions);
        orderedBoard.sort((a, b) => {
            const aOrch = (a.display_name || a.board_job_title || '').toLowerCase().includes('orchestrator');
            const bOrch = (b.display_name || b.board_job_title || '').toLowerCase().includes('orchestrator');
            if (aOrch && !bOrch) return -1;
            if (!aOrch && bOrch) return 1;
            return 0;
        });
        html += _renderAgentListWithSubgroups(orderedBoard, boardWorkDir, true, boardName);
        html += `</ul></li>`;
    }

    // Step 2b: Render workflow agent groups
    for (const [wfName, wfSessions] of Object.entries(workflowGroups)) {
        const wfCollapsed = _isGroupCollapsed('wf:' + wfName);
        const wfChevron = wfCollapsed ? '&#x25B8;' : '&#x25BE;';
        const wfRunId = wfSessions[0]?.workflow_run_id || '';
        const wfSubline = `<div class="board-card-subline"><svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 3H5a2 2 0 0 0-2 2v4m6-6h10a2 2 0 0 1 2 2v4M9 3v18m0 0h10a2 2 0 0 0 2-2V9M9 21H5a2 2 0 0 1-2-2V9m0 0h18"/></svg> Workflow · ${wfSessions.length} agent${wfSessions.length !== 1 ? 's' : ''}${wfRunId ? ' · Run #' + wfRunId : ''}</div>`;
        const wfKebab = `<div class="sidebar-kebab-wrapper group-kebab">
            <button class="sidebar-kebab-btn group-kebab-btn" onclick="event.stopPropagation(); toggleSidebarKebab(this)" title="Workflow actions">&#x22EE;</button>
            <div class="sidebar-kebab-menu" style="display:none">
                ${wfRunId ? `<button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); selectWorkflowRun(${wfRunId})">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><path d="M2 8h12M10 4l4 4-4 4"/></svg>
                    View Run
                </button>` : ''}
                <button class="overflow-menu-item overflow-menu-danger" onclick="event.stopPropagation(); closeSidebarKebabs(); killGroup('wf:${escapeAttr(wfName)}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="4" y1="4" x2="12" y2="12"/><line x1="12" y1="4" x2="4" y2="12"/></svg>
                    Kill All
                </button>
            </div>
        </div>`;

        html += `<li class="session-board-card session-board-card-toplevel session-wf-card" style="border-left-color: #d2a8ff">
            <div class="session-group-header board-card-header" data-group-name="wf:${escapeAttr(wfName)}" onclick="toggleGroupCollapse('wf:${escapeAttr(wfName)}')">
                <span class="group-chevron">${wfChevron}</span><div class="group-header-text"><div class="group-name-line"><span class="material-icons" style="font-size:14px;vertical-align:-2px;margin-right:3px;color:#d2a8ff">account_tree</span>${escapeHtml(wfName)}</div>${wfSubline}</div><span class="session-name-spacer"></span>${wfKebab}
            </div>
            <ul class="board-card-agents${wfCollapsed ? ' board-card-collapsed' : ''}">`;

        for (const s of wfSessions) {
            html += _renderSessionItem(s, 'wf:' + wfName, true, wfCollapsed, '');
        }
        html += `</ul></li>`;
    }

    // Step 3: Render standalone agents grouped by folder
    const sortedFolders = _sortGroups(Object.entries(standaloneByFolder));
    for (const [groupName, groupSessions] of sortedFolders) {
        const sorted = _sortByOrder(groupSessions);
        const isMulti = sorted.length > 1;
        const countBadge = isMulti ? ` <span class="session-group-count">${sorted.length}</span>` : "";
        const collapsed = _isGroupCollapsed(groupName);
        const chevron = collapsed ? '&#x25B8;' : '&#x25BE;';
        const groupWorkDirEsc = escapeAttr(sorted[0]?.working_directory || '');
        const groupKebab = `<div class="sidebar-kebab-wrapper group-kebab">
            <button class="sidebar-kebab-btn group-kebab-btn" onclick="event.stopPropagation(); toggleSidebarKebab(this)" title="Group actions">&#x22EE;</button>
            <div class="sidebar-kebab-menu" style="display:none">
                <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); launchDefaultAgent('${groupWorkDirEsc}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="8" y1="3" x2="8" y2="13"/><line x1="3" y1="8" x2="13" y2="8"/></svg>
                    Add Agent
                </button>
                <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); showAddStandaloneAgent('${groupWorkDirEsc}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="5" r="2.5"/><path d="M3 14c0-2.8 2.2-5 5-5s5 2.2 5 5"/><line x1="12" y1="3" x2="12" y2="7"/><line x1="10" y1="5" x2="14" y2="5"/></svg>
                    Add Custom Agent
                </button>
                <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); launchStandaloneTerminal('${groupWorkDirEsc}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="12" height="10" rx="1.5"/><polyline points="5 7 7 9 5 11"/><line x1="9" y1="11" x2="11" y2="11"/></svg>
                    Add Terminal
                </button>
                <hr class="overflow-menu-divider">
                <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); showFolderTagDropdown('${escapeAttr(groupName)}', this.closest('.session-group-header'))">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 8.5V3a1 1 0 0 1 1-1h5.5l5.5 5.5-5.5 5.5L2 8.5z"/><circle cx="5.5" cy="5.5" r="1" fill="currentColor" stroke="none"/></svg>
                    Manage Tags
                </button>
                <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); moveGroupUp('${escapeAttr(groupName)}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M8 3v10"/><path d="M4 7l4-4 4 4"/></svg>
                    Move Up
                </button>
                <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); moveGroupDown('${escapeAttr(groupName)}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M8 13V3"/><path d="M4 9l4 4 4-4"/></svg>
                    Move Down
                </button>
                <button class="overflow-menu-item overflow-menu-danger" onclick="event.stopPropagation(); closeSidebarKebabs(); killGroup('${escapeAttr(groupName)}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="4" y1="4" x2="12" y2="12"/><line x1="12" y1="4" x2="4" y2="12"/></svg>
                    Kill All
                </button>
            </div>
        </div>`;
        const groupBranchSession = sorted.find(s => s.branch);
        const groupBranch = _repoBranch(groupBranchSession?.repo_name, groupBranchSession?.branch);
        const groupBranchLine = groupBranch ? `<div class="group-branch-line"><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M6 3v5a3 3 0 0 0 3 3h1"/><circle cx="6" cy="3" r="1.5"/><circle cx="11" cy="11" r="1.5"/></svg> ${escapeHtml(groupBranch)}</div>` : "";
        const fTags = getFolderTags(groupName);
        const tagDots = renderFolderTagPills(fTags);
        const groupWorkDir = sorted[0]?.working_directory || '';
        const groupDirLine = groupWorkDir ? `<div class="board-card-dir" title="${escapeAttr(groupWorkDir)}"><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 4v8a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V6a1 1 0 0 0-1-1H8L6.5 3H3a1 1 0 0 0-1 1z"/></svg> ${escapeHtml(_shortPath(groupWorkDir, 3))}</div>` : '';
        const copyBtn = `<button class="folder-copy-btn" onclick="event.stopPropagation(); copyFolderPath('${escapeAttr(groupWorkDir)}')" title="Copy path"><svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="5.5" y="5.5" width="8" height="8" rx="1.5"/><path d="M5.5 10.5h-1a1.5 1.5 0 0 1-1.5-1.5v-5a1.5 1.5 0 0 1 1.5-1.5h5a1.5 1.5 0 0 1 1.5 1.5v1"/></svg></button>`;
        html += `<li class="session-group-header" data-group-name="${escapeAttr(groupName)}" onclick="toggleGroupCollapse('${escapeAttr(groupName)}')">
            <span class="group-chevron">${chevron}</span><div class="group-header-text"><div class="group-name-line">${escapeHtml(groupName)}${countBadge}</div>${groupDirLine}${groupBranchLine}</div>${tagDots}<span class="session-name-spacer"></span>${copyBtn}${groupKebab}</li>`;

        if (!collapsed) {
            html += _renderAgentListWithSubgroups(sorted, groupWorkDir, false, groupName);
        }
    }

    } else {
    // ── Folder-first mode: all sessions grouped by folder, boards nested inside ──

    // Group sessions by folder name (primary grouping)
    const groups = {};
    for (const s of sessions) {
        const key = s.name || "unknown";
        if (!groups[key]) groups[key] = [];
        groups[key].push(s);
    }

    const sortedGroups = _sortGroups(Object.entries(groups));
    for (const [groupName, groupSessions] of sortedGroups) {
        const sorted = _sortByOrder(groupSessions);
        const isMulti = sorted.length > 1;
        const countBadge = isMulti ? ` <span class="session-group-count">${sorted.length}</span>` : "";
        const collapsed = _isGroupCollapsed(groupName);
        const chevron = collapsed ? '&#x25B8;' : '&#x25BE;';
        const groupWorkDirEsc = escapeAttr(sorted[0]?.working_directory || '');
        const groupKebab = `<div class="sidebar-kebab-wrapper group-kebab">
            <button class="sidebar-kebab-btn group-kebab-btn" onclick="event.stopPropagation(); toggleSidebarKebab(this)" title="Group actions">&#x22EE;</button>
            <div class="sidebar-kebab-menu" style="display:none">
                <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); launchDefaultAgent('${groupWorkDirEsc}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="8" y1="3" x2="8" y2="13"/><line x1="3" y1="8" x2="13" y2="8"/></svg>
                    Add Agent
                </button>
                <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); showAddStandaloneAgent('${groupWorkDirEsc}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="5" r="2.5"/><path d="M3 14c0-2.8 2.2-5 5-5s5 2.2 5 5"/><line x1="12" y1="3" x2="12" y2="7"/><line x1="10" y1="5" x2="14" y2="5"/></svg>
                    Add Custom Agent
                </button>
                <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); launchStandaloneTerminal('${groupWorkDirEsc}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="12" height="10" rx="1.5"/><polyline points="5 7 7 9 5 11"/><line x1="9" y1="11" x2="11" y2="11"/></svg>
                    Add Terminal
                </button>
                <hr class="overflow-menu-divider">
                <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); showFolderTagDropdown('${escapeAttr(groupName)}', this.closest('.session-group-header'))">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 8.5V3a1 1 0 0 1 1-1h5.5l5.5 5.5-5.5 5.5L2 8.5z"/><circle cx="5.5" cy="5.5" r="1" fill="currentColor" stroke="none"/></svg>
                    Manage Tags
                </button>
                <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); moveGroupUp('${escapeAttr(groupName)}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M8 3v10"/><path d="M4 7l4-4 4 4"/></svg>
                    Move Up
                </button>
                <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); moveGroupDown('${escapeAttr(groupName)}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M8 13V3"/><path d="M4 9l4 4 4-4"/></svg>
                    Move Down
                </button>
                <button class="overflow-menu-item overflow-menu-danger" onclick="event.stopPropagation(); closeSidebarKebabs(); killGroup('${escapeAttr(groupName)}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="4" y1="4" x2="12" y2="12"/><line x1="12" y1="4" x2="4" y2="12"/></svg>
                    Kill All
                </button>
            </div>
        </div>`;
        const groupBranchSession = sorted.find(s => s.branch);
        const groupBranch = _repoBranch(groupBranchSession?.repo_name, groupBranchSession?.branch);
        const groupBranchLine = groupBranch ? `<div class="group-branch-line"><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M6 3v5a3 3 0 0 0 3 3h1"/><circle cx="6" cy="3" r="1.5"/><circle cx="11" cy="11" r="1.5"/></svg> ${escapeHtml(groupBranch)}</div>` : "";
        const fTags = getFolderTags(groupName);
        const tagDots = renderFolderTagPills(fTags);
        const groupWorkDir = sorted[0]?.working_directory || '';
        const groupDirLine = groupWorkDir ? `<div class="board-card-dir" title="${escapeAttr(groupWorkDir)}"><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 4v8a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V6a1 1 0 0 0-1-1H8L6.5 3H3a1 1 0 0 0-1 1z"/></svg> ${escapeHtml(_shortPath(groupWorkDir, 3))}</div>` : '';
        const copyBtn = `<button class="folder-copy-btn" onclick="event.stopPropagation(); copyFolderPath('${escapeAttr(groupWorkDir)}')" title="Copy path"><svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="5.5" y="5.5" width="8" height="8" rx="1.5"/><path d="M5.5 10.5h-1a1.5 1.5 0 0 1-1.5-1.5v-5a1.5 1.5 0 0 1 1.5-1.5h5a1.5 1.5 0 0 1 1.5 1.5v1"/></svg></button>`;
        html += `<li class="session-group-header" data-group-name="${escapeAttr(groupName)}" onclick="toggleGroupCollapse('${escapeAttr(groupName)}')">
            <span class="group-chevron">${chevron}</span><div class="group-header-text"><div class="group-name-line">${escapeHtml(groupName)}${countBadge}</div>${groupDirLine}${groupBranchLine}</div>${tagDots}<span class="session-name-spacer"></span>${copyBtn}${groupKebab}</li>`;

        if (collapsed) {
            // Skip rendering items when collapsed
        } else {
            // Sub-group by board within this folder
            const boardSubs = {};
            const unboardedItems = [];
            for (const s of sorted) {
                if (s.board_project) {
                    if (!boardSubs[s.board_project]) boardSubs[s.board_project] = [];
                    boardSubs[s.board_project].push(s);
                } else {
                    unboardedItems.push(s);
                }
            }

            // Render board sub-groups as cards within the folder
            for (const [boardName, boardSessions] of Object.entries(boardSubs)) {
                const accentColor = _boardAccentColor(boardName);
                const boardCollapsed = _isGroupCollapsed(boardName);
                const bChevron = boardCollapsed ? '&#x25B8;' : '&#x25BE;';
                const boardLink = '';
                const boardWorkDir = boardSessions[0]?.working_directory || '';
                const boardBranchSession = boardSessions.find(s => s.branch);
                const boardBranch = _repoBranch(boardBranchSession?.repo_name, boardBranchSession?.branch);
                const boardIsSleeping = boardSessions.some(s => s.sleeping);
                const boardSleepIcon = boardIsSleeping ? ' <span class="agent-icon" title="Team is sleeping">🌙</span>' : '';
                const sleepLabel = boardIsSleeping ? 'Wake Team' : 'Sleep Team';
                const sleepAction = boardIsSleeping ? 'wake' : 'sleep';
                const bKebab = `<div class="sidebar-kebab-wrapper group-kebab">
                    <button class="sidebar-kebab-btn group-kebab-btn" onclick="event.stopPropagation(); toggleSidebarKebab(this)" title="Group actions">&#x22EE;</button>
                    <div class="sidebar-kebab-menu" style="display:none">
                        <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); showAddAgentToBoard('${escapeAttr(boardName)}', '${escapeAttr(boardWorkDir)}')">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="8" y1="3" x2="8" y2="13"/><line x1="3" y1="8" x2="13" y2="8"/></svg>
                            Add Agent
                        </button>
                        <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); launchTerminalToBoard('${escapeAttr(boardName)}', '${escapeAttr(boardWorkDir)}')">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="12" height="10" rx="1.5"/><polyline points="5 7 7 9 5 11"/><line x1="9" y1="11" x2="11" y2="11"/></svg>
                            Add Terminal
                        </button>
                        <hr class="overflow-menu-divider">
                        <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); saveTeamFromSidebar('${escapeAttr(boardName)}')">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3h8l2 2v8a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z"/><path d="M6 3v3h4V3"/><rect x="5" y="9" width="6" height="3"/></svg>
                            Save as Template
                        </button>
                        <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); shareAgentTeam('${escapeAttr(boardName)}')">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12v1a2 2 0 0 0 2 2h4a2 2 0 0 0 2-2v-1"/><polyline points="8 3 8 10"/><polyline points="5 6 8 3 11 6"/></svg>
                            Share Team
                        </button>
                        <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); toggleTeamSleep('${escapeAttr(boardName)}', '${sleepAction}')">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3a6 6 0 1 0 0 10 5 5 0 0 1 0-10z"/></svg>
                            ${sleepLabel}
                        </button>
                        <hr class="overflow-menu-divider">
                        <button class="overflow-menu-item" onclick="event.stopPropagation(); setBoardAccentColor('${escapeAttr(boardName)}'); closeSidebarKebabs()">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="8" r="6.5"/><path d="M8 1.5v13M1.5 8h13"/></svg>
                            Set Color
                        </button>
                        <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); showTeamTokenUsage('${escapeAttr(boardName)}')">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 13V5h3v8H2zM7 13V3h3v10H7zM12 13V7h3v6h-3z"/></svg>
                            Token Usage
                        </button>
                        <hr class="overflow-menu-divider">
                        <button class="overflow-menu-item overflow-menu-danger" onclick="event.stopPropagation(); closeSidebarKebabs(); killBoard('${escapeAttr(boardName)}')">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="4" y1="4" x2="12" y2="12"/><line x1="12" y1="4" x2="4" y2="12"/></svg>
                            Kill All
                        </button>
                        <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); dismissBoardKilled('${escapeAttr(boardName)}')" title="Remove all killed agents from the sidebar">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h10M5 6V4a3 3 0 0 1 6 0v2M6 10v2M10 10v2M4 6l1 8h6l1-8"/></svg>
                            Dismiss All
                        </button>
                    </div>
                </div>`;
                const branchLine = boardBranch ? `<div class="board-card-branch-line"><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="5" cy="4" r="2"/><circle cx="5" cy="12" r="2"/><circle cx="12" cy="6" r="2"/><path d="M5 6v4M10.2 5.2 7 8"/></svg> <span class="branch-name">${escapeHtml(boardBranch)}</span><button class="branch-copy-btn" onclick="event.stopPropagation(); navigator.clipboard.writeText('${escapeAttr(boardBranch)}'); this.textContent='✓'; setTimeout(() => this.innerHTML='<svg width=\\'10\\' height=\\'10\\' viewBox=\\'0 0 16 16\\' fill=\\'none\\' stroke=\\'currentColor\\' stroke-width=\\'1.5\\' stroke-linecap=\\'round\\'><rect x=\\'4\\' y=\\'4\\' width=\\'9\\' height=\\'9\\' rx=\\'1\\'/><path d=\\'M4 8H3a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v1\\'/></svg>', 1000)" title="Copy branch name"><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><rect x="4" y="4" width="9" height="9" rx="1"/><path d="M4 8H3a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v1"/></svg></button></div>` : '';
                const teamDirLine = boardWorkDir ? `<div class="board-card-dir" title="${escapeAttr(boardWorkDir)}"><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 4v8a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V6a1 1 0 0 0-1-1H8L6.5 3H3a1 1 0 0 0-1 1z"/></svg> ${escapeHtml(_shortPath(boardWorkDir, 3))}</div>${branchLine}` : '';
                const teamSubline = `<div class="board-card-subline"><svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="9" cy="7" r="3"/><circle cx="17" cy="7" r="3"/><path d="M3 21v-2a4 4 0 0 1 4-4h4a4 4 0 0 1 4 4v2"/><path d="M17 11a4 4 0 0 1 4 4v2"/></svg> ${boardSessions.length} agents</div>`;
                html += `<li class="session-board-card" style="border-left-color: ${accentColor}">
                    <div class="session-group-header board-card-header" onclick="toggleGroupCollapse('${escapeAttr(boardName)}')">
                        <span class="group-chevron">${bChevron}</span><div class="group-header-text"><div class="group-name-line">${escapeHtml(boardName)}${boardSleepIcon} <span class="session-group-count">${boardSessions.length}</span></div>${teamDirLine}${teamSubline}</div><span class="session-name-spacer"></span>${boardLink}${bKebab}
                    </div>
                    <ul class="board-card-agents${boardCollapsed ? ' board-card-collapsed' : ''}">`;
                const orderedBoardNested = _sortByOrder(boardSessions);
                orderedBoardNested.sort((a, b) => {
                    const aOrch = (a.display_name || a.board_job_title || '').toLowerCase().includes('orchestrator');
                    const bOrch = (b.display_name || b.board_job_title || '').toLowerCase().includes('orchestrator');
                    if (aOrch && !bOrch) return -1;
                    if (!aOrch && bOrch) return 1;
                    return 0;
                });
                html += _renderAgentListWithSubgroups(orderedBoardNested, boardWorkDir, true, groupName);
                html += `</ul></li>`;
            }

            // Render unboarded items as flat list with subgroups
            html += _renderAgentListWithSubgroups(unboardedItems, groupWorkDir, false, groupName);
        }
    }

    } // end if/else groupByTeam

    list.innerHTML = html;

    // Attach hover listeners for fixed-position tooltips
    for (const item of list.querySelectorAll(".session-group-item")) {
        const tip = item.querySelector(".session-tooltip");
        if (!tip) continue;
        item.addEventListener("mouseenter", () => {
            const rect = item.getBoundingClientRect();
            tip.style.left = rect.right + 8 + "px";
            tip.style.top = rect.top + "px";
            tip.style.display = "block";
        });
        item.addEventListener("mouseleave", () => {
            tip.style.display = "none";
        });
    }

    // Attach drag-and-drop listeners for reordering
    _attachDragListeners(list);

    // Populate team token usage (non-blocking)
    document.querySelectorAll('.team-token-usage[data-board]').forEach(el => {
        getTeamTokenUsage(el.dataset.board).then(text => {
            if (text) el.textContent = ' · ' + text;
        });
    });

    // Sync mobile agent list
    syncMobileAgentList();
}

function _attachDragListeners(list) {
    const items = list.querySelectorAll(".session-group-item[draggable]");

    // Allow drops inside nested board-card-agents containers
    for (const container of list.querySelectorAll(".board-card-agents")) {
        container.addEventListener("dragover", (e) => {
            e.preventDefault();
            e.dataTransfer.dropEffect = "move";
        });
    }

    for (const item of items) {
        item.addEventListener("dragstart", (e) => {
            e.stopPropagation(); // Prevent board card from intercepting
            _draggedSid = item.dataset.sessionId;
            item.classList.add("dragging");
            e.dataTransfer.effectAllowed = "move";
            // Use a minimal drag image to avoid tooltip flash
            e.dataTransfer.setData("text/plain", _draggedSid);
        });

        item.addEventListener("dragend", () => {
            item.classList.remove("dragging");
            _draggedSid = null;
            // Clear all drop indicators
            for (const el of list.querySelectorAll(".drag-over")) {
                el.classList.remove("drag-over");
            }
        });

        item.addEventListener("dragover", (e) => {
            e.preventDefault();
            if (!_draggedSid || _draggedSid === item.dataset.sessionId) return;
            // Only allow reorder within the same group
            const draggedEl = list.querySelector(`[data-session-id="${_draggedSid}"]`);
            if (!draggedEl || draggedEl.dataset.group !== item.dataset.group) return;
            e.dataTransfer.dropEffect = "move";
            item.classList.add("drag-over");
        });

        item.addEventListener("dragleave", () => {
            item.classList.remove("drag-over");
        });

        item.addEventListener("drop", (e) => {
            e.preventDefault();
            e.stopPropagation();
            item.classList.remove("drag-over");
            if (!_draggedSid || _draggedSid === item.dataset.sessionId) return;

            const group = item.dataset.group;
            const draggedEl = list.querySelector(`[data-session-id="${_draggedSid}"]`);
            if (!draggedEl || draggedEl.dataset.group !== group) return;

            // Gather current order of session_ids in this group
            const groupItems = [...list.querySelectorAll(".session-group-item")].filter(el => el.dataset.group === group);
            const ids = groupItems.map(el => el.dataset.sessionId);

            // Remove dragged from its position and insert before drop target
            const fromIdx = ids.indexOf(_draggedSid);
            const toIdx = ids.indexOf(item.dataset.sessionId);
            if (fromIdx === -1 || toIdx === -1) return;

            ids.splice(fromIdx, 1);
            ids.splice(toIdx, 0, _draggedSid);

            // Save all session IDs in current DOM order (preserves other groups)
            const allItems = [...list.querySelectorAll(".session-group-item")];
            const allIds = allItems.map(el => el.dataset.sessionId);
            // Replace the current group's IDs with the reordered ones
            const groupSet = new Set(ids);
            const merged = [];
            let groupInserted = false;
            for (const sid of allIds) {
                if (groupSet.has(sid)) {
                    if (!groupInserted) {
                        merged.push(...ids);
                        groupInserted = true;
                    }
                } else {
                    merged.push(sid);
                }
            }
            if (!groupInserted) merged.push(...ids);

            // Save and re-render
            _saveSessionOrder(merged);
            renderLiveSessions(state.liveSessions);
        });
    }
}

export function renderHistorySessions(sessions, total, page, pageSize) {
    const list = document.getElementById("history-sessions-list");
    state.historySessionsList = sessions || [];

    updateSectionVisibility('history', total || (sessions ? sessions.length : 0));

    if (!sessions || !sessions.length) {
        list.innerHTML = '<li class="empty-state">No history found</li>';
        renderPaginationControls(0, 1, 50);
        return;
    }

    list.innerHTML = sessions.map(s => {
        const isGroup = s.type === 'group' || (s.session_id && s.session_id.startsWith('board:'));
        if (isGroup) {
            const projectName = s.title || s.session_id.replace(/^board:/, '');
            const label = projectName;
            const truncated = label.length > 40 ? label.substring(0, 40) + "..." : label;
            const isActive = state.currentSession && state.currentSession.type === "board" && state.currentSession.name === projectName;
            const timeStr = s.updated_at ? formatShortTime(s.updated_at) : "";
            const timeTag = timeStr ? `<span class="session-time">${escapeHtml(timeStr)}</span>` : "";
            const countInfo = s.message_count != null ? `${s.message_count} msgs` : "";
            const subsInfo = s.subscriber_count != null ? `${s.subscriber_count} members` : "";
            const meta = [countInfo, subsInfo].filter(Boolean).join(' · ');
            return `<li class="${isActive ? 'active' : ''}" onclick="selectBoardProject('${escapeAttr(projectName)}')">
                <div class="session-row-top">${timeTag}<span class="badge board-badge">Group</span></div>
                <div class="session-row-mid"><span class="session-label" title="${escapeHtml(label)}">${escapeHtml(truncated)}</span></div>
                ${meta ? `<div class="session-row-bottom"><span class="session-meta">${escapeHtml(meta)}</span></div>` : ""}
            </li>`;
        }
        const label = s.summary_title || s.summary || s.session_id;
        const truncated = label.length > 40 ? label.substring(0, 40) + "..." : label;
        const isActive = state.currentSession && state.currentSession.type === "history" && state.currentSession.name === s.session_id;
        const typeTag = s.source_type === "gemini" ? '<span class="badge gemini">gemini</span>' : "";
        const agentName = s.display_name || s.agent_name || "";
        const agentTag = agentName ? `<span class="sidebar-agent-name">${escapeHtml(agentName)}</span>` : "";
        const sourceTypeTag = s.source_type ? `<span class="sidebar-source-type">${escapeHtml(s.source_type)}</span>` : "";
        const branchDisplay = _repoBranch(s.repo_name, s.branch);
        const branchTag = branchDisplay ? `<span class="sidebar-branch">${escapeHtml(branchDisplay)}</span>` : "";
        const tagDots = s.tags ? renderSidebarTagDots(s.tags) : "";
        const timeStr = s.last_timestamp ? formatShortTime(s.last_timestamp) : "";
        const timeTag = timeStr ? `<span class="session-time">${escapeHtml(timeStr)}</span>` : "";
        const durStr = s.duration_sec != null ? formatDuration(s.duration_sec) : '';
        const durTag = durStr ? `<span class="session-dur">${escapeHtml(durStr)}</span>` : '';
        const bottomMeta = [agentTag, sourceTypeTag, branchTag].filter(Boolean).join('');
        return `<li class="${isActive ? 'active' : ''}" onclick="selectHistorySession('${escapeAttr(s.session_id)}')">
            <div class="session-row-top">${timeTag}${durTag}${typeTag}${tagDots}</div>
            <div class="session-row-mid"><span class="session-label" title="${escapeHtml(label)}">${escapeHtml(truncated)}</span></div>
            ${bottomMeta ? `<div class="session-row-bottom">${bottomMeta}</div>` : ""}
        </li>`;
    }).join("");

    if (total !== undefined) {
        renderPaginationControls(total, page || 1, pageSize || 50);
    }
}

export function renderPaginationControls(total, page, pageSize) {
    let container = document.getElementById("history-pagination");
    if (!container) {
        // Create pagination container after the history list
        const list = document.getElementById("history-sessions-list");
        container = document.createElement("div");
        container.id = "history-pagination";
        container.className = "pagination-controls";
        list.parentNode.appendChild(container);
    }

    const totalPages = Math.max(1, Math.ceil(total / pageSize));
    if (totalPages <= 1) {
        container.innerHTML = `<span class="pagination-info">${total} session${total !== 1 ? 's' : ''}</span>`;
        return;
    }

    container.innerHTML = `
        <button class="btn btn-small" ${page <= 1 ? 'disabled' : ''} onclick="loadHistoryPage(${page - 1})">Prev</button>
        <span class="pagination-info">${page} / ${totalPages} (${total})</span>
        <button class="btn btn-small" ${page >= totalPages ? 'disabled' : ''} onclick="loadHistoryPage(${page + 1})">Next</button>
    `;
}

function stripPulseLines(text) {
    return text.replace(/^\|\|PULSE:(STATUS|SUMMARY|CONFIDENCE)\s[^\|]*\|\|$/gm, '').replace(/\n{3,}/g, '\n\n');
}

export function renderHistoryChat(messages) {
    const container = document.getElementById("history-messages");
    container.innerHTML = "";

    for (const entry of messages) {
        const type = entry.type || "unknown";
        // Handle both normalized format (content/text on entry) and raw JSONL (nested under entry.message)
        const msg = entry.message || entry;
        let content = "";

        if (typeof msg.content === "string") {
            content = msg.content;
        } else if (typeof msg.text === "string") {
            content = msg.text;
        } else if (Array.isArray(msg.content)) {
            content = msg.content
                .filter(b => b.type === "text")
                .map(b => b.text)
                .join("\n");
        }

        if (!content.trim()) continue;

        const isHuman = type === "human" || type === "user";
        const bubbleClass = isHuman ? "human" : "assistant";
        const roleLabel = isHuman ? "You" : "Assistant";

        const bubble = document.createElement("div");
        bubble.className = `chat-bubble ${bubbleClass}`;

        let messageHtml;
        if (isHuman) {
            messageHtml = escapeHtml(content);
        } else {
            const cleaned = stripPulseLines(content);
            const rawHtml = marked.parse(cleaned);
            messageHtml = typeof DOMPurify !== 'undefined' ? DOMPurify.sanitize(rawHtml) : rawHtml;
        }

        bubble.innerHTML = `
            <div class="role-label">${roleLabel}</div>
            <div class="message-text${!isHuman ? " markdown-body" : ""}">${messageHtml}</div>
            ${isHuman ? `<button class="edit-btn" onclick="editAndResubmit(this)">Edit & Resubmit</button>` : ""}
        `;
        container.appendChild(bubble);
    }

    container.scrollTop = container.scrollHeight;
}

export function updateSessionStatus(status) {
    const el = document.getElementById("session-status");
    if (status) {
        el.querySelector(".status-text").textContent = status;
    }
}

function _renderTokenLine(s) {
    const pct = s.context_pct || 0;
    if (pct === 0) return '';
    const ctxWindow = s.context_window || 0;
    const barColor = pct >= 80 ? 'var(--status-error, #f85149)' : pct >= 50 ? 'var(--status-warning, #d29922)' : 'var(--text-muted, #8b949e)';
    const titleText = ctxWindow > 0 ? `${_formatTokens(Math.round(ctxWindow * pct / 100))} / ${_formatTokens(ctxWindow)} tokens (${pct}%)` : `Context: ${pct}%`;
    return `<span class="session-context-bar" title="${titleText}">
        <span class="context-bar-track"><span class="context-bar-fill" style="width:${pct}%;background:${barColor}"></span></span>
        <span class="context-bar-label">${pct}%</span>
    </span>`;
}

function _formatTokens(n) {
    if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M';
    if (n >= 1000) return (n / 1000).toFixed(1) + 'K';
    return String(n);
}

function _formatCost(c) {
    if (c < 0.01) return '$' + c.toFixed(4);
    return '$' + c.toFixed(2);
}

export async function updateTokenUsage(sessionId) {
    const el = document.getElementById('session-token-usage');
    if (!el) return;
    if (!sessionId) { el.style.display = 'none'; return; }

    try {
        const resp = await fetch(`/api/token-usage?session_id=${encodeURIComponent(sessionId)}`);
        if (!resp.ok) { el.style.display = 'none'; return; }
        const data = await resp.json();
        if (!data.totals.total_tokens || data.totals.total_tokens === 0) { el.style.display = 'none'; return; }

        const parts = [_formatTokens(data.totals.total_tokens) + ' tokens'];
        if (data.totals.cost_usd > 0) parts.push(_formatCost(data.totals.cost_usd));
        el.textContent = parts.join(' · ');
        el.style.display = '';
    } catch {
        el.style.display = 'none';
    }
}

export async function updateHistoryTokenUsage(sessionId) {
    const container = document.getElementById('history-token-usage');
    if (!container) return;
    if (!sessionId) { container.style.display = 'none'; return; }

    try {
        const resp = await fetch(`/api/token-usage?session_id=${encodeURIComponent(sessionId)}`);
        if (!resp.ok) { container.style.display = 'none'; return; }
        const data = await resp.json();
        const t = data.totals;
        if (!t || !t.total_tokens || t.total_tokens === 0) { container.style.display = 'none'; return; }

        document.getElementById('htu-input').textContent = _formatTokens(t.input_tokens || 0);
        document.getElementById('htu-output').textContent = _formatTokens(t.output_tokens || 0);
        const cache = (t.cache_read_tokens || 0) + (t.cache_write_tokens || 0);
        document.getElementById('htu-cache').textContent = cache > 0 ? _formatTokens(cache) : '—';
        document.getElementById('htu-requests').textContent = String(t.num_sessions || '—');
        document.getElementById('htu-cost').textContent = t.cost_usd > 0 ? _formatCost(t.cost_usd) : '—';
        container.style.display = '';
    } catch {
        container.style.display = 'none';
    }
}

export async function getTeamTokenUsage(boardName) {
    try {
        const resp = await fetch(`/api/token-usage?board_name=${encodeURIComponent(boardName)}`);
        if (!resp.ok) return null;
        const data = await resp.json();
        if (!data.totals.total_tokens || data.totals.total_tokens === 0) return null;
        const parts = [_formatTokens(data.totals.total_tokens) + ' tokens'];
        if (data.totals.cost_usd > 0) parts.push(_formatCost(data.totals.cost_usd));
        if (data.totals.execution_time_sec > 0) parts.push(formatDuration(data.totals.execution_time_sec));
        return parts.join(' · ');
    } catch {
        return null;
    }
}

export async function showTeamTokenUsage(boardName) {
    const modal = document.getElementById('task-detail-modal');
    const titleEl = document.getElementById('task-detail-modal-title');
    const content = document.getElementById('task-detail-content');
    if (!modal || !content) return;

    titleEl.textContent = `Token Usage — ${boardName}`;
    content.innerHTML = '<div style="text-align:center;padding:16px;color:var(--text-muted)">Loading...</div>';
    modal.style.display = '';
    modal.onclick = (e) => { if (e.target === modal) { modal.style.display = 'none'; } };

    // Collect session IDs for this board
    const teamSessions = (state.liveSessions || []).filter(s => s.board_project === boardName);
    if (teamSessions.length === 0) {
        content.innerHTML = '<div style="text-align:center;padding:16px;color:var(--text-muted)">No agents in this team</div>';
        return;
    }

    // Fetch proxy cost per session and message-based execution timing in parallel.
    const [results, usageData] = await Promise.all([Promise.all(teamSessions.map(async (s) => {
        try {
            const resp = await fetch(`/api/proxy/session/${encodeURIComponent(s.session_id)}/cost`);
            if (!resp.ok) return null;
            const data = await resp.json();
            return { name: s.display_name || s.name, ...data };
        } catch { return null; }
    })), fetch(`/api/token-usage?board_name=${encodeURIComponent(boardName)}`)
        .then(resp => resp.ok ? resp.json() : null)
        .catch(() => null)]);

    const agents = results.filter(r => r && r.total_requests > 0);

    // Compute totals
    let totalIn = 0, totalOut = 0, totalCacheR = 0, totalCacheW = 0, totalCost = 0, totalReqs = 0;
    for (const a of agents) {
        totalIn += a.total_input_tokens || 0;
        totalOut += a.total_output_tokens || 0;
        totalCacheR += a.total_cache_read_tokens || 0;
        totalCacheW += a.total_cache_write_tokens || 0;
        totalCost += a.total_cost_usd || 0;
        totalReqs += a.total_requests || 0;
    }

    if (agents.length === 0) {
        content.innerHTML = '<div style="text-align:center;padding:16px;color:var(--text-muted)">No proxy usage recorded for this team</div>';
        return;
    }

    // Render summary + agent table
    const executionTimeRow = usageData?.totals?.execution_time_sec > 0
        ? `<span class="info-token-label">Execution Time</span><span class="info-token-value">${formatDuration(usageData.totals.execution_time_sec)}</span>`
        : '';
    let html = `<div class="info-token-grid" style="margin-bottom:16px">
        <span class="info-token-label">Input</span><span class="info-token-value">${_formatTokens(totalIn)}</span>
        <span class="info-token-label">Output</span><span class="info-token-value">${_formatTokens(totalOut)}</span>
        <span class="info-token-label">Cache Read</span><span class="info-token-value">${_formatTokens(totalCacheR)}</span>
        <span class="info-token-label">Cache Write</span><span class="info-token-value">${_formatTokens(totalCacheW)}</span>
        <span class="info-token-label">Requests</span><span class="info-token-value">${totalReqs}</span>
        <span class="info-token-label">Cost</span><span class="info-token-value">${_formatCost(totalCost)}</span>
        ${executionTimeRow}
    </div>`;

    html += `<table class="cost-table" style="font-size:12px">
        <thead><tr>
            <th>Agent</th>
            <th style="text-align:right">Input</th>
            <th style="text-align:right">Output</th>
            <th style="text-align:right">Cache R</th>
            <th style="text-align:right">Cache W</th>
            <th style="text-align:right">Cost</th>
        </tr></thead><tbody>`;

    for (const a of agents.sort((x, y) => (y.total_cost_usd || 0) - (x.total_cost_usd || 0))) {
        html += `<tr>
            <td style="font-weight:500">${escapeHtml(a.name)}</td>
            <td style="text-align:right">${_formatTokens(a.total_input_tokens || 0)}</td>
            <td style="text-align:right">${_formatTokens(a.total_output_tokens || 0)}</td>
            <td style="text-align:right">${_formatTokens(a.total_cache_read_tokens || 0)}</td>
            <td style="text-align:right">${_formatTokens(a.total_cache_write_tokens || 0)}</td>
            <td style="text-align:right;font-weight:600;color:var(--accent,#58a6ff)">${_formatCost(a.total_cost_usd || 0)}</td>
        </tr>`;
    }
    html += '</tbody></table>';
    content.innerHTML = html;
}

export function updateSessionBranch(branch, repoName) {
    const el = document.getElementById("session-branch");
    const display = _repoBranch(repoName, branch);
    if (display) {
        el.querySelector(".branch-text").textContent = display;
        el.style.display = "";
    } else {
        el.style.display = "none";
    }
}

export function updateWaitingIndicator(s) {
    const dot = document.getElementById("session-status-dot");
    const banner = document.getElementById("waiting-banner");
    if (dot) {
        dot.classList.toggle("waiting", !!(s.waiting_for_input || s.not_started));
        dot.classList.toggle("stuck", !!s.stuck);
        dot.classList.toggle("working", !!s.working);
        dot.classList.toggle("done", !!s.done);
    }
    if (banner) {
        // Two states share this banner. waiting_for_input is a running agent
        // asking a question. not_started is an agent that has never done
        // anything — blocked on a first-run prompt, or simply never given a
        // task; those are indistinguishable from outside the CLI, so the copy
        // claims only what is true of both. We never answer a prompt on the
        // user's behalf: the banner's job is to point at the terminal, where
        // the question is already on screen.
        banner.style.display = (s.waiting_for_input || s.not_started) ? "" : "none";
        if (s.waiting_for_input) {
            banner.className = "waiting-banner";
            banner.textContent = "⏳ Agent is waiting for input";
        } else if (s.not_started) {
            banner.className = "waiting-banner waiting-banner-not-started";
            banner.textContent =
                "⏳ This agent hasn't done anything yet — check the terminal below. Some agents ask permission to trust a folder the first time they run in it.";
        }
    }
}

export function updateSessionSummary(summary) {
    const el = document.getElementById("session-summary");
    if (!el) return;
    if (summary) {
        el.querySelector(".summary-text").textContent = summary;
        el.style.display = "";
    }
    // Don't hide — a null summary from a WebSocket tick shouldn't
    // clear a previously-known goal. The log parser may simply not
    // have found the PULSE:SUMMARY line in the current chunk.
}
