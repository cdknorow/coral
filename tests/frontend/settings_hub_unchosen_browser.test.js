// Two things the Coral Hub controls of Settings > Network and Hub say about a computer that has not connected yet (#5962), both themes are the page's own:
//  (A) before Connect the level question has an answer, View, held on the page and sent with Connect (#6579); a computer linked without ever choosing (status max_mode_saved false, connected) shows
//      "Not chosen yet (View)", not a plain View that contradicts the first-run screen's Launch; choosing a value there saves it (PUT /api/cloud/max-mode) and the placeholder goes; a saved choice shows as itself;
//  (B) a hub that could not be reached is said in a sentence ("Coral could not reach Coral Hub. Check your connection and try again."), with the seconds until the next try counted down beside it
//      in a plain element (not a live region: a screen reader hears the sentence once) and Connect off until the count is over.
// Drives Chrome over CDP against the isolated dev server; the cloud routes are stood in for the page's fetch.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const shot = async name => { if (!process.env.CORAL_SCREENSHOT_DIR) return; fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const showHub = () => ev(`(() => { const m = document.getElementById('settings-modal'); window.showSettingsModal(); return true; })()`).then(() => ev(`document.getElementById('settings-tab-network').click(); document.getElementById('settings-cloud-max-mode').scrollIntoView({ block: 'center' }); true`));
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(async () => {
            window.__coral.buildAllLazy(); // Settings is built on first open
            const real = window.fetch; window.__puts = [];
            window.__st = { configured: true, state: 'idle', hub_url: 'https://hub.example', max_mode: 'view', max_mode_saved: false, allow_hub_auto_accept: true };
            window.fetch = async (url, opts = {}) => { const u = String(url), m = (opts.method || 'GET').toUpperCase();
                if (u.endsWith('/api/cloud/status')) return new Response(JSON.stringify(window.__st), { headers: { 'Content-Type': 'application/json' } });
                if (u.endsWith('/api/cloud/max-mode') && m === 'PUT') { const b = JSON.parse(opts.body); window.__puts.push(b.mode); window.__st = { ...window.__st, max_mode: b.mode, max_mode_saved: true }; return new Response(JSON.stringify(window.__st), { headers: { 'Content-Type': 'application/json' } }); }
                return real(url, opts); };
            const cc = await import(${S} + 'cloud_connect.js'); window.__cc = cc; cc.initCloudConnect(); await cc.loadCloudStatus(); return true; })()`);
        const sel = () => ev(`(() => { const s = document.getElementById('settings-cloud-max-mode'); return { options: [...s.options].map(o => o.textContent), value: s.value, shown: s.options[s.selectedIndex].textContent, desc: document.getElementById('settings-cloud-mode-desc').textContent }; })()`);

        console.log('A. not connected: View is the answer before Connect (held, sent with Connect); a link that never chose says "Not chosen yet (View)" and a choice saves');
        let b = await sel();
        assert.equal(b.shown, 'View'); assert.equal(b.value, 'view'); assert.equal(b.desc, 'You can change this later.');
        assert.deepEqual(b.options, ['View', 'Interact', 'Launch'], 'no "Not chosen yet" option before Connect');
        await showHub(); await shot('hub-settings-unchosen');
        await ev(`(() => { const s = document.getElementById('settings-cloud-max-mode'); s.value = 'interact'; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
        assert.equal(await ev(`window.__puts.length`), 0, 'a choice before Connect saves nothing: it goes with Connect');
        b = await sel();
        assert.equal(b.shown, 'Interact'); assert.equal(b.desc, 'You can change this later.');
        console.log('   ok (before Connect)');

        // connected from the command line (no answer was sent, so no cap is saved): connected, so not "until you connect"
        await ev(`(async () => { window.__st = { configured: true, state: 'connected', hub_url: 'https://hub.example', max_mode: 'view', max_mode_saved: false, allow_hub_auto_accept: true }; await window.__cc.loadCloudStatus(); return true; })()`);
        b = await sel();
        assert.equal(b.shown, 'Not chosen yet (View)'); assert.equal(b.desc, 'Coral Hub can only view until you choose here.', 'a connected computer is not told "until you connect"');
        assert.equal(b.value, '');
        await ev(`(() => { const s = document.getElementById('settings-cloud-max-mode'); s.value = ''; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
        assert.equal(await ev(`window.__puts.length`), 0, 'the placeholder saves nothing');
        await ev(`(() => { const s = document.getElementById('settings-cloud-max-mode'); s.value = 'interact'; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
        await waitFor(`window.__puts.length === 1 && !document.querySelector('#settings-cloud-max-mode option[data-unchosen]')`, 'the choice saved and the placeholder gone', 5000);
        b = await sel();
        assert.equal(b.shown, 'Interact'); assert.ok(!b.options.some(o => /Not chosen/.test(o)));
        console.log('   ok (connected, never chosen; a choice saves)');

        console.log('B. an unreachable hub: a sentence, a countdown beside it, Connect off until it is over');
        await ev(`(async () => { window.__st = { configured: true, state: 'idle', hub_url: 'https://hub.example', max_mode: 'view', max_mode_saved: true, last_error: 'could not reach the hub to start enrollment', last_error_code: 'hub_unreachable', retry_in_s: 3 }; await window.__cc.loadCloudStatus(); return true; })()`);
        const r = () => ev(`(() => { const st = document.getElementById('settings-cloud-status'), rt = document.querySelector('#settings-cloud-status + [data-cloud="retry"]'), c = document.querySelector('#settings-cloud-hub [data-cloud="connect"], [data-cloud="connect"]'); return { status: st.textContent, retry: rt ? rt.textContent : null, retryLive: rt ? rt.getAttribute('aria-live') : null, connectOff: c ? c.disabled : null, statusLive: st.getAttribute('aria-live') }; })()`);
        await ev(`document.getElementById('settings-cloud-status').scrollIntoView({ block: 'center' }); true`); await shot('hub-settings-unreachable');
        let x = await r();
        assert.equal(x.status, 'Coral could not reach Coral Hub. Check your connection and try again.');
        assert.match(x.retry, /^Try again in [123] s$/); assert.equal(x.retryLive, 'off', 'the countdown is not a live region'); assert.equal(x.connectOff, true, 'Connect is off while it counts');
        assert.equal(x.statusLive, 'polite', 'the sentence is said once, by the status');
        await waitFor(`!document.querySelector('#settings-cloud-status + [data-cloud="retry"]')`, 'the countdown over', 8000);
        x = await r();
        assert.equal(x.connectOff, false, 'Connect is on again'); assert.equal(x.status, 'Coral could not reach Coral Hub. Check your connection and try again.', 'the sentence stays');
        console.log('   ok');
        console.log('PASS Settings says what is not chosen yet and what could not be reached');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
