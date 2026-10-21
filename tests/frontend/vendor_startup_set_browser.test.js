// The exact set of vendor scripts a cold page loads (#4021). vendor_lazy_browser.test.js pins the loader (once each, retry, no CodeMirror or
// xterm on a cold dashboard); this pins the startup set itself on two views. A cold load of the home page and of an agent's Chat view
// must request only the two small libraries every view renders text with (marked and DOMPurify) among the vendor scripts: no
// CodeMirror (the editor, about 1 MB), no xterm or its addons (the terminal, about 430 KB), no highlight.js (code colouring).
// They are loaded on first use by vendor_loader.js; this fails the moment a page tag or a static import pulls one into startup again.
// Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    const { Network } = client;
    const sleep = ms => new Promise(r => setTimeout(r, ms));
    try {
        await Promise.all([Page.enable(), Network.enable()]);
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        const seen = [];
        Network.requestWillBeSent(p => { const m = /\/vendor\/([^?#]+)/.exec(p.request.url); if (m) seen.push(m[1]); });
        for (const view of ['home', 'chat']) {
            seen.length = 0;
            await Network.clearBrowserCache();
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            if (view === 'chat') {
                await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('vendor-bot', 'A', 6, { sleeping: false }); true`);
                await ev(`window.selectLiveSession('vendor-bot', 'claude', 'vendor-bot-id', 'local'); true`);
                for (let i = 0; i < 100 && !(await ev(`!!document.querySelector('.chat-bubble, .chat-message, [class*="chat-bubble"]')`)); i++) await sleep(50);
            }
            await sleep(500); // sleep-ok: a late lazy load would land within this window; there is no event for "nothing else loads"
            const scripts = [...new Set(seen.filter(f => /\.(js|css)$/.test(f)))].sort();
            assert.deepEqual(scripts, ['marked.min.js', 'purify.min.js', 'safe-markdown.js'], `${view}: only marked, DOMPurify and the shared Inbox renderer load at startup, not the editor, the terminal or the highlighter (saw ${scripts.join(', ')})`);
        }
        console.log('PASS vendor libraries are lazy: the home page and the Chat view load only marked and DOMPurify');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
