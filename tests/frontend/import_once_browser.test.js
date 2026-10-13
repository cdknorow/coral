// Retry-safe dynamic imports (#3116):  run via run_cdp_suites.mjs
// Chrome caches a failed module fetch for the life of the page. On the Cloud Hub
// the Servers tab loads cloud_servers.js on first use; when that request fails
// once, the user gets a toast and opening the tab again loads it (as
// cloud_servers.js?retry=1), and every later importer shares that one instance.
// A background importer that fails gets no toast.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const sleep = ms => new Promise(r => setTimeout(r, ms));

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Network, Fetch } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
        return r.result.value;
    };
    const waitFor = async (expr, what, ms = 8000) => {
        for (const t = Date.now(); Date.now() - t < ms; await sleep(100)) if (await ev(expr)) return;
        throw new Error('timed out waiting for ' + what);
    };
    const json = body => ({ responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }], body: Buffer.from(JSON.stringify(body)).toString('base64') });
    const seen = [];      // cloud_servers.js requests, by file name + query
    let failNext = true;  // fail the first cloud_servers.js request
    Fetch.requestPaused(async e => {
        const u = new URL(e.request.url);
        if (e.resourceType === 'Document') {
            // The dashboard, as the Cloud Hub serves it (cloud-mode boot flag).
            const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
            const html = (base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body).replace('{"mode":"local"}', '{"mode":"cloud"}');
            const headers = (e.responseHeaders || []).filter(h => !/^content-length$/i.test(h.name));
            return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: headers, body: Buffer.from(html).toString('base64') });
        }
        if (u.pathname === '/auth/session') return Fetch.fulfillRequest({ requestId: e.requestId, ...json({ authenticated: true, email: 'tester@example.com', csrf: 'test-csrf' }) });
        if (u.pathname === '/api/servers') return Fetch.fulfillRequest({ requestId: e.requestId, ...json({ servers: [{ id: 's1', label: 'Work Mac', online: true, last_seen: '2026-10-13T00:00:00Z', access_mode: 'view', local_mode: 'launch' }] }) });
        if (u.pathname.endsWith('/cloud_servers.js')) {
            seen.push('cloud_servers.js' + u.search);
            if (failNext) { failNext = false; return Fetch.failRequest({ requestId: e.requestId, errorReason: 'Failed' }); }
        }
        return Fetch.continueRequest({ requestId: e.requestId });
    });
    const toasts = () => ev(`[...document.querySelectorAll('.toast')].map(t => t.textContent.trim())`);
    const openServers = async () => {
        await ev(`switchNavTab('home')`);
        await waitFor(`document.querySelector('.top-nav-tab.active')?.id !== 'nav-tab-servers'`, 'leaving the Servers tab', 3000);
        await ev(`switchNavTab('servers')`);
    };
    try {
        await Promise.all([Page.enable(), Runtime.enable(), Network.enable()]);
        await Network.setBypassServiceWorker({ bypass: true });
        await Network.setCacheDisabled({ cacheDisabled: true });
        await Page.navigate({ url: 'about:blank' }); await Page.loadEventFired();
        await Fetch.enable({ patterns: [
            { urlPattern: `${BASE}/`, resourceType: 'Document', requestStage: 'Response' },
            { urlPattern: '*/auth/session*', requestStage: 'Request' },
            { urlPattern: '*/api/servers*', requestStage: 'Request' },
            { urlPattern: '*/cloud_servers.js*', requestStage: 'Request' },
        ] });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await waitFor(`typeof window.switchNavTab === 'function'`, 'the dashboard');
        // The first live list must be in before Servers opens: its load re-applies
        // a #servers hash (restoreChatFromHash) and would open the tab a second
        // time, an automatic retry in the middle of step 1.
        await require('./helpers/wait').bind(ev).coralReady('startup', 'liveList');
        assert.equal(await ev(`JSON.parse(document.getElementById('coral-boot').textContent).mode`), 'cloud', 'the page runs in cloud mode');
        // Nothing has loaded the server list yet (it is not part of the startup graph).
        assert.deepEqual(seen, [], 'cloud_servers.js is not loaded at startup');

        // 1. The first load fails: a toast says so and the tab shows no list. 'No list' is read at the moment the
        // toast appears (a MutationObserver runs right after the toast is inserted): later, a background caller's
        // ?retry=N may already have loaded the module and rendered the list, which is fine.
        await ev(`(() => { window.__qaAtToast = null; new MutationObserver((_, o) => {
            if (![...document.querySelectorAll('.toast')].some(t => /Could not load the server list/.test(t.textContent))) return;
            window.__qaAtToast = { list: !!document.querySelector('#servers-tab-body .cs-row') }; o.disconnect();
        }).observe(document.body, { childList: true, subtree: true, characterData: true }); return true; })()`);
        await openServers();
        await waitFor(`[...document.querySelectorAll('.toast')].some(t => /Could not load the server list/.test(t.textContent))`, 'the failure toast');
        // The plain URL was requested once and failed. A background caller (the access lookup) may already have
        // retried by now under load; a retry is always a new ?retry=N URL, never the cached failed one (#3118).
        assert.equal(seen[0], 'cloud_servers.js', 'the first request is the plain URL, and it failed');
        assert.ok(seen.slice(1).every(u => /\?retry=\d+$/.test(u)), 'nothing re-requests the failed URL: ' + JSON.stringify(seen));
        await waitFor(`window.__qaAtToast !== null`, 'the toast snapshot');
        assert.equal((await ev(`window.__qaAtToast`)).list, false, 'no list when the failure is reported');
        console.log('PASS a failed load shows "Could not load the server list"');

        // 2. The next action loads it, under a new URL the failure is not cached for.
        await openServers();
        await waitFor(`!!document.querySelector('#servers-tab-body .cs-row')`, 'the server list after the retry');
        assert.match(await ev(`document.querySelector('#servers-tab-body').textContent`), /Work Mac/);
        assert.deepEqual(seen, ['cloud_servers.js', 'cloud_servers.js?retry=1'], 'the retry fetched cloud_servers.js?retry=1');
        console.log('PASS opening the tab again loads it (?retry=1)');

        // 3. One instance: later importers (here a background lookup and another
        // tab visit) share the retried module; nothing is fetched again.
        const same = await ev(`(async () => {
            const { importOnce } = await import((window.__coralStatic || '/static/') + 'import_once.js');
            const a = await importOnce('./cloud_servers.js');
            const b = await import(new URL('cloud_servers.js?retry=1', new URL((window.__coralStatic || '/static/'), location.href)).href);
            return a === b && typeof a.serverAccess === 'function';
        })()`);
        assert.ok(same, 'importOnce returns the retried instance');
        await openServers();
        await waitFor(`!!document.querySelector('#servers-tab-body .cs-row')`, 'the server list again');
        assert.deepEqual(seen, ['cloud_servers.js', 'cloud_servers.js?retry=1'], 'no further module request');
        console.log('PASS later importers share the one instance; no refetch');

        // 4. Without a label (a background caller) a failure is quiet; with one it toasts.
        const before = (await toasts()).length;
        const quiet = await ev(`(async () => {
            const { importOnce } = await import((window.__coralStatic || '/static/') + 'import_once.js');
            try { await importOnce('./no_such_module_3116.js'); return 'resolved'; } catch { return 'rejected'; }
        })()`);
        assert.equal(quiet, 'rejected');
        await sleep(400); // sleep-ok: proves a negative, a background failure raises no toast within this window
        assert.ok(!(await toasts()).some(t => /no_such_module/.test(t)), 'a background failure shows no toast');
        assert.ok((await toasts()).length <= before, 'no new toast for a background failure');
        console.log('PASS a background failure is quiet');
    } finally {
        await Fetch.disable().catch(() => {});
        await client.close();
    }
})().catch(e => {
    console.error('FAIL ' + e.message);
    process.exitCode = 1;
});
