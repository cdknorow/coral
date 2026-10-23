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

import { importOnce } from './import_once.js';
import { state } from './state.js';
import { LOCAL_SERVER, normServer, isLocalServer, serverFetch, splitKey } from './server_base.js';
import { storeKey } from './local_store.js';

/** Statuses that make a registered remote unusable as a launch target, with
 *  the reason shown next to it in the dropdown. */
 const UNUSABLE_STATUS_REASON = {
    unreachable: 'offline',
    offline: 'offline',
    stale: 'offline',
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
    // The Cloud Hub runs no agents of its own: only the user's servers are targets.
    const opts = state.cloud === true ? [] : [{ id: LOCAL_SERVER, label: 'Local (this machine)', disabled: false, reason: '' }];
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
export const CONNECT_FIRST = 'Connect a computer first';

/** What the hub's owned-machine list says, once it has loaded (a list that has not loaded, or failed, is null: never read as an empty account):
 *  'none' (no machine is connected, the first run), 'offline' (machines, but none can be used) or 'ready' (one can). Null off the hub. */
export function machineState(servers = state.servers) {
    if (state.cloud !== true || state.serversKnown !== true) return null;
    const remotes = remoteServers(servers);
    if (!remotes.length) return 'none';
    return remotes.some(s => !UNUSABLE_STATUS_REASON[s.status]) ? 'ready' : 'offline';
}

/** Coral Hub on its first run: the list loaded and no machine is connected. Anything the hub would do on a machine has nowhere to go. */
export function noMachine(servers = state.servers) {
    return machineState(servers) === 'none';
}

const MACHINES_OFFLINE = 'Your computers are offline';
/** What a machine-dependent action (Browse) says when there is nothing to run it on, or '' when there is. */
export function machineBlock(servers = state.servers) {
    const m = machineState(servers);
    return m === 'none' ? CONNECT_FIRST : m === 'offline' ? MACHINES_OFFLINE : '';
}

export function launchBlockReason(server, hub, servers) {
    const id = normServer(server);
    if (id === LOCAL_SERVER) return state.cloud === true ? (noMachine(servers) ? CONNECT_FIRST : 'The Coral Hub has no local agents. Choose one of your servers.') : '';
    if (hub !== true) return 'Hub mode is off; remote servers are unavailable.';
    const hit = remoteServers(servers).find(s => s.id === id);
    if (!hit) return `Server "${id}" is not registered.`;
    const reason = UNUSABLE_STATUS_REASON[hit.status];
    return reason ? `Server "${hit.label || id}" is unavailable (${reason}).` : '';
}

/** Keep a stored selection only while it is still a usable choice. */
export function coerceSelection(server, hub, servers) {
    const id = normServer(server);
    if (!launchBlockReason(id, hub, servers)) return id;
    if (state.cloud === true) { // no Local to fall back to: first usable server
        const ok = remoteServers(servers).find(s => !launchBlockReason(s.id, hub, servers));
        if (ok) return ok.id;
    }
    return LOCAL_SERVER;
}

/** localStorage key of the recent-directories list of `server`. Local keeps the
 *  pre-hub key so existing history stays; a remote's paths never mix into it. On Coral Hub it is
 *  the signed-in account's (local_store.js, #3681). */
export function recentDirsKey(server) {
    const id = normServer(server);
    return storeKey(id === LOCAL_SERVER ? 'coral-recent-dirs' : `coral-recent-dirs:${id}`);
}

/** Bare board/team name from a UI key ("@srv/team" -> "team"). Payloads sent to
 *  a server carry the name that server knows, never the hub's composite key. */
export function bareBoardName(key) {
    return splitKey(key).name;
}

/** Parse a launch response body that may be empty or non-JSON (proxy/gateway errors). */
 function parseLaunchBody(text) {
    if (!text) return null;
    try { const v = JSON.parse(text); return v && typeof v === 'object' ? v : null; } catch { return null; }
}

/** The access-refusal message for a launch response ('' when it is not one).
 *  cloud_servers.js owns the wording (refusalMessage); it is loaded only when a
 *  403 arrives, so this module stays light. */
async function accessRefusal(status, body, serverName) {
    if (status !== 403 || !body || typeof body !== 'object') return '';
    if (!['access_mode', 'launch_disabled', 'local_mode'].includes(body.error)) return '';
    try {
        const { refusalMessage } = await importOnce('./cloud_servers.js');
        return refusalMessage(status, body, serverName) || '';
    } catch {
        return '';
    }
}

/**
 * Send a launch to `server`.
 * Resolves {ok, status, data, error, refused}; it does not throw for HTTP or
 * network failures (error carries the message to show). When `server` is not
 * usable nothing is sent at all.
 */
export async function postLaunch(server, path, payload, { hub = state.hub, servers = state.servers, fetchFn = serverFetch } = {}) {
    const id = normServer(server);
    const blocked = launchBlockReason(id, hub, servers);
    if (blocked) return { ok: false, status: 0, data: null, error: blocked, sent: false };
    let resp;
    try {
        resp = await fetchFn(id, path, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload),
        });
    } catch (e) {
        const where = isLocalServer(id) ? '' : ` on ${serverLabel(id, servers)}`;
        return { ok: false, status: 0, data: null, error: `Failed to launch${where}: ${e && e.message ? e.message : 'network error'}`, sent: true };
    }
    const data = parseLaunchBody(await resp.text().catch(() => ''));
    const status = resp.status;
    // An access refusal (the hub's or the machine's mode is too low) reads as the
    // same "Blocked: ..." message everywhere, never as the raw error code.
    const refusal = await accessRefusal(status, data, isLocalServer(id) ? '' : serverLabel(id, servers));
    if (refusal) {
        return { ok: false, status, data, error: refusal, sent: true, refused: true };
    }
    if (!resp.ok || (data && data.error)) {
        const error = (data && data.error) || `Launch failed (HTTP ${status})`;
        return { ok: false, status, data, error: isLocalServer(id) ? error : `${serverLabel(id, servers)}: ${error}`, sent: true };
    }
    return { ok: true, status, data: data || {}, error: '', sent: true };
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
