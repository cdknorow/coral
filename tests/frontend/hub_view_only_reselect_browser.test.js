// Re-selecting an agent that Coral Hub only lets you view keeps "View only through Coral Hub" (#5860, the sessions.js writer of #5856). In cloud mode the access answer for a remote agent
// arrives after the select's header draw, so the FIRST select is always draw then access; a RE-select of the same agent draws while the earlier access state is still on, and a draw that
// writes the agent's normal placeholder over it leaves the box disabled with the normal words (the access lookup that follows finds the state already on and changes nothing).
// Cloud mode is the hub's boot flag injected into Coral's own page (as in hub_readonly_browser); the Servers list and the terminal socket are in-page fakes.
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds it by its name.
const assert = require('node:assert/strict');
const { safePaused } = require('./helpers/paused');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const VIEW = 'View only through Coral Hub';

// Installed after the harness PRELOAD: /api/servers from window.__qaServers, and a terminal socket that opens and says nothing.
const CLOUD_PRELOAD = `(() => {
    window.__qaServers = { rows: [] };
    const inner = window.fetch;
    window.fetch = (url, opts) => {
        if (new URL(String(url), location.origin).pathname === '/api/servers') return Promise.resolve(new Response(JSON.stringify({ servers: window.__qaServers.rows }), { status: 200, headers: { 'content-type': 'application/json' } }));
        return inner(url, opts);
    };
    const Real = window.WebSocket;
    function FakeTerm(u) { this.url = String(u); this.readyState = 0; this.binaryType = 'blob'; window.__wsTerm = this; setTimeout(() => { this.readyState = 1; this.onopen && this.onopen({}); }, 0); }
    FakeTerm.prototype.send = function () {}; FakeTerm.prototype.close = function () { this.readyState = 3; };
    FakeTerm.prototype.addEventListener = function () {}; FakeTerm.prototype.removeEventListener = function () {};
    function W(u, p) { const s = String(u); if (/\\/ws\\/terminal/.test(s)) return new FakeTerm(s); return p === undefined ? new Real(u) : new Real(u, p); }
    W.prototype = Real.prototype; W.CONNECTING = 0; W.OPEN = 1; W.CLOSING = 2; W.CLOSED = 3;
    window.WebSocket = W;
})();`;

(async () => {
    const { client, Page, ev, PRELOAD, install } = await connect();
    const { Fetch, Network } = client;
    const w = require('./helpers/wait').bind(ev);
    const row = (id, access_mode, local_mode) => ({ id, label: 'Box ' + id.toUpperCase(), online: true, access_mode, local_mode, last_seen: '2026-10-13T00:00:00Z' });
    const box = () => ev(`(() => { const i = document.getElementById('command-input'); const c = document.getElementById('hub-readonly-chip'); return { disabled: i.disabled, placeholder: i.placeholder, chip: !!c && !c.hidden }; })()`);
    // selectLiveSession resolves when the draw is done; the access lookup (cached after the first) lands after it, so settle on the chip and the box before reading (the access lookup is a few microtasks after the draw; the quiet period outlasts it).
    const select = async (name, server) => {
        await ev(`(async () => { await window.selectLiveSession(${JSON.stringify(name)}, 'claude', ${JSON.stringify(name + '-id')}, ${JSON.stringify(server)}); return true; })()`);
        await w.waitStable(`JSON.stringify((c => c ? [c.hidden, c.textContent] : null)(document.getElementById('hub-readonly-chip')))`, 'the access chip', 250);
        await w.waitStable(`document.getElementById('command-input').placeholder`, 'the placeholder', 250);
    };

    try {
        await Page.enable(); await Network.enable(); await Network.setBypassServiceWorker({ bypass: true });
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: CLOUD_PRELOAD });
        await Fetch.enable({ patterns: [{ urlPattern: `${BASE}/`, resourceType: 'Document', requestStage: 'Response' }, { urlPattern: `${BASE}/auth/session`, requestStage: 'Request' }] });
        safePaused(client, async e => {
            if (e.request.url.endsWith('/auth/session')) {
                return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }],
                    body: Buffer.from(JSON.stringify({ authenticated: true, email: 'qa@example.com', csrf: 'qa-csrf' })).toString('base64') });
            }
            if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
            const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
            const html = (base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body).replace('"mode":"local"', '"mode":"cloud"');
            const headers = (e.responseHeaders || []).filter(h => !/^content-length$/i.test(h.name));
            return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: headers, body: Buffer.from(html).toString('base64') });
        });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        assert.equal(await ev(`JSON.parse(document.getElementById('coral-boot').textContent).mode`), 'cloud', 'cloud mode boot flag');
        const rows = [row('v1', 'view', 'launch'), row('w1', 'interact', 'launch')];
        await ev(`(() => { window.__qaServers.rows = ${JSON.stringify(rows)}; T.state.servers = ${JSON.stringify(rows.map(r => ({ id: r.id, label: r.label, status: 'online' })))};
            window.__m.add('view-agent', 'V', 3, { sleeping: false, server: 'v1' }); window.__m.add('write-agent', 'W', 3, { sleeping: false, server: 'w1' });
            window.T.render.renderLiveSessions(window.T.state.liveSessions); return true; })()`);

        console.log('1. the first select of an agent on a View server: view only, with its words');
        await select('view-agent', 'v1');
        let b = await box();
        assert.deepEqual(b, { disabled: true, placeholder: VIEW, chip: true });

        console.log('2. a RE-select of the same agent: the words stay (the draw runs while the state is on)');
        await select('view-agent', 'v1');
        b = await box();
        assert.deepEqual(b, { disabled: true, placeholder: VIEW, chip: true }, 'the re-select\'s draw does not put the agent\'s normal text over "View only"');
        await select('view-agent', 'v1');
        assert.deepEqual(await box(), { disabled: true, placeholder: VIEW, chip: true }, 'and a third time');

        console.log('3. an agent on an Interact server opens writable, with its own words (not the view-only ones, not the previous agent\'s)');
        await select('write-agent', 'w1');
        b = await box();
        assert.equal(b.disabled, false); assert.equal(b.chip, false);
        assert.ok(/write-agent|W/.test(b.placeholder) && b.placeholder !== VIEW && !/view-agent/.test(b.placeholder), `its own placeholder: ${b.placeholder}`);

        console.log('4. back to the View agent: view only again, then lifting it (another agent) restores that agent\'s own text');
        await select('view-agent', 'v1');
        assert.deepEqual(await box(), { disabled: true, placeholder: VIEW, chip: true });
        await select('write-agent', 'w1');
        b = await box();
        assert.equal(b.disabled, false); assert.notEqual(b.placeholder, VIEW);
        console.log('PASS a re-select of a view-only agent keeps the view-only text');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
