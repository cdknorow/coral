// The terminal socket answers input (#3276): the server acknowledges each terminal_input that
// carries an id, so a composer send is settled on the answer:
//   - accepted: "Sent" and the saved draft cleared as soon as both frames (text, Enter)
//     are acknowledged, well before the old window;
//   - refused by a gone terminal: terminal_gone names the input's id; the draft comes back;
//   - refused by a read-only Coral Hub (hub_read_only, the input dropped): the draft comes back.
// Real isolated server and real terminal agents. The page's terminal socket is wrapped
// only to observe frames, or to act as a read-only hub.
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

async function httpJson(method, path, body) {
    const r = await fetch(BASE + path, { method, headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
    let data = null; try { data = await r.json(); } catch { /* no body */ }
    return { status: r.status, data };
}

// window.__in: text frames received (parsed); window.__out: input frames sent (parsed).
// __readOnlyHub: drop terminal_input and
// answer hub_read_only, as a read-only Coral Hub does.
const WRAP = `(function(){ const Real = window.WebSocket; window.__in = []; window.__out = []; window.__readOnlyHub = false;
  function W(u, p) { const s = p === undefined ? new Real(u) : new Real(u, p);
    if (!/\\/ws\\/terminal/.test(String(u))) return s;
    const send = s.send.bind(s);
    s.send = d => { let m = null; try { m = JSON.parse(d); } catch {}
      if (m && m.type === 'terminal_input') { window.__out.push(m);
        if (window.__readOnlyHub) { setTimeout(() => s.dispatchEvent(new MessageEvent('message', { data: JSON.stringify({ type: 'hub_read_only', mode: 'view', error: 'View only' }) })), 20); return; } }
      return send(d); };
    let handler = null;
    Object.defineProperty(s, 'onmessage', { configurable: true, get: () => handler, set: f => { handler = f; } });
    s.addEventListener('message', e => {
      if (typeof e.data === 'string') { let m = null; try { m = JSON.parse(e.data); } catch {}
        if (m) window.__in.push({ ...m, at: performance.now() }); }
      if (handler) handler.call(s, e); });
    return s; }
  W.prototype = Real.prototype; W.CONNECTING = 0; W.OPEN = 1; W.CLOSING = 2; W.CLOSED = 3; window.WebSocket = W; })();`;

async function run() {
    const launched = [];
    const client = await CDP({ port: CDP_PORT });
    const { Page, Runtime } = client;
    await Promise.all([Page.enable(), Runtime.enable()]);
    await Page.addScriptToEvaluateOnNewDocument({ source: WRAP });
    const exceptions = [];
    Runtime.exceptionThrown(e => exceptions.push((((e.exceptionDetails || {}).exception || {}).description || 'exception').slice(0, 200)));
    const ev = async (x) => { const r = await Runtime.evaluate({ expression: x, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 300)); return r.result.value; };
    const { waitFor } = require('./helpers/wait').bind(ev);
    const until = (expression, what, timeout = 8000) => waitFor(expression, what, { soft: true, timeout });
    const XT = `(await import((window.__coralStatic||'/static/')+'xterm_renderer.js'))`;
    const ST = `(await import((window.__coralStatic||'/static/')+'state.js'))`;

    const launch = async (name) => {
        const r = await httpJson('POST', '/api/sessions/launch', { working_dir: '/tmp', agent_type: 'terminal', display_name: name });
        if (!r.data || !r.data.session_id) throw new Error(`launch ${name}: ${r.status} ${JSON.stringify(r.data)}`);
        launched.push(r.data);
        return r.data;
    };
    const attach = async (s) => {
        await until(`(window._coralGetLiveSessions ? window._coralGetLiveSessions() : []).some(x => x.session_id === ${JSON.stringify(s.session_id)})`, `${s.session_name} listed`);
        await ev(`window.__in = []; window.selectLiveSession(${JSON.stringify(s.session_name)}, 'terminal', ${JSON.stringify(s.session_id)}, ''); true`);
        await until(`(async () => ${XT}.sendTerminalInputWs(''))()`, `${s.session_name}: socket open`);
        await until(`(async () => ${XT}.terminalInputAcks())()`, `${s.session_name}: the socket takes input`);
    };
    const type = text => ev(`(() => { const i = document.getElementById('command-input'); i.value = ${JSON.stringify(text)}; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
    const savedDraft = () => ev(`(async () => { const m = ${ST}; return m.state.sessionInputText[m.sessionKey(m.state.currentSession)] || null; })()`);
    const sentToastAt = text => ev(`window.__sentAt && window.__sentAt[${JSON.stringify(text)}] || null`);
    const clearToasts = () => ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); true`);
    // Record when each "Sent: …" toast appears.
    const watchToasts = () => ev(`(() => { window.__sentAt = {}; new MutationObserver(() => document.querySelectorAll('.toast .toast-message').forEach(t => {
        const m = /^Sent: (.*)$/.exec(t.textContent); if (m && !window.__sentAt[m[1]]) window.__sentAt[m[1]] = performance.now(); })).observe(document.body, { childList: true, subtree: true }); return true; })()`);
    const send = async text => { await clearToasts(); await type(text); const t0 = await ev('performance.now()'); await ev(`window.sendCommand()`); return t0; };

    try {
        const live = await launch('Ack Live');
        const gone = await launch('Ack Gone');
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await until(`typeof window.selectLiveSession === 'function' && typeof window._coralGetLiveSessions === 'function'`, 'the app hooks', 10000);
        await ev(`localStorage.setItem('coral-live-view-mode', 'terminal'); true`);
        await watchToasts();

        // ── Accepted on the acks: Sent well before the old window ──
        await attach(live);
        await ev(`window.__out = []; true`);
        let t0 = await send('echo ack-fast');
        await until(`!!(window.__sentAt || {})['echo ack-fast']`, 'Sent', 4000);
        const sentAt = await sentToastAt('echo ack-fast');
        const out = await ev(`window.__out.slice()`);
        const acks = await ev(`window.__in.filter(m => m.type === 'terminal_input_ack').map(m => m.id)`);
        check('accepted: text and Enter carry ids, both acknowledged', out.length === 2 && out.every(m => Number.isInteger(m.id)) && out.every(m => acks.includes(m.id)), JSON.stringify({ out, acks }));
        // "Sent" comes from the acks, not from the old 1.2 s window: it appears no earlier than the last ack (the
        // window would fire on its own, before slow acks) and right after it. Relative to the acks, not a fixed
        // bound from the send: under load the acks themselves can take over a second (read 1.3 s on a saturated box).
        const lastAckAt = await ev(`Math.max(...window.__in.filter(m => m.type === 'terminal_input_ack').map(m => m.at))`);
        check('accepted: "Sent" on the acks (after the last ack, at once), not on the old window', sentAt !== null && sentAt >= lastAckAt - 5 && sentAt - lastAckAt < 500,
            sentAt === null ? 'no Sent' : `${Math.round(sentAt - lastAckAt)} ms after the last ack, ${Math.round(sentAt - t0)} ms after the send`);
        check('accepted: the saved draft is cleared', (await savedDraft()) === null, await savedDraft());

        // ── Refused by a read-only Coral Hub: the input is dropped, hub_read_only answers ──
        await ev(`window.__readOnlyHub = true; true`);
        await send('echo read-only');
        await until(`document.getElementById('command-input').value === 'echo read-only'`, 'the draft back', 4000);
        check('hub read-only: refused at once, draft back in the box and saved, never "Sent"', (await ev(`document.getElementById('command-input').value`)) === 'echo read-only' && (await savedDraft()) === 'echo read-only' && !(await sentToastAt('echo read-only')), JSON.stringify({ saved: await savedDraft() }));
        await ev(`window.__readOnlyHub = false; true`);
        // The read-only lock clears once the hub is out of the way (reload the agent).
        await ev(`(async () => { const m = ${ST}; if (m.state.currentSession) m.state.currentSession.hubReadOnly = null; return true; })()`);

        // ── Refused by a gone terminal: terminal_gone names the input's id ──
        await attach(gone);
        await ev(`(async () => { const x = ${XT}; x.sendTerminalInputWs('exit'); await new Promise(r => setTimeout(r, 300)); return x.sendTerminalInputWs('\\r'); })()`); // sleep-ok: the composer's own text-then-Enter gap, so the shell runs exit
        // Wait for the pane to be gone, not a fixed time: the stream ends a moment after (on pty, 300 ms after the process), and the input has to reach the server inside that window.
        const paneGone = async () => (await httpJson('POST', `/api/sessions/live/${encodeURIComponent(gone.session_name)}/resize`, { columns: 80, agent_type: 'terminal', session_id: gone.session_id })).status === 409;
        for (const end = Date.now() + 8000; !(await paneGone()) && Date.now() < end; await new Promise(r => setTimeout(r, 50))); // sleep-ok: poll interval of a condition wait
        await ev(`window.__out = []; window.__in = []; true`);
        await send('echo to-gone');
        await until(`window.__in.some(m => m.type === 'terminal_gone')`, 'terminal_gone');
        const goneFrame = await ev(`window.__in.find(m => m.type === 'terminal_gone') || null`);
        const textId = await ev(`(window.__out[0] || {}).id`);
        check('gone: terminal_gone carries the id of the refused input', !!goneFrame && goneFrame.id === textId, JSON.stringify({ goneFrame, textId }));
        await until(`document.getElementById('command-input').value === 'echo to-gone'`, 'the draft back', 4000);
        check('gone: draft back and saved, never "Sent", the line under the composer', (await savedDraft()) === 'echo to-gone' && !(await sentToastAt('echo to-gone')) && !(await ev(`document.getElementById('composer-error').hidden`)), JSON.stringify({ saved: await savedDraft() }));
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
