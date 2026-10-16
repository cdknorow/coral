// The home screen's text follows the text size (#4086, QA's nit): the eyebrow, the launch cards' glyph, text and arrow, the links and the
// "more ways" cards are in rem, so at 100 percent they are the sizes they always were and at 200 percent (the root font size doubled) they
// are twice that; the launch glyph stays inside its box. Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

// [selector, px at the default size]
const SIZES = [['.welcome-eyebrow', 11], ['.welcome-header p', 14], ['.welcome-launch-icon', 21], ['.welcome-launch-copy strong', 15], ['.welcome-launch-copy span', 12],
    ['.welcome-launch-arrow', 19], ['.welcome-links a, .welcome-links button', 13], ['.welcome-links .material-icons', 17]];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: the layout re-flows over a few frames; there is no completion event
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await waitFor(`!!document.querySelector('.welcome-launch-icon') && document.querySelector('.welcome-launch-icon').getClientRects().length > 0`, 'the home screen', 8000);
        const read = () => ev(`Object.fromEntries(${JSON.stringify(SIZES.map(s => s[0]))}.map(s => { const e = document.querySelector(s); return [s, e ? parseFloat(getComputedStyle(e).fontSize) : null]; }))`);
        const box = () => ev(`(() => { const e = document.querySelector('.welcome-launch-icon'); const b = e.getBoundingClientRect(); const g = document.createRange(); g.selectNodeContents(e); const r = g.getBoundingClientRect(); return { w: b.width, h: b.height, gw: r.width, gh: r.height }; })()`);
        const at100 = await read(); const box100 = await box();
        for (const [sel, px] of SIZES) assert.equal(at100[sel], px, `${sel} is ${px} px at the default size (got ${at100[sel]})`);
        assert.equal(box100.w, 42, 'the launch glyph box is 42 px at the default size');
        await ev(`document.documentElement.style.fontSize = '200%'; true`); await sleep(500); // sleep-ok: the layout re-flows over a few frames; no completion event
        const at200 = await read(); const box200 = await box();
        for (const [sel, px] of SIZES) assert.equal(at200[sel], px * 2, `${sel} follows the text size (${at200[sel]} at 200 percent)`);
        assert.equal(box200.w, 84, 'the glyph box grows with the glyph');
        assert.ok(box200.gw <= box200.w + 0.5 && box200.gh <= box200.h + 0.5, `the glyph (${box200.gw} x ${box200.gh}) stays inside its box (${box200.w} x ${box200.h})`);
        console.log('\nPASS the home screen follows the text size');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
