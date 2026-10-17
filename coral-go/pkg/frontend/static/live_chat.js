/* Live history view — renders JSONL messages as a read-only conversation log */

import { state } from './state.js';
import { escapeHtml, renderMarkdown, labelCodeBlocks, showToast, copyText } from './utils.js';
import { registerActions } from './actions.js';
import { platform } from './platform/detect.js';
import { fitTerminal } from './xterm_renderer.js';
import { fireAgentView } from './agent_view.js';
import { deriveSessionState } from './render.js';
import { chatAgentAdapter } from './chat_agents.js';
import { serverFetch, sessionServer, serverUrl, currentServer, isCloud } from './server_base.js';
import { responseError } from './server_status.js';
import { reportSendError } from './terminal_gone.js';
import { pollEvery, isPaused, afterPause, isBusyStatus, busyRetryingText } from './overload.js';
import { storeKey, onAccountForgotten } from './local_store.js';
import { TRAILING_CHROME, appendContent, appendTurnFinal, appendWork, htmlToElement, isShortReply, lastContent, noticeStartedTurn, rollUpGroups, updateWorkGroupSummary } from './chat_content.js';
import { flashCopied, makeBubble } from './chat_bubbles.js';
import { unwrapPastedContent } from './pasted_content.js';
import { loginBySession, ownPromptCard, pendingToolBySession, promptOptionsBySession, syncNeedsInputCard } from './needs_input_card.js';
import { INTERRUPT_RE, appendCoralNotice, isCoralHealthNudge, isCoralNudge, normalizeNudgeText, noteAgentActivity, noteInterrupt, rememberUserMessage, syncPendingBubbles, syncWorkingIndicator } from './pending_messages.js';
import { browserLocal } from './safe_storage.js';
export { addPendingMessage, loadPendingFromStorage, removePendingMessage } from './pending_messages.js';
export { linkifyRefs } from './chat_bubbles.js';

let historyPollInterval = null;
let historyMessageCount = 0;
let historyStart = 0;       // absolute index of the oldest rendered message (keyset cursor)
let historyHasMore = false;  // whether older messages exist
let initialLoadDone = false; // whether the initial full load has completed
let loadingMore = false;     // an older page is being fetched

// The chat page of each recently seen agent, kept so a switch back shows it at once and fetches only what came after
// (#3726), instead of the whole first page again. A page is kept for a few minutes after the agent was last on screen,
// for the last few agents, and only its newest messages (what the first page and the polls rendered).
const KEPT_TTL_MS = 5 * 60 * 1000;
const KEPT_MAX_AGENTS = 6;
const KEPT_MAX_MESSAGES = 600;
const keptPages = new Map(); // session_id -> { messages, total, start, at }; the newest-used last

function keepFirstPage(sessionId, messages, total, start) {
    keptPages.delete(sessionId);
    keptPages.set(sessionId, { messages: messages.slice(), total, start, at: Date.now() });
    while (keptPages.size > KEPT_MAX_AGENTS) keptPages.delete(keptPages.keys().next().value);
}

function keepMore(sessionId, messages, total) {
    const kept = keptPages.get(sessionId);
    if (!kept) return;
    kept.messages.push(...messages);
    kept.total = total;
    kept.at = Date.now();
    if (kept.messages.length > KEPT_MAX_MESSAGES) {
        const drop = kept.messages.length - KEPT_MAX_MESSAGES;
        kept.messages.splice(0, drop);
        kept.start += drop;
    }
    keptPages.delete(sessionId);
    keptPages.set(sessionId, kept);
}

/** Forget every kept page (a suite that reuses an agent id with different content between its cases). */
export function clearKeptChatPages() { keptPages.clear(); }
// The account's chat text is not kept past sign-out or a change of Coral Hub account.
onAccountForgotten(clearKeptChatPages);

/** The kept page of a session seen within the last few minutes, or null (an older one is dropped). */
function freshKeptPage(sessionId) {
    const kept = keptPages.get(sessionId);
    if (!kept) return null;
    if (Date.now() - kept.at > KEPT_TTL_MS || kept.messages.length === 0) { keptPages.delete(sessionId); return null; }
    return kept;
}
// Messages per page. Tool calls and results each count as one and are folded
// away in the chat, so a page covers fewer turns than its size suggests. The
// first page is small so a switch paints quickly (the payload crosses the hub
// leg); older pages load as the reader scrolls toward the top. 30 messages fill
// the view and are about a third of the 100 it was (#3723: 22 to 61 KB against
// 68 to 170 KB on 100 MB transcripts).
const FIRST_PAGE = 30;
const OLDER_PAGE = 200;
// Pages carry at most CHAT_TRIM bytes of each long tool field (an output, a
// written file, an edit): they are folded away, and a card loads its message in
// full when opened (loadTrimmedCard).
const CHAT_TRIM = 2048;
// Bumped on reset so responses for a previous session are dropped, and used to
// keep a single refresh in flight: overlapping initial loads would otherwise
// both render the full history.
let historyGeneration = 0;
let keptRenderGeneration = -1; // the generation whose kept page is being built (refreshLiveHistory)
let refreshInFlight = -1;
let activeChatAbortController = null;
let activeLoadMoreAbortController = null;
// The query every chat request starts from: which session, and where it is.
function chatParams(session) {
    const params = new URLSearchParams();
    params.set("session_id", session.session_id);
    if (session.agent_type) {
        params.set("agent_type", session.agent_type);
    }
    // Through the hub the machine resolves the directory from session_id (its own stored record), and the hub's route
    // table refuses an absolute path in a file-ref parameter: sending it would turn the chat into a 404.
    if (session.working_directory && !(isCloud() && sessionServer(session) !== 'local')) {
        params.set("working_directory", session.working_directory);
    }
    return params;
}

// A trimmed message (#3723) keeps its absolute index, so its card can load it in full.
function noteIndexes(messages, start) {
    messages.forEach((m, i) => { if (m && m.trimmed) m._index = start + i; });
}

// One message, whole: the page of one that ends after it, without trim.
async function fetchChatMessage(session, index) {
    const params = chatParams(session);
    params.set("after", "0");
    params.set("limit", "1");
    params.set("before", String(index + 1));
    const resp = await serverFetch(sessionServer(session), `/api/sessions/live/${encodeURIComponent(session.name)}/chat?${params}`);
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    const data = await resp.json();
    return (data && Array.isArray(data.messages) && data.messages[0]) || null;
}

// Opening a card whose tool or output was shortened loads its message in full and shows that in
// the card. Checked by tool_use_id, so a transcript that changed underneath shows nothing wrong:
// the card then says its text was shortened.
async function loadTrimmedCard(card) {
    const session = state.currentSession;
    if (!session || card.dataset.trimLoading) return;
    card.dataset.trimLoading = "1";
    const generation = historyGeneration;
    try {
        const toolUseId = card.dataset.toolUseId || "";
        if (card.dataset.trimUse !== undefined) {
            const msg = await fetchChatMessage(session, Number(card.dataset.trimUse));
            if (generation !== historyGeneration) return;
            const tool = (msg && Array.isArray(msg.tool_uses) ? msg.tool_uses : []).find(t => (t.tool_use_id || "") === toolUseId);
            if (!tool || tool.trimmed) throw new Error("the message changed");
            const fresh = htmlToElement(renderToolCard(tool, chatAgentAdapter(session.agent_type)));
            const body = card.querySelector(":scope > .tool-call-body");
            const outputs = [...body.querySelectorAll(":scope > .tool-card-output")];
            body.replaceChildren(...fresh.querySelector(".tool-call-body").childNodes, ...outputs);
            delete card.dataset.trimUse;
        }
        if (card.dataset.trimResult !== undefined) {
            const msg = await fetchChatMessage(session, Number(card.dataset.trimResult));
            if (generation !== historyGeneration) return;
            if (!msg || msg.type !== "tool_result" || (msg.tool_use_id || "") !== (card.dataset.trimResultId || "") || msg.trimmed) {
                throw new Error("the message changed");
            }
            const pre = card.querySelector(".tool-card-output > pre");
            if (pre) pre.textContent = msg.content || "";
            delete card.dataset.trimResult;
            delete card.dataset.trimResultId;
        }
    } catch (e) {
        if (generation !== historyGeneration) return;
        const body = card.querySelector(":scope > .tool-call-body");
        if (body && !body.querySelector(":scope > .tool-card-trim-note")) {
            body.insertAdjacentHTML("beforeend", `<div class="tool-card-trim-note">Shortened; the full text couldn't be loaded (${escapeHtml(e.message || "error")}).</div>`);
        }
    } finally {
        delete card.dataset.trimLoading;
    }
}

// toggle doesn't bubble: caught on the way down.
document.addEventListener("toggle", (e) => {
    const card = e.target;
    if (!card || !card.open || !card.classList || !card.classList.contains("tool-call")) return;
    if (card.dataset.trimUse !== undefined || card.dataset.trimResult !== undefined) loadTrimmedCard(card);
}, true);



const TOOL_ICONS = {
    Read: "\u{1F4C4}",
    Edit: "\u{270F}\uFE0F",
    Write: "\u{1F4DD}",
    Bash: "\u{1F4BB}",
    Grep: "\u{1F50D}",
    Glob: "\u{1F4C2}",
    Agent: "\u{1F916}",
    WebSearch: "\u{1F310}",
    WebFetch: "\u{1F310}",
    TaskCreate: "\u{2611}\uFE0F",
    TaskUpdate: "\u{2611}\uFE0F",
    NotebookEdit: "\u{1F4D3}",
    AskUserQuestion: "\u{2753}",
};

function getToolIcon(name) {
    return TOOL_ICONS[name] || "\u{1F527}";
}

function renderDiffLines(oldStr, newStr) {
    const oldLines = oldStr.split("\n");
    const newLines = newStr.split("\n");
    let html = "";
    for (const line of oldLines) {
        html += `<div class="diff-line diff-del">- ${escapeHtml(line)}</div>`;
    }
    for (const line of newLines) {
        html += `<div class="diff-line diff-add">+ ${escapeHtml(line)}</div>`;
    }
    return html;
}

function renderPatchLines(patch) {
    return patch.split("\n").map(line => {
        const cls = line.startsWith("+") ? " diff-add" : line.startsWith("-") ? " diff-del" : "";
        return `<div class="diff-line${cls}">${escapeHtml(line)}</div>`;
    }).join("");
}

function toolLabel(tool) {
    if (tool.description) return tool.description;
    if (tool.command) return tool.command.split("\n")[0];
    return tool.input_summary || "";
}

function renderToolCard(tool, adapter, index) {
    const toolUseId = tool.tool_use_id || "";
    // A shortened tool (#3723): the index of its message, loaded in full when the card opens.
    const trim = tool.trimmed && Number.isInteger(index) ? ` data-trim-use="${Number(index)}"` : "";
    const view = adapter.toolView(tool);
    let bodyHtml = "";

    if (tool.command) {
        bodyHtml += `<pre class="tool-card-command">${escapeHtml(tool.command)}</pre>`;
    } else if (tool.input_summary && tool.input_summary !== toolLabel(tool)) {
        bodyHtml += `<div class="tool-card-summary">${escapeHtml(tool.input_summary)}</div>`;
    }
    if (tool.patch) {
        bodyHtml += `<div class="diff-content">${renderPatchLines(tool.patch)}</div>`;
    }

    if ((tool.name === "Edit" || view.name === "Edit" || view.icon === "Edit") && (tool.old_string || tool.new_string)) {
        bodyHtml += `<div class="diff-content">${renderDiffLines(tool.old_string || "", tool.new_string || "")}</div>`;
    }
    if ((tool.name === "Write" || view.name === "Write" || view.icon === "Write") && tool.write_content) {
        bodyHtml += `<pre class="tool-result-content">${escapeHtml(tool.write_content)}</pre>`;
    }

    const label = toolLabel(tool);
    return `<details class="tool-call" data-tool-use-id="${escapeHtml(toolUseId)}"${trim}>
        <summary class="tool-call-summary">
            <span class="tool-call-icon">${getToolIcon(view.icon)}</span>
            <span class="tool-call-name" title="${escapeHtml(tool.name || "")}">${escapeHtml(view.name)}</span>
            <span class="tool-call-label" title="${escapeHtml(label)}">${escapeHtml(label)}</span>
        </summary>
        <div class="tool-call-body">${bodyHtml}</div>
    </details>`;
}

// Questions to the user are part of the conversation, so they stay expanded.
function renderQuestionCard(tool) {
    let bodyHtml = "";
    if (tool.description) {
        bodyHtml += `<div class="tool-card-description">${escapeHtml(tool.description)}</div>`;
    }
    if (tool.questions) {
        for (const q of tool.questions) {
            bodyHtml += `<div class="tool-question">`;
            bodyHtml += `<div class="tool-question-text">${escapeHtml(q.question)}</div>`;
            if (q.options && q.options.length > 0) {
                bodyHtml += `<div class="tool-question-options">`;
                for (const opt of q.options) {
                    const label = typeof opt === "string" ? opt : (opt.label || opt.text || "");
                    const desc = typeof opt === "object" && opt ? opt.description : "";
                    bodyHtml += `<div class="tool-question-option">`;
                    bodyHtml += `<span class="tool-question-option-label">${escapeHtml(label)}</span>`;
                    if (desc) {
                        bodyHtml += `<span class="tool-question-option-desc">${escapeHtml(desc)}</span>`;
                    }
                    bodyHtml += `</div>`;
                }
                bodyHtml += `</div>`;
            }
            bodyHtml += `</div>`;
        }
    }

    return `<div class="tool-card" data-tool-use-id="${escapeHtml(tool.tool_use_id || "")}">${bodyHtml}</div>`;
}

// Join a prepended older page to the page after it: a run of tool calls split by the page
// boundary is one group again. A reply at the end of the older page stays a reply (#4105).
function stitchPages(lastOlder) {
    const next = lastOlder && lastOlder.nextElementSibling;
    if (!next || !next.classList.contains("work-group")) return;
    const nextBody = next.querySelector(":scope > .work-group-body");
    if (lastOlder && lastOlder.classList.contains("work-group")) {
        const olderBody = lastOlder.querySelector(":scope > .work-group-body");
        while (nextBody.firstChild) olderBody.appendChild(nextBody.firstChild);
        next.remove();
        updateWorkGroupSummary(lastOlder);
    } else {
        updateWorkGroupSummary(next);
    }
}

// Agent replies carry a copy button that copies the reply's markdown as the
// agent wrote it (not the rendered text).
const COPY_BUTTON = `<button type="button" class="chat-copy-btn" title="Copy" aria-label="Copy message"><span class="material-icons">content_copy</span></button>`;
const bubbleSource = new WeakMap();

document.addEventListener("click", async (e) => {
    const btn = e.target.closest && e.target.closest(".chat-copy-btn");
    if (!btn) return;
    const bubble = btn.closest(".chat-bubble");
    const text = bubbleSource.get(bubble) ?? bubble.querySelector(".message-text")?.innerText ?? "";
    if (!(await copyText(text))) {
        showToast("Could not copy", true);
        return;
    }
    flashCopied(btn);
});

// One message that can't be drawn must not cost the rest of the history (#3694): it gets a note
// in its place, and the messages around it are drawn as usual.
function renderMessage(msg, container, agentType = "claude") {
    try {
        renderOneMessage(msg, container, agentType);
    } catch (e) {
        console.warn("[coral] a chat message could not be shown", e);
        appendContent(container, makeBubble("chat-note chat-message-unrendered", "This message could not be shown."));
    }
}

function renderOneMessage(msg, container, agentType) {
    const adapter = chatAgentAdapter(agentType);
    if (msg.type === "user" && INTERRUPT_RE.test(String(msg.content || "").trim())) {
        appendContent(container, makeBubble("chat-note", "Interrupted"));
	} else if (msg.type === "user" && isCoralNudge(msg.content)) {
		appendCoralNotice(container, normalizeNudgeText(msg.content), isCoralHealthNudge(msg.content));
    } else if (msg.type === "user") {
        appendContent(container, makeBubble("chat-bubble human",
            `<div class="role-label">You</div><div class="message-text">${renderMarkdown(unwrapPastedContent(msg.content), undefined, 'chat')}</div>`));
    } else if (msg.type === "assistant") {
        if (msg.text) {
            const bubble = makeBubble(`chat-bubble assistant${msg.phase === "commentary" ? " commentary" : ""}`,
                `<div class="message-text">${renderMarkdown(msg.text, undefined, 'chat')}</div>${COPY_BUTTON}`);
            bubbleSource.set(bubble, msg.text);
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

function syncUsageLimitNotice(container, notice) {
    const existing = container.querySelector('.chat-usage-limit-notice');
    if (!notice) {
        existing?.remove();
        return;
    }
    if (existing) {
        existing.querySelector('.message-text').textContent = notice;
        return;
    }
    const bubble = makeBubble('chat-note chat-usage-limit-notice', `<span class="material-icons">warning</span><span class="message-text">${escapeHtml(notice)}</span>`);
    container.append(bubble);
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
async function renderMessagesChunked(messages, target, agentType, stillWanted) {
    for (let i = 0; i < messages.length; i += RENDER_SLICE) {
        for (const msg of messages.slice(i, i + RENDER_SLICE)) renderMessage(msg, target, agentType);
        if (i + RENDER_SLICE < messages.length) {
            await new Promise(resolve => setTimeout(resolve, 0));
            if (!stillWanted()) return false;
        }
    }
    return true;
}

export async function refreshLiveHistory() {
    if (!state.currentSession || state.currentSession.type !== "live") return;
    if (!platform.isNative && document.hidden) return;

    const session = state.currentSession;
    if (!session.session_id) return;

    const container = document.getElementById("live-history-messages");
    if (!container) return;

    // An agent seen in the last few minutes: its kept page shows at once, and only what came after is asked for
    // below (a poll from the page's end), not the first page again (#3726).
    if (!initialLoadDone && keptRenderGeneration !== historyGeneration) {
        const kept = freshKeptPage(session.session_id);
        if (kept) {
            // Built off screen in slices; a poll that comes while it builds returns at once (keptRenderGeneration).
            const generation = historyGeneration;
            keptRenderGeneration = generation;
            const built = document.createElement("div");
            let rebuilt = false;
            try {
                rebuilt = await renderMessagesChunked(kept.messages, built, session.agent_type, () => generation === historyGeneration);
            } catch (e) {
                // A failed build must not leave the chat blank or the guard set: forget the kept page, and the first page is
                // loaded below as for an agent not seen before.
                console.error("[coral] the kept chat page could not be rebuilt", e);
                keptPages.delete(session.session_id);
            } finally {
                if (!rebuilt && keptRenderGeneration === generation) keptRenderGeneration = -1;
            }
            if (generation !== historyGeneration) return; // the agent was switched while it built
            if (rebuilt) {
                container.innerHTML = "";
                while (built.firstChild) container.appendChild(built.firstChild);
                historyHasMore = kept.start > 0;
                historyStart = kept.start;
                historyMessageCount = kept.total;
                _updateLoadMoreButton(container);
                initialLoadDone = true;
                if (state.autoScroll) container.scrollTop = container.scrollHeight;
            }
        }
    }

    // Show loading indicator on first load
    if (!initialLoadDone && !container.querySelector('.loading-indicator') && !container.querySelector('.chat-load-error') && container.children.length === 0) {
        container.innerHTML = '<div class="loading-indicator">Loading chat history</div>';
    }

    const params = chatParams(session);

    if (!initialLoadDone) {
        // First load: fetch most recent messages with pagination
        params.set("after", "0");
        params.set("limit", String(FIRST_PAGE));
        params.set("offset", "0");
        params.set("trim", String(CHAT_TRIM));
    } else {
        // Subsequent polls: only fetch new messages
        params.set("after", historyMessageCount);
    }

    if (refreshInFlight === historyGeneration) return;
    const generation = historyGeneration;
    const server = sessionServer(session);
    // The first load during a pause (#3430): the server would refuse it as busy. Say so, and load
    // the moment the pause ends rather than a poll later.
    if (!initialLoadDone && isPaused(server === 'local' ? '' : server)) {
        _showHistoryBusy(container, null, server, generation);
        return;
    }
    refreshInFlight = generation;

    if (activeChatAbortController) {
        activeChatAbortController.abort();
    }
    const abortCtrl = new AbortController();
    activeChatAbortController = abortCtrl;

    try {
        const resp = await serverFetch(sessionServer(session), `/api/sessions/live/${encodeURIComponent(session.name)}/chat?${params}`, {
            signal: abortCtrl.signal,
        });
        if (generation !== historyGeneration) return;

        // The first load refused as busy (#3430): not an error. Show that it is retrying, and retry
        // once the pause the answer started has run out.
        if (!resp.ok && !initialLoadDone && isBusyStatus(resp.status)) {
            _showHistoryBusy(container, resp, server, generation);
            return;
        }

        if (!resp.ok) {
            let errorText = `HTTP ${resp.status}`;
            try {
                const errData = await resp.json();
                if (errData && errData.error) errorText = errData.error;
            } catch (_) {}
            throw new Error(errorText);
        }

        let data;
        try {
            data = await resp.json();
        } catch (err) {
            // A reply that isn't JSON says so, rather than only a parser's position (#3694).
            throw new Error(`the reply from the server could not be read: ${err.message}`);
        }
        if (generation !== historyGeneration) return;

        // The transcript is shorter than what this client holds (it was cut or replaced while the agent was away, or a kept
        // page outlived it): forget what is shown and load it again from its start.
        if (initialLoadDone && data.total < historyMessageCount) {
            keptPages.delete(session.session_id);
            container.innerHTML = "";
            historyMessageCount = 0;
            historyStart = 0;
            historyHasMore = false;
            initialLoadDone = false;
            setTimeout(refreshLiveHistory, 0); // after this refresh has ended
            return;
        }

        // What the server says about an open prompt rides on this reply (#3720): the pending tool call, and the options
        // of a prompt that may be open on the terminal.
        pendingToolBySession.set(session.session_id, data.pending || null);
        const po = data.prompt_options;
        if (po) promptOptionsBySession.set(session.session_id, { question: po.question || "", options: po.options || [], review: po.review || [] });
        else promptOptionsBySession.delete(session.session_id);
        // A sign-in the CLI waits on (#3830): the reply is fresher than the session row.
        if (typeof data.needs_login === "boolean") loginBySession.set(session.session_id, data.needs_login ? { reason: String(data.login_reason || "") } : null);

        if (data.messages && data.messages.length > 0) {
            if (!initialLoadDone) {
                container.innerHTML = "";
                noteIndexes(data.messages, data.start);
                // Initial load — render all and add "Load More" button if needed
                for (const msg of data.messages) {
                    renderMessage(msg, container, session.agent_type);
                }
                historyHasMore = data.has_more || false;
                historyStart = data.start;
                _updateLoadMoreButton(container);
                initialLoadDone = true;
                keepFirstPage(session.session_id, data.messages, data.total, historyStart);
            } else {
                // Poll — append new messages at the bottom.
                for (const msg of data.messages) {
                    renderMessage(msg, container, session.agent_type);
                }
                keepMore(session.session_id, data.messages, data.total);
            }
            for (const msg of data.messages) {
                if (msg.type === "user" && INTERRUPT_RE.test(String(msg.content || "").trim())) noteInterrupt(session.session_id, msg);
                else if (msg.type === "user") rememberUserMessage(session.session_id, msg);
                else noteAgentActivity(session.session_id, msg);
            }
            historyMessageCount = data.total;
        } else if (!initialLoadDone) {
            container.innerHTML = "";
            historyHasMore = false;
            initialLoadDone = true;
            _updateLoadMoreButton(container);
        }
        syncUsageLimitNotice(container, data.limit_notice || '');
        // The agent is still on screen: its kept page is as fresh as this reply, so the time limit counts from when it left.
        const keptNow = keptPages.get(session.session_id);
        if (keptNow) keptNow.at = Date.now();

        // Pending first: it settles its place at the bottom, then the working row goes above it
        syncPendingBubbles(container, session.session_id);
        syncNeedsInputCard(container, session);
        syncWorkingIndicator(container, session);
        syncEmptyState(container);
        if (state.autoScroll) {
            container.scrollTop = container.scrollHeight;
        }
    } catch (e) {
        if (e.name === 'AbortError') {
            return;
        }
        console.error("Failed to refresh live history:", e);
        if (generation === historyGeneration && !initialLoadDone) {
            container.innerHTML = `<div class="chat-load-error" role="alert">
                <span class="chat-error-message">Failed to load chat history (${escapeHtml(e.message || 'error')})</span>
                <button type="button" class="btn btn-secondary btn-small chat-retry-btn">Retry</button>
            </div>`;
            const retryBtn = container.querySelector('.chat-retry-btn');
            if (retryBtn) {
                retryBtn.onclick = () => {
                    container.innerHTML = '<div class="loading-indicator">Loading chat history</div>';
                    refreshInFlight = -1;
                    refreshLiveHistory();
                };
            }
        }
    } finally {
        if (activeChatAbortController === abortCtrl) {
            activeChatAbortController = null;
        }
        if (refreshInFlight === generation) refreshInFlight = -1;
    }
}

// A first history load refused as busy, or due during a pause: "Loading… (Coral Hub busy,
// retrying)" in place of the loading line, and the load again as soon as the pause is over, for
// the same agent only. One retry is queued at a time; the 1 s poll covers it too.
let _historyBusyRetry = null;
function _showHistoryBusy(container, resp, server, generation) {
    let el = container.querySelector(':scope > .loading-indicator');
    if (!el) {
        container.innerHTML = '<div class="loading-indicator"></div>';
        el = container.querySelector(':scope > .loading-indicator');
    }
    el.classList.add('chat-history-busy');
    el.setAttribute('role', 'status');
    el.textContent = busyRetryingText(resp, { machine: server, hub: isCloud() });
    if (_historyBusyRetry) clearTimeout(_historyBusyRetry);
    _historyBusyRetry = afterPause(() => {
        _historyBusyRetry = null;
        if (generation === historyGeneration && !initialLoadDone) refreshLiveHistory();
    }, server);
}

/** Load older messages (prepend above current messages). */
/** True while older transcript pages remain unloaded (used by find-in-chat). */
export function chatHasOlder() { return historyHasMore; }

export async function loadMoreHistory() {
    if (!state.currentSession || !historyHasMore || loadingMore) return;
    loadingMore = true;
    const generation = historyGeneration;
    let ok = false;
    try {
        ok = await _loadMoreHistory(generation);
    } finally {
        if (generation === historyGeneration) loadingMore = false;
    }
    // A page shorter than the view leaves the sentinel in sight, and the
    // observer only reports changes: keep loading while it is still within
    // reach. Measured, because the observer's entry for the prepend has not
    // been delivered yet.
    if (ok && generation === historyGeneration && historyHasMore && sentinelInReach()) {
        loadMoreHistory();
    }
    // Re-arm the observer: observe() reports the current state, so a reader
    // who left and came back to the top while the page loaded is not stuck.
    if (ok && generation === historyGeneration && sentinelObserver) {
        const btn = document.querySelector("#live-history-messages > .load-more-btn");
        if (btn) {
            sentinelObserver.unobserve(btn);
            sentinelObserver.observe(btn);
        }
    }
}

function sentinelInReach() {
    const container = document.getElementById("live-history-messages");
    const btn = container && container.querySelector(":scope > .load-more-btn");
    if (!btn || !container.clientHeight) return false;
    return btn.getBoundingClientRect().bottom >= container.getBoundingClientRect().top - AUTO_LOAD_PX;
}

async function _loadMoreHistory(generation) {

    const session = state.currentSession;
    const container = document.getElementById("live-history-messages");
    if (!container) return;

    const params = chatParams(session);
    // Pagination is anchored by the cursor; omit an explicit after=0 so
    // providers and test harnesses can distinguish load-more from initial load.
    // before= is the oldest rendered message's absolute index, so messages
    // appended since the first page do not shift the window.
    params.set("limit", String(OLDER_PAGE));
    params.set("trim", String(CHAT_TRIM));
    params.set("before", String(historyStart));
    const server = sessionServer(session);

    if (activeLoadMoreAbortController) {
        activeLoadMoreAbortController.abort();
    }
    const abortCtrl = new AbortController();
    activeLoadMoreAbortController = abortCtrl;

    try {
        const url = () => `/api/sessions/live/${encodeURIComponent(session.name)}/chat?${params}`;
        const resp = await serverFetch(server, url(), { signal: abortCtrl.signal });
        if (generation !== historyGeneration) return; // switched agents meanwhile
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        const data = await resp.json();
        if (generation !== historyGeneration) return;

        if (data.messages && data.messages.length > 0) {
            const temp = document.createElement("div");
            noteIndexes(data.messages, data.start);
            if (!await renderMessagesChunked(data.messages, temp, session.agent_type, () => generation === historyGeneration)) return;

            // Preserve scroll position: measure just before inserting
            const prevScrollHeight = container.scrollHeight;
            const prevScrollTop = container.scrollTop;

            // Find the insertion point (after the Load More button)
            const loadMoreBtn = container.querySelector(".load-more-btn");
            const refNode = loadMoreBtn ? loadMoreBtn.nextSibling : container.firstChild;

            const lastOlder = temp.lastElementChild;
            while (temp.firstChild) {
                container.insertBefore(temp.firstChild, refNode);
            }
            stitchPages(lastOlder);
            rollUpGroups(container);

            historyStart = data.start;
            historyHasMore = (data.has_more || false) && historyStart > 0;

            // Restore scroll position so the view doesn't jump
            container.scrollTop = prevScrollTop + (container.scrollHeight - prevScrollHeight);
        } else {
            historyHasMore = false;
        }

        _updateLoadMoreButton(container);
        return true;
    } catch (e) {
        if (e.name === 'AbortError') return false;
        console.error("Failed to load more history:", e);
        return false;
    } finally {
        if (activeLoadMoreAbortController === abortCtrl) {
            activeLoadMoreAbortController = null;
        }
    }
}

// The button at the top of the chat is also the autoload sentinel: when it
// comes within AUTO_LOAD_PX of the visible area, the next older page loads.
// The button stays for find-in-chat and for when a load failed.
const AUTO_LOAD_PX = 200;
let sentinelObserver = null;

function observeSentinel(container, btn) {
    if (typeof IntersectionObserver === "undefined") return;
    if (sentinelObserver && sentinelObserver.root !== container) {
        sentinelObserver.disconnect();
        sentinelObserver = null;
    }
    if (!sentinelObserver) {
        sentinelObserver = new IntersectionObserver((entries) => {
            for (const e of entries) {
                if (e.isIntersecting && historyHasMore && initialLoadDone) loadMoreHistory();
            }
        }, { root: container, rootMargin: `${AUTO_LOAD_PX}px 0px 0px 0px` });
    }
    sentinelObserver.observe(btn);
}

function _updateLoadMoreButton(container) {
    let btn = container.querySelector(".load-more-btn");
    if (historyHasMore) {
        if (!btn) {
            btn = document.createElement("button");
            btn.className = "load-more-btn chat-history-sentinel";
            btn.textContent = "Load older messages";
            btn.onclick = loadMoreHistory;
            container.insertBefore(btn, container.firstChild);
            observeSentinel(container, btn);
        }
    } else if (btn) {
        if (sentinelObserver) sentinelObserver.unobserve(btn);
        btn.remove();
    }
}

// Returns the first load, so a caller can hold slower work (the git/file
// queries) until the transcript is in. The browser allows only a handful of
// connections per origin, and on a big repo those queries are seconds long.
 function startLiveHistoryPoll() {
    stopLiveHistoryPoll();
    const firstLoad = refreshLiveHistory();
    historyPollInterval = pollEvery(refreshLiveHistory, 1000, { machine: () => state.currentSession && sessionServer(state.currentSession), agentView: true });
    return firstLoad;
}

export function stopLiveHistoryPoll() {
    if (historyPollInterval) {
        clearInterval(historyPollInterval);
        historyPollInterval = null;
    }
    if (activeChatAbortController) {
        activeChatAbortController.abort();
        activeChatAbortController = null;
    }
}

// Reset transcript state when the selected live session changes. A restart
// keeps the same pane but allocates a new Coral session ID; callers can ask us
// to resume an already-running poll so the new transcript is loaded from zero.
export function resetLiveHistory({ resume = false } = {}) {
    const wasPolling = !!historyPollInterval;
    stopLiveHistoryPoll();
    // Free the connections of the previous chat's fetches right away: through
    // the hub, a switch otherwise queues behind them.
    if (activeLoadMoreAbortController) {
        activeLoadMoreAbortController.abort();
        activeLoadMoreAbortController = null;
    }
    if (activeChatAbortController) {
        activeChatAbortController.abort();
        activeChatAbortController = null;
    }
    if (sentinelObserver) {
        sentinelObserver.disconnect();
        sentinelObserver = null;
    }
    const container = document.getElementById("live-history-messages");
    if (container) container.innerHTML = "";
    historyGeneration++;
    refreshInFlight = -1;
    // A new chat opens at the bottom. Only scroll events set autoScroll, and
    // the one emptying the container fires after the first page renders.
    state.autoScroll = true;
    historyMessageCount = 0;
    historyStart = 0;
    historyHasMore = false;
    initialLoadDone = false;
    loadingMore = false;
    if (resume) {
        if (wasPolling) startLiveHistoryPoll();
    }
}

// ── Center view mode: terminal vs. chat ─────────────────────────────────
// The transcript renders in the center pane in place of the terminal. The
// terminal keeps running underneath (hidden) so switching back is instant.

const VIEW_MODE_KEY = "coral-live-view-mode";

/** The chosen center view; Chat unless the user picked Terminal. */
export function getLiveViewMode() {
    try {
        return browserLocal.get(VIEW_MODE_KEY) === "terminal" ? "terminal" : "chat";
    } catch {
        return "chat";
    }
}

// Plain terminals have no transcript, so they always show the terminal.
function hasTranscript(session) {
    return !!session && session.agent_type !== "terminal";
}

/** The view the selected agent shows in the centre: "chat" or "terminal". A plain terminal has no transcript,
 *  so it is always the terminal; otherwise the persisted mode, or a one-off override. */
export function effectiveLiveViewMode(override) {
    return hasTranscript(state.currentSession) ? (override || getLiveViewMode()) : "terminal";
}

/** Apply the persisted mode (or a one-off override) to the DOM and start/stop the transcript poll. */
export function applyLiveViewMode(override) {
    const mode = effectiveLiveViewMode(override);
    const canChat = hasTranscript(state.currentSession);
    const toggle = document.querySelector(".live-view-toggle");
    if (toggle) toggle.hidden = !canChat;
    const findBtn = document.getElementById("chat-find-btn");
    if (findBtn) findBtn.hidden = mode !== "chat";
    if (mode !== "chat") window.closeChatFind?.();
    const wrapper = document.getElementById("capture-wrapper");
    if (wrapper) wrapper.classList.toggle("chat-mode", mode === "chat");
    // The command pane drops terminal-only controls in chat mode
    const pane = document.getElementById("command-pane");
    if (pane) pane.classList.toggle("chat-mode", mode === "chat");
    autosizeCommandInput();
    for (const m of ["terminal", "chat"]) {
        const btn = document.getElementById(`live-view-btn-${m}`);
        if (!btn) continue;
        btn.classList.toggle("active", m === mode);
        btn.setAttribute("aria-pressed", String(m === mode));
    }
    if (mode === "chat") {
        // The terminal is not shown: its socket closes (sessions.js), and opens again when it is.
        fireAgentView("coral:terminal-unwanted");
        return startLiveHistoryPoll();
    } else {
        stopLiveHistoryPoll();
        // The terminal is shown: it attaches now if it has not (sessions.js; no socket or xterm.js in Chat view).
        fireAgentView("coral:terminal-wanted");
        // The xterm canvas was hidden; refit now that it has a size again.
        setTimeout(fitTerminal, 0);
    }
    return Promise.resolve();
}


// On desktop the command box grows with its text, like a chat composer
// (CSS caps it at 40vh). Mobile keeps its own compact input bar.
const desktopComposer = window.matchMedia("(min-width: 768px)");

// What the last autosize measured for: the text, the box's width (a ResizeObserver keeps it; 0 while the box is hidden), the window
// height (the 40vh cap) and the mode. Measuring forces layout several times (#3866: 112 ms over five agent switches), so
// when none of these changed since the last run the result cannot have changed either and there is nothing to do.
let _autosizeKey = "";
let _inputWidth = 0;

// A phone's box has a fixed range of heights (mobile.css); at a large text size the placeholder wraps onto more lines than the range holds
// and is cut off. An empty box is made tall enough for its placeholder (up to 40 percent of the window); a box with text keeps the range.
function fitPlaceholder(input) {
    input.style.removeProperty("min-height");
    input.style.removeProperty("max-height");
    if (input.value || !input.placeholder || input.clientWidth < 40) return;   // not laid out yet: nothing to measure
    const cs = getComputedStyle(input);
    const pad = parseFloat(cs.paddingTop) + parseFloat(cs.paddingBottom) + parseFloat(cs.borderTopWidth) + parseFloat(cs.borderBottomWidth);
    const probe = document.createElement("div");
    probe.style.cssText = `position:absolute;visibility:hidden;white-space:pre-wrap;overflow-wrap:break-word;box-sizing:content-box;width:${input.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight)}px;font:${cs.font};line-height:${cs.lineHeight};letter-spacing:${cs.letterSpacing}`;
    probe.textContent = input.placeholder;
    document.body.appendChild(probe);
    const need = Math.min(probe.getBoundingClientRect().height + pad, window.innerHeight * 0.4);
    probe.remove();
    if (need > input.offsetHeight + 0.5) {
        input.style.setProperty("min-height", `${need}px`, "important");
        input.style.setProperty("max-height", `${need}px`, "important");
    }
}

function autosizeCommandInput() {
    const input = document.getElementById("command-input");
    if (!input) return;
    const key = [input.value, _inputWidth, window.innerHeight, desktopComposer.matches, getComputedStyle(input).fontSize,
        document.getElementById("command-pane")?.classList.contains("chat-mode")].join("\u0001");
    if (key === _autosizeKey) return;
    _autosizeKey = key;
    const before = input.offsetHeight;
    if (!desktopComposer.matches || !input.offsetParent) {
        input.style.height = "";
        fitPlaceholder(input);
        return;
    }
    // Collapse first so scrollHeight measures the text, not the old height
    input.style.height = "0px";
    input.style.height = input.scrollHeight + "px";
    if (input.offsetHeight === before) return;
    if (document.getElementById("capture-wrapper")?.classList.contains("chat-mode")) {
        // Keep the newest reply in view as the box grows
        const messages = document.getElementById("live-history-messages");
        if (messages && state.autoScroll) messages.scrollTop = messages.scrollHeight;
    } else {
        // The terminal area changed size; refit xterm to it
        fitTerminal();
    }
}

/** Autosize the command box on typing and on every programmatic value change
 *  (send clears it, session switches restore drafts, macros fill it). */
export function initCommandInputAutosize() {
    const input = document.getElementById("command-input");
    if (!input) return;
    const desc = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value");
    Object.defineProperty(input, "value", {
        configurable: true,
        get() { return desc.get.call(this); },
        set(v) { desc.set.call(this, v); autosizeCommandInput(); },
    });
    input.addEventListener("input", autosizeCommandInput);
    window.addEventListener("resize", autosizeCommandInput);
    if (typeof ResizeObserver === "function") {
        new ResizeObserver(entries => {
            const width = Math.round(entries[entries.length - 1].contentRect.width);
            if (width !== _inputWidth) { _inputWidth = width; autosizeCommandInput(); }
        }).observe(input);
    }
}

export function setLiveViewMode(mode) {
    try {
        browserLocal.set(VIEW_MODE_KEY, mode === "chat" ? "chat" : "terminal");
    } catch { /* storage unavailable — mode lasts for this page only */ }
    applyLiveViewMode();
    if (mode === "chat") {
        const container = document.getElementById("live-history-messages");
        if (container) container.scrollTop = container.scrollHeight;
        state.autoScroll = true;
    }
}

/** Show the terminal for this agent without changing the saved Chat/Terminal choice. */
export function showTerminalView() {
    applyLiveViewMode("terminal");
}


// A brand-new agent has no transcript yet; say so instead of a blank pane.
function syncEmptyState(container) {
    const hasContent = Array.from(container.children).some(c => !c.matches(TRAILING_CHROME + ", .load-more-btn, .loading-indicator"));
    const busy = container.querySelector(":scope > .chat-working, :scope > .chat-needs-input, :scope > .pending-messages");
    let el = container.querySelector(":scope > .chat-empty");
    if (hasContent || busy) {
        if (el) el.remove();
        return;
    }
    if (!el) {
        el = document.createElement("div");
        el.className = "chat-empty";
        el.textContent = "No messages yet. Send a message below to get started.";
        container.appendChild(el);
    }
}

// Answering: the server re-reads the terminal and sends the digit (plus the
// typed text for a text option) only if that option is still on screen with
// this label.
async function sendPromptAnswer(card, n, label, action, text) {
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

document.addEventListener("click", (e) => {
    const btn = e.target.closest && e.target.closest("#live-history-messages .cni-answer");
    if (!btn || btn.disabled || btn.nodeName !== "BUTTON") return;
    const card = ownPromptCard(btn);
    if (!card) return;
    btn.classList.add("sending");
    sendPromptAnswer(card, Number(btn.dataset.n), btn.dataset.label, btn.dataset.action).then(ok => {
        if (!ok) btn.classList.remove("sending");
    });
});

document.addEventListener("submit", (e) => {
    const form = e.target.closest && e.target.closest("#live-history-messages .cni-text-form");
    if (!form) return;
    e.preventDefault();
    if (!ownPromptCard(form)) return;
    const input = form.querySelector("input");
    const text = input.value.trim();
    if (!text) {
        input.focus();
        return;
    }
    sendPromptAnswer(ownPromptCard(form), Number(form.dataset.n), form.dataset.label, "text", text);
});

// A prompt card's Open terminal (#3645).
registerActions({
    'terminal-view-open': () => window.showTerminalView(),
});
