// First loads under an overloaded hub (#3430). A fake hub in the page answers chosen requests once
// with 503, Retry-After: 1 and X-Coral-Hub-Overload: 1, then lets them through. Each first load:
//   - shows "Loading… (Coral Hub busy, retrying)" meanwhile, never an error or an empty view;
//   - is in within about 1-2 s (the 1 s the hub asked for, not a doubled pause or a poll later);
//   1. the chat history (the hub's report: a long blank wait, then everything at once);
//   2. a burst of refused requests pauses for the Retry-After once, not the fifth step of the doubling;
//   3. the message board's messages (a refusal used to render as an empty board);
//   4. the board's tasks (a refusal used to show "No tasks" until the next 10 s poll).
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;
const { removeBoards } = require('./helpers/board_cleanup');
const BOARD = 'qa-busy-board';

// Wraps the harness's fetch mock (registered after it): a rule refuses its next `left` matching
// requests the way the hub does when it is congested.
const FAKE_HUB = `(() => {
    const busy = window.__busy = { rules: [], hits: [] };
    const inner = window.fetch;
    window.fetch = (url, opts = {}) => {
        let p = '';
        try { const u = new URL(String(url && url.url || url), location.href); p = u.pathname + u.search; } catch {}
        for (const r of busy.rules) {
            if (r.left > 0 && new RegExp(r.re).test(p)) {
                r.left--;
                busy.hits.push({ path: p, at: Math.round(performance.now()) });
                return Promise.resolve(new Response(JSON.stringify({ error: 'Coral Hub is busy' }), { status: 503,
                    headers: { 'content-type': 'application/json', 'Retry-After': '1', 'X-Coral-Hub-Overload': '1' } }));
            }
        }
        return inner(url, opts);
    };
    // What the reader ever saw: an error or an empty view in place of the busy line.
    window.__seen = new Set();
    const look = () => {
        if (document.querySelector('#live-history-messages .chat-load-error')) window.__seen.add('chat-error');
        const mb = document.getElementById('mb-messages');
        if (mb && /No messages yet/.test(mb.textContent)) window.__seen.add('board-empty');
        const bt = document.getElementById('board-task-list');
        if (bt && /No tasks/.test(bt.textContent)) window.__seen.add('tasks-empty');
        for (const el of document.querySelectorAll('.chat-history-busy, .mb-busy, .board-task-busy')) window.__seen.add('busy:' + el.textContent.trim());
    };
    document.addEventListener('DOMContentLoaded', () => new MutationObserver(look).observe(document.body, { childList: true, subtree: true, characterData: true }));
})();`;

async function api(method, path, body) {
    const r = await fetch(BASE + path, { method, headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
    assert.ok(r.ok, `${method} ${path}: ${r.status} ${await r.text()}`);
}

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install, select } = await connect();
    const BUSY = 'Loading… (Coral Hub busy, retrying)';
    const overload = `(await import((window.__coralStatic || '/static/') + 'overload.js'))`;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: FAKE_HUB });
        await Page.navigate({ url: BASE + '/' });
        await Page.loadEventFired();
        await install();

        console.log('1. Chat history, first load refused once (Retry-After: 1)');
        await ev(`(async () => { ${overload}._resetOverloadForTests(); window.__m.add('busy-agent', 'A', 30, { sleeping: false });
            window.__busy.rules.push({ re: '^/api/sessions/live/busy-agent/chat\\\\?', left: 1 }); window.__seen.clear(); return true; })()`);
        const t0 = Date.now();
        await select('busy-agent');
        await waitFor(`!!document.querySelector('#live-history-messages .chat-history-busy')`, 'the busy line', 1500);
        assert.equal(await ev(`document.querySelector('#live-history-messages .chat-history-busy').textContent`), BUSY);
        assert.equal(await ev(`document.querySelector('#live-history-messages .chat-history-busy').getAttribute('role')`), 'status');
        await waitFor(`window.__rendered().length > 0`, 'the history', 4000);
        const took = Date.now() - t0;
        assert.ok(took <= 2200, `the history is in ${took} ms after opening: about the 1 s the hub asked for, not a doubled pause or a poll later`);
        const seen = await ev(`[...window.__seen]`);
        assert.ok(!seen.includes('chat-error'), `no error was shown meanwhile (${JSON.stringify(seen)})`);
        assert.equal(await ev(`window.__busy.hits.length`), 1, 'refused once');
        assert.equal(await ev(`window.__m.requests.filter(r => r.kind === 'chat' && r.first && r.agent === 'busy-agent').length`), 1, 'then loaded once: no extra first loads');
        console.log(`   ✓ busy line shown, history in ${took} ms, no error`);

        console.log('2. A burst of refusals pauses for the Retry-After once');
        const pause = await ev(`(async () => {
            const o = ${overload}; o._resetOverloadForTests();
            window.__busy.rules.push({ re: '^/api/health', left: 5 });
            await Promise.all(Array.from({ length: 5 }, () => fetch('/api/health')));
            return o.pausedForMs();
        })()`);
        assert.ok(pause > 0 && pause <= 1250, `five refusals at once: one pause of Retry-After 1 s (x1-1.25), got ${pause} ms`);
        console.log(`   ✓ paused ${pause} ms (was the 5th doubling step, 16-24 s)`);
        await ev(`(async () => { ${overload}._resetOverloadForTests(); return true; })()`);

        console.log('3. Message board, first load refused once');
        await api('POST', `/api/board/${BOARD}/subscribe`, { subscriber_id: 'Busy Dev', job_title: 'Developer' });
        await api('POST', `/api/board/${BOARD}/messages`, { subscriber_id: 'Busy Dev', content: 'hello from a busy board' });
        // Only what this load draws counts: the list may still hold an earlier view's empty state.
        await ev(`(() => { window.__busy.rules.push({ re: '^/api/board/${BOARD}/messages/all\\\\?', left: 1 }); const mb = document.getElementById('mb-messages'); if (mb) mb.innerHTML = ''; window.__seen.clear(); return true; })()`);
        const t1 = Date.now();
        await ev(`(async () => { (await import((window.__coralStatic || '/static/') + 'message_board.js')).selectBoardProject(${JSON.stringify(BOARD)}); return true; })()`);
        await waitFor(`!!document.querySelector('#mb-messages .mb-busy')`, 'the board busy line', 1500);
        assert.equal(await ev(`document.querySelector('#mb-messages .mb-busy').textContent`), BUSY);
        await waitFor(`/hello from a busy board/.test(document.getElementById('mb-messages').textContent)`, 'the board message', 4000);
        const tookBoard = Date.now() - t1;
        assert.ok(tookBoard <= 2200, `the board is in ${tookBoard} ms, not at the next 10 s poll`);
        const seenBoard = await ev(`[...window.__seen]`);
        assert.ok(!seenBoard.includes('board-empty'), `the refusal never showed as an empty board (${JSON.stringify(seenBoard)})`);
        console.log(`   ✓ busy line shown, messages in ${tookBoard} ms, never "No messages yet"`);
        await ev(`(async () => { ${overload}._resetOverloadForTests(); return true; })()`);

        console.log('4. Board tasks, first load refused once');
        await api('POST', `/api/board/${BOARD}/tasks`, { subscriber_id: 'Operator', created_by: 'Operator', title: 'a task on a busy board' });
        await ev(`(() => { window.__busy.rules.push({ re: '^/api/board/${BOARD}/tasks\\\\?', left: 1 }); const bt = document.getElementById('board-task-list'); if (bt) bt.innerHTML = ''; window.__seen.clear(); return true; })()`);
        const t2 = Date.now();
        await ev(`(async () => {
            const tasks = await import((window.__coralStatic || '/static/') + 'tasks.js');
            T.state.selectedTeam = ${JSON.stringify(BOARD)};
            const sec = document.getElementById('board-tasks-section'); if (sec) sec.style.display = '';
            tasks.loadBoardTasks(${JSON.stringify(BOARD)});
            return true;
        })()`);
        await waitFor(`!!document.querySelector('#board-task-list .board-task-busy')`, 'the tasks busy line', 1500);
        assert.equal(await ev(`document.querySelector('#board-task-list .board-task-busy').textContent`), BUSY);
        await waitFor(`/a task on a busy board/.test(document.getElementById('board-task-list').textContent)`, 'the board task', 4000);
        const tookTasks = Date.now() - t2;
        assert.ok(tookTasks <= 2200, `the tasks are in ${tookTasks} ms, not at the next 10 s poll`);
        const seenTasks = await ev(`[...window.__seen]`);
        assert.ok(!seenTasks.includes('tasks-empty'), `the refusal never showed as "No tasks" (${JSON.stringify(seenTasks)})`);
        console.log(`   ✓ busy line shown, tasks in ${tookTasks} ms, never "No tasks"`);

        console.log('\nAll first-load-under-overload checks passed');
    } finally {
        await removeBoards(ev, [BOARD]);
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
