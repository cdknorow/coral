/* Message Board: project list, messages, subscribers, posting */

import { OPERATOR_ID, isOperatorId, escapeHtml, escapeAttr, showView, getAgentColor, showToast, externalMessageBadge, hubUserBadge, renderBoardMessageBody, setBoardMentionNames, highlightMentionsIn, boardAvatar, boardMessageColorStyle } from './utils.js';
import { registerActions, withId, textOf } from './actions.js';
import { scrollBehavior } from './motion.js';
import { boardPageChanged } from './dock_page.js';
import { emptyState } from './dom_write.js';
import { state } from './state.js';
import { resolveBoardArtifacts } from './board_artifacts.js';
import { messageAttachmentAttrs, messageAttachmentsHtml } from './board_attachments.js';
import { groupTaskQueueRuns, renderTaskQueueRun } from './board_system_lines.js';
import { platform } from './platform/detect.js';
import { BOARD_SAVE_BUTTON } from './saved_marks.js';
import { messageTimeHtml } from './message_time.js';
import { paintPauseButton } from './board_pause_button.js';
import { pathId, keyLabel, noteBoardLabel, hasBoardLabel, onBoardLabelsChanged, splitKey, boardFetch, teamFetch, isCloud, inTeam } from './server_base.js';
import { importOnce } from './import_once.js';
import { launchBlockedReason, blockedReason } from './hub_access.js';
import { pollEvery, afterPause, isBusyStatus, busyRetryingText, machineOf } from './overload.js';
import { note } from './nav.js';
import { keyRoute } from './routes.js';
import { attachDialogFocus } from './dialog_focus.js';
import { mountBoardSearch, highlightBoard, setBoardMembers, resetBoardSearch } from './board_search.js';
import { PAGE_SIZE, fetchAllBoardMessages, fetchAllHistory, fetchAllLiveChat, fetchMessages, fetchProjects, fetchSubscribers, mergeMessages } from './board_fetch.js';
import { navigateToAgentSession } from './board_agent_links.js';
export { fetchAllBoardMessages, fetchAllHistory, fetchAllLiveChat } from './board_fetch.js';
import { connectBoard, isSelectMode, isSelected, resetSelection, toggleMessageSelect, doExportBoard, hideExportBoardModal, selectAllMessages, selectNoneMessages, exportSelectedAsMarkdown, cancelSelectMode } from './board_export.js';
export { showExportBoardModal, doExportBoard, toggleSelectMode, toggleMessageSelect, selectAllMessages, selectNoneMessages, cancelSelectMode, exportSelectedAsMarkdown } from './board_export.js';

let currentProject = null;
let pollTimer = null;
let isPaused = false;
let isSleeping = false;
let _allMessages = [];
let _totalMessages = 0;
let _loadedOffset = 0; // older messages not loaded yet (the offset of the first loaded one)
let _loadedProject = null; // board whose initial page is loaded; polls wait for it
let _pollInFlight = false;
let _countedTotal = 0; // the board's total as the rows loaded account for it (see _pollNewMessages)
let _staleRows = false; // a message shown may have been deleted: the latest page is loaded again at the bottom

let pendingMessageId = null;

// ── API helpers ──────────────────────────────────────────────────────────








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
        emptyState(list, 'No projects', 'empty-state', 'li');
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
    const computer = (state.servers || []).find(s => s.id === state.currentBoardServer);
    document.getElementById('messageboard-title').textContent = isCloud() && computer ? `Board on ${computer.label || computer.id}` : 'Message Board';
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
    document.getElementById('messageboard-title').textContent = 'Message Board';
    setMoreOpen(false);
    resetBoardSearch();
    currentProject = null;
    state.boardPageProject = null;
    clearNewPill();
    stopBoardPoll();
    // Exit select mode if active
    resetSelection();

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
                    <div class="mb-project-meta">${p.server ? escapeHtml((state.servers || []).find(s => s.id === p.server)?.label || p.server) + ' · ' : ''}${p.subscriber_count} subscriber${p.subscriber_count !== 1 ? 's' : ''}</div>
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
        loadingEl.setAttribute('role', 'listitem');
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
        loadEarlierHtml = `<div class="mb-load-earlier" role="listitem" style="text-align:center;padding:8px 0 12px">
            <button class="btn btn-small" data-on-click="mb-earlier" class="mb-project-meta">
                Load ${Math.min(remaining, PAGE_SIZE)} earlier messages (${remaining} remaining)
            </button>
        </div>`;
    }
    // Message count indicator: only while the window is smaller than the board, where it says how much is not shown ("Showing 2 of 2" said nothing, #5991)
    const countHtml = messages.length < _totalMessages ? `<div class="mb-message-count" role="listitem">
        Showing ${messages.length} of ${_totalMessages} messages
    </div>` : '';
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
    const selectedClass = isSelectMode() && isSelected(m.id) ? ' mb-message-selected' : '';
    const deleteLabel = `Delete the message from ${agent}, ${formatTime(m.created_at)}`; // names the message: the × alone is read as "times"
    const checkbox = isSelectMode() ? `<input type="checkbox" class="mb-select-checkbox" data-msg-id="${Number(m.id)}" ${isSelected(m.id) ? 'checked' : ''} data-on-click="mb-msg-select">` : '';
    return `
        <div class="mb-message ${spacing}${alignClass}${selectedClass}" role="listitem" data-message-id="${escapeAttr(String(m.id))}"${messageAttachmentAttrs(m)} style="${boardMessageColorStyle(color)}">
            ${boardAvatar(m, agent)}
            <div class="mb-message-header">
                ${checkbox}
                <span class="mb-agent-name">${m.icon ? escapeHtml(m.icon) + ' ' : ''}${escapeHtml(agent)}</span>
                ${externalMessageBadge(m)}${hubUserBadge(m)}
                ${messageTimeHtml(m.created_at)}
                ${BOARD_SAVE_BUTTON(currentProject || '', keyLabel(currentProject || ''), String(m.created_at || ''), String(m.job_title || m.subscriber_id || ''))}
                <button class="mb-delete-msg-btn" data-on-click="mb-msg-delete" data-msg-id="${Number(m.id)}" title="Delete message" aria-label="${escapeAttr(deleteLabel)}">&times;</button>
            </div>
            <div class="mb-message-body">${renderBoardMessageBody(m)}</div>
            ${messageAttachmentsHtml(m)}
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
    if (!first || isSelectMode() || first.classList.contains('mb-queue-run') || container.querySelector(':scope > .empty-state, :scope > .loading-indicator')) return false;
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
    target.scrollIntoView({ block: 'center', behavior: scrollBehavior() });
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
        emptyState(container, 'No messages yet');
        container.firstElementChild.setAttribute('role', 'listitem'); // the messages box is a list: its one notice is an item
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
        const items = groupTaskQueueRuns(messages, !isSelectMode());
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
            else { container.tabIndex = -1; container.focus({ preventScroll: true }); } // every message is loaded and the count line is gone (#5991)
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
    announceArrivals(_allMessages.slice(-count), isAtBottom(container)); // (a board read higher up is told by the pill below instead)
    const rows = container.querySelectorAll(ROW);
    const last = rows[rows.length - 1];
    const lastIdx = last ? indexOfId(_allMessages, rowFirstId(last)) : -1;
    if (!last || lastIdx < 0 || lastIdx >= _allMessages.length - count || container.querySelector(':scope > .empty-state, :scope > .loading-indicator')) {
        renderMessages(_allMessages);
        return;
    }
    const wasAtBottom = isAtBottom(container);
    const collapse = !isSelectMode();
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

// What arrives while the board is at the bottom is said once, politely, to a screen reader (the pill below does that for a board read higher up): who, and the start
// of what they wrote; several at once are a count and the latest. Nothing for the task queue's own lines, a system post, or what the Operator posted themselves.
let _announceFlip = false;
function announceArrivals(fresh, atBottom) {
    if (!atBottom) return;
    const said = fresh.filter(m => m && !m.is_system && m.subscriber_id !== 'Operator' && String(m.content || '').trim());
    const region = document.getElementById('mb-announce');
    if (!said.length || !region) return;
    const last = said[said.length - 1];
    const who = last.job_title || last.subscriber_id || 'Unknown';
    const start = String(last.content).replace(/\s+/g, ' ').trim();
    const text = start.length > 140 ? start.slice(0, 140) + '…' : start;
    _announceFlip = !_announceFlip;
    region.textContent = (said.length === 1 ? `New message from ${who}: ${text}` : `${said.length} new messages. The latest is from ${who}: ${text}`) + (_announceFlip ? '' : ' ');
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
    if (isSelectMode() || pendingMessageId || _allMessages.length <= BOARD_KEEP + PAGE_SIZE) return;
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
    if (count && !fresh('.mb-message-count')) count.remove();
    else if (count) count.textContent = fresh('.mb-message-count').textContent;
    else if (fresh('.mb-message-count')) container.prepend(fresh('.mb-message-count'));
    const row = container.querySelector(':scope > .mb-load-earlier');
    const newRow = fresh('.mb-load-earlier');
    if (row && newRow) row.querySelector('button').textContent = newRow.querySelector('button').textContent;
    else if (row) row.remove();
    else if (newRow) container.prepend(newRow);
}

// The time of a message today, the date and time of an older one: the date said nothing on a board that is read the day it is written (#5991).
function formatTime(iso) {
    try {
        const d = new Date(iso);
        if (d.toDateString() === new Date().toDateString()) return d.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
        return d.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' });
    } catch {
        return iso;
    }
}

// ── Subscribers ──────────────────────────────────────────────────────────



async function loadBoardSubscribers(project) {
    try {
        const subs = await fetchSubscribers(project);
        setBoardMembers([...new Set(subs.map(s => s.job_title || s.subscriber_id).filter(n => typeof n === 'string' && n))].sort().slice(0, 50)); // the From field's names
        // @mentions of these members are highlighted; the bodies on screen are marked in place
        if (setBoardMentionNames(subs.flatMap(s => [s.job_title, s.subscriber_id]))) highlightMentionsIn(document.getElementById('mb-messages')); // in place: no message is redrawn, so a jump's outline and focus stay
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
        if (isCloud()) {
            const access = await (await importOnce('./cloud_servers.js')).serverAccess(splitKey(project).server);
            if (!access?.mayInteract) return;
        }
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
    const blocked = blockedReason(splitKey(currentProject).server, 'interact');
    if (blocked) { showToast(blocked, true); return; }
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
    // the page's own item is text; the team panel's button is an icon that one function paints (#5491)
    paintPauseButton(document.getElementById('board-chat-pause-btn'), isPaused);
    const btn = document.getElementById('mb-pause-btn');
    if (!btn) return;
    btn.textContent = isPaused ? 'Resume Board' : 'Pause Board';
    btn.classList.toggle('mb-action-danger', isPaused);
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

/** The agents running on this board now, by name (the live list: not asleep, not archived). */
function runningAgentsOn(project) {
    return (state.liveSessions || []).filter(a => inTeam(a, project) && !a.sleeping && !a.archived).map(a => a.display_name || a.name);
}

/** "2 agents are still on this board (A, B): kill them or move them first": the server's words (DeleteBoard, #5043), for the answer it does not have to give. */
function runningAgentsText(names) {
    const shown = names.length > 5 ? [...names.slice(0, 5), '…'] : names;
    return names.length === 1 ? `1 agent is still on this board (${shown.join(', ')}): kill it or move it first`
        : `${names.length} agents are still on this board (${shown.join(', ')}): kill them or move them first`;
}

export function deleteMessageBoardProject() {
    const blocked = currentProject ? launchBlockedReason(splitKey(currentProject).server) : '';
    if (blocked) { showToast(blocked, true); return; }
    if (!currentProject) return;
    const project = currentProject;
    // A board with agents running is not deleted (#5043): they would stay live in a group named with the raw board id. Said here, before asking; the server
    // says it again for an agent this page's list does not show yet.
    const running = runningAgentsOn(project);
    if (running.length) { showToast(runningAgentsText(running), true); return; }
    window.showConfirmModal('Delete Board', `Delete board "${keyLabel(project)}" with all its messages and tasks? This cannot be undone.`, async () => {
        try {
            const resp = await boardFetch(project, ``, { method: 'DELETE' });
            if (!resp.ok) {
                const out = await resp.json().catch(() => ({}));
                showToast(out.error || 'The board was not deleted.', true);
                return;
            }
            showMessageBoardProjects();
        } catch (e) {
            console.error('Failed to delete project:', e);
            showToast('Could not reach Coral to delete the board.', true);
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
        if (_staleRows && !isSelectMode() && !pendingMessageId && (!container || isAtBottom(container))) {
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

// ── Init ─────────────────────────────────────────────────────────────────

// What the export and select code needs of the open board (board_export.js).
connectBoard({ project: () => currentProject, messages: () => _allMessages, render: () => renderMessages(_allMessages), formatTime });

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
