// The team board on a 390 px phone for a person using touch, a screen reader or the light theme (#6566): no two visible buttons of the board page share a name (the top bar's "Subscribers" and the side panel's
// Subscribers tab did), the More menu's items hold 4.5:1 in the light theme ("Select" was 4.4:1), and a subscriber's "View chat history" link in the Subscribers sheet is a 44 px target. A real board over the
// API with two subscribers. Drives Chrome over CDP against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const B = 'phone-board-' + Date.now().toString(36);
    const api = (m, p, body) => ev(`fetch(${JSON.stringify(p)}, { method: ${JSON.stringify(m)}, headers: { 'Content-Type': 'application/json' }, body: ${body === undefined ? 'undefined' : JSON.stringify(JSON.stringify(body))} }).then(r => r.status)`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await Emulation.setFocusEmulationEnabled({ enabled: true });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true }); await Emulation.setTouchEmulationEnabled({ enabled: true });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install(); await ev(`window.__coralReady.then(() => true)`);
        for (const [id, t] of [['Boss', 'Orchestrator'], ['dev-1', 'Developer']]) assert.equal(await api('POST', `/api/board/${B}/subscribe`, { subscriber_id: id, job_title: t, session_name: id + '-s' }), 200);
        assert.equal(await api('POST', `/api/board/${B}/messages`, { subscriber_id: 'Boss', content: 'Please check the release notes.' }), 200);
        await Page.navigate({ url: `${BASE}/board/${B}` }); await Page.loadEventFired(); await install(); await ev(`window.__coralReady.then(() => true)`);
        await waitFor(`document.querySelectorAll('#mb-messages .mb-message').length >= 1`, 'the message', 15000);

        // names: the board page's visible buttons, with the side panel's tabs
        await ev(`window.toggleAgenticPanel && window.toggleAgenticPanel(true); true`);
        await new Promise(r => setTimeout(r, 600)); // sleep-ok: the side panel opens over the page
        const dup = await ev(`(() => { const vis = e => e.getClientRects().length > 0 && getComputedStyle(e).visibility !== 'hidden'; const name = e => (e.getAttribute('aria-label') || e.textContent).replace(/\\s+/g, ' ').trim();
            const names = [...document.querySelectorAll('#messageboard-view button, #agentic-block-top .agentic-tab')].filter(vis).map(name).filter(Boolean);
            return { dup: [...new Set(names.filter((n, i) => names.indexOf(n) !== i))], n: names.length }; })()`);
        assert.ok(dup.n >= 4, 'the board page has its buttons: ' + JSON.stringify(dup));
        assert.deepEqual(dup.dup, [], 'no two visible buttons on the board page share a name');
        await ev(`window.toggleAgenticPanel && window.toggleAgenticPanel(false); true`);

        // the More menu in the light theme
        await installAudit(ev); await setTheme(ev, 'light');
        await ev(`document.getElementById('mb-more-btn').click(); true`);
        await waitFor(`document.getElementById('mb-more-btn').getAttribute('aria-expanded') === 'true'`, 'the More menu', 5000);
        const menu = await auditRegion(ev, ['#mb-more-menu']);
        assert.ok(menu.checked > 0 && !menu.fails.length, 'the More menu holds 4.5:1 in the light theme: ' + JSON.stringify(menu.fails).slice(0, 400));
        await ev(`document.getElementById('mb-more-btn').click(); true`);

        // the Subscribers sheet: a name's history link is a 44 px target
        await ev(`document.getElementById('mb-subscribers-btn').click(); true`);
        await waitFor(`getComputedStyle(document.getElementById('mb-subscribers-sheet')).display !== 'none' && document.querySelectorAll('#mb-subscribers-sheet .subscriber-history-link').length >= 2`, 'the Subscribers sheet', 8000);
        const h = await ev(`[...document.querySelectorAll('#mb-subscribers-sheet .subscriber-history-link')].map(e => Math.round(e.getBoundingClientRect().height))`);
        assert.ok(h.every(x => x >= 44), 'each subscriber link is at least 44 px tall: ' + JSON.stringify(h));
        console.log('PASS the phone board: names are distinct, the More menu holds contrast, the subscriber links are touch targets');
    } finally { await removeBoards(ev, [B]).catch(() => {}); await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
