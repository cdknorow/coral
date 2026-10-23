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
import { escapeAttr, showToast } from './utils.js';
import { scheduleLiveSessionsRender } from './render.js';
import { TERMINAL_GONE_MESSAGE } from './server_status.js';
import { isPopout } from './popout.js';
import { importOnce } from './import_once.js';
import { noteHeld, noteDone, wasCancelled } from './held_sends.js'; // the queued lines under the composer (#6611)

/** Agents whose terminal answered terminal_gone, by server and session id. A restart
 *  gives the agent a new session id, so its new row is never marked. */
const _gone = new Set();
const keyOf = s => `${sessionServer(s)}|${s.session_id}`;

 function isTerminalGone(s) {
    return !!s && !!s.session_id && (_gone.has(keyOf(s)) || isGoneAsleep(s));
}

/** Coral put this agent to sleep because its terminal session no longer exists (#3344): the
 *  server says so (sleep_reason). An archived agent is just archived. */
export function isGoneAsleep(s) {
    return !!s && !!s.sleeping && !s.archived && s.sleep_reason === 'terminal_gone';
}

/** Record that this agent's terminal is gone and show it on its row and header. */
 function markTerminalGone(s) {
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

/** Coral put this agent to sleep because its CLI exited and left a plain shell (#6058): the row says "Ended" and a click restarts it. */
export function isExitedAsleep(s) {
    return !!s && !!s.sleeping && !s.archived && s.sleep_reason === 'agent_exited';
}

/** The "Terminal gone" chip for an agent's sidebar row, "Ended" for one whose CLI exited ('' otherwise); a click restarts it. */
export function terminalGoneChip(s) {
    const ended = isExitedAsleep(s);
    if (!ended && !isTerminalGone(s)) return '';
    return `<button type="button" class="terminal-gone-chip" title="${ended ? "This agent's CLI exited. Click to restart it." : "This agent's terminal is gone. Click to restart it."}"`
        + `${ended ? ' aria-label="Ended. Restart"' : ''} data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type || '')}" data-session-id="${escapeAttr(s.session_id)}">${ended ? 'Ended' : 'Terminal gone'}</button>`;
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
let _hooksReview = false; // the error under the composer is Codex's hooks question: answering it clears the error and says to send again (#6200)

/** Say a send error under the composer. restart: offer Restart (the terminal is gone). */
export function showComposerError(message, { restart = false } = {}) {
    const box = document.getElementById('composer-error');
    if (!box) return;
    box.querySelector('.composer-error-text').textContent = message;
    _hooksReview = false; // reportSendError sets it again for the hooks question
    box.querySelector('.composer-error-restart').hidden = !restart || isPopout(); // no destructive actions in the popout
    box.hidden = false;
    _composerGone = restart;
}

export function clearComposerError() {
    const box = document.getElementById('composer-error');
    if (box) box.hidden = true;
    _composerGone = false;
    _hooksReview = false;
}

/** The agent's prompt was answered: if the message was held back by Codex's hooks question, the error under the composer goes and a line says the message is still there to send. */
export function noteHooksAnswered() {
    if (!_hooksReview) return;
    clearComposerError();
    showToast('Answered. Your message is still in the box: send it again.');
}

/** A failed send for `session`: inline under the composer; a gone terminal also marks the agent. */
export function reportSendError(session, message, code, exited = false) {
    // The message did not go because the agent's CLI has exited and the server has just marked it Ended (#6058): say who stopped, keep the draft, offer Restart, and read the row again now.
    if (exited) {
        const who = (session && (session.display_name || session.name)) || 'The agent';
        showComposerError(`${who} stopped before answering. Your message is kept below; Restart the agent to send it.`, { restart: true });
        importOnce('./api.js').then(m => m.loadLiveSessions()).catch(() => {}); // loaded when it is needed: a static import pulls api.js's tabs into the startup sequence
        return;
    }
    if (code === 'terminal_gone') {
        markTerminalGone(session);
        showComposerError(TERMINAL_GONE_TEXT, { restart: true });
        return;
    }
    showComposerError(message);
    _hooksReview = code === 'codex_hooks_review_pending'; // set after the error is shown (showing it keeps the flag as it was)
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

// A composer send over the terminal socket (#3218, #3276). The server acknowledges each frame
// carrying an id: the send is accepted once all its frames are acknowledged, and refused when one
// of them is (terminal_gone, terminal_input_failed, the hub's hub_read_only).
// A send that gets no answer in this long is not taken as delivered.
 const SOCKET_ACK_TIMEOUT_MS = 20000;
 const NO_ANSWER_TEXT = "The agent's terminal did not confirm the message. It may not have been sent: check the terminal before sending it again.";
const _socketSends = new Set();
let _nextInputId = 1;

/** A fresh id for a terminal_input frame. */
export function nextInputId() { return _nextInputId++; }

/** Resolves true when a socket send for `session` is accepted (the server answered every frame
 *  in ids), false when it is refused or not answered in SOCKET_ACK_TIMEOUT_MS. */
export function awaitSocketAccept(session, ids) {
    return new Promise(resolve => {
        const p = { sid: session && session.session_id, resolve, ids: new Set(ids) };
        p.timer = setTimeout(() => { if (_settle(p, false)) showComposerError(NO_ANSWER_TEXT); }, SOCKET_ACK_TIMEOUT_MS);
        _socketSends.add(p);
    });
}

function _settle(p, ok) {
    if (!_socketSends.delete(p)) return false;
    clearTimeout(p.timer);
    p.resolve(ok);
    return true;
}

/** Refuse the sends for sessionId (all of them, or only the one that sent frame `id`). */
export function refuseSocketSends(sessionId, id) {
    for (const p of [..._socketSends]) {
        if (sessionId && p.sid !== sessionId) continue;
        if (id !== undefined && id !== null && !p.ids.has(id)) continue;
        _settle(p, false);
    }
}

// A message held behind the agent's question (#6610): the server types it once no box is left, and answers by this id then. It waits as long as the server holds it (30 minutes), not the usual seconds.
const HELD_ACK_TIMEOUT_MS = 31 * 60 * 1000;
document.addEventListener('coral:terminal-input-held', e => {
    const { sessionId, id, note, heldId } = e.detail || {};
    noteHeld(sessionId, id, undefined, heldId); // the line under the composer, with Cancel (#6611)
    for (const p of [..._socketSends]) {
        if ((sessionId && p.sid !== sessionId) || !p.ids.has(id)) continue;
        clearTimeout(p.timer);
        p.timer = setTimeout(() => { if (_settle(p, false)) showComposerError(NO_ANSWER_TEXT); }, HELD_ACK_TIMEOUT_MS);
    }
    if (note) showToast(note);
});

// The server delivered frame `id`.
document.addEventListener('coral:terminal-input-ack', e => {
    const { sessionId, id } = e.detail || {};
    noteDone(sessionId, id, 'sent');
    for (const p of [..._socketSends]) {
        if ((sessionId && p.sid !== sessionId) || !p.ids.delete(id)) continue;
        if (p.ids.size === 0) _settle(p, true);
    }
});

// Input refused by the server (it could not deliver it) or by the Coral Hub (read-only).
document.addEventListener('coral:terminal-input-refused', e => {
    const { sessionId, id, error } = e.detail || {};
    const before = _socketSends.size;
    const own = wasCancelled(sessionId, id); // the person cancelled it: its refusal is theirs, not an error
    noteDone(sessionId, id);
    refuseSocketSends(sessionId, id);
    const cur = state.currentSession;
    if (error && !own && _socketSends.size < before && cur && (!sessionId || sessionId === cur.session_id)) showComposerError(error);
});

// The terminal socket said the agent's terminal is gone: shown as for the HTTP 409.
document.addEventListener('coral:terminal-gone', e => {
    const sid = e.detail && e.detail.sessionId;
    noteDone(sid, e.detail && e.detail.id);
    refuseSocketSends(sid, e.detail && e.detail.id);
    const cur = state.currentSession;
    if (!cur || cur.type !== 'live' || (sid && sid !== cur.session_id)) return;
    reportSendError(cur, TERMINAL_GONE_TEXT, 'terminal_gone');
});

// The stream ended: a send still in its window may not have reached the agent.
document.addEventListener('coral:terminal-closed', e => refuseSocketSends(e.detail && e.detail.sessionId));
