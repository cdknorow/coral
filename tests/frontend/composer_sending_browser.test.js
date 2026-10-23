// Chat describes Chat, and Sent means submitted (#4587, coral-hub's O2):
//   - the Chat view opens no terminal socket, so it shows none of the terminal's states: the bar under the terminal
//     (Connecting to terminal…, its size) is in the Terminal view only;
//   - the conversation says "Loading conversation…" while its first page loads, and "Could not load this conversation.
//     Reload the page to try again." when it fails;
//   - a message shows "Sending…" until its own request answers, and "Sent" only when that answer says it was
//     submitted; a refusal (not submitted, agent not ready) takes the bubble away and puts the text back in the box,
//     never "Sent"; a second click while it is sending sends nothing more;
//   - what a send is doing is said in one live region that stays in the page (#4680), its text changed in place: Sending message, Message sent,
//     Message not sent, your text is back in the box; the bubbles' own labels carry no role.
// The agent and its chat and send routes are mocked in the page (agent_view_harness.js); the rest is the isolated
// test server. Drives Chrome over CDP; run_cdp_suites.mjs finds CDP suites by the harness's name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

// The send and first-chat answers, held until the test lets them go: window.__hold.send / .chat are arrays of
// { resolve(status, body) }, and window.__hold.chatMode = 'hold' | 'fail' | 'pass'.
const HOLD = `(() => {
    const prev = window.fetch;
    const hold = window.__hold = { send: [], chatMode: 'pass', chat: [], sends: 0 };
    const json = (b, status) => new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } });
    window.fetch = (url, opts = {}) => {
        const u = String(url);
        if (/\\/api\\/sessions\\/live\\/cs-[^/]+\\/send$/.test(u) && (opts.method || 'GET').toUpperCase() === 'POST') {
            hold.sends++;
            return new Promise(res => hold.send.push({ resolve: (status, body) => res(json(body, status)) }));
        }
        if (/\\/api\\/sessions\\/live\\/cs-[^/]+\\/chat\\?/.test(u) && /[?&]after=0(&|$)/.test(u) && hold.chatMode !== 'pass') {
            if (hold.chatMode === 'fail') return Promise.resolve(json({ error: 'boom' }, 500));
            return new Promise(res => hold.chat.push(() => res(prev(url, opts))));
        }
        return prev(url, opts);
    };
})()`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    const pending = () => ev(`[...document.querySelectorAll('#live-history-messages .pending-messages .chat-bubble.pending')].map(b => ({ text: b.querySelector('.message-text').textContent.trim(), label: b.querySelector('.pending-label').textContent }))`);
    const sentToasts = () => ev(`[...document.querySelectorAll('.toast .toast-message')].map(t => t.textContent).filter(t => /^Sent: /.test(t))`);
    const type = text => ev(`(() => { const i = document.getElementById('command-input'); i.value = ${JSON.stringify(text)}; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
    const answer = (status, body) => ev(`(() => { const h = window.__hold.send.shift(); h.resolve(${status}, ${JSON.stringify(body)}); return true; })()`);
    const visible = sel => ev(`window.__vis(${JSON.stringify(sel)})`);
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: HOLD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('cs-a', 'A', 4, { sleeping: false }); window.__m.add('cs-b', 'B', 4, { sleeping: false }); true`);

        console.log('Chat view: the conversation\'s own states, none of the terminal\'s');
        await ev(`window.__hold.chatMode = 'hold'; true`);
        select('cs-a'); // not awaited: its first page is held
        await waitFor(`!!document.querySelector('#live-history-messages .loading-indicator')`, 'the loading line');
        assert.equal(await ev(`document.querySelector('#live-history-messages .loading-indicator').textContent`), 'Loading conversation…');
        assert.equal(await ev(`document.querySelector('#live-history-messages .loading-indicator').getAttribute('role')`), 'status');
        assert.equal(await visible('#terminal-statusbar'), false, 'no terminal bar in the Chat view');
        const words = await ev(`document.getElementById('live-session-view').innerText`);
        assert.doesNotMatch(words, /\bConnect(ing|ed)\b/, 'nothing in the agent view says Connecting or Connected: ' + JSON.stringify(words.match(/.{0,30}Connect.{0,30}/)));
        assert.equal(await ev(`document.getElementById('command-input').disabled`), false, 'the box takes a message while the conversation loads');
        await ev(`window.__hold.chatMode = 'pass'; window.__hold.chat.splice(0).forEach(f => f()); true`);
        await waitFor(`window.__rendered().length > 0`, 'the conversation');
        assert.equal(await ev(`!!document.querySelector('#live-history-messages .loading-indicator')`), false, 'the loading line goes');
        assert.equal(await visible('#terminal-statusbar'), false, 'still no terminal bar');

        console.log('Chat view: a conversation that fails to load says so');
        await ev(`window.__hold.chatMode = 'fail'; true`);
        await select('cs-b');
        await waitFor(`!!document.querySelector('#live-history-messages .chat-load-error')`, 'the failure line');
        assert.equal(await ev(`document.querySelector('#live-history-messages .chat-error-message').textContent`), 'Could not load this conversation. Reload the page to try again.');
        await ev(`window.__hold.chatMode = 'pass'; true`);

        console.log('Terminal view: its bar names the terminal');
        await ev(`window.setLiveViewMode('terminal'); true`);
        await waitFor(`window.__vis('#terminal-statusbar')`, 'the terminal bar');
        assert.match(await ev(`document.querySelector('#terminal-statusbar .terminal-statusbar-state-text').textContent`), /^(Connecting to terminal…|Terminal connected|Terminal disconnected)$/);
        await ev(`window.setLiveViewMode('chat'); true`);
        await waitFor(`!window.__vis('#terminal-statusbar')`, 'the bar to go with the terminal');

        console.log('A message: Sending… until its request answers, Sent only on a submitted answer');
        await select('cs-a');
        await waitFor(`window.__rendered().length > 0`, 'the conversation');
        const REGION = `document.getElementById('chat-send-status')`;
        assert.deepEqual(await ev(`({ n: document.querySelectorAll('#chat-send-status').length, role: ${REGION}.getAttribute('role'), live: ${REGION}.getAttribute('aria-live'), text: ${REGION}.textContent })`), { n: 1, role: 'status', live: 'polite', text: '' }, 'the send-state region is in the page before any send, empty');
        await ev(`window.__sendRegion = ${REGION}; true`);
        const labelRoles = () => ev(`[...document.querySelectorAll('#live-history-messages .pending-label')].map(l => l.getAttribute('role'))`);
        await type('first message');
        await ev(`window.sendCommand(); true`);
        await waitFor(`window.__hold.send.length === 1`, 'the send request');
        await waitFor(`document.querySelectorAll('#live-history-messages .pending-sending .chat-bubble').length === 1`, 'the Sending bubble');
        assert.deepEqual(await pending(), [{ text: 'first message', label: 'Sending…' }]);
        assert.equal(await ev(`${REGION}.textContent`), 'Sending message', 'the region says it is sending');
        assert.deepEqual(await labelRoles(), [null], 'the bubble label has no role of its own');
        assert.equal(await ev(`document.getElementById('command-input').value`), '', 'the box is emptied while it sends');
        await ev(`window.sendCommand({ nudge: true }); true`); // a second click while it sends
        await ev(`new Promise(r => setTimeout(r, 200))`); // sleep-ok: a second request, if any, would be made within this
        assert.equal(await ev(`window.__hold.sends`), 1, 'a second click sends nothing more');
        assert.deepEqual(await sentToasts(), [], 'never Sent while sending');
        await answer(200, { ok: true });
        await waitFor(`document.querySelectorAll('#live-history-messages .pending-sent .chat-bubble').length === 1`, 'the Sent bubble');
        assert.deepEqual(await pending(), [{ text: 'first message', label: 'Sent' }]);
        assert.equal(await ev(`${REGION}.textContent`), 'Message sent', 'the region says it was sent');
        assert.equal(await ev(`${REGION} === window.__sendRegion`), true, 'the same element: its text changed in place');
        assert.deepEqual(await labelRoles(), [null], 'still no role on the label');

        console.log('A message not submitted: no Sent, the text back in the box, the reason under it');
        await type('second message');
        await ev(`window.sendCommand(); true`);
        await waitFor(`window.__hold.send.length === 1`, 'the second send');
        assert.deepEqual((await pending()).map(p => p.label), ['Sent', 'Sending…']);
        await answer(409, { error: "Your message is in the agent's input box but wasn't submitted. Send it again to submit it, or press Enter in its terminal.", code: 'not_submitted', not_sent: false });
        await waitFor(`document.getElementById('command-input').value === 'second message'`, 'the text back');
        assert.deepEqual(await pending(), [{ text: 'first message', label: 'Sent' }], 'its bubble is gone');
        // one failure, one announcement (#6213): the status says it, or the composer's own alert does when that is on screen (a moment later)
        await waitFor(`${REGION}.textContent === 'Message not sent, your text is back in the box' || !document.getElementById('composer-error').hidden`, 'the failure is said', 3000);
        assert.equal(await ev(`${REGION} === window.__sendRegion`), true, 'the same element again');
        assert.equal(await ev(`document.getElementById('composer-error').hidden`), false, 'the reason is shown');
        assert.match(await ev(`document.getElementById('composer-error').textContent`), /wasn't submitted/);
        assert.deepEqual(await sentToasts(), ['Sent: first message'], 'no Sent for it');

        console.log('An agent not ready: the same');
        await type('third message');
        await ev(`window.sendCommand(); true`);
        await waitFor(`window.__hold.send.length === 1`, 'the third send');
        await answer(409, { error: "The agent isn't ready for messages yet: your message was not sent. Try again in a moment.", code: 'agent_not_ready', not_sent: true });
        await waitFor(`document.getElementById('command-input').value === 'third message'`, 'the text back');
        assert.deepEqual((await pending()).map(p => p.text), ['first message']);

        console.log('A reload while a message is sending: it does not come back as Sent');
        await ev(`window.sendCommand(); true`);
        await waitFor(`window.__hold.send.length === 1`, 'the fourth send');
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`window.__m.add('cs-a', 'A', 4, { sleeping: false }); true`);
        await select('cs-a');
        await waitFor(`window.__rendered().length > 0`, 'the conversation after the reload');
        assert.deepEqual((await pending()).filter(p => p.text === 'third message'), [], 'the unconfirmed send is not shown as sent');
        console.log('\nPASS composer sending: Chat states its own state; Sending… until submitted; refusals give the text back');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
