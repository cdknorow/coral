// Which agents are restarting, and what the one overlay over the agent's pane shows (#4481). The overlay is a single
// node shared by every agent and by both views, so "restarting" is kept per session id: it follows the agent that is
// restarting, not whichever one is on screen when the restart ends.
import { state } from './state.js';

const restarting = new Set();

export function markRestarting(sessionId, on) {
    if (!sessionId) return;
    if (on) restarting.add(sessionId);
    else restarting.delete(sessionId);
}

// A restart that returns a new session id moves its mark to it.
export function moveRestarting(fromId, toId) {
    if (!fromId || !toId || fromId === toId || !restarting.delete(fromId)) return;
    restarting.add(toId);
}

export function isRestarting(sessionId) {
    return !!sessionId && restarting.has(sessionId);
}

export function currentIsRestarting() {
    return isRestarting(state.currentSession && state.currentSession.session_id);
}

function show(id, on) {
    const el = document.getElementById(id);
    if (el) el.style.display = on ? '' : 'none';
}

export function showRestartingOverlay() {
    const overlay = document.getElementById('session-ended-overlay');
    if (!overlay) return;
    overlay.style.display = '';
    show('session-ended-default', false);
    show('session-restarting', true);
    show('session-lost-connection', false);
}

export function hideSessionOverlay() {
    const overlay = document.getElementById('session-ended-overlay');
    if (overlay) overlay.style.display = 'none';
}

// The overlay for the agent now on screen: "restarting" if it is, otherwise gone (hide: false leaves a shown "Session ended" alone).
// An ended agent's overlay comes back from its terminal socket's closing frame, which only the Terminal view has.
export function syncSessionOverlay({ hide = true } = {}) {
    if (currentIsRestarting()) showRestartingOverlay();
    else if (hide) hideSessionOverlay();
}
