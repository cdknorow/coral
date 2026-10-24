// One verb and noun for starting things (#7064, UI/UX's #7059 findings 3 and 9): "New agent" and "New team" in the chooser (title and cards), the welcome cards (desktop and phone), the sidebar
// button, the servers' menu and the two dialogs' titles; a sleeping agent is said once (the bar, not the disabled box too); no line under the Working Directory label; "Click to type" in the
// terminal's bar only until the terminal has had focus. Drives Chrome over CDP through agent_view_harness.js against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const S = `(window.__coralStatic || '/static/')`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const text = sel => ev(`[...document.querySelectorAll(${JSON.stringify(sel)})].map(e => e.textContent.replace(/\\s+/g, ' ').trim().replace(/^←\\s*/, ''))`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        console.log('1. the chooser, its cards and the two dialogs');
        await ev(`(async () => { await window.showLaunchModal(); return true; })()`);
        assert.deepEqual(await text('#launch-title'), ['New agent or team']);
        assert.deepEqual((await text('#launch-step-chooser .launch-type-label')).slice(0, 3), ['New agent', 'New team', 'Terminal']);
        await ev(`window._selectLaunchType('agent'); true`);
        assert.deepEqual(await text('#launch-agent-title'), ['New agent']);
        assert.equal(await ev(`(() => { const h = document.getElementById('launch-dir-hint'); return !h.textContent && getComputedStyle(h).display === 'none'; })()`), true, 'no "Pick your project folder." under the label');
        await ev(`window._selectLaunchType('back'); window._selectLaunchType('team'); true`);
        assert.deepEqual(await text('#launch-team-title'), ['New team']);
        await ev(`window.hideLaunchModal(); true`);
        console.log('2. the sidebar button and the welcome cards (desktop)');
        assert.ok((await text('.sidebar-new-btn-full')).every(t => t === '+ New agent'), JSON.stringify(await text('.sidebar-new-btn-full')));
        assert.deepEqual((await text('.welcome-launch-copy strong')).slice(0, 2), ['New agent', 'New team']);
        console.log('4. a sleeping agent says it once: the bar, with an empty box');
        await ev(`(async () => { const m = await import(${S} + 'sleeping_view.js'); window.__sleep = m; return true; })()`);
        assert.equal(await ev(`window.__sleep.sleepingPlaceholder(false) === '' && window.__sleep.sleepingPlaceholder(true) === ''`), true);
        console.log('5. "Click to type" only until the terminal has had focus');
        await ev(`(async () => { const m = await import(${S} + 'terminal_statusbar.js'); window.__tsb = m; return true; })()`);
        const shown = () => ev(`getComputedStyle(document.querySelector('#xterm-focus-hint .xterm-click-hint')).display`);
        assert.notEqual(await shown(), 'none', 'offered before the first focus');
        await ev(`window.__tsb.setTerminalFocused(true); window.__tsb.setTerminalFocused(false); true`);
        assert.equal(await shown(), 'none', 'gone once the terminal has had focus, and it stays gone');
        assert.match((await text('#xterm-focus-hint'))[0], /Ctrl\+Shift\+Esc to unfocus/, 'the way out is still said');
        console.log('PASS one verb and noun for starting things');
    } finally { await client.close(); }
})().catch(err => { console.error(err); process.exit(1); });
