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
    offline:          { label: 'Offline',          cls: 'unreachable',  hint: 'This server is not connected to the hub. Agents shown are the last known state; it reconnects by itself when its network is back.' },
    stale:            { label: 'Offline',          cls: 'unreachable',  hint: 'This server is not connected to the hub. Agents shown are the last known state; it reconnects by itself when its network is back.' },
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
    return shortServerName(e) || e.id;
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

// Cloud Hub update notice (#3059; coral-hub docs/proxy-feed.md "Update notice").
// A servers entry may carry coral_version, latest_coral and
// update: current | available | required | unknown. Only available and
// required show anything, and nothing is ever disabled. The link is this
// constant, never built from a field the server sent.
export const RELEASES_URL = 'https://github.com/cdknorow/coral/releases/latest';

/** {level, label, text} for an entry that needs an update, else null. */
export function updateNotice(e) {
    if (!e || (e.update !== 'available' && e.update !== 'required')) return null;
    const field = v => (typeof v === 'string' ? v.trim() : '');
    const latest = field(e.latest_coral);
    if (e.update === 'available') {
        return { level: 'available', label: 'Update', text: latest ? `Update available: ${latest}` : 'Update available' };
    }
    const current = field(e.coral_version);
    return {
        level: 'required', label: 'Update required',
        text: `Update required: this Coral${current ? ` (${current})` : ''} is too old for some Coral Hub features. `
            + (latest ? `Update to ${latest}.` : 'Update to the latest release.'),
    };
}

/** The update badge for a server row: a link to the releases page. '' when none. */
export function updateBadgeHtml(e) {
    const n = updateNotice(e);
    if (!n) return '';
    return `<a class="server-update-badge server-update-${n.level}" href="${escHtml(RELEASES_URL)}" target="_blank" rel="noopener noreferrer"`
        + ` title="${escHtml(n.text)}" aria-label="${escHtml(n.text)}">${escHtml(n.label)}</a>`;
}

/** The Coral version a servers entry reports: coral_version from the Cloud Hub,
 *  version from a self-hosted hub's remote. '' when unknown. */
export function serverVersion(e) {
    const v = e && (e.coral_version || e.version);
    return typeof v === 'string' ? v.trim() : '';
}

/** Multi-line hover text for a server: status, address, agent count, last contact. */
export function serverHoverText(id) {
    const e = serverEntry(id);
    const m = statusMeta(e.status);
    const local = normServer(e.id) === LOCAL_SERVER;
    // Archived agents are not counted (#3030); the hub's own count leaves them out too.
    const countOn = srv => (state.liveSessions || []).filter(x => x && !x.archived && normServer(x.server) === srv).length;
    const count = local
        ? countOn(LOCAL_SERVER)
        : (typeof e.agents === 'number' ? e.agents : countOn(normServer(e.id)));
    const lines = [`${e.label || e.id}: ${m.label}`];
    lines.push(local ? 'This server (the hub)' : (e.url || e.id));
    lines.push(`${count} agent${count === 1 ? '' : 's'}`);
    if (!local && serverVersion(e)) lines.push(`Version ${serverVersion(e)}`);
    if (!local) lines.push(`Last contact: ${formatLastSeen(e.last_seen)}`);
    if (!local && e.status !== 'online' && e.last_error) lines.push(`Error: ${e.last_error}`);
    if (!local && e.status !== 'online') lines.push(m.hint);
    const notice = local ? null : updateNotice(e);
    if (notice) lines.push(notice.text);
    return lines.join('\n');
}

/** Small "server name + status dot" badge. '' when labels are not shown. */
export function serverBadgeHtml(id, extraClass) {
    if (!showServerLabels()) return '';
    const e = serverEntry(id);
    const m = statusMeta(e.status);
    return `<span class="server-badge server-${m.cls}${extraClass ? ' ' + extraClass : ''}" data-server="${escHtml(e.id)}" title="${escHtml(serverHoverText(e.id))}">`
        + `<span class="server-dot" aria-hidden="true"></span>${escHtml(e.label || e.id)}`
        + `<span class="sr-only"> (${escHtml(m.label)})</span></span>`;
}

// ── Agent list servers UI (#3085): short names, the search-row server button
// and, with two or more servers, collapsible server group headers. Details
// (status, version, last contact, agents, update) are in a <details>
// disclosure, so touch and keyboard users reach them, not only a hover title.

/** The servers the agent list is about: the remotes, plus this server unless on the Cloud Hub. */
export function serversShown() {
    if (!showServerLabels()) return [];
    return [...(state.cloud === true ? [] : [serverEntry(LOCAL_SERVER)]), ...remoteServers()];
}

const IPV4 = /^\d{1,3}(\.\d{1,3}){3}$/;
const HOSTNAME = /^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$/;

function baseShortName(e) {
    const raw = String((e && (e.label || e.id)) || '').trim();
    if (!raw) return '';
    if (IPV4.test(raw) || raw.includes(':')) return raw;
    if (HOSTNAME.test(raw)) return raw.split('.')[0];
    return raw;
}

/** A short display name: the label (or id); a hostname-like label loses its domain
 *  (.local, .lan, .home or any other), an IP address or a renamed label stays.
 *  When two shown servers would get the same short name ('box.lan', 'box.home'),
 *  both keep their full labels so they can be told apart. */
export function shortServerName(e) {
    const short = baseShortName(e);
    if (!short || !e) return short;
    const clash = serversShown().some(x => x.id !== e.id && baseShortName(x) === short);
    return clash ? String(e.label || e.id) : short;
}

/** Agents on a server: the hub's count for a remote when it sent one, else the agent list's. Archived agents are not counted. */
export function serverAgentCount(e) {
    const local = normServer(e.id) === LOCAL_SERVER;
    const own = (state.liveSessions || []).filter(x => x && !x.archived && normServer(x.server) === normServer(e.id)).length;
    return local || typeof e.agents !== 'number' ? own : e.agents;
}

/** The details panel shared by the server button and the group headers. */
export function serverDetailsHtml(e) {
    const m = statusMeta(e.status);
    const local = normServer(e.id) === LOCAL_SERVER;
    const count = serverAgentCount(e);
    const rows = [
        ['Status', m.label],
        ['Name', e.label || e.id],
        local ? ['Server', 'This server (the hub)'] : (e.url ? ['Address', e.url] : null),
        ['Agents', String(count)],
        !local && serverVersion(e) ? ['Version', serverVersion(e)] : null,
        !local ? ['Last contact', formatLastSeen(e.last_seen)] : null,
        !local && e.status !== 'online' && e.last_error ? ['Error', e.last_error] : null,
    ].filter(Boolean);
    const notice = local ? null : updateNotice(e);
    return `<dl class="server-details-list">${rows.map(([k, v]) => `<dt>${escHtml(k)}</dt><dd>${escHtml(v)}</dd>`).join('')}</dl>`
        + (notice ? `<p class="server-details-update">${escHtml(notice.text)} ${updateBadgeHtml(e)}</p>` : '')
        + (!local && e.status !== 'online' ? `<p class="server-details-hint">${escHtml(m.hint)}</p>` : '');
}

/** Aggregate status for several servers: online only when every one is. */
function aggregateStatus(list) {
    if (list.every(e => e.status === 'online')) return 'online';
    return list.some(e => e.status === 'unauthorized' || e.status === 'key_unreadable') ? 'unauthorized' : 'unreachable';
}

/** The search-row server button ('● MacBook-Pro ▾' or '● 3 servers ▾'), '' without servers.
 *  `open` keeps the disclosure open across list re-renders. */
export function serverSwitcherHtml(open = false) {
    const list = serversShown();
    if (!list.length) return '';
    const single = list.length === 1;
    const e = single ? list[0] : null;
    const status = single ? e.status : aggregateStatus(list);
    const m = statusMeta(status);
    const label = single ? shortServerName(e) : `${list.length} servers`;
    const down = list.filter(x => x.status !== 'online').length;
    const aria = single ? `Server ${e.label || e.id}: ${m.label}` : `${list.length} servers${down ? `, ${down} not online` : ''}`;
    const body = single
        ? serverDetailsHtml(e) + `<button type="button" class="btn btn-small server-open-btn" data-server-open="${escHtml(e.id)}">Open in Servers</button>`
        : `<ul class="server-switcher-list">${list.map(x => {
            const xm = statusMeta(x.status);
            // Which server needs the update (#3059): a marker and the accessible name say so.
            const xn = normServer(x.id) === LOCAL_SERVER ? null : updateNotice(x);
            const upd = xn ? (xn.level === 'required' ? 'Update required' : 'Update available') : '';
            return `<li><button type="button" class="server-switcher-item server-${xm.cls}" data-server-open="${escHtml(x.id)}" aria-label="${escHtml(`${x.label || x.id}: ${xm.label}, ${serverAgentCount(x)} agents. Open in Servers${upd ? `. ${upd}` : ''}`)}">`
                + `<span class="server-dot" aria-hidden="true"></span><span class="server-switcher-name">${escHtml(shortServerName(x))}</span>`
                + (xn ? `<span class="server-update-marker server-update-${xn.level}">${xn.level === 'required' ? 'Update required' : 'Update'}</span>` : '')
                + `<span class="server-switcher-meta">${escHtml(xm.label)} · ${serverAgentCount(x)}</span></button></li>`;
        }).join('')}</ul>`
            + `<button type="button" class="btn btn-small server-open-btn" data-server-open="">Open Servers</button>`;
    // #3059: an update notice stays visible on the button; the link is in the panel.
    const notices = list.filter(x => normServer(x.id) !== LOCAL_SERVER).map(updateNotice).filter(Boolean);
    const level = notices.some(n => n.level === 'required') ? 'required' : (notices.length ? 'available' : '');
    const marker = level ? `<span class="server-update-marker server-update-${level}">${level === 'required' ? 'Update required' : 'Update'}</span>` : '';
    return `<details class="server-switcher server-${m.cls}" data-server-switcher${open ? ' open' : ''}>`
        + `<summary class="server-switcher-btn" aria-label="${escHtml(aria + (level ? `. ${level === 'required' ? 'Update required' : 'Update available'}` : ''))}" title="${escHtml(single ? serverHoverText(e.id) : aria)}">`
        + `<span class="server-dot" aria-hidden="true"></span><span class="server-switcher-label">${escHtml(label)}</span>`
        + marker + `<span class="server-switcher-caret" aria-hidden="true">▾</span></summary>`
        + `<div class="server-switcher-panel">${body}</div></details>`;
}

/** A collapsible server group header for the agent list (2+ servers). */
// A computer (monitor) icon for a server header (#3507: replaces the status dot).
const SERVER_ICON = '<svg class="server-group-icon" aria-hidden="true" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor"'
    + ' stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="12" rx="2"/><path d="M8 20h8M12 16v4"/></svg>';

export function serverGroupHeaderHtml(e, count, collapsed, detailsOpen = false) {
    const m = statusMeta(e.status);
    const name = shortServerName(e);
    // Not checked yet ('unknown') isn't shown as offline (QA's review of #3507).
    const off = e.status !== 'online' && e.status !== 'unknown' && normServer(e.id) !== LOCAL_SERVER;
    // The outermost level of the list, flush left: the icon and name first. No chevron (#3507 v4):
    // the row itself collapses, and aria-expanded says so.
    // An offline server is dimmed (no colored dot), and its state is in the tooltip and the label.
    return `<li class="server-group-header server-${m.cls}${off ? ' is-down' : ''}" data-server-group="${escHtml(e.id)}">`
        + `<button type="button" class="server-group-toggle" data-server-toggle="${escHtml(e.id)}" aria-expanded="${collapsed ? 'false' : 'true'}"`
        + ` title="${escHtml(`${e.label || e.id}: ${m.label}`)}"`
        + ` aria-label="${escHtml(`Server ${e.label || e.id}: ${m.label}, ${count} agents. ${collapsed ? 'Expand' : 'Collapse'}`)}">`
        + SERVER_ICON + `<span class="server-group-name">${escHtml(name)}</span>`
        + (off ? `<span class="server-group-state">${escHtml(m.label.toLowerCase())}</span>` : '')
        + ((n => n ? `<span class="server-update-marker server-update-${n.level}">${n.level === 'required' ? 'Update required' : 'Update'}</span>` : '')(normServer(e.id) === LOCAL_SERVER ? null : updateNotice(e)))
        + `<span class="session-group-count">${count}</span></button>`
        + `<details class="server-group-more" data-server-details="${escHtml(e.id)}"${detailsOpen ? ' open' : ''}>`
        + `<summary class="server-group-more-btn" aria-label="${escHtml(`Details for ${e.label || e.id}`)}" title="${escHtml(serverHoverText(e.id))}">⋮</summary>`
        + `<div class="server-switcher-panel">${serverDetailsHtml(e)}`
        + `<button type="button" class="btn btn-small server-open-btn" data-server-open="${escHtml(e.id)}">Open in Servers</button>`
        + `<button type="button" class="btn btn-small" data-server-toggle="${escHtml(e.id)}">${collapsed ? 'Expand' : 'Collapse'}</button></div></details>`
        + `</li>`;
}

/** Strip listing every server (local first) with its status. '' without remotes.
 *  (Replaced in the agent list by serverSwitcherHtml + group headers, #3085.) */
export function serverStripHtml() {
    if (!showServerLabels()) return '';
    const rows = [...(state.cloud === true ? [] : [serverEntry(LOCAL_SERVER)]), ...remoteServers()].map(e => {
        const m = statusMeta(e.status);
        const down = e.status !== 'online' && e.id !== LOCAL_SERVER;
        // A button: its details (status, version, last contact) are in the Servers
        // tab it opens, so they reach touch and keyboard users, not only a hover title.
        return `<li class="server-strip-item server-${m.cls}" data-server="${escHtml(e.id)}" title="${escHtml(serverHoverText(e.id))}">`
            + `<button type="button" class="server-strip-btn" data-server-open="${escHtml(e.id)}"`
            + ` aria-label="${escHtml(`${e.label || e.id}: ${m.label}. Open in Servers`)}">`
            + `<span class="server-dot" aria-hidden="true"></span>`
            + `<span class="server-strip-name">${escHtml(e.label || e.id)}</span>`
            + (down ? `<span class="server-strip-state">${escHtml(m.label)}</span>` : '')
            + `</button>`
            + (e.id === LOCAL_SERVER ? '' : updateBadgeHtml(e))
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

// The hub's proxy answers a request it could not pass to a server with
// {error, server} (pkg/hubproxy writeErr). Each known error gets a sentence that
// names the server; nothing a server sent is echoed (#3151).
const PROXY_ERRORS = {
    'remote unreachable': l => `${l} is unreachable. Check that Coral is running there and that this hub can reach it.`,
    'remote unavailable': l => `${l} is not available right now. Try again in a moment.`,
    'remote key unreadable': l => `This hub cannot read the stored API key for ${l}. Enter the key again in Servers.`,
    'remote rejected API key': l => `${l} rejected this hub's API key. Update the key in Servers.`,
    'remote address not allowed': l => `${l}'s address is not allowed. If it is on your LAN or tailnet, allow private network for it in Servers.`,
    'unknown server': l => `${l} is no longer registered with this hub.`,
    'too_many_streams': l => `${l} is busy. Try again in a moment.`,
};

/** Plain text for a hub-proxy failure talking to `serverId`, or '' when the
 *  response is not one (the caller then uses its own message). */
export function proxyErrorMessage(httpStatus, body, serverId) {
    const e = serverEntry(serverId);
    const label = (e && (e.label || e.id)) || 'The server';
    const code = body && typeof body === 'object' ? (body.error || body.code || '') : '';
    if (typeof code === 'string' && PROXY_ERRORS[code]) return PROXY_ERRORS[code](label);
    if (httpStatus === 502 || httpStatus === 503 || httpStatus === 504) return `${label} is not responding. Try again in a moment.`;
    return '';
}

/** The agent's own terminal no longer exists (409 terminal_gone, #3211): nothing was sent.
 *  Restart starts it in a fresh terminal (#3213), so the text says so (#3218). */
export const TERMINAL_GONE_MESSAGE = "The agent's terminal is gone. Restart it to continue.";

/** A plain sentence for a failed response: terminal_gone's fixed text, the proxy's
 *  mapped text, else the body's own error string (never raw JSON), else `fallback`.
 *  Reads the body. */
export async function responseErrorText(resp, serverId, fallback) {
    let body = null;
    try { body = await resp.clone().json(); } catch { /* not JSON */ }
    if (body && body.code === 'terminal_gone') return TERMINAL_GONE_MESSAGE;
    const mapped = proxyErrorMessage(resp.status, body, serverId);
    if (mapped) return mapped;
    if (body && typeof body.error === 'string' && body.error.trim()) return body.error.trim();
    return fallback;
}

/** responseErrorText plus the body's error code ('' when none), for callers that act on it. */
export async function responseError(resp, serverId, fallback) {
    let code = '';
    try { const b = await resp.clone().json(); if (b && typeof b.code === 'string') code = b.code; } catch { /* not JSON */ }
    return { text: await responseErrorText(resp, serverId, fallback), code };
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
