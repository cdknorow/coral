/* Nudging a task's assignee and the reminder on a task (moved out of tasks.js, #4996; no behaviour change). */
import { showToast } from './utils.js';
import { importOnce } from './import_once.js';
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
    // Its own dialog (#6329): window.prompt is not available in the native app
    const { openReminderDialog } = await importOnce('./reminder_dialog.js');
    openReminderDialog({
        title: 'Remind the assignee',
        intro: (every) => `Nudges the assignee about this task ${every} until you stop the reminders.`,
        minutes: 5,
        submitLabel: 'Start reminders',
        onSubmit: async ({ seconds }) => {
            try {
                const resp = await boardFetch(boardProject, `/tasks/${pathId(taskId)}/reminder`, {
                    method: 'POST', headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ interval_seconds: seconds, subscriber_id: 'Operator' }),
                });
                const data = await resp.json().catch(() => ({}));
                if (!resp.ok) return { ok: false, error: data.error || `HTTP ${resp.status}` };
                showToast(`Periodic reminders started for ${data.assignee || 'the assignee'}`);
                return { ok: true };
            } catch (e) { return { ok: false, error: e.message || 'Failed to start reminders' }; }
        },
    });
}

export async function stopBoardTaskReminder(taskId) {
    const boardProject = _getBoardProject();
    if (!boardProject) return;
    await boardFetch(boardProject, `/tasks/${pathId(taskId)}/reminder?subscriber_id=Operator`, { method: 'DELETE' });
    showToast('Periodic reminders stopped');
}
