// An idle dashboard re-renders its task list, activity timeline and capture state on every poll (#3881: 16 times in 45 s each in the
// idle trace, a layout each time). Redrawing with nothing changed must now write nothing to the DOM, and a real change must still show.
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('iw-a', 'A', 3, { sleeping: false }); true`);
        await ev(`window.selectLiveSession('iw-a', 'claude', 'iw-a-id', 'local'); true`);
        await waitFor(`!!document.getElementById('board-task-list') && !!document.getElementById('agentic-state-events') && !!document.getElementById('task-bar-count')`, 'the task list and the timeline', 8000);
        await ev(`(async () => {
            const S = window.__coralStatic || '/static/';
            const [{ state }, tasks, agentic] = await Promise.all([import(S + 'state.js'), import(S + 'tasks.js'), import(S + 'agentic_state.js')]);
            window.T = { state, tasks, agentic };
            const sec = document.getElementById('board-tasks-section'); if (sec) sec.style.display = '';
            state.currentBoardTasks = [
                { id: 1, title: 'First task', status: 'pending', priority: 'high', assigned_to: 'A', created_at: '2026-10-16T10:00:00Z' },
                { id: 2, title: 'Second task', status: 'completed', priority: 'low', assigned_to: 'A', created_at: '2026-10-16T10:05:00Z' }];
            state.currentAgentTasks = []; state.currentSubagents = [];
            state.currentAgentEvents = [
                { id: 1, event_type: 'tool_use', tool_name: 'Bash', summary: 'go test ./...', created_at: '2026-10-16T10:01:00Z' },
                { id: 2, event_type: 'tool_use', tool_name: 'Read', summary: 'main.go', created_at: '2026-10-16T10:02:00Z' }];
            window.__muts = 0;
            window.__mutLog = []; new MutationObserver(l => { for (const m of l) { const el = m.target.nodeType === 1 ? m.target : m.target.parentElement; if (!el || !window.__watched.some(id => { const w = document.getElementById(id); return w && w.contains(el); })) continue; window.__muts++; window.__mutLog.push(m.type + ' ' + (m.attributeName || '') + ' ' + (m.target.id || m.target.className || m.target.nodeName)); } }).observe(document.documentElement, { subtree: true, childList: true, attributes: true, characterData: true });
            // Only what this suite is about: the task list, its toggle and count, and the activity timeline and filter. The rest of the page
            // (the live session header and the activity chart redraw from their own feeds) is not under test, and a busy machine lets one of
            // those land inside the window (a 6-shard epoch run failed with 2 such mutations).
            window.__watched = ['board-task-list', 'board-task-hide-toggle-container', 'task-bar-count', 'agentic-state-events', 'event-filters'];
            return true;
        })()`);
        // The mutation observer reports after the call returns: let a task finish before reading the count.
        const settle = () => ev(`new Promise(r => setTimeout(() => r(window.__muts), 30))`);
        const redraw = () => ev(`(() => { T.tasks.renderBoardTaskList(); T.agentic.renderEventTimeline(); return true; })()`);

        console.log('1. The first draw shows the tasks and the events');
        await redraw();
        await settle();
        const rows = await ev(`document.querySelectorAll('#board-task-list [data-task-id], #board-task-list .board-task-row, #board-task-list tr').length`);
        assert.ok(rows >= 2 || (await ev(`document.getElementById('board-task-list').textContent.includes('First task')`)), 'the task rows are drawn');
        assert.ok(await ev(`document.getElementById('agentic-state-events').textContent.includes('go test')`), 'the events are drawn');
        assert.equal(await ev(`document.getElementById('task-bar-count').textContent`), '1/2');
        console.log('   ✓ tasks, events and the 1/2 count are drawn');

        console.log('2. Drawing again with nothing changed writes nothing');
        await settle();
        const before = await ev(`window.__muts`);
        const kept = await ev(`(() => { const r = document.querySelector('#board-task-list > *'); const e = document.querySelector('#agentic-state-events > *'); window.__r = r; window.__e = e; return !!r && !!e; })()`);
        assert.ok(kept);
        for (let i = 0; i < 5; i++) await redraw();
        await settle();
        const after = await ev(`window.__muts`);
        assert.equal(after - before, 0, `five redraws with the same data made ${after - before} DOM mutations: ${JSON.stringify((await ev('window.__mutLog')).slice(-10))}`);
        assert.ok(await ev(`document.querySelector('#board-task-list > *') === window.__r && document.querySelector('#agentic-state-events > *') === window.__e`), 'the same nodes are still there');
        console.log('   ✓ five identical redraws: 0 mutations, same nodes');

        console.log('3. A real change shows, and only what changed is rewritten');
        await ev(`T.state.currentBoardTasks[0].status = 'completed'; T.state.currentAgentEvents.unshift({ id: 3, event_type: 'tool_use', tool_name: 'Edit', summary: 'render.js', created_at: '2026-10-16T10:03:00Z' }); true`);
        await redraw();
        await settle();
        assert.equal(await ev(`document.getElementById('task-bar-count').textContent`), '2/2', 'the count follows');
        assert.ok(await ev(`document.getElementById('agentic-state-events').textContent.includes('render.js')`), 'the new event shows');
        assert.ok(await ev(`window.__muts`) > after, 'something was written');
        console.log('   ✓ the count reads 2/2 and the new event is there');

        console.log('\nAll idle DOM write checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
