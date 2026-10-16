// Agent list render batching (#3121 step 2a): feed messages schedule the list
// render, so a burst renders once per animation frame; a synchronous render (a
// user action) drops the pending one; flushLiveSessionsRender() renders at once.
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;
const { framesJs } = require('./helpers/wait');

(async () => {
    const { client, Page, ev, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD + `;window.__renders = 0; window.addEventListener('coral:live-list-rendered', () => window.__renders++);` });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);

        const r = await ev(`(async () => {
            const st = window.__coralStatic || '/static/';
            const ws = await import(st + 'websocket.js');
            const render = await import(st + 'render.js');
            const frames = n => ${framesJs('n')};
            const mk = i => ({ name: 'batch-' + i, session_id: 'batch-' + i, agent_type: 'claude', server: 'local', status: 'working', summary: 's' + i, display_name: 'B' + i });
            ws.handleCoralMessage({ type: 'coral_update', sessions: Array.from({ length: 20 }, (_, i) => mk(i)) });
            await frames(2);
            const out = {};

            // 1. A burst of 10 diffs in one task: one render, in the next frame.
            let before = window.__renders;
            for (let k = 0; k < 10; k++) ws.handleCoralMessage({ type: 'coral_diff', changed: [{ name: 'batch-1', session_id: 'batch-1', server: 'local', summary: 'burst ' + k }] });
            out.burstSync = window.__renders - before;
            await frames(2);
            out.burstRenders = window.__renders - before;
            out.burstLatest = document.getElementById('live-sessions-list').textContent.includes('burst 9');

            // 2. A synchronous render drops the pending scheduled one.
            before = window.__renders;
            ws.handleCoralMessage({ type: 'coral_diff', changed: [{ name: 'batch-2', session_id: 'batch-2', server: 'local', summary: 'sync wins' }] });
            render.renderLiveSessions((await import(st + 'state.js')).state.liveSessions);
            await frames(2);
            out.syncRenders = window.__renders - before;

            // 3. flushLiveSessionsRender renders at once; nothing pending afterwards.
            before = window.__renders;
            ws.handleCoralMessage({ type: 'coral_diff', changed: [{ name: 'batch-3', session_id: 'batch-3', server: 'local', summary: 'flushed' }] });
            render.flushLiveSessionsRender();
            out.flushSync = window.__renders - before;
            out.flushInDom = document.getElementById('live-sessions-list').textContent.includes('flushed');
            await frames(2);
            out.flushTotal = window.__renders - before;
            return out;
        })()`);
        assert.equal(r.burstSync, 0, 'feed messages do not render synchronously');
        assert.equal(r.burstRenders, 1, `a burst of 10 feed messages renders once (${r.burstRenders})`);
        assert.ok(r.burstLatest, 'and shows the latest state');
        assert.equal(r.syncRenders, 1, `a synchronous render drops the pending one (${r.syncRenders})`);
        assert.equal(r.flushSync, 1, 'flushLiveSessionsRender renders at once');
        assert.ok(r.flushInDom, 'the flushed state is in the DOM');
        assert.equal(r.flushTotal, 1, 'nothing left pending after a flush');
        console.log('PASS agent_list_batching_browser');
    } finally {
        await client.close();
    }
})().catch(e => { console.error(e); process.exit(1); });
