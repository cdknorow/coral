/* Moved out of render.js as it was (#3795): no behaviour change. */
import { formatHourMinute } from './format.js';
import { morphHtml } from './dom_morph.js';
import { OPERATOR_ID, isOperatorId, escapeHtml, showToast, escapeAttr, getAgentColor, externalMessageBadge, hubUserBadge, renderBoardMessageBody, setBoardMentionNames, highlightMentionsIn, boardAvatar, boardMessageColorStyle } from './utils.js';
import { splitKey, keyLabel, boardFetch } from './server_base.js';
import { groupTaskQueueRuns, renderTaskQueueRun } from './board_system_lines.js';
import { messageAttachmentAttrs, messageAttachmentsHtml } from './board_attachments.js';
import { pollEvery } from './overload.js';
import { resolveBoardArtifacts } from './board_artifacts.js';
import { initGrowingTextarea } from './growing_textarea.js';
import { announceNotice } from './notifications.js';
import { paintPauseButton as _paintPauseButton } from './board_pause_button.js';

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
// The panel keeps the newest _BOARD_CHAT_KEEP messages while the reader is at the bottom (#4318): a tab
// left open on a busy board kept every message it ever received and re-templated all of them for each
// new one. Older messages stay behind "Load earlier". The list is cut back a page at a time, so it is
// rebuilt once per page of new messages, not shifted on every one.
const _BOARD_CHAT_KEEP = 300;
let _boardChatSelectMode = false;
let _boardChatSelectedIds = new Set();

// What a rebuild used to reset and a kept panel must reset itself on a switch: select mode, an open @-mention list, the title.
function _resetBoardPanelForSwitch(panel, boardName) {
    if (_boardChatSelectMode) {
        const btn = panel.querySelector('.board-select-btn');
        if (btn) { btn.classList.remove('active'); btn.setAttribute('aria-pressed', 'false'); }
        const inputPane = document.getElementById('board-chat-input-pane');
        if (inputPane) inputPane.style.display = '';
        const bar = document.getElementById('board-chat-select-bar');
        if (bar) bar.style.display = 'none';
    }
    const mention = document.getElementById('board-mention-dropdown');
    if (mention && mention.style.display !== 'none') mention.style.display = 'none';
    const title = panel.querySelector('.board-chat-title');
    const label = keyLabel(boardName);
    if (title && title.textContent !== label) title.textContent = label;
}

export function showBoardChatTab(boardName) {
    _activeBoardChat = boardName;
    ++_boardChatGen;
    const panel = document.getElementById('agentic-panel-board');
    if (!panel) return;

    // The same board again (another agent of its team): the panel is kept as it is, header, messages and input pane, so a draft in
    // the textarea survives the switch and nothing is rebuilt (#3898; before, every switch rebuilt the panel and wiped the draft).
    // A different board, or a panel that was cleared, is built afresh.
    const sameBoard = _boardChatBoard === boardName && panel.dataset.board === boardName
        && !!panel.querySelector('#board-panel-msgs') && !!panel.querySelector('#board-panel-input');
    if (sameBoard) {
        _resetBoardPanelForSwitch(panel, boardName);
    } else {
        // The same board again (another agent of its team): the messages element, with its rendered messages, is carried over into
        // the rebuilt panel, so the render below keeps those nodes instead of building them again (#3881).
        const carried = _boardChatLoaded && _boardChatBoard === boardName ? document.getElementById('board-panel-msgs') : null;
        if (carried) carried.remove();

        // Populate the board tab content (visibility managed by switchAgenticTab)
        panel.innerHTML = `
            <div class="board-chat-header">
                <a class="board-chat-title" href="#" data-on-click="board-chat-open" data-board="${escapeAttr(boardName)}" title="Open full board view">${escapeHtml(keyLabel(boardName))}</a>
                <button class="btn-nav board-icon-btn" id="board-chat-pause-btn" data-on-click="board-chat-pause" data-board="${escapeAttr(boardName)}" aria-label="Pause board" title="Pause board: stops agents from receiving new messages until resumed"><span class="material-icons" aria-hidden="true">pause</span></button>
                <button class="btn-nav board-select-btn board-icon-btn" data-on-click="board-chat-select" aria-label="Export messages" aria-pressed="false" title="Export: select messages to export as Markdown"><span class="material-icons" aria-hidden="true">download</span></button>
            </div>
            <div class="board-paused-banner" id="board-chat-paused-banner" style="display:none">Board reads are paused — agents cannot see new messages</div>
            <div class="board-chat-messages" id="board-panel-msgs"></div>
            <div class="board-chat-input-pane" id="board-chat-input-pane">
                <div class="command-pane-body">
                    <div class="command-editor board-chat-editor-wrap">
                        <textarea class="board-chat-textarea" id="board-panel-input" rows="1" placeholder="Message the team... (@ to mention)" aria-label="Message the team"
                            role="combobox" aria-haspopup="listbox" aria-expanded="false" aria-controls="board-mention-dropdown"
                            data-on-keydown="board-chat-key" data-on-input="board-chat-mention" data-board="${escapeAttr(boardName)}"></textarea>
                        <div class="board-mention-dropdown" id="board-mention-dropdown" role="listbox" aria-label="Mention an agent" style="display:none"></div>
                    </div>
                </div>
                <!-- After the text box in the DOM so Tab reaches the box first, then @all, then Send; CSS order draws them under it, as the chat's composer does -->
                <div class="command-pane-toolbar board-chat-toolbar">
                    <button type="button" class="btn-nav board-mention-chip" data-on-click="board-chat-all" title="Notify all agents">@all</button>
                    <span class="toolbar-spacer"></span>
                    <button type="button" class="btn-nav btn-send" data-on-click="board-chat-send" data-board="${escapeAttr(boardName)}" aria-label="Send message" title="Send message (Enter)">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><line x1="22" y1="2" x2="11" y2="13"/><polygon points="22 2 15 22 11 13 2 9 22 2"/></svg>
                    </button>
                </div>
            </div>`;

        if (carried) document.getElementById('board-panel-msgs').replaceWith(carried);

        initGrowingTextarea(document.getElementById('board-panel-input'));

    panel.dataset.board = boardName;
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
    _syncMentionNames(boardName); // the board's members, for the @mentions in its messages
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
                _trimBoardChatWindow(el);
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

// Drops the oldest messages beyond _BOARD_CHAT_KEEP (they count as not loaded, so "Load earlier"
// brings them back), unless the reader is scrolled up among them or selecting messages to export.
function _trimBoardChatWindow(msgsEl) {
    if (_boardChatSelectMode || _boardChatMessages.length <= _BOARD_CHAT_KEEP + _BOARD_CHAT_PAGE) return;
    if (msgsEl && msgsEl.scrollTop < msgsEl.scrollHeight - msgsEl.clientHeight - 20) return;
    const drop = _boardChatMessages.length - _BOARD_CHAT_KEEP;
    _boardChatMessages = _boardChatMessages.slice(drop);
    _boardChatOffset += drop;
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

/** An empty board says what to do next (#5989): tell the lead what to build, the agent named Orchestrator, else the board's first member; the compose box is focused by the caller. */
function _emptyBoardLine() {
    const subs = (_boardSubscribers[_activeBoardChat] || []).filter(s => !isOperatorId(s.subscriber_id));
    const lead = subs.find(s => (s.job_title || s.subscriber_id) === 'Orchestrator') || subs[0];
    return lead ? `Tell @${lead.job_title || lead.subscriber_id} what to build` : 'No messages yet';
}

function _renderBoardPanelMessages(msgsEl, scrollToBottom) {
    if (!msgsEl) return;
    const messages = _boardChatMessages;
    if (!messages.length && _boardChatOffset <= 0) {
        msgsEl.innerHTML = `<div class="board-chat-empty">${escapeHtml(_emptyBoardLine())}</div>`;
        msgsEl.dataset.rendered = '1';
        return;
    }
    const wasAtBottom = scrollToBottom || msgsEl.scrollTop >= msgsEl.scrollHeight - msgsEl.clientHeight - 20;

    // "Load Earlier" button
    let loadEarlierHtml = '';
    if (_boardChatOffset > 0) {
        const remaining = _boardChatOffset;
        loadEarlierHtml = `<div style="text-align:center;padding:6px 0 8px">
            <button class="btn btn-small" data-on-click="board-chat-earlier" style="font-size:0.6875rem;color:var(--text-muted)">
                ▲ Load ${Math.min(remaining, _BOARD_CHAT_PAGE)} earlier (${remaining} remaining)
            </button>
        </div>`;
    }

    // Runs of task-queue lines are one collapsed row (#3343), except in select mode.
    const items = groupTaskQueueRuns(messages, !_boardChatSelectMode);
    // Unchanged messages keep their nodes (and the artifact images and links already resolved in them); only new or changed ones
    // are built (#3881: the whole page was rebuilt on every agent switch, 170 to 200 ms over five switches).
    morphHtml(msgsEl, loadEarlierHtml + items.map((item, i) => {
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
        return `<div class="mb-message ${spacing}${alignClass}${selectedClass}"${messageAttachmentAttrs(m)} style="${boardMessageColorStyle(color)}">
            ${boardAvatar(m, agent)}
            <div class="mb-message-header">
                ${checkbox}
                <span class="mb-agent-name">${m.icon ? escapeHtml(m.icon) + ' ' : ''}${escapeHtml(agent)}</span>
                ${externalMessageBadge(m)}${hubUserBadge(m)}
                <span class="mb-message-time">${_formatTime(m.created_at)}</span>
            </div>
            <div class="mb-message-body">${renderBoardMessageBody(m)}</div>
            ${messageAttachmentsHtml(m)}
        </div>`;
    }).join(''));
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
    if (btn) { btn.classList.toggle('active', _boardChatSelectMode); btn.setAttribute('aria-pressed', String(_boardChatSelectMode)); }
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
    if (btn) { btn.classList.remove('active'); btn.setAttribute('aria-pressed', 'false'); }
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
    let sent = false;
    try {
        const resp = await boardFetch(boardName, `/messages`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ subscriber_id: OPERATOR_ID, as: 'Operator', content }),
        });
        sent = resp.ok;
    } catch { /* not sent: said below */ }
    if (!sent) {
        // The words go back in the box (ahead of anything typed since) and the failure is said: a message that vanished with no word is the worst way to fail (#6275).
        inputEl.value = inputEl.value ? `${content}\n${inputEl.value}` : content;
        inputEl.dispatchEvent(new Event('input', { bubbles: true }));
        showToast('Could not send to the team. Your message is back in the box.', true);
        return;
    }
    announceNotice('Message sent'); // heard once, not drawn: the message itself appears in the list
    _loadBoardPanelChat(boardName);
}
(window.__coral ||= {})._sendBoardChat = _sendBoardChat;

export async function _toggleBoardChatPause(boardName) {
    const btn = document.getElementById('board-chat-pause-btn');
    const banner = document.getElementById('board-chat-paused-banner');
    const wasPaused = btn?.classList.contains('mb-action-danger');
    const endpoint = wasPaused ? 'resume' : 'pause';
    try {
        await boardFetch(boardName, `/${endpoint}`, { method: 'POST' });
        _paintPauseButton(btn, !wasPaused);
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
        _paintPauseButton(btn, !!data.paused);
        if (banner) {
            banner.style.display = data.paused ? '' : 'none';
        }
    } catch { /* ignore */ }
}

// Board chat @mention autocomplete
let _boardSubscribers = {};
let _mentionBoard = '';
/** The board's member names for @mention highlighting, once per board: fetched (or taken from the @ picker's cache), then the bodies on screen are marked in place if the names changed. */
async function _syncMentionNames(boardName) {
    if (_mentionBoard === boardName) return;
    _mentionBoard = boardName;
    try {
        if (!_boardSubscribers[boardName]) { const r = await boardFetch(boardName, `/subscribers`); if (!r.ok) { _mentionBoard = ''; return; } _boardSubscribers[boardName] = await r.json(); }
    } catch { _mentionBoard = ''; return; }
    if (_activeBoardChat !== boardName) return;
    const subs = Array.isArray(_boardSubscribers[boardName]) ? _boardSubscribers[boardName] : [];
    if (setBoardMentionNames(subs.flatMap(s => [s.job_title, s.subscriber_id]))) highlightMentionsIn(document.getElementById('board-panel-msgs')); // in place: nothing is redrawn
    const empty = document.querySelector('#board-panel-msgs > .board-chat-empty'); // an empty board names its lead once the members are known
    if (empty && empty.textContent !== _emptyBoardLine()) empty.textContent = _emptyBoardLine();
}
export async function _boardMentionInput(event, boardName) {
    const input = event.target;
    const text = input.value;
    const cursor = input.selectionStart;
    const beforeCursor = text.slice(0, cursor);
    const dropdown = document.getElementById('board-mention-dropdown');
    if (!dropdown) return;

    // Find @ trigger
    const atMatch = beforeCursor.match(/@(\w*)$/);
    if (!atMatch) { _closeBoardMentions(); return; }
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

    if (subs.length === 0) { _closeBoardMentions(); return; }

    dropdown.innerHTML = subs.map((name, i) =>
        `<div class="board-mention-item" id="board-mention-opt-${Number(i)}" role="option" aria-selected="false" data-on-mousedown="board-mention-pick" data-name="${escapeAttr(name)}">${escapeHtml(name)}</div>`
    ).join('');
    const first = dropdown.querySelector('.board-mention-item');
    first.classList.add('active');
    first.setAttribute('aria-selected', 'true');
    dropdown.style.display = 'block';
    _syncBoardMentions();
}

function _closeBoardMentions() {
    const dropdown = document.getElementById('board-mention-dropdown');
    if (dropdown) dropdown.style.display = 'none';
    _syncBoardMentions();
}

/** The combobox attributes follow the list: expanded while it shows, and the highlighted option is the active descendant. */
function _syncBoardMentions() {
    const input = document.getElementById('board-panel-input');
    const dropdown = document.getElementById('board-mention-dropdown');
    if (!input || !dropdown) return;
    const open = dropdown.style.display !== 'none';
    input.setAttribute('aria-expanded', String(open));
    const active = open && dropdown.querySelector('.board-mention-item.active');
    if (active) input.setAttribute('aria-activedescendant', active.id); else input.removeAttribute('aria-activedescendant');
}

/** Arrow keys move through the @mention list, Enter or Tab picks the highlighted name, Escape closes it. True when the key was used. */
export function _boardMentionKey(event) {
    const dropdown = document.getElementById('board-mention-dropdown');
    if (!dropdown || dropdown.style.display === 'none') return false;
    const items = [...dropdown.querySelectorAll('.board-mention-item')];
    if (!items.length) return false;
    const at = Math.max(0, items.findIndex(i => i.classList.contains('active')));
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
        const next = (at + (event.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length;
        items.forEach((it, i) => { it.classList.toggle('active', i === next); it.setAttribute('aria-selected', String(i === next)); });
        items[next].scrollIntoView?.({ block: 'nearest' });
        _syncBoardMentions();
    } else if ((event.key === 'Enter' && !event.ctrlKey && !event.metaKey) || event.key === 'Tab') {
        _insertBoardMention(items[at].dataset.name);
    } else if (event.key === 'Escape') {
        _closeBoardMentions();
    } else return false;
    event.preventDefault();
    event.stopPropagation();
    return true;
}

export function _insertBoardMention(name) {
    const input = document.getElementById('board-panel-input');
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
    _closeBoardMentions();
}
