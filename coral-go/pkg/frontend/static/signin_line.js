/* One calm line while Claude is not signed in (#5478, for #5469): the server's GET /api/system/status carries claude_signed_in once a background check was
   made; false means background jobs are waiting for a sign-in. It is a line, not an alert or a toast, and it goes by itself when the field turns true.
   Hover (title) or tap (the button) tells what to do. */
import { fetchSystemStatus } from './system_status.js';
import { pollEvery } from './overload.js';
import { registerActions } from './actions.js';

const RECHECK_MS = 60_000;

/** Shows the line for a status with claude_signed_in === false, hides it otherwise (absent is "not known": nothing to say). */
export function applySigninStatus(data) {
    const line = document.getElementById('claude-signin-line');
    if (!line) return;
    const off = !!data && data.claude_signed_in === false;
    line.hidden = !off;
    if (!off) { // a tap's hint does not stay open for the next time
        const hint = document.getElementById('signin-line-hint'), btn = document.getElementById('signin-line-btn');
        if (hint) hint.hidden = true;
        if (btn) btn.setAttribute('aria-expanded', 'false');
    }
}

async function refresh() {
    try {
        const resp = await fetchSystemStatus({ fresh: true });
        if (resp.ok) applySigninStatus(await resp.json());
    } catch { /* the next look asks again */ }
}

/** Starts with the startup answer and looks again every minute. */
export function startSigninLine(first) {
    applySigninStatus(first);
    pollEvery(refresh, RECHECK_MS);
}

registerActions({
    'm-signin-hint': () => {
        const hint = document.getElementById('signin-line-hint'), btn = document.getElementById('signin-line-btn');
        if (!hint || !btn) return;
        hint.hidden = !hint.hidden;
        btn.setAttribute('aria-expanded', String(!hint.hidden));
    },
});
