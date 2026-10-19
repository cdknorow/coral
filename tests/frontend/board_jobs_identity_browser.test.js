// Board chat identity and one-off job runs (#3171):  run via run_cdp_suites.mjs
// (6) The dashboard's team chat (the board page and the side panel) posts as the
//     reserved Operator identity, subscribed, like task actions; no "dashboard"
//     subscriber is created.
// (10) A one-off run in the Jobs sidebar is "Job run N" with a one-off badge,
//     never "__oneshot__" with a cron badge.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
const { removeBoards } = require('./helpers/board_cleanup');
    const api = async (path, method = 'GET', body) => {
        const r = await fetch(BASE + path, { method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
        const text = await r.text();
        return { status: r.status, body: text ? JSON.parse(text) : null };
    };
    const board = 'qa-identity-' + Date.now();
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await coralReady('startup', 'liveList', 'liveListLoaded');
        // A board with one agent on it.
        assert.equal((await api(`/api/board/${board}/subscribe`, 'POST', { subscriber_id: 'Lead', job_title: 'Lead Developer' })).status < 300, true);

        // ── (6) The board page: open, post.
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'message_board.js'); m.selectBoardProject(${JSON.stringify(board)}); return true; })()`);
        await waitFor(`!!document.getElementById('mb-post-input')`, 'the board page');
        await waitFor(`fetch('/api/board/${board}/subscribers').then(r => r.json()).then(s => (Array.isArray(s) ? s : s.subscribers || []).some(x => (x.subscriber_id || x.session_id) === 'Operator'))`, 'the Operator subscription', { timeout: 8000 });
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'message_board.js'); document.getElementById('mb-post-input').value = 'hello from the board page'; await m.postBoardMessage(); return true; })()`);
        // The side panel's team chat (render.js) posts the same way.
        await ev(`(async () => { const i = document.createElement('textarea'); i.id = 'board-panel-input'; i.value = 'hello from the side panel'; document.body.appendChild(i); await window.__coral._sendBoardChat(${JSON.stringify(board)}); i.remove(); return true; })()`);

        const msgs = (await api(`/api/board/${board}/messages/all?limit=50`)).body;
        const list = Array.isArray(msgs) ? msgs : (msgs.messages || []);
        const mine = list.filter(m => /hello from the (board page|side panel)/.test(m.content));
        assert.equal(mine.length, 2, 'both posts are on the board: ' + JSON.stringify(list.map(m => [m.subscriber_id, m.content])));
        for (const m of mine) assert.equal(m.subscriber_id, 'Operator', 'posted as Operator: ' + m.content);
        const subs = (await api(`/api/board/${board}/subscribers`)).body;
        const ids = (Array.isArray(subs) ? subs : subs.subscribers || []).map(s => s.subscriber_id || s.session_id);
        assert.ok(ids.includes('Operator'), 'Operator is subscribed: ' + ids);
        assert.ok(!ids.includes('dashboard'), 'no "dashboard" subscriber: ' + ids);
        console.log('PASS (6) team chat posts as the subscribed Operator (board page and side panel)');

        // ── (10) The Jobs sidebar: a one-off run.
        const row = await ev(`(async () => {
            const lj = await import((window.__coralStatic || '/static/') + 'live_jobs.js');
            lj.renderLiveJobs([
                { id: 42, status: 'running', trigger_type: 'api', job_name: null, started_at: new Date().toISOString() },
                { id: 43, status: 'running', trigger_type: 'cron', job_name: 'nightly', started_at: new Date().toISOString() },
            ]);
            const items = [...document.querySelectorAll('#live-jobs-list li, [id$=jobs-list] li')];
            return items.map(li => li.textContent.replace(/\\s+/g, ' ').trim());
        })()`);
        const oneOff = row.find(t => /Job run 42/.test(t));
        assert.ok(oneOff, 'the one-off run is "Job run 42": ' + JSON.stringify(row));
        assert.match(oneOff, /one-off/);
        assert.ok(!/__oneshot__|\bcron\b/.test(oneOff), 'no sentinel name or cron badge: ' + oneOff);
        assert.ok(row.some(t => /nightly/.test(t) && /\bcron\b/.test(t)), 'a scheduled run keeps its job name and cron badge');
        console.log('PASS (10) one-off runs: "Job run N" with a one-off badge');
    } finally {
        await removeBoards(ev, [board]);
        await client.close();
    }
})().catch(e => {
    console.error('FAIL ' + e.message);
    process.exitCode = 1;
});
