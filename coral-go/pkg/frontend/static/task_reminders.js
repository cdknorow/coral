/* Nudging a task's assignee and the reminder on a task (moved out of tasks.js, #4996; no behaviour change). */
import { showToast } from './utils.js';
import { pathId, boardFetch } from './server_base.js';
import { _getBoardProject } from './task_board_context.js';

export function _taskRequiredOutputs(task) {
    const outputs = task?.workflow?.required_outputs || task?.required_outputs || [];
    const list = Array.isArray(outputs) ? outputs : [outputs];
    return list.map(output => typeof output === 'string' ? output : output?.name).filter(Boolean);
}

// Resend the task's nudge to its assignee (a nudge typed while the agent was
// busy, e.g. compacting, can be lost).
export async function nudgeBoardTask(taskId) {
    const boardProject = _getBoardProject();
    if (!boardProject) return;
    try {
        const resp = await boardFetch(boardProject, `/tasks/${pathId(taskId)}/nudge?subscriber_id=Operator`, { method: 'POST' });
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
        const resp = await boardFetch(boardProject, `/tasks/${pathId(taskId)}/reminder`, {
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
    await boardFetch(boardProject, `/tasks/${pathId(taskId)}/reminder?subscriber_id=Operator`, { method: 'DELETE' });
    showToast('Periodic reminders stopped');
}
