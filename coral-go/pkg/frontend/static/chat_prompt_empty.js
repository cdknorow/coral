/* The empty chat's words and sending a prompt card's answer (moved out of live_chat.js, #5065; no behaviour change; its click and submit listeners stay in live_chat.js, which registers them after its own earlier ones). */
import { state } from './state.js';
import { deriveSessionState } from './render.js';
import { TRAILING_CHROME } from './chat_content.js';
import { showToast } from './utils.js';
import { serverFetch, sessionServer } from './server_base.js';
import { responseError } from './server_status.js';
import { reportSendError } from './terminal_gone.js';
import { promptOptionsBySession } from './needs_input_card.js';

// What a brand-new agent's empty chat says, by what the agent is doing: a title, and a hint on what to do next.
function emptyStateWords(session) {
    const row = (state.liveSessions || []).find(s => s.session_id === session?.session_id) || session;
    switch (deriveSessionState(row)) {
        case 'sleeping': return ["This agent is asleep", "Use Wake below to chat with it."];
        default: return ["No messages yet", "Type below and press Enter to send, Shift+Enter for a new line. The agent's replies and steps appear here."];
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
    controls.forEach(c => { c.disabled = true; });
    try {
        const resp = await serverFetch(sessionServer(session), `/api/sessions/live/${encodeURIComponent(session.name)}/answer-prompt`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ session_id: session.session_id, agent_type: session.agent_type || "", n, label, text: text || "" }),
        });
        if (!resp.ok) {
            const { text, code } = await responseError(resp, sessionServer(session), "Could not send the answer. Use the terminal.");
            reportSendError(session, text, code); // under the composer (#3218)
            controls.forEach(c => { c.disabled = false; });
            return false;
        }
        // The card refreshes from the terminal: the next question, the review
        // step, or gone once the tool runs.
        promptOptionsBySession.delete(session.session_id);
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
