/* The one-time "Let Coral Hub sign you in here?" notice (#3966) for a computer that was linked to Coral Hub before Hub sign-in
 * existed. The server says when to offer it (GET /api/auth/hub-signin: offer). Hub sign-in stays off unless the person ticks
 * it here or in Settings; "Not now" is final (POST /api/auth/hub-signin/dismiss) and Settings, Browser sign-in can still turn
 * it on. The switch is written only through PUT /api/auth/hub-signin, as in Settings. Never shown through Coral Hub. */

import { showToast } from './utils.js';
import { isCloud } from './server_base.js';
import { importOnce } from './import_once.js';

// Asked for when used, not awaited at the top level: a module that awaits a bundled module at load can wait on the stub of a module that waits for it (#4326).
const sendJson = (...args) => importOnce('./password_ui.js').then(m => m.sendJson(...args));

const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };

async function hubSigninState() {
    try {
        // The Hub sign-in settings are for a browser signed in with a session (a link, the app, the tray or the password). A
        // dashboard reached with no cookie (strict off) or an API key is answered 401 dashboard_auth_required, which the page
        // treats as "signed out": so ask only when auth-status says this browser has a session.
        const a = await fetch('/api/system/auth-status', { credentials: 'same-origin', cache: 'no-store' });
        const st = a.ok ? await a.json() : null;
        if (!st || st.method !== 'session') return null;
        const r = await fetch('/api/auth/hub-signin', { credentials: 'same-origin', cache: 'no-store' });
        return r.ok ? await r.json() : null;
    } catch { return null; }
}

/** Offer Hub sign-in once, after the set-a-password prompt (if there is one) has been answered. */
export async function maybeOfferHubSignin() {
    if (isCloud() || document.getElementById('hub-signin-notice')) return;
    if (document.getElementById('set-password-modal')) {   // one question at a time: wait for the password prompt to close
        document.addEventListener('coral:password-prompt-closed', () => maybeOfferHubSignin(), { once: true });
        return;
    }
    const st = await hubSigninState();
    if (!st || st.offer !== true || st.enabled === true) return;
    showHubNotice();
}

function showHubNotice() {
    const opener = document.activeElement;
    const modal = el('div', 'modal'); modal.id = 'hub-signin-notice';
    modal.setAttribute('role', 'dialog'); modal.setAttribute('aria-modal', 'true'); modal.setAttribute('aria-labelledby', 'hub-signin-notice-title');
    const content = el('div', 'modal-content modal-md');
    const head = el('div', 'modal-header'); const title = el('h3', '', 'Let Coral Hub sign you in here?'); title.id = 'hub-signin-notice-title'; head.append(title);
    const body = el('div', 'modal-body');
    body.append(el('p', 'pw-lead', 'You linked this computer to Coral Hub. You can let your Coral Hub account sign in to this dashboard from any browser, instead of a password.'));
    const warn = el('div', 'sec-warn'); const wt = el('span');
    wt.append(el('b', '', 'It gives full control of this computer.'), ' Anyone who gets into your Coral Hub account could run commands here.');
    warn.append(el('span', '', '⚠'), wt);
    body.append(warn, el('p', 'pw-note', 'Not now is final. You can turn it on later in Settings, Browser sign-in.'));
    const foot = el('div', 'modal-actions modal-footer');
    const no = el('button', 'btn', 'Not now'); no.type = 'button'; no.id = 'hub-notice-no';
    const yes = el('button', 'btn btn-primary', 'Let Coral Hub sign me in'); yes.type = 'button'; yes.id = 'hub-notice-yes';
    foot.append(no, yes);
    content.append(head, body, foot); modal.append(content);
    document.body.append(modal);

    const close = () => { document.removeEventListener('keydown', onKey, true); modal.remove(); if (opener && opener.focus && opener.isConnected) opener.focus(); };
    const dismiss = async () => { try { await sendJson('POST', '/api/auth/hub-signin/dismiss', {}); } catch { /* the notice may come back next visit: harmless */ } };
    const notNow = async () => { close(); await dismiss(); };
    const onKey = e => {
        if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); notNow(); return; }
        if (e.key !== 'Tab') return;
        const items = [...modal.querySelectorAll('button')].filter(x => !x.disabled);
        const first = items[0], last = items[items.length - 1];
        if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
        else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
    };
    document.addEventListener('keydown', onKey, true);
    no.addEventListener('click', notNow);
    yes.addEventListener('click', async () => {
        yes.disabled = true; no.disabled = true;
        let res;
        try { res = await sendJson('PUT', '/api/auth/hub-signin', { enabled: true }); } catch { res = { ok: false, data: {} }; }
        if (!res.ok) { showToast((res.data && res.data.error) || 'Could not turn on Sign in with Coral Hub.', true); yes.disabled = false; no.disabled = false; return; }
        close(); await dismiss();
        showToast('Sign in with Coral Hub is on. You can turn it off in Settings, Browser sign-in.');
        document.dispatchEvent(new CustomEvent('coral:hub-signin-changed'));
    });
    yes.focus();
    return modal;
}
