// #4742 follow-up (UX review): opening Saved while it already shows (the toast's "View saved", the jump bar's Back to Saved) must not forget what the dock was before the
// first open: closing then puts the dock back on its tab, open, instead of shutting it.
'use strict';
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`window.__m.add('sv-bot', 'A', 3, { sleeping: false }); true`); await sleep(600); // sleep-ok: the first live list renders from several answers; no single completion event
        await ev(`window.selectLiveSession('sv-bot', 'claude', 'sv-bot-id', 'local'); true`);
        await ev(`(() => { const p = document.getElementById('agentic-state'); if (p.classList.contains('collapsed')) window.toggleAgenticPanel(true); return true; })()`);
        await waitFor(`document.getElementById('agentic-tab-files').getBoundingClientRect().width > 0`, 'the dock is open', 8000);
        await ev(`document.getElementById('agentic-tab-files').click(); true`);
        const st = () => ev(`({ open: !document.getElementById('agentic-state').classList.contains('collapsed'), active: document.querySelector('#agentic-block-top .agentic-tab.active')?.dataset.tab, pressed: document.getElementById('saved-btn').getAttribute('aria-pressed') })`);
        await ev(`document.getElementById('saved-btn').click(); true`);
        await waitFor(`document.getElementById('saved-btn').getAttribute('aria-pressed') === 'true'`, 'Saved shows');
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'saved.js'); await m.openSaved(); return true; })()`);
        assert.equal((await st()).pressed, 'true', 'still showing after a second open');
        await ev(`document.getElementById('saved-btn').click(); true`);
        await waitFor(`document.getElementById('saved-btn').getAttribute('aria-pressed') === 'false'`, 'Saved closed');
        assert.deepEqual(await st(), { open: true, active: 'files', pressed: 'false' }, 'the dock is back on Files, open');
        console.log('PASS');
    } catch (e) { console.error(e && e.stack || e); process.exitCode = 1; } finally { try { await client.close(); } catch (_) {} }
})();
