// The four things xterm_internals.js reads that xterm does not document (#5411), against a REAL terminal of the bundled xterm (the one the page loads, with the canvas renderer as the page uses it): each
// must be there and do what the page relies on, and when one is not, this fails naming it and what would break, so an xterm upgrade cannot quietly break fit, cursor follow or the query filter:
//   cellHeight   the renderer measures a row (what _fit counts rows from), a number between 6 and 120 px that grows with the font
//   screenElement  .xterm-screen exists and is as tall as rows x cell (what the cursor box and the drawn width read)
//   clearRenderer  there is a renderer service to clear before a resize
//   onUserInput  fires for what a person typed (term.input) and NOT for xterm's own reply to a query in the output (a device-attributes request): the filter that keeps replies off the pane
// Drives Chrome over CDP against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { framesJs } = require('./helpers/wait');

(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const settle = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: xterm renders and parses on its own frames; there is no completion event
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(async () => { const v = await import(${S} + 'vendor_loader.js'); await v.loadXterm();
            const host = document.createElement('div'); host.id = 'xi-host'; host.style.cssText = 'width:640px;height:400px;position:fixed;left:0;top:0'; document.body.appendChild(host);
            const t = new window.Terminal({ cols: 80, rows: 24, fontSize: 14, fontFamily: 'monospace', scrollback: 100 }); t.loadAddon(new window.CanvasAddon.CanvasAddon()); t.open(host);
            window.__xt = t; window.__got = { user: 0, data: [] }; t.onData(d => window.__got.data.push(d)); return true; })()`);
        await settle(300);
        const MSG = (field, breaks) => `xterm no longer has ${field}: ${breaks}. Update coral-go/pkg/frontend/static/xterm_internals.js for this xterm version (its header lists each read and its fallback).`;
        const probe = await ev(`import(${S} + 'xterm_internals.js').then(m => { const t = window.__xt; const p = m.probe(t); const h1 = m.cellHeight(t); t.options.fontSize = 24; return ${framesJs(8)}.then(() => ({ p, h1, h2: m.cellHeight(t), screenH: m.screenElement(t).getBoundingClientRect().height, rows: t.rows })); })`);
        check(`the renderer measures a row: ${probe.h1} px at 14 px, ${probe.h2} at 24 px`, probe.p.cellHeight && probe.h1 >= 6 && probe.h1 <= 120 && probe.h2 > probe.h1, MSG('the renderer service\'s dimensions.css.cell.height', 'fit would count rows from the screen element (a figure that lags a frame behind a resize: the 113,987-row runaway, #5396)'));
        check('.xterm-screen exists, is as tall as rows x cell', probe.p.screen && Math.abs(probe.screenH - probe.rows * probe.h2) < probe.h2, MSG('the .xterm-screen element', 'the cursor would not follow and the drawn width would read 0'));
        check('there is a renderer service to clear before a resize', probe.p.clear, MSG('_renderService.clear', 'a resize would leave stale pixels until the next write (fit still works)'));
        check('xterm says when a person typed', probe.p.userInput, MSG('coreService.onUserInput', 'every onData, including xterm\'s answers to queries in the output, would be sent to the pane'));
        const input = await ev(`import(${S} + 'xterm_internals.js').then(async m => { const t = window.__xt; let user = 0; m.onUserInput(t, () => { user++; }); window.__got.data.length = 0;
            t.input('x'); const afterTyped = { user, data: window.__got.data.slice() };               // what a person typed
            window.__got.data.length = 0; user = 0; t.write('\\x1b[c'); await ${framesJs(8)};  // a device-attributes request in the output: xterm answers it through onData
            return { afterTyped, afterQuery: { user, data: window.__got.data.slice() } }; })`);
        check(`typing is marked as user input and reaches onData (${JSON.stringify(input.afterTyped)})`, input.afterTyped.user === 1 && input.afterTyped.data.join('') === 'x', MSG('the onUserInput mark on typed input', 'the filter would drop real typing, or pass everything'));
        check(`xterm's own reply to a query is NOT marked (${JSON.stringify(input.afterQuery)})`, input.afterQuery.data.length > 0 && input.afterQuery.user === 0, MSG('the difference between typed input and an automatic reply', 'replies to queries in the output would be typed into the pane'));
        // the adapter's fallbacks, with nothing under it
        const fallbacks = await ev(`import(${S} + 'xterm_internals.js').then(m => { const bare = { options: {} }; return { screen: m.screenElement(bare), cell: m.cellHeight(bare), clear: m.clearRenderer(bare), user: m.onUserInput(bare, () => {}), none: [m.screenElement(null), m.cellHeight(null), m.clearRenderer(null), m.onUserInput(null, () => {})] }; })`);
        check(`with nothing there each read gives its fallback (${JSON.stringify(fallbacks)})`, fallbacks.screen === null && fallbacks.cell === 0 && fallbacks.clear === false && fallbacks.user === false && JSON.stringify(fallbacks.none) === JSON.stringify([null, 0, false, false]));
        const failed = results.filter(r => !r).length;
        console.log(`${results.length - failed} of ${results.length} checks passed`);
        await client.close(); process.exit(failed ? 1 : 0);
    } catch (e) { console.error('FAIL', e && e.stack || e); await client.close().catch(() => {}); process.exit(1); }
})();
