/* A red banner while local sign-in is off (#4291, Security's #4288): with CORAL_STRICT_LOCAL_AUTH=0 any program on this computer can use
 * Coral without a credential, and the person at the dashboard should know. GET /api/system/auth-status says so with strict_local_auth
 * (false); a missing field or an older server shows nothing, and the Coral Hub's view never does (the Hub is not this computer's door).
 * The banner has no close button: it goes when sign-in is turned back on. It is a role=alert that is only ever shown or hidden, never
 * rewritten, so a screen reader hears it once when it appears and not again on the polls that find nothing changed. */
import { isCloud } from './server_base.js';
import { pollEvery } from './overload.js';

const CHECK_MS = 60000;
let started = false;

async function localSigninOff() {
    try {
        const r = await fetch('/api/system/auth-status', { credentials: 'same-origin', cache: 'no-store' });
        const st = r.ok ? await r.json() : null;
        return !!st && st.strict_local_auth === false;
    } catch { return false; }
}

export async function checkLocalAuthBanner() {
    const banner = document.getElementById('local-auth-banner');
    if (!banner || isCloud()) return;
    const off = await localSigninOff();
    if (banner.hidden === off) banner.hidden = !off; // only a change touches it
}

export function startLocalAuthBanner() {
    if (started || isCloud()) return;
    started = true;
    checkLocalAuthBanner();
    pollEvery(checkLocalAuthBanner, CHECK_MS);
}
