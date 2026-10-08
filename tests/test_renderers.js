#!/usr/bin/env node
/**
 * Tests for the block-group renderer classification and stateful spinner tracking.
 *
 * Run with: node tests/test_renderers.js
 *
 * Extracts the core logic from renderers.js (no DOM dependencies) and verifies:
 *   1. classifyLine returns correct types for all line variants
 *   2. groupIntoBlocks produces stable block structure
 *   3. Stateful spinner tracking prevents block jumping when the spinner
 *      character (⏺, ✶, etc.) appears and disappears between frames
 */

// ── Inline the regex constants and helpers from syntax.js + renderers.js ──

function isSeparatorLine(line) {
    const stripped = line.trim();
    if (stripped.length < 4) return false;
    return /^[─━═╌╍┄┅┈┉\-]+$/.test(stripped);
}

function isUserPromptLine(line) {
    return /^\s*[❯›>$]\s+\S/.test(line);
}

const CODE_FENCE_RE = /^(\s*\d+\s*[│|])/;
const DIFF_ADD_RE = /^(\s*\d+\s*\+)/;
const DIFF_DEL_RE = /^(\s*\d+\s*-)/;
const DIFF_SUMMARY_RE = /^\s*Added \d+ lines?,\s*removed \d+ lines?/;
const TOOL_HEADER_RE = /^⏺\s+(Read|Write|Edit|Bash|Glob|Grep|NotebookEdit|Task)\b/;
const TOOL_RESULT_RE = /^\s*⎿\s*/;

const TOOL_CALL_RE = /^[\s●]*⏺\s+[A-Z]/;
const PROGRESS_RE = /^\s*.+…\s*(\(.*\))?\s*$/;
const STATUS_BAR_RE = /^\s*(worktree:|⏵)/;

const SPINNER_CHARS_RE = /^[\s✶✷✸✹✺✻✼✽✾✿⏺⏵⏴⏹⏏⚡●○◉◎◌◐◑◒◓▪▫▸▹►▻\u2800-\u28FF·•]*/;
const HAS_SPINNER_RE = /^[\s]*[✶✷✸✹✺✻✼✽✾✿⏺⏵⏴⏹⏏⚡●○◉◎◌◐◑◒◓▪▫▸▹►▻\u2800-\u28FF·•]/;

let _knownSpinnerTexts = new Map();

function stripSpinner(line) {
    return line.replace(SPINNER_CHARS_RE, "");
}

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
    if (PROGRESS_RE.test(line)) return "status";
    if (TOOL_CALL_RE.test(line)) return "tool-header";
    if (TOOL_RESULT_RE.test(line)) return "tool-body";
    if (DIFF_ADD_RE.test(line) || DIFF_DEL_RE.test(line)) return "tool-body";
    if (DIFF_SUMMARY_RE.test(line)) return "tool-body";
    if (CODE_FENCE_RE.test(line)) return "tool-body";

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

function collectSpinnerTexts(lines) {
    const fresh = new Map();
    for (let i = 0; i < lines.length; i++) {
        const line = lines[i];
        if (!HAS_SPINNER_RE.test(line)) continue;
        const stripped = stripSpinner(line);
        if (!stripped) continue;
        const cls = classifyLine(line, i, lines);
        if (cls !== "text" && cls !== "empty") {
            fresh.set(stripped, cls);
        }
    }
    return fresh;
}

/** Simulate a render frame: classify + collect for next frame. */
function renderFrame(text) {
    const lines = text.split("\n");
    const blocks = groupIntoBlocks(lines);
    _knownSpinnerTexts = collectSpinnerTexts(lines);
    return blocks;
}

function resetState() {
    _knownSpinnerTexts = new Map();
}

// ── Test harness ────────────────────────────────────────────────────

let passed = 0;
let failed = 0;
const failures = [];

function assert(cond, msg) {
    if (cond) {
        passed++;
    } else {
        failed++;
        failures.push(msg);
        console.log(`  FAIL: ${msg}`);
    }
}

function assertEqual(actual, expected, msg) {
    if (actual === expected) {
        passed++;
    } else {
        failed++;
        const detail = `${msg} — expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`;
        failures.push(detail);
        console.log(`  FAIL: ${detail}`);
    }
}

function assertBlockTypes(blocks, expectedTypes, msg) {
    const actualTypes = blocks.map(b => b.type);
    const a = JSON.stringify(actualTypes);
    const e = JSON.stringify(expectedTypes);
    if (a === e) {
        passed++;
    } else {
        failed++;
        const detail = `${msg} — expected ${e}, got ${a}`;
        failures.push(detail);
        console.log(`  FAIL: ${detail}`);
    }
}

// ── Tests: classifyLine ─────────────────────────────────────────────

console.log("=== classifyLine tests ===");
resetState();

// Basic line types
assertEqual(classifyLine("", 0, [""]), "empty", "empty line");
assertEqual(classifyLine("   ", 0, ["   "]), "empty", "whitespace line");
assertEqual(classifyLine("────────────────", 0, ["────────────────"]), "separator", "separator line");
assertEqual(classifyLine("❯ hello world", 0, ["❯ hello world"]), "user", "user prompt");
assertEqual(classifyLine("> some input", 0, ["> some input"]), "user", "user prompt with >");
assertEqual(classifyLine("This is plain text", 0, ["This is plain text"]), "text", "plain text");

// Tool headers
assertEqual(classifyLine("⏺ Read 1 file (ctrl+o to expand)", 0, ["⏺ Read 1 file (ctrl+o to expand)"]), "tool-header",
    "⏺ Read... is tool-header (no …)");

assertEqual(classifyLine("⏺ Bash(git status)", 0, ["⏺ Bash(git status)"]), "tool-header",
    "⏺ Bash is tool-header");

assertEqual(classifyLine("⏺ Update(src/coral/static/style.css)", 0, ["⏺ Update(src/coral/static/style.css)"]), "tool-header",
    "⏺ Update is tool-header");

assertEqual(classifyLine("⏺ Claude in Chrome[tabs_context]", 0, ["⏺ Claude in Chrome[tabs_context]"]), "tool-header",
    "⏺ Claude in Chrome is tool-header");

// Tool results
assertEqual(classifyLine("  ⎿ some result", 0, ["  ⎿ some result"]), "tool-body", "tool result line");

// Progress/status lines (have …)
assertEqual(classifyLine("⏺ Searching for 1 pattern, reading 1 file…", 0, ["⏺ Searching for 1 pattern, reading 1 file…"]), "status",
    "⏺ with … is status (progress)");

assertEqual(classifyLine("✽ Sublimating…", 0, ["✽ Sublimating…"]), "status",
    "✽ with … is status");

assertEqual(classifyLine("· Unravelling… (30s · ↓ 741 tokens)", 0, ["· Unravelling… (30s · ↓ 741 tokens)"]), "status",
    "· with … and parenthetical is status");

assertEqual(classifyLine("✶ Thinking…", 0, ["✶ Thinking…"]), "status",
    "✶ Thinking… is status");

assertEqual(classifyLine("  Searching for 1 pattern, reading 1 file…", 0, ["  Searching for 1 pattern, reading 1 file…"]), "status",
    "indented line with … is status (no spinner)");

// PULSE-like text is no longer a protocol line: it is ordinary text
for (const l of ["||PULSE:STATUS Reading codebase||", "⏺ ||PULSE:STATUS Reading codebase||", "||PULSE:SUMMARY Implementing feature||"]) {
    assert(classifyLine(l, 0, [l]) !== "pulse", "PULSE-like text is not classified as pulse: " + l);
}

// Status bar
assertEqual(classifyLine("  worktree:main | session:abc123", 0, ["  worktree:main | session:abc123"]), "statusbar",
    "worktree: line is statusbar");

assertEqual(classifyLine("  ⏵⏵ accept edits on (shift+tab to cycle)", 0, ["  ⏵⏵ accept edits on (shift+tab to cycle)"]), "statusbar",
    "⏵⏵ accept edits is statusbar");

// Diff lines
assertEqual(classifyLine("  185 + new code", 0, ["  185 + new code"]), "tool-body",
    "diff add line is tool-body");

assertEqual(classifyLine("  185 - old code", 0, ["  185 - old code"]), "tool-body",
    "diff del line is tool-body");

assertEqual(classifyLine("Added 5 lines, removed 2 lines", 0, ["Added 5 lines, removed 2 lines"]), "tool-body",
    "diff summary is tool-body");

// Code fence
assertEqual(classifyLine("  10 │ const x = 1;", 0, ["  10 │ const x = 1;"]), "tool-body",
    "numbered code line is tool-body");

// Separator adjacent to user prompt → classified as user
{
    const lines = ["❯ hello", "────────────────"];
    assertEqual(classifyLine(lines[1], 1, lines), "user",
        "separator after user prompt is classified as user");
}
{
    const lines = ["────────────────", "❯ hello"];
    assertEqual(classifyLine(lines[0], 0, lines), "user",
        "separator before user prompt is classified as user");
}

// ── Tests: stripSpinner ─────────────────────────────────────────────

console.log("\n=== stripSpinner tests ===");

assertEqual(stripSpinner("⏺ Claude in Chrome[tabs_context]"), "Claude in Chrome[tabs_context]",
    "strip ⏺ prefix");
assertEqual(stripSpinner("✽ Sublimating…"), "Sublimating…",
    "strip ✽ prefix");
assertEqual(stripSpinner("· Unravelling… (30s · ↓ 741 tokens)"), "Unravelling… (30s · ↓ 741 tokens)",
    "strip · prefix (but not mid-text ·)");
assertEqual(stripSpinner("  Claude in Chrome[tabs_context]"), "Claude in Chrome[tabs_context]",
    "strip leading whitespace");
assertEqual(stripSpinner("Hello world"), "Hello world",
    "no spinner to strip");

// ── Tests: stateful spinner tracking ────────────────────────────────

console.log("\n=== Stateful spinner tracking tests ===");

// Test: ⏺ tool-header line loses spinner on next frame
{
    resetState();

    const frame1 = [
        "Some assistant text",
        "",
        "⏺ Claude in Chrome[tabs_context]",
    ].join("\n");

    const frame2 = [
        "Some assistant text",
        "",
        "  Claude in Chrome[tabs_context]",
    ].join("\n");

    const blocks1 = renderFrame(frame1);
    assertBlockTypes(blocks1, ["text", "tool"], "frame1: text + tool blocks");

    const blocks2 = renderFrame(frame2);
    assertBlockTypes(blocks2, ["text", "tool"], "frame2: text + tool blocks (stateful fallback keeps tool-header)");
}

// Test: ⏺ Read line loses spinner on next frame
{
    resetState();

    const frame1 = [
        "⏺ Now let me check the file",
        "",
        "⏺ Read 1 file (ctrl+o to expand)",
        "  ⎿ file contents here",
    ].join("\n");

    const frame2 = [
        "⏺ Now let me check the file",
        "",
        "  Read 1 file (ctrl+o to expand)",
        "  ⎿ file contents here",
    ].join("\n");

    const blocks1 = renderFrame(frame1);
    assertBlockTypes(blocks1, ["tool", "tool"], "frame1: two tool blocks");

    const blocks2 = renderFrame(frame2);
    assertBlockTypes(blocks2, ["tool", "tool"], "frame2: still two tool blocks (spinner gone from Read)");
}

// Test: progress line with … keeps status classification
{
    resetState();

    const frame1 = [
        "⏺ Now searching the codebase",
        "",
        "✽ Searching for patterns…",
    ].join("\n");

    const frame2 = [
        "⏺ Now searching the codebase",
        "",
        "  Searching for patterns…",
    ].join("\n");

    const blocks1 = renderFrame(frame1);
    assertBlockTypes(blocks1, ["tool", "status"], "frame1: tool + status");

    // Frame2: the … line still matches PROGRESS_RE directly, no fallback needed
    const blocks2 = renderFrame(frame2);
    assertBlockTypes(blocks2, ["tool", "status"], "frame2: tool + status (… still matches PROGRESS_RE)");
}

// Test: multiple spinner lines in same frame, some lose spinners
{
    resetState();

    const frame1 = [
        "⏺ Some explanation text",
        "",
        "⏺ Bash(git status)",
        "  ⎿ M file.js",
        "",
        "✶ Thinking…",
    ].join("\n");

    const frame2 = [
        "  Some explanation text",
        "",
        "  Bash(git status)",
        "  ⎿ M file.js",
        "",
        "  Thinking…",
    ].join("\n");

    const blocks1 = renderFrame(frame1);
    assertBlockTypes(blocks1, ["tool", "tool", "status"],
        "frame1: tool-header(explanation) + tool(bash+result) + status(thinking)");

    const blocks2 = renderFrame(frame2);
    assertBlockTypes(blocks2, ["tool", "tool", "status"],
        "frame2: same structure after all spinners vanish");
}

// Test: stale entries are cleared — a progress line that's gone doesn't persist
{
    resetState();

    const frame1 = [
        "✶ Thinking…",
        "",
        "⏺ Read 1 file",
    ].join("\n");

    const frame2 = [
        "⏺ Done with the analysis.",
        "",
        "⏺ Read 1 file",
    ].join("\n");

    renderFrame(frame1);
    const blocks2 = renderFrame(frame2);
    // "Done with the analysis." has ⏺ + uppercase D → tool-header
    // "Thinking…" is gone, so no stale match
    assertBlockTypes(blocks2, ["tool", "tool"],
        "frame2: stale 'Thinking…' entry doesn't leak");
}

// Test: without stateful tracking, spinnerless line stays in same text block
{
    resetState();
    // No prior frame → _knownSpinnerTexts is empty

    const lines = [
        "Some explanation text",
        "",
        "  Claude in Chrome[tabs_context]",
    ];

    const blocks = renderFrame(lines.join("\n"));
    // Both lines are "text", so they stay in one text block (text blocks
    // don't split on empty lines when the next non-empty line is also text)
    assertBlockTypes(blocks, ["text"],
        "no prior frame: consecutive text lines stay in one block");
}

// Test: PULSE-like text never becomes its own block type
{
    resetState();

    const lines = [
        "⏺ Bash(git checkout -b features/new_feature main)",
        "  ⎿  Switched to a new branch 'features/new_feature'",
        "⏺ ||PULSE:SUMMARY Waiting for instructions||",
        "  ||PULSE:STATUS Waiting for instructions||",
    ];
    const blocks = groupIntoBlocks(lines);
    assert(blocks.every(b => b.type !== "pulse"), "no pulse block type exists");
    const covered = blocks.flatMap(b => b.lines).sort();
    assertEqual(covered.join(","), "0,1,2,3", "every line, including PULSE-like text, is rendered");
}

// Test: block structure stability across 3 frames with alternating spinner
{
    resetState();

    const frameA = [
        "  Let me look at the file",
        "",
        "⏺ Claude in Chrome[tabs_context]",
        "",
        "────────────────────────────────",
        "❯ some user input",
        "────────────────────────────────",
        "  worktree:main | session:abc",
    ].join("\n");

    const frameB = [
        "  Let me look at the file",
        "",
        "  Claude in Chrome[tabs_context]",
        "",
        "────────────────────────────────",
        "❯ some user input",
        "────────────────────────────────",
        "  worktree:main | session:abc",
    ].join("\n");

    const expected = ["text", "tool", "user", "statusbar"];

    const b1 = renderFrame(frameA);
    assertBlockTypes(b1, expected, "alternating frame A (with ⏺)");

    const b2 = renderFrame(frameB);
    assertBlockTypes(b2, expected, "alternating frame B (without ⏺)");

    const b3 = renderFrame(frameA);
    assertBlockTypes(b3, expected, "alternating frame A again (with ⏺)");

    const b4 = renderFrame(frameB);
    assertBlockTypes(b4, expected, "alternating frame B again (without ⏺)");
}

// ── Tests: text block continuity across empty lines ─────────────────

console.log("\n=== Text block continuity tests ===");

// Text paragraphs separated by blank lines stay in one block
{
    resetState();

    const text = [
        "  First paragraph of assistant text.",
        "",
        "  Second paragraph of assistant text.",
        "",
        "  Third paragraph of assistant text.",
    ].join("\n");

    const blocks = renderFrame(text);
    assertBlockTypes(blocks, ["text"],
        "multiple text paragraphs separated by blank lines → one block");
}

// Text block breaks when followed by a tool-header
{
    resetState();

    const text = [
        "  Some explanation text.",
        "",
        "⏺ Read 1 file (ctrl+o to expand)",
    ].join("\n");

    const blocks = renderFrame(text);
    assertBlockTypes(blocks, ["text", "tool"],
        "text block breaks before tool-header");
}

// Text block breaks when followed by a user prompt
{
    resetState();

    const text = [
        "  Some explanation text.",
        "",
        "────────────────────────────────",
        "❯ user input here",
        "────────────────────────────────",
    ].join("\n");

    const blocks = renderFrame(text);
    assertBlockTypes(blocks, ["text", "user"],
        "text block breaks before user prompt");
}

// Code output (renderers.js content) stays in one block
{
    resetState();

    const text = [
        "  const TOOL_CALL_RE = /^[\\s●]*⏺\\s+[A-Z]/;",
        "  const PROGRESS_RE = /^\\s*.+…\\s*(\\(.*\\))?\\s*$/;",
        "  const STATUS_BAR_RE = /^\\s*(worktree:|⏵)/;",
        "",
        "  let _knownSpinnerTexts = new Map();",
        "",
        "  function stripSpinner(line) { return line.replace(SPINNER_CHARS_RE, ''); }",
        "",
        "  function classifyLine(line, i, lines) {",
        "      if (line.trim() === '') return 'empty';",
        "  }",
    ].join("\n");

    const blocks = renderFrame(text);
    assertBlockTypes(blocks, ["text"],
        "code-like text with blank lines stays in one block");
}

// Approval prompt stays in one block
{
    resetState();

    const text = [
        "   Bash command",
        "     git add src/coral/static/renderers.js tests/test_renderers.js",
        "     Stage modified and new files",
        "   This command requires approval",
        "   Do you want to proceed?",
    ].join("\n");

    const blocks = renderFrame(text);
    assertBlockTypes(blocks, ["text"],
        "approval prompt stays in one block");
}

// Text followed by status bar breaks correctly
{
    resetState();

    const text = [
        "  Some text output",
        "",
        "  worktree:main | session:abc",
    ].join("\n");

    const blocks = renderFrame(text);
    assertBlockTypes(blocks, ["text", "statusbar"],
        "text block breaks before statusbar");
}

// ── Summary ─────────────────────────────────────────────────────────

console.log(`\n${"=".repeat(50)}`);
console.log(`Results: ${passed} passed, ${failed} failed`);
if (failures.length > 0) {
    console.log("\nFailures:");
    for (const f of failures) {
        console.log(`  - ${f}`);
    }
    process.exit(1);
} else {
    console.log("All tests passed!");
    process.exit(0);
}
