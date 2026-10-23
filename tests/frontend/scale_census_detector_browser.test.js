// The 200% text census's detector (#6256, WCAG 1.4.4) finds what it is for: on a page with a fixed-height chip whose label is cut, two controls drawn over each other, a control past the screen's edge and a
// row wider than the screen, at a root font of 32 px, helpers/phone_targets.js's SCALE reports each; a clean page reports none of them. A census that finds nothing is only worth something if it can find something.
// Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { SCALE } = require('./helpers/phone_targets');
(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`document.documentElement.style.fontSize = '32px'; true`);
        await new Promise(r => setTimeout(r, 400)); // sleep-ok: the page reflows at the new size
        const clean = await ev(SCALE);
        assert.deepEqual([clean.hscroll, clean.offscreen, clean.overlap, clean.clipped], [null, [], [], []], 'the dashboard itself has none of the four at 200%: ' + JSON.stringify(clean));
        await ev(`(() => {
            const d = document.createElement('div'); d.style.cssText = 'position:fixed;top:100px;left:0;z-index:99999;background:#fff;color:#000;width:390px';
            d.innerHTML = '<button style="display:block;height:20px;width:60px;overflow:hidden;white-space:nowrap">Chip label that is long</button><div style="position:relative">'
                + '<button style="position:absolute;left:350px;top:60px;width:100px;height:40px">Gamma</button><button style="position:absolute;left:0;top:0;width:100px;height:40px">Alpha</button><button style="position:absolute;left:50px;top:0;width:100px;height:40px">Beta</button></div>';
            document.body.appendChild(d);
            const w = document.createElement('div'); w.style.cssText = 'width:600px;height:20px;background:red'; document.body.appendChild(w); return true; })()`);
        const r = await ev(SCALE);
        assert.ok(r.clipped.some(c => /Chip label/.test(c)), 'a cut label is found: ' + JSON.stringify(r));
        assert.ok(r.overlap.some(o => /Alpha/.test(o) && /Beta/.test(o)), 'two controls over each other are found: ' + JSON.stringify(r));
        assert.ok(r.offscreen.some(o => /Gamma/.test(o)), 'a control past the edge is found: ' + JSON.stringify(r));
        assert.ok(r.hscroll, 'a row wider than the screen is found: ' + JSON.stringify(r));
        console.log('PASS the 200% detector finds a cut label, an overlap, a control past the edge and a wide row, and nothing on the dashboard itself');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
