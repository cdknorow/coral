/* Sleeping agent view. A sleeping agent has no terminal, but its transcript is
   on disk: the center pane shows the chat history as usual (scrollable, older
   pages autoload), and the composer is disabled and offers Wake and Restart…
   in place of the send controls. Same in the dashboard, the popout and on
   mobile, for local and hub-remote agents. */

import { importOnce } from './import_once.js';
import { state } from './state.js';

const lastState = new Map(); // session_id -> the words its state last had, to say a change once (#6213)
let stateFlip = false;

const PLACEHOLDER = 'Agent is sleeping — wake it to send commands';
const ARCHIVED_PLACEHOLDER = 'Agent is archived — unarchive or wake it to send commands';
let waking = false;

/**
 * Show or hide the sleeping composer for the current session.
 * manageInput: false leaves the text input to the caller (the popout drives
 * its input from its own lifecycle state).
 * archived: an archived agent (#3030) offers Unarchive and Wake, no Restart.
 * Omitted, it is read from the current session's live row.
 */
export function applySleepingView(sleeping, { manageInput = true, archived } = {}) {
    sleeping = !!sleeping;
    if (archived === undefined) {
        const cur = state.currentSession;
        const row = cur && (state.liveSessions || []).find(s => s.session_id === cur.session_id);
        archived = !!(row && row.archived);
    }
    archived = sleeping && !!archived;
    if (state.currentSession) {
        state.currentSession.sleeping = sleeping;
        state.currentSession.archived = archived;
    }
    const pane = document.getElementById('command-pane');
    if (pane) {
        pane.classList.toggle('agent-sleeping', sleeping);
        pane.classList.toggle('agent-archived', archived);
    }
    // A sleeping agent's terminal takes no typing: no "Click to type" hint over it (#3735).
    document.getElementById('xterm-container')?.classList.toggle('agent-sleeping', sleeping);
    const bar = document.getElementById('sleeping-composer');
    if (bar) bar.hidden = !sleeping;
    // Asleep because its terminal session is gone (#3344): say that, not "sleeping".
    const cur = state.currentSession;
    const row = cur && (state.liveSessions || []).find(s => s.session_id === cur.session_id);
    const gone = sleeping && !archived && !!row && row.sleep_reason === 'terminal_gone';
    const text = document.getElementById('sleeping-composer-text');
    // Its CLI exited (#6058): said once, with the agent's name, and "before answering" when a message of the person's is still waiting.
    const ended = sleeping && !archived && !gone && !!row && row.sleep_reason === 'agent_exited';
    const who = (cur && (cur.display_name || cur.name)) || 'This agent';
    const endedText = !!(cur && state.lastSendAt && state.lastSendAt.has(cur.session_id)) ? `${who} stopped before answering` : `${who} has ended`;
    if (text) {
        const next = archived ? 'This agent is archived' : gone ? "This agent's terminal is gone" : ended ? endedText : 'This agent is sleeping';
        if (text.textContent !== next) text.textContent = next; // a status line: written only when it changes, so it is announced once
    }
    // A change of the open agent's state is said once, in a status that stays in the page: asleep (or ended, archived), then awake. Opening an agent that already sleeps says nothing: that is not a change.
    if (cur && cur.session_id) {
        const name = who === 'This agent' ? 'The agent' : who;
        const words = sleeping ? (text ? text.textContent : 'This agent is asleep').replace(/^This agent/, name) : `${name} is awake`;
        const before = lastState.get(cur.session_id);
        lastState.set(cur.session_id, words);
        const live = document.getElementById('agent-state-status');
        if (live && before !== undefined && before !== words) { stateFlip = !stateFlip; live.textContent = words + (stateFlip ? '' : '\u00a0'); }
    }
    const icon = document.getElementById('sleeping-composer-icon');
    if (icon) icon.textContent = archived ? '🗄️' : gone || ended ? '⚠️' : '🌙';
    if (bar) bar.classList.toggle('is-terminal-gone', gone || ended);
    // Its terminal session is gone, so there is nothing to wake (Wake answers ok and the agent
    // falls asleep again): Restart is the way back (#3344).
    const wakeBtn = document.getElementById('sleeping-wake-btn');
    if (wakeBtn) wakeBtn.hidden = gone;
    const unarchive = document.getElementById('sleeping-unarchive-btn');
    if (unarchive) unarchive.hidden = !archived;
    const wake = document.getElementById('sleeping-wake-btn');
    if (wake) {
        // Archived: Unarchive is the primary action, Wake the second.
        wake.classList.toggle('btn-primary', !archived);
        wake.classList.toggle('btn-secondary', archived);
    }
    const restart = document.getElementById('sleeping-restart-btn');
    if (restart) restart.hidden = archived;
    if (!manageInput) return;
    const input = document.getElementById('command-input');
    if (!input) return;
    if (sleeping) {
        // An input another state already disabled stays that state's to undo.
        if (!input.dataset.sleepDisabled && input.disabled) return;
        input.dataset.sleepDisabled = '1';
        // The awake placeholder for this agent, kept for waking up.
        if (input.placeholder !== PLACEHOLDER && input.placeholder !== ARCHIVED_PLACEHOLDER) input.dataset.awakePlaceholder = input.placeholder;
        input.disabled = true;
        input.placeholder = archived ? ARCHIVED_PLACEHOLDER : PLACEHOLDER;
    } else if (input.dataset.sleepDisabled) {
        // Only undo what this view did: other states may disable the input too.
        input.disabled = false;
        input.placeholder = input.dataset.awakePlaceholder || input.placeholder;
        delete input.dataset.sleepDisabled;
        delete input.dataset.awakePlaceholder;
    }
}

/** The placeholder a sleeping composer shows (the popout sets it itself). */
export function sleepingPlaceholder(archived) {
    return archived ? ARCHIVED_PLACEHOLDER : PLACEHOLDER;
}

async function wakeCurrent(btn) {
    const s = state.currentSession;
    if (waking || !s || s.type !== 'live') return;
    waking = true;
    const label = btn.textContent;
    btn.disabled = true;
    btn.textContent = 'Waking…';
    try {
        const { isPopout, popoutWake } = await importOnce('./popout.js');
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

async function unarchiveCurrent(btn) {
    const s = state.currentSession;
    if (!s || s.type !== 'live' || btn.disabled) return;
    btn.disabled = true;
    try {
        // toggleAgentArchive updates the view of the agent on screen.
        await window.toggleAgentArchive(s.name, s.agent_type, s.session_id, 'unarchive');
    } finally {
        btn.disabled = false;
    }
}

document.addEventListener('click', (e) => {
    const btn = e.target.closest && e.target.closest('#sleeping-composer button[data-action]');
    if (!btn) return;
    if (btn.dataset.action === 'wake') wakeCurrent(btn);
    else if (btn.dataset.action === 'unarchive') unarchiveCurrent(btn);
    else if (btn.dataset.action === 'restart') window.restartSession();
});
