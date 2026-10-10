/* Rendering engine abstraction for terminal capture output.
 *
 * Each RenderEngine takes raw terminal text and produces DOM nodes.
 * Engines can be assigned per agent type, with runtime overrides.
 */

import {
    isSeparatorLine, isUserPromptLine, highlightCodeLine,
    CODE_FENCE_RE, DIFF_ADD_RE, DIFF_DEL_RE, DIFF_SUMMARY_RE,
    TOOL_HEADER_RE, TOOL_RESULT_RE,
} from './syntax.js';
import { state } from './state.js';

// ── Regex constants shared across renderers ──────────────────────────

// Match ⏺ followed by a capitalized word (tool name like Read, Write, Bash)
// or common assistant prose starters. Excludes spinner/progress lines where
// ⏺ is just one frame of the rotating spinner character.
const TOOL_CALL_RE = /^[\s●]*⏺\s+[A-Z]/;
// Matches progress/thinking lines. The spinner char rotates through many
// Unicode characters (braille, geometric shapes, ⏺, ·, etc.), and sometimes
// the spinner disappears entirely leaving just indented text with an ellipsis.
// The Unicode ellipsis … (U+2026) is the reliable marker — Claude Code uses
// it specifically for progress output, normal prose uses "..." (three dots).
// Matches lines containing … that aren't tool results (⎿) or user prompts.
const PROGRESS_RE = /^\s*.+…\s*(\(.*\))?\s*$/;
const STATUS_BAR_RE = /^\s*(worktree:|⏵)/;

// All spinner characters Claude Code cycles through (from log_streamer.py).
// Used to strip spinner prefixes for stateful progress line tracking.
const SPINNER_CHARS_RE = /^[\s✶✷✸✹✺✻✼✽✾✿⏺⏵⏴⏹⏏⚡●○◉◎◌◐◑◒◓▪▫▸▹►▻\u2800-\u28FF·•]*/;

// ── Stateful spinner tracking ────────────────────────────────────────
// The spinner char (⏺, ✶, etc.) appears and disappears between frames.
// When we see a line with a spinner, we store its text (stripped of the
// spinner) and its classification. On the next render, if a line has the
// same text but no spinner, we reuse the stored classification instead
// of falling through to "text".
let _knownSpinnerTexts = new Map(); // stripped text -> classification

/** Strip leading spinner characters and whitespace from a line. */
function stripSpinner(line) {
    return line.replace(SPINNER_CHARS_RE, "");
}

// ── Shared line rendering ────────────────────────────────────────────

/** Render a single line div with syntax highlighting. */
function renderLine(line) {
    const div = document.createElement("div");
    div.className = "capture-line";

    const diffAddMatch = line.match(DIFF_ADD_RE);
    const diffDelMatch = !diffAddMatch && line.match(DIFF_DEL_RE);
    const isDiffSummary = DIFF_SUMMARY_RE.test(line);
    const isNumberedCode = !diffAddMatch && !diffDelMatch && CODE_FENCE_RE.test(line);
    const isToolHeader = TOOL_HEADER_RE.test(line) || TOOL_CALL_RE.test(line);
    const isToolResult = TOOL_RESULT_RE.test(line);

    if (isSeparatorLine(line)) {
        div.classList.add("capture-separator");
        div.textContent = line;
    } else if (isUserPromptLine(line)) {
        div.classList.add("capture-user-input");
        div.textContent = line;
    } else if (isDiffSummary) {
        div.classList.add("capture-diff-summary");
        div.textContent = line;
    } else if (diffAddMatch) {
        div.classList.add("capture-diff-add");
        const gutter = diffAddMatch[1];
        const code = line.slice(gutter.length);
        const gutterSpan = document.createElement("span");
        gutterSpan.className = "sh-diff-gutter-add";
        gutterSpan.textContent = gutter;
        div.appendChild(gutterSpan);
        const highlighted = highlightCodeLine(code);
        if (highlighted) {
            div.appendChild(highlighted);
        } else {
            div.appendChild(document.createTextNode(code));
        }
    } else if (diffDelMatch) {
        div.classList.add("capture-diff-del");
        const gutter = diffDelMatch[1];
        const code = line.slice(gutter.length);
        const gutterSpan = document.createElement("span");
        gutterSpan.className = "sh-diff-gutter-del";
        gutterSpan.textContent = gutter;
        div.appendChild(gutterSpan);
        div.appendChild(document.createTextNode(code));
    } else if (isToolHeader) {
        div.classList.add("capture-tool-header");
        div.textContent = line;
    } else if (isToolResult) {
        div.classList.add("capture-tool-result");
        div.textContent = line;
    } else if (isNumberedCode) {
        div.classList.add("capture-code");
        const match = line.match(CODE_FENCE_RE);
        const gutter = match[1];
        const code = line.slice(gutter.length);
        const gutterSpan = document.createElement("span");
        gutterSpan.className = "sh-gutter";
        gutterSpan.textContent = gutter;
        div.appendChild(gutterSpan);
        const highlighted = highlightCodeLine(code);
        if (highlighted) {
            div.appendChild(highlighted);
        } else {
            div.appendChild(document.createTextNode(code));
        }
    } else {
        div.textContent = line;
    }

    return div;
}

// ── Base class ───────────────────────────────────────────────────────

class RenderEngine {
    /** Human-readable name for UI display. */
    get name() { return "base"; }

    /** Render raw terminal text into the given DOM element. */
    render(el, text) {
        throw new Error("RenderEngine.render() not implemented");
    }
}

// ── Block-group renderer (current) ───────────────────────────────────

function classifyLine(line, i, lines) {
    if (line.trim() === "") return "empty";

    if (isSeparatorLine(line)) {
        const prevIsUser = i > 0 && isUserPromptLine(lines[i - 1]);
        const nextIsUser = i < lines.length - 1 && isUserPromptLine(lines[i + 1]);
        if (prevIsUser || nextIsUser) return "user";
        return "separator";
    }
    if (isUserPromptLine(line)) return "user";
    if (STATUS_BAR_RE.test(line)) return "statusbar";
    // Progress/thinking lines must be checked before tool headers because
    // the spinner cycles through characters including ⏺, which would
    // otherwise match TOOL_CALL_RE and cause blocks to jump on each frame.
    if (PROGRESS_RE.test(line)) return "status";
    if (TOOL_CALL_RE.test(line)) return "tool-header";
    if (TOOL_RESULT_RE.test(line)) return "tool-body";
    if (DIFF_ADD_RE.test(line) || DIFF_DEL_RE.test(line)) return "tool-body";
    if (DIFF_SUMMARY_RE.test(line)) return "tool-body";
    if (CODE_FENCE_RE.test(line)) return "tool-body";

    // Stateful fallback: if the spinner char disappeared but the text
    // matches a previously-seen spinner line, reuse its classification.
    const stripped = stripSpinner(line);
    if (stripped && _knownSpinnerTexts.has(stripped)) {
        return _knownSpinnerTexts.get(stripped);
    }

    return "text";
}

function groupIntoBlocks(lines) {
    const blocks = [];
    let current = null;

    function finishBlock() {
        if (current && current.lines.length > 0) {
            while (current.lines.length > 0 && lines[current.lines[current.lines.length - 1]].trim() === "") {
                current.lines.pop();
            }
            if (current.lines.length > 0) {
                blocks.push(current);
            }
        }
        current = null;
    }

    for (let i = 0; i < lines.length; i++) {
        const cls = classifyLine(lines[i], i, lines);

        if (cls === "empty") {
            // For text blocks, only break if the next non-empty line is a
            // different block type. This keeps paragraphs of assistant prose
            // (separated by blank lines) in a single block.
            if (current && current.type === "text") {
                let nextCls = null;
                for (let j = i + 1; j < lines.length; j++) {
                    if (lines[j].trim() !== "") {
                        nextCls = classifyLine(lines[j], j, lines);
                        break;
                    }
                }
                if (nextCls && nextCls !== "text") {
                    finishBlock();
                }
            }
            continue;
        }

        if (cls === "user") {
            if (!current || current.type !== "user") {
                finishBlock();
                current = { type: "user", lines: [] };
            }
            current.lines.push(i);
        } else if (cls === "tool-header") {
            finishBlock();
            current = { type: "tool", lines: [i] };
        } else if (cls === "tool-body") {
            if (current && (current.type === "tool" || current.type === "status")) {
                current.lines.push(i);
            } else {
                finishBlock();
                current = { type: "tool", lines: [i] };
            }
        } else if (cls === "text") {
            if (current && (current.type === "text" || current.type === "tool" || current.type === "user" || current.type === "status")) {
                current.lines.push(i);
            } else {
                finishBlock();
                current = { type: "text", lines: [i] };
            }
        } else if (cls === "separator") {
            finishBlock();
        } else if (cls === "status") {
            if (current && current.type === "status") {
                current.lines.push(i);
            } else {
                finishBlock();
                current = { type: "status", lines: [i] };
            }
        } else if (cls === "statusbar") {
            if (current && current.type === "statusbar") {
                current.lines.push(i);
            } else {
                finishBlock();
                current = { type: "statusbar", lines: [i] };
            }
        }
    }

    finishBlock();
    return blocks;
}

/** Regex to detect a line that starts with a spinner character. */
const HAS_SPINNER_RE = /^[\s]*[✶✷✸✹✺✻✼✽✾✿⏺⏵⏴⏹⏏⚡●○◉◎◌◐◑◒◓▪▫▸▹►▻\u2800-\u28FF·•]/;

/**
 * Scan lines and record the classification of any line with a spinner prefix.
 * On the next render, if the spinner vanishes, the stateful fallback in
 * classifyLine will reuse the stored classification instead of "text".
 */
function collectSpinnerTexts(lines) {
    const fresh = new Map();
    for (let i = 0; i < lines.length; i++) {
        const line = lines[i];
        if (!HAS_SPINNER_RE.test(line)) continue;
        const stripped = stripSpinner(line);
        if (!stripped) continue;
        // Classify this line normally (it has a spinner so it will match
        // PROGRESS_RE or TOOL_CALL_RE etc.) and store the result.
        const cls = classifyLine(line, i, lines);
        if (cls !== "text" && cls !== "empty") {
            fresh.set(stripped, cls);
        }
    }
    return fresh;
}

class BlockGroupRenderer extends RenderEngine {
    get name() { return "block-group"; }

    render(el, text) {
        el.innerHTML = "";
        const lines = text.split("\n");

        // Classify using known spinner texts from the PREVIOUS render,
        // then rebuild the map for the NEXT render. This ensures that if
        // a line loses its spinner on this frame, it still gets the same
        // classification via the stateful fallback.
        const blocks = groupIntoBlocks(lines);
        _knownSpinnerTexts = collectSpinnerTexts(lines);

        for (const block of blocks) {
            const container = document.createElement("div");
            container.className = `capture-block capture-block-${block.type}`;

            for (const idx of block.lines) {
                container.appendChild(renderLine(lines[idx]));
            }

            el.appendChild(container);
        }
    }
}

// ── Plain renderer (previous flat line-by-line) ──────────────────────

class PlainRenderer extends RenderEngine {
    get name() { return "plain"; }

    render(el, text) {
        el.innerHTML = "";
        const lines = text.split("\n");

        for (const line of lines) {
            if (line.trim() === "") continue;
            el.appendChild(renderLine(line));
        }
    }
}

// ── Registry ─────────────────────────────────────────────────────────

const ENGINES = {
    "block-group": new BlockGroupRenderer(),
    "plain": new PlainRenderer(),
    "xterm": "xterm",  // Sentinel; actual rendering handled by xterm_renderer.js
};

/** Default renderer name per agent type. */
const AGENT_DEFAULTS = {
    claude: "xterm",
    agy: "xterm",
    antigravity: "xterm",
    gemini: "xterm",
    codex: "xterm",
    pi: "xterm",
};

/** Per-agent overrides stored at runtime (sessionId -> engineName). */
const agentOverrides = {};

/** Get all registered engine names. */
export function getEngineNames() {
    return Object.keys(ENGINES);
}

/** Get the current engine name for a given agent. */
export function getEngineName(agentType, sessionId) {
    if (sessionId && agentOverrides[sessionId]) {
        return agentOverrides[sessionId];
    }
    // Fall back to global user setting before hardcoded agent-type defaults
    if (state.settings && state.settings.default_renderer && ENGINES[state.settings.default_renderer]) {
        return state.settings.default_renderer;
    }
    return AGENT_DEFAULTS[agentType] || AGENT_DEFAULTS.claude;
}

/** Get the renderer mode name (returns "xterm" for the xterm sentinel). */
export function getRendererMode(agentType, sessionId) {
    return getEngineName(agentType, sessionId);
}

/** Get the renderer instance for a given agent. */
export function getRenderer(agentType, sessionId) {
    const name = getEngineName(agentType, sessionId);
    const engine = ENGINES[name];
    // If it's a sentinel string (like "xterm"), fall back to block-group
    if (typeof engine === 'string' || !engine) return ENGINES["block-group"];
    return engine;
}

/** Override the renderer for a specific agent session. */
export function setRendererOverride(sessionId, engineName) {
    if (!ENGINES[engineName]) {
        console.warn(`Unknown render engine: ${engineName}`);
        return;
    }
    agentOverrides[sessionId] = engineName;
}

/** Clear the renderer override for a specific agent session. */
export function clearRendererOverride(sessionId) {
    delete agentOverrides[sessionId];
}

/** Set the default renderer for an agent type. */
export function setAgentDefault(agentType, engineName) {
    if (!ENGINES[engineName]) {
        console.warn(`Unknown render engine: ${engineName}`);
        return;
    }
    AGENT_DEFAULTS[agentType] = engineName;
}
