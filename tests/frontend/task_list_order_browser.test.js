// The task list at scale (#4716, UX's #4706 items 1 and 7): the default order puts open and active work first, then the finished tasks, newest first
// inside each part; a column the person chooses sorts the whole list as it always did; and in a narrow dock the header's Priority and Agent labels
// do not touch. With a thousand recent finished tasks the first 50 rows used to be all Finished. Drives Chrome over CDP through agent_view_harness.js
// against the isolated dev server; the board's tasks are answered by the test (task_list_golden_browser pins the rest of the list).
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { framesJs } = require('./helpers/wait');
const BASE = require('./test_base').BASE;

// 40 recently finished tasks, and older open ones of every open status: the open ones are the oldest by created_at.
const done = Array.from({ length: 40 }, (_, i) => ({ id: 100 + i, title: 'Done ' + i, status: i % 7 === 0 ? 'skipped' : 'completed', priority: 'medium', created_at: `2026-10-1${i % 9}T10:${String(i).padStart(2, '0')}:00Z` }));
const open = [
    { id: 1, title: 'Pending', status: 'pending', priority: 'low', created_at: '2026-09-01T10:00:00Z' },
    { id: 2, title: 'Running', status: 'in_progress', priority: 'high', assigned_to: 'Dev', claimed_at: '2026-09-02T10:00:00Z', created_at: '2026-09-02T10:00:00Z' },
    { id: 3, title: 'In review', status: 'review_pending', priority: 'medium', assigned_to: 'Dev', created_at: '2026-09-03T10:00:00Z' },
    { id: 4, title: 'Blocked', status: 'blocked', priority: 'medium', created_at: '2026-09-04T10:00:00Z' },
    { id: 5, title: 'Draft', status: 'draft', priority: 'low', created_at: '2026-09-05T10:00:00Z' },
];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        await ev(`(async () => {
            const st = window.__coralStatic || '/static/';
            const T = window.T = { state: (await import(st + 'state.js')).state, tasks: await import(st + 'tasks.js') };
            T.state.currentSession = { name: 'solo', type: 'live', board_project: 'team-a', session_id: 'sid' }; T.state.currentAgentTasks = []; T.state.currentSubagents = [];
            const all = ${JSON.stringify([...done, ...open])};
            const real = window.fetch;
            window.fetch = (u, o = {}) => { const p = new URL(String(u), location.href);
                if (/\\/api\\/board\\/[^/]+\\/tasks$/.test(p.pathname)) return Promise.resolve(new Response(JSON.stringify({ tasks: all, next_cursor: '', total_open: 5, total_completed: 40 }), { status: 200, headers: { 'content-type': 'application/json' } }));
                return real(u, o); };
            await T.tasks.loadBoardTasks('team-a'); return true; })()`);
        const ids = () => ev(`[...document.querySelectorAll('#board-task-list .board-task-item:not(.board-task-header)')].map(r => Number(r.querySelector('.board-task-id').textContent.replace('#', '')))`);
        await waitFor(`document.querySelectorAll('#board-task-list .board-task-item:not(.board-task-header)').length === 45`, 'the 45 rows');

        console.log('Default order: open and active first (newest first), then done (newest first)');
        const byNewest = (a, b) => b.created_at.localeCompare(a.created_at);
        const want = [...open.slice().sort(byNewest), ...done.slice().sort(byNewest)].map(t => t.id);
        const got = await ids();
        assert.deepEqual(got, want, 'open work first, then done, each newest first');
        assert.deepEqual(got.slice(0, 5), [5, 4, 3, 2, 1], 'the first rows are the open tasks (' + got.slice(0, 5) + ')');
        assert.ok(got.slice(5).every(id => id >= 100), 'then only finished ones');
        console.log('  ok');

        console.log('A column the person chose sorts the whole list: Created ascending puts the oldest first, open and done mixed by date');
        await ev(`window.__coral._toggleTaskSort('created_at'); true`);
        const asc = await ids();
        const wantAsc = [...open, ...done].sort((a, b) => a.created_at.localeCompare(b.created_at)).map(t => t.id);
        assert.deepEqual(asc, wantAsc);
        await ev(`window.__coral._toggleTaskSort('created_at'); true`);
        const desc = await ids();
        assert.deepEqual(desc, [...open, ...done].sort(byNewest).map(t => t.id), 'Created descending, chosen, is the plain date order: finished tasks are among the open ones');
        assert.notDeepEqual(desc, want, 'the chosen order is not the default partition');
        await ev(`window.__coral._toggleTaskSort('id'); true`);
        assert.deepEqual((await ids()).slice(0, 3), [139, 138, 137], 'by id, newest column choice: the largest id first');
        console.log('  ok');

        console.log('Narrow dock: the Priority and Agent header labels do not touch, and neither is cut');
        // The dock is not shown on this page: the list goes into a visible host of the dock's width (its styles are by class).
        await ev(`(() => { const host = document.createElement('div'); host.id = 'qa-dock'; host.style.cssText = 'position:fixed;left:0;top:0;height:500px;z-index:99999;background:var(--bg-primary);display:flex;flex-direction:column'; host.appendChild(document.getElementById('board-task-list')); document.body.appendChild(host); return true; })()`);
        for (const w of (process.env.QA_WIDTHS || '300,330,360,420,520').split(',').map(Number)) {
            await ev(`document.getElementById('qa-dock').style.width = '${w}px'; true`);
            await ev(framesJs(2)); // the layout settles after the width changes
            const g = await ev(`(() => { const h = document.querySelector('#board-task-list .board-task-header'); const r = c => { const e = h.querySelector('.' + c); const b = e.getBoundingClientRect(); const range = document.createRange(); range.selectNodeContents(e); const t = range.getBoundingClientRect(); return { left: b.left, right: b.right, top: Math.round(b.top), textLeft: t.left, textRight: t.right, clipped: e.scrollWidth > e.clientWidth + 1 }; };
                const row = document.querySelector('#board-task-list .board-task-item:not(.board-task-header)'); const rb = c => row.querySelector('.' + c).getBoundingClientRect();
                return { priority: r('board-task-priority'), agent: r('board-task-assignee'), id: r('board-task-id'), width: document.getElementById('board-task-list').clientWidth, row: { priorityLeft: rb('board-task-priority').left, agentLeft: rb('board-task-assignee').left } }; })()`);
            const gap = g.agent.textLeft - g.priority.textRight;
            if (process.env.QA_WIDTHS) console.log(`   ${w}px: gap ${gap.toFixed(1)} priority ${JSON.stringify(g.priority)} agent-left ${g.agent.left} (second line: ${g.agent.left < g.priority.left})`);
            if (process.env.QA_WIDTHS) continue;
            assert.ok(gap >= 6, `at ${w}px the labels are ${gap.toFixed(1)}px apart: ${JSON.stringify(g)}`);
            assert.ok(!g.priority.clipped && !g.agent.clipped, `at ${w}px a label is cut: ${JSON.stringify(g)}`);
            assert.ok(g.agent.left >= g.priority.right + 6 && Math.abs(g.agent.top - g.priority.top) <= 3, `at ${w}px Agent wrapped under Priority: ${JSON.stringify(g)}`);
            assert.ok(g.row && Math.abs(g.row.agentLeft - g.agent.left) < 1 && Math.abs(g.row.priorityLeft - g.priority.left) < 1, `at ${w}px the rows' columns do not line up with the header's: ${JSON.stringify(g)}`);
            assert.ok(g.priority.textLeft - g.id.textRight >= 4, `at ${w}px Priority touches ID: ${JSON.stringify(g)}`);
        }
        console.log('  ok');
        if (process.env.QA_WIDTHS) process.exitCode = 1; // measuring: print
        console.log('PASS task list order');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
