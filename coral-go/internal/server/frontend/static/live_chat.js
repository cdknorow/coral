/* Live history view — renders JSONL messages as a read-only conversation log */

import { state } from './state.js';
import { escapeHtml, renderMarkdown, labelCodeBlocks, showToast } from './utils.js';
import { platform } from './platform/detect.js';
import { fitTerminal } from './xterm_renderer.js';

let historyPollInterval = null;
let historyMessageCount = 0;
let historyOffset = 0;      // pagination offset for "Load More"
let historyHasMore = false;  // whether older messages exist
let initialLoadDone = false; // whether the initial full load has completed
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

function demoteTurnFinal(container) {
    const last = container.lastElementChild;
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
    let group = container.lastElementChild;
    if (!group || !group.classList.contains("work-group")) {
        group = createWorkGroup();
        container.appendChild(group);
    }
    group.querySelector(":scope > .work-group-body").appendChild(el);
    updateWorkGroupSummary(group);
}

function appendTurnFinal(container, el) {
    demoteTurnFinal(container);
    el.classList.add("turn-final");
    container.appendChild(el);
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
    if (msg.type === "user") {
        container.appendChild(makeBubble("chat-bubble human",
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
        params.set("limit", "100");
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
                if (msg.type === "user") settlePending(session.session_id, msg);
            }
            historyMessageCount = data.total;
        } else if (!initialLoadDone) {
            container.innerHTML = "";
            historyHasMore = false;
            initialLoadDone = true;
            _updateLoadMoreButton(container);
        }

        syncPendingBubbles(container, session.session_id);
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
    if (!state.currentSession || !historyHasMore) return;

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
    params.set("limit", "100");
    params.set("offset", String(historyOffset));

    try {
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(session.name)}/chat?${params}`);
        const data = await resp.json();

        if (data.messages && data.messages.length > 0) {
            // Preserve scroll position: measure before inserting
            const prevScrollHeight = container.scrollHeight;

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
            container.scrollTop = container.scrollHeight - prevScrollHeight;
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

/** Apply the persisted mode to the DOM and start/stop the transcript poll. */
export function applyLiveViewMode() {
    const canChat = hasTranscript(state.currentSession);
    const mode = canChat ? getLiveViewMode() : "terminal";
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
// the bottom of the chat as "Queued" so it does not seem to vanish.

const PENDING_TTL_MS = 15 * 60 * 1000;
const pendingBySession = new Map(); // session_id -> [{ id, text, at }]
let pendingSeq = 0;

const normalizeMsg = (t) => String(t || "").replace(/\s+/g, " ").trim();

/** Record a message just sent to an agent so the chat can show it right away. */
export function addPendingMessage(sessionId, text) {
    if (!sessionId || !normalizeMsg(text)) return;
    const list = pendingBySession.get(sessionId) || [];
    list.push({ id: ++pendingSeq, text, at: Date.now() });
    pendingBySession.set(sessionId, list);
    const container = document.getElementById("live-history-messages");
    if (container && state.currentSession && state.currentSession.session_id === sessionId) {
        syncPendingBubbles(container, sessionId);
        container.scrollTop = container.scrollHeight;
    }
}

// Drop the oldest pending message this transcript entry accounts for. Only
// entries written after the send can match, so an earlier identical message
// ("continue") does not clear a new one.
function settlePending(sessionId, msg) {
    const list = pendingBySession.get(sessionId);
    if (!list || !list.length) return;
    const got = normalizeMsg(msg.content);
    const ts = Date.parse(msg.timestamp || "");
    if (!got) return;
    const i = list.findIndex(p => {
        if (!Number.isNaN(ts) && ts < p.at - 10000) return false;
        const want = normalizeMsg(p.text);
        return got === want || got.includes(want) || want.includes(got);
    });
    if (i !== -1) list.splice(i, 1);
}

function syncPendingBubbles(container, sessionId) {
    const now = Date.now();
    const list = (pendingBySession.get(sessionId) || []).filter(p => now - p.at < PENDING_TTL_MS);
    pendingBySession.set(sessionId, list);
    let wrap = container.querySelector(":scope > .pending-messages");
    if (!list.length) {
        if (wrap) wrap.remove();
        return;
    }
    if (!wrap) {
        wrap = document.createElement("div");
        wrap.className = "pending-messages";
    }
    const have = new Set(Array.from(wrap.children).map(el => Number(el.dataset.pendingId)));
    const want = new Set(list.map(p => p.id));
    for (const el of Array.from(wrap.children)) {
        if (!want.has(Number(el.dataset.pendingId))) el.remove();
    }
    for (const p of list) {
        if (have.has(p.id)) continue;
        const el = makeBubble("chat-bubble human pending",
            `<div class="message-text">${renderMarkdown(p.text)}</div><div class="pending-label" role="status">Queued</div>`);
        el.dataset.pendingId = String(p.id);
        wrap.appendChild(el);
    }
    // Always last, below any newly rendered messages
    if (container.lastElementChild !== wrap) container.appendChild(wrap);
}
