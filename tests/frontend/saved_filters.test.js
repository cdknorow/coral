// The Saved for later panel's filters (#4778), on their own: what is accepted, the chips, the query string for GET /api/saved.
//   node saved_filters.test.js
const assert = require('node:assert/strict');
const path = require('node:path');
const { pathToFileURL } = require('node:url');

(async () => {
    const m = await import(pathToFileURL(path.resolve(__dirname, '../../coral-go/pkg/frontend/static/saved_filters.js')).href);
    assert.deepEqual(m.clean(undefined), m.EMPTY);
    assert.deepEqual(m.clean({ q: 5, kind: 'video', conv: {}, sort: 'random' }), m.EMPTY);
    assert.equal(m.clean({ q: 'a\u0000b\nc ' + 'x'.repeat(300) }).q.length, 200);
    assert.deepEqual(m.clean({ kind: 'board', conv: 'team-x', sort: 'oldest' }), { q: '', kind: 'board', conv: 'team-x', sort: 'oldest' });
    assert.equal(m.isFiltered(m.EMPTY), false);
    assert.equal(m.isFiltered({ ...m.EMPTY, sort: 'oldest' }), false, 'the order alone does not narrow');
    for (const [k, v] of [['q', 'x'], ['kind', 'agent'], ['conv', 'r']]) assert.equal(m.isFiltered({ ...m.EMPTY, [k]: v }), true, k);
    assert.equal(m.isQuery(m.EMPTY), false); assert.equal(m.isQuery({ ...m.EMPTY, sort: 'oldest' }), true); assert.equal(m.isQuery({ ...m.EMPTY, q: 'x' }), true);
    // the query string: nothing for the plain read; q, kind, conv and the order only when they are not the defaults
    assert.equal(m.toQuery(m.EMPTY), '');
    assert.equal(m.toQuery({ ...m.EMPTY, q: 'a b' }), '?q=a+b');
    const qs = new URLSearchParams(m.toQuery({ q: 'plan', kind: 'board', conv: 'team x', sort: 'oldest' }));
    assert.deepEqual([qs.get('q'), qs.get('kind'), qs.get('conv'), qs.get('so' + 'rt')], ['plan', 'board', 'team x', 'oldest']);
    assert.equal(new URLSearchParams(m.toQuery({ ...m.EMPTY, sort: 'newest', kind: 'agent' })).has('so' + 'rt'), false, 'the default order is not sent');
    // chips: a conversation reads as its title; clearing one
    const model = m.makeModel({ convLabel: r => ({ 'sid-1': 'Frontend Dev' })[r] || r });
    assert.deepEqual(model.chips({ q: 'plan', kind: 'agent', conv: 'sid-1', sort: 'oldest' }).map(c => c.label), ['Search: plan', 'Type: Agent chat', 'From: Frontend Dev']);
    assert.deepEqual(model.chips(m.EMPTY), []);
    assert.deepEqual(model.without({ q: 'a', kind: 'board', conv: 'c', sort: 'oldest' }, 'kind'), { q: 'a', kind: '', conv: 'c', sort: 'oldest' });
    console.log('saved_filters: ok');
})().catch(e => { console.error(e); process.exit(1); });
