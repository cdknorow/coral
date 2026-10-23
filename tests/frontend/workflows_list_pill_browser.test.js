// The Workflows list says "disabled" only on a workflow that is (#6515 finding 7, #6521): a green "enabled" pill on every card was noise. 1280 px, a stubbed list.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const LIST = [{ id: 1, name: 'nightly-build', description: 'Build and test', enabled: true, step_count: 2, last_run: { status: 'failed', started_at: new Date().toISOString() } },
        { id: 2, name: 'release-notes', description: '', enabled: false, step_count: 1, last_run: null }];
    const STUB = `(() => { const real = window.fetch; window.fetch = (u, o) => /\\/api\\/workflows$/.test(String(u)) && !(o && o.method && o.method !== 'GET') ? Promise.resolve(new Response(JSON.stringify({ workflows: ${JSON.stringify(LIST)} }), { status: 200, headers: { 'Content-Type': 'application/json' } })) : real(u, o); return true; })()`;
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await Page.addScriptToEvaluateOnNewDocument({ source: STUB });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        await ev(`window.switchNavTab('workflows'); true`);
        await waitFor(`document.querySelectorAll('#wf-list .wf-card').length === 2`, 'the two workflows', 8000);
        const pills = await ev(`[...document.querySelectorAll('#wf-list .wf-card')].map(c => [c.querySelector('.wf-card-name, h3, .wf-name')?.textContent.trim() || c.textContent.trim().slice(0, 14), [...c.querySelectorAll('.wf-badge')].map(b => b.textContent.trim())])`);
        assert.deepEqual(pills.map(p => p[1].filter(t => /enabled|disabled/.test(t))), [[], ['disabled']], 'no "enabled" pill; "disabled" only where it is: ' + JSON.stringify(pills));
        console.log('PASS workflows list: only a disabled workflow says so');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
