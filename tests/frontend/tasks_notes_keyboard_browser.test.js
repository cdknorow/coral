// The side panel's Tasks list and Notes by keyboard (#4026 follow-up): a task row opens its details and a column heading sorts on
// Enter or Space (both were mouse-only), and the rendered notes open for editing on Enter. Tasks come from the page state; run
// against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const press = async (k, code, vk) => { const base = { key: k, code, windowsVirtualKeyCode: vk }; await Input.dispatchKeyEvent({ type: 'keyDown', ...base, ...(k === 'Enter' ? { text: '\r' } : k === ' ' ? { text: ' ' } : {}) }); await Input.dispatchKeyEvent({ type: 'keyUp', ...base }); };
    const S = `(window.__coralStatic || '/static/')`;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`window.__m.add('task-bot', 'A', 3, { sleeping: false }); true`);
        await new Promise(r => setTimeout(r, 600)); // sleep-ok: the first live list renders from several answers; no single completion event
        await ev(`window.selectLiveSession('task-bot', 'claude', 'task-bot-id', 'local'); true`);
        await waitFor(`!!document.getElementById('board-task-list')`, 'the tasks panel', 8000);
        await ev(`window.switchAgenticTab('tasks', 'top'); true`);
        await new Promise(r => setTimeout(r, 1500)); // sleep-ok: selecting the agent starts task loads whose late answers would redraw the list; no single completion event
        await ev(`(async () => { const { state } = await import(${S} + 'state.js'); const t = await import(${S} + 'tasks.js'); t.stopBoardTaskPoll();
            state.currentBoardTasks = [{ id: 11, title: 'Write the keyboard test', status: 'pending', priority: 'high', created_at: '2026-10-01T10:00:00Z', assigned_to: 'Backend' },
                                       { id: 12, title: 'Fix the focus ring', status: 'pending', priority: 'low', created_at: '2026-10-02T10:00:00Z', assigned_to: 'QA' }];
            document.getElementById('board-tasks-section').style.display = '';
            t.renderBoardTaskList(); return true; })()`);
        await waitFor(`document.querySelectorAll('#board-task-list [data-on-click="task-open"]').length === 2`, 'two task rows', 5000);

        console.log('1. Tasks: rows and headings');
        const kinds = await ev(`[...document.querySelectorAll('#board-task-list [data-on-click="task-sort"]')].map(e => ({ tab: e.tabIndex, role: e.getAttribute('role') }))`);
        assert.ok(kinds.length >= 3 && kinds.every(k => k.tab === 0 && k.role === 'button'), 'the headings are focusable buttons: ' + JSON.stringify(kinds));
        // a task row is neither (#5813): its one keyboard control is the native Open button, named by the task and its status
        const rowKinds = await ev(`[...document.querySelectorAll('#board-task-list [data-on-click="task-open"]')].map(e => ({ tab: e.tabIndex, role: e.getAttribute('role'), open: e.querySelector('button.board-task-open')?.getAttribute('aria-label') || '' }))`);
        assert.ok(rowKinds.length === 2 && rowKinds.every(k => k.tab === -1 && k.role === null && /^Open task #\d+: .+, .+$/.test(k.open)), 'rows are not buttons; each holds a named native Open button: ' + JSON.stringify(rowKinds));
        const sortLabel = () => ev(`document.querySelector('#board-task-list [data-sort="priority"]').textContent`);
        assert.equal((await sortLabel()).includes('▲') || (await sortLabel()).includes('▼'), false, 'not sorted by priority yet');
        for (const [k, code, vk] of [['Enter', 'Enter', 13], [' ', 'Space', 32]]) {
            await ev(`document.querySelector('#board-task-list [data-sort="priority"]').focus(); true`);
            const before = await sortLabel();
            await press(k, code, vk);
            await waitFor(`document.querySelector('#board-task-list [data-sort="priority"]').textContent !== ${JSON.stringify(before)}`, `${JSON.stringify(k)} sorts by the heading`, 4000);
        }
        await ev(`document.querySelector('#board-task-list .board-task-open').focus(); true`);
        await press('Enter', 'Enter', 13);
        await waitFor(`getComputedStyle(document.getElementById('task-detail-modal')).display !== 'none'`, 'the task details open on Enter', 5000);
        assert.ok((await ev(`document.getElementById('task-detail-modal').textContent`)).includes('keyboard test') || (await ev(`document.getElementById('task-detail-modal').textContent`)).includes('focus ring'), 'the details are the task\'s');
        await ev(`(async () => { const m = await import(${S} + 'tasks.js'); m.hideTaskDetailModal?.(); document.getElementById('task-detail-modal').style.display = 'none'; return true; })()`);
        console.log('   ✓ Enter opens a task, Enter and Space sort by a heading');

        console.log('2. Notes');
        await ev(`window.switchAgenticTab('notes', 'top'); true`);
        const r = await ev(`(() => { const e = document.getElementById('note-md-rendered'); return { tab: e.tabIndex, role: e.getAttribute('role'), label: e.getAttribute('aria-label') }; })()`);
        assert.deepEqual(r, { tab: 0, role: 'button', label: 'Edit notes' }, 'the rendered notes are a named, focusable button');
        await ev(`document.getElementById('note-md-rendered').focus(); true`);
        await press('Enter', 'Enter', 13);
        await waitFor(`document.activeElement === document.getElementById('note-md-editor')`, 'Enter starts editing and focus moves to the editor', 4000);
        console.log('   ✓ Enter on the notes starts editing');
        console.log('\nPASS tasks and notes by keyboard');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
