// The composer grows with the text size (#4086 follow-up): at 200 percent (the root font size doubled) its placeholder is not clipped,
// on a phone and on the desktop card, and at the default size the box is the size it always was. The box's content height must hold
// the placeholder as laid out at the box's own width and font. Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: the layout re-flows over a few frames; there is no completion event
    const MEASURE = `(() => { const t = document.getElementById('command-input'); const cs = getComputedStyle(t); const probe = document.createElement('div');
        probe.style.cssText = 'position:absolute;visibility:hidden;white-space:pre-wrap;overflow-wrap:break-word;box-sizing:content-box;width:' + (t.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight)) + 'px;font:' + cs.font + ';line-height:' + cs.lineHeight + ';letter-spacing:' + cs.letterSpacing + ';font-size:' + getComputedStyle(t, '::placeholder').fontSize; // the placeholder's own size: on a phone it is scaled to its widest word (#6971)
        probe.textContent = t.placeholder; document.body.appendChild(probe); const need = probe.getBoundingClientRect().height; probe.remove();
        return { have: t.clientHeight - parseFloat(cs.paddingTop) - parseFloat(cs.paddingBottom), need, box: t.offsetHeight, font: cs.fontSize, placeholder: t.placeholder.length }; })()`;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        const results = {};
        for (const [dev, w, h, mobile] of [['phone', 390, 844, true], ['desktop', 1280, 800, false]]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            await Emulation.setTouchEmulationEnabled(mobile ? { enabled: true, maxTouchPoints: 5 } : { enabled: false });
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('a-bot-with-a-rather-long-name-here', 'A', 4, { sleeping: false }); true`); await sleep(800); // sleep-ok: the layout re-flows over a few frames; no completion event
            await ev(`window.selectLiveSession('a-bot-with-a-rather-long-name-here', 'claude', 'a-bot-with-a-rather-long-name-here-id', 'local'); true`);
            await waitFor(`!!document.getElementById('command-input') && document.getElementById('command-input').offsetHeight > 0`, 'the composer', 8000);
            await sleep(600); // sleep-ok: the layout re-flows over a few frames; no completion event
            for (const pct of ['100%', '200%']) {
                await ev(`document.documentElement.style.fontSize = ${JSON.stringify(pct)}; window.dispatchEvent(new Event('resize')); true`); await sleep(700); // sleep-ok: the layout re-flows over a few frames; no completion event
                results[`${dev} ${pct}`] = await ev(MEASURE);
            }
            await ev(`document.documentElement.style.fontSize = ''; true`);
        }
        console.log(JSON.stringify(results));
        for (const [k, r] of Object.entries(results)) assert.ok(r.have + 1 >= r.need, `${k}: the composer's text area (${r.have} px) holds the placeholder (${r.need} px)`);
        assert.ok(results['phone 200%'].box > results['phone 100%'].box, 'on a phone the box is taller at 200 percent');
        assert.ok(results['desktop 200%'].box > results['desktop 100%'].box, 'on the desktop card the box is taller at 200 percent');
        console.log('\nPASS the composer holds its placeholder at 200 percent text');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
