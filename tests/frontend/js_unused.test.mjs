// Dead JavaScript in the dashboard (#4974): a function, class or constant that nothing uses, an export no module imports, a window hook that nothing reads. They keep their imports alive, hide the code
// that matters in a 47,000-line tree, and after the inline handlers went (#3645) a whole layer of window hooks was left behind. This parses every static script (acorn: it is eslint's own parser, already
// here) and fails on:
//   1. a top-level function, class or const that nothing in its file refers to and no module exports (recursion inside itself does not count);
//   2. an export no OTHER product script uses (an import, a destructuring of importOnce, a member read such as mod.name): a test mentioning it does not count (#5015), what the suites need is imported by
//      test_hooks.js (window.__coralTest) or named in ALLOW;
//   3. a window hook (Object.assign(window, {...}), window.x = ..., or a name listed for lazyFns / lazyWindowFns) that no product script reads as window.x or window['x'] or names in a string it looks up
//      (modal_chrome's byWindow('hideAlertModal')); a test calling it does not count: the hooks that are left are the ones another script drives;
//   4. an import (import { a } from, or const { a } = await importOnce(...)) that nothing in its file uses.
// (Exports the Go bundler's generated entry imports, such as registerBundled, count as used.) A name that is used some other way is listed in ALLOW with the reason. Run it on its own: node js_unused.test.mjs [--list] (--list prints what it finds without failing).
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';
import * as acorn from 'acorn'; // eslint's parser (a dependency of eslint and jsdom, both in devDependencies)

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, '../..');
const st = path.join(root, 'coral-go/pkg/frontend/static');
const walk = (d, ok) => fs.readdirSync(d, { withFileTypes: true }).flatMap(e => {
    const p = path.join(d, e.name);
    return e.isDirectory() ? (['node_modules', 'vendor', 'css', 'fonts'].includes(e.name) ? [] : walk(p, ok)) : ok(p) ? [p] : [];
});
const rel = f => path.relative(st, f);

// name -> reason. Add a name only with the reason it is used in a way this scan cannot see.
export const ALLOW = new Map([
    ['__coralReady', 'the suites wait on it (ready.js): a promise-like with the page\'s startup parts, in 117 suites and the harness preload'],
    ['__coralTest', 'the namespace test_hooks.js installs when the page\'s boot data asks for it (dev build, CORAL_TEST_HOOKS=1)'],
    ['_resetForTests', 'artifact_readers.js remembers once per page whether this person may see who can read an artifact; the suite forgets it between its cases'],
    ['_setHubForTests', 'update_settings.js and team_cross_team_settings.js load lazily, so test_hooks.js cannot import them; update_channel_browser and team_messaging_tab_browser import the module by URL to draw the Hub view of the pane or tab'],
    ['_resetHubNavForTests', 'hub_nav.js loads lazily, so test_hooks.js cannot import it; its suite imports the module by URL'],
    ['savedLinkUrl', 'saved_link.js loads lazily, so test_hooks.js cannot import it; its suite imports the module by URL'],
]);

const idsOfPat = pat => !pat ? [] : pat.type === 'Identifier' ? [pat] : pat.type === 'ObjectPattern' ? pat.properties.flatMap(p => idsOfPat(p.value || p.argument))
    : pat.type === 'ArrayPattern' ? pat.elements.filter(Boolean).flatMap(idsOfPat) : pat.type === 'AssignmentPattern' ? idsOfPat(pat.left) : pat.type === 'RestElement' ? idsOfPat(pat.argument) : [];
const children = n => Object.keys(n).filter(k => k !== 'loc').flatMap(k => { const v = n[k]; return Array.isArray(v) ? v.filter(c => c && typeof c.type === 'string') : v && typeof v.type === 'string' ? [v] : []; });

/** Parses the static scripts and returns what the checks need. `text` is read for each file. */
export function scan(files) {
    const mods = new Map();
    for (const f of files) {
        const text = fs.readFileSync(f, 'utf8');
        const ast = acorn.parse(text, { ecmaVersion: 'latest', sourceType: 'module', allowAwaitOutsideFunction: true, locations: true });
        const m = { f, text, idRefs: new Map(), props: new Map(), strings: new Set(), decls: [], windowSets: [], windowReads: new Set(), lazyNames: new Set(), importSpecs: [] };
        const bump = (map, k) => map.set(k, (map.get(k) || 0) + 1);
        const declIds = new Set();
        const lazyArrays = new Set();
        (function walkNode(n, parent) {
            switch (n.type) {
                case 'FunctionDeclaration': case 'ClassDeclaration': if (n.id) declIds.add(n.id); break;
                case 'VariableDeclarator': for (const id of idsOfPat(n.id)) declIds.add(id); break;
                case 'ImportSpecifier': case 'ImportDefaultSpecifier': case 'ImportNamespaceSpecifier': declIds.add(n.local); m.importSpecs.push({ name: n.local.name, line: n.loc.start.line }); if (n.imported) bump(m.props, n.imported.name); break;
                case 'FunctionExpression': case 'ArrowFunctionExpression': for (const p of n.params) for (const id of idsOfPat(p)) declIds.add(id); if (n.id) declIds.add(n.id); break;
                case 'CatchClause': for (const id of idsOfPat(n.param)) declIds.add(id); break;
            }
            if (n.type === 'FunctionDeclaration') for (const p of n.params) for (const id of idsOfPat(p)) declIds.add(id);
            // the names a lazy stub is made for: lazyFns(load, ['a', 'b']) and lazyWindowFns(load, [...]) define hooks, they do not use them
            if (n.type === 'CallExpression' && n.callee.type === 'Identifier' && /^lazy(Window)?Fns$/.test(n.callee.name)) for (const a of n.arguments) if (a.type === 'ArrayExpression') { lazyArrays.add(a); for (const e of a.elements) if (e && e.type === 'Literal') m.lazyNames.add(e.value); }
            if (n.type === 'Literal' && typeof n.value === 'string' && !(parent && lazyArrays.has(parent))) m.strings.add(n.value);
            if (n.type === 'TemplateElement') m.strings.add(n.value.cooked || '');
            if (n.type === 'MemberExpression') {
                if (!n.computed && n.property.type === 'Identifier') bump(m.props, n.property.name);
                const isWindow = n.object.type === 'Identifier' && n.object.name === 'window';
                const assigned = parent && parent.type === 'AssignmentExpression' && parent.left === n;
                if (isWindow) {
                    const k = !n.computed ? n.property.name : n.property.type === 'Literal' ? String(n.property.value) : null;
                    if (k) { if (assigned) m.windowSets.push({ name: k, line: n.loc.start.line }); else m.windowReads.add(k); }
                }
            }
            if (n.type === 'CallExpression' && n.callee.type === 'MemberExpression' && n.callee.object.name === 'Object' && n.callee.property.name === 'assign' && n.arguments[0]?.type === 'Identifier' && n.arguments[0].name === 'window')
                for (const a of n.arguments.slice(1)) if (a.type === 'ObjectExpression') for (const pr of a.properties) if (pr.key) m.windowSets.push({ name: pr.key.name || pr.key.value, line: pr.loc.start.line, value: pr.shorthand ? pr.key.name : null });
            if (n.type === 'Property' && !n.computed && n.key.type === 'Identifier' && !n.shorthand) bump(m.props, n.key.name);
            if (n.type === 'ObjectPattern') for (const p of n.properties) if (p.type === 'Property' && !p.computed && p.key.type === 'Identifier') bump(m.props, p.key.name);
            if (n.type === 'Identifier' && !declIds.has(n)) {
                const skip = parent && ((parent.type === 'MemberExpression' && parent.property === n && !parent.computed) || (parent.type === 'Property' && parent.key === n && !parent.computed && !parent.shorthand)
                    || (parent.type === 'MethodDefinition' && parent.key === n) || (parent.type === 'PropertyDefinition' && parent.key === n) || parent.type === 'LabeledStatement' || parent.type === 'ImportSpecifier'
                    || (parent.type === 'ExportSpecifier' && parent.exported === n && parent.local !== n));
                if (!skip) bump(m.idRefs, n.name);
            }
            for (const c of children(n)) walkNode(c, n);
        })(ast, null);
        for (const n of ast.body) {
            let d = n, exported = false;
            if (n.type === 'ExportNamedDeclaration') { exported = true; if (n.declaration) d = n.declaration; else { for (const s of n.specifiers) m.decls.push({ name: s.local.name, exportedAs: s.exported.name, kind: 'export', exported: true, line: s.loc.start.line, spec: true }); continue; } }
            if (d.type === 'FunctionDeclaration' || d.type === 'ClassDeclaration') m.decls.push({ name: d.id.name, kind: d.type === 'FunctionDeclaration' ? 'function' : 'class', exported, line: d.loc.start.line, node: d });
            if (d.type === 'VariableDeclaration') for (const v of d.declarations) for (const id of idsOfPat(v.id)) m.decls.push({ name: id.name, kind: d.kind, exported, line: v.loc.start.line, node: v, fromImport: v.id.type !== 'Identifier' && /importOnce|import\(/.test(text.slice(v.start, v.end)) });
        }
        mods.set(f, m);
    }
    return mods;
}
const selfRefs = d => { if (!d.node || d.kind !== 'function') return 0; let c = 0; (function w(n) { if (n.type === 'Identifier' && n.name === d.name) c++; for (const k of children(n)) w(k); })(d.node.body); return c; };

/** What the checks find: { local, exports, hooks, imports } as arrays of "file:line name". */
export function findDead(mods, goText = '') {
    const word = (name, text) => new RegExp(`(?<![\\w$])${name.replace(/\$/g, '\\$')}(?![\\w$])`).test(text);
    // the Go bundler's generated entry naming it (registerBundled) keeps an export. A test mentioning it does not (#5015): what the suites need from the page
    // is in test_hooks.js, which imports it, or in ALLOW with the reason.
    const mentioned = name => word(name, goText);
    const out = { local: [], exports: [], hooks: [], imports: [] };
    for (const [f, m] of mods) {
        for (const d of m.decls) {
            const inFile = (m.idRefs.get(d.name) || 0) - selfRefs(d);
            const label = `${rel(f)}:${d.line} ${d.kind} ${d.name}`;
            if (ALLOW.has(d.name)) continue;
            if (d.fromImport) { if ((m.idRefs.get(d.name) || 0) === 0) out.imports.push(label); continue; }
            if (!d.exported) { if (inFile <= 0 && !m.decls.some(x => x.spec && x.name === d.name)) out.local.push(label); continue; }
            const pub = d.exportedAs || d.name;
            let other = 0; for (const [g, v] of mods) if (g !== f) other += (v.props.get(pub) || 0) + (v.idRefs.get(pub) || 0);
            if (other === 0 && inFile <= 0 && !mentioned(pub)) out.exports.push(label);
        }
        // an import nothing in the file uses (side-effect imports have no specifiers and are not looked at)
        for (const s of m.importSpecs) if ((m.idRefs.get(s.name) || 0) === 0 && !ALLOW.has(s.name)) out.imports.push(`${rel(f)}:${s.line} import ${s.name}`);
        for (const h of m.windowSets) {
            if (ALLOW.has(h.name)) continue;
            let read = false; for (const [, v] of mods) if (v.windowReads.has(h.name) || v.strings.has(h.name)) read = true;
            if (!read && !mentioned(h.name)) out.hooks.push(`${rel(f)}:${h.line} window.${h.name}`);
        }
        for (const nm of m.lazyNames) {
            if (ALLOW.has(nm)) continue;
            let read = false; for (const [, v] of mods) if (v.windowReads.has(nm) || v.strings.has(nm)) read = true;
            let used = false; for (const [, v] of mods) if ((v.idRefs.get(nm) || 0) > 0 && v !== m) used = true;
            if (!read && !used && !mentioned(nm) && !(m.idRefs.get(nm) > 0)) out.hooks.push(`${rel(f)} lazy ${nm}`);
        }
    }
    for (const k of Object.keys(out)) out[k] = [...new Set(out[k])].sort();
    return out;
}

// The scan itself, on small modules: what it finds and what it leaves alone.
import os from 'node:os';
{
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'js-unused-'));
    const w = (n, t) => { const p = path.join(dir, n); fs.writeFileSync(p, t); return p; };
    const a = w('a.js', `
        function deadLocal() {}
        function onlyRecursive(n) { return n ? onlyRecursive(n - 1) : 0; }
        function used() {}
        const deadConst = 1;
        export function usedElsewhere() {}
        export function usedByTestOnly() {}
        export function usedByNobody() {}
        export const viaProp = 1;
        import { gone, kept } from './b.js';
        kept(); used();
        Object.assign(window, { hookRead, hookString, hookTest, hookDead, hookOnWindowOnly });
        function hookRead() {} function hookString() {} function hookTest() {} function hookDead() {} function hookOnWindowOnly() {}
        window.directDead = () => {};
    `);
    const b = w('b.js', `
        import { usedElsewhere } from './a.js'; import * as ns from './a.js';
        export function kept() {} export function gone() {}
        usedElsewhere(); ns.viaProp; window.hookRead(); window.hookTest(); const f = name => window[name]; f('hookString');
    `);
    const dead = findDead(scan([a, b]), 'registerBundledUsed();');
    const names = list => list.map(x => x.split(' ').pop());
    assert.deepEqual(names(dead.local).sort(), ['deadConst', 'deadLocal', 'onlyRecursive'], 'a name nothing refers to, and a function only its own body refers to');
    assert.deepEqual(names(dead.exports), ['usedByTestOnly', 'usedByNobody'], 'an export nothing imports (an import and a member read count; a test mentioning it does not: the page\'s test surface is test_hooks.js)');
    assert.deepEqual(names(dead.hooks).sort(), ['window.directDead', 'window.hookDead', 'window.hookOnWindowOnly'], 'a window hook nothing reads or looks up by name (a test calling it does not count)');
    assert.deepEqual(names(dead.imports), ['gone'], 'an import nothing in the file uses');
    fs.rmSync(dir, { recursive: true });
}

const files = walk(st, p => p.endsWith('.js'));
const goText = walk(path.join(root, 'coral-go/pkg/frontend'), p => p.endsWith('.go') && !p.endsWith('_test.go')).map(p => fs.readFileSync(p, 'utf8')).join('\n'); // the bundler writes an entry that imports by name
if (process.argv[1] === fileURLToPath(import.meta.url)) {
    const dead = findDead(scan(files), goText);
    if (process.argv.includes('--list')) { for (const [k, v] of Object.entries(dead)) console.log(`== ${k} (${v.length})\n${v.join('\n')}`); }
    else {
        const msg = Object.entries(dead).filter(([, v]) => v.length).map(([k, v]) => `${k}:\n  ${v.join('\n  ')}`).join('\n');
        assert.equal(msg, '', `dead JavaScript (remove it, or name it in ALLOW with the reason):\n${msg}`);
        console.log('js_unused: ok (no unused top-level code, export or window hook)');
    }
}
