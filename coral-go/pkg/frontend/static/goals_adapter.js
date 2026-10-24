/* The one place the goals routes' JSON becomes the Goals view's model (#7113): when #7087's shape is fixed or changes, this file is the only change. The view reads only what these functions return.
 * Field names follow the #7078 design note and the Orchestrator's list: id, title, outcome, done_when, state, blocked_on, driver, updated_at, rollup {done, active, blocked, queued}, and for a goal
 * blocked on someone decision, recommendation and if_silent. Nothing here throws on a missing field: a goal with fewer fields shows less, never nothing. No imports. */
const text = (v) => (typeof v === 'string' ? v : '');
const count = (v) => { const n = Number(v); return Number.isFinite(n) && n > 0 ? Math.floor(n) : 0; };

/** One goal from the list or show route. */
export function toGoal(g) {
    const r = (g && g.rollup) || {};
    return {
        id: Number(g && g.id) || 0,
        title: text(g && g.title),
        outcome: text(g && g.outcome),
        doneWhen: text(g && (g.done_when ?? g.doneWhen)),
        state: text(g && g.state).toLowerCase(),
        blockedOn: text(g && (g.blocked_on ?? g.blockedOn)),
        driver: text(g && g.driver),
        updatedAt: text(g && (g.updated_at ?? g.updatedAt)),
        rollup: { done: count(r.done), active: count(r.active), blocked: count(r.blocked), queued: count(r.queued) },
        decision: text(g && g.decision),
        recommendation: text(g && g.recommendation),
        ifSilent: text(g && (g.if_silent ?? g.ifSilent)),
    };
}

/** The list route's answer: an array of goals, or {goals: [...], require_goal}. Newest activity first. */
export function toGoalList(json) {
    const rows = Array.isArray(json) ? json : (json && Array.isArray(json.goals) ? json.goals : []);
    const goals = rows.map(toGoal).sort((a, b) => (b.updatedAt || '').localeCompare(a.updatedAt || '') || b.id - a.id);
    return { goals, requireGoal: !!(json && !Array.isArray(json) && (json.require_goal ?? json.requireGoal)) };
}

/** One task of a goal (the tasks route filtered by goal). `session` is filled by the view from the live list, not by the route. */
export function toTask(t) {
    return {
        id: Number(t && t.id) || 0,
        title: text(t && t.title),
        assignee: text(t && (t.assigned_to ?? t.assignee)),
        state: text(t && (t.status ?? t.state)).toLowerCase(),
        statusLine: text(t && (t.status_line ?? t.statusLine)), // the task row has none today (#7087): empty, and the view draws nothing
        sessionId: text(t && (t.session_id ?? t.sessionId)), // the agent session that claimed it, when there is one
        raw: t,
    };
}

/** The tasks route's answer: an array, or {tasks: [...]}. */
export function toTaskList(json) {
    const rows = Array.isArray(json) ? json : (json && Array.isArray(json.tasks) ? json.tasks : []);
    return rows.map(toTask).sort((a, b) => a.id - b.id);
}

/** A page of a goal's tasks: the rows and the cursor that continues them ('' when there is no more; today the route answers every task up to its cap with no cursor, and a cursor is followed when it comes). */
export function toTaskPage(json) {
    return { rows: toTaskList(json), cursor: text(json && !Array.isArray(json) ? json.next_cursor : '') };
}

/** The states the list route is asked for: it answers only the open ones (proposed, active, blocked) by default, and the view's chips include Done and Dropped. */
export const LIST_PATH = '/goals?state=proposed,active,blocked,done,dropped';

/** A refused or failed answer: the route's {error: sentence, code: word}. */
export function errorOf(json) {
    return { code: text(json && json.code), message: text(json && (json.error || json.message)) };
}

/** The people a goal can be driven by: this board's subscribers (the route refuses a driver that is not one), the Orchestrator first. The value is the subscriber's id (what the route checks), the label its role and id. */
export function toDrivers(json) {
    const rows = Array.isArray(json) ? json : [];
    const out = rows.map(s => ({ id: text(s && s.subscriber_id), role: text(s && s.job_title) })).filter(s => s.id);
    const seen = new Set();
    return out.filter(s => !seen.has(s.id) && seen.add(s.id)).map(s => ({ id: s.id, label: s.role && s.role !== s.id ? `${s.role} (${s.id})` : s.id, orchestrator: /orchestrator/i.test(s.role || s.id) }))
        .sort((a, b) => Number(b.orchestrator) - Number(a.orchestrator));
}

/** The body the create route takes; `by` is the writer (the dashboard writes as the Operator). */
export function createBody({ title, doneWhen, driver, by }) {
    const body = { title: String(title || '').trim() };
    if (by) body.subscriber_id = by;
    if (String(doneWhen || '').trim()) body.done_when = String(doneWhen).trim();
    if (String(driver || '').trim()) body.driver = String(driver).trim();
    return body;
}
