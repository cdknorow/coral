/* Servers dialog for the Cloud Hub: the servers enrolled to this account.
 * List (name, online/offline, last contact), rename, "allow launching from the
 * web" switch, revoke (confirmed in the dialog, never window.confirm), and the
 * enrollment instructions. All text from the server goes through textContent.
 *
 * API (docs/enrollment-tunnel.md in coral-hub):
 *   GET    /api/servers                -> {servers: [{id, label, created_at, last_seen, access_mode, local_mode, effective_mode, limited_by, online}]}
 *   PATCH  /api/servers/{id} {label?, access_mode?: follow|interact|view}
 *   DELETE /api/servers/{id}           (revoke; closes the live tunnel)
 *
 * Access modes (see docs/access-modes): the connector reports the most it will allow
 * locally as `local_mode` (chosen on that computer when it connected: "What may Coral Hub do
 * on this computer?"). The owner's access to a server FOLLOWS it (`access_mode` "follow", the
 * default; the old "launch" means follow) or is limited to "interact" or "view": a control that
 * only lowers, never raises. The effective mode is the machine's announced max when following,
 * else the LOWER of the owner's cap and the announced max; a machine that has not announced yet
 * counts as View. The hub's own effective_mode / limited_by win when the row carries them. */

import { rememberFocus } from './dom_morph.js';
import { importOnce } from './import_once.js';
import { state } from './state.js';
import { h } from './cloud.js';
import { formatLastSeen, updateNotice, RELEASES_URL, DOWNLOAD_URL, serverVersion, serverAgentCount, belowMinimumText } from './server_status.js';
import { accessBlockedMessage, accessChanged } from './hub_access.js';
import { pollEvery } from './overload.js';
import { announceNotice } from './notifications.js';
import { OWNER_LABEL_STEP } from './owner_label.js';
import { healthDot, healthCard } from './machine_health.js';
// This module is loaded lazily, so its search box is loaded the same way (a static import of a module not loaded at startup could not be retried: import_hygiene).
const { syncRelaunchReads, resetRelaunchReads, relaunchBadge } = await importOnce('./relaunch_badge.js'); // the "Updating" badge of a hub relaunch (#5355, behind the update flag)
const { syncServersBar, serverVisible, serversFiltered, resetServersBar } = await importOnce('./server_search.js');

// The Servers tab (#3070) hosts this list; there is no separate dialog.
let host = null;
let servers = [];
let loadError = '';
let confirming = null;   // id awaiting revoke confirmation
let renaming = null;     // id being renamed
let busy = new Set();
let timer = null;
let loaded = false;
let enrollOpen = null; // null follows the account: first machine open, existing machines closed
let enrollCode = '';
const accessDetails = new Set();
const relinkOpen = new Set(); // ids whose "Owner not confirmed" explanation is open (#6110)
/** The words for a machine the hub says cannot confirm its owner yet (GET /api/servers needs_relink, #6110). The step is owner_label.js's, the same one the message on that computer says (#6108); the Servers row names the machine,
 *  because it is not the computer in the person's hand. */
export const RELINK_BUTTON = 'Owner not confirmed';
export const relinkHelp = label => `${label || 'This machine'} can't confirm its owner yet. On that computer, ${OWNER_LABEL_STEP}.`;
const relinkId = s => `${rowId(s.id)}-relink`;

/** Open the Servers tab, optionally at one server's row. */
export async function showCloudServers(focusId) {
    const { openServersTab } = await importOnce('./servers_tab.js');
    return openServersTab(focusId);
}

/** Render the server list into `el` (the Servers tab) and keep it fresh. Resolves once loaded. */
export async function renderCloudServersInto(el) {
    if (host !== el) enrollOpen = null; // a new visit follows the account again; a redraw into the same page keeps what the person opened
    host = el;
    confirming = null; renaming = null; loadError = '';
    loaded = false;
    resetRelaunchReads(); // the first list of a visit reads each online machine's relaunch status once
    render();
    await load();
    clearInterval(timer);
    timer = pollEvery(() => { if (!renaming && !busy.size) load(); }, 5000);
}

/** Leaving the Servers tab: stop refreshing. */
export function hideCloudServers() {
    clearInterval(timer); timer = null;
    host = null;
    resetServersBar();
}

async function load() {
    try {
        const r = await fetch('/api/servers', { headers: { Accept: 'application/json' } });
        if (!r.ok) loadError = 'Could not load your servers.';
        else {
            const d = await r.json();
            const before = loaded ? new Map(servers.map(s => [s.id, s.online === true])) : null;
            servers = Array.isArray(d && d.servers) ? d.servers : [];
            // A computer that goes offline or comes back is said once (#6226): the list redraws in place every 5 s and nothing in it is a live region.
            if (before) for (const s of servers) { const was = before.get(s.id); if (was !== undefined && was !== (s.online === true)) announceNotice(`${s.label || 'A computer'} is ${s.online === true ? 'online' : 'offline'}`); }
            loaded = true;
            loadError = '';
            syncRelaunchReads(servers, render);
        }
    } catch { loadError = 'Could not reach the hub.'; }
    render();
}

async function call(id, method, body) {
    busy.add(id); render();
    const accessChange = method === 'PATCH' && !!body && 'access_mode' in body;
    let changed = false;
    try {
        const r = await fetch(`/api/servers/${encodeURIComponent(id)}`, {
            method, headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined,
        });
        if (!r.ok) loadError = r.status === 404 ? 'That server no longer exists.' : 'The change was not saved.';
        else { loadError = ''; invalidateServerAccess(); changed = accessChange; }
    } catch { loadError = 'Could not reach the hub.'; }
    busy.delete(id);
    await load();
    // An agent already open on this server re-decides its read-only state from a
    // fresh lookup: the effective mode, not the hub setting alone (#3138).
    if (changed) accessChanged(id, await serverAccess(id));
}

export const MODES = ['view', 'interact', 'launch'];

export const MODE_INFO = {
    view: { label: 'View', text: 'Read terminals, chats, files, tasks and boards. Nothing you do here changes anything on the machine.' },
    // What each mode allows, as the route table has it (testdata/cloud_routes.golden, what the connector enforces): pinned by
    // TestDashboardModeTextMatchesTheRouteTable (#4526; this said Interact could kill an agent long after #4274 made it Launch).
    interact: { label: 'Interact', text: 'Also type into running agents, post on boards, change tasks and notes, and rename or archive agents. Agents can run commands, so this can make the machine run code. It can also change a team\'s working mode.' },
    launch: { label: 'Launch', text: 'Also start, stop, sleep, wake and restart agents (one agent or a whole team), browse the machine\'s folders, pause boards, delete board messages and boards, and turn on auto-accept.' },
};

const rank = m => MODES.indexOf(m);

/** The lower of two modes; an unknown or missing local mode does not limit. */
export function lowerMode(a, b) {
    if (rank(b) < 0) return rank(a) < 0 ? 'view' : a;
    if (rank(a) < 0) return b;
    return rank(a) <= rank(b) ? a : b;
}

/** What the owner chose for a server's hub access: view, interact or launch (the hub reads a stored launch as "up to what the computer allows"). An unknown or missing value never widens: view. */
export function accessChoice(s) {
    const m = s && s.access_mode;
    if (m === 'follow') return 'launch'; // an older hub's word for the same thing
    return m === 'launch' || m === 'interact' ? m : 'view';
}

/** The owner's choice as a cap. */
export function hubMode(s) { return accessChoice(s); }

/** What actually applies: the lower of the owner's choice and the machine's announced max; a machine that has not announced counts as View. */
export function effectiveMode(s) {
    return rank(s.local_mode) < 0 ? 'view' : lowerMode(hubMode(s), s.local_mode);
}

/** The most this computer allows, as words for a note: "This computer allows up to Interact; change it in Coral on that computer." Fixed words, never machine text; '' when it allows Launch. */
export function launchNote(s) {
    const local = rank(s.local_mode) >= 0 ? s.local_mode : null;
    if (local && rank(local) < rank('launch')) return `This computer allows up to ${MODE_INFO[local].label}; change it in Coral on that computer (Settings > Network and Hub).`;
    return '';
}

/** "Effective: Interact, limited by this computer" when what applies is lower than the owner's choice, else ''. */
export function effectiveNote(s) {
    const eff = effectiveMode(s);
    return rank(eff) < rank(accessChoice(s)) ? `Effective: ${MODE_INFO[eff].label}, limited by this computer` : '';
}

let _modeCache = { at: 0, byId: null, pending: null };

/** Mark the cached Servers list stale, so the next access lookup asks the hub (after
 *  a mode change, so it applies at once rather than within 15 s). The last list stays
 *  readable by cachedServerAccess until the new one arrives: a dialog drawn from it keeps
 *  its answer instead of flickering to "allowed" while the hub is asked again (#5004). */
export function invalidateServerAccess() { _modeCache = { at: 0, byId: _modeCache.byId, pending: null }; }

async function _serverRow(serverId) {
    const now = Date.now();
    if (!_modeCache.byId || now - _modeCache.at > 15000) {
        _modeCache.pending = _modeCache.pending || fetch('/api/servers', { credentials: 'same-origin', quiet: true })
            .then(r => (r.ok ? r.json() : null))
            .then(d => { _modeCache.byId = d && Array.isArray(d.servers) ? Object.fromEntries(d.servers.map(x => [x.id, x])) : (_modeCache.byId || {}); _modeCache.at = Date.now(); })
            .catch(() => { _modeCache.byId = _modeCache.byId || {}; _modeCache.at = Date.now(); })
            .finally(() => { _modeCache.pending = null; });
        await _modeCache.pending;
    } else if (_modeCache.pending) {
        await _modeCache.pending;
    }
    return (_modeCache.byId && _modeCache.byId[serverId]) || null;
}

/** Whether this tab's account may send interact-level requests to `serverId` right now (the lower of the hub mode and the
 *  machine's own limit). Cached for 15 s; an unknown server or a failed lookup answers true so a lookup problem never
 *  blocks the UI (the hub still enforces). Used to skip background writes a viewer could only get refused. */
export async function serverMayInteract(serverId) {
    const a = await serverAccess(serverId);
    return !a || a.mayInteract;
}

/** What the Cloud Hub lets this account do on `serverId`: {hub, local, effective, limitedBy, mayInteract, label},
 *  or null for an unknown server (a lookup problem never blocks the UI; the hub still enforces). Prefers the hub's own
 *  effective_mode / limited_by when the row carries them. */
export async function serverAccess(serverId) {
    const s = await _serverRow(serverId);
    return s ? _access(s, serverId) : null;
}

/** Whether the Servers list has loaded (even if that load failed or left a server out) and no mode
 *  change has marked it stale since: a failed load counts, it stamps the time and is not retried at once. */
export function serverListSettled() {
    return _modeCache.byId !== null && _modeCache.at !== 0;
}

/** serverAccess from the cached Servers list, without asking the hub: null when the list was not
 *  loaded yet or the server is unknown. For drawing a menu (#3443); serverAccess loads it. */
export function cachedServerAccess(serverId) {
    const s = _modeCache.byId && _modeCache.byId[serverId];
    return s ? _access(s, serverId) : null;
}

function _access(s, serverId) {
    const hub = hubMode(s);
    const local = rank(s.local_mode) >= 0 ? s.local_mode : null;
    const effective = rank(s.effective_mode) >= 0 ? s.effective_mode : effectiveMode(s);
    let limitedBy = s.limited_by === 'hub' || s.limited_by === 'machine' ? s.limited_by : null;
    if (!limitedBy && rank(effective) < rank('launch')) limitedBy = !local || rank(local) < rank(hub) ? 'machine' : 'hub'; // a computer that has not announced is the limit: there is nothing for the owner to raise
    return { hub, local, effective, limitedBy, mayInteract: rank(effective) >= rank('interact'), mayLaunch: rank(effective) >= rank('launch'), label: s.label || serverId };
}


/** Plain-language text for a hub refusal of a proxied request, or '' when it is not one we explain.
 *  `local_mode` = the machine's own Cloud access setting is lower than the hub allows (mode is its
 *  current maximum); `access_mode` / `launch_disabled` = the hub setting is too low. */
export function refusalMessage(status, body, serverName = '') {
    if (status !== 403 || !body || typeof body !== 'object') return '';
    const known = m => (rank(m) >= 0 ? m : null);
    if (body.error === 'local_mode') {
        const m = known(body.mode);
        return accessBlockedMessage({ local: m, effective: m, limitedBy: 'machine', label: serverName }, known(body.required));
    }
    if (body.error === 'access_mode' || body.error === 'launch_disabled') {
        const hub = known(body.hub_mode), local = known(body.local_mode);
        const effective = hub && local ? lowerMode(hub, local) : hub || local;
        const limitedBy = hub && local && rank(local) < rank(hub) ? 'machine' : 'hub';
        return accessBlockedMessage({ hub, local, effective, limitedBy, label: serverName },
            known(body.required) || (body.error === 'launch_disabled' ? 'launch' : null));
    }
    return '';
}

/** The row's update badge (a link to the releases page), or null. */
function updateBadge(e) {
    const n = updateNotice(e);
    return n ? h('a', { className: `server-update-badge server-update-${n.level}`, href: RELEASES_URL, target: '_blank', rel: 'noopener noreferrer',
        title: n.text, 'aria-label': n.text, text: n.label }) : null;
}

/** Change a server's hub access (view | interact | launch; launch is "up to what the computer allows"). Immediate, no dialog. Focus stays on the select. */
async function setAccess(s, value) {
    await call(s.id, 'PATCH', { access_mode: value });
    const sel = document.getElementById(rowId(s.id))?.querySelector('.cs-launch select');
    if (sel && document.activeElement === document.body) sel.focus();
}

// The list is a table (Name, Status, Coral, Agents, Hub access, and the row actions Rename and Revoke) with sortable headers; a row expands for the rest (health, what applies, the update notice).
const COLUMNS = [
    { key: 'name', label: 'Name' },
    { key: 'status', label: 'Status' },
    { key: 'version', label: 'Coral' },
    { key: 'agents', label: 'Agents' },
    { key: 'access', label: 'Hub access' },
    { key: 'actions', label: 'Actions', plain: true }, // Rename and Revoke: not sortable
];
let sortKey = 'name';
let sortDir = 'asc';
const expanded = new Set(); // ids of the rows that are open

/** Compare two dotted versions numerically ("1.4.10" after "1.4.9"); anything unreadable sorts as 0. */
function versionParts(v) { return String(v || '').replace(/^v/, '').split(/[.-]/).slice(0, 3).map(n => parseInt(n, 10) || 0); }
function cmpVersion(a, b) {
    const x = versionParts(a), y = versionParts(b);
    for (let i = 0; i < 3; i++) if ((x[i] || 0) !== (y[i] || 0)) return (x[i] || 0) - (y[i] || 0);
    return 0;
}

function liveOf(s) { return { ...s, ...((state.servers || []).find(x => x && x.id === s.id) || {}) }; } // version, agent count and the update notice (#3059, #3099)
function effectiveOf(s) { return rank(s.effective_mode) >= 0 ? s.effective_mode : effectiveMode(s); }

/** The value a column sorts by. */
function sortValue(s, key) {
    const live = liveOf(s);
    if (key === 'status') return s.online === true ? 1 : 0;
    if (key === 'version') return serverVersion(live);
    if (key === 'agents') return serverAgentCount(live);
    if (key === 'access') return rank(effectiveOf(s));
    return String(s.label || 'Unnamed server').toLowerCase();
}

function sorted(list) {
    const dir = sortDir === 'asc' ? 1 : -1;
    const by = (a, b) => {
        const x = sortValue(a, sortKey), y = sortValue(b, sortKey);
        const c = sortKey === 'version' ? cmpVersion(x, y) : (typeof x === 'number' ? x - y : String(x).localeCompare(String(y), undefined, { numeric: true, sensitivity: 'base' }));
        return c || String(a.label || '').toLowerCase().localeCompare(String(b.label || '').toLowerCase()) || String(a.id).localeCompare(String(b.id));
    };
    return [...list].sort((a, b) => dir * by(a, b));
}

function setSort(key) {
    if (sortKey === key) sortDir = sortDir === 'asc' ? 'desc' : 'asc'; else { sortKey = key; sortDir = key === 'agents' || key === 'access' ? 'desc' : 'asc'; }
    render();
}

function headerRow() {
    return h('tr', { role: 'row' }, COLUMNS.map(c => {
        if (c.plain) return h('th', { scope: 'col', role: 'columnheader', className: `cs-th cs-col-${c.key}` }, h('span', { className: 'sr-only', text: c.label }));
        const on = sortKey === c.key;
        const th = h('th', { scope: 'col', role: 'columnheader', className: `cs-th cs-col-${c.key}`, 'aria-sort': on ? (sortDir === 'asc' ? 'ascending' : 'descending') : 'none' },
            h('button', { type: 'button', className: 'cs-sort', dataset: { key: c.key }, onclick: () => setSort(c.key) },
                h('span', { text: c.label }), h('span', { className: 'cs-sort-mark', 'aria-hidden': 'true', text: on ? (sortDir === 'asc' ? '▲' : '▼') : '' })));
        return th;
    }));
}

/** Open one machine's details (a deep link to it). */
export function expandServer(id) { expanded.add(id); render(); }

function toggle(id) {
    if (expanded.has(id)) expanded.delete(id); else expanded.add(id);
    render();
}

function nameCell(s, live, online, isOpen, detailId) {
    if (renaming === s.id) {
        return h('form', { className: 'cs-actions cs-rename', onsubmit: e => { e.preventDefault(); const v = e.target.elements.label.value.trim(); renaming = null; if (v && v !== s.label) call(s.id, 'PATCH', { label: v }); else render(); } },
            h('input', { name: 'label', type: 'text', value: s.label || '', maxLength: 100, 'aria-label': 'Server name', autocomplete: 'off' }),
            h('button', { type: 'submit', className: 'btn btn-small btn-primary', text: 'Save' }),
            h('button', { type: 'button', className: 'btn btn-small', text: 'Cancel', onclick: () => { renaming = null; render(); } }));
    }
    return h('div', { className: 'cs-title' },
        h('button', { type: 'button', className: 'cs-toggle', 'aria-expanded': isOpen ? 'true' : 'false', 'aria-controls': detailId, onclick: () => toggle(s.id) },
            h('span', { className: 'cs-chevron', 'aria-hidden': 'true', text: isOpen ? '▾' : '▸' }),
            h('span', { className: 'cs-dot ' + (online ? 'online' : 'offline'), 'aria-hidden': 'true' }),
            h('span', { className: 'cs-name', text: s.label || 'Unnamed server' })),
        // The machine's health (#3446): the hub relays it while the machine is connected; without it (offline, held, reconnecting) the dot says unknown, never a stale ok.
        healthDot(online ? live.machine_health : null, s.label || 'Server'),
        relaunchBadge(h, s.id));
}

function accessPicker(s, isBusy, helpId) {
    // The owner's choice, View / Interact / Launch; an option above what the computer announced is disabled (Launch means "up to what the computer allows"). The text under it says why (in the open row).
    const choice = accessChoice(s);
    const local = rank(s.local_mode) >= 0 ? s.local_mode : 'view'; // not announced yet: View
    return h('label', { className: 'cs-launch' }, h('span', { className: 'sr-only', text: `Hub access for ${s.label || 'this server'} ` }),
        h('select', { disabled: isBusy, value: choice, 'aria-describedby': helpId, onchange: e => setAccess(s, e.target.value) },
            ['view', 'interact', 'launch'].map(v => h('option', { value: v, selected: v === choice, disabled: rank(v) > rank(local) && v !== choice, text: MODE_INFO[v].label }))));
}

/** Put the keyboard back on a control of a machine's row after the redraw. */
function focusRow(id, selector) { const el = document.getElementById(rowId(id))?.querySelector(selector); if (el) el.focus(); }

/** Revoke asks first, in the row: the keyboard goes to Cancel (the safe answer), and Cancel or Escape leave the machine connected and put the keyboard back on that row's Revoke. */
function askRevoke(s) { confirming = s.id; render(); focusRow(s.id, '.cs-col-actions .cs-revoke-cancel'); }
function cancelRevoke(s) { confirming = null; render(); focusRow(s.id, '.cs-revoke'); }
const revokeNoteId = s => `${rowId(s.id)}-revoke-note`;

/** Rename and Revoke, in the row's last column; Revoke asks in place (Confirm revoke / Cancel). */
function rowActions(s, isBusy) {
    return confirming === s.id
        ? [h('button', { type: 'button', className: 'btn btn-small btn-danger', text: 'Confirm revoke', 'aria-describedby': revokeNoteId(s), onclick: () => { confirming = null; call(s.id, 'DELETE'); } }),
           h('button', { type: 'button', className: 'btn btn-small cs-revoke-cancel', text: 'Cancel', 'aria-describedby': revokeNoteId(s), onclick: () => cancelRevoke(s) })]
        : [s.needs_relink === true ? h('button', { type: 'button', className: 'btn btn-small btn-primary cs-relink', text: RELINK_BUTTON, 'aria-label': `${RELINK_BUTTON} for ${s.label || 'this server'}: how to confirm it`, 'aria-expanded': String(relinkOpen.has(s.id)), 'aria-controls': relinkId(s),
               onclick: () => { if (relinkOpen.has(s.id)) relinkOpen.delete(s.id); else relinkOpen.add(s.id); render(); focusRow(s.id, '.cs-relink'); } }) : null,
           h('button', { type: 'button', className: 'btn btn-small cs-rename-btn', text: 'Rename', 'aria-label': `Rename ${s.label || 'this server'}`, disabled: isBusy, onclick: () => { renaming = s.id; render(); } }),
           h('button', { type: 'button', className: 'btn btn-small btn-danger-text cs-revoke', text: 'Revoke', 'aria-label': `Revoke ${s.label || 'this server'}`, disabled: isBusy, onclick: () => askRevoke(s) })];
}

function detailCell(s, live, online, isBusy, helpId) {
    const local = rank(s.local_mode) >= 0 ? s.local_mode : null;
    const effective = effectiveOf(s);
    // What the computer allows, and (only when lower than the choice) what applies; the select is described by this line.
    const help = local ? launchNote(s) : 'View until this computer connects.';
    // Keep the consequence and the local limit beside the control. The full capability matrix is optional.
    const consequence = effective === 'view' ? (help ? '' : 'Read-only access.') : (local ? 'Can run commands on this computer.' : '');
    const helpEl = h('div', { className: 'cs-meta cs-access-help', id: helpId, text: [help, consequence].filter(Boolean).join(' ') });
    const infoId = `${rowId(s.id)}-access-details`;
    const infoOpen = accessDetails.has(s.id);
    const info = h('div', { className: 'cs-access-details' },
        h('button', { type: 'button', className: 'btn btn-small cs-access-info', 'aria-label': `Access details for ${s.label || 'this server'}`, 'aria-expanded': String(infoOpen), 'aria-controls': infoId,
            onclick: () => { if (accessDetails.has(s.id)) accessDetails.delete(s.id); else accessDetails.add(s.id); render(); } },
            h('span', { className: 'material-icons', 'aria-hidden': 'true', text: 'info' }), 'Access details'),
        h('div', { id: infoId, className: 'cs-access-matrix', hidden: !infoOpen },
            h('p', { text: 'What Coral Hub may do on a machine is what you allowed when you connected it; you can limit it further here.' }),
            ...MODES.map(mode => h('p', {}, h('strong', { text: `${MODE_INFO[mode].label}: ` }), MODE_INFO[mode].text))));
    const eff = effectiveNote(s);
    const effEl = eff ? h('div', { className: 'cs-meta cs-access-effective', text: eff }) : null;
    const update = updateNotice(live);
    const updateNote = update
        ? h('div', { className: `cs-note cs-update cs-update-${update.level}` },
            h('span', { text: update.text + ' ' }),
            h('a', { href: RELEASES_URL, target: '_blank', rel: 'noopener noreferrer', text: 'Download the latest Coral' }))
        : null;
    // The row shows the status and the version; the details add only what it does not: when an offline machine was last heard from.
    const meta = online ? null : h('div', { className: 'cs-meta cs-lastseen', text: `Last contact ${formatLastSeen(s.last_seen)}` });
    return h('td', { colSpan: COLUMNS.length, role: 'cell', className: 'cs-detail-cell' },
        s.needs_relink === true ? h('div', { className: 'cs-note cs-relink-note', id: relinkId(s), hidden: !relinkOpen.has(s.id), text: relinkHelp(s.label) }) : null,
        h('div', { className: 'cs-main' }, meta, belowMinimumText(live) ? h('div', { className: 'cs-meta server-below-minimum', text: belowMinimumText(live) }) : null,
            online ? healthCard(live.machine_health, { title: 'Health', online: true, waitingText: 'No health reading yet.' }) : null, effEl, helpEl, info),
        confirming === s.id ? h('div', { className: 'cs-note', id: revokeNoteId(s), text: `Revoke ${s.label || 'this server'}? It disconnects this computer from Coral Hub. Its token stops working; to connect it again you must enroll it again.` }) : null,
        updateNote);
}

/** One server: its table row and the details row under it (shown while the row is open). */
function rows(s) {
    const live = liveOf(s);
    const isBusy = busy.has(s.id);
    const online = s.online === true;
    const id = rowId(s.id), detailId = `${id}-detail`, helpId = `${id}-access-help`;
    const isOpen = expanded.has(s.id) || renaming === s.id || confirming === s.id || (s.needs_relink === true && relinkOpen.has(s.id));
    const version = serverVersion(live);
    const agents = serverAgentCount(live);
    const upd = updateBadge(live);
    const main = h('tr', { className: 'cs-row' + (isOpen ? ' cs-open' : ''), id, tabIndex: -1, role: 'row', dataset: { id: s.id } },
        h('td', { role: 'cell', className: 'cs-col-name' }, nameCell(s, live, online, isOpen, detailId)),
        h('td', { role: 'cell', className: 'cs-col-status', dataset: { label: 'Status' } }, online ? 'Online' : 'Offline'),
        h('td', { role: 'cell', className: 'cs-col-version', dataset: { label: 'Coral' } }, h('span', { text: version ? version : '—' }), upd ? ' ' : null, upd),
        h('td', { role: 'cell', className: 'cs-col-agents', dataset: { label: 'Agents' } }, String(agents)),
        h('td', { role: 'cell', className: 'cs-col-access' }, accessPicker(s, isBusy, helpId)),
        h('td', { role: 'cell', className: 'cs-col-actions' }, h('div', { className: 'cs-actions' }, rowActions(s, isBusy))));
    const detail = h('tr', { className: 'cs-detail', id: detailId, role: 'row', hidden: !isOpen }, detailCell(s, live, online, isBusy, helpId));
    const escape = e => { if (e.key === 'Escape' && confirming === s.id) { e.preventDefault(); e.stopPropagation(); cancelRevoke(s); } };
    main.addEventListener('keydown', escape); detail.addEventListener('keydown', escape);
    return [main, detail];
}

/** The steps to add a machine, with the download link and what the person will see on that computer. */
function enrollBlock() {
    const open = enrollOpen ?? (loaded && servers.length === 0);
    const input = h('input', { type: 'text', className: 'cloud-code', placeholder: 'XXXX-XXXX', value: enrollCode, maxLength: 9, autocomplete: 'off', spellcheck: false, 'aria-label': 'Enrollment code', id: 'cs-enroll-code', oninput: e => { enrollCode = e.target.value; } });
    const go = () => {
        const v = input.value.trim().toUpperCase();
        location.href = '/enroll' + (v ? '?code=' + encodeURIComponent(v) : '');
    };
    const shot = theme => h('img', { className: `cs-shot cs-shot-${theme}`, src: new URL(theme === 'dark' ? './hub-img/connect-question-dark.png' : './hub-img/connect-question-light.png', import.meta.url).pathname, width: 520, height: 636, loading: 'lazy',
        alt: theme === 'dark' ? 'Coral\'s Connect to Coral Hub screen asking "What may Coral Hub do on this computer?" with View, Interact and Launch, Launch selected, and a Connect button.' : '' });
    // The light picture carries the alt text through its sibling: one description, one picture shown per theme (css).
    const lightShot = shot('light'); lightShot.alt = shot('dark').alt;
    return h('section', { className: 'cs-enroll', id: 'cs-add-machine', 'aria-labelledby': 'cs-enroll-title' },
        h('h3', { className: 'cs-enroll-title', id: 'cs-enroll-title' },
            h('button', { type: 'button', id: 'cs-enroll-toggle', className: open ? 'btn btn-small' : 'btn btn-primary btn-small', 'aria-expanded': String(open), 'aria-controls': 'cs-enroll-body', onclick: () => { enrollOpen = !open; render(); } }, 'Add a machine')),
        h('div', { className: 'cs-enroll-body', id: 'cs-enroll-body', hidden: !open },
            h('div', { className: 'cs-enroll-steps' },
                h('p', { className: 'cs-meta', text: 'Each machine connects out to this hub; no inbound port is needed.' }),
                h('ol', {},
                    h('li', {}, h('strong', { text: 'Install Coral' }), ' on the computer you want to add: ', h('a', { href: DOWNLOAD_URL, target: '_blank', rel: 'noopener noreferrer', text: 'coralhub.dev/coral.html#download' })),
                    h('li', {}, 'Open Coral on that computer and press ', h('strong', { text: 'Connect to Coral Hub' }), ' (Settings › Network and Hub, or the Coral Hub card on the home screen).'),
                    h('li', {}, 'Choose what Coral Hub may do on that computer, then press ', h('strong', { text: 'Connect' }), '. Coral shows a short code.'),
                    h('li', {}, 'Enter the code here, check the computer\'s details on the next page and approve.')),
                h('form', { className: 'cs-enroll-form', onsubmit: e => { e.preventDefault(); go(); } }, input,
                    h('button', { type: 'submit', className: 'btn btn-primary', text: 'Continue' }))),
            h('div', { className: 'cs-shots' }, shot('dark'), lightShot)));
}

/** The DOM id of a server's row, for deep links. */
 function rowId(id) { return 'server-row-' + String(id).replace(/[^A-Za-z0-9_-]/g, '_'); }

function render() {
    if (!host) return;
    // Re-rendering replaces every row: keep focus on the same control where we can.
    const active = document.activeElement && host.contains(document.activeElement) ? document.activeElement : null;
    const keepRow = active && active.closest('.cs-row, .cs-detail');
    const keep = keepRow ? { row: keepRow.id, text: active.textContent, cls: active.className, tag: active.tagName } : null;
    syncServersBar(servers, 'machine', render); // the search box, from six machines up
    const shown = sorted(servers.filter(serverVisible));
    const table = shown.length
        ? h('table', { className: 'cs-table', role: 'table', 'aria-label': 'Your servers' },
            h('thead', { role: 'rowgroup' }, headerRow()),
            h('tbody', { role: 'rowgroup' }, shown.flatMap(rows)))
        : h('p', { className: 'servers-empty', text: loadError ? '' : serversFiltered() ? 'No machines match this search.' : 'No servers connected yet.' });
    // replaceChildren() would render a null as the text "null": leave it out.
    const restoreFocus = rememberFocus(host); // a control the redraw replaced gets focus back (#4882)
    host.replaceChildren(...[
        h('p', { className: 'servers-intro', text: 'Manage connected machines.' }),
        enrollBlock(),
        loadError ? h('div', { className: 'servers-form-error', role: 'alert', text: loadError }) : null,
        table].filter(Boolean));
    restoreFocus();
    if (keep) {
        const r = document.getElementById(keep.row);
        const again = r && [...r.querySelectorAll('button, select, input')].find(el => el.textContent === keep.text && el.className === keep.cls);
        if (again) again.focus(); else if (r) (r.querySelector('.cs-toggle') || r).focus();
    }
}
