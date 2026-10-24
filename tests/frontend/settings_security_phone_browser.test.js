// Settings > Security and privacy at 390 px and 200% text (#7041, UI/UX's #7036 finding 6): the pane scrolled sideways (347 px of content in 334), the API key field and the "password set" pill ran past
// its edge, and the Install ID was drawn one letter per line beside Copy. Nothing in the pane may leave it, at 100% and 200% text, with a real-length install id. Drives Chrome over CDP through
// agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: the pane fills from several answers after it opens
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const font of ['', '32px']) {
            await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true }); await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            if (font) await ev(`document.documentElement.style.fontSize = '${font}'; true`);
            await ev(`window.showSettingsModal('security'); true`);
            await waitFor(`!!document.getElementById('settings-telemetry-install-id') && document.getElementById('settings-telemetry-install-id').getClientRects().length > 0`, 'the Security pane', 8000);
            await sleep(700); // sleep-ok: the pane fills from several answers after it opens
            // a real install id is one unbroken 36-character string
            await ev(`document.getElementById('settings-telemetry-install-id').textContent = '0f8fad5b-d9cb-469f-a165-70867728950e'; true`);
            const tag = font ? '390 px, 200% text' : '390 px';
            const r = await ev(`(() => { const pane = document.getElementById('settings-pane-security'); const sc = pane.parentElement; const cr = sc.getBoundingClientRect();
                const out = [...pane.querySelectorAll('*')].filter(e => e.getClientRects().length && !e.closest('[hidden]')).filter(e => { const b = e.getBoundingClientRect(); return b.width && b.right > cr.right + 1; }).map(e => (e.id || e.className || e.tagName) + ' ' + Math.round(e.getBoundingClientRect().right - cr.right)).slice(0, 6);
                const id = document.getElementById('settings-telemetry-install-id'); const lh = parseFloat(getComputedStyle(id).lineHeight) || parseFloat(getComputedStyle(id).fontSize) * 1.3;
                return { sideways: sc.scrollWidth - sc.clientWidth, out, idLines: Math.round(id.getBoundingClientRect().height / lh) }; })()`);
            assert.ok(r.sideways <= 1, `${tag}: the pane does not scroll sideways (${r.sideways} px too wide)`);
            assert.deepEqual(r.out, [], `${tag}: nothing leaves the pane: ` + JSON.stringify(r.out));
            assert.ok(r.idLines <= 3, `${tag}: the install id is not drawn a letter at a time (${r.idLines} lines)`);
        }
        console.log('PASS settings security phone');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
