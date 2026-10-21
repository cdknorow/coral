// #5572 owner path, the UI half: Coral Hub's owner may turn a team's cross-team switches on or off unless this computer forbids it.
//   - Settings > Network and Hub: the row "Let Coral Hub change cross-team settings" (Security's sentence), saved at once with
//     PUT /api/cloud/hub-cross-team {allow}, on unless the server says off;
//   - Team settings > Messaging: a persistent line "Coral Hub can change these settings." while the lock allows it (cross-team-state
//     hub_may_change), and under each switch a line saying who changed it last ("Turned on by <hub user> via Coral Hub at <time>"
//     or "... on this computer ..."), drawn as text (a hostile name is shown, never run).
// Drives Chrome over CDP against the isolated dev server; the machine's answers are stubbed by the test.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const shot = async name => { if (!process.env.CORAL_SCREENSHOT_DIR) return; fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); await new Promise(r => setTimeout(r, 250)); // sleep-ok: a screenshot settles layout, there is no event for it
        fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        await ev(`(() => {
            window.__lock = { allow: true, puts: [] };
            const real = window.fetch; const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
            const status = () => ({ configured: true, connected: true, state: 'connected', max_mode: 'view', hub_cross_team_settings: window.__lock.allow, server_id: 'srv_demo' });
            window.fetch = (u, o = {}) => { const p = new URL(String(u), location.href), m = o.method || 'GET';
                if (p.pathname === '/api/cloud/status') return json(status());
                if (p.pathname === '/api/cloud/hub-cross-team' && m === 'PUT') { const b = JSON.parse(o.body); window.__lock.puts.push(b); window.__lock.allow = b.allow; return json(status()); }
                return real(u, o); };
            return true; })()`);

        console.log('1. Settings > Network and Hub: the lock row');
        await ev(`(async () => { await window.showSettingsModal('network'); return true; })()`);
        await waitFor(`!!document.getElementById('settings-cloud-hub-cross-team') && document.getElementById('settings-cloud-hub-cross-team').closest('#settings-cloud-mode-row').hidden === false`, 'the lock row', 10000);
        const row = await ev(`(() => { const c = document.getElementById('settings-cloud-hub-cross-team'); const d = document.getElementById('settings-cloud-hub-cross-team-desc');
            return { label: c.closest('label').textContent.trim(), desc: d.textContent.trim(), described: c.getAttribute('aria-describedby'), checked: c.checked, shown: c.getClientRects().length > 0, small: c.closest('#settings-cloud-cross-team-row').getBoundingClientRect().height }; })()`);
        assert.equal(row.label, 'Let Coral Hub change cross-team settings');
        assert.equal(row.desc, "Coral Hub's owner can turn cross-team messaging and file sharing on or off for your teams. Turn this off to forbid it.");
        assert.equal(row.described, 'settings-cloud-hub-cross-team-desc'); assert.equal(row.checked, true, 'on unless the server says off'); assert.ok(row.shown);
        await shot('settings-network-lock-row');
        await ev(`(() => { const c = document.getElementById('settings-cloud-hub-cross-team'); c.checked = false; c.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
        await waitFor(`window.__lock.puts.length === 1`, 'the save', 6000);
        assert.deepEqual(await ev(`window.__lock.puts`), [{ allow: false }], 'saved at once as { allow }');
        await waitFor(`document.getElementById('settings-cloud-hub-cross-team').checked === false`, 'the row follows the answer');
        await ev(`window.hideSettingsModal(); true`);

        console.log('2. Messaging tab: the persistent line and the last-changed lines');
        const mount = state => ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'team_cross_team_settings.js'); m._setHubForTests(false);
            document.getElementById('owner-host')?.remove(); const host = document.createElement('div'); host.id = 'owner-host'; host.style.cssText = 'position:fixed;left:20px;top:20px;width:560px;background:var(--bg-secondary);padding:16px;z-index:9999'; document.body.append(host);
            await m.mountCrossTeamSettings(host, 'owner-team', { state: ${JSON.stringify(state)}, settings: { cross_team_messaging: true, cross_team_artifacts: false } }); return true; })()`);
        const read = () => ev(`(() => { const h = document.getElementById('owner-host'); return { may: h.querySelector('.xt-hub-may')?.textContent.trim() || null, changed: [...h.querySelectorAll('.xt-changed')].map(e => [e.dataset.for, e.textContent.trim()]), imgs: h.querySelectorAll('img, script, iframe').length, links: h.querySelectorAll('a').length }; })()`);
        await mount({ status: { state: 'ready', text: 'A team on another Coral can message this team.' }, links: [], hub_may_change: true, can_change: true, owner_path_live: true,
            last_changed: { cross_team_messaging: { via: 'hub', by: 'ada', at: '2026-10-21T10:00:00Z', on: true }, cross_team_artifacts: { via: 'local', at: '2026-10-21T09:00:00Z', on: false } } });
        let r = await read();
        assert.equal(r.may, 'Coral Hub can change these settings.');
        assert.deepEqual(r.changed.map(c => c[0]), ['cross_team_messaging', 'cross_team_artifacts']);
        assert.match(r.changed[0][1], /^Turned on by ada via Coral Hub at .+/); assert.match(r.changed[1][1], /^Turned off on this computer at .+/);
        await shot('messaging-owner-path');
        await mount({ status: { state: 'off', text: 'Off.' }, hub_may_change: false, last_changed: {} });
        r = await read(); assert.equal(r.may, null, 'no line when the lock forbids it'); assert.deepEqual(r.changed, [], 'and none for a switch nobody changed');
        await mount({ status: { state: 'off', text: 'Off.' } });
        r = await read(); assert.equal(r.may, null, 'an older server (no field) says nothing'); assert.deepEqual(r.changed, []);
        await mount({ status: { state: 'ready', text: 'x' }, hub_may_change: 'yes', last_changed: { cross_team_messaging: { via: 'hub', by: '<img src=x onerror=alert(1)>‮<script>', at: 'not a time', on: true }, cross_team_artifacts: 'x', bogus: { via: 'hub' } } });
        r = await read(); assert.equal(r.may, null, 'only a boolean true counts'); assert.equal(r.imgs, 0, 'a hostile name is text, never markup');
        assert.equal(r.changed.length, 1); assert.match(r.changed[0][1], /^Turned on by <img src=x onerror=alert\(1\)><script> via Coral Hub$/, 'shown as text, control and bidi characters removed, a bad time left out: ' + r.changed[0][1]);
        await mount({ status: { state: 'ready', text: 'x' }, last_changed: { cross_team_messaging: { via: 'hub', at: '2026-10-21T10:00:00Z' } } });
        r = await read(); assert.match(r.changed[0][1], /^Last changed by a Coral Hub user via Coral Hub at /, 'no name: said plainly: ' + r.changed[0][1]);
        console.log('   ✓');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
