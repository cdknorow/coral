// Machine health (#3446) from the machine_health feed object (#3444).
//   Local dashboard (Settings, "This machine"):
//   1. unknown before the first value;
//   2. coral_update fills the card (status with reason, Coral, agents, load, memory, disk);
//   3. coral_diff merges the changed fields (null removes one: back at OK the reason goes);
//   4. unknown while the feed socket is down, never a stale value;
//   5. a full update without the field (an older Coral) is unknown too.
//   Through Coral Hub (the Servers tab):
//   6. an online machine with machine_health: a status dot named for screen readers, and the card;
//   7. an online machine without it: unknown; an offline machine: the dot says unknown, no card.
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { safePaused } = require('./helpers/paused');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;
const GB = 1024 ** 3;
const HEALTH = {
    status: 'busy', reason: 'cpu', responsiveness_ms: 42,
    coral: { cpu_percent: 3.5, rss_bytes: 210 * 1024 ** 2 },
    agents: { count: 3, cpu_percent: 120.4, rss_bytes: 2.5 * GB },
    host: { load1_per_core: 1.37, cpu_count: 8, mem_total: 16 * GB, mem_available: 6 * GB },
    disk: { free_bytes: 120 * GB, total_bytes: 500 * GB },
};

(async () => {
    const { client, Page, ev, waitFor } = await connect();
    const { Fetch, Network } = client;
    const ws = `(await import((window.__coralStatic || '/static/') + 'websocket.js'))`;
    const card = () => ev(`(() => {
        const c = document.querySelector('#machine-health-local .machine-health-card');
        if (!c) return null;
        const dot = c.querySelector('.machine-health-dot');
        return { status: c.querySelector('.machine-health-status').textContent, dot: dot.className, label: dot.getAttribute('aria-label'),
            rows: Object.fromEntries([...c.querySelectorAll('dt')].map(dt => [dt.textContent, dt.nextElementSibling.textContent])) };
    })()`);
    const send = msg => ev(`(async () => { ${ws}.handleCoralMessage(${JSON.stringify(msg)}); return true; })()`);
    // The server's own coral_updates on /ws/coral, counted as they arrive. The real server's
    // machine_health is taken out of every feed message before the page sees it (its value is the
    // test host's: a nearly full disk says Busy (disk), DevOps on epoch), so each socket's first
    // one is a full snapshot without it, which the page rightly turns into unknown: wait for it
    // before injecting, or it can wipe an injected value (QA, #3446).
    const feedUpdates = () => ev(`window.__feedUpdates`);
    const feedSynced = since => waitFor(`window.__feedUpdates > ${since}`, 'the feed snapshot', 15000);

    try {
        await Promise.all([Page.enable(), Network.enable()]);
        await Network.setBypassServiceWorker({ bypass: true });
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            window.__feedUpdates = 0; window.__feedHealthSeen = 0;
            const Real = window.WebSocket;
            // The page's handler sees each message with machine_health removed.
            const strip = e => {
                let m; try { m = JSON.parse(e.data); } catch { return e; }
                if (m && m.type === 'coral_update') window.__feedUpdates++;
                if (!m || !('machine_health' in m)) return e;
                window.__feedHealthSeen++;
                delete m.machine_health;
                return new MessageEvent('message', { data: JSON.stringify(m) });
            };
            function W(u, p) {
                const s = p === undefined ? new Real(u) : new Real(u, p);
                if (/\\/ws\\/coral/.test(String(u))) {
                    let h = null;
                    Object.defineProperty(s, 'onmessage', { configurable: true, get: () => h,
                        set: fn => { h = fn; Real.prototype.__lookupSetter__('onmessage').call(s, fn ? (e => fn.call(s, strip(e))) : null); } });
                }
                return s;
            }
            W.prototype = Real.prototype; W.CONNECTING = 0; W.OPEN = 1; W.CLOSING = 2; W.CLOSED = 3;
            window.WebSocket = W;
        })();` });
        await Page.navigate({ url: BASE + '/' });
        await Page.loadEventFired();
        // Settings, which holds the slot, is built on first open (#4500): built here, as the test reads the slot without opening it.
        await waitFor(`(window.__coral?.buildAllLazy?.(), !!document.getElementById('machine-health-local'))`, 'the Settings slot', 10000);
        await waitFor(`(async () => { const { state } = await import((window.__coralStatic || '/static/') + 'state.js'); return !!state.coralWs && state.coralWs.readyState === 1; })()`, 'the feed open', 10000);
        await feedSynced(0);

        // The real server does send it (#3444); the page only ever gets the values injected below.
        await waitFor(`window.__feedHealthSeen > 0`, 'the real feed carrying machine_health', 20000);
        console.log('1. Local: unknown before the first value');
        let c = await card();
        assert.equal(c.status, 'Unknown');
        assert.match(c.dot, /machine-health-unknown/);
        assert.equal(c.label, 'Machine health: Unknown');
        console.log('   ✓ Unknown');

        console.log('2. coral_update fills the card');
        await send({ type: 'coral_update', sessions: [], active_runs: [], machine_health: HEALTH });
        c = await card();
        assert.equal(c.status, 'Busy (CPU)');
        assert.match(c.dot, /machine-health-busy/);
        assert.equal(c.label, 'Machine health: Busy (CPU)');
        assert.deepEqual(c.rows, {
            'Coral': '4% CPU · 210 MB',
            'Agents': '3 · 120% CPU · 2.5 GB',
            'Load per core': '1.37 (8 cores)',
            'Memory available': '6.0 GB of 16.0 GB',
            'Disk free': '120.0 GB of 500.0 GB',
            'Responsiveness': '42 ms',
        });
        console.log(`   ✓ ${c.status}: ${JSON.stringify(c.rows)}`);

        console.log('3. coral_diff merges; null removes a field');
        await send({ type: 'coral_diff', machine_health: { status: 'ok', reason: null, host: { ...HEALTH.host, load1_per_core: 0.42 } } });
        c = await card();
        assert.equal(c.status, 'OK', 'no reason once OK');
        assert.match(c.dot, /machine-health-ok/);
        assert.equal(c.rows['Load per core'], '0.42 (8 cores)');
        assert.equal(c.rows['Coral'], '4% CPU · 210 MB', 'unchanged fields stay');
        console.log('   ✓ OK, load 0.42, the rest kept');

        console.log('4. Unknown while the feed is down');
        const before = await feedUpdates();
        await ev(`(async () => { const { state } = await import((window.__coralStatic || '/static/') + 'state.js'); state.coralWs.close(); return true; })()`);
        await waitFor(`(document.querySelector('#machine-health-local .machine-health-status') || {}).textContent === 'Unknown'`, 'unknown while disconnected', 5000);
        console.log('   ✓ Unknown while disconnected');

        console.log('5. A full update without the field (an older Coral) is unknown');
        await waitFor(`(async () => { const { state } = await import((window.__coralStatic || '/static/') + 'state.js'); return !!state.coralWs && state.coralWs.readyState === 1; })()`, 'the feed back', 15000);
        await feedSynced(before); // the reconnect's own snapshot first
        await send({ type: 'coral_update', sessions: [], active_runs: [], machine_health: HEALTH });
        assert.equal((await card()).status, 'Busy (CPU)');
        await send({ type: 'coral_update', sessions: [], active_runs: [] });
        assert.equal((await card()).status, 'Unknown');
        console.log('   ✓ Unknown');

        console.log('8. Values outside the enums show as unknown, never echoed (QA, #3446 review)');
        for (const status of ['constructor', 'toString', '__proto__', 'valueOf']) {
            await send({ type: 'coral_update', sessions: [], active_runs: [], machine_health: { ...HEALTH, status } });
            const u = await card();
            assert.equal(u.status, 'Unknown', `status ${status}`);
            assert.match(u.dot, /machine-health-unknown/, `no machine-health-${status} class`);
            assert.doesNotMatch(JSON.stringify(u), /native code|\[object Object\]/);
        }
        for (const reason of ['toString', 'constructor']) {
            await send({ type: 'coral_update', sessions: [], active_runs: [], machine_health: { ...HEALTH, reason } });
            assert.equal((await card()).status, 'Busy', `reason ${reason} is left out`);
        }
        await send({ type: 'coral_update', sessions: [], active_runs: [], machine_health: HEALTH });
        await ev(`(async () => { ${ws}.handleCoralMessage(JSON.parse('{"type":"coral_diff","machine_health":{"__proto__":{"status":"ok"},"extra":"x"}}')); return true; })()`);
        c = await card();
        assert.equal(c.status, 'Busy (CPU)', 'a diff with __proto__ or an unknown key changes nothing');
        console.log('   ✓ constructor / toString / __proto__ / valueOf: Unknown; unknown reasons left out; a __proto__ diff ignored');

        console.log('6-7. Through Coral Hub: the Servers tab');
        await Fetch.enable({ patterns: [
            { urlPattern: `${BASE}/`, resourceType: 'Document', requestStage: 'Response' },
            { urlPattern: `${BASE}/auth/session`, requestStage: 'Request' },
        ] });
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
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            const inner = window.fetch;
            window.fetch = (url, opts = {}) => {
                const p = new URL(String(url && url.url || url), location.origin).pathname;
                if (p === '/api/servers') return Promise.resolve(new Response(JSON.stringify({ servers: [
                    { id: 'm1', label: 'Box One', online: true, access_mode: 'interact', local_mode: 'launch' },
                    { id: 'm2', label: 'Box Two', online: true, access_mode: 'interact', local_mode: 'launch' },
                    { id: 'm3', label: 'Box Three', online: false, access_mode: 'view', local_mode: 'view', last_seen: '2026-10-14T00:00:00Z' },
                ] }), { status: 200, headers: { 'content-type': 'application/json' } }));
                return inner(url, opts);
            };
        })();` });
        await Page.navigate({ url: BASE + '/' });
        await Page.loadEventFired();
        await waitFor(`typeof window.switchNavTab === 'function'`, 'the hub page', 10000);
        // The page's own first live-list load sets state.servers too: inject after it.
        await ev(`window.__coralReady.startup('liveListLoaded').then(() => true)`);
        await ev(`(async () => {
            const { state } = await import((window.__coralStatic || '/static/') + 'state.js');
            // The test server's own feed would replace these servers with its (empty) list: detach it.
            const w = ${ws};
            w.feedHooks.schedule = () => {};
            if (state.coralWs) { state.coralWs.onmessage = null; state.coralWs.onclose = null; state.coralWs.close(); }
            state.hub = true;
            window.__injectServers = () => w.handleCoralMessage({ type: 'coral_update', sessions: [], active_runs: [], servers: [
                { id: 'm1', label: 'Box One', status: 'online', agents: 3, machine_health: ${JSON.stringify(HEALTH)} },
                { id: 'm2', label: 'Box Two', status: 'online', agents: 0 },
                { id: 'm3', label: 'Box Three', status: 'offline', agents: 0 },
            ] });
            window.__injectServers();
            switchNavTab('servers');
            return true; })()`);
        await waitFor(`document.querySelectorAll('#servers-view .cs-row').length === 3`, 'the three rows', 8000);
        // The test server answers the live list as a bare array, which sets state.servers to [] (a real
        // hub answers {sessions, servers}); a late list load can do that after the injection. Inject
        // again until the row shows the relayed health.
        await waitFor(`(() => { const d = document.querySelector('#servers-view .cs-row[data-id="m1"] .cs-title .machine-health-dot');
            if (d && /busy/.test(d.className)) return true; window.__injectServers(); return false; })()`, 'm1 health', 8000);
        const rows = await ev(`[...document.querySelectorAll('#servers-view .cs-row')].map(r => {
            const dot = r.querySelector('.cs-title .machine-health-dot');
            const c = document.getElementById(r.id + '-detail').querySelector('.machine-health-card'); // the card is in the row's details
            return { id: r.dataset.id, label: dot && dot.getAttribute('aria-label'), dot: dot && dot.className,
                card: c ? c.querySelector('.machine-health-status').textContent : null,
                load: c && [...c.querySelectorAll('dt')].find(d => d.textContent === 'Load per core')?.nextElementSibling.textContent || null };
        })`);
        const by = Object.fromEntries(rows.map(r => [r.id, r]));
        assert.equal(by.m1.label, 'Box One health: Busy (CPU)');
        assert.match(by.m1.dot, /machine-health-busy/);
        assert.equal(by.m1.card, 'Busy (CPU)');
        assert.equal(by.m1.load, '1.37 (8 cores)');
        assert.equal(by.m2.label, 'Box Two health: Unknown', 'online without a value: unknown');
        assert.equal(by.m2.card, 'Unknown');
        assert.equal(by.m3.label, 'Box Three health: Unknown', 'offline: unknown');
        assert.equal(by.m3.card, null, 'no card for an offline machine');
        console.log(`   ✓ ${JSON.stringify(rows.map(r => [r.id, r.label]))}`);

        console.log('\nAll machine health checks passed');
    } finally {
        try { await Fetch.disable(); } catch {}
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
