/* Send errors under the composer, and agents whose terminal is gone (#3218).

   A failed send (the composer, keys, a prompt answer) is said inline, under the
   composer where the user typed: not as a toast that covers the composer on a
   phone or appears far away on a desktop. When the server answers 409
   terminal_gone, the agent's own terminal no longer exists: the line says so with
   a Restart button (Restart starts it in a fresh terminal in its folder, #3213),
   and the agent's row and header show "Terminal gone" at once, without waiting
   for the server to notice and mark it asleep. */

import { state } from './state.js';
import { sessionServer } from './server_base.js';
import { escapeAttr } from './utils.js';
import { scheduleLiveSessionsRender } from './render.js';
import { TERMINAL_GONE_MESSAGE } from './server_status.js';
import { isPopout } from './popout.js';

/** Agents whose terminal answered terminal_gone, by server and session id. A restart
 *  gives the agent a new session id, so its new row is never marked. */
const _gone = new Set();
const keyOf = s => `${sessionServer(s)}|${s.session_id}`;

export function isTerminalGone(s) {
    return !!s && !!s.session_id && _gone.has(keyOf(s));
}

/** Record that this agent's terminal is gone and show it on its row and header. */
export function markTerminalGone(s) {
    if (!s || !s.session_id || isTerminalGone(s)) return;
    _gone.add(keyOf(s));
    _dismissed.delete(keyOf(s)); // a new failure is said again
    scheduleLiveSessionsRender();
    syncTerminalGoneHeader();
}

/** Forget agents the feed no longer lists awake (asleep, ended or restarted): their own
 *  state takes over. */
export function pruneTerminalGone(sessions) {
    if (!_gone.size) return;
    const awake = new Set((sessions || []).filter(s => !s.sleeping).map(keyOf));
    let changed = false;
    for (const k of [..._gone]) if (!awake.has(k)) { _gone.delete(k); changed = true; }
    if (changed) syncTerminalGoneHeader();
}

/** The "Terminal gone" chip for an agent's sidebar row ('' otherwise); a click restarts it. */
export function terminalGoneChip(s) {
    if (!isTerminalGone(s)) return '';
    return `<button type="button" class="terminal-gone-chip" title="This agent's terminal is gone. Click to restart it."`
        + ` data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type || '')}" data-session-id="${escapeAttr(s.session_id)}">Terminal gone</button>`;
}

/** The header badge for the selected agent while its terminal is gone. */
export function syncTerminalGoneHeader() {
    const host = document.querySelector('.terminal-header-actions');
    if (!host) return;
    let badge = document.getElementById('terminal-gone-header');
    const cur = state.currentSession;
    if (!cur || cur.type !== 'live' || !isTerminalGone(cur)) {
        badge?.remove();
        if (_composerGone) clearComposerError();
        return;
    }
    if (!badge) {
        badge = document.createElement('button');
        badge.type = 'button';
        badge.id = 'terminal-gone-header';
        badge.className = 'terminal-gone-chip terminal-gone-header';
        // The popout has no destructive actions: there it only says so (restart from Coral).
        badge.textContent = isPopout() ? 'Terminal gone' : 'Terminal gone · Restart';
        badge.title = isPopout() ? "This agent's terminal is gone. Restart it from Coral." : "This agent's terminal is gone. Restart it to continue.";
        if (isPopout()) badge.disabled = true;
        else badge.addEventListener('click', () => window.restartSession());
        host.prepend(badge);
    }
    // The composer says it too, unless the user dismissed it for this agent.
    if (!_dismissed.has(keyOf(cur))) showComposerError(TERMINAL_GONE_TEXT, { restart: true });
}

/** Agents whose terminal-gone line the user closed: the feed does not bring it back. */
const _dismissed = new Set();

export const TERMINAL_GONE_TEXT = TERMINAL_GONE_MESSAGE;
let _composerGone = false;

/** Say a send error under the composer. restart: offer Restart (the terminal is gone). */
export function showComposerError(message, { restart = false } = {}) {
    const box = document.getElementById('composer-error');
    if (!box) return;
    box.querySelector('.composer-error-text').textContent = message;
    box.querySelector('.composer-error-restart').hidden = !restart || isPopout(); // no destructive actions in the popout
    box.hidden = false;
    _composerGone = restart;
}

export function clearComposerError() {
    const box = document.getElementById('composer-error');
    if (box) box.hidden = true;
    _composerGone = false;
}

/** A failed send for `session`: inline under the composer; a gone terminal also marks the agent. */
export function reportSendError(session, message, code) {
    if (code === 'terminal_gone') {
        markTerminalGone(session);
        showComposerError(TERMINAL_GONE_TEXT, { restart: true });
        return;
    }
    showComposerError(message);
}

document.addEventListener('click', e => {
    const t = e.target;
    if (!t || !t.closest) return;
    if (t.closest('#composer-error .composer-error-restart')) window.restartSession();
    else if (t.closest('#composer-error .composer-error-dismiss')) {
        const cur = state.currentSession;
        if (_composerGone && cur) _dismissed.add(keyOf(cur));
        clearComposerError();
    }
});

// The row chip restarts its agent. Capture phase: it runs before the row's own click (select).
document.addEventListener('click', e => {
    const chip = e.target && e.target.closest && e.target.closest('.session-group-item .terminal-gone-chip');
    if (!chip) return;
    e.stopPropagation();
    e.preventDefault();
    window.restartDirect(chip.dataset.name, chip.dataset.agentType, chip.dataset.sessionId);
}, true);

// The terminal socket sends no reply to input. A composer send over it counts as accepted
// once the socket has said nothing against it for SOCKET_ACCEPT_MS (its Enter goes at
// +300 ms). terminal_gone or terminal_closed for that agent in the window refuses it, and
// the composer keeps the draft (#3218).
export const SOCKET_ACCEPT_MS = 1200;
const _socketSends = new Set();

/** Resolves true when a socket send for `session` is accepted, false when it is refused. */
export function awaitSocketAccept(session, ms = SOCKET_ACCEPT_MS) {
    return new Promise(resolve => {
        const p = { sid: session && session.session_id, resolve };
        p.timer = setTimeout(() => { _socketSends.delete(p); resolve(true); }, ms);
        _socketSends.add(p);
    });
}

function refuseSocketSends(sessionId) {
    for (const p of [..._socketSends]) {
        if (sessionId && p.sid !== sessionId) continue;
        clearTimeout(p.timer);
        _socketSends.delete(p);
        p.resolve(false);
    }
}

// The terminal socket said the agent's terminal is gone: shown as for the HTTP 409.
document.addEventListener('coral:terminal-gone', e => {
    const sid = e.detail && e.detail.sessionId;
    refuseSocketSends(sid);
    const cur = state.currentSession;
    if (!cur || cur.type !== 'live' || (sid && sid !== cur.session_id)) return;
    reportSendError(cur, TERMINAL_GONE_TEXT, 'terminal_gone');
});

// The stream ended: a send still in its window may not have reached the agent.
document.addEventListener('coral:terminal-closed', e => refuseSocketSends(e.detail && e.detail.sessionId));
