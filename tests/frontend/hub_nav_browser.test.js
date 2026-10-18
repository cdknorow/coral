// Coral Hub's own pages in the hub-served dashboard (#3406). Served as the hub would serve it
// (the boot flag says mode "cloud", as in cloud_csp_browser), the Servers tab shows the links
// from /hub/nav.json that pass the agreed checks, as plain same-tab anchors with text labels.
// On a local Coral it is never fetched. An oversized or redirected answer shows nothing.
//
// Expects CORAL_TEST_URL + CDP_PORT from tests/frontend/run.sh. Drives Chrome over CDP
// (chrome-remote-interface). Exit 0 / 1.
const assert = require('node:assert/strict');
const { safePaused } = require('./helpers/paused');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Network, Fetch } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor } = require('./helpers/wait').bind(ev);
    const S = `(window.__coralStatic||'/static/')`;
    let cloud = false, navBody = '[]', navStatus = 200, navAsked = 0;
    try {
        await Promise.all([Page.enable(), Runtime.enable(), Network.enable()]);
        await Network.setCacheDisabled({ cacheDisabled: true });
        await Network.setBypassServiceWorker({ bypass: true });
        await Fetch.enable({ patterns: [
            { urlPattern: `${BASE}/`, resourceType: 'Document', requestStage: 'Response' },
            { urlPattern: `${BASE}/auth/session`, requestStage: 'Request' },
            { urlPattern: `${BASE}/hub/nav.json`, requestStage: 'Request' },
        ] });
        safePaused(client, async e => {
            const u = e.request.url;
            if (u.endsWith('/hub/nav.json')) {
                navAsked++;
                if (navStatus === 302) return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 302, responseHeaders: [{ name: 'Location', value: 'https://evil.example/nav.json' }], body: '' });
                return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: navStatus, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }], body: Buffer.from(navBody).toString('base64') });
            }
            if (u.endsWith('/auth/session')) {
                return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }],
                    body: Buffer.from(JSON.stringify({ authenticated: true, email: 'tester@example.com', csrf: 'test-csrf' })).toString('base64') });
            }
            if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
            const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
            let html = base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body;
            if (cloud) html = html.replace('{"mode":"local"}', '{"mode":"cloud"}');
            const headers = (e.responseHeaders || []).filter(h => !/^content-length$/i.test(h.name));
            await Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: headers, body: Buffer.from(html).toString('base64') });
        });
        const boot = async () => { await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
            await waitFor(`!!document.getElementById('hub-nav')`, 'the page'); };
        const load = () => ev(`(async () => { const m = await import(${S}+'hub_nav.js'); m._resetHubNavForTests(); return m.loadHubNav(); })()`);
        const nav = () => ev(`(() => { const n = document.getElementById('hub-nav');
            return { hidden: n.hidden, links: [...n.querySelectorAll('a')].map(a => ({ text: a.textContent, href: a.getAttribute('href'), target: a.getAttribute('target'), kids: a.children.length })) }; })()`);

        console.log('1. A local Coral never asks');
        navBody = JSON.stringify([{ label: 'Machines', path: '/machines' }]);
        await boot();
        assert.deepEqual(await load(), []);
        assert.equal(navAsked, 0, 'no request to /hub/nav.json');
        assert.equal((await nav()).hidden, true);
        console.log('   ✓ not fetched, nothing shown');

        console.log('2. Hub-served: the valid links, as plain anchors with text labels');
        cloud = true;
        navBody = JSON.stringify([
            { label: 'Machines', path: '/machines' },
            { label: 'API keys', path: '/machines/keys' },
            { label: '<b>Add</b> a machine', path: '/machines/add' },
            { label: 'Evil', path: 'https://evil.example/machines' },
            { label: 'Proto-relative', path: '//evil.example/machines' },
            { label: 'Up', path: '/machines/../admin' },
            { label: 'x'.repeat(41), path: '/machines/long' },
        ]);
        await boot();
        const links = await load();
        assert.equal(links.length, 3, 'only the three valid links');
        const shown = await nav();
        assert.equal(shown.hidden, false);
        assert.deepEqual(shown.links, [
            { text: 'Machines', href: '/machines', target: null, kids: 0 },
            { text: 'API keys', href: '/machines/keys', target: null, kids: 0 },
            { text: '<b>Add</b> a machine', href: '/machines/add', target: null, kids: 0 },
        ], 'same-tab anchors; the label is text, never markup');
        console.log('   ✓ three links; markup in a label stays text; the rest dropped');

        console.log('3. The Servers tab shows them');
        await ev(`(async () => { (await import(${S}+'hub_nav.js'))._resetHubNavForTests(); document.getElementById('hub-nav').replaceChildren(); window.switchNavTab && window.switchNavTab('servers'); return true; })()`);
        await waitFor(`document.querySelectorAll('#hub-nav a').length === 3`, 'the links in the Servers tab');
        console.log('   ✓ opening Servers loads and shows them');

        console.log('4. An oversized answer, a redirect, or an error shows nothing');
        for (const [what, status, body] of [['over 4 KiB', 200, JSON.stringify([{ label: 'M', path: '/machines', pad: 'x'.repeat(5000) }])],
                                             ['a redirect', 302, ''], ['a 404', 404, '[]']]) {
            navStatus = status; navBody = body;
            await boot();
            assert.deepEqual(await load(), [], what);
            assert.equal((await nav()).hidden, true, what + ': hidden');
        }
        console.log('   ✓ nothing shown for any of them');
        console.log('PASS hub_nav_browser');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
