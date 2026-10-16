// A CSS class nothing builds (#3793, audit #3781 item 4): a rule for a class that no script, template, test or the Go side
// ever puts on an element styles nothing and hides real faults (the #3735 Send rule named an inline onclick that #3645 had
// removed, so it matched nothing for weeks). This fails on a class (or attribute) in a stylesheet that nothing produces.
// A class counts as built when its name is a token in a static script or template, in a test, in the Go sources, or when a
// script builds it from a prefix (`prefix-${x}` or 'prefix-' + x); classes of libraries that draw their own markup
// (CodeMirror, xterm, highlight.js, the icon font) are exempt.
// To keep a class that is built some other way, name it in EXTERNAL below with the reason.
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, '../..');
const fe = path.join(root, 'coral-go/pkg/frontend');
const st = path.join(fe, 'static');
const walk = (d, ok) => fs.readdirSync(d, { withFileTypes: true }).flatMap(e => {
    const p = path.join(d, e.name);
    return e.isDirectory() ? (e.name === 'node_modules' || e.name === 'vendor' ? [] : walk(p, ok)) : ok(p) ? [p] : [];
});
const read = p => fs.readFileSync(p, 'utf8');
const textFiles = ps => ps.map(read).join('\n');

const src = textFiles([...walk(st, p => /\.(js|html)$/.test(p)), ...walk(path.join(fe, 'templates'), p => p.endsWith('.html'))]);
const tests = textFiles(walk(here, p => /\.(js|mjs)$/.test(p)));
const other = textFiles([...walk(path.join(root, 'coral-go'), p => p.endsWith('.go') && !p.endsWith('_test.go')), ...walk(path.join(root, 'installers'), p => /\.(sh|ps1|wxs|plist|html|go)$/.test(p))].filter(p => !p.includes(`${path.sep}pkg${path.sep}frontend${path.sep}`)));

const tokens = new Set(src.match(/[A-Za-z_][A-Za-z0-9_-]*/g));
const prefixes = new Set([...(src.matchAll(/([A-Za-z][A-Za-z0-9_-]*[-_])\$\{/g))].map(m => m[1])
    .concat([...src.matchAll(/['"]([A-Za-z][A-Za-z0-9_-]*[-_])['"]\s*\+/g)].map(m => m[1])));
// Classes drawn by libraries, or put on the page by the native shell, which no script of ours names.
const EXTERNAL = /^(cm-|xterm|hljs|material-icons|ͼ)/;
const built = c => tokens.has(c) || tests.includes(c) || other.includes(c) || EXTERNAL.test(c) || [...prefixes].some(p => c.startsWith(p));

const DEAD_ATTRS_OK = new Set(['aria-', 'data-theme', 'lang', 'type', 'href', 'src', 'hidden', 'disabled', 'open', 'checked', 'role', 'dir', 'title', 'tabindex', 'value',
    'placeholder', 'name', 'class', 'id', 'style', 'target', 'rel', 'draggable', 'contenteditable', 'readonly', 'selected', 'popover', 'inert']);
const dead = [];
for (const f of walk(path.join(st, 'css'), p => p.endsWith('.css'))) {
    const css = read(f).replace(/\/\*[\s\S]*?\*\//g, '');
    for (const m of css.matchAll(/([^{}@;]+)\{/g)) {
        const head = m[1];
        if (/^\s*[\d.%\s,]+$/.test(head) || /^\s*(from|to)\b/.test(head)) continue;
        for (const sel of head.split(',')) {
            const bare = sel.replace(/:not\([^)]*\)/g, '').replace(/"[^"]*"/g, '');
            for (const c of bare.replace(/\[[^\]]*\]/g, ' ').matchAll(/\.(-?[A-Za-z_][\w-]*)/g)) if (!built(c[1])) dead.push(`${path.basename(f)}: .${c[1]}`);
            for (const a of bare.matchAll(/\[\s*([a-zA-Z_:][\w:-]*)/g)) {
                if (![...DEAD_ATTRS_OK].some(ok => a[1] === ok || a[1].startsWith(ok)) && !src.includes(a[1]) && !tests.includes(a[1]) && !other.includes(a[1])) dead.push(`${path.basename(f)}: [${a[1]}]`);
            }
        }
    }
}
const uniq = [...new Set(dead)].sort();
assert.deepEqual(uniq, [], `a stylesheet names classes or attributes nothing builds (remove the rule, or build the class):\n  ${uniq.join('\n  ')}`);
console.log('css_unused: ok (every stylesheet class and attribute is built somewhere)');
