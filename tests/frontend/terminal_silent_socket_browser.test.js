'use strict';
// A terminal socket that opens and then says nothing is closed and reconnected (#4518). The server answers every connect with the replay and
// a hello at once, so an open socket that is silent after SILENT_SOCKET_MS is stuck (a hub path that dropped the stream, a stalled attach):
// before, the pane stayed blank until something else happened. WebSocket is replaced by a stub that opens and stays silent (or speaks);
// the delays the terminal asks for run SPEED times faster through terminalHooks.schedule. Checks: the silent socket is closed after
// SILENT_SOCKET_MS and a new one is opened (with the reconnect's own backoff after it); a socket that sends its hello is left open; and
// a socket for an agent that is no longer selected is never closed by a stale timer. Run against an isolated dev server.
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
            const RealWS = window.WebSocket;
            const SPEED = 100, asked = [];
            x.terminalHooks.schedule = (fn, ms) => { asked.push(ms); setTimeout(fn, ms / SPEED); };
            const socks = [];
            let speak = false;
            window.WebSocket = class extends EventTarget {
                static get OPEN() { return 1; }
                constructor(url) { super(); this.url = url; this.readyState = 0; this.closedByClient = false; socks.push(this);
                    setTimeout(() => { this.readyState = 1; if (this.onopen) this.onopen();
                        if (speak && this.onmessage) this.onmessage({ data: JSON.stringify({ type: 'terminal_hello', input_acks: true }) }); }, 5); }
                close() { this.closedByClient = true; if (this.readyState !== 3) { this.readyState = 3; setTimeout(() => this.onclose && this.onclose({ code: 1005, reason: '' }), 0); } }
                send() {}
            };
            const waitFor = (cond, what, ms = 4000) => new Promise((resolve, reject) => {
                const end = performance.now() + ms;
                const w = () => cond() ? resolve() : performance.now() > end ? reject(new Error('timed out waiting for ' + what + ' (sockets ' + socks.length + ', asked ' + JSON.stringify(asked) + ')')) : setTimeout(w, 5);
                w();
            });
            const start = id => { state.currentSession = { type: 'live', session_id: id, name: id, tmux_session: id, agent_type: 'terminal' }; x.connectTerminalWs(id, 'terminal', id, ''); };
            const out = {};
            // 1. a socket that opens and stays silent is closed after SILENT_SOCKET_MS, and the terminal opens another one
            start('s-a');
            await waitFor(() => socks.length >= 2, 'a second socket after the first stayed silent');
            out.silentClosed = socks[0].closedByClient;
            out.silentAskedMs = asked.includes(x.SILENT_SOCKET_MS);
            out.reconnectDelay = asked.filter(ms => ms !== x.SILENT_SOCKET_MS)[0];
            // 2. a socket that speaks (its hello) is left alone for longer than the silent limit
            speak = true;
            const before = socks.length;
            start('s-b');
            await new Promise(r => setTimeout(r, 2 * x.SILENT_SOCKET_MS / SPEED + 100));
            out.talkingOpen = socks.length === before + 1 && !socks[socks.length - 1].closedByClient;
            // 3. another agent is selected before the timer fires: the old socket's timer closes nothing of the new one
            speak = false;
            start('s-c');
            const c = socks[socks.length - 1];
            await new Promise(r => setTimeout(r, 40));
            start('s-d');
            const d = socks[socks.length - 1];
            speak = true;
            await new Promise(r => setTimeout(r, x.SILENT_SOCKET_MS / SPEED + 100));
            out.staleTimerLeftNewAlone = c.closedByClient && !d.closedByClient;
            x.disconnectTerminalWs();
            window.WebSocket = RealWS;
            return out;
        })()`);
        assert.equal(out.silentClosed, true, 'the silent socket was closed by the client');
        assert.equal(out.silentAskedMs, true, 'the silent timer asked for SILENT_SOCKET_MS');
        assert.ok(out.reconnectDelay >= 1000 && out.reconnectDelay <= 2000, 'the reconnect backs off as for any failure: ' + out.reconnectDelay);
        assert.equal(out.talkingOpen, true, 'a socket that sent its hello was left open');
        assert.equal(out.staleTimerLeftNewAlone, true, 'a stale timer closes nothing of the newly selected agent\'s socket');
        console.log('PASS terminal silent socket: closed after the silent limit and reconnected; a talking socket is left; a stale timer touches nothing');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
