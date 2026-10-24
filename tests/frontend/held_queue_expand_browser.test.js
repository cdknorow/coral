// A queued message clipped to its first words can be read whole before it sends (#6794, #6737 LOW 1): a tap (or Enter/Space) on the line opens it to the whole message, Cancel stays, a tap again
// closes it, and it closes when the message is sent or cancelled; a short message is plain text, not a control. At 390 px with touch (and 1280 px). The held frames are the page's own events, and the
// cancel request is answered by a stand-in fetch, as held_queue_browser does. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const line = () => ev(`(() => { const b = document.getElementById('composer-queued'); const items = [...b.querySelectorAll('.composer-queued-item')];
        return { hiddenAttr: b.hasAttribute('hidden'), shown: items.length > 0 && b.getClientRects().length > 0, head: (b.querySelector('.composer-queued-head') || {}).textContent || '', note: (b.querySelector('.composer-queued-note') || {}).textContent || '', zero: b.getBoundingClientRect().height <= 1 && getComputedStyle(b).position === 'absolute', texts: items.map(i => i.querySelector('.composer-queued-text').textContent), cancels: items.map(i => i.querySelector('button').getAttribute('aria-label')), box: document.getElementById('command-input').value,
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
            const LONG = "Please run the whole test suite twice, then tell me which tests failed and why, before you touch anything else.", FIRST = LONG.slice(0, 40) + '…';
            const row = id => ev(`(() => { const it = document.querySelector('#composer-queued .composer-queued-item[data-key="${id}"]'); if (!it) return null; const t = it.querySelector('.composer-queued-text'); const c = it.querySelector('.composer-queued-cancel'); const r = t.getBoundingClientRect();
                return { text: t.textContent, role: t.getAttribute('role'), expanded: t.getAttribute('aria-expanded'), tab: t.tabIndex, cancel: !!c, h: Math.round(r.height), w: Math.round(r.width) }; })()`);
            await send(21, 'Short one'); await send(22, LONG);
            await waitFor(`document.querySelectorAll('#composer-queued .composer-queued-item').length === 2`, 'two queued lines', 4000);
            let a = await row(121), b = await row(122);
            check(`${tag}: a short message is plain text, a long one is clipped to its first words and is a closed button`, a.role === null && a.expanded === null && b.text === '“' + FIRST + '”' && b.role === 'button' && b.expanded === 'false' && b.tab === 0, JSON.stringify([a, b]));
            if (mobile) check(`${tag}: the long line is a 44 px target on a phone`, b.h >= 44, String(b.h));
            await ev(`document.querySelector('#composer-queued .composer-queued-item[data-key="122"] .composer-queued-text').click(); true`);
            await waitFor(`document.querySelector('#composer-queued .composer-queued-item[data-key="122"] .composer-queued-text').textContent.includes('before you touch anything else')`, 'the whole message', 3000);
            b = await row(122);
            check(`${tag}: a tap opens it to the whole message, Cancel stays, and it says it is expanded`, b.text === '“' + LONG + '”' && b.expanded === 'true' && b.cancel, JSON.stringify(b));
            await ev(`document.querySelector('#composer-queued .composer-queued-item[data-key="122"] .composer-queued-text').click(); true`);
            await waitFor(`document.querySelector('#composer-queued .composer-queued-item[data-key="122"] .composer-queued-text').textContent.endsWith('…”')`, 'it closed again', 3000);
            b = await row(122);
            check(`${tag}: a second tap closes it`, b.text === '“' + FIRST + '”' && b.expanded === 'false', JSON.stringify(b));
            await ev(`(() => { const t = document.querySelector('#composer-queued .composer-queued-item[data-key="122"] .composer-queued-text'); t.focus(); t.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })); return true; })()`);
            await waitFor(`document.querySelector('#composer-queued .composer-queued-item[data-key="122"] .composer-queued-text').getAttribute('aria-expanded') === 'true'`, 'Enter to open it', 3000);
            check(`${tag}: Enter opens it too, and the box stays usable`, (await row(122)).text === '“' + LONG + '”' && await ev(`!document.getElementById('command-input').disabled`));
            // it is sent: the line goes; the same message queued again starts closed
            await emit('coral:terminal-input-ack', { sessionId: 'hq-id', id: 22 });
            await waitFor(`!document.querySelector('#composer-queued .composer-queued-item[data-key="122"]')`, 'the sent line to go', 4000);
            await send(22, LONG);
            await waitFor(`!!document.querySelector('#composer-queued .composer-queued-item[data-key="122"]')`, 'the message queued again', 4000);
            b = await row(122);
            check(`${tag}: once sent it is gone, and the same message queued again is closed`, b.expanded === 'false' && b.text === '“' + FIRST + '”', JSON.stringify(b));
            await ev(`document.querySelector('#composer-queued .composer-queued-item[data-key="122"] .composer-queued-text').click(); true`);
            await waitFor(`document.querySelector('#composer-queued .composer-queued-item[data-key="122"] .composer-queued-text').getAttribute('aria-expanded') === 'true'`, 'it open', 3000);
            await ev(`document.querySelector('#composer-queued .composer-queued-item[data-key="122"] .composer-queued-cancel').click(); true`);
            await waitFor(`!document.querySelector('#composer-queued .composer-queued-item[data-key="122"]')`, 'the line to go after Cancel', 4000);
            await emit('coral:terminal-input-refused', { sessionId: 'hq-id', id: 22, error: 'Cancelled: your message was not sent.' });
            await send(22, LONG);
            await waitFor(`!!document.querySelector('#composer-queued .composer-queued-item[data-key="122"]')`, 'queued after Cancel', 4000);
            check(`${tag}: and one cancelled while open is closed when it is queued again`, (await row(122)).expanded === 'false');
            await ev(`window.hideSettingsModal && window.hideSettingsModal(); document.querySelectorAll('#composer-error .composer-error-dismiss').forEach(x => x.click()); true`);
        }
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} queued-message expand checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
