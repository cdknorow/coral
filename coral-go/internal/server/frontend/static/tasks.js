/* Agent task bar — CRUD, rendering, drag reorder */

import { state } from './state.js';
import { escapeHtml, escapeAttr, showToast, renderMarkdown, DOMPURIFY_CONFIG } from './utils.js';
import { addPendingMessage } from './live_chat.js';

// ── Board task polling ───────────────────────────────────────────────
let _boardTaskPollTimer = null;
// Live cost cache: taskID → { cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, request_count }
let _liveCosts = {};

export function startBoardTaskPoll() {
    stopBoardTaskPoll();
    // Load agent tasks + board tasks immediately, then poll board tasks every 10s
    if (state.currentSession && state.currentSession.type === 'live') {
        loadAgentTasks(state.currentSession.name, state.currentSession.session_id);
    }
    // Subagents are loaded by the poll below, every tick: their status and
    // spend keep changing while they run, with no user action to trigger a reload.
    _pollBoardTasksOnce();
    _boardTaskPollTimer = setInterval(_pollBoardTasksOnce, 10000);
}

export function stopBoardTaskPoll() {
    if (_boardTaskPollTimer) {
        clearInterval(_boardTaskPollTimer);
        _boardTaskPollTimer = null;
    }
}

async function _pollBoardTasksOnce() {
    if (!state.currentSession || state.currentSession.type !== 'live') return;
    loadSubagents(state.currentSession.name, state.currentSession.session_id);
    const boardProject = state.currentSession.board_project || state.currentSession.name;
    if (boardProject) {
        await loadBoardTasks(boardProject);
        _fetchLiveCosts(boardProject);
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
            const resp = await fetch(`/api/board/${encodeURIComponent(boardProject)}/tasks/${t.id}/cost`);
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
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(agentName)}/tasks${qs}`, options);
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

// Which session state.currentSubagents belongs to. Lets a session switch drop
// the previous agent's subagents at once instead of showing them until the
// new fetch returns, and lets a slow response for an old session be ignored.
let _subagentsSessionId = null;

export async function loadSubagents(agentName, sessionId, options) {
    const sid = sessionId || (state.currentSession && state.currentSession.session_id);
    if (!agentName || !sid) {
        _subagentsSessionId = null;
        state.currentSubagents = [];
        renderBoardTaskList();
        return;
    }
    if (sid !== _subagentsSessionId) {
        _subagentsSessionId = sid;
        state.currentSubagents = [];
        renderBoardTaskList();
    }
    let subagents = [];
    try {
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(agentName)}/subagents?session_id=${encodeURIComponent(sid)}`, options);
        if (!resp.ok) throw new Error(`subagents fetch failed: ${resp.status}`);
        subagents = await resp.json();
    } catch (e) {
        if (e?.name === 'AbortError') return;
        subagents = [];
    }
    if (sid !== _subagentsSessionId) return; // the user moved to another session meanwhile
    state.currentSubagents = Array.isArray(subagents) ? subagents : [];
    renderBoardTaskList();
    _refreshOpenSubagentModal();
}

/** Shape a subagent like a task row so it can share the unified task table. */
export function subagentToTaskRow(sa, mainAgentName) {
    const type = sa.subagent_type || '';
    return {
        ...sa,
        _source: 'subagent',
        id: `subagent-${sa.id}`,
        title: sa.description || (type ? `${type} subagent` : 'Subagent'),
        status: sa.status || (sa.finished ? 'completed' : 'in_progress'),
        priority: null,
        // A subagent works on behalf of the main agent that launched it.
        assigned_to: mainAgentName || null,
        created_at: sa.started_at || sa.created_at,
    };
}

function _subagentTooltip(t) {
    const parts = ['Subagent' + (t.subagent_type ? ` (${t.subagent_type})` : '')];
    if (t.model) parts.push(t.model);
    if (t.api_calls) parts.push(`${t.api_calls} API call${t.api_calls === 1 ? '' : 's'}`);
    const tokens = (t.input_tokens || 0) + (t.output_tokens || 0) + (t.cache_read_tokens || 0) + (t.cache_write_tokens || 0);
    if (tokens > 0) parts.push(`${_formatTokenCount(tokens)} tokens`);
    return parts.join(' \u00b7 ');
}

/* ── Subagent detail modal ──────────────────────────────────
   Shares the task detail overlay. Stats come from the list already in state;
   the prompt and result are fetched on open, since they are read from the
   subagent's transcript on demand. */

// subagent_id shown in the modal, or null when it is closed or showing a task.
let _openSubagentId = null;
// Conversation for the open modal: { key, finished, data } where data is the
// detail response, or null when the fetch failed.
let _subagentConversation = null;

// endIso === null means "still running": measure up to now. A missing end for
// something that has stopped yields no duration rather than one that keeps
// growing against the clock.
function _formatDuration(startIso, endIso) {
    if (endIso === undefined || endIso === '') return null;
    const start = Date.parse(startIso), end = endIso === null ? Date.now() : Date.parse(endIso);
    if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) return null;
    const secs = Math.round((end - start) / 1000);
    if (secs < 60) return `${secs}s`;
    const mins = Math.floor(secs / 60);
    if (mins < 60) return `${mins}m ${secs % 60}s`;
    return `${Math.floor(mins / 60)}h ${mins % 60}m`;
}

// How long an in-progress board task has been claimed, e.g. "42m" or "3h 5m".
function _claimedFor(task) {
    if (task.status !== 'in_progress' || !task.claimed_at) return null;
    const ms = Date.now() - Date.parse(task.claimed_at);
    if (!Number.isFinite(ms) || ms < 0) return null;
    const mins = Math.floor(ms / 60000);
    if (mins < 1) return 'just now';
    if (mins < 60) return `${mins}m`;
    const hours = Math.floor(mins / 60);
    if (hours < 24) return `${hours}h ${mins % 60}m`;
    return `${Math.floor(hours / 24)}d ${hours % 24}h`;
}

// Prompt and result are model-written and almost always markdown. They are
// also untrusted, so this fails closed: without the sanitizer the text is
// shown escaped rather than rendered. breaks:true keeps single newlines,
// which prompts use for "Key: value" style lines.
function _subagentMarkdown(text) {
    if (typeof marked === 'undefined' || typeof DOMPurify === 'undefined') {
        return `<div class="task-detail-body subagent-detail-text">${escapeHtml(text)}</div>`;
    }
    // renderMarkdown has already sanitized against script execution. This second
    // pass removes anything that loads or embeds a resource. The text can be
    // steered by whatever the subagent read, and an <img> with a remote URL
    // makes the browser call out the moment the modal opens: a silent way to
    // leak data through the URL. A report has no need for images.
    const html = DOMPurify.sanitize(renderMarkdown(text, { breaks: true, gfm: true }), {
        ALLOWED_URI_REGEXP: DOMPURIFY_CONFIG.ALLOWED_URI_REGEXP,
        FORBID_TAGS: ['img', 'picture', 'source', 'video', 'audio', 'track', 'svg', 'math', 'style', 'link', 'form', 'input', 'button', 'textarea', 'select'],
        FORBID_ATTR: ['style', 'srcset', 'background', 'poster'],
    });
    return `<div class="task-detail-body subagent-detail-text subagent-detail-md">${html}</div>`;
}

function _subagentConversationHtml(sa) {
    const block = (label, inner) => `<div class="task-detail-section"><div class="task-detail-label">${label}</div>${inner}</div>`;
    const note = (text) => `<div class="subagent-detail-note">${escapeHtml(text)}</div>`;
    const c = _subagentConversation;
    if (!c || c.key !== `${sa.session_id}:${sa.subagent_id}`) {
        return block('Prompt', note('Loading\u2026'));
    }
    if (!c.data || !c.data.conversation_available) {
        return block('Prompt', note('The transcript for this subagent is no longer available.'));
    }
    const text = _subagentMarkdown;
    let html = block('Prompt', c.data.prompt ? text(c.data.prompt) : note('No prompt recorded.'));
    html += block('Result', c.data.result ? text(c.data.result)
        : note(sa.status === 'in_progress' ? 'Still working \u2014 no result yet.' : 'No final answer was recorded.'));
    if (c.data.truncated) html += note('Long text was shortened for display.');
    return html;
}

function _renderSubagentDetail(sa) {
    const content = document.getElementById('task-detail-content');
    const titleEl = document.getElementById('task-detail-modal-title');
    if (!content) return;
    if (titleEl) titleEl.textContent = 'Subagent';

    const row = subagentToTaskRow(sa, state.currentSession ? (state.currentSession.display_name || state.currentSession.name) : '');
    const statusLabel = row.status === 'completed' ? 'Completed' : row.status === 'in_progress' ? 'In Progress' : 'Stopped';
    const statusClass = row.status === 'completed' ? 'task-detail-status-completed'
        : row.status === 'in_progress' ? 'task-detail-status-inprogress' : 'task-detail-status-cancelled';
    const running = row.status === 'in_progress';
    const field = (label, value, extraClass = '') => value == null || value === '' ? '' : `
        <div class="task-detail-field">
            <span class="task-detail-label">${label}</span>
            <span class="task-detail-value${extraClass}">${value}</span>
        </div>`;
    const duration = _formatDuration(sa.started_at, running ? null : sa.last_activity_at);

    let html = `
        <div class="task-detail-title">${escapeHtml(row.title)}</div>
        <div class="task-detail-meta">
            <span class="task-detail-status ${statusClass}">${statusLabel}</span>
            <span class="board-task-type board-task-type-subagent subagent-detail-chip"><span class="material-icons">account_tree</span>subagent</span>
            ${sa.subagent_type ? `<span class="board-task-subagent-badge">${escapeHtml(sa.subagent_type)}</span>` : ''}
        </div>
        <div class="task-detail-fields subagent-detail-fields">
            ${field('Launched By', escapeHtml(row.assigned_to || '\u2014'))}
            ${field('Model', sa.model ? escapeHtml(sa.model) : '')}
            ${field('Started', sa.started_at ? formatTaskDate(sa.started_at) : '')}
            ${field(running ? 'Last Activity' : 'Finished', sa.last_activity_at ? formatTaskDate(sa.last_activity_at) : '')}
            ${field(running ? 'Running For' : 'Duration', duration || '')}
            ${field('API Calls', sa.api_calls ? String(sa.api_calls) : '')}
            ${field('Subagent ID', escapeHtml(sa.subagent_id || ''), ' subagent-detail-mono')}
        </div>`;

    const warningClass = sa.cost_usd >= 1.0 ? ' board-task-cost-warning' : '';
    const tok = (label, n) => `<div class="task-detail-token-item"><span class="task-detail-token-label">${label}</span><span class="task-detail-token-value">${_formatTokenCount(n || 0)}</span></div>`;
    html += `<div class="task-detail-section">
            <div class="task-detail-label">${running ? 'Cost So Far' : 'Cost'}</div>
            <div class="task-detail-cost-summary${running ? ' board-task-cost-live' : ''}${warningClass}">${running ? '~' : ''}${_formatCost(sa.cost_usd || 0, true)}</div>
            <div class="task-detail-tokens">
                ${tok('Input', sa.input_tokens)}${tok('Output', sa.output_tokens)}${tok('Cache Read', sa.cache_read_tokens)}${tok('Cache Write', sa.cache_write_tokens)}
            </div>
        </div>`;

    html += _subagentConversationHtml(row);

    // This runs on every poll while the modal is open. Replacing the DOM resets
    // the scroll position of the text boxes, so do nothing when the output is
    // unchanged (always the case once a subagent has finished), and otherwise
    // put the reader back where they were.
    if (content._subagentHtml === html && content.dataset.subagentId === sa.subagent_id) return;
    const sameSubagent = content.dataset.subagentId === sa.subagent_id;
    const scrollOf = (root) => Array.from(root.querySelectorAll('.subagent-detail-text')).map(el => el.scrollTop);
    const body = content.closest('.modal-body');
    const saved = sameSubagent ? { boxes: scrollOf(content), body: body ? body.scrollTop : 0 } : null;

    content.innerHTML = html;
    content._subagentHtml = html;
    content.dataset.subagentId = sa.subagent_id;

    // A link in a report must not navigate the dashboard away.
    content.querySelectorAll('.subagent-detail-md a[href]').forEach(a => {
        a.setAttribute('target', '_blank');
        a.setAttribute('rel', 'noopener noreferrer');
    });
    if (saved) {
        content.querySelectorAll('.subagent-detail-text').forEach((el, i) => { el.scrollTop = saved.boxes[i] || 0; });
        if (body) body.scrollTop = saved.body;
    }
}

async function _loadSubagentConversation(sa) {
    const key = `${sa.session_id}:${sa.subagent_id}`;
    let data = null;
    try {
        const name = state.currentSession ? state.currentSession.name : '_';
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(name)}/subagents/${encodeURIComponent(sa.subagent_id)}?session_id=${encodeURIComponent(sa.session_id)}`);
        if (resp.ok) data = await resp.json();
    } catch (e) { /* rendered as unavailable */ }
    if (_openSubagentId !== sa.subagent_id) return; // closed or switched while loading
    _subagentConversation = { key, finished: !!sa.finished, data };
    const current = (state.currentSubagents || []).find(s => s.subagent_id === sa.subagent_id) || sa;
    _renderSubagentDetail(current);
}

export function showSubagentDetailModal(subagentId) {
    const sa = (state.currentSubagents || []).find(s => s.subagent_id === subagentId);
    const modal = document.getElementById('task-detail-modal');
    if (!sa || !modal) return;

    _openSubagentId = sa.subagent_id;
    _subagentConversation = null;
    _renderSubagentDetail(sa);
    _loadSubagentConversation(sa);

    // The footer is shared with board tasks, which put action buttons in it.
    const footer = document.getElementById('task-detail-modal-footer');
    if (footer) footer.innerHTML = `<button class="btn" onclick="window.hideTaskDetailModal()">Close</button>`;

    modal.style.display = '';
    modal.onclick = (e) => { if (e.target === modal) hideTaskDetailModal(); };
    if (modal._escHandler) document.removeEventListener('keydown', modal._escHandler);
    modal._escHandler = (e) => { if (e.key === 'Escape') hideTaskDetailModal(); };
    document.addEventListener('keydown', modal._escHandler);
}
window.showSubagentDetailModal = showSubagentDetailModal;

// Called after each subagent poll: keep an open modal's stats current, and
// fetch the result once a running subagent finishes.
function _refreshOpenSubagentModal() {
    if (!_openSubagentId) return;
    const sa = (state.currentSubagents || []).find(s => s.subagent_id === _openSubagentId);
    if (!sa) return; // no longer listed (session switched); leave the modal as it is
    _renderSubagentDetail(sa);
    if (_subagentConversation && _subagentConversation.finished !== !!sa.finished) {
        _loadSubagentConversation(sa);
    }
}

export async function addAgentTask() {
    if (!state.currentSession || state.currentSession.type !== 'live') return;
    const input = document.getElementById('task-bar-input');
    const title = input.value.trim();
    if (!title) return;

    try {
        const sid = state.currentSession.session_id;
        await fetch(`/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/tasks`, {
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
        const response = await fetch(`/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/tasks/${taskId}`, {
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
        const response = await fetch(`/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/tasks/${taskId}`, {
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
            await fetch(`/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/tasks/${taskId}`, {
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

function initTaskDragReorder() {
    const list = document.getElementById('task-bar-list');
    if (!list) return;

    let dragItem = null;

    list.querySelectorAll('.task-item').forEach(item => {
        item.addEventListener('dragstart', (e) => {
            dragItem = item;
            item.classList.add('dragging');
            e.dataTransfer.effectAllowed = 'move';
        });

        item.addEventListener('dragend', () => {
            item.classList.remove('dragging');
            dragItem = null;
            // Save new order
            saveTaskOrder();
        });

        item.addEventListener('dragover', (e) => {
            e.preventDefault();
            e.dataTransfer.dropEffect = 'move';
            if (!dragItem || dragItem === item) return;

            const rect = item.getBoundingClientRect();
            const midY = rect.top + rect.height / 2;
            if (e.clientY < midY) {
                list.insertBefore(dragItem, item);
            } else {
                list.insertBefore(dragItem, item.nextSibling);
            }
        });
    });
}

/* ── Board Tasks ────────────────────────────────────────── */

export async function loadBoardTasks(boardName) {
    if (!boardName) {
        state.currentBoardTasks = [];
        renderBoardTaskList();
        return;
    }
    try {
        const resp = await fetch(`/api/board/${encodeURIComponent(boardName)}/tasks`);
        if (!resp.ok) throw new Error(`board tasks fetch failed: ${resp.status}`);
        const data = await resp.json();
        state.currentBoardTasks = data.tasks || [];
    } catch (e) {
        state.currentBoardTasks = [];
    }
    renderBoardTaskList();
}

// Current sort and filter state for task list
let _taskSortField = 'created_at';
let _taskSortAsc = false; // default newest first
let _hideCompleted = localStorage.getItem('coral-hide-completed') === 'true'; // off by default, persisted

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
    localStorage.setItem('coral-hide-completed', _hideCompleted);
    renderBoardTaskList();
}

// Expose globally
window._toggleTaskSort = _toggleTaskSort;
window._toggleHideCompleted = _toggleHideCompleted;

const _priorityOrder = { critical: 0, high: 1, medium: 2, low: 3 };
const _priorityRank = { critical: 1, high: 2, medium: 3, low: 4 };

function _formatCost(usd, precise) {
    if (usd == null) return '$0.00';
    if (usd === 0) return '$0.00';
    if (precise) {
        // Detail view: up to 4 decimal places, trim trailing zeros (keep at least 2)
        const s = usd.toFixed(4);
        return '$' + s.replace(/0{1,2}$/, '');
    }
    // Badge: show 2 decimals, but use <$0.01 for sub-cent costs
    if (usd > 0 && usd < 0.01) return '<$0.01';
    return '$' + usd.toFixed(2);
}

function _formatTokenCount(n) {
    if (n == null) return '0';
    if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M';
    if (n >= 1000) return (n / 1000).toFixed(1) + 'k';
    return n.toLocaleString();
}

function _formatTaskTime(ts) {
    if (!ts) return '';
    try {
        const d = new Date(ts);
        return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
            + ' ' + d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
    } catch { return ''; }
}

function _agentTaskStatus(t) {
    if (t.status) return t.status;
    return t.completed === 1 ? 'completed' : t.completed === 2 ? 'in_progress' : t.completed === 3 ? 'skipped' : 'pending';
}

export function renderBoardTaskList() {
    const container = document.getElementById('board-task-list');
    if (!container) return;

    // Merge board tasks and agent tasks into a unified list
    const boardTasks = (state.currentBoardTasks || []).map(t => ({ ...t, _source: 'board' }));
    const agentDisplayName = state.currentSession ? (state.currentSession.display_name || state.currentSession.name) : '';
    const agentTasks = (state.currentAgentTasks || []).map(t => ({
        ...t,
        _source: 'agent',
        // Normalize agent task fields to match board task shape
        status: _agentTaskStatus(t),
        priority: t.priority || null,
        assigned_to: t.display_name || t.agent_name || agentDisplayName || null,
        created_at: t.created_at,
    }));

    const subagentTasks = (state.currentSubagents || []).map(sa => subagentToTaskRow(sa, agentDisplayName));

    const allTasks = [...boardTasks, ...agentTasks, ...subagentTasks];
    const completedCount = allTasks.filter(t => t.status === 'completed' || t.status === 'skipped').length;
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
        if (countEl) countEl.textContent = '';
        const live = state.currentSession && state.currentSession.type === 'live';
        if (!live) {
            if (section) section.style.display = 'none';
            container.innerHTML = '';
            return;
        }
        // A live agent always gets the section, so "+ Task" is reachable
        if (section) section.style.display = '';
        const headerLabel = section ? section.querySelector('.board-tasks-header > span:first-child') : null;
        if (headerLabel) headerLabel.textContent = 'Tasks';
        const toggle = document.getElementById('board-task-hide-toggle-container');
        if (toggle) toggle.innerHTML = '';
        container.innerHTML = '<div class="board-task-empty">No tasks yet. Use + Task to give this agent something to work on.</div>';
        return;
    }
    if (section) section.style.display = '';

    const arrow = (field) => _taskSortField === field ? (_taskSortAsc ? ' ▲' : ' ▼') : '';

    // Render hide-done toggle in the section header
    const toggleContainer = document.getElementById('board-task-hide-toggle-container');
    if (toggleContainer) {
        toggleContainer.innerHTML = completedCount > 0
            ? `<label class="board-task-hide-toggle"><input type="checkbox" ${_hideCompleted ? 'checked' : ''} onchange="_toggleHideCompleted()"> Hide done (${completedCount})</label>`
            : '';
    }

    // Update section header to "Tasks" instead of "Board Tasks"
    const headerLabel = section ? section.querySelector('.board-tasks-header > span:first-child') : null;
    if (headerLabel) headerLabel.textContent = 'Tasks';

    const header = `
        <div class="board-task-item board-task-header">
            <span class="board-task-status-col">Status</span>
            <span class="board-task-id board-task-sort" onclick="_toggleTaskSort('id')">ID${arrow('id')}</span>
            <span class="board-task-priority board-task-sort" onclick="_toggleTaskSort('priority')">Priority${arrow('priority')}</span>
            <span class="board-task-type board-task-sort" onclick="_toggleTaskSort('type')">Type${arrow('type')}</span>
            <span class="board-task-assignee board-task-sort" onclick="_toggleTaskSort('assignee')">Agent${arrow('assignee')}</span>
            <span class="board-task-desc board-task-sort" onclick="_toggleTaskSort('created_at')">Task${arrow('created_at')}</span>
            <span class="board-task-cost board-task-sort" onclick="_toggleTaskSort('cost')">Cost${arrow('cost')}</span>
            <span class="board-task-time board-task-sort" onclick="_toggleTaskSort('created_at')">Created${arrow('created_at')}</span>
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
            : t.body ? ` title="${escapeAttr(t.body)}"` : '';
        const timeStr = _formatTaskTime(t.created_at);
        const statusWrap = (className, detail, icon, label) =>
            `<span class="board-task-status-wrap ${className}" title="${escapeAttr(detail)}" aria-label="${escapeAttr(detail)}" role="img" tabindex="0">${icon}<span class="board-task-status-label">${label}</span></span>`;
        const activelyClaimed = t.status === 'in_progress' && Boolean(t.claimed_at);
        const workingDetail = activelyClaimed ? 'Claimed · Working' : 'Open · Working';
        const statusIcon = t.workflow?.outcome === 'failed'
            ? statusWrap('completed', 'Finished', '<span class="material-icons board-task-status-icon completed" aria-hidden="true">check_circle</span>', 'Finished')
            : t.status === 'completed'
            ? statusWrap('completed', 'Finished', '<span class="material-icons board-task-status-icon completed" aria-hidden="true">check_circle</span>', 'Finished')
            : t.status === 'review_pending'
            ? statusWrap('blocked', 'Open · Review pending', '<span class="material-icons board-task-status-icon blocked" aria-hidden="true">rate_review</span>', 'Open <b>· Review pending</b>')
            : t.workflow?.completion_review && t.status === 'in_progress'
            ? statusWrap('blocked', 'Open · Review requested', '<span class="material-icons board-task-status-icon blocked" aria-hidden="true">rate_review</span>', 'Open <b>· Review requested</b>')
            : t.status === 'in_progress'
            ? statusWrap('in-progress', workingDetail, '<span class="task-spinner" aria-hidden="true"></span>', activelyClaimed ? 'Claimed' : 'Open')
            : t.status === 'skipped'
            ? statusWrap('cancelled', 'Cancelled', '<span class="material-icons board-task-status-icon cancelled" aria-hidden="true">cancel</span>', 'Cancelled')
            : t.status === 'blocked'
            ? statusWrap('blocked', 'Open · Blocked: waiting for prerequisites', '<span class="material-icons board-task-status-icon blocked" aria-hidden="true">hourglass_empty</span>', 'Open')
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
        // The subagent id is read from the data attribute rather than inlined
        // into the handler, so it never has to be escaped as a JS string.
        const clickHandler = isSubagent ? ` onclick="showSubagentDetailModal(this.dataset.subagentId)" style="cursor:pointer"`
            : isAgent ? ` onclick="showAgentTaskDetailModal(${t.id})" style="cursor:pointer"`
            : ` onclick="showTaskDetailModal(${t.id})" style="cursor:pointer"`;
        const typeCell = isSubagent
            ? `<span class="board-task-type board-task-type-subagent" title="Subagent launched by ${escapeAttr(assignee)}"><span class="material-icons">account_tree</span>sub</span>`
            : `<span class="board-task-type">${isAgent ? 'agent' : 'board'}</span>`;
        const subagentBadge = isSubagent && t.subagent_type
            ? `<span class="board-task-subagent-badge">${escapeHtml(t.subagent_type)}</span>` : '';
        const claimedFor = !isAgent && !isSubagent ? _claimedFor(t) : null;
        const claimedBadge = claimedFor
            ? `<span class="board-task-claimed" title="Claimed ${escapeAttr(formatTaskDate(t.claimed_at))}"><span class="material-icons">schedule</span>${claimedFor}</span>` : '';
        // Blocked is conveyed by the status icon and its focusable detail;
        // avoid repeating a red text badge in the task description column.
        const blockedBadge = '';
        return `
        <div class="board-task-item ${statusClass}${isSubagent ? ' board-task-subagent' : ''}"${clickHandler}${isSubagent ? ` data-subagent-id="${escapeAttr(t.subagent_id || '')}"` : ''}>
            ${statusIcon}
            <span class="board-task-id" title="Task ID">${taskIdLabel}</span>
            <span class="board-task-priority ${priorityClass}" title="Claim priority: ${escapeAttr(t.priority || 'none')}">${t.priority ? (_priorityRank[t.priority] || '\u2014') : '\u2014'}</span>
            ${typeCell}
            <span class="board-task-assignee">${escapeHtml(assignee)}</span>
            <span class="board-task-desc"${tooltip}>${subagentBadge}${claimedBadge}${blockedBadge}${title}</span>
            <span class="${costClass}">${costText}</span>
            <span class="board-task-time">${timeStr}</span>
        </div>`;
    }).join('');

    container.innerHTML = header + rows;

    // Update task count badge
    const countEl = document.getElementById('task-bar-count');
    if (countEl) {
        const doneCount = allTasks.filter(t => t.status === 'completed' || t.status === 'skipped').length;
        countEl.textContent = allTasks.length > 0 ? `${doneCount}/${allTasks.length}` : '';
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

    let html = `<div class="dep-picker-selected" id="${containerId}-tags"></div>`;
    html += `<div class="dep-picker-toggle"><a class="dep-picker-add-link" onclick="document.getElementById('${containerId}-list').style.display = document.getElementById('${containerId}-list').style.display === 'none' ? '' : 'none'">+ Add dependency</a></div>`;
    html += `<div class="dep-picker-list" id="${containerId}-list" style="display:none">`;
    if (tasks.length === 0) {
        html += `<div class="dep-picker-empty">No eligible tasks</div>`;
    } else {
        tasks.forEach(t => {
            const checked = selected.has(t.id) ? ' checked' : '';
            html += `<label class="dep-picker-item"><input type="checkbox" value="${t.id}"${checked} onchange="window._updateDepTags('${containerId}')"> #${t.id} — ${escapeHtml(t.title || '')}</label>`;
        });
    }
    html += `</div>`;
    container.innerHTML = html;
    window._updateDepTags(containerId);
}

window._updateDepTags = function(containerId) {
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
        return `<span class="dep-tag">${escapeHtml(label)} <a onclick="document.querySelector('#${containerId} input[value=\\'${cb.value}\\']').click()">&times;</a></span>`;
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
    const modal = document.getElementById('create-task-modal');
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
            const resp = await fetch(`/api/board/${encodeURIComponent(boardProject)}/subscribers`);
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

    modal.onclick = (e) => { if (e.target === modal) hideCreateTaskModal(); };
    modal._escHandler = (e) => { if (e.key === 'Escape') hideCreateTaskModal(); };
    document.addEventListener('keydown', modal._escHandler);
}

export function hideCreateTaskModal() {
    const modal = document.getElementById('create-task-modal');
    if (!modal) return;
    modal.style.display = 'none';
    if (modal._escHandler) {
        document.removeEventListener('keydown', modal._escHandler);
        modal._escHandler = null;
    }
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
        const resp = await fetch(`/api/board/${encodeURIComponent(boardProject)}/tasks`, {
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
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(session.name)}/tasks`, {
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
    return !!state.currentSession && state.currentSession.type === 'live' && !state.currentSession.board_project;
}

function _getBoardProject() {
    if (!state.currentSession) return null;
    return state.currentSession.board_project || state.currentSession.name;
}

/* ── Task Detail Modal ─────────────────────────────────── */

export function showTaskDetailModal(taskId) {
    ++_editGeneration;
    _editOriginalTask = null;
    _openSubagentId = null; // the modal is shared; it now shows a board task
    const sharedContent = document.getElementById('task-detail-content');
    if (sharedContent) { sharedContent._subagentHtml = null; delete sharedContent.dataset.subagentId; }
    const tasks = state.currentBoardTasks || [];
    const task = tasks.find(t => t.id === taskId);
    if (!task) return;

    const modal = document.getElementById('task-detail-modal');
    const titleEl = document.getElementById('task-detail-modal-title');
    const content = document.getElementById('task-detail-content');
    if (!modal || !content) return;

    titleEl.textContent = `Task #${task.id}`;
    content.innerHTML = _taskDetailHtml(task, task.status === 'in_progress' ? _liveCosts[task.id] : null);

    // Update footer with action buttons for editable tasks
    const footer = document.getElementById('task-detail-modal-footer');
    if (footer) {
        const isEditable = !task.workflow?.completion_review && (task.status === 'pending' || task.status === 'in_progress' || task.status === 'blocked' || task.status === 'draft');
        if (isEditable) {
            const canComplete = task.status !== 'blocked' && task.status !== 'draft';
            const showPublish = task.status === 'draft';
            const canNudge = (task.status === 'pending' || task.status === 'in_progress') && !!task.assigned_to;
            footer.innerHTML = `
                <button class="btn btn-danger-text" onclick="window.cancelBoardTask(${task.id})">Cancel Task</button>
                <span style="flex:1"></span>
                <button class="btn" onclick="window.hideTaskDetailModal()">Close</button>
                ${canNudge ? `<button class="btn" onclick="window.nudgeBoardTask(${task.id})" title="Remind ${escapeAttr(task.assigned_to)} about this task in their terminal">Nudge</button><button class="btn" onclick="window.remindBoardTask(${task.id})" title="Send periodic reminders to ${escapeAttr(task.assigned_to)}">Remind</button><button class="btn" onclick="window.stopBoardTaskReminder(${task.id})" title="Stop periodic reminders">Stop reminders</button>` : ''}
                <button class="btn" onclick="window.enableTaskEditMode(${task.id})">Edit</button>
                ${showPublish ? `<button class="btn btn-primary" onclick="window.publishBoardTask(${task.id})">Publish</button>` : ''}
                ${canComplete ? `<button class="btn btn-success" onclick="window.completeBoardTask(${task.id})">Complete</button>` : ''}`;
        } else {
            footer.innerHTML = `<button class="btn" onclick="window.hideTaskDetailModal()">Close</button>`;
        }
    }

    _openTaskDetailModal(modal);
}

// Tasks created for an agent that is not on a board. They share the board
// task modal but not its actions: no edit, dependencies or cancel.
export function showAgentTaskDetailModal(taskId) {
    _openSubagentId = null;
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
            <button class="btn" onclick="window.hideTaskDetailModal()">Close</button>
            ${open ? `<button class="btn btn-success" onclick="window.completeBoardTask(${task.id}, true)">Complete</button>` : ''}`;
    }

    _openTaskDetailModal(modal);
}
window.showAgentTaskDetailModal = showAgentTaskDetailModal;

function _openTaskDetailModal(modal) {
    modal.style.display = '';
    // Close on backdrop click
    modal.onclick = (e) => { if (e.target === modal) hideTaskDetailModal(); };
    // Close on Escape
    if (modal._escHandler) document.removeEventListener('keydown', modal._escHandler);
    modal._escHandler = (e) => { if (e.key === 'Escape') hideTaskDetailModal(); };
    document.addEventListener('keydown', modal._escHandler);
}

function _taskDetailHtml(task, liveCost) {
    const statusLabel = task.workflow?.outcome === 'failed' ? 'Finished' : task.status === 'completed' ? 'Finished'
        : task.status === 'review_pending' ? 'Open · Review pending (slot released)'
        : task.workflow?.completion_review && task.status === 'in_progress' ? 'Open · Completion review requested (slot occupied)'
        : task.status === 'in_progress' ? 'Open · In Progress'
        : task.status === 'skipped' ? 'Cancelled'
        : task.status === 'blocked' ? 'Open · Blocked'
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
            <span class="task-detail-meta-item"><span class="task-detail-meta-label">Priority</span><span class="board-task-priority ${priorityClass}" title="Claim priority: ${escapeAttr(task.priority || 'none')}">${_priorityRank[task.priority] || '\u2014'}</span></span>
            ${task.workflow?.outcome === 'failed' ? '<span class="task-detail-meta-item"><span class="task-detail-meta-label">Result</span><span class="task-detail-outcome-failed">Failed</span></span>' : ''}
        </div>`;

    const revision = Number(task.revision || 1);
    const amendments = task.workflow?.amendments || [];
    const preview = value => {
        const text = String(value || '').replace(/\s+/g, ' ').trim();
        return text.length > 180 ? `${text.slice(0, 177)}…` : text || '(empty)';
    };
    html += `<div class="task-detail-section task-detail-revision" data-task-revision="${revision}">
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
            const href = coralMatch ? `/api/artifacts/${coralMatch[1]}` : uri;
            const uriDisplay = (/^https?:\/\//i.test(uri) || coralMatch)
                ? `<a href="${coralMatch ? '#' : escapeAttr(href)}" data-artifact-uri="${escapeAttr(uri)}"${coralMatch ? ` onclick="event.preventDefault(); window.hideTaskDetailModal?.(); window.openFilePreview?.('${escapeAttr(uri)}')"` : ' target="_blank" rel="noopener noreferrer"'}>${escapeHtml(uri)}</a>`
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
            const isCrossBoard = task._source !== 'agent' && dep.board_id && dep.board_id !== boardProject;
            const boardPrefix = isCrossBoard ? `${escapeHtml(dep.board_id)} ` : '';
            const depTitle = dep.title ? ` — ${escapeHtml(dep.title)}` : '';
            const clickable = !isCrossBoard ? ` onclick="${task._source === 'agent' ? 'showAgentTaskDetailModal' : 'showTaskDetailModal'}(${dep.task_id})" style="cursor:pointer"` : '';
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
    const tasks = state.currentBoardTasks || [];
    const task = tasks.find(t => t.id === taskId);
    if (!task) return;
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
            const resp = await fetch(`/api/board/${encodeURIComponent(boardProject)}/subscribers`);
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
        `<option value="${p}"${p === task.priority ? ' selected' : ''}>${p}</option>`
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
            <button class="btn" onclick="window.cancelTaskEdit()">Cancel</button>
            <button class="btn btn-primary" onclick="window.saveTaskEdit(${taskId})">Save</button>`;
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
            const amendmentResp = await fetch(`/api/board/${encodeURIComponent(boardProject)}/tasks/${taskId}/amend`, {
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
            const resp = await fetch(`/api/board/${encodeURIComponent(boardProject)}/tasks/${taskId}`, {
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
        : (state.currentBoardTasks || []).find(item => item.id === taskId);
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
        <button class="btn" onclick="window.${personal ? 'showAgentTaskDetailModal' : '_restoreTaskFooter'}(${taskId})">Back</button>
        <span style="flex:1"></span>
        <button class="btn btn-success" onclick="window._doCompleteTask(${taskId}, ${personal})">Complete</button>`;
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
            : (state.currentBoardTasks || []).find(item => item.id === taskId);
        const requiredOutputs = _taskRequiredOutputs(task);
        const suppliedNames = new Set(artifacts.map(artifact => artifact?.name).filter(Boolean));
        const missingOutputs = requiredOutputs.filter(output => !suppliedNames.has(output));
        if (missingOutputs.length) throw new Error(`Required outputs missing: ${missingOutputs.join(', ')}`);
        const outcome = document.getElementById('task-complete-outcome')?.value || 'success';
        const endpoint = personal ? `/api/agent/tasks/${taskId}/complete` : `/api/board/${encodeURIComponent(boardProject)}/tasks/${taskId}/complete`;
        const identity = personal ? { session_id: state.currentSession.session_id } : { subscriber_id: 'Operator' };
        const expectedRevision = !personal && task ? Number(task.revision || 1) : undefined;
        const resp = await fetch(endpoint, {
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
        const resp = await fetch(`/api/board/${encodeURIComponent(boardProject)}/tasks/${taskId}/nudge`, { method: 'POST' });
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
        const resp = await fetch(`/api/board/${encodeURIComponent(boardProject)}/tasks/${taskId}/reminder`, {
            method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ interval_seconds: intervalSeconds }),
        });
        const data = await resp.json().catch(() => ({}));
        if (!resp.ok) throw new Error(data.error || `HTTP ${resp.status}`);
        showToast(`Periodic reminders started for ${data.assignee || 'the assignee'}`);
    } catch (e) { showToast(e.message || 'Failed to start reminders', true); }
}

export async function stopBoardTaskReminder(taskId) {
    const boardProject = _getBoardProject();
    if (!boardProject) return;
    await fetch(`/api/board/${encodeURIComponent(boardProject)}/tasks/${taskId}/reminder`, { method: 'DELETE' });
    showToast('Periodic reminders stopped');
}

export function cancelBoardTask(taskId) {
    const footer = document.getElementById('task-detail-modal-footer');
    if (!footer) return;
    footer.innerHTML = `
        <div class="task-confirm-inline">
            <span class="task-confirm-text">Cancel this task? This cannot be undone.</span>
            <div class="task-confirm-buttons">
                <button class="btn" onclick="window._restoreTaskFooter(${taskId})">No, Go Back</button>
                <button class="btn btn-danger" onclick="window._doCancelTask(${taskId})">Yes, Cancel</button>
            </div>
        </div>`;
}

export async function _doCancelTask(taskId) {
    const boardProject = _getBoardProject();
    if (!boardProject) return;
    try {
        const resp = await fetch(`/api/board/${encodeURIComponent(boardProject)}/tasks/${taskId}/cancel`, {
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
        const resp = await fetch(`/api/board/${encodeURIComponent(boardProject)}/tasks/${taskId}/publish`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
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
    _openSubagentId = null;
    const modal = document.getElementById('task-detail-modal');
    if (!modal) return;
    modal.style.display = 'none';
    if (modal._escHandler) {
        document.removeEventListener('keydown', modal._escHandler);
        modal._escHandler = null;
    }
}

function formatTaskDate(isoStr) {
    try {
        const d = new Date(isoStr);
        return d.toLocaleString(undefined, {
            month: 'short', day: 'numeric',
            hour: '2-digit', minute: '2-digit',
        });
    } catch {
        return isoStr;
    }
}

async function saveTaskOrder() {
    if (!state.currentSession || state.currentSession.type !== 'live') return;
    const list = document.getElementById('task-bar-list');
    if (!list) return;

    const taskIds = Array.from(list.querySelectorAll('.task-item'))
        .map(el => parseInt(el.dataset.taskId))
        .filter(id => !isNaN(id));

    try {
        await fetch(`/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/tasks/reorder`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ task_ids: taskIds }),
        });
    } catch (e) {
        // silent fail for reorder
    }
}
