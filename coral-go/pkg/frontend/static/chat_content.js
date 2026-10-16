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

// A run of tool calls (with their results) is folded into one collapsed "work group". Only tool calls
// fold (#4105): the agent's own prose is never put in a group. A reply, a message from the user or a
// notice from Coral ends the run, so a turn that goes on after an answer reads as steps, the answer,
// more steps. (Before, more work after an answer folded the answer back into the next group, and a
// reply the operator wanted to read sat behind "5 steps".)
function createWorkGroup() {
    return htmlToElement(`<details class="work-group">
        <summary class="work-group-summary"><span class="work-group-count"></span><span class="work-group-latest"></span></summary>
        <div class="work-group-body"></div>
    </details>`);
}

export function updateWorkGroupSummary(group) {
    const body = group.querySelector(":scope > .work-group-body");
    const steps = body.children.length;
    group.querySelector(".work-group-count").textContent = `${steps} step${steps === 1 ? "" : "s"}`;
    const lastTool = Array.from(body.querySelectorAll(":scope > .tool-call")).pop();
    const latest = lastTool ? lastTool.querySelector(".tool-call-label").textContent : "";
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

export function appendWork(container, el) {
    let group = lastContent(container);
    if (!group || !group.classList.contains("work-group")) {
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
