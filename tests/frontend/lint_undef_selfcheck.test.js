// The no-undef lint must actually bite: a name used without an import is exactly what jsdom
// suites (which stub modules) cannot see and real Chrome reports as a ReferenceError.
const { spawnSync } = require('child_process');
const path = require('path');
const assert = require('assert');

const frontendDir = path.resolve(__dirname, '../../coral-go/pkg/frontend');
const eslint = path.resolve(__dirname, 'node_modules/eslint/bin/eslint.js'); // run by node: the .bin entry is a shell script (a .cmd on Windows)
const config = path.resolve(__dirname, 'eslint.config.mjs');

function lint(code) {
    return spawnSync(process.execPath, [eslint, '-c', config, '--stdin', '--stdin-filename', 'static/_selfcheck.js'], {
        cwd: frontendDir, input: code, encoding: 'utf8',
    });
}

// Missing import: the original bug class (escapeAttr in changed_files.js).
let r = lint("import { escapeHtml } from './utils.js';\nexport const f = (s) => escapeAttr(escapeHtml(s));\n");
assert.notStrictEqual(r.status, 0, 'a missing import must fail the lint');
assert.match(r.stdout, /'escapeAttr' is not defined/);

// A typeof guard on an unimported function (the modals.js case) is still an undefined module name.
r = lint("export function g() { if (typeof refreshChangedFiles === 'function') refreshChangedFiles(); }\n");
assert.notStrictEqual(r.status, 0);
assert.match(r.stdout, /'refreshChangedFiles' is not defined/);

// Declared globals: browser APIs and the script-tag libraries are fine.
r = lint("export const ok = () => document.title + marked.parse('x') + DOMPurify.sanitize('y') + hljs.highlightAuto('z') + new Terminal();\n");
assert.strictEqual(r.status, 0, r.stdout + r.stderr);

// Properly imported names are fine.
r = lint("import { escapeAttr } from './utils.js';\nexport const f = (s) => escapeAttr(s);\n");
assert.strictEqual(r.status, 0, r.stdout + r.stderr);

console.log('lint_undef_selfcheck: ok');
