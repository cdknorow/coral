// The composer box grows with its text, and autosizing it is skipped when nothing it depends on changed (#3866: measuring forces layout
// several times, 112 ms over five agent switches in the dashboard profile). Pins:
//  - typing several lines makes the box taller, clearing it makes it short again;
//  - setting the same value again, or applying the view mode again, measures nothing (no scrollHeight read);
//  - a different value, or a different window width that rewraps the text, measures again and the height follows.
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    const { Input, Emulation } = client;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('as-a', 'A', 3, { sleeping: false }); true`);
        await ev(`window.selectLiveSession('as-a', 'claude', 'as-a-id', 'local'); true`);
        await waitFor(`!!document.getElementById('command-input') && !document.getElementById('command-input').disabled && !!document.querySelector('#capture-wrapper.chat-mode')`, 'the composer in chat mode', 8000);
        const h = () => ev(`document.getElementById('command-input').offsetHeight`);
        // Count reads of scrollHeight, the measurement autosize makes.
        await ev(`(() => { window.__sh = 0; const d = Object.getOwnPropertyDescriptor(Element.prototype, 'scrollHeight'); Object.defineProperty(HTMLTextAreaElement.prototype, 'scrollHeight', { configurable: true, get() { window.__sh++; return d.get.call(this); } }); return true; })()`);
        const reads = () => ev(`window.__sh`);

        console.log('1. Typing lines makes the box taller; clearing it shrinks it');
        await waitFor(`document.getElementById('command-input').offsetHeight > 0`, 'the box laid out', 3000);
        const h0 = await h();
        await ev(`document.getElementById('command-input').focus(); true`);
        await Input.insertText({ text: 'one\ntwo\nthree\nfour\nfive' });
        await waitFor(`document.getElementById('command-input').offsetHeight > ${h0}`, 'the box to grow', 3000);
        const h5 = await h();
        assert.ok(h5 > h0, `five lines (${h5}) taller than empty (${h0})`);
        await ev(`document.getElementById('command-input').value = ''; true`);
        await waitFor(`document.getElementById('command-input').offsetHeight <= ${h0}`, 'the box to shrink', 3000);
        console.log(`   ✓ ${h0}px empty, ${h5}px with five lines, back to ${await h()}px`);

        console.log('2. The same value or view mode again measures nothing');
        await ev(`document.getElementById('command-input').value = 'same text'; true`);
        const before = await reads();
        await ev(`(() => { const i = document.getElementById('command-input'); for (let k = 0; k < 5; k++) i.value = i.value; for (let k = 0; k < 3; k++) window.applyLiveViewMode?.(); return true; })()`).catch(() => {});
        assert.equal(await reads(), before, 'no scrollHeight read for an unchanged value and mode');
        console.log('   ✓ eight repeats, no measurement');

        console.log('3. A different value, or a window width that rewraps it, measures again');
        await ev(`document.getElementById('command-input').value = 'a different text'; true`);
        assert.ok(await reads() > before, 'a new value measures');
        const long = 'word '.repeat(80);
        await ev(`document.getElementById('command-input').value = ${JSON.stringify(long)}; true`);
        const wide = await h();
        const r1 = await reads();
        await Emulation.setDeviceMetricsOverride({ width: 900, height: 800, deviceScaleFactor: 1, mobile: false });
        await waitFor(`window.innerWidth === 900`, 'the window to resize', 3000);
        await waitFor(`window.__sh > ${r1}`, 'the box to be measured again after the width changed', 3000);
        await waitFor(`document.getElementById('command-input').offsetHeight !== ${wide}`, 'the height to follow the new width', 3000);
        await Emulation.clearDeviceMetricsOverride();
        console.log(`   ✓ new value measured; narrowing the window rewrapped the text (${wide}px -> ${await h()}px)`);

        console.log('\nAll composer autosize checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
