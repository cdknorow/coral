// The agent view at 200% text (a 1280 px screen with the root font doubled, and a 640 px window) (#7065, UI/UX's #7059 findings 7 and 8): the sidebar's rows and count badges were cut, the search icon sat on top of its placeholder ("QSearch"),
// the header drew only a coloured dot (its state was a tooltip, or on a touch screen wider than a phone), and Stop was a live button on an agent that was doing nothing. Drives Chrome over CDP through
// agent_view_harness.js with its mocked agents.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: layout settles after the root font changes
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const W of [1280, 640]) {
            await Emulation.setDeviceMetricsOverride({ width: W, height: 900, deviceScaleFactor: 1, mobile: false }); await Emulation.setTouchEmulationEnabled({ enabled: false });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'liveListLoaded', 'settings')`);
            await ev(`document.documentElement.style.fontSize = '32px'; true`);
            await ev(`window.__m.add('rep', 'R', 24, { sleeping: false }); window.__m.add('nap', 'N', 6, { sleeping: true }); window._coralSetLiveSessions(Object.values(window.__m.agents).map(a => window.__m.entry(a))); localStorage.setItem('coral-live-view-mode', 'chat'); true`);
            await select('rep');
            await waitFor(`window.__rendered && window.__rendered().length >= 10`, 'the chat', 15000);
            await sleep(900); // sleep-ok: layout settles after the root font changes
            // 1. nothing in the sidebar's rows is cut: a row's text and badges are inside the row's box
            const cut = await ev(`(() => [...document.querySelectorAll('#live-sessions-list li')].flatMap(li => { const lr = li.getBoundingClientRect(); return [...li.querySelectorAll('.session-label, .session-group-count, .group-name, .session-label-name, .badge, .session-meta, .session-goal')].filter(e => e.getClientRects().length).filter(e => { const r = e.getBoundingClientRect(); return r.bottom > lr.bottom + 1 || r.top < lr.top - 1 || r.right > lr.right + 1 || e.scrollHeight > e.clientHeight + 2 && getComputedStyle(e).overflow !== 'visible'; }).map(e => (li.textContent.trim().slice(0, 20) + ' > ' + e.className + ' ' + Math.round(e.getBoundingClientRect().bottom - lr.bottom))); }))()`);
            assert.deepEqual(cut, [], `${W} px: a sidebar row has text outside its box: ` + JSON.stringify(cut));
            // 2. the search icon has its own room: the input's text starts after the icon
            const search = await ev(`(() => { const i = document.getElementById('agent-search-input'), ic = document.querySelector('.sidebar-search-icon'); return { icon: Math.round(ic.getBoundingClientRect().right), text: Math.round(i.getBoundingClientRect().left + parseFloat(getComputedStyle(i).paddingLeft)) }; })()`);
            assert.ok(search.text >= search.icon, `${W} px: the search text starts after its icon: ` + JSON.stringify(search));
            // 3. the header writes the state beside the dot, at any window and with a mouse
            const word = await ev(`(() => { const w = document.getElementById('terminal-state-word'); return { shown: w.getClientRects().length > 0, text: w.getClientRects().length ? getComputedStyle(w, '::before').content : '' }; })()`);
            if (W === 1280) assert.ok(word.shown && /Idle/i.test(word.text), `${W} px: the header says Idle beside the dot: ` + JSON.stringify(word));
            // 4. an idle agent has no live Stop
            const stop = await ev(`[...document.querySelectorAll('button')].filter(b => /^Stop/.test(b.getAttribute('aria-label') || '')).map(b => ({ shown: b.getClientRects().length > 0, disabled: b.disabled }))`);
            assert.ok(stop.every(s => !s.shown || s.disabled), `${W} px: Stop is hidden or off while the agent is idle: ` + JSON.stringify(stop));
            // and it is there while the agent works, asks or is stuck (the 640 px window is the phone layout, which has no Stop button of its own)
            if (W === 1280) for (const [st, want] of [[{ working: true }, true], [{ waiting_for_input: true }, true], [{ stuck: true }, true], [{ awaiting_user: true }, false]]) {
                const shown = await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'session_state.js'); m.applyTerminalDot(document.getElementById('terminal-status-dot'), ${JSON.stringify(st)}); return [...document.querySelectorAll('button')].filter(b => /^Stop/.test(b.getAttribute('aria-label') || '')).some(b => b.getClientRects().length > 0); })()`);
                assert.equal(shown, want, `${W} px: Stop ${want ? 'shown' : 'hidden'} for ${JSON.stringify(st)}`);
            }
        }
        console.log('PASS agent view large text');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
