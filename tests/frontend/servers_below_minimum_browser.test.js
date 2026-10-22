// "Update Coral to <min>" beside a machine the Coral Hub marks below its minimum version (#3915). The live feed's servers entry carries
// below_minimum (true only when below) and min_coral; the Servers list rows and the server details dialog show the hint as plain text,
// a hostile min_coral shows as text and is cut, and absent fields (or below_minimum not true) change nothing. The feed is faked.
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const { safePaused } = require('./helpers/paused');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

const HOSTILE = '<img src=x onerror=window.__pwned=1>"><a href="https://evil.example/">click</a>' + 'x'.repeat(60);
const SERVERS = [
    { id: 'm1', label: 'Box One', online: true, access_mode: 'launch', local_mode: 'launch', last_seen: '2026-10-13T00:00:00Z' },
    { id: 'm2', label: 'Box Two', online: true, access_mode: 'launch', local_mode: 'launch', last_seen: '2026-10-13T00:00:00Z' },
    { id: 'm3', label: 'Box Three', online: true, access_mode: 'launch', local_mode: 'launch', last_seen: '2026-10-13T00:00:00Z' },
    { id: 'm4', label: 'Box Four', online: true, access_mode: 'launch', local_mode: 'launch', last_seen: '2026-10-13T00:00:00Z' },
    { id: 'm5', label: 'Box Five', online: true, access_mode: 'launch', local_mode: 'launch', last_seen: '2026-10-13T00:00:00Z' },
];
// The feed's servers entries: m1 below with a version, m2 below with a hostile string, m3 nothing, m4 below_minimum false (not "true only"),
// m5 below_minimum true with no min_coral.
const LIVE = [
    { id: 'm1', label: 'Box One', status: 'online', coral_version: '1.4.2', agents: 2, below_minimum: true, min_coral: '1.4.61' },
    { id: 'm2', label: 'Box Two', status: 'online', coral_version: '1.4.2', agents: 1, below_minimum: true, min_coral: HOSTILE },
    { id: 'm3', label: 'Box Three', status: 'online', coral_version: '1.4.62', agents: 0 },
    { id: 'm4', label: 'Box Four', status: 'online', coral_version: '1.4.62', agents: 0, below_minimum: false, min_coral: '1.4.61' },
    { id: 'm5', label: 'Box Five', status: 'online', coral_version: '1.4.2', agents: 0, below_minimum: true },
];

const CLOUD_PRELOAD = `(() => {
    const inner = window.fetch;
    window.fetch = (url, opts = {}) => {
        const p = new URL(String(url), location.origin).pathname;
        if (p === '/api/servers' && (opts.method || 'GET').toUpperCase() === 'GET') {
            return Promise.resolve(new Response(JSON.stringify(window.__arrayShape ? ${JSON.stringify(SERVERS.map(x => ({ ...x, url: 'https://' + x.id + '.example.com:8420', status: 'online' })))} : { servers: ${JSON.stringify(SERVERS)} }), { status: 200, headers: { 'content-type': 'application/json' } }));
        }
        return inner(url, opts);
    };
})();`;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    const { Fetch, Network } = client;
    try {
        await Promise.all([Page.enable(), Network.enable()]);
        await Network.setBypassServiceWorker({ bypass: true });
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: CLOUD_PRELOAD });
        // Coral Hub page mode, as servers_tab_browser does it: the shell's boot mode is rewritten to cloud and the session answered.
        await Fetch.enable({ patterns: [{ urlPattern: `${BASE}/*`, resourceType: 'Document', requestStage: 'Response' }, { urlPattern: `${BASE}/auth/session`, requestStage: 'Request' }] });
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
        await Page.navigate({ url: BASE + '/' });
        await Page.loadEventFired();
        await install();
        await ev(`(() => { T.state.servers = ${JSON.stringify(LIVE)}; return true; })()`);

        console.log('1. The Servers list: the hint beside the machines that are below, nowhere else');
        await ev(`document.getElementById('nav-tab-servers').click(); true`);
        await waitFor(`document.querySelectorAll('#servers-view .cs-row').length === ${SERVERS.length}`, 'the server rows', 6000);
        const hints = () => ev(`Object.fromEntries([...document.querySelectorAll('#servers-view .cs-row')].map(r => [r.dataset.id, [...document.getElementById(r.id + '-detail').querySelectorAll('.server-below-minimum')].map(n => ({ text: n.textContent, kids: n.children.length, links: n.querySelectorAll('a, img').length }))]))`);
        let h = await hints();
        assert.deepEqual(h.m1, [{ text: 'Update Coral to 1.4.61', kids: 0, links: 0 }], 'm1: below, with its minimum');
        assert.equal(h.m2.length, 1, 'm2: one hint');
        assert.ok(h.m2[0].text.startsWith('Update Coral to <img src=x'), 'm2: the hostile string is shown as text: ' + h.m2[0].text);
        assert.equal(h.m2[0].kids, 0, 'm2: no child elements'); assert.equal(h.m2[0].links, 0, 'm2: no link or image');
        assert.ok(h.m2[0].text.length <= 'Update Coral to '.length + 32, 'm2: the string is cut to 32 characters (' + h.m2[0].text.length + ')');
        assert.deepEqual(h.m3, [], 'm3: no fields, no hint');
        assert.deepEqual(h.m4, [], 'm4: below_minimum false, no hint');
        assert.deepEqual(h.m5, [{ text: 'Update Coral', kids: 0, links: 0 }], 'm5: below with no min_coral says so without a version');
        assert.equal(await ev('window.__pwned || 0'), 0, 'the hostile markup did not run');
        console.log('   ✓ m1 "Update Coral to 1.4.61"; m2 hostile text, no markup, cut; m3 and m4 nothing; m5 "Update Coral"');

        console.log('2. The server details panel');
        for (const [id, want, hostile] of [['m1', 'Update Coral to 1.4.61'], ['m2', null, true], ['m3', ''], ['m4', ''], ['m5', 'Update Coral']]) {
            const out = await ev(`(async () => {
                const S = window.__coralStatic || '/static/';
                const m = await import(S + 'server_status.js');
                T.state.hub = true;
                const srv = ${JSON.stringify(LIVE)}.find(s => s.id === ${JSON.stringify(id)});
                T.state.servers = [srv];
                const host = document.createElement('div'); host.id = 'qa-details-host'; document.body.appendChild(host);
                host.innerHTML = m.serverDetailsHtml(srv);
                const n = [...host.querySelectorAll('.server-below-minimum')];
                const r = { count: n.length, text: n[0] ? n[0].textContent : '', kids: n[0] ? n[0].children.length : 0, imgs: host.querySelectorAll('img').length };
                host.remove();
                return r;
            })()`);
            if (want === '') assert.equal(out.count, 0, `${id}: no hint in the dialog`);
            else {
                assert.equal(out.count, 1, `${id}: one hint in the dialog`);
                if (hostile) assert.ok(out.text.startsWith('Update Coral to <img src=x') && out.kids === 0 && out.imgs === 0, `${id}: hostile text stays text in the dialog`);
                else assert.equal(out.text, want, `${id}: the dialog's hint`);
            }
        }
        assert.equal(await ev('window.__pwned || 0'), 0, 'nothing ran from the dialog either');
        console.log('   ✓ the dialog shows the same hint as text, and nothing for absent fields');

        console.log('3. A self-hosted hub\'s Servers list (not the Coral Hub page) shows the same hint');
        await Fetch.disable();
        await Page.navigate({ url: 'about:blank' });
        await Page.navigate({ url: BASE + '/' });
        await Page.loadEventFired();
        await install();
        await ev(`(() => { window.__arrayShape = true; T.state.hub = true; T.state.servers = ${JSON.stringify(LIVE)}; return true; })()`);
        await ev(`window.switchNavTab('servers'); true`);
        await waitFor(`document.querySelectorAll('#servers-view .servers-row').length >= ${SERVERS.length}`, 'the self-hosted hub server rows', 8000);
        const own = await ev(`Object.fromEntries([...document.querySelectorAll('#servers-view .servers-row')].map(r => [r.dataset.id, [...r.querySelectorAll('.server-below-minimum')].map(n => ({ text: n.textContent, kids: n.children.length }))]))`);
        assert.deepEqual(own.m1, [{ text: 'Update Coral to 1.4.61', kids: 0 }], 'm1 in the self-hosted list');
        assert.ok(own.m2.length === 1 && own.m2[0].text.startsWith('Update Coral to <img src=x') && own.m2[0].kids === 0, 'm2: hostile text stays text');
        assert.deepEqual(own.m3, []); assert.deepEqual(own.m4, []);
        assert.deepEqual(own.m5, [{ text: 'Update Coral', kids: 0 }]);
        assert.equal(await ev('window.__pwned || 0'), 0);
        console.log('   ✓ the self-hosted list: m1 hint, m2 text only, m3 and m4 nothing, m5 "Update Coral"');

        console.log('\nAll below-minimum checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
