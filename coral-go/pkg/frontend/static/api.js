/* REST API fetch functions */

/* Multi-server routing (hub mode)
 * ------------------------------------------------------------------
 * Every per-agent and per-team resource is addressed by (server, name).
 * Calls for a remote server go through the hub proxy: serverBase(server)
 * returns "" for "local" and "/api/remote/{server}" otherwise, and the same
 * prefix applies to WebSockets (serverWsBase). Use apiFetch(url, {server}),
 * serverFetch(server, url, opts) or serverUrl(url, server); never hand-build
 * "/api/remote/...".
 *
 * Calls that are INTENTIONALLY hub-local (never proxied, whatever agent is
 * selected) because they describe the hub itself or its user, not an agent:
 *   - /api/settings, /api/settings/default-prompts (user preferences)
 *   - /api/themes*, /api/tags, /api/folder-tags, /api/views (hub UI data)
 *   - /api/scheduled/*, /api/workflows (hub automation)
 *   - /api/system/* (update check, privacy, API key,
 *     network info, database security, editors; system/status and
 *     system/cli-check are local unless launching on a remote, see WP6)
 *   - /api/servers* (the remote registry itself) and /api/remote/* (proxy)
 *   - /api/tracking/event
 *   - /api/token-usage*, /api/call-metrics/*, cost dashboard: aggregate
 *     views, "this server only" per the spec's non-goals
 *   - /api/sessions/live (merged list, already carries `server` per row),
 *     /api/sessions/history* on a local dashboard; the Cloud Hub's Chats
 *     explicitly scopes its history reads to the chosen linked computer
 *   - /api/templates/*, /api/agent-models, /api/teams/generate (hub catalogs)
 *   - static assets, service worker, artifact previews of hub files
 */

import { historyFetch, historyServer } from './history_source.js';
import { state, loadSessionDrafts } from './state.js';
import { renderLiveSessions, renderHistorySessions } from './render.js';
import { buildApiParams } from './search_filters.js';
import { serverUrl, initHub, fetchValidated, isCloud } from './server_base.js';
import { updateServersTabVisibility } from './servers_tab.js';
import { updateInboxTab } from './inbox_tab.js';
import { ageOutStored } from './local_store.js';

export {
    LOCAL_SERVER, initHub, isHub, normServer, isLocalServer, serverBase, serverWsBase, serverUrl,
    serverFetch, sessionServer, identityKey, identityDomId, agentPath, parseAgentPath,
    currentServer, boardServer, serverForSession,
} from './server_base.js';

/**
 * Thin wrapper around fetch that checks resp.ok and parses JSON.
 * Throws on non-2xx responses so callers get consistent error handling.
 * Use for all internal API calls. `options.server` routes the call to a
 * registered remote through the hub proxy (default: the hub itself).
 */
export async function apiFetch(url, options) {
    let init = options;
    let target = url;
    if (options && 'server' in options) {
        const { server, ...rest } = options;
        init = rest;
        target = serverUrl(url, server);
    }
    const resp = await fetch(target, init);
    if (!resp.ok) throw await httpError(resp);
    return resp.json();
}

// The error for a refused answer: the server's own reason when it gives one ({"error": "..."}),
// e.g. a per-action limit's "too many workflow runs, try again in 30 s" (#3435); otherwise the
// status and the body.
async function httpError(resp) {
    const text = await resp.text().catch(() => '');
    let reason = '';
    try { const body = JSON.parse(text); if (body && typeof body.error === 'string') reason = body.error; } catch { /* not JSON */ }
    const error = new Error(reason || `${resp.status}: ${text || resp.statusText}`);
    error.status = resp.status;
    error.body = text;
    return error;
}

/** Send JSON to a same-origin route and read the answer without throwing for an HTTP refusal (#3948, moved here from password_ui.js so a startup module can use it). Resolves
 *  {ok, status, data, retryAfter}; data is the parsed body or {}. A DELETE sends no body. */
export async function sendJson(method, url, body) {
    const resp = await fetch(url, { method, headers: method === 'DELETE' ? {} : { 'Content-Type': 'application/json' }, credentials: 'same-origin', body: method === 'DELETE' ? undefined : JSON.stringify(body || {}) });
    let data = {};
    try { data = await resp.json(); } catch { /* an empty or non-JSON body */ }
    return { ok: resp.ok, status: resp.status, data: data || {}, retryAfter: resp.headers.get('Retry-After') };
}

export function postJson(url, body) { return sendJson('POST', url, body); }

/** Request a same-origin JSON route and return its answer, or throw. The error is the server's own reason ({"error": "..."}) or "HTTP <status>", with .status and .data
 *  on it, so a caller that treats one status specially (a 409) reads e.status. A body, when there is one, is sent as JSON; an empty answer is {}. Unlike sendJson it throws
 *  for a refusal, and unlike apiFetch it names the status the way the settings dialogs always did. */
export async function requestJson(method, url, body) {
    const init = { method };
    if (body !== undefined) { init.headers = { 'Content-Type': 'application/json' }; init.body = JSON.stringify(body); }
    const resp = await fetch(url, init);
    const text = await resp.text().catch(() => '');
    let data = {};
    try { data = text ? JSON.parse(text) : {}; } catch (e) { if (resp.ok) throw e; }
    if (!resp.ok) {
        const error = new Error((data && typeof data.error === 'string' && data.error) || `HTTP ${resp.status}`);
        error.status = resp.status;
        error.data = data || {};
        throw error;
    }
    return data;
}

/** apiFetch for a polled GET that is often unchanged (#3439): sends the last answer's validator
 *  and reuses that answer on a 304 (fetchValidated). Errors as apiFetch. */
 async function apiFetchValidated(url) {
    const r = await fetchValidated(url, headers => fetch(url, { headers }));
    if (!r.ok) throw await httpError(r.resp);
    return JSON.parse(r.text);
}

// A load that is already in flight is shared: the page start asks for the list twice in the same moment (once on
// DOMContentLoaded, once when the startup poll finds nothing to wait for), and that was two full list fetches per
// page load. Only a caller with the same failure handler joins, so no failure goes unreported.
let _liveLoad = null;

export function loadLiveSessions(onFailure) {
    if (_liveLoad && _liveLoad.onFailure === onFailure) return _liveLoad.promise;
    const promise = _loadLiveSessions(onFailure).finally(() => { if (_liveLoad && _liveLoad.promise === promise) _liveLoad = null; });
    _liveLoad = { onFailure, promise };
    return promise;
}

async function _loadLiveSessions(onFailure) {
    let rendering = false;
    try {
        // /api/health (is this a hub?) and the list are asked for together: the list used to wait for the health answer, one more round trip
        // before the first paint (#4326). The list is read only once the flag is known.
        const hubReady = initHub();
        const raw = await apiFetchValidated("/api/sessions/live");
        await hubReady;
        // With remotes registered the list may arrive as {sessions, servers}.
        const sessions = Array.isArray(raw) ? raw : (raw && Array.isArray(raw.sessions) ? raw.sessions : null);
        if (!sessions) throw new SyntaxError('Invalid live session list');
        state.servers = (state.hub && raw && !Array.isArray(raw) && Array.isArray(raw.servers)) ? raw.servers : [];
        state.serversKnown = true;
        updateServersTabVisibility();
        state.liveSessions = sessions;
        updateInboxTab(); // the waiting number follows the list (#4718)
        // Drafts of sessions that have ended (and were left a day) go (#3681), at most hourly.
        const kept = ageOutStored({ liveIds: new Set(sessions.map(s => s && s.session_id).filter(Boolean)) });
        if (kept) loadSessionDrafts(kept);
        rendering = true;
        renderLiveSessions(state.liveSessions);
        return true;
    } catch (e) {
        console.error("Failed to load live sessions:", e);
        if (typeof onFailure === 'function') onFailure(rendering ? 'init_failed' : e.status ? 'sessions_fetch_http' : e instanceof SyntaxError ? 'sessions_fetch_invalid' : 'sessions_fetch_network');
        return false;
    }
}

export async function loadHistorySessionsPaged(page = 1, pageSize = 50) {
    try {
        const params = buildApiParams(page, pageSize);
        const source = historyServer();
        const response = await historyFetch(`/api/sessions/history?${params}`);
        if (!response.ok) throw await httpError(response);
        const data = await response.json();
        if (source !== historyServer()) return null;
        const sessions = data.sessions || data;
        renderHistorySessions(sessions, data.total, data.page, data.page_size);
        return data;
    } catch (e) {
        if (e.name === 'AbortError') return;
        console.error("Failed to load paged history sessions:", e);
        if (isCloud()) {
            renderHistorySessions([], 0, page, pageSize);
            const empty = document.querySelector('#history-sessions-list .empty-state');
            if (empty) empty.textContent = historyServer() ? 'Could not load chats from this computer.' : 'No computers online.';
        }
        return null;
    }
}

export async function loadHistoryMessages(sessionId, options = {}) {
    try {
        const params = new URLSearchParams();
        if (options.limit != null) params.set('limit', String(options.limit));
        if (options.offset != null) params.set('offset', String(options.offset));
        const qs = params.toString() ? `?${params}` : '';
        const source = historyServer();
        const response = await historyFetch(`/api/sessions/history/${encodeURIComponent(sessionId)}${qs}`);
        if (!response.ok) throw await httpError(response);
        const data = await response.json();
        return source === historyServer() ? data : null;
    } catch (e) {
        if (e.name === 'AbortError') return;
        console.error("Failed to load history messages:", e);
        return null;
    }
}
