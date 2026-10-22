// The auto-update UI is on only when the SERVER says so (updater C, #5997 review): the dashboard asks GET /api/system/update once at startup and every later local status read refreshes the answer.
// `enabled: true` turns it on; `enabled: false`, a 404 (the update feature is off), a refusal or a reply that is not JSON leave it off; a later 404 or `enabled: false` from a status read turns it off again.
// No localStorage flag is involved any more. The route is stubbed (the page's fetch), and the answer is read through the app's own modules (update_flag.js, update_api.js).
// Not covered here: a dashboard served through Coral Hub (it never asks; update_flag.js returns false before any request).
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const STATUS = { enabled: true, installed: '1.4.155', state: 'none', not_ready_reason: 'up_to_date' };
const stub = (status, body, raw) => `(() => { window.__asks = 0; const R = window.fetch;
    window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin);
        if (url.pathname === '/api/system/update') { window.__asks++; return Promise.resolve(new Response(${raw ? JSON.stringify(raw) : `JSON.stringify(${JSON.stringify(body)})`}, { status: ${status}, headers: { 'content-type': 'application/json' } })); }
        return R(u, o); }; })();`;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        const S = `(window.__coralStatic || '/static/')`;
        const load = async (source) => {
            const { identifier } = await Page.addScriptToEvaluateOnNewDocument({ source });
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            return identifier;
        };
        const flag = () => ev(`import(${S} + 'update_flag.js').then(m => m.loadUpdatesFlag().then(on => ({ on, now: m.updatesUiOn() })))`);
        const cases = [
            ['enabled: true', stub(200, STATUS), true],
            ['enabled: false', stub(200, { ...STATUS, enabled: false }), false],
            ['404 (the update feature is off)', stub(404, { error: 'not_found' }), false],
            ['403 (a refusal)', stub(403, { error: 'owner_only' }), false],
            ['500', stub(500, { error: 'x' }), false],
            ['a reply that is not JSON', stub(200, null, 'not json at all'), false],
            ['enabled is the string "true", not true', stub(200, { ...STATUS, enabled: 'true' }), false],
        ];
        for (const [name, source, want] of cases) {
            const id = await load(source);
            const r = await flag();
            assert.equal(r.now, want, `${name}: the UI is ${want ? 'on' : 'off'} (${JSON.stringify(r)})`);
            assert.equal(r.on, want, `${name}: loadUpdatesFlag says ${want}`);
            await Page.removeScriptToEvaluateOnNewDocument({ identifier: id });
            console.log(`PASS ${name} -> ${want ? 'on' : 'off'}`);
        }
        // The app itself starts the update UI from the same answer: with enabled, the pill's code runs (a status poll is made); without it, nothing asks again.
        let id = await load(stub(200, STATUS));
        await waitFor(`import(${S} + 'update_flag.js').then(m => m.updatesUiOn())`, 'the app turned the UI on from the server\'s answer', 8000);
        await waitFor(`window.__asks >= 2`, 'the started UI reads the status again (startup probe plus its own refresh)', 8000);
        // a later status read that says 404 (the feature went off) turns it off again, and one that says enabled turns it on
        await ev(`window.fetch = (u, o) => Promise.resolve(new Response('{}', { status: 404 })); true`);
        assert.equal(await ev(`import(${S} + 'update_api.js').then(m => m.fetchUpdateStatus()).then(s => s)`), null);
        assert.equal(await ev(`import(${S} + 'update_flag.js').then(m => m.updatesUiOn())`), false, 'a 404 status read turns the UI off');
        await ev(`window.fetch = (u, o) => Promise.resolve(new Response(JSON.stringify(${JSON.stringify(STATUS)}), { status: 200, headers: { 'content-type': 'application/json' } })); true`);
        await ev(`import(${S} + 'update_api.js').then(m => m.fetchUpdateStatus()).then(() => true)`);
        assert.equal(await ev(`import(${S} + 'update_flag.js').then(m => m.updatesUiOn())`), true, 'an enabled status read turns it on');
        await Page.removeScriptToEvaluateOnNewDocument({ identifier: id });
        id = await load(stub(404, { error: 'not_found' }));
        const r = await flag();
        assert.equal(r.now, false);
        assert.equal(await ev(`window.__asks`), 1, 'with the feature off the page asked once (the probe) and the UI never started polling');
        console.log('PASS the update UI follows the server: on only for enabled, refreshed by each status read, no polling when off');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
