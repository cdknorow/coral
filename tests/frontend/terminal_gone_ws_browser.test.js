// A real terminal socket says the agent's terminal is gone (#3218 v2).
//
// terminal_gone_browser.test.js covers the HTTP 409. This suite uses no mocks: it
// launches a real terminal agent on the isolated test server, selects it so the
// dashboard attaches its real /ws/terminal socket, ends the agent's shell, and
// sends from the composer over that socket. The server answers the input with a
// {type:'terminal_gone'} frame, and the dashboard must:
//   - say it under the composer with Restart, mark the row and the header (as for a 409);
//   - never say "Sent", withdraw the pending bubble, and keep the draft, both in
//     the box and in the saved drafts (a draft clears only once a send is accepted).
// A send to a live terminal is accepted: the saved draft clears and "Sent" shows.
//
// Expects CORAL_TEST_URL + CDP_PORT from tests/frontend/run.sh. Exit 0 / 1.

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const CDP_PORT = parseInt(process.env.CDP_PORT || '9222', 10);

const results = [];
function check(label, cond, detail) {
    const v = cond ? 'PASS' : 'FAIL';
    results.push(v);
    console.log(`[${v}] ${label}${detail !== undefined ? ' — ' + detail : ''}`);
}
const MESSAGE = "The agent's terminal is gone. Restart it to continue.";

async function httpJson(method, path, body) {
    const r = await fetch(BASE + path, { method, headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
    let data = null; try { data = await r.json(); } catch { /* no body */ }
    return { status: r.status, data };
}

async function run() {
    const launched = [];
    const client = await CDP({ port: CDP_PORT });
    const { Page, Runtime } = client;
    await Promise.all([Page.enable(), Runtime.enable()]);
    const exceptions = [];
    Runtime.exceptionThrown(e => exceptions.push((((e.exceptionDetails || {}).exception || {}).description || 'exception').slice(0, 200)));
    const ev = async (x) => { const r = await Runtime.evaluate({ expression: x, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 300)); return r.result.value; };
    const { waitFor } = require('./helpers/wait').bind(ev);
    const until = (expression, what, timeout = 8000) => waitFor(expression, what, { soft: true, timeout });

    const launch = async (name) => {
        const r = await httpJson('POST', '/api/sessions/launch', { working_dir: '/tmp', agent_type: 'terminal', display_name: name });
        if (!r.data || !r.data.session_id) throw new Error(`launch ${name}: ${r.status} ${JSON.stringify(r.data)}`);
        launched.push(r.data);
        return r.data;
    };
    const XT = `(await import((window.__coralStatic||'/static/')+'xterm_renderer.js'))`;
    const ST = `(await import((window.__coralStatic||'/static/')+'state.js'))`;
    // Selected, its terminal socket open (an empty input frame is ignored by the server).
    const attach = async (s) => {
        await until(`(window._coralGetLiveSessions ? window._coralGetLiveSessions() : []).some(x => x.session_id === ${JSON.stringify(s.session_id)})`, `${s.session_name} in the live list`);
        await ev(`window.selectLiveSession(${JSON.stringify(s.session_name)}, 'terminal', ${JSON.stringify(s.session_id)}, ''); true`);
        await until(`(async () => ${XT}.sendTerminalInputWs(''))()`, `${s.session_name}: terminal socket open`);
    };
    const type = text => ev(`(() => { const i = document.getElementById('command-input'); i.value = ${JSON.stringify(text)}; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
    const savedDraft = s => ev(`(async () => { const m = ${ST}; return m.state.sessionInputText[m.sessionKey(m.state.currentSession)] || null; })()`);
    const toasts = () => ev(`[...document.querySelectorAll('.toast .toast-message')].map(t => t.textContent)`);
    const clearToasts = () => ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); true`);
    const pendingBubbles = text => ev(`[...document.querySelectorAll('#live-history-messages .pending-sent, #live-history-messages .pending-queued')].filter(b => b.textContent.includes(${JSON.stringify(text)})).length`);
    // Frames the page's terminal socket received, recorded from the real socket.
    await Page.addScriptToEvaluateOnNewDocument({ source: `(function(){ const Real = window.WebSocket; window.__termFrames = [];
        function W(u, p) { const s = p === undefined ? new Real(u) : new Real(u, p);
            if (/\\/ws\\/terminal/.test(String(u))) window.__lastTermWs = s;
            if (/\\/ws\\/terminal/.test(String(u))) s.addEventListener('message', e => { if (typeof e.data === 'string') { try { const t = JSON.parse(e.data).type; window.__termFrames.push(t);
                } catch {} } });
            return s; }
        W.prototype = Real.prototype; W.CONNECTING = 0; W.OPEN = 1; W.CLOSING = 2; W.CLOSED = 3; window.WebSocket = W;
        // Probe inside the page's own handling of terminal_closed (it dispatches coral:terminal-closed
        // synchronously from the socket's message handler): the server's close frame follows
        // terminal_closed at once, so a probe one task later found the socket already closed under
        // load. Here the socket is still open, so it shows the page itself refuses to send.
        document.addEventListener('coral:terminal-closed', () => { if (window.__xt && !window.__closedProbe)
            window.__closedProbe = { open: !!window.__lastTermWs && window.__lastTermWs.readyState === 1, sent: window.__xt.sendTerminalInputWs('') }; }); })();` });

    try {
        const live = await launch('WS Live');
        const gone = await launch('WS Gone');
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await until(`typeof window.selectLiveSession === 'function' && typeof window._coralGetLiveSessions === 'function'`, 'the app hooks', 10000);
        await ev(`localStorage.setItem('coral-live-view-mode', 'terminal'); true`);

        // ── A send to a live terminal is accepted: the saved draft clears, "Sent" shows ──
        await attach(live);
        await clearToasts();
        await type('echo ws-accepted');
        check('live: typing saves the draft', (await savedDraft()) === 'echo ws-accepted', await savedDraft());
        await ev(`window.sendCommand()`);
        check('live: the box clears at once', (await ev(`document.getElementById('command-input').value`)) === '');
        check('live: the saved draft stays until the send is accepted', (await savedDraft()) === 'echo ws-accepted', await savedDraft());
        await until(`(async () => { const m = ${ST}; return !m.state.sessionInputText[m.sessionKey(m.state.currentSession)]; })()`, 'live: draft cleared on accept', 4000);
        check('live: accepted, the saved draft is cleared', (await savedDraft()) === null, await savedDraft());
        check('live: "Sent" only once accepted, no error line', (await toasts()).some(t => /^Sent: echo ws-accepted/.test(t)) && await ev(`document.getElementById('composer-error').hidden`), JSON.stringify(await toasts()));

        // ── The terminal goes away under an open socket ──
        // The shell's own exit ends the pane at once; the attach watchdog ends the stream about
        // 1.4 s later. A composer send in between reaches the server, which answers terminal_gone.
        await attach(gone);
        await ev(`(async () => { window.__xt = ${XT}; window.__termFrames = []; return true; })()`);
        await ev(`(async () => { const x = ${XT}; x.sendTerminalInputWs('exit'); await new Promise(r => setTimeout(r, 300)); return x.sendTerminalInputWs('\\r'); })()`); // sleep-ok: the composer's own text-then-Enter gap (bracketed paste), so the shell runs exit
        await new Promise(r => setTimeout(r, 300)); // sleep-ok: the pane is gone but the attach watchdog has not ended the stream; no event marks that window
        await clearToasts();
        await type('hello gone');
        const viaSocket = await ev(`(async () => ${XT}.sendTerminalInputWs(''))()`);
        await ev(`window.sendCommand()`);
        await until(`window.__termFrames.includes('terminal_gone')`, 'the real terminal_gone frame');
        // terminal_hello is the server's greeting after the replay seed, and terminal_size reports the pane's size
        // (#3422): under load either can land after the reset above, and neither answers the input, so they are not
        // what 'first' means here.
        const frames = await ev(`window.__termFrames.slice()`);
        check('the composer sent over the open socket and the server answered terminal_gone', viaSocket === true && frames.filter(t => t !== 'terminal_hello' && t !== 'terminal_size')[0] === 'terminal_gone', JSON.stringify({ viaSocket, frames }));
        await until(`!document.getElementById('composer-error').hidden`, 'the line under the composer');
        const box = await ev(`(() => { const b = document.getElementById('composer-error'); return { hidden: b.hidden, text: b.querySelector('.composer-error-text').textContent, restart: !b.querySelector('.composer-error-restart').hidden }; })()`);
        check('the line under the composer, with Restart (as for a 409)', !box.hidden && box.text === MESSAGE && box.restart, JSON.stringify(box));
        await until(`!!document.querySelector('.session-group-item[data-session-id="${gone.session_id}"] .terminal-gone-chip')`, 'the row chip');
        check('the row chip marks that agent only', await ev(`!!document.querySelector('.session-group-item[data-session-id="${gone.session_id}"] .terminal-gone-chip') && !document.querySelector('.session-group-item[data-session-id="${live.session_id}"] .terminal-gone-chip')`));
        check('the header badge', (await ev(`(document.getElementById('terminal-gone-header') || {}).textContent || null`)) === 'Terminal gone · Restart');
        await until(`document.getElementById('command-input').value === 'hello gone'`, 'the draft back in the box', 3000);
        check('refused: the draft is back in the box and still saved', (await ev(`document.getElementById('command-input').value`)) === 'hello gone' && (await savedDraft()) === 'hello gone', JSON.stringify({ box: await ev(`document.getElementById('command-input').value`), saved: await savedDraft() }));
        check('refused: never "Sent", no pending bubble left', !(await toasts()).some(t => /Sent/.test(t)) && (await pendingBubbles('hello gone')) === 0, JSON.stringify({ toasts: await toasts(), bubbles: await pendingBubbles('hello gone') }));
        await until(`window.__termFrames.includes('terminal_closed')`, 'terminal_closed follows', 5000);
        check('terminal_closed follows terminal_gone', (await ev(`window.__termFrames.indexOf('terminal_closed')`)) > (await ev(`window.__termFrames.indexOf('terminal_gone')`)), JSON.stringify(await ev(`window.__termFrames`)));
        const probe = await ev(`window.__closedProbe || null`);
        check('right after terminal_closed, with the socket still open, the page sends nothing into it', !!probe && probe.open === true && probe.sent === false, JSON.stringify(probe));
        // The accept window has passed: nothing clears the refused draft later.
        await new Promise(r => setTimeout(r, 1500)); // sleep-ok: proving a negative: nothing clears the refused draft once the accept window has passed
        check('the refused draft is not cleared after the accept window', (await savedDraft()) === 'hello gone' && (await ev(`document.getElementById('command-input').value`)) === 'hello gone');

        // ── After terminal_closed the socket may still be open, but the server reads nothing more:
        // the composer must not send into it. It falls back to HTTP, which answers 409 terminal_gone. ──
        await ev(`document.getElementById('composer-error').hidden = true; true`);
        await clearToasts();
        await type('after closed');
        const socketRefused = await ev(`(async () => ${XT}.sendTerminalInputWs('') === false)()`);
        await ev(`window.sendCommand()`);
        await until(`!document.getElementById('composer-error').hidden`, 'after closed: the line under the composer');
        const box2 = await ev(`(() => { const b = document.getElementById('composer-error'); return { hidden: b.hidden, text: b.querySelector('.composer-error-text').textContent }; })()`);
        check('after terminal_closed: no socket send; HTTP says the terminal is gone, the draft stays, never "Sent"', socketRefused && !box2.hidden && box2.text === MESSAGE && (await ev(`document.getElementById('command-input').value`)) === 'after closed' && (await savedDraft()) === 'after closed' && !(await toasts()).some(t => /Sent/.test(t)), JSON.stringify({ socketRefused, box2, toasts: await toasts() }));
        check('no page exceptions', exceptions.length === 0, JSON.stringify(exceptions));
    } finally {
        for (const s of launched) await httpJson('POST', `/api/sessions/live/${encodeURIComponent(s.session_name)}/kill`, { agent_type: 'terminal', session_id: s.session_id }).catch(() => {});
        await client.close();
    }
}

run().then(() => {
    const failed = results.filter(r => r === 'FAIL').length;
    console.log(`\n${results.length - failed}/${results.length} checks passed`);
    process.exit(failed ? 1 : 0);
}).catch(err => { console.error(err); process.exit(1); });
