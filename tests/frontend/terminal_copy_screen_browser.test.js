// Copy screen (#6694, the phone terminal audit #6669 finding 6): a long press on the terminal's canvas selects nothing, so the Terminal view's header has a "Copy screen" button that copies the text on the screen
// (the rows shown, trailing spaces and blank rows dropped). It is in the Terminal view only, 44 px on a phone, and says what happened in a toast.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`window.__m.add('cp-a', 'A', 4, { sleeping: false }); true`); await select('cp-a');
        await ev(`window.setLiveViewMode('chat'); true`);
        assert.equal(await ev(`window.__vis('#terminal-copy-screen-btn')`), false, 'not in the Chat view');
        await ev(`window.setLiveViewMode('terminal'); true`);
        await waitFor(`window.__vis('#terminal-copy-screen-btn')`, 'the Copy screen button', 8000);
        const box = await ev(`(() => { const r = document.getElementById('terminal-copy-screen-btn').getBoundingClientRect(); return { w: Math.round(r.width), h: Math.round(r.height), name: document.getElementById('terminal-copy-screen-btn').getAttribute('aria-label') }; })()`);
        assert.ok(box.w >= 44 && box.h >= 44, 'a 44 px target: ' + JSON.stringify(box));
        assert.equal(box.name, 'Copy screen');
        // The terminal gets text; the clipboard is stubbed so the test reads what was copied.
        await ev(`window.__copied = null; Object.defineProperty(navigator, 'clipboard', { value: { writeText: t => { window.__copied = t; return Promise.resolve(); } }, configurable: true }); true`);
        const XT = `(await import((window.__coralStatic||'/static/')+'xterm_renderer.js'))`;
        await waitFor(`(async () => { const t = ${XT}.getTerminal(); return !!t && !!t.textarea; })()`, 'the terminal', 8000);
        await ev(`(async () => { const t = ${XT}.getTerminal(); await new Promise(r => t.write('\\x1b[2J\\x1b[Hfirst line   \\r\\n\\r\\nthird line\\r\\n', r)); return true; })()`);
        await ev(`document.getElementById('terminal-copy-screen-btn').click(); true`);
        await waitFor(`window.__copied !== null`, 'the copy', 5000);
        assert.equal(await ev(`window.__copied`), 'first line\n\nthird line', 'the screen text: trailing spaces and blank rows dropped, inner blank row kept');
        assert.ok(/Terminal text copied/.test(await ev(`document.body.innerText`)), 'a toast says it was copied');
        console.log('PASS terminal copy screen');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
