// The CDP runner's queue order (#3916): longest first from recorded durations; a suite with no recorded duration counts as the median; ties,
// and a missing or broken durations file, keep alphabetical order; durations come from a results.json's passing first attempts.
//   node suite_order.test.mjs
import assert from 'node:assert';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { loadDurations, orderLongestFirst, durationsFromResults } from './suite_order.mjs';

const tests = ['a.test.js', 'b.test.js', 'c.test.js', 'd.test.js', 'e.test.js', 'f.test.js'];
assert.deepEqual(orderLongestFirst(tests, {}), tests, 'no durations: alphabetical');
assert.deepEqual(orderLongestFirst(tests, { 'b.test.js': 'slow', 'zzz': 5 }), tests, 'no usable numbers: alphabetical');
assert.deepEqual(orderLongestFirst(tests, { 'a.test.js': 1, 'b.test.js': 9, 'c.test.js': 4, 'd.test.js': 4, 'e.test.js': 20, 'f.test.js': 2 }),
    ['e.test.js', 'b.test.js', 'c.test.js', 'd.test.js', 'f.test.js', 'a.test.js'], 'longest first, ties alphabetical');
// c has no recorded duration: it counts as the median of the known ones (here 4), so it sits between the 9 and the 2.
assert.deepEqual(orderLongestFirst(tests, { 'a.test.js': 1, 'b.test.js': 9, 'd.test.js': 4, 'e.test.js': 20, 'f.test.js': 2 }),
    ['e.test.js', 'b.test.js', 'c.test.js', 'd.test.js', 'f.test.js', 'a.test.js'], 'an unrecorded suite counts as the median');
assert.deepEqual(orderLongestFirst(['x.test.js'], { 'x.test.js': 3 }), ['x.test.js']);
const input = tests.slice(); orderLongestFirst(input, { 'f.test.js': 99 });
assert.deepEqual(input, tests, 'the input array is not changed');

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'suite-order-'));
try {
    assert.deepEqual(loadDurations(path.join(dir, 'missing.json')), {}, 'a missing file is no durations');
    fs.writeFileSync(path.join(dir, 'bad.json'), '{not json'); assert.deepEqual(loadDurations(path.join(dir, 'bad.json')), {}, 'a broken file is no durations');
    fs.writeFileSync(path.join(dir, 'arr.json'), '[1,2]'); assert.deepEqual(loadDurations(path.join(dir, 'arr.json')), {}, 'an array is not durations');
    fs.writeFileSync(path.join(dir, 'ok.json'), '{"a.test.js": 2.5}'); assert.deepEqual(loadDurations(path.join(dir, 'ok.json')), { 'a.test.js': 2.5 });
} finally { fs.rmSync(dir, { recursive: true, force: true }); }

assert.deepEqual(durationsFromResults([
    { test: 'a.test.js', attempts: [{ status: 'pass', secs: '12.34' }] },
    { test: 'b.test.js', attempts: [{ status: 'fail', secs: '3.0' }, { status: 'pass', secs: '9.0' }] }, // failed first: not a duration
    { test: 'c.test.js', attempts: [{ status: 'timeout', secs: '120.0' }] },
    { test: 'strict: d.test.js', attempts: [{ status: 'pass', secs: '1.04' }] },
    { test: 'e.test.js', attempts: [{ status: 'pass', secs: 'x' }] },
    null,
]), { 'a.test.js': 12.3, 'd.test.js': 1 }, 'passing first attempts only, one decimal');
assert.deepEqual(durationsFromResults({ results: [{ test: 'z.test.js', attempts: [{ status: 'pass', secs: '2' }] }] }), { 'z.test.js': 2 }, 'a { results } wrapper works');
assert.deepEqual(durationsFromResults(null), {});
console.log('suite_order: ok');
