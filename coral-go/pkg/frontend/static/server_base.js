/* Server routing helpers for the multi-server hub.
 *
 * Identity of an agent, terminal or team is (server, name). `server` is the
 * registered remote's id, or "local" (the default) for the hub itself. Every
 * helper treats a missing/empty server as "local", so single-server behaviour
 * and legacy stored keys are unchanged.
 *
 * Kept dependency-free (apart from the shared state object) so it can be
 * imported from any module without creating import cycles. api.js re-exports
 * everything here. */

import { state } from './state.js';

export const LOCAL_SERVER = 'local';

/** Normalise a server id: undefined/null/""/"local" all mean the hub itself. */
export function normServer(server) {
    return (!server || server === LOCAL_SERVER) ? LOCAL_SERVER : String(server);
}

export function isLocalServer(server) {
    return normServer(server) === LOCAL_SERVER;
}

/** URL prefix for HTTP calls to `server`: "" for local, "/api/remote/{id}" otherwise. */
export function serverBase(server) {
    const s = normServer(server);
    return s === LOCAL_SERVER ? '' : `/api/remote/${encodeURIComponent(s)}`;
}

/** WebSocket origin + prefix for `server`, e.g. "wss://host/api/remote/ws1". */
export function serverWsBase(server) {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    return `${proto}//${location.host}${serverBase(server)}`;
}

/** Prefix an absolute-path API url ("/api/...") for `server`. */
export function serverUrl(url, server) {
    return serverBase(server) + url;
}

/** fetch() against `server`. Same contract as fetch (returns the Response). */
export function serverFetch(server, url, options) {
    return fetch(serverUrl(url, server), options);
}

/** The server a session/agent object belongs to ("local" when untagged). */
export function sessionServer(session) {
    return normServer(session && session.server);
}

/** Stable string key for (server, name). Local keys are the bare name, so keys
 *  persisted before the hub existed keep working. Remote keys can never equal a
 *  local name because server ids are slugs and contain no "/" or "@". */
export function identityKey(server, name) {
    const s = normServer(server);
    return s === LOCAL_SERVER ? String(name ?? '') : `@${s}/${name ?? ''}`;
}

/** DOM-id/attribute-safe version of identityKey. */
export function identityDomId(server, name) {
    return identityKey(server, name).replace(/[^A-Za-z0-9_-]/g, '_');
}

/** Path of the single-agent popout page: /agent/{id} (local) or /agent/{server}/{id}. */
export function agentPath(server, id) {
    const s = normServer(server);
    return s === LOCAL_SERVER
        ? `/agent/${encodeURIComponent(id)}`
        : `/agent/${encodeURIComponent(s)}/${encodeURIComponent(id)}`;
}

/** Parse /agent/{id} or /agent/{server}/{id}. Returns {server, id} or null. */
export function parseAgentPath(pathname) {
    const m = String(pathname || '').match(/^\/agent\/([^/]+)(?:\/([^/]+))?\/?$/);
    if (!m) return null;
    try {
        if (m[2] === undefined) return { server: LOCAL_SERVER, id: decodeURIComponent(m[1]) };
        return { server: normServer(decodeURIComponent(m[1])), id: decodeURIComponent(m[2]) };
    } catch { return null; }
}

/** Server of the currently selected session. */
export function currentServer() {
    return sessionServer(state.currentSession);
}

/** Server of the currently open team/board context (set when a board is opened). */
export function boardServer() {
    return normServer(state.currentBoardServer);
}

/** Resolve the server for a live session by its (unique) session id, falling
 *  back to a unique name match, then to the current selection. */
export function serverForSession(name, sessionId) {
    const list = state.liveSessions || [];
    if (sessionId) {
        const byId = list.find(s => s.session_id === sessionId);
        if (byId) return sessionServer(byId);
    }
    if (name) {
        const cur = state.currentSession;
        if (cur && cur.name === name) return sessionServer(cur);
        const byName = list.filter(s => s.name === name);
        if (byName.length === 1) return sessionServer(byName[0]);
    }
    return currentServer();
}

/* ── Team / group keys ─────────────────────────────────────────────────
 * Teams (boards) and sidebar groups are per-server too, so the UI keys them
 * with identityKey(server, name). Handlers that receive such a key unpack it
 * with splitKey() to learn which server to call. Local keys are the bare name,
 * so everything stored before the hub existed (collapse state, group order,
 * accent colours, folder tags) keeps matching. */

/** Unpack a key made by identityKey(): {server, name}. */
export function splitKey(key) {
    const k = String(key ?? '');
    if (k.startsWith('@')) {
        const i = k.indexOf('/');
        if (i > 1 && /^[a-z0-9-]{1,32}$/.test(k.slice(1, i))) return { server: k.slice(1, i), name: k.slice(i + 1) };
    }
    return { server: LOCAL_SERVER, name: k };
}

/** Display name of a key (drops the server part). */
export function keyLabel(key) {
    return splitKey(key).name;
}

/** Team key of a session ("" when it is not on a board). */
export function sessionTeamKey(s) {
    return s && s.board_project ? identityKey(sessionServer(s), s.board_project) : '';
}

/** True when session `s` belongs to the team identified by `teamKey`. */
export function inTeam(s, teamKey) {
    return !!teamKey && sessionTeamKey(s) === String(teamKey);
}

/** Folder-group key of a session (standalone agents are grouped by folder name). */
export function sessionFolderKey(s) {
    return identityKey(sessionServer(s), (s && s.name) || 'unknown');
}

/** fetch for a board endpoint: boardFetch(teamKey, '/messages', opts) ->
 *  /api/board/{name}/messages on the team's server. */
export function boardFetch(teamKey, suffix, options) {
    const { server, name } = splitKey(teamKey);
    return fetch(serverUrl(`/api/board/${encodeURIComponent(name)}${suffix || ''}`, server), options);
}

/** fetch for any team-scoped endpoint whose path embeds the bare board name:
 *  teamFetch(teamKey, n => `/api/teams/detail/${encodeURIComponent(n)}/knowledge`). */
export function teamFetch(teamKey, pathFn, options) {
    const { server, name } = splitKey(teamKey);
    return fetch(serverUrl(pathFn(name), server), options);
}

/** Server of whatever the user is looking at: the open team if any, else the
 *  selected agent. Use for resources (artifacts, files) that belong to the
 *  current view rather than to a named agent or team. */
export function contextServer() {
    return state.selectedTeam ? splitKey(state.selectedTeam).server : currentServer();
}

/* ── Hub mode ──────────────────────────────────────────────────────────
 * Hub features are a runtime switch on the server (coral --hub / CORAL_HUB=1).
 * /api/health reports {hub: bool}; it is read once at startup into state.hub.
 * With hub=false the UI never touches /api/servers or /api/remote/*. */

let _hubPromise = null;

/** Read /api/health once and cache the flag in state.hub. Safe to call often. */
export function initHub() {
    if (!_hubPromise) {
        _hubPromise = fetch('/api/health')
            .then(r => (r.ok ? r.json() : {}))
            .then(d => { state.hub = d.hub === true; return state.hub; })
            .catch(() => { state.hub = false; return false; });
    }
    return _hubPromise;
}

/** True when this server runs in hub mode (valid after initHub() resolved). */
export function isHub() {
    return state.hub === true;
}
