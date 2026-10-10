/* Syntax highlighting and line detection for capture output */

// Detect separator lines (horizontal rules made of box-drawing chars like ─ ━ ═)
export function isSeparatorLine(line) {
    const stripped = line.trim();
    if (stripped.length < 4) return false;
    return /^[─━═╌╍┄┅┈┉\-]+$/.test(stripped);
}

// Detect user prompt lines (❯, >, $)
export function isUserPromptLine(line) {
    return /^\s*[❯›>$]\s+\S/.test(line);
}

// Detect code fence markers from Claude Code output (e.g. "  1 │ ...", tool headers)
export const CODE_FENCE_RE = /^(\s*\d+\s*[│|])/;
// Diff lines: "  185 -  old code" or "  185 +  new code" (number then +/-)
export const DIFF_ADD_RE = /^(\s*\d+\s*\+)/;
export const DIFF_DEL_RE = /^(\s*\d+\s*-)/;
// Diff summary line: "Added N lines, removed M lines"
export const DIFF_SUMMARY_RE = /^\s*Added \d+ lines?,\s*removed \d+ lines?/;
export const TOOL_HEADER_RE = /^⏺\s+(Read|Write|Edit|Bash|Glob|Grep|NotebookEdit|Task)\b/;
export const TOOL_RESULT_RE = /^\s*⎿\s*/;

// Syntax highlighting patterns applied inside code lines
const SYNTAX_RULES = [
    // Comments (# ..., // ..., /* ... */, <!-- ... -->)
    { re: /(#[^!].*|\/\/.*|\/\*.*?\*\/|<!--.*?-->)/, cls: "sh-comment" },
    // Strings (double-quoted and single-quoted)
    { re: /("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|`(?:[^`\\]|\\.)*`)/, cls: "sh-string" },
    // Keywords (common across Python, JS, TS, Go, Rust, shell, etc.)
    { re: /\b(function|const|let|var|return|if|else|elif|for|while|class|def|import|from|export|default|async|await|try|catch|except|finally|raise|throw|new|this|self|yield|match|case|fn|pub|mod|use|impl|struct|enum|trait|interface|type|extends|implements|package|func|go|defer|select|chan)\b/, cls: "sh-keyword" },
    // Built-in values
    { re: /\b(true|false|True|False|null|None|undefined|nil)\b/, cls: "sh-builtin" },
    // Numbers
    { re: /\b(\d+\.?\d*(?:e[+-]?\d+)?|0x[0-9a-fA-F]+|0b[01]+|0o[0-7]+)\b/, cls: "sh-number" },
    // Decorators / annotations
    { re: /(@\w+)/, cls: "sh-decorator" },
];

export function highlightCodeLine(text) {
    // Build a list of {start, end, cls} spans, non-overlapping
    const spans = [];

    for (const rule of SYNTAX_RULES) {
        const global = new RegExp(rule.re.source, "g");
        let m;
        while ((m = global.exec(text)) !== null) {
            const start = m.index;
            const end = start + m[0].length;
            // Only add if it doesn't overlap with existing spans
            const overlaps = spans.some(s => start < s.end && end > s.start);
            if (!overlaps) {
                spans.push({ start, end, cls: rule.cls });
            }
        }
    }

    if (spans.length === 0) return null; // no highlighting needed

    spans.sort((a, b) => a.start - b.start);

    const frag = document.createDocumentFragment();
    let pos = 0;
    for (const span of spans) {
        if (span.start > pos) {
            frag.appendChild(document.createTextNode(text.slice(pos, span.start)));
        }
        const el = document.createElement("span");
        el.className = span.cls;
        el.textContent = text.slice(span.start, span.end);
        frag.appendChild(el);
        pos = span.end;
    }
    if (pos < text.length) {
        frag.appendChild(document.createTextNode(text.slice(pos)));
    }
    return frag;
}
