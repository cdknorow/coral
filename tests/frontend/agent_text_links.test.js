// #3542: agent-written Markdown (board posts, chat, tasks, notes, knowledge, files) is rendered
// with a link policy, so a link in it can never move the dashboard tab to an /api URL or a hash
// route. This guards the callers: every renderMarkdown / sanitizeMarkdown call from a module
// that imports it from utils.js names its mode ('board', 'chat' or 'file'), and the renderers
// that bypass those helpers (marked.parse, DOMPurify.sanitize, a marked instance) appear only
// where this list says why. A new call site has to pick a mode or be added here on purpose.
const fs = require('node:fs');
const path = require('node:path');

const STATIC = path.resolve(__dirname, '../../coral-go/pkg/frontend/static');
const MODES = new Set(['board', 'chat', 'file']);

// Where marked.parse may be called directly, and why. Files marked `links` must pass a link
// mode to the sanitizeHtml that follows.
const MARKED_PARSE = {
    'utils.js': 'the renderers themselves',
    'notes.js': 'links', // session notes, which can be an agent-derived auto summary
    'preview_pane.js': 'links', // a workspace file, which can be agent-written
    'team_artifacts.js': 'links', // knowledge distilled from agents' text
};
const DOMPURIFY_SANITIZE = {
    'utils.js': 'sanitizeHtml itself',
    'subagents.js': 'a second pass over renderMarkdown(..., "board") output',
};

function walk(dir, out = []) {
    for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
        const p = path.join(dir, e.name);
        if (e.isDirectory()) { if (!['vendor', '_src', 'node_modules', 'agent_docs'].includes(e.name)) walk(p, out); } else if (e.name.endsWith('.js')) out.push(p);
    }
    return out;
}

// The top-level arguments of the call whose '(' is at `open` (strings, templates, comments and
// nesting aware enough for this codebase's calls).
function callArgs(src, open) {
    const args = [];
    let depth = 0, cur = '', quote = null;
    for (let i = open; i < src.length; i++) {
        const c = src[i];
        if (quote) {
            cur += c;
            if (c === '\\') { cur += src[++i] || ''; continue; }
            if (c === quote) quote = null;
            continue;
        }
        if (c === '"' || c === "'" || c === '`') { quote = c; cur += c; continue; }
        if (c === '(' || c === '[' || c === '{') { depth++; if (depth === 1 && c === '(') continue; }
        if (c === ')' || c === ']' || c === '}') {
            depth--;
            if (depth === 0) { if (cur.trim()) args.push(cur.trim()); return args; }
        }
        if (c === ',' && depth === 1) { args.push(cur.trim()); cur = ''; continue; }
        cur += c;
    }
    return null;
}

const problems = [];
let checkedCalls = 0;
for (const file of walk(STATIC)) {
    const name = path.relative(STATIC, file);
    const src = fs.readFileSync(file, 'utf8');
    for (const fn of ['renderMarkdown', 'sanitizeMarkdown']) {
        const imported = new RegExp(`import\\s*\\{[^}]*\\b${fn}\\b[^}]*\\}\\s*from\\s*['"]\\./utils\\.js['"]`).test(src);
        if (!imported) continue; // utils.js defines it; agent_notes.js has an unrelated local renderMarkdown()
        const re = new RegExp(`(?<![\\w.$])${fn}\\(`, 'g');
        let m;
        while ((m = re.exec(src))) {
            const before = src.slice(Math.max(0, m.index - 9), m.index);
            if (/function\s+$/.test(before)) continue;
            const args = callArgs(src, m.index + fn.length);
            checkedCalls++;
            const mode = args && args[2] && /^['"](\w+)['"]$/.exec(args[2]);
            if (!mode || !MODES.has(mode[1])) {
                const line = src.slice(0, m.index).split('\n').length;
                problems.push(`${name}:${line}: ${fn}(...) without a link mode ('board', 'chat' or 'file') as its third argument`);
            }
        }
    }
    if (/(?<![\w$])marked\.parse\(/.test(src)) {
        if (!(name in MARKED_PARSE)) problems.push(`${name}: calls marked.parse directly; use renderMarkdown(text, options, mode) or add it to MARKED_PARSE with a reason`);
        else if (MARKED_PARSE[name] === 'links' && !/sanitizeHtml\([^;]*\{\s*links:\s*'(board|chat|file)'/.test(src)) problems.push(`${name}: marked.parse output must go through sanitizeHtml(..., { links: mode })`);
    }
    if (/DOMPurify\.sanitize\(/.test(src) && !(name in DOMPURIFY_SANITIZE)) problems.push(`${name}: calls DOMPurify.sanitize directly; use sanitizeHtml(html, config, { links: mode }) or add it to DOMPURIFY_SANITIZE with a reason`);
    if (/new\s+marked\.Marked\(/.test(src) && name !== 'utils.js') problems.push(`${name}: makes its own marked instance; render through utils.js`);
}

if (checkedCalls < 8) problems.push(`only ${checkedCalls} renderMarkdown/sanitizeMarkdown calls found: the scan is broken`);
if (problems.length) {
    console.error('agent_text_links: FAIL\n  ' + problems.join('\n  '));
    process.exit(1);
}
console.log(`agent_text_links: ok (${checkedCalls} calls checked)`);
