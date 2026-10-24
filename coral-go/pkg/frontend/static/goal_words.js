/* The Goals view's words (#7113, UI/UX's #7091 design): a goal's state, the rollup of its tasks, the counts and the empty, filtered-empty and failed lines as a person says them, not as the
 * routes store them. Pure functions (node-tested like job_words.js); the numbers come from the routes' rollup, never from counting rows in the page. No imports.
 * The state words are capitalised and lower-cased on use: the icon scanner reads a quoted lower-case word ("done", "block") as a Material icon name (icon_subset). */
const STATES = ['Active', 'Blocked', 'Proposed', 'Done', 'Dropped'];
/** The filter chips, in order: All, then the five states. */
export const FILTERS = ['All', ...STATES].map(w => w.toLowerCase());

const plural = (n, one, many) => `${n} ${n === 1 ? one : many}`;
const word = (s) => { const t = String(s || '').trim(); return t ? t[0].toUpperCase() + t.slice(1).toLowerCase() : ''; };

/** The chip's label for a filter: "All", "Active", ... */
export const filterLabel = (f) => word(f);

/** A goal's state as the card says it: "Blocked on the Operator" when the routes name who or what it waits for, "Blocked" alone when they do not, else the state's word. Unknown states come back as sent, capitalised. */
export function stateWord(goal) {
    const s = word(goal && goal.state);
    if (s === 'Blocked') {
        const on = String((goal && goal.blockedOn) || '').trim();
        return on ? `Blocked on ${on}` : 'Blocked';
    }
    return s;
}

/** A task's state, one word each (a task is blocked, never "blocked on" in a row). */
export const taskStateWord = (state) => {
    const s = String(state || '').toLowerCase();
    const done = ['completed', 'skipped'], active = ['in_progress', 'review_pending'], blocked = ['blocked', 'parked'], queued = ['pending', 'draft'];
    if (done.includes(s)) return 'Done';
    if (active.includes(s)) return 'Active';
    if (blocked.includes(s)) return 'Blocked';
    if (queued.includes(s)) return 'Queued';
    return word(s.replace(/_/g, ' '));
};

/** "9 tasks: 5 done, 2 active, 1 blocked, 1 queued"; "1 task: 1 done"; "No tasks yet". Zero counts are left out. `rollup` is {done, active, blocked, queued}. */
export function rollupSentence(rollup) {
    const r = rollup || {};
    const parts = [['done', r.done], ['active', r.active], ['blocked', r.blocked], ['queued', r.queued]].map(([w, n]) => [w, Math.max(0, Number(n) || 0)]);
    const total = parts.reduce((a, [, n]) => a + n, 0);
    if (!total) return 'No tasks yet';
    return `${plural(total, 'task', 'tasks')}: ${parts.filter(([, n]) => n).map(([w, n]) => `${n} ${w}`).join(', ')}`;
}

/** The header's count line: "5 goals, 1 waiting on you"; "1 goal"; "No goals yet". */
export function goalCountLine(total, waiting = 0) {
    if (!total) return 'No goals yet';
    return waiting ? `${plural(total, 'goal', 'goals')}, ${waiting} waiting on you` : plural(total, 'goal', 'goals');
}

/** What the page's status region says once after a chip is pressed: "Showing all 5 goals", "Showing 1 blocked goal", "No dropped goals". */
export function filterAnnouncement(which, count) {
    if (!count) return which === 'all' ? 'No goals yet' : `No ${which} goals`;
    return which === 'all' ? `Showing all ${plural(count, 'goal', 'goals')}` : `Showing ${count} ${which} ${count === 1 ? 'goal' : 'goals'}`;
}

/** The list's line when a filter shows nothing, and the button that clears it: { text: "No dropped goals", action: "Show all 4" }. */
export const filteredEmpty = (which, total) => ({ text: `No ${which} goals`, action: `Show all ${total}` });

/** The list's line when the board has no goals at all. */
export const noGoalsText = 'No goals yet. A goal says what the team is for and when it is done; tasks belong to it.';
/** The terminal line shown under it (the same fields as New goal). */
export const noGoalsCommand = 'coral-board goal add "title" --done-when "..."';

/** A goal's task list when it has none. */
export const noTasksText = (id) => `No tasks in this goal yet. Add one with coral-board task add "title" --goal ${Number(id) || 0}.`;

/** The failed read: "Goals could not be read: <reason>", and the line that marks a list kept on screen from before: "as of 14:32". */
export const failedText = (reason) => { const r = String(reason || '').trim(); return r ? `Goals could not be read: ${r}` : 'Goals could not be read'; };
export const asOfText = (when) => (when ? `as of ${when}` : '');

/** "Driver: <agent> · <when>", leaving out what it does not have. */
export function driverLine(driver, when) {
    const d = String(driver || '').trim();
    return [d ? `Driver: ${d}` : '', when || ''].filter(Boolean).join(' · ');
}

/** What a refused create or change says, by the route's error code (the code is a word, the sentence is the server's): our own words where the code is known, the server's sentence otherwise. */
const ERRORS = { goal_title_required: 'A goal needs a title.', goal_text_too_long: 'That is too long: shorten it and try again.', goal_driver_unknown: 'That driver is not on this board.', goal_bad_state: 'That state is not one a goal can have.', goal_not_found: 'That goal is no longer on this board.', goal_closed: 'That goal is closed.', goal_already_closed: 'That goal is already closed.', goal_blocked_on_required: 'A blocked goal says who it waits for.', goal_required: 'This board needs a goal on every task.', local_only: 'This can only be done on this computer.' };
export const errorWords = (code, sentence) => ERRORS[code] || String(sentence || '').trim() || 'try again';

/** The note under the header when the board refuses tasks without a goal. */
export const requireGoalNote = 'Tasks without a goal are refused on this board (require a goal is on).';
/** The line that says the other boards are elsewhere, drawn only while the Hub is connected. */
export const otherBoardsNote = 'Other boards and computers: in Coral Hub';

/** The bar's accessible name is the sentence; this is the part widths a CSS bar needs (done, active, blocked as shares of the task total; queued is the track). */
export function rollupShares(rollup) {
    const r = rollup || {};
    const n = (x) => Math.max(0, Number(x) || 0);
    const total = n(r.done) + n(r.active) + n(r.blocked) + n(r.queued);
    if (!total) return { done: 0, active: 0, blocked: 0 };
    return { done: n(r.done) / total, active: n(r.active) / total, blocked: n(r.blocked) / total };
}

/** Which goals a filter keeps, and how many each chip counts. */
export const keepsGoal = (which, goal) => which === 'all' || String(goal && goal.state || '').toLowerCase() === which;
export function filterCounts(goals) {
    const out = Object.fromEntries(FILTERS.map(f => [f, 0]));
    for (const g of goals || []) { out.all++; const s = String(g && g.state || '').toLowerCase(); if (s in out) out[s]++; }
    return out;
}

/** Goals blocked on someone: the ones "Waiting on you" shows. A goal blocked on nothing named is blocked, not waiting on the person. */
export const waitingGoals = (goals) => (goals || []).filter(g => String(g && g.state || '').toLowerCase() === 'blocked' && String(g.blockedOn || '').trim() && (g.decision || g.recommendation || g.ifSilent));
