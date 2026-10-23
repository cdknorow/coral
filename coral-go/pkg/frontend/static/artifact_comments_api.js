/* The comments routes of an artifact (#5182), the only place the UI talks to /api/artifacts/{id}/comments, so the server's shape is read in one spot. Every call resolves the parsed answer or throws
 * an Error whose message is the server's reason (a 403 says why) and whose .status is the HTTP status (requestJson, api.js). `server` routes the call to a registered remote like the artifact itself. */
import { serverUrl, isCloud } from './server_base.js';

// One request: the parsed answer, or an Error with the server's reason ({"error": "..."}) or "HTTP <status>" and .status on it (the same as api.js requestJson once that lands).
async function requestJson(method, url, body) {
    const init = { method };
    if (body !== undefined) { init.headers = { 'Content-Type': 'application/json' }; init.body = JSON.stringify(body); }
    const resp = await fetch(url, init);
    const text = await resp.text().catch(() => '');
    let data = {};
    try { data = text ? JSON.parse(text) : {}; } catch (e) { if (resp.ok) throw e; }
    if (!resp.ok) {
        const error = new Error((data && typeof data.error === 'string' && data.error) || `HTTP ${resp.status}`);
        error.status = resp.status;
        throw error;
    }
    return data;
}

const base = (hex, server) => serverUrl(`/api/artifacts/${hex}/comments`, server);

/** What the panel draws from one comment: the server's fields under the names the UI uses. `can` is what this viewer may do to it (the server decides again on every write; the UI only hides what would be refused). */
export function normalizeComment(c) {
    const can = c.can || {}, who = c.author || {};
    return {
        id: c.id, parentId: c.parent_id == null ? null : c.parent_id, anchor: c.anchor || { whole: true }, body: String(c.body == null ? '' : c.body),
        authorId: who.subject || '', authorName: who.name || '', authorKind: who.kind || '',
        createdAt: c.created_at || '', editedAt: c.edited_at || '', resolved: !!c.resolved_at, resolvedBy: c.resolved_by ? (c.resolved_by.subject || '') : '', deleted: !!c.deleted, mine: !!c.mine,
        can: { reply: !!can.reply, edit: !!can.edit, delete: !!can.delete, resolve: !!can.resolve },
        asks: Array.isArray(c.asks) ? c.asks.map(k => ({ agent: String(k.agent || ''), open: !!k.open, replies: Math.max(0, Number(k.replies) || 0) })) : [], // the agents asked into this thread, on the owner's list only (#6462)
    };
}

const boardQuery = (board) => (board ? `&board=${encodeURIComponent(board)}` : '');

/** { comments, locked, canComment, canLock, reason, limits }: every comment of the artifact (the panel filters by state itself). `you` is what the server says of the viewer. */
export async function listComments(hex, server, board) {
    const d = await requestJson('GET', `${base(hex, server)}?state=all${boardQuery(board)}`);
    const you = d.you || {};
    const out = { comments: (d.comments || []).map(normalizeComment), locked: !!d.locked, canComment: !!you.can_comment, canLock: !!you.can_lock, reason: you.reason || '', limits: d.limits || {} };
    // Through Coral Hub the comments can be read and not written: writing, replying, resolving and the lock are not routed from there (pkg/tunnel/routes.go). Offering the controls would end in a refused request, so the panel says where to write instead (#6245).
    if (isCloud()) {
        out.canComment = false; out.canLock = false; out.reason = 'Comments can be added on the computer itself.';
        for (const c of out.comments) c.can = { reply: false, edit: false, delete: false, resolve: false };
    }
    return out;
}

/** Make a comment: a thread on an anchor ({quote, prefix, suffix, start_line, end_line} or {whole: true}), or a reply to parentId (a reply has no anchor). */
export const createComment = (hex, server, { body, anchor, parentId, board }) => requestJson('POST', base(hex, server),
    { body, ...(parentId != null ? { parent_id: parentId } : { anchor }), ...(board ? { board } : {}) }).then(normalizeComment);
export const editComment = (hex, server, id, body) => requestJson('PATCH', `${base(hex, server)}/${id}`, { body }).then(normalizeComment);
export const deleteComment = (hex, server, id) => requestJson('DELETE', `${base(hex, server)}/${id}`);
export const resolveComment = (hex, server, id) => requestJson('POST', `${base(hex, server)}/${id}/resolve`, {}).then(normalizeComment);
export const reopenComment = (hex, server, id) => requestJson('POST', `${base(hex, server)}/${id}/reopen`, {}).then(normalizeComment);
/** The owner's lock: this artifact's comments read only for everyone (reads stay open). */
export const setLocked = (hex, server, locked) => requestJson(locked ? 'PUT' : 'DELETE', `${base(hex, server)}/lock`, { scope: 'artifact' });

/** Send to agent (#6357, #6456): ask 1 to 10 agents of the comment's board into its thread; the server posts one board message to each from the owner. `agents` are the names the board knows them by (their roles).
 *  Resolves { ok, status, results: [{agent, state, reason}], error }: a 409 with results (nobody was asked) is an answer to show, not a throw. Local only: through Coral Hub the route is refused, and the UI never offers it there. */
export async function sendComment(hex, server, id, agents) {
    const resp = await fetch(`${base(hex, server)}/${id}/send`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ agents }) });
    let data = {};
    try { data = await resp.json(); } catch { /* an answer that is not JSON has no results */ }
    const results = Array.isArray(data.results) ? data.results.map(r => ({ agent: String(r.agent || ''), state: String(r.state || ''), reason: String(r.reason || '') })) : [];
    return { ok: resp.ok && !!data.ok, status: resp.status, results, error: typeof data.error === 'string' ? data.error : (resp.ok ? '' : `HTTP ${resp.status}`) };
}
