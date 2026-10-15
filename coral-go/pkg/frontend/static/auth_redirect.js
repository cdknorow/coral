/* A dashboard whose session has ended (#3418). When an /api call answers 401 (strict mode:
 * dashboard_auth_required; or the API-key sign-in on a remote browser), the page goes to
 * /auth?return_to=<this page> once. /auth shows how to get back in (the locked page, or the
 * key form). If it was sent there in the last 30 s and is still being refused, it stays put
 * and says so instead of bouncing again: no redirect loop. The Coral Hub has its own sign-in
 * (cloud.js) and is not handled here. */

import { importOnce } from './import_once.js';

export const AUTH_BOUNCE_MS = 30000;
const KEY = 'coral-auth-redirect-at';

/** The /auth URL for the current page: return_to is this page's own path (same origin). */
export function authUrlFor(loc) {
    const back = (loc.pathname || '/') + (loc.search || '') + (loc.hash || '');
    return '/auth?return_to=' + encodeURIComponent(back.startsWith('/') && !back.startsWith('//') ? back : '/');
}

/** Whether to send the page to /auth for a 401 now (false while one was sent recently). */
export function shouldRedirect(now, lastAt) {
    return !(Number.isFinite(lastAt) && now - lastAt >= 0 && now - lastAt < AUTH_BOUNCE_MS);
}

/** Whether a 401's body means the dashboard is signed out (#3528): strict mode's
 *  dashboard_auth_required, or the auth middleware's plain "Unauthorized" (the API-key sign-in on
 *  a remote browser). Any other JSON 401 (an agent-only route's session_token_required, an
 *  identity refusal) is the caller's to show: going to /auth would not help a signed-in user. */
export function isSignedOut401(bodyText) {
    let body;
    try { body = JSON.parse(bodyText); } catch { return true; }
    if (!body || typeof body !== 'object') return true;
    return body.code === 'dashboard_auth_required' || body.error === 'dashboard_auth_required';
}

let _said = false;

/** Handle a 401 from an /api call: redirect once, or (recently redirected) tell the user. */
export function handleUnauthorized(win = window) {
    if (win.location.pathname.startsWith('/auth')) return;
    let last = NaN;
    try { last = Number(win.sessionStorage.getItem(KEY)); } catch { /* storage off: still redirect */ }
    const now = Date.now();
    if (!shouldRedirect(now, last)) {
        if (!_said) {
            _said = true;
            importOnce('./utils.js').then(u => u.showToast("Coral is locked: open it from the Coral app or tray, or run 'coral open' on this computer.", true, 10000)).catch(() => {});
        }
        return;
    }
    try { win.sessionStorage.setItem(KEY, String(now)); } catch { /* keep going */ }
    win.location.href = authUrlFor(win.location);
}
