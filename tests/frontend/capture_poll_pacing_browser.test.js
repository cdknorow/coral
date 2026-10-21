// The capture poll (GET .../sessions/live/{n}/poll) ran every 500 ms for as long as an agent was open, although the
// terminal socket already carries the output: 120 requests/min per open agent (P2 of the T137 profile). With the
// terminal socket open it now runs every 3 s; the moment the socket is down it is back to 500 ms.
// The terminal socket and the poll endpoint are stubbed; real timers, real capture.js.
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
        const r = await evaluate(`(async () => {
            const { state } = await import((window.__coralStatic||'/static/')+'state.js');
            const cap = await import((window.__coralStatic||'/static/')+'capture.js');
            const xt = await import((window.__coralStatic||'/static/')+'xterm_renderer.js');
            const RealWS = window.WebSocket, realFetch = window.fetch;
            // A terminal socket that is open until told otherwise.
            let sock;
            window.WebSocket = class { static OPEN = 1; static CONNECTING = 0; static CLOSED = 3; constructor(u) { this.url = u; this.readyState = 1; sock = this; } close() { this.readyState = 3; } send() {} };
            let polls = 0;
            window.fetch = async (u, o) => {
                if (/\\/sessions\\/live\\/pacing-agent\\/poll/.test(String(u))) { polls++; return new Response(JSON.stringify({ capture: { capture: 'x' }, tasks: [], events: [] }), { headers: { 'Content-Type': 'application/json' } }); }
                return realFetch(u, o);
            };
            document.getElementById('pane-capture') || document.body.appendChild(Object.assign(document.createElement('div'), { id: 'pane-capture' }));
            state.currentSession = { type: 'live', name: 'pacing-agent', agent_type: 'terminal', session_id: 'pacing-sid', server: 'local' };
            // Polls run only while the agent view is on screen (#3151), as after a real selection.
            (await import((window.__coralStatic||'/static/')+'utils.js')).showView('live-session-view');
            xt.connectTerminalWs('pacing-agent', 'terminal', 'pacing-sid', 'local');
            const up = xt.isTerminalWsOpen();
            cap.startCaptureRefresh();
            const wait = ms => new Promise(r => setTimeout(r, ms));
            await wait(7200); // sleep-ok: the observation window for the poll rate (an immediate poll, then one every 3 s): time is what is measured
            const whileUp = polls;          // the immediate one, then one every 3 s
            sock.readyState = 3;            // the terminal socket dropped
            const downFrom = polls;
            await wait(3200); // sleep-ok: the observation window after the socket drops: no poll may start in the interval (a negative)
            const whileDown = polls - downFrom;
            cap.stopCaptureRefresh();
            window.WebSocket = RealWS; window.fetch = realFetch; state.currentSession = null;
            return { up, whileUp, whileDown };
        })()`);
        assert.equal(r.up, true, 'the stubbed terminal socket counts as open');
        assert.ok(r.whileUp >= 2 && r.whileUp <= 4, `with the terminal socket up: ${r.whileUp} polls in 7.2 s (was ~14); expected the initial poll plus one per 3 s`);
        assert.ok(r.whileDown >= 5, `with the terminal socket down: ${r.whileDown} polls in 3.2 s; expected the 500 ms pace`);
        console.log(`PASS capture poll: ${r.whileUp} polls in 7.2 s with the terminal socket up, ${r.whileDown} in 3.2 s with it down`);
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
