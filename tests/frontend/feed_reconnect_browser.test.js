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
                constructor(url) { super(); this.url = url; times.push(performance.now()); setTimeout(() => this.onclose && this.onclose({ code: window.__closeCode || 1006, reason: '' }), 10); }
                close() {} send() {}
            };
            window.__closeCode = 1006;
            m.connectCoralWs();
            await new Promise(r => { const w = () => times.length >= 4 ? r() : setTimeout(w, 100); w(); });
            const gaps = times.slice(1).map((t, i) => Math.round(t - times[i]));
            // A session-ended close must not schedule another attempt.
            window.__closeCode = 4401;
            const before = times.length;
            await new Promise(r => setTimeout(r, 17000));
            const after = times.length;
            window.WebSocket = RealWS;
            return { gaps, ended: after - before };
        })()`);
        const [g1, g2, g3] = gaps.gaps;
        assert.ok(g1 >= 1000 && g1 <= 2300, `first retry 1-2 s, was ${g1} ms`);
        assert.ok(g2 >= 2000 && g2 <= 4300, `second retry 2-4 s, was ${g2} ms`);
        assert.ok(g3 >= 4000 && g3 <= 8300, `third retry 4-8 s, was ${g3} ms`);
        // one more attempt may already have been scheduled when the close code changed (it is then the last)
        assert.ok(gaps.ended <= 2, `no reconnect loop after a session-ended close (${gaps.ended} attempts in 17 s)`);
        console.log(`PASS feed reconnect backs off (${g1}, ${g2}, ${g3} ms) and stops on 4401`);
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
