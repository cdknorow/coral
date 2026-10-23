// The Artifacts tab on the Coral Hub (#6236): the hub's /artifacts page in a frame, exactly as the Inbox's is, only where GET /api/account says artifacts_tab is true (a strict boolean, read once at load).
// True: the tab is in the row (after Docs) and in the phone's list, no frame is in the page until the tab is opened, then one frame with the constant address and the Inbox frame's attributes, kept on a second
// visit. False, a missing field, a 404, an unreadable answer, a string "true" or the number 1: no tab and no frame. A local Coral: neither. Served as the hub would serve the dashboard (cloud boot flag,
// stubbed session, servers and live list), against the isolated dev server; the account and the /artifacts page are stood in with fake names only.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const { safePaused } = require('./helpers/paused');
const BASE = require('./test_base').BASE;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Network, Fetch, Emulation } = client;
    const ev = async expression => { const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true }); if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400)); return r.result.value; };
    const { waitFor } = require('./helpers/wait').bind(ev);
    let cloud = true;
    let account = { status: 200, body: { artifacts_tab: true } }; // what GET /api/account answers: {status, body} (body a string for an unreadable one)
    try {
        await Promise.all([Page.enable(), Runtime.enable(), Network.enable()]);
        await Network.setCacheDisabled({ cacheDisabled: true });
        await Fetch.enable({ patterns: [{ urlPattern: `${BASE}/`, resourceType: 'Document', requestStage: 'Response' }, { urlPattern: `${BASE}/auth/session`, requestStage: 'Request' }, { urlPattern: `${BASE}/api/account*`, requestStage: 'Request' },
            { urlPattern: `${BASE}/api/servers*`, requestStage: 'Request' }, { urlPattern: `${BASE}/api/sessions/live*`, requestStage: 'Request' }, { urlPattern: `${BASE}/artifacts`, requestStage: 'Request' }] });
        const reply = (id, status, body, type = 'application/json') => Fetch.fulfillRequest({ requestId: id, responseCode: status, responseHeaders: [{ name: 'Content-Type', value: type }], body: Buffer.from(typeof body === 'string' ? body : JSON.stringify(body)).toString('base64') });
        safePaused(client, async e => {
            const u = e.request.url;
            if (u.endsWith('/auth/session')) return reply(e.requestId, 200, { authenticated: true, email: 'tester@example.invalid', csrf: 'x' });
            if (/\/api\/account(\?|$)/.test(u)) return reply(e.requestId, account.status, account.body);
            if (/\/api\/servers(\?|$)/.test(u)) return reply(e.requestId, 200, { servers: [] });
            if (/\/api\/sessions\/live(\?|$)/.test(u)) return reply(e.requestId, 200, cloud ? { sessions: [], servers: [] } : []);
            if (u === `${BASE}/artifacts`) return reply(e.requestId, 200, '<!doctype html><title>Artifacts (stand-in)</title><p>Artifacts</p>', 'text/html');
            if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
            const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
            let html = base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body;
            if (cloud) html = html.replace('"mode":"local"', '"mode":"cloud"');
            const headers = (e.responseHeaders || []).filter(h => !/^content-length$/i.test(h.name));
            await Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: headers, body: Buffer.from(html).toString('base64') });
        });
        const open = async (width = 1280, mobile = false) => {
            await Emulation.setDeviceMetricsOverride({ width, height: 800, deviceScaleFactor: 1, mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
            await waitFor(`document.body.classList.contains('cloud-mode') === ${cloud} && !!document.querySelector('.top-nav-tabs')`, 'the dashboard', { timeout: 15000 });
            await new Promise(r => setTimeout(r, 700)); // sleep-ok: the account answer lands after the page starts
        };
        const tabShown = () => ev(`(() => { const t = document.getElementById('nav-tab-artifacts'); return !!t && !t.hidden && t.getClientRects().length > 0; })()`);

        console.log('1. artifacts_tab: true: the tab after Docs, no frame until it is opened, then one frame as the Inbox\'s');
        await open();
        assert.equal(await tabShown(), true);
        assert.deepEqual(await ev(`[...document.querySelectorAll('.top-nav-tabs > .top-nav-tab')].filter(t => !t.hidden && t.getClientRects().length).map(t => t.textContent.trim())`).then(l => l.slice(l.indexOf('Docs'), l.indexOf('Docs') + 2)), ['Docs', 'Artifacts'], 'right after Docs');
        assert.equal(await ev(`document.getElementById('nav-tab-artifacts').getAttribute('aria-controls')`), 'artifacts-view');
        assert.equal(await ev(`!!document.getElementById('artifacts-frame')`), false, 'no frame in a visit that stays on Agents');
        await ev(`document.getElementById('nav-tab-artifacts').click(); true`);
        await waitFor(`!!document.getElementById('artifacts-frame')`, 'the frame', { timeout: 5000 });
        const f = await ev(`(() => { const x = document.getElementById('artifacts-frame'), v = document.getElementById('artifacts-view'), i = document.getElementById('inbox-frame'); return { src: x.getAttribute('src'), ref: x.getAttribute('referrerpolicy'), allow: x.getAttribute('allow'), title: x.title, cls: x.className, sandbox: x.getAttribute('sandbox'), parentOk: x.parentElement === v, shown: getComputedStyle(v).display !== 'none', region: v.getAttribute('role') + ':' + v.getAttribute('aria-label'), current: document.getElementById('nav-tab-artifacts').getAttribute('aria-current'), frames: document.querySelectorAll('iframe#artifacts-frame').length }; })()`);
        assert.deepEqual(f, { src: '/artifacts', ref: 'no-referrer', allow: '', title: 'Artifacts', cls: 'inbox-frame', sandbox: null, parentOk: true, shown: true, region: 'region:Artifacts', current: 'page', frames: 1 });
        await ev(`document.getElementById('nav-tab-agents').click(); document.getElementById('nav-tab-artifacts').click(); true`);
        await new Promise(r => setTimeout(r, 300)); // sleep-ok: the second visit settles
        assert.equal(await ev(`document.querySelectorAll('iframe#artifacts-frame').length`), 1, 'a second visit keeps the one frame');

        console.log('2. On a phone it is in the list of views, after Docs');
        await open(390, true);
        await ev(`document.getElementById('nav-select').click(); true`);
        await waitFor(`!document.getElementById('nav-more-menu').hidden`, 'the phone list', { timeout: 5000 });
        assert.deepEqual(await ev(`[...document.querySelectorAll('#nav-more-menu .nav-more-item')].map(b => b.textContent.trim())`).then(l => l.filter(t => /Docs|Artifacts/.test(t))), ['menu_bookDocs', 'inventory_2Artifacts']);

        console.log('3. False, missing, an error, unreadable, "true" or 1: no tab and no frame');
        for (const [what, a] of [['false', { status: 200, body: { artifacts_tab: false } }], ['missing', { status: 200, body: { email: 'tester@example.invalid' } }], ['404', { status: 404, body: { error: 'not found' } }], ['500', { status: 500, body: { error: 'x' } }],
            ['unreadable', { status: 200, body: 'not json' }], ['the string "true"', { status: 200, body: { artifacts_tab: 'true' } }], ['the number 1', { status: 200, body: { artifacts_tab: 1 } }], ['null', { status: 200, body: null }]]) {
            account = a;
            await open();
            assert.equal(await tabShown(), false, `${what}: no tab`);
            assert.equal(await ev(`!document.getElementById('artifacts-frame') && ![...document.querySelectorAll('.top-nav-tab')].some(t => t.id === 'nav-tab-artifacts' && !t.hidden)`), true, `${what}: no frame`);
        }

        console.log('4. A local Coral: no tab');
        account = { status: 200, body: { artifacts_tab: true } };
        cloud = false;
        await open();
        assert.equal(await tabShown(), false);
        console.log('   ✓');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
