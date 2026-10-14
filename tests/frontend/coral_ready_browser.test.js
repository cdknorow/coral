// window.__coralReady (#3118): the readiness signals suites await instead of
// fixed sleeps. Startup resolves once (live list rendered, the page start's
// list answers applied, feed connected, history and settings loaded);
// 'liveListLoaded' never resolves before a startup list answer has landed,
// and never hangs when the list or the startup check fails; liveListRendered() resolves on the next render
// with its row count; agentSelected(name) resolves when that agent is current
// and its view settled (chat first page, or terminal attached), at once if it
// already is, and never for an agent switched away from before it settled.
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD + `;(() => {
            window.__readyEvents = [];
            for (const t of ['coral:ready', 'coral:live-list-rendered', 'coral:agent-view-ready'])
                window.addEventListener(t, e => window.__readyEvents.push([t, e.detail]));
        })();` });
        // The page start's /api/sessions/live requests, counted; window.__failList / __failStatus
        // (from sessionStorage) make them fail. Installed after the harness, which serves the list. When 'liveListLoaded' resolves, the counts are kept.
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            const real = window.fetch.bind(window);
            const lists = window.__lists = { started: 0, done: 0, statusDone: false, atLoaded: null };
            const fail = k => sessionStorage.getItem(k) === '1';
            window.fetch = (url, opts) => {
                const p = new URL(String(url), location.origin).pathname;
                if (p === '/api/system/status' && fail('failStatus')) return Promise.resolve(new Response('{}', { status: 500 }));
                // The startup check answers late, so its list load is a second request after the first.
                if (p === '/api/system/status') return new Promise(r => setTimeout(r, 400)).then(() => real(url, opts)).finally(() => { lists.statusDone = true; }); // sleep-ok: the startup check answering late is the case under test
                if (p !== '/api/sessions/live') return real(url, opts);
                lists.started++;
                const r = fail('failList') ? Promise.resolve(new Response('{"error":"boom"}', { status: 500 })) : real(url, opts);
                // The answer is counted once the page has had it (the app applies it after this).
                return r.then(v => { setTimeout(() => { lists.done++; }, 0); return v; }, e => { lists.done++; throw e; });
            };
            let rd;
            Object.defineProperty(window, '__coralReady', { configurable: true, get: () => rd, set: v => {
                rd = v; v.startup('liveListLoaded').then(() => { lists.atLoaded = { started: lists.started, done: lists.done, statusDone: lists.statusDone }; });
            } });
        })();` });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();

        // 1. Startup
        assert.equal(await ev(`window.__coralReady instanceof Promise`), true, '__coralReady is a Promise');
        await ev(`Promise.race([window.__coralReady.then(() => 'ok'), new Promise((_, no) => setTimeout(() => no(new Error('__coralReady did not resolve in 10 s')), 10000))])`);
        assert.equal(await ev(`window.__readyEvents.filter(e => e[0] === 'coral:ready').length`), 1, 'coral:ready fires once');
        assert.deepEqual(await ev(`window.__coralReady.parts`), { liveList: true, liveListLoaded: true, feed: true, history: true, settings: true }, 'parts snapshot');
        assert.equal(await ev(`(() => { const p = window.__coralReady.parts; p.feed = false; return window.__coralReady.parts.feed; })()`), true, 'parts is a copy');
        assert.equal(await ev(`window.__coralReady.startup('liveList', 'history', 'settings').then(() => 'ok')`), 'ok', 'startup(...parts) for a subset');
        assert.match(await ev(`window.__coralReady.startup('bogus').then(() => 'ok', e => e.message)`), /unknown startup part: bogus/);
        console.log('   ✓ startup: __coralReady resolved, coral:ready once; parts snapshot; startup(...parts)');

        // 1b. 'liveListLoaded': after the page start's list answers, not just a render
        const at = await ev(`window.__lists.atLoaded`);
        assert.ok(at && at.started >= 2, 'both start list loads ran (the startup check answered late): ' + JSON.stringify(at));
        assert.ok(at.statusDone, 'it waited for the startup check and its list load, not just the first one');
        assert.equal(at.done, at.started, 'every start list answer was in when it resolved: ' + JSON.stringify(at));
        console.log('   ✓ liveListLoaded: resolved after ' + at.started + ' start list request(s)');

        // 1c. It never hangs: a refused list, or a failing startup check, still marks it
        for (const key of ['failList', 'failStatus']) {
            await ev(`sessionStorage.setItem('${key}', '1'); true`);
            await Page.navigate({ url: BASE }); await Page.loadEventFired();
            const got = await ev(`Promise.race([window.__coralReady.startup('liveListLoaded').then(() => 'ok'), new Promise(r => setTimeout(() => r('timeout'), 8000))])`); // sleep-ok: an upper bound, not a wait
            assert.equal(got, 'ok', key + ': liveListLoaded still resolves');
            await ev(`sessionStorage.removeItem('${key}'); true`);
        }
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await install();
        await ev(`window.__coralReady`);
        console.log('   ✓ liveListLoaded: resolves when the list is refused and when the startup check fails');

        // 2. The next live-list render, with its count
        const count = await ev(`(async () => {
            const next = window.__coralReady.liveListRendered();
            window.__m.add('ready-a', 'A', 3, { sleeping: false });
            window.__m.add('ready-b', 'B', 3, { sleeping: false });
            (await import((window.__coralStatic || '/static/') + 'render.js')).renderLiveSessions(window.__qaState.liveSessions);
            return next;
        })()`);
        assert.ok(count >= 2, 'liveListRendered resolves with the row count: ' + count);
        assert.ok(await ev(`window.__readyEvents.some(e => e[0] === 'coral:live-list-rendered' && e[1].count === ${count})`), 'coral:live-list-rendered carries the count');
        console.log(`   ✓ liveListRendered: next render, ${count} rows`);

        // 3. agentSelected: chat view settled; at once when already true
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); true`);
        const a = await ev(`(async () => {
            window.selectLiveSession('ready-a', 'claude', 'ready-a-id', 'local');
            const r = await window.__coralReady.agentSelected('ready-a');
            return { ...r, msgs: document.querySelectorAll('#live-history-messages .chat-bubble, #live-history-messages [data-msg-index], #live-history-messages > *').length };
        })()`);
        assert.equal(a.name, 'ready-a'); assert.equal(a.mode, 'chat', 'chat view');
        assert.ok(a.msgs > 0, 'the chat\'s first page is in the DOM when it resolves');
        assert.deepEqual(await ev(`window.__coralReady.agentSelected('ready-a')`), { name: 'ready-a', sessionId: 'ready-a-id', mode: 'chat' }, 'resolves at once when already settled');
        assert.deepEqual(await ev(`window.__coralReady.agentSelected('ready-a-id')`), { name: 'ready-a', sessionId: 'ready-a-id', mode: 'chat' }, 'and by session_id');
        console.log('   ✓ agentSelected: chat first page rendered; immediate when already settled');

        // 4. A switch away before A settles: A never resolves, B does
        const sw = await ev(`(async () => {
            let aDone = false;
            window.selectLiveSession('ready-a', 'claude', 'ready-a-id', 'local');
            window.__coralReady.agentSelected('ready-a').then(() => { aDone = true; });
            window.selectLiveSession('ready-b', 'claude', 'ready-b-id', 'local');
            const b = await window.__coralReady.agentSelected('ready-b');
            await new Promise(r => setTimeout(r, 300)); // sleep-ok: proving A stays pending
            return { b, aDone, current: window.__qaState?.currentSession?.name || null };
        })()`);
        assert.deepEqual(sw.b, { name: 'ready-b', sessionId: 'ready-b-id', mode: 'chat' });
        assert.equal(sw.aDone, false, 'an agent switched away from before it settled never resolves');
        console.log('   ✓ rapid switch: only the agent that stayed selected resolves');

        // 4b. Same name, different sessions: the session_id tells them apart
        const dup = await ev(`(async () => {
            window.__m.add('twin', 'T1', 2, { sleeping: false, session_id: 'twin-1' });
            window.__m.add('twin', 'T2', 2, { sleeping: false, session_id: 'twin-2' });
            await window.selectLiveSession('twin', 'claude', 'twin-1', 'local');
            await window.__coralReady.agentSelected('twin-1');
            window.selectLiveSession('twin', 'claude', 'twin-2', 'local');
            const byOldId = window.__coralReady.agentSelected('twin-1').then(() => 'resolved');
            const r = await window.__coralReady.agentSelected('twin-2');
            const old = await Promise.race([byOldId, new Promise(res => setTimeout(() => res('pending'), 300))]); // sleep-ok: proving it stays pending
            return { r, old };
        })()`);
        assert.deepEqual(dup.r, { name: 'twin', sessionId: 'twin-2', mode: 'chat' }, 'the session_id selects the exact session');
        assert.equal(dup.old, 'pending', 'the other session with the same name does not resolve');
        console.log('   ✓ duplicate names: agentSelected(session_id) is exact');

        // 5. Terminal mode
        await ev(`localStorage.setItem('coral-live-view-mode', 'terminal'); true`);
        const t = await ev(`(async () => { window.selectLiveSession('ready-a', 'claude', 'ready-a-id', 'local'); return window.__coralReady.agentSelected('ready-a'); })()`);
        assert.deepEqual(t, { name: 'ready-a', sessionId: 'ready-a-id', mode: 'terminal' }, 'terminal view');
        console.log('   ✓ agentSelected: terminal view');
        console.log('PASS coral_ready_browser');
    } finally {
        await client.close();
    }
})().catch(e => { console.error(e); process.exit(1); });
