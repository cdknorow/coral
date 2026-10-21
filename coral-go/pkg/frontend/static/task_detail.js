/* A task's detail dialog, editing it, and completing, cancelling or publishing it (moved out of tasks.js, #5082; no behaviour change). */
import { importOnce } from './import_once.js';
import { state } from './state.js';
import { _getBoardProject } from './task_board_context.js';
import { escapeAttr, escapeHtml, showToast } from './utils.js';
import { clearOpenSubagent } from './subagents.js';
const { _taskDetailHtml } = await importOnce('./task_detail_html.js'); // lazy modules load the others they need through importOnce, so a failed load is retried
import { _agentTaskStatus } from './task_format.js';
import { closeOnBackdropAndEscape, releaseEscape } from './dialog_close.js';
import { boardFetch, pathId, serverFetch, currentServer } from './server_base.js';
import { _renderDepPicker, _getSelectedDeps } from './task_dep_picker.js';
import { _taskRequiredOutputs } from './task_reminders.js';
import { _boardTasksFetch, _liveCosts, _reviewRequested, loadBoardTasks, loadAgentTasks } from './task_list.js';

// Full tasks fetched for the detail view, by id. A list item is light (#3269).
const _fullBoardTasks = new Map();

/** The full task when fetched and still current, else the list item. */
function _boardTaskById(taskId) {
    const item = (state.currentBoardTasks || []).find(t => t.id === taskId);
    const full = _fullBoardTasks.get(taskId);
    if (item && full && full.revision === item.revision && full.status === item.status) return full;
    return item || null;
}

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
    modal.setAttribute('aria-labelledby', 'task-detail-modal-title task-detail-heading'); // named by its number and its task (the dialog is shared: the others keep the title alone)

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
    modal.setAttribute('aria-labelledby', 'task-detail-modal-title task-detail-heading');

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
            const amendmentResp = await boardFetch(boardProject, `/tasks/${pathId(taskId)}/amend`, {
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
            const resp = await boardFetch(boardProject, `/tasks/${pathId(taskId)}`, {
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
            ? serverFetch(currentServer(), `/api/agent/tasks/${pathId(taskId)}/complete`, init)
            : boardFetch(boardProject, `/tasks/${pathId(taskId)}/complete`, init);
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
        const resp = await boardFetch(boardProject, `/tasks/${pathId(taskId)}/cancel`, {
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
        const resp = await boardFetch(boardProject, `/tasks/${pathId(taskId)}/publish`, {
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
