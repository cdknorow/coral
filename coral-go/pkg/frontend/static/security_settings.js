/* Settings > Browser sign-in (#3948): change the password, see which browsers are signed in, sign the others out, and the
 * recent password sign-in attempts. The API is #3949's (GET /api/system/auth-status, GET /api/auth/sessions, POST
 * /api/auth/password/{set,change}, POST /api/auth/sessions/sign-out-others). Every label and address comes from a client and is
 * put in the page with textContent only, never as HTML. None of this exists through Coral Hub. */

import { showToast } from './utils.js';
import { isCloud } from './server_base.js';
import { importOnce } from './import_once.js';
import { signOutThisBrowser } from './local_store.js';

// Lazy modules, awaited here so a retried load of this file re-imports them through importOnce (import_hygiene).
const { passwordProblem, timeMs, timeLeft, ageText } = await importOnce('./password_rules.js');
const { postJson, sendJson, passwordField, usernameStub, strengthMeter, errorText, showSetPasswordModal } = await importOnce('./password_ui.js');

const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };

async function getJson(url) {
    const r = await fetch(url, { credentials: 'same-origin', cache: 'no-store' });
    return r.ok ? r.json() : null;
}

/** The signed-in browsers list. A caller with no browser sign-in at all gets 403 password_session_required: shown inline as a
 *  note, never as "signed out" (any sign-in, a link, the app, the tray or the password, can list the browsers). */
async function getSessions() {
    const r = await fetch('/api/auth/sessions', { credentials: 'same-origin', cache: 'no-store' });
    if (r.ok) return r.json();
    let body = {};
    try { body = await r.json(); } catch { /* no body */ }
    return r.status === 403 && body.code === 'password_session_required' ? { needSignIn: true } : null;
}

/** Called when Settings opens: fills the row (or leaves it hidden where it does not apply). */
export async function loadSecurityRow() {
    const root = document.getElementById('settings-security');
    if (!root) return;
    if (isCloud()) { root.hidden = true; return; }
    let st = null;
    try { st = await getJson('/api/system/auth-status'); } catch { /* shown as hidden */ }
    if (!st || st.authenticated === false) { root.hidden = true; return; }
    let sessions = null;
    if (st.password_set) { try { sessions = await getSessions(); } catch { /* the list is optional */ } }
    render(root, st, sessions);
    root.hidden = false;
    if (st.method === 'session') appendHub(root, sessions);   // only for a browser session, and only where this computer is linked to Coral Hub (#3966)
}

function render(root, st, sessions) {
    root.replaceChildren();
    const head = el('div', 'tb-head');
    const label = el('span', 'tb-label', 'Browser sign-in'); label.id = 'sec-label';
    // The one sentence that explains the row is behind an (i): a button that says it is about this row, closed at first, the text in the page.
    const why = el('button', 'settings-info-btn', ''); why.type = 'button'; why.id = 'sec-why';
    why.setAttribute('aria-expanded', 'false'); why.setAttribute('aria-controls', 'sec-why-text'); why.setAttribute('aria-label', 'About browser sign-in');
    const icon = el('span', 'material-icons', 'info'); icon.setAttribute('aria-hidden', 'true'); why.append(icon);
    const whyText = el('p', 'settings-info-text', 'Browsers ask for this password. The Coral app, the tray and coral open sign in by themselves.'); whyText.id = 'sec-why-text'; whyText.hidden = true;
    why.addEventListener('click', () => { whyText.hidden = !whyText.hidden; why.setAttribute('aria-expanded', String(!whyText.hidden)); });
    head.append(label, why, el('span', 'tb-now', st.password_set ? 'password set' : 'no password'));
    root.setAttribute('aria-labelledby', 'sec-label');
    root.append(head, whyText);
    if (!st.password_set) {
        const b = el('button', 'btn btn-small', 'Set a password'); b.type = 'button'; b.id = 'sec-set';
        b.addEventListener('click', () => showSetPasswordModal({ local: st.local === true }));
        root.append(b);
        return;
    }
    root.append(changeForm(), sessionsBlock(sessions));
}

function changeForm() {
    const d = el('details', 'sec-change'); d.id = 'sec-change';
    d.append(el('summary', '', 'Change password'));
    const form = el('form', 'pw-form'); form.autocomplete = 'on';
    const cur = passwordField({ id: 'sec-current', label: 'Current password', autocomplete: 'current-password', name: 'current-password' });
    const nw = passwordField({ id: 'sec-new', label: 'New password', autocomplete: 'new-password', name: 'new-password' });
    nw.input.setAttribute('aria-describedby', 'sec-strength');
    const meter = strengthMeter('sec-strength');
    const cf = passwordField({ id: 'sec-confirm', label: 'Confirm new password', autocomplete: 'new-password', name: 'confirm-password' });
    const err = el('p', 'pw-error'); err.id = 'sec-error'; err.setAttribute('role', 'alert'); err.hidden = true;
    const btn = el('button', 'btn btn-primary btn-small', 'Change password'); btn.type = 'submit'; btn.id = 'sec-change-save';
    form.append(usernameStub(), cur.root, nw.root, meter.root, cf.root, err, btn);
    nw.input.addEventListener('input', () => meter.update(nw.input.value));
    form.addEventListener('submit', async e => {
        e.preventDefault();
        const problem = !cur.input.value ? 'Enter your current password.' : passwordProblem(nw.input.value, cf.input.value);
        err.hidden = !problem; err.textContent = problem;
        if (problem) return;
        btn.disabled = true; btn.textContent = 'Changing…';
        let res;
        try { res = await postJson('/api/auth/password/change', { current: cur.input.value, password: nw.input.value }); }
        catch { res = { ok: false, status: 0, data: { error: 'Could not reach Coral. Try again.' } }; }
        btn.disabled = false; btn.textContent = 'Change password';
        if (!res.ok) { err.hidden = false; err.textContent = errorText(res, 'Could not change the password.'); return; }
        const n = Number(res.data.signed_out) || 0;
        showToast(n ? `Password changed. ${n} other browser${n === 1 ? '' : 's'} signed out.` : 'Password changed.');
        form.reset(); meter.update('');
        document.dispatchEvent(new CustomEvent('coral:password-changed'));
        loadSecurityRow();
    });
    d.append(form, el('p', 'tb-note', 'Forgot it? Run coral reset-password on this computer.'));
    return d;
}

function sessionsBlock(data) {
    const box = el('div', 'sec-sessions');
    if (data && data.needSignIn) { const b = el('div', 'sec-sessions'); b.append(el('p', 'tb-note', 'Sign in here to manage browsers.')); return b; }
    const all = (data && Array.isArray(data.sessions)) ? data.sessions : null;
    const list = all && all.filter(s => s.method !== 'hub');   // Coral Hub sign-ins are listed, with Sign out, in their own block
    if (!list) { box.append(el('p', 'tb-note', 'The list of signed-in browsers could not be loaded.')); return box; }
    const h = el('div', 'sec-sub-head', 'Signed-in browsers '); h.append(el('span', 'sec-count', `(${list.length})`));
    box.append(h);
    const ul = el('ul', 'sec-list'); ul.id = 'sec-sessions';
    for (const s of list) {
        const li = el('li', 'sec-item');
        const who = el('span', 'sec-who', String(s.label || 'Unknown browser'));
        if (s.current) who.append(' ', el('b', '', '(this browser)'));
        const when = [s.current ? 'now' : ageText(timeMs(s.last_seen)), s.persistent ? timeLeft(timeMs(s.expires_at)) : 'until it closes'].filter(Boolean).join(' · ');
        li.append(who, el('span', 'sec-when', when));
        ul.append(li);
    }
    box.append(ul);
    const others = all.filter(s => !s.current).length;
    const out = el('button', 'btn btn-small', 'Sign out other browsers'); out.type = 'button'; out.id = 'sec-signout-others'; out.disabled = others === 0;
    out.addEventListener('click', async () => {
        out.disabled = true;
        let res;
        try { res = await postJson('/api/auth/sessions/sign-out-others', {}); } catch { res = { ok: false, data: { error: 'Could not reach Coral.' } }; }
        if (!res.ok) { showToast(errorText(res, 'Could not sign out the other browsers.'), true); out.disabled = false; return; }
        const n = Number(res.data.signed_out) || 0;
        showToast(n ? `Signed out ${n} other browser${n === 1 ? '' : 's'}.` : 'No other browsers were signed in.');
        await loadSecurityRow();
        document.getElementById('sec-signout')?.focus(); // the button just used is redrawn disabled (nobody else is signed in now): keep focus in the block
    });
    // This browser's own Sign out, next to it: confirm-free (signing in again undoes it), a plain button for keyboard and touch.
    const me = el('button', 'btn btn-small', 'Sign out'); me.type = 'button'; me.id = 'sec-signout';
    me.addEventListener('click', () => { me.disabled = true; out.disabled = true; signOutThisBrowser(); });
    const actions = el('div', 'sec-actions'); actions.append(out, me);
    box.append(actions);
    const recent = (data && Array.isArray(data.recent)) ? data.recent : [];
    if (recent.length) {
        const d = el('details', 'sec-recent'); d.id = 'sec-recent';
        d.append(el('summary', '', `Recent password sign-ins (${recent.length})`));
        const rl = el('ul', 'sec-list');
        for (const r of recent) {
            const li = el('li', 'sec-item' + (r.ok ? '' : ' sec-failed'));
            const t = timeMs(r.at);
            li.append(el('span', 'sec-who', `${r.ok ? 'Signed in' : 'Wrong password'} · ${r.address || 'unknown address'} · ${r.label || 'unknown browser'}`), el('span', 'sec-when', Number.isFinite(t) ? new Date(t).toLocaleString() : ''));
            rl.append(li);
        }
        d.append(rl); box.append(d);
    }
    return box;
}

// ── Sign in with Coral Hub (#3966) ────────────────────────────────────────────────────────────────────────────
// The switch writes only through PUT /api/auth/hub-signin, at once (it is not part of the dialog's Apply, so a stale form can
// never turn it on); every state shown is read back from the server after a change.

async function hubGet(url) {
    try { const r = await fetch(url, { credentials: 'same-origin', cache: 'no-store' }); return r.ok ? await r.json() : null; } catch { return null; }
}

async function appendHub(root, sessions) {
    const st = await hubGet('/api/auth/hub-signin');
    root.querySelector('#sec-hub')?.remove();
    if (!st || !st.linked) return;   // nothing to show, and nothing to switch on, until this computer is linked
    const origins = await hubGet('/api/auth/hub-origins');
    const hubSessions = (sessions && Array.isArray(sessions.sessions) ? sessions.sessions : []).filter(s => s.method === 'hub');
    root.append(hubBlock(st, origins && Array.isArray(origins.origins) ? origins.origins : [], hubSessions));
}

function hubBlock(st, origins, hubSessions) {
    const box = el('div', 'sec-sessions sec-hub'); box.id = 'sec-hub';
    box.append(el('div', 'sec-sub-head', 'Sign in with Coral Hub'));
    const row = el('label', 'sec-switch');
    const cb = el('input'); cb.type = 'checkbox'; cb.id = 'sec-hub-enabled'; cb.checked = st.enabled === true;
    const txt = el('span');
    txt.append(el('b', '', 'Let Coral Hub sign in here'), document.createElement('br'), el('span', 'sec-muted', st.owner_known === false
        ? 'Off by default. Sign-in stays off until Coral Hub reports the account that linked this computer.'
        : 'Off by default. Only the Coral Hub account that linked this computer can use it.'));
    row.append(cb, txt);
    const warn = el('div', 'sec-warn'); warn.id = 'sec-hub-warn';
    const wi = el('span', '', '\u26A0'); const wt = el('span');
    wt.append(el('b', '', 'This lets your Coral Hub account run commands on this computer.'), ' A Coral Hub sign-in has full control, like your password. Turning it off signs out every Coral Hub session at once.');
    warn.append(wi, wt);
    box.append(row, warn, el('p', 'tb-note', 'Turned off while "Don\'t let Coral Hub act as the Operator" is on.'));
    cb.addEventListener('change', async () => {
        const want = cb.checked;
        cb.disabled = true;
        let res;
        try { res = await sendJson('PUT', '/api/auth/hub-signin', { enabled: want }); } catch { res = { ok: false, data: { error: 'Could not reach Coral.' } }; }
        if (!res.ok) showToast(errorText(res, 'Could not change Sign in with Coral Hub.'), true);
        loadSecurityRow();   // the server's answer is what shows, whatever happened
    });

    box.append(el('div', 'sec-sub-head sec-gap', 'Return addresses'));
    const ul = el('ul', 'sec-list'); ul.id = 'sec-hub-origins';
    // The server always sends the hub the three loopback origins on this port, always as http:// (hub_signin.go), whatever the page's scheme.
    const port = location.port ? `:${location.port}` : '';
    for (const host of ['localhost', '127.0.0.1', '[::1]']) {
        const always = el('li', 'sec-item'); always.append(el('span', 'sec-who', `http://${host}${port}`), el('span', 'sec-when', 'always'));
        ul.append(always);
    }
    for (const o of origins) {
        const li = el('li', 'sec-item');
        const rm = el('button', 'btn btn-small', 'Remove'); rm.type = 'button';
        rm.setAttribute('aria-label', `Remove ${o}`);
        rm.addEventListener('click', () => putOrigins(origins.filter(x => x !== o), err));
        const when = el('span', 'sec-when'); when.append(rm);
        li.append(el('span', 'sec-who', o), when);
        ul.append(li);
    }
    const add = el('div', 'sec-add');
    const input = el('input', 'pw-input'); input.type = 'text'; input.id = 'sec-hub-origin'; input.placeholder = 'https://host:port'; input.setAttribute('aria-label', 'New return address'); input.style.paddingRight = '11px';
    const addBtn = el('button', 'btn btn-small', 'Add'); addBtn.type = 'button'; addBtn.id = 'sec-hub-origin-add';
    const err = el('p', 'pw-error'); err.id = 'sec-hub-origin-error'; err.setAttribute('role', 'alert'); err.hidden = true;
    addBtn.addEventListener('click', () => {
        const v = input.value.trim();
        if (!v) { err.hidden = false; err.textContent = 'Enter an address.'; return; }
        putOrigins([...origins, v], err);
    });
    add.append(input, addBtn);
    box.append(ul, add, err);

    box.append(el('div', 'sec-sub-head sec-gap', `Coral Hub sign-ins now (${hubSessions.length})`));
    const sl = el('ul', 'sec-list'); sl.id = 'sec-hub-sessions';
    if (!hubSessions.length) sl.append(el('li', 'sec-item sec-muted', 'None'));
    for (const s of hubSessions) {
        const li = el('li', 'sec-item');
        const out = el('button', 'btn btn-small', 'Sign out'); out.type = 'button';
        out.setAttribute('aria-label', `Sign out ${s.label || 'this Coral Hub sign-in'}`);
        out.addEventListener('click', async () => {
            out.disabled = true;
            let res;
            try { res = await sendJson('DELETE', `/api/auth/sessions/${encodeURIComponent(s.id)}`); } catch { res = { ok: false, data: { error: 'Could not reach Coral.' } }; }
            if (!res.ok) { showToast(errorText(res, 'Could not sign that out.'), true); out.disabled = false; return; }
            loadSecurityRow();
        });
        const when = el('span', 'sec-when', `${ageText(timeMs(s.last_seen))} `); when.append(out);
        li.append(el('span', 'sec-who', String(s.label || 'Coral Hub')), when);
        sl.append(li);
    }
    box.append(sl);
    return box;
}

async function putOrigins(list, err) {
    err.hidden = true;
    let res;
    try { res = await sendJson('PUT', '/api/auth/hub-origins', { origins: list }); } catch { res = { ok: false, data: { error: 'Could not reach Coral.' } }; }
    if (!res.ok) { err.hidden = false; err.textContent = errorText(res, 'That address was not accepted.'); return; }
    loadSecurityRow();
}
