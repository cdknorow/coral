/* The browser password, dashboard side (#3948): the building blocks both screens share (a password field with Show/Hide that
 * password managers understand, the advisory strength meter, one JSON POST helper) and the one-time "Set a password for Coral"
 * prompt, shown to a signed-in browser while no password is set. The API is #3949's: GET /api/system/auth-status, POST
 * /api/auth/password/set. A password is any non-empty text (password_rules.js); nothing here ever trims, rejects or logs one. */

import { showToast } from './utils.js';
import { postJson } from './api.js';
import { importOnce } from './import_once.js';
import { pollEvery } from './overload.js';
import { browserLocal } from './safe_storage.js';

// The pure rules are a lazy module of their own (a lazily loaded module may only statically import startup modules).
const { passwordProblem, strengthOf } = await importOnce('./password_rules.js');

const NOT_NOW_KEY = 'coral-password-not-now';

// sendJson and postJson live in api.js now (a startup module); they are re-exported here for the modules that destructure them from this one.
export { sendJson, postJson } from './api.js';

const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };

/** A labelled password input with a Show/Hide button. `autocomplete` is what tells a password manager to save or fill. */
export function passwordField({ id, label, autocomplete, name }) {
    const wrap = el('label', 'pw-field');
    wrap.append(el('span', 'pw-label', label));
    const row = el('span', 'pw-input-row');
    const input = el('input', 'pw-input');
    input.type = 'password'; input.id = id; input.name = name || id; input.autocomplete = autocomplete;
    input.spellcheck = false; input.autocapitalize = 'off'; input.setAttribute('autocorrect', 'off');
    const toggle = el('button', 'pw-toggle', 'Show');
    toggle.type = 'button'; toggle.setAttribute('aria-pressed', 'false'); toggle.setAttribute('aria-label', `Show ${label.toLowerCase()}`);
    toggle.addEventListener('click', () => {
        const show = input.type === 'password';
        input.type = show ? 'text' : 'password';
        toggle.textContent = show ? 'Hide' : 'Show';
        toggle.setAttribute('aria-pressed', String(show));
        toggle.setAttribute('aria-label', `${show ? 'Hide' : 'Show'} ${label.toLowerCase()}`);
    });
    row.append(input, toggle);
    wrap.append(row);
    return { root: wrap, input };
}

/** The username a password manager files the password under. Hidden: Coral has no user names. */
export function usernameStub() {
    const i = el('input'); i.type = 'text'; i.name = 'username'; i.autocomplete = 'username'; i.value = 'coral'; i.hidden = true;
    i.setAttribute('aria-hidden', 'true'); i.tabIndex = -1; i.readOnly = true;
    return i;
}

/** The strength meter and its words: advice only, it never disables a button. Returns {root, update(password)}. */
export function strengthMeter(id) {
    const root = el('div', 'pw-strength');
    const bar = el('div', 'pw-meter'); bar.setAttribute('role', 'img'); const fill = el('i'); bar.append(fill);
    const text = el('p', 'pw-hint'); text.id = id; text.setAttribute('aria-live', 'polite');
    root.append(bar, text);
    return {
        root,
        update(password) {
            const s = strengthOf(password);
            root.setAttribute('data-level', s.level);
            fill.style.width = `${s.pct}%`;
            bar.setAttribute('aria-label', s.label ? `Strength: ${s.label.replace('.', '')}` : 'Strength');
            text.textContent = '';
            if (s.label) { text.append(el('b', '', s.label)); if (s.hint) text.append(` ${s.hint}`); }
        },
    };
}

/** What a failed password request says: the server's own text, with the wait for a 429. */
export function errorText(res, fallback) {
    if (res.status === 429) return res.data.error || 'Too many attempts. Try again later.';
    return (res.data && res.data.error) || fallback;
}

// ── The one-time prompt ──────────────────────────────────────────────────────────────────────────────

const _notNowGet = () => { try { return browserLocal.get(NOT_NOW_KEY) === '1'; } catch { return false; } };
const _notNowSet = () => { try { browserLocal.set(NOT_NOW_KEY, '1'); } catch { /* private mode: asked again, harmless */ } };

/** GET /api/system/auth-status, or null when it cannot be read. */
async function authStatus() {
    try {
        const r = await fetch('/api/system/auth-status', { credentials: 'same-origin', cache: 'no-store' });
        return r.ok ? await r.json() : null;
    } catch { return null; }
}

/** Ask a signed-in browser to set a password when none is set. "Not now" is final on this computer (the server's own
 *  `local` check says which), and asks again on each visit from another computer. It never opens over another dialog and
 *  never takes the keyboard from an agent's terminal (#294: it covered the New Agent dialog's error, took the Esc a terminal
 *  was sent, and later the words a person typed after a pause). It waits until no dialog is on screen and asks the server
 *  again. Then, with the keyboard in a terminal, it shows a card instead (passwordCard): it never takes the focus, Tab or F6
 *  reach its button, and the button opens the dialog. Otherwise the dialog opens at once, as on a fresh install's first
 *  load. The card stays until a password is set or the dialog is answered, so neither way can be missed. The promise
 *  settles once the card or the dialog is up (or nothing is needed), so the next question (Coral Hub sign-in) stays behind. */
export async function maybePromptSetPassword() {
    if (document.getElementById('set-password-modal') || document.getElementById('set-password-card')) return;
    let st = await authStatus();
    if (!st || st.authenticated === false || st.password_set !== false) return;
    if (st.local === true && _notNowGet()) return;
    if (dialogOpen()) {
        await untilNoDialog();
        if (document.getElementById('set-password-modal') || document.getElementById('set-password-card')) return;
        st = await authStatus(); // a password may have been set meanwhile (in Settings, or from another browser)
        if (!st || st.authenticated === false || st.password_set !== false) return;
        if (st.local === true && _notNowGet()) return;
    }
    if (terminalHasKeyboard()) passwordCard({ local: st.local === true });
    else showSetPasswordModal({ local: st.local === true });
}

/** The keyboard is in an agent's terminal. */
function terminalHasKeyboard() {
    const a = document.activeElement;
    return !!(a && a.closest && a.closest('.xterm'));
}

/** Another dialog is on screen. */
function dialogOpen() {
    return [...document.querySelectorAll('.modal, dialog[open], [role="dialog"][aria-modal="true"]')]
        .some(m => m.id !== 'set-password-modal' && m.style.display !== 'none' && m.getClientRects().length > 0);
}

// How long the page must have no dialog before the prompt opens: one dialog closing as the next opens (Launch, then its
// error) is not a gap to open into.
const FREE_SETTLE_MS = 400;

/** Resolves once no dialog has been on screen for FREE_SETTLE_MS, looking again on any change to the page's dialogs and
 *  every second. */
function untilNoDialog() {
    return new Promise(resolve => {
        let settle = null;
        const look = () => {
            if (dialogOpen()) { clearTimeout(settle); settle = null; return; }
            if (settle) return;
            settle = setTimeout(() => {
                settle = null;
                if (dialogOpen()) return;
                observer.disconnect(); clearInterval(tick);
                resolve();
            }, FREE_SETTLE_MS);
        };
        const observer = new MutationObserver(look);
        observer.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ['style', 'class', 'open', 'hidden'] });
        const tick = setInterval(look, 1000); // interval-ok: a look at this page's own dialogs, no request
        look();
    });
}

/** The card that asks while the keyboard is in a terminal (#294): role="status", placed right after the terminal's area in the
 *  page (so F6 from the terminal lands on its button, and Tab reaches it), never focused by itself. It goes in empty and gets its
 *  words a moment later, so screen readers (NVDA, JAWS) announce them: a live region filled as it is added is often not read. "Set password" opens the dialog with the focus in its field; the card
 *  goes once the dialog is answered, or once a password is set (looked for when the page is focused or shown, and every few
 *  seconds, as the dialog does). */
function passwordCard({ local }) {
    const card = el('div', 'update-toast password-card'); card.id = 'set-password-card';
    card.setAttribute('role', 'status');
    const body = el('div', 'update-toast-body');
    const header = el('div', 'update-toast-header');
    header.append(el('span', 'update-toast-title', 'Set a password for Coral'));
    const text = el('p', 'security-warning-text', 'Browsers on other computers sign in to Coral with it.');
    const open = el('button', 'btn btn-primary', 'Set password'); open.type = 'button'; open.id = 'set-password-card-open';
    body.append(header, text, open);
    let timer = null;
    const remove = () => {
        clearInterval(timer);
        document.removeEventListener('visibilitychange', onVisible);
        window.removeEventListener('focus', recheck);
        card.remove();
    };
    const recheck = async () => {
        const st = await authStatus();
        if (card.isConnected && st && st.password_set === true) remove();
    };
    const onVisible = () => { if (document.visibilityState === 'visible') recheck(); };
    open.addEventListener('click', () => {
        remove();
        showSetPasswordModal({ local });
    });
    document.addEventListener('visibilitychange', onVisible);
    window.addEventListener('focus', recheck);
    timer = pollEvery(recheck, 5000);
    // Right after the terminal's area (its status bar, else the terminal itself): the next control there in the page's order, so F6
    // (#121) and Tab from the terminal reach it. It is drawn at the corner (position: fixed) wherever it sits in the page.
    const term = document.activeElement && document.activeElement.closest ? document.activeElement.closest('.xterm') : null;
    let after = term ? (term.closest('#xterm-container') || term) : null;
    const bar = document.getElementById('terminal-statusbar');
    if (after && bar && bar.previousElementSibling === after) after = bar; // the dashboard's terminal: its bar comes next, then the card
    if (after) after.insertAdjacentElement('afterend', card);
    else document.body.appendChild(card);
    setTimeout(() => { if (card.isConnected) card.append(body); }, 100); // the words a tick after the empty region (see above)
}

export function showSetPasswordModal({ local = false } = {}) {
    const existing = document.getElementById('set-password-modal'); // two callers can race (startup, Settings): one prompt
    if (existing) return existing;
    const opener = document.activeElement;
    const modal = el('div', 'modal'); modal.id = 'set-password-modal';
    modal.setAttribute('role', 'dialog'); modal.setAttribute('aria-modal', 'true'); modal.setAttribute('aria-labelledby', 'set-password-title');
    const content = el('div', 'modal-content modal-md');
    const head = el('div', 'modal-header'); const title = el('h3', '', 'Set a password for Coral'); title.id = 'set-password-title'; head.append(title);
    const body = el('div', 'modal-body');
    // One wording for everyone (a new install and an upgrade alike): what the password is for. The server has no first-run marker, and nobody needs "nothing changed here" (#6010).
    body.append(el('p', 'pw-lead', 'Choose a password to open Coral in a browser. The Coral app and the tray open without it.'));
    const form = el('form', 'pw-form'); form.autocomplete = 'on'; form.id = 'set-password-form';
    const pw = passwordField({ id: 'set-password-new', label: 'Password', autocomplete: 'new-password', name: 'new-password' });
    pw.input.setAttribute('aria-describedby', 'set-password-strength');
    const meter = strengthMeter('set-password-strength');
    const confirm = passwordField({ id: 'set-password-confirm', label: 'Confirm password', autocomplete: 'new-password', name: 'confirm-password' });
    const err = el('p', 'pw-error'); err.id = 'set-password-error'; err.setAttribute('role', 'alert'); err.hidden = true;
    form.append(usernameStub(), pw.root, meter.root, confirm.root, err);
    body.append(form, el('p', 'pw-note', 'There is no email: if you forget it, run coral reset-password on this computer.'));
    const foot = el('div', 'modal-actions modal-footer');
    const later = el('button', 'btn', 'Not now'); later.type = 'button'; later.id = 'set-password-later';
    const save = el('button', 'btn btn-primary', 'Set password'); save.type = 'submit'; save.setAttribute('form', 'set-password-form'); save.id = 'set-password-save';
    foot.append(later, save);
    content.append(head, body, foot); modal.append(content);
    document.body.append(modal);

    // The prompt can outlive the question: a password set in another browser while this page stays open (the Coral app keeps its page
    // between uses) must not leave the prompt up. Ask the server again when the page is looked at again, and every few seconds meanwhile.
    let recheckTimer = null;
    const recheck = async () => {
        if (!modal.isConnected) return;
        const st = await authStatus();
        if (!modal.isConnected || !st || st.password_set !== true) return;
        close();
        showToast('A password is already set. Change it in Settings, Security.');
        document.dispatchEvent(new CustomEvent('coral:password-changed'));
    };
    const onVisible = () => { if (document.visibilityState === 'visible') recheck(); };
    document.addEventListener('visibilitychange', onVisible);
    window.addEventListener('focus', recheck);
    recheckTimer = pollEvery(recheck, 5000);
    const close = () => {
        document.removeEventListener('keydown', onKey, true);
        document.removeEventListener('visibilitychange', onVisible);
        window.removeEventListener('focus', recheck);
        clearInterval(recheckTimer);
        modal.remove();
        if (opener && opener.focus && opener.isConnected) opener.focus();
        document.dispatchEvent(new CustomEvent('coral:password-prompt-closed'));
    };
    const notNow = () => { if (local) _notNowSet(); close(); };
    const onKey = e => {
        if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); notNow(); return; }
        if (e.key !== 'Tab') return;
        const items = [...modal.querySelectorAll('input:not([hidden]), button')].filter(x => !x.disabled);
        if (!items.length) return;
        const first = items[0], last = items[items.length - 1];
        if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
        else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
    };
    document.addEventListener('keydown', onKey, true);
    pw.input.addEventListener('input', () => meter.update(pw.input.value));
    later.addEventListener('click', notNow);
    form.addEventListener('submit', async e => {
        e.preventDefault();
        const problem = passwordProblem(pw.input.value, confirm.input.value);
        err.hidden = !problem; err.textContent = problem;
        if (problem) return;
        save.disabled = true; save.textContent = 'Setting…';
        let res;
        try { res = await postJson('/api/auth/password/set', { password: pw.input.value }); }
        catch { res = { ok: false, status: 0, data: { error: 'Could not reach Coral. Try again.' } }; }
        if (res.ok || res.status === 409) {
            close();
            showToast(res.ok ? 'Password set. Browsers on other computers can now sign in with it.' : 'A password is already set. Change it in Settings, Security.');
            document.dispatchEvent(new CustomEvent('coral:password-changed'));
            return;
        }
        err.hidden = false; err.textContent = errorText(res, 'Could not set the password.');
        save.disabled = false; save.textContent = 'Set password';
    });
    pw.input.focus();
    return modal;
}
