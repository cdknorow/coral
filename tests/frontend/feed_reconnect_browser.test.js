// The live feed's reconnect backs off with jitter and stops on "session ended" (4401) (P4 of the T137 profile).
// The feed socket is replaced by one that never delivers a message, so every attempt counts as a failure and the
// delays can be read off the real timers: 1-2 s, then 2-4 s, then 4-8 s. Run against an isolated dev server.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = process.env.CORAL_URL || 'http://127.0.0.1:8462';
if (/:8420(\/|$)/.test(BASE)) throw new Error('Use an isolated test server');
(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const evaluate = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
        return r.result.value;
    };
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await evaluate(`new Promise(r => { const w = () => typeof window.selectLiveSession === 'function' ? r() : setTimeout(w, 50); w(); })`);
        const gaps = await evaluate(`(async () => {
            const m = await import('/static/websocket.js');
            const { state } = await import('/static/state.js');
            // The page's own feed must not deliver (a delivered message resets the failure count) or reconnect.
            if (state.coralWs) { state.coralWs.onmessage = null; state.coralWs.onclose = null; state.coralWs.close(); }
            const RealWS = window.WebSocket;
            const times = [];
            window.WebSocket = class extends EventTarget {
                constructor(url) { super(); this.url = url; times.push(performance.now()); setTimeout(() => { if (window.__deliver && this.onmessage) this.onmessage({ data: JSON.stringify({ type: 'noop' }) }); if (this.onclose) this.onclose({ code: window.__closeCode || 1006, reason: '' }); }, 10); }
                close() {} send() {}
            };
            window.__closeCode = 1006;
            m.connectCoralWs();
            await new Promise(r => { const w = () => times.length >= 4 ? r() : setTimeout(w, 100); w(); });
            const gaps = times.slice(1).map((t, i) => Math.round(t - times[i]));
            // A session-ended close (4401) is checked against GET /auth/session, which (like the hub) answers 200 with
            // {authenticated:false} for a browser that is really signed out and never 401. Only that stops the retries
            // and goes to sign-in; {authenticated:true}, a 503, a bad body or a network error all mean a blip.
            const realFetch = window.fetch;
            let answer = null, sessionChecks = 0, signedOutCalls = 0;
            m.feedHooks.signedOut = () => { signedOutCalls++; };
            window.fetch = async (u, o) => {
                if (String(u).includes('/auth/session')) {
                    sessionChecks++;
                    if (answer === 'error') throw new TypeError('network down');
                    if (answer === 'badjson') return new Response('<html>', { status: 200 });
                    if (answer === 503) return new Response('{"error":"unavailable"}', { status: 503 });
                    return new Response(JSON.stringify({ authenticated: answer === 'true' }), { status: 200, headers: { 'Content-Type': 'application/json' } });
                }
                return realFetch(u, o);
            };
            window.__closeCode = 4401;
            window.__deliver = true;                          // a delivered message resets the backoff to 1-2 s
            await new Promise(r => { const n = times.length; const w = () => times.length > n ? r() : setTimeout(w, 100); w(); });
            await new Promise(r => setTimeout(r, 200));       // drain the attempt that was already scheduled
            const out = { cases: {} };
            for (const c of ['false', 'true', 503, 'badjson', 'error']) {
                answer = c; signedOutCalls = 0;
                const t0 = times.length, checks0 = sessionChecks;
                m.connectCoralWs();
                await new Promise(r => setTimeout(r, 3500));
                out.cases[c] = { attempts: times.length - t0 - 1, checks: sessionChecks - checks0, signedOut: signedOutCalls };
                // stop this chain before the next case: park every further attempt as signed out
                answer = 'false'; await new Promise(r => setTimeout(r, 2500));
            }
            window.fetch = realFetch;
            window.WebSocket = RealWS;
            return { gaps, cases: out.cases };
        })()`);
        const [g1, g2, g3] = gaps.gaps;
        assert.ok(g1 >= 1000 && g1 <= 2300, `first retry 1-2 s, was ${g1} ms`);
        assert.ok(g2 >= 2000 && g2 <= 4300, `second retry 2-4 s, was ${g2} ms`);
        assert.ok(g3 >= 4000 && g3 <= 8300, `third retry 4-8 s, was ${g3} ms`);
        // one more attempt may already have been scheduled when the close code changed (it is then the last)
        const c = gaps.cases;
        assert.ok(c.false.checks >= 1 && c.false.signedOut === 1, `{authenticated:false} goes to sign-in once (${JSON.stringify(c.false)})`);
        assert.equal(c.false.attempts, 0, `and stops the retries (${JSON.stringify(c.false)})`);
        for (const k of ['true', '503', 'badjson', 'error']) {
            assert.ok(c[k].attempts >= 1 && c[k].signedOut === 0, `${k}: a blip, not a sign-out: it keeps reconnecting (${JSON.stringify(c[k])})`);
        }
        console.log(`PASS feed reconnect backs off (${g1}, ${g2}, ${g3} ms); 4401 stops only when /auth/session says authenticated:false`);
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
