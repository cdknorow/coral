// The agent list's filters and sort (#4744 stage 3): the pure part (agent_list_filters.js): cleaning, the state groups, the chips, matching and the comparators.
// node agent_list_filters.test.mjs
import assert from 'node:assert/strict';
import { STATES, SORTS, EMPTY, clean, isFiltered, stateGroup, makeModel, matches, comparator } from '../../coral-go/pkg/frontend/static/agent_list_filters.js';

// clean: anything becomes a valid filters object
assert.deepEqual(clean(null), EMPTY); assert.deepEqual(clean('x'), EMPTY); assert.deepEqual(clean(undefined), EMPTY);
assert.deepEqual(clean({ state: 'working', board: ' team-a ', machine: 's1', sort: 'cost' }), { state: 'working', board: 'team-a', machine: 's1', sort: 'cost' });
assert.equal(clean({ state: 'bogus' }).state, '', 'an unknown state is none'); assert.equal(clean({ state: 'constructor' }).state, '');
assert.equal(clean({ sort: 'bogus' }).sort, 'default'); assert.equal(clean({ sort: 'toString' }).sort, 'default');
assert.equal(clean({ board: 'a\u0000b\nc' }).board, 'a b c', 'control characters become spaces');
assert.equal(clean({ board: 'x'.repeat(500) }).board.length, 200); assert.equal(clean({ board: 5 }).board, '', 'a non-string is none');
assert.deepEqual(clean({ q: 'ignored', other: 1 }), EMPTY, 'only the known fields are kept');

// isFiltered: the sort alone does not narrow
assert.equal(isFiltered(EMPTY), false); assert.equal(isFiltered({ ...EMPTY, sort: 'name' }), false);
for (const k of ['state', 'board', 'machine']) assert.equal(isFiltered({ ...EMPTY, [k]: k === 'state' ? 'idle' : 'x' }), true, k);

// the state groups: what the session states map to
const keys = { needs_input: 'needs_input', working: 'working', sleeping: 'asleep', ended: '', idle: 'idle', your_turn: 'idle', waiting_on: 'idle', task_idle: 'idle', check_terminal: 'idle', stuck: 'idle' };
for (const [k, g] of Object.entries(keys)) assert.equal(stateGroup(k), g, k);
assert.deepEqual(STATES, ['needs_input', 'working', 'idle', 'asleep']);

// chips and removal
const model = makeModel({ boardLabel: v => 'B:' + v, machineLabel: v => 'M:' + v });
assert.deepEqual(model.chips(EMPTY), []);
assert.deepEqual(model.chips({ state: 'needs_input', board: 'tb', machine: 's1', sort: 'name' }), [{ key: 'state', label: 'State: Needs input' }, { key: 'board', label: 'Board: B:tb' }, { key: 'machine', label: 'Machine: M:s1' }], 'the sort is not a chip');
assert.deepEqual(model.without({ state: 'idle', board: 'tb', machine: '', sort: 'cost' }, 'state'), { state: '', board: 'tb', machine: '', sort: 'cost' });
assert.equal(model.without({ ...EMPTY, sort: 'cost' }, 'sort').sort, 'default');
assert.equal(model.isFiltered, isFiltered); assert.equal(model.clean, clean); assert.deepEqual(model.EMPTY, EMPTY);

// matching
const ctx = { stateKey: s => s.k, boardOf: s => s.b || '', machineOf: s => s.m || 'local' };
const a = { k: 'working', b: 'team-a', m: 'local' }, b = { k: 'sleeping', b: 'team-b', m: 's1' }, c = { k: 'your_turn', m: 'local' };
assert.equal(matches(a, EMPTY, ctx), true);
assert.equal(matches(a, { ...EMPTY, state: 'working' }, ctx), true); assert.equal(matches(b, { ...EMPTY, state: 'working' }, ctx), false);
assert.equal(matches(b, { ...EMPTY, state: 'asleep' }, ctx), true); assert.equal(matches(c, { ...EMPTY, state: 'idle' }, ctx), true);
assert.equal(matches(c, { ...EMPTY, board: 'team-a' }, ctx), false, 'an agent with no board matches no board'); assert.equal(matches(a, { ...EMPTY, board: 'team-a' }, ctx), true);
assert.equal(matches(b, { ...EMPTY, machine: 's1' }, ctx), true); assert.equal(matches(a, { ...EMPTY, machine: 's1' }, ctx), false);
assert.equal(matches(a, { ...EMPTY, state: 'working', board: 'team-a', machine: 'local' }, ctx), true, 'all together');
assert.equal(matches(a, { ...EMPTY, state: 'working', board: 'team-b' }, ctx), false, 'one failing is enough');
assert.equal(matches({ k: 'ended' }, { ...EMPTY, state: 'idle' }, ctx), false, 'an ended agent is no state');

// sort comparators
const nameOf = s => s.n;
assert.equal(comparator('default', nameOf), null); assert.equal(comparator('bogus', nameOf), null);
const list = [{ n: 'beta', staleness_seconds: 30, token_cost_usd: 1 }, { n: 'Alpha', staleness_seconds: 5, token_cost_usd: 9 }, { n: 'gamma' }, { n: 'delta', staleness_seconds: 5, token_cost_usd: 9 }];
const order = sort => [...list].sort(comparator(sort, nameOf)).map(x => x.n);
assert.deepEqual(order('name'), ['Alpha', 'beta', 'delta', 'gamma'], 'by name, ignoring case');
assert.deepEqual(order('recent'), ['Alpha', 'delta', 'beta', 'gamma'], 'most recent first, a tie by name, an unknown time last');
assert.deepEqual(order('cost'), ['Alpha', 'delta', 'beta', 'gamma'], 'dearest first, a tie by name, an unknown cost last');
assert.deepEqual(SORTS, ['default', 'name', 'recent', 'cost']);
console.log('PASS agent_list_filters');
