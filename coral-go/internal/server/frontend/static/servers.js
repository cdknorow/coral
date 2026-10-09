/* Servers settings view (multi-server hub).
 *
 * A self-contained dialog, built on demand, to list / add / edit / test /
 * remove the remote Coral servers the hub talks to. Everything here is
 * hub-local (/api/servers*, never proxied) and gated on isHub(). The remote
 * API key is write-only: it is sent on add/edit and never rendered, echoed,
 * logged or kept after the request completes. */

import { state } from './state.js';
import { isHub, initHub } from './server_base.js';
import { showToast } from './utils.js';
import {
    statusMeta, escHtml, describeServerError, formatLastSeen, isValidServerId,
} from './server_status.js';

let _servers = [];
let _editing = null;      // id being edited, '' for "add", null when the form is closed
let _confirmDelete = null;
let _busy = new Set();    // ids (or '*') with a request in flight
let _loadError = '';
let _formError = '';
let _draft = null;        // non-secret field values kept across a failed submit
let _timer = null;
let _lastFocus = null;

/** Unhide hub-only entry points. Safe to call on every page load. */
export async function initServersUi() {
    try { await initHub(); } catch { /* treated as non-hub */ }
    const hub = isHub();
    document.querySelectorAll('[data-hub-only]').forEach(el => { el.style.display = hub ? '' : 'none'; });
    return hub;
}

function modalEl() {
    let m = document.getElementById('servers-modal');
    if (!m) {
        m = document.createElement('div');
        m.id = 'servers-modal';
        m.className = 'modal';
        m.setAttribute('role', 'dialog');
        m.setAttribute('aria-modal', 'true');
        m.setAttribute('aria-labelledby', 'servers-modal-title');
        m.style.display = 'none';
        m.addEventListener('mousedown', e => { if (e.target === m) hideServersModal(); });
        m.addEventListener('keydown', e => { if (e.key === 'Escape') { e.stopPropagation(); hideServersModal(); } });
        m.addEventListener('click', onClick);
        m.addEventListener('submit', onSubmit);
        document.body.appendChild(m);
    }
    return m;
}

export async function showServersModal() {
    if (!isHub()) return;   // hub features are off: no UI, no /api/servers calls
    const m = modalEl();
    _editing = null; _draft = null; _confirmDelete = null; _formError = ''; _loadError = '';
    _lastFocus = document.activeElement;
    m.style.display = 'flex';
    render();
    await load();
    clearInterval(_timer);
    // Keep status / last-seen fresh while the page is open.
    _timer = setInterval(() => { if (_editing === null && !_busy.size) load(); }, 5000);
    const first = m.querySelector('[data-action="close"]');
    if (first) first.focus();
}

export function hideServersModal() {
    clearInterval(_timer); _timer = null;
    const m = document.getElementById('servers-modal');
    if (m) m.style.display = 'none';
    _editing = null;
    if (_lastFocus && _lastFocus.focus) { try { _lastFocus.focus(); } catch { /* ignore */ } }
}

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

function rowHtml(s) {
    if (s.local || s.id === 'local') {
        return `<li class="servers-row servers-row-local">
            <div class="servers-row-main"><div class="servers-row-title">${escHtml(s.label || 'Local')} <span class="servers-id">local</span></div>
            <div class="servers-row-meta">This hub. Always available.</div></div>
            <div class="servers-row-status">${statusPill('online')}</div></li>`;
    }
    const busy = _busy.has(s.id);
    const confirming = _confirmDelete === s.id;
    const err = s.last_error ? `<div class="servers-row-error" role="status">${escHtml(s.last_error)}</div>` : '';
    const hint = (s.status === 'key_unreadable' || s.status === 'unauthorized')
        ? `<div class="servers-row-error">${escHtml(statusMeta(s.status).hint)}</div>` : '';
    return `<li class="servers-row" data-id="${escHtml(s.id)}">
        <div class="servers-row-main">
            <div class="servers-row-title">${escHtml(s.label || s.id)} <span class="servers-id">${escHtml(s.id)}</span>${s.allow_private ? ' <span class="servers-tag" title="Private network addresses are allowed for this server">private network</span>' : ''}</div>
            <div class="servers-row-meta">${escHtml(s.url)} &middot; last seen ${escHtml(formatLastSeen(s.last_seen))}</div>
            ${err}${hint}
        </div>
        <div class="servers-row-status">${statusPill(s.status)}</div>
        <div class="servers-row-actions">
            <button type="button" class="btn btn-small" data-action="test" data-id="${escHtml(s.id)}"${busy ? ' disabled aria-busy="true"' : ''}>${busy ? 'Testing...' : 'Test'}</button>
            <button type="button" class="btn btn-small" data-action="edit" data-id="${escHtml(s.id)}"${busy ? ' disabled' : ''}>Edit</button>
            ${confirming
                ? `<button type="button" class="btn btn-small btn-danger" data-action="delete-confirm" data-id="${escHtml(s.id)}">Confirm remove</button>
                   <button type="button" class="btn btn-small" data-action="delete-cancel">Cancel</button>`
                : `<button type="button" class="btn btn-small btn-danger-text" data-action="delete" data-id="${escHtml(s.id)}"${busy ? ' disabled' : ''}>Remove</button>`}
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
            <input type="url" name="url" value="${escHtml(cur.url || '')}" placeholder="https://coral.example.com:8420" required>
            <span class="servers-help">Scheme, host and port only.</span></label>
        <label>API key
            <input type="password" name="api_key" value="" autocomplete="new-password" spellcheck="false" placeholder="${adding ? 'Paste the remote server\'s API key' : 'Leave blank to keep the current key'}" ${adding ? 'required' : ''}>
            <span class="servers-help">Write-only. The key is stored encrypted and is never shown again.</span></label>
        <label class="servers-check"><input type="checkbox" name="allow_private" ${cur.allow_private ? 'checked' : ''}>
            <span>Allow private network address
            <span class="servers-help servers-warning">Only tick this for a server on your LAN, tailnet or localhost that you trust. It lets this hub connect to private and reserved addresses, which is normally blocked to prevent requests being steered at internal services.</span></span></label>
        <div class="servers-form-actions">
            <button type="button" class="btn" data-action="cancel-form"${busy ? ' disabled' : ''}>Cancel</button>
            <button type="submit" class="btn btn-primary"${busy ? ' disabled aria-busy="true"' : ''}>${busy ? 'Checking connection...' : (adding ? 'Add server' : 'Save')}</button>
        </div>
    </form>`;
}

function render() {
    const m = modalEl();
    const remotes = _servers.filter(s => !(s.local || s.id === 'local'));
    const local = _servers.find(s => s.local || s.id === 'local') || { local: true, id: 'local', label: 'Local' };
    // Preserve typed form values across the periodic refresh (never the key).
    m.innerHTML = `<div class="modal-content modal-content-wide servers-modal-content">
        <div class="modal-header">
            <h3 id="servers-modal-title">Servers</h3>
            <button type="button" class="modal-close-btn" data-action="close" aria-label="Close dialog"><span class="material-icons" aria-hidden="true">close</span></button>
        </div>
        <div class="modal-body">
            <p class="servers-intro">Servers whose agents and teams appear in this hub. The hub stores each server's API key (encrypted); a key grants full control of that server.</p>
            ${_loadError ? `<div class="servers-form-error" role="alert">${escHtml(_loadError)}</div>` : ''}
            <ul class="servers-list" aria-label="Registered servers">${rowHtml(local)}${remotes.map(rowHtml).join('')}</ul>
            ${!remotes.length && !_loadError ? '<p class="servers-empty">No remote servers yet.</p>' : ''}
            ${_editing !== null ? formHtml() : '<div class="servers-form-actions"><button type="button" class="btn btn-primary" data-action="add">Add server</button></div>'}
        </div>
    </div>`;
    if (_editing !== null) {
        const f = m.querySelector('#servers-form');
        const target = f && (f.elements[_editing === '' ? 'id' : 'label']);
        if (target) target.focus();
    }
}

function setBusy(id, on) { if (on) _busy.add(id); else _busy.delete(id); }

async function onClick(e) {
    const btn = e.target.closest('[data-action]');
    if (!btn) return;
    const action = btn.dataset.action;
    const id = btn.dataset.id;
    if (action === 'close') return hideServersModal();
    if (action === 'add') { _editing = ''; _formError = ''; _draft = null; return render(); }
    if (action === 'edit') { _editing = id; _formError = ''; _draft = null; _confirmDelete = null; return render(); }
    if (action === 'cancel-form') { _editing = null; _formError = ''; _draft = null; return render(); }
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
    setBusy('*', true); _formError = '';
    try {
        const resp = await fetch(adding ? '/api/servers' : `/api/servers/${encodeURIComponent(_editing)}`, {
            method: adding ? 'POST' : 'PATCH',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body),
        });
        const data = await readBody(resp);
        if (!resp.ok) {
            _formError = describeServerError(resp.status, data);
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

// Exposed for tests / other modules.
export function _serversState() { return { servers: _servers, editing: _editing, hub: state.hub }; }
