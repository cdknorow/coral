// The Activity panel's filters (#4785), on their own: what is accepted, how they become the events search's parameters (the server's names and event types, the last day included), the chips,
// the count's words.
//   node activity_filters.test.js
const assert = require('node:assert/strict');
const path = require('node:path');
const { pathToFileURL } = require('node:url');

(async () => {
    const m = await import(pathToFileURL(path.resolve(__dirname, '../../coral-go/pkg/frontend/static/activity_filters.js')).href);

    // clean: anything unknown becomes empty; the date is a real day; strings are cut and stripped of control characters.
    assert.deepEqual(m.clean(undefined), m.EMPTY);
    assert.deepEqual(m.clean({ type: 'weird', sort: 'random', from: '2026-13-40', to: 'tomorrow', q: 5 }), m.EMPTY);
    assert.equal(m.clean({ q: 'a\u0000b\nc ' + 'x'.repeat(300) }).q.length, 200);
    assert.deepEqual(m.clean({ type: 'prompt', sort: 'oldest', from: '2026-10-05', to: '2026-10-06', q: ' Edit ' }), { ...m.EMPTY, q: 'Edit', type: 'prompt', from: '2026-10-05', to: '2026-10-06', sort: 'oldest' });

    // isFiltered: the sort alone is not a filter, but it is a query (the live feed has one order).
    assert.equal(m.isFiltered(m.EMPTY), false);
    assert.equal(m.isFiltered({ ...m.EMPTY, sort: 'oldest' }), false);
    assert.equal(m.isQuery({ ...m.EMPTY, sort: 'oldest' }), true);
    assert.equal(m.isQuery(m.EMPTY), false);
    for (const k of ['q', 'type', 'from', 'to']) assert.equal(m.isFiltered({ ...m.EMPTY, [k]: k === 'type' ? 'goal' : k === 'q' ? 'x' : '2026-10-05' }), true, k);

    // the tool groups: every group is shown by default; a hidden one counts as a filter (the Filters badge, Clear all) but is NOT part of the query: it narrows the rows already loaded.
    assert.deepEqual(m.hiddenGroups(m.EMPTY), []);
    const hid = m.withHidden(m.EMPTY, ['read', 'bash']);
    assert.deepEqual(m.hiddenGroups(hid), ['read', 'bash']);
    assert.equal(hid.g_read, false); assert.equal(hid.g_write, true);
    assert.equal(m.isFiltered(hid), true); assert.equal(m.isQuery(hid), false, 'a hidden group is not a query');
    assert.equal(m.queryKey(hid), m.queryKey(m.EMPTY), 'and does not change what the server is asked');
    assert.notEqual(m.queryKey({ ...m.EMPTY, q: 'x' }), m.queryKey(m.EMPTY));
    assert.deepEqual(m.hiddenGroups(m.clean({ g_read: false, g_nothing: false, g_bash: 0 })), ['read'], 'only a literal false hides a group of this list');
    assert.deepEqual(m.hiddenGroups(m.withHidden(m.EMPTY, new Set(['goal', 'not-a-group']))), ['goal'], 'a Set works and an unknown key is ignored');
    assert.deepEqual(m.chips(hid).map(c => c.label), ['Hidden: Read, Bash']);
    assert.deepEqual(m.without(hid, 'groups'), m.EMPTY, 'removing the chip shows every group again');
    assert.deepEqual(m.toParams(hid, { sessionId: 'sid1' }).map(([k, v]) => k), ['session_id', 'sort', 'limit'], 'no tool group reaches the request');

    // every choice stands for event types the server accepts (a lower-case word), and none is empty.
    for (const t of m.TYPES) { assert.ok(m.TYPE_LABEL[t], t); assert.ok(m.TYPE_WORDS[t].length && m.TYPE_WORDS[t].every(w => /^[a-z0-9_]{1,40}$/.test(w)), t); }

    // toParams: the server's names, one type parameter per word, the day range as local instants (until is the start of the day after `to`).
    const pairs = f => m.toParams(f, { sessionId: 'sid1', limit: 50 }).map(([k, v]) => `${k}=${v}`);
    assert.deepEqual(pairs(m.EMPTY), ['session_id=sid1', 'sort=newest', 'limit=50']);
    assert.deepEqual(pairs({ ...m.EMPTY, q: 'edit', type: 'stop' }), ['session_id=sid1', 'q=edit', 'type=stop', 'type=notification', 'sort=newest', 'limit=50']);
    const p = Object.fromEntries(m.toParams({ ...m.EMPTY, from: '2026-10-05', to: '2026-10-06', sort: 'oldest' }, { cursor: 'abc' }));
    assert.equal(p.since, new Date(2026, 9, 5).toISOString());
    assert.equal(p.until, new Date(2026, 9, 7).toISOString(), 'the last day picked is included: until is the start of the day after');
    assert.equal(p.sort, 'oldest'); assert.equal(p.cursor, 'abc'); assert.equal(p.session_id, undefined);
    assert.equal(m.toParams({ ...m.EMPTY, to: '2026-12-31' }).find(([k]) => k === 'until')[1], new Date(2027, 0, 1).toISOString(), 'the day after the last of a month and a year');

    // chips and without.
    assert.deepEqual(m.chips(m.EMPTY), []);
    assert.deepEqual(m.chips({ ...m.EMPTY, q: 'x', type: 'goal', from: '2026-10-05', to: '2026-10-06' }).map(c => c.label), ['Search: x', 'Type: Goal', 'From 2026-10-05', 'Until 2026-10-06']);
    assert.deepEqual(m.without({ ...m.EMPTY, type: 'goal', q: 'x' }, 'type'), { ...m.EMPTY, q: 'x' });

    // the count's words: singular, plural, and N+ for a search the server cut (never singular).
    assert.equal(m.countText(1, false), '1 event');
    assert.equal(m.countText(0, false), '0 events');
    assert.equal(m.countText(70, false), '70 events');
    assert.equal(m.countText(500, true), '500+ events');
    assert.equal(m.countText(1, true), '1+ events');
    console.log('activity_filters: ok');
})().catch(e => { console.error(e); process.exit(1); });
