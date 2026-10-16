/* Moved out of tasks.js as it was (#3837): no behaviour change. */
import { escapeHtml, renderMarkdown, DOMPURIFY_CONFIG } from './utils.js';
import { _formatCost, _formatTokenCount, formatTaskDate } from './task_format.js';
import { redrawBoardTaskList, closeTaskDetailModal } from './task_hooks.js';
import { serverFetch, serverForSession } from './server_base.js';
import { state } from './state.js';

// Which session state.currentSubagents belongs to. Lets a session switch drop
// the previous agent's subagents at once instead of showing them until the
// new fetch returns, and lets a slow response for an old session be ignored.
let _subagentsSessionId = null;

export async function loadSubagents(agentName, sessionId, options) {
    const sid = sessionId || (state.currentSession && state.currentSession.session_id);
    if (!agentName || !sid) {
        _subagentsSessionId = null;
        state.currentSubagents = [];
        redrawBoardTaskList();
        return;
    }
    if (sid !== _subagentsSessionId) {
        _subagentsSessionId = sid;
        state.currentSubagents = [];
        redrawBoardTaskList();
    }
    let subagents = [];
    try {
        const resp = await serverFetch(serverForSession(agentName, sid), `/api/sessions/live/${encodeURIComponent(agentName)}/subagents?session_id=${encodeURIComponent(sid)}`, options);
        if (!resp.ok) throw new Error(`subagents fetch failed: ${resp.status}`);
        subagents = await resp.json();
    } catch (e) {
        if (e?.name === 'AbortError') return;
        subagents = [];
    }
    if (sid !== _subagentsSessionId) return; // the user moved to another session meanwhile
    state.currentSubagents = Array.isArray(subagents) ? subagents : [];
    redrawBoardTaskList();
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

export function _subagentTooltip(t) {
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
/** The dialog is shared: closing it, or showing a task in it, ends the subagent view. */
export function clearOpenSubagent() { _openSubagentId = null; }
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
export function _claimedFor(task) {
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
    const html = DOMPurify.sanitize(renderMarkdown(text, { breaks: true, gfm: true }, 'board'), {
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
        const resp = await serverFetch(serverForSession(name, sa.session_id), `/api/sessions/live/${encodeURIComponent(name)}/subagents/${encodeURIComponent(sa.subagent_id)}?session_id=${encodeURIComponent(sa.session_id)}`);
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
    if (footer) footer.innerHTML = `<button class="btn" data-on-click="task-close">Close</button>`;

    modal.style.display = '';
    modal.onclick = (e) => { if (e.target === modal) closeTaskDetailModal(); };
    if (modal._escHandler) document.removeEventListener('keydown', modal._escHandler);
    modal._escHandler = (e) => { if (e.key === 'Escape') closeTaskDetailModal(); };
    document.addEventListener('keydown', modal._escHandler);
}

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
