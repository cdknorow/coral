// Text on a phone is at least 13px (coral-hub's Mobile probe found 10 to 12px): the agent list (the group count, a state chip, the goal) and the Servers view
// (its empty line, a machine's meta line, the machine health card). At 320 and 390 px; nothing in the list wraps badly or runs off the screen; desktop keeps its sizes.
// Drives Chrome over CDP against the isolated dev server through agent_view_harness.js; run_cdp_suites.mjs finds it by name.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    const shot = async name => { if (!process.env.CORAL_SCREENSHOT_DIR) return; fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); await new Promise(r => setTimeout(r, 250)); // sleep-ok: a screenshot settles layout, there is no event for it
        fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        const open = async (w, h, mobile) => {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'settings')`);
            await ev(`(async () => { const st = window.__coralStatic || '/static/'; const ws = await import(st + 'websocket.js'); const render = await import(st + 'render.js'); const { state } = await import(st + 'state.js');
                if (state.coralWs) { state.coralWs.onclose = null; state.coralWs.onmessage = null; try { state.coralWs.close(); } catch {} }
                const names = ['release-engineering-coordinator-4-9', 'qa', 'data-pipeline-quality-analyst-4-10'];
                ws.handleCoralMessage({ type: 'coral_update', servers: [{ id: 'local', label: 'here', status: 'online' }], sessions: names.map((n, i) => ({ name: 'p-' + i, session_id: 'p-' + i, agent_type: 'claude', server: 'local', status: i === 1 ? 'working' : 'idle', summary: 'Goal ' + i + ': keep the release train on schedule', display_name: n, working_directory: '/repo/a', branch: 'main', board_project: 'team-a' })) });
                render.flushLiveSessionsRender(); return true; })()`);
            await new Promise(r => setTimeout(r, 500)); // sleep-ok: the list draws on the next frame
        };
        // the Servers view's text, as the stylesheets draw it (the classes carry the sizes)
        const probe = () => ev(`(() => { document.getElementById('probe-box')?.remove(); const box = document.createElement('div'); box.id = 'probe-box'; box.style.cssText = 'position:fixed;left:0;top:0;opacity:0;pointer-events:none';
            box.innerHTML = '<p class="servers-empty">x</p><div class="cs-meta">x</div><h4 class="machine-health-title">x</h4><span class="machine-health-status">x</span><dl class="machine-health-grid"><dt>a</dt><dd>b</dd></dl>'; document.body.append(box);
            const px = sel => parseFloat(getComputedStyle(box.querySelector(sel)).fontSize); const out = { servers: { empty: px('.servers-empty'), meta: px('.cs-meta'), healthTitle: px('.machine-health-title'), healthStatus: px('.machine-health-status'), healthDt: px('.machine-health-grid dt'), healthDd: px('.machine-health-grid dd') } };
            const q = sel => [...document.querySelectorAll(sel)].filter(e => e.getClientRects().length); const sizes = sel => q(sel).map(e => parseFloat(getComputedStyle(e).fontSize));
            out.list = { count: sizes('.session-group-count'), goal: sizes('.session-goal'), chip: sizes('.session-status-chip'), n: q('.session-group-item').length };
            out.over = document.documentElement.scrollWidth - innerWidth;
            out.offscreen = q('.session-group-item *').filter(e => { const r = e.getBoundingClientRect(); return r.width > 0 && (r.right > innerWidth + 1 || r.left < -1); }).map(e => e.className.toString().slice(0, 40));
            box.remove(); return out; })()`);

        for (const w of [320, 390]) {
            console.log(`${w} px: at least 13px`);
            await open(w, 800, true);
            await shot(`mobile-text-${w}`);
            if (process.env.CORAL_SHOTS_ONLY) continue; // the before/after pictures of a change to the stylesheet
            const m = await probe();
            for (const [k, v] of Object.entries(m.servers)) assert.ok(v >= 13, `${w}: ${k} is ${v}px`);
            assert.ok(m.list.n >= 3, 'the agent rows are drawn: ' + JSON.stringify(m.list));
            for (const k of ['count', 'goal', 'chip']) for (const v of m.list[k]) assert.ok(v >= 13, `${w}: the agent list's ${k} is ${v}px`);
            assert.ok(m.list.goal.length >= 1 && m.list.chip.length >= 1, 'a goal and a chip were measured: ' + JSON.stringify(m.list));
            assert.ok(m.over <= 1, `${w}: nothing scrolls sideways (${m.over})`);
            assert.deepEqual(m.offscreen, [], `${w}: no part of a row is off the screen`);
        }
        if (process.env.CORAL_SHOTS_ONLY) return;
        console.log('1280 px: desktop keeps its sizes');
        await open(1280, 800, false);
        const d = await probe();
        assert.equal(d.servers.empty, 12); assert.equal(d.servers.meta, 12); assert.equal(d.servers.healthTitle, 11);
        for (const v of d.list.goal) assert.equal(v, 12, 'the goal on a desktop is 12px');
        for (const v of d.list.count) assert.equal(v, 11, 'a group count on a desktop is 11px');
        console.log('   ✓');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
