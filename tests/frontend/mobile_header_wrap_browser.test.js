// The phone's list header wraps only when it must (#4062 with #4086): at a normal phone width (390px) its controls share one row and one
// centre line, and with the text size at 200% (what #4086's zoom pass was for) they wrap to a second line instead of running off the
// screen. With flex-wrap on and the server button's flex-basis at its full label, one control sat on a second line at 390px once #4062's
// header had its controls (the batch conflict resolution); the server button is now the flexible control.
// Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const NOW = new Date().toISOString();
const SERVERS = [
    { id: 'm1', label: 'studio-mac.local', status: 'online', version: '1.4.69', last_seen: NOW },
    { id: 'm2', label: 'workstation.lan', status: 'online', version: '1.4.2', update: 'available', latest_coral: '1.4.69', last_seen: NOW },
];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a layout settles over frames; there is no completion event
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`T.state.hub = true; T.state.cloud = true; T.state.servers = ${JSON.stringify(SERVERS)}; T.state.liveSessions = []; window.__m.agents = {};
            window.__m.add('a-m1', 'S', 3, { sleeping: false, server: 'm1' }); T.render.renderLiveSessions(T.state.liveSessions); true`);
        const measure = () => ev(`(() => { const h = [...document.querySelectorAll('.mobile-list-header')].find(x => x.getClientRects().length);
            if (!h) return null;
            const kids = [...h.children].filter(c => c.getClientRects().length && !c.classList.contains('mobile-search-row') && !c.classList.contains('sr-only')).map(c => { const r = c.getBoundingClientRect(); return { top: Math.round(r.top), mid: Math.round(r.top + r.height / 2), right: Math.round(r.right), left: Math.round(r.left) }; });
            return { kids, wrap: getComputedStyle(h).flexWrap, vw: innerWidth }; })()`);
        await waitFor(`!![...document.querySelectorAll('.mobile-list-header')].find(x => x.getClientRects().length)`, 'the phone list header', 8000);
        await sleep(500); // sleep-ok: a layout settles over frames; there is no completion event
        let m = await measure();
        const mids = m.kids.map(k => k.mid);
        assert.ok(Math.max(...mids) - Math.min(...mids) <= 2, 'one centre line at 390px: ' + JSON.stringify(m));
        assert.ok(m.kids.every(k => k.right <= m.vw), 'all inside the screen: ' + JSON.stringify(m));
        console.log('   ✓ 390px: one row, one centre line');
        await ev(`document.documentElement.style.fontSize = '200%'; true`);
        await sleep(600); // sleep-ok: the root text size change relays out over frames; there is no completion event
        m = await measure();
        const mids2 = m.kids.map(k => k.mid);
        assert.ok(Math.max(...mids2) - Math.min(...mids2) > 20, 'with 200% text the controls wrap to a second line: ' + JSON.stringify(m));
        assert.ok(m.kids.every(k => k.left >= 0 && k.right <= m.vw + 1), 'at 200% text nothing runs off the screen: ' + JSON.stringify(m));
        console.log('   ✓ 200% text: wraps, nothing off screen');
        console.log('\nPASS the phone list header wraps only when it must');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
