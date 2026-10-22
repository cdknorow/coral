// The Servers tab on a machine the hub says cannot confirm its owner yet (#6110): GET /api/servers needs_relink: true shows "Owner not confirmed" in that row's actions (a button with aria-expanded) and, opened, the
// words that say what to do on that computer; needs_relink false or missing changes nothing. Desktop and a 390 px phone (44 px target, no sideways scroll). SHOTS=<dir> also writes screenshots.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { safePaused } = require('./helpers/paused');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

const SERVERS = [
    { id: 'r1', label: 'Needs Link', online: true, access_mode: 'follow', local_mode: 'launch', last_seen: '2026-10-13T00:00:00Z', needs_relink: true },
    { id: 'r2', label: 'Linked Fine', online: true, access_mode: 'follow', local_mode: 'launch', last_seen: '2026-10-13T00:00:00Z', needs_relink: false },
    { id: 'r3', label: 'Old Hub Answer', online: false, access_mode: 'follow', local_mode: 'launch', last_seen: '2026-10-12T00:00:00Z' },
];
const LIVE = SERVERS.map(s => ({ id: s.id, label: s.label, status: s.online ? 'online' : 'offline', coral_version: '1.4.157', agents: 2, update: 'current' }));
const HELP = "Needs Link can't confirm its owner yet. On that computer, open Settings > Network and Hub and choose Confirm owner.";

const CLOUD_PRELOAD = `(() => {
    window.__qaServers = ${JSON.stringify(SERVERS)};
    window.__patches = []; window.__serverGets = 0;
    const inner = window.fetch;
    window.fetch = (url, opts = {}) => {
        const p = new URL(String(url), location.origin).pathname;
        const m = (opts.method || 'GET').toUpperCase();
        if (p === '/api/servers' && m === 'GET') {
            window.__serverGets++;
            return Promise.resolve(new Response(JSON.stringify({ servers: window.__qaServers }), { status: 200, headers: { 'content-type': 'application/json' } }));
        }
        const pm = p.match(/^\\/api\\/servers\\/([^/]+)$/);
        if (pm && m === 'PATCH') {
            const body = JSON.parse(opts.body || '{}');
            window.__patches.push({ id: decodeURIComponent(pm[1]), body });
            const row = window.__qaServers.find(s => s.id === decodeURIComponent(pm[1]));
            if (row && body.access_mode) row.access_mode = body.access_mode;
            return Promise.resolve(new Response(JSON.stringify(row || {}), { status: 200, headers: { 'content-type': 'application/json' } }));
        }
        if (pm && m === 'DELETE') { (window.__deletes = window.__deletes || []).push(decodeURIComponent(pm[1])); return Promise.resolve(new Response('{}', { status: 200, headers: { 'content-type': 'application/json' } })); }
        return inner(url, opts);
    };
})();`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const { Fetch, Network } = client;

    const cloudMode = async on => {
        await Fetch.disable();
        if (!on) return;
        await Fetch.enable({ patterns: [
            { urlPattern: `${BASE}/*`, resourceType: 'Document', requestStage: 'Response' }, // every dashboard page (#3532)
            { urlPattern: `${BASE}/auth/session`, requestStage: 'Request' },
        ] });
    };
    safePaused(client, async e => {
        if (e.request.url.endsWith('/auth/session')) {
            return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }],
                body: Buffer.from(JSON.stringify({ authenticated: true, email: 'qa@example.com', csrf: 'qa-csrf' })).toString('base64') });
        }
        if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
        const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
        const html = (base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body).replace('"mode":"local"', '"mode":"cloud"');
        const headers = (e.responseHeaders || []).filter(h => !/^content-(length|encoding)$/i.test(h.name));
        return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: headers, body: Buffer.from(html).toString('base64') });
    });
    const open = async (where = '') => { // a path ("servers/m2") or an old hash link ("#servers")
        // A fresh load every time: since #3529 the dashboard URL has no query, so going from
        // /#a to /#b is a same-document hash change that fires no load event. Each wait is
        // registered before its navigation, or about:blank's quick load could be missed.
        const blank = Page.loadEventFired();
        await Page.navigate({ url: 'about:blank' });
        await blank;
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' + where });
        await loaded;
        await install();
        await ev(`(() => { T.state.servers = ${JSON.stringify(LIVE)}; return true; })()`);
    };
    const tabState = () => ev(`(() => {
        const tab = document.getElementById('nav-tab-servers'), view = document.getElementById('servers-view');
        return {
            tabShown: !!tab && !tab.hidden && getComputedStyle(tab).display !== 'none',
            role: tab && tab.getAttribute('role'), controls: tab && tab.getAttribute('aria-controls'), selected: tab && tab.getAttribute('aria-selected'), current: tab && tab.getAttribute('aria-current'),
            viewShown: !!view && getComputedStyle(view).display !== 'none', viewRole: view && view.getAttribute('role'),
            focus: document.activeElement && document.activeElement.id,
        };
    })()`);
    const rows = () => ev(`[...document.querySelectorAll('#servers-view .cs-row')].map(r => ({
        id: r.id, tabindex: r.getAttribute('tabindex'),
        access: (r.querySelector('.cs-access') || {}).textContent || '',
        target: r.classList.contains('server-row-target'),
    }))`);

    try {
        await Promise.all([Page.enable(), Network.enable()]);
        await Network.setBypassServiceWorker({ bypass: true });
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: CLOUD_PRELOAD });
        await cloudMode(true);
        for (const [dev, w, h, mobile] of [['desktop', 1280, 800, false], ['phone', 390, 844, true]]) {
            console.log(dev);
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await open('servers');
            await waitFor(`document.querySelectorAll('#servers-view .cs-row').length === 3`, 'the three machines', 15000);
            const state = () => ev(`(() => { const row = id => document.querySelector('#servers-view .cs-row[data-id="' + id + '"]'); const btn = id => row(id).querySelector('.cs-relink'); const b = btn('r1');
                const note = document.getElementById(b ? b.getAttribute('aria-controls') : 'none'); const r = b ? b.getBoundingClientRect() : null;
                return { r1: !!b, r2: !!btn('r2'), r3: !!btn('r3'), text: b ? b.textContent : null, expanded: b ? b.getAttribute('aria-expanded') : null, noteHidden: note ? note.hidden : null, noteText: note ? note.textContent : null,
                    detailOpen: !document.getElementById(row('r1').id + '-detail').hidden, box: r ? [Math.round(r.width), Math.round(r.height)] : null, sw: document.documentElement.scrollWidth, cw: document.documentElement.clientWidth }; })()`);
            let s = await state();
            assert.deepEqual([s.r1, s.r2, s.r3], [true, false, false], dev + ': the button is on the machine that needs it only: ' + JSON.stringify(s));
            assert.deepEqual([s.text, s.expanded, s.noteHidden, s.noteText], ['Owner not confirmed', 'false', true, HELP], dev + ': ' + JSON.stringify(s));
            if (mobile) assert.ok(s.box[1] >= 44, dev + ': a 44 px target ' + JSON.stringify(s.box));
            assert.ok(s.sw <= s.cw + 1, dev + ': no sideways scroll ' + s.sw + ' vs ' + s.cw);
            if (process.env.SHOTS) { fs.mkdirSync(process.env.SHOTS, { recursive: true }); fs.writeFileSync(`${process.env.SHOTS}/relink-${dev}-closed.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); }
            await ev(`document.querySelector('#servers-view .cs-row[data-id="r1"] .cs-relink').click(); true`);
            s = await state();
            assert.deepEqual([s.expanded, s.noteHidden, s.detailOpen], ['true', false, true], dev + ': opened shows the words in the row: ' + JSON.stringify(s));
            assert.equal(await ev(`document.activeElement && document.activeElement.classList.contains('cs-relink')`), true, dev + ': the keyboard stays on the button');
            if (process.env.SHOTS) fs.writeFileSync(`${process.env.SHOTS}/relink-${dev}-open.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
            await ev(`document.querySelector('#servers-view .cs-row[data-id="r1"] .cs-relink').click(); true`);
            s = await state();
            assert.deepEqual([s.expanded, s.noteHidden], ['false', true], dev + ': and closes again');
        }
        console.log('PASS servers relink: the button only on needs_relink true, its words, 44 px on a phone');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
