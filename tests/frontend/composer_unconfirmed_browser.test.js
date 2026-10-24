// A message Coral can't confirm, though it may well have arrived, is not taken back (#318, QA on main's #6089):
//   - the server answers 409 with not_sent false (hooks_unreachable: the agent's hooks are gone and nothing confirms the message);
//     before, the composer dropped the bubble and put the text back, and a person who sent it again typed it a second time;
//   - now its bubble stays, labelled "Sent, but Coral can't confirm the agent received it: its hooks aren't running", the box stays
//     empty, the saved draft is cleared, the reason is under the box, the live region says the note, and there is no "Sent" toast;
//   - the same over the terminal socket (a composer_send refused with not_sent false), where a refusal with not_sent true still puts
//     the text back, and an ack is still Sent;
//   - an unconfirmed Coral panel request, a bubble of its own kind, carries the same note.
// The agent and its send route are mocked in the page (agent_view_harness.js); the terminal socket is a fake one opened for the
// agent on screen. Drives Chrome over CDP.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const NOTE = "Sent, but Coral can't confirm the agent received it: its hooks aren't running";
const GONE = { error: "Coral can't reach this agent's hooks, so it can't tell whether your message arrived. Check the agent's terminal. To fix this, reopen Coral or restart the agent.", code: 'hooks_unreachable', not_sent: false };

// The send answers, held until the test lets them go (as in composer_sending_browser), and a fake terminal socket.
const HOLD = `(() => {
    const prev = window.fetch;
    const hold = window.__hold = { send: [], sends: 0 };
    const json = (b, status) => new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } });
    window.fetch = (url, opts = {}) => {
        const u = String(url);
        if (/\\/api\\/sessions\\/live\\/cu-[^/]+\\/send$/.test(u) && (opts.method || 'GET').toUpperCase() === 'POST') {
            hold.sends++;
            return new Promise(res => hold.send.push({ resolve: (status, body) => res(json(body, status)) }));
        }
        return prev(url, opts);
    };
    window.__frames = [];
    class FakeWS { constructor(u) { this.url = String(u); this.readyState = 0; window.__ws = this; setTimeout(() => { if (this.readyState === 0) { this.readyState = 1; this.onopen && this.onopen({}); } }, 5); }
        send(d) { window.__frames.push(String(d)); } close() { this.readyState = 3; } addEventListener() {} removeEventListener() {} }
    FakeWS.CONNECTING = 0; FakeWS.OPEN = 1; FakeWS.CLOSING = 2; FakeWS.CLOSED = 3;
    const RealWS = window.WebSocket;
    window.WebSocket = function (u, p) { return /\\/ws\\/terminal\\//.test(String(u)) && window.__fakeTerminal ? new FakeWS(u) : (p === undefined ? new RealWS(u) : new RealWS(u, p)); };
    Object.assign(window.WebSocket, { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 }); window.WebSocket.prototype = RealWS.prototype;
})()`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    const pending = () => ev(`[...document.querySelectorAll('#live-history-messages .pending-messages .chat-bubble.pending')].map(b => ({ text: b.querySelector('.message-text').textContent.trim(), label: b.querySelector('.pending-label').textContent }))`);
    const sentToasts = () => ev(`[...document.querySelectorAll('.toast .toast-message')].map(t => t.textContent).filter(t => /^Sent: /.test(t))`);
    const type = text => ev(`(() => { const i = document.getElementById('command-input'); i.value = ${JSON.stringify(text)}; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
    const answer = (status, body) => ev(`(() => { const h = window.__hold.send.shift(); h.resolve(${status}, ${JSON.stringify(body)}); return true; })()`);
    const box = () => ev(`document.getElementById('command-input').value`);
    const draft = () => ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'state.js'); return m.state.sessionInputText[m.sessionKey(m.state.currentSession)] || ''; })()`);
    const region = () => ev(`document.getElementById('chat-send-status').textContent`);
    const composerError = () => ev(`(() => { const e = document.getElementById('composer-error'); return e.hidden ? '' : e.textContent; })()`);
    const settle = () => ev(`new Promise(r => setTimeout(r, 200))`); // sleep-ok: a text put back, if any, would be within this
    // What a composer_send frame was answered with: the server's frame, delivered to the page's terminal socket.
    const reply = frame => ev(`(() => { const f = window.__frames.map(x => JSON.parse(x)).filter(x => x.type === 'composer_send').pop();
        window.__ws.onmessage({ data: JSON.stringify(Object.assign(${JSON.stringify(frame)}, { id: f.id })) }); return f.data; })()`);
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: HOLD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('cu-a', 'A', 4, { sleeping: false }); true`);
        await select('cu-a');
        await waitFor(`window.__rendered().length > 0`, 'the conversation');

        console.log('1. POST /send answers 409 hooks_unreachable, not_sent false: the bubble stays with its note, the box stays empty');
        await type('deploy to staging');
        await ev(`window.sendCommand(); true`);
        await waitFor(`window.__hold.send.length === 1`, 'the send request');
        await answer(409, GONE);
        await waitFor(`document.querySelectorAll('#live-history-messages .pending-unconfirmed .chat-bubble').length === 1`, 'the unconfirmed bubble');
        await settle();
        assert.deepEqual(await pending(), [{ text: 'deploy to staging', label: NOTE }]);
        assert.equal(await box(), '', 'nothing is put back in the box: sending it again would type it twice');
        assert.equal(await draft(), '', 'nor in the saved draft');
        assert.match(await composerError(), /can't reach this agent's hooks/, 'the reason is under the box');
        assert.equal(await region(), NOTE, 'the live region says the note');
        assert.deepEqual(await sentToasts(), [], 'never a plain Sent');

        console.log('2. The terminal socket: a composer_send refused with not_sent false is kept the same way');
        await ev(`(async () => { window.__fakeTerminal = true; const x = await import((window.__coralStatic || '/static/') + 'xterm_renderer.js'); const s = (await import((window.__coralStatic || '/static/') + 'state.js')).state.currentSession;
            x.connectTerminalWs(s.tmux_session || s.name, s.agent_type, s.session_id, 'local'); return true; })()`);
        await waitFor(`window.__ws && window.__ws.readyState === 1`, 'the fake terminal socket open');
        await type('run the tests');
        await ev(`window.sendCommand(); true`);
        await waitFor(`window.__frames.some(f => JSON.parse(f).type === 'composer_send')`, 'the composer_send frame');
        assert.equal(await ev(`window.__hold.sends`), 1, 'over the socket, not POST');
        assert.equal(await reply({ type: 'terminal_input_failed', ...GONE }), 'run the tests');
        await waitFor(`document.querySelectorAll('#live-history-messages .pending-unconfirmed .chat-bubble').length === 2`, 'the second unconfirmed bubble');
        await settle();
        assert.deepEqual((await pending()).map(p => p.label), [NOTE, NOTE]);
        assert.equal(await box(), '', 'the box stays empty');
        assert.equal(await draft(), '', 'and the saved draft');
        assert.match(await composerError(), /can't reach this agent's hooks/);

        console.log('3. The socket: a refusal with not_sent true still puts the text back');
        await ev(`window.__frames.length = 0; true`);
        await type('not ready yet');
        await ev(`window.sendCommand(); true`);
        await waitFor(`window.__frames.some(f => JSON.parse(f).type === 'composer_send')`, 'the frame');
        await reply({ type: 'terminal_input_failed', error: "The agent isn't ready for messages yet: your message was not sent. Try again in a moment.", code: 'agent_not_ready', not_sent: true });
        await waitFor(`document.getElementById('command-input').value === 'not ready yet'`, 'the text back');
        assert.deepEqual((await pending()).map(p => p.text), ['deploy to staging', 'run the tests'], 'its bubble is gone');

        console.log('4. The socket: an ack is Sent, as before');
        await ev(`window.__frames.length = 0; true`);
        await type('all good');
        await ev(`window.sendCommand(); true`);
        await waitFor(`window.__frames.some(f => JSON.parse(f).type === 'composer_send')`, 'the frame');
        await reply({ type: 'terminal_input_ack' });
        await waitFor(`document.querySelectorAll('#live-history-messages .pending-sent .chat-bubble').length === 1`, 'the Sent bubble');
        assert.deepEqual(await sentToasts(), ['Sent: all good']);

        console.log('5. An unconfirmed Coral panel request (its own bubble kind) is labelled with the note too, never the label function');
        await ev(`window.__frames.length = 0; true`);
        await type('[Coral UI panel request] open the changes panel');
        await ev(`window.sendCommand(); true`);
        await waitFor(`window.__frames.some(f => JSON.parse(f).type === 'composer_send')`, 'the frame');
        await reply({ type: 'terminal_input_failed', ...GONE });
        await waitFor(`document.querySelectorAll('#live-history-messages .pending-unconfirmed .chat-system.pending').length === 1`, 'the unconfirmed Coral bubble');
        assert.equal(await ev(`document.querySelector('#live-history-messages .pending-unconfirmed .chat-system.pending .pending-label').textContent`), NOTE);
        console.log('\nPASS composer unconfirmed: a message that may have arrived keeps its bubble and is not put back in the box');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
