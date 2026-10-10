// Scans the UI sources for template-literal holes (${...}) that land inside HTML attributes
// built as strings, and classifies how each is protected. Shared by the lint test and the
// one-time codemod.
//
//   on*="..."   JS code inside an attribute. The browser HTML-decodes the attribute BEFORE the
//               JS parser sees it, so entity escaping (&#39;) does not protect a JS string
//               literal. Holes must be wrapped by jsStr(...) (quoted position) or Number(...)
//               (unquoted position).
//   data-*="..." and other attributes: holes must be wrapped by escapeAttr(...), escapeHtml(...)
//               or Number(...).
import fs from 'node:fs';
import path from 'node:path';

const HANDLER = /^on[a-z]+$/;

/** Skip a JS string/regex/template starting at i (for hole expressions). Returns index after it. */
function skipString(src, i) {
    const q = src[i];
    let j = i + 1;
    while (j < src.length && src[j] !== q) {
        if (src[j] === '\\') j++;
        else if (q === '`' && src[j] === '$' && src[j + 1] === '{') j = skipHole(src, j + 2) - 1;
        j++;
    }
    return j + 1;
}

/** i points just after "${". Returns index just after the matching "}". */
function skipHole(src, i) {
    let depth = 1;
    while (i < src.length && depth > 0) {
        const c = src[i];
        if (c === '"' || c === "'" || c === '`') { i = skipString(src, i); continue; }
        if (c === '{') depth++;
        else if (c === '}') depth--;
        i++;
    }
    return i;
}

/**
 * Find attribute values inside the source: ` name="...."`, with holes. Works on the raw file
 * text; attribute text starts after `name="` and ends at the next `"` outside a hole.
 */
export function scanAttributes(src) {
    const out = [];
    const re = /[\s'"`]([a-zA-Z][\w:-]*)=\\?"/g;
    let m;
    while ((m = re.exec(src))) {
        const name = m[1];
        const valueStart = m.index + m[0].length;
        let i = valueStart;
        const parts = [];
        let textStart = i;
        let closed = false;
        while (i < src.length) {
            const c = src[i];
            if (c === '$' && src[i + 1] === '{') {
                if (i > textStart) parts.push({ kind: 'text', s: textStart, e: i });
                const hs = i + 2;
                const he = skipHole(src, hs);
                parts.push({ kind: 'hole', s: hs, e: he - 1 });
                i = he; textStart = i; continue;
            }
            if (c === '"' || c === '\n' || c === '`') { closed = c === '"'; break; }
            if (c === '\\' ) { i += 2; continue; }
            i++;
        }
        if (!closed) continue; // an attribute that spans lines or is not a plain quoted value
        if (i > textStart) parts.push({ kind: 'text', s: textStart, e: i });
        out.push({ name, start: valueStart, end: i, parts, line: src.slice(0, valueStart).split('\n').length });
        re.lastIndex = i;
    }
    return out;
}

const WRAPPERS_HANDLER_QUOTED = /^\s*jsStr\(/;
// Unquoted position: numbers, a validated function name, or (nested-escape sites the quote tracker
// cannot follow) jsStr itself.
const WRAPPERS_NUMERIC = /^\s*(Number|Math\.\w+|parseInt|parseFloat|jsIdent|jsStr)\(/;
// Variables that hold a handler string assembled from jsStr()/Number() pieces (reviewed by hand).
const PREBUILT = new Set(['clickHandler']);
const WRAPPERS_ATTR = /^\s*(escapeAttr|escapeHtml|escAttr|escHtml|esc|Number|jsStr)\(/;
// cond ? 'literal' : 'literal' (nothing attacker-controlled can reach the attribute)
const LITERAL_TERNARY = /^[^?]*\?\s*('[^'\\]*'|"[^"\\]*"|``)\s*:\s*('[^'\\]*'|"[^"\\]*"|``)\s*$/;

/** Classify each hole: {ok, reason}. */
export function checkAttribute(src, a) {
    const problems = [];
    const isHandler = HANDLER.test(a.name);
    const isData = a.name.startsWith('data-') || ['title', 'value', 'id', 'for', 'name', 'alt', 'aria-label'].includes(a.name);
    if (!isHandler && !isData && !['href', 'src'].includes(a.name)) return problems;
    let inQuote = false;
    for (const p of a.parts) {
        if (p.kind === 'text') {
            for (let k = p.s; k < p.e; k++) {
                if (src[k] === '\\') { k++; continue; }
                if (src[k] === "'") inQuote = !inQuote;
            }
            continue;
        }
        const expr = src.slice(p.s, p.e);
        if (LITERAL_TERNARY.test(expr.trim()) || (isHandler && PREBUILT.has(expr.trim()))) continue;
        if (isHandler) {
            if (inQuote ? !WRAPPERS_HANDLER_QUOTED.test(expr) : !WRAPPERS_NUMERIC.test(expr)) {
                problems.push({ line: a.line, attr: a.name, expr: expr.trim().slice(0, 80), quoted: inQuote, start: p.s, end: p.e });
            }
        } else if (!WRAPPERS_ATTR.test(expr)) {
            problems.push({ line: a.line, attr: a.name, expr: expr.trim().slice(0, 80), quoted: true, start: p.s, end: p.e });
        }
    }
    return problems;
}

export function scanTree(dir) {
    const results = [];
    const files = [];
    (function walk(d) {
        for (const e of fs.readdirSync(d, { withFileTypes: true })) {
            const p = path.join(d, e.name);
            if (e.isDirectory()) { if (e.name !== 'vendor') walk(p); } else if (/\.js$/.test(e.name)) files.push(p);
        }
    })(dir);
    for (const f of files.sort()) {
        const src = fs.readFileSync(f, 'utf8');
        for (const a of scanAttributes(src)) {
            for (const pr of checkAttribute(src, a)) results.push({ file: path.relative(dir, f), ...pr });
        }
    }
    return results;
}

if (import.meta.url === `file://${process.argv[1]}`) {
    const dir = path.resolve(process.argv[2] || path.join(path.dirname(new URL(import.meta.url).pathname), '../coral-go/pkg/frontend/static'));
    const r = scanTree(dir);
    for (const x of r) console.log(`${x.file}:${x.line} ${x.attr}${x.quoted ? ' (quoted)' : ''}: \${${x.expr}}`);
    console.log(`${r.length} unprotected holes`);
}
