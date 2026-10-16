// Every browser storage access goes through safe_storage.js (#4116, Security's storage audit #4113). A direct localStorage or
// sessionStorage use throws a SecurityError in a browser that blocks site storage, and a module-level one took the dashboard down at start.
// AST scan of every dashboard module: any reference to the localStorage or sessionStorage globals (or window.localStorage and the like)
// outside safe_storage.js fails. theme_init.js is a classic script in <head> (no imports; its read has its own try/catch).
//   node storage_lint.test.js
const fs = require('node:fs');
const path = require('node:path');
const acorn = require('acorn');

const STATIC = path.resolve(__dirname, '../../coral-go/pkg/frontend/static');
const ALLOWED = new Set(['safe_storage.js', 'theme_init.js']);
const NAMES = new Set(['localStorage', 'sessionStorage']);

function walk(dir, out = []) {
    for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
        const p = path.join(dir, e.name);
        if (e.isDirectory()) { if (!['vendor', '_src', 'node_modules'].includes(e.name)) walk(p, out); } else if (e.name.endsWith('.js')) out.push(p);
    }
    return out;
}

function visit(node, fn) {
    if (!node || typeof node.type !== 'string') return;
    fn(node);
    for (const k of Object.keys(node)) {
        const v = node[k];
        if (Array.isArray(v)) v.forEach(c => visit(c, fn));
        else if (v && typeof v.type === 'string') visit(v, fn);
    }
}

// The scan has to see what it claims to: a fixture with each spelling must be caught.
function scan(src) {
    const hits = [];
    const ast = acorn.parse(src, { ecmaVersion: 'latest', sourceType: 'module', locations: true, allowHashBang: true });
    visit(ast, n => {
        if (n.type === 'Identifier' && NAMES.has(n.name)) hits.push(n.loc.start.line);
        if (n.type === 'Literal' && typeof n.value === 'string' && NAMES.has(n.value)) hits.push(n.loc.start.line); // window['localStorage']
    });
    // A property key such as { localStorage: x } or obj.localStorage is also an Identifier; both are reported, which is what we want for window.localStorage.
    return hits;
}
for (const spelling of ['localStorage.getItem("a")', 'window.localStorage.setItem("a","b")', 'const s = sessionStorage;', 'globalThis["localStorage"]', 'const { localStorage: s } = window;']) {
    if (!scan(spelling).length) { console.error('storage_lint self-check: missed ' + spelling); process.exit(1); }
}
if (scan('// localStorage in a comment\nconst x = "not storage";').length) { console.error('storage_lint self-check: flagged a comment'); process.exit(1); }

const problems = [];
for (const f of walk(STATIC)) {
    const base = path.basename(f);
    if (ALLOWED.has(base) && path.dirname(f) === STATIC) continue;
    for (const line of scan(fs.readFileSync(f, 'utf8'))) problems.push(`${path.relative(STATIC, f)}:${line} uses browser storage directly: import { browserLocal, browserSession } from './safe_storage.js'`);
}
if (problems.length) { console.error('storage_lint: FAIL\n  ' + problems.join('\n  ')); process.exit(1); }
console.log('storage_lint: ok (no direct localStorage or sessionStorage use outside safe_storage.js)');
