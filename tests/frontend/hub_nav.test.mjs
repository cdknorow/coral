// hub_nav.js (#3406): which links from Coral Hub's /hub/nav.json are shown. Every rejection case of the
// agreed spec: path shape, label length and type, at most 8, the 4 KiB cap, non-lists and non-objects.
import assert from 'node:assert/strict';
import { parseHubNav, HUB_NAV_MAX_BYTES } from '../../coral-go/pkg/frontend/static/hub_nav.js';

const p = v => parseHubNav(JSON.stringify(v));
const one = (path, label = 'Machines') => p([{ label, path }]);

// Valid paths.
for (const path of ['/machines', '/machines/keys', '/machines/add', '/machines/a-1/b2']) {
    assert.deepEqual(one(path), [{ label: 'Machines', path }], path);
}
// Every rejected path shape: another prefix, scheme, host, protocol-relative, query, fragment, dot segments,
// backslash, double slash, trailing slash, upper case, encoded characters, spaces, a lookalike prefix.
for (const path of ['/', '/settings', '/machine', '/machinesx', '/Machines', 'machines', 'https://evil.example/machines',
    '//evil.example/machines', '/machines?x=1', '/machines#top', '/machines/../admin', '/machines/..', '/machines\\\\keys',
    '/machines//keys', '/machines/', '/machines/KEYS', '/machines/%2e%2e', '/machines/a b', 'javascript:alert(1)', '/machines/\\u2028', '']) {
    assert.deepEqual(one(path), [], 'rejected: ' + JSON.stringify(path));
}
// Labels: trimmed; 1-40 characters (counted as characters, not bytes); strings only.
assert.deepEqual(one('/machines', '  Machines  '), [{ label: 'Machines', path: '/machines' }]);
assert.deepEqual(one('/machines', 'x'.repeat(40)).length, 1);
assert.deepEqual(one('/machines', 'x'.repeat(41)), [], 'over 40');
assert.deepEqual(one('/machines', '🖥️'.repeat(20)).length, 1, 'counted in characters');
assert.deepEqual(one('/machines', ''), [], 'empty');
assert.deepEqual(one('/machines', '   '), [], 'blank');
for (const label of [null, 3, ['a'], { a: 1 }, true]) assert.deepEqual(one('/machines', label), [], 'non-string label');
// The label is kept as text: markup in it is just characters (the renderer uses textContent).
assert.deepEqual(one('/machines', '<img src=x onerror=alert(1)>'), [{ label: '<img src=x onerror=alert(1)>', path: '/machines' }]);
// At most 8; invalid entries don't count toward them; extra fields are ignored.
const many = Array.from({ length: 12 }, (_, i) => ({ label: 'M' + i, path: '/machines/m' + i }));
assert.equal(p(many).length, 8, 'at most 8');
assert.deepEqual(p([{ label: 'bad', path: '/x' }, ...many]).map(l => l.label).slice(0, 2), ['M0', 'M1'], 'invalid entries are skipped');
assert.deepEqual(p([{ label: 'M', path: '/machines', target: '_blank', onclick: 'x' }]), [{ label: 'M', path: '/machines' }], 'extra fields dropped');
// Not a list, not objects, not JSON, too big.
for (const body of ['{"label":"M","path":"/machines"}', '"x"', 'null', '42', 'not json', '', '[1, "a", null, [], true]']) {
    assert.deepEqual(parseHubNav(body), [], 'rejected body: ' + body);
}
assert.deepEqual(parseHubNav(undefined), []);
const big = JSON.stringify([{ label: 'M', path: '/machines' }, { label: 'x'.repeat(HUB_NAV_MAX_BYTES), path: '/machines' }]);
assert.deepEqual(parseHubNav(big), [], 'over 4 KiB: ignored entirely');
console.log('hub_nav: ok');
