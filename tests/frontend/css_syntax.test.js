// Every stylesheet in static/css parses the way its author meant (#5725, from coral-hub's report). A browser drops what it cannot parse without a word: three
// declarations left at the top of layout.css after a rule was cut made the next rule's selector invalid, so the whole `.welcome` rule after them was never applied.
// A small tokenizer (comments, strings, url(), braces) lists, for each file: a comment that never ends, a string that never ends, braces that do not balance, and a
// declaration (a `;` in a rule list, outside an @rule) where a rule should be: the orphans such a cut leaves. No dependency; run: node css_syntax.test.js
'use strict';
const fs = require('node:fs');
const path = require('node:path');

const DIR = path.resolve(__dirname, '../../coral-go/pkg/frontend/static/css');
const GROUPS = /^@(media|supports|layer|container|document|scope|starting-style|keyframes|-webkit-keyframes)\b/i;

function check(text) {
    const errors = [];
    const stack = []; // each open block: { kind: 'rules' | 'decls', line }
    let line = 1, i = 0, prelude = '', preludeLine = 1;
    const ctx = () => (stack.length ? stack[stack.length - 1].kind : 'rules');
    const note = (msg, at = line) => errors.push(`line ${at}: ${msg}`);
    while (i < text.length) {
        const c = text[i];
        if (c === '\n') { line++; prelude += c; i++; continue; }
        if (c === '/' && text[i + 1] === '*') {
            const end = text.indexOf('*/', i + 2);
            const start = line;
            if (end < 0) { note('a comment that never ends', start); return errors; }
            line += (text.slice(i, end).match(/\n/g) || []).length; i = end + 2; continue;
        }
        if (c === '"' || c === "'") {
            let j = i + 1;
            while (j < text.length && text[j] !== c) { if (text[j] === '\\') j++; if (text[j] === '\n') { note('a string that never ends'); break; } j++; }
            prelude += text.slice(i, j + 1); i = j + 1; continue;
        }
        if (c === '(' && /url$/i.test(prelude.trimEnd().slice(-3)) ) { // url( ... ) with no quotes may hold ; and braces
            const end = text.indexOf(')', i); if (end < 0) { note('url( that never ends'); return errors; }
            prelude += text.slice(i, end + 1); line += (text.slice(i, end).match(/\n/g) || []).length; i = end + 1; continue;
        }
        if (c === '{') {
            const p = prelude.trim();
            const kind = ctx() === 'rules' && GROUPS.test(p) ? 'rules' : 'decls';
            if (ctx() === 'rules' && p === '') note('a block with no selector');
            stack.push({ kind, line: preludeLine }); prelude = ''; i++; continue;
        }
        if (c === '}') {
            if (!stack.length) note('a } with no block open'); else stack.pop();
            prelude = ''; i++; continue;
        }
        if (c === ';') {
            const p = prelude.trim();
            if (ctx() === 'rules' && p && !p.startsWith('@')) note(`a declaration where a rule should be (it swallows the next rule's selector): "${p.replace(/\s+/g, ' ').slice(0, 60)}"`, preludeLine);
            prelude = ''; i++; continue;
        }
        if (!prelude.trim()) preludeLine = line;
        prelude += c; i++;
    }
    for (const b of stack) note('a { that is never closed', b.line);
    return errors;
}

// the checker itself: it must see each fault it exists for
const samples = {
    'an orphan declaration': '.a { color: red; }\n font-size: 1px;\n.b { color: blue; }\n',
    'an unclosed block': '.a { color: red;\n.b { color: blue; }\n',
    'a stray }': '.a { color: red; }\n}\n',
    'an unterminated comment': '.a { color: red; } /* never ends\n.b { color: blue; }\n',
};
for (const [name, css] of Object.entries(samples)) if (!check(css).length) { console.error(`[FAIL] the checker misses ${name}`); process.exit(1); }
const fine = '@charset "utf-8";\n@import url(x.css);\n@media (min-width: 1px) { .a { color: red; background: url(data:image/png;base64,AAAA); } }\n@keyframes k { from { opacity: 0; } to { opacity: 1; } }\n.a { color: red; &:hover { color: blue; } }\n/* a ; in a comment { */\n.b::after { content: "};"; }\n';
if (check(fine).length) { console.error('[FAIL] the checker flags valid css: ' + check(fine).join('; ')); process.exit(1); }

const files = fs.readdirSync(DIR).filter(f => f.endsWith('.css')).sort();
let bad = 0;
for (const f of files) {
    const errors = check(fs.readFileSync(path.join(DIR, f), 'utf8'));
    for (const e of errors) { bad++; console.error(`[FAIL] css/${f} ${e}`); }
}
if (bad) { console.error(`${bad} css syntax fault(s): a browser drops the rule they touch`); process.exit(1); }
console.log(`css_syntax: ok (${files.length} stylesheets parse: braces balance, no comment or string left open, no declaration outside a rule)`);
