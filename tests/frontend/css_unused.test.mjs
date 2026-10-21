// A CSS class nothing builds (#3793, audit #3781 item 4): a rule for a class that no script, template, test or the Go side
// ever puts on an element styles nothing and hides real faults (the #3735 Send rule named an inline onclick that #3645 had
// removed, so it matched nothing for weeks). This fails on a class (or attribute) in a stylesheet that nothing produces.
// A class or id counts as built when its name is a token in a static script or template (comments do not count), in the Go sources,
// or when a script builds it from a prefix of two or more words (`event-type-${x}` or 'board-task-' + x); a name only a TEST mentions is
// dead (a test that looks for an element the page no longer draws is no reason to keep its rule: #4973). Classes of libraries that draw
// their own markup (CodeMirror, xterm, highlight.js, the icon font) are exempt. A name built some other way (a single-word prefix such as
// server-${status}, a value that comes from the server) is named in DYNAMIC below with the reason.
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

// Comments are not references: a class named only in a comment (or an HTML comment) is not drawn.
const noComments = (p, t) => p.endsWith('.js') ? t.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|\s)\/\/[^\n]*/g, '$1') : t.replace(/<!--[\s\S]*?-->/g, '');
const src = [...walk(st, p => /\.(js|html)$/.test(p)), ...walk(path.join(fe, 'templates'), p => p.endsWith('.html'))].map(p => noComments(p, read(p))).join('\n');
const other = textFiles([...walk(path.join(root, 'coral-go'), p => p.endsWith('.go') && !p.endsWith('_test.go')), ...walk(path.join(root, 'installers'), p => /\.(sh|ps1|wxs|plist|html|go)$/.test(p))].filter(p => !p.includes(`${path.sep}pkg${path.sep}frontend${path.sep}`)));

const tokens = new Set(src.match(/[A-Za-z_][A-Za-z0-9_-]*/g));
// A class a script builds from a prefix: `file-${kind}` (a template literal) or 'file-' + kind (a concatenation).
export const prefixesOf = text => new Set([...text.matchAll(/([A-Za-z][A-Za-z0-9_-]*[-_])\$\{/g)].map(m => m[1])
    .concat([...text.matchAll(/['"]([A-Za-z][A-Za-z0-9_-]*[-_])['"]\s*\+/g)].map(m => m[1])));
// A one-word prefix (server-, acf-, availability-) would excuse every class that starts with it: only prefixes of two or more words count.
const prefixes = new Set([...prefixesOf(src)].filter(p => p.split(/[-_]/).filter(Boolean).length >= 2));
// Classes drawn by libraries, or put on the page by the native shell, which no script of ours names.
const EXTERNAL = /^(cm-|xterm|hljs|material-icons|ͼ)/;
const builtFrom = (c, pre) => [...pre].some(p => c.startsWith(p));
// Built some other way, each with the reason (a class whose name comes from a value the page gets, not from a literal).
const DYNAMIC = [
    [/^server-(online|unauthorized|unknown|unreachable|stale)$/, 'server_status.js builds server-${status} from the machine state (one-word prefix)'],
    [/^availability-(waiting|task_idle|available)$/, 'team_availability.js builds availability-${key} from the summary keys (one-word prefix)'],
    [/^md-(h|partial)$/, 'vendor/safe-markdown.js (the renderer shared with Coral Hub, byte for byte; vendor/ is not scanned) builds the md-h heading and the md-partial "shown in part" note'],
];
const dynamic = c => DYNAMIC.some(([re]) => re.test(c));
const built = c => tokens.has(c) || other.includes(c) || EXTERNAL.test(c) || builtFrom(c, prefixes) || dynamic(c);

// The prefix rule itself (Security's question on #3793): both ways of building a class name from a prefix are seen, a name that is
// merely similar is not, and a plain concatenation of two names is not a prefix.
const sample = prefixesOf('el.className = `row-${kind} x`; b.className = "cost-agent-" + type; c.className = "plain" + z; d = "abc" + e;');
assert.deepEqual([...sample].sort(), ['cost-agent-', 'row-'], 'the template and the concatenation forms give their prefixes');
assert.equal(builtFrom('row-error', sample), true);
assert.equal(builtFrom('cost-agent-claude', sample), true);
assert.equal(builtFrom('rowing', sample), false, 'a similar name is not built from the prefix');
assert.equal(builtFrom('plainer', sample), false, 'a name joined from two plain names is no prefix');

// What counts as a reference (#4973): comments do not, a URL's // is not a comment, and a one-word prefix excuses nothing.
assert.equal(noComments('a.js', "x('ok'); // .gone\nfetch('http://h/.live-url');").includes('.gone'), false, 'a line comment is not a reference');
assert.equal(noComments('a.js', "/* .gone */ y('.kept')").includes('.gone'), false, 'a block comment is not a reference');
assert.ok(noComments('a.js', "fetch('http://h/.live-url')").includes('.live-url'), 'a URL is not a comment');
assert.equal(noComments('a.html', '<!-- .gone --><b class="kept"></b>').includes('gone'), false, 'an HTML comment is not a reference');
assert.equal(builtFrom('server-online', new Set([...prefixesOf('c = `server-${s}`')].filter(p => p.split(/[-_]/).filter(Boolean).length >= 2))), false, 'a one-word prefix excuses nothing');

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
            // An id nothing draws or looks up styles nothing either (a colour like #fff in a selector is not one: ids start with a letter and are 3+ characters).
            for (const c of bare.replace(/\[[^\]]*\]/g, ' ').matchAll(/#(-?[A-Za-z_][\w-]{2,})/g)) if (!tokens.has(c[1]) && !other.includes(c[1])) dead.push(`${path.basename(f)}: #${c[1]}`);
            for (const a of bare.matchAll(/\[\s*([a-zA-Z_:][\w:-]*)/g)) {
                if (![...DEAD_ATTRS_OK].some(ok => a[1] === ok || a[1].startsWith(ok)) && !src.includes(a[1]) && !other.includes(a[1])) dead.push(`${path.basename(f)}: [${a[1]}]`);
            }
        }
    }
}
const uniq = [...new Set(dead)].sort();
assert.deepEqual(uniq, [], `a stylesheet names classes or attributes nothing builds (remove the rule, or build the class):\n  ${uniq.join('\n  ')}`);
console.log('css_unused: ok (every stylesheet class and attribute is built somewhere)');
