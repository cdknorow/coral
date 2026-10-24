// Open Agent Tab on the phone layout (#6777, UI Accessibility's #6763 note 1): the header no longer carries the pop-out link (#6756), and a narrow desktop window is the phone layout too, so the link must be reachable
// another way: the agent's "..." menu in the Agents list offers it, with a real href for that exact session, as a 44 px item, at a phone's width and at a narrow desktop window's. Also the header's right edge (note 2):
// the side-panel button is not against the screen edge (an OS edge swipe would win); the name's 100 px at 320 px is header_text200's.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[390, 844, true], [700, 900, false]]) {
            const at = `${w}px${mobile ? '' : ' (a narrow desktop window)'}: `;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`window.__m.add('oat-a', 'A', 4, { sleeping: false, session_id: 'oat-sid' }); true`);
            await ev(`window.switchNavTab('agents'); Promise.resolve(window._coralLoadLiveSessions && window._coralLoadLiveSessions()).then(() => true)`);
            await waitFor(`document.querySelectorAll('#mobile-agent-list .sidebar-kebab-btn[aria-controls^="kebab-agent-"]').length >= 1`, 'an agent row', 10000);
            const hdr = await ev(`(async () => { window.setLiveViewMode && 0; return getComputedStyle(document.getElementById('terminal-open-window-link')).display; })()`);
            assert.equal(hdr, 'none', at + 'the header has no Open Agent Tab');
            await ev(`document.querySelector('#mobile-agent-list .sidebar-kebab-btn[aria-controls^="kebab-agent-"]').click(); true`);
            await waitFor(`!!document.querySelector('.overflow-menu-open-window') && document.querySelector('.overflow-menu-open-window').checkVisibility()`, 'the Open Agent Tab item in the agent\'s menu', 6000);
            const item = await ev(`(() => { const a = document.querySelector('.overflow-menu-open-window'); const r = a.getBoundingClientRect(); return { href: a.getAttribute('href'), target: a.target, h: Math.round(r.height), name: a.getAttribute('aria-label') }; })()`);
            assert.match(item.href, /^\/agent\//, at + 'a real link to this agent: ' + JSON.stringify(item));
            assert.equal(item.target, '_blank');
            if (mobile) assert.ok(item.h >= 40, at + `a touch-sized menu item (${item.h}px)`);
            assert.equal(item.name, 'Open Agent Tab');
        }
        // the header's right edge at 320 px
        await Emulation.setDeviceMetricsOverride({ width: 320, height: 800, deviceScaleFactor: 1, mobile: true }); await Emulation.setTouchEmulationEnabled({ enabled: true });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`window.__m.add('oat-b', 'B', 4, { sleeping: false, session_id: 'oat-sid2' }); true`); await select('oat-b');
        await waitFor(`!!document.getElementById('terminal-header-label') && document.getElementById('terminal-header-label').textContent.length > 0`, 'the header', 8000);
        await new Promise(r => setTimeout(r, 500)); // sleep-ok: the header lays out after the selection
        const edge = await ev(`(() => { const b = document.getElementById('popout-panel-toggle-btn').getBoundingClientRect(); return { gap: Math.round(innerWidth - b.right), left: Math.round(document.querySelector('.live-view-toggle').getBoundingClientRect().left) }; })()`);
        assert.ok(edge.left >= 6, `320px: the view toggle is at least 6 px from the left screen edge (${edge.left}px), where an iOS back swipe starts`);
        assert.ok(edge.gap >= 6, `320px: the side-panel button is at least 6 px from the screen edge (${edge.gap}px)`);
        console.log('PASS open agent tab on the phone layout, header edge');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
