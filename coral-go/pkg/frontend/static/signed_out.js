/* "Coral now requires sign-in" (#3939). With strict local auth a dashboard tab with no sign-in session (an old tab, a bookmark opened before the
 * switch, a session that ended) gets 401 from /api. Instead of broken views and silent failures, the page shows one modal panel that
 * says how to get in, and every poller and the live feed wait until a call succeeds. A 401 that is not "signed out" (an agent-only route, an identity
 * refusal) is not handled here: see isSignedOut401 in auth_redirect.js. The Coral Hub has its own sign-in (cloud.js). */

import { state } from './state.js';
import { setSignedOut, isSignedOut } from './overload.js';

/** The /auth URL for the current page: return_to is this page's own path (same origin). */
export function authUrlFor(loc) {
    const back = (loc.pathname || '/') + (loc.search || '') + (loc.hash || '');
    return '/auth?return_to=' + encodeURIComponent(back.startsWith('/') && !back.startsWith('//') ? back : '/');
}

let _dialog = null;
let _checking = false;

function el(tag, props = {}, ...kids) {
    const n = document.createElement(tag);
    for (const [k, v] of Object.entries(props)) { if (k === 'text') n.textContent = v; else n.setAttribute(k, v); }
    n.append(...kids);
    return n;
}

/** One call to an authenticated route: signed in again when it answers 2xx. */
async function stillSignedOut() {
    try {
        const r = await fetch('/api/settings', { cache: 'no-store', credentials: 'same-origin' });
        return !r.ok; // only a 2xx is signed in
    } catch { return true; } // unreachable: not signed in, as far as this page can tell
}

// at most one unprompted reload a minute
function mayAutoReload() {
    try { const n = Date.now(); if (n - +sessionStorage.getItem('so') < 6e4) return false; sessionStorage.setItem('so', n); } catch { return false; } // no storage: never unprompted
    return true;
}

/** Try again (the button, or the tab coming back to the front after the sign-in): signed in -> reload the page, which restores the view. */
async function retrySignIn({ quiet = false, auto = false } = {}) {
    if (_checking || !isSignedOut()) return false;
    _checking = true;
    const status = _dialog && _dialog.querySelector('#signed-out-status');
    if (status && !quiet) status.textContent = 'Checking…';
    try {
        if (await stillSignedOut()) {
            if (status && !quiet) status.textContent = 'Still not signed in.';
            return false;
        }
        if (auto && !mayAutoReload()) return false;
        location.reload();
        return true;
    } finally { _checking = false; }
}

export const recheckSignedOut = () => retrySignIn({ quiet: true, auto: true });

function show() {
    if (_dialog) return;
    const dlg = el('dialog', { id: 'signed-out-panel', class: 'signed-out-panel', 'aria-labelledby': 'signed-out-title', 'aria-describedby': 'signed-out-text' },
        el('h2', { id: 'signed-out-title', text: 'Coral now requires sign-in' }),
        el('p', { id: 'signed-out-text' }, 'This page has no sign-in session, so it cannot load your agents. Open Coral from the Coral app or the tray menu (they sign you in), or run ',
            el('code', { text: 'coral open' }), ' in a terminal on this computer, then press Try again.'),
        el('p', { class: 'signed-out-note', text: 'The agent list and the other views are paused until you are signed in.' }),
        el('div', { class: 'signed-out-actions' },
            el('button', { type: 'button', id: 'signed-out-retry', class: 'btn btn-primary', text: 'Try again' }),
            el('a', { id: 'signed-out-help', class: 'btn', href: authUrlFor(location), text: 'How to sign in' })),
        el('p', { id: 'signed-out-status', class: 'signed-out-status', role: 'status' }));
    // Not dismissable: there is nothing to do on the page until the sign-in is back.
    dlg.addEventListener('cancel', e => e.preventDefault());
    dlg.querySelector('#signed-out-retry').addEventListener('click', () => retrySignIn());
    document.body.appendChild(dlg);
    _dialog = dlg;
    try { dlg.showModal(); } catch { dlg.setAttribute('open', ''); }
    dlg.querySelector('#signed-out-retry').focus();
}

/** The page is signed out: show the panel once and pause the pollers and the live feed. */
export function enterSignedOut(win = window) {
    if (isSignedOut() || win.location.pathname.startsWith('/auth')) return;
    setSignedOut(true);
    document.dispatchEvent(new CustomEvent('coral:signed-out')); // the terminal socket closes itself
    try { if (state.coralWs) state.coralWs.close(); } catch { /* already closed */ }
    show();
}

// Coming back to this tab after running `coral open` or signing in elsewhere: check quietly, and reload if it worked.
if (typeof document !== 'undefined') {
    document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible') retrySignIn({ quiet: true }); });
    window.addEventListener('focus', () => { retrySignIn({ quiet: true }); });
}
