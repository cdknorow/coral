// A dashboard whose session ended (#3418, strict mode): an /api call answering 401
// dashboard_auth_required sends the page to /auth?return_to=<this page> once. Refused again
// within 30 s, it stays and says how to get in, instead of bouncing in a loop. A 401 from
// another origin is not Coral's session and changes nothing.
//
// Expects CORAL_TEST_URL + CDP_PORT from tests/frontend/run.sh. Drives Chrome over CDP
// (chrome-remote-interface). Exit 0 / 1.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        // Under the app's wrapper: /api/qa-locked answers like strict mode does.
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => { const real = window.fetch.bind(window);
            window.fetch = (u, o) => String(u).includes('/api/qa-locked')
                ? Promise.resolve(new Response('{"error":"sign in","code":"dashboard_auth_required"}', { status: 401, headers: { 'Content-Type': 'application/json' } }))
                : real(u, o); })();` });
        await Page.navigate({ url: BASE + '/?view=team#chat/abc' }); await Page.loadEventFired();
        await coralReady();
        await ev(`sessionStorage.removeItem('coral-auth-redirect-at'); true`);

        console.log("1. A 401 from Coral's /api: to /auth with return_to, once");
        const here = await ev(`location.pathname + location.search + location.hash`);
        await ev(`fetch('/api/qa-locked'); true`);
        await waitFor(`location.pathname === '/auth'`, 'the /auth page', { timeout: 8000 });
        const url = await ev(`location.pathname + location.search`);
        assert.equal(url, '/auth?return_to=' + encodeURIComponent(here), 'return_to is this page, as it was');
        assert.match(here, /#chat\/abc$/, 'including the hash');
        console.log('   ✓ ' + url);

        console.log('2. Refused again within 30 s: no second bounce; it says how to get in');
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await coralReady();
        await ev(`fetch('/api/qa-locked'); true`);
        await waitFor(`[...document.querySelectorAll('.toast .toast-message')].some(t => /Coral is locked/.test(t.textContent))`, 'the locked toast');
        assert.equal(await ev(`location.pathname`), '/', 'stayed on the dashboard');
        console.log('   ✓ stayed; "Coral is locked: … coral open" shown');

        console.log("3. A 401 from elsewhere is not Coral's session");
        await ev(`sessionStorage.removeItem('coral-auth-redirect-at'); true`);
        await ev(`(async () => { const real = window.fetch; await window.fetch('https://example.invalid/api/x').catch(() => {}); return true; })()`);
        assert.equal(await ev(`location.pathname`), '/', 'no redirect for another origin');
        console.log('   ✓ unchanged');
        console.log('PASS auth_redirect_browser');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
