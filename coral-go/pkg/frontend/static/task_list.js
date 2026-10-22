/* The task list: polling, loading, filtering, sorting and drawing it (moved out of tasks.js, #5082; no behaviour change). */
import { state } from './state.js';
import { boardOfPage } from './dock_page.js';
import { pollEvery, afterPause, isBusyStatus, busyRetryingText, machineOf } from './overload.js';
import { platform } from './platform/detect.js';
import { loadSubagents, _claimedFor, _subagentTooltip, subagentToTaskRow } from './subagents.js';
import { _currentBoardKey } from './task_board_context.js';
import { pathId, boardFetch, serverFetch, serverForSession, fetchValidated, dropBody, isCloud, serverUrl, splitKey } from './server_base.js';
import { EMPTY as TASK_EMPTY, isQuery, loadFilters as loadTaskFilters, phases as taskPhases, toParams as taskParams, saveFilters as saveTaskFilters } from './task_filters.js';
import { browserLocal } from './safe_storage.js';
import { showToast, escapeHtml, escapeAttr } from './utils.js';
import { buildTaskBar } from './task_bar.js';
import { loadPanelOpen, savePanelOpen } from './list_bar.js';
import { _priorityOrder, _formatCost, _formatTaskTime, _priorityLabel, formatTaskDate, _agentTaskStatus } from './task_format.js';
import { morphHtml } from './dom_morph.js';
import { _verifyMark } from './task_verify_marks.js';

// ── Board task polling ───────────────────────────────────────────────
export let _boardTaskPollTimer = null;

// Seam for tests: the poll period, read when the poll starts (tests shorten it instead of waiting 10 s per tick).
export const boardTaskPollHooks = { intervalMs: 10000 };

// The tick in flight, if any. A large board's task list can take longer than the period to
// fetch and draw; a tick never starts while the previous one runs, so slow fetches cannot
// pile up behind each other (#3262).
let _boardTaskPollInFlight = null;

// Live cost cache: taskID → { cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, request_count }
export let _liveCosts = {};

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
export function _boardTaskPollTick(force = false) {
    // force: a (re)start for a new selection loads at once, even past the old selection's tick.
    if (!force && (_boardTaskPollInFlight || (!platform.isNative && document.hidden))) return;
    const run = _pollBoardTasksOnce().catch(() => {}).finally(() => {
        if (_boardTaskPollInFlight === run) _boardTaskPollInFlight = null;
    });
    _boardTaskPollInFlight = run;
}

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
            const resp = await boardFetch(boardProject, `/tasks/${pathId(t.id)}/cost`);
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

export function renderTaskList() {
    // Agent tasks are now rendered in the unified board task table.
    // Trigger a re-render of the unified table to include updated agent tasks.
    renderBoardTaskList();
}

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

export async function _boardTasksFetch(boardName, suffix) {
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), boardTaskFetchHooks.timeoutMs);
    try {
        // Polled every 10 s per open board: an unchanged page comes back 304 (#3439).
        const r = await fetchValidated('board-tasks ' + boardName + suffix, headers => boardFetch(boardName, suffix, { signal: ctrl.signal, headers }));
        if (!r.ok) { dropBody(r.resp); const err = new Error(`board tasks fetch failed: ${r.resp.status}`); err.resp = r.resp; throw err; }
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
    _syncTaskFiltersBoard(boardName);
    await _loadBoardTasksDefault(boardName);
    if (_queryActive()) await _loadFilteredTasks(boardName, { refresh: true });
}

async function _loadBoardTasksDefault(boardName) {
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

// ── Search and filters (#4744) ───────────────────────────────────────────
// With a search, a filter or an order the default list does not have, the board's tasks come from the task query (GET /api/board/tasks, the server matches every task of the board, #4745),
// as pages with a total. Without them nothing here runs and the list is the paged default above. "Open first" has no server sort: it is the open tasks, then the done ones (two phases).
const _emptyFiltered = board => ({ board, rows: [], phase: 0, cursor: '', total: null, doneTotal: 0, truncated: false, busy: false, error: '' });

let _filteredList = _emptyFiltered(null);

let _taskFilters = { ...TASK_EMPTY };

let _taskFiltersBoard = null;

let _taskBar = null;

let _filteredSeq = 0;

const _queryActive = () => isQuery(_taskFilters) && !!_boardList.board;

/** A board change: its own saved filters, a fresh bar (the old one lets go of the page's click listener). */
function _syncTaskFiltersBoard(boardName) {
    if (boardName === _taskFiltersBoard) return;
    _taskFiltersBoard = boardName;
    _taskFilters = loadTaskFilters(boardName, browserLocal);
    _filteredList = _emptyFiltered(boardName);
    _filteredSeq++;
    if (_taskBar) { _taskBar.dispose(); _taskBar = null; }
}

async function _taskQueryFetch(boardKey, pairs) {
    const { server, name } = splitKey(boardKey);
    const qs = new URLSearchParams(pairs.map(([k, v]) => [k, k === 'board' ? name : v])).toString();
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), boardTaskFetchHooks.timeoutMs);
    try {
        const r = await fetch(serverUrl(`/api/board/tasks?${qs}`, server), { signal: ctrl.signal });
        if (!r.ok) { const err = new Error(`task search failed: ${r.status}`); err.resp = r; throw err; }
        return await r.json();
    } finally { clearTimeout(timer); }
}

/** Fetch the filtered list's first page (or the next one: `append`), from the phase and cursor it is at. A poll (`refresh`) keeps a list the person paged further. */
async function _loadFilteredTasks(boardName, { append = false, refresh = false } = {}) {
    const f = _taskFilters;
    const ph = taskPhases(f);
    const L = _filteredList.board === boardName ? _filteredList : (_filteredList = _emptyFiltered(boardName));
    if (refresh && L.rows.length > BOARD_TASK_PAGE) return; // paged further than the first page: a poll leaves it
    const seq = ++_filteredSeq;
    L.busy = true; L.error = '';
    if (!append && !refresh) renderBoardTaskList();
    try {
        let rows, cursor, total, doneTotal = L.doneTotal, truncated = false, phase = L.phase;
        if (append) {
            const statuses = ph ? ph[phase] : null;
            if (ph && phase === 0 && !L.cursor) phase = 1;
            const pairs = taskParams(f, boardName, { statuses: ph ? ph[phase] : null });
            if (phase === L.phase && L.cursor) pairs.push(['cursor', L.cursor]);
            const data = await _taskQueryFetch(boardName, pairs);
            if (seq !== _filteredSeq || _taskFilters !== f) return;
            const seen = new Set(L.rows.map(t => t.id));
            rows = [...L.rows, ...(data.tasks || []).filter(t => !seen.has(t.id))];
            cursor = data.next_cursor || ''; total = L.total; truncated = L.truncated || !!data.truncated; void statuses;
        } else if (ph) {
            const [open, done] = await Promise.all([
                _taskQueryFetch(boardName, taskParams(f, boardName, { statuses: ph[0] })),
                _taskQueryFetch(boardName, taskParams(f, boardName, { statuses: ph[1], limit: 1 })),
            ]);
            if (seq !== _filteredSeq || _taskFilters !== f) return;
            rows = open.tasks || []; cursor = open.next_cursor || ''; phase = 0;
            doneTotal = done.total ?? 0; total = (open.total ?? rows.length) + doneTotal; truncated = !!(open.truncated || done.truncated);
        } else {
            const data = await _taskQueryFetch(boardName, taskParams(f, boardName));
            if (seq !== _filteredSeq || _taskFilters !== f) return;
            rows = data.tasks || []; cursor = data.next_cursor || ''; total = data.total ?? rows.length; truncated = !!data.truncated; phase = 0;
        }
        Object.assign(L, { rows, cursor, total, doneTotal, truncated, phase });
    } catch (e) {
        if (seq !== _filteredSeq || _taskFilters !== f) return;
        L.error = e && e.resp && e.resp.status === 400 ? 'This search can not be run. Change it and try again.' : 'Could not search the tasks. Try again.';
        if (append) showToast(L.error, true);
    } finally {
        if (seq === _filteredSeq) { L.busy = false; renderBoardTaskList(); }
    }
}

/** Is there another page of the filtered list (the open phase ending hands over to the done phase). */
function _filteredHasMore() {
    const L = _filteredList;
    if (L.cursor) return true;
    return !!taskPhases(_taskFilters) && L.phase === 0 && L.doneTotal > 0;
}

/** The bar's choice: kept for the board, and the list is asked again (or the default list is back). */
function _setTaskFilters(next) {
    _taskFilters = next;
    saveTaskFilters(_taskFiltersBoard, next, browserLocal);
    _filteredList = _emptyFiltered(_taskFiltersBoard);
    _filteredSeq++;
    if (_queryActive()) _loadFilteredTasks(_taskFiltersBoard); else renderBoardTaskList();
}

/** The bar in its place above the list, shown for a board's tasks, told the filters and the counts. */
function _syncTaskBar(show, counts) {
    const host = document.getElementById('board-task-bar');
    if (!host) return;
    if (!show) { if (_taskBar && _taskBar.node.parentElement === host) _taskBar.node.remove(); return; }
    if (!_taskBar) {
        const board = _taskFiltersBoard;
        _taskBar = buildTaskBar(_setTaskFilters, { onPanel: open => savePanelOpen('tasks', board, open) });
        _taskBar.setOpen(loadPanelOpen('tasks', board)); // the panel is left as it was for this board (#5293)
    }
    if (_taskBar.node.parentElement !== host) host.append(_taskBar.node);
    _taskBar.names([...new Set((state.currentBoardTasks || []).map(t => t.assigned_to).filter(n => typeof n === 'string' && n))].sort().slice(0, 50));
    _taskBar.set(_taskFilters, counts);
}

/** "Load more": the next page of done tasks (or of the filtered list). */
export async function loadMoreBoardTasks() {
    if (_queryActive()) { if (_filteredHasMore() && !_filteredList.busy) await _loadFilteredTasks(_boardList.board, { append: true }); return; }
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

/** A list item says review_requested; a full task has workflow.completion_review. */
export function _reviewRequested(t) {
    return !!(t && (t.review_requested || t.workflow?.completion_review));
}

// Current sort and filter state for task list
let _taskSortField = 'created_at';

let _taskSortClicks = 0; // clicks on the chosen column in a row

// clicks on the chosen column in a row
let _taskSortExplicit = false; // a column was chosen: until then open work comes before done work (#4716)

// a column was chosen: until then open work comes before done work (#4716)
let _taskSortAsc = false; // default newest first

// default newest first
let _hideCompleted = browserLocal.get('coral-hide-completed') === 'true';

export function _toggleTaskSort(field) {
    // A column's clicks: the first chooses it, the second turns it round, the third gives the default order back (open work first, #4737).
    if (_taskSortExplicit && _taskSortField === field) {
        if (++_taskSortClicks >= 3) {
            _taskSortExplicit = false; _taskSortField = 'created_at'; _taskSortAsc = false; _taskSortClicks = 0;
        } else {
            _taskSortAsc = !_taskSortAsc;
        }
    } else {
        _taskSortExplicit = true;
        _taskSortField = field;
        _taskSortAsc = field === 'priority'; // priority defaults asc (critical first)
        _taskSortClicks = 1;
    }
    renderBoardTaskList();
}

export function _toggleHideCompleted() {
    _hideCompleted = !_hideCompleted;
    browserLocal.set('coral-hide-completed', _hideCompleted);
    renderBoardTaskList();
}

/** The tasks to show: done ones dropped when hidden, in the chosen sort order. */
function _filterAndSortTasks(allTasks, querying = false) {
    const isDone = t => t.status === 'completed' || t.status === 'skipped';
    if (querying && !_taskSortExplicit) return allTasks; // the server's order (a search is not cut by Hide done)
    const tasks = allTasks.filter(t => {
        if (!querying && _hideCompleted && (t.status === 'completed' || t.status === 'skipped')) return false;
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
    // The default order puts open and active work first, then the finished tasks, newest first inside each part (a stable split): with a thousand
    // recent finished tasks the first rows were all Finished (#4716). A column the person chose sorts the whole list as it always did.
    if (!_taskSortExplicit && !querying) return [...tasks.filter(t => !isDone(t)), ...tasks.filter(isDone)];
    return tasks;
}

/** Nothing to show: hide the section, or say so for a live agent or a team (and a busy board). */
function _renderEmptyTaskList(container, section, onBoardPage) {
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
            ? '<div class="board-task-empty">No tasks. Add one with + Task.</div>'
            : '<div class="board-task-empty">No tasks. Add one with + Task.</div>');
    return;
}

/** The hide-done toggle and the section label in the header. */
function _renderTaskListChrome(section, completedCount, querying = false) {
    // Render hide-done toggle in the section header (not during a search: the status filter says what to show)
    const toggleContainer = document.getElementById('board-task-hide-toggle-container');
    if (toggleContainer) {
        morphHtml(toggleContainer, completedCount > 0 && !querying
            ? `<label class="board-task-hide-toggle"><input type="checkbox" ${_hideCompleted ? 'checked' : ''} data-on-change="task-hide-done"> Hide done (${completedCount})</label>`
            : '');
    }

    // Update section header to "Tasks" instead of "Board Tasks"
    const headerLabel = section ? section.querySelector('.board-tasks-header > span:first-child') : null;
    if (headerLabel && headerLabel.textContent !== 'Tasks') headerLabel.textContent = 'Tasks';
}

/** The table header, with the sort arrow on the column in use. */
function _taskListHeaderHtml() {
    const arrow = (field) => _taskSortExplicit && _taskSortField === field ? (_taskSortAsc ? ' ▲' : ' ▼') : ''; // the default order shows no arrow (#4737)

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
    return header;
}

/** A row's status icon and label. */
function _taskStatus(t) {
    const statusWrap = (className, detail, icon, label) => ({ className, detail, icon, label });
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
    return statusIcon;
}

/** The status cell. Not a tab stop (#5813): a focusable image is a known anti-pattern; the status is spoken as part of the row's Open button (_taskStatus(t).detail) and is its title for the mouse. */
function _taskStatusIconHtml(t) {
    const { className, detail, icon, label } = _taskStatus(t);
    return `<span class="board-task-status-wrap ${className}" title="${escapeAttr(detail)}" aria-label="${escapeAttr(detail)}" role="img">${icon}<span class="board-task-status-label">${label}</span></span>`;
}

/** A row's cost text and class: a subagent's spend as it runs, an agent's or a finished task's, or a running task's live cost. */
function _taskRowCost(t, isAgent, isSubagent) {
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
    return { costText, costClass };
}

/** The rows of the table. */
function _taskRowsHtml(tasks) {
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
        const statusIcon = _taskStatusIconHtml(t);
        const { costText, costClass } = _taskRowCost(t, isAgent, isSubagent);
        // The row is not a button (#5813): a button holding other controls hides them from some screen readers and gives the row three tab stops. Its one keyboard control is the real button in the id
        // cell (Enter or Space on it is a click, which bubbles to the row's action); a click anywhere else on the row still opens the task for the mouse. The subagent id stays in its
        // data-subagent-id attribute; the action reads it from there.
        const clickHandler = isSubagent ? ` data-on-click="task-subagent-open" style="cursor:pointer"`
            : isAgent ? ` data-on-click="task-open" data-kind="agent" data-task-id="${Number(t.id)}" style="cursor:pointer"`
            : ` data-on-click="task-open" data-kind="board" data-task-id="${Number(t.id)}" style="cursor:pointer"`;
        const openLabel = `${isSubagent ? 'Open subagent' : `Open task ${taskIdLabel}`}: ${t.title || t.description || ''}, ${_taskStatus(t).detail}`;
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
            <button type="button" class="board-task-id board-task-open" title="${isSubagent ? 'Open the subagent' : 'Task ID'}" aria-label="${escapeAttr(openLabel)}">${taskIdLabel}</button>
            <span class="board-task-priority ${priorityClass}" title="Claim priority: ${escapeAttr(t.priority || 'none')}">${t.priority ? (_priorityLabel[t.priority] || escapeHtml(t.priority)) : '\u2014'}</span>
            ${typeCell}
            <span class="board-task-assignee">${escapeHtml(assignee)}</span>
            <span class="board-task-desc"${tooltip}>${subagentBadge}${claimedBadge}${blockedBadge}${title}${isSubagent || isAgent ? '' : _verifyMark(t)}</span>
            <span class="${costClass}">${costText}</span>
            <span class="board-task-time">${timeStr}</span>
        </div>`;
    }).join('');
    return rows;
}

/** The "Load more" button of a filtered list: the next page of what matches. */
function _filteredMoreHtml() {
    if (!_filteredHasMore()) return '';
    const left = _filteredList.total !== null ? Math.max(0, _filteredList.total - _filteredList.rows.length) : 0;
    return `<div class="board-task-more"><button type="button" class="btn board-task-load-more" data-on-click="task-load-more"${_filteredList.busy ? ' aria-busy="true"' : ''}>Load more tasks${left ? ` (${left} not shown)` : ''}</button></div>`;
}

/** The "Load more done tasks" button while older done tasks are not loaded. */
function _taskLoadMoreHtml(boardDoneTotal, boardDoneLoaded) {
    // Older done tasks come a page at a time (#3269).
    const notLoaded = Math.max(0, boardDoneTotal - boardDoneLoaded);
    const more = _boardList.nextCursor && !_hideCompleted
        ? `<div class="board-task-more"><button type="button" class="btn board-task-load-more" data-on-click="task-load-more">Load more done tasks${notLoaded ? ` (${notLoaded} not shown)` : ''}</button></div>`
        : '';
    return more;
}

/** The badge: done / all, from the server's totals for the board. */
function _updateTaskCount(boardTasks, otherTasks, completedCount) {
    const countEl = document.getElementById('task-bar-count');
    if (countEl) {
        const boardTotal = _boardList.totalOpen !== null && _boardList.totalCompleted !== null
            ? _boardList.totalOpen + _boardList.totalCompleted : boardTasks.length;
        const total = boardTotal + otherTasks.length;
        const countText = total > 0 ? `${completedCount}/${total}` : '';
        if (countEl.textContent !== countText) countEl.textContent = countText; // same text: no new text node, no layout
    }
}

export function renderBoardTaskList() {
    const container = document.getElementById('board-task-list');
    if (!container) return;

    // Merge board tasks and agent tasks into a unified list
    const querying = _queryActive();
    const boardTasks = (querying ? _filteredList.rows : state.currentBoardTasks || []).map(t => ({ ...t, _source: 'board' }));
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

    const allTasks = querying ? boardTasks : [...boardTasks, ...agentTasks, ...subagentTasks]; // a search is about the board's tasks
    const isDone = t => t.status === 'completed' || t.status === 'skipped';
    const otherTasks = querying ? [] : [...agentTasks, ...subagentTasks];
    // The board's done tasks are counted by the server: only a page of them is loaded (#3269).
    const boardDoneLoaded = boardTasks.filter(isDone).length;
    const boardDoneTotal = _boardList.totalCompleted ?? boardDoneLoaded;
    const completedCount = boardDoneTotal + otherTasks.filter(isDone).length;
    const tasks = _filterAndSortTasks(allTasks, querying);
    const section = document.getElementById('board-tasks-section');
    const hasBoard = !!_boardList.board; // the bar belongs to the board whose tasks are listed
    // The bar's counts: what matches of what the board has (the server's totals); with nothing narrowing it, the board's own total.
    const boardAll = _boardList.totalOpen !== null && _boardList.totalCompleted !== null ? _boardList.totalOpen + _boardList.totalCompleted : boardTasks.length;
    const barCounts = querying ? { matched: _filteredList.total ?? boardTasks.length, all: Math.max(boardAll, _filteredList.total ?? 0), truncated: _filteredList.truncated } : { matched: boardAll, all: boardAll };

    if (querying && allTasks.length === 0 && section) {
        // Nothing matches (or the search is still on its way): the bar stays, so the search can be changed.
        section.style.display = '';
        _renderTaskListChrome(section, 0, true);
        _syncTaskBar(true, barCounts);
        morphHtml(container, `<div class="board-task-empty" role="status">${escapeHtml(_filteredList.error || (_filteredList.busy ? 'Searching tasks…' : 'No tasks match this search.'))}</div>`);
        return;
    }
    if (allTasks.length === 0) {
        _syncTaskBar(false);
        _renderEmptyTaskList(container, section, onBoardPage);
        return;
    }
    if (section) section.style.display = '';

    // Render hide-done toggle in the section header
    _renderTaskListChrome(section, completedCount, querying);
    _syncTaskBar(hasBoard, barCounts);

    const header = _taskListHeaderHtml();

    const rows = _taskRowsHtml(tasks);

    // Older done tasks come a page at a time (#3269).
    const more = querying ? _filteredMoreHtml() : _taskLoadMoreHtml(boardDoneTotal, boardDoneLoaded);
    // Unchanged rows keep their nodes: the list was rebuilt on every poll, a layout each time (#3881).
    morphHtml(container, header + rows + more);

    // Update task count badge: done / all; the board's numbers are the server's totals.
    _updateTaskCount(boardTasks, otherTasks, completedCount);
}
