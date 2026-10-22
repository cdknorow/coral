// Settings > Run as a hub (#3460) on the real test server, from the dashboard on this computer:
//   1. the row: a labelled toggle, off, nothing said, no Restart button (a test server can't restart itself);
//   2. on: saved at once (GET /api/system/hub-mode), "becomes a hub when it restarts" and how to
//      restart; the running server is unchanged (/api/health hub false, no /api/servers);
//   3. where the process can restart itself (stubbed: this suite never restarts the test server):
//      a Restart Coral button; it POSTs /api/system/restart and says it is restarting;
//   4. off again: nothing said, nothing to restart;
//   5. PUT /api/settings can't set hub_mode (400): only its own local route;
//   6. through Coral Hub the row is hidden, with the other machine settings.
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, ev, waitFor } = await connect();
    const { Network } = client;
    const row = () => ev(`(() => {
        const box = document.getElementById('settings-hub-mode-enabled');
        const st = document.getElementById('settings-hub-mode-status');
        const btn = document.getElementById('settings-hub-mode-restart');
        const dd = document.getElementById('settings-hub-mode');
        return box && { checked: box.checked, disabled: box.disabled, label: box.getAttribute('aria-label'),
            described: (box.getAttribute('aria-describedby') || '').split(' ').every(id => !!document.getElementById(id)),
            status: st.textContent, restart: !btn.hidden, shown: !!dd && !dd.hidden && box.offsetParent !== null };
    })()`);
    const openSettings = async () => {
        await ev(`(async () => { await showSettingsModal('network'); return true; })()`);
        await waitFor(`(() => { const d = document.getElementById('settings-hub-mode-enabled'); return !!d && d.offsetParent !== null; })()`, 'the hub mode row', 8000); // off says nothing now, so the row being shown is the sign it was read
    };

    const guard = await require('./helpers/settings_guard').guardSettings(); // the settings this suite changes are put back in its finally (#4849)
    try {
        await Promise.all([Page.enable(), Network.enable()]);
        await Network.setBypassServiceWorker({ bypass: true });
        await Page.navigate({ url: BASE + '/' });
        await Page.loadEventFired();
        await waitFor(`typeof window.showSettingsModal === 'function'`, 'the dashboard', 10000);

        console.log('1. The row');
        await openSettings();
        let r = await row();
        assert.ok(r.shown, 'shown in Settings');
        assert.equal(r.label, 'Run this Coral as a hub');
        assert.ok(r.described, 'the toggle is described by the explanation and the status');
        assert.equal(r.checked, false);
        assert.equal(r.status, '', 'off says nothing: the switch shows it');
        assert.equal(r.restart, false, 'no Restart button where the process cannot restart itself');
        console.log('   ✓ off, labelled, no Restart button');

        console.log('2. On: saved, applies at the next start');
        await ev(`(() => { document.getElementById('settings-hub-mode-enabled').click(); return true; })()`);
        await waitFor(`/when it restarts/.test(document.getElementById('settings-hub-mode-status').textContent)`, 'saved', 5000);
        r = await row();
        assert.equal(r.checked, true);
        assert.match(r.status, /^Saved: this Coral becomes a hub when it restarts\. Quit Coral and start it again to apply\.$/);
        const st = await ev(`fetch('/api/system/hub-mode').then(r => r.json())`);
        assert.deepEqual(st, { saved: true, running: false, forced: false, restart_required: true, can_restart: false });
        assert.equal((await (await fetch(BASE + '/api/health')).json()).hub, false, 'the running server is not a hub yet');
        assert.equal(await ev(`fetch('/api/servers').then(r => r.status)`), 404);
        console.log(`   ✓ ${r.status}`);

        console.log('3. Where the process can restart itself: Restart Coral');
        await ev(`(() => {
            const real = window.fetch; window.__restarts = 0;
            window.fetch = (u, o = {}) => {
                const p = String(u && u.url || u);
                if (p.endsWith('/api/system/restart')) { window.__restarts++; return Promise.resolve(new Response('{"ok":true}', { status: 202, headers: { 'content-type': 'application/json' } })); }
                if (p.endsWith('/api/system/hub-mode')) return real(u, o).then(r => r.json()).then(j => new Response(JSON.stringify({ ...j, can_restart: true }), { status: 200, headers: { 'content-type': 'application/json' } }));
                if (p.includes('/api/health')) return Promise.resolve(new Response('{"status":"ok","hub":false}', { status: 200, headers: { 'content-type': 'application/json' } }));
                return real(u, o);
            };
            return true; })()`);
        await openSettings();
        await waitFor(`!document.getElementById('settings-hub-mode-restart').hidden`, 'the Restart button', 5000);
        r = await row();
        assert.match(r.status, /^Saved: this Coral becomes a hub when it restarts\.$/);
        await ev(`(() => { document.getElementById('settings-hub-mode-restart').click(); return true; })()`);
        await waitFor(`window.__restarts === 1 && document.getElementById('settings-hub-mode-status').textContent === 'Restarting Coral…'`, 'restarting', 5000);
        console.log('   ✓ Restart Coral → POST /api/system/restart, "Restarting Coral…"');
        // Back to the real routes (a fresh page) before turning it off.
        await Page.navigate({ url: BASE + '/' });
        await Page.loadEventFired();
        await waitFor(`typeof window.showSettingsModal === 'function'`, 'the dashboard', 10000);

        console.log('4. Off again');
        await openSettings();
        await ev(`(() => { document.getElementById('settings-hub-mode-enabled').click(); return true; })()`);
        await waitFor(`document.getElementById('settings-hub-mode-status').textContent === ''`, 'off', 5000);
        r = await row();
        assert.equal(r.checked, false); assert.equal(r.restart, false);
        assert.equal((await ev(`fetch('/api/system/hub-mode').then(r => r.json())`)).saved, false);
        console.log('   ✓ Off.');

        console.log('5. PUT /api/settings cannot set it');
        const put = await ev(`fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ hub_mode: 'true' }) }).then(async r => ({ status: r.status, body: await r.text() }))`);
        assert.equal(put.status, 400, JSON.stringify(put));
        assert.match(put.body, /hub-mode/);
        assert.equal((await ev(`fetch('/api/system/hub-mode').then(r => r.json())`)).saved, false);
        console.log('   ✓ 400, still off');

        console.log('6. Through Coral Hub: hidden');
        await ev(`(async () => { (await import((window.__coralStatic || '/static/') + 'cloud_settings.js')).applyCloudSettingsDialog(); return true; })()`);
        assert.equal((await row()).shown, false);
        console.log('   ✓ hidden with the machine settings');

        console.log('\nAll Run as a hub checks passed');
    } finally {
        // Never leave the shared test server marked as a hub.
        try { await ev(`fetch('/api/system/hub-mode', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: '{"enabled":false}' }).then(r => r.status)`); } catch {}
        await guard.restore().catch(e => console.error('settings restore: ' + (e && e.message)));
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
