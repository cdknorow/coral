/* Servers dialog for the Cloud Hub: the servers enrolled to this account.
 * List (name, online/offline, last seen), rename, "allow launching from the
 * web" switch, revoke (confirmed in the dialog, never window.confirm), and the
 * enrollment instructions. All text from the server goes through textContent.
 *
 * API (docs/enrollment-tunnel.md in coral-hub):
 *   GET    /api/servers                -> {servers: [{id, label, created_at, last_seen, allow_launch, online}]}
 *   PATCH  /api/servers/{id} {label?, allow_launch?}
 *   DELETE /api/servers/{id}           (revoke; closes the live tunnel)
 * A server may also report `local_launch: false` when the connector's own
 * "Allow launching from the web" switch is off; launching then stays blocked
 * until it is turned on on that machine. */

import { state } from './state.js';
import { h } from './cloud.js';
import { formatLastSeen } from './server_status.js';

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

/** Note shown under a row when launching from the web cannot work yet. */
export function launchNote(s) {
    if (s.allow_launch && s.local_launch === false) {
        return 'Launch blocked: enable "Allow launching from the web" in Coral on this server. The hub cannot turn it on for you.';
    }
    if (!s.allow_launch) return 'Launching agents from the web is off for this server.';
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
    const launch = h('label', { className: 'cs-meta' },
        h('input', { type: 'checkbox', checked: !!s.allow_launch, disabled: isBusy, onchange: e => call(s.id, 'PATCH', { allow_launch: e.target.checked }) }),
        ' Allow launching agents from the web');
    const actions = confirming === s.id
        ? [h('button', { type: 'button', className: 'btn btn-small btn-danger', text: 'Confirm revoke', onclick: () => { confirming = null; call(s.id, 'DELETE'); } }),
           h('button', { type: 'button', className: 'btn btn-small', text: 'Cancel', onclick: () => { confirming = null; render(); } })]
        : [h('button', { type: 'button', className: 'btn btn-small', text: 'Rename', disabled: isBusy, onclick: () => { renaming = s.id; render(); } }),
           h('button', { type: 'button', className: 'btn btn-small btn-danger-text', text: 'Revoke', disabled: isBusy, onclick: () => { confirming = s.id; render(); } })];
    const note = launchNote(s);
    return h('li', { className: 'cs-row', dataset: { id: s.id } },
        h('div', {}, title, meta, launch),
        h('div', { className: 'cs-actions' }, actions),
        confirming === s.id ? h('div', { className: 'cs-note', text: 'Revoking disconnects this server now and its token stops working. To connect it again you must enroll it again.' }) : null,
        note && s.allow_launch !== undefined ? h('div', { className: 'cs-note', text: note }) : null);
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
    void state;
}
