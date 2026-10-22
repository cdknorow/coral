// Scans the UI sources for template-literal holes (${...}) that land inside HTML attributes
// built as strings, and classifies how each is protected (tests/frontend/attr_holes.test.js).
//
//   data-*="..." and other attributes: holes must be wrapped by escapeAttr(...), escapeHtml(...)
//               or Number(...).
//   on*="..."   not allowed at all: the dashboard CSP has no 'unsafe-inline' (#3645), so an inline
//               handler would not run; markup names a data-on-* action instead.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

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

const WRAPPERS_ATTR = /^\s*(escapeAttr|escapeHtml|escAttr|escHtml|esc|Number)\(/;
// cond ? 'literal' : 'literal' (nothing attacker-controlled can reach the attribute)
const LITERAL_TERNARY = /^[^?]*\?\s*('[^'\\]*'|"[^"\\]*"|``)\s*:\s*('[^'\\]*'|"[^"\\]*"|``)\s*$/;

/** Classify each hole: {ok, reason}. */
export function checkAttribute(src, a) {
    const problems = [];
    if (HANDLER.test(a.name)) return [{ line: a.line, attr: a.name, expr: 'an inline handler (name a data-on-* action instead)', quoted: false, start: 0, end: 0 }];
    const isData = a.name.startsWith('data-') || ['title', 'value', 'id', 'for', 'name', 'alt', 'aria-label'].includes(a.name);
    if (!isData && !['href', 'src'].includes(a.name)) return problems;
    for (const p of a.parts) {
        if (p.kind === 'text') continue;
        const expr = src.slice(p.s, p.e);
        if (LITERAL_TERNARY.test(expr.trim())) continue;
        if (!WRAPPERS_ATTR.test(expr)) {
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

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
    const dir = path.resolve(process.argv[2] || path.join(path.dirname(fileURLToPath(import.meta.url)), '../coral-go/pkg/frontend/static'));
    const r = scanTree(dir);
    for (const x of r) console.log(`${x.file}:${x.line} ${x.attr}${x.quoted ? ' (quoted)' : ''}: \${${x.expr}}`);
    console.log(`${r.length} unprotected holes`);
}
