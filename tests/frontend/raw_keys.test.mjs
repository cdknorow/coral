// raw_keys.js: the keys a button may send to an agent's terminal, and the bytes for them.
import assert from 'node:assert/strict';
import { rawKeySeq, rawKeyOk } from '../../coral-go/pkg/frontend/static/raw_keys.js';

assert.equal(rawKeySeq('Escape'), '\x1b');
assert.equal(rawKeySeq('Tab'), '\t');
assert.equal(rawKeySeq('Up'), '\x1b[A');
assert.equal(rawKeySeq('Right'), '\x1b[C');
assert.equal(rawKeySeq('Enter'), '\r');
assert.equal(rawKeySeq('C-c'), '\x03', 'Ctrl-C is the control character 3');
assert.equal(rawKeySeq('C-a'), '\x01');
assert.equal(rawKeySeq('C-z'), '\x1a');
for (const s of ['|', '~', '/', '-']) assert.equal(rawKeySeq(s), s, `${s} is typed as itself`);
for (const bad of ['C-C', 'C-1', 'C-', 'M-x', 'x', 'a', ';', '\\', '`', 'F1', 'Esc', 'escape', '', 'Up,Down', null, undefined, 3, {}, 'toString', '__proto__', 'constructor']) assert.equal(rawKeyOk(bad), false, `${JSON.stringify(bad)} is refused`);
assert.equal(rawKeyOk('BSpace'), true);
console.log('raw_keys: ok');
