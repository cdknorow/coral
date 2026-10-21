// Accessibility pass on what 146 to 148 added (#5675), keyboard only, 200% text, 390 px:
//   - the artifact viewer's "Who can read this" is reached with Tab: the page's Tab trap (modal_chrome.js) did not count a <summary>, so from Close (the
//     last control it knew) it went back to Download and the disclosure could not be reached;
//   - Team settings > Messaging at 200% text on a 390 px phone: the "Link a team on another Coral" button wraps, it is not one line cut off by the panel;
//   - Settings > Network and Hub: the access list's label reads as a sentence ("The most the hub may do on this computer").
// Drives Chrome over CDP against the isolated dev server through agent_view_harness.js; run_cdp_suites.mjs finds it by name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const TEAM = 'a11y149-' + Date.now().toString(36);
    const key = async (k, code, vk, modifiers = 0) => { await Input.dispatchKeyEvent({ type: 'keyDown', key: k, code, windowsVirtualKeyCode: vk, modifiers }); await Input.dispatchKeyEvent({ type: 'keyUp', key: k, code, windowsVirtualKeyCode: vk, modifiers }); };
    const active = () => ev(`(() => { const a = document.activeElement; return a.tagName.toLowerCase() + '.' + String(a.className || '').split(' ')[0]; })()`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        const start = async (w, h, mobile) => {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'settings')`);
            await ev(`(() => { const real = window.fetch; const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
                window.fetch = (u, o = {}) => { const p = new URL(String(u), location.href);
                    if (p.pathname.endsWith('/readers')) return json({ machine_owner: 'op', owners: [{ kind: 'operator', id: 'x', records: 1, teams: [{ label: 't' }] }], note: 'A Coral control, not an operating-system boundary.' });
                    if (p.pathname === '/api/board/${TEAM}/cross-team-state') return json({ status: { state: 'ready', text: 'A team on another Coral can message this team.' }, links: [], hub_may_change: true, can_change: true, owner_path_live: true, last_changed: {} });
                    return real(u, o); }; })(); true`);
        };

        console.log('1. Tab reaches "Who can read this" in the artifact viewer, and wraps around it');
        await start(1280, 900, false);
        await ev(`import((window.__coralStatic || '/static/') + 'artifact_viewer.js').then(m => m.openArtifactViewer('${'a'.repeat(64)}', {})); true`);
        await waitFor(`!!document.querySelector('.artifact-readers > summary')`, 'the disclosure');
        await ev(`document.querySelector('.artifact-viewer-close').focus(); true`);
        await key('Tab', 'Tab', 9);
        assert.equal(await active(), 'summary.', 'Tab from Close goes to Who can read this (it went back to Download)');
        await key('Tab', 'Tab', 9);
        assert.match(await active(), /^a\.btn$|^a\./, 'from the last control Tab wraps to the first (Download)');
        await key('Tab', 'Tab', 9, 8); // Shift+Tab from Download goes to the last control
        assert.equal(await active(), 'summary.', 'Shift+Tab from the first control goes to the last, the disclosure');
        await ev(`document.querySelector('.artifact-viewer-close')?.click(); true`);

        console.log('2. Messaging at 200% text on a phone: the primary button wraps');
        await ev(`fetch('/api/board/${TEAM}/subscribe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'Boss', job_title: 'Orchestrator', session_name: 'boss-session' }) }).then(r => r.status)`);
        await start(390, 844, true);
        await ev(`document.documentElement.style.fontSize = '32px'; import((window.__coralStatic || '/static/') + 'team_working_mode.js').then(m => m.showTeamWorkingMode('${TEAM}', { workspace: false })); true`);
        await waitFor(`!!document.querySelector('[data-settings-tab=messaging]')`, 'the tabs', 15000);
        await ev(`document.querySelector('[data-settings-tab=messaging]').click(); true`);
        await waitFor(`!!document.querySelector('.xt-primary')`, 'the primary button', 15000);
        const b = await ev(`(() => { const e = document.querySelector('.xt-primary'); const r = e.getBoundingClientRect(); const d = document.getElementById('team-working-mode-dialog').getBoundingClientRect(); return { text: e.textContent.trim().slice(0, 40), clipped: e.scrollWidth > e.clientWidth + 1, right: Math.round(r.right), dialogRight: Math.round(d.right), lines: Math.round(r.height / parseFloat(getComputedStyle(e).lineHeight || 20)) }; })()`);
        assert.equal(b.clipped, false, 'the label is not cut off: ' + JSON.stringify(b)); assert.ok(b.right <= b.dialogRight + 1, 'and the button stays in the panel: ' + JSON.stringify(b));
        await ev(`document.getElementById('team-working-mode-dialog')?.close?.(); document.documentElement.style.fontSize = ''; true`);

        console.log('3. Network and Hub: the access list reads as a sentence');
        await start(1280, 900, false);
        await ev(`(async () => { await window.showSettingsModal('network'); return true; })()`);
        assert.equal(await ev(`document.querySelector('label[for="settings-cloud-max-mode"]').textContent.trim()`), 'The most the hub may do on this computer');
        console.log('   ✓');
    } finally { await removeBoards(ev, [TEAM]); await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
