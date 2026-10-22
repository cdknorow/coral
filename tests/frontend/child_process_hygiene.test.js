// Every process a suite or a helper starts goes through helpers/child_process.js, which hides the console window on Windows (#204): a direct
// require/import of child_process would put a window on the person's screen for each go, node, git or sqlite3 it starts.
//   node child_process_hygiene.test.js
'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const walk = d => fs.readdirSync(d, { withFileTypes: true }).flatMap(e => e.name === 'node_modules' ? []
    : e.isDirectory() ? walk(path.join(d, e.name)) : /\.(c|m)?js$/.test(e.name) ? [path.join(d, e.name)] : []);
const OWN = path.join(__dirname, 'helpers', 'child_process.js');
const direct = /(?:require\s*\(|\bfrom\s+|\bimport\s*\()\s*['"](?:node:)?child_process['"]/;

const bad = walk(__dirname).filter(f => f !== OWN && direct.test(fs.readFileSync(f, 'utf8'))).map(f => path.relative(__dirname, f));
assert.deepEqual(bad, [], 'these start processes through node:child_process directly; use helpers/child_process.js (windowsHide on)');

// The wrapper puts windowsHide where the options belong and keeps one the caller set.
const { hidden } = require('./helpers/child_process.js');
assert.deepEqual(hidden(['go']), ['go', { windowsHide: true }]);
assert.deepEqual(hidden(['go', ['a']]), ['go', ['a'], { windowsHide: true }]);
assert.deepEqual(hidden(['go', ['a'], { cwd: 'x' }]), ['go', ['a'], { windowsHide: true, cwd: 'x' }]);
assert.deepEqual(hidden(['go', { cwd: 'x' }]), ['go', { windowsHide: true, cwd: 'x' }]);
assert.deepEqual(hidden(['go', ['a'], { windowsHide: false }]), ['go', ['a'], { windowsHide: false }]);
const cb = () => {};
assert.deepEqual(hidden(['go', ['a'], cb]), ['go', ['a'], { windowsHide: true }, cb]);
console.log('child_process_hygiene: ok (no direct child_process use in ' + walk(__dirname).length + ' files)');
