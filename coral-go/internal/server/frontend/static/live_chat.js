/* Live history view — renders JSONL messages as a read-only conversation log */

import { state } from './state.js';
import { escapeHtml, renderMarkdown } from './utils.js';
import { platform } from './platform/detect.js';

let historyPollInterval = null;
let historyMessageCount = 0;
let historyOffset = 0;      // pagination offset for "Load More"
let historyHasMore = false;  // whether older messages exist
let initialLoadDone = false; // whether the initial full load has completed


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

/** Render inline output preview: first 3 lines visible, rest expandable */
function renderInlineOutput(content, isError) {
    const lines = content.split("\n");
    const errorClass = isError ? " tool-result-error" : "";
    const preview = lines.slice(0, 3).join("\n");

    if (lines.length <= 3) {
        return `<pre class="tool-output-inline${errorClass}">${escapeHtml(preview)}</pre>`;
    }

    const full = content;
    return `<div class="tool-output-wrapper">
        <pre class="tool-output-inline tool-output-collapsed${errorClass}">${escapeHtml(preview)}</pre>
        <pre class="tool-output-inline tool-output-full${errorClass}" style="display:none">${escapeHtml(full)}</pre>
        <button class="tool-output-toggle" onclick="this.parentElement.querySelector('.tool-output-collapsed').style.display='none';this.parentElement.querySelector('.tool-output-full').style.display='';this.textContent='';this.style.display='none'">... ${lines.length - 3} more lines</button>
    </div>`;
}

function renderToolCard(tool) {
    const icon = getToolIcon(tool.name);
    const toolUseId = tool.tool_use_id || "";
    let bodyHtml = "";

    // Show description if present (Claude's prompt to the user)
    if (tool.description) {
        bodyHtml += `<div class="tool-card-description">${escapeHtml(tool.description)}</div>`;
    }

    if (tool.name === "Bash" && tool.command) {
        // Bash: show full command in a code block
        bodyHtml += `<pre class="tool-card-command">${escapeHtml(tool.command)}</pre>`;
    } else if (tool.input_summary) {
        bodyHtml += `<div class="tool-card-summary">${escapeHtml(tool.input_summary)}</div>`;
    }

    // AskUserQuestion: render structured question with options
    if (tool.name === "AskUserQuestion" && tool.questions) {
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

    // Edit tool: show diff
    if (tool.name === "Edit" && (tool.old_string || tool.new_string)) {
        const diffHtml = renderDiffLines(tool.old_string || "", tool.new_string || "");
        bodyHtml += `<details class="tool-card-diff"><summary>Diff</summary><div class="diff-content">${diffHtml}</div></details>`;
    }
    // Write tool: show content preview
    if (tool.name === "Write" && tool.write_content) {
        bodyHtml += `<details class="tool-card-diff"><summary>Content</summary><pre class="tool-result-content">${escapeHtml(tool.write_content)}</pre></details>`;
    }

    return `<div class="tool-card" data-tool-use-id="${escapeHtml(toolUseId)}">
        ${bodyHtml}
    </div>`;
}

function renderMessage(msg, container) {
    if (msg.type === "user") {
        const div = document.createElement("div");
        div.className = "chat-bubble human";
        div.innerHTML = `
            <div class="role-label">You</div>
            <div class="message-text">${renderMarkdown(msg.content)}</div>
        `;
        container.appendChild(div);
    } else if (msg.type === "assistant") {
        const tools = msg.tool_uses || [];
        const textHtml = msg.text ? `<div class="message-text">${renderMarkdown(msg.text)}</div>` : "";
        if (tools.length > 0) {
            // Text before tools (if any)
            if (textHtml) {
                const textDiv = document.createElement("div");
                textDiv.className = "chat-bubble assistant";
                textDiv.innerHTML = textHtml;
                container.appendChild(textDiv);
            }
            // Each tool gets its own bubble with tool name as the role label
            for (const tool of tools) {
                const icon = getToolIcon(tool.name);
                const toolDiv = document.createElement("div");
                toolDiv.className = "chat-bubble assistant";
                toolDiv.innerHTML = `<div class="role-label">${icon} ${escapeHtml(tool.name)}</div>` +
                    renderToolCard(tool);
                container.appendChild(toolDiv);
            }
        } else {
            // Text-only assistant message
            const div = document.createElement("div");
            div.className = "chat-bubble assistant";
            div.innerHTML = textHtml;
            container.appendChild(div);
        }
    } else if (msg.type === "tool_result") {
        // Inject output inline into the matching tool card
        const toolUseId = msg.tool_use_id || "";
        let targetCard = null;
        if (toolUseId) {
            targetCard = container.querySelector(`.tool-card[data-tool-use-id="${CSS.escape(toolUseId)}"]`);
        }
        if (targetCard) {
            // Append inline output to the tool card
            const outputDiv = document.createElement("div");
            outputDiv.className = "tool-card-output";
            outputDiv.innerHTML = renderInlineOutput(msg.content, msg.is_error);
            targetCard.appendChild(outputDiv);
        } else {
            // Fallback: render as standalone block if no matching card found
            const div = document.createElement("div");
            div.className = "tool-result-block";
            const toolName = msg.tool_name || "";
            const icon = toolName ? getToolIcon(toolName) : "";
            const label = toolName ? `${icon} ${escapeHtml(toolName)} Output` : "Output";
            div.innerHTML = `<div class="tool-result-standalone">${label}</div>` +
                renderInlineOutput(msg.content, msg.is_error);
            container.appendChild(div);
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

    try {
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(session.name)}/chat?${params}`);
        const data = await resp.json();

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
            historyMessageCount = data.total;
        } else if (!initialLoadDone) {
            container.innerHTML = "";
            historyHasMore = false;
            initialLoadDone = true;
            _updateLoadMoreButton(container);
        }

        if (state.autoScroll) {
            container.scrollTop = container.scrollHeight;
        }
    } catch (e) {
        console.error("Failed to refresh live history:", e);
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

            for (const msg of data.messages) {
                const temp = document.createElement("div");
                renderMessage(msg, temp);
                while (temp.firstChild) {
                    container.insertBefore(temp.firstChild, refNode);
                }
            }

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
    historyMessageCount = 0;
    historyOffset = 0;
    historyHasMore = false;
    initialLoadDone = false;
}
