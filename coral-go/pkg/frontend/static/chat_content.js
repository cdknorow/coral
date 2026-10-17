/* How the live chat's content is appended to its container (#3837). Moved out of live_chat.js as it was; no behaviour change.
 *
 * The interface between the transcript renderer (live_chat.js) and the features that add rows of their own to the bottom of the
 * chat (pending messages, the Working row, the needs-input card, the empty state):
 *   - The container holds transcript content first, then at most a few "trailing chrome" rows (TRAILING_CHROME is the selector:
 *     .chat-working, .chat-needs-input, .chat-empty, .pending-messages).
 *   - lastContent(container): the last transcript element, skipping the trailing chrome.
 *   - appendContent(container, el): add transcript content before the trailing chrome, so chrome stays at the bottom.
 *   - appendWork / appendTurnFinal: the same for the turn's folded work group and its final reply.
 *   - A feature that adds a chrome row gives it one of the TRAILING_CHROME classes and never appends transcript content itself.
 * Nothing here knows what a chrome row contains. */

export function htmlToElement(html) {
    const t = document.createElement("template");
    t.innerHTML = html.trim();
    return t.content.firstElementChild;
}

// A run of tool calls (with their results) is folded into one collapsed "work group", with the one-line
// preamble of a message that makes them (live_chat.js) and the Coral notices that start or interrupt the work (#4171). Prose in a message with no tool calls, a reply, is
// never put in a group (#4105). A reply, a message from the user or a
// notice from Coral ends the run, so a turn that goes on after an answer reads as steps, the answer,
// more steps. (Before, more work after an answer folded the answer back into the next group, and a
// reply the operator wanted to read sat behind "5 steps".)
function createWorkGroup() {
    return htmlToElement(`<details class="work-group">
        <summary class="work-group-summary"><span class="work-group-count"></span><span class="work-group-latest"></span></summary>
        <div class="work-group-body"></div>
    </details>`);
}

// A reply to a turn that Coral's own notice started (the unread-messages notice, the health check, task-queue notices, stop-hook
// feedback; #4171) folds into the steps group when it is short: under FOLD_REPLY_MAX characters and one paragraph. A longer reply, and
// every reply to a message from the user, stays on the page.
export const FOLD_REPLY_MAX = 280;

export function isShortReply(text) {
    const t = String(text || "").trim();
    return t.length > 0 && t.length < FOLD_REPLY_MAX && !/\n\s*\n/.test(t);
}

/** The first sentence of a reply, for the group's label. */
export function firstSentence(text) {
    const t = String(text || "").replace(/\s+/g, " ").trim();
    const m = /^.*?[.!?](?=\s|$)/.exec(t);
    return (m ? m[0] : t).trim();
}

/** True when the turn now being drawn was started by a Coral notice, not by the user: the earliest group of the run of groups at the
 *  end of the transcript (since the last reply or user message) opens with a notice. A notice that arrives in the middle of the
 *  user's own turn joins a group that already has steps before it, so that turn stays the user's. */
export function noticeStartedTurn(container) {
    let el = lastContent(container);
    let earliest = null;
    while (el && el.classList.contains("work-group")) { earliest = el; el = el.previousElementSibling; }
    const first = earliest && earliest.querySelector(":scope > .work-group-body")?.firstElementChild;
    return !!(first && first.classList.contains("chat-system"));
}

export function updateWorkGroupSummary(group) {
    const body = group.querySelector(":scope > .work-group-body");
    // The folded reply is the label, not a step.
    const steps = Array.from(body.children).filter(c => !c.classList.contains("folded-reply")).length;
    group.querySelector(".work-group-count").textContent = `${steps} step${steps === 1 ? "" : "s"}`;
    const last = body.lastElementChild;
    let latest = "";
    if (last && last.classList.contains("folded-reply")) latest = firstSentence(last.querySelector(".message-text")?.textContent);
    else {
        const lastTool = Array.from(body.querySelectorAll(":scope > .tool-call")).pop();
        if (lastTool) latest = lastTool.querySelector(".tool-call-label").textContent;
        else if (last && last.classList.contains("chat-system")) latest = last.querySelector(".chat-system-text")?.textContent || "";
    }
    group.querySelector(".work-group-latest").textContent = latest;
}

// The Working row and pending (Sent/Queued) bubbles live at the bottom of the
// live chat but are not part of the transcript. Rendering reads and appends
// transcript content as if they were not there, so a new tool call still
// joins the turn's group while the Working row is showing.
export const TRAILING_CHROME = ".chat-working, .chat-needs-input, .chat-empty, .pending-messages";

export function lastContent(container) {
    let el = container.lastElementChild;
    while (el && el.matches(TRAILING_CHROME)) el = el.previousElementSibling;
    return el;
}

export function appendContent(container, el) {
    const firstChrome = Array.from(container.children).find(c => c.matches(TRAILING_CHROME));
    container.insertBefore(el, firstChrome || null);
}

export function appendWork(container, el, { newGroup = false } = {}) {
    let group = lastContent(container);
    if (!group || !group.classList.contains("work-group") || newGroup) {
        group = createWorkGroup();
        appendContent(container, group);
    }
    group.querySelector(":scope > .work-group-body").appendChild(el);
    updateWorkGroupSummary(group);
}

export function appendTurnFinal(container, el) {
    el.classList.add("turn-final");
    appendContent(container, el);
}
