/* Live history view — renders JSONL messages as a read-only conversation log */

import { state } from './state.js';
import { escapeHtml, renderMarkdown, labelCodeBlocks, showToast } from './utils.js';
import { platform } from './platform/detect.js';
import { fitTerminal } from './xterm_renderer.js';
import { deriveSessionState } from './render.js';

let historyPollInterval = null;
let historyMessageCount = 0;
let historyOffset = 0;      // pagination offset for "Load More"
let historyHasMore = false;  // whether older messages exist
let initialLoadDone = false; // whether the initial full load has completed
let loadingMore = false;     // an older page is being fetched
// Messages per page. Tool calls and results each count as one, and they are
// folded away in the chat, so a page needs to be large to cover a few turns.
const HISTORY_PAGE = 400;
// Bumped on reset so responses for a previous session are dropped, and used to
// keep a single refresh in flight: overlapping initial loads would otherwise
// both render the full history.
let historyGeneration = 0;
let refreshInFlight = -1;


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

function renderToolCard(tool) {
    const toolUseId = tool.tool_use_id || "";
    let bodyHtml = "";

    if (tool.command) {
        bodyHtml += `<pre class="tool-card-command">${escapeHtml(tool.command)}</pre>`;
    } else if (tool.input_summary && tool.input_summary !== toolLabel(tool)) {
        bodyHtml += `<div class="tool-card-summary">${escapeHtml(tool.input_summary)}</div>`;
    }
    if (tool.patch) {
        bodyHtml += `<div class="diff-content">${renderPatchLines(tool.patch)}</div>`;
    }

    if (tool.name === "Edit" && (tool.old_string || tool.new_string)) {
        bodyHtml += `<div class="diff-content">${renderDiffLines(tool.old_string || "", tool.new_string || "")}</div>`;
    }
    if (tool.name === "Write" && tool.write_content) {
        bodyHtml += `<pre class="tool-result-content">${escapeHtml(tool.write_content)}</pre>`;
    }

    const label = toolLabel(tool);
    return `<details class="tool-call" data-tool-use-id="${escapeHtml(toolUseId)}">
        <summary class="tool-call-summary">
            <span class="tool-call-icon">${getToolIcon(tool.name)}</span>
            <span class="tool-call-name">${escapeHtml(tool.name)}</span>
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
                    bodyHtml += `<div class="tool-question-option">`;
                    bodyHtml += `<span class="tool-question-option-label">${escapeHtml(opt.label)}</span>`;
                    if (opt.description) {
                        bodyHtml += `<span class="tool-question-option-desc">${escapeHtml(opt.description)}</span>`;
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

function makeBubble(className, html) {
    const div = document.createElement("div");
    div.className = className;
    div.innerHTML = html;
    labelCodeBlocks(div);
    linkifyRefs(div);
    return div;
}

// A path-looking inline code span: has a directory, a :line suffix, or a
// common source extension. Keeps `window.fetch` or `v1.2.3` from matching.
const FILE_REF_RE = /^(?:~?\.{0,2}\/)?(?:[\w@.+-]+\/)*[\w@.+-]+\.[A-Za-z][\w]{0,7}(?::\d+(?:[-:]\d+)?)?$/;
const FILE_REF_EXTS = new Set(["md", "js", "mjs", "cjs", "ts", "tsx", "jsx", "go", "py", "rb", "rs", "java", "kt", "swift", "c", "h", "cc", "cpp", "hpp", "cs", "php", "sh", "zsh", "bash", "json", "yaml", "yml", "toml", "ini", "css", "scss", "html", "sql", "txt", "mod", "sum", "lock", "xml", "svg", "vue", "svelte"]);

function looksLikeFileRef(text) {
    if (!FILE_REF_RE.test(text) || /^https?:/i.test(text)) return false;
    const path = text.replace(/:\d+(?:[-:]\d+)?$/, "");
    const ext = path.slice(path.lastIndexOf(".") + 1).toLowerCase();
    return path.includes("/") || path !== text || FILE_REF_EXTS.has(ext);
}

// Web links open in a new tab; links to relative paths and path-looking
// inline code become file references (opened in the Files preview on click
// in the live Chat view).
function linkifyRefs(root) {
    for (const a of root.querySelectorAll("a[href]")) {
        const href = a.getAttribute("href");
        if (/^(https?:|mailto:)/i.test(href)) {
            a.target = "_blank";
            a.rel = "noopener noreferrer";
        } else if (!href.startsWith("#") && looksLikeFileRef(decodeURIComponent(href))) {
            a.dataset.fileRef = decodeURIComponent(href);
            a.classList.add("chat-file-ref");
        }
    }
    for (const code of root.querySelectorAll(":not(pre) > code")) {
        if (code.closest("a")) continue;
        const text = code.textContent.trim();
        if (looksLikeFileRef(text)) {
            code.dataset.fileRef = text;
            code.classList.add("chat-file-ref");
        }
    }
}

async function openFileRef(ref) {
    const s = state.currentSession;
    if (!s || s.type !== "live") return;
    try {
        const qs = new URLSearchParams({ filepath: ref, session_id: s.session_id || "" });
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(s.name)}/resolve-path?${qs}`);
        if (!resp.ok) {
            showToast(`File not found: ${ref}`, true);
            return;
        }
        const data = await resp.json();
        if (window.openFilePreview) window.openFilePreview(data.filepath);
    } catch {
        showToast(`Could not open ${ref}`, true);
    }
}

// File references are only actionable in the live Chat view (it has an agent
// and a Files panel to open them in).
document.addEventListener("click", (e) => {
    const el = e.target.closest && e.target.closest("#live-history-messages [data-file-ref]");
    if (!el) return;
    e.preventDefault();
    openFileRef(el.dataset.fileRef);
});

// Relative links in the history view have nowhere to go; keep them from
// navigating the dashboard away.
document.addEventListener("click", (e) => {
    const el = e.target.closest && e.target.closest("#history-messages a[data-file-ref]");
    if (el) e.preventDefault();
});

function htmlToElement(html) {
    const t = document.createElement("template");
    t.innerHTML = html.trim();
    return t.content.firstElementChild;
}

// Everything between a user message and the agent's final reply is folded
// into one collapsed "work group". The latest reply is shown as final until
// more work arrives in the same turn, at which point it is folded back in.
function createWorkGroup() {
    return htmlToElement(`<details class="work-group">
        <summary class="work-group-summary"><span class="work-group-count"></span><span class="work-group-latest"></span></summary>
        <div class="work-group-body"></div>
    </details>`);
}

function updateWorkGroupSummary(group) {
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
const TRAILING_CHROME = ".chat-working, .chat-needs-input, .chat-empty, .pending-messages";

function lastContent(container) {
    let el = container.lastElementChild;
    while (el && el.matches(TRAILING_CHROME)) el = el.previousElementSibling;
    return el;
}

function appendContent(container, el) {
    const firstChrome = Array.from(container.children).find(c => c.matches(TRAILING_CHROME));
    container.insertBefore(el, firstChrome || null);
}

function demoteTurnFinal(container) {
    const last = lastContent(container);
    if (!last || !last.classList.contains("turn-final")) return;
    let group = last.previousElementSibling;
    if (!group || !group.classList.contains("work-group")) {
        group = createWorkGroup();
        container.insertBefore(group, last);
    }
    last.classList.remove("turn-final");
    group.querySelector(":scope > .work-group-body").appendChild(last);
    updateWorkGroupSummary(group);
}

function appendWork(container, el) {
    demoteTurnFinal(container);
    let group = lastContent(container);
    if (!group || !group.classList.contains("work-group")) {
        group = createWorkGroup();
        appendContent(container, group);
    }
    group.querySelector(":scope > .work-group-body").appendChild(el);
    updateWorkGroupSummary(group);
}

function appendTurnFinal(container, el) {
    demoteTurnFinal(container);
    el.classList.add("turn-final");
    appendContent(container, el);
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

function renderMessage(msg, container) {
    if (msg.type === "user" && INTERRUPT_RE.test(String(msg.content || "").trim())) {
        appendContent(container, makeBubble("chat-note", "Interrupted"));
    } else if (msg.type === "user") {
        appendContent(container, makeBubble("chat-bubble human",
            `<div class="role-label">You</div><div class="message-text">${renderMarkdown(msg.content)}</div>`));
    } else if (msg.type === "assistant") {
        if (msg.text) {
            appendTurnFinal(container, makeBubble("chat-bubble assistant",
                `<div class="message-text">${renderMarkdown(msg.text)}</div>`));
        }
        for (const tool of msg.tool_uses || []) {
            if (tool.name === "AskUserQuestion") {
                appendTurnFinal(container, makeBubble("chat-bubble assistant",
                    `<div class="role-label">${getToolIcon(tool.name)} Question</div>` + renderQuestionCard(tool)));
            } else {
                appendWork(container, htmlToElement(renderToolCard(tool)));
            }
        }
    } else if (msg.type === "tool_result") {
        const toolUseId = msg.tool_use_id || "";
        const target = toolUseId
            ? container.querySelector(`[data-tool-use-id="${CSS.escape(toolUseId)}"]`)
            : null;
        const outputHtml = `<pre class="tool-output-inline${msg.is_error ? " tool-result-error" : ""}">${escapeHtml(msg.content || "")}</pre>`;
        if (target && target.classList.contains("tool-call")) {
            if (msg.is_error) target.classList.add("tool-call-error");
            target.querySelector(".tool-call-body").insertAdjacentHTML("beforeend",
                `<div class="tool-card-output">${outputHtml}</div>`);
        } else if (target) {
            target.insertAdjacentHTML("beforeend", `<div class="tool-card-output">${outputHtml}</div>`);
        } else {
            // The matching tool call is on an older, not-yet-loaded page.
            const toolName = msg.tool_name || "Tool";
            appendWork(container, htmlToElement(`<details class="tool-call${msg.is_error ? " tool-call-error" : ""}">
                    <summary class="tool-call-summary">
                        <span class="tool-call-icon">${getToolIcon(toolName)}</span>
                        <span class="tool-call-name">${escapeHtml(toolName)}</span>
                        <span class="tool-call-label">output</span>
                    </summary>
                    <div class="tool-call-body"><div class="tool-card-output">${outputHtml}</div></div>
                </details>`));
        }
    }
}

/** Render a full transcript into an empty container (history Chat tab). */
export function renderTranscript(messages, container) {
    for (const msg of messages) renderMessage(msg, container);
}

export async function refreshLiveHistory() {
    if (!state.currentSession || state.currentSession.type !== "live") return;
    if (!platform.isNative && document.hidden) return;

    const session = state.currentSession;
    if (!session.session_id) return;

    const container = document.getElementById("live-history-messages");
    if (!container) return;

    // Show loading indicator on first load
    if (!initialLoadDone && !container.querySelector('.loading-indicator') && container.children.length === 0) {
        container.innerHTML = '<div class="loading-indicator">Loading chat history</div>';
    }

    const params = new URLSearchParams();
    params.set("session_id", session.session_id);
    if (session.agent_type) {
        params.set("agent_type", session.agent_type);
    }
    if (session.working_directory) {
        params.set("working_directory", session.working_directory);
    }

    if (!initialLoadDone) {
        // First load: fetch most recent messages with pagination
        params.set("after", "0");
        params.set("limit", String(HISTORY_PAGE));
        params.set("offset", "0");
    } else {
        // Subsequent polls: only fetch new messages
        params.set("after", historyMessageCount);
    }

    if (refreshInFlight === historyGeneration) return;
    const generation = historyGeneration;
    refreshInFlight = generation;
    try {
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(session.name)}/chat?${params}`);
        const data = await resp.json();
        if (generation !== historyGeneration) return;

        if (data.messages && data.messages.length > 0) {
            if (!initialLoadDone) {
                container.innerHTML = "";
                // Initial load — render all and add "Load More" button if needed
                for (const msg of data.messages) {
                    renderMessage(msg, container);
                }
                historyHasMore = data.has_more || false;
                historyOffset = data.messages.length;
                _updateLoadMoreButton(container);
                initialLoadDone = true;
            } else {
                // Poll — append new messages at the bottom
                for (const msg of data.messages) {
                    renderMessage(msg, container);
                }
            }
            for (const msg of data.messages) {
                if (msg.type === "user" && INTERRUPT_RE.test(String(msg.content || "").trim())) noteInterrupt(session.session_id, msg);
                else if (msg.type === "user") settlePending(session.session_id, msg);
                else noteAgentActivity(session.session_id, msg);
            }
            historyMessageCount = data.total;
        } else if (!initialLoadDone) {
            container.innerHTML = "";
            historyHasMore = false;
            initialLoadDone = true;
            _updateLoadMoreButton(container);
        }

        // Pending first: it settles its place at the bottom, then the working row goes above it
        syncPendingBubbles(container, session.session_id);
        syncNeedsInputCard(container, session);
        syncWorkingIndicator(container, session);
        syncEmptyState(container);
        refreshPendingTool(session);
        if (container.querySelector(":scope > .chat-needs-input")) refreshPromptOptions(session);
        else promptOptionsBySession.delete(session.session_id);
        if (state.autoScroll) {
            container.scrollTop = container.scrollHeight;
        }
    } catch (e) {
        console.error("Failed to refresh live history:", e);
    } finally {
        if (refreshInFlight === generation) refreshInFlight = -1;
    }
}

/** Load older messages (prepend above current messages). */
export async function loadMoreHistory() {
    if (!state.currentSession || !historyHasMore || loadingMore) return;
    loadingMore = true;
    const generation = historyGeneration;
    try {
        await _loadMoreHistory(generation);
    } finally {
        loadingMore = false;
    }
}

async function _loadMoreHistory(generation) {

    const session = state.currentSession;
    const container = document.getElementById("live-history-messages");
    if (!container) return;

    const params = new URLSearchParams();
    params.set("session_id", session.session_id);
    if (session.agent_type) {
        params.set("agent_type", session.agent_type);
    }
    if (session.working_directory) {
        params.set("working_directory", session.working_directory);
    }
    params.set("after", "0");
    params.set("limit", String(HISTORY_PAGE));
    params.set("offset", String(historyOffset));

    try {
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(session.name)}/chat?${params}`);
        const data = await resp.json();
        if (generation !== historyGeneration) return; // switched agents meanwhile

        if (data.messages && data.messages.length > 0) {
            // Preserve scroll position: measure before inserting
            const prevScrollHeight = container.scrollHeight;
            const prevScrollTop = container.scrollTop;

            // Find the insertion point (after the Load More button)
            const loadMoreBtn = container.querySelector(".load-more-btn");
            const refNode = loadMoreBtn ? loadMoreBtn.nextSibling : container.firstChild;

            const temp = document.createElement("div");
            for (const msg of data.messages) {
                renderMessage(msg, temp);
            }
            const lastOlder = temp.lastElementChild;
            while (temp.firstChild) {
                container.insertBefore(temp.firstChild, refNode);
            }
            stitchPages(lastOlder);

            historyOffset += data.messages.length;
            historyHasMore = data.has_more || false;

            // Restore scroll position so the view doesn't jump
            container.scrollTop = prevScrollTop + (container.scrollHeight - prevScrollHeight);
        } else {
            historyHasMore = false;
        }

        _updateLoadMoreButton(container);
    } catch (e) {
        console.error("Failed to load more history:", e);
    }
}

function _updateLoadMoreButton(container) {
    let btn = container.querySelector(".load-more-btn");
    if (historyHasMore) {
        if (!btn) {
            btn = document.createElement("button");
            btn.className = "load-more-btn";
            btn.textContent = "Load older messages";
            btn.onclick = loadMoreHistory;
            container.insertBefore(btn, container.firstChild);
        }
    } else if (btn) {
        btn.remove();
    }
}

export function startLiveHistoryPoll() {
    stopLiveHistoryPoll();
    refreshLiveHistory();
    historyPollInterval = setInterval(refreshLiveHistory, 1000);
}

export function stopLiveHistoryPoll() {
    if (historyPollInterval) {
        clearInterval(historyPollInterval);
        historyPollInterval = null;
    }
}

export function resetLiveHistory() {
    const container = document.getElementById("live-history-messages");
    if (container) container.innerHTML = "";
    historyGeneration++;
    historyMessageCount = 0;
    historyOffset = 0;
    historyHasMore = false;
    initialLoadDone = false;
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

/** Apply the persisted mode (or a one-off override) to the DOM and start/stop the transcript poll. */
export function applyLiveViewMode(override) {
    const canChat = hasTranscript(state.currentSession);
    const mode = canChat ? (override || getLiveViewMode()) : "terminal";
    const toggle = document.querySelector(".live-view-toggle");
    if (toggle) toggle.hidden = !canChat;
    const wrapper = document.getElementById("capture-wrapper");
    if (wrapper) wrapper.classList.toggle("chat-mode", mode === "chat");
    for (const m of ["terminal", "chat"]) {
        const btn = document.getElementById(`live-view-btn-${m}`);
        if (!btn) continue;
        btn.classList.toggle("active", m === mode);
        btn.setAttribute("aria-pressed", String(m === mode));
    }
    if (mode === "chat") {
        startLiveHistoryPoll();
    } else {
        stopLiveHistoryPoll();
        // The xterm canvas was hidden; refit now that it has a size again.
        setTimeout(fitTerminal, 0);
    }
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
const pendingBySession = new Map(); // session_id -> [{ id, text, at, queued }]
let pendingSeq = 0;

const normalizeMsg = (t) => String(t || "").replace(/\s+/g, " ").trim();

// Claude Code records an Esc interrupt as a user entry like this
const INTERRUPT_RE = /^\[Request interrupted by user[^\]]*\]$/;

/** Record a message just sent to an agent so the chat can show it right away. */
export function addPendingMessage(sessionId, text) {
    if (!sessionId || !normalizeMsg(text)) return;
    const session = (state.liveSessions || []).find(s => s.session_id === sessionId)
        || (state.currentSession && state.currentSession.session_id === sessionId ? state.currentSession : { session_id: sessionId });
    const queued = agentIsBusy(session);
    const list = pendingBySession.get(sessionId) || [];
    list.push({ id: ++pendingSeq, text, at: Date.now(), queued });
    pendingBySession.set(sessionId, list);
    if (!queued) lastSendAt.set(sessionId, Date.now());
    const container = document.getElementById("live-history-messages");
    if (container && state.currentSession && state.currentSession.session_id === sessionId) {
        syncPendingBubbles(container, sessionId);
        syncWorkingIndicator(container, state.currentSession);
        container.scrollTop = container.scrollHeight;
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
    if (released) lastSendAt.set(sessionId, Date.now());
    else lastSendAt.delete(sessionId);
}

// Drop the pending messages this transcript entry accounts for. Messages
// queued while the agent was busy can arrive merged into one entry (e.g.
// after an interrupt), so every pending message it contains is settled, each
// occurrence consuming one. Only entries written after the send can match,
// so an earlier identical message ("continue") does not clear a new one.
function settlePending(sessionId, msg) {
    const list = pendingBySession.get(sessionId);
    if (!list || !list.length) return;
    let rest = normalizeMsg(msg.content);
    const ts = Date.parse(msg.timestamp || "");
    if (!rest) return;
    const whole = rest;
    const eligible = (p) => Number.isNaN(ts) || ts >= p.at - 10000;
    let settled = 0;
    for (let i = 0; i < list.length;) {
        const p = list[i];
        const want = normalizeMsg(p.text);
        const at = eligible(p) && want ? rest.indexOf(want) : -1;
        if (at !== -1) {
            rest = rest.slice(0, at) + rest.slice(at + want.length);
            list.splice(i, 1);
            settled++;
        } else {
            i++;
        }
    }
    // The transcript may hold a shortened form of a long message
    if (!settled) {
        const i = list.findIndex(p => eligible(p) && normalizeMsg(p.text).includes(whole));
        if (i !== -1) {
            list.splice(i, 1);
            settled++;
        }
    }
    // The agent took one of our messages: it is working on it from then on
    if (settled) lastSendAt.set(sessionId, Number.isNaN(ts) ? Date.now() : ts);
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
        const el = makeBubble("chat-bubble human pending",
            `<div class="message-text">${renderMarkdown(p.text)}</div><div class="pending-label" role="status">${label}</div>`);
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
    if (key !== "idle" && key !== "your_turn") return false;
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

// Load older messages automatically when the reader scrolls near the top.
const AUTO_LOAD_PX = 200;
document.addEventListener("scroll", (e) => {
    const el = e.target;
    if (!el || el.id !== "live-history-messages") return;
    if (el.scrollTop < AUTO_LOAD_PX && historyHasMore && initialLoadDone) loadMoreHistory();
}, true);

// ── Needs input ──────────────────────────────────────────────────────────
// Claude Code writes a tool call to the transcript only once it completes, so
// an open permission prompt, AskUserQuestion or plan approval never shows in
// the chat on its own. The PreToolUse hook reports the call as it starts
// (GET /pending-tool); the Notification hook sets waiting_for_input. Together
// they drive a card at the bottom of the chat saying what is being asked.

const pendingToolBySession = new Map(); // session_id -> pending tool or null
let pendingToolInFlight = false;

async function refreshPendingTool(session) {
    if (pendingToolInFlight || !session || !session.session_id) return;
    pendingToolInFlight = true;
    try {
        const qs = new URLSearchParams({ session_id: session.session_id });
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(session.name)}/pending-tool?${qs}`);
        if (resp.ok) {
            const data = await resp.json();
            pendingToolBySession.set(session.session_id, data.pending || null);
        }
    } catch { /* keep the last known value */ } finally {
        pendingToolInFlight = false;
    }
}

// The prompt's options as the terminal shows them right now (numbered, with
// the digit that answers each), read by the server from a pane capture.
const promptOptionsBySession = new Map(); // session_id -> { question, options: [{ n, label, action }], review }
let promptOptionsInFlight = false;

async function refreshPromptOptions(session) {
    if (promptOptionsInFlight || !session || !session.session_id) return;
    promptOptionsInFlight = true;
    try {
        const qs = new URLSearchParams({ session_id: session.session_id, agent_type: session.agent_type || "" });
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(session.name)}/prompt-options?${qs}`);
        if (resp.ok) {
            const data = await resp.json();
            promptOptionsBySession.set(session.session_id, { question: data.question || "", options: data.options || [], review: data.review || [] });
        }
    } catch { /* keep the last known value */ } finally {
        promptOptionsInFlight = false;
    }
}

// Tools whose whole purpose is to wait for the user
const BLOCKING_TOOLS = { AskUserQuestion: "question", ExitPlanMode: "plan" };

function needsInputInfo(session) {
    const row = (state.liveSessions || []).find(s => s.session_id === session.session_id) || session;
    const key = deriveSessionState(row);
    if (key === "ended" || key === "sleeping") return null;
    const pt = pendingToolBySession.get(session.session_id) || null;
    const screen = promptOptionsBySession.get(session.session_id) || null;
    if (pt && BLOCKING_TOOLS[pt.tool_name]) return { kind: BLOCKING_TOOLS[pt.tool_name], tool: pt, screen };
    if (row.waiting_for_input) return { kind: "permission", tool: pt, screen, summary: String(row.waiting_summary || "").replace(/^Notification:\s*/, "") };
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
};

function optionButtonHtml(o, desc) {
    return `<button type="button" class="cni-answer${o.selected ? " is-default" : ""}" data-n="${o.n}" data-label="${escapeHtml(o.label)}" data-action="select">`
        + `<kbd class="cni-key">${o.n}</kbd>`
        + `<span class="cni-answer-text"><span class="cni-answer-label">${escapeHtml(o.label)}</span>`
        + (desc ? `<span class="cni-answer-desc">${escapeHtml(desc)}</span>` : "")
        + `</span></button>`;
}

function answerControlsHtml(info, descriptions = {}) {
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
        foot += selects.map((o, i) => `<button type="button" class="cni-answer btn btn-sm ${i === 0 ? "btn-primary" : ""}" data-n="${o.n}" data-label="${escapeHtml(o.label)}" data-action="select">${escapeHtml(o.label)}</button>`).join("");
    } else if (selects.length) {
        const withDesc = selects.some(o => descriptions[o.label]);
        body += `<div class="cni-options ${withDesc ? "cni-options-cards" : "cni-options-chips"}">`
            + selects.map(o => optionButtonHtml(o, descriptions[o.label])).join("") + `</div>`;
    }
    for (const o of texts) {
        const placeholder = /^type something/i.test(o.label) ? "Or type your own answer…" : `${o.label.replace(/\.$/, "")}…`;
        body += `<form class="cni-text-form" data-n="${o.n}" data-label="${escapeHtml(o.label)}" data-action="text">`
            + `<input type="text" class="cni-text-input" placeholder="${escapeHtml(placeholder)}" aria-label="${escapeHtml(o.label)}">`
            + `<button type="submit" class="btn btn-sm">Send</button></form>`;
    }
    for (const o of chats) {
        foot = `<button type="button" class="cni-link cni-answer" data-n="${o.n}" data-label="${escapeHtml(o.label)}" data-action="chat">${escapeHtml(o.label)} instead</button>` + foot;
    }
    return { body, foot };
}

function reviewHtml(info) {
    const items = (info.screen && info.screen.review) || [];
    if (!items.length) return "";
    return `<dl class="cni-review">` + items.map(it =>
        `<div class="cni-review-row"><dt>${escapeHtml(it.question)}</dt><dd>${escapeHtml(it.answer || "—")}</dd></div>`).join("") + `</dl>`;
}

function needsInputHtml(info) {
    const inp = (info.tool && info.tool.input) || {};
    const screen = info.screen || {};
    const onScreen = (screen.options || []).length > 0;
    const screenQuestion = onScreen ? (screen.question || "") : "";
    let kicker = KICKERS[info.kind] || "Needs your input";
    let question = "";
    let context = "";
    let descriptions = {};

    if (info.kind === "question") {
        const qs = Array.isArray(inp.questions) ? inp.questions : [];
        for (const q of qs) for (const o of Array.isArray(q.options) ? q.options : []) if (o.description) descriptions[o.label] = o.description;
        question = screenQuestion || (qs[0] && qs[0].question) || "";
        if (!onScreen && qs.length) {
            // No live options (yet): list the questions from the hook
            context = qs.map(q => `<div class="cni-static-q">${escapeHtml(q.question || "")}`
                + (Array.isArray(q.options) && q.options.length ? `<ul>${q.options.map(o => `<li>${escapeHtml(o.label || "")}</li>`).join("")}</ul>` : "") + `</div>`).slice(1).join("");
        }
    } else if (info.kind === "plan") {
        question = screenQuestion ? "Ready to proceed with this plan?" : "Claude has a plan ready";
        if (inp.plan) context = `<div class="cni-plan message-text">${renderMarkdown(inp.plan)}</div>`;
    } else if (info.kind === "permission") {
        const tool = info.tool && info.tool.tool_name;
        if (tool) kicker = `Permission needed · ${escapeHtml(tool)}`;
        question = screenQuestion || (tool ? `Allow ${tool}?` : (info.summary || "The agent is waiting for you"));
        const target = inp.command || inp.file_path || inp.path || inp.url || inp.query || inp.pattern || "";
        if (inp.description && inp.command) context += `<div class="cni-context-text">${escapeHtml(inp.description)}</div>`;
        if (target) context += `<pre class="cni-target"><code>${escapeHtml(target)}</code></pre>`;
    } else {
        question = "The agent may be waiting at a startup prompt";
        context = `<div class="cni-context-text">For example, trusting this folder or logging in. Answer it in the terminal.</div>`;
    }

    const controls = answerControlsHtml(info, descriptions);
    const hint = !onScreen && info.kind !== "startup" ? `<span class="cni-hint">Answer in the terminal</span>` : "";
    return `<div class="cni-head"><span class="cni-dot" aria-hidden="true"></span><span class="cni-kicker">${kicker}</span></div>`
        + (question ? `<div class="cni-question">${escapeHtml(question)}</div>` : "")
        + context
        + reviewHtml(info)
        + controls.body
        + `<div class="cni-foot"><button type="button" class="cni-link" onclick="window.showTerminalView()">Open terminal</button>${hint}<span class="cni-foot-spacer"></span>${controls.foot}</div>`;
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
    }
    if (el.dataset.key !== key) {
        // Keep an answer being typed if the card re-renders
        const open = Array.from(el.querySelectorAll(".cni-text-form")).find(f => f.querySelector("input").value || document.activeElement === f.querySelector("input"));
        const typing = open ? { n: open.dataset.n, value: open.querySelector("input").value, focused: document.activeElement === open.querySelector("input") } : null;
        el.dataset.key = key;
        el.innerHTML = needsInputHtml(info);
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
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(session.name)}/answer-prompt`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ session_id: session.session_id, agent_type: session.agent_type || "", n, label, text: text || "" }),
        });
        const data = await resp.json().catch(() => ({}));
        if (!resp.ok) {
            showToast(data.error || "Could not send the answer. Use the terminal.", true);
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
    if (!btn || btn.disabled) return;
    const card = btn.closest(".chat-needs-input");
    btn.classList.add("sending");
    sendPromptAnswer(card, Number(btn.dataset.n), btn.dataset.label, btn.dataset.action).then(ok => {
        if (!ok) btn.classList.remove("sending");
    });
});

document.addEventListener("submit", (e) => {
    const form = e.target.closest && e.target.closest("#live-history-messages .cni-text-form");
    if (!form) return;
    e.preventDefault();
    const input = form.querySelector("input");
    const text = input.value.trim();
    if (!text) {
        input.focus();
        return;
    }
    sendPromptAnswer(form.closest(".chat-needs-input"), Number(form.dataset.n), form.dataset.label, "text", text);
});
