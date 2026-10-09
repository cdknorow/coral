/* Server status helpers for the multi-server hub UI.
 *
 * Pure and DOM-free (imports only the shared state and routing helpers) so the
 * sidebar renderer, the Servers settings view and the node tests can share it.
 * Every function is a no-op / returns '' unless hub mode is on AND at least one
 * remote is registered, so a standalone Coral renders exactly as before. */

import { state } from './state.js';
import { LOCAL_SERVER, normServer, isHub } from './server_base.js';

export const STATUS_META = {
    online:           { label: 'Online',           cls: 'online',       hint: 'Connected' },
    unreachable:      { label: 'Unreachable',      cls: 'unreachable',  hint: 'The hub cannot reach this server. Agents shown are the last known state.' },
    unauthorized:     { label: 'Key rejected',     cls: 'unauthorized', hint: 'The server rejected the stored API key. Edit the server and enter a new key.' },
    key_unreadable:   { label: 'Key unreadable',   cls: 'unauthorized', hint: 'The stored API key cannot be decrypted on this hub. Edit the server and re-enter the key.' },
    version_mismatch: { label: 'Version mismatch', cls: 'unreachable',  hint: 'This server runs a version older than the hub supports.' },
    unknown:          { label: 'Unknown',          cls: 'unknown',      hint: 'Not checked yet' },
};

export function statusMeta(status) {
    return STATUS_META[status] || STATUS_META.unknown;
}

export function escHtml(v) {
    return String(v ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

/** Registered remotes from the live-list `servers` summary (never includes local). */
export function remoteServers() {
    if (!isHub() || !Array.isArray(state.servers)) return [];
    return state.servers.filter(s => s && s.id && normServer(s.id) !== LOCAL_SERVER);
}

/** True when the hub UI should label things by server. */
export function showServerLabels() {
    return remoteServers().length > 0;
}

/** Summary entry for a server id (local is synthesised as online). */
export function serverEntry(id) {
    const sid = normServer(id);
    if (sid === LOCAL_SERVER) return { id: LOCAL_SERVER, label: 'Local', status: 'online' };
    const found = remoteServers().find(s => s.id === sid);
    return found || { id: sid, label: sid, status: 'unknown' };
}

export function serverLabel(id) {
    const e = serverEntry(id);
    return e.label || e.id;
}

export function serverStatus(id) {
    return serverEntry(id).status || 'unknown';
}

/** A session is stale when its remote is not online (last-known data). */
export function isStaleSession(s) {
    return !!(s && s.stale === true);
}

/** Tooltip/explanation for a stale session. */
export function staleReason(s) {
    const e = serverEntry(s && s.server);
    return `${e.label || e.id} is ${statusMeta(e.status).label.toLowerCase()}. This agent shows its last known state and cannot be used until the server is back.`;
}

/** Small "server name + status dot" badge. '' when labels are not shown. */
export function serverBadgeHtml(id, extraClass) {
    if (!showServerLabels()) return '';
    const e = serverEntry(id);
    const m = statusMeta(e.status);
    return `<span class="server-badge server-${m.cls}${extraClass ? ' ' + extraClass : ''}" data-server="${escHtml(e.id)}" title="${escHtml((e.label || e.id) + ': ' + m.label)}">`
        + `<span class="server-dot" aria-hidden="true"></span>${escHtml(e.label || e.id)}`
        + `<span class="sr-only"> (${escHtml(m.label)})</span></span>`;
}

/** Strip listing every server (local first) with its status. '' without remotes. */
export function serverStripHtml() {
    if (!showServerLabels()) return '';
    const rows = [serverEntry(LOCAL_SERVER), ...remoteServers()].map(e => {
        const m = statusMeta(e.status);
        const down = e.status !== 'online' && e.id !== LOCAL_SERVER;
        return `<li class="server-strip-item server-${m.cls}" data-server="${escHtml(e.id)}" title="${escHtml(m.hint)}">`
            + `<span class="server-dot" aria-hidden="true"></span>`
            + `<span class="server-strip-name">${escHtml(e.label || e.id)}</span>`
            + (down ? `<span class="server-strip-state">${escHtml(m.label)}</span>` : '')
            + `</li>`;
    }).join('');
    return `<li class="server-strip-row" aria-label="Servers"><ul class="server-strip">${rows}</ul></li>`;
}

/** Stable partition: local-server group keys first (keeps relative order). */
export function localFirst(entries) {
    if (!showServerLabels()) return entries;
    const isLocalKey = k => !String(k).startsWith('@');
    return [...entries.filter(e => isLocalKey(e[0])), ...entries.filter(e => !isLocalKey(e[0]))];
}

/** Readable message for a failed /api/servers call. `body` is the parsed JSON (or {}). */
export function describeServerError(httpStatus, body) {
    const b = body || {};
    const st = b.status;
    const msg = b.error || '';
    if (httpStatus === 409 && (st === 'key_unreadable' || /cannot be decrypted/i.test(msg))) {
        return 'The stored API key cannot be decrypted on this hub (the secret key file changed or is missing). Enter the API key again to save it.';
    }
    if (httpStatus === 409) return msg || 'A server with that id already exists.';
    if (httpStatus === 502) {
        if (st === 'unauthorized') return 'The server rejected the API key. Check the key and try again.';
        if (st === 'unreachable') return `Could not reach the server${msg ? ' (' + msg + ')' : ''}. Check the URL, and tick "Allow private network" if it is on your LAN or tailnet.`;
        return msg ? `The server check failed: ${msg}` : 'The server check failed.';
    }
    if (httpStatus === 404) return 'Hub features are not enabled on this server, or the server no longer exists.';
    return msg || `Request failed (HTTP ${httpStatus}).`;
}

/** Relative "x ago" for an RFC3339 timestamp; '' / 'never' for empty. */
export function formatLastSeen(ts, now = Date.now()) {
    if (!ts) return 'never';
    const t = Date.parse(ts);
    if (Number.isNaN(t)) return 'never';
    const s = Math.max(0, Math.round((now - t) / 1000));
    if (s < 10) return 'just now';
    if (s < 60) return `${s}s ago`;
    if (s < 3600) return `${Math.floor(s / 60)}m ago`;
    if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
    return `${Math.floor(s / 86400)}d ago`;
}

/** Valid server id slug (matches the backend). */
export function isValidServerId(id) {
    return /^[a-z0-9-]{1,32}$/.test(id) && id !== LOCAL_SERVER;
}
