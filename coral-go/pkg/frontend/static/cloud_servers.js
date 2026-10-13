/* Servers dialog for the Cloud Hub: the servers enrolled to this account.
 * List (name, online/offline, last seen), rename, "allow launching from the
 * web" switch, revoke (confirmed in the dialog, never window.confirm), and the
 * enrollment instructions. All text from the server goes through textContent.
 *
 * API (docs/enrollment-tunnel.md in coral-hub):
 *   GET    /api/servers                -> {servers: [{id, label, created_at, last_seen, allow_launch, online}]}
 *   PATCH  /api/servers/{id} {label?, allow_launch?}
 *   DELETE /api/servers/{id}           (revoke; closes the live tunnel)
 *
 * Access modes (see docs/access-modes): each server has a hub-side `access_mode`
 * ("view" | "interact" | "launch", default view) and the connector reports the most
 * it will allow locally as `local_mode`. The effective mode is the LOWER of the two,
 * so raising the hub setting alone never grants more than the machine permits.
 * Servers without `access_mode` (older hub) fall back to the `allow_launch` switch. */

import { importOnce } from './import_once.js';
import { state } from './state.js';
import { h } from './cloud.js';
import { formatLastSeen, updateNotice, RELEASES_URL, serverVersion, serverAgentCount } from './server_status.js';
import { accessBlockedMessage, accessChanged } from './hub_access.js';

// The Servers tab (#3070) hosts this list; there is no separate dialog.
let host = null;
let servers = [];
let loadError = '';
let confirming = null;   // id awaiting revoke confirmation
let renaming = null;     // id being renamed
let busy = new Set();
let timer = null;

/** Open the Servers tab, optionally at one server's row. */
export async function showCloudServers(focusId) {
    const { openServersTab } = await importOnce('./servers_tab.js');
    return openServersTab(focusId);
}

/** Render the server list into `el` (the Servers tab) and keep it fresh. Resolves once loaded. */
export async function renderCloudServersInto(el) {
    host = el;
    confirming = null; renaming = null; loadError = '';
    render();
    await load();
    clearInterval(timer);
    timer = setInterval(() => { if (!renaming && !busy.size) load(); }, 5000);
}

/** Leaving the Servers tab: stop refreshing. */
export function hideCloudServers() {
    clearInterval(timer); timer = null;
    host = null;
}

async function load() {
    try {
        const r = await fetch('/api/servers', { headers: { Accept: 'application/json' } });
        if (!r.ok) loadError = 'Could not load your servers.';
        else {
            const d = await r.json();
            servers = Array.isArray(d && d.servers) ? d.servers : [];
            loadError = '';
        }
    } catch { loadError = 'Could not reach the hub.'; }
    render();
}

async function call(id, method, body) {
    busy.add(id); render();
    const accessChange = method === 'PATCH' && !!body && ('access_mode' in body || 'allow_launch' in body);
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

/** Ask before raising a server's hub mode to Launch (#3115): Launch lets the hub
 *  start processes on that machine. A modal <dialog> with Cancel focused; Escape,
 *  Cancel and closing all count as no. Exactly one of the callbacks runs. */
export function confirmLaunch(label, onConfirm, onCancel) {
    document.querySelectorAll('dialog.cs-confirm').forEach(d => d.remove());
    const cancel = h('button', { type: 'button', className: 'btn', text: 'Cancel', autofocus: true });
    const ok = h('button', { type: 'button', className: 'btn btn-primary', text: 'Raise to Launch' });
    const dlg = h('dialog', { className: 'cs-confirm', 'aria-labelledby': 'cs-confirm-title', 'aria-describedby': 'cs-confirm-text' },
        h('h3', { id: 'cs-confirm-title', text: `Allow ${label} to launch agents from the Cloud Hub?` }),
        h('p', { id: 'cs-confirm-text', text: 'This lets the hub start new processes on that machine.' }),
        h('div', { className: 'cs-confirm-actions' }, cancel, ok));
    let done = false;
    const finish = (yes) => {
        if (done) return;
        done = true;
        if (dlg.open) dlg.close();
        dlg.remove();
        if (yes) onConfirm(); else if (onCancel) onCancel();
    };
    cancel.addEventListener('click', () => finish(false));
    ok.addEventListener('click', () => finish(true));
    dlg.addEventListener('cancel', (e) => { e.preventDefault(); finish(false); }); // Escape
    dlg.addEventListener('close', () => finish(false));
    document.body.appendChild(dlg);
    dlg.showModal();
    cancel.focus();
    return dlg;
}

/** Change a server's hub mode; raising it to Launch asks first. A cancelled
 *  change re-renders, so the Access select shows the current mode again, and
 *  focus goes back to the control the user came from (`via`: 'raise' or 'select'). */
function setHubMode(s, mode, legacy, via) {
    const send = () => call(s.id, 'PATCH', legacy ? { allow_launch: mode === 'launch' } : { access_mode: mode });
    if (mode !== 'launch' || hubMode(s) === 'launch') { send(); return; }
    confirmLaunch(s.label || 'this server', send, () => {
        render();
        const r = document.getElementById(rowId(s.id));
        const back = r && (r.querySelector(via === 'select' ? '.cs-launch select' : '.cs-raise') || r);
        if (back) back.focus();
    });
}

export const MODES = ['view', 'interact', 'launch'];

export const MODE_INFO = {
    view: { label: 'View', text: 'Read terminals, chats, files, tasks and boards. Nothing you do here changes anything on the machine.' },
    interact: { label: 'Interact', text: 'Also type into agents, change tasks and boards, and stop or delete work (kill agents, delete boards). Agents can run commands, so this can make the machine run code. It can also change board settings, including turning on cross-team messaging.' },
    launch: { label: 'Launch', text: 'Also start new agents and teams and browse the machine\'s folders.' },
};

const rank = m => MODES.indexOf(m);

/** The lower of two modes; an unknown or missing local mode does not limit. */
export function lowerMode(a, b) {
    if (rank(b) < 0) return rank(a) < 0 ? 'view' : a;
    if (rank(a) < 0) return b;
    return rank(a) <= rank(b) ? a : b;
}

/** The mode a server row currently has, from access_mode or the legacy allow_launch. */
export function hubMode(s) {
    if (rank(s.access_mode) >= 0) return s.access_mode;
    return s.allow_launch ? 'launch' : 'view';
}

/** What actually applies: the lower of the hub setting and the machine's own limit. */
export function effectiveMode(s) { return lowerMode(hubMode(s), s.local_mode); }

/** Note shown under a row when the machine's own setting limits what the hub setting allows. */
export function launchNote(s) {
    const want = hubMode(s), eff = effectiveMode(s);
    if (eff !== want) {
        return `Limited by this machine's local setting: it allows ${MODE_INFO[eff].label} only (you chose ${MODE_INFO[want].label}). To allow more, open Coral on that machine: Settings > Coral Hub > "Most the hub may do on this computer".`;
    }
    return '';
}

let _modeCache = { at: 0, byId: null, pending: null };

/** Forget the cached Servers list, so the next access lookup asks the hub (after
 *  a mode change, so it applies at once rather than within 15 s). */
export function invalidateServerAccess() { _modeCache = { at: 0, byId: null, pending: null }; }

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
    if (!s) return null;
    const hub = hubMode(s);
    const local = rank(s.local_mode) >= 0 ? s.local_mode : null;
    const effective = rank(s.effective_mode) >= 0 ? s.effective_mode : effectiveMode(s);
    let limitedBy = s.limited_by === 'hub' || s.limited_by === 'machine' ? s.limited_by : null;
    if (!limitedBy && rank(effective) < rank('launch')) limitedBy = local && rank(local) < rank(hub) ? 'machine' : 'hub';
    return { hub, local, effective, limitedBy, mayInteract: rank(effective) >= rank('interact'), label: s.label || serverId };
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

function row(s) {
    // Version, agent count and the update notice come with the live list's servers entry (#3059, #3099).
    const live = { ...s, ...((state.servers || []).find(x => x && x.id === s.id) || {}) };
    const isBusy = busy.has(s.id);
    const online = s.online === true;
    const title = renaming === s.id
        ? h('form', { className: 'cs-actions', onsubmit: e => { e.preventDefault(); const v = e.target.elements.label.value.trim(); renaming = null; if (v && v !== s.label) call(s.id, 'PATCH', { label: v }); else render(); } },
            h('input', { name: 'label', type: 'text', value: s.label || '', maxLength: 100, 'aria-label': 'Server name', autocomplete: 'off' }),
            h('button', { type: 'submit', className: 'btn btn-small btn-primary', text: 'Save' }),
            h('button', { type: 'button', className: 'btn btn-small', text: 'Cancel', onclick: () => { renaming = null; render(); } }))
        : h('div', { className: 'cs-title' },
            h('span', { className: 'cs-dot ' + (online ? 'online' : 'offline'), 'aria-hidden': 'true' }),
            h('span', { text: s.label || 'Unnamed server' }),
            h('span', { className: 'sr-only', text: online ? ' (online)' : ' (offline)' }),
            updateBadge(live));
    const version = serverVersion(live);
    const agents = serverAgentCount(live);
    const meta = h('div', { className: 'cs-meta', text: [online ? 'Online' : 'Offline', version ? `Coral ${version}` : '', `${agents} agent${agents === 1 ? '' : 's'}`, `last seen ${formatLastSeen(s.last_seen)}`].filter(Boolean).join(' · ') });
    const mode = hubMode(s);
    const legacy = rank(s.access_mode) < 0;
    const picker = h('label', { className: 'cs-meta cs-launch' }, 'Access ',
        h('select', {
            disabled: isBusy, 'aria-label': 'Access mode', value: mode,
            onchange: e => setHubMode(s, e.target.value, legacy, 'select'),
        }, MODES.map(m => h('option', { value: m, selected: m === mode, text: MODE_INFO[m].label }))));
    const modeText = h('div', { className: 'cs-meta', text: MODE_INFO[mode].text });
    // C1: what each side allows and what applies. The hub's effective_mode and
    // limited_by win; older hubs fall back to the lower of the two here.
    const machine = rank(s.local_mode) >= 0 ? s.local_mode : null;
    const effective = rank(s.effective_mode) >= 0 ? s.effective_mode : effectiveMode(s);
    const limitedBy = s.limited_by === 'hub' || s.limited_by === 'machine' ? s.limited_by
        : (machine && rank(machine) < rank(mode) ? 'machine' : (rank(effective) < rank(machine || 'launch') ? 'hub' : null));
    const access = h('div', { className: 'cs-access', role: 'group', 'aria-label': 'Access' },
        h('span', { className: 'cs-access-item', text: `Hub: ${MODE_INFO[mode].label}` }),
        h('span', { className: 'cs-access-item', text: `Machine: ${machine ? MODE_INFO[machine].label : 'unknown'}` }),
        h('span', { className: 'cs-access-item cs-access-effective', text: `Effective: ${MODE_INFO[effective].label}` }));
    // The hub is what holds this server back: one click raises it to what the machine allows.
    const raise = limitedBy === 'hub' && machine && rank(machine) > rank(mode) && !legacy
        ? h('button', { type: 'button', className: 'btn btn-small btn-primary cs-raise', disabled: isBusy,
            text: `Raise hub mode to ${MODE_INFO[machine].label}`, onclick: () => setHubMode(s, machine, false, 'raise') })
        : null;
    const launch = h('div', {}, access, raise, picker, modeText);
    const actions = confirming === s.id
        ? [h('button', { type: 'button', className: 'btn btn-small btn-danger', text: 'Confirm revoke', onclick: () => { confirming = null; call(s.id, 'DELETE'); } }),
           h('button', { type: 'button', className: 'btn btn-small', text: 'Cancel', onclick: () => { confirming = null; render(); } })]
        : [h('button', { type: 'button', className: 'btn btn-small', text: 'Rename', disabled: isBusy, onclick: () => { renaming = s.id; render(); } }),
           h('button', { type: 'button', className: 'btn btn-small btn-danger-text cs-revoke', text: 'Revoke', disabled: isBusy, onclick: () => { confirming = s.id; render(); } })];
    const note = launchNote(s);
    const update = updateNotice(live);
    const updateNote = update
        ? h('div', { className: `cs-note cs-update cs-update-${update.level}` },
            h('span', { text: update.text + ' ' }),
            h('a', { href: RELEASES_URL, target: '_blank', rel: 'noopener noreferrer', text: 'Get the latest release' }))
        : null;
    return h('li', { className: 'cs-row', id: rowId(s.id), tabIndex: -1, dataset: { id: s.id } },
        h('div', { className: 'cs-main' }, title, meta, launch),
        h('div', { className: 'cs-actions' }, actions),
        confirming === s.id ? h('div', { className: 'cs-note', text: 'Revoking disconnects this server now and its token stops working. To connect it again you must enroll it again.' }) : null,
        note ? h('div', { className: 'cs-note', text: note }) : null,
        updateNote);
}

function enrollBlock() {
    const input = h('input', { type: 'text', className: 'cloud-code', placeholder: 'XXXX-XXXX', maxLength: 9, autocomplete: 'off', spellcheck: false, 'aria-label': 'Enrollment code' });
    const go = () => {
        const v = input.value.trim().toUpperCase();
        location.href = '/enroll' + (v ? '?code=' + encodeURIComponent(v) : '');
    };
    return h('div', { className: 'cs-enroll' },
        h('h3', { className: 'cs-enroll-title', text: 'Add a machine' }),
        h('ol', {},
            h('li', {}, 'On the machine, open Coral → Settings → ', h('strong', { text: 'Connect to Coral Hub' }), ' (or start it with ', h('code', { text: `--cloud-url ${location.origin}` }), ').'),
            h('li', { text: 'Coral shows a short code. Enter it below.' }),
            h('li', { text: 'Check the machine details on the next page and approve.' })),
        h('form', { className: 'cs-enroll-form', onsubmit: e => { e.preventDefault(); go(); } }, input,
            h('button', { type: 'submit', className: 'btn btn-primary', text: 'Continue' })));
}

/** The DOM id of a server's row, for deep links. */
export function rowId(id) { return 'server-row-' + String(id).replace(/[^A-Za-z0-9_-]/g, '_'); }

function render() {
    if (!host) return;
    // Re-rendering replaces every row: keep focus on the same control where we can.
    const active = document.activeElement && host.contains(document.activeElement) ? document.activeElement : null;
    const keep = active && active.closest('.cs-row') ? { row: active.closest('.cs-row').id, text: active.textContent } : null;
    const list = servers.length
        ? h('ul', { className: 'cs-list', 'aria-label': 'Your servers' }, servers.map(row))
        : h('p', { className: 'servers-empty', text: loadError ? '' : 'No servers connected yet.' });
    // replaceChildren() would render a null as the text "null": leave it out.
    host.replaceChildren(...[
        h('p', { className: 'servers-intro', text: 'Servers connected to your account. Each one connects out to this hub; no inbound port is needed. New machines start in View.' }),
        loadError ? h('div', { className: 'servers-form-error', role: 'alert', text: loadError }) : null,
        list,
        enrollBlock()].filter(Boolean));
    if (keep) {
        const r = document.getElementById(keep.row);
        const again = r && [...r.querySelectorAll('button, select, input')].find(el => el.textContent === keep.text);
        if (again) again.focus(); else if (r) r.focus();
    }
}
