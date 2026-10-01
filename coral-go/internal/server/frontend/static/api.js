/* REST API fetch functions */

import { state } from './state.js';
import { renderLiveSessions, renderHistorySessions } from './render.js';
import { buildApiParams } from './search_filters.js';

/**
 * Thin wrapper around fetch that checks resp.ok and parses JSON.
 * Throws on non-2xx responses so callers get consistent error handling.
 * Use for all internal API calls.
 */
export async function apiFetch(url, options) {
    const resp = await fetch(url, options);
    if (!resp.ok) {
        const text = await resp.text().catch(() => '');
        throw new Error(`${resp.status}: ${text || resp.statusText}`);
    }
    return resp.json();
}

export async function loadLiveSessions() {
    try {
        state.liveSessions = await apiFetch("/api/sessions/live");
        renderLiveSessions(state.liveSessions);
    } catch (e) {
        console.error("Failed to load live sessions:", e);
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
        const params = new URLSearchParams();
        if (agentType) params.set("agent_type", agentType);
        if (sessionId) params.set("session_id", sessionId);
        const qs = params.toString() ? `?${params}` : "";
        return await apiFetch(`/api/sessions/live/${encodeURIComponent(name)}${qs}`, options);
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
