// No static script of the dashboard opens a browser dialog (#6329): window.prompt, window.alert and window.confirm do not exist in the native app (the Remind agent button did nothing there), and a
// page that asks through them cannot be tested, styled or read by a screen reader the way our own dialogs are. This parses every static script (acorn, as js_unused does) and fails on a CALL to
// prompt, alert or confirm that is the browser's: the bare global (not a name the file declares itself, such as a `confirm` parameter or a local function), window.x, globalThis.x or self.x, with
// the dot or the bracket. The dashboard's own dialogs are showConfirmModal, showPromptModal, showAlertModal and the dialogs of their own modules. vendor/ is not ours and is not scanned.
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';
import * as acorn from 'acorn';

const here = path.dirname(fileURLToPath(import.meta.url));
const st = path.resolve(here, '../../coral-go/pkg/frontend/static');
const NATIVE = new Set(['prompt', 'alert', 'confirm']);
const GLOBALS = new Set(['window', 'globalThis', 'self']);
const walk = (d) => fs.readdirSync(d, { withFileTypes: true }).flatMap(e => {
    const p = path.join(d, e.name);
    return e.isDirectory() ? (['node_modules', 'vendor', 'css', 'fonts'].includes(e.name) ? [] : walk(p)) : p.endsWith('.js') ? [p] : [];
});

/** The native dialog calls in a script's text: [{ name, line }]. */
export function nativeDialogCalls(text) {
    const ast = acorn.parse(text, { ecmaVersion: 'latest', sourceType: 'module', allowAwaitOutsideFunction: true, locations: true });
    const declared = new Set(); // names the file binds itself (a parameter, a const, a function): a call of those is its own
    const calls = [];
    const idsOf = (pat) => !pat ? [] : pat.type === 'Identifier' ? [pat.name] : pat.type === 'ObjectPattern' ? pat.properties.flatMap(p => idsOf(p.value || p.argument))
        : pat.type === 'ArrayPattern' ? pat.elements.filter(Boolean).flatMap(idsOf) : pat.type === 'AssignmentPattern' ? idsOf(pat.left) : pat.type === 'RestElement' ? idsOf(pat.argument) : [];
    (function walkNode(n) {
        if (!n || typeof n.type !== 'string') return;
        if (n.type === 'FunctionDeclaration' || n.type === 'ClassDeclaration') { if (n.id) declared.add(n.id.name); }
        if (n.type === 'VariableDeclarator') idsOf(n.id).forEach(x => declared.add(x));
        if (/Function/.test(n.type)) { for (const p of n.params) idsOf(p).forEach(x => declared.add(x)); if (n.id) declared.add(n.id.name); }
        if (n.type === 'CatchClause' && n.param) idsOf(n.param).forEach(x => declared.add(x));
        if (n.type === 'ImportSpecifier' || n.type === 'ImportDefaultSpecifier') declared.add(n.local.name);
        if (n.type === 'CallExpression') {
            const c = n.callee;
            if (c.type === 'Identifier' && NATIVE.has(c.name)) calls.push({ name: c.name, line: n.loc.start.line, bare: true });
            else if (c.type === 'MemberExpression' && c.object.type === 'Identifier' && GLOBALS.has(c.object.name)) {
                const key = !c.computed && c.property.type === 'Identifier' ? c.property.name : (c.computed && c.property.type === 'Literal' ? c.property.value : null);
                if (NATIVE.has(key)) calls.push({ name: key, line: n.loc.start.line, bare: false });
            }
        }
        for (const k of Object.keys(n)) { if (k === 'loc') continue; const v = n[k]; if (Array.isArray(v)) v.forEach(walkNode); else if (v && typeof v.type === 'string') walkNode(v); }
    })(ast);
    return calls.filter(c => !(c.bare && declared.has(c.name))).map(({ name, line }) => ({ name, line }));
}

// The scan itself, shown to catch what it is for (mutants) and to leave alone what it must (a file's own confirm).
assert.deepEqual(nativeDialogCalls("const n = Number(window.prompt('x'));").map(c => c.name), ['prompt'], 'window.prompt is found');
assert.deepEqual(nativeDialogCalls("alert('x');").map(c => c.name), ['alert'], 'a bare alert is found');
assert.deepEqual(nativeDialogCalls("if (confirm('sure?')) go();").map(c => c.name), ['confirm'], 'a bare confirm is found');
assert.deepEqual(nativeDialogCalls("globalThis.confirm('x'); self['alert']('y');").map(c => c.name), ['confirm', 'alert'], 'globalThis, self and the bracket form are found');
assert.deepEqual(nativeDialogCalls("const confirm = window.showConfirmModal; confirm('Paste?', 'x', go);"), [], "a file's own confirm is not the browser's");
assert.deepEqual(nativeDialogCalls("function ask(prompt) { return prompt('x'); }"), [], 'a parameter named prompt is not the browser\'s');
assert.deepEqual(nativeDialogCalls("window.showPromptModal('t', 'l', '', () => {}); el.prompt('x');"), [], 'our own dialogs and a method of another object are not it');

const found = [];
for (const f of walk(st)) for (const c of nativeDialogCalls(fs.readFileSync(f, 'utf8'))) found.push(`${path.relative(st, f)}:${c.line} ${c.name}()`);
assert.deepEqual(found, [], `a dashboard script opens a browser dialog (the native app has none): use showConfirmModal, showPromptModal, showAlertModal or a dialog of its own:\n  ${found.join('\n  ')}`);
console.log('no_native_dialogs: ok (no static script calls window.prompt, alert or confirm; 7 negative controls)');
