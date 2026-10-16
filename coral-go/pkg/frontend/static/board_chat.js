/* Moved out of render.js as it was (#3795): no behaviour change. */
import { formatHourMinute } from './format.js';
import { OPERATOR_ID, isOperatorId, escapeHtml, showToast, escapeAttr, getAgentColor, externalMessageBadge, hubUserBadge, renderBoardMessageBody, boardAvatar, boardMessageColorStyle } from './utils.js';
import { splitKey, keyLabel, boardFetch } from './server_base.js';
import { groupTaskQueueRuns, renderTaskQueueRun } from './board_system_lines.js';
import { pollEvery } from './overload.js';
import { resolveBoardArtifacts } from './board_artifacts.js';

// ── Board Chat (right-panel tab) ──────────────────────────────────────
let _boardChatTimer = null;
let _activeBoardChat = null;
let _boardChatGen = 0;   // bumped whenever the board chat panel is rebuilt, to drop stale loads

/** The board whose chat the panel currently shows (or null). */
export function getActiveBoardChat() { return _activeBoardChat; }
let _boardChatMessages = [];
let _boardChatTotal = 0;
let _boardChatOffset = 0;   // older messages not loaded yet
// The board the message state above belongs to, and whether its latest page has loaded. The state
// outlives the panel, so switching between agents of one board doesn't load the page again (#3722).
let _boardChatBoard = null;
let _boardChatLoaded = false;
let _boardChatInFlight = 0;   // the panel generation whose load is in flight (0: none)
let _boardChatAgain = false;  // a load was asked for while one was in flight (after a send)
const _BOARD_CHAT_PAGE = 50;
let _boardChatSelectMode = false;
let _boardChatSelectedIds = new Set();

export function showBoardChatTab(boardName) {
    _activeBoardChat = boardName;
    ++_boardChatGen;
    const panel = document.getElementById('agentic-panel-board');
    if (!panel) return;

    // Populate the board tab content (visibility managed by switchAgenticTab)
    panel.innerHTML = `
        <div class="board-chat-header">
            <a class="board-chat-title" href="#" data-on-click="board-chat-open" data-board="${escapeAttr(boardName)}" title="Open full board view">${escapeHtml(keyLabel(boardName))}</a>
            <button class="btn-nav" id="board-chat-pause-btn" data-on-click="board-chat-pause" data-board="${escapeAttr(boardName)}" title="Pause Board — stops agents from receiving new messages until resumed">Pause Board</button>
            <button class="btn-nav board-select-btn" data-on-click="board-chat-select" title="Export — select messages to export as Markdown">Export</button>
        </div>
        <div class="board-paused-banner" id="board-chat-paused-banner" style="display:none">Board reads are paused — agents cannot see new messages</div>
        <div class="board-chat-messages" id="board-panel-msgs"></div>
        <div class="board-chat-input-pane" id="board-chat-input-pane">
            <div class="board-chat-resize-handle" id="board-chat-resize-handle"></div>
            <div class="command-pane-toolbar board-chat-toolbar">
                <div class="toolbar-group">
                    <button class="btn-nav" data-on-click="board-chat-all" title="Notify all agents">@all</button>
                </div>
                <span class="toolbar-spacer"></span>
                <div class="toolbar-group">
                    <button class="btn-nav" data-on-click="board-chat-send" data-board="${escapeAttr(boardName)}" title="Send message">
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><line x1="2" y1="8" x2="14" y2="8"/><polyline points="9 3 14 8 9 13"/></svg>
                    </button>
                </div>
            </div>
            <div class="board-chat-editor-wrap">
                <textarea class="board-chat-textarea" id="board-panel-input" placeholder="Message the team... (@ to mention)"
                    data-on-keydown="board-chat-key" data-on-input="board-chat-mention" data-board="${escapeAttr(boardName)}"></textarea>
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

    // A new board starts afresh; the same board shows what it had at once, and the poll fetches
    // only what came since.
    if (_boardChatBoard !== boardName) {
        _boardChatBoard = boardName;
        _boardChatLoaded = false;
        _boardChatMessages = [];
        _boardChatTotal = 0;
        _boardChatOffset = 0;
    }
    _boardChatSelectMode = false;
    _boardChatSelectedIds.clear();
    if (_boardChatLoaded) _renderBoardPanelMessages(document.getElementById('board-panel-msgs'), true);

    _loadBoardPanelChat(boardName);
    _checkBoardPauseState(boardName);
    // Start polling
    if (_boardChatTimer) clearInterval(_boardChatTimer);
    // Unread counts and notifications keep a slow beat (30 s) while the agent's page is off screen (#3719).
    _boardChatTimer = pollEvery(() => _loadBoardPanelChat(boardName), 3000, { whenHidden: true, agentView: 30000 });
}

export function hideBoardChatTab() {
    _activeBoardChat = null;
    ++_boardChatGen;
    _boardChatSelectMode = false;
    _boardChatSelectedIds.clear();
    if (_boardChatTimer) { clearInterval(_boardChatTimer); _boardChatTimer = null; }
    const panel = document.getElementById('agentic-panel-board');
    if (panel) panel.innerHTML = '';
}

function _formatTime(iso) {
    if (!iso) return '';
    return formatHourMinute(new Date(iso));
}

// Loads what's new on the board into the panel. Once the latest page is in, the board is asked
// only for the messages after the newest one shown (after_id), so a steady poll costs one small
// request that returns nothing (#3722). The latest page is loaded again when the total says
// messages were deleted, or a page or more arrived at once.
async function _loadBoardPanelChat(boardName, { full = false } = {}) {
    if (!document.getElementById('board-panel-msgs')) return;
    // Rapid agent/team switching rebuilds the panel while requests are in flight;
    // a response for an older panel must never touch the shared message state.
    const gen = _boardChatGen;
    const stale = () => gen !== _boardChatGen || _activeBoardChat !== boardName;
    if (_boardChatInFlight === gen) { _boardChatAgain = true; return; } // one at a time, then once more
    _boardChatInFlight = gen;
    _boardChatAgain = false;
    try {
        const loaded = !full && _boardChatLoaded && _boardChatBoard === boardName;
        if (loaded) {
            const last = _boardChatMessages[_boardChatMessages.length - 1];
            const afterId = last ? last.id : 0;
            const resp = await boardFetch(boardName, `/messages/all?limit=${_BOARD_CHAT_PAGE}&after_id=${encodeURIComponent(afterId)}&format=dashboard`);
            if (!resp.ok) return;
            const data = await resp.json();
            if (stale()) return;
            const fresh = (Array.isArray(data?.messages) ? data.messages : []).filter(m => m.id > afterId);
            const expected = _boardChatTotal + fresh.length;
            // The total must be exactly what the panel counts plus the new ones: a message deleted in
            // the same tick as one arrives (a secret, say) still makes it reload, and goes within a poll.
            if (fresh.length < _BOARD_CHAT_PAGE && Number(data.total) === expected) {
                if (!fresh.length) return;
                const byId = new Map([..._boardChatMessages, ...fresh].map(m => [m.id, m]));
                _boardChatMessages = [...byId.values()].sort((a, b) => a.id - b.id);
                _boardChatTotal = expected;
                const el = document.getElementById('board-panel-msgs');
                if (el) _renderBoardPanelMessages(el, false);
                return;
            }
            // Deleted messages, or a page or more at once.
        }

        // The latest page (offset=0: no OFFSET scan).
        const resp = await boardFetch(boardName, `/messages/all?limit=${_BOARD_CHAT_PAGE}&offset=0&format=dashboard`);
        if (!resp.ok) return; // a refusal is not an empty board
        const data = await resp.json();
        if (stale()) return;
        const messages = Array.isArray(data.messages) ? data.messages : [];
        const total = Number(data.total) || 0;
        const first = !(_boardChatLoaded && _boardChatBoard === boardName);

        _boardChatBoard = boardName;
        _boardChatMessages = messages;
        _boardChatTotal = total;
        _boardChatOffset = Number(data.before_count) || 0;
        _boardChatLoaded = true;

        const el = document.getElementById('board-panel-msgs');
        if (!el) return;
        _renderBoardPanelMessages(el, first);
    } catch { /* ignore */ } finally {
        if (_boardChatInFlight === gen) {
            _boardChatInFlight = 0;
            if (_boardChatAgain && !stale()) { _boardChatAgain = false; _loadBoardPanelChat(boardName); }
        }
    }
}

async function _loadEarlierBoardChat() {
    if (!_activeBoardChat || _boardChatOffset <= 0 || !_boardChatMessages.length) return;
    const msgsEl = document.getElementById('board-panel-msgs');
    if (!msgsEl) return;
    const boardName = _activeBoardChat;
    const gen = _boardChatGen;
    try {
        const resp = await boardFetch(boardName, `/messages/all?limit=${_BOARD_CHAT_PAGE}&before_id=${encodeURIComponent(_boardChatMessages[0].id)}&format=dashboard`);
        if (!resp.ok) return;
        const data = await resp.json();
        // The panel may have switched boards or reloaded while this was in flight.
        if (gen !== _boardChatGen || _activeBoardChat !== boardName) return;
        const messages = Array.isArray(data.messages) ? data.messages : [];

        // Save scroll height to preserve position after prepending
        const prevScrollHeight = msgsEl.scrollHeight;

        const byId = new Map([...messages, ..._boardChatMessages].map(m => [m.id, m]));
        _boardChatMessages = [...byId.values()].sort((a, b) => a.id - b.id);
        _boardChatOffset = Number(data.before_count) || 0;

        _renderBoardPanelMessages(msgsEl, false);

        // Restore scroll position so user stays where they were
        msgsEl.scrollTop = msgsEl.scrollHeight - prevScrollHeight;
    } catch { /* ignore */ }
}
(window.__coral ||= {})._loadEarlierBoardChat = _loadEarlierBoardChat;

function _renderBoardPanelMessages(msgsEl, scrollToBottom) {
    if (!msgsEl) return;
    const messages = _boardChatMessages;
    if (!messages.length && _boardChatOffset <= 0) {
        msgsEl.innerHTML = '<div class="board-chat-empty">No messages yet</div>';
        msgsEl.dataset.rendered = '1';
        return;
    }
    const wasAtBottom = scrollToBottom || msgsEl.scrollTop >= msgsEl.scrollHeight - msgsEl.clientHeight - 20;

    // "Load Earlier" button
    let loadEarlierHtml = '';
    if (_boardChatOffset > 0) {
        const remaining = _boardChatOffset;
        loadEarlierHtml = `<div style="text-align:center;padding:6px 0 8px">
            <button class="btn btn-small" data-on-click="board-chat-earlier" style="font-size:11px;color:var(--text-muted)">
                ▲ Load ${Math.min(remaining, _BOARD_CHAT_PAGE)} earlier (${remaining} remaining)
            </button>
        </div>`;
    }

    // Runs of task-queue lines are one collapsed row (#3343), except in select mode.
    const items = groupTaskQueueRuns(messages, !_boardChatSelectMode);
    msgsEl.innerHTML = loadEarlierHtml + items.map((item, i) => {
        if (item.kind === 'queue') return renderTaskQueueRun(item, _formatTime);
        const m = item.m;
        const agent = m.job_title || m.sender_name || 'Unknown';
        const color = getAgentColor(agent);
        const prev = i > 0 && items[i - 1].kind === 'msg' ? items[i - 1].m : null;
        const prevAgent = prev ? (prev.job_title || prev.sender_name || 'Unknown') : null;
        const sameAsPrev = agent === prevAgent;
        const spacing = sameAsPrev ? 'mb-message-grouped' : 'mb-message-first';
        const isLeader = /orchestrator/i.test(agent) || isOperatorId(m.subscriber_id || m.session_id);
        const alignClass = isLeader ? ' board-msg-left' : ' board-msg-right';

        const selectedClass = _boardChatSelectMode && _boardChatSelectedIds.has(m.id) ? ' mb-message-selected' : '';
        const checkbox = _boardChatSelectMode ? `<input type="checkbox" class="mb-select-checkbox" data-msg-id="${Number(m.id)}" ${_boardChatSelectedIds.has(m.id) ? 'checked' : ''} data-on-click="board-chat-msg-select">` : '';
        return `<div class="mb-message ${spacing}${alignClass}${selectedClass}" style="${boardMessageColorStyle(color)}">
            ${boardAvatar(m, agent)}
            <div class="mb-message-header">
                ${checkbox}
                <span class="mb-agent-name">${m.icon ? escapeHtml(m.icon) + ' ' : ''}${escapeHtml(agent)}</span>
                ${externalMessageBadge(m)}${hubUserBadge(m)}
                <span class="mb-message-time">${_formatTime(m.created_at)}</span>
            </div>
            <div class="mb-message-body">${renderBoardMessageBody(m)}</div>
        </div>`;
    }).join('');
    resolveBoardArtifacts(msgsEl, _activeBoardChat ? splitKey(_activeBoardChat).server : undefined);
    if (wasAtBottom) msgsEl.scrollTop = msgsEl.scrollHeight;
    msgsEl.dataset.rendered = '1';
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
(window.__coral ||= {})._toggleBoardChatSelect = _toggleBoardChatSelect;

export function _toggleBoardChatMsgSelect(msgId, checked) {
    if (checked) {
        _boardChatSelectedIds.add(msgId);
    } else {
        _boardChatSelectedIds.delete(msgId);
    }
    const cb = document.querySelector(`#board-panel-msgs .mb-select-checkbox[data-msg-id="${msgId}"]`);
    if (cb) cb.closest('.mb-message').classList.toggle('mb-message-selected', checked);
    _updateBoardChatSelectBar();
}

export function _boardChatSelectAll() {
    _boardChatMessages.forEach(m => _boardChatSelectedIds.add(m.id));
    const msgsEl = document.getElementById('board-panel-msgs');
    if (msgsEl) _renderBoardPanelMessages(msgsEl, false);
}

export function _boardChatSelectNone() {
    _boardChatSelectedIds.clear();
    const msgsEl = document.getElementById('board-panel-msgs');
    if (msgsEl) _renderBoardPanelMessages(msgsEl, false);
}

export function _cancelBoardChatSelect() {
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
        <button class="btn mb-select-action-btn" data-on-click="board-chat-select-all">Select All</button>
        <button class="btn mb-select-action-btn" data-on-click="board-chat-select-none">None</button>
        <button class="btn btn-primary mb-select-action-btn" data-on-click="board-chat-export" ${count === 0 ? 'disabled' : ''}>Export Markdown</button>
        <button class="btn mb-select-action-btn" data-on-click="board-chat-select-cancel">Cancel</button>
    `;
    bar.style.display = '';
}

export async function _exportBoardChatSelected() {
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

async function _sendBoardChat(boardName) {
    const inputEl = document.getElementById('board-panel-input');
    if (!inputEl) return;
    const content = inputEl.value.trim();
    if (!content) return;
    inputEl.value = '';
    try {
        await boardFetch(boardName, `/messages`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ subscriber_id: OPERATOR_ID, as: 'Operator', content }),
        });
        _loadBoardPanelChat(boardName);
    } catch { /* ignore */ }
}
(window.__coral ||= {})._sendBoardChat = _sendBoardChat;

export async function _toggleBoardChatPause(boardName) {
    const btn = document.getElementById('board-chat-pause-btn');
    const banner = document.getElementById('board-chat-paused-banner');
    const wasPaused = btn?.classList.contains('mb-action-danger');
    const endpoint = wasPaused ? 'resume' : 'pause';
    try {
        await boardFetch(boardName, `/${endpoint}`, { method: 'POST' });
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

async function _checkBoardPauseState(boardName) {
    try {
        const resp = await boardFetch(boardName, `/paused`);
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

// Board chat @mention autocomplete
let _boardSubscribers = {};
export async function _boardMentionInput(event, boardName) {
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
            const resp = await boardFetch(boardName, `/subscribers`);
            _boardSubscribers[boardName] = await resp.json();
        } catch { _boardSubscribers[boardName] = []; }
    }

    const subs = (_boardSubscribers[boardName] || [])
        .map(s => s.job_title || s.session_id || 'unknown')
        .filter(name => name.toLowerCase().includes(query));

    if (subs.length === 0) { dropdown.style.display = 'none'; return; }

    dropdown.innerHTML = subs.map(name =>
        `<div class="board-mention-item" data-on-mousedown="board-mention-pick" data-name="${escapeAttr(name)}">${escapeHtml(name)}</div>`
    ).join('');
    dropdown.style.display = 'block';
}

export function _insertBoardMention(name) {
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
