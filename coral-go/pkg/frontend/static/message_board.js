/* Message Board: project list, messages, subscribers, posting */

import { OPERATOR_ID, isOperatorId, escapeHtml, escapeAttr, showView, renderMarkdown, getAgentColor, showToast, externalMessageBadge, hubUserBadge, renderBoardMessageBody, boardAvatar, boardMessageColorStyle } from './utils.js';
import { registerActions, withId, textOf } from './actions.js';
import { boardPageChanged } from './dock_page.js';
import { state } from './state.js';
import { resolveBoardArtifacts } from './board_artifacts.js';
import { groupTaskQueueRuns, renderTaskQueueRun } from './board_system_lines.js';
import { loadLiveSessions } from './api.js';
import { platform } from './platform/detect.js';
import { BOARD_SAVE_BUTTON } from './saved_marks.js';
import { pathId, identityKey, keyLabel, noteBoardLabel, noteBoardLabels, hasBoardLabel, onBoardLabelsChanged, splitKey, serverFetch, sessionServer, boardFetch, teamFetch, isCloud } from './server_base.js';
import { launchBlockedReason } from './hub_access.js';
import { pollEvery, afterPause, isBusyStatus, busyRetryingText, machineOf } from './overload.js';
import { note } from './nav.js';
import { keyRoute } from './routes.js';
import { attachDialogFocus } from './dialog_focus.js';
import { mountBoardSearch, highlightBoard, setBoardMembers, resetBoardSearch } from './board_search.js';

let currentProject = null;
let pollTimer = null;
let isPaused = false;
let isSleeping = false;
const PAGE_SIZE = 50;
let _allMessages = [];
let _totalMessages = 0;
let _loadedOffset = 0; // older messages not loaded yet (the offset of the first loaded one)
let _loadedProject = null; // board whose initial page is loaded; polls wait for it
let _pollInFlight = false;
let _countedTotal = 0; // the board's total as the rows loaded account for it (see _pollNewMessages)
let _staleRows = false; // a message shown may have been deleted: the latest page is loaded again at the bottom

// Responses can arrive after a board switch or overlap another request; keep
// each message once, in id order.
function mergeMessages(...lists) {
    const byId = new Map();
    for (const list of lists) for (const m of list) byId.set(m.id, m);
    return [...byId.values()].sort((a, b) => a.id - b.id);
}
let _selectMode = false;
let _selectedIds = new Set();
let pendingMessageId = null;

// ── API helpers ──────────────────────────────────────────────────────────

// Boards live on the server that hosts the team, so the project list is the
// hub's own boards plus those of every online remote. Each entry's `project`
// is a team key (server + name); local keys are the bare name.
async function fetchProjects() {
    const resp = await fetch('/api/board/projects');
    const local = await resp.json();
    noteBoardLabels(local, 'project'); // board_label beside each key (#3536)
    const remotes = !state.hub ? [] : (state.servers || []).filter(s => s.id !== 'local' && s.status === 'online');
    const lists = await Promise.all(remotes.map(async srv => {
        try {
            const r = await serverFetch(srv.id, '/api/board/projects');
            if (!r.ok) return [];
            const rows = await r.json();
            noteBoardLabels(rows, 'project', srv.id);
            return (Array.isArray(rows) ? rows : []).map(p => ({ ...p, project: identityKey(srv.id, p.project), server: srv.id }));
        } catch { return []; }
    }));
    return [...local, ...lists.flat()];
}

// page is {before_id} or {after_id} (none: the latest page). A reply reports
// before_count, the messages older than the page.
async function fetchMessages(project, limit = PAGE_SIZE, page = {}) {
    const params = new URLSearchParams({ limit: String(limit), format: 'dashboard' });
    for (const [k, v] of Object.entries(page)) params.set(k, String(v));
    if (!('offset' in page)) params.set('offset', '0');
    const resp = await boardFetch(project, `/messages/all?${params}`);
    // A refusal is not an empty board: a 503's {error} body once read as zero messages (#3430).
    if (!resp.ok) { const err = new Error(`messages: HTTP ${resp.status}`); err.resp = resp; throw err; }
    const data = await resp.json();
    return {
        messages: Array.isArray(data.messages) ? data.messages : [], total: Number(data.total) || 0,
        beforeCount: Number(data.before_count) || 0,
    };
}

// The export reads whole boards and transcripts in pages (#3718): through Coral Hub a JSON reply
// is held whole to be checked for secrets and refused over 2 MiB, so it never asks for all of one
// at once.
const EXPORT_PAGE = 200;
const EXPORT_CHAT_PAGE = 100;

export async function fetchAllBoardMessages(project) {
    const first = await fetchMessages(project, EXPORT_PAGE);
    let all = first.messages;
    let before = first.beforeCount;
    while (before > 0 && all.length) {
        const r = await fetchMessages(project, EXPORT_PAGE, { before_id: all[0].id });
        if (!r.messages.length) break;
        all = mergeMessages(r.messages, all);
        before = r.beforeCount;
    }
    return all;
}

export async function fetchAllLiveChat(live) {
    const messages = [];
    for (let offset = 0; ; offset += EXPORT_CHAT_PAGE) {
        const params = new URLSearchParams({
            agent_type: live.agent_type || 'claude', session_id: live.session_id,
            working_directory: live.working_directory || '', after: '0',
            limit: String(EXPORT_CHAT_PAGE), offset: String(offset),
        });
        const resp = await serverFetch(sessionServer(live), `/api/sessions/live/${encodeURIComponent(live.name)}/chat?${params}`);
        if (!resp.ok) throw new Error(`chat: HTTP ${resp.status}`);
        const data = await resp.json();
        const page = Array.isArray(data.messages) ? data.messages : [];
        messages.unshift(...page);
        if (!page.length || messages.length >= data.total) break;
    }
    return { messages };
}

export async function fetchAllHistory(sessionId) {
    const messages = [];
    for (let offset = 0; ; offset += EXPORT_CHAT_PAGE) {
        const resp = await fetch(`/api/sessions/history/${encodeURIComponent(sessionId)}?limit=${EXPORT_CHAT_PAGE}&offset=${offset}`);
        if (!resp.ok) throw new Error(`history: HTTP ${resp.status}`);
        const data = await resp.json();
        const page = Array.isArray(data.messages) ? data.messages : [];
        messages.unshift(...page);
        if (!page.length || !data.start) break;
    }
    return { messages };
}

async function fetchSubscribers(project) {
    const resp = await boardFetch(project, `/subscribers`);
    return await resp.json();
}

// ── Sidebar rendering ────────────────────────────────────────────────────

async function loadBoardProjects() {
    try {
        const projects = await fetchProjects();
        renderBoardSidebar(projects);
        const badge = document.getElementById('messageboard-count');
        if (badge) badge.textContent = String(projects.length);
    } catch (e) {
        console.error('Failed to load board projects:', e);
    }
}

function renderBoardSidebar(projects) {
    const list = document.getElementById('messageboard-sidebar-list');
    if (!list) return;
    if (!projects.length) {
        list.innerHTML = '<li class="empty-state">No projects</li>';
        return;
    }
    list.innerHTML = projects.map(p => {
        const active = currentProject === p.project ? 'active' : '';
        return `<li class="session-list-item ${escapeAttr(active)}" data-on-click="mb-project" data-project="${escapeAttr(p.project)}">
            <span class="session-name">${escapeHtml(keyLabel(p.project))}</span>
            <span class="mb-project-count">${p.message_count} msgs</span>
        </li>`;
    }).join('');
}

// The open board's header and the board list show labels; when one arrives or changes, they
// follow (a board opened at /board/<id> before the lists loaded, a rename).
onBoardLabelsChanged(() => {
    const badge = document.getElementById('messageboard-project-badge');
    if (badge && currentProject) badge.textContent = keyLabel(currentProject);
});

// ── View switching ───────────────────────────────────────────────────────

export function selectBoardProject(project, messageId = null) {
    setMoreOpen(false);
    currentProject = project;
    state.boardPageProject = project; // the sidebar's Tasks and Team artifacts tabs read this board (#4539)
    clearNewPill();
    state.currentBoardServer = splitKey(project).server;
    pendingMessageId = messageId != null ? String(messageId) : null;
    note(keyRoute('board', project)); // the board's page (#3532)

    // Hide other views, show messageboard
    showView("messageboard-view");

    // Show board panel
    document.getElementById('mb-project-list').style.display = 'none';
    const board = document.getElementById('mb-board');
    board.style.display = 'flex';
    hideBoardSubscribers();
    document.getElementById('messageboard-view').classList.add('mb-has-board');
    const backBtn = document.getElementById('mb-back-btn');
    if (backBtn) backBtn.style.display = '';
    document.getElementById('mb-pause-btn').style.display = '';
    document.getElementById('mb-export-btn').style.display = '';
    document.getElementById('mb-select-btn').style.display = '';
    const sleepBtn = document.getElementById('mb-sleep-btn');
    if (sleepBtn) sleepBtn.style.display = '';
    document.getElementById('mb-delete-btn').style.display = '';
    syncBoardDeleteButton(project);

    const badge = document.getElementById('messageboard-project-badge');
    badge.textContent = keyLabel(project);
    badge.style.display = '';
    // A board opened before its label is known (a /board/<id> page, a board with no agents)
    // asks for it; the header follows when it, or any list, brings it (#3536).
    if (!hasBoardLabel(project)) {
        boardFetch(project, '').then(r => (r.ok ? r.json() : null)).then(d => {
            if (d && typeof d.board_label === 'string') noteBoardLabel(splitKey(project).server, splitKey(project).name, d.board_label);
        }).catch(() => {});
    }
    document.getElementById('messageboard-title').textContent = 'Message Board';
    mountBoardSearch(document.getElementById('mb-tools'), project, SEARCH_HOOKS);

    // Ensure dashboard is subscribed as a reader
    subscribeDashboard(project);

    // Load paused and sleep state
    loadPausedState(project);
    loadSleepState(project);

    loadBoardMessages(project);
    loadBoardSubscribers(project);
    loadBoardProjects();
    startBoardPoll();
    boardPageChanged(); // the sidebar's tabs read the board again
}

export function showMessageBoardProjects() {
    setMoreOpen(false);
    resetBoardSearch();
    currentProject = null;
    state.boardPageProject = null;
    clearNewPill();
    stopBoardPoll();
    // Exit select mode if active
    _selectMode = false;
    _selectedIds.clear();

    showView("messageboard-view");

    document.getElementById('mb-project-list').style.display = '';
    document.getElementById('mb-board').style.display = 'none';
    hideBoardSubscribers();
    for (const list of ['mb-subscribers-list', 'mb-subscribers-sheet-list']) { const el = document.getElementById(list); if (el) el.innerHTML = '<li class="mb-project-meta">Open a board to see who is subscribed.</li>'; }
    boardPageChanged();
    document.getElementById('messageboard-view').classList.remove('mb-has-board');
    const backBtn2 = document.getElementById('mb-back-btn');
    if (backBtn2) backBtn2.style.display = 'none';
    document.getElementById('mb-pause-btn').style.display = 'none';
    const sleepBtn2 = document.getElementById('mb-sleep-btn');
    if (sleepBtn2) sleepBtn2.style.display = 'none';
    document.getElementById('mb-delete-btn').style.display = 'none';
    document.getElementById('mb-select-btn').style.display = 'none';
    document.getElementById('messageboard-project-badge').style.display = 'none';

    loadBoardProjectList();
    loadBoardProjects();
}

// ── Project list view ────────────────────────────────────────────────────

async function loadBoardProjectList() {
    const ul = document.getElementById('mb-projects-ul');
    const empty = document.getElementById('mb-projects-empty');
    // Show loading indicator while fetching
    empty.style.display = 'none';
    ul.innerHTML = '<li class="loading-indicator">Loading projects</li>';
    try {
        const projects = await fetchProjects();
        if (!projects.length) {
            ul.innerHTML = '';
            empty.style.display = '';
            return;
        }
        empty.style.display = 'none';
        ul.innerHTML = projects.map(p => `
            <li class="session-list-item" data-on-click="mb-project" data-project="${escapeAttr(p.project)}"
                style="display:flex;justify-content:space-between;align-items:center;padding:12px 16px">
                <div>
                    <strong>${escapeHtml(keyLabel(p.project))}</strong>
                    <div class="mb-project-meta">${p.subscriber_count} subscriber${p.subscriber_count !== 1 ? 's' : ''}</div>
                </div>
                <span class="mb-project-meta">${p.message_count} messages</span>
            </li>
        `).join('');
    } catch (e) {
        console.error('Failed to load board project list:', e);
    }
}

// ── Messages ─────────────────────────────────────────────────────────────

async function loadBoardMessages(project) {
    const msgsEl = document.getElementById('mb-messages');
    const empty = document.getElementById('mb-messages-empty');
    if (empty) empty.style.display = 'none';
    // Show loading indicator below any existing messages
    let loadingEl = msgsEl?.querySelector('.loading-indicator');
    if (!loadingEl && msgsEl) {
        loadingEl = document.createElement('div');
        loadingEl.className = 'loading-indicator';
        loadingEl.textContent = 'Loading messages';
        msgsEl.appendChild(loadingEl);
    }
    _loadedProject = null;
    try {
        const result = await fetchMessages(project, PAGE_SIZE);
        if (currentProject !== project) return;
        _loadedProject = project;
        _totalMessages = _countedTotal = result.total;
        _staleRows = false;
        _allMessages = result.messages;
        _loadedOffset = result.beforeCount;
        renderMessages(_allMessages);
    } catch (e) {
        // Refused as busy (#3430): say it is retrying, and load again once the pause is over
        // instead of at the next 10 s poll (which only fetches new messages after a first load).
        if (e && e.resp && isBusyStatus(e.resp.status) && currentProject === project) {
            if (loadingEl) {
                loadingEl.classList.add('mb-busy');
                loadingEl.setAttribute('role', 'status');
                loadingEl.textContent = busyRetryingText(e.resp, { hub: isCloud() });
            }
            let machine = '';
            try { machine = machineOf(new URL(e.resp.url).pathname); } catch { /* same server */ }
            clearTimeout(_boardBusyRetry);
            _boardBusyRetry = afterPause(() => {
                _boardBusyRetry = null;
                if (currentProject === project && _loadedProject !== project) loadBoardMessages(project);
            }, machine);
            return;
        }
        console.error('Failed to load board messages:', e);
    }
}
let _boardBusyRetry = null;

let _earlierInFlight = false;

async function loadEarlierMessages() {
    const project = currentProject;
    if (!project || _loadedProject !== project || _loadedOffset <= 0 || !_allMessages.length || _earlierInFlight) return;
    _earlierInFlight = true;
    const firstId = _allMessages[0].id;
    let result;
    try {
        result = await fetchMessages(project, PAGE_SIZE, { before_id: firstId });
    } catch (e) {
        console.error('Failed to load earlier messages:', e);
        return;
    } finally {
        _earlierInFlight = false; // before the render: a search jump that still misses its message loads the next page from it
    }
    // A trim or a reload while it was out moved the oldest message loaded: this page would leave a gap.
    if (currentProject !== project || !_allMessages.length || _allMessages[0].id !== firstId) return;
    const had = _allMessages.length;
    _allMessages = mergeMessages(result.messages, _allMessages);
    _loadedOffset = result.beforeCount;
    renderMessages(_allMessages, { keepAnchor: true, prepended: _allMessages.length - had });
}
(window.__coral ||= {}).loadEarlierMessages = loadEarlierMessages;

// The board keeps about this many messages in the DOM while you read at the bottom (#4492): a poll appends what is new, and once
// a page more than this has built up the oldest go (they count as not loaded, so scrolling up or "Load earlier" brings them back).
// Nothing is dropped while you read higher up, select messages or wait for a search jump.
const BOARD_KEEP = 300;
const ROW = ':scope > .mb-message, :scope > .mb-queue-run';

function isAtBottom(container) {
    return (container.scrollHeight - container.scrollTop - container.clientHeight) < 50;
}

// Scrolling near the top loads the page before the oldest message shown, as "Load earlier" does. Not the scroll that moving
// keyboard focus onto "Load earlier" makes (the browser scrolls it into view): that is the reader reaching the button, who
// then presses it or not (Frontend's finding on #4492).
function wireBoardScroll(container) {
    if (container._mbScrollWired) return;
    container._mbScrollWired = true;
    let focusScrollUntil = 0;
    container.addEventListener('focusin', (e) => {
        if (e.target.closest?.('.mb-load-earlier')) focusScrollUntil = performance.now() + 300;
    });
    container.addEventListener('scroll', () => {
        if (performance.now() < focusScrollUntil && document.activeElement?.closest?.('.mb-load-earlier')) return;
        if (container.scrollTop < 80 && _loadedOffset > 0) loadEarlierMessages();
        if (_unseen && isAtBottom(container)) clearNewPill();
    }, { passive: true });
}

function boardHeaderHtml(messages) {
    // "Load Earlier" button if there are older messages
    let loadEarlierHtml = '';
    if (_loadedOffset > 0) {
        const remaining = _loadedOffset;
        loadEarlierHtml = `<div class="mb-load-earlier" style="text-align:center;padding:8px 0 12px">
            <button class="btn btn-small" data-on-click="mb-earlier" class="mb-project-meta">
                Load ${Math.min(remaining, PAGE_SIZE)} earlier messages (${remaining} remaining)
            </button>
        </div>`;
    }
    // Message count indicator
    const countHtml = `<div class="mb-message-count">
        Showing ${messages.length} of ${_totalMessages} messages
    </div>`;
    return loadEarlierHtml + countHtml;
}

// One row: a message, or a collapsed run of task-queue lines. prev is the item before it (grouping by sender).
function renderBoardItem(item, prev) {
    if (item.kind === 'queue') return renderTaskQueueRun(item, formatTime);
    const m = item.m;
    const color = getAgentColor(m.job_title || 'Unknown');
    const agent = m.job_title || 'Unknown';
    const prevMsg = prev && prev.kind === 'msg' ? prev.m : null;
    const prevAgent = prevMsg ? (prevMsg.job_title || 'Unknown') : null;
    const sameAsPrev = agent === prevAgent;
    const spacing = sameAsPrev ? 'mb-message-grouped' : 'mb-message-first';
    const isLeader = /orchestrator/i.test(agent) || isOperatorId(m.subscriber_id);
    const alignClass = isLeader ? ' board-msg-left' : ' board-msg-right';
    const selectedClass = _selectMode && _selectedIds.has(m.id) ? ' mb-message-selected' : '';
    const checkbox = _selectMode ? `<input type="checkbox" class="mb-select-checkbox" data-msg-id="${Number(m.id)}" ${_selectedIds.has(m.id) ? 'checked' : ''} data-on-click="mb-msg-select">` : '';
    return `
        <div class="mb-message ${spacing}${alignClass}${selectedClass}" data-message-id="${escapeAttr(String(m.id))}" style="${boardMessageColorStyle(color)}">
            ${boardAvatar(m, agent)}
            <div class="mb-message-header">
                ${checkbox}
                <span class="mb-agent-name">${m.icon ? escapeHtml(m.icon) + ' ' : ''}${escapeHtml(agent)}</span>
                ${externalMessageBadge(m)}${hubUserBadge(m)}
                <span class="mb-message-time">${formatTime(m.created_at)}</span>
                ${BOARD_SAVE_BUTTON(currentProject || '', keyLabel(currentProject || ''), String(m.created_at || ''))}
                <button class="mb-delete-msg-btn" data-on-click="mb-msg-delete" data-msg-id="${Number(m.id)}" title="Delete message">&times;</button>
            </div>
            <div class="mb-message-body">${renderBoardMessageBody(m)}</div>
        </div>`;
}

// Where messages[i] is in the list, searching from the end (new rows are at the end).
function indexOfId(messages, id) {
    for (let i = messages.length - 1; i >= 0; i--) if (messages[i].id === id) return i;
    return -1;
}

// A row's first message id: a message row's own, a task-queue run's first line.
function rowFirstId(row) {
    return Number(row.classList.contains('mb-queue-run') ? row.dataset.queueRun : row.dataset.messageId);
}

// "Load earlier" (and a search jump's older pages) draws only the rows the page adds, in one batch above the rows shown (#4638): every older page used to
// draw the whole board again, which cost more with each page (a board of 700 took over a second in UX's run). The rows shown stay as they are, with the focus
// and what the reader opened; the first of them follows the new rows' last for grouping. Returns false (and the caller draws it all) when the rows shown are not
// the loaded messages after the page, in select mode, or when a task-queue run could join across the page's edge.
function prependEarlier(container, count) {
    const first = container.querySelector(ROW);
    if (!first || _selectMode || first.classList.contains('mb-queue-run') || container.querySelector(':scope > .empty-state, :scope > .loading-indicator')) return false;
    if (!_allMessages[count] || _allMessages[count].id !== rowFirstId(first)) return false;
    const items = groupTaskQueueRuns(_allMessages.slice(0, count + 1), true); // with the first message shown, to see what it would join
    const tail = items[items.length - 1];
    if (tail.kind !== 'msg' || tail.m.id !== _allMessages[count].id) return false;
    const fresh = items.slice(0, -1);
    const tmp = document.createElement('template');
    tmp.innerHTML = fresh.map((item, i) => renderBoardItem(item, i > 0 ? fresh[i - 1] : null)).join('');
    const added = [...tmp.content.children];
    const prev = fresh[fresh.length - 1];
    const grouped = !!prev && prev.kind === 'msg' && (prev.m.job_title || 'Unknown') === (tail.m.job_title || 'Unknown');
    first.className = first.className.replace(/mb-message-(first|grouped)/, grouped ? 'mb-message-grouped' : 'mb-message-first'); // in place: the same class order as a whole render
    first.before(tmp.content);
    for (const el of added) resolveBoardArtifacts(el, state.currentBoardServer);
    updateBoardHeader(container);
    return true;
}

// The jump's landing: the message in the middle of the board with the focus on it (Save for Later's jump and the search's, #4510, #4771); the one before it loses its mark.
function revealMessage(target) {
    for (const old of document.querySelectorAll('#mb-messages .chat-search-target')) if (old !== target) old.classList.remove('chat-search-target');
    const run = target.closest('details');
    if (run) run.open = true; // a task-queue line inside a collapsed row
    target.classList.add('chat-search-target');
    target.scrollIntoView({ block: 'center', behavior: 'smooth' });
    target.setAttribute('tabindex', '-1'); // the focus goes to the message
    target.focus({ preventScroll: true });
}

/** Go to a message of the board, loading the older pages until it is there (the board's own jump; the search's matches use it). */
function focusMessageById(id) {
    const container = document.getElementById('mb-messages');
    const target = container && container.querySelector(`[data-message-id="${CSS.escape(String(id))}"]`);
    if (target) { revealMessage(target); highlightBoard(container); return; }
    pendingMessageId = String(id);
    loadEarlierMessages();
}

/** "First": every older page is loaded, and the board is at its first message. */
async function jumpToFirst() {
    const container = document.getElementById('mb-messages');
    if (!container) return;
    for (let guard = 0; _loadedOffset > 0 && guard < 400; guard++) {
        const before = _loadedOffset;
        await loadEarlierMessages();
        if (_loadedOffset >= before) break;
    }
    container.scrollTop = 0;
    const first = container.querySelector(ROW);
    if (first) { first.setAttribute('tabindex', '-1'); first.focus({ preventScroll: true }); }
}

const SEARCH_HOOKS = { me: OPERATOR_ID, redraw: () => highlightBoard(document.getElementById('mb-messages')), jumpTo: id => focusMessageById(id), first: () => jumpToFirst(), latest: () => jumpToLatest() };

function renderMessages(messages, { keepAnchor = false, prepended = 0 } = {}) {
    const container = document.getElementById('mb-messages');
    wireBoardScroll(container);
    if (!messages.length) {
        container.innerHTML = '<div class="empty-state">No messages yet</div>';
        return;
    }
    // Save scroll position before replacing content
    const prevScrollTop = container.scrollTop;
    const prevScrollHeight = container.scrollHeight;
    const wasAtBottom = isAtBottom(container);
    // "Load earlier" pressed from the keyboard is drawn again with the list: focus goes to the new one (UX on #4492).
    const earlierFocused = !!document.activeElement?.closest?.('#mb-messages > .mb-load-earlier');

    // Runs of task-queue lines are one collapsed row (#3343), except in select mode.
    if (!(prepended > 0 && prependEarlier(container, prepended))) {
        const items = groupTaskQueueRuns(messages, !_selectMode);
        container.innerHTML = boardHeaderHtml(messages) + items.map((item, i) => renderBoardItem(item, i > 0 ? items[i - 1] : null)).join('');
        resolveBoardArtifacts(container, state.currentBoardServer);
    }
    if (earlierFocused) {
        // The button again, or with every message loaded the count line, so focus doesn't drop to the page.
        const next = container.querySelector(':scope > .mb-load-earlier button');
        if (next) next.focus({ preventScroll: true });
        else {
            const count = container.querySelector(':scope > .mb-message-count');
            if (count) { count.tabIndex = -1; count.focus({ preventScroll: true }); }
        }
    }
    if (pendingMessageId) {
        const target = container.querySelector(`[data-message-id="${CSS.escape(pendingMessageId)}"]`);
        if (target) {
            revealMessage(target);
            pendingMessageId = null;
            highlightBoard(container);
            return; // the jump decides the scroll position
        } else if (_loadedOffset > 0) {
            // Search hits may be older than the initial latest page. Keep
            // loading real pages until the stable message id is present.
            loadEarlierMessages();
        }
    }
    highlightBoard(container);
    if (keepAnchor) {
        // Older messages went in above: what you were reading stays where it was.
        container.scrollTop = prevScrollTop + (container.scrollHeight - prevScrollHeight);
    } else if (wasAtBottom) {
        container.scrollTop = container.scrollHeight;
    } else {
        container.scrollTop = prevScrollTop;
    }
}

// A poll's new messages (already merged into _allMessages) go in after the rows shown (#4492): message rows already shown are
// left as they are, and only a task-queue run at the end is drawn again, since the new lines can extend it. Falls back to a whole
// render when the rows shown don't match the loaded messages.
function appendNewMessages(count) {
    const container = document.getElementById('mb-messages');
    const rows = container.querySelectorAll(ROW);
    const last = rows[rows.length - 1];
    const lastIdx = last ? indexOfId(_allMessages, rowFirstId(last)) : -1;
    if (!last || lastIdx < 0 || lastIdx >= _allMessages.length - count || container.querySelector(':scope > .empty-state, :scope > .loading-indicator')) {
        renderMessages(_allMessages);
        return;
    }
    const wasAtBottom = isAtBottom(container);
    const collapse = !_selectMode;
    let start, prev, replace = null;
    if (last.classList.contains('mb-queue-run') && collapse) {
        start = lastIdx; // the run is drawn again with the new lines it gains
        replace = last;
        const before = rows.length > 1 ? rows[rows.length - 2] : null;
        prev = before && before.classList.contains('mb-message') ? { kind: 'msg', m: _allMessages[indexOfId(_allMessages, rowFirstId(before))] } : null;
        if (prev && !prev.m) prev = null;
    } else {
        start = lastIdx + 1; // a message row: kept, and the new rows group with it
        prev = { kind: 'msg', m: _allMessages[lastIdx] };
    }
    const items = groupTaskQueueRuns(_allMessages.slice(start), collapse);
    const tmp = document.createElement('template');
    tmp.innerHTML = items.map((item, i) => renderBoardItem(item, i > 0 ? items[i - 1] : prev)).join('');
    const added = [...tmp.content.children];
    if (replace) replace.replaceWith(tmp.content); else container.appendChild(tmp.content);
    for (const el of added) resolveBoardArtifacts(el, state.currentBoardServer);
    if (wasAtBottom) trimBoardWindow(container);
    updateBoardHeader(container);
    highlightBoard(container);
    if (wasAtBottom) container.scrollTop = container.scrollHeight;
    else noteUnseenMessages(count);
}

// Reading higher up: the posts a poll brings are not shown to you, so a small pill at the board's bottom edge counts them and jumps to the latest
// (#4502). A polite live region announces it; it goes when the board is at the bottom again, by the button or by scrolling.
let _unseen = 0;
function noteUnseenMessages(n) {
    if (!n) return;
    _unseen += n;
    renderNewPill();
}
function renderNewPill() {
    const pill = document.getElementById('mb-new-pill');
    if (!pill) return;
    // The live region stays displayed and only its text changes (a region hidden and shown with its text can go unannounced).
    const btn = pill.querySelector('button');
    btn.textContent = _unseen > 0 ? `${_unseen} new message${_unseen === 1 ? '' : 's'}, jump to latest` : '';
    btn.hidden = _unseen <= 0;
}
function clearNewPill() { _unseen = 0; renderNewPill(); }
function jumpToLatest() {
    const container = document.getElementById('mb-messages');
    if (!container) return;
    container.scrollTop = container.scrollHeight;
    clearNewPill();
    container.focus({ preventScroll: true });
}

// Drops the oldest loaded messages beyond BOARD_KEEP, and their rows, once a page more has built up (see BOARD_KEEP).
function trimBoardWindow(container) {
    if (_selectMode || pendingMessageId || _allMessages.length <= BOARD_KEEP + PAGE_SIZE) return;
    const drop = _allMessages.length - BOARD_KEEP;
    const firstKept = _allMessages[drop].id;
    // The message a search or Save for Later jump just landed on is kept: the jump's smooth scroll may not have left the bottom yet when a poll comes.
    const target = container.querySelector(':scope .chat-search-target');
    if (target && Number(target.dataset.messageId) < firstKept) return;
    _allMessages = _allMessages.slice(drop);
    _loadedOffset += drop;
    for (const row of container.querySelectorAll(ROW)) {
        if (row.classList.contains('mb-queue-run')) {
            const lines = [...row.querySelectorAll('.mb-queue-line')].map(l => Number(l.dataset.messageId));
            if (lines[lines.length - 1] < firstKept) { row.remove(); continue; }
            if (lines[0] < firstKept) { // a run across the cut: drawn again from its first kept line
                const run = groupTaskQueueRuns(_allMessages.slice(0, lines.filter(id => id >= firstKept).length), true)[0];
                const tmp = document.createElement('template');
                tmp.innerHTML = renderBoardItem(run, null);
                row.replaceWith(tmp.content);
            }
            break;
        }
        if (rowFirstId(row) < firstKept) { row.remove(); continue; }
        // The first message shown starts its group: there is no row above it now.
        row.classList.replace('mb-message-grouped', 'mb-message-first');
        break;
    }
}

// The "Load earlier" button and the count above the rows, after a poll changed them: their text changes in place, so keyboard
// focus on the button stays (UX on #4492); the button's row goes in or out only when there are older messages or no longer.
function updateBoardHeader(container) {
    const tmp = document.createElement('template');
    tmp.innerHTML = boardHeaderHtml(_allMessages);
    const fresh = sel => tmp.content.querySelector(sel);
    const count = container.querySelector(':scope > .mb-message-count');
    if (count) count.textContent = fresh('.mb-message-count').textContent;
    else container.prepend(fresh('.mb-message-count'));
    const row = container.querySelector(':scope > .mb-load-earlier');
    const newRow = fresh('.mb-load-earlier');
    if (row && newRow) row.querySelector('button').textContent = newRow.querySelector('button').textContent;
    else if (row) row.remove();
    else if (newRow) container.prepend(newRow);
}

function formatTime(iso) {
    try {
        const d = new Date(iso);
        return d.toLocaleString();
    } catch {
        return iso;
    }
}

// ── Subscribers ──────────────────────────────────────────────────────────

function navigateToAgentSession(sessionName, displayName) {
    if (sessionName) {
        // session_name format is "<agentType>-<uuid>" — extract the UUID
        const dashIdx = sessionName.indexOf('-');
        const sessionId = dashIdx >= 0 ? sessionName.substring(dashIdx + 1) : sessionName;
        // Try live session first, fall back to history
        const live = (state.liveSessions || []).find(s => s.session_id === sessionId);
        if (live) {
            window.selectLiveSession(live.name, live.agent_type, live.session_id, sessionServer(live));
        } else {
            window.selectHistorySession(sessionId);
        }
        return;
    }
    // Fallback: search by display name
    navigateToAgentHistoryByName(displayName);
}

async function navigateToAgentHistoryByName(displayName) {
    const resp = await fetch(`/api/sessions/history?page_size=50`);
    const data = await resp.json();
    const sessions = data.sessions || [];
    const match = sessions.find(s => s.display_name === displayName || s.agent_name === displayName);
    if (match) {
        window.selectHistorySession(match.session_id);
    } else {
        showToast(`No history found for ${displayName}`);
    }
}

async function loadBoardSubscribers(project) {
    try {
        const subs = await fetchSubscribers(project);
        setBoardMembers([...new Set(subs.map(s => s.job_title || s.subscriber_id).filter(n => typeof n === 'string' && n))].sort().slice(0, 50)); // the From field's names
        // The column and the phone sheet (#3610) show the same list.
        const lists = ['mb-subscribers-list', 'mb-subscribers-sheet-list'].map(id => document.getElementById(id)).filter(Boolean);
        if (!subs.length) {
            for (const list of lists) list.innerHTML = '<li class="mb-project-meta">No subscribers</li>';
            return;
        }
        const html = subs.map(s => {
            const displayName = escapeAttr(s.job_title || s.subscriber_id);
            const sessionName = escapeAttr(s.session_name || '');
            const icon = s.icon ? `<span class="agent-icon">${escapeHtml(s.icon)}</span> ` : '';
            return `<li class="mb-subscriber">
                <div class="mb-subscriber-name">${icon}<button type="button" class="subscriber-history-link" data-on-click="mb-subscriber-history" data-session="${escapeAttr(sessionName)}" data-name="${escapeAttr(displayName)}" title="View chat history">${escapeHtml(s.job_title)}</button></div>
                <div class="mb-subscriber-id">${escapeHtml(s.subscriber_id)}</div>
            </li>`;
        }).join('');
        for (const list of lists) list.innerHTML = html;
    } catch (e) {
        console.error('Failed to load subscribers:', e);
    }
}

// ── Subscribers sheet (phone width, #3610) ───────────────────────────────

export function openBoardSubscribers() {
    const sheet = document.getElementById('mb-subscribers-sheet');
    if (!sheet || !currentProject) return;
    if (!sheet._wired) {
        sheet._wired = true;
        // Opening a subscriber's history leaves the board: the sheet closes first.
        sheet.addEventListener('click', e => { if (e.target.closest('.subscriber-history-link')) hideBoardSubscribers(); });
        // Escape (modal_chrome.js) and the other closes all hide it: the button follows.
        new MutationObserver(() => {
            const btn = document.getElementById('mb-subscribers-btn');
            if (btn) btn.setAttribute('aria-expanded', String(sheet.style.display !== 'none'));
        }).observe(sheet, { attributes: true, attributeFilter: ['style'] });
    }
    attachDialogFocus(sheet);
    sheet.style.display = 'flex';
}

export function hideBoardSubscribers() {
    const sheet = document.getElementById('mb-subscribers-sheet');
    if (sheet && sheet.style.display !== 'none') sheet.style.display = 'none';
}

// ── Dashboard subscription ───────────────────────────────────────────────

async function subscribeDashboard(project) {
    try {
        await boardFetch(project, `/subscribe`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ subscriber_id: OPERATOR_ID, job_title: 'Operator' }),
        });
    } catch (e) {
        // Best effort
    }
}

// ── Posting ──────────────────────────────────────────────────────────────

export async function postBoardMessage() {
    if (!currentProject) return;
    const input = document.getElementById('mb-post-input');
    const content = input.value.trim();
    if (!content) return;

    try {
        const resp = await boardFetch(currentProject, `/messages`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ subscriber_id: OPERATOR_ID, as: 'Operator', content }),
        });
        if (!resp.ok) {
            // The draft stays; say why, in the server's words when it gives them (a per-sender rate
            // limit's "too many messages…; retry in 20s", #3435).
            const text = await resp.text().catch(() => '');
            let reason = '';
            try { const b = JSON.parse(text); if (b && typeof b.error === 'string') reason = b.error; } catch { /* not JSON */ }
            console.error('Post failed:', resp.status, text);
            showToast(reason ? `Not posted: ${reason}` : `Not posted (HTTP ${resp.status}).`, true);
            return;
        }
        input.value = '';
        // Fetch new messages since our last known total
        await _pollNewMessages();
    } catch (e) {
        console.error('Failed to post message:', e);
    }
}

// ── Pause / Resume reads ─────────────────────────────────────────────────

async function loadPausedState(project) {
    try {
        const resp = await boardFetch(project, `/paused`);
        const data = await resp.json();
        isPaused = !!data.paused;
        updatePauseButton();
    } catch (e) {
        isPaused = false;
        updatePauseButton();
    }
}

function updatePauseButton() {
    const buttons = [
        document.getElementById('mb-pause-btn'),
        document.getElementById('board-chat-pause-btn'),
    ];
    for (const btn of buttons) {
        if (!btn) continue;
        if (isPaused) {
            btn.textContent = 'Resume Board';
            btn.classList.add('mb-action-danger');
        } else {
            btn.textContent = 'Pause Board';
            btn.classList.remove('mb-action-danger');
        }
    }
}

export async function toggleBoardPause() {
    if (!currentProject) return;
    const action = isPaused ? 'resume' : 'pause';
    try {
        const resp = await boardFetch(currentProject, `/${action}`, {
            method: 'POST',
        });
        const data = await resp.json();
        isPaused = !!data.paused;
        updatePauseButton();
    } catch (e) {
        console.error('Failed to toggle pause:', e);
    }
}

// ── Delete message ───────────────────────────────────────────────────────

// The × on a message asks first: a deleted message is gone for every reader. The wording names the
// sender and the start of the text, so a mis-click on the wrong row is easy to spot (#3696).
 function confirmDeleteBoardMessage(messageId) {
    const m = _allMessages.find(x => x.id === messageId);
    const text = m ? String(m.content || '').replace(/\s+/g, ' ').trim() : '';
    const what = m ? `the message from ${m.job_title || 'Unknown'}${text ? ` ("${text.length > 60 ? text.slice(0, 60) + '\u2026' : text}")` : ''}` : `message #${messageId}`;
    window.showConfirmModal('Delete Message', `Delete ${what}? This cannot be undone.`, () => deleteBoardMessage(messageId));
}

export async function deleteBoardMessage(messageId) {
    if (!currentProject) return;
    try {
        const resp = await boardFetch(currentProject, `/messages/${pathId(messageId)}`, {
            method: 'DELETE',
        });
        if (!resp.ok) {
            console.error('Delete message failed:', resp.status);
            return;
        }
        // Remove locally and re-render
        _allMessages = _allMessages.filter(m => m.id !== messageId);
        _totalMessages = Math.max(0, _totalMessages - 1);
        _countedTotal = Math.max(0, _countedTotal - 1);
        renderMessages(_allMessages);
    } catch (e) {
        console.error('Failed to delete message:', e);
    }
}

// ── Delete project ───────────────────────────────────────────────────────

// Through the Coral Hub, deleting a board needs Launch on its machine (#3433): below that the
// button is shown disabled with the reason (#3443), and redrawn once the access list is in.
function syncBoardDeleteButton(project) {
    const btn = document.getElementById('mb-delete-btn');
    if (!btn || !project) return;
    const blocked = launchBlockedReason(splitKey(project).server);
    btn.disabled = !!blocked;
    btn.setAttribute('aria-disabled', blocked ? 'true' : 'false');
    btn.title = blocked || '';
    btn.textContent = blocked ? 'Delete (needs Launch)' : 'Delete';
}
document.addEventListener('coral:server-access-loaded', () => syncBoardDeleteButton(currentProject));

export function deleteMessageBoardProject() {
    const blocked = currentProject ? launchBlockedReason(splitKey(currentProject).server) : '';
    if (blocked) { showToast(blocked, true); return; }
    if (!currentProject) return;
    const project = currentProject;
    window.showConfirmModal('Delete Board', `Delete project "${keyLabel(project)}" and all its messages?`, async () => {
        try {
            await boardFetch(project, ``, { method: 'DELETE' });
            showMessageBoardProjects();
        } catch (e) {
            console.error('Failed to delete project:', e);
        }
    });
}

// ── Polling ──────────────────────────────────────────────────────────────

async function _pollNewMessages() {
    const project = currentProject;
    // Wait for the initial page (an after_id=0 poll would fetch the oldest
    // page), and never run two polls at once.
    if (!project || isPaused || _loadedProject !== project || _pollInFlight) return;
    _pollInFlight = true;
    try {
        // What follows the newest message shown.
        const last = _allMessages[_allMessages.length - 1];
        const result = await fetchMessages(project, PAGE_SIZE, { after_id: last ? last.id : 0 });
        if (currentProject !== project || _loadedProject !== project) return;
        // With after_id every returned row is new, even if deletes kept the total flat.
        const fresh = result.messages.filter(m => !last || m.id > last.id);
        if (fresh.length < PAGE_SIZE) {
            // The total should be what was counted plus the new ones; less means messages were deleted (a secret taken down,
            // say), also when one arrived in the same tick. A whole page means more are waiting: counted, checked once caught up.
            if (result.total !== _countedTotal + fresh.length) _staleRows = true;
            _countedTotal = result.total;
        } else {
            _countedTotal += fresh.length;
        }
        const totalChanged = _totalMessages !== result.total;
        _totalMessages = result.total;
        const container = document.getElementById('mb-messages');
        if (_staleRows && !_selectMode && !pendingMessageId && (!container || isAtBottom(container))) {
            // Rows shown may hold a deleted message: the latest page again. Not while you read higher up, select messages
            // or wait for a search jump; then at a later poll.
            const page = await fetchMessages(project, PAGE_SIZE);
            if (currentProject !== project || _loadedProject !== project) return;
            _allMessages = page.messages;
            _totalMessages = _countedTotal = page.total;
            _loadedOffset = page.beforeCount;
            _staleRows = false;
            renderMessages(_allMessages);
            return;
        }
        if (fresh.length > 0) {
            _allMessages = mergeMessages(_allMessages, fresh);
            appendNewMessages(fresh.length);
        } else if (totalChanged && container && container.querySelector(ROW)) {
            updateBoardHeader(container);
        }
    } catch (e) {
        // Silent fail on poll
    } finally {
        _pollInFlight = false;
    }
}

(window.__coral ||= {}).pollBoardNow = _pollNewMessages; // tests: a poll without waiting 10 s

let _pollCount = 0;

function startBoardPoll() {
    stopBoardPoll();
    _pollCount = 0;
    pollTimer = pollEvery(() => {
        if (currentProject && (platform.isNative || !document.hidden)) {
            _pollNewMessages();
            // Refresh subscribers less often (every 30s instead of every 10s)
            _pollCount++;
            if (_pollCount % 3 === 0) {
                loadBoardSubscribers(currentProject);
            }
        }
    }, 10000, { whenHidden: true }); // the native app polls while hidden
}

function stopBoardPoll() {
    if (pollTimer) {
        clearInterval(pollTimer);
        pollTimer = null;
    }
}

// ── Sleep / Wake ─────────────────────────────────────────────────────

async function loadSleepState(project) {
    try {
        const resp = await teamFetch(project, n => `/api/sessions/live/team/${encodeURIComponent(n)}/sleep-status`);
        const data = await resp.json();
        isSleeping = !!data.sleeping;
        updateSleepUI();
    } catch (e) {
        isSleeping = false;
        updateSleepUI();
    }
}

function updateSleepUI() {
    const btn = document.getElementById('mb-sleep-btn');
    if (btn) {
        btn.textContent = isSleeping ? 'Wake Team' : 'Sleep Team';
        btn.classList.toggle('btn-warning', isSleeping);
    }
    const banner = document.getElementById('mb-sleep-banner');
    if (banner) {
        banner.style.display = isSleeping ? '' : 'none';
    }
    const input = document.getElementById('mb-post-input');
    const sendBtn = document.querySelector('#mb-board .btn-primary');
    if (input) {
        input.disabled = isSleeping;
        input.placeholder = isSleeping ? 'Team is sleeping...' : 'Post a message as Operator...';
    }
    if (sendBtn) {
        sendBtn.disabled = isSleeping;
        sendBtn.style.opacity = isSleeping ? '0.4' : '';
    }
}

export function toggleBoardSleep() {
    if (!currentProject) return;
    const action = isSleeping ? 'wake' : 'sleep';
    const doIt = async () => {
        try {
            const resp = await teamFetch(currentProject, n => `/api/sessions/live/team/${encodeURIComponent(n)}/${action}`, { method: 'POST' });
            const data = await resp.json();
            isSleeping = !!data.sleeping;
            updateSleepUI();
            loadLiveSessions();
        } catch (e) {
            console.error('Failed to toggle sleep:', e);
        }
    };
    if (!isSleeping) {
        window.showConfirmModal('Sleep Team', `Put all agents on "${keyLabel(currentProject)}" to sleep?`, doIt);
    } else {
        doIt();
    }
}

// ── Export ────────────────────────────────────────────────────────────────

export async function showExportBoardModal() {
    if (!currentProject) return;

    // Fetch subscribers to build the agent selection list
    const subs = await fetchSubscribers(currentProject);

    // Build modal HTML
    const agentCheckboxes = subs.map(s => {
        const role = escapeHtml(s.job_title || s.subscriber_id);
        const sid = escapeAttr(s.subscriber_id);
        return `<label style="display:flex;align-items:center;gap:6px;padding:2px 0;cursor:pointer;white-space:nowrap">
            <input type="checkbox" class="export-agent-cb" value="${escapeAttr(sid)}" data-role="${escapeAttr(s.job_title || s.subscriber_id)}">
            <span style="font-weight:600;font-size:0.75rem">${role}</span>
        </label>`;
    }).join('');

    const modal = document.getElementById('mb-export-modal');
    const body = document.getElementById('mb-export-body');
    const actions = document.getElementById('mb-export-actions');

    body.innerHTML = `
        <div style="margin-bottom:16px">
            <p style="margin:0 0 8px;font-size:0.875rem">Export <strong>${escapeHtml(keyLabel(currentProject))}</strong> board messages.</p>
            <p style="margin:0 0 12px;font-size:0.8125rem;color:var(--text-muted)">Optionally include individual agent chat histories, interleaved by timestamp.</p>
            <p id="export-secrets-note" style="margin:0;font-size:0.8125rem;color:var(--text-muted)">The export holds agent output as written and may include secrets the agents printed. It is plain text, not encrypted.</p>
        </div>
        <div style="margin-bottom:16px">
            <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:8px">
                <label style="font-weight:600;font-size:0.8125rem">Include agent chats:</label>
                <div style="display:flex;gap:8px">
                    <button class="mb-action-btn" data-on-click="mb-export-agents" data-checked="1" style="font-size:0.6875rem;padding:2px 8px">All</button>
                    <button class="mb-action-btn" data-on-click="mb-export-agents" data-checked="0" style="font-size:0.6875rem;padding:2px 8px">None</button>
                </div>
            </div>
            <div style="max-height:200px;overflow-y:auto;border:1px solid var(--border);border-radius:6px;padding:4px 12px">
                ${agentCheckboxes || '<p style="font-size:0.8125rem;color:var(--text-muted)">No subscribers</p>'}
            </div>
        </div>
        <div style="margin-bottom:8px">
            <label style="font-weight:600;font-size:0.8125rem;display:block;margin-bottom:4px">Format:</label>
            <select id="export-format-select" style="padding:6px 10px;border:1px solid var(--border);border-radius:6px;background:var(--bg-secondary);color:var(--text-primary);font-size:0.8125rem">
                <option value="html">HTML (styled, shareable)</option>
                <option value="json">JSON (canonical data)</option>
                <option value="markdown">Markdown</option>
            </select>
        </div>
    `;
    actions.innerHTML = `
        <button class="btn" data-on-click="mb-export-close">Cancel</button>
        <button class="btn btn-primary" data-on-click="mb-export-do">Export</button>
    `;
    attachDialogFocus(modal);
    modal.style.display = 'flex';
}

 function hideExportBoardModal() {
    const modal = document.getElementById('mb-export-modal');
    if (modal) modal.style.display = 'none';
}

export async function doExportBoard() {
    const format = document.getElementById('export-format-select').value;
    const selectedAgents = Array.from(document.querySelectorAll('.export-agent-cb:checked'))
        .map(cb => ({ sessionId: cb.value, role: cb.dataset.role }));

    // Show progress in the export dialog's own footer
    const actions = document.getElementById('mb-export-actions');
    actions.innerHTML = '<span style="color:var(--text-muted);font-size:0.8125rem">Exporting...</span>';

    try {
        // 1. Fetch all board messages (in pages, #3718)
        const boardMessages = await fetchAllBoardMessages(currentProject);

        // 2. Fetch subscribers
        const subsResp = await boardFetch(currentProject, `/subscribers`);
        const subscribers = await subsResp.json();

        // 3. Build entries from board messages
        const entries = boardMessages.map(m => ({
            timestamp: (m.created_at || '').substring(0, 16),
            role: m.job_title || m.subscriber_id || 'unknown',
            content: m.content || '',
            source: 'board',
        }));

        // 4. Fetch live sessions to get working directories and names
        const liveResp = await fetch('/api/sessions/live');
        const liveRaw = await liveResp.json();
        // The list is {sessions, servers} when remotes exist; only this board's server counts.
        const boardSrv = splitKey(currentProject).server;
        const liveSessions = (Array.isArray(liveRaw) ? liveRaw : (liveRaw.sessions || [])).filter(s => sessionServer(s) === boardSrv);

        // 5. Fetch selected agent chat histories and merge
        for (const agent of selectedAgents) {
            try {
                // Find the live session for this subscriber to get name + working_directory
                const live = liveSessions.find(s => s.display_name === agent.role);
                // Live: the chat endpoint (it has working_directory for JSONL resolution); ended:
                // the history endpoint. Both in pages (#3718).
                const histData = live ? await fetchAllLiveChat(live) : await fetchAllHistory(agent.sessionId);
                if (histData.messages && histData.messages.length) {
                    const parsed = parseAgentHistory(histData.messages, agent.role);
                    entries.push(...parsed);
                }
            } catch (e) {
                console.warn(`Failed to load history for ${agent.sessionId}:`, e);
            }
        }

        // 5. Sort by timestamp
        entries.sort((a, b) => a.timestamp.localeCompare(b.timestamp));

        // 6. Build export data
        const exportData = {
            project: keyLabel(currentProject),
            exported_at: new Date().toISOString(),
            subscribers: subscribers.map(s => ({
                session_id: s.subscriber_id,
                role: s.job_title || s.subscriber_id,
            })),
            messages: entries,
            stats: {
                total: entries.length,
                board: entries.filter(e => e.source === 'board').length,
                agent_chat: entries.filter(e => e.source === 'agent-chat').length,
            },
        };

        // 7. Render and download
        let output, filename, mime;
        if (format === 'json') {
            output = JSON.stringify(exportData, null, 2);
            filename = `${keyLabel(currentProject)}-export.json`;
            mime = 'application/json';
        } else if (format === 'markdown') {
            output = renderExportMarkdown(exportData);
            filename = `${keyLabel(currentProject)}-export.md`;
            mime = 'text/markdown';
        } else {
            output = renderExportHTML(exportData);
            filename = `${keyLabel(currentProject)}-export.html`;
            mime = 'text/html';
        }

        // Trigger download
        const blob = new Blob([output], { type: mime });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = filename;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        URL.revokeObjectURL(url);

        hideExportBoardModal();
        showToast(`Exported ${entries.length} messages as ${format}`);
    } catch (e) {
        console.error('Export failed:', e);
        actions.innerHTML = `<span style="color:var(--error)">Export failed: ${escapeHtml(e.message)}</span>
            <button class="btn" data-on-click="mb-export-close">Close</button>`;
    }
}

// ── Agent History Parsers ─────────────────────────────────────────────────
// Modular parsers for different agent JSONL formats. Each returns an array
// of { timestamp, role, content, source } entries.

function parseAgentHistory(messages, agentRole) {
    const entries = [];
    for (const msg of messages) {
        const ts = (msg.timestamp || '').substring(0, 16);
        if (!ts) continue;

        if (msg.type === 'user') {
            // Human/operator message to this agent
            const content = typeof msg.content === 'string' ? msg.content : '';
            if (!content.trim()) continue;
            // Skip system notifications (board read reminders, task notifications)
            if (content.startsWith('You have ') && content.includes('unread message')) continue;
            if (content.startsWith('<task-notification>')) continue;
            entries.push({
                timestamp: ts,
                role: `Operator → ${agentRole}`,
                content: content.trim(),
                source: 'agent-chat',
            });
        } else if (msg.type === 'assistant') {
            // Agent's text response (skip tool-only messages)
            const text = (msg.text || '').trim();
            if (!text) continue;
            entries.push({
                timestamp: ts,
                role: agentRole,
                content: text,
                source: 'agent-chat',
            });
        }
        // Skip tool_result messages — they're tool output, not conversation
    }
    return entries;
}

function agentHue(name) {
    let h = 0;
    for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) & 0xFFFFFF;
    return h % 360;
}

function renderExportMarkdown(d) {
    let out = `# Board Chat Export: ${d.project}\n\n`;
    out += `**Exported**: ${d.exported_at}\n`;
    out += `**Messages**: ${d.stats.total}\n`;
    if (d.stats.agent_chat > 0) {
        out += `**Board**: ${d.stats.board} | **Agent chats**: ${d.stats.agent_chat}\n`;
    }
    if (d.subscribers.length) {
        out += `**Subscribers**: ${d.subscribers.length}\n\n`;
        out += '| Agent | Role |\n|-------|------|\n';
        d.subscribers.forEach(s => { out += `| ${s.subscriber_id} | ${s.role} |\n`; });
    }
    out += '\n---\n\n## Messages\n\n';
    d.messages.forEach(e => {
        const tag = e.source === 'agent-chat' ? ' (agent chat)' : '';
        out += `**[${e.timestamp}] ${e.role}${tag}:**\n\n${e.content}\n\n---\n\n`;
    });
    return out;
}

function renderExportHTML(d) {
    const msgs = d.messages.map(e => {
        const cls = e.source === 'agent-chat' ? 'msg side-chat' : 'msg';
        const color = `hsl(${agentHue(e.role)}, 60%, 45%)`;
        const tag = e.source === 'agent-chat' ? '<span class="msg-tag">AGENT CHAT</span>' : '';
        return `<div class="${cls}">
  <div class="msg-header">
    <span class="msg-role" style="color:${color}">${escapeHtml(e.role)}</span>
    <span class="msg-time">${escapeHtml(e.timestamp)}</span>
    ${tag}
  </div>
  <div class="msg-content">${renderMarkdown(e.content, undefined, 'board')}</div>
</div>`;
    }).join('\n');

    const subList = d.subscribers.map(s =>
        `<div class="sub-chip"><span class="role">${escapeHtml(s.role)}</span></div>`
    ).join('\n');

    return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Board Chat Export: ${escapeHtml(d.project)}</title>
<style>
  :root { --bg: #1a1a1e; --surface: #212126; --border: #3a3a40; --text: #e0e0e4; --muted: #8c8c94; --accent: #58a6ff; --side-chat: #22222a; }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif; background: var(--bg); color: var(--text); line-height: 1.6; }
  .container { max-width: 900px; margin: 0 auto; padding: 2rem 1rem; }
  h1 { font-size: 1.8rem; margin-bottom: 0.5rem; }
  .stats { color: var(--muted); margin-bottom: 1.5rem; font-size: 0.9rem; }
  .stats span { margin-right: 1.5rem; }
  .subscribers { margin-bottom: 2rem; }
  .subscribers summary { cursor: pointer; color: var(--accent); font-weight: 600; margin-bottom: 0.5rem; }
  .sub-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 0.5rem; padding: 0.5rem 0; }
  .sub-chip { background: var(--surface); border: 1px solid var(--border); border-radius: 6px; padding: 0.4rem 0.8rem; font-size: 0.85rem; }
  .sub-chip .role { font-weight: 600; }
  .messages { display: flex; flex-direction: column; gap: 0.75rem; }
  .msg { background: var(--surface); border: 1px solid var(--border); border-radius: 8px; padding: 1rem; border-left: 3px solid var(--accent); }
  .msg.side-chat { background: var(--side-chat); border-left-color: #f0883e; }
  .msg-header { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.5rem; flex-wrap: wrap; }
  .msg-role { font-weight: 700; font-size: 0.95rem; }
  .msg-time { color: var(--muted); font-size: 0.8rem; }
  .msg-tag { background: #f0883e33; color: #f0883e; font-size: 0.7rem; padding: 0.15rem 0.5rem; border-radius: 10px; font-weight: 600; }
  .msg-content { word-wrap: break-word; font-size: 0.9rem; }
  .msg-content p { margin: 0.4em 0; }
  .msg-content h1, .msg-content h2, .msg-content h3, .msg-content h4 { margin: 0.8em 0 0.3em; font-size: 1.1em; }
  .msg-content h2 { font-size: 1.05em; }
  .msg-content h3 { font-size: 1em; }
  .msg-content code { background: rgba(255,255,255,0.08); padding: 0.15em 0.4em; border-radius: 4px; font-size: 0.9em; }
  .msg-content pre { background: rgba(0,0,0,0.3); padding: 0.8em; border-radius: 6px; overflow-x: auto; margin: 0.5em 0; }
  .msg-content pre code { background: none; padding: 0; }
  .msg-content ul, .msg-content ol { padding-left: 1.5em; margin: 0.3em 0; }
  .msg-content li { margin: 0.2em 0; }
  .msg-content strong { font-weight: 700; }
  .msg-content blockquote { border-left: 3px solid var(--border); padding-left: 0.8em; color: var(--muted); margin: 0.5em 0; }
  .msg-content table { border-collapse: collapse; margin: 0.5em 0; width: 100%; }
  .msg-content th, .msg-content td { border: 1px solid var(--border); padding: 0.4em 0.6em; text-align: left; font-size: 0.85em; }
  .msg-content th { background: rgba(0,0,0,0.2); font-weight: 600; }
</style>
</head>
<body>
<div class="container">
<h1>${escapeHtml(d.project)}</h1>
<div class="stats">
  <span>Exported: ${escapeHtml(d.exported_at)}</span>
  <span>Messages: ${d.stats.total}</span>
  ${d.stats.agent_chat > 0 ? `<span>Board: ${d.stats.board}</span><span>Agent chats: ${d.stats.agent_chat}</span>` : ''}
</div>
${d.subscribers.length ? `<details class="subscribers"><summary>${d.subscribers.length} Subscribers</summary><div class="sub-grid">${subList}</div></details>` : ''}
<div class="messages">
${msgs}
</div>
</div>
</body>
</html>`;
}

// ── Multi-select export ─────────────────────────────────────────────────

export function toggleSelectMode() {
    _selectMode = !_selectMode;
    _selectedIds.clear();
    const btn = document.getElementById('mb-select-btn');
    if (btn) btn.classList.toggle('active', _selectMode);
    updateSelectBar();
    renderMessages(_allMessages);
}

export function toggleMessageSelect(msgId, checked) {
    if (checked) {
        _selectedIds.add(msgId);
    } else {
        _selectedIds.delete(msgId);
    }
    // Update selected highlight without full re-render
    const msg = document.querySelector(`.mb-select-checkbox[data-msg-id="${msgId}"]`);
    if (msg) {
        msg.closest('.mb-message').classList.toggle('mb-message-selected', checked);
    }
    updateSelectBar();
}

export function selectAllMessages() {
    _allMessages.forEach(m => _selectedIds.add(m.id));
    renderMessages(_allMessages);
    updateSelectBar();
}

export function selectNoneMessages() {
    _selectedIds.clear();
    renderMessages(_allMessages);
    updateSelectBar();
}

export function cancelSelectMode() {
    _selectMode = false;
    _selectedIds.clear();
    const btn = document.getElementById('mb-select-btn');
    if (btn) btn.classList.remove('active');
    updateSelectBar();
    renderMessages(_allMessages);
}

function updateSelectBar() {
    let bar = document.getElementById('mb-select-bar');
    if (!_selectMode) {
        if (bar) bar.style.display = 'none';
        return;
    }
    if (!bar) {
        bar = document.createElement('div');
        bar.id = 'mb-select-bar';
        bar.className = 'mb-select-bar';
        document.getElementById('mb-board').appendChild(bar);
    }
    const count = _selectedIds.size;
    bar.innerHTML = `
        <span class="mb-select-count">${count} selected</span>
        <button class="btn mb-select-action-btn" data-on-click="mb-select-all">Select All</button>
        <button class="btn mb-select-action-btn" data-on-click="mb-select-none">None</button>
        <button class="btn btn-primary mb-select-action-btn" data-on-click="mb-export-selected" ${count === 0 ? 'disabled' : ''}>Export Markdown</button>
        <button class="btn mb-select-action-btn" data-on-click="mb-select-cancel">Cancel</button>
    `;
    bar.style.display = '';
}

export async function exportSelectedAsMarkdown() {
    const selected = _allMessages
        .filter(m => _selectedIds.has(m.id))
        .sort((a, b) => (a.created_at || '').localeCompare(b.created_at || ''));

    if (!selected.length) return;

    const now = new Date().toLocaleString();
    let md = `# ${keyLabel(currentProject)} — Exported Chat\n\n`;
    md += `**Exported**: ${now} · **Messages**: ${selected.length}\n\n---\n\n`;

    for (const m of selected) {
        const agent = m.job_title || 'Unknown';
        const time = formatTime(m.created_at);
        md += `### ${agent} — ${time}\n${m.content}\n\n`;
    }
    md += `---\n*Exported from Coral*\n`;

    // Copy to clipboard
    try {
        await navigator.clipboard.writeText(md);
        showToast(`Copied ${selected.length} messages to clipboard`);
    } catch {
        // Fallback: just download
    }

    // Also offer download
    const blob = new Blob([md], { type: 'text/markdown' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${keyLabel(currentProject)}-selected-export.md`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);

    cancelSelectMode();
}

// ── Init ─────────────────────────────────────────────────────────────────

export function initMessageBoard() {
    loadBoardProjects();
}

export { loadBoardProjects };

// ── The toolbar's More menu (#4538, UX #4536) ───────────────────────────────
// On a phone the toolbar keeps Subscribers and puts Select, Export, Pause and Delete in a More menu (the same buttons: on a wider
// screen their group is laid out inline and the More button is hidden, mobile.css). Arrow keys, Home and End move between its
// items, Escape closes it with focus back on More; choosing an item, a click outside or leaving the board closes it.
function moreItems() {
    return [...document.querySelectorAll('#mb-more-menu .mb-action-btn')].filter(b => b.getClientRects().length && !b.disabled);
}
function setMoreOpen(open, { focus = false } = {}) {
    const wrap = document.getElementById('mb-more');
    const btn = document.getElementById('mb-more-btn');
    if (!wrap || !btn) return;
    wrap.classList.toggle('open', open);
    btn.setAttribute('aria-expanded', String(open));
    if (open && focus) moreItems()[0]?.focus();
}
function toggleMoreMenu(event) {
    const open = !document.getElementById('mb-more')?.classList.contains('open');
    // Opened from the keyboard (Enter or Space on More: a click whose detail, the pointer's click count, is 0), focus goes to the
    // first item; a tap leaves it on More, also when focus is already there after an item's action.
    setMoreOpen(open, { focus: open && event?.detail === 0 });
}
document.addEventListener('keydown', (e) => {
    const wrap = document.getElementById('mb-more');
    if (!wrap || !wrap.classList.contains('open') || !wrap.contains(document.activeElement)) return;
    const items = moreItems();
    const i = items.indexOf(document.activeElement);
    let next = null;
    if (e.key === 'ArrowDown') next = items[(i + 1) % items.length];
    else if (e.key === 'ArrowUp') next = items[(i - 1 + items.length) % items.length];
    else if (e.key === 'Home') next = items[0];
    else if (e.key === 'End') next = items[items.length - 1];
    else if (e.key === 'Escape') {
        e.preventDefault();
        e.stopPropagation(); // not also the page's Escape
        setMoreOpen(false);
        document.getElementById('mb-more-btn')?.focus();
        return;
    }
    if (next) { e.preventDefault(); next.focus(); }
}, true);
document.addEventListener('click', (e) => {
    const wrap = document.getElementById('mb-more');
    if (!wrap || !wrap.classList.contains('open')) return;
    if (e.target.closest?.('#mb-more-menu .mb-action-btn')) {
        // The item's action runs (its own data-on-click); the menu closes, and focus goes back to More unless the action opened a dialog.
        setTimeout(() => {
            setMoreOpen(false);
            if (!document.activeElement || document.activeElement === document.body || wrap.contains(document.activeElement)) document.getElementById('mb-more-btn')?.focus();
        }, 0);
    } else if (!e.target.closest?.('#mb-more')) {
        setMoreOpen(false);
    }
});

// The board's generated markup (#3645): data-on-* names, never code. Ids and names are checked
// before use; a bad one does nothing. Deleting a message never asked first, and still doesn't.
registerActions({
    'mb-project': el => { const p = textOf(el.dataset.project); if (p) selectBoardProject(p); },
    'mb-earlier': () => loadEarlierMessages(),
    'mb-more': (el, event) => toggleMoreMenu(event),
    'mb-jump-latest': () => jumpToLatest(),
    'mb-msg-select': el => withId(el.dataset.msgId, id => toggleMessageSelect(id, el.checked)),
    'mb-msg-delete': el => withId(el.dataset.msgId, confirmDeleteBoardMessage),
    'mb-subscriber-history': el => {
        const s = textOf(el.dataset.session);
        if (s) navigateToAgentSession(s, textOf(el.dataset.name) || '');
    },
    'mb-export-agents': el => document.querySelectorAll('.export-agent-cb').forEach(c => { c.checked = el.dataset.checked === '1'; }),
    'mb-export-do': () => doExportBoard(),
    'mb-export-close': () => hideExportBoardModal(),
    'mb-select-all': () => selectAllMessages(),
    'mb-select-none': () => selectNoneMessages(),
    'mb-export-selected': () => exportSelectedAsMarkdown(),
    'mb-select-cancel': () => cancelSelectMode(),
});
