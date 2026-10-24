// The one place the goals routes' JSON becomes the Goals view's model (#7113): Security's posted contract (goal fields, rollup with total, {goals: [...]}, the tasks page shape, {error, code}).
import assert from 'node:assert/strict';
import { toGoal, toGoalList, toTask, toTaskList, toTaskPage, toDrivers, createBody, errorOf, LIST_PATH } from '../../coral-go/pkg/frontend/static/goals_adapter.js';

const g = toGoal({ id: 4, goal_uid: 'u', board_id: 'b', title: 'T', outcome: 'O', done_when: 'D', state: 'BLOCKED', blocked_on: 'the Operator', driver: 'QA', decision: 'Ship?', recommendation: 'Yes', if_silent: 'Nothing', created_at: 'x', updated_at: '2026-10-24T12:00:00Z', rollup: { done: 5, active: 2, blocked: 1, queued: 1, total: 9 } });
assert.deepEqual(g, { id: 4, title: 'T', outcome: 'O', doneWhen: 'D', state: 'blocked', blockedOn: 'the Operator', driver: 'QA', updatedAt: '2026-10-24T12:00:00Z', rollup: { done: 5, active: 2, blocked: 1, queued: 1 }, decision: 'Ship?', recommendation: 'Yes', ifSilent: 'Nothing' });
assert.deepEqual(toGoal({ id: 1, title: 'Only a title' }), { id: 1, title: 'Only a title', outcome: '', doneWhen: '', state: '', blockedOn: '', driver: '', updatedAt: '', rollup: { done: 0, active: 0, blocked: 0, queued: 0 }, decision: '', recommendation: '', ifSilent: '' }, 'omitted fields are empty, never missing');
assert.equal(toGoal(null).id, 0); assert.deepEqual(toGoal({ rollup: { done: -1, active: 'x', blocked: 2.9, queued: null } }).rollup, { done: 0, active: 0, blocked: 2, queued: 0 });

const list = toGoalList({ goals: [{ id: 1, updated_at: '2026-10-24T10:00:00Z' }, { id: 2, updated_at: '2026-10-24T12:00:00Z' }, { id: 3, updated_at: '2026-10-24T12:00:00Z' }], require_goal: true });
assert.deepEqual(list.goals.map(x => x.id), [3, 2, 1], 'newest activity first, the later id first on a tie'); assert.equal(list.requireGoal, true);
assert.deepEqual(toGoalList([{ id: 1 }]).goals.map(x => x.id), [1], 'a bare array is accepted'); assert.equal(toGoalList([{ id: 1 }]).requireGoal, false);
assert.deepEqual(toGoalList(null), { goals: [], requireGoal: false }); assert.deepEqual(toGoalList({ goals: 'x' }), { goals: [], requireGoal: false });

const t = toTask({ id: 7, title: 'Do it', assigned_to: 'goal-qa', status: 'in_progress', status_line: 'signing' });
assert.deepEqual({ ...t, raw: undefined }, { id: 7, title: 'Do it', assignee: 'goal-qa', state: 'in_progress', statusLine: 'signing', sessionId: '', raw: undefined });
assert.equal(toTask({ id: 8, assigned_to: null, status: 'pending', session_id: 'abc', goal_id: 4 }).sessionId, 'abc', 'the claiming session; a null assignee is empty');
assert.equal(toTask({ id: 8, assigned_to: null }).assignee, '');
assert.deepEqual(toTaskList({ tasks: [{ id: 9 }, { id: 3 }], next_cursor: '', totals: {} }).map(x => x.id), [3, 9], 'the page shape: tasks, sorted by number');
assert.deepEqual(toTaskList(null), []);
assert.deepEqual(toTaskPage({ tasks: [{ id: 2 }, { id: 1 }], next_cursor: 'c9' }).rows.map(x => x.id), [1, 2]); assert.equal(toTaskPage({ tasks: [{ id: 2 }], next_cursor: 'c9' }).cursor, 'c9');
assert.equal(toTaskPage({ tasks: [] }).cursor, '', 'no cursor: nothing more'); assert.equal(toTaskPage([{ id: 1 }]).cursor, ''); assert.equal(toTaskPage(null).cursor, '');

assert.deepEqual(errorOf({ error: 'a goal needs a title', code: 'goal_title_required' }), { code: 'goal_title_required', message: 'a goal needs a title' });
assert.deepEqual(errorOf(null), { code: '', message: '' });
assert.deepEqual(createBody({ title: ' T ', doneWhen: ' D ', driver: ' ', by: 'Operator' }), { title: 'T', subscriber_id: 'Operator', done_when: 'D' });
assert.deepEqual(toDrivers([{ subscriber_id: 'qa-eng', job_title: 'QA' }, { subscriber_id: 'orch', job_title: 'Orchestrator' }, { subscriber_id: 'orch', job_title: 'Orchestrator' }, { subscriber_id: 'plain' }, { job_title: 'no id' }]), [{ id: 'orch', label: 'Orchestrator (orch)', orchestrator: true }, { id: 'qa-eng', label: 'QA (qa-eng)', orchestrator: false }, { id: 'plain', label: 'plain', orchestrator: false }], 'the Orchestrator first, a name once, an entry with no id left out, the id alone when there is no role');
assert.deepEqual(toDrivers(null), []); assert.deepEqual(toDrivers({}), []);
assert.equal(LIST_PATH, '/goals?state=proposed,active,blocked,done,dropped');
console.log('goals_adapter: ok');
