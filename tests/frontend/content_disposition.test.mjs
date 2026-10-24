// The stored file's name from Content-Disposition (#7045): the same answers as before for ordinary headers, and a hostile header (an unterminated quote and many backslashes) answers at once.
import assert from 'node:assert/strict';
import { responseFilename } from '../../coral-go/pkg/frontend/static/content_disposition.js';

assert.equal(responseFilename('inline; filename="report.html"'), 'report.html');
assert.equal(responseFilename('attachment; filename=plain.txt'), 'plain.txt');
assert.equal(responseFilename('attachment; filename=plain.txt; size=3'), 'plain.txt', 'a bare value ends at the next ;');
assert.equal(responseFilename('attachment; filename="a;b.txt"; size=3'), 'a;b.txt', 'a quoted value may hold a ;');
assert.equal(responseFilename('attachment; filename="a\\"b.txt"'), 'a"b.txt', 'a backslash escapes the next character');
assert.equal(responseFilename("attachment; filename=\"fallback.txt\"; filename*=UTF-8''na%C3%AFve.txt"), 'naïve.txt', 'filename* wins');
assert.equal(responseFilename("attachment; filename*=UTF-8''%E0%A4%A"), '', 'a bad escape in filename* with no filename gives nothing');
assert.equal(responseFilename('attachment; filename="x.txt'), '"x.txt', 'an unterminated quote reads as a bare value, as before');
assert.equal(responseFilename(''), ''); assert.equal(responseFilename(null), ''); assert.equal(responseFilename('inline'), '');
for (const hostile of ['filename="' + '\\'.repeat(40), 'filename="' + '\\'.repeat(100000), ';'.repeat(100000) + 'filename', ' '.repeat(100000) + 'filename', 'filename=' + ' '.repeat(100000), 'x;'.repeat(50000) + 'filename*=UTF-8\'' + 'a'.repeat(100000)]) {
    const t0 = performance.now(); responseFilename(hostile);
    assert.ok(performance.now() - t0 < 200, `a long hostile header returns at once (${hostile.slice(0, 20)}…: ${Math.round(performance.now() - t0)} ms)`);
}
console.log('content_disposition: ok');
