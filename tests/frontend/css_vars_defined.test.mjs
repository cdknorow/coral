// Every CSS variable a stylesheet reads is defined somewhere (#5146, found by the #5135 sweep). A var(--name) whose name nothing declares has no value: with no fallback the
// property is invalid, and with one the fallback is a colour picked for one theme (the light theme drew white hairlines and a white hover on white, and the same red and amber
// as the dark one: --border-subtle, --hover-bg, --color-error, --color-warning). This fails on a var(--name), with or without a fallback, that no stylesheet declares (--name:
// in any rule) and no script, template or Go theme sets (setProperty, a style string, a theme map). A name that is read and truly never set is named in
// OPTIONAL below with the reason (the theme editor's table in internal/server/routes/themes.go counts as setting a name). Run it on its own: node css_vars_defined.test.mjs
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, '../..');
const fe = path.join(root, 'coral-go/pkg/frontend');
const walk = (d, ok) => fs.readdirSync(d, { withFileTypes: true }).flatMap(e => {
    const p = path.join(d, e.name);
    return e.isDirectory() ? (['node_modules', 'vendor'].includes(e.name) ? [] : walk(p, ok)) : ok(p) ? [p] : [];
});
const read = p => fs.readFileSync(p, 'utf8');

// name -> reason. Add a name only with the reason it is read without a declaration.
export const OPTIONAL = new Map([
    // (none: a name a custom theme may set is listed in the theme editor's Go table, which counts as a declaration)
]);

/** The var() names a stylesheet reads, each with its file. */
export function readNames(files) {
    const used = new Map();
    for (const f of files) {
        const css = read(f).replace(/\/\*[\s\S]*?\*\//g, '');
        for (const m of css.matchAll(/var\(\s*(--[A-Za-z0-9_-]+)/g)) if (!used.has(m[1])) used.set(m[1], path.relative(fe, f));
    }
    return used;
}

export function declaredNames(cssFiles, otherFiles) {
    const declared = new Set();
    for (const f of cssFiles) for (const m of read(f).replace(/\/\*[\s\S]*?\*\//g, '').matchAll(/(--[A-Za-z0-9_-]+)\s*:/g)) declared.add(m[1]);
    for (const f of otherFiles) for (const m of read(f).matchAll(/(--[A-Za-z][A-Za-z0-9_-]*)/g)) declared.add(m[1]);
    return declared;
}

const cssFiles = walk(path.join(fe, 'static/css'), p => p.endsWith('.css'));
const other = [...walk(path.join(fe, 'static'), p => /\.(js|html)$/.test(p) && !p.includes(`${path.sep}css${path.sep}`)), ...walk(path.join(fe, 'templates'), p => p.endsWith('.html')),
    ...walk(path.join(root, 'coral-go/internal'), p => p.endsWith('.go') && !p.endsWith('_test.go'))];

const used = readNames(cssFiles);
const declared = declaredNames(cssFiles, other);
const missing = [...used].filter(([n]) => !declared.has(n) && !OPTIONAL.has(n)).map(([n, f]) => `${n} (read in ${f})`);
assert.deepEqual(missing, [], `a CSS variable is read but nothing declares it (declare it in variables.css, or use the token that exists):\n  ${missing.join('\n  ')}`);
// An OPTIONAL entry that is declared now, or no longer read, is stale.
for (const n of OPTIONAL.keys()) assert.ok(used.has(n) && !declared.has(n), `${n} is listed as optional but ${used.has(n) ? 'is declared now' : 'is not read any more'}: drop it from OPTIONAL`);

// The scan itself: it sees a name that is read and never declared (with and without a fallback), and one that is declared.
{
    const tmp = fs.mkdtempSync(path.join(fs.realpathSync(process.env.TMPDIR || os.tmpdir()), 'cssvars-'));
    try {
        const f = path.join(tmp, 'a.css');
        fs.writeFileSync(f, ':root { --ok: 1px; }\n.a { border: var(--ok); color: var(--nope-a); background: var(--nope-b, red); /* var(--in-comment) */ }');
        const u = readNames([f]), d = declaredNames([f], []);
        const bad = [...u.keys()].filter(n => !d.has(n)).sort();
        assert.deepEqual(bad, ['--nope-a', '--nope-b'], 'the scan finds the undeclared names, with and without a fallback, and ignores comments');
    } finally { fs.rmSync(tmp, { recursive: true, force: true }); }
}
console.log(`css_vars_defined: ok (${used.size} variables read, all declared; ${OPTIONAL.size} optional)`);
