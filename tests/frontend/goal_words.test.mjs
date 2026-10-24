// The Goals view's words (#7113): states, the rollup sentence and its one-task and no-task forms, counts, the filter announcement and the empty and failed lines.
import assert from 'node:assert/strict';
import * as W from '../../coral-go/pkg/frontend/static/goal_words.js';

assert.deepEqual(W.FILTERS, ['all', 'active', 'blocked', 'proposed', 'done', 'dropped']);
assert.equal(W.filterLabel('blocked'), 'Blocked'); assert.equal(W.filterLabel('all'), 'All');

assert.equal(W.stateWord({ state: 'active' }), 'Active');
assert.equal(W.stateWord({ state: 'proposed' }), 'Proposed');
assert.equal(W.stateWord({ state: 'done' }), 'Done'); assert.equal(W.stateWord({ state: 'dropped' }), 'Dropped');
assert.equal(W.stateWord({ state: 'blocked', blockedOn: 'the Operator' }), 'Blocked on the Operator', 'named: on whom');
assert.equal(W.stateWord({ state: 'blocked', blockedOn: '  ' }), 'Blocked', 'nothing named: Blocked alone');
assert.equal(W.stateWord({ state: 'blocked' }), 'Blocked');
assert.equal(W.stateWord({ state: 'ODD_state' }), 'Odd_state', 'an unknown state is shown, not hidden');
assert.equal(W.stateWord(null), '');

assert.equal(W.taskStateWord('completed'), 'Done'); assert.equal(W.taskStateWord('skipped'), 'Done');
assert.equal(W.taskStateWord('in_progress'), 'Active'); assert.equal(W.taskStateWord('review_pending'), 'Active');
assert.equal(W.taskStateWord('blocked'), 'Blocked'); assert.equal(W.taskStateWord('parked'), 'Blocked');
assert.equal(W.taskStateWord('pending'), 'Queued'); assert.equal(W.taskStateWord('draft'), 'Queued');
assert.equal(W.taskStateWord('some_new_state'), 'Some new state');

assert.equal(W.rollupSentence({ done: 5, active: 2, blocked: 1, queued: 1 }), '9 tasks: 5 done, 2 active, 1 blocked, 1 queued');
assert.equal(W.rollupSentence({ done: 1, active: 0, blocked: 0, queued: 0 }), '1 task: 1 done', 'one task, singular');
assert.equal(W.rollupSentence({ done: 0, active: 0, blocked: 2, queued: 0 }), '2 tasks: 2 blocked', 'zero counts are left out');
assert.equal(W.rollupSentence({ done: 0, active: 0, blocked: 0, queued: 0 }), 'No tasks yet');
assert.equal(W.rollupSentence({}), 'No tasks yet'); assert.equal(W.rollupSentence(null), 'No tasks yet');
assert.equal(W.rollupSentence({ done: '3', active: -2, blocked: 'x', queued: 1 }), '4 tasks: 3 done, 1 queued', 'bad numbers are not counted');

assert.deepEqual(W.rollupShares({ done: 5, active: 2, blocked: 1, queued: 2 }), { done: 0.5, active: 0.2, blocked: 0.1 });
assert.deepEqual(W.rollupShares({}), { done: 0, active: 0, blocked: 0 });

assert.equal(W.goalCountLine(5, 1), '5 goals, 1 waiting on you'); assert.equal(W.goalCountLine(1, 0), '1 goal');
assert.equal(W.goalCountLine(3, 0), '3 goals'); assert.equal(W.goalCountLine(0, 0), 'No goals yet');

assert.equal(W.filterAnnouncement('blocked', 1), 'Showing 1 blocked goal'); assert.equal(W.filterAnnouncement('active', 2), 'Showing 2 active goals');
assert.equal(W.filterAnnouncement('all', 5), 'Showing all 5 goals'); assert.equal(W.filterAnnouncement('all', 1), 'Showing all 1 goal');
assert.equal(W.filterAnnouncement('dropped', 0), 'No dropped goals'); assert.equal(W.filterAnnouncement('all', 0), 'No goals yet');
assert.deepEqual(W.filteredEmpty('dropped', 4), { text: 'No dropped goals', action: 'Show all 4' });

assert.match(W.noGoalsText, /^No goals yet\. A goal says what the team is for and when it is done; tasks belong to it\.$/);
assert.equal(W.noGoalsCommand, 'coral-board goal add "title" --done-when "..."');
assert.equal(W.noTasksText(4), 'No tasks in this goal yet. Add one with coral-board task add "title" --goal 4.');
assert.equal(W.failedText('timed out'), 'Goals could not be read: timed out'); assert.equal(W.failedText(''), 'Goals could not be read');
assert.equal(W.asOfText('14:32'), 'as of 14:32'); assert.equal(W.asOfText(''), '');
assert.equal(W.driverLine('Orchestrator', 'today 14:32'), 'Driver: Orchestrator · today 14:32');
assert.equal(W.driverLine('', 'Oct 17'), 'Oct 17'); assert.equal(W.driverLine('Dev', ''), 'Driver: Dev'); assert.equal(W.driverLine('', ''), '');

const goals = [{ state: 'active' }, { state: 'active' }, { state: 'blocked', blockedOn: 'the Operator', decision: 'Ship?' }, { state: 'done' }, { state: 'blocked' }, { state: 'weird' }];
assert.deepEqual(W.filterCounts(goals), { all: 6, active: 2, blocked: 2, proposed: 0, done: 1, dropped: 0 }, 'an unknown state counts in All only');
assert.equal(W.keepsGoal('all', goals[5]), true); assert.equal(W.keepsGoal('blocked', goals[2]), true); assert.equal(W.keepsGoal('done', goals[2]), false);
assert.deepEqual(W.waitingGoals(goals), [goals[2]], 'only a goal blocked on someone, with a decision to make, waits on the person');
assert.deepEqual(W.waitingGoals(null), []);
assert.equal(W.errorWords('goal_title_required', 'x'), 'A goal needs a title.'); assert.equal(W.errorWords('goal_driver_unknown', ''), 'That driver is not on this board.');
assert.equal(W.errorWords('some_new_code', 'The server says this'), 'The server says this', 'an unknown code shows the server\'s sentence'); assert.equal(W.errorWords('', ''), 'try again');
assert.equal(W.showMoreText(120), 'Show 50 more'); assert.equal(W.showMoreText(20), 'Show 20 more'); assert.equal(W.showMoreText(0), 'Show 0 more');
assert.equal(W.tasksShownText(100, 120), 'Showing 100 of 120 tasks');
assert.equal(W.tasksCappedText(120, 130), 'Showing 120 of 130 tasks. The rest are on the Tasks panel.');
console.log('goal_words: ok');
