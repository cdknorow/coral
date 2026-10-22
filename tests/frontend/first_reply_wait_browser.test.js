// The wait for a first answer (#6058, from #6050's report): the Working row shows quiet digits from 5 s, "Still working" with a real terminal link from 30 s, one polite status at 30 s and then once a minute; a row that leaves the
// page stops its own timer. Time is moved by patching Date.now and calling tickWorking, so the suite does not wait.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [dev, w, h, mobile] of [['desktop', 1280, 800, false], ['phone', 390, 844, true]]) {
            console.log(dev);
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            const M = `import((window.__coralStatic || '/static/') + 'pending_messages.js')`;
            await ev(`(async () => {
                const m = await ${M}; const st = (await import((window.__coralStatic || '/static/') + 'state.js')).state;
                st.liveSessions = [{ session_id: 'frw-1', name: 'frw', working: true, agent_type: 'claude' }];
                const c = document.createElement('div'); c.id = 'frw-chat'; document.body.append(c);
                window.__frw = { m, c, real: Date.now, t0: Date.now() };
                m.syncWorkingIndicator(c, { session_id: 'frw-1' }); window.__frw.t0 = Number(c.querySelector('.chat-working').dataset.since); return true; })()`);
            const at = async secs => ev(`(() => { const f = window.__frw; Date.now = () => f.t0 + ${secs} * 1000; const el = f.c.querySelector('.chat-working'); f.m.tickWorking(el);
                const q = s => el.querySelector(s); const still = q('.chat-working-still'), link = q('.chat-working-link'); const r = link.getBoundingClientRect();
                return { time: q('.chat-working-time').textContent, timeHidden: q('.chat-working-time').getAttribute('aria-hidden'), live: q('.chat-working-live').textContent, stillShown: !still.hidden, label: q('.chat-working-label').textContent,
                    role: el.getAttribute('role'), liveRole: q('.chat-working-live').getAttribute('role'), link: link.tagName + ':' + link.textContent, linkBox: [Math.round(r.width), Math.round(r.height)] }; })()`);
            let s = await at(1);
            assert.deepEqual([s.label, s.time, s.live, s.stillShown, s.role, s.liveRole], ['Working', '', 'Working', false, null, 'status'], dev + ': at 1 s only the label; the one status is its own element: ' + JSON.stringify(s));
            s = await at(6); assert.equal(s.time, ' · 6 s', dev + ': digits from 5 s'); assert.equal(s.timeHidden, 'true', 'the digits are not read'); assert.equal(s.live, 'Working'); assert.equal(s.stillShown, false);
            s = await at(29); assert.deepEqual([s.live, s.stillShown], ['Working', false], dev + ': nothing yet at 29 s');
            s = await at(30); assert.deepEqual([s.live, s.stillShown, s.link], ['Still working, 30 seconds', true, 'BUTTON:Open the terminal'], dev + ': at 30 s the line, the link and one status: ' + JSON.stringify(s));
            if (mobile) assert.ok(s.linkBox[1] >= 44, dev + ': the link is a 44 px target ' + JSON.stringify(s.linkBox));
            s = await at(45); assert.equal(s.live, 'Still working, 30 seconds', dev + ': not announced again at 45 s'); assert.equal(s.time, ' · 45 s');
            s = await at(60); assert.deepEqual([s.live, s.time], ['Still working, 1 minute', ' · 1 min 00 s'], dev + ': once a minute');
            s = await at(61); assert.deepEqual([s.live, s.time], ['Still working, 1 minute', ' · 1 min 01 s'], 'and not again at 61 s');
            s = await at(120); assert.equal(s.live, 'Still working, 2 minutes');
            // the row leaving the page stops its timer; the link is in the tab order and opens the terminal view
            assert.equal(await ev(`(() => { const f = window.__frw; const el = f.c.querySelector('.chat-working'); window.showTerminalView = () => { window.__frwOpened = true; }; el.querySelector('.chat-working-link').click(); return window.__frwOpened === true; })()`), true, dev + ': the link opens the terminal view');
            assert.equal(await ev(`(() => { const f = window.__frw; const st = f.m; return f.c.querySelector('.chat-working-link').tabIndex; })()`), 0, 'in the tab order');
            await ev(`(async () => { const f = window.__frw; const st = (await import((window.__coralStatic || '/static/') + 'state.js')).state; st.liveSessions = []; f.m.syncWorkingIndicator(f.c, { session_id: 'frw-1' }); Date.now = f.real; return true; })()`);
            assert.equal(await ev(`!!window.__frw.c.querySelector('.chat-working')`), false, dev + ': the row is gone when the agent is not working');
        }
        console.log('PASS first reply wait: digits from 5 s, Still working and a real link at 30 s, one polite status then once a minute');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
