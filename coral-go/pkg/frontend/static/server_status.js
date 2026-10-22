/* Server status helpers for the multi-server hub UI.
 *
 * Pure and DOM-free (imports only the shared state and routing helpers) so the
 * sidebar renderer, the Servers settings view and the node tests can share it.
 * Every function is a no-op / returns '' unless hub mode is on AND at least one
 * remote is registered, so a standalone Coral renders exactly as before. */

import { state } from './state.js';
import { LOCAL_SERVER, normServer, isHub } from './server_base.js';
import { launchBlockReason } from './launch_server.js';

 const STATUS_META = {
    online:           { label: 'Online',           cls: 'online',       hint: 'Connected' },
    unreachable:      { label: 'Unreachable',      cls: 'unreachable',  hint: 'The hub cannot reach this server. Agents shown are the last known state.' },
    unauthorized:     { label: 'Key rejected',     cls: 'unauthorized', hint: 'The server rejected the stored API key. Edit the server and enter a new key.' },
    key_unreadable:   { label: 'Key unreadable',   cls: 'unauthorized', hint: 'The stored API key cannot be decrypted on this hub. Edit the server and re-enter the key.' },
    host_refused:     { label: 'Host refused',     cls: 'unreachable',  hint: 'The server refused the host name the hub uses for it (its protection against DNS rebinding). Through a port forward, edit the server to use 127.0.0.1 or localhost.' },
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
export const DOWNLOAD_URL = 'https://coralhub.dev/coral.html#download';
export const RELEASES_URL = DOWNLOAD_URL; // the update badge's link: the download page, not GitHub's releases
const VERSION_SHAPE = /^v?\d{1,9}\.\d{1,9}\.\d{1,9}(?:-[0-9A-Za-z.]{1,40})?$/;

/** {level, label, text} for an entry that needs an update, else null. */
export function updateNotice(e) {
    if (!e || (e.update !== 'available' && e.update !== 'required')) return null;
    // The same shape the server accepts for a GitHub release tag (updatecheck.tagShape, #4133): a field that
    // isn't a version is not shown at all, so a hostile or broken Hub can't put its own sentence in the badge.
    const field = v => { const t = typeof v === 'string' ? v.trim() : ''; return VERSION_SHAPE.test(t) ? t : ''; };
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

/** The Hub's hint for a machine below its minimum Coral version: "Update Coral to <min_coral>" (#3915). The feed's servers entry carries
 *  below_minimum (true only when below) and min_coral; absent fields give ''. It is a hint for the machine's owner, not a gate, and
 *  min_coral comes from outside this page: it is only ever shown as text (callers escape it or set textContent), never as markup or a
 *  link, and is cut to 32 characters. */
export function belowMinimumText(e) {
    if (!e || e.below_minimum !== true) return '';
    const min = typeof e.min_coral === 'string' ? e.min_coral.trim().slice(0, 32) : '';
    return min ? `Update Coral to ${min}` : 'Update Coral';
}

/** The version without its build suffix: 1.4.129-dev+f9fef71f.dirty reads 1.4.129-dev (a release build is just 1.4.134). */
export function shortVersion(v) {
    const t = String(v || '').trim();
    const i = t.indexOf('+');
    return i > 0 ? t.slice(0, i) : t;
}

/** The details panel shared by the Servers list and the group headers (#4932: the search-row pill was removed). A two-column grid with muted labels; the version is cut before "+", with the full string
 *  in its tooltip and accessible name; the one Update control sits at the bottom (the header only carries a chip that says an update exists). */
export function serverDetailsHtml(e) {
    const m = statusMeta(e.status);
    const local = normServer(e.id) === LOCAL_SERVER;
    const count = serverAgentCount(e);
    const full = !local ? serverVersion(e) : '';
    const rows = [
        ['Status', m.label],
        ['Name', e.label || e.id],
        local ? ['Server', 'This server (the hub)'] : (e.url ? ['Address', e.url] : null),
        ['Agents', String(count)],
        full ? ['Version', shortVersion(full), full] : null,
        !local ? ['Last contact', formatLastSeen(e.last_seen)] : null,
        !local && e.status !== 'online' && e.last_error ? ['Error', e.last_error] : null,
    ].filter(Boolean);
    const dd = ([k, v, fullText]) => (fullText && fullText !== v
        ? `<dd class="server-version" tabindex="0" title="${escHtml(fullText)}" aria-label="${escHtml(`${k} ${v}, full build ${fullText}`)}">${escHtml(v)}</dd>`
        : `<dd>${escHtml(v)}</dd>`);
    const notice = local ? null : updateNotice(e);
    const latestText = notice && notice.level === 'available' ? (/^Update available: (.+)$/.exec(notice.text) || [])[1] || '' : '';
    return `<dl class="server-details-list">${rows.map(r => `<dt>${escHtml(r[0])}</dt>${dd(r)}`).join('')}</dl>`
        + (notice ? `<p class="server-details-update">${notice.level === 'available' && latestText ? `<span>Update to <strong>${escHtml(latestText)}</strong></span>` : `<span>${escHtml(notice.text)}</span>`} ${updateBadgeHtml(e)}</p>` : '')
        + (!local && belowMinimumText(e) ? `<p class="server-details-update server-below-minimum">${escHtml(belowMinimumText(e))}</p>` : '')
        + (!local && e.status !== 'online' ? `<p class="server-details-hint">${escHtml(m.hint)}</p>` : '');
}

/** One row at the top of a server's menu: Launch agent, Launch team, and Open in Servers (a text link when the row is narrow); Open folder is a fourth button on the hub's own machine only (a pinned folder lives
 *  on this Coral, pinned_folders.js), and elsewhere a one-line hint says to choose the folder when launching an agent. Each launch opens the launch flow with this server chosen; one that cannot run is disabled
 *  and the reason is said once under the row (launch_server.js decides: offline, key rejected, no hub). */
function serverLaunchActionsHtml(e) {
    const id = normServer(e.id);
    const local = id === LOCAL_SERVER;
    const blocked = launchBlockReason(id, isHub(), state.servers);
    const actions = [['agent', 'Launch agent'], ['team', 'Launch team']];
    if (local) actions.push(['folder', 'Open folder']);
    const btn = ([kind, label]) => `<button type="button" class="server-launch-btn${kind === 'agent' ? ' server-launch-primary' : ''}" data-server-launch="${escHtml(kind)}" data-server-id="${escHtml(e.id)}"`
        + (blocked ? ` disabled aria-disabled="true" title="${escHtml(blocked)}"` : '') + `>${label}</button>`;
    return `<div class="server-launch-actions" role="group" aria-label="${escHtml(`Launch on ${e.label || e.id}`)}">`
        + actions.map(btn).join('')
        + `<button type="button" class="server-open-btn" data-server-open="${escHtml(e.id)}">Open in Servers</button>`
        + `</div>`
        + (blocked ? `<p class="server-launch-note">${escHtml(blocked)}</p>` : '')
        + (!local && !blocked ? `<p class="server-launch-hint">To use a folder, choose it when you launch an agent.</p>` : '');
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
        + ` aria-label="${escHtml(`Server ${e.label || e.id}: ${m.label}, ${count} agents`)}">`
        + SERVER_ICON + `<span class="server-group-name">${escHtml(name)}</span>`
        + (off ? `<span class="server-group-state">${escHtml(m.label.toLowerCase())}</span>` : '')
        + ((n => n ? `<span class="server-update-marker server-update-${n.level}">${n.level === 'required' ? 'Update required' : 'Update'}</span>` : '')(normServer(e.id) === LOCAL_SERVER ? null : updateNotice(e)))
        + `<span class="session-group-count">${count}</span></button>`
        + `<details class="server-group-more sidebar-kebab-wrapper group-kebab" data-server-details="${escHtml(e.id)}"${detailsOpen ? ' open' : ''}>`
        + `<summary class="server-group-more-btn sidebar-kebab-btn group-kebab-btn" aria-label="${escHtml(`Details for ${e.label || e.id}`)}" title="${escHtml(serverHoverText(e.id))}">⋮</summary>`
        + `<div class="server-switcher-panel">${serverLaunchActionsHtml(e)}${serverDetailsHtml(e)}</div></details>`
        + `</li>`;
}

/** Strip listing every server (local first) with its status. '' without remotes.
 *  (Replaced in the agent list by the server group headers, #3085.) */
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
        if (st === 'host_refused') return 'The server refused the host name in this URL. Through a port forward, use 127.0.0.1 or localhost.';
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
 function proxyErrorMessage(httpStatus, body, serverId) {
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
    let code = '', exited = false;
    try { const b = await resp.clone().json(); if (b && typeof b.code === 'string') code = b.code; exited = !!(b && b.agent_exited); } catch { /* not JSON */ }
    return { text: await responseErrorText(resp, serverId, fallback), code, exited };
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
