// Automated messages held for over a minute (#6473, the dashboard side of #6466): Coral holds an agent's board nudges and reminders while a prompt is open or its input box is not on screen (held_inputs on the live row). A few seconds
// is the gate working; once an agent has held for more than a minute (counted from when the page first saw it holding, or the row's held_since) its row says "N messages waiting: Coral can't see the input box" and the agent view says it
// with a Show terminal button; both go when the count is 0, and the clock starts over. Nothing is delivered from the page: there is no deliver-now button. A sleeping agent and a row that never held say nothing. At 1280 and 390 px.
// The minute is shortened by window.__coralHeldAfterMs here. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 800, false], [390, 844, true]]) {
            const tag = `${w}px`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            const mod = await ev(`import(${S} + 'held_inputs.js').then(m => ({ after: m.HELD_AFTER_MS, none: m.heldNotice({ session_id: 'x', held_inputs: 0 }), early: m.heldNotice({ session_id: 'y', held_inputs: 2 }), junk: m.heldCount({ held_inputs: 'abc' }), neg: m.heldCount({ held_inputs: -3 }), big: m.heldCount({ held_inputs: 5000 }) }))`);
            check(`${tag}: the minute is a minute; a row with nothing held, or only just holding, says nothing; a junk or negative count is 0`, mod.after === 60000 && mod.none === null && mod.early === null && mod.junk === 0 && mod.neg === 0 && mod.big === 999, JSON.stringify(mod));

            await ev(`window.__coralHeldAfterMs = 700; window.__m.add('held-agent', 'A', 3, { sleeping: false }); window.__m.add('quiet-agent', 'B', 3, { sleeping: false }); true`);
            await sleep(600); // sleep-ok: the first live list renders from several answers; no single completion event
            await ev(`window.selectLiveSession('held-agent', 'claude', 'held-agent-id', 'local'); true`);
            await sleep(500); // sleep-ok: a selection settles over frames
            const feed = held => ev(`(() => { const rows = window._coralGetLiveSessions().map(r => r.session_id === 'held-agent-id' ? { ...r, held_inputs: ${held} } : r); window._coralSetLiveSessions(rows);
                return import(${S} + 'render.js').then(m => { m.updateWaitingIndicator(rows.find(r => r.session_id === 'held-agent-id')); return true; }); })()`);
            const view = () => ev(`(() => { const n = document.getElementById('composer-held-notice'), b = n && n.querySelector('button');
                const row = document.querySelector('[data-session-id="held-agent-id"] .session-held-line, .session-item[data-name="held-agent"] .session-held-line, .session-held-line');
                const quiet = [...document.querySelectorAll('.session-held-line')].length;
                return { shown: !!n && !n.hidden && n.getClientRects().length > 0, text: n ? document.getElementById('composer-held-text').textContent : '', btn: b ? b.textContent : '', action: b ? b.dataset.onClick : '', row: row ? row.textContent : null, rowLines: quiet,
                    fits: document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1, deliver: !!document.querySelector('[data-on-click*="deliver"], [data-on-click*="release"]') }; })()`);

            await feed(2);
            let v = await view();
            check(`${tag}: just started holding: no notice yet`, !v.shown && v.row === null, JSON.stringify(v));
            await sleep(900); // sleep-ok: past the shortened minute
            await feed(2);
            await waitFor(`!!document.querySelector('.session-held-line')`, 'the row line', 6000);
            v = await view();
            check(`${tag}: held for over the minute: the row says "2 messages waiting: Coral can't see the input box"`, v.row === "2 messages waiting: Coral can't see the input box", JSON.stringify(v.row));
            check(`${tag}: the agent view says it too, with a Show terminal button; no deliver button anywhere`, v.shown && v.text === "2 messages waiting: Coral can't see the input box" && v.btn === 'Show terminal' && v.action === 'terminal-view-open' && !v.deliver, JSON.stringify(v));
            const boxW = await ev(`document.getElementById('command-input').getBoundingClientRect().width / document.querySelector('.command-pane-body').getBoundingClientRect().width`);
            check(`${tag}: the message box keeps its width beside the line (a phone put it in a column once)`, boxW >= 0.5, String(boxW));
            check(`${tag}: only the agent that holds has it (the other row has none) and nothing scrolls sideways`, v.rowLines === 1 && v.fits, JSON.stringify([v.rowLines, v.fits]));
            await feed(1);
            v = await view();
            check(`${tag}: one message is "1 message waiting"`, /^1 message waiting: /.test(v.text) && /^1 message waiting: /.test(v.row), JSON.stringify([v.text, v.row]));
            await ev(`window.__shown = false; window.showTerminalView = () => { window.__shown = true; }; true`);
            await ev(`document.querySelector('#composer-held-notice button').click(); true`);
            check(`${tag}: Show terminal opens the terminal view`, await ev('window.__shown'));
            await ev(`document.querySelector('#composer-held-notice button').dispatchEvent(new Event('x')); true`);

            await feed(0);
            v = await view();
            check(`${tag}: nothing held any more: both go`, !v.shown && v.row === null, JSON.stringify(v));
            await feed(3);
            v = await view();
            check(`${tag}: holding again starts the minute over (no notice at once)`, !v.shown && v.row === null, JSON.stringify(v));
            await ev(`window.__shot && 0; true`);
        }
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} held input checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
