// The Servers list's search (#4778), on its own: what is accepted, which machines match, the count's words.
//   node server_filters.test.js
const assert = require('node:assert/strict');
const path = require('node:path');
const { pathToFileURL } = require('node:url');

(async () => {
    const m = await import(pathToFileURL(path.resolve(__dirname, '../../coral-go/pkg/frontend/static/server_filters.js')).href);
    assert.deepEqual(m.clean(undefined), m.EMPTY);
    assert.deepEqual(m.clean({ q: 7 }), m.EMPTY);
    assert.equal(m.clean({ q: 'a\u0000b\nc ' + 'x'.repeat(300) }).q.length, 200);
    assert.equal(m.clean({ q: '  studio  ' }).q, 'studio');
    assert.equal(m.isFiltered(m.EMPTY), false);
    assert.equal(m.isFiltered({ q: 'x' }), true);
    assert.deepEqual(m.chips({ q: 'x' }), [], 'the search is in the box: no chip');
    assert.deepEqual(m.without({ q: 'x' }, 'q'), { q: '' });

    const S = [
        { id: 'm1', label: 'Studio Mac', url: 'http://10.0.0.11:8420' },
        { id: 'm2', label: 'build-box', url: 'https://build.example.net:8420', host: 'build.example.net' },
        { id: 'edge-7', label: 'Edge', url: 'http://192.168.1.77:8420' },
        { id: 'm4', label: null, url: undefined },
        null,
    ];
    const names = f => S.filter(s => m.matches(s, f)).map(s => s && s.id);
    assert.deepEqual(names({ q: '' }), ['m1', 'm2', 'edge-7', 'm4', null], 'no search keeps every machine, whatever its shape');
    assert.deepEqual(names({ q: 'STUDIO' }), ['m1'], 'a name, in any case');
    assert.deepEqual(names({ q: 'edge-7' }), ['edge-7'], 'an id');
    assert.deepEqual(names({ q: '10.0.0.11' }), ['m1'], 'an address');
    assert.deepEqual(names({ q: 'example.net' }), ['m2'], 'a host');
    assert.deepEqual(names({ q: ':8420' }), ['m1', 'm2', 'edge-7'], 'a port');
    assert.deepEqual(names({ q: 'build 8420' }), ['m2'], 'every word, in any order of the haystack');
    assert.deepEqual(names({ q: 'nothing-like-this' }), []);
    assert.deepEqual(names({ q: '%' }), [], '% is an ordinary character');
    assert.equal(m.matches({ id: 'm4' }, { q: 'm4' }), true, 'a machine with only an id');
    assert.equal(m.countText(20, 20, 'machine', false), '20 machines');
    assert.equal(m.countText(3, 20, 'machine', true), '3 of 20 machines');
    assert.equal(m.countText(1, 1, 'server', false), '1 server');
    assert.equal(m.countText(0, 8, 'server', true), '0 of 8 servers');
    console.log('server_filters: ok');
})().catch(e => { console.error(e); process.exit(1); });
