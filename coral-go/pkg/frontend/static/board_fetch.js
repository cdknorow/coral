/* What the message board asks the server for (moved out of message_board.js, #5065; no behaviour change). */
import { state } from './state.js';
import { identityKey, noteBoardLabels, serverFetch, boardFetch, sessionServer, isCloud } from './server_base.js';

export const PAGE_SIZE = 50;

// Responses can arrive after a board switch or overlap another request; keep
// each message once, in id order.
export function mergeMessages(...lists) {
    const byId = new Map();
    for (const list of lists) for (const m of list) byId.set(m.id, m);
    return [...byId.values()].sort((a, b) => a.id - b.id);
}

// Boards live on the server that hosts the team, so the project list is the
// hub's own boards plus those of every online remote. Each entry's `project`
// is a team key (server + name); local keys are the bare name.
export async function fetchProjects() {
    const resp = isCloud() ? null : await fetch('/api/board/projects');
    const data = resp ? await resp.json() : [];
    const local = Array.isArray(data) ? data : [];
    noteBoardLabels(local, 'project'); // board_label beside each key (#3536)
    const remotes = !state.hub ? [] : (state.servers || []).filter(s => s.id !== 'local' && s.status === 'online');
    const lists = await Promise.all(remotes.map(async srv => {
        try {
            const r = await serverFetch(srv.id, '/api/board/projects');
            if (!r.ok) return [];
            const rows = await r.json();
            noteBoardLabels(rows, 'project', srv.id);
            return (Array.isArray(rows) ? rows : []).map(p => ({ ...p, project: identityKey(srv.id, p.project), server: srv.id }));
        } catch { return []; }
    }));
    return [...local, ...lists.flat()];
}

// page is {before_id} or {after_id} (none: the latest page). A reply reports
// before_count, the messages older than the page.
export async function fetchMessages(project, limit = PAGE_SIZE, page = {}) {
    const params = new URLSearchParams({ limit: String(limit), format: 'dashboard' });
    for (const [k, v] of Object.entries(page)) params.set(k, String(v));
    if (!('offset' in page)) params.set('offset', '0');
    const resp = await boardFetch(project, `/messages/all?${params}`);
    // A refusal is not an empty board: a 503's {error} body once read as zero messages (#3430).
    if (!resp.ok) { const err = new Error(`messages: HTTP ${resp.status}`); err.resp = resp; throw err; }
    const data = await resp.json();
    return {
        messages: Array.isArray(data.messages) ? data.messages : [], total: Number(data.total) || 0,
        beforeCount: Number(data.before_count) || 0,
    };
}

// The export reads whole boards and transcripts in pages (#3718): through Coral Hub a JSON reply
// is held whole to be checked for secrets and refused over 2 MiB, so it never asks for all of one
// at once.
const EXPORT_PAGE = 200;

const EXPORT_CHAT_PAGE = 100;

export async function fetchAllBoardMessages(project) {
    const first = await fetchMessages(project, EXPORT_PAGE);
    let all = first.messages;
    let before = first.beforeCount;
    while (before > 0 && all.length) {
        const r = await fetchMessages(project, EXPORT_PAGE, { before_id: all[0].id });
        if (!r.messages.length) break;
        all = mergeMessages(r.messages, all);
        before = r.beforeCount;
    }
    return all;
}

export async function fetchAllLiveChat(live) {
    const messages = [];
    for (let offset = 0; ; offset += EXPORT_CHAT_PAGE) {
        const params = new URLSearchParams({
            agent_type: live.agent_type || 'claude', session_id: live.session_id,
            working_directory: live.working_directory || '', after: '0',
            limit: String(EXPORT_CHAT_PAGE), offset: String(offset),
        });
        const resp = await serverFetch(sessionServer(live), `/api/sessions/live/${encodeURIComponent(live.name)}/chat?${params}`);
        if (!resp.ok) throw new Error(`chat: HTTP ${resp.status}`);
        const data = await resp.json();
        const page = Array.isArray(data.messages) ? data.messages : [];
        messages.unshift(...page);
        if (!page.length || messages.length >= data.total) break;
    }
    return { messages };
}

export async function fetchAllHistory(sessionId) {
    const messages = [];
    for (let offset = 0; ; offset += EXPORT_CHAT_PAGE) {
        const resp = await fetch(`/api/sessions/history/${encodeURIComponent(sessionId)}?limit=${EXPORT_CHAT_PAGE}&offset=${offset}`);
        if (!resp.ok) throw new Error(`history: HTTP ${resp.status}`);
        const data = await resp.json();
        const page = Array.isArray(data.messages) ? data.messages : [];
        messages.unshift(...page);
        if (!page.length || !data.start) break;
    }
    return { messages };
}

export async function fetchSubscribers(project) {
    const resp = await boardFetch(project, `/subscribers`);
    return await resp.json();
}
