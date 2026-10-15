// A failing suite's [FAIL] lines never leave the runner's console output (#3609): the last lines are
// shown as before, and every [FAIL] line above them is printed too, once each and in order.
//   node failing_output.test.mjs
import assert from 'node:assert/strict';
import { failingOutput } from './failing_output.mjs';

const lines = n => Array.from({ length: n }, (_, i) => `line ${i + 1}`);

// Short output: printed whole, unchanged.
assert.deepEqual(failingOutput(lines(5).join('\n')), lines(5));
assert.deepEqual(failingOutput('\n' + lines(30).join('\n') + '\n\n'), lines(30), 'exactly 30 lines: whole, trimmed');

// Long output with no hidden [FAIL]: a note, then the last 30, as before.
let out = failingOutput(lines(40).join('\n'));
assert.equal(out.length, 31);
assert.match(out[0], /^… 10 earlier lines not shown \(CDP_FULL=1 prints them; results\.json has them\)$/);
assert.deepEqual(out.slice(1), lines(40).slice(-30));

// Hidden [FAIL] lines: each once, in order, above the last lines; the indented and the duplicated ones too.
const early = [
  '[PASS] empty 1400x900 dark: the heading',
  '[FAIL] empty 1400x900 dark: the heading — 0',
  '      [FAIL] empty phone dark: Delete — 10',
  '[FAIL] empty 1400x900 dark: the heading — 0',
  'not a failure: the word FAIL alone',
  '[FAIL] shown later too',
];
const tail = [...lines(28), '[FAIL] shown later too', '0/1 passed'];
out = failingOutput([...early, ...lines(20), ...tail].join('\n'));
assert.equal(out[0], '… 26 earlier lines not shown (CDP_FULL=1 prints them; results.json has them); their [FAIL] lines:');
assert.deepEqual(out.slice(1, 3), ['[FAIL] empty 1400x900 dark: the heading — 0', '      [FAIL] empty phone dark: Delete — 10'],
  'hidden FAIL lines once each, in order; one already in the last lines is not repeated');
assert.equal(out[3], '… the last lines:');
assert.deepEqual(out.slice(4), tail);

// keep and full.
assert.deepEqual(failingOutput(lines(40).join('\n'), { full: true }), lines(40), 'CDP_FULL: everything');
out = failingOutput(['[FAIL] a', ...lines(5)].join('\n'), { keep: 3 });
assert.deepEqual(out, ['… 3 earlier lines not shown (CDP_FULL=1 prints them; results.json has them); their [FAIL] lines:', '[FAIL] a', '… the last lines:', ...lines(5).slice(-3)]);
assert.match(failingOutput(lines(4).join('\n'), { keep: 3 })[0], /^… 1 earlier line not shown/, 'one hidden line: singular');

console.log('PASS failing_output: hidden [FAIL] lines are printed once each, in order, above the last lines');
