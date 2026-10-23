// A message held behind the agent's question shows as queued under the composer (#6611, the dashboard half of #6610): "Queued: <first words>. Sends after you answer the question above." with Cancel,
// one line per held message, the box still usable. The line goes when the message is typed (ack), refused (its reason shows) or cancelled (the text comes back into the box and the refusal that follows
// is not shown as an error); a failed cancel leaves the line; the live row's own list (held_messages) draws the same after a reload. At 1280 and 390 px. The server side is human_send_hold_test.go.
// Drives Chrome over CDP through agent_view_harness.js against the isolated dev server; the held frames are the page's own events, and the cancel request is answered by a stand-in fetch.
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const line = () => ev(`(() => { const b = document.getElementById('composer-queued'); const items = [...b.querySelectorAll('.composer-queued-item')];
        return { shown: !b.hidden && b.getClientRects().length > 0, texts: items.map(i => i.querySelector('.composer-queued-text').textContent), cancels: items.map(i => i.querySelector('button').getAttribute('aria-label')), box: document.getElementById('command-input').value,
            usable: !document.getElementById('command-input').disabled, err: (() => { const e = document.getElementById('composer-error'); return e && !e.hidden ? e.querySelector('.composer-error-text').textContent : ''; })() }; })()`);
    const emit = (type, detail) => ev(`document.dispatchEvent(new CustomEvent(${JSON.stringify(type)}, { detail: ${JSON.stringify(detail)} })); true`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 800, false], [390, 844, true]]) {
            const tag = `${w}px`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`(() => { const inner = window.__innerFetch || (window.__innerFetch = window.fetch); window.__calls = []; window.__cancelStatus = 200;
                window.fetch = (url, opts = {}) => { const u = String(url); if (/\\/api\\/sessions\\/live\\/hq\\/held\\//.test(u)) { window.__calls.push([(opts.method || 'GET'), u.replace(/^https?:\\/\\/[^/]+/, '')]); return Promise.resolve(new Response('{}', { status: window.__cancelStatus, headers: { 'content-type': 'application/json' } })); } return inner(url, opts); }; return true; })()`);
            await ev(`window.__m.add('hq', 'A', 0, { sleeping: false, agent_type: 'claude', session_id: 'hq-id' }); true`);
            await sleep(500); // sleep-ok: the first live list renders from several answers; no single completion event
            await ev(`window.selectLiveSession('hq', 'claude', 'hq-id', 'local'); true`);
            await waitFor(`!!document.getElementById('composer-queued') && !!window.__qaState.liveSessions.find(s => s.session_id === 'hq-id')`, 'the composer and the agent', 8000);
            // a send as the composer makes it: remembered, waiting for its answer (awaitSocketAccept), then answered "held"
            const send = async (id, text) => { await ev(`Promise.all([import(${S} + 'held_sends.js'), import(${S} + 'terminal_gone.js')]).then(([h, g]) => { h.rememberSend('hq-id', ${id}, ${JSON.stringify(text)}); g.awaitSocketAccept({ session_id: 'hq-id' }, [${id}]); return true; })`); await emit('coral:terminal-input-held', { sessionId: 'hq-id', id, heldId: 100 + id }); }; // the server's own id for the message is 100 + the composer's frame id here
            await ev(`document.getElementById('command-input').value = ''; true`);

            console.log(`${tag}: a held message is queued under the box`);
            let l = await line();
            check(`${tag}: nothing is queued at first`, !l.shown && l.texts.length === 0, JSON.stringify(l));
            await send(7, 'Run the tests please');
            await waitFor(`!document.getElementById('composer-queued').hidden`, 'the queued line', 4000);
            l = await line();
            check(`${tag}: the line says what is queued and when it sends, with a named Cancel, and the box is usable`, l.shown && l.texts.join() === 'Queued: “Run the tests please”. Sends after you answer the question above.' && l.cancels.join() === 'Cancel the queued message: Run the tests please' && l.usable, JSON.stringify(l));
            await send(8, 'x'.repeat(100));
            l = await line();
            const g = await ev(`(() => { const b = document.getElementById('composer-queued'); return { head: (b.querySelector('.composer-queued-head') || {}).textContent, words: b.textContent.split('Sends after you answer').length - 1, rows: [...b.querySelectorAll('.composer-queued-item')].map(r => ({ text: r.querySelector('.composer-queued-text').textContent, oneLine: r.getBoundingClientRect().height < 50, cancelLast: r.lastElementChild.tagName === 'BUTTON' })) }; })()`);
            check(`${tag}: several held messages say when they send once, above them, and a long one is cut to about 40 characters on one line with its Cancel at the end`, g.head === 'Queued. Sends after you answer the question above:' && g.words === 1 && g.rows.length === 2 && g.rows[1].text.includes('…') && g.rows[1].text.length < 50 && g.rows.every(r => r.oneLine && r.cancelLast), JSON.stringify(g));

            console.log(`${tag}: typed, refused, cancelled`);
            await emit('coral:terminal-input-ack', { sessionId: 'hq-id', id: 8 });
            l = await line();
            check(`${tag}: the message that is typed (ack) leaves the list, the other stays and says it all on its own line`, l.texts.length === 1 && /Run the tests/.test(l.texts[0]) && /Sends after you answer the question above/.test(l.texts[0]), JSON.stringify(l.texts));
            await emit('coral:terminal-input-refused', { sessionId: 'hq-id', id: 7, error: 'The agent session ended, so your message was not sent.' });
            l = await line();
            check(`${tag}: a refusal takes the line away and its reason shows under the box`, !l.shown && /session ended/.test(l.err), JSON.stringify(l));
            await ev(`document.querySelector('#composer-error .composer-error-dismiss')?.click(); true`);

            await send(9, 'Please cancel me');
            await ev(`document.querySelector('#composer-queued .composer-queued-cancel').click(); true`);
            await waitFor(`document.getElementById('composer-queued').hidden`, 'the line to go after Cancel', 4000);
            await emit('coral:terminal-input-refused', { sessionId: 'hq-id', id: 9, error: 'Cancelled: your message was not sent.' }); // what the server answers its own waiter
            await sleep(200); // sleep-ok: nothing to wait for: an error that must NOT appear
            l = await line();
            const calls = await ev(`window.__calls`);
            check(`${tag}: Cancel asks the server to drop it (DELETE .../held/<the server's id>), the text comes back into the box, and the refusal that follows is not an error`, JSON.stringify(calls) === JSON.stringify([['DELETE', '/api/sessions/live/hq/held/109']]) && l.box === 'Please cancel me' && !l.shown && l.err === '', JSON.stringify([calls, l]));

            console.log(`${tag}: a cancel that fails, and the live row's own list`);
            await ev(`document.getElementById('command-input').value = ''; window.__cancelStatus = 500; true`);
            await send(10, 'Stays queued');
            await ev(`document.querySelector('#composer-queued .composer-queued-cancel').click(); true`);
            await sleep(300); // sleep-ok: nothing to wait for: the line must STAY
            l = await line();
            check(`${tag}: a cancel the server refuses leaves the line (and says so)`, l.shown && l.texts.length === 1 && (await ev(`[...document.querySelectorAll('.toast')].some(t => /Could not cancel/.test(t.textContent))`)), JSON.stringify(l));
            await emit('coral:terminal-input-ack', { sessionId: 'hq-id', id: 10 });
            const rowWith = list => ev(`import(${S} + 'session_header_updates.js').then(m => { const row = { ...(window.__qaState.liveSessions.find(s => s.session_id === 'hq-id')), held_messages: ${JSON.stringify(list)} }; m.updateWaitingIndicator(row); return true; })`);
            await rowWith([{ id: 21, text: 'from the row' }]);
            l = await line();
            check(`${tag}: the live row's held_messages draw the same line (a reload or a second window)`, l.shown && l.texts.join() === 'Queued: “from the row”. Sends after you answer the question above.', JSON.stringify(l));
            await ev(`window.__cancelStatus = 200; true`);
            await rowWith([{ id: 21, text: 'from the row', sending: true }]);
            const sending = await ev(`(() => { const b = document.getElementById('composer-queued'); return { text: b.textContent, cancels: b.querySelectorAll('button').length }; })()`);
            check(`${tag}: a message the server is typing says so and has no Cancel`, /^Sending: “from the row”/.test(sending.text) && !/Sends after/.test(sending.text) && sending.cancels === 0, JSON.stringify(sending));
            await rowWith([{ id: 22, text: 'gone already', sending: false }]);
            await ev(`window.__cancelStatus = 404; document.getElementById('command-input').value = ''; document.querySelector('#composer-queued .composer-queued-cancel').click(); true`);
            await waitFor(`document.getElementById('composer-queued').hidden`, 'the line to go after a 404', 4000);
            l = await line();
            check(`${tag}: a Cancel the server answers "not held" (typed already) drops the line and gives no text back`, !l.shown && l.box === '', JSON.stringify(l));
            await rowWith([]);
            l = await line();
            check(`${tag}: and an empty list from the row clears it`, !l.shown && l.texts.length === 0, JSON.stringify(l));
        }
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} held-queue checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
