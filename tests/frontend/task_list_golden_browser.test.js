// The task list (#4702): renderBoardTaskList (tasks.js) is a 206-line function that merges a board's tasks, the agent's own tasks and its subagents into one table, sorts and filters it, draws the
// header, the hide-done toggle, every row (status icon, priority, type, cost, claimed time), the "Load more" button and the count badge, and says so when there is nothing to show. Splitting it must
// change nothing. This renders it in every situation (nothing and no agent, a live agent or a team with nothing, a board of tasks of every status and cost, each sort both ways, done tasks hidden, a
// paged board, an agent's own tasks and subagents, a running task's live cost) and records the list, the toggle, the section, its label and the count. Compared with
// fixtures/task_list_golden.json (sha256 per scenario, with the readable lines). CORAL_UPDATE_GOLDEN=1 rewrites it. Dates are read in UTC and the claimed time is normalised. Drives Chrome over CDP
// through agent_view_harness.js against the isolated dev server; the board's tasks are answered by the test.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const GOLDEN = path.join(__dirname, 'fixtures', 'task_list_golden.json');
const sha = t => require('node:crypto').createHash('sha256').update(t).digest('hex');

const t = (id, status, o = {}) => ({ id, title: 'Task ' + id, status, priority: 'medium', created_by: 'Orchestrator', created_at: `2026-10-0${1 + (id % 9)}T0${id % 9}:00:00Z`, ...o });
const BOARD = [
    t(1, 'pending', { priority: 'low' }), t(2, 'draft', { priority: 'critical', body_preview: 'Preview of "2"' }), t(3, 'blocked', { priority: 'high', assigned_to: 'Dev' }),
    t(4, 'in_progress', { assigned_to: 'Dev', claimed_at: '2026-10-01T10:00:00Z', session_id: 'sess-4', body: 'Working body <b>x</b>' }), t(5, 'in_progress', { assigned_to: 'QA' }),
    t(6, 'review_pending', { assigned_to: 'Dev' }), t(7, 'in_progress', { assigned_to: 'Dev', claimed_at: '2026-10-01T10:00:00Z', workflow: { completion_review: { submitted_by: 'Dev' } } }),
    t(8, 'completed', { assigned_to: 'Dev', cost_usd: 0.42 }), t(9, 'completed', { assigned_to: 'QA', cost_usd: 12.5, workflow: { outcome: 'failed' }, verification: { verified_by: 'QA', state: 'verified' } }),
    t(10, 'skipped', { assigned_to: 'Dev', cost_usd: 0 }), t(11, 'completed', { assigned_to: 'Dev' }), t(12, 'pending', { priority: null, title: '<img src=x onerror=alert(1)> & "q"' }),
];
const AGENT = [{ id: 101, title: 'Mine open', completed: 0, created_at: '2026-10-02T08:00:00Z', agent_name: 'solo' }, { id: 102, title: 'Mine running', completed: 2, created_at: '2026-10-03T08:00:00Z', cost_usd: 1.5, display_name: 'Solo' },
    { id: 103, title: 'Mine done', completed: 1, created_at: '2026-10-04T08:00:00Z', cost_usd: 0.25 }, { id: 104, title: 'Mine cancelled', completed: 3, created_at: '2026-10-05T08:00:00Z' }];
const SUBAGENTS = [{ id: 'a1', description: 'Search the repo', subagent_type: 'Explore', status: 'in_progress', started_at: '2026-10-02T09:00:00Z', cost_usd: 0.75, model: 'm', api_calls: 3, input_tokens: 100, output_tokens: 50 },
    { id: 'a2', subagent_type: 'Plan', finished: true, started_at: '2026-10-03T09:00:00Z', cost_usd: 3.2 }, { id: 'a3', status: 'skipped', started_at: '2026-10-04T09:00:00Z' }];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const out = {};
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setTimezoneOverride({ timezoneId: 'UTC' }); await Emulation.setLocaleOverride({ locale: 'en-US' });
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        await ev(`(async () => {
            const st = window.__coralStatic || '/static/';
            const T = window.T = { state: (await import(st + 'state.js')).state, tasks: await import(st + 'tasks.js') };
            const FROZEN = Date.parse('2026-10-18T12:00:00Z'); Date.now = () => FROZEN; // the claimed-for time reads the clock: frozen, the trace does not drift with the hour
            window.__page = { tasks: [], next_cursor: '', total_open: null, total_completed: null };
            const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
            const real = window.fetch;
            window.fetch = (u, o = {}) => { const p = new URL(String(u), location.href);
                if (/\\/tasks\\/\\d+\\/cost$/.test(p.pathname)) return json({ cost_usd: 1.5, input_tokens: 1, output_tokens: 2, cache_read_tokens: 3, cache_write_tokens: 4, request_count: 5 });
                if (/\\/api\\/board\\/[^/]+\\/tasks$/.test(p.pathname)) return json(window.__page);
                return real(u, o); };
            return true; })()`);
        const SNAP = `(() => { const q = id => document.getElementById(id), sec = q('board-tasks-section'), norm = s => s.replace(/>(just now|\\d+m|\\d+h \\d+m|\\d+d \\d+h)</g, '>…<');
            const rows = [...document.querySelectorAll('#board-task-list .board-task-item:not(.board-task-header)')];
            return { html: norm(q('board-task-list').innerHTML), toggle: q('board-task-hide-toggle-container') ? q('board-task-hide-toggle-container').innerHTML : null, count: q('task-bar-count') ? q('task-bar-count').textContent : null,
                section: sec ? sec.style.display : null, label: sec && sec.querySelector('.board-tasks-header > span:first-child') ? sec.querySelector('.board-tasks-header > span:first-child').textContent : null,
                ids: rows.map(r => (r.querySelector('.board-task-id') || {}).textContent.trim()).join(','), text: q('board-task-list').textContent.replace(/\\s+/g, ' ').trim() }; })()`;
        const record = async name => { out[name] = await ev(SNAP); };
        const show = async (name, { tasks = [], page = {}, agent = [], subs = [], session = { name: 'solo', type: 'live', board_project: 'team-a', session_id: 'sid' }, team = null } = {}) => {
            await ev(`T.state.currentSession = ${JSON.stringify(session)}; T.state.selectedTeam = ${JSON.stringify(team)}; T.state.currentAgentTasks = ${JSON.stringify(agent)}; T.state.currentSubagents = ${JSON.stringify(subs)};
                window.__page = Object.assign({ tasks: ${JSON.stringify(tasks)}, next_cursor: '', total_open: null, total_completed: null }, ${JSON.stringify(page)}); T.tasks.loadBoardTasks(${tasks.length || team ? "'team-a'" : 'null'}).then(() => 1)`);
            await ev(`new Promise(r => setTimeout(r, 120))`); // sleep-ok: the load answers over a few frames and the list morphs in; there is no completion event
            if (name) await record(name);
        };
        await show('nothing, no agent', { session: null });
        await show('nothing, a live agent', {});
        await show('nothing, a team', { team: 'team-a' });
        await show('a board of every status', { tasks: BOARD });
        for (const f of ['id', 'priority', 'type', 'assignee', 'created_at', 'cost']) for (const again of [0, 1]) {
            await ev(`window.__coral._toggleTaskSort(${JSON.stringify(f)}); true`); await record(`sorted by ${f}${again ? ' again' : ''}`);
        }
        await ev(`window.__coral._toggleTaskSort('cost'); true`); await record('the third click on the column gives the default order back');
        await ev(`document.querySelector('.board-task-hide-toggle input').click(); true`); await record('done hidden');
        await ev(`document.querySelector('.board-task-hide-toggle input').click(); true`); await record('done shown again');
        await show('a paged board', { tasks: BOARD.slice(0, 9), page: { next_cursor: 'c1', total_open: 7, total_completed: 60 } });
        await ev(`document.querySelector('.board-task-hide-toggle input').click(); true`);
        await show('a paged board, done hidden', { tasks: BOARD.slice(0, 9), page: { next_cursor: 'c1', total_open: 7, total_completed: 60 } });
        await ev(`document.querySelector('.board-task-hide-toggle input').click(); true`);
        await show('the agent\'s own tasks and subagents with a board', { tasks: BOARD.slice(0, 4), agent: AGENT, subs: SUBAGENTS });
        await show('only the agent\'s tasks and subagents', { agent: AGENT, subs: SUBAGENTS });
        await show(null, { tasks: BOARD, team: 'team-a' });
        await ev(`T.tasks.startBoardTaskPoll(); true`);
        await waitFor(`/~\\$1\\.50/.test(document.getElementById('board-task-list').innerHTML)`, 'a live cost in the list', { timeout: 8000 });
        await ev(`T.tasks.stopBoardTaskPoll(); true`); await record('a running task with its live cost');
        assert.match(out['a board of every status'].text, /Task 11/); assert.match(out['a running task with its live cost'].html, /~\$1\.50/);
        const hashes = {}, lines = {};
        for (const [name, s] of Object.entries(out)) { hashes[name] = sha(JSON.stringify(s)); lines[name] = `ids ${s.ids} | section ${s.section} | label ${s.label} | count ${s.count} | toggle ${(s.toggle || '').replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ').trim()} | ${s.text.slice(0, 500)}`; }
        if (process.env.CORAL_UPDATE_GOLDEN) {
            fs.mkdirSync(path.dirname(GOLDEN), { recursive: true });
            fs.writeFileSync(GOLDEN, JSON.stringify({ about: 'what the task list shows, scenario by scenario, for task_list_golden_browser.test.js: sha256 of the list, toggle, section, label and count, and the readable text. CORAL_UPDATE_GOLDEN=1 rewrites it.', hashes, lines }, null, 2) + '\n');
            console.log('golden written: ' + Object.keys(hashes).length + ' scenarios');
            return;
        }
        const want = JSON.parse(fs.readFileSync(GOLDEN, 'utf8'));
        assert.deepEqual(Object.keys(hashes), Object.keys(want.hashes), 'the same scenarios');
        for (const k of Object.keys(want.hashes)) if (hashes[k] !== want.hashes[k]) assert.fail(`${k}: changed.\n  now:  ${lines[k]}\n  was:  ${want.lines[k]}`);
        console.log(`PASS task list golden: ${Object.keys(want.hashes).length} scenarios identical`);
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
