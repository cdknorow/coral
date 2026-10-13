/* Reachability of the server behind the selected agent (#3151). The live feed
 * marks an agent `stale` while its server is unreachable; the dashboard then
 * shows a banner, refuses sends (draft kept) and stops polling that agent. For
 * other gateway failures the polls back off (2 s doubling to 30 s) and reset as
 * soon as a request succeeds or the server is back. */

import { state } from './state.js';
import { normServer, sessionServer } from './server_base.js';
import { staleReason, serverEntry } from './server_status.js';

/** The selected session's live-list entry: the feed keeps its `stale` current
 *  (state.currentSession is a copy taken when the agent was selected). */
export function liveEntry(session) {
    if (!session) return null;
    const srv = normServer(sessionServer(session));
    return (state.liveSessions || []).find(s => s && s.session_id === session.session_id && normServer(sessionServer(s)) === srv) || null;
}

/** True while the session's server is unreachable: the feed marks the agent stale
 *  AND the server is not reported online. The second half matters because the
 *  dashboard's merge keeps fields a diff omits: a feed that clears staleness by
 *  dropping the key (not stale:false) would otherwise leave a recovered agent
 *  locked until a reload. The server list's status is sent on every change. */
export function sessionUnreachable(session) {
    const e = liveEntry(session);
    if (!e || e.stale !== true) return false;
    const srv = serverEntry(sessionServer(e));
    return !(srv && srv.status === 'online');
}

/** The banner/refusal text for an unreachable session, or ''. */
export function unreachableReason(session) {
    return sessionUnreachable(session) ? staleReason(liveEntry(session)) : '';
}

const GATEWAY = new Set([502, 503, 504]);
const BASE_MS = 2000, MAX_MS = 30000;
const _backoff = new Map(); // key -> { fails, until }

/** True while `key` is backing off after gateway failures. */
export function backoffBlocked(key, now = Date.now()) {
    const b = _backoff.get(key);
    return !!(b && now < b.until);
}

/** Record a response for `key`: a gateway failure extends the back-off, any
 *  other answer (or `ok`) clears it. Returns true when it was a gateway failure. */
export function backoffRecord(key, status, now = Date.now()) {
    if (!GATEWAY.has(status)) { _backoff.delete(key); return false; }
    const fails = (_backoff.get(key)?.fails || 0) + 1;
    _backoff.set(key, { fails, until: now + Math.min(MAX_MS, BASE_MS * 2 ** (fails - 1)) });
    return true;
}

/** Forget every back-off for `serverId` (it is reachable again). */
export function backoffReset(serverId) {
    const suffix = ':' + normServer(serverId);
    for (const k of [..._backoff.keys()]) if (k.endsWith(suffix)) _backoff.delete(k);
}

/** True when the agent view is on screen (another top tab is not shown instead). */
export function liveViewShown() {
    const v = document.getElementById('live-session-view');
    return !!v && v.style.display !== 'none';
}
