// Coral Hub's first run (#4586, coral-hub's copy): an account whose machine list loaded and is empty gets "Connect your first computer" (install Coral on each, then Settings > Network and Hub > Connect to Coral Hub) with an Add a computer link (the hub's /machines/add, same tab, #6351)
// instead of launch cards that go nowhere, and the empty agent list says so too (it is the phone's Agents page); computers that are all offline get "Your computers are offline"; a usable
// machine with no agents gets "No agents yet"; a list that failed to load is never shown as a first run. A folder Browse says "Connect a computer first" instead of "Failed to load".
// A local Coral never shows any of it. Served as the hub would serve it (the boot flag says mode
// "cloud", as in hub_nav_browser) with the list of servers answered by the test.
//
// Expects CORAL_TEST_URL + CDP_PORT from the runner. CORAL_SCREENSHOT_DIR=<dir> saves screenshots.
const assert = require('node:assert/strict');
const { safePaused } = require('./helpers/paused');
const fs = require('node:fs');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Network, Fetch, Emulation } = client;
    const ev = async expression => { const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true }); if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400)); return r.result.value; };
    const { waitFor } = require('./helpers/wait').bind(ev);
const { settle } = require('./helpers/wait');
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: the layout settles over frames after a viewport change; there is no completion event
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; await settle(ev, 300); fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); }; // sleep-ok: the screenshot waits for the layout to settle
    let cloud = true, machines = [], listFails = false, enrollHit = 0;
    try {
        await Promise.all([Page.enable(), Runtime.enable(), Network.enable()]);
        await Network.setCacheDisabled({ cacheDisabled: true });
        await Fetch.enable({ patterns: [{ urlPattern: `${BASE}/`, resourceType: 'Document', requestStage: 'Response' }, { urlPattern: `${BASE}/auth/session`, requestStage: 'Request' },
            { urlPattern: `${BASE}/api/servers*`, requestStage: 'Request' }, { urlPattern: `${BASE}/api/sessions/live*`, requestStage: 'Request' }, { urlPattern: `${BASE}/machines/add*`, requestStage: 'Request' }] });
        const json = (id, status, body) => Fetch.fulfillRequest({ requestId: id, responseCode: status, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }], body: Buffer.from(JSON.stringify(body)).toString('base64') });
        safePaused(client, async e => {
            const u = e.request.url;
            if (u.endsWith('/auth/session')) return json(e.requestId, 200, { authenticated: true, email: 'tester@example.com', csrf: 'x' });
            if (/\/api\/servers(\?|$)/.test(u)) return json(e.requestId, 200, { servers: machines.map(m => ({ id: m.id, label: m.label, online: m.status === 'online', created_at: '2026-10-01T00:00:00Z', last_seen: '2026-10-18T00:00:00Z' })) });
            if (/\/api\/sessions\/live(\?|$)/.test(u)) return listFails ? json(e.requestId, 500, { error: 'boom' }) : json(e.requestId, 200, cloud ? { sessions: [], servers: machines } : []);
            if (/\/machines\/add(\?|$)/.test(u)) { enrollHit++; return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'text/html' }], body: Buffer.from('<!doctype html><title>add</title>').toString('base64') }); }
            if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
            const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
            let html = base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body;
            if (cloud) html = html.replace('"mode":"local"', '"mode":"cloud"');
            const headers = (e.responseHeaders || []).filter(h => !/^content-length$/i.test(h.name));
            await Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: headers, body: Buffer.from(html).toString('base64') });
        });
        const open = async (w, h) => {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: w < 700 ? 2 : 1, mobile: w < 700 });
            await Emulation.setTouchEmulationEnabled({ enabled: w < 700 });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
            await settle(ev, 2500); // sleep-ok: the page and its first list of servers arrive from requests; there is no completion event for them
            assert.equal(await ev(`document.body.classList.contains('cloud-mode')`), cloud, 'the page is served as the hub or as a local Coral');
        };
        const shown = sel => `(() => { const e = document.querySelector(${JSON.stringify(sel)}); return !!e && getComputedStyle(e).display !== 'none' && e.getClientRects().length > 0; })()`;

        console.log('1. No machine connected, desktop 1280x756: a connect card in place of the launch cards, and the agent list says so too');
        await open(1280, 756);
        assert.equal(await ev(`document.body.classList.contains('hub-no-machine')`), true);
        assert.equal(await ev(shown('.welcome-first-run')), true, 'the connect card');
        assert.equal(await ev(shown('.welcome-launch-list')), false, 'the launch cards that go nowhere are gone');
        assert.equal(await ev(`document.querySelector('.welcome-connect-btn').textContent.trim()`), 'Add a computer');
        assert.equal(await ev(`document.querySelector('.welcome-connect-btn').getAttribute('href')`), '/machines/add', 'a real link, same tab (no target)');
        assert.equal(await ev(`document.querySelector('.welcome-connect-btn').target`), '');
        assert.equal(await ev(shown('.welcome-connect-another')), false, 'no Connect another computer link with none connected');
        assert.equal(await ev(`document.querySelector('.welcome-first-run h2').textContent`), 'Connect your first computer');
        assert.deepEqual(await ev(`[...document.querySelectorAll('.welcome-first-run p')].map(p => p.textContent)`), ['Install Coral on each computer. In Coral, go to Settings > Network and Hub > Connect to Coral Hub, and the computer appears here.']);
        assert.equal(await ev(shown('.welcome-offline')), false);
        const side = await ev(`({ text: document.getElementById('live-sessions-list').innerText.replace(/\\s+/g, ' ').trim(), open: !document.querySelector('[data-section="live-sessions"]').classList.contains('collapsed') })`);
        assert.deepEqual(side, { text: 'No computers connected yet', open: true }, 'on a wide screen the empty list says one line (the home carries the panel) and stays open');
        assert.equal(await ev(`[...document.querySelectorAll('[data-on-click="launch-show"]')].some(b => b.getClientRects().length > 0)`), false, 'no New Agent button while nothing can be launched');
        await shot('hub-first-run-desktop');

        console.log('2. Add a computer goes to the hub\'s /machines/add, in this tab, from the panel and from the empty list');
        await ev(`document.querySelector('.welcome-connect-btn').click(); true`);
        await waitFor(`location.pathname === '/machines/add'`, 'the add page', { timeout: 8000 });
        assert.equal(enrollHit, 1);
        await open(1280, 756);
        await open(390, 756); // the phone's Agents page is the list: the panel is there
        await ev(`document.querySelector('#mobile-agent-list .hub-connect-empty a.btn, #live-sessions-list .hub-connect-empty a.btn').click(); true`);
        await waitFor(`location.pathname === '/machines/add'`, 'the add page from the empty list', { timeout: 8000 });
        assert.equal(enrollHit, 2);
        await open(1280, 756);

        console.log('3. Browse says what to do, not "Failed to load"');
        await ev(`document.querySelector('[data-on-click^="m-show-launch-modal"]').click(); true`);
        await waitFor(`!!document.getElementById('launch-modal') && getComputedStyle(document.getElementById('launch-modal')).display !== 'none'`, 'the launch dialog', { timeout: 6000 });
        await ev(`document.querySelector('#launch-modal [data-on-click="m-toggle-browser"]').click(); true`);
        await waitFor(`[...document.querySelectorAll('#launch-modal .empty-state')].some(e => e.textContent === 'Connect a computer first')`, 'the message', { timeout: 6000 });
        assert.equal(await ev(`[...document.querySelectorAll('#launch-modal .empty-state')].some(e => /Failed to load/.test(e.textContent))`), false);
        await shot('hub-first-run-browse');

        console.log('4. A phone (390 px): the Agents page has the same empty state, the action is a 44 px target and nothing scrolls sideways');
        await open(390, 756);
        const phone = await ev(`(() => { const b = document.querySelector('#mobile-agent-list .hub-connect-empty a.btn, #live-sessions-list .hub-connect-empty a.btn'); const r = b && b.getBoundingClientRect();
            return { has: !!b, h: r ? Math.round(r.height) : 0, inside: r ? r.right <= innerWidth && r.left >= 0 : false, page: document.documentElement.scrollWidth - document.documentElement.clientWidth }; })()`);
        assert.ok(phone.has && phone.h >= 44 && phone.inside && phone.page <= 0, JSON.stringify(phone));
        assert.equal(await ev(`[...document.querySelectorAll('[data-on-click="launch-show"]')].some(b => b.getClientRects().length > 0)`), false, 'no + New on the phone while nothing can be launched');
        await shot('hub-first-run-phone');

        console.log('5. Machines that are all offline: "Your machines are offline", not a first run; a usable machine with no agents: "No agents yet"; a list that failed is not a first run');
        machines = [{ id: 'box-b', label: 'Box B', status: 'unreachable' }];
        await open(1280, 756);
        assert.equal(await ev(`document.body.classList.contains('hub-no-machine')`), false, 'not a first run');
        assert.equal(await ev(`document.body.classList.contains('hub-machines-offline')`), true);
        assert.equal(await ev(shown('.welcome-offline')), true); assert.equal(await ev(shown('.welcome-first-run')), false); assert.equal(await ev(shown('.welcome-launch-list')), false);
        assert.equal(await ev(`document.querySelector('.welcome-offline h2').textContent`), 'Your computers are offline');
        assert.equal(await ev(`document.querySelector('.welcome-offline p').textContent`), 'Keep Coral running on a connected computer to see its agents here.');
        assert.equal(await ev(`document.getElementById('live-sessions-list').innerText.replace(/\\s+/g, ' ').trim()`), 'Your computers are offline Keep Coral running on a connected computer to see its agents here.');
        await shot('hub-machines-offline');
        await ev(`document.querySelector('[data-on-click^="m-show-launch-modal"]').click(); true`);
        await waitFor(`!!document.getElementById('launch-modal') && getComputedStyle(document.getElementById('launch-modal')).display !== 'none'`, 'the launch dialog', { timeout: 6000 });
        await ev(`document.querySelector('#launch-modal [data-on-click="m-toggle-browser"]').click(); true`);
        await waitFor(`[...document.querySelectorAll('#launch-modal .empty-state')].some(e => e.textContent === 'Your computers are offline')`, 'the offline message in Browse', { timeout: 6000 });
        machines = [{ id: 'box-b', label: 'Box B', status: 'online' }];
        await open(1280, 756);
        assert.equal(await ev(`document.body.classList.contains('hub-no-machine') || document.body.classList.contains('hub-machines-offline')`), false);
        assert.equal(await ev(shown('.welcome-launch-list')), true); assert.equal(await ev(shown('.welcome-first-run')), false); assert.equal(await ev(shown('.welcome-offline')), false);
        assert.equal(await ev(`(() => { const a = document.querySelector('.welcome-connect-another'); return a && a.getClientRects().length > 0 && a.getAttribute('href') + '|' + a.textContent.trim(); })()`), '/machines/add|Connect another computer', 'with a computer connected: the normal home and a small Connect another computer link (same tab)');
        await shot('hub-machines-ready');
        assert.equal(await ev(`document.getElementById('live-sessions-list').innerText.replace(/\\s+/g, ' ').trim()`), 'No agents yet Start an agent on a connected computer to see it here.');
        machines = []; listFails = true;
        await open(1280, 756);
        assert.equal(await ev(`document.body.classList.contains('hub-no-machine') || document.body.classList.contains('hub-machines-offline')`), false, 'a list that failed to load is not a first run');
        assert.equal(await ev(`/Connect your first computer|Your computers are offline|No agents yet/.test(document.getElementById('live-sessions-list').innerText)`), false);
        assert.equal(await ev(shown('.welcome-first-run')), false);
        listFails = false;

        console.log('6. A local Coral never shows any of it');
        cloud = false; machines = [];
        await open(1280, 756);
        assert.equal(await ev(`document.body.classList.contains('hub-no-machine')`), false);
        assert.equal(await ev(shown('.welcome-connect')), false); assert.equal(await ev(shown('.welcome-launch-list')), true);
        assert.equal(await ev(shown('.welcome-connect-another')), false, 'a local Coral has no Connect another computer link (Hub mode only)');
        console.log('   ✓');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
