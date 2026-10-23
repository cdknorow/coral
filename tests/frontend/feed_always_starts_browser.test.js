// #6233: the dashboard's live feed always starts and retries. (A) an init step before it that throws no longer takes the feed with it; (B) a WebSocket constructor that throws schedules a retry;
// (C) one transient 401 (a false sign-out) is re-checked on the retry ticks and the page recovers, while a real sign-out keeps its panel. Drives Chrome over CDP against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Runtime, ev, waitFor, sleep } = await connect();
    const errors = [];
    try {
        await Page.enable(); await Runtime.enable();
        Runtime.consoleAPICalled(e => { if (e.type === 'error') errors.push(e.args.map(a => a.value || a.description || '').join(' ')); });
        const load = async () => { await Page.navigate({ url: BASE }); await Page.loadEventFired(); };
        const feedReady = `!!(window.__coralReady && window.__coralReady.parts && window.__coralReady.parts.feed === true)`;

        console.log('A. an init step that throws: the feed starts anyway');
        // initTabCustomize watches the style of #agentic-tab-board with a MutationObserver: an observe() that throws for exactly that makes that step throw (the bundle is minified, so no name can be matched).
        const broke = await Page.addScriptToEvaluateOnNewDocument({ source: `(() => { const real = MutationObserver.prototype.observe; window.__moThrown = 0;
            MutationObserver.prototype.observe = function (el, opts) { if (el && el.id === 'agentic-tab-board' && opts && opts.attributeFilter && opts.attributeFilter[0] === 'style' && !window.__moThrown) { window.__moThrown++; throw new Error('test: this init step fails'); } return real.call(this, el, opts); }; })()` });
        await load();
        await waitFor(feedReady, 'the feed ready although an init step threw', 15000, `({ ready: typeof window.__coralReady, thrown: window.__moThrown, errors: ${JSON.stringify(errors.slice(-3))} })`);
        assert.equal(await ev('window.__moThrown'), 1, 'the step really threw');
        assert.ok(errors.some(t => /init step initTabCustomize failed/.test(t)), 'the failed step is logged: ' + JSON.stringify(errors.slice(-3)));
        await Page.removeScriptToEvaluateOnNewDocument({ identifier: broke.identifier });

        console.log('B. a WebSocket constructor that throws: a retry is scheduled');
        const wsBreak = await Page.addScriptToEvaluateOnNewDocument({ source: `(() => { const Real = window.WebSocket; window.__wsTries = 0;
            window.WebSocket = new Proxy(Real, { construct(t, args) { if (String(args[0]).includes('/ws/coral') && window.__wsTries++ < 2) throw new SyntaxError('test: constructor refused'); return Reflect.construct(t, args); } }); })()` });
        await load();
        await waitFor(feedReady, 'the feed ready after two constructor failures', 20000);
        assert.ok(await ev('window.__wsTries >= 3'), 'the feed was attempted three times: two thrown, one connected');
        await Page.removeScriptToEvaluateOnNewDocument({ identifier: wsBreak.identifier });

        console.log('C. a false sign-out recovers on the retry tick; a real one keeps the panel');
        await load();
        await waitFor(feedReady, 'the feed ready');
        await ev(`window.__marker = 1; import((window.__coralStatic || '/static/') + 'signed_out.js').then(m => m.enterSignedOut()); true`);
        await waitFor(`!!document.getElementById('signed-out-panel')`, 'the signed-out panel');
        await waitFor('window.__marker !== 1', 'the page to reload once the sign-in is found to be fine', 15000);
        await waitFor(feedReady, 'the feed ready again', 15000);

        await ev(`window.__marker = 1; const real = window.fetch; window.fetch = (u, o) => String(u).includes('/api/settings') ? Promise.resolve(new Response('{}', { status: 401 })) : real(u, o);
            import((window.__coralStatic || '/static/') + 'signed_out.js').then(m => m.enterSignedOut()); true`);
        await waitFor(`!!document.getElementById('signed-out-panel')`, 'the signed-out panel (a real sign-out)');
        await sleep(7000); // sleep-ok: a real sign-out must NOT reload the page: the test waits out the four retry ticks (2, 4, 8, 15 s) to see that nothing happens
        assert.equal(await ev('window.__marker'), 1, 'a real sign-out does not reload the page');
        assert.ok(await ev(`!!document.getElementById('signed-out-panel')`), 'and keeps its panel');
        console.log('PASS the live feed always starts and retries');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
