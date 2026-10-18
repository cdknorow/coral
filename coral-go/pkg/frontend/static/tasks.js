/* Agent task bar — CRUD, rendering, drag reorder */

import { state } from './state.js';
import { boardOfPage } from './dock_page.js';
import { morphHtml } from './dom_morph.js';
import { closeOnBackdropAndEscape, releaseEscape } from './dialog_close.js';
import { registerTaskView, redrawBoardTaskList, closeTaskDetailModal } from './task_hooks.js';
import { escapeHtml, escapeAttr, showToast, renderMarkdown, DOMPURIFY_CONFIG } from './utils.js';
import { registerActions, idOf, tokenOf, textOf } from './actions.js';
import { addPendingMessage } from './live_chat.js';
import { serverUrl, artifactPath, contextServer, keyLabel, boardKeyName, splitKey, serverFetch, currentServer, serverForSession, sessionServer, boardFetch, identityKey, isCloud, fetchValidated } from './server_base.js';
import { platform } from './platform/detect.js';
import { pollEvery, afterPause, isBusyStatus, busyRetryingText, machineOf } from './overload.js';
import { _agentTaskStatus, _formatCost, _formatTaskTime, _formatTokenCount, _priorityLabel, _priorityOrder, formatTaskDate } from './task_format.js';
import { _claimedFor, _subagentTooltip, clearOpenSubagent, loadSubagents, showSubagentDetailModal, subagentToTaskRow } from './subagents.js';
import { browserLocal } from './safe_storage.js';
import { ensureBuilt } from './lazy_dom.js';
export { clearOpenSubagent, loadSubagents } from './subagents.js';

// ── Board task polling ───────────────────────────────────────────────
let _boardTaskPollTimer = null;
// Seam for tests: the poll period, read when the poll starts (tests shorten it instead of waiting 10 s per tick).
export const boardTaskPollHooks = { intervalMs: 10000 };
// The tick in flight, if any. A large board's task list can take longer than the period to
// fetch and draw; a tick never starts while the previous one runs, so slow fetches cannot
// pile up behind each other (#3262).
let _boardTaskPollInFlight = null;
// Live cost cache: taskID → { cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, request_count }
let _liveCosts = {};

export function startBoardTaskPoll() {
    stopBoardTaskPoll();
    // Load agent tasks + board tasks immediately, then poll board tasks every 10s
    if (!state.selectedTeam && !boardOfPage() && state.currentSession && state.currentSession.type === 'live') {
        loadAgentTasks(state.currentSession.name, state.currentSession.session_id);
    }
    // Subagents are loaded by the poll below, every tick: their status and
    // spend keep changing while they run, with no user action to trigger a reload.
    _boardTaskPollTick(true);
    _boardTaskPollTimer = pollEvery(_boardTaskPollTick, boardTaskPollHooks.intervalMs, { whenHidden: true }); // its own guard decides (#3200)
}

/** One poll tick: skipped while the previous tick runs, and while the tab is hidden (a hidden
 *  tab catches up when shown). Native webviews can report hidden permanently, so they always poll. */
function _boardTaskPollTick(force = false) {
    // force: a (re)start for a new selection loads at once, even past the old selection's tick.
    if (!force && (_boardTaskPollInFlight || (!platform.isNative && document.hidden))) return;
    const run = _pollBoardTasksOnce().catch(() => {}).finally(() => {
        if (_boardTaskPollInFlight === run) _boardTaskPollInFlight = null;
    });
    _boardTaskPollInFlight = run;
}

document.addEventListener('visibilitychange', () => {
    if (!document.hidden && _boardTaskPollTimer) _boardTaskPollTick();
});

export function stopBoardTaskPoll() {
    if (_boardTaskPollTimer) {
        clearInterval(_boardTaskPollTimer);
        _boardTaskPollTimer = null;
    }
}

async function _pollBoardTasksOnce() {
    // The board page's sidebar shows that board's tasks (#4539).
    const pageBoard = boardOfPage();
    if (pageBoard) {
        await loadBoardTasks(pageBoard);
        await _fetchLiveCosts(pageBoard);
        return;
    }
    // A selected team shows that team's board tasks, even with no agent selected.
    if (state.selectedTeam) {
        await loadBoardTasks(state.selectedTeam);
        await _fetchLiveCosts(state.selectedTeam);
        return;
    }
    if (!state.currentSession || state.currentSession.type !== 'live') return;
    loadSubagents(state.currentSession.name, state.currentSession.session_id);
    const boardProject = _currentBoardKey();
    if (boardProject) {
        await loadBoardTasks(boardProject);
        await _fetchLiveCosts(boardProject);
    }
}

async function _fetchLiveCosts(boardProject) {
    const tasks = state.currentBoardTasks || [];
    const inProgress = tasks.filter(t => t.status === 'in_progress' && t.session_id);
    if (inProgress.length === 0) {
        _liveCosts = {};
        return;
    }
    const newCosts = {};
    await Promise.all(inProgress.map(async (t) => {
        try {
            const resp = await boardFetch(boardProject, `/tasks/${t.id}/cost`);
            if (resp.ok) {
                const data = await resp.json();
                if (data) newCosts[t.id] = data;
            }
        } catch { /* ignore */ }
    }));
    _liveCosts = newCosts;
    renderBoardTaskList();
}

export async function loadAgentTasks(agentName, sessionId, options) {
    if (!agentName) return;
    const sid = sessionId || (state.currentSession && state.currentSession.session_id);
    try {
        const params = new URLSearchParams();
        if (sid) params.set("session_id", sid);
        const qs = params.toString() ? `?${params}` : "";
        const resp = await serverFetch(serverForSession(agentName, sid), `/api/sessions/live/${encodeURIComponent(agentName)}/tasks${qs}`, options);
        if (!resp.ok) throw new Error(`tasks fetch failed: ${resp.status}`);
        const tasks = await resp.json();
        if (sid && state.currentSession?.session_id !== sid) return;
        state.currentAgentTasks = tasks;
    } catch (e) {
        if (e?.name === 'AbortError') return;
        if (sid && state.currentSession?.session_id !== sid) return;
        state.currentAgentTasks = [];
    }
    renderTaskList();
}

export async function addAgentTask() {
    if (!state.currentSession || state.currentSession.type !== 'live') return;
    const input = document.getElementById('task-bar-input');
    const title = input.value.trim();
    if (!title) return;

    try {
        const sid = state.currentSession.session_id;
        await serverFetch(currentServer(), `/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/tasks`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ title, session_id: sid }),
        });
        input.value = '';
        await loadAgentTasks(state.currentSession.name, sid);
    } catch (e) {
        showToast('Failed to add task', true);
    }
}

export async function toggleAgentTask(taskId, completed) {
    if (!state.currentSession || state.currentSession.type !== 'live') return;
    try {
        const response = await serverFetch(currentServer(), `/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/tasks/${taskId}`, {
            method: 'PATCH',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ completed: completed ? 1 : 0 }),
        });
        if (!response.ok) { const data = await response.json(); throw new Error(data.error || 'Failed to update task'); }
        await loadAgentTasks(state.currentSession.name, state.currentSession.session_id);
    } catch (e) {
        showToast(e.message || 'Failed to update task', true);
    }
}

export async function deleteAgentTask(taskId) {
    if (!state.currentSession || state.currentSession.type !== 'live') return;
    try {
        const response = await serverFetch(currentServer(), `/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/tasks/${taskId}`, {
            method: 'DELETE',
        });
        if (!response.ok) {const data = await response.json(); throw new Error(data.error || 'Failed to delete task');}
        await loadAgentTasks(state.currentSession.name, state.currentSession.session_id);
    } catch (e) {
        showToast(e.message || 'Failed to delete task', true);
    }
}

export function editAgentTaskTitle(taskId, spanEl) {
    if (!state.currentSession || state.currentSession.type !== 'live') return;
    const original = spanEl.textContent;
    spanEl.contentEditable = 'true';
    spanEl.focus();

    // Select all text
    const range = document.createRange();
    range.selectNodeContents(spanEl);
    const sel = window.getSelection();
    sel.removeAllRanges();
    sel.addRange(range);

    const finish = async () => {
        spanEl.contentEditable = 'false';
        const newTitle = spanEl.textContent.trim();
        if (!newTitle || newTitle === original) {
            spanEl.textContent = original;
            return;
        }
        try {
            await serverFetch(currentServer(), `/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/tasks/${taskId}`, {
                method: 'PATCH',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ title: newTitle }),
            });
            await loadAgentTasks(state.currentSession.name, state.currentSession.session_id);
        } catch (e) {
            spanEl.textContent = original;
            showToast('Failed to update task title', true);
        }
    };

    spanEl.addEventListener('blur', finish, { once: true });
    spanEl.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') {
            e.preventDefault();
            spanEl.blur();
        } else if (e.key === 'Escape') {
            spanEl.textContent = original;
            spanEl.blur();
        }
    });
}

export function renderTaskList() {
    // Agent tasks are now rendered in the unified board task table.
    // Trigger a re-render of the unified table to include updated agent tasks.
    renderBoardTaskList();
}

/* ── Board Tasks ────────────────────────────────────────── */

// The latest loadBoardTasks call wins: a slow answer for a board the user has left (or an
// older answer for the same board) never replaces a newer one (#3262).
let _boardTasksLoadSeq = 0;

// The task list is paged (#3269): a load gets every open task and the newest done tasks,
// as light items (no body or workflow text; the detail view fetches the full task).
// "Load more" pages older done tasks with the cursor; a poll refreshes the first page
// and keeps the older pages already loaded.
 const BOARD_TASK_PAGE = 50;
// A fetch that never answers (a hung server, a dead hub link) must not stop the poll for
// good, since the poll never starts a tick while one runs (#3262).
export const boardTaskFetchHooks = { timeoutMs: 20000 };
const _emptyBoardList = board => ({ board, first: [], older: [], nextCursor: '', totalOpen: null, totalCompleted: null });
let _boardList = _emptyBoardList(null);

async function _boardTasksFetch(boardName, suffix) {
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), boardTaskFetchHooks.timeoutMs);
    try {
        // Polled every 10 s per open board: an unchanged page comes back 304 (#3439).
        const r = await fetchValidated('board-tasks ' + boardName + suffix, headers => boardFetch(boardName, suffix, { signal: ctrl.signal, headers }));
        if (!r.ok) { const err = new Error(`board tasks fetch failed: ${r.resp.status}`); err.resp = r.resp; throw err; }
        return JSON.parse(r.text);
    } finally {
        clearTimeout(timer);
    }
}

/** The list: the first page, then the older done tasks loaded with "Load more". */
function _setBoardTasks() {
    const first = new Set(_boardList.first.map(t => t.id));
    _boardList.older = _boardList.older.filter(t => !first.has(t.id) && (t.status === 'completed' || t.status === 'skipped'));
    state.currentBoardTasks = [..._boardList.first, ..._boardList.older];
}

export async function loadBoardTasks(boardName) {
    const seq = ++_boardTasksLoadSeq;
    if (!boardName) {
        _boardList = _emptyBoardList(null);
        state.currentBoardTasks = [];
        renderBoardTaskList();
        return;
    }
    let data = null, refused = null;
    try {
        data = await _boardTasksFetch(boardName, `/tasks?limit=${BOARD_TASK_PAGE}`);
    } catch (e) {
        data = null;
        if (e && e.resp && isBusyStatus(e.resp.status)) refused = e.resp;
    }
    if (seq !== _boardTasksLoadSeq) return;
    const sameBoard = _boardList.board === boardName;
    if (!data) {
        // A failed refresh keeps the board's last good list; another board's is dropped.
        if (sameBoard && !_boardList.busy) return;
        _boardList = _emptyBoardList(boardName);
        state.currentBoardTasks = [];
        // A first load refused as busy (#3430): say so instead of "No tasks yet", and load again
        // once the pause is over rather than at the next 10 s poll; a newer load supersedes it.
        if (refused) {
            _boardList.busy = busyRetryingText(refused, { hub: isCloud() });
            let machine = '';
            try { machine = machineOf(new URL(refused.url).pathname); } catch { /* same server */ }
            afterPause(() => {
                // Only for the board still shown, and only if no newer load has started.
                if (seq === _boardTasksLoadSeq && (boardOfPage() || state.selectedTeam || _currentBoardKey()) === boardName) loadBoardTasks(boardName);
            }, machine);
        }
        renderBoardTaskList();
        return;
    }
    // Once "Load more" was used, the done tasks the old first page had are kept too: a task
    // finished since pushes the oldest of them off page one, and the cursor continues after
    // the older pages, so nothing else would bring it back.
    const older = sameBoard && _boardList.older.length
        ? [..._boardList.first.filter(t => t.status === 'completed' || t.status === 'skipped'), ..._boardList.older]
        : [];
    _boardList = {
        board: boardName,
        first: data.tasks || [],
        older,
        // Once older pages are loaded, "Load more" continues after the last of them.
        nextCursor: older.length ? _boardList.nextCursor : (data.next_cursor || ''),
        // A server from before pagination sends every task and no totals.
        totalOpen: data.total_open ?? null,
        totalCompleted: data.total_completed ?? null,
    };
    _setBoardTasks();
    renderBoardTaskList();
}

/** "Load more": the next page of done tasks. */
export async function loadMoreBoardTasks() {
    const board = _boardList.board;
    const cursor = _boardList.nextCursor;
    if (!board || !cursor) return;
    const seq = _boardTasksLoadSeq;
    let data;
    try {
        data = await _boardTasksFetch(board, `/tasks?status=completed&limit=${BOARD_TASK_PAGE}&cursor=${encodeURIComponent(cursor)}`);
    } catch (e) {
        showToast('Could not load more tasks. Try again.', true);
        return;
    }
    // The user moved to another board meanwhile, or the list was reloaded from scratch.
    if (_boardList.board !== board || _boardList.nextCursor !== cursor || seq !== _boardTasksLoadSeq) return;
    _boardList.older = [..._boardList.older, ...(data.tasks || [])];
    _boardList.nextCursor = data.next_cursor || '';
    _setBoardTasks();
    renderBoardTaskList();
}

// Full tasks fetched for the detail view, by id. A list item is light (#3269).
const _fullBoardTasks = new Map();

/** The full task when fetched and still current, else the list item. */
function _boardTaskById(taskId) {
    const item = (state.currentBoardTasks || []).find(t => t.id === taskId);
    const full = _fullBoardTasks.get(taskId);
    if (item && full && full.revision === item.revision && full.status === item.status) return full;
    return item || null;
}

/** A list item says review_requested; a full task has workflow.completion_review. */
function _reviewRequested(t) {
    return !!(t && (t.review_requested || t.workflow?.completion_review));
}

// Current sort and filter state for task list
let _taskSortField = 'created_at';
let _taskSortAsc = false; // default newest first
let _hideCompleted = browserLocal.get('coral-hide-completed') === 'true'; // off by default, persisted

function _toggleTaskSort(field) {
    if (_taskSortField === field) {
        _taskSortAsc = !_taskSortAsc;
    } else {
        _taskSortField = field;
        _taskSortAsc = field === 'priority'; // priority defaults asc (critical first)
    }
    renderBoardTaskList();
}

function _toggleHideCompleted() {
    _hideCompleted = !_hideCompleted;
    browserLocal.set('coral-hide-completed', _hideCompleted);
    renderBoardTaskList();
}

// Expose globally
(window.__coral ||= {})._toggleTaskSort = _toggleTaskSort;

export function renderBoardTaskList() {
    const container = document.getElementById('board-task-list');
    if (!container) return;

    // Merge board tasks and agent tasks into a unified list
    const boardTasks = (state.currentBoardTasks || []).map(t => ({ ...t, _source: 'board' }));
    const agentDisplayName = state.currentSession ? (state.currentSession.display_name || state.currentSession.name) : '';
    const onBoardPage = !!boardOfPage(); // a board's page has no agent: its tasks are the board's
    const agentTasks = (onBoardPage ? [] : state.currentAgentTasks || []).map(t => ({
        ...t,
        _source: 'agent',
        // Normalize agent task fields to match board task shape
        status: _agentTaskStatus(t),
        priority: t.priority || null,
        assigned_to: t.display_name || t.agent_name || agentDisplayName || null,
        created_at: t.created_at,
    }));

    const subagentTasks = (onBoardPage ? [] : state.currentSubagents || []).map(sa => subagentToTaskRow(sa, agentDisplayName));

    const allTasks = [...boardTasks, ...agentTasks, ...subagentTasks];
    const isDone = t => t.status === 'completed' || t.status === 'skipped';
    const otherTasks = [...agentTasks, ...subagentTasks];
    // The board's done tasks are counted by the server: only a page of them is loaded (#3269).
    const boardDoneLoaded = boardTasks.filter(isDone).length;
    const boardDoneTotal = _boardList.totalCompleted ?? boardDoneLoaded;
    const completedCount = boardDoneTotal + otherTasks.filter(isDone).length;
    const tasks = allTasks.filter(t => {
        if (_hideCompleted && (t.status === 'completed' || t.status === 'skipped')) return false;
        return true;
    }).sort((a, b) => {
        let cmp = 0;
        if (_taskSortField === 'created_at') {
            cmp = (a.created_at || '').localeCompare(b.created_at || '');
        } else if (_taskSortField === 'priority') {
            cmp = (_priorityOrder[a.priority] ?? 2) - (_priorityOrder[b.priority] ?? 2);
        } else if (_taskSortField === 'assignee') {
            cmp = (a.assigned_to || '').localeCompare(b.assigned_to || '');
        } else if (_taskSortField === 'cost') {
            const aCost = a.cost_usd ?? -1;
            const bCost = b.cost_usd ?? -1;
            cmp = aCost - bCost;
        } else if (_taskSortField === 'type') {
            cmp = (a._source || '').localeCompare(b._source || '');
        } else if (_taskSortField === 'id') {
            cmp = (a.id || 0) - (b.id || 0);
        }
        return _taskSortAsc ? cmp : -cmp;
    });
    const section = document.getElementById('board-tasks-section');

    if (allTasks.length === 0) {
        // Drop the previous agent's rows too; hiding alone leaves them in the DOM.
        const countEl = document.getElementById('task-bar-count');
        if (countEl && countEl.textContent !== '') countEl.textContent = '';
        const live = state.selectedTeam || onBoardPage || (state.currentSession && state.currentSession.type === 'live');
        if (!live) {
            if (section) section.style.display = 'none';
            container.innerHTML = '';
            return;
        }
        // A live agent always gets the section, so "+ Task" is reachable
        if (section) section.style.display = '';
        const headerLabel = section ? section.querySelector('.board-tasks-header > span:first-child') : null;
        if (headerLabel && headerLabel.textContent !== 'Tasks') headerLabel.textContent = 'Tasks';
        const toggle = document.getElementById('board-task-hide-toggle-container');
        if (toggle && toggle.firstChild) toggle.innerHTML = '';
        morphHtml(container, _boardList.busy
            ? `<div class="board-task-empty board-task-busy" role="status">${escapeHtml(_boardList.busy)}</div>`
            : state.selectedTeam || onBoardPage
                ? '<div class="board-task-empty">No tasks yet. Use + Task to add one for this team.</div>'
                : '<div class="board-task-empty">No tasks yet. Use + Task to give this agent something to work on.</div>');
        return;
    }
    if (section) section.style.display = '';

    const arrow = (field) => _taskSortField === field ? (_taskSortAsc ? ' ▲' : ' ▼') : '';

    // Render hide-done toggle in the section header
    const toggleContainer = document.getElementById('board-task-hide-toggle-container');
    if (toggleContainer) {
        morphHtml(toggleContainer, completedCount > 0
            ? `<label class="board-task-hide-toggle"><input type="checkbox" ${_hideCompleted ? 'checked' : ''} data-on-change="task-hide-done"> Hide done (${completedCount})</label>`
            : '');
    }

    // Update section header to "Tasks" instead of "Board Tasks"
    const headerLabel = section ? section.querySelector('.board-tasks-header > span:first-child') : null;
    if (headerLabel && headerLabel.textContent !== 'Tasks') headerLabel.textContent = 'Tasks';

    const header = `
        <div class="board-task-item board-task-header">
            <span class="board-task-status-col">Status</span>
            <span class="board-task-id board-task-sort" role="button" tabindex="0" data-key-click data-on-click="task-sort" data-sort="id">ID${arrow('id')}</span>
            <span class="board-task-priority board-task-sort" role="button" tabindex="0" data-key-click data-on-click="task-sort" data-sort="priority">Priority${arrow('priority')}</span>
            <span class="board-task-type board-task-sort" role="button" tabindex="0" data-key-click data-on-click="task-sort" data-sort="type">Type${arrow('type')}</span>
            <span class="board-task-assignee board-task-sort" role="button" tabindex="0" data-key-click data-on-click="task-sort" data-sort="assignee">Agent${arrow('assignee')}</span>
            <span class="board-task-desc board-task-sort" role="button" tabindex="0" data-key-click data-on-click="task-sort" data-sort="created_at">Task${arrow('created_at')}</span>
            <span class="board-task-cost board-task-sort" role="button" tabindex="0" data-key-click data-on-click="task-sort" data-sort="cost">Cost${arrow('cost')}</span>
            <span class="board-task-time board-task-sort" role="button" tabindex="0" data-key-click data-on-click="task-sort" data-sort="created_at">Created${arrow('created_at')}</span>
        </div>`;

    const rows = tasks.map(t => {
        const isAgent = t._source === 'agent';
        const isSubagent = t._source === 'subagent';
        const statusClass = t.status === 'completed' ? 'completed'
            : t.status === 'in_progress' ? 'in-progress'
            : t.status === 'skipped' ? 'cancelled'
            : t.status === 'blocked' ? 'blocked'
            : t.status === 'draft' ? 'draft' : '';
        const priorityClass = t.priority ? 'board-task-priority-' + t.priority : 'board-task-priority-none';
        const assignee = t.assigned_to || '\u2014';
        const taskIdLabel = t.id != null ? `#${t.id}` : '\u2014';
        const title = escapeHtml(t.title || t.description || '');
        const tooltip = isSubagent ? ` title="${escapeAttr(_subagentTooltip(t))}"`
            : (t.body || t.body_preview) ? ` title="${escapeAttr(t.body || t.body_preview)}"` : '';
        const timeStr = _formatTaskTime(t.created_at);
        const statusWrap = (className, detail, icon, label) =>
            `<span class="board-task-status-wrap ${className}" title="${escapeAttr(detail)}" aria-label="${escapeAttr(detail)}" role="img" tabindex="0">${icon}<span class="board-task-status-label">${label}</span></span>`;
        const activelyClaimed = t.status === 'in_progress' && Boolean(t.claimed_at);
        const workingDetail = activelyClaimed ? 'Claimed · Working' : 'Open · Working';
        const statusIcon = (t.outcome || t.workflow?.outcome) === 'failed'
            ? statusWrap('completed', 'Finished', '<span class="material-icons board-task-status-icon completed" aria-hidden="true">check_circle</span>', 'Finished')
            : t.status === 'completed'
            ? statusWrap('completed', 'Finished', '<span class="material-icons board-task-status-icon completed" aria-hidden="true">check_circle</span>', 'Finished')
            : t.status === 'review_pending'
            ? statusWrap('blocked', 'Open · Review pending', '<span class="material-icons board-task-status-icon blocked" aria-hidden="true">rate_review</span>', '<b>Review</b>')
            : _reviewRequested(t) && t.status === 'in_progress'
            ? statusWrap('blocked', 'Open · Review requested', '<span class="material-icons board-task-status-icon blocked" aria-hidden="true">rate_review</span>', 'Open <b>· Review requested</b>')
            : t.status === 'in_progress'
            ? statusWrap('in-progress', workingDetail, '<span class="task-spinner" aria-hidden="true"></span>', activelyClaimed ? 'Claimed' : 'Open')
            : t.status === 'skipped'
            ? statusWrap('cancelled', 'Cancelled', '<span class="material-icons board-task-status-icon cancelled" aria-hidden="true">cancel</span>', 'Cancelled')
            : t.status === 'blocked'
            ? statusWrap('blocked', 'Blocked: waiting for prerequisites', '<span class="material-icons board-task-status-icon blocked" aria-hidden="true">hourglass_empty</span>', 'Blocked')
            : t.status === 'draft'
            ? statusWrap('pending', 'Open · Draft', '<span class="material-icons board-task-status-icon draft" aria-hidden="true">edit_note</span>', 'Open <b>· Draft</b>')
            : statusWrap('pending', 'Open', '<span class="material-icons board-task-status-icon pending" aria-hidden="true">radio_button_unchecked</span>', 'Open');
        let costText = '';
        let costClass = 'board-task-cost';
        if (isSubagent) {
            // Spend is known while the subagent is still running, so show it
            // live rather than waiting for completion like a board task.
            if (t.cost_usd > 0) {
                costText = (t.status === 'in_progress' ? '~' : '') + _formatCost(t.cost_usd, false);
                if (t.status === 'in_progress') costClass += ' board-task-cost-live';
                if (t.cost_usd >= 1.0) costClass += ' board-task-cost-warning';
            }
        } else if (isAgent && t.cost_usd > 0) {
            costText = _formatCost(t.cost_usd, false);
            if (t.cost_usd >= 1.0) costClass += ' board-task-cost-warning';
        } else if ((t.status === 'completed' || t.status === 'skipped') && t.cost_usd != null) {
            costText = _formatCost(t.cost_usd, false);
            if (t.cost_usd >= 1.0) costClass += ' board-task-cost-warning';
        } else if (t.status === 'in_progress' && _liveCosts[t.id]) {
            const lc = _liveCosts[t.id];
            costText = '~' + _formatCost(lc.cost_usd, false);
            costClass += ' board-task-cost-live';
            if (lc.cost_usd >= 1.0) costClass += ' board-task-cost-warning';
        }
        // The subagent id stays in its data-subagent-id attribute; the action reads it from there.
        const clickHandler = isSubagent ? ` role="button" tabindex="0" data-key-click data-on-click="task-subagent-open" style="cursor:pointer"`
            : isAgent ? ` role="button" tabindex="0" data-key-click data-on-click="task-open" data-kind="agent" data-task-id="${Number(t.id)}" style="cursor:pointer"`
            : ` role="button" tabindex="0" data-key-click data-on-click="task-open" data-kind="board" data-task-id="${Number(t.id)}" style="cursor:pointer"`;
        const typeCell = isSubagent
            ? `<span class="board-task-type board-task-type-subagent" title="Subagent launched by ${escapeAttr(assignee)}"><span class="material-icons" aria-hidden="true">account_tree</span>sub</span>`
            : `<span class="board-task-type">${isAgent ? 'agent' : 'board'}</span>`;
        const subagentBadge = isSubagent && t.subagent_type
            ? `<span class="board-task-subagent-badge">${escapeHtml(t.subagent_type)}</span>` : '';
        const claimedFor = !isAgent && !isSubagent ? _claimedFor(t) : null;
        const claimedBadge = claimedFor
            ? `<span class="board-task-claimed" title="Claimed ${escapeAttr(formatTaskDate(t.claimed_at))}"><span class="material-icons" aria-hidden="true">schedule</span>${claimedFor}</span>` : '';
        // Blocked is conveyed by the status icon and its focusable detail;
        // avoid repeating a red text badge in the task description column.
        const blockedBadge = '';
        return `
        <div class="board-task-item ${statusClass}${isSubagent ? ' board-task-subagent' : ''}"${clickHandler}${isSubagent ? ` data-subagent-id="${escapeAttr(t.subagent_id || '')}"` : ''}>
            ${statusIcon}
            <button type="button" class="board-task-id board-task-open" title="Task ID" aria-label="Open task ${escapeAttr(taskIdLabel)}: ${escapeAttr(t.title || t.description || '')}">${taskIdLabel}</button>
            <span class="board-task-priority ${priorityClass}" title="Claim priority: ${escapeAttr(t.priority || 'none')}">${t.priority ? (_priorityLabel[t.priority] || escapeHtml(t.priority)) : '\u2014'}</span>
            ${typeCell}
            <span class="board-task-assignee">${escapeHtml(assignee)}</span>
            <span class="board-task-desc"${tooltip}>${subagentBadge}${claimedBadge}${blockedBadge}${title}</span>
            <span class="${costClass}">${costText}</span>
            <span class="board-task-time">${timeStr}</span>
        </div>`;
    }).join('');

    // Older done tasks come a page at a time (#3269).
    const notLoaded = Math.max(0, boardDoneTotal - boardDoneLoaded);
    const more = _boardList.nextCursor && !_hideCompleted
        ? `<div class="board-task-more"><button type="button" class="btn board-task-load-more" data-on-click="task-load-more">Load more done tasks${notLoaded ? ` (${notLoaded} not shown)` : ''}</button></div>`
        : '';
    // Unchanged rows keep their nodes: the list was rebuilt on every poll, a layout each time (#3881).
    morphHtml(container, header + rows + more);

    // Update task count badge: done / all; the board's numbers are the server's totals.
    const countEl = document.getElementById('task-bar-count');
    if (countEl) {
        const boardTotal = _boardList.totalOpen !== null && _boardList.totalCompleted !== null
            ? _boardList.totalOpen + _boardList.totalCompleted : boardTasks.length;
        const total = boardTotal + otherTasks.length;
        const countText = total > 0 ? `${completedCount}/${total}` : '';
        if (countEl.textContent !== countText) countEl.textContent = countText; // same text: no new text node, no layout
    }
}

/* ── Dependency Picker ─────────────────────────────────── */

function _renderDepPicker(containerId, selectedIds = [], excludeTaskId = null) {
    const container = document.getElementById(containerId);
    if (!container) return;
    const tasks = (state.currentBoardTasks || []).filter(t =>
        t.id !== excludeTaskId
    );
    const selected = new Set(selectedIds.map(Number));

    let html = `<div class="dep-picker-selected" id="${escapeAttr(containerId)}-tags"></div>`;
    html += `<div class="dep-picker-toggle"><a class="dep-picker-add-link" data-on-click="dep-picker-toggle" data-container="${escapeAttr(containerId)}">+ Add dependency</a></div>`;
    html += `<div class="dep-picker-list" id="${escapeAttr(containerId)}-list" style="display:none">`;
    if (tasks.length === 0) {
        html += `<div class="dep-picker-empty">No eligible tasks</div>`;
    } else {
        tasks.forEach(t => {
            const checked = selected.has(t.id) ? ' checked' : '';
            html += `<label class="dep-picker-item"><input type="checkbox" value="${Number(t.id)}"${checked} data-on-change="dep-picker-change" data-container="${escapeAttr(containerId)}"> #${t.id} — ${escapeHtml(t.title || '')}</label>`;
        });
    }
    html += `</div>`;
    container.innerHTML = html;
    _updateDepTags(containerId);
}

function _updateDepTags(containerId) {
    const container = document.getElementById(containerId);
    if (!container) return;
    const tagsEl = document.getElementById(`${containerId}-tags`);
    if (!tagsEl) return;
    const checked = container.querySelectorAll('input[type="checkbox"]:checked');
    if (checked.length === 0) {
        tagsEl.innerHTML = '';
        return;
    }
    const tags = Array.from(checked).map(cb => {
        const label = cb.parentElement.textContent.trim();
        return `<span class="dep-tag">${escapeHtml(label)} <a data-on-click="dep-tag-remove" data-container="${escapeAttr(containerId)}" data-task-id="${Number(cb.value)}">&times;</a></span>`;
    }).join('');
    tagsEl.innerHTML = tags;
};

function _getSelectedDeps(containerId) {
    const container = document.getElementById(containerId);
    if (!container) return [];
    return Array.from(container.querySelectorAll('input[type="checkbox"]:checked')).map(cb => parseInt(cb.value));
}

/* ── Create Task Modal ─────────────────────────────────── */

export async function showCreateTaskModal() {
    const modal = ensureBuilt('create-task-modal'); // built on first open (#4500)
    if (!modal) return;

    // Reset form
    document.getElementById('create-task-title').value = '';
    document.getElementById('create-task-body').value = '';
    for (const field of ['workflow', 'stage', 'outputs', 'inputs', 'instructions']) {
        const el = document.getElementById(`create-task-${field}`);
        if (el) el.value = '';
    }
    const condition = document.getElementById('create-task-condition');
    if (condition) condition.value = 'success';
    document.getElementById('create-task-priority').value = 'medium';
    const draftCheck = document.getElementById('create-task-draft');
    if (draftCheck) draftCheck.checked = false;
    const errEl = document.getElementById('create-task-error');
    if (errEl) errEl.style.display = 'none';

    // Solo agent: no board fields; offer to send the task to the agent
    const solo = _isSoloAgent();
    const boardFields = document.getElementById('create-task-board-fields');
    if (boardFields) boardFields.hidden = solo;
    const sendRow = document.getElementById('create-task-send-row');
    if (sendRow) sendRow.hidden = !solo;
    const sendCheck = document.getElementById('create-task-send');
    if (sendCheck) sendCheck.checked = true;
    const heading = document.getElementById('create-task-heading');
    if (heading) heading.textContent = solo
        ? `New task for ${state.currentSession.display_name || state.currentSession.name}`
        : 'Create Task';

    // Populate assignee dropdown from board subscribers
    const assigneeSelect = document.getElementById('create-task-assignee');
    assigneeSelect.innerHTML = '<option value="">Unassigned</option>';
    const boardProject = solo ? null : _getBoardProject();
    if (boardProject) {
        try {
            const resp = await boardFetch(boardProject, `/subscribers`);
            if (resp.ok) {
                const subs = await resp.json();
                (subs || []).forEach(s => {
                    const name = s.subscriber_id || s.name;
                    if (name) {
                        const opt = document.createElement('option');
                        opt.value = name;
                        opt.textContent = name;
                        assigneeSelect.appendChild(opt);
                    }
                });
            }
        } catch { /* ignore */ }
    }

    // Populate dependency picker
    if (!solo) _renderDepPicker('create-task-deps');

    modal.style.display = '';
    document.getElementById('create-task-title').focus();

    closeOnBackdropAndEscape(modal, hideCreateTaskModal);
}

export function hideCreateTaskModal() {
    const modal = document.getElementById('create-task-modal');
    if (!modal) return;
    modal.style.display = 'none';
    releaseEscape(modal);
}

export async function submitCreateTask() {
    const title = document.getElementById('create-task-title').value.trim();
    const errEl = document.getElementById('create-task-error');

    if (!title) {
        if (errEl) {
            errEl.textContent = 'Title is required';
            errEl.style.display = '';
        }
        return;
    }

    const body = document.getElementById('create-task-body').value.trim();
    if (_isSoloAgent()) {
        await _createSoloAgentTask(title, body, errEl);
        return;
    }
    const priority = document.getElementById('create-task-priority').value;
    const assignedTo = document.getElementById('create-task-assignee').value;
    const boardProject = _getBoardProject();
    if (!boardProject) {
        if (errEl) {
            errEl.textContent = 'No board project found';
            errEl.style.display = '';
        }
        return;
    }

    const blockedBy = _getSelectedDeps('create-task-deps');
    const isDraft = document.getElementById('create-task-draft')?.checked || false;

    try {
        const payload = {
            title,
            body,
            priority,
            assigned_to: assignedTo,
            created_by: 'Operator',
        };
        const names = id => (document.getElementById(id)?.value || '').split(',').map(s => s.trim()).filter(Boolean);
        if (blockedBy.length > 0) payload.blocked_by = blockedBy.map(task_id => ({
            task_id, condition: document.getElementById('create-task-condition')?.value || 'success',
            required_artifacts: names('create-task-inputs'),
        }));
        payload.workflow = {
            name: document.getElementById('create-task-workflow')?.value.trim() || '',
            stage: document.getElementById('create-task-stage')?.value.trim() || '',
            required_outputs: names('create-task-outputs'),
            instructions: document.getElementById('create-task-instructions')?.value.trim() || '',
        };
        if (isDraft) payload.draft = true;
        const resp = await boardFetch(boardProject, `/tasks`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload),
        });
        if (!resp.ok) {
            const data = await resp.json().catch(() => ({}));
            throw new Error(data.error || `HTTP ${resp.status}`);
        }
        hideCreateTaskModal();
        await loadBoardTasks(boardProject);
        showToast('Task created');
    } catch (e) {
        if (errEl) {
            errEl.textContent = e.message || 'Failed to create task';
            errEl.style.display = '';
        }
    }
}

// A task for an agent without a board goes on the agent's task list in Coral
// (title and details). Unless unchecked, the agent gets a short prompt to
// claim it with `coral-agent task claim`, which marks it in progress and
// shows the details; `coral-agent task complete <id>` marks it done.
async function _createSoloAgentTask(title, body, errEl) {
    const session = state.currentSession;
    const notify = document.getElementById('create-task-send')?.checked ?? true;
    try {
        // With notify, the server types the claim prompt into the agent's
        // terminal and returns it (see claimPrompt in routes/agent_tasks.go).
        const resp = await serverFetch(sessionServer(session), `/api/sessions/live/${encodeURIComponent(session.name)}/tasks`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ title, body, priority: document.getElementById('create-task-priority')?.value || 'medium', session_id: session.session_id, notify }),
        });
        const task = await resp.json().catch(() => ({}));
        if (!resp.ok) throw new Error(task.error || `HTTP ${resp.status}`);
        if (task.notify_error) throw new Error(`The task was added, but telling the agent failed: ${task.notify_error}`);
        if (task.notified) addPendingMessage(session.session_id, task.notified);
        hideCreateTaskModal();
        await loadAgentTasks(session.name, session.session_id);
        showToast(notify ? 'Task added; the agent was told to claim it' : 'Task added');
    } catch (e) {
        if (errEl) {
            errEl.textContent = e.message || 'Failed to create task';
            errEl.style.display = '';
        }
    }
}

// An agent that is not on a team board: tasks created for it are agent
// tasks (its own list in Coral), not board tasks.
function _isSoloAgent() {
    if (state.selectedTeam || boardOfPage()) return false;
    return !!state.currentSession && state.currentSession.type === 'live' && !state.currentSession.board_project;
}

// Team key (server + board) of the selected agent; a solo agent uses its own name as the board.
function _currentBoardKey() {
    const cs = state.currentSession;
    if (!cs) return null;
    const board = cs.board_project || cs.name;
    return board ? identityKey(sessionServer(cs), board) : null;
}

function _getBoardProject() {
    const pageBoard = boardOfPage();
    if (pageBoard) return pageBoard;
    if (state.selectedTeam) return state.selectedTeam;
    if (!state.currentSession) return null;
    return _currentBoardKey();
}

/* ── Task Detail Modal ─────────────────────────────────── */

/** The full task: fetched now unless already fetched and current. null when it cannot be read. */
async function _ensureFullBoardTask(taskId) {
    const have = _boardTaskById(taskId);
    if (have && have.workflow) return have;
    const board = _getBoardProject();
    if (!board) return null;
    try {
        const task = await _boardTasksFetch(board, `/tasks/${Number(taskId)}`);
        if (!task || task.id !== taskId) return null;
        _fullBoardTasks.set(taskId, task);
        return task;
    } catch {
        return null;
    }
}

/** Fetch the full task for the open detail view; show it if the view still shows that task. */
async function _fetchFullBoardTask(taskId, generation) {
    const task = await _ensureFullBoardTask(taskId);
    if (task && generation === _editGeneration && _boardTaskById(taskId) === task) showTaskDetailModal(taskId);
}

/** Shows a board task. A list item is light (#3269): it shows at once, then the
 *  full task once fetched; the returned promise resolves when that is on screen. */
export function showTaskDetailModal(taskId) {
    ++_editGeneration;
    _editOriginalTask = null;
    clearOpenSubagent(); // the modal is shared; it now shows a board task
    const sharedContent = document.getElementById('task-detail-content');
    if (sharedContent) { sharedContent._subagentHtml = null; delete sharedContent.dataset.subagentId; }
    const task = _boardTaskById(taskId);
    if (!task) return Promise.resolve();
    const full = task.workflow ? Promise.resolve() : _fetchFullBoardTask(taskId, _editGeneration);

    const modal = document.getElementById('task-detail-modal');
    const titleEl = document.getElementById('task-detail-modal-title');
    const content = document.getElementById('task-detail-content');
    if (!modal || !content) return full;

    titleEl.textContent = `Task #${task.id}`;
    content.innerHTML = _taskDetailHtml(task, task.status === 'in_progress' ? _liveCosts[task.id] : null);

    // Update footer with action buttons for editable tasks
    const footer = document.getElementById('task-detail-modal-footer');
    if (footer) {
        const isEditable = !_reviewRequested(task) && (task.status === 'pending' || task.status === 'in_progress' || task.status === 'blocked' || task.status === 'draft');
        if (isEditable) {
            const canComplete = task.status !== 'blocked' && task.status !== 'draft';
            const showPublish = task.status === 'draft';
            const canNudge = (task.status === 'pending' || task.status === 'in_progress') && !!task.assigned_to;
            footer.innerHTML = `
                <button class="btn btn-danger-text" data-on-click="task-cancel" data-task-id="${Number(task.id)}">Cancel Task</button>
                <span style="flex:1"></span>
                <button class="btn" data-on-click="task-close">Close</button>
                ${canNudge ? `<button class="btn" data-on-click="task-nudge" data-task-id="${Number(task.id)}" title="Remind ${escapeAttr(task.assigned_to)} about this task in their terminal">Nudge</button><button class="btn" data-on-click="task-remind" data-task-id="${Number(task.id)}" title="Send periodic reminders to ${escapeAttr(task.assigned_to)}">Remind</button><button class="btn" data-on-click="task-remind-stop" data-task-id="${Number(task.id)}" title="Stop periodic reminders">Stop reminders</button>` : ''}
                <button class="btn" data-on-click="task-edit" data-task-id="${Number(task.id)}">Edit</button>
                ${showPublish ? `<button class="btn btn-primary" data-on-click="task-publish" data-task-id="${Number(task.id)}">Publish</button>` : ''}
                ${canComplete ? `<button class="btn btn-success" data-on-click="task-complete" data-task-id="${Number(task.id)}">Complete</button>` : ''}`;
        } else {
            footer.innerHTML = `<button class="btn" data-on-click="task-close">Close</button>`;
        }
    }

    _openTaskDetailModal(modal);
    return full;
}

// Tasks created for an agent that is not on a board. They share the board
// task modal but not its actions: no edit, dependencies or cancel.
export function showAgentTaskDetailModal(taskId) {
    clearOpenSubagent();
    const sharedContent = document.getElementById('task-detail-content');
    if (sharedContent) { sharedContent._subagentHtml = null; delete sharedContent.dataset.subagentId; }
    const t = (state.currentAgentTasks || []).find(t => t.id === taskId);
    const modal = document.getElementById('task-detail-modal');
    const titleEl = document.getElementById('task-detail-modal-title');
    const content = document.getElementById('task-detail-content');
    if (!t || !modal || !content) return;

    const agentDisplayName = state.currentSession ? (state.currentSession.display_name || state.currentSession.name) : '';
    const task = {
        ...t,
        _source: 'agent',
        status: _agentTaskStatus(t),
        assigned_to: t.display_name || t.agent_name || agentDisplayName || null,
        claimed_at: t.started_at || null,
        // Agent tasks report 0 until usage is attributed; don't show a $0 breakdown.
        cost_usd: t.cost_usd > 0 ? t.cost_usd : null,
    };

    titleEl.textContent = `Task #${task.id}`;
    content.innerHTML = _taskDetailHtml(task, null);

    const footer = document.getElementById('task-detail-modal-footer');
    if (footer) {
        const open = task.status === 'pending' || task.status === 'in_progress';
        footer.innerHTML = `
            <span style="flex:1"></span>
            <button class="btn" data-on-click="task-close">Close</button>
            ${open ? `<button class="btn btn-success" data-on-click="task-complete" data-task-id="${Number(task.id)}" data-personal="1">Complete</button>` : ''}`;
    }

    _openTaskDetailModal(modal);
}

function _openTaskDetailModal(modal) {
    modal.style.display = '';
    // Close on backdrop click and on Escape
    closeOnBackdropAndEscape(modal, hideTaskDetailModal);
}

function _taskDetailHtml(task, liveCost) {
    const statusLabel = task.workflow?.outcome === 'failed' ? 'Finished' : task.status === 'completed' ? 'Finished'
        : task.status === 'review_pending' ? 'Open · Review pending (slot released)'
        : task.workflow?.completion_review && task.status === 'in_progress' ? 'Open · Completion review requested (slot occupied)'
        : task.status === 'in_progress' ? 'Open · In Progress'
        : task.status === 'skipped' ? 'Cancelled'
        : task.status === 'blocked' ? 'Blocked'
        : task.status === 'draft' ? 'Open · Draft'
        : 'Open';
    const statusClass = task.status === 'completed' ? 'task-detail-status-completed'
        : task.status === 'in_progress' ? 'task-detail-status-inprogress'
        : task.status === 'skipped' ? 'task-detail-status-cancelled'
        : task.status === 'blocked' ? 'task-detail-status-blocked'
        : task.status === 'draft' ? 'task-detail-status-draft'
        : 'task-detail-status-pending';
    const priorityClass = 'board-task-priority-' + (task.priority || 'medium');

    const assignee = task.assigned_to || '\u2014';
    const createdBy = task.created_by || '\u2014';
    const createdAt = task.created_at ? formatTaskDate(task.created_at) : '\u2014';
    const claimedAt = task.claimed_at ? formatTaskDate(task.claimed_at) : null;
    const completedAt = task.completed_at ? formatTaskDate(task.completed_at) : null;
    const completedBy = task.completed_by || null;

    let html = `
        <div class="task-detail-title">#${Number(task.id)} · ${escapeHtml(task.title)}</div>
        <div class="task-detail-meta">
            <span class="task-detail-meta-item"><span class="task-detail-meta-label">Lifecycle</span><span class="task-detail-status ${statusClass}">${statusLabel}</span></span>
            <span class="task-detail-meta-item"><span class="task-detail-meta-label">Priority</span><span class="board-task-priority ${priorityClass}" title="Claim priority: ${escapeAttr(task.priority || 'none')}">${task.priority ? (_priorityLabel[task.priority] || escapeHtml(task.priority)) : '\u2014'}</span></span>
            ${task.workflow?.outcome === 'failed' ? '<span class="task-detail-meta-item"><span class="task-detail-meta-label">Result</span><span class="task-detail-outcome-failed">Failed</span></span>' : ''}
        </div>`;

    const revision = Number(task.revision || 1);
    const amendments = task.workflow?.amendments || [];
    const preview = value => {
        const text = String(value || '').replace(/\s+/g, ' ').trim();
        return text.length > 180 ? `${text.slice(0, 177)}…` : text || '(empty)';
    };
    // Revisions only matter once the instructions have changed; a new task has nothing to show.
    if (revision > 1 || amendments.length) html += `<div class="task-detail-section task-detail-revision" data-task-revision="${Number(revision)}">
        <div class="task-detail-label">Instruction revision</div>
        <div class="task-detail-body">Current revision <strong>${revision}</strong>${amendments.length ? ` · ${amendments.length} amendment${amendments.length === 1 ? '' : 's'}` : ''}</div>
        ${amendments.length ? `<details class="task-amendment-history"><summary>Changed since task creation</summary>${amendments.map(amendment => {
            const previous = amendment.previous_snapshot || {};
            const effective = amendment.effective_snapshot || {};
            const changes = amendment.changes || {};
            const fields = Object.keys(changes).map(field => `<div><strong>${escapeHtml(field === 'workflow_instructions' ? 'Instructions' : 'Description')}</strong>: ${escapeHtml(preview(previous[field]))} → ${escapeHtml(preview(effective[field] ?? changes[field]))}</div>`).join('');
            return `<div class="task-amendment-entry"><strong>Revision ${Number(amendment.revision)}</strong> · ${escapeHtml(amendment.actor || 'Planner')} · ${escapeHtml(amendment.created_at || '')}<br><span>${escapeHtml(amendment.reason || '')}</span>${fields}</div>`;
        }).join('')}</details>` : ''}
    </div>`;

    if (task.body) {
        html += `<div class="task-detail-section">
            <div class="task-detail-label">Description</div>
            <div class="task-detail-body">${escapeHtml(task.body)}</div>
        </div>`;
    }

    const workflow = task.workflow;
    if (workflow) {
        const artifactHtml = a => {
            const uri = String(a.uri || '');
            const coralMatch = /^coral:\/\/artifacts\/([a-f0-9]{64})$/i.exec(uri);
            const href = coralMatch ? serverUrl(artifactPath(coralMatch[1]), contextServer()) : uri;
            const uriDisplay = (/^https?:\/\//i.test(uri) || coralMatch)
                ? `<a href="${escapeAttr(coralMatch ? '#' : escapeAttr(href))}" data-artifact-uri="${escapeAttr(uri)}"${coralMatch ? ' data-on-click="task-artifact-open"' : ' target="_blank" rel="noopener noreferrer"'}>${escapeHtml(uri)}</a>`
                : escapeHtml(uri);
            return `<div class="task-detail-body"><strong>${escapeHtml(a.name)}</strong>${a.revision ? ` · ${escapeHtml(a.revision)}` : ''}${a.digest ? ` · ${escapeHtml(a.digest)}` : ''}${uri ? `<div>${uriDisplay}</div>` : ''}${a.content ? `<pre style="white-space:pre-wrap;overflow-wrap:anywhere">${escapeHtml(a.content)}</pre>` : ''}</div>`;
        };
        html += `<details class="task-detail-section"><summary>Workflow${workflow.name ? `: ${escapeHtml(workflow.name)}` : ''}${workflow.stage ? ` · ${escapeHtml(workflow.stage)}` : ''}</summary>
            <div class="task-detail-body">${escapeHtml(workflow.instructions || '')}</div>
            <div>Required outputs: ${escapeHtml((workflow.required_outputs || []).join(', ') || 'None')}</div>
            <div>Completion gates: ${(workflow.completion_gates || []).length ? `<em>experimental, disabled by default (inactive unless the server enables them)</em> — ` : ''}${escapeHtml((workflow.completion_gates || []).map(g => `${g.name || g.type}${g.check_id ? ` [${g.check_id}]` : ''} (${g.artifact || 'server check'})`).join(', ') || 'None')}</div>
            ${workflow.candidate_revision ? `<div>Candidate revision: <code>${escapeHtml(workflow.candidate_revision)}</code></div>` : ''}
            ${(workflow.gate_results || []).length ? `<div>Gate results: ${(workflow.gate_results || []).map(g => `<span class="task-gate-result ${g.passed ? 'passed' : 'failed'}">${escapeHtml(g.name || g.type)}: ${escapeHtml(g.details || (g.passed ? 'passed' : 'failed'))}</span>`).join(' · ')}</div>` : ''}
            ${workflow.parent_task_id ? `<div>Parent task #${Number(workflow.parent_task_id)}</div>` : ''}
            ${workflow.retry_of ? `<div>Retry of task #${Number(workflow.retry_of)}</div>` : ''}</details>`;
        for (const input of workflow.inputs || []) html += `<details class="task-detail-section"><summary>Input from ${escapeHtml(input.board_id)} #${Number(input.task_id)} (${escapeHtml(input.outcome)})</summary>${(input.artifacts || []).map(artifactHtml).join('')}</details>`;
        if (workflow.completion_review) {
            const review = workflow.completion_review;
            html += `<div class="task-detail-section"><div class="task-detail-label">Completion candidate · not an accepted result</div>
                <div class="task-detail-body">Submitted by ${escapeHtml(review.submitted_by)} · ${escapeHtml(review.submitted_at)}<br>
                Proposed outcome: ${escapeHtml(review.proposed_outcome)}<br>${escapeHtml(review.reason)}<br>${escapeHtml(review.message || '')}</div>
                ${(review.artifacts || []).map(artifactHtml).join('')}
                ${review.released_by ? `<div>Slot released by ${escapeHtml(review.released_by)} · ${escapeHtml(review.released_at)}: ${escapeHtml(review.release_reason)}</div>` : '<div>The worker slot is still occupied. An orchestrator can release it with coral-board task release-review.</div>'}</div>`;
        }
        if (workflow.artifacts?.length) html += `<div class="task-detail-section"><div class="task-detail-label">Completion artifacts</div>${workflow.artifacts.map(artifactHtml).join('')}</div>`;
    }

    html += `<div class="task-detail-fields">
        <div class="task-detail-field">
            <span class="task-detail-label">Assigned To</span>
            <span class="task-detail-value">${escapeHtml(assignee)}</span>
        </div>
        <div class="task-detail-field">
            <span class="task-detail-label">Created By</span>
            <span class="task-detail-value">${escapeHtml(createdBy)}</span>
        </div>
        <div class="task-detail-field">
            <span class="task-detail-label">Created</span>
            <span class="task-detail-value">${createdAt}</span>
        </div>`;

    if (claimedAt) {
        const claimedFor = _claimedFor(task);
        html += `<div class="task-detail-field">
            <span class="task-detail-label">Claimed</span>
            <span class="task-detail-value">${claimedAt}${claimedFor ? ` <span class="task-detail-claimed-for">(${claimedFor} ago)</span>` : ''}</span>
        </div>`;
    }
    if (completedAt) {
        html += `<div class="task-detail-field">
            <span class="task-detail-label">${task.status === 'skipped' ? 'Cancelled' : 'Completed'}</span>
            <span class="task-detail-value">${completedAt}</span>
        </div>`;
    }
    if (completedBy) {
        html += `<div class="task-detail-field">
            <span class="task-detail-label">${task.status === 'skipped' ? 'Cancelled By' : 'Completed By'}</span>
            <span class="task-detail-value">${escapeHtml(completedBy)}</span>
        </div>`;
    }
    if (task.completion_message) {
        html += `<div class="task-detail-field task-detail-field-wide">
            <span class="task-detail-label">Message</span>
            <span class="task-detail-value">${escapeHtml(task.completion_message)}</span>
        </div>`;
    }
    if (task.workflow?.outcome === 'failed' && !task.completion_message) {
        html += `<div class="task-detail-field task-detail-field-wide task-detail-failure-reason">
            <span class="task-detail-label">Result details</span>
            <span class="task-detail-value">No failure reason was provided.</span>
        </div>`;
    }

    html += `</div>`;

    // Blocked by dependencies
    if (task.blocked_by && task.blocked_by.length > 0) {
        const boardProject = _getBoardProject();
        const depsHtml = task.blocked_by.map(dep => {
            const depStatusClass = dep.satisfied === false ? 'task-dep-status-blocked'
                : dep.status === 'completed' ? 'task-dep-status-completed'
                : dep.status === 'in_progress' ? 'task-dep-status-inprogress'
                : dep.status === 'skipped' ? 'task-dep-status-cancelled'
                : dep.status === 'blocked' ? 'task-dep-status-blocked'
                : dep.status === 'draft' ? 'task-dep-status-draft'
                : 'task-dep-status-pending';
            const depStatusLabel = dep.satisfied === false ? 'unmet'
                : dep.status === 'completed' ? 'completed'
                : dep.status === 'in_progress' ? 'in progress'
                : dep.status === 'skipped' ? 'cancelled'
                : dep.status === 'blocked' ? 'blocked'
                : dep.status === 'draft' ? 'draft'
                : 'pending';
            const isCrossBoard = task._source !== 'agent' && dep.board_id && dep.board_id !== boardKeyName(boardProject); // keys, not labels
            const boardPrefix = isCrossBoard ? `${escapeHtml(dep.board_label || keyLabel(identityKey(splitKey(boardProject).server, dep.board_id)))} ` : '';
            const depTitle = dep.title ? ` — ${escapeHtml(dep.title)}` : '';
            const clickable = !isCrossBoard ? ` data-on-click="task-open" data-kind="${escapeAttr(task._source === 'agent' ? 'agent' : 'board')}" data-task-id="${Number(dep.task_id)}" style="cursor:pointer"` : '';
            return `<div class="task-dep-item">
                <span class="task-dep-status ${depStatusClass}">${depStatusLabel}</span>
                <a class="task-dep-link"${clickable}>${boardPrefix}#${dep.task_id}${depTitle}</a>
                <span>${escapeHtml(dep.condition || 'success')}${dep.required_artifacts?.length ? ` · requires ${escapeHtml(dep.required_artifacts.join(', '))}` : ''}${dep.blocked_reason ? ` · ${escapeHtml(dep.blocked_reason)}` : ''}</span>
            </div>`;
        }).join('');
        html += `<div class="task-detail-section">
            <div class="task-detail-label">Blocked By</div>
            <div class="task-deps-list">${depsHtml}</div>
        </div>`;
    }

    // Cost & token breakdown for completed tasks with cost data
    if (task.cost_usd != null) {
        const warningClass = task.cost_usd >= 1.0 ? ' board-task-cost-warning' : '';
        html += `<div class="task-detail-section">
            <div class="task-detail-label">Cost</div>
            <div class="task-detail-cost-summary${warningClass}">${_formatCost(task.cost_usd, true)}</div>
            <div class="task-detail-tokens">
                <div class="task-detail-token-item">
                    <span class="task-detail-token-label">Input</span>
                    <span class="task-detail-token-value">${_formatTokenCount(task.input_tokens)}</span>
                </div>
                <div class="task-detail-token-item">
                    <span class="task-detail-token-label">Output</span>
                    <span class="task-detail-token-value">${_formatTokenCount(task.output_tokens)}</span>
                </div>
                <div class="task-detail-token-item">
                    <span class="task-detail-token-label">Cache Read</span>
                    <span class="task-detail-token-value">${_formatTokenCount(task.cache_read_tokens)}</span>
                </div>
                <div class="task-detail-token-item">
                    <span class="task-detail-token-label">Cache Write</span>
                    <span class="task-detail-token-value">${_formatTokenCount(task.cache_write_tokens)}</span>
                </div>
            </div>
        </div>`;
    }
    // Live cost for in-progress tasks
    if (liveCost) {
        const lc = liveCost;
        const warningClass = lc.cost_usd >= 1.0 ? ' board-task-cost-warning' : '';
        html += `<div class="task-detail-section">
            <div class="task-detail-label">Running Cost</div>
            <div class="task-detail-cost-summary board-task-cost-live${warningClass}">~${_formatCost(lc.cost_usd, true)}</div>
            <div class="task-detail-tokens">
                <div class="task-detail-token-item">
                    <span class="task-detail-token-label">Input</span>
                    <span class="task-detail-token-value">${_formatTokenCount(lc.input_tokens)}</span>
                </div>
                <div class="task-detail-token-item">
                    <span class="task-detail-token-label">Output</span>
                    <span class="task-detail-token-value">${_formatTokenCount(lc.output_tokens)}</span>
                </div>
                <div class="task-detail-token-item">
                    <span class="task-detail-token-label">Cache Read</span>
                    <span class="task-detail-token-value">${_formatTokenCount(lc.cache_read_tokens)}</span>
                </div>
                <div class="task-detail-token-item">
                    <span class="task-detail-token-label">Cache Write</span>
                    <span class="task-detail-token-value">${_formatTokenCount(lc.cache_write_tokens)}</span>
                </div>
            </div>
            <div class="task-detail-token-item" style="margin-top:4px;opacity:0.6">
                <span class="task-detail-token-label">Requests</span>
                <span class="task-detail-token-value">${lc.request_count}</span>
            </div>
        </div>`;
    }
    return html;
}

let _editOriginalTask = null;
let _editBoardProject = null;
let _editGeneration = 0;

export async function enableTaskEditMode(taskId) {
    let task = _boardTaskById(taskId);
    if (!task) return;
    if (!task.workflow) {
        // Never edit a light list item: its body is only a preview, and saving it would
        // cut the body short (#3269).
        task = await _ensureFullBoardTask(taskId);
        if (!task) {
            showToast('Could not load the task to edit it. Try again.', true);
            return;
        }
    }
    _editOriginalTask = task;
    const editGeneration = ++_editGeneration;
    _editBoardProject = _getBoardProject();

    const content = document.getElementById('task-detail-content');
    if (!content) return;

    const originalContent = content.firstChild;

    // Subscriber presence is not assignment authority. Keep an inactive or
    // temporarily unavailable assignee selected even if discovery fails.
    let assigneeOptions = '<option value="">Unassigned</option>';
    const boardProject = _editBoardProject;
    const names = new Set();
    if (task.assigned_to) {
        names.add(task.assigned_to);
        assigneeOptions += `<option value="${escapeAttr(task.assigned_to)}" selected>${escapeHtml(task.assigned_to)}</option>`;
    }
    if (boardProject) {
        try {
            const resp = await boardFetch(boardProject, `/subscribers`);
            if (resp.ok) {
                const subs = await resp.json();
                (Array.isArray(subs) ? subs : []).forEach(s => {
                    const name = s?.subscriber_id || s?.name;
                    if (typeof name === 'string' && name && !names.has(name)) {
                        names.add(name);
                        assigneeOptions += `<option value="${escapeAttr(name)}">${escapeHtml(name)}</option>`;
                    }
                });
            }
        } catch { /* ignore */ }
    }

    if (editGeneration !== _editGeneration || _editOriginalTask !== task ||
        _getBoardProject() !== boardProject || !content.isConnected || content.firstChild !== originalContent) return;

    const priorityOptions = ['critical', 'high', 'medium', 'low'].map(p =>
        `<option value="${escapeAttr(p)}"${p === task.priority ? ' selected' : ''}>${p}</option>`
    ).join('');

    const showDepPicker = task.status === 'blocked' || task.status === 'pending' || task.status === 'draft';
    const workflowInstructions = task.workflow?.effective_instructions || task.workflow?.instructions || '';
    const revision = Number(task.revision || 1);
    content.innerHTML = `
        <div id="task-edit-error" class="modal-error" style="display:none"></div>
        <div class="task-edit-revision">Editing revision <strong>${revision}</strong>. Changes to description or instructions require a reason.</div>
        <label for="task-edit-title">Title
            <input type="text" id="task-edit-title" value="${escapeAttr(task.title)}">
        </label>
        <label for="task-edit-body">Description
            <textarea id="task-edit-body" rows="3">${escapeHtml(task.body || '')}</textarea>
        </label>
        <label for="task-edit-instructions">Workflow instructions
            <textarea id="task-edit-instructions" rows="3">${escapeHtml(workflowInstructions)}</textarea>
        </label>
        <label for="task-edit-reason">Amendment reason
            <input type="text" id="task-edit-reason" placeholder="Why should this instruction change?">
        </label>
        <label for="task-edit-priority">Priority
            <select id="task-edit-priority">${priorityOptions}</select>
        </label>
        <label for="task-edit-assignee">Assigned To
            <select id="task-edit-assignee">${assigneeOptions}</select>
        </label>
        ${showDepPicker ? '<label>Blocked By <span class="text-muted-sm">(optional)</span></label><div id="task-edit-deps" class="dep-picker"></div>' : ''}`;

    if (showDepPicker) {
        const currentDepIds = (task.blocked_by || []).map(d => d.task_id);
        _renderDepPicker('task-edit-deps', currentDepIds, taskId);
    }

    const footer = document.getElementById('task-detail-modal-footer');
    if (footer) {
        footer.innerHTML = `
            <button class="btn" data-on-click="task-edit-cancel">Cancel</button>
            <button class="btn btn-primary" data-on-click="task-edit-save" data-task-id="${Number(taskId)}">Save</button>`;
    }
}

export async function saveTaskEdit(taskId) {
    const task = _editOriginalTask;
    if (!task || task.id !== taskId || _editBoardProject !== _getBoardProject()) return;

    const title = document.getElementById('task-edit-title').value.trim();
    const errEl = document.getElementById('task-edit-error');
    if (!title) {
        if (errEl) { errEl.textContent = 'Title is required'; errEl.style.display = ''; }
        return;
    }

    const body = document.getElementById('task-edit-body').value.trim();
    const priority = document.getElementById('task-edit-priority').value;
    const assignedTo = document.getElementById('task-edit-assignee').value;

    const updates = {};
    if (title !== task.title) updates.title = title;
    const workflowInstructions = document.getElementById('task-edit-instructions')?.value.trim() || '';
    const originalInstructions = task.workflow?.effective_instructions || task.workflow?.instructions || '';
    const amendmentChanges = {};
    if (body !== (task.body || '')) amendmentChanges.body = body;
    if (workflowInstructions !== originalInstructions) amendmentChanges.workflow_instructions = workflowInstructions;
    if (priority !== task.priority) updates.priority = priority;
    if (assignedTo !== (task.assigned_to || '')) {
        updates.assigned_to = assignedTo;
        updates.subscriber_id = 'Operator';
    }

    const depPickerEl = document.getElementById('task-edit-deps');
    if (depPickerEl) {
        const newDeps = _getSelectedDeps('task-edit-deps');
        const oldDeps = (task.blocked_by || []).map(d => d.task_id).sort();
        const sortedNew = [...newDeps].sort();
        if (JSON.stringify(oldDeps) !== JSON.stringify(sortedNew)) {
            updates.blocked_by = newDeps.map(id => (task.blocked_by || []).find(d => d.task_id === id) || { task_id: id, condition: 'success' });
        }
    }

    if (Object.keys(updates).length === 0 && Object.keys(amendmentChanges).length === 0) {
        cancelTaskEdit();
        return;
    }

    const boardProject = _getBoardProject();
    if (!boardProject) return;

    try {
        if (Object.keys(amendmentChanges).length) {
            const reason = document.getElementById('task-edit-reason')?.value.trim() || '';
            if (!reason) throw new Error('An amendment reason is required for description or instruction changes');
            const amendmentResp = await boardFetch(boardProject, `/tasks/${taskId}/amend`, {
                method: 'PATCH',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ subscriber_id: 'Operator', base_revision: Number(task.revision || 1), reason, changes: amendmentChanges }),
            });
            if (!amendmentResp.ok) {
                const data = await amendmentResp.json().catch(() => ({}));
                throw new Error(data.error || `HTTP ${amendmentResp.status}`);
            }
        }
        if (Object.keys(updates).length) {
            const resp = await boardFetch(boardProject, `/tasks/${taskId}`, {
                method: 'PATCH',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(updates),
            });
            if (!resp.ok) {
                const data = await resp.json().catch(() => ({}));
                throw new Error(data.error || `HTTP ${resp.status}`);
            }
        }
        _editOriginalTask = null;
        await loadBoardTasks(boardProject);
        showTaskDetailModal(taskId);
        showToast('Task updated');
    } catch (e) {
        if (errEl) { errEl.textContent = e.message || 'Failed to save'; errEl.style.display = ''; }
    }
}

export function cancelTaskEdit() {
    if (_editOriginalTask) {
        showTaskDetailModal(_editOriginalTask.id);
        _editOriginalTask = null;
    }
}

function _taskRequiredOutputs(task) {
    const outputs = task?.workflow?.required_outputs || task?.required_outputs || [];
    const list = Array.isArray(outputs) ? outputs : [outputs];
    return list.map(output => typeof output === 'string' ? output : output?.name).filter(Boolean);
}

export function completeBoardTask(taskId, personal = false) {
    const footer = document.getElementById('task-detail-modal-footer');
    const content = document.getElementById('task-detail-content');
    if (!footer || !content) return;
    const task = personal
        ? (state.currentAgentTasks || []).find(item => item.id === taskId)
        : _boardTaskById(taskId);
    const requiredOutputs = _taskRequiredOutputs(task);
    const requiredLabel = requiredOutputs.length
        ? `<section class="task-completion-required" aria-label="Required outputs"><strong>Required outputs</strong><ul>${requiredOutputs.map(output => `<li>${escapeHtml(output)}</li>`).join('')}</ul><p>Provide each named output below or in the manifest before completing.</p></section>`
        : '<p class="task-completion-hint">Add a concise outcome and message. Evidence is optional unless the task specifies required outputs.</p>';
    content.insertAdjacentHTML('beforeend', `
        <section class="task-completion-form" aria-label="Complete task">
            <p class="task-completion-revision">Submitting the task as revision <strong>${Number(task?.revision || 1)}</strong>.</p>
            <div class="task-confirm-inline">
                <label for="task-complete-message">Completion message <textarea id="task-complete-message" rows="3" placeholder="What was completed and how was it verified?"></textarea></label>
                <label for="task-complete-outcome">Outcome <select id="task-complete-outcome"><option value="success">Success</option><option value="failed">Failed</option></select></label>
                ${requiredLabel}
                <details class="task-completion-evidence" ${requiredOutputs.length ? 'open' : ''}>
                    <summary>${requiredOutputs.length ? 'Evidence and artifacts (required)' : 'Add evidence or artifact metadata (optional)'}</summary>
                    <label for="task-artifact-name">Artifact name ${requiredOutputs.length ? '<span class="text-muted-sm">(match a required output)</span>' : ''}<input id="task-artifact-name" placeholder="build or test_report"></label>
                    <label for="task-artifact-uri">Artifact URL or reference<input id="task-artifact-uri" placeholder="Durable URL or artifact reference"></label>
                    <label for="task-artifact-revision">Revision<input id="task-artifact-revision" placeholder="Commit or build version"></label>
                    <label for="task-artifact-content">Artifact content<textarea id="task-artifact-content" rows="3" placeholder="Report or evidence (optional if a reference is provided)"></textarea></label>
                    <label for="task-artifacts-file">Attach an artifact manifest<input type="file" id="task-artifacts-file" accept="application/json,.json"></label>
                </details>
                <div id="task-complete-error" class="modal-error" role="alert" hidden></div>
                <button id="task-complete-refresh" class="btn task-completion-refresh" type="button" hidden>Refresh task before retrying</button>
            </div>
        </section>`);
    footer.innerHTML = `
        <button class="btn" data-on-click="task-complete-back" data-task-id="${Number(taskId)}" data-personal="${Number(personal ? 1 : 0)}">Back</button>
        <span style="flex:1"></span>
        <button class="btn btn-success" data-on-click="task-complete-do" data-task-id="${Number(taskId)}" data-personal="${Number(personal ? 1 : 0)}">Complete</button>`;
    document.getElementById('task-complete-message').focus();
}

export async function _doCompleteTask(taskId, personal = false) {
    const boardProject = _getBoardProject();
    if (personal ? !state.currentSession?.session_id : !boardProject) return;
    const msgEl = document.getElementById('task-complete-message');
    const message = msgEl ? msgEl.value.trim() : '';
    try {
        let artifacts = [];
        const file = document.getElementById('task-artifacts-file')?.files[0];
        if (file) {
            if (file.size > 3 * 1024 * 1024) throw new Error('Artifact manifest is too large');
            artifacts = JSON.parse(await file.text());
            if (!Array.isArray(artifacts)) throw new Error('Artifact manifest must contain an array');
        }
        const name = document.getElementById('task-artifact-name')?.value.trim();
        if (name) artifacts.push({ name,
            uri: document.getElementById('task-artifact-uri').value.trim(),
            revision: document.getElementById('task-artifact-revision').value.trim(),
            content: document.getElementById('task-artifact-content').value,
        });
        const task = personal
            ? (state.currentAgentTasks || []).find(item => item.id === taskId)
            : _boardTaskById(taskId);
        const requiredOutputs = _taskRequiredOutputs(task);
        const suppliedNames = new Set(artifacts.map(artifact => artifact?.name).filter(Boolean));
        const missingOutputs = requiredOutputs.filter(output => !suppliedNames.has(output));
        if (missingOutputs.length) throw new Error(`Required outputs missing: ${missingOutputs.join(', ')}`);
        const outcome = document.getElementById('task-complete-outcome')?.value || 'success';
        const send = init => personal
            ? serverFetch(currentServer(), `/api/agent/tasks/${taskId}/complete`, init)
            : boardFetch(boardProject, `/tasks/${taskId}/complete`, init);
        const identity = personal ? { session_id: state.currentSession.session_id } : { subscriber_id: 'Operator' };
        const expectedRevision = !personal && task ? Number(task.revision || 1) : undefined;
        const resp = await send({
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ ...identity, message: message || undefined, outcome, artifacts, ...(expectedRevision ? { expected_revision: expectedRevision } : {}) }),
        });
        if (!resp.ok) {
            const data = await resp.json().catch(() => ({}));
            const error = new Error(data.error || `HTTP ${resp.status}`);
            error.status = resp.status;
            throw error;
        }
        hideTaskDetailModal();
        if (personal) await loadAgentTasks(state.currentSession.name, state.currentSession.session_id);
        else await loadBoardTasks(boardProject);
        showToast('Task completed');
    } catch (e) {
        const errorEl = document.getElementById('task-complete-error');
        if (errorEl) { errorEl.textContent = e.message || 'Failed to complete task'; errorEl.hidden = false; }
        const refresh = document.getElementById('task-complete-refresh');
        if (refresh && e.status === 409) {
            refresh.hidden = false;
            refresh.onclick = async () => {
                if (personal) {
                    await loadAgentTasks(state.currentSession.name, state.currentSession.session_id);
                    showAgentTaskDetailModal(taskId);
                } else {
                    await loadBoardTasks(boardProject);
                    showTaskDetailModal(taskId);
                }
            };
        }
        showToast(e.message || 'Failed to complete task', true);
    }
}

// Resend the task's nudge to its assignee (a nudge typed while the agent was
// busy, e.g. compacting, can be lost).
export async function nudgeBoardTask(taskId) {
    const boardProject = _getBoardProject();
    if (!boardProject) return;
    try {
        const resp = await boardFetch(boardProject, `/tasks/${taskId}/nudge?subscriber_id=Operator`, { method: 'POST' });
        const data = await resp.json().catch(() => ({}));
        if (!resp.ok) throw new Error(data.error || `HTTP ${resp.status}`);
        showToast(`Nudged ${data.assignee || 'the assignee'}`);
    } catch (e) {
        showToast(e.message || 'Failed to nudge', true);
    }
}

export async function remindBoardTask(taskId) {
    const boardProject = _getBoardProject();
    if (!boardProject) return;
    const minutes = Number(window.prompt('Send a reminder every how many minutes?', '5'));
    if (!Number.isFinite(minutes) || minutes <= 0) return;
    const intervalSeconds = Math.round(minutes * 60);
    if (intervalSeconds < 30 || intervalSeconds > 86400) {
        showToast('Reminder interval must be between 0.5 minutes and 24 hours', true);
        return;
    }
    try {
        const resp = await boardFetch(boardProject, `/tasks/${taskId}/reminder`, {
            method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ interval_seconds: intervalSeconds, subscriber_id: 'Operator' }),
        });
        const data = await resp.json().catch(() => ({}));
        if (!resp.ok) throw new Error(data.error || `HTTP ${resp.status}`);
        showToast(`Periodic reminders started for ${data.assignee || 'the assignee'}`);
    } catch (e) { showToast(e.message || 'Failed to start reminders', true); }
}

export async function stopBoardTaskReminder(taskId) {
    const boardProject = _getBoardProject();
    if (!boardProject) return;
    await boardFetch(boardProject, `/tasks/${taskId}/reminder?subscriber_id=Operator`, { method: 'DELETE' });
    showToast('Periodic reminders stopped');
}

export function cancelBoardTask(taskId) {
    const footer = document.getElementById('task-detail-modal-footer');
    if (!footer) return;
    footer.innerHTML = `
        <div class="task-confirm-inline">
            <span class="task-confirm-text">Cancel this task? This cannot be undone.</span>
            <div class="task-confirm-buttons">
                <button class="btn" data-on-click="task-cancel-back" data-task-id="${Number(taskId)}">No, Go Back</button>
                <button class="btn btn-danger" data-on-click="task-cancel-do" data-task-id="${Number(taskId)}">Yes, Cancel</button>
            </div>
        </div>`;
}

export async function _doCancelTask(taskId) {
    const boardProject = _getBoardProject();
    if (!boardProject) return;
    try {
        const resp = await boardFetch(boardProject, `/tasks/${taskId}/cancel`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ subscriber_id: 'Operator' }),
        });
        if (!resp.ok) {
            const data = await resp.json().catch(() => ({}));
            throw new Error(data.error || `HTTP ${resp.status}`);
        }
        hideTaskDetailModal();
        await loadBoardTasks(boardProject);
        showToast('Task cancelled');
    } catch (e) {
        showToast(e.message || 'Failed to cancel task', true);
    }
}

export async function publishBoardTask(taskId) {
    const boardProject = _getBoardProject();
    if (!boardProject) return;
    try {
        const resp = await boardFetch(boardProject, `/tasks/${taskId}/publish`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ subscriber_id: 'Operator' }), // publishing is a planner's step (#3668)
        });
        if (!resp.ok) {
            const data = await resp.json().catch(() => ({}));
            throw new Error(data.error || `HTTP ${resp.status}`);
        }
        await loadBoardTasks(boardProject);
        showTaskDetailModal(taskId);
        showToast('Task published');
    } catch (e) {
        showToast(e.message || 'Failed to publish task', true);
    }
}

export function _restoreTaskFooter(taskId) {
    showTaskDetailModal(taskId);
}

export function hideTaskDetailModal() {
    ++_editGeneration;
    _editOriginalTask = null;
    clearOpenSubagent();
    const modal = document.getElementById('task-detail-modal');
    if (!modal) return;
    modal.style.display = 'none';
    releaseEscape(modal);
}

// What the subagent dialog calls back (task_hooks.js).
registerTaskView({ renderBoardTaskList, hideTaskDetailModal });

// The task list and task dialogs' markup (#3645): data-on-* names, never code. Each action calls
// the same function the inline handler did, after checking its values (a bad one does nothing).
// Cancelling and completing a task still go through their confirm footers (task-cancel shows
// "Yes, Cancel"; task-complete shows the completion form), as before.
const TASK_SORTS = new Set(['id', 'priority', 'type', 'assignee', 'created_at', 'cost']);
const withId = (v, f) => { const id = idOf(v); if (id) f(id); };
const isPersonal = el => el.dataset.personal === '1';
const depContainer = el => { const id = tokenOf(el.dataset.container); return id ? document.getElementById(id) : null; };
registerActions({
    'task-close': () => window.hideTaskDetailModal(),
    'task-hide-done': () => _toggleHideCompleted(),
    'task-sort': el => { if (TASK_SORTS.has(el.dataset.sort)) window.__coral._toggleTaskSort(el.dataset.sort); },
    'task-open': el => withId(el.dataset.taskId, id => (el.dataset.kind === 'agent' ? window.showAgentTaskDetailModal(id) : window.showTaskDetailModal(id))),
    'task-subagent-open': el => { const sid = textOf(el.dataset.subagentId); if (sid) showSubagentDetailModal(sid); },
    'task-load-more': () => window.loadMoreBoardTasks(),
    'dep-picker-toggle': el => {
        const list = depContainer(el) && document.getElementById(el.dataset.container + '-list');
        if (list) list.style.display = list.style.display === 'none' ? '' : 'none';
    },
    'dep-picker-change': el => { if (depContainer(el)) _updateDepTags(el.dataset.container); },
    'dep-tag-remove': el => {
        const box = depContainer(el), id = idOf(el.dataset.taskId);
        if (box && id) box.querySelector(`input[type="checkbox"][value="${id}"]`)?.click();
    },
    'task-cancel': el => withId(el.dataset.taskId, id => window.cancelBoardTask(id)),
    'task-cancel-back': el => withId(el.dataset.taskId, id => window._restoreTaskFooter(id)),
    'task-cancel-do': el => withId(el.dataset.taskId, id => window._doCancelTask(id)),
    'task-nudge': el => withId(el.dataset.taskId, id => window.nudgeBoardTask(id)),
    'task-remind': el => withId(el.dataset.taskId, id => window.remindBoardTask(id)),
    'task-remind-stop': el => withId(el.dataset.taskId, id => window.stopBoardTaskReminder(id)),
    'task-edit': el => withId(el.dataset.taskId, id => window.enableTaskEditMode(id)),
    'task-edit-cancel': () => window.cancelTaskEdit(),
    'task-edit-save': el => withId(el.dataset.taskId, id => window.saveTaskEdit(id)),
    'task-publish': el => withId(el.dataset.taskId, id => window.publishBoardTask(id)),
    'task-complete': el => withId(el.dataset.taskId, id => (isPersonal(el) ? window.completeBoardTask(id, true) : window.completeBoardTask(id))),
    'task-complete-back': el => withId(el.dataset.taskId, id => (isPersonal(el) ? window.showAgentTaskDetailModal(id) : window._restoreTaskFooter(id))),
    'task-complete-do': el => withId(el.dataset.taskId, id => window._doCompleteTask(id, isPersonal(el))),
    // A task's coral:// artifact opens in the preview (only an artifact URI; the link doesn't navigate).
    'task-artifact-open': (el, event) => {
        event.preventDefault();
        const uri = el.dataset.artifactUri || '';
        if (!/^coral:\/\/artifacts\/[a-f0-9]{64}$/i.test(uri)) return;
        window.hideTaskDetailModal?.();
        window.openFilePreview?.(uri);
    },
});
