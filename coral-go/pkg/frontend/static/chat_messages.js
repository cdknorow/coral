/* Drawing a chat's messages (moved out of live_chat.js, #5065; no behaviour change; the copy button's click listener stays in live_chat.js, which registers it after its own earlier ones). */
import { appendContent, appendTurnFinal, appendWork, htmlToElement, isShortReply, noticeStartedTurn } from './chat_content.js';
import { makeBubble } from './chat_bubbles.js';
import { unwrapPastedContent } from './pasted_content.js';
import { isCoralNudge, INTERRUPT_RE, appendCoralNotice, isCoralHealthNudge, normalizeNudgeText } from './pending_messages.js';
import { escapeHtml, renderMarkdown } from './utils.js';
import { CHAT_MARKDOWN } from './markdown.js';
import { thumbHtml, wireThumbs, sharedImageChips } from './chat_thumbs.js';
import { splitSharedImages } from './shared_images.js';
import { chatAgentAdapter } from './chat_agents.js';
import { SAVE_BUTTON } from './saved_marks.js';
import { messageTimeHtml } from './message_time.js';
import { getToolIcon, renderToolCard, renderQuestionCard } from './chat_tool_cards.js';

// Agent replies carry a copy button that copies the reply's markdown as the
// agent wrote it (not the rendered text).
const COPY_BUTTON = `<button type="button" class="chat-copy-btn" title="Copy" aria-label="Copy message"><span class="material-icons" aria-hidden="true">content_copy</span></button>`;

export const bubbleSource = new WeakMap();

// One message that can't be drawn must not cost the rest of the history (#3694): it gets a note
// in its place, and the messages around it are drawn as usual.
export function renderMessage(msg, container, agentType = "claude") {
    try {
        renderOneMessage(msg, container, agentType);
    } catch (e) {
        console.warn("[coral] a chat message could not be shown", e);
        appendContent(container, makeBubble("chat-note chat-message-unrendered", "This message could not be shown."));
    }
}

// A user turn that Coral typed into the agent, not the Operator: the server says which it knows, by where the turn came from (coral_notice, #4306): true for a turn Coral delivered itself, false for one the
// Operator sent from the dashboard (#5482: a person's own "[Coral ...]", "[Task #4]" or "Stop hook feedback:" is theirs, whatever its words). Only a turn with no recorded origin (older history, anything sent
// before the server started, keys typed in the terminal view) is read by its text. A long notice reaches the agent as a paste, which Claude Code records wrapped in pasted_content tags, so the text is
// unwrapped before it is read.
function isCoralDelivery(msg) {
    if (msg.coral_notice === true) return true;
    if (msg.coral_notice === false) return false;
    return isCoralNudge(unwrapPastedContent(msg.content));
}

// A pasted image in a user turn (Codex records it as a name like "[Image #1]" and the saved file's base name): a chip with
// the name, and the file name when there is one. The chip is not a link; the image is shown as a thumbnail from its reference in the session's transcript, or
// from an upload by the file's name (chat_thumbs.js, #5028, #5032).
function attachmentChips(attachments) {
    const chips = (Array.isArray(attachments) ? attachments : []).filter(a => a && a.name).map(a =>
        `<span class="chat-attachment-chip">${escapeHtml(a.name)}${a.file ? ` <span class="chat-attachment-file">${escapeHtml(a.file)}</span>` : ""}</span>${thumbHtml(a)}`);
    return chips.length ? `<div class="chat-attachments">${chips.join("")}</div>` : "";
}

function renderOneMessage(msg, container, agentType) {
    const adapter = chatAgentAdapter(agentType);
    if (msg.type === "user" && INTERRUPT_RE.test(String(msg.content || "").trim())) {
        appendContent(container, makeBubble("chat-note", "Interrupted"));
    } else if (msg.type === "user" && msg.local_command === true) {
        // A slash command the CLI ran by itself ("/model sonnet"): a small note of what was typed, not a bubble (#5488). Seeing it here is how the message the person sent is known to be taken.
        appendContent(container, makeBubble("chat-note chat-local-command", escapeHtml(String(msg.content || ""))));
	} else if (msg.type === "user" && isCoralDelivery(msg)) {
		const text = unwrapPastedContent(msg.content);
		appendCoralNotice(container, normalizeNudgeText(text), isCoralHealthNudge(text));
    } else if (msg.type === "user") {
        // Images pasted in the dashboard lead the turn as "<path> coral://artifacts/<hex>" pairs (#5284): shown as artifact thumbnails, not as the text of a path and a link.
        const lead = splitSharedImages(unwrapPastedContent(msg.content));
        const bubble = makeBubble("chat-bubble human",
            `<div class="role-label">You</div><div class="message-text">${renderMarkdown(lead.rest, CHAT_MARKDOWN, 'chat')}</div>${attachmentChips(msg.attachments)}${sharedImageChips(lead.images)}${msg.key ? SAVE_BUTTON : ""}${messageTimeHtml(msg.timestamp)}`);
        if (msg.key) { bubble.dataset.msgKey = msg.key; bubble.dataset.said = msg.timestamp || ""; } // Save for Later finds the message again by this (#4510)
        wireThumbs(bubble);
        appendContent(container, bubble);
    } else if (msg.type === "assistant") {
        if (msg.text) {
            const bubble = makeBubble(`chat-bubble assistant${msg.phase === "commentary" ? " commentary" : ""}`,
                `<div class="message-text">${renderMarkdown(msg.text, CHAT_MARKDOWN, 'chat')}</div>${msg.key ? SAVE_BUTTON : ""}${COPY_BUTTON}${messageTimeHtml(msg.timestamp)}`);
            bubbleSource.set(bubble, msg.text);
            if (msg.key) { bubble.dataset.msgKey = msg.key; bubble.dataset.said = msg.timestamp || ""; } // Save for Later finds the message again by this (#4510)
            // Prose in a message that also makes tool calls is a one-line preamble ("Checking the CSS first."): it goes
            // in the steps group with the calls. Prose in a message with no tool calls (a question card is not a step)
            // is a reply: it always shows and it ends the group (#4105).
            const preamble = (msg.tool_uses || []).some(t => !adapter.toolView(t).isQuestion);
            // A short reply to a turn a Coral notice started folds into the group too, and labels it (#4171).
            const folded = !preamble && (msg.tool_uses || []).length === 0 && isShortReply(msg.text) && noticeStartedTurn(container);
            if (folded) bubble.classList.add("folded-reply");
            if (preamble || folded || adapter.assistantPlacement(msg) === "work") appendWork(container, bubble);
            else appendTurnFinal(container, bubble);
        }
        for (const tool of msg.tool_uses || []) {
            const view = adapter.toolView(tool);
            if (view.isQuestion) {
                appendTurnFinal(container, makeBubble("chat-bubble assistant",
                    `<div class="role-label">${getToolIcon(view.icon)} ${escapeHtml(view.questionLabel)}</div>` + renderQuestionCard(tool)));
            } else {
                appendWork(container, htmlToElement(renderToolCard(tool, adapter, msg._index)));
            }
        }
    } else if (msg.type === "tool_result") {
        const toolUseId = msg.tool_use_id || "";
        const target = toolUseId
            ? container.querySelector(`[data-tool-use-id="${CSS.escape(toolUseId)}"]`)
            : null;
        const outputHtml = `<pre class="tool-output-inline${msg.is_error ? " tool-result-error" : ""}">${escapeHtml(msg.content || "")}</pre>`;
        const markTrimmed = card => {
            if (!msg.trimmed || !Number.isInteger(msg._index)) return card;
            card.dataset.trimResult = String(msg._index);
            card.dataset.trimResultId = toolUseId;
            return card;
        };
        if (target && target.classList.contains("tool-call")) {
            if (msg.is_error) target.classList.add("tool-call-error");
            target.querySelector(".tool-call-body").insertAdjacentHTML("beforeend",
                `<div class="tool-card-output">${outputHtml}</div>`);
            markTrimmed(target);
        } else if (target) {
            target.insertAdjacentHTML("beforeend", `<div class="tool-card-output">${outputHtml}</div>`);
        } else {
            // The matching tool call is on an older, not-yet-loaded page.
            const toolName = msg.tool_name || "Tool";
            const view = adapter.toolView({ name: toolName });
            appendWork(container, markTrimmed(htmlToElement(`<details class="tool-call${msg.is_error ? " tool-call-error" : ""}">
                    <summary class="tool-call-summary">
                        <span class="tool-call-icon">${getToolIcon(view.icon)}</span>
                        <span class="tool-call-name" title="${escapeHtml(toolName)}">${escapeHtml(view.name)}</span>
                        <span class="tool-call-label">output</span>
                    </summary>
                    <div class="tool-call-body"><div class="tool-card-output">${outputHtml}</div></div>
                </details>`)));
        }
    }
}

/** Render a full transcript into an empty container (history Chat tab). */
export function renderTranscript(messages, container, agentType = "claude") {
    container.dataset.agentType = agentType;
    for (const msg of messages) {
        const before = container.children.length;
        renderMessage(msg, container, agentType);
        if (msg.__coralMessageIndex != null) {
            for (let i = before; i < container.children.length; i++) {
                container.children[i].dataset.messageIndex = String(msg.__coralMessageIndex);
            }
        }
    }
}

// Messages rendered in slices of RENDER_SLICE with a yield between them, so a long page is not one long task (#3866: 200 messages
// took about 190 ms and a 600-message kept page about 500 ms in one go). `target` is a detached element; the caller inserts what
// it built. Resolves false, rendering no more, when `stillWanted()` says the page was left meanwhile.
const RENDER_SLICE = 40;

export async function renderMessagesChunked(messages, target, agentType, stillWanted) {
    for (let i = 0; i < messages.length; i += RENDER_SLICE) {
        for (const msg of messages.slice(i, i + RENDER_SLICE)) renderMessage(msg, target, agentType);
        if (i + RENDER_SLICE < messages.length) {
            await new Promise(resolve => setTimeout(resolve, 0));
            if (!stillWanted()) return false;
        }
    }
    return true;
}
