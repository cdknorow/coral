/* Agent task bar — CRUD, rendering, drag reorder */

import { state } from './state.js';
import { escapeHtml, escapeAttr, showToast, renderMarkdown } from './utils.js';
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

export async function loadAgentTasks(agentName, sessionId) {
    if (!agentName) return;
    try {
        const params = new URLSearchParams();
        const sid = sessionId || (state.currentSession && state.currentSession.session_id);
        if (sid) params.set("session_id", sid);
        const qs = params.toString() ? `?${params}` : "";
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(agentName)}/tasks${qs}`);
        if (!resp.ok) throw new Error(`tasks fetch failed: ${resp.status}`);
        state.currentAgentTasks = await resp.json();
    } catch (e) {
        state.currentAgentTasks = [];
    }
    renderTaskList();
}

// Which session state.currentSubagents belongs to. Lets a session switch drop
// the previous agent's subagents at once instead of showing them until the
// new fetch returns, and lets a slow response for an old session be ignored.
let _subagentsSessionId = null;

export async function loadSubagents(agentName, sessionId) {
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
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(agentName)}/subagents?session_id=${encodeURIComponent(sid)}`);
        if (!resp.ok) throw new Error(`subagents fetch failed: ${resp.status}`);
        subagents = await resp.json();
    } catch (e) {
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
        await fetch(`/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/tasks/${taskId}`, {
            method: 'PATCH',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ completed: completed ? 1 : 0 }),
        });
        await loadAgentTasks(state.currentSession.name, state.currentSession.session_id);
    } catch (e) {
        showToast('Failed to update task', true);
    }
}

export async function deleteAgentTask(taskId) {
    if (!state.currentSession || state.currentSession.type !== 'live') return;
    try {
        await fetch(`/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/tasks/${taskId}`, {
            method: 'DELETE',
        });
        await loadAgentTasks(state.currentSession.name, state.currentSession.session_id);
    } catch (e) {
        showToast('Failed to delete task', true);
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
        status: t.completed === 1 ? 'completed' : t.completed === 2 ? 'in_progress' : 'pending',
        priority: null,
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
            <span class="board-task-status-col"></span>
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
            : t.status === 'skipped' ? 'completed'
            : t.status === 'blocked' ? 'blocked'
            : t.status === 'draft' ? 'draft' : '';
        const priorityClass = t.priority ? 'board-task-priority-' + t.priority : 'board-task-priority-none';
        const assignee = t.assigned_to || '\u2014';
        const title = escapeHtml(t.title || t.description || '');
        const tooltip = isSubagent ? ` title="${escapeAttr(_subagentTooltip(t))}"`
            : t.body ? ` title="${escapeAttr(t.body)}"` : '';
        const timeStr = _formatTaskTime(t.created_at);
        const statusIcon = t.status === 'completed'
            ? '<span class="material-icons board-task-status-icon completed">check_circle</span>'
            : t.status === 'in_progress'
            ? '<span class="task-spinner" title="In progress"></span>'
            : t.status === 'skipped'
            ? '<span class="material-icons board-task-status-icon skipped">block</span>'
            : t.status === 'blocked'
            ? '<span class="material-icons board-task-status-icon blocked" title="Blocked">lock</span>'
            : t.status === 'draft'
            ? '<span class="material-icons board-task-status-icon draft" title="Draft">edit_note</span>'
            : '<span class="material-icons board-task-status-icon pending">radio_button_unchecked</span>';
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
            : isAgent ? '' : ` onclick="showTaskDetailModal(${t.id})" style="cursor:pointer"`;
        const typeCell = isSubagent
            ? `<span class="board-task-type board-task-type-subagent" title="Subagent launched by ${escapeAttr(assignee)}"><span class="material-icons">account_tree</span>sub</span>`
            : `<span class="board-task-type">${isAgent ? 'agent' : 'board'}</span>`;
        const subagentBadge = isSubagent && t.subagent_type
            ? `<span class="board-task-subagent-badge">${escapeHtml(t.subagent_type)}</span>` : '';
        return `
        <div class="board-task-item ${statusClass}${isSubagent ? ' board-task-subagent' : ''}"${clickHandler}${isSubagent ? ` data-subagent-id="${escapeAttr(t.subagent_id || '')}"` : ''}>
            ${statusIcon}
            <span class="board-task-priority ${priorityClass}">${t.priority ? escapeHtml(t.priority) : '\u2014'}</span>
            ${typeCell}
            <span class="board-task-assignee">${escapeHtml(assignee)}</span>
            <span class="board-task-desc"${tooltip}>${subagentBadge}${title}</span>
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
        t.status !== 'completed' && t.status !== 'skipped' && t.id !== excludeTaskId
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
        if (blockedBy.length > 0) payload.blocked_by = blockedBy;
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
            body: JSON.stringify({ title, body, session_id: session.session_id, notify }),
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

    const statusLabel = task.status === 'completed' ? 'Completed'
        : task.status === 'in_progress' ? 'In Progress'
        : task.status === 'skipped' ? 'Cancelled'
        : task.status === 'blocked' ? 'Blocked'
        : task.status === 'draft' ? 'Draft'
        : 'Pending';
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
        <div class="task-detail-title">${escapeHtml(task.title)}</div>
        <div class="task-detail-meta">
            <span class="task-detail-status ${statusClass}">${statusLabel}</span>
            <span class="board-task-priority ${priorityClass}">${escapeHtml(task.priority || 'medium')}</span>
        </div>`;

    if (task.body) {
        html += `<div class="task-detail-section">
            <div class="task-detail-label">Description</div>
            <div class="task-detail-body">${escapeHtml(task.body)}</div>
        </div>`;
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
        html += `<div class="task-detail-field">
            <span class="task-detail-label">Claimed</span>
            <span class="task-detail-value">${claimedAt}</span>
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

    html += `</div>`;

    // Blocked by dependencies
    if (task.blocked_by && task.blocked_by.length > 0) {
        const boardProject = _getBoardProject();
        const depsHtml = task.blocked_by.map(dep => {
            const depStatusClass = dep.status === 'completed' ? 'task-dep-status-completed'
                : dep.status === 'in_progress' ? 'task-dep-status-inprogress'
                : dep.status === 'skipped' ? 'task-dep-status-cancelled'
                : dep.status === 'blocked' ? 'task-dep-status-blocked'
                : dep.status === 'draft' ? 'task-dep-status-draft'
                : 'task-dep-status-pending';
            const depStatusLabel = dep.status === 'completed' ? 'completed'
                : dep.status === 'in_progress' ? 'in progress'
                : dep.status === 'skipped' ? 'cancelled'
                : dep.status === 'blocked' ? 'blocked'
                : dep.status === 'draft' ? 'draft'
                : 'pending';
            const isCrossBoard = dep.board_id && dep.board_id !== boardProject;
            const boardPrefix = isCrossBoard ? `${escapeHtml(dep.board_id)} ` : '';
            const depTitle = dep.title ? ` — ${escapeHtml(dep.title)}` : '';
            const clickable = !isCrossBoard ? ` onclick="showTaskDetailModal(${dep.task_id})" style="cursor:pointer"` : '';
            return `<div class="task-dep-item">
                <span class="task-dep-status ${depStatusClass}">${depStatusLabel}</span>
                <a class="task-dep-link"${clickable}>${boardPrefix}#${dep.task_id}${depTitle}</a>
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
    if (task.status === 'in_progress' && _liveCosts[task.id]) {
        const lc = _liveCosts[task.id];
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
    content.innerHTML = html;

    // Update footer with action buttons for editable tasks
    const footer = document.getElementById('task-detail-modal-footer');
    if (footer) {
        const isEditable = task.status === 'pending' || task.status === 'in_progress' || task.status === 'blocked' || task.status === 'draft';
        if (isEditable) {
            const canComplete = task.status !== 'blocked' && task.status !== 'draft';
            const showPublish = task.status === 'draft';
            footer.innerHTML = `
                <button class="btn btn-danger-text" onclick="window.cancelBoardTask(${task.id})">Cancel Task</button>
                <span style="flex:1"></span>
                <button class="btn" onclick="window.hideTaskDetailModal()">Close</button>
                <button class="btn" onclick="window.enableTaskEditMode(${task.id})">Edit</button>
                ${showPublish ? `<button class="btn btn-primary" onclick="window.publishBoardTask(${task.id})">Publish</button>` : ''}
                ${canComplete ? `<button class="btn btn-success" onclick="window.completeBoardTask(${task.id})">Complete</button>` : ''}`;
        } else {
            footer.innerHTML = `<button class="btn" onclick="window.hideTaskDetailModal()">Close</button>`;
        }
    }

    modal.style.display = '';

    // Close on backdrop click
    modal.onclick = (e) => { if (e.target === modal) hideTaskDetailModal(); };
    // Close on Escape
    modal._escHandler = (e) => { if (e.key === 'Escape') hideTaskDetailModal(); };
    document.addEventListener('keydown', modal._escHandler);
}

let _editOriginalTask = null;

export async function enableTaskEditMode(taskId) {
    const tasks = state.currentBoardTasks || [];
    const task = tasks.find(t => t.id === taskId);
    if (!task) return;
    _editOriginalTask = task;

    const content = document.getElementById('task-detail-content');
    if (!content) return;

    // Build assignee options
    let assigneeOptions = '<option value="">Unassigned</option>';
    const boardProject = _getBoardProject();
    if (boardProject) {
        try {
            const resp = await fetch(`/api/board/${encodeURIComponent(boardProject)}/subscribers`);
            if (resp.ok) {
                const subs = await resp.json();
                (subs || []).forEach(s => {
                    const name = s.subscriber_id || s.name;
                    if (name) {
                        const selected = name === task.assigned_to ? ' selected' : '';
                        assigneeOptions += `<option value="${escapeAttr(name)}"${selected}>${escapeHtml(name)}</option>`;
                    }
                });
            }
        } catch { /* ignore */ }
    }

    const priorityOptions = ['critical', 'high', 'medium', 'low'].map(p =>
        `<option value="${p}"${p === task.priority ? ' selected' : ''}>${p}</option>`
    ).join('');

    const showDepPicker = task.status === 'blocked' || task.status === 'pending' || task.status === 'draft';
    content.innerHTML = `
        <div id="task-edit-error" class="modal-error" style="display:none"></div>
        <label for="task-edit-title">Title
            <input type="text" id="task-edit-title" value="${escapeAttr(task.title)}">
        </label>
        <label for="task-edit-body">Description
            <textarea id="task-edit-body" rows="3">${escapeHtml(task.body || '')}</textarea>
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
    if (!task) return;

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
    if (body !== (task.body || '')) updates.body = body;
    if (priority !== task.priority) updates.priority = priority;
    if (assignedTo !== (task.assigned_to || '')) updates.assigned_to = assignedTo;

    const depPickerEl = document.getElementById('task-edit-deps');
    if (depPickerEl) {
        const newDeps = _getSelectedDeps('task-edit-deps');
        const oldDeps = (task.blocked_by || []).map(d => d.task_id).sort();
        const sortedNew = [...newDeps].sort();
        if (JSON.stringify(oldDeps) !== JSON.stringify(sortedNew)) {
            updates.blocked_by = newDeps;
        }
    }

    if (Object.keys(updates).length === 0) {
        cancelTaskEdit();
        return;
    }

    const boardProject = _getBoardProject();
    if (!boardProject) return;

    try {
        const resp = await fetch(`/api/board/${encodeURIComponent(boardProject)}/tasks/${taskId}`, {
            method: 'PATCH',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(updates),
        });
        if (!resp.ok) {
            const data = await resp.json().catch(() => ({}));
            throw new Error(data.error || `HTTP ${resp.status}`);
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

export function completeBoardTask(taskId) {
    const footer = document.getElementById('task-detail-modal-footer');
    if (!footer) return;
    footer.innerHTML = `
        <div class="task-confirm-inline">
            <input type="text" id="task-complete-message" placeholder="Completion message (optional)" class="task-confirm-input">
            <div class="task-confirm-buttons">
                <button class="btn" onclick="window._restoreTaskFooter(${taskId})">Back</button>
                <button class="btn btn-success" onclick="window._doCompleteTask(${taskId})">Complete</button>
            </div>
        </div>`;
    document.getElementById('task-complete-message').focus();
}

export async function _doCompleteTask(taskId) {
    const boardProject = _getBoardProject();
    if (!boardProject) return;
    const msgEl = document.getElementById('task-complete-message');
    const message = msgEl ? msgEl.value.trim() : '';
    try {
        const resp = await fetch(`/api/board/${encodeURIComponent(boardProject)}/tasks/${taskId}/complete`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ subscriber_id: 'Operator', message: message || undefined }),
        });
        if (!resp.ok) {
            const data = await resp.json().catch(() => ({}));
            throw new Error(data.error || `HTTP ${resp.status}`);
        }
        hideTaskDetailModal();
        await loadBoardTasks(boardProject);
        showToast('Task completed');
    } catch (e) {
        showToast(e.message || 'Failed to complete task', true);
    }
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
