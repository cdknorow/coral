// The terminal socket's reconnect backs off with jitter, like the live feed (#3944): it used to retry every 3 s forever, so after a
// server restart every open terminal reconnected in the same second and a refused socket kept knocking. WebSocket is replaced by a
// stub that fails (or opens, then drops) on command; the delays the terminal asks for are recorded through terminalHooks.schedule and
// the retry runs SPEED times faster. Checks: delays grow 1-2, 2-4, 4-8 s; jitter spans the range (Math.random pinned low and high);
// an open socket resets the backoff; a hub "busy" close keeps its 15 s; a different agent's failures start from the first step.
// Run against an isolated dev server.
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
        const out = await evaluate(`(async () => {
            const x = await import((window.__coralStatic||'/static/')+'xterm_renderer.js');
            const { state } = await import((window.__coralStatic||'/static/')+'state.js');
            const RealWS = window.WebSocket, realRandom = Math.random;
            const SPEED = 50, requested = [];
            const realSchedule = x.terminalHooks.schedule;
            x.terminalHooks.schedule = (fn, ms) => { requested.push(ms); setTimeout(fn, ms / SPEED); };
            let attempts = 0, mode = 'fail', closeCode = 1006;
            window.WebSocket = class extends EventTarget {
                static get OPEN() { return 1; }
                constructor(url) { super(); this.url = url; this.readyState = 0; attempts++;
                    setTimeout(() => { if (mode === 'open') { this.readyState = 1; if (this.onopen) this.onopen(); }
                        this.readyState = 3; if (this.onclose) this.onclose({ code: closeCode, reason: '' }); }, 10); }
                close() {} send() {}
            };
            const waitFor = (cond, what, ms = 6000) => new Promise((resolve, reject) => {
                const end = performance.now() + ms;
                const w = () => cond() ? resolve() : performance.now() > end ? reject(new Error('timed out waiting for ' + what + ' (attempts ' + attempts + ', requested ' + JSON.stringify(requested) + ')')) : setTimeout(w, 10);
                w();
            });
            const start = id => { state.currentSession = { type: 'live', session_id: id, name: id, tmux_session: id, agent_type: 'terminal' };
                requested.length = 0; x.connectTerminalWs(id, 'terminal', id, ''); };
            const out = {};
            // 1. growth: four failed attempts in a row
            Math.random = realRandom;
            start('t-a');
            await waitFor(() => requested.length >= 4, 'four failures');
            out.growth = requested.slice(0, 4);
            // 2. jitter spans the range: the same step with Math.random pinned low, then high
            x.disconnectTerminalWs();
            Math.random = () => 0; start('t-b'); await waitFor(() => requested.length >= 3, 'low jitter'); out.low = requested.slice(0, 3);
            x.disconnectTerminalWs();
            Math.random = () => 0.999; start('t-c'); await waitFor(() => requested.length >= 3, 'high jitter'); out.high = requested.slice(0, 3);
            Math.random = realRandom;
            // 3. a socket that opens resets the backoff: fail x3, one that opens and drops, then the next delay is the first step again
            x.disconnectTerminalWs();
            start('t-d'); await waitFor(() => requested.length >= 3, 'three failures');
            mode = 'open'; const n = requested.length; await waitFor(() => requested.length > n, 'the opening attempt drops');
            out.afterOpen = requested[requested.length - 1];
            mode = 'fail';
            // 4. a different agent's failures start from the first step, not the previous agent's count
            x.disconnectTerminalWs();
            start('t-e'); await waitFor(() => requested.length >= 3, 'agent e fails'); out.agentE = requested.slice(0, 3);
            x.disconnectTerminalWs();
            start('t-f'); await waitFor(() => requested.length >= 1, 'agent f fails'); out.agentF = requested[0];
            // 5. the hub busy close keeps its own longer delay
            x.disconnectTerminalWs(); closeCode = 4429;
            start('t-g'); await waitFor(() => requested.length >= 1, 'busy close'); out.busy = requested[0];
            x.disconnectTerminalWs();
            window.WebSocket = RealWS; Math.random = realRandom; x.terminalHooks.schedule = realSchedule;
            return out;
        })()`);
        const [g1, g2, g3, g4] = out.growth;
        assert.ok(g1 >= 1000 && g1 <= 2000, `first retry 1-2 s, was ${g1} ms`);
        assert.ok(g2 >= 2000 && g2 <= 4000, `second retry 2-4 s, was ${g2} ms`);
        assert.ok(g3 >= 4000 && g3 <= 8000, `third retry 4-8 s, was ${g3} ms`);
        assert.ok(g4 >= 7500 && g4 <= 15000, `fourth retry 7.5-15 s, was ${g4} ms`);
        assert.deepEqual(out.low, [1000, 2000, 4000], 'lowest jitter is half the step');
        assert.ok(out.high.every((v, i) => v >= [1990, 3990, 7990][i]), `highest jitter is the whole step: ${JSON.stringify(out.high)}`);
        assert.ok(out.afterOpen >= 1000 && out.afterOpen <= 2000, `an open socket resets the backoff, next retry ${out.afterOpen} ms`);
        assert.ok(out.agentE[0] >= 1000 && out.agentE[0] <= 2000, `another agent starts from the first step: ${out.agentE[0]} ms`);
        assert.ok(out.agentF >= 1000 && out.agentF <= 2000, `and again for the next agent: ${out.agentF} ms`);
        assert.equal(out.busy, 15000, 'a hub busy close keeps its 15 s wait');
        console.log(`PASS terminal reconnect backs off (${g1}, ${g2}, ${g3}, ${g4} ms), spreads, resets on open and per agent; busy keeps 15 s`);
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
