// Coral Hub's first run (#4586, coral-hub's walkthrough): an account with no machine connected has nothing to launch on, so the Agents page offers "Connect a machine" (the Servers tab's Add a
// machine steps, with the code box ready) instead of launch cards that go nowhere, an empty agent list says so with the same action (it is the phone's Agents page too), and a folder Browse says
// "Connect a machine first" instead of "Failed to load". With a usable machine none of it shows, and a local Coral never shows it. Served as the hub would serve it (the boot flag says mode
// "cloud", as in hub_nav_browser) with the list of servers answered by the test.
//
// Expects CORAL_TEST_URL + CDP_PORT from the runner. CORAL_SCREENSHOT_DIR=<dir> saves screenshots.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Network, Fetch, Emulation } = client;
    const ev = async expression => { const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true }); if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400)); return r.result.value; };
    const { waitFor } = require('./helpers/wait').bind(ev);
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: the layout settles over frames after a viewport change; there is no completion event
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; await sleep(300); fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); }; // sleep-ok: the screenshot waits for the layout to settle
    let cloud = true, machines = [];
    try {
        await Promise.all([Page.enable(), Runtime.enable(), Network.enable()]);
        await Network.setCacheDisabled({ cacheDisabled: true });
        await Fetch.enable({ patterns: [{ urlPattern: `${BASE}/`, resourceType: 'Document', requestStage: 'Response' }, { urlPattern: `${BASE}/auth/session`, requestStage: 'Request' },
            { urlPattern: `${BASE}/api/servers*`, requestStage: 'Request' }, { urlPattern: `${BASE}/api/sessions/live*`, requestStage: 'Request' }] });
        const json = (id, status, body) => Fetch.fulfillRequest({ requestId: id, responseCode: status, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }], body: Buffer.from(JSON.stringify(body)).toString('base64') });
        Fetch.requestPaused(async e => {
            const u = e.request.url;
            if (u.endsWith('/auth/session')) return json(e.requestId, 200, { authenticated: true, email: 'tester@example.com', csrf: 'x' });
            if (/\/api\/servers(\?|$)/.test(u)) return json(e.requestId, 200, { servers: machines.map(m => ({ id: m.id, label: m.label, online: m.status === 'online', created_at: '2026-10-01T00:00:00Z', last_seen: '2026-10-18T00:00:00Z' })) });
            if (/\/api\/sessions\/live(\?|$)/.test(u)) return json(e.requestId, 200, cloud ? { sessions: [], servers: machines } : []);
            if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
            const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
            let html = base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body;
            if (cloud) html = html.replace('{"mode":"local"}', '{"mode":"cloud"}');
            const headers = (e.responseHeaders || []).filter(h => !/^content-length$/i.test(h.name));
            await Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: headers, body: Buffer.from(html).toString('base64') });
        });
        const open = async (w, h) => {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: w < 700 ? 2 : 1, mobile: w < 700 });
            await Emulation.setTouchEmulationEnabled({ enabled: w < 700 });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
            await sleep(2500); // sleep-ok: the page and its first list of servers arrive from requests; there is no completion event for them
            assert.equal(await ev(`document.body.classList.contains('cloud-mode')`), cloud, 'the page is served as the hub or as a local Coral');
        };
        const shown = sel => `(() => { const e = document.querySelector(${JSON.stringify(sel)}); return !!e && getComputedStyle(e).display !== 'none' && e.getClientRects().length > 0; })()`;

        console.log('1. No machine connected, desktop 1280x756: a connect card in place of the launch cards, and the agent list says so too');
        await open(1280, 756);
        assert.equal(await ev(`document.body.classList.contains('hub-no-machine')`), true);
        assert.equal(await ev(shown('.welcome-connect')), true, 'the connect card');
        assert.equal(await ev(shown('.welcome-launch-list')), false, 'the launch cards that go nowhere are gone');
        assert.equal(await ev(`document.querySelector('.welcome-connect-btn').textContent.trim()`), 'Connect a machine');
        assert.equal(await ev(`document.querySelector('.welcome-connect h2').textContent`), 'Connect a machine to get started');
        const side = await ev(`({ text: document.getElementById('live-sessions-list').innerText.replace(/\\s+/g, ' ').trim(), open: !document.querySelector('[data-section="live-sessions"]').classList.contains('collapsed') })`);
        assert.deepEqual(side, { text: 'No machine is connected yet. Connect a machine', open: true }, 'the empty list carries the action and stays open');
        await shot('hub-first-run-desktop');

        console.log('2. Connect a machine opens the Servers tab at the Add a machine steps with the code box ready');
        await ev(`document.querySelector('.welcome-connect-btn').click(); true`);
        await waitFor(`document.activeElement && document.activeElement.id === 'cs-enroll-code'`, 'the enrollment code box focused', { timeout: 8000 });
        assert.equal(await ev(`/Add a machine/.test(document.getElementById('servers-view').innerText)`), true);
        await ev(`window.switchNavTab('agents'); true`); await sleep(300); // sleep-ok: the tab switch settles over a few frames
        await ev(`document.querySelector('#live-sessions-list .hub-connect-empty button').click(); true`);
        await waitFor(`document.activeElement && document.activeElement.id === 'cs-enroll-code'`, 'the same from the empty list', { timeout: 8000 });
        await ev(`window.switchNavTab('agents'); true`); await sleep(300); // sleep-ok: the tab switch settles over a few frames

        console.log('3. Browse says what to do, not "Failed to load"');
        await ev(`document.querySelector('[data-on-click^="m-show-launch-modal"]').click(); true`);
        await waitFor(`getComputedStyle(document.getElementById('launch-modal')).display !== 'none'`, 'the launch dialog', { timeout: 6000 });
        await ev(`document.querySelector('#launch-modal [data-on-click="m-toggle-browser"]').click(); true`);
        await waitFor(`[...document.querySelectorAll('#launch-modal .empty-state')].some(e => e.textContent === 'Connect a machine first')`, 'the message', { timeout: 6000 });
        assert.equal(await ev(`[...document.querySelectorAll('#launch-modal .empty-state')].some(e => /Failed to load/.test(e.textContent))`), false);
        await shot('hub-first-run-browse');

        console.log('4. A phone (390 px): the Agents page has the same empty state, the action is a 44 px target and nothing scrolls sideways');
        await open(390, 756);
        const phone = await ev(`(() => { const b = document.querySelector('#mobile-agent-list .hub-connect-empty button, #live-sessions-list .hub-connect-empty button'); const r = b && b.getBoundingClientRect();
            return { has: !!b, h: r ? Math.round(r.height) : 0, inside: r ? r.right <= innerWidth && r.left >= 0 : false, page: document.documentElement.scrollWidth - document.documentElement.clientWidth }; })()`);
        assert.ok(phone.has && phone.h >= 44 && phone.inside && phone.page <= 0, JSON.stringify(phone));
        await shot('hub-first-run-phone');

        console.log('5. With a usable machine none of it shows; a machine that is offline is still no machine');
        machines = [{ id: 'box-b', label: 'Box B', status: 'online' }];
        await open(1280, 756);
        assert.equal(await ev(`document.body.classList.contains('hub-no-machine')`), false);
        assert.equal(await ev(shown('.welcome-launch-list')), true); assert.equal(await ev(shown('.welcome-connect')), false);
        assert.equal(await ev(`!!document.querySelector('.hub-connect-empty')`), false);
        machines = [{ id: 'box-b', label: 'Box B', status: 'unreachable' }];
        await open(1280, 756);
        assert.equal(await ev(`document.body.classList.contains('hub-no-machine')`), true, 'every machine offline: nothing to launch on');

        console.log('6. A local Coral never shows any of it');
        cloud = false; machines = [];
        await open(1280, 756);
        assert.equal(await ev(`document.body.classList.contains('hub-no-machine')`), false);
        assert.equal(await ev(shown('.welcome-connect')), false); assert.equal(await ev(shown('.welcome-launch-list')), true);
        console.log('   ✓');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
