/* Coral's side of the shared Inbox (#5754): the adapter that gives inbox_panel.js its tasks, task text, reply and saved-message read from this Coral's own routes, and the one mounted panel
 * (the Inbox tab's "Tasks for you" and the "Read the whole message" control of a Saved for later item). The component itself is inbox_panel.js; Coral Hub mounts the same file with its own adapter. */
import { importOnce } from './import_once.js';
import { makeLinkPolicy } from './safe_markdown.js';
import { onAccountForgotten } from './local_store.js';
import { keyLabel, pathId, serverFetch } from './server_base.js';
const { mountInbox, clearInboxDrafts, INBOX_CONTRACT } = await importOnce('./inbox_panel.js'); // the component (shared with the hub); this file is the one that is Coral's

const OPEN_STATUSES = ['pending', 'in_progress', 'review_pending', 'blocked'];
const FIELDS = 'id,board_id,board_label,title,priority,status,created_by,created_at,updated_at';

async function json(resp) {
    let data = {};
    try { data = await resp.json(); } catch { /* no body */ }
    if (!resp.ok) throw new Error((data && typeof data.error === 'string' && data.error) || `HTTP ${resp.status}`);
    return data;
}

/** The adapter for this Coral (the Operator's own, on this computer). */
export const coralInboxAdapter = {
    contract: INBOX_CONTRACT,
    async listTasks() {
        const q = new URLSearchParams({ assigned_to: 'Operator', fields: FIELDS, sort: 'updated_at', order: 'desc', limit: '100' });
        for (const s of OPEN_STATUSES) q.append('status', s);
        const d = await json(await fetch(`/api/board/tasks?${q}`));
        return (d.tasks || []).map(t => ({ id: t.id, project: t.board_id, board: t.board_label || keyLabel(t.board_id), title: t.title, priority: t.priority, status: t.status,
            created_by: t.created_by, created_at: t.created_at, updated_at: t.updated_at }));
    },
    async getTask(project, id) {
        return json(await fetch(`/api/board/${encodeURIComponent(project)}/tasks/${pathId(id)}`));
    },
    async replyToTask({ project, taskId, content }) {
        const d = await json(await fetch(`/api/board/${encodeURIComponent(project)}/tasks/${pathId(taskId)}/reply`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ content }) }));
        return { ok: !!d.ok, notified: d.notified || [], warnings: d.warnings || [] };
    },
    canReply() { return { ok: true }; }, // the Operator of this computer; a Coral Hub viewer's adapter says why not
    async readSavedMessage(item) {
        // GET /api/saved/{id}/message (Backend, #5753): the redacted, capped text of the stored item's source; reason says why there is none (gone, not_reachable, too_long)
        const d = await json(await serverFetch(item.server, `/api/saved/${pathId(item.id)}/message`));
        return { text: typeof d.text === 'string' ? d.text : '', partial: !!d.partial, reason: typeof d.reason === 'string' ? d.reason : '' };
    },
    linkPolicy: makeLinkPolicy({}), // no artifact lookup yet: artifact links stay plain text until there is one that says the id exists
};

let _inbox = null;
onAccountForgotten(clearInboxDrafts);

/** The mounted panel (made the first time it is asked for, in the Inbox tab's Tasks panel), or null where the page has no such host. */
export function coralInbox() {
    if (_inbox) return _inbox;
    const host = document.getElementById('inbox-tasks-host');
    if (!host) return null;
    _inbox = mountInbox(host, coralInboxAdapter, { load: false });
    return _inbox;
}

/** Read the tasks again (the Tasks tab was opened). */
export function refreshTasks() { const i = coralInbox(); if (i) i.refresh(); }

/** The "Read the whole message" control for a saved item, or null. */
export function savedExpandFor(item) { const i = coralInbox(); return i ? i.savedExpand(item) : null; }
