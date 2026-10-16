// The "Set a password" prompt must not outlive the question (#4058): a password set in another browser while this page stays open (the
// Coral app keeps its page between uses) left the prompt up when the app window was shown again. Now the prompt asks the server again
// when the page is focused or shown, and every few seconds meanwhile, and goes away once a password is set. The server's
// /api/system/auth-status is stubbed on the page. Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { const R = window.fetch; window.__ps = false; window.__statusCalls = 0; window.__changed = 0;
            document.addEventListener('coral:password-changed', () => { window.__changed++; });
            window.fetch = (u, o = {}) => { if (String(u).startsWith('/api/system/auth-status')) { window.__statusCalls++; return Promise.resolve(new Response(JSON.stringify({ authenticated: true, method: 'session', password_set: window.__ps, local: true }), { status: 200 })); } return R(u, o); };
            return true; })()`);
        // dismiss whatever the page's own startup check put up
        await ev(`document.getElementById('set-password-modal') && document.getElementById('set-password-modal').remove(); true`);
        const open = () => ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'password_ui.js'); window.__ps = false; m.showSetPasswordModal({ local: true }); return !!document.getElementById('set-password-modal'); })()`);
        const gone = () => ev(`!document.getElementById('set-password-modal')`);

        console.log('1. focusing the page again closes a stale prompt');
        assert.equal(await open(), true, 'the prompt opens while no password is set');
        await ev(`window.dispatchEvent(new Event('focus')); true`);
        await new Promise(r => setTimeout(r, 400)); // sleep-ok: a recheck asks the server; the answer has no event to wait on
        assert.equal(await gone(), false, 'while no password is set, focusing the page leaves the prompt up');
        await ev(`window.__ps = true; window.dispatchEvent(new Event('focus')); true`);
        await waitFor(`!document.getElementById('set-password-modal')`, 'the prompt to close after a password was set elsewhere', 4000);
        assert.equal(await ev(`window.__changed`), 1, 'the page is told a password is set');

        console.log('2. the page shown again (visibilitychange) closes it too');
        assert.equal(await open(), true);
        await ev(`window.__ps = true; document.dispatchEvent(new Event('visibilitychange')); true`);
        await waitFor(`!document.getElementById('set-password-modal')`, 'the prompt to close on visibilitychange', 4000);

        console.log('3. an open prompt notices on its own within seconds');
        assert.equal(await open(), true);
        await ev(`window.__ps = true; true`);
        await waitFor(`!document.getElementById('set-password-modal')`, 'the prompt to close by itself', 9000);

        console.log('4. a closed prompt asks nothing more');
        const before = await ev(`window.__statusCalls`);
        await ev(`window.dispatchEvent(new Event('focus')); document.dispatchEvent(new Event('visibilitychange')); true`);
        await new Promise(r => setTimeout(r, 600)); // sleep-ok: nothing should happen; wait out a would-be request
        assert.equal(await ev(`window.__statusCalls`), before, 'no auth-status request after the prompt is closed');
        console.log('\nPASS the set-password prompt closes once a password is set elsewhere');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
