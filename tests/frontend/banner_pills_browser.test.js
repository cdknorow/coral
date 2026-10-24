// The two notices over the agent view are overlay pills (#5287, Coral Hub's request): "server unreachable" (#server-unreachable-banner) and "waiting for input"
// (#waiting-banner). Each takes no layout space (layout shift 0, the agent view does not move), shows only after 1.5 s of being wanted, goes at once when it
// is not, is not restarted by the feed updates that repeat the same state, is role=status with aria-live=polite, and lets clicks through. A drop that ends
// within the delay never shows it. Drives Chrome over CDP (chrome-remote-interface); run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

// The remote 'box-b' answers (up) so the page is quiet; the suite flips the agent's stale flag through the real feed handler.
const STUB = `(() => {
    const real = window.fetch.bind(window);
    const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'Content-Type': 'application/json' } }));
    window.fetch = (url, opts) => {
        const p = new URL(String(url), location.origin).pathname;
        if (!p.startsWith('/api/remote/box-b/')) return real(url, opts);
        if (/\\/poll$/.test(p)) return json({ capture: { capture: 'remote prompt $' }, tasks: [], events: [] });
        if (/\\/api\\/agent\\/ui$/.test(p)) return json([]);
        return json({});
    };
    // Layout shifts, and every change of a pill's hidden flag with its time.
    window.__cls = 0; window.__shifts = [];
    new PerformanceObserver(l => { for (const e of l.getEntries()) if (!e.hadRecentInput) { window.__cls += e.value; window.__shifts.push({ v: e.value, nodes: (e.sources || []).map(x => { const n = x.node; if (!n) return '(gone)'; const el = n.nodeType === 1 ? n : n.parentElement; return (el.id ? '#' + el.id : '') + '.' + String(el.className).split(' ')[0] + ' in ' + (el.closest('#agent-list, .agent-list, .live-left-column, .session-list, aside, nav') || {className: '?'}).className; }) }); } }).observe({ type: 'layout-shift', buffered: true });
    window.__flips = [];
    window.__watch = () => {
        for (const id of ['server-unreachable-banner', 'waiting-banner']) {
            const el = document.getElementById(id);
            if (el && !el.__watched) { el.__watched = true; new MutationObserver(() => window.__flips.push({ id, hidden: el.hidden, at: performance.now() })).observe(el, { attributes: true, attributeFilter: ['hidden'] }); }
        }
    };
})();`;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation } = client;
    const ev = async x => {
        const r = await Runtime.evaluate({ expression: x, awaitPromise: true, returnByValue: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const sleep = ms => new Promise(r => setTimeout(r, ms));
    const SHOT = process.env.BANNER_SHOTS || '';
    const shot = async name => { if (!SHOT) return; const r = await Page.captureScreenshot({ format: 'png' }); fs.writeFileSync(`${SHOT}/${name}.png`, Buffer.from(r.data, 'base64')); };
    const feed = down => ev(`(async () => {
        const ws = await import((window.__coralStatic || '/static/') + 'websocket.js');
        ws.handleCoralMessage({ type: 'coral_diff', changed: [${JSON.stringify({ name: 'rb', session_id: 'rb-1', server: 'box-b', agent_type: 'terminal' })}].map(s => ${down ? '({ ...s, stale: true })' : 's'}),
            servers: [{ id: 'box-b', label: 'box-b.lan', status: ${down ? "'unreachable'" : "'online'"} }] });
        return true;
    })()`);
    const pill = id => ev(`(() => { const b = document.getElementById('${id}'); const r = b.getBoundingClientRect(); const c = getComputedStyle(b);
        return { shown: !b.hidden && b.getClientRects().length > 0, text: b.textContent, role: b.parentElement.getAttribute('role'), live: b.parentElement.getAttribute('aria-live'), own: b.getAttribute('role') || b.getAttribute('aria-live') || '', pos: getComputedStyle(b.parentElement).position, ptr: c.pointerEvents, inTop: r.top, h: r.height,
                 column: (() => { const col = document.querySelector('.live-left-column').getBoundingClientRect(); return { top: col.top, left: col.left, right: col.right }; })(), left: r.left, right: r.right }; })()`);
    // The agent view's geometry: the terminal header and the capture area must not move when a pill appears or goes.
    const geometry = () => ev(`(() => { const r = s => { const e = document.querySelector(s); if (!e) return null; const b = e.getBoundingClientRect(); return [Math.round(b.top * 10) / 10, Math.round(b.left * 10) / 10, Math.round(b.width * 10) / 10, Math.round(b.height * 10) / 10]; };
        return { header: r('.terminal-header'), capture: r('#capture-wrapper'), column: r('.live-left-column') }; })()`);
    const flips = () => ev(`window.__flips.map(f => ({ ...f }))`);
    const setup = async () => {
        await Page.addScriptToEvaluateOnNewDocument({ source: STUB });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await coralReady('startup', 'liveList', 'liveListLoaded', 'settings');
        await ev(`(async () => {
            const st = window.__coralStatic || '/static/';
            const { state } = await import(st + 'state.js');
            const ws = await import(st + 'websocket.js');
            if (state.coralWs) { state.coralWs.onclose = null; state.coralWs.onmessage = null; try { state.coralWs.close(); } catch {} }
            state.hub = true;
            localStorage.setItem('coral-live-view-mode', 'terminal');
            ws.handleCoralMessage({ type: 'coral_update', servers: [{ id: 'box-b', label: 'box-b.lan', status: 'online' }],
                sessions: [{ name: 'rb', session_id: 'rb-1', agent_type: 'terminal', server: 'box-b', display_name: 'Remote shell', status: 'working', stale: false }] });
            await window.selectLiveSession('rb', 'terminal', 'rb-1', 'box-b');
            window.__watch();
            return true;
        })()`);
        await waitFor(`!!document.getElementById('server-unreachable-banner') && document.querySelector('.terminal-header') && document.querySelector('.terminal-header').getClientRects().length > 0`, 'the agent view');
    };
    const waiting = on => ev(`(async () => {
        const m = await import((window.__coralStatic || '/static/') + 'session_header_updates.js');
        m.updateWaitingIndicator(${on ? "{ waiting_for_input: true, waiting_kind: 'permission', session_id: 'rb-1' }" : "{ session_id: 'rb-1' }"});
        return true;
    })()`);
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        for (const phone of [false, true]) {
            const tag = phone ? 'phone' : 'desktop';
            await Emulation.setDeviceMetricsOverride(phone ? { width: 390, height: 844, deviceScaleFactor: 1, mobile: true } : { width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
            await setup();
            // a phone's Keys strip opens by itself for a question (#6802) and moves the view by design; this suite is about the pills moving nothing, so the strip and its chip are out of the page here (agent_keys_browser covers them)
            if (phone) await ev(`(() => { const st = document.createElement('style'); st.textContent = '.keys-chip, .agent-key-strip { display: none !important; }'; document.head.appendChild(st); return true; })()`);
            await ev(`window.__cls = 0; window.__shifts = []; window.__flips.length = 0; true`);
            const g0 = await geometry();
            assert.ok(g0.header && g0.capture, `${tag}: the agent view is on screen: ` + JSON.stringify(g0));

            // 1. The server drops: nothing for 1.5 s, then the pill, over the view, moving nothing.
            const t0 = Date.now();
            await feed(true);
            await sleep(900); // sleep-ok: inside the 1.5 s delay: the pill must not be there yet
            assert.equal((await pill('server-unreachable-banner')).shown, false, `${tag}: not shown 0.9 s into a drop`);
            await waitFor(`!document.getElementById('server-unreachable-banner').hidden`, 'the unreachable pill', { timeout: 4000 });
            const dt = Date.now() - t0;
            assert.ok(dt >= 1400 && dt < 2600, `${tag}: shown after about 1.5 s (${dt} ms)`);
            const p1 = await pill('server-unreachable-banner');
            assert.ok(/box-b\.lan is unreachable/.test(p1.text), `${tag}: it says why: ${p1.text}`);
            assert.equal(p1.role, 'status'); assert.equal(p1.live, 'polite', `${tag}: the stack the pill is in is the polite live region (always rendered, so a pill that shows already filled is announced)`);
            assert.equal(p1.own, '', `${tag}: the pill itself is not a second live region`);
            assert.equal(p1.pos, 'absolute', `${tag}: an overlay, not in the flow`); assert.equal(p1.ptr, 'none', `${tag}: clicks pass through`);
            assert.ok(p1.left >= p1.column.left - 1 && p1.right <= p1.column.right + 1 && p1.inTop >= g0.header[0] + g0.header[3] - 0.5, `${tag}: the pill is over the agent view, under the header (the Chat/Terminal toggle stays uncovered): ` + JSON.stringify(p1));
            assert.deepEqual(await geometry(), g0, `${tag}: showing the pill moved nothing`);
            await shot(`${tag}-unreachable`);
            console.log(`   ✓ ${tag}: a drop shows the pill after ${dt} ms, over the view, moving nothing; role=status aria-live=polite, clicks pass through`);

            // 2. It returns: the pill goes at once, and nothing moves.
            const t1 = Date.now();
            await feed(false);
            await waitFor(`document.getElementById('server-unreachable-banner').hidden`, 'the pill going', { timeout: 1000 });
            assert.ok(Date.now() - t1 < 400, `${tag}: gone at once on recovery (${Date.now() - t1} ms)`);
            assert.deepEqual(await geometry(), g0, `${tag}: hiding the pill moved nothing`);
            console.log(`   ✓ ${tag}: recovery hides it in ${Date.now() - t1} ms`);

            // 3. A drop shorter than the delay never shows it.
            await ev(`window.__flips.length = 0; true`);
            await feed(true); await sleep(600); // sleep-ok: a drop shorter than the 1.5 s delay
            await feed(false); await sleep(1500); // sleep-ok: past where the delay would have ended
            assert.equal((await flips()).filter(f => f.id === 'server-unreachable-banner' && f.hidden === false).length, 0, `${tag}: a 0.6 s drop never showed the pill`);
            console.log(`   ✓ ${tag}: a 0.6 s drop never shows it`);

            // 4. Feed updates that repeat the same state do not restart the wait.
            await ev(`window.__flips.length = 0; true`);
            const t2 = Date.now();
            await feed(true);
            for (let i = 0; i < 4; i++) { await sleep(300); await feed(true); } // sleep-ok: the same state repeated every 0.3 s for 1.2 s
            await waitFor(`!document.getElementById('server-unreachable-banner').hidden`, 'the pill after repeated updates', { timeout: 3000 });
            const dt2 = Date.now() - t2;
            assert.ok(dt2 < 2400, `${tag}: repeated feed updates did not restart the delay (${dt2} ms)`);
            await feed(false);
            await waitFor(`document.getElementById('server-unreachable-banner').hidden`, 'the pill going again', { timeout: 1000 });
            console.log(`   ✓ ${tag}: repeated updates of the same state show it after ${dt2} ms (not restarted)`);

            // 5. The waiting-for-input pill: same rules.
            const w0 = await geometry();
            const t3 = Date.now();
            await waiting(true);
            await sleep(900); // sleep-ok: inside the 1.5 s delay
            assert.equal((await pill('waiting-banner')).shown, false, `${tag}: waiting pill not shown 0.9 s in`);
            await waitFor(`!document.getElementById('waiting-banner').hidden`, 'the waiting pill', { timeout: 4000 });
            const dt3 = Date.now() - t3;
            assert.ok(dt3 >= 1400 && dt3 < 2600, `${tag}: waiting pill after about 1.5 s (${dt3} ms)`);
            const p3 = await pill('waiting-banner');
            assert.ok(/waiting for input/.test(p3.text), p3.text); assert.equal(p3.role, 'status'); assert.equal(p3.live, 'polite');
            assert.equal(p3.pos, 'absolute'); assert.equal(p3.ptr, 'none');
            assert.deepEqual(await geometry(), w0, `${tag}: the waiting pill moved nothing`);
            await shot(`${tag}-waiting`);
            await waiting(false);
            assert.equal(await ev(`document.getElementById('waiting-banner').hidden`), true, `${tag}: gone at once when the agent is no longer waiting`);
            await ev(`window.__flips.length = 0; true`);
            await waiting(true); await sleep(500); await waiting(false); await sleep(1500); // sleep-ok: a wait shorter than the delay, then past it
            assert.equal((await flips()).filter(f => f.id === 'waiting-banner' && f.hidden === false).length, 0, `${tag}: a 0.5 s wait never showed the pill`);
            console.log(`   ✓ ${tag}: the waiting pill shows after ${dt3} ms, moves nothing, goes at once, a 0.5 s wait never shows it`);

            // 6. Both wanted: one notice at a time (the unreachable pill has the slot; the agent's waiting is only its last known state); nothing moves.
            await feed(true); await waiting(true);
            await waitFor(`!document.getElementById('server-unreachable-banner').hidden && !document.getElementById('waiting-banner').hidden`, 'both pills wanted and past the delay', { timeout: 4000 });
            const both = await ev(`({ unreachable: document.getElementById('server-unreachable-banner').getClientRects().length > 0, waiting: document.getElementById('waiting-banner').getClientRects().length > 0 })`);
            assert.deepEqual(both, { unreachable: true, waiting: false }, `${tag}: only the unreachable pill is drawn while the server is down`);
            await shot(`${tag}-both`);
            await feed(false);
            await waitFor(`document.getElementById('server-unreachable-banner').hidden`, 'the unreachable pill gone', { timeout: 1000 });
            await waiting(false);
            assert.deepEqual(await geometry(), g0, `${tag}: the agent view is where it was`);
            // The terminal's own size readout (#terminal-statusbar-size, "80x24") changes text when a poll lands and shifts by about 5e-6 each time: it is not a pill's
            // doing (no pill is a source, and the geometry check above moved nothing), so it is left out and reported.
            const cls = await ev(`({ shifts: window.__shifts })`);
            const readout = x => x.nodes.length > 0 && x.nodes.every(n => n.startsWith('#terminal-statusbar-size'));
            const ours = cls.shifts.filter(x => !readout(x)), theirs = cls.shifts.filter(readout).reduce((t, x) => t + x.v, 0);
            assert.deepEqual(ours, [], `${tag}: layout shift 0 through the whole run, apart from the terminal's size readout: ` + JSON.stringify(ours));
            console.log(`   ✓ ${tag}: one pill at a time; layout shift over the whole run: 0 (the terminal size readout's own ${theirs.toExponential(1)} left out)`);
        }
        console.log('PASS banner_pills_browser');
    } finally {
        await client.close();
    }
})().catch(e => { console.error(e); process.exit(1); });
