/* Sleeping agent view. A sleeping agent has no terminal, but its transcript is
   on disk: the center pane shows the chat history as usual (scrollable, older
   pages autoload), and the composer is disabled and offers Wake and Restart…
   in place of the send controls. Same in the dashboard, the popout and on
   mobile, for local and hub-remote agents. */

import { state } from './state.js';

const PLACEHOLDER = 'Agent is sleeping — wake it to send commands';
let waking = false;

/**
 * Show or hide the sleeping composer for the current session.
 * manageInput: false leaves the text input to the caller (the popout drives
 * its input from its own lifecycle state).
 */
export function applySleepingView(sleeping, { manageInput = true } = {}) {
    sleeping = !!sleeping;
    if (state.currentSession) state.currentSession.sleeping = sleeping;
    const pane = document.getElementById('command-pane');
    if (pane) pane.classList.toggle('agent-sleeping', sleeping);
    const bar = document.getElementById('sleeping-composer');
    if (bar) bar.hidden = !sleeping;
    if (!manageInput) return;
    const input = document.getElementById('command-input');
    if (!input) return;
    if (sleeping) {
        // An input another state already disabled stays that state's to undo.
        if (!input.dataset.sleepDisabled && input.disabled) return;
        input.dataset.sleepDisabled = '1';
        // The awake placeholder for this agent, kept for waking up.
        if (input.placeholder !== PLACEHOLDER) input.dataset.awakePlaceholder = input.placeholder;
        input.disabled = true;
        input.placeholder = PLACEHOLDER;
    } else if (input.dataset.sleepDisabled) {
        // Only undo what this view did: other states may disable the input too.
        input.disabled = false;
        input.placeholder = input.dataset.awakePlaceholder || input.placeholder;
        delete input.dataset.sleepDisabled;
        delete input.dataset.awakePlaceholder;
    }
}

/** The placeholder a sleeping composer shows (the popout sets it itself). */
export const SLEEPING_PLACEHOLDER = PLACEHOLDER;

async function wakeCurrent(btn) {
    const s = state.currentSession;
    if (waking || !s || s.type !== 'live') return;
    waking = true;
    const label = btn.textContent;
    btn.disabled = true;
    btn.textContent = 'Waking…';
    try {
        const { isPopout, popoutWake } = await import('./popout.js');
        if (isPopout()) {
            await popoutWake();
            return;
        }
        const ok = await window.toggleAgentSleep(s.name, s.agent_type, s.session_id, 'wake');
        // Re-select so the terminal attaches to the relaunched agent, once: if
        // the feed saw the wake first it has already re-selected (a new
        // currentSession object).
        if (ok && state.currentSession === s && s.sleeping) {
            await window.selectLiveSession(s.name, s.agent_type, s.session_id, s.server);
        }
    } finally {
        waking = false;
        btn.disabled = false;
        btn.textContent = label;
    }
}

document.addEventListener('click', (e) => {
    const btn = e.target.closest && e.target.closest('#sleeping-composer button[data-action]');
    if (!btn) return;
    if (btn.dataset.action === 'wake') wakeCurrent(btn);
    else if (btn.dataset.action === 'restart') window.restartSession();
});
