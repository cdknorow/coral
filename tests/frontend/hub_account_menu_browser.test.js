// Coral Hub's account menu (#4596, coral-go#1): email, Servers, Account (the hub's /account), Saved for later (the hub's /saved), Sign out. Plain same-tab links on the hub's own origin;
// the button says whether the menu is open (aria-expanded); a local Coral has no account menu and so neither entry. Served as the hub would serve it (boot flag mode "cloud", as in
// hub_nav_browser).
//
// Expects CORAL_TEST_URL + CDP_PORT from the runner. CORAL_SCREENSHOT_DIR=<dir> saves a screenshot.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Network, Fetch, Emulation } = client;
    const ev = async expression => { const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true }); if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400)); return r.result.value; };
    const { waitFor } = require('./helpers/wait').bind(ev);
    let cloud = true;
    try {
        await Promise.all([Page.enable(), Runtime.enable(), Network.enable()]);
        await Network.setCacheDisabled({ cacheDisabled: true });
        await Fetch.enable({ patterns: [{ urlPattern: `${BASE}/`, resourceType: 'Document', requestStage: 'Response' }, { urlPattern: `${BASE}/auth/session`, requestStage: 'Request' },
            { urlPattern: `${BASE}/api/servers*`, requestStage: 'Request' }, { urlPattern: `${BASE}/api/sessions/live*`, requestStage: 'Request' }] });
        const json = (id, status, body) => Fetch.fulfillRequest({ requestId: id, responseCode: status, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }], body: Buffer.from(JSON.stringify(body)).toString('base64') });
        Fetch.requestPaused(async e => {
            const u = e.request.url;
            if (u.endsWith('/auth/session')) return json(e.requestId, 200, { authenticated: true, email: 'tester@example.com', csrf: 'x' });
            if (/\/api\/servers(\?|$)/.test(u)) return json(e.requestId, 200, { servers: [] });
            if (/\/api\/sessions\/live(\?|$)/.test(u)) return json(e.requestId, 200, cloud ? { sessions: [], servers: [] } : []);
            if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
            const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
            let html = base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body;
            if (cloud) html = html.replace('{"mode":"local"}', '{"mode":"cloud"}');
            const headers = (e.responseHeaders || []).filter(h => !/^content-length$/i.test(h.name));
            await Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: headers, body: Buffer.from(html).toString('base64') });
        });
        const open = async () => {
            await Emulation.setDeviceMetricsOverride({ width: 1280, height: 756, deviceScaleFactor: 1, mobile: false });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
            await waitFor(`document.body.classList.contains('cloud-mode') === ${cloud} && !!document.querySelector('.top-bar-actions')`, 'the dashboard', { timeout: 15000 });
        };

        console.log('1. The hub: the menu lists email, Servers, Account, Saved for later, Sign out, with the two links on this origin');
        await open();
        await waitFor(`!!document.querySelector('.cloud-account-btn')`, 'the account button', { timeout: 8000 });
        assert.equal(await ev(`document.querySelector('.cloud-account-btn').getAttribute('aria-expanded')`), 'false');
        await ev(`document.querySelector('.cloud-account-btn').click(); true`);
        assert.equal(await ev(`document.querySelector('.cloud-account-btn').getAttribute('aria-expanded')`), 'true');
        assert.deepEqual(await ev(`[...document.querySelectorAll('#cloud-account-menu > *')].map(e => e.textContent)`), ['tester@example.com', 'Servers', 'Account', 'Saved for later', 'Sign out']);
        const links = await ev(`[...document.querySelectorAll('#cloud-account-menu a')].map(a => ({ text: a.textContent, href: a.getAttribute('href'), role: a.getAttribute('role'), cls: a.className, target: a.getAttribute('target'), resolved: new URL(a.href).origin === location.origin }))`);
        assert.deepEqual(links, [{ text: 'Account', href: '/account', role: 'menuitem', cls: 'overflow-menu-item', target: null, resolved: true }, { text: 'Saved for later', href: '/saved', role: 'menuitem', cls: 'overflow-menu-item', target: null, resolved: true }]);
        if (process.env.CORAL_SCREENSHOT_DIR) { fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/hub-account-menu.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); }
        await ev(`document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })); true`);
        assert.equal(await ev(`document.querySelector('.cloud-account-btn').getAttribute('aria-expanded')`), 'false');
        assert.equal(await ev(`document.getElementById('cloud-account-menu').hidden`), true);
        await ev(`document.querySelector('.cloud-account-btn').click(); true`);
        await ev(`document.body.click(); true`);
        assert.equal(await ev(`document.querySelector('.cloud-account-btn').getAttribute('aria-expanded')`), 'false', 'a click outside closes it');

        console.log('2. A local Coral has no account menu');
        cloud = false;
        await open();
        assert.equal(await ev(`!document.getElementById('cloud-account-menu') && ![...document.querySelectorAll('a')].some(a => /^\\/(account|saved)$/.test(a.getAttribute('href') || ''))`), true);
        console.log('   ✓');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
