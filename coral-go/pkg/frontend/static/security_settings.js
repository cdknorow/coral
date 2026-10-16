/* Settings > Browser sign-in (#3948): change the password, see which browsers are signed in, sign the others out, and the
 * recent password sign-in attempts. The API is #3949's (GET /api/system/auth-status, GET /api/auth/sessions, POST
 * /api/auth/password/{set,change}, POST /api/auth/sessions/sign-out-others). Every label and address comes from a client and is
 * put in the page with textContent only, never as HTML. None of this exists through Coral Hub. */

import { showToast } from './utils.js';
import { isCloud } from './server_base.js';
import { importOnce } from './import_once.js';

// Lazy modules, awaited here so a retried load of this file re-imports them through importOnce (import_hygiene).
const { passwordProblem, timeMs, timeLeft, ageText } = await importOnce('./password_rules.js');
const { postJson, passwordField, usernameStub, strengthMeter, errorText, showSetPasswordModal } = await importOnce('./password_ui.js');

const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
let _root = null;

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
    _root = root;
    if (isCloud()) { root.hidden = true; return; }
    let st = null;
    try { st = await getJson('/api/system/auth-status'); } catch { /* shown as hidden */ }
    if (!st || st.authenticated === false) { root.hidden = true; return; }
    let sessions = null;
    if (st.password_set) { try { sessions = await getSessions(); } catch { /* the list is optional */ } }
    render(root, st, sessions);
    root.hidden = false;
}

function render(root, st, sessions) {
    root.replaceChildren();
    const head = el('div', 'tb-head');
    const label = el('span', 'tb-label', 'Browser sign-in'); label.id = 'sec-label';
    head.append(label, el('span', 'tb-now', st.password_set ? 'password set' : 'no password'));
    root.setAttribute('aria-labelledby', 'sec-label');
    root.append(head);
    root.append(el('p', 'tb-sub', 'Browsers ask for this password. The Coral app, the tray and coral open sign in by themselves.'));
    if (!st.password_set) {
        const b = el('button', 'btn btn-small', 'Set a password'); b.type = 'button'; b.id = 'sec-set';
        b.addEventListener('click', () => showSetPasswordModal({ local: st.local === true }));
        root.append(b);
        return;
    }
    root.append(changeForm(), sessionsBlock(sessions), el('p', 'tb-note', 'Forgot it? Run coral reset-password on this computer.'));
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
    d.append(form);
    return d;
}

function sessionsBlock(data) {
    const box = el('div', 'sec-sessions');
    if (data && data.needSignIn) { const b = el('div', 'sec-sessions'); b.append(el('p', 'tb-note', 'Sign in in this browser to see signed-in browsers and sign the others out.')); return b; }
    const list = (data && Array.isArray(data.sessions)) ? data.sessions : null;
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
    const others = list.filter(s => !s.current).length;
    const out = el('button', 'btn btn-small', 'Sign out other browsers'); out.type = 'button'; out.id = 'sec-signout-others'; out.disabled = others === 0;
    out.addEventListener('click', async () => {
        out.disabled = true;
        let res;
        try { res = await postJson('/api/auth/sessions/sign-out-others', {}); } catch { res = { ok: false, data: { error: 'Could not reach Coral.' } }; }
        if (!res.ok) { showToast(errorText(res, 'Could not sign out the other browsers.'), true); out.disabled = false; return; }
        const n = Number(res.data.signed_out) || 0;
        showToast(n ? `Signed out ${n} other browser${n === 1 ? '' : 's'}.` : 'No other browsers were signed in.');
        loadSecurityRow();
    });
    box.append(out);
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
