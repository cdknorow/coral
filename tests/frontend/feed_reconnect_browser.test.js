// The live feed's reconnect backs off with jitter and stops on "session ended" (4401) (P4 of the T137 profile).
// The feed socket is replaced by one that never delivers a message, so every attempt counts as a failure. The delays
// the feed asks for are recorded through feedHooks.schedule (1-2 s, then 2-4 s, then 4-8 s) and run SPEED times
// faster, so the backoff is checked without waiting it out. Run against an isolated dev server.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
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
            const m = await import((window.__coralStatic||'/static/')+'websocket.js');
            const { state } = await import((window.__coralStatic||'/static/')+'state.js');
            // The page's own feed must not deliver (a delivered message resets the failure count) or reconnect.
            if (state.coralWs) { state.coralWs.onmessage = null; state.coralWs.onclose = null; state.coralWs.close(); }
            const RealWS = window.WebSocket;
            const times = [];
            // Record the real backoff delays, run them SPEED times faster.
            const SPEED = 50, requested = [];
            const realSchedule = m.feedHooks.schedule;
            m.feedHooks.schedule = (fn, ms) => { requested.push(ms); setTimeout(fn, ms / SPEED); };
            const waitFor = (cond, what, ms = 5000) => new Promise((resolve, reject) => {
                const end = performance.now() + ms;
                const w = () => cond() ? resolve() : performance.now() > end ? reject(new Error('timed out waiting for ' + what + ' (attempts ' + times.length + ', requested ' + JSON.stringify(requested) + ')')) : setTimeout(w, 10);
                w();
            });
            window.WebSocket = class extends EventTarget {
                constructor(url) { super(); this.url = url; times.push(performance.now()); setTimeout(() => { if (window.__deliver && this.onmessage) this.onmessage({ data: JSON.stringify({ type: 'noop' }) }); if (this.onclose) this.onclose({ code: window.__closeCode || 1006, reason: '' }); }, 10); }
                close() {} send() {}
            };
            window.__closeCode = 1006;
            m.connectCoralWs();
            // four attempts, the last of which has already failed and scheduled the next (still a 1006 close)
            await waitFor(() => times.length >= 4 && requested.length >= 4, 'four failed attempts');
            const gaps = requested.slice(0, 3);
            // The scheduler really waited what was asked (scaled): the measured gaps follow the requested delays.
            const measured = times.slice(1, 4).map((t, i) => Math.round(t - times[i]));
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
            { const n = times.length; await waitFor(() => times.length > n, 'the delivering attempt'); }
            await new Promise(r => setTimeout(r, 400));       // sleep-ok: drain the attempt that was already scheduled (max 15 s / SPEED); no event for a timer firing
            const out = { cases: {} };
            for (const c of ['false', 'true', 503, 'badjson', 'error']) {
                answer = c; signedOutCalls = 0;
                const t0 = times.length, checks0 = sessionChecks;
                m.connectCoralWs();
                if (c === 'false') {
                    await waitFor(() => signedOutCalls >= 1, 'sign-out (' + c + ')');
                    await new Promise(r => setTimeout(r, 400));   // sleep-ok: longer than any scaled retry: none may follow (a negative)
                } else {
                    await waitFor(() => times.length - t0 - 1 >= 1 && sessionChecks - checks0 >= 1, 'a retry (' + c + ')');
                }
                out.cases[c] = { attempts: times.length - t0 - 1, checks: sessionChecks - checks0, signedOut: signedOutCalls };
                // stop this chain before the next case: park every further attempt as signed out
                answer = 'false';
                if (c !== 'false') await waitFor(() => signedOutCalls >= 1, 'parking (' + c + ')');
                await new Promise(r => setTimeout(r, 400)); // sleep-ok: once parked, no further attempt may follow within this window (a negative)
            }
            window.fetch = realFetch;
            window.WebSocket = RealWS;
            m.feedHooks.schedule = realSchedule;
            return { gaps, measured, speed: SPEED, cases: out.cases };
        })()`);
        const [g1, g2, g3] = gaps.gaps;
        assert.ok(g1 >= 1000 && g1 <= 2300, `first retry 1-2 s, was ${g1} ms`);
        assert.ok(g2 >= 2000 && g2 <= 4300, `second retry 2-4 s, was ${g2} ms`);
        assert.ok(g3 >= 4000 && g3 <= 8300, `third retry 4-8 s, was ${g3} ms`);
        gaps.measured.forEach((ms, i) => {
            const want = gaps.gaps[i] / gaps.speed;
            assert.ok(ms >= want - 5 && ms <= want + 120, `retry ${i + 1} ran after its (scaled) delay: ${ms} ms vs ${want} ms`);
        });
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
