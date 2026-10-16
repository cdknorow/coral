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
import { TRAILING_CHROME, appendContent, appendTurnFinal, appendWork, htmlToElement, lastContent, updateWorkGroupSummary } from './chat_content.js';
import { flashCopied, makeBubble } from './chat_bubbles.js';
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

// Join a prepended older page to the page after it: a trailing "final" reply
// that was followed by more work is folded in, and split groups are merged.
function stitchPages(lastOlder) {
    let next = lastOlder && lastOlder.nextElementSibling;
    if (!next || !next.classList.contains("work-group")) return;
    const nextBody = next.querySelector(":scope > .work-group-body");
    if (lastOlder.classList.contains("turn-final")) {
        lastOlder.classList.remove("turn-final");
        nextBody.insertBefore(lastOlder, nextBody.firstChild);
        lastOlder = next.previousElementSibling;
    }
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
            `<div class="role-label">You</div><div class="message-text">${renderMarkdown(msg.content, undefined, 'chat')}</div>`));
    } else if (msg.type === "assistant") {
        if (msg.text) {
            const bubble = makeBubble(`chat-bubble assistant${msg.phase === "commentary" ? " commentary" : ""}`,
                `<div class="message-text">${renderMarkdown(msg.text, undefined, 'chat')}</div>${COPY_BUTTON}`);
            bubbleSource.set(bubble, msg.text);
            if (adapter.assistantPlacement(msg) === "work") appendWork(container, bubble);
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

export async function refreshLiveHistory() {
    if (!state.currentSession || state.currentSession.type !== "live") return;
    if (!platform.isNative && document.hidden) return;

    const session = state.currentSession;
    if (!session.session_id) return;

    const container = document.getElementById("live-history-messages");
    if (!container) return;

    // An agent seen in the last few minutes: its kept page shows at once, and only what came after is asked for
    // below (a poll from the page's end), not the first page again (#3726).
    if (!initialLoadDone) {
        const kept = freshKeptPage(session.session_id);
        if (kept) {
            container.innerHTML = "";
            for (const msg of kept.messages) renderMessage(msg, container, session.agent_type);
            historyHasMore = kept.start > 0;
            historyStart = kept.start;
            historyMessageCount = kept.total;
            _updateLoadMoreButton(container);
            initialLoadDone = true;
            if (state.autoScroll) container.scrollTop = container.scrollHeight;
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
            // Preserve scroll position: measure before inserting
            const prevScrollHeight = container.scrollHeight;
            const prevScrollTop = container.scrollTop;

            // Find the insertion point (after the Load More button)
            const loadMoreBtn = container.querySelector(".load-more-btn");
            const refNode = loadMoreBtn ? loadMoreBtn.nextSibling : container.firstChild;

            const temp = document.createElement("div");
            noteIndexes(data.messages, data.start);
            for (const msg of data.messages) {
                renderMessage(msg, temp, session.agent_type);
            }
            const lastOlder = temp.lastElementChild;
            while (temp.firstChild) {
                container.insertBefore(temp.firstChild, refNode);
            }
            stitchPages(lastOlder);

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
        return localStorage.getItem(VIEW_MODE_KEY) === "terminal" ? "terminal" : "chat";
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

function autosizeCommandInput() {
    const input = document.getElementById("command-input");
    if (!input) return;
    const before = input.offsetHeight;
    if (!desktopComposer.matches || !input.offsetParent) {
        input.style.height = "";
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
}

export function setLiveViewMode(mode) {
    try {
        localStorage.setItem(VIEW_MODE_KEY, mode === "chat" ? "chat" : "terminal");
    } catch { /* storage unavailable — mode lasts for this page only */ }
    applyLiveViewMode();
    if (mode === "chat") {
        const container = document.getElementById("live-history-messages");
        if (container) container.scrollTop = container.scrollHeight;
        state.autoScroll = true;
    }
}

// ── Pending (sent, not yet in the transcript) messages ───────────────────
// A message typed into the command box only reaches the transcript once the
// agent takes it, which can be a whole turn later. Until then it is shown at
// the bottom of the chat so it does not seem to vanish: "Sent" when the agent
// was free and is taking it now, "Queued" when it was busy and the message
// waits behind the current work. The Working row sits between the two.

const PENDING_TTL_MS = 15 * 60 * 1000;
const STORAGE_KEY_PENDING = "coral-pending-messages";
const pendingBySession = new Map(); // session_id -> [{ id, text, at, queued }]
let pendingSeq = 0;
const recentUserEntriesBySession = new Map();

// Loaded by app.js once the store knows whose they are (#3681).
export function loadPendingFromStorage() {
    pendingBySession.clear();
    try {
        const raw = localStorage.getItem(storeKey(STORAGE_KEY_PENDING));
        if (!raw) return;
        const obj = JSON.parse(raw);
        const now = Date.now();
        for (const [sid, list] of Object.entries(obj)) {
            if (Array.isArray(list)) {
                const valid = list.filter(p => p && p.text && (now - (p.at || 0) < PENDING_TTL_MS));
                if (valid.length > 0) {
                    pendingBySession.set(sid, valid);
                    for (const p of valid) {
                        if (p.id > pendingSeq) pendingSeq = p.id;
                    }
                }
            }
        }
    } catch (e) {
        console.warn("[coral] Failed to load pending messages from storage", e);
    }
}

function savePendingToStorage() {
    try {
        const obj = {};
        const now = Date.now();
        for (const [sid, list] of pendingBySession.entries()) {
            const valid = (list || []).filter(p => now - (p.at || 0) < PENDING_TTL_MS);
            if (valid.length > 0) {
                obj[sid] = valid;
            }
        }
        localStorage.setItem(storeKey(STORAGE_KEY_PENDING), JSON.stringify(obj));
    } catch (e) {
        console.warn("[coral] Failed to save pending messages to storage", e);
    }
}


const normalizeMsg = (t) => String(t || "").replace(/\s+/g, " ").trim();

// Codex can record attached images separately or wrap their paths in image
// metadata. Match the accompanying text, not the transport representation.
function pendingMatchText(text) {
    const normalized = normalizeMsg(String(text || "")
        .replace(/<image\b[^>]*\bpath="([^"]+)"[^>]*>[\s\S]*?<\/image>/g, '$1')
        .replace(/\[Image #\d+\]/g, ''));
    const body = normalized.replace(/^(?:(?:\/(?:[^\/]+\/)*uploads\/[^\s]+|\/\S*\/uploads\/\S+)\s+)+/, '').trim();
    return body || normalized;
}

function rememberUserMessage(sessionId, msg) {
    const entries = recentUserEntriesBySession.get(sessionId) || [];
    const entry = { ...msg, receivedAt: Date.now(), remaining: pendingMatchText(msg.content) };
    entries.push(entry);
    recentUserEntriesBySession.set(sessionId, entries.slice(-200));
    settlePending(sessionId, entry);
    dropStaleSentPending(sessionId, msg);
}


// Claude Code records an Esc interrupt as a user entry like this
const INTERRUPT_RE = /^\[Request interrupted by user[^\]]*\]$/;

// Messages Coral types into the agent's terminal: board and task nudges
// (internal/background/notifier.go, routes/board.go, routes/agent_tasks.go).
// The transcript records them as if the user sent them.
// Not anchored at the end: two nudges typed at the same moment arrive as one
// message ("...to start.You have 1 unread message...").
const CORAL_NUDGE_RES = [
    /^You have \d+ unread messages? on the message board/i,
    /^You have tasks available/i,
    /^You have a new task in Coral \(#\d+:/i,
    /^\[Task #\d+[^\]]*\]/i,
    /^\[Wait resolved\]/i,
	/^\[Coral\b[^\]]*\]/i,
];

const CORAL_HEALTH_NUDGE_RE = /^\[Coral health check\]/i;

// Back-to-back notices (nudges that queued up while the agent was busy)
// roll up into one showing the latest text and how many there were.
function coralNoticeSummary(text) {
    return /^\[Coral UI panel request\b/i.test(text) ? text.split(/\r?\n/, 1)[0] : text;
}
function appendCoralNotice(container, text, health = false) {
    text = coralNoticeSummary(text);
	const prev = lastContent(container);
	const noticeClass = health ? "chat-system-health" : "chat-system-general";
	if (prev && prev.classList.contains("chat-system") && prev.classList.contains(noticeClass)) {
        const count = (Number(prev.dataset.count) || 1) + 1;
        prev.dataset.count = String(count);
        const textEl = prev.querySelector(".chat-system-text");
        textEl.textContent = text;
        textEl.title = text;
        let badge = prev.querySelector(".chat-system-count");
        if (!badge) {
            badge = document.createElement("span");
            badge.className = "chat-system-count";
            prev.appendChild(badge);
        }
        badge.textContent = `×${count}`;
        badge.title = `${count} notices from Coral in a row`;
        return;
    }
	appendContent(container, makeBubble(`chat-system ${noticeClass}`,
		`<span class="material-icons" aria-hidden="true">${health ? "health_and_safety" : "notifications"}</span><span class="chat-system-label">${health ? "Coral · Health check" : "Coral"}</span><span class="chat-system-text" title="${escapeHtml(text)}">${escapeHtml(text)}</span>`));
}

function normalizeNudgeText(content) {
    let text = String(content || "").trim();
    // In CLI agents (e.g. Antigravity CLI in plan/goal mode), the agent prefixes
    // terminal input with one or more slash commands (e.g. "/plan ").
	text = text.replace(/^(?:\/[a-zA-Z0-9_-]+\s*)+/, "").trim();
	text = text.replace(/^\[Coral health check\]\s*/i, "").trim();
    // If preceded by a notification tag like "@QA Engineer " or "@notify-all "
    return text.replace(/^@[^@\n\r]+?\s+(?=(?:You have|\[Task #|\[Wait resolved\]|\[Coral\b))/i, "").trim();
}

function isCoralHealthNudge(content) {
	const text = String(content || "").trim().replace(/^(?:\/[a-zA-Z0-9_-]+\s*)+/, "").trim();
	return CORAL_HEALTH_NUDGE_RE.test(text);
}

function isCoralNudge(content) {
	const raw = String(content || "").trim().replace(/^(?:\/[a-zA-Z0-9_-]+\s*)+/, "").trim();
	if (CORAL_HEALTH_NUDGE_RE.test(raw)) return true;
	const text = normalizeNudgeText(content);
	return CORAL_NUDGE_RES.some(re => re.test(text));
}

/** Record a message just sent to an agent so the chat can show it right away. */
export function addPendingMessage(sessionId, text, sentAt = Date.now()) {
    if (!sessionId || !normalizeMsg(text)) return;
    // Panel retries carry one stable identity even when the first delivery's
    // transcript arrived before the retry began. Never add a second notice.
    const panelMarker = /^\[Coral UI panel request [^\]]+\]/.exec(text)?.[0];
    if (panelMarker && ((pendingBySession.get(sessionId) || []).some(p => p.text.includes(panelMarker))
        || (recentUserEntriesBySession.get(sessionId) || []).some(entry => String(entry.content || '').includes(panelMarker)))) return;
    const session = (state.liveSessions || []).find(s => s.session_id === sessionId)
        || (state.currentSession && state.currentSession.session_id === sessionId ? state.currentSession : { session_id: sessionId });
    const queued = agentIsBusy(session);
    const list = pendingBySession.get(sessionId) || [];
    list.push({ id: ++pendingSeq, text, at: sentAt, queued });
    pendingBySession.set(sessionId, list);
    // A fast transcript poll may beat the send response. Reconcile against
    // unconsumed recent receipts instead of waiting for another transcript entry.
    for (const entry of recentUserEntriesBySession.get(sessionId) || []) {
        if (entry.receivedAt >= sentAt && !Number.isNaN(Date.parse(entry.timestamp || ""))) settlePending(sessionId, entry);
    }
    savePendingToStorage();
    if (!queued) lastSendAt.set(sessionId, Date.now());
    const container = document.getElementById("live-history-messages");
    if (container && state.currentSession && state.currentSession.session_id === sessionId) {
        syncPendingBubbles(container, sessionId);
        syncWorkingIndicator(container, state.currentSession);
        container.scrollTop = container.scrollHeight;
    }
}

/** Withdraw the pending bubble of a send that was refused (its terminal was gone, #3218). */
export function removePendingMessage(sessionId, sentAt) {
    const list = pendingBySession.get(sessionId);
    const i = list ? list.findIndex(p => p.at === sentAt) : -1;
    if (i < 0) return;
    list.splice(i, 1);
    lastSendAt.delete(sessionId);
    savePendingToStorage();
    const container = document.getElementById("live-history-messages");
    if (container && state.currentSession && state.currentSession.session_id === sessionId) {
        syncPendingBubbles(container, sessionId);
        syncWorkingIndicator(container, state.currentSession);
    }
}

// An interrupt ends the current work; Claude Code then submits whatever was
// queued, so those messages are now being worked on.
function noteInterrupt(sessionId, msg) {
    const list = pendingBySession.get(sessionId);
    const ts = Date.parse(msg.timestamp || "");
    let released = false;
    for (const p of list || []) {
        if (p.queued && (Number.isNaN(ts) || p.at <= ts + 5000)) {
            p.queued = false;
            released = true;
        }
    }
    if (released) {
        lastSendAt.set(sessionId, Date.now());
        savePendingToStorage();
    } else {
        lastSendAt.delete(sessionId);
    }
}

// Drop the pending messages this transcript entry accounts for. Messages
// queued while the agent was busy can arrive merged into one entry (e.g.
// after an interrupt), so every pending message it contains is settled, each
// occurrence consuming one. Only entries written after the send can match,
// so an earlier identical message ("continue") does not clear a new one.
function settlePending(sessionId, msg) {
    const list = pendingBySession.get(sessionId);
    if (!list || !list.length) return;
    let rest = msg.remaining;
    const ts = Date.parse(msg.timestamp || "");
    if (!rest) return;
    const whole = rest;
    const eligible = (p) => Number.isNaN(ts) || ts >= p.at - 10000;
    let settled = 0;
    let lastSettledId = 0;
    for (let i = 0; i < list.length;) {
        const p = list[i];
        const want = pendingMatchText(p.text);
        const at = eligible(p) && want ? rest.indexOf(want) : -1;
        if (at !== -1) {
            lastSettledId = Math.max(lastSettledId, p.id);
            rest = rest.slice(0, at) + rest.slice(at + want.length);
            list.splice(i, 1);
            settled++;
        } else {
            i++;
        }
    }
    // The transcript may hold a shortened form of a long message
    if (!settled) {
        const i = list.findIndex(p => eligible(p) && pendingMatchText(p.text).includes(whole));
        if (i !== -1) {
            lastSettledId = Math.max(lastSettledId, list[i].id);
            list.splice(i, 1);
            settled++;
            rest = "";
        }
    }
    msg.remaining = rest;
    // The agent took one of our messages: it is working on it from then on
    if (settled) {
        dropEarlierPending(list, lastSettledId);
        lastSendAt.set(sessionId, Number.isNaN(ts) ? Date.now() : ts);
        savePendingToStorage();
    }
}

// Messages reach the agent in the order they were sent, so once a later one
// shows up in the transcript every earlier pending message was taken too, even
// when its text did not match (reworded, merged, image paths). Without this
// such a bubble would sit at the bottom of the chat until it expired.
function dropEarlierPending(list, settledId) {
    for (let i = list.length - 1; i >= 0; i--) {
        if (list[i].id < settledId) list.splice(i, 1);
    }
}

// A "Sent" message (the agent was free) is picked up within moments. When the
// transcript has moved on well past it, it was taken even if never matched.
// Queued messages are exempt: they legitimately wait behind a long turn.
const SENT_STALE_MS = 60 * 1000;
function dropStaleSentPending(sessionId, msg) {
    const list = pendingBySession.get(sessionId);
    if (!list || !list.length) return;
    const ts = Date.parse(msg.timestamp || "");
    if (Number.isNaN(ts)) return;
    const before = list.length;
    for (let i = list.length - 1; i >= 0; i--) {
        if (!list[i].queued && ts > list[i].at + SENT_STALE_MS) list.splice(i, 1);
    }
    if (list.length !== before) savePendingToStorage();
}

function syncPendingGroup(container, cls, items, label) {
    let wrap = container.querySelector(`:scope > .pending-messages.${cls}`);
    if (!items.length) {
        if (wrap) wrap.remove();
        return null;
    }
    if (!wrap) {
        wrap = document.createElement("div");
        wrap.className = `pending-messages ${cls}`;
    }
    const want = new Set(items.map(p => p.id));
    for (const el of Array.from(wrap.children)) {
        if (!want.has(Number(el.dataset.pendingId))) el.remove();
    }
    const have = new Set(Array.from(wrap.children).map(el => Number(el.dataset.pendingId)));
    for (const p of items) {
        if (have.has(p.id)) continue;
        const coral = /^\[Coral UI panel request\b/i.test(normalizeNudgeText(p.text));
        const summary = coralNoticeSummary(normalizeNudgeText(p.text));
        const el = coral
            ? makeBubble("chat-system chat-system-general pending", `<span class="material-icons" aria-hidden="true">notifications</span><span class="chat-system-label">Coral</span><span class="chat-system-text" title="${escapeHtml(summary)}">${escapeHtml(summary)}</span><span class="pending-label" role="status">${label}</span>`)
            : makeBubble("chat-bubble human pending", `<div class="message-text">${renderMarkdown(p.text, undefined, 'chat')}</div><div class="pending-label" role="status">${label}</div>`);
        el.dataset.pendingId = String(p.id);
        // Keep send order even when a message moves between groups
        const next = Array.from(wrap.children).find(c => Number(c.dataset.pendingId) > p.id);
        wrap.insertBefore(el, next || null);
    }
    return wrap;
}

function syncPendingBubbles(container, sessionId) {
    const now = Date.now();
    const list = (pendingBySession.get(sessionId) || []).filter(p => now - p.at < PENDING_TTL_MS);
    pendingBySession.set(sessionId, list);
    savePendingToStorage();
    const sent = syncPendingGroup(container, "pending-sent", list.filter(p => !p.queued), "Sent");
    const queued = syncPendingGroup(container, "pending-queued", list.filter(p => p.queued), "Queued");
    // Always last, below any newly rendered messages: sent, then queued
    for (const wrap of [sent, queued]) {
        if (wrap && container.lastElementChild !== wrap) container.appendChild(wrap);
    }
}

// ── Working indicator ────────────────────────────────────────────────────
// Between sending a message and the first tool call or reply there can be a
// long silence. Show a "Working" row at the bottom of the chat while the
// agent's state is Working, and right after a send until the agent's first
// output lands (its state can take a moment to flip).

const SEND_GRACE_MS = 90 * 1000;
const lastSendAt = new Map(); // session_id -> ms of the last send with no agent output since

function noteAgentActivity(sessionId, msg) {
    dropStaleSentPending(sessionId, msg);
    const sent = lastSendAt.get(sessionId);
    if (!sent) return;
    const ts = Date.parse(msg.timestamp || "");
    if (Number.isNaN(ts) || ts >= sent - 2000) lastSendAt.delete(sessionId);
}

/** Show the terminal for this agent without changing the saved Chat/Terminal choice. */
export function showTerminalView() {
    applyLiveViewMode("terminal");
}

function agentIsWorking(session) {
    const row = (state.liveSessions || []).find(s => s.session_id === session.session_id) || session;
    const key = deriveSessionState(row);
    if (key === "working") return true;
    // Awaiting the user, stuck, asleep or ended: nothing is in flight
    if (key !== "idle" && key !== "your_turn" && key !== "waiting_on") return false;
    const sent = lastSendAt.get(session.session_id);
    return !!sent && Date.now() - sent < SEND_GRACE_MS;
}

// Busy for queueing purposes: working, or still on a message just sent
const agentIsBusy = agentIsWorking;

function syncWorkingIndicator(container, session) {
    let el = container.querySelector(":scope > .chat-working");
    // A card asking for input replaces the Working row
    if (!agentIsWorking(session) || container.querySelector(":scope > .chat-needs-input")) {
        if (el) el.remove();
        return;
    }
    if (!el) {
        el = document.createElement("div");
        el.className = "chat-working";
        el.setAttribute("role", "status");
        // Just the state: the latest step is already the group summary above
        el.innerHTML = `<span class="chat-working-dots" aria-hidden="true"><i></i><i></i><i></i></span><span class="chat-working-label">Working</span>`;
    }
    // Below what the agent is working on (including Sent messages), above
    // anything Queued behind it
    const queued = container.querySelector(":scope > .pending-messages.pending-queued");
    if (queued) {
        if (el.nextElementSibling !== queued) container.insertBefore(el, queued);
    } else if (container.lastElementChild !== el) {
        container.appendChild(el);
    }
}

// ── Needs input ──────────────────────────────────────────────────────────
// Claude Code writes a tool call to the transcript only once it completes, so
// an open permission prompt, AskUserQuestion or plan approval never shows in
// the chat on its own. The PreToolUse hook reports the call as it starts
// (it arrives with the chat poll's reply as `pending`); the Notification hook sets waiting_for_input. Together
// they drive a card at the bottom of the chat saying what is being asked.

const pendingToolBySession = new Map(); // session_id -> pending tool or null
// The prompt's options as the terminal shows them right now (numbered, with
// the digit that answers each), read by the server from a pane capture.
const promptOptionsBySession = new Map(); // session_id -> { question, options: [{ n, label, action }], review }

// Tools whose whole purpose is to wait for the user
const BLOCKING_TOOLS = {
    AskUserQuestion: "question",
    ask_question: "question",
    ExitPlanMode: "plan",
};

// Codex has no Notification hook, so nothing reports its sign-in and update
// menus or its approval prompts. For Codex the screen is read on every poll
// and an open numbered prompt raises the card by itself. Antigravity/Agy and
// Gemini also display interactive numbered prompts directly on screen.
function readsPromptsFromScreen(session) {
    return !!session && (
        session.agent_type === "codex" ||
        session.agent_type === "agy" ||
        session.agent_type === "antigravity" ||
        session.agent_type === "gemini"
    );
}

function needsInputInfo(session) {
    const row = (state.liveSessions || []).find(s => s.session_id === session.session_id) || session;
    const key = deriveSessionState(row);
    if (key === "ended" || key === "sleeping") return null;
    const pt = pendingToolBySession.get(session.session_id) || null;
    const screen = promptOptionsBySession.get(session.session_id) || null;
    if (pt && BLOCKING_TOOLS[pt.tool_name]) return { kind: BLOCKING_TOOLS[pt.tool_name], tool: pt, screen };
    if (row.waiting_for_input) return { kind: "permission", tool: pt, screen, summary: String(row.waiting_summary || "").replace(/^Notification:\s*/, "") };
    if (readsPromptsFromScreen(session) && screen && screen.options.length) return { kind: pt ? "permission" : "prompt", tool: pt, screen };
    if (key === "check_terminal") return { kind: "startup" };
    return null;
}

// ── Needs-input card markup ──────────────────────────────────────────────
// The question leads, large; options are compact chips (or small cards when
// they carry descriptions), numbered like the terminal's keys, with the
// terminal's default outlined. A text option becomes an inline field, "Chat
// about this" a quiet link, and the review step a summary with Submit as the
// primary action. Open terminal is a footer link.

const KICKERS = {
    question: "Claude is asking",
    plan: "Plan ready for your review",
    permission: "Permission needed",
    startup: "Waiting in the terminal",
    prompt: "Needs your input",
};

function agentDisplayName(session) {
    const at = (session && session.agent_type) || "claude";
    if (at === "agy" || at === "antigravity" || at === "gemini") return "Antigravity";
    if (at === "codex") return "Codex";
    if (at === "pi") return "Pi";
    return "Claude";
}

// data-label is the on-screen label (what the server checks before
// answering); the button shows the hook's full label when the screen's was
// cut off by wrapping.
function optionButtonHtml(o, desc, fullLabel) {
    return `<button type="button" class="cni-answer${o.selected ? " is-default" : ""}" data-n="${Number(o.n)}" data-label="${escapeHtml(o.label)}" data-action="select">`
        + `<kbd class="cni-key">${o.n}</kbd>`
        + `<span class="cni-answer-text"><span class="cni-answer-label">${escapeHtml(fullLabel || o.label)}</span>`
        + (desc ? `<span class="cni-answer-desc">${escapeHtml(desc)}</span>` : "")
        + `</span></button>`;
}

function answerControlsHtml(info, descriptions = {}, fullLabels = {}) {
    const opts = (info.screen && info.screen.options) || [];
    if (!opts.length) return { body: "", foot: "" };
    const review = ((info.screen && info.screen.review) || []).length > 0;
    const selects = opts.filter(o => (o.action || "select") === "select");
    const texts = opts.filter(o => o.action === "text");
    const chats = opts.filter(o => o.action === "chat");
    let body = "";
    let foot = "";
    if (review) {
        // Submit answers / Cancel: footer actions, Submit primary
        foot += selects.map((o, i) => `<button type="button" class="cni-answer btn btn-sm ${i === 0 ? "btn-primary" : ""}" data-n="${Number(o.n)}" data-label="${escapeHtml(o.label)}" data-action="select">${escapeHtml(o.label)}</button>`).join("");
    } else if (selects.length) {
        const withDesc = selects.some(o => descriptions[fullLabels[o.label] || o.label] || descriptions[o.label]);
        body += `<div class="cni-options ${withDesc ? "cni-options-cards" : "cni-options-chips"}">`
            + selects.map(o => optionButtonHtml(o, descriptions[fullLabels[o.label] || o.label] || descriptions[o.label], fullLabels[o.label])).join("") + `</div>`;
    }
    for (const o of texts) {
        const placeholder = /^type something/i.test(o.label) ? "Or type your own answer…" : `${o.label.replace(/\.$/, "")}…`;
        body += `<form class="cni-text-form" data-n="${Number(o.n)}" data-label="${escapeHtml(o.label)}" data-action="text">`
            + `<input type="text" class="cni-text-input" placeholder="${escapeHtml(placeholder)}" aria-label="${escapeHtml(o.label)}">`
            + `<button type="submit" class="btn btn-sm">Send</button></form>`;
    }
    for (const o of chats) {
        foot = `<button type="button" class="cni-link cni-answer" data-n="${Number(o.n)}" data-label="${escapeHtml(o.label)}" data-action="chat">${escapeHtml(o.label)} instead</button>` + foot;
    }
    return { body, foot };
}

function reviewHtml(info) {
    const items = (info.screen && info.screen.review) || [];
    if (!items.length) return "";
    return `<dl class="cni-review">` + items.map(it =>
        `<div class="cni-review-row"><dt>${escapeHtml(it.question)}</dt><dd>${escapeHtml(it.answer || "—")}</dd></div>`).join("") + `</dl>`;
}

function needsInputHtml(info, session) {
    const inp = (info.tool && info.tool.input) || {};
    const screen = info.screen || {};
    const onScreen = (screen.options || []).length > 0;
    const screenQuestion = onScreen ? (screen.question || "") : "";
    const name = agentDisplayName(session);
    let kicker = (info.kind === "question") ? `${name} is asking` : (KICKERS[info.kind] || "Needs your input");
    let question = "";
    let context = "";
    let descriptions = {};

    let fullLabels = {};
    if (info.kind === "question") {
        const qs = Array.isArray(inp.questions) ? inp.questions : [];
        for (const q of qs) {
            for (const o of Array.isArray(q.options) ? q.options : []) {
                const label = typeof o === "string" ? o : (o && o.label);
                const desc = typeof o === "object" && o ? o.description : "";
                if (label && desc) descriptions[label] = desc;
            }
        }
        // The screen can cut a long question or label short (wrapping); use
        // the hook's full text for whichever one matches what is on screen.
        const norm = (t) => String(t || "").replace(/\s+/g, " ").trim().toLowerCase();
        const onScreenQ = norm(screenQuestion);
        const match = onScreenQ ? qs.find(q => { const h = norm(q.question); return h && (h === onScreenQ || h.endsWith(onScreenQ) || onScreenQ.endsWith(h) || h.includes(onScreenQ)); }) : null;
        question = (match && match.question) || screenQuestion || (qs[0] && qs[0].question) || "";
        const hookLabels = (match ? match.options : qs.flatMap(q => q.options || [])) || [];
        for (const o of (screen.options || [])) {
            const full = hookLabels.find(h => {
                const hLabel = typeof h === "string" ? h : (h && h.label);
                const a = norm(hLabel), b = norm(o.label);
                return a && b && a !== b && a.startsWith(b);
            });
            if (full) fullLabels[o.label] = typeof full === "string" ? full : full.label;
        }
        if (!onScreen && qs.length) {
            // No live options (yet): list the questions from the hook
            context = qs.map(q => `<div class="cni-static-q">${escapeHtml(q.question || "")}`
                + (Array.isArray(q.options) && q.options.length ? `<ul>${q.options.map(o => `<li>${escapeHtml(typeof o === "string" ? o : (o && o.label) || "")}</li>`).join("")}</ul>` : "") + `</div>`).slice(1).join("");
        }
    } else if (info.kind === "plan") {
        question = screenQuestion ? "Ready to proceed with this plan?" : `${name} has a plan ready`;
        if (inp.plan) context = `<div class="cni-plan message-text">${renderMarkdown(inp.plan, undefined, 'chat')}</div>`;
    } else if (info.kind === "permission") {
        const tool = info.tool && info.tool.tool_name;
        if (tool) kicker = `Permission needed · ${escapeHtml(tool)}`;
        question = screenQuestion || (tool ? `Allow ${tool}?` : (info.summary || "The agent is waiting for you"));
        const target = inp.command || inp.file_path || inp.path || inp.url || inp.query || inp.pattern || "";
        if (inp.description && inp.command) context += `<div class="cni-context-text">${escapeHtml(inp.description)}</div>`;
        if (target) context += `<pre class="cni-target"><code>${escapeHtml(target)}</code></pre>`;
    } else if (info.kind === "prompt") {
        question = screenQuestion || "The agent is waiting for you";
    } else {
        question = "The agent may be waiting at a startup prompt";
        context = `<div class="cni-context-text">For example, trusting this folder or logging in. Answer it in the terminal.</div>`;
    }

    const controls = answerControlsHtml(info, descriptions, fullLabels);
    const hint = !onScreen && info.kind !== "startup" ? `<span class="cni-hint">Answer in the terminal</span>` : "";
    return `<div class="cni-head"><span class="cni-dot" aria-hidden="true"></span><span class="cni-kicker">${kicker}</span></div>`
        + (question ? `<div class="cni-question">${escapeHtml(question)}</div>` : "")
        + context
        + reviewHtml(info)
        + controls.body
        + `<div class="cni-foot"><button type="button" class="cni-link" data-on-click="terminal-view-open">Open terminal</button>${hint}<span class="cni-foot-spacer"></span>${controls.foot}</div>`;
}

// The prompt cards Coral itself made (#3622 F1). Only a control inside one of these, and outside
// any agent-written part of it (a plan is the agent's Markdown), can answer a prompt: rendered
// content can never answer for the Operator, whatever it looks like.
const ownPromptCards = new WeakSet();

/** The Coral prompt card that owns this answer control, or null when the control isn't Coral's. */
function ownPromptCard(control) {
    const card = control.closest(".chat-needs-input");
    if (!card || !ownPromptCards.has(card) || card.parentElement?.id !== "live-history-messages") return null;
    if (control.closest(".message-text, .cni-plan")) return null;
    return card;
}

function syncNeedsInputCard(container, session) {
    const info = needsInputInfo(session);
    let el = container.querySelector(":scope > .chat-needs-input");
    if (!info) {
        if (el) el.remove();
        return;
    }
    const key = JSON.stringify(info);
    if (!el) {
        el = document.createElement("div");
        el.className = "chat-needs-input";
        el.setAttribute("role", "alert");
        ownPromptCards.add(el);
    }
    if (el.dataset.key !== key) {
        // Keep an answer being typed if the card re-renders
        const open = Array.from(el.querySelectorAll(".cni-text-form")).find(f => f.querySelector("input").value || document.activeElement === f.querySelector("input"));
        const typing = open ? { n: open.dataset.n, value: open.querySelector("input").value, focused: document.activeElement === open.querySelector("input") } : null;
        el.dataset.key = key;
        el.innerHTML = needsInputHtml(info, session);
        const form = typing && el.querySelector(`.cni-text-form[data-n="${typing.n}"]`);
        if (form) {
            const input = form.querySelector("input");
            input.value = typing.value;
            if (typing.focused) input.focus();
        }
    }
    // Same slot as the Working row: below Sent messages, above Queued ones
    const queued = container.querySelector(":scope > .pending-messages.pending-queued");
    if (queued) {
        if (el.nextElementSibling !== queued) container.insertBefore(el, queued);
    } else if (container.lastElementChild !== el) {
        container.appendChild(el);
    }
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
