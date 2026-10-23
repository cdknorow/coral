/* One calm line while Claude is not signed in (#5478, for #5469): the server's GET /api/system/status carries claude_signed_in once a background check was
   made; false means background jobs are waiting for a sign-in. It is a line, not an alert or a toast, and it goes by itself when the field turns true.
   Hover (title) or tap (the button) tells what to do. */
import { fetchSystemStatus } from './system_status.js';
import { pollEvery } from './overload.js';
import { registerActions } from './actions.js';
import { isCloud, normServer, sessionServer, LOCAL_SERVER } from './server_base.js';
import { setPill } from './dom_write.js';
import { state } from './state.js';
import { browserLocal } from './safe_storage.js';

const RECHECK_MS = 60_000;
const DISMISS_KEY = 'coral-signin-line-dismissed'; // when the note was dismissed (ms); it stays away for a day, and a sign-in clears it
const DISMISS_MS = 24 * 60 * 60 * 1000;
const dismissed = () => { const at = Number(browserLocal.get(DISMISS_KEY)); return Number.isFinite(at) && at > 0 && Date.now() - at < DISMISS_MS; };

let _known; // the last status's claude_signed_in: true, false, or undefined (not known: no background check or first launch yet)
let _askedAt = 0;

/** What the last status said about Claude's sign-in: true, false, or undefined when it did not say (#5852). */
export function claudeSignedIn() { return _known; }

/** Whether the sign-in status is about this agent's Claude: a claude agent on THIS computer, not one on another server or seen through Coral Hub (claude_signed_in is this computer's Claude). */
export function isLocalClaude(row) {
    return !!row && row.agent_type === 'claude' && !isCloud() && normServer(sessionServer(row)) === LOCAL_SERVER;
}

/** Whether this agent runs on THIS computer (not another server, not seen through Coral Hub): the only place this computer's CLI sign-in says anything about it (#6048). */
export function isLocalAgent(row) {
    return !!row && !isCloud() && normServer(sessionServer(row)) === LOCAL_SERVER;
}

/** A fresh answer for Claude (Check again asked the CLI itself): the card and the line follow it at once instead of at the next status read (#6048). */
export function setClaudeSignedIn(v) {
    _known = typeof v === 'boolean' ? v : undefined;
    applySigninStatus({ claude_signed_in: _known });
}

/** A new Claude agent that has not started, on a computer where Claude is not signed in: the card says so (#5852), so the generic "hasn't done anything yet" pill is not shown beside it (one message, not two). */
export function startupSignedOut(row) { return _known === false && isLocalClaude(row); }

/** A card that is waiting to say whether Claude is signed in asks for a fresh status, at most every 10 seconds (the server checks once after the first Claude launch, #5838). */
export function askSigninSoon() {
    const now = Date.now();
    if (now - _askedAt < 10_000) return;
    _askedAt = now;
    refresh();
}

/** Shows the line for a status with claude_signed_in === false, hides it otherwise (absent is "not known": nothing to say). */
export function applySigninStatus(data) {
    _known = data && typeof data.claude_signed_in === 'boolean' ? data.claude_signed_in : undefined;
    if (_known === false && startupSignedOut(state.currentSession)) { const pill = document.getElementById('waiting-banner'); if (pill && pill.classList.contains('waiting-banner-not-started')) setPill(pill, false, ''); } // the card now says it (only for the agent that is open and whose Claude it is)
    const line = document.getElementById('claude-signin-line');
    if (!line) return;
    const off = !!data && data.claude_signed_in === false;
    if (data && data.claude_signed_in === true) browserLocal.remove(DISMISS_KEY); // signed in: a later sign-out says so again
    line.hidden = !off || dismissed();
    if (line.hidden) { // a tap's hint does not stay open for the next time
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
    'm-signin-dismiss': () => {
        browserLocal.set(DISMISS_KEY, String(Date.now()));
        const line = document.getElementById('claude-signin-line');
        if (line) line.hidden = true;
        // focus goes on to the page: the line it was on is gone
        [document.querySelector('.top-nav-tab.active'), document.getElementById('nav-select')].find(e => e && e.getClientRects().length)?.focus();
    },
    'm-signin-hint': () => {
        const hint = document.getElementById('signin-line-hint'), btn = document.getElementById('signin-line-btn');
        if (!hint || !btn) return;
        hint.hidden = !hint.hidden;
        btn.setAttribute('aria-expanded', String(!hint.hidden));
    },
});
