// The version compare's line diff and time words (#6480): added and removed lines in reading order, a removed line before the one that replaces it, equal texts have no change, a text too long to compare says so
// (null), and a time is "today 08:15" / "yesterday 16:40" with the full time for a title. node artifact_versions_diff.test.mjs
import assert from 'node:assert';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

installDom({ mode: 'local' });
const { diffLines, whenOf } = await importUi('artifact_versions');
const kinds = (a, b) => diffLines(a, b).map(l => l.t[0] + ':' + l.text);
assert.deepEqual(kinds('a\nb\nc', 'a\nb\nc'), ['s:a', 's:b', 's:c']);
assert.deepEqual(kinds('a\nb\nc', 'a\nB\nc'), ['s:a', 'd:b', 'a:B', 's:c'], 'a changed line is the removed one, then the added one');
assert.deepEqual(kinds('a\nc', 'a\nb\nc'), ['s:a', 'a:b', 's:c'], 'an added line');
assert.deepEqual(kinds('a\nb\nc', 'a\nc'), ['s:a', 'd:b', 's:c'], 'a removed line');
assert.deepEqual(kinds('a', 'x\ny'), ['d:a', 'a:x', 'a:y']);
assert.deepEqual(kinds('', 'x'), ['d:', 'a:x'], 'an empty text is one empty line');
const big = Array.from({ length: 4001 }, (_, i) => 'l' + i).join('\n');
assert.equal(diffLines(big, big + '\nz'), null, 'more than 4000 lines is not compared');
const many = Array.from({ length: 3000 }, (_, i) => 'a' + i).join('\n'), other = Array.from({ length: 3000 }, (_, i) => 'b' + i).join('\n');
assert.equal(diffLines(many, other), null, 'a middle too large for the table is not compared');
const now = new Date(2026, 9, 23, 12, 0, 0);
assert.match(whenOf(new Date(2026, 9, 23, 8, 15).toISOString(), now).label, /^today \d\d:\d\d$/);
assert.match(whenOf(new Date(2026, 9, 22, 16, 40).toISOString(), now).label, /^yesterday \d\d:\d\d$/);
assert.match(whenOf(new Date(2026, 9, 20, 9, 0).toISOString(), now).label, /\d\d:\d\d$/);
assert.equal(whenOf('not a time', now).label, '');
assert.ok(whenOf(new Date(2026, 9, 23, 8, 15).toISOString(), now).full.length > 8);
console.log('PASS artifact_versions_diff');
