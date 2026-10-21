/* Creating a task: the dialog and its submit (moved out of tasks.js, #5082; no behaviour change). */
import { state } from './state.js';
import { closeOnBackdropAndEscape, releaseEscape } from './dialog_close.js';
import { boardFetch, serverFetch, sessionServer } from './server_base.js';
import { ensureBuilt } from './lazy_dom.js';
import { _getBoardProject, _isSoloAgent } from './task_board_context.js';
import { _renderDepPicker, _getSelectedDeps } from './task_dep_picker.js';
import { showToast } from './utils.js';
import { addPendingMessage } from './live_chat.js';
import { loadBoardTasks, loadAgentTasks } from './task_list.js';

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
    clearTitleInvalid(document.getElementById('create-task-title'));

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

// A refusal in the dialog is a role=alert line, so it is spoken when it shows; one about the title also marks the title invalid and ties the box to the line.
function showCreateError(errEl, message, titleInvalid = false) {
    const title = document.getElementById('create-task-title');
    if (title) {
        if (titleInvalid) { title.setAttribute('aria-invalid', 'true'); title.setAttribute('aria-describedby', 'create-task-error'); title.focus(); }
        else clearTitleInvalid(title);
    }
    if (errEl) { errEl.style.display = ''; errEl.textContent = ''; errEl.textContent = message; }
}
function clearTitleInvalid(title) { title.removeAttribute('aria-invalid'); title.removeAttribute('aria-describedby'); }

export async function submitCreateTask() {
    const title = document.getElementById('create-task-title').value.trim();
    const errEl = document.getElementById('create-task-error');

    if (!title) {
        showCreateError(errEl, 'Title is required', true);
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
        showCreateError(errEl, 'No board project found');
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
