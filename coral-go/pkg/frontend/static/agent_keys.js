/* The agent's keys and Stop on a phone's chat (#6802, design #6795, UI Accessibility's conditions on it). Three things, all for a person at Interact or above (a viewer's composer is `.hub-view-only`: none of
 * this shows there, and the server refuses a viewer's keys anyway):
 *  - Stop: while the agent works and the message box is empty (no words, no pictures) the round Send button is a red rounded square, "Stop the agent". It sends Escape (the CLI's interrupt; Ctrl-C quits the
 *    Claude CLI on a second press), once: it is aria-disabled (still focusable) until the agent's state changes or 3 s pass. Typing a word turns it back into Send.
 *  - The Keys chip (aria-pressed) opens a row of seven keys above the message box. It is closed on every visit: a new agent, or the chat coming back on screen, starts it closed.
 *  - A question or menu (the needs-input state) opens the strip by itself, once per question, unless the person closed it for that question; when the question is answered it closes again only if it opened itself.
 * Everything the screen reader hears goes through #agent-keys-status, a region that is in the page from the start. The key taps themselves say "Sent: Escape" through the toast (the Notifications regions). */
import { deriveSessionState } from './session_state.js';
import { isPhoneLayout } from './phone_layout.js';
import { registerActions } from './actions.js';
import { setHidden, setAttr, setText } from './dom_write.js';
import { sendRawKeys } from './controls.js';

const $ = id => document.getElementById(id);
const STOP_AGAIN_MS = 3000;
const MIN_CHAT_PX = 72; // three lines: below it with the keyboard up the strip offers to close itself

let open = false;            // the strip is showing
let openedBy = null;         // byChip | byQuestion
let closedFor = null;        // the question the person (or the lack of room) closed it for
let sessionId = null;        // the agent this state is for
let away = true;             // the chat was off screen at the last look: the next look is a new visit
let stopShown = false;
let stopHeldUntil = 0;
let stopTimer = 0;
let last = null;             // the latest row of the open agent

const say = text => {
    const r = $('agent-keys-status');
    if (!r) return;
    setText(r, '');
    requestAnimationFrame(() => setText(r, text)); // cleared first: the same words said twice are heard twice
};

/** The question the agent is asking now, as a key (a different question is a different key), or '' when it is not asking one a person answers with keys (a sign-in is answered in its terminal). */
export function questionKey(row) {
    if (!row || deriveSessionState(row) !== 'needs_input' || row.waiting_kind === 'login') return '';
    return `${row.session_id || ''}|${row.waiting_kind || ''}|${row.waiting_summary || ''}`;
}

/** Whether Send is Stop now: the agent works, the box is empty, and this is a phone's layout for a person who may send. */
export function stopWanted({ working, text, pictures, viewOnly, phone }) {
    return !!working && !!phone && !viewOnly && !String(text || '').trim() && !pictures;
}

function paintStrip() {
    const strip = $('agent-key-strip'), chip = $('keys-chip');
    if (!strip || !chip) return;
    setHidden(strip, !open);
    setAttr(chip, 'aria-pressed', String(open));
}

function openStrip(by, words) {
    if (open) return;
    open = true; openedBy = by; paintStrip();
    if (words) say(words);
}

function closeStrip(words, { forQuestion = false } = {}) {
    if (!open) return;
    const strip = $('agent-key-strip'), box = $('command-input');
    if (strip && strip.contains(document.activeElement) && box) box.focus(); // never to the page
    open = false; openedBy = null; paintStrip();
    if (forQuestion && last) closedFor = questionKey(last) || closedFor;
    if (words) say(words);
}

function paintStop(want) {
    const btn = $('mobile-send-btn');
    if (!btn) return;
    if (want === stopShown) return;
    stopShown = want;
    btn.classList.toggle('is-stop', want);
    if (want) btn.setAttribute('data-on-click', 'cmd-stop'); else btn.setAttribute('data-on-click', 'cmd-send'); // literal names: the action census reads them
    setAttr(btn, 'aria-label', want ? 'Stop the agent' : 'Send');
    if (!want) { btn.removeAttribute('aria-disabled'); stopHeldUntil = 0; }
    else say('Stop is shown');
}

function inputState() {
    const box = $('command-input');
    return { text: box ? box.value : '', pictures: document.querySelectorAll('#image-attachments > *').length };
}

/** Called with the open agent's row each time the page learns of its state (updateWaitingIndicator), and when the box changes. */
export function syncAgentKeys(row) {
    if (row) last = row;
    const r = last;
    const pane = $('command-pane');
    if (!pane || !r) return;
    const visible = pane.checkVisibility && pane.checkVisibility();
    if (!visible) { away = true; return; }
    const id = r.session_id || r.name || null;
    if (away || id !== sessionId) { // a new visit: closed, nothing remembered
        sessionId = id; away = false; open = false; openedBy = null; closedFor = null; paintStrip();
    }
    const viewOnly = pane.classList.contains('hub-view-only');
    if (viewOnly) { open = false; openedBy = null; paintStrip(); }
    const key = deriveSessionState(r);
    const q = questionKey(r);
    if (!viewOnly) {
        if (q && !open && closedFor !== q) openStrip('byQuestion', 'Keys for the question are shown');
        else if (!q && open && openedBy === 'byQuestion') closeStrip();
        if (!q) closedFor = null;
    }
    const { text, pictures } = inputState();
    paintStop(stopWanted({ working: key === 'working', text, pictures, viewOnly, phone: isPhoneLayout() }));
}

registerActions({
    'agent-keys-toggle': () => {
        if (open) closeStrip(undefined, { forQuestion: true });
        else { open = false; openStrip('byChip'); }
    },
    'cmd-stop': async () => {
        const btn = $('mobile-send-btn');
        if (!btn || Date.now() < stopHeldUntil) return; // one press is enough: a second interrupt quits the CLI
        stopHeldUntil = Date.now() + STOP_AGAIN_MS;
        setAttr(btn, 'aria-disabled', 'true');
        clearTimeout(stopTimer);
        stopTimer = setTimeout(() => { if (stopShown) btn.removeAttribute('aria-disabled'); stopHeldUntil = 0; }, STOP_AGAIN_MS); // timeout-ok: the second press is allowed again if nothing changed
        say('Stopping the agent');
        await sendRawKeys(['Escape'], { silent: true });
    },
});

/** With the keyboard up the strip must not push the chat out: under three lines of chat it closes itself and says so. */
function checkRoom() {
    if (!open) return;
    const chat = $('live-history-messages');
    const pane = chat && chat.closest('.chat-wrapper, .live-history, #live-history') || chat;
    const h = pane ? pane.clientHeight : 0;
    if (h && h < MIN_CHAT_PX) closeStrip('Keys closed: there is no room for them with the keyboard up', { forQuestion: true });
}

if (typeof document !== 'undefined') {
    const wire = () => {
        const strip = $('agent-key-strip'), chip = $('keys-chip'), btn = $('mobile-send-btn'), box = $('command-input');
        if (!strip || !chip || !btn) return false;
        // a tap on a key, the chip or Stop leaves focus where it is: the on-screen keyboard stays open (a keyboard or switch user still reaches them by Tab and Enter)
        for (const el of [strip, chip, btn]) el.addEventListener('pointerdown', e => { if (e.pointerType === 'touch' || e.pointerType === 'pen') e.preventDefault(); });
        // an aria-disabled Stop does nothing when pressed again
        btn.addEventListener('click', e => { if (btn.getAttribute('aria-disabled') === 'true') { e.stopPropagation(); e.preventDefault(); } }, true);
        if (box) box.addEventListener('input', () => syncAgentKeys());
        window.addEventListener('resize', () => { checkRoom(); });
        if (window.visualViewport) window.visualViewport.addEventListener('resize', () => { checkRoom(); });
        return true;
    };
    if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', wire, { once: true }); else wire();
}
