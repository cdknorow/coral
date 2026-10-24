/* The local Goals view (#7113, UI/UX's #7091 design, layout A): the second face of a board's screen, beside Messages. A list on the left (filter chips, Waiting on you, one card per goal), the open goal
 * on the right (outcome, done when, the rollup, its tasks with Open task and Open the agent); on a phone the two are two screens with a back row. First cut: read, plus New goal.
 * The routes' JSON becomes the model in goals_adapter.js, the words come from goal_words.js; everything drawn here is built with the DOM and textContent (no text from a goal or a task is read as markup).
 * Lazy: message_board.js imports it when the Goals face is chosen. */
import { state } from './state.js';
import { boardFetch, keyLabel } from './server_base.js';
import { setRowCurrent, OPERATOR_ID } from './utils.js';
import { whenText } from './when_text.js';
import { toGoalList, toTaskPage, toDrivers, createBody, errorOf, LIST_PATH } from './goals_adapter.js';

const PAGE = 50; // a goal's tasks are drawn this many at a time
import * as W from './goal_words.js';

const model = { project: '', goals: [], requireGoal: false, filter: 'all', openId: 0, tasks: new Map(), loading: false, failed: '', asOf: '', creating: false };
let root = null;

const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
const btn = (cls, text, onClick) => { const b = el('button', cls, text); b.type = 'button'; if (onClick) b.addEventListener('click', onClick); return b; };
const clock = () => new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
const say = (t) => { const s = root && root.querySelector('.goals-status'); if (s) { s.textContent = ''; s.textContent = t; } }; // a status region: written once, announced once

/** An Error carrying the route's code and sentence (or the status when the answer is not JSON). */
async function failure(r) {
    let e = { code: '', message: '' };
    try { e = errorOf(await r.json()); } catch { /* the status stays */ }
    return Object.assign(new Error(e.message || `${r.status}`), { code: e.code });
}

async function readJson(path) {
    const r = await boardFetch(model.project, path);
    if (!r.ok) throw await failure(r);
    return r.json();
}

/** Read the board's goals. A failed read keeps the last list on screen, marked "as of <time>". */
export async function loadGoals() {
    if (!model.project) return;
    const project = model.project;
    model.loading = !model.goals.length;
    draw();
    try {
        const list = toGoalList(await readJson(LIST_PATH));
        if (project !== model.project) return;
        model.goals = list.goals; model.requireGoal = list.requireGoal; model.failed = ''; model.asOf = clock();
        if (model.openId && !model.goals.some(g => g.id === model.openId)) model.openId = 0;
    } catch (e) {
        if (project !== model.project) return;
        model.failed = W.failedText(e && e.message);
    }
    model.loading = false;
    draw();
    if (model.openId) loadTasks(model.openId);
}

async function loadTasks(id) {
    try {
        const page = toTaskPage(await readJson(`/tasks?goal=${encodeURIComponent(id)}`));
        model.tasks.set(id, { rows: page.rows, cursor: page.cursor, shown: PAGE, moreFailed: '' });
    } catch (e) {
        model.tasks.set(id, { failed: String((e && e.message) || '') });
    }
    if (model.openId === id) drawDetail();
}

/** Show the next page of a goal's tasks: from the rows already read, and from the route's cursor when it has sent one. */
async function showMoreTasks(id) {
    const t = model.tasks.get(id); if (!t || !t.rows) return;
    const before = t.shown;
    if (t.shown >= t.rows.length && t.cursor) {
        try {
            const page = toTaskPage(await readJson(`/tasks?goal=${encodeURIComponent(id)}&cursor=${encodeURIComponent(t.cursor)}`));
            t.rows = t.rows.concat(page.rows); t.cursor = page.cursor; t.moreFailed = '';
        } catch (e) { t.moreFailed = W.failedText(String((e && e.message) || '')).replace('Goals', 'More tasks'); drawDetail(); return; }
    }
    t.shown = Math.min(t.rows.length, t.shown + PAGE);
    drawDetail();
    say(W.tasksShownText(t.shown, taskTotal(goalById(id), t)));
    const first = root.querySelectorAll('.goal-task')[before]; // focus goes to the first task that was not there before
    if (first) first.focus();
}

/** How many tasks the goal has: the rollup's count (the route's own numbers), at least the rows read. */
const taskTotal = (goal, t) => Math.max(t.rows.length, goal ? goal.rollup.done + goal.rollup.active + goal.rollup.blocked + goal.rollup.queued : 0);

const goalById = (id) => model.goals.find(g => g.id === id);
const visible = () => model.goals.filter(g => W.keepsGoal(model.filter, g));

/** The live agent on this computer that is a task's assignee, or null (the button then says it is not running here). */
function agentFor(task) {
    const live = state.liveSessions || [];
    if (task.sessionId) { const bySession = live.find(s => s && s.session_id === task.sessionId); if (bySession) return bySession; }
    const name = task.assignee;
    if (!name) return null;
    return live.find(s => s && (s.board_project === model.project || !s.board_project) && (s.display_name === name || s.name === name)) || null;
}

function openTask(task) {
    const existing = state.currentBoardTasks || [];
    if (task.raw && !existing.find(t => t.id === task.id)) state.currentBoardTasks = [...existing, task.raw]; // showTaskDetailModal reads the task from here (the Analytics page does the same)
    window.showTaskDetailModal?.(task.id);
}

function openAgent(session) {
    window.switchNavTab?.('agents');
    window.selectLiveSession?.(session.name, session.agent_type, session.session_id, session.server);
}

// ── drawing ──────────────────────────────────────────────────────────────

function stateChip(goal) {
    const chip = el('span', `goal-chip goal-state-${goal.state || 'unknown'}`);
    chip.append(el('span', 'goal-dot'), document.createTextNode(W.stateWord(goal)));
    return chip;
}

function rollupBar(rollup) {
    const bar = el('span', 'goal-rollup'); bar.setAttribute('role', 'img'); bar.setAttribute('aria-label', W.rollupSentence(rollup));
    const sh = W.rollupShares(rollup);
    for (const k of ['done', 'active', 'blocked']) { if (!sh[k]) continue; const seg = el('span', `goal-rollup-${k}`); seg.style.width = `${(sh[k] * 100).toFixed(1)}%`; bar.append(seg); }
    return bar;
}

function goalCard(goal) {
    const li = el('li', 'goal-item');
    const b = el('button', 'row-btn goal-card'); b.type = 'button'; b.dataset.goalId = String(goal.id);
    const top = el('span', 'goal-card-top');
    top.append(el('span', 'goal-card-title', goal.title || `Goal ${goal.id}`), stateChip(goal));
    b.append(top);
    const meta = W.driverLine(goal.driver, whenText(goal.updatedAt));
    if (meta) b.append(el('span', 'goal-card-meta', meta));
    b.append(rollupBar(goal.rollup), el('span', 'goal-card-sentence', W.rollupSentence(goal.rollup)));
    b.addEventListener('click', () => openGoal(goal.id, true));
    li.append(b);
    setRowCurrent(li, goal.id === model.openId);
    return li;
}

function drawListRaw() {
    const col = root.querySelector('.goals-list-col'); if (!col) return;
    col.replaceChildren();
    const counts = W.filterCounts(model.goals);
    const chips = el('div', 'goals-chips'); chips.setAttribute('role', 'group'); chips.setAttribute('aria-label', 'Show goals that are');
    for (const f of W.FILTERS) {
        const c = btn('goals-chip', `${W.filterLabel(f)} ${counts[f]}`, () => { model.filter = f; say(W.filterAnnouncement(f, counts[f])); drawList(); });
        c.setAttribute('aria-pressed', String(model.filter === f)); c.dataset.filter = f;
        chips.append(c);
    }
    col.append(chips);
    const waiting = W.waitingGoals(model.goals);
    if (waiting.length) {
        const strip = el('div', 'goals-waiting');
        strip.append(el('h3', 'goals-waiting-head', 'Waiting on you'));
        for (const g of waiting) {
            const row = el('div', 'goals-waiting-row');
            row.append(el('span', 'goals-waiting-text', `${g.title}: ${g.decision || g.recommendation}`), btn('btn btn-small goals-waiting-open', 'Open the goal', () => openGoal(g.id, true)));
            strip.append(row);
        }
        col.append(strip);
    }
    if (model.failed) {
        const f = el('div', 'goals-failed'); f.setAttribute('role', 'alert');
        f.append(el('span', null, `${model.failed}${model.goals.length ? ` (${W.asOfText(model.asOf)})` : ''}`), btn('btn btn-small', 'Try again', loadGoals));
        col.append(f);
    }
    if (model.loading) { col.append(el('p', 'goals-note', 'Loading goals…')); return; }
    if (!model.goals.length) {
        if (!model.failed) {
            const e = el('div', 'goals-empty');
            e.append(el('p', null, W.noGoalsText), btn('btn btn-primary', 'New goal', () => openCreate()), el('code', 'goals-cmd', W.noGoalsCommand));
            col.append(e);
        }
        return;
    }
    const shown = visible();
    if (!shown.length) {
        const fe = W.filteredEmpty(model.filter, model.goals.length);
        const e = el('div', 'goals-empty'); e.append(el('p', null, fe.text), btn('btn', fe.action, () => { model.filter = 'all'; say(W.filterAnnouncement('all', model.goals.length)); drawList(); }));
        col.append(e);
        return;
    }
    const ul = el('ul', 'goals-ul'); ul.setAttribute('aria-label', `Goals, ${shown.length}`);
    for (const g of shown) ul.append(goalCard(g));
    col.append(ul);
}

function taskRow(t) {
    const li = el('li', 'goal-task'); li.tabIndex = -1; // focus goes to the first task a Show more adds
    const head = el('span', 'goal-task-head');
    head.append(el('span', 'goal-task-id', `#${t.id}`), el('span', 'goal-task-title', t.title));
    const chip = el('span', `goal-chip goal-state-task-${W.taskStateWord(t.state).toLowerCase()}`); chip.append(el('span', 'goal-dot'), document.createTextNode(W.taskStateWord(t.state)));
    head.append(chip);
    li.append(head);
    const sub = [t.assignee, t.statusLine].filter(Boolean).join(' · ');
    if (sub) li.append(el('span', 'goal-task-sub', sub));
    const acts = el('span', 'goal-task-actions');
    const openT = btn('btn btn-small goal-open-task', 'Open task', () => openTask(t)); openT.setAttribute('aria-label', `Open task #${t.id}: ${t.title}`); // three rows, three names (the visible words start the name)
    acts.append(openT);
    const session = agentFor(t);
    const ob = btn('btn btn-small goal-open-agent', 'Open the agent', session ? () => openAgent(session) : null); ob.setAttribute('aria-label', `Open the agent for task #${t.id}`);
    if (!session) { ob.setAttribute('aria-disabled', 'true'); ob.classList.add('is-off'); const why = el('span', 'goal-task-why', 'Not running here'); ob.setAttribute('aria-describedby', (why.id = `goal-why-${t.id}`)); acts.append(ob, why); } else acts.append(ob);
    li.append(acts);
    return li;
}

function drawDetailRaw() {
    const col = root.querySelector('.goals-detail-col'); if (!col) return;
    const keepFocus = document.activeElement && document.activeElement.id === 'goals-detail-title'; // the tasks arrive after the goal opens: the redraw must not take the focus off its heading
    col.replaceChildren();
    const g = goalById(model.openId);
    if (!g) { col.classList.add('goals-detail-none'); return; }
    col.classList.remove('goals-detail-none');
    const back = btn('goals-back', '‹ Goals', () => closeGoal());
    col.append(back);
    const h = el('h3', 'goals-detail-title', g.title); h.tabIndex = -1; h.id = 'goals-detail-title'; col.append(h);
    const line = el('div', 'goals-detail-line'); line.append(stateChip(g)); const meta = W.driverLine(g.driver, whenText(g.updatedAt)); if (meta) line.append(el('span', 'goal-card-meta', meta)); col.append(line);
    if (g.outcome) { col.append(el('h4', 'goals-sub', 'Outcome'), el('p', 'goals-text', g.outcome)); }
    if (g.doneWhen) { col.append(el('h4', 'goals-sub', 'Done when'), el('p', 'goals-text', g.doneWhen)); }
    col.append(rollupBar(g.rollup), el('p', 'goal-card-sentence', W.rollupSentence(g.rollup)));
    if (W.waitingGoals([g]).length) {
        const w = el('div', 'goals-waiting goals-waiting-full'); w.append(el('h4', 'goals-sub', 'Waiting on you'));
        if (g.decision) w.append(el('p', 'goals-text', g.decision));
        if (g.recommendation) { w.append(el('span', 'goals-label', 'Recommendation'), el('p', 'goals-text', g.recommendation)); }
        if (g.ifSilent) { w.append(el('span', 'goals-label', 'If you say nothing'), el('p', 'goals-text', g.ifSilent)); }
        col.append(w);
    }
    col.append(el('h4', 'goals-sub', 'Tasks'));
    const tasks = model.tasks.get(g.id);
    if (!tasks) col.append(el('p', 'goals-note', 'Loading tasks…'));
    else if (tasks.failed !== undefined) { const f = el('div', 'goals-failed'); f.setAttribute('role', 'alert'); f.append(el('span', null, W.failedText(tasks.failed).replace('Goals', 'Tasks')), btn('btn btn-small', 'Try again', () => loadTasks(g.id))); col.append(f); }
    else if (!tasks.rows.length) col.append(el('p', 'goals-note', W.noTasksText(g.id)));
    else {
        const ul = el('ul', 'goal-tasks'); for (const t of tasks.rows.slice(0, tasks.shown)) ul.append(taskRow(t)); col.append(ul);
        const total = taskTotal(g, tasks), left = Math.max(tasks.rows.length - tasks.shown, 0) || (tasks.cursor ? Math.max(total - tasks.shown, 1) : 0);
        if (left) col.append(btn('btn goals-more', W.showMoreText(left, PAGE), () => showMoreTasks(g.id)));
        if (tasks.moreFailed) { const f = el('div', 'goals-failed'); f.setAttribute('role', 'alert'); f.append(el('span', null, tasks.moreFailed)); col.append(f); }
        if (left || tasks.shown < total) col.append(el('p', 'goals-note', !left && !tasks.cursor ? W.tasksCappedText(Math.min(tasks.shown, tasks.rows.length), total) : W.tasksShownText(Math.min(tasks.shown, tasks.rows.length), total)));
    }
    if (keepFocus) h.focus();
}

function drawHead() {
    const head = root.querySelector('.goals-head'); if (!head) return;
    head.replaceChildren();
    const left = el('div', 'goals-head-text');
    const h = el('h2', 'goals-title', 'Goals'); h.tabIndex = -1;
    left.append(h, el('p', 'goals-sub-line', `This board only · ${W.goalCountLine(model.goals.length, W.waitingGoals(model.goals).length)}`));
    if (state.hubConnected || window.__goalsHub) left.append(el('p', 'goals-note', W.otherBoardsNote));
    head.append(left, btn('btn btn-primary goals-new', 'New goal', () => openCreate()));
    if (model.requireGoal) head.after(Object.assign(el('p', 'goals-note goals-require'), { textContent: W.requireGoalNote }));
    else root.querySelector('.goals-require')?.remove();
}

/** Redraw a column and give the keyboard its place back: the control that had focus (a chip by its filter, a card by its goal, anything else by its class and words) is found again in the new draw; when it is gone ("Show all" and "Try again" go when they work) focus goes to the pressed chip, never to the page. */
function keepFocus(col, redraw) {
    const a = document.activeElement;
    const held = a && col && col.contains(a) && a !== col ? { filter: a.dataset.filter, goal: a.dataset.goalId, id: a.id, cls: a.className, text: a.textContent } : null;
    redraw();
    if (!held) return;
    const all = [...col.querySelectorAll('button, [tabindex]')];
    const same = held.filter ? col.querySelector(`.goals-chip[data-filter="${CSS.escape(held.filter)}"]`) : held.goal ? col.querySelector(`.goal-card[data-goal-id="${CSS.escape(held.goal)}"]`) : held.id ? col.querySelector(`#${CSS.escape(held.id)}`) : all.find(e => e.className === held.cls && e.textContent === held.text);
    (same || col.querySelector('.goals-chip[aria-pressed="true"]'))?.focus({ preventScroll: true });
}

const drawList = () => keepFocus(root && root.querySelector('.goals-list-col'), drawListRaw);
const drawDetail = () => keepFocus(root && root.querySelector('.goals-detail-col'), drawDetailRaw);

function draw() {
    if (!root) return;
    drawHead(); drawList(); drawDetail();
    root.classList.toggle('goals-open', !!model.openId && !!goalById(model.openId));
}

/** Open a goal (focus moves to its heading when asked: a person chose it) and read its tasks. */
export function openGoal(id, focus = false) {
    model.openId = id;
    draw();
    if (focus) root.querySelector('#goals-detail-title')?.focus();
    if (!model.tasks.has(id) || model.tasks.get(id).failed !== undefined) loadTasks(id);
}

function closeGoal() {
    const id = model.openId;
    model.openId = 0;
    draw();
    root.querySelector(`.goal-card[data-goal-id="${id}"]`)?.focus();
}

// ── New goal ─────────────────────────────────────────────────────────────

function openCreate() {
    if (model.creating) return;
    model.creating = true;
    const dlg = document.createElement('dialog'); dlg.className = 'goals-dialog'; dlg.setAttribute('aria-labelledby', 'goals-create-title');
    const form = el('form', 'goals-form'); form.noValidate = true;
    const title = el('h3', null, 'New goal'); title.id = 'goals-create-title';
    const field = (label, input, id) => { const l = el('label', 'goals-field'); l.append(el('span', null, label), input); input.id = id; return l; };
    const t = el('input'); t.type = 'text'; t.maxLength = 200; t.autocomplete = 'off';
    const d = el('input'); d.type = 'text'; d.maxLength = 300; d.autocomplete = 'off';
    const dr = el('select'); dr.disabled = true; dr.append(new Option('Loading this board\'s agents…', '')); // the board's own agents: the route refuses a name that is not a subscriber
    const err = el('p', 'goals-field-error'); err.setAttribute('role', 'alert');
    const where = el('p', 'goals-note', `Created on this board: ${keyLabel(model.project)}`);
    const go = btn('btn btn-primary', 'Create goal'); go.type = 'submit';
    const cancel = btn('btn', 'Cancel', () => dlg.close());
    form.append(title, field('Title (required)', t, 'goal-new-title'), field('Done when (optional): what a person can check', d, 'goal-new-done'), field('Driver (optional)', dr, 'goal-new-driver'), where, err, el('div', 'goals-actions'));
    form.querySelector('.goals-actions').append(cancel, go);
    dlg.append(form); document.body.append(dlg);
    readJson('/subscribers').then(json => {
        const people = toDrivers(json);
        dr.replaceChildren(...people.map(p => new Option(p.label, p.id)), new Option('No driver yet', ''));
        dr.value = people.length && people[0].orchestrator ? people[0].id : ''; // the Orchestrator drives unless the person says otherwise
        dr.disabled = false;
    }).catch(() => { dr.replaceChildren(new Option('No driver yet', '')); dr.disabled = false; where.textContent += '. This board\'s agents could not be read, so no driver is set.'; });
    form.addEventListener('submit', async (e) => {
        e.preventDefault();
        err.textContent = '';
        if (!t.value.trim()) { err.textContent = 'A goal needs a title.'; t.setAttribute('aria-invalid', 'true'); t.focus(); return; }
        t.removeAttribute('aria-invalid'); go.disabled = true;
        try {
            const r = await boardFetch(model.project, '/goals', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(createBody({ title: t.value, doneWhen: d.value, driver: dr.value, by: OPERATOR_ID })) });
            if (!r.ok) throw await failure(r);
            dlg.close();
            say('Goal created');
            await loadGoals();
            root.querySelector('.goals-new')?.focus(); // the list was drawn again: focus goes back to the button that opened the dialog
        } catch (ex) { err.textContent = `The goal was not created: ${W.errorWords(ex && ex.code, ex && ex.message)}`; go.disabled = false; }
    });
    dlg.addEventListener('close', () => { model.creating = false; dlg.remove(); root.querySelector('.goals-new')?.focus(); });
    dlg.showModal(); t.focus();
}

// ── showing and hiding ───────────────────────────────────────────────────

/** Show the board's goals in `host` (the page's #mb-goals) and read them. */
export function showGoals(host, project) {
    if (project !== model.project) { Object.assign(model, { project, goals: [], requireGoal: false, filter: 'all', openId: 0, tasks: new Map(), failed: '', asOf: '', loading: false }); }
    if (!host.dataset.built) {
        host.dataset.built = '1';
        const page = el('div', 'goals-page');
        const status = el('div', 'goals-status sr-only'); status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite');
        const cols = el('div', 'goals-cols');
        cols.append(el('section', 'goals-list-col'), el('section', 'goals-detail-col'));
        page.append(el('div', 'goals-head'), status, cols);
        host.replaceChildren(page);
    }
    root = host.querySelector('.goals-page');
    host.hidden = false;
    draw();
    loadGoals();
}
