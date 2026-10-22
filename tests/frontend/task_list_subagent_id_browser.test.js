// A subagent's id comes from a file an agent can write, so a row whose id is markup must show as text and make no element (the Tasks table
// put it into innerHTML unescaped). Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

const HOSTILE = ['<img src=x onerror=alert(1)>', '<b onclick=x>hostile</b>'];

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
            T.state.currentSession = { name: 'solo', type: 'live', board_project: 'team-a', session_id: 'sid' }; T.state.currentAgentTasks = [];
            T.state.currentSubagents = ${JSON.stringify(HOSTILE)}.map((id, i) => ({ id, subagent_id: id, session_id: 'sid', status: 'in_progress', description: 'Sub ' + i, started_at: '2026-10-1' + i + 'T10:00:00Z' }));
            const real = window.fetch;
            window.fetch = (u, o = {}) => { const p = new URL(String(u), location.href);
                if (p.pathname.startsWith('/api/board/') && p.pathname.endsWith('/tasks')) return Promise.resolve(new Response(JSON.stringify({ tasks: [], next_cursor: '', total_open: 0, total_completed: 0 }), { status: 200, headers: { 'Content-Type': 'application/json' } }));
                return real(u, o); };
            await T.tasks.loadBoardTasks('team-a'); return true; })()`);
        await waitFor(`document.querySelectorAll('#board-task-list .board-task-item:not(.board-task-header)').length === 2`, 'the two subagent rows');

        console.log('A subagent id that is markup shows as text and makes no element');
        const got = await ev(`[...document.querySelectorAll('#board-task-list .board-task-item:not(.board-task-header) .board-task-id')].map(e => e.textContent)`);
        assert.deepEqual(got.sort(), HOSTILE.map(id => '#subagent-' + id).sort());
        assert.equal(await ev(`document.querySelectorAll('#board-task-list img, #board-task-list b').length`), 0, 'no element came out of the id');
        assert.equal(await ev(`document.querySelectorAll('#board-task-list .board-task-id *').length`), 0, 'the id button holds text only');
        console.log('  ok');
        console.log('PASS task list subagent id');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
