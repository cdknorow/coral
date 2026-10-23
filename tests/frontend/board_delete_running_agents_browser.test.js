// A board is not deleted while its agents run (#5043): they would stay live in a sidebar group named with the raw board id. The Delete button says so, naming them, instead of asking;
// the server's 409 (an agent this page's list does not show yet) is shown as its message, with the board still open; once nothing runs the delete asks and goes through. Chrome over CDP
// against the isolated dev server, with the harness's mocked live list; the board itself is real.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const B = 'bdr-' + Date.now().toString(36);
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: `window.__qaSeed = [{ name: 'bd-agent', prefix: 'A', n: 2, sleeping: false, board_project: ${JSON.stringify(B)}, session_id: 'bd-agent-id' }];` });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        let l = Page.loadEventFired(); await Page.navigate({ url: BASE + '/' }); await l;
        await ev(`fetch('/api/board/${B}/messages', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'qa', as: 'QA Engineer', content: 'a board to delete' }) }).then(r => r.status)`);
        l = Page.loadEventFired(); await Page.navigate({ url: `${BASE}/board/${B}` }); await l;
        await install();
        await ev(`window.__coralReady.then(() => true)`);
        await waitFor(`!!document.getElementById('mb-delete-btn') && document.getElementById('mb-more-btn').getBoundingClientRect().width > 0`, 'the board and its More menu (Delete sits behind it on a wide screen, #6279)', 15000);
        await waitFor(`(window.T.state.liveSessions || []).some(s => s.name === 'bd-agent')`, 'the running agent in the list', 15000);
        await ev(`window.__confirm = null; window.__deletes = []; window.__server409 = false;
            window.showConfirmModal = (title, msg, cb) => { window.__confirm = { title, msg, cb }; };
            { const R = window.fetch; window.fetch = (u, o) => { if (o && o.method === 'DELETE' && /\\/api\\/board\\/[^/?]+$/.test(String(u))) { window.__deletes.push(String(u));
                if (window.__server409) return Promise.resolve(new Response(JSON.stringify({ error: '1 agent is still on this board (Late Agent): kill it or move it first', code: 'board_has_running_agents' }), { status: 409, headers: { 'content-type': 'application/json' } })); }
                return R(u, o); }; }
            true`);
        const toasts = () => ev(`[...document.querySelectorAll('.toast')].map(t => t.textContent)`);
        const clickDelete = () => ev(`document.getElementById('mb-delete-btn').click(); true`);
        const boardListed = () => ev(`fetch('/api/board/projects').then(r => r.json()).then(p => (Array.isArray(p) ? p : p.projects || []).some(x => x.project === ${JSON.stringify(B)}))`);

        console.log('1. An agent is running on the board: Delete says so, names it, asks nothing and sends nothing');
        await clickDelete();
        await waitFor(`[...document.querySelectorAll('.toast')].some(t => /1 agent is still on this board \\(bd-agent\\): kill it or move it first/.test(t.textContent))`, 'the toast', 6000);
        assert.equal(await ev(`window.__confirm`), null, 'no confirmation is asked');
        assert.deepEqual(await ev(`window.__deletes`), [], 'no DELETE is sent');
        assert.equal(await boardListed(), true);
        console.log('   ✓');

        console.log('2. The agent is asleep: Delete asks; the server then refuses (an agent the list did not show): its message is shown and the board stays');
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); window.__feed('bd-agent-id', { sleeping: true }); true`);
        await waitFor(`((window.T.state.liveSessions || []).find(s => s.name === 'bd-agent') || {}).sleeping === true`, 'the agent asleep in the list', 6000);
        await clickDelete();
        assert.equal(await ev(`window.__confirm && window.__confirm.title`), 'Delete Board', 'a board with nothing running asks first');
        assert.deepEqual(await toasts(), [], 'and says nothing else');
        await ev(`window.__server409 = true; window.__confirm.cb(); true`);
        await waitFor(`[...document.querySelectorAll('.toast')].some(t => /1 agent is still on this board \\(Late Agent\\)/.test(t.textContent))`, 'the server\'s message', 6000);
        assert.equal((await ev(`window.__deletes`)).length, 1, 'one DELETE went out');
        assert.equal(await boardListed(), true, 'the board is still there');
        assert.ok(await ev(`!!document.getElementById('mb-delete-btn')`), 'and still open: the page did not leave for the board list');
        console.log('   ✓');

        console.log('3. Nothing runs: the delete goes through');
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); window.__server409 = false; window.__confirm.cb(); true`);
        await waitFor(`!document.getElementById('messageboard-view').classList.contains('mb-has-board')`, 'the page leaving the deleted board', 10000);
        assert.equal(await boardListed(), false, 'the board is gone');
        console.log('   ✓');
    } finally { await removeBoards(ev, [B]); await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
