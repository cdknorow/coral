/* The empty chat's words and sending a prompt card's answer (moved out of live_chat.js, #5065; no behaviour change; its click and submit listeners stay in live_chat.js, which registers them after its own earlier ones). */
import { state } from './state.js';
import { deriveSessionState } from './render.js';
import { TRAILING_CHROME } from './chat_content.js';
import { showToast } from './utils.js';
import { serverFetch, sessionServer } from './server_base.js';
import { responseError } from './server_status.js';
import { reportSendError, noteHooksAnswered } from './terminal_gone.js';
import { announcePrompt, promptOptionsBySession, syncNeedsInputCard } from './needs_input_card.js';

// The first seconds of a new agent (#6058): its CLI has written nothing yet (the list has no output age for it), and it was started less than STARTING_MS ago by the list's created_at AND this page first saw it less than STARTING_MS ago (the second bounds a server clock that runs ahead of this one). "No messages yet" there reads as ready; it says the CLI is coming up.
// After that the row's own states take over (the "Waiting in the terminal" card from 25 s with no event at all).
const STARTING_MS = 25 * 1000;
const firstSeen = new Map(); // session_id -> ms of the first look at its empty chat
function isStarting(row) {
    if (!row || !row.session_id || row.agent_type === "terminal" || row.not_started || row.working || row.waiting_for_input || typeof row.staleness_seconds === "number") return false;
    const startedAt = Date.parse(row.created_at || ""); // the agent's own start: one that has been idle for hours is not starting, whenever this page opens it
    if (Number.isFinite(startedAt) && Date.now() - startedAt >= STARTING_MS) return false;
    if (!firstSeen.has(row.session_id)) firstSeen.set(row.session_id, Date.now());
    return Date.now() - firstSeen.get(row.session_id) < STARTING_MS;
}
const CLI_NAMES = { claude: "Claude", codex: "Codex", pi: "Pi", agy: "Antigravity", antigravity: "Antigravity", gemini: "Antigravity" };

// What a brand-new agent's empty chat says, by what the agent is doing: a title, and a hint on what to do next.
function emptyStateWords(session) {
    const row = (state.liveSessions || []).find(s => s.session_id === session?.session_id) || session;
    if (isStarting(row) && deriveSessionState(row) === "idle") return [`Waiting for ${CLI_NAMES[row.agent_type] || "the agent"} to start`, "Its first screen is not up yet. You can type below once it is."];
    switch (deriveSessionState(row)) {
        case 'sleeping': return row.sleep_reason === 'agent_exited' ? ["This agent has ended", "Its CLI exited. Use Restart below to start it again."] : ["This agent is asleep", "Use Wake below to chat with it."];
        default: return ["No messages yet", "Type below to start."];
    }
}

// A brand-new agent has no transcript yet; say so, and what to do, instead of a blank pane.
export function syncEmptyState(container) {
    const hasContent = Array.from(container.children).some(c => !c.matches(TRAILING_CHROME + ", .load-more-btn, .loading-indicator"));
    const busy = container.querySelector(":scope > .chat-working, :scope > .chat-needs-input, :scope > .pending-messages");
    let el = container.querySelector(":scope > .chat-empty");
    if (hasContent || busy) {
        if (el) el.remove();
        return;
    }
    const [title, hint] = emptyStateWords(state.currentSession);
    if (!el) {
        el = document.createElement("div");
        el.className = "chat-empty";
        el.setAttribute("role", "status");
        el.innerHTML = '<div class="chat-empty-title"></div><div class="chat-empty-hint"></div>';
        container.appendChild(el);
    }
    // Only write a word that changed, so a screen reader is not told the same thing at every poll
    const t = el.querySelector(".chat-empty-title"), h = el.querySelector(".chat-empty-hint");
    if (t.textContent !== title) t.textContent = title;
    if (h.textContent !== hint) h.textContent = hint;
}

// Answering: the server re-reads the terminal and sends the digit (plus the
// typed text for a text option) only if that option is still on screen with
// this label.
export async function sendPromptAnswer(card, n, label, action, text) {
    const session = state.currentSession;
    if (!session || session.type !== "live") return false;
    const controls = card ? Array.from(card.querySelectorAll(".cni-answer, .cni-text-form button, .cni-text-form input")) : [];
    // A disabled control loses focus: remember which one had it, and put it back (a toggle keeps it) or, when the card goes with the answer, move it to the message box (#5252).
    const hadFocus = card && card.contains(document.activeElement) ? document.activeElement : null;
    controls.forEach(c => { c.disabled = true; });
    const giveFocusBack = (to) => { if (hadFocus && to && to.isConnected) to.focus({ preventScroll: true }); };
    try {
        const resp = await serverFetch(sessionServer(session), `/api/sessions/live/${encodeURIComponent(session.name)}/answer-prompt`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(action === "submit"
                ? { session_id: session.session_id, agent_type: session.agent_type || "", submit: true }
                : { session_id: session.session_id, agent_type: session.agent_type || "", n, label, text: text || "" }),
        });
        if (!resp.ok) {
            const { text, code } = await responseError(resp, sessionServer(session), "Could not send the answer. Use the terminal.");
            reportSendError(session, text, code); // under the composer (#3218)
            controls.forEach(c => { c.disabled = false; });
            giveFocusBack(hadFocus);
            return false;
        }
        if (action === "toggle") {
            // A box of a multi-select question: nothing is answered. The card shows the screen the server
            // read after the toggle (the mark it confirmed), never a mark flipped here (#5232).
            const po = (await resp.json().catch(() => null) || {}).prompt_options;
            if (po) promptOptionsBySession.set(session.session_id, { question: po.question || "", options: po.options || [], review: po.review || [], multi: !!po.multi, submit: po.submit || null });
            const container = document.getElementById("live-history-messages");
            if (container) syncNeedsInputCard(container, session);
            controls.forEach(c => { c.disabled = false; });
            giveFocusBack(hadFocus);
            const row = ((po && po.options) || []).find(o => Number(o.n) === n);
            if (row) announcePrompt(`${row.label}, ${row.checked ? "checked" : "not checked"}`);
            return true;
        }
        // The card refreshes from the terminal: the next question, the review
        // step, or gone once the tool runs.
        promptOptionsBySession.delete(session.session_id);
        noteHooksAnswered();
        if (action === "submit") announcePrompt("Answer sent");
        if (action !== "chat") giveFocusBack(document.getElementById("command-input")); // the card goes with its answer: focus goes to the message box
        if (action === "chat") {
            const input = document.getElementById("command-input");
            if (input) input.focus();
            showToast("Type your reply below");
        }
        return true;
    } catch {
        showToast("Could not send the answer. Use the terminal.", true);
        controls.forEach(c => { c.disabled = false; });
        return false;
    }
}
