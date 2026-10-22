// The phone frictions of the Hub walk (#6107, #6116), at 390 px with touch:
//  - the toast's Dismiss is a 44 px target;
//  - the side panel's tab strip, which scrolls on a phone, shows an edge shadow where more is hidden and every tab can be brought into view;
//  - an artifact opened while the side panel is open takes the whole screen (the preview's phone full screen), and on a desktop it does not.
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { STUB, HEX } = require('./helpers/comments_scale');
const BASE = require('./test_base').BASE;
const S = `(window.__coralStatic || '/static/')`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [name, w, h, phone] of [['phone', 390, 844, true], ['desktop', 1280, 800, false]]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile: phone }); await Emulation.setTouchEmulationEnabled({ enabled: phone });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install(); await ev(`window.__coralReady.then(() => true)`);
            await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('pf', 'PF', 3, {}); window.selectLiveSession('pf', 'claude', 'pf-id', 'local'); true`);
            await waitFor(`!!document.querySelector('#command-input')`, `${name}: the agent`, 15000);

            // the toast's Dismiss
            await ev(`import(${S} + 'utils.js').then(m => m.showToast('Settings saved')).then(() => true)`);
            await waitFor(`!!document.querySelector('.toast .toast-close')`, `${name}: a toast`, 8000);
            const close = await ev(`(() => { const b = document.querySelector('.toast .toast-close').getBoundingClientRect(); return { w: Math.round(b.width), h: Math.round(b.height) }; })()`);
            if (phone) assert.ok(close.w >= 44 && close.h >= 44, `${name}: the toast's Dismiss is a 44 px target ${JSON.stringify(close)}`);

            // the side panel's tabs
            if (phone) {
                await ev(`document.getElementById('panel-toggle-btn').click(); true`);
                await waitFor(`!!document.querySelector('.agentic-state.mobile-panel-overlay .agentic-state-tabs') && document.querySelector('.agentic-state-tabs').getClientRects().length > 0`, `${name}: the side panel`, 8000);
                const tabs = await ev(`(() => { const s = document.querySelector('.agentic-state-tabs'); const cs = getComputedStyle(s); const sizes = [...s.children].filter(e => e.getClientRects().length).map(e => { const b = e.getBoundingClientRect(); return { w: Math.round(b.width), h: Math.round(b.height) }; }); const before = s.scrollWidth > s.clientWidth + 1; s.scrollLeft = s.scrollWidth; const last = [...s.children].filter(e => e.getClientRects().length).pop().getBoundingClientRect(); return { n: sizes.length, small: sizes.filter(x => x.w < 44 || x.h < 44).length, scrolls: before, shadows: /radial-gradient/.test(cs.backgroundImage), lastRight: Math.round(last.right), vw: innerWidth }; })()`);
                assert.ok(tabs.n >= 8 && tabs.small === 0, `${name}: the tabs are there and 44 px ${JSON.stringify(tabs)}`);
                // ten 44 px targets cannot share one 390 px row: the strip scrolls, shows a shadow where more is hidden, and its last tab can be brought into view
                assert.ok(!tabs.scrolls || tabs.shadows, `${name}: a strip that scrolls shows an edge shadow ${JSON.stringify(tabs)}`);
                assert.ok(tabs.lastRight <= tabs.vw + 1, `${name}: scrolled to the end, the last tab is fully in view ${JSON.stringify(tabs)}`);
            }

            // an artifact with the side panel open
            await ev(`(async () => { window.PP = await import(${S} + 'preview_pane.js'); return true; })()`);
            await ev(STUB({ filename: 'release-notes.md', content: '# Release notes\n\nThe build is green.\n', mediaType: 'text/markdown', comments: [] }));
            await ev(`import(${S} + 'changed_files.js').then(m => m.openFilePreview('coral://artifacts/${HEX}')).then(() => true)`); // the way a person opens one: a link or the Artifacts tab
            await waitFor(`!!document.querySelector('#preview-pane #preview-toolbar')`, `${name}: the artifact`, 10000);
            const pane = await ev(`(() => { const p = document.getElementById('preview-pane'), b = p.getBoundingClientRect(); return { full: p.classList.contains('preview-phone-full'), w: Math.round(b.width), h: Math.round(b.height), vw: innerWidth, vh: innerHeight, back: !!document.getElementById('preview-full-back') && document.getElementById('preview-full-back').getClientRects().length > 0 }; })()`);
            if (phone) {
                assert.ok(pane.full && pane.w >= pane.vw - 1 && pane.h >= pane.vh - 1 && pane.back, `phone: the artifact takes the whole screen with a Back button ${JSON.stringify(pane)}`);
                await ev(`document.getElementById('preview-full-back').click(); true`);
                await waitFor(`!document.getElementById('preview-pane').classList.contains('preview-phone-full')`, 'Back returns to the chat', 5000);
            } else assert.ok(!pane.full, `desktop: the artifact does not go full screen ${JSON.stringify(pane)}`);
            console.log(`ok ${name}`);
        }
        console.log('PASS the phone frictions of the Hub walk');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
