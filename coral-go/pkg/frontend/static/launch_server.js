/* Launch-target helpers for the multi-server hub (spec section 5).
 *
 * Pure logic, no DOM: which servers can be picked as a launch target, how a
 * launch request is routed, and how the UI recognises the new agent once the
 * hub's poller has picked it up. modals.js owns the DOM; this module is kept
 * importable from node so the rules can be unit tested.
 *
 * Invariants:
 *  - A launch for server X is sent to X's own POST /api/sessions/launch[-team]
 *    through the hub proxy. If X cannot be used the launch is refused with an
 *    error; it is NEVER sent to the hub itself instead.
 *  - Every path in the payload (working_dir) is interpreted by the target
 *    server, so the UI must never carry a directory across a server change. */

import { state } from './state.js';
import { LOCAL_SERVER, normServer, isLocalServer, serverFetch, splitKey } from './server_base.js';

/** Statuses that make a registered remote unusable as a launch target, with
 *  the reason shown next to it in the dropdown. */
export const UNUSABLE_STATUS_REASON = {
    unreachable: 'offline',
    unauthorized: 'API key rejected',
    key_unreadable: 'API key unreadable',
};

/** Registered remotes (everything except the hub itself). */
export function remoteServers(servers) {
    return (Array.isArray(servers) ? servers : []).filter(s => s && s.id && s.id !== LOCAL_SERVER);
}

/** The picker is only offered in hub mode when at least one remote is not unreachable. */
export function pickerVisible(hub, servers) {
    return hub === true && remoteServers(servers).some(s => s.status !== 'unreachable');
}

/** Dropdown entries: Local first, then each remote, unusable ones disabled with a reason. */
export function pickerOptions(servers) {
    const opts = [{ id: LOCAL_SERVER, label: 'Local (this machine)', disabled: false, reason: '' }];
    for (const s of remoteServers(servers)) {
        const reason = UNUSABLE_STATUS_REASON[s.status] || '';
        const name = s.label || s.id;
        opts.push({ id: s.id, label: reason ? `${name} (${reason})` : name, disabled: !!reason, reason });
    }
    return opts;
}

/** Human label of a server id ("Local" for the hub). */
export function serverLabel(server, servers) {
    const id = normServer(server);
    if (id === LOCAL_SERVER) return 'Local';
    const hit = remoteServers(servers).find(s => s.id === id);
    return (hit && hit.label) || id;
}

/** Why `server` cannot be launched on right now, or "" when it can. */
export function launchBlockReason(server, hub, servers) {
    const id = normServer(server);
    if (id === LOCAL_SERVER) return '';
    if (hub !== true) return 'Hub mode is off; remote servers are unavailable.';
    const hit = remoteServers(servers).find(s => s.id === id);
    if (!hit) return `Server "${id}" is not registered.`;
    const reason = UNUSABLE_STATUS_REASON[hit.status];
    return reason ? `Server "${hit.label || id}" is unavailable (${reason}).` : '';
}

/** Keep a stored selection only while it is still a usable choice. */
export function coerceSelection(server, hub, servers) {
    const id = normServer(server);
    return launchBlockReason(id, hub, servers) ? LOCAL_SERVER : id;
}

/** localStorage key of the recent-directories list of `server`. Local keeps the
 *  pre-hub key so existing history stays; a remote's paths never mix into it. */
export function recentDirsKey(server) {
    const id = normServer(server);
    return id === LOCAL_SERVER ? 'coral-recent-dirs' : `coral-recent-dirs:${id}`;
}

/** Bare board/team name from a UI key ("@srv/team" -> "team"). Payloads sent to
 *  a server carry the name that server knows, never the hub's composite key. */
export function bareBoardName(key) {
    return splitKey(key).name;
}

/** Parse a launch response body that may be empty or non-JSON (proxy/gateway errors). */
export function parseLaunchBody(text) {
    if (!text) return null;
    try { const v = JSON.parse(text); return v && typeof v === 'object' ? v : null; } catch { return null; }
}

/**
 * Send a launch to `server`.
 * Resolves {ok, status, data, error, demoLimit}; it does not throw for HTTP or
 * network failures (error carries the message to show). When `server` is not
 * usable nothing is sent at all.
 */
export async function postLaunch(server, path, payload, { hub = state.hub, servers = state.servers, fetchFn = serverFetch } = {}) {
    const id = normServer(server);
    const blocked = launchBlockReason(id, hub, servers);
    if (blocked) return { ok: false, status: 0, data: null, error: blocked, demoLimit: false, sent: false };
    let resp;
    try {
        resp = await fetchFn(id, path, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload),
        });
    } catch (e) {
        const where = isLocalServer(id) ? '' : ` on ${serverLabel(id, servers)}`;
        return { ok: false, status: 0, data: null, error: `Failed to launch${where}: ${e && e.message ? e.message : 'network error'}`, demoLimit: false, sent: true };
    }
    const data = parseLaunchBody(await resp.text().catch(() => ''));
    const status = resp.status;
    if (status === 403 && isLocalServer(id)) {
        return { ok: false, status, data, error: (data && data.error) || 'Demo limit reached', demoLimit: true, sent: true };
    }
    if (!resp.ok || (data && data.error)) {
        const error = (data && data.error) || `Launch failed (HTTP ${status})`;
        return { ok: false, status, data, error: isLocalServer(id) ? error : `${serverLabel(id, servers)}: ${error}`, demoLimit: false, sent: true };
    }
    return { ok: true, status, data: data || {}, error: '', demoLimit: false, sent: true };
}

/** True when the polled live list already contains what a launch created. */
export function launchedVisible(sessions, server, { sessionName, sessionId, boardName } = {}) {
    const id = normServer(server);
    return (Array.isArray(sessions) ? sessions : []).some(s => {
        if (normServer(s && s.server) !== id) return false;
        if (sessionId && s.session_id === sessionId) return true;
        if (sessionName && s.name === sessionName) return true;
        return !!boardName && !sessionName && !sessionId && s.board_project === boardName;
    });
}

/**
 * After a successful launch on a remote, the hub only learns of the agent on
 * its next poll. Re-run `refresh` (which should reload the live list) until the
 * new agent shows up or the schedule runs out. Returns whether it was seen.
 */
export async function waitForLaunched(server, target, refresh, {
    delays = [800, 1500, 2500, 3500, 5000], getSessions = () => state.liveSessions, sleep = ms => new Promise(r => setTimeout(r, ms)),
} = {}) {
    for (const d of delays) {
        await sleep(d);
        try { await refresh(); } catch { /* keep polling */ }
        if (launchedVisible(getSessions(), server, target)) return true;
    }
    return false;
}
