/* Servers settings view (multi-server hub).
 *
 * A self-contained dialog, built on demand, to list / add / edit / test /
 * remove the remote Coral servers the hub talks to. Everything here is
 * hub-local (/api/servers*, never proxied) and gated on isHub(). The remote
 * API key is write-only: it is sent on add/edit and never rendered, echoed,
 * logged or kept after the request completes. */

import { rememberFocus } from './dom_morph.js';
import { importOnce } from './import_once.js';
import { state } from './state.js';
import { isHub, initHub, isCloud } from './server_base.js';
import { showToast } from './utils.js';
import { switchRow } from './switch_row.js';
import { pollEvery } from './overload.js';
import {
    statusMeta, escHtml, describeServerError, formatLastSeen, isValidServerId,
    serverVersion, serverAgentCount, updateBadgeHtml, belowMinimumText,
} from './server_status.js';
import { onBuilt } from './lazy_dom.js';
import { syncServersBar, serverVisible, serversFiltered, resetServersBar } from './server_search.js'; // this module is in the startup graph (app.js imports it), so its search box is imported directly

let _servers = [];
let _editing = null;      // id being edited, '' for "add", null when the form is closed
let _confirmDelete = null;
let _busy = new Set();    // ids (or '*') with a request in flight
let _loadError = '';
let _formError = '';
let _draft = null;        // non-secret field values kept across a failed submit
let _timer = null;
let _host = null;         // the Servers tab body (#3070); there is no separate dialog

/** Unhide hub-only entry points. Safe to call on every page load. */
export async function initServersUi() {
    try { await initHub(); } catch { /* treated as non-hub */ }
    const hub = isHub();
    document.querySelectorAll('[data-hub-only]').forEach(el => { el.style.display = hub ? '' : 'none'; });
    // The top-bar menu's Servers item, once the menu is built (#4500).
    onBuilt('top-bar-settings-menu', menu => menu.querySelectorAll('[data-hub-only]').forEach(el => { el.style.display = hub ? '' : 'none'; }));
    const { updateServersTabVisibility } = await importOnce('./servers_tab.js');
    updateServersTabVisibility();
    return hub;
}

/** Open the Servers tab (the settings menu's "Servers" entry), optionally at a row. */
export async function showServersModal(focusId) {
    if (!isCloud() && !isHub()) return;   // hub features are off: no UI, no /api/servers calls
    const { openServersTab } = await importOnce('./servers_tab.js');
    return openServersTab(focusId);
}

/** Render the registry into `el` (the Servers tab) and keep it fresh. Resolves once loaded. */
export async function renderServersInto(el) {
    if (_host !== el) {
        el.addEventListener('click', onClick);
        el.addEventListener('submit', onSubmit);
    }
    _host = el;
    _editing = null; _draft = null; _confirmDelete = null; _formError = ''; _loadError = '';
    render();
    await load();
    clearInterval(_timer);
    // Keep status / last-seen fresh while the tab is open.
    _timer = pollEvery(() => { if (_editing === null && !_busy.size) load(); }, 5000);
}

/** Leaving the Servers tab: stop refreshing and drop an open form. */
export function hideServersModal() {
    clearInterval(_timer); _timer = null;
    _editing = null;
    resetServersBar();
}

// A refused URL (the server's 400 for an http URL at an address that is not private) is shown next to the URL field.
let _urlError = '';

async function readBody(resp) {
    try { return await resp.json(); } catch { return {}; }
}

async function load() {
    try {
        const resp = await fetch('/api/servers');
        if (!resp.ok) {
            _loadError = describeServerError(resp.status, await readBody(resp));
        } else {
            const data = await resp.json();
            _servers = Array.isArray(data) ? data : [];
            _loadError = '';
        }
    } catch {
        _loadError = 'Could not load the server list. Is this Coral server still running?';
    }
    render();
}

function statusPill(status) {
    const m = statusMeta(status);
    return `<span class="server-status server-${m.cls}" title="${escHtml(m.hint)}"><span class="server-dot" aria-hidden="true"></span>${escHtml(m.label)}</span>`;
}

/** The server's warning for a registered server (a plain-http URL at an address that is not this computer: the API key crosses the network
 *  unencrypted), as an icon and words, not only a colour: "Warning: The key crosses the network unencrypted; use https or a tunnel." */
function warningHtml(text, cls = '') {
    const t = String(text || '').trim();
    if (!t) return '';
    return `<div class="servers-row-warn ${cls}"><span class="material-icons" aria-hidden="true">warning</span><span><b>Warning:</b> ${escHtml(t.charAt(0).toUpperCase() + t.slice(1))}${/[.!?]$/.test(t) ? '' : '.'}</span></div>`;
}

function rowHtml(s) {
    if (s.local || s.id === 'local') {
        // This hub: its version and agent count, like the remote rows (#3152).
        const n = serverAgentCount({ id: 'local' });
        const localMeta = ['This hub', state.localVersion ? `Coral ${state.localVersion}` : '', `${n} agent${n === 1 ? '' : 's'}`].filter(Boolean);
        return `<li class="servers-row servers-row-local" id="server-row-local" tabindex="-1">
            <div class="servers-row-main"><div class="servers-row-title">${escHtml(s.label || 'Local')} <span class="servers-id">local</span></div>
            <div class="servers-row-meta">${localMeta.map(escHtml).join(' &middot; ')}</div></div>
            <div class="servers-row-status">${statusPill('online')}</div></li>`;
    }
    const busy = _busy.has(s.id);
    const confirming = _confirmDelete === s.id;
    // One line says what to do; the raw error is behind Details, for support (#3981).
    const act = (s.status === 'key_unreadable' || s.status === 'unauthorized')
        ? statusMeta(s.status).hint
        : s.status === 'unreachable' ? 'Is Coral running on that machine? Check the URL.' : '';
    const raw = s.last_error && s.last_error !== act ? s.last_error : '';
    const attention = act
        ? `<div class="servers-row-error" role="status"><span class="servers-row-hint">${escHtml(act)}</span>${raw ? ` <details class="servers-row-detail"><summary>Details</summary>${escHtml(raw)}</details>` : ''}</div>`
        : raw ? `<div class="servers-row-error" role="status">${escHtml(raw)}</div>` : '';
    // Version, agent count and update come with the live list's servers entry (#3099).
    const live = { ...s, ...((state.servers || []).find(x => x && x.id === s.id) || {}) };
    const version = serverVersion(live);
    const agents = serverAgentCount(live);
    const badge = updateBadgeHtml(live);
    const below = belowMinimumText(live); // the Hub's "Update Coral to <min>" hint (#3915), plain text
    // An online server's status is live from the feed; 'last contact' (refreshed
    // about once a minute) is shown only once it is not online (#3152).
    const contact = (live.status || s.status) !== 'online' ? `last contact ${formatLastSeen(s.last_seen)}` : '';
    const meta = [s.url, version ? `Coral ${version}` : '', `${agents} agent${agents === 1 ? '' : 's'}`, contact].filter(Boolean);
    return `<li class="servers-row" id="server-row-${escHtml(s.id)}" tabindex="-1" data-id="${escHtml(s.id)}">
        <div class="servers-row-main">
            <div class="servers-row-title">${escHtml(s.label || s.id)} <span class="servers-id">${escHtml(s.id)}</span>${s.allow_private ? ' <span class="servers-tag" title="Private network addresses are allowed for this server">private network</span>' : ''}${badge ? ' ' + badge : ''}</div>
            <div class="servers-row-meta">${meta.map(escHtml).join(' &middot; ')}</div>
            ${warningHtml(s.warning)}
            ${below ? `<div class="servers-row-hint server-below-minimum">${escHtml(below)}</div>` : ''}
            ${attention}
        </div>
        <div class="servers-row-status">${statusPill(s.status)}</div>
        <div class="servers-row-actions">
            <button type="button" class="btn btn-small" data-action="test" data-id="${escHtml(s.id)}"${busy ? ' disabled aria-busy="true"' : ''}>${busy ? 'Testing...' : 'Test'}</button>
            <button type="button" class="btn btn-small" data-action="edit" data-id="${escHtml(s.id)}"${busy ? ' disabled' : ''}>Edit</button>
            ${confirming
                ? `<button type="button" class="btn btn-small btn-danger" data-action="delete-confirm" data-id="${escHtml(s.id)}">Confirm remove</button>
                   <button type="button" class="btn btn-small" data-action="delete-cancel">Cancel</button>`
                : `<button type="button" class="btn btn-small btn-danger-text servers-remove" data-action="delete" data-id="${escHtml(s.id)}"${busy ? ' disabled' : ''}>Remove</button>`}
        </div>
    </li>`;
}

function formHtml() {
    const adding = _editing === '';
    const cur = { ...(adding ? {} : (_servers.find(s => s.id === _editing) || {})), ...(_draft || {}) };
    const title = adding ? 'Add server' : `Edit ${escHtml(cur.label || cur.id || '')}`;
    const busy = _busy.has('*');
    return `<form class="servers-form" id="servers-form" autocomplete="off" novalidate>
        <h4>${title}</h4>
        ${_formError ? `<div class="servers-form-error" role="alert">${escHtml(_formError)}</div>` : ''}
        <label>Server id
            <input type="text" name="id" value="${escHtml(cur.id || '')}" ${adding ? '' : 'disabled'} placeholder="workstation" maxlength="32" pattern="[a-z0-9-]{1,32}" required>
            <span class="servers-help">Lowercase letters, digits and dashes. Cannot be changed later.</span></label>
        <label>Display name
            <input type="text" name="label" value="${escHtml(cur.label || '')}" placeholder="Workstation"></label>
        <label>URL
            <input type="url" name="url" value="${escHtml(cur.url || '')}" placeholder="https://coral.example.com:8420" required${_urlError ? ' aria-invalid="true" aria-describedby="servers-url-error"' : ''}>
            <span class="servers-help">Scheme, host and port only.</span></label>
        ${_urlError ? `<div class="servers-url-error" id="servers-url-error" role="alert"><span class="material-icons" aria-hidden="true">error</span><span>${escHtml(_urlError)}</span></div>` : ''}
        ${adding ? '' : warningHtml((_servers.find(x => x.id === _editing) || {}).warning)}
        <label>API key
            <input type="password" name="api_key" value="" autocomplete="new-password" spellcheck="false" placeholder="${adding ? 'Paste the remote server\'s API key' : 'Leave blank to keep the current key'}" ${adding ? 'required' : ''}>
            <span class="servers-help">Write-only. The key is stored encrypted and is never shown again.</span></label>
        ${switchRow({ label: 'Allow private network address', name: 'allow_private', checked: cur.allow_private, labelClass: 'servers-check', labelId: 'servers-private-t', desc: 'Only tick this for a server on your LAN, tailnet or localhost that you trust. It lets this hub connect to private and reserved addresses, which is normally blocked to prevent requests being steered at internal services.', descId: 'servers-private-d', descClass: 'servers-help servers-warning', descInside: true })}
        <div class="servers-form-actions">
            <button type="button" class="btn" data-action="cancel-form"${busy ? ' disabled' : ''}>Cancel</button>
            <button type="submit" class="btn btn-primary"${busy ? ' disabled aria-busy="true"' : ''}>${busy ? 'Checking connection...' : (adding ? 'Add server' : 'Save')}</button>
        </div>
    </form>`;
}

function render() {
    const m = _host;
    if (!m) return;
    const allRemotes = _servers.filter(s => !(s.local || s.id === 'local'));
    syncServersBar(allRemotes, 'server', render); // the search box, from six servers up
    const remotes = allRemotes.filter(serverVisible);
    const local = _servers.find(s => s.local || s.id === 'local') || { local: true, id: 'local', label: 'Local' };
    // The periodic refresh must not pull focus out of a row the user is on.
    const activeRow = document.activeElement && m.contains(document.activeElement) ? document.activeElement.closest('.servers-row') : null;
    const keep = activeRow && _editing === null ? { row: activeRow.id, action: document.activeElement.dataset.action } : null;
    // A long list pushes "Add server" screens below the fold: with many machines it sits above the list, beside how many answer.
    const many = allRemotes.length > 5;
    const online = allRemotes.filter(s => s.status === 'online').length;
    const topBar = `<div class="servers-top"><span class="servers-summary">${allRemotes.length} servers, ${online} online${online < allRemotes.length ? `, ${allRemotes.length - online} not` : ''}</span><button type="button" class="btn btn-primary" data-action="add">Add server</button></div>`;
    const restoreFocus = rememberFocus(m); // a control the redraw replaced (Add server, Test, Edit) gets focus back (#4882)
    m.innerHTML = `<p class="servers-intro">Servers whose agents and teams appear in this hub. The hub stores each server's API key (encrypted); a key grants full control of that server.</p>
            ${_loadError ? `<div class="servers-form-error" role="alert">${escHtml(_loadError)}</div>` : ''}
            ${many && _editing === null ? topBar : ''}
            <ul class="servers-list" aria-label="Registered servers">${rowHtml(local)}${remotes.map(rowHtml).join('')}</ul>
            ${!remotes.length && !_loadError ? `<p class="servers-empty">${serversFiltered() ? 'No servers match this search.' : 'No remote servers yet.'}</p>` : ''}
            ${_editing !== null ? formHtml() : many ? '' : '<div class="servers-form-actions"><button type="button" class="btn btn-primary" data-action="add">Add server</button></div>'}`;
    restoreFocus();
    if (keep) {
        const r = document.getElementById(keep.row);
        const again = r && (keep.action ? r.querySelector(`[data-action="${keep.action}"]`) : null);
        if (again) again.focus(); else if (r) r.focus();
    }
    if (_editing !== null) {
        const f = m.querySelector('#servers-form');
        const target = f && (_urlError ? f.elements.url : f.elements[_editing === '' ? 'id' : 'label']);
        if (target) target.focus();
    }
}

function setBusy(id, on) { if (on) _busy.add(id); else _busy.delete(id); }

async function onClick(e) {
    const btn = e.target.closest('[data-action]');
    if (!btn) return;
    const action = btn.dataset.action;
    const id = btn.dataset.id;
    if (action === 'add') { _editing = ''; _formError = ''; _urlError = ''; _draft = null; return render(); }
    if (action === 'edit') { _editing = id; _formError = ''; _urlError = ''; _draft = null; _confirmDelete = null; return render(); }
    if (action === 'cancel-form') { _editing = null; _formError = ''; _urlError = ''; _draft = null; return render(); }
    if (action === 'delete') { _confirmDelete = id; return render(); }
    if (action === 'delete-cancel') { _confirmDelete = null; return render(); }
    if (action === 'delete-confirm') return doDelete(id);
    if (action === 'test') return doTest(id);
}

async function doTest(id) {
    setBusy(id, true); render();
    try {
        const resp = await fetch(`/api/servers/${encodeURIComponent(id)}/test`, { method: 'POST' });
        const body = await readBody(resp);
        if (!resp.ok) {
            _loadError = describeServerError(resp.status, body);
        } else {
            _loadError = '';
            const m = statusMeta(body.status);
            showToast(`${body.label || id}: ${m.label}`, body.status !== 'online');
        }
    } catch {
        _loadError = 'Test failed: could not reach the hub.';
    }
    setBusy(id, false);
    await load();
}

async function doDelete(id) {
    setBusy(id, true); _confirmDelete = null; render();
    try {
        const resp = await fetch(`/api/servers/${encodeURIComponent(id)}`, { method: 'DELETE' });
        if (!resp.ok) _loadError = describeServerError(resp.status, await readBody(resp));
        else { _loadError = ''; showToast('Server removed'); }
    } catch {
        _loadError = 'Remove failed: could not reach the hub.';
    }
    setBusy(id, false);
    await load();
}

async function onSubmit(e) {
    e.preventDefault();
    const f = e.target;
    // Not f.id: the form has an input named "id" that shadows the property.
    if (!f || !f.matches || !f.matches('#servers-form')) return;
    const adding = _editing === '';
    const val = n => (f.elements[n] ? f.elements[n].value : '');
    const body = {};
    const id = val('id').trim();
    const url = val('url').trim();
    const label = val('label').trim();
    const key = val('api_key');
    if (adding) {
        if (!isValidServerId(id)) { _formError = 'Server id must be 1 to 32 lowercase letters, digits or dashes, and cannot be "local".'; return render(); }
        if (!url) { _formError = 'Enter the server URL.'; return render(); }
        if (!key.trim()) { _formError = 'Enter the remote server\'s API key.'; return render(); }
        body.id = id; body.url = url; body.api_key = key;
        if (label) body.label = label;
        body.allow_private = !!f.elements.allow_private.checked;
    } else {
        const cur = _servers.find(s => s.id === _editing) || {};
        if (label && label !== cur.label) body.label = label;
        if (url && url !== cur.url) body.url = url;
        if (key.trim()) body.api_key = key;       // blank = keep the stored key
        const ap = !!f.elements.allow_private.checked;
        if (ap !== !!cur.allow_private) body.allow_private = ap;
        if (!Object.keys(body).length) { _editing = null; return render(); }
    }
    _draft = { id, label, url, allow_private: !!f.elements.allow_private.checked };
    // Drop the key from the DOM immediately; it only lives in `body` for this request.
    if (f.elements.api_key) f.elements.api_key.value = '';
    setBusy('*', true); _formError = ''; _urlError = '';
    try {
        const resp = await fetch(adding ? '/api/servers' : `/api/servers/${encodeURIComponent(_editing)}`, {
            method: adding ? 'POST' : 'PATCH',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body),
        });
        const data = await readBody(resp);
        if (!resp.ok) {
            const message = describeServerError(resp.status, data);
            if (resp.status === 400 && /\burl\b/i.test(data && data.error || '')) _urlError = message; else _formError = message;
        } else {
            _editing = null; _draft = null;
            showToast(adding ? 'Server added' : 'Server updated');
        }
    } catch {
        _formError = 'Could not reach the hub. Try again.';
    }
    body.api_key = undefined;
    setBusy('*', false);
    if (_editing === null) await load(); else render();
}

