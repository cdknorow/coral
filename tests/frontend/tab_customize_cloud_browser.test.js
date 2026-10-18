// #4741 (operator, via coral-hub): the panel's Customize tabs menu lists exactly the tabs the row shows, each with an icon. On the hosted
// hub the Saved tab is hidden by the hub's stylesheet (data-local-only), and the menu used to list it, ticked, with nothing to tick; its
// row also had no icon because Saved draws an inline SVG, not a font glyph. On a local Coral Saved is listed, with its SVG as the icon.
// Cloud mode is the hub's boot flag injected into Coral's own page (as in hub_readonly_browser). Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const { Network, Fetch } = client;
    const cloud = { on: false };
    const menu = () => ev(`(() => { document.getElementById('agentic-tab-customize').click(); const rows = [...document.querySelectorAll('#tab-customize .tc-row')].map(r => ({ tab: r.dataset.tab, glyph: (r.querySelector('.tc-icon.material-icons') || {}).textContent || '', svg: !!r.querySelector('.tc-icon svg') })); document.getElementById('tab-customize')?.remove(); return rows; })()`);
    const shown = () => ev(`[...document.querySelectorAll('#agentic-block-top .agentic-tab[data-tab]')].filter(b => getComputedStyle(b).display !== 'none').map(b => b.dataset.tab)`);
    const load = async () => {
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install(); await ev(`window.__coralReady.then(() => true)`);
        await ev(`localStorage.removeItem('coral-agentic-tab-prefs'); window.__m.add('tab-bot', 'A', 3, { sleeping: false }); true`);
        await sleep(600); // sleep-ok: the first live list renders from several answers; no single completion event
        await ev(`window.selectLiveSession('tab-bot', 'claude', 'tab-bot-id', 'local'); true`);
        await ev(`(() => { const p = document.getElementById('agentic-state'); if (p.classList.contains('collapsed') || document.getElementById('agentic-tab-files').getBoundingClientRect().width === 0) window.toggleAgenticPanel(); return true; })()`);
        await waitFor(`document.getElementById('agentic-tab-files').getBoundingClientRect().width > 0`, 'side panel open', 8000);
    };
    try {
        await Page.enable(); await Network.enable(); await Network.setBypassServiceWorker({ bypass: true });
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        const intercept = () => Fetch.enable({ patterns: [{ urlPattern: `${BASE}/`, resourceType: 'Document', requestStage: 'Response' }, { urlPattern: `${BASE}/auth/session`, requestStage: 'Request' }] });
        Fetch.requestPaused(async e => {
            if (e.request.url.endsWith('/auth/session')) {
                return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }],
                    body: Buffer.from(JSON.stringify({ authenticated: true, email: 'qa@example.com', csrf: 'qa-csrf' })).toString('base64') });
            }
            if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
            const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
            let html = base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body;
            if (cloud.on) html = html.replace('{"mode":"local"}', '{"mode":"cloud"}');
            const headers = (e.responseHeaders || []).filter(h => !/^content-length$/i.test(h.name));
            return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: headers, body: Buffer.from(html).toString('base64') });
        });

        console.log('1. A local Coral: Saved is in the row and in the menu, with its icon');
        await load();
        assert.equal(await ev(`document.body.classList.contains('cloud-mode')`), false);
        let rows = await menu();
        assert.ok((await shown()).includes('saved'), 'Saved is in the row');
        assert.ok(rows.some(r => r.tab === 'saved'), 'Saved is in the menu: ' + JSON.stringify(rows.map(r => r.tab)));
        assert.deepEqual(rows.filter(r => !r.glyph && !r.svg).map(r => r.tab), [], 'every row has an icon');
        assert.equal(rows.find(r => r.tab === 'saved').svg, true, "Saved's icon is its SVG");

        console.log('2. The hosted hub: the menu lists the tabs the row shows, each with an icon, and not Saved');
        cloud.on = true; await intercept();
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await waitFor(`!!document.getElementById('agentic-tab-customize')`, 'the tab row is in the page', 10000);
        await ev(`(async () => { const base = (window.__coralStatic || '/static/'); const { state } = await import(base + 'state.js'); const { showView } = await import(base + 'utils.js');
            state.currentSession = { type: 'live', name: 'tab-bot', session_id: 'tab-bot-id', board_project: 'tab-team' }; showView('live-session-view');
            const side = document.getElementById('agentic-state'); side.classList.remove('collapsed'); side.style.width = '680px'; return true; })()`);
        await waitFor(`document.getElementById('agentic-tab-files').getBoundingClientRect().width > 0`, 'the panel is open', 8000);
        assert.equal(await ev(`document.body.classList.contains('cloud-mode')`), true);
        rows = await menu();
        const visible = await shown();
        assert.equal(visible.includes('saved'), false, 'Saved is not in the row');
        assert.equal(rows.some(r => r.tab === 'saved'), false, 'and not in the menu');
        assert.deepEqual(rows.map(r => r.tab).filter(t => !visible.includes(t)), [], 'every menu row is a tab the row shows');
        assert.deepEqual(rows.filter(r => !r.glyph && !r.svg).map(r => r.tab), [], 'every row has an icon');
        console.log('PASS');
    } catch (e) {
        console.error(e && e.stack || e);
        process.exitCode = 1;
    } finally {
        try { await client.close(); } catch (_) {}
    }
})();
