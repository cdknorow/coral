/* A chat's tool-call and question cards (moved out of live_chat.js, #5065; no behaviour change). */
import { escapeHtml } from './utils.js';

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

export function getToolIcon(name) {
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

export function renderToolCard(tool, adapter, index) {
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
export function renderQuestionCard(tool) {
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
