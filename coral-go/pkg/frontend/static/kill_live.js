/* The one kill call and the one row-drop the four kill paths share (#6892, R1 of #6877): the sidebar's Kill Session (agent_actions.js), the agent view's (controls.js), a folder's Kill All and a team's Kill Team
 * (team_actions.js) each wrote their own fetch and follow-up and had drifted. Each caller keeps its confirmation words and its own change of view. */
import { state } from './state.js';
import { serverFetch, sessionServer, teamFetch } from './server_base.js';
import { stopCaptureRefresh } from './capture.js';
import { redrawLiveList } from './list_hooks.js';

/** Kill one agent: POST its kill with its type and session id to its server. Resolves to the parsed answer ({ ok, error?, changes_artifact_error?, worktree? }), or null when the body is not JSON. Network errors throw. */
export async function killLiveSession(row, server = sessionServer(row)) {
    const resp = await serverFetch(server, `/api/sessions/live/${encodeURIComponent(row.name)}/kill`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ agent_type: row.agent_type, session_id: row.session_id }),
    });
    return resp.json().catch(() => null);
}

/** Stop a whole team through the team route, so the team is marked stopped as an API kill does (its worktree is then not "in use"): resolves to { ok, answer } (answer: { ok, sessions_killed, worktree? } or null). */
export async function killLiveTeam(boardKey) {
    const resp = await teamFetch(boardKey, n => `/api/sessions/live/team/${encodeURIComponent(n)}/kill`, { method: 'POST' });
    return { ok: resp.ok, answer: await resp.json().catch(() => null) };
}

/** The killed agents' rows go at once (their chats stay in History), by session id AND server (the same id on another server is another agent). When the open agent is one of them its capture refresh stops. The list is redrawn. */
export function dropKilledRows(rows) {
    const gone = new Set(rows.map(s => s.session_id + '@' + sessionServer(s)));
    if (state.currentSession && gone.has(state.currentSession.session_id + '@' + sessionServer(state.currentSession))) stopCaptureRefresh();
    state.liveSessions = (state.liveSessions || []).filter(s => !gone.has(s.session_id + '@' + sessionServer(s)));
    redrawLiveList();
}
