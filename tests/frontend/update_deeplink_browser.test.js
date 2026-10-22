// The tray's "Update to vX available" opens the dashboard at /?settings=updates (updater D, #6006): Settings on its Updates tab, when the server says the updater is on. That one value only: another value,
// a repeated parameter or a case other than "updates" opens nothing; with the update feature off (a 404) it opens nothing either; the parameter is taken off the address so a reload does not open it again, and
// any other parameter stays. The status route is stubbed (the page's fetch, installed before the page's own scripts run).
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const STATUS = { enabled: true, installed: '1.4.155', state: 'none', not_ready_reason: 'up_to_date', relaunch: { available: false, reason: 'nothing_staged', restarts: [], ack: '' } };
const stub = (status, body) => `(() => { const R = window.fetch;
    window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin);
        if (url.pathname === '/api/system/update') return Promise.resolve(new Response(JSON.stringify(${JSON.stringify(body)}), { status: ${status}, headers: { 'content-type': 'application/json' } }));
        if (url.pathname === '/api/system/update-check') return Promise.resolve(new Response(JSON.stringify({ available: false }), { status: 200, headers: { 'content-type': 'application/json' } }));
        return R(u, o); }; })();`;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        const open = async (source, query) => {
            const { identifier } = await Page.addScriptToEvaluateOnNewDocument({ source });
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' + query }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            return identifier;
        };
        const paneOpen = () => ev(`!!document.getElementById('settings-pane-updates') && !document.getElementById('settings-pane-updates').hidden`);
        const done = async (id) => Page.removeScriptToEvaluateOnNewDocument({ identifier: id });

        // the one value opens the Updates tab, and the parameter leaves the address (other parameters stay)
        let id = await open(stub(200, STATUS), '?settings=updates&keep=1');
        await waitFor(`!!document.getElementById('settings-pane-updates') && !document.getElementById('settings-pane-updates').hidden`, 'Settings opens on the Updates tab', 10000);
        const search = await ev(`location.search`);
        assert.ok(!/settings=/.test(search) && /keep=1/.test(search), `the parameter is taken off the address and the rest stays (${search})`);
        console.log('PASS ?settings=updates opens Settings on Updates and leaves the address without it');
        await done(id);

        // anything else opens nothing: another tab, another case, a repeat, a value with more in it, an empty value
        for (const q of ['?settings=general', '?settings=Updates', '?settings=UPDATES', '?settings=updates&settings=updates', '?settings=updates%00', '?settings=updates%20', '?settings=', '?settings', '?setting=updates', '?settings[]=updates']) {
            id = await open(stub(200, STATUS), q);
            await ev(`new Promise(r => setTimeout(r, 400))`); // sleep-ok: the absence of a modal needs the startup to have run
            assert.equal(await paneOpen(), false, `${q}: nothing opened`);
            assert.equal(await ev(`!document.getElementById('settings-modal') || document.getElementById('settings-modal').style.display === 'none' || document.getElementById('settings-modal').hidden || getComputedStyle(document.getElementById('settings-modal')).display === 'none'`), true, `${q}: no Settings modal`);
            await done(id);
            console.log(`PASS ${q} opens nothing`);
        }

        // the update feature is off: the dashboard has no Updates tab to open
        id = await open(stub(404, { error: 'not_found' }), '?settings=updates');
        await ev(`new Promise(r => setTimeout(r, 400))`); // sleep-ok: the absence of a modal needs the startup to have run
        assert.equal(await paneOpen(), false, 'with the feature off nothing opens');
        await done(id);
        // enabled false (the apply is off in this build): the UI is off, so nothing opens
        id = await open(stub(200, { ...STATUS, enabled: false }), '?settings=updates');
        await ev(`new Promise(r => setTimeout(r, 400))`); // sleep-ok: the absence of a modal needs the startup to have run
        assert.equal(await paneOpen(), false, 'with the updater off nothing opens');
        await done(id);
        console.log('PASS with the feature off, or the updater off, the link opens nothing');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
