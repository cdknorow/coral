// One-time codemod: wrap template-literal holes that land in HTML attributes (see attr_holes.mjs).
import fs from 'node:fs';
import path from 'node:path';
import { scanAttributes, checkAttribute } from './attr_holes.mjs';

const dir = path.resolve(process.argv[2]);
const NUMERIC_NAME = /(^|[._])(id|ids|taskId|task_id|workflow_id|run_id|index|idx|i|j|n|revision|num|number|count|line)$|[a-z]Id$|Idx$|Index$/;
let changed = 0, left = [];
for (const f of fs.readdirSync(dir).filter(x => x.endsWith('.js')).sort()) {
    const file = path.join(dir, f);
    let src = fs.readFileSync(file, 'utf8');
    const edits = [];
    for (const a of scanAttributes(src)) {
        for (const p of checkAttribute(src, a)) {
            const expr = src.slice(p.start, p.end);
            const t = expr.trim();
            const isHandler = /^on[a-z]+$/.test(p.attr);
            let repl = null;
            const m = /^(escapeAttr|escapeHtml|esc|escAttr|escHtml)\(([\s\S]*)\)$/.exec(t);
            if (isHandler) {
                if (p.quoted) repl = m ? `jsStr(${m[2]})` : `jsStr(${t})`;
                else if (NUMERIC_NAME.test(t) && /^[\w$.]+$/.test(t)) repl = `Number(${t})`;
            } else {
                repl = NUMERIC_NAME.test(t) && /^[\w$.]+$/.test(t) ? `Number(${t})` : `escapeAttr(${t})`;
                if (/^(escapeAttr|escapeHtml|esc)\(/.test(t)) repl = null;
            }
            if (repl) edits.push([p.start, p.end, repl]); else left.push(`${f}:${p.line} ${p.attr}: ${t.slice(0, 70)}`);
        }
    }
    if (!edits.length) continue;
    edits.sort((a, b) => b[0] - a[0]);
    for (const [s, e, r] of edits) src = src.slice(0, s) + r + src.slice(e);
    fs.writeFileSync(file, src);
    changed += edits.length;
}
console.log(`rewrote ${changed} holes; ${left.length} need manual review`);
for (const l of left) console.log('  ' + l);
