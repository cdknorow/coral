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
 *   - /api/webhooks*, /api/scheduled/*, /api/workflows (hub automation)
 *   - /api/license/*, /api/system/* (licence, update check, privacy, API key,
 *     network info, database security, editors; system/status and
 *     system/cli-check are local unless launching on a remote, see WP6)
 *   - /api/servers* (the remote registry itself) and /api/remote/* (proxy)
 *   - /api/tracking/event
 *   - /api/token-usage*, /api/call-metrics/*, cost dashboard: aggregate
 *     views, "this server only" per the spec's non-goals
 *   - /api/sessions/live (merged list, already carries `server` per row),
 *     /api/sessions/history* (the hub's own history database)
 *   - /api/templates/*, /api/agent-models, /api/teams/generate (hub catalogs)
 *   - static assets, service worker, artifact previews of hub files
 */

import { state } from './state.js';
import { renderLiveSessions, renderHistorySessions } from './render.js';
import { buildApiParams } from './search_filters.js';
import { serverUrl, serverForSession, initHub } from './server_base.js';

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
    if (!resp.ok) {
        const text = await resp.text().catch(() => '');
        const error = new Error(`${resp.status}: ${text || resp.statusText}`);
        error.status = resp.status;
        throw error;
    }
    return resp.json();
}

export async function loadLiveSessions(onFailure) {
    let rendering = false;
    try {
        await initHub();
        const raw = await apiFetch("/api/sessions/live");
        // With remotes registered the list may arrive as {sessions, servers}.
        const sessions = Array.isArray(raw) ? raw : (raw && Array.isArray(raw.sessions) ? raw.sessions : null);
        if (!sessions) throw new SyntaxError('Invalid live session list');
        state.servers = (state.hub && raw && !Array.isArray(raw) && Array.isArray(raw.servers)) ? raw.servers : [];
        state.liveSessions = sessions;
        rendering = true;
        renderLiveSessions(state.liveSessions);
        return true;
    } catch (e) {
        console.error("Failed to load live sessions:", e);
        if (typeof onFailure === 'function') onFailure(rendering ? 'init_failed' : e.status ? 'sessions_fetch_http' : e instanceof SyntaxError ? 'sessions_fetch_invalid' : 'sessions_fetch_network');
        return false;
    }
}

export async function loadHistorySessions() {
    try {
        const data = await apiFetch("/api/sessions/history");
        // Handle new paginated response shape
        const sessions = data.sessions || data;
        renderHistorySessions(sessions, data.total, data.page, data.page_size);
    } catch (e) {
        console.error("Failed to load history sessions:", e);
    }
}

export async function loadHistorySessionsPaged(page = 1, pageSize = 50) {
    try {
        const params = buildApiParams(page, pageSize);
        const data = await apiFetch(`/api/sessions/history?${params}`);
        const sessions = data.sessions || data;
        renderHistorySessions(sessions, data.total, data.page, data.page_size);
        return data;
    } catch (e) {
        console.error("Failed to load paged history sessions:", e);
        return null;
    }
}

export async function loadLiveSessionDetail(name, agentType, sessionId, options) {
    try {
        const server = (options && options.server) || serverForSession(name, sessionId);
        const params = new URLSearchParams();
        if (agentType) params.set("agent_type", agentType);
        if (sessionId) params.set("session_id", sessionId);
        const qs = params.toString() ? `?${params}` : "";
        return await apiFetch(`/api/sessions/live/${encodeURIComponent(name)}${qs}`, { ...options, server });
    } catch (e) {
        if (e.name === 'AbortError') return null;
        console.error("Failed to load session detail:", e);
        return null;
    }
}

export async function loadHistoryMessages(sessionId, options = {}) {
    try {
        const params = new URLSearchParams();
        if (options.limit != null) params.set('limit', String(options.limit));
        if (options.offset != null) params.set('offset', String(options.offset));
        const qs = params.toString() ? `?${params}` : '';
        return await apiFetch(`/api/sessions/history/${encodeURIComponent(sessionId)}${qs}`);
    } catch (e) {
        console.error("Failed to load history messages:", e);
        return null;
    }
}
