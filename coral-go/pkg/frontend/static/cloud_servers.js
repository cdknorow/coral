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

import { state } from './state.js';
import { h } from './cloud.js';
import { formatLastSeen, updateNotice, RELEASES_URL } from './server_status.js';

let modal = null;
let servers = [];
let loadError = '';
let confirming = null;   // id awaiting revoke confirmation
let renaming = null;     // id being renamed
let busy = new Set();
let timer = null;
let lastFocus = null;

function ensureModal() {
    if (modal) return modal;
    modal = h('div', { id: 'cloud-servers-modal', className: 'modal', role: 'dialog', 'aria-modal': 'true', 'aria-labelledby': 'cloud-servers-title' });
    modal.style.display = 'none';
    modal.addEventListener('mousedown', e => { if (e.target === modal) hideCloudServers(); });
    modal.addEventListener('keydown', e => { if (e.key === 'Escape') { e.stopPropagation(); hideCloudServers(); } });
    document.body.append(modal);
    return modal;
}

export async function showCloudServers() {
    ensureModal();
    lastFocus = document.activeElement;
    confirming = null; renaming = null; loadError = '';
    modal.style.display = 'flex';
    render();
    await load();
    clearInterval(timer);
    timer = setInterval(() => { if (!renaming && !busy.size) load(); }, 5000);
}

export function hideCloudServers() {
    clearInterval(timer); timer = null;
    if (modal) modal.style.display = 'none';
    if (lastFocus && lastFocus.focus) { try { lastFocus.focus(); } catch { /* ignore */ } }
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
    try {
        const r = await fetch(`/api/servers/${encodeURIComponent(id)}`, {
            method, headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined,
        });
        if (!r.ok) loadError = r.status === 404 ? 'That server no longer exists.' : 'The change was not saved.';
        else loadError = '';
    } catch { loadError = 'Could not reach the hub.'; }
    busy.delete(id);
    await load();
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
/** Whether this tab's account may send interact-level requests to `serverId` right now (the lower of the hub mode and the
 *  machine's own limit). Cached for 15 s; an unknown server or a failed lookup answers true so a lookup problem never
 *  blocks the UI (the hub still enforces). Used to skip background writes a viewer could only get refused. */
export async function serverMayInteract(serverId) {
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
    const s = _modeCache.byId && _modeCache.byId[serverId];
    return !s || rank(effectiveMode(s)) >= rank('interact');
}

/** Plain-language text for a hub refusal of a proxied request, or '' when it is not one we explain.
 *  `local_mode` = the machine's own Cloud access setting is lower than the hub allows (mode is its
 *  current maximum); `access_mode` / `launch_disabled` = the hub setting is too low. */
export function refusalMessage(status, body) {
    if (status !== 403 || !body || typeof body !== 'object') return '';
    if (body.error === 'local_mode') {
        const m = MODE_INFO[body.mode] ? MODE_INFO[body.mode].label : 'a lower mode';
        return `Limited by this machine's local setting (${m}). Change it in Coral on that machine.`;
    }
    if (body.error === 'access_mode' || body.error === 'launch_disabled') {
        return 'Not allowed by this server\'s access mode. Raise it on the Servers page.';
    }
    return '';
}

function row(s) {
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
            h('span', { className: 'sr-only', text: online ? ' (online)' : ' (offline)' }));
    const meta = h('div', { className: 'cs-meta', text: `${online ? 'Online' : 'Offline'} · last seen ${formatLastSeen(s.last_seen)}` });
    const mode = hubMode(s);
    const legacy = rank(s.access_mode) < 0;
    const picker = h('label', { className: 'cs-meta cs-launch' }, 'Access ',
        h('select', {
            disabled: isBusy, 'aria-label': 'Access mode', value: mode,
            onchange: e => call(s.id, 'PATCH', legacy ? { allow_launch: e.target.value === 'launch' } : { access_mode: e.target.value }),
        }, MODES.map(m => h('option', { value: m, selected: m === mode, text: MODE_INFO[m].label }))));
    const modeText = h('div', { className: 'cs-meta', text: MODE_INFO[mode].text });
    const launch = h('div', {}, picker, modeText);
    const actions = confirming === s.id
        ? [h('button', { type: 'button', className: 'btn btn-small btn-danger', text: 'Confirm revoke', onclick: () => { confirming = null; call(s.id, 'DELETE'); } }),
           h('button', { type: 'button', className: 'btn btn-small', text: 'Cancel', onclick: () => { confirming = null; render(); } })]
        : [h('button', { type: 'button', className: 'btn btn-small', text: 'Rename', disabled: isBusy, onclick: () => { renaming = s.id; render(); } }),
           h('button', { type: 'button', className: 'btn btn-small btn-danger-text', text: 'Revoke', disabled: isBusy, onclick: () => { confirming = s.id; render(); } })];
    const note = launchNote(s);
    // The update notice comes with the live list's servers entry (#3059).
    const update = updateNotice((state.servers || []).find(x => x && x.id === s.id) || s);
    const updateNote = update
        ? h('div', { className: `cs-note cs-update cs-update-${update.level}` },
            h('span', { text: update.text + ' ' }),
            h('a', { href: RELEASES_URL, target: '_blank', rel: 'noopener noreferrer', text: 'Get the latest release' }))
        : null;
    return h('li', { className: 'cs-row', dataset: { id: s.id } },
        h('div', {}, title, meta, launch),
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
        h('strong', { text: 'Connect a server' }),
        h('ol', {},
            h('li', {}, 'On the machine, open Coral → Settings → ', h('strong', { text: 'Connect to Coral Hub' }), ' (or start it with ', h('code', { text: `--cloud-url ${location.origin}` }), ').'),
            h('li', { text: 'Coral shows a short code. Enter it below.' }),
            h('li', { text: 'Check the machine details on the next page and approve.' })),
        h('form', { className: 'cs-enroll-form', onsubmit: e => { e.preventDefault(); go(); } }, input,
            h('button', { type: 'submit', className: 'btn btn-primary', text: 'Continue' })));
}

function render() {
    if (!modal) return;
    const list = servers.length
        ? h('ul', { className: 'cs-list', 'aria-label': 'Your servers' }, servers.map(row))
        : h('p', { className: 'servers-empty', text: loadError ? '' : 'No servers connected yet.' });
    modal.replaceChildren(h('div', { className: 'modal-content modal-content-wide' },
        h('div', { className: 'modal-header' },
            h('h3', { id: 'cloud-servers-title', text: 'Servers' }),
            h('button', { type: 'button', className: 'modal-close-btn', 'aria-label': 'Close dialog', onclick: hideCloudServers },
                h('span', { className: 'material-icons', 'aria-hidden': 'true', text: 'close' }))),
        h('div', { className: 'modal-body' },
            h('p', { className: 'servers-intro', text: 'Servers connected to your account. Each one connects out to this hub; no inbound port is needed.' }),
            loadError ? h('div', { className: 'servers-form-error', role: 'alert', text: loadError }) : null,
            list,
            enrollBlock())));
}
