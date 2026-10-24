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

// What is known about Claude's sign-in: true, false, or undefined (nothing known: no background check or first launch yet, and the CLI
// not asked), and whether the CLI said it (_fromCli) or the server's status did. A status that does not say (no claude_signed_in)
// clears what an earlier status said, but leaves the CLI's own answer alone: on a fresh install the status says nothing until the
// server's first check, and replacing the CLI's "not signed in" with "not known" at each status read sent the card back to the
// generic words until the next ask, 30 s later (#6070's race, the Orchestrator on #307). A CLI ask that could not tell changes nothing.
let _known;
let _fromCli = false;
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

/** A fresh answer for Claude (the CLI itself was asked): the card and the line follow it at once instead of at the next status read (#6048).
 *  An ask that could not tell (not a boolean) keeps what was known before. */
export function setClaudeSignedIn(v) {
    if (typeof v === 'boolean') { _known = v; _fromCli = true; }
    showSigninState();
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

/** Whether this computer has a Claude agent in the list: the line is about Claude's background work, so a home with no Claude agent (a Codex-only first run) has nothing waiting on it (#6407). */
const hasLocalClaude = () => (state.liveSessions || []).some(isLocalClaude);

/** Puts the line right for the agents the page has now (the list changed); the last status answer is kept. */
export function syncSigninLine() { showSigninState(); }

/** Takes a status's claude_signed_in: what it says, or, when it does not say, "not known" unless the CLI's own answer is what is known.
 *  Shows the line while the answer is false, hides it otherwise. */
export function applySigninStatus(data) {
    if (data && typeof data.claude_signed_in === 'boolean') { _known = data.claude_signed_in; _fromCli = false; }
    else if (!_fromCli) _known = undefined;
    showSigninState();
}

function showSigninState() {
    if (_known === false && startupSignedOut(state.currentSession)) { const pill = document.getElementById('waiting-banner'); if (pill && pill.classList.contains('waiting-banner-not-started')) setPill(pill, false, ''); } // the card now says it (only for the agent that is open and whose Claude it is)
    const line = document.getElementById('claude-signin-line');
    if (!line) return;
    const off = _known === false;
    if (_known === true) browserLocal.remove(DISMISS_KEY); // signed in: a later sign-out says so again
    // Written only when it differs: the line follows the agent list, which a feed message re-reads (syncSigninLine), and a write of the value already there is still a write to the page (#5010's budget).
    const hide = !off || dismissed() || !hasLocalClaude();
    if (line.hidden !== hide) line.hidden = hide;
    // What a screen reader says, once, when the line appears: a region that is always in the page gets the words (a status that appears with its words is often not read). The line itself is not live.
    const live = document.getElementById('claude-signin-live'), btn0 = document.getElementById('signin-line-btn');
    const said = hide || !btn0 ? '' : (btn0.lastElementChild.textContent || '').replace(/\s+/g, ' ').trim(); // the button's label (its last element, after the icon): the whole sentence, though a phone shows only its first part (#6737)
    if (live && live.textContent !== said) live.textContent = said;
    if (hide) { // a tap's hint does not stay open for the next time
        const hint = document.getElementById('signin-line-hint'), btn = document.getElementById('signin-line-btn');
        if (hint && !hint.hidden) hint.hidden = true;
        if (btn && btn.getAttribute('aria-expanded') !== 'false') btn.setAttribute('aria-expanded', 'false');
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
