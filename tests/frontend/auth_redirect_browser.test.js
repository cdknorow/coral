// A dashboard with no sign-in session (#3418, #3939; strict mode): an /api call answering 401 dashboard_auth_required shows the
// "Coral now requires sign-in" panel (a modal dialog: how to get in, a link to the /auth explanation, Try again), once, and every poller
// and the live feed wait until a call succeeds. Try again while still refused keeps the panel; once signed in it reloads the page and
// the view is back. A 401 that is not "signed out" (an agent-only route's session_token_required) and a 401 from another origin change
// nothing.
//
// Expects CORAL_TEST_URL + CDP_PORT from the runner. Drives Chrome over CDP (chrome-remote-interface). Exit 0 / 1.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: shows that nothing runs while signed out (no condition to wait for)
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        // /api/qa-locked answers like strict mode; /api/qa-agent-only is an agent-only route's own 401; while sessionStorage 'qa-locked' is '1',
        // the authenticated probe /api/settings is refused too (sessionStorage so the state survives the reload the panel does).
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => { const real = window.fetch.bind(window);
            const json = (b, s) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'Content-Type': 'application/json' } }));
            window.__api = 0; window.__inflight = 0;
            // Count the terminal sockets the page opens, and run the terminal's 3 s reconnect delay in 0.2 s (this suite only).
            const RealWS = window.WebSocket; window.__termWs = 0;
            window.WebSocket = function (u, p) { if (String(u).includes('/ws/terminal')) window.__termWs++; return p === undefined ? new RealWS(u) : new RealWS(u, p); };
            window.WebSocket.prototype = RealWS.prototype; for (const k of ['CONNECTING', 'OPEN', 'CLOSING', 'CLOSED']) window.WebSocket[k] = RealWS[k];
            const realSetTimeout = window.setTimeout.bind(window); window.setTimeout = (fn, ms, ...rest) => realSetTimeout(fn, ms === 3000 ? 200 : ms, ...rest);
            window.fetch = (u, o) => {
                const s = String(u);
                if (s.includes('/api/qa-locked')) return json({ error: 'sign in', code: 'dashboard_auth_required' }, 401);
                if (s.includes('/api/qa-agent-only')) return json({ error: 'session_token_required', message: 'agents only' }, 401);
                if (s.includes('/api/') && sessionStorage.getItem('qa-locked') === '1' && s.includes('/api/settings')) return json({ error: 'sign in', code: 'dashboard_auth_required' }, 401);
                if (s.startsWith('/api/') || s.startsWith(location.origin + '/api/')) {
                    window.__api++; window.__inflight++;
                    const pr = real(u, o); const done = () => { window.__inflight--; }; pr.then(done, done); return pr;
                }
                return real(u, o);
            }; })();` });
        await Page.navigate({ url: BASE + '/chats?q=battle' }); await Page.loadEventFired(); // a page with a path and query (#3532)
        await coralReady();
        await ev(`sessionStorage.removeItem('qa-locked'); true`);
        const S = `(window.__coralStatic || '/static/')`;
        await ev(`(async () => { const o = await import(${S} + 'overload.js'); window.__ticks = 0; window.__poll = o.pollEvery(() => { window.__ticks++; }, 100); window.__o = o; return true; })()`);
        await waitFor(`window.__ticks >= 3`, 'a poller ticking while signed in', { timeout: 5000 });
        const here = await ev(`location.pathname + location.search + location.hash`);

        // A terminal socket for an agent that is not there: the server refuses it and the terminal keeps trying to reconnect, as it does for any dropped socket.
        await ev(`(async () => { const st = (await import(${S} + 'state.js')).state; st.currentSession = { type: 'live', name: 'qa-term', tmux_session: 'qa-term', agent_type: 'terminal', session_id: 'qa-term-sid' };
            window.connectTerminalWs('qa-term', 'terminal', 'qa-term-sid', 'local'); return true; })()`);
        await waitFor(`window.__termWs >= 3`, 'the terminal reconnecting while signed in', { timeout: 8000 });

        console.log('1. A 401 from another origin, or one that is not "signed out", changes nothing');
        await ev(`fetch('http://127.0.0.1:1/api/x').catch(() => 0); fetch('/api/qa-agent-only'); true`);
        await sleep(400); // sleep-ok: proves a negative, no panel appears for a 401 that is not signed out
        assert.equal(await ev(`!!document.getElementById('signed-out-panel')`), false, 'no panel for a 401 that is not signed out');
        console.log('   ✓ no panel');

        console.log('2. A signed-out 401 shows the panel once, as a modal, and says how to get in');
        await ev(`sessionStorage.setItem('qa-locked', '1'); fetch('/api/qa-locked'); fetch('/api/qa-locked'); true`);
        await waitFor(`!!document.getElementById('signed-out-panel') && document.getElementById('signed-out-panel').open`, 'the sign-in panel', { timeout: 8000 });
        assert.equal(await ev(`document.querySelectorAll('#signed-out-panel').length`), 1, 'one panel for several 401s');
        const p = await ev(`(() => { const d = document.getElementById('signed-out-panel'); return { modal: d.matches(':modal'), title: d.querySelector('#signed-out-title').textContent, text: d.querySelector('#signed-out-text').textContent,
            help: d.querySelector('#signed-out-help').getAttribute('href'), focus: document.activeElement && document.activeElement.id, labelled: d.getAttribute('aria-labelledby') }; })()`);
        assert.equal(p.title, 'Coral now requires sign-in');
        assert.match(p.text, /coral open/); assert.match(p.text, /app or the tray/);
        assert.equal(p.help, '/auth?return_to=' + encodeURIComponent(here), 'the help link is the /auth explanation, returning to this page');
        assert.equal(p.modal, true, 'a modal dialog (the page behind it cannot be used)'); assert.equal(p.focus, 'signed-out-retry', 'focus on Try again');
        assert.equal(await ev(`window.location.pathname`), '/chats', 'no redirect: the page stays where it is');
        console.log('   ✓ modal panel, focus on Try again, help link ' + p.help);

        console.log('3. Every poller and the live feed wait');
        // Wait until no /api request is in flight before taking the baseline, so a poll already
        // dispatched when the panel opened doesn't land mid-measurement (#3939 flake: 23 vs 22).
        await waitFor(`(window.__inflight || 0) === 0`, 'no /api request in flight before the baseline', { timeout: 5000 });
        const ticks = await ev(`window.__ticks`);
        const feed = await ev(`(() => { const T = window.T; return null; })()`);
        const sock = await ev(`(async () => { const st = (await import(${S} + 'state.js')).state; window.__sock = st.coralWs; return st.coralWs ? st.coralWs.readyState : -1; })()`);
        await sleep(2500); // sleep-ok: proves a negative, the 100 ms poller does not tick while signed out
        assert.equal(await ev(`window.__ticks`), ticks, 'a poller (100 ms) did not tick for 2.5 s while signed out');
        assert.equal(await ev(`(async () => { const st = (await import(${S} + 'state.js')).state; return st.coralWs === window.__sock && (!st.coralWs || st.coralWs.readyState >= 2); })()`), true, 'the live feed is closed and not reconnected');
        const calls = await ev(`window.__api`);
        await sleep(1500); // sleep-ok: proves a negative, the page makes no /api request while signed out
        assert.equal(await ev(`window.__api`), calls, 'no /api request from the page while signed out');
        console.log(`   ✓ poller frozen at ${ticks} ticks, feed closed (was ${sock}), no /api calls in 4 s`);

        console.log('3b. The terminal socket is closed and does not reconnect either');
        const term = await ev(`window.__termWs`);
        await sleep(1500); // sleep-ok: proves a negative, no terminal reconnect in 1.5 s (the retry delay is 0.2 s here)
        assert.equal(await ev(`window.__termWs`), term, 'no new terminal socket while signed out');
        assert.equal(await ev(`window.connectTerminalWs('qa-term', 'terminal', 'qa-term-sid', 'local') === undefined && window.__termWs`), term, 'and a new connect call does nothing');
        console.log(`   ✓ ${term} terminal sockets before, none after`);

        console.log('4. Try again while still refused keeps the panel and says so');
        await ev(`document.getElementById('signed-out-retry').click(); true`);
        await waitFor(`document.getElementById('signed-out-status').textContent === 'Still not signed in.'`, 'the still-refused message', { timeout: 5000 });
        assert.equal(await ev(`document.getElementById('signed-out-panel').open`), true);
        console.log('   ✓ panel stays: "Still not signed in."');

        console.log('5. Signed in again: Try again reloads the page and the view is back, no panel, pollers running');
        await ev(`sessionStorage.removeItem('qa-locked'); true`);
        const reloaded = Page.loadEventFired();
        await ev(`document.getElementById('signed-out-retry').click(); true`);
        await reloaded;
        await coralReady();
        assert.equal(await ev(`!!document.getElementById('signed-out-panel')`), false, 'no panel after the reload');
        assert.equal(await ev(`location.pathname + location.search`), '/chats?q=battle', 'the same page');
        assert.equal(await ev(`(async () => { const o = await import(${S} + 'overload.js'); return o.isSignedOut(); })()`), false, 'pollers are not paused');
        console.log('   ✓ reloaded on the same page, no panel');
        console.log('\nAll sign-in panel checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
