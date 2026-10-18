// The Tasks panel's filters (#4744), on their own: what is accepted, how they become the task query's parameters (and the two phases of the open-first order), the chips, the per-board memory.
//   node task_filters.test.js
const assert = require('node:assert/strict');
const path = require('node:path');
const { pathToFileURL } = require('node:url');

(async () => {
    const m = await import(pathToFileURL(path.resolve(__dirname, '../../coral-go/pkg/frontend/static/task_filters.js')).href);

    // clean: anything unknown becomes empty; the date is a real day; strings are cut and stripped of control characters.
    assert.deepEqual(m.clean(undefined), m.EMPTY);
    assert.deepEqual(m.clean({ status: 'weird', priority: 'urgent', sort: 'random', from: '2026-13-40', q: 5, assignee: {} }), m.EMPTY);
    assert.equal(m.clean({ q: 'a\u0000b\nc ' + 'x'.repeat(300) }).q.length, 200);
    assert.equal(m.clean({ assignee: ' Dev\tOne ' }).assignee, 'Dev One');
    assert.deepEqual(m.clean({ status: 'blocked', priority: 'high', sort: 'oldest', from: '2026-10-05' }), { ...m.EMPTY, status: 'blocked', priority: 'high', sort: 'oldest', from: '2026-10-05' });

    // isFiltered: the sort alone does not narrow; isQuery: an order the default list lacks is a query too.
    assert.equal(m.isFiltered(m.EMPTY), false);
    assert.equal(m.isFiltered({ ...m.EMPTY, sort: 'updated' }), false);
    for (const k of ['q', 'status', 'priority', 'assignee', 'from']) assert.equal(m.isFiltered({ ...m.EMPTY, [k]: k === 'status' ? 'done' : k === 'priority' ? 'low' : k === 'from' ? '2026-10-05' : 'x' }), true, k);
    assert.equal(m.isQuery(m.EMPTY), false);
    assert.equal(m.isQuery({ ...m.EMPTY, sort: 'oldest' }), true);
    assert.equal(m.isQuery({ ...m.EMPTY, q: 'x' }), true);

    // toParams: the server's names; the statuses a choice stands for; parked is its own filter; the sort and the order; empty filters send nothing.
    const obj = pairs => pairs.reduce((o, [k, v]) => ((o[k] = k in o ? [].concat(o[k], v) : v), o), {});
    assert.deepEqual(m.toParams(m.EMPTY, 'b-1'), [['board', 'b-1'], ['sort', 'created_at'], ['order', 'desc'], ['limit', '50']]);
    const full = obj(m.toParams({ q: 'plan', status: 'done', priority: 'high', assignee: 'Dev', from: '2026-10-05', sort: 'updated' }, 'b-1'));
    assert.deepEqual([full.board, full.q, full.status, full.priority, full.assigned_to, full.sort, full.order], ['b-1', 'plan', ['completed', 'skipped'], 'high', 'Dev', 'updated_at', 'desc']);
    assert.match(full.updated_since, /^\d{4}-\d{2}-\d{2}T/);
    assert.deepEqual(obj(m.toParams({ ...m.EMPTY, status: 'review' }, 'b')).status, 'review_pending');
    assert.deepEqual(obj(m.toParams({ ...m.EMPTY, status: 'working' }, 'b')).status, 'in_progress');
    const parked = obj(m.toParams({ ...m.EMPTY, status: 'parked' }, 'b'));
    assert.equal(parked.parked, 'true'); assert.equal('status' in parked, false);
    assert.deepEqual(obj(m.toParams({ ...m.EMPTY, sort: 'oldest' }, 'b')).order, 'asc');
    assert.deepEqual(m.toParams({ ...m.EMPTY, q: 'x' }, 'b', { statuses: m.OPEN_STATUSES, limit: 1 }).filter(([k]) => k === 'status').map(([, v]) => v), m.OPEN_STATUSES);
    assert.equal(m.toParams({ ...m.EMPTY, q: 'x' }, 'b', { limit: 1 }).find(([k]) => k === 'limit')[1], '1');

    // phases: the open-first order with no status chosen is the open tasks, then the done ones; a chosen status or any other order is one request.
    assert.deepEqual(m.phases(m.EMPTY), [m.OPEN_STATUSES, m.DONE_STATUSES]);
    assert.deepEqual(m.phases({ ...m.EMPTY, q: 'x' }), [m.OPEN_STATUSES, m.DONE_STATUSES]);
    assert.equal(m.phases({ ...m.EMPTY, status: 'blocked' }), null);
    assert.equal(m.phases({ ...m.EMPTY, sort: 'newest' }), null);
    assert.deepEqual([m.OPEN_STATUSES, m.DONE_STATUSES].flat().sort(), ['blocked', 'completed', 'draft', 'in_progress', 'pending', 'review_pending', 'skipped']); // every status the server has

    // chips, and clearing one.
    assert.deepEqual(m.chips(m.EMPTY), []);
    assert.deepEqual(m.chips({ ...m.EMPTY, q: 'plan', status: 'review', priority: 'critical', assignee: 'Dev', from: '2026-10-05' }).map(c => c.label),
        ['Search: plan', 'Status: In review', 'Priority: Critical', 'Agent: Dev', 'Updated since 2026-10-05']);
    assert.deepEqual(m.without({ ...m.EMPTY, status: 'done', q: 'x' }, 'status'), { ...m.EMPTY, q: 'x' });

    // the per-board memory: stored only when it narrows or reorders; a corrupt value is the empty filters; no store, no crash.
    const mem = new Map(); const store = { get: k => mem.get(k) ?? null, set: (k, v) => mem.set(k, v), remove: k => mem.delete(k) };
    m.saveFilters('b-1', { ...m.EMPTY, status: 'blocked' }, store);
    assert.deepEqual(m.loadFilters('b-1', store), { ...m.EMPTY, status: 'blocked' });
    assert.deepEqual(m.loadFilters('b-2', store), m.EMPTY);
    m.saveFilters('b-1', m.EMPTY, store);
    assert.equal(mem.size, 0, 'the default is not stored');
    mem.set(m.storageKey('b-3'), '{not json'); assert.deepEqual(m.loadFilters('b-3', store), m.EMPTY);
    mem.set(m.storageKey('b-4'), JSON.stringify({ status: 'nope', sort: 'oldest' })); assert.deepEqual(m.loadFilters('b-4', store), { ...m.EMPTY, sort: 'oldest' });
    assert.deepEqual(m.loadFilters('b-1', null), m.EMPTY); m.saveFilters('b-1', { ...m.EMPTY, q: 'x' }, null);
    console.log('task_filters: ok');
})().catch(e => { console.error(e); process.exit(1); });
