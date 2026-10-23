// The phone chat at 200% text (#6737, the phone chat audit #6712 findings 4 and 5): the Claude sign-in note is one short line on a phone with what is waiting and what to do behind its button
// (it was four lines at 200% on a 360 px phone), and the queued messages are one line each with a count when there is more than one (two queued messages took 103 px at 100%, half the screen at 200%).
// Drives Chrome over CDP through agent_view_harness.js with its mocked agents; the sign-in status is the page's own, with claude_signed_in false added by the page's fetch.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const SIGNED_OUT = `(() => { const R = window.fetch; window.fetch = async function (u, o) { const resp = await R.apply(this, arguments); let p = ''; try { p = new URL(String(u && u.url || u), location.origin).pathname; } catch { /* not a URL */ }
    if (p !== '/api/system/status' || !resp.ok) return resp; const d = await resp.clone().json(); d.claude_signed_in = false; return new Response(JSON.stringify(d), { status: 200, headers: { 'content-type': 'application/json' } }); }; })()`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: layout settles after the text size changes
    const S = `(window.__coralStatic || '/static/')`;
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await Page.addScriptToEvaluateOnNewDocument({ source: SIGNED_OUT });
        await Emulation.setDeviceMetricsOverride({ width: 360, height: 800, deviceScaleFactor: 2, mobile: true }); await Emulation.setTouchEmulationEnabled({ enabled: true });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'liveListLoaded', 'settings')`);
        await ev(`window.__m.add('rep', 'R', 8, { sleeping: false }); window._coralSetLiveSessions(Object.values(window.__m.agents).map(a => window.__m.entry(a))); localStorage.setItem('coral-live-view-mode', 'chat'); true`);
        await select('rep');
        await waitFor(`window.__rendered && window.__rendered().length >= 5`, 'the chat', 15000);
        const lineH = () => ev(`(() => { const b = document.getElementById('signin-line-btn'); const r = b.getBoundingClientRect(), fs = parseFloat(getComputedStyle(b).fontSize); return { shown: !document.getElementById('claude-signin-line').hidden && r.height > 0, h: r.height, fs, lines: Math.round(r.height / (fs * 1.2)), text: b.innerText.replace(/\\s+/g, ' ').trim() }; })()`);
        // a queued message is only queued while the agent is busy
        await ev(`import(${S} + 'state.js').then(st => { const r = st.state.liveSessions.find(s => s.session_id === 'rep-id'); r.working = true; r.awaiting_user = false; return true; })`);
        const queue = n => ev(`import(${S} + 'pending_messages.js').then(m => { for (let i = 0; i < ${n}; i++) m.addPendingMessage('rep-id', 'Then also update the docs and the changelog for the release, number ' + i + ', and tell me when it is done', Date.now() + i); const w = document.querySelector('#live-history-messages .pending-queued'); const r = w.getBoundingClientRect(); const items = [...w.querySelectorAll('.chat-bubble.pending')].map(b => b.getBoundingClientRect().height); return { h: r.height, items, count: w.dataset.count || '', before: getComputedStyle(w, '::before').content }; })`);
        for (const [pct, fs] of [[100, 16], [200, 32]]) {
            await ev(`document.documentElement.style.fontSize = '${fs}px'; true`); await sleep(400); // sleep-ok: layout settles after the text size changes
            await ev(`import(${S} + 'signin_line.js').then(m => { m.setClaudeSignedIn(false); return true; })`);
            await waitFor(`!document.getElementById('claude-signin-line').hidden`, `${pct}%: the sign-in note`, 20000);
            const s = await lineH();
            assert.ok(s.shown, `${pct}%: the note is shown`);
            assert.ok(!/scheduled jobs/.test(s.text), `${pct}%: its button says only that Claude is not signed in; what waits is behind it (${s.text})`);
            assert.ok(s.lines <= (pct === 100 ? 1 : 2), `${pct}%: the note takes ${pct === 100 ? 'one line' : 'at most two lines'} (${s.lines} lines, ${s.h} px)`);
            await ev(`document.getElementById('signin-line-btn').click(); true`);
            const hint = await ev(`(() => { const h = document.getElementById('signin-line-hint'); return { shown: !h.hidden, text: h.innerText.replace(/\\s+/g, ' ').trim() }; })()`);
            assert.ok(hint.shown && /scheduled jobs and workflows that use it are waiting/i.test(hint.text) && /Sign in with claude in a terminal/.test(hint.text), `${pct}%: its button opens what is waiting and what to do (${hint.text})`);
            await ev(`document.getElementById('signin-line-btn').click(); true`);
        }
        await ev(`document.documentElement.style.fontSize = '16px'; true`); await sleep(300); // sleep-ok: layout settles after the text size changes
        const one = await queue(1);
        assert.ok(one.items[0] <= 48, `one queued message is one line (${one.items[0]} px)`);
        const two = await queue(1);
        assert.ok(two.items.every(h => h <= 48), `each queued message is one line (${JSON.stringify(two.items)})`);
        assert.ok(two.h <= 140, `two queued messages with their count take little of the screen (${two.h} px)`);
        assert.ok(/2/.test(two.count || two.before), `with more than one, the count is said (${JSON.stringify([two.count, two.before])})`);
        await ev(`document.documentElement.style.fontSize = '32px'; true`); await sleep(400); // sleep-ok: layout settles after the text size changes
        const big = await ev(`(() => { const w = document.querySelector('#live-history-messages .pending-queued'); return { h: w.getBoundingClientRect().height, items: [...w.querySelectorAll('.chat-bubble.pending')].map(b => b.getBoundingClientRect().height) }; })()`);
        assert.ok(big.items.every(h => h <= 72), `at 200% each queued message is one line (${JSON.stringify(big.items)})`);
        assert.ok(big.h <= 160, `at 200% two queued messages with their count take under 160 px (${big.h} px; at 100% they took 103 px before and the pane is only 182 px)`);
        console.log('PASS phone sign-in note and queued messages at 100% and 200% text');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
