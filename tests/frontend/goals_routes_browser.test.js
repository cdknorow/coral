// The Goals view against the REAL goals routes (#7113, #7087): goals and tasks are made through the API of the isolated server, then read, opened and created through the view. Checks the answers the
// adapter relies on: the list asked for every state, the rollup's numbers in the sentence, a goal's tasks from tasks?goal=, a refused create's code, a closed goal under Done. Drives Chrome over CDP.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');
const BASE = require('./test_base').BASE;
const S = `(window.__coralStatic || '/static/')`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const board = `goals-real-${Date.now()}`;
    const api = (method, path, body) => ev(`fetch('/api/board/${board}${path}', { method: ${JSON.stringify(method)}, headers: { 'Content-Type': 'application/json' }, body: ${body ? JSON.stringify(JSON.stringify(body)) : 'undefined'} }).then(async r => ({ status: r.status, json: await r.json().catch(() => null) }))`);
    const texts = sel => ev(`[...document.querySelectorAll(${JSON.stringify(sel)})].map(e => e.textContent.replace(/\\s+/g, ' ').trim())`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`localStorage.setItem('coral.board.face', 'goals'); true`);
        await ev(`window.__coralReady.then(() => true)`);
        await api('POST', '/messages', { subscriber_id: 'Operator', as: 'Operator', content: 'a board for goals' });
        const g1 = await api('POST', '/goals', { subscriber_id: 'Operator', title: 'Ship it', done_when: 'it is shipped', outcome: 'Shipped.' });
        assert.equal(g1.status, 201, JSON.stringify(g1)); const id1 = g1.json.id || (g1.json.goal && g1.json.goal.id);
        const g2 = await api('POST', '/goals', { subscriber_id: 'Operator', title: 'Proposed idea', state: 'proposed' });
        assert.equal(g2.status, 201);
        const g3 = await api('POST', '/goals', { subscriber_id: 'Operator', title: 'Already finished' });
        const id3 = g3.json.id || (g3.json.goal && g3.json.goal.id);
        for (const title of ['First task', 'Second task']) { const t = await api('POST', '/tasks', { subscriber_id: 'Operator', title, goal_id: id1 }); assert.ok(t.status < 300, `a task is linked at create: ${JSON.stringify(t)}`); }
        const closed = await api('POST', `/goals/${id3}/close`, { subscriber_id: 'Operator', reason: 'done in a test' });
        assert.equal(closed.status, 200, JSON.stringify(closed));
        console.log('the view reads what the routes answer');
        await ev(`(async () => { const m = await import(${S} + 'message_board.js'); const { showView } = await import(${S} + 'utils.js'); showView('messageboard-view'); m.selectBoardProject(${JSON.stringify(board)}); return true; })()`);
        await waitFor(`document.querySelectorAll('#mb-goals .goal-card').length === 3`, 'three goals (the closed one too: the list asks for every state)', 10000);
        assert.deepEqual(await texts('.goals-chip'), ['All 3', 'Active 1', 'Blocked 0', 'Proposed 1', 'Done 1', 'Dropped 0']);
        assert.equal((await texts(`.goal-card[data-goal-id="${id1}"] .goal-card-sentence`))[0], '2 tasks: 2 queued', 'the rollup\'s numbers, as a sentence');
        assert.equal((await texts(`.goal-card[data-goal-id="${id3}"] .goal-state-done`))[0], 'Done');
        await ev(`document.querySelector('.goal-card[data-goal-id="${id1}"]').click(); true`);
        await waitFor(`document.querySelectorAll('#mb-goals .goal-task').length === 2`, 'the goal\'s two tasks from tasks?goal=', 10000);
        assert.deepEqual(await texts('.goal-task-title'), ['First task', 'Second task']);
        assert.deepEqual(await texts('.goals-sub'), ['Outcome', 'Done when', 'Tasks']);
        console.log('New goal through the real route');
        await ev(`document.querySelector('.goals-new').click(); true`);
        await waitFor(`!!document.querySelector('dialog.goals-dialog[open]')`, 'the dialog', 3000);
        await ev(`document.getElementById('goal-new-title').value = 'x'.repeat(250); document.querySelector('.goals-dialog button[type=submit]').click(); true`);
        await waitFor(`/was not created: /.test(document.querySelector('.goals-field-error').textContent)`, 'the refusal', 6000);
        console.log('   a refusal says:', (await texts('.goals-field-error'))[0]);
        await ev(`document.getElementById('goal-new-title').value = 'Made in the dialog'; document.getElementById('goal-new-done').value = 'it shows up'; document.querySelector('.goals-dialog button[type=submit]').click(); true`);
        await waitFor(`!document.querySelector('dialog.goals-dialog')`, 'the dialog to close', 6000);
        await waitFor(`document.querySelectorAll('.goal-card').length === 4`, 'the new goal in the list', 6000);
        assert.ok((await texts('.goal-card-title')).includes('Made in the dialog'));
        const listed = await api('GET', '/goals');
        assert.ok(listed.json.goals.some(g => g.title === 'Made in the dialog'), 'and the route has it');
        console.log('PASS the Goals view against the real routes');
    } finally { await removeBoards(ev, [board]).catch(() => {}); await client.close(); }
})().catch(err => { console.error(err); process.exit(1); });
