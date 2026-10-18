// A long agent name wraps to a second line in the phone's agent list without the state chip drawn over it (#4612: the name row was a fixed 16 px high, so the second line
// overflowed it and the IDLE chip below covered "...-4-9"). Drives Chrome over CDP through agent_view_harness.js against an isolated dev server; run_cdp_suites.mjs finds it by name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true }); await Emulation.setTouchEmulationEnabled({ enabled: true });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        await ev(`(async () => { const st = window.__coralStatic || '/static/'; const ws = await import(st + 'websocket.js'); const render = await import(st + 'render.js'); const { state } = await import(st + 'state.js');
            if (state.coralWs) { state.coralWs.onclose = null; state.coralWs.onmessage = null; try { state.coralWs.close(); } catch {} }
            const names = ['release-engineering-coordinator-4-9', 'backend-platform-reliability-engineer-4-7', 'qa', 'data-pipeline-quality-analyst-4-10'];
            ws.handleCoralMessage({ type: 'coral_update', servers: [{ id: 'local', label: 'here', status: 'online' }], sessions: names.map((n, i) => ({ name: 'p-' + i, session_id: 'p-' + i, agent_type: 'claude', server: 'local', status: 'idle', summary: 'Task ' + i, display_name: n, working_directory: '/repo/a', branch: 'main', board_project: 'team-a' })) });
            render.flushLiveSessionsRender(); return true; })()`);
        await new Promise(r => setTimeout(r, 500)); // sleep-ok: the list draws on the next frame
        const rows = await ev(`[...document.querySelectorAll('#mobile-session-list .session-group-item, .session-group-item')].filter(r => r.getClientRects().length).map(r => { const nm = r.querySelector('.session-label'); const row = r.querySelector('.session-name-row'); const chip = r.querySelector('.session-status-chip'); const a = nm.getBoundingClientRect(), b = row.getBoundingClientRect(), c = chip ? chip.getBoundingClientRect() : null;
            return { name: nm.textContent.trim(), lines: Math.round(a.height / parseFloat(getComputedStyle(nm).lineHeight)), labelBottom: Math.round(a.bottom), rowBottom: Math.round(b.bottom), chipTop: c ? Math.round(c.top) : null }; })`);
        assert.ok(rows.length >= 4, 'the rows are in the list: ' + JSON.stringify(rows));
        assert.ok(rows.some(r => r.lines >= 2), 'a long name wraps to two lines on a phone (the case under test): ' + JSON.stringify(rows));
        for (const r of rows) {
            assert.ok(r.rowBottom >= r.labelBottom - 1, `${r.name}: the name row holds its name (row bottom ${r.rowBottom}, name bottom ${r.labelBottom})`);
            if (r.chipTop !== null) assert.ok(r.chipTop >= r.labelBottom - 1, `${r.name}: the state chip starts below the name (chip ${r.chipTop}, name bottom ${r.labelBottom})`);
        }
        console.log('PASS mobile name wrap: ' + JSON.stringify(rows));
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
