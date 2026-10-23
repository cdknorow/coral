// The agent list's header (#6597, the operator's): no summary line under Search and no Clear link beside it; the blue dot on Display options shows only when a real filter (state, board
// machine) narrows the list, never for a show/hide display setting (provider, model, thinking level, branches, summaries) and not for a sort. 1280 and 390 px. Six agents on two teams.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

const uid = n => '0000' + String(n).padStart(4, '0') + '-0000-4000-8000-000000000000';
const SESSIONS = Array.from({ length: 6 }, (_, i) => {
    const n = i + 1;
    return { name: 'ag-' + n, session_id: uid(n), agent_type: 'claude', server: 'local', status: 'idle', summary: 'Task ' + n, display_name: 'Agent ' + n, working_directory: '/repo/' + (n % 2 ? 'team-a' : 'team-b'), branch: 'main',
        board_project: n % 2 ? 'team-a' : 'team-b', staleness_seconds: 100 * n, token_cost_usd: n, working: n === 2, waiting_for_input: n === 1, sleeping: false };
});

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; await ev(`document.querySelectorAll('.notification-toast, .toast').forEach(e => e.remove()); true`); fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile, header, bar] of [[1280, 900, false, '.sidebar-header', '#agent-list-bar'], [390, 844, true, '.mobile-list-header', '#mobile-agent-list-bar']]) {
            const at = `at ${w}px: `;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'settings')`);
            await ev(`(async () => { const st = window.__coralStatic || '/static/'; const ws = await import(st + 'websocket.js'); const render = await import(st + 'render.js'); const { state } = await import(st + 'state.js');
                if (state.coralWs) { state.coralWs.onclose = null; state.coralWs.onmessage = null; try { state.coralWs.close(); } catch {} }
                localStorage.clear(); state.hub = false; window.__T = { ws, render, state };
                ws.handleCoralMessage({ type: 'coral_update', servers: [], sessions: ${JSON.stringify(SESSIONS)} }); render.flushLiveSessionsRender(); render.renderLiveSessions(state.liveSessions); return true; })()`);
            await waitFor(`document.querySelectorAll('#live-sessions-list .session-group-item, #mobile-session-list .session-group-item').length === 6`, 'six agents', 8000);
            const dot = () => ev(`(() => { const b = document.querySelector('${header} [data-display-badge]'); return !!b && !b.hidden && b.getClientRects().length > 0; })()`);
            const label = () => ev(`document.querySelector('${header} [data-display-button]').getAttribute('aria-label')`);
            const gone = async what => {
                assert.equal(await ev(`document.querySelector('${header} [data-agent-list-summary]')`), null, at + 'no summary line ' + what);
                assert.equal(await ev(`document.querySelector('${header} .agent-list-summary-clear')`), null, at + 'no Clear link ' + what);
                assert.equal(await ev(`/ hidden ·| hidden$|Sort:/.test(document.querySelector('${header}').innerText)`), false, at + 'no "... hidden" text in the header ' + what);
            };
            const panel = async open => { const isOpen = await ev(`document.querySelector('${header} [data-display-button]').getAttribute('aria-expanded') === 'true'`); if (isOpen !== open) await ev(`document.querySelector('${header} [data-display-button]').click(); true`); };

            // 1. nothing on: no line, no dot
            await gone('with nothing on');
            assert.equal(await dot(), false, at + 'no dot with nothing on');
            assert.equal(await label(), 'Display options');

            // 2. display settings only (provider, model, thinking level hidden; branches shown): still no dot, no line
            await panel(true);
            await waitFor(`!!document.querySelector('${header} [data-display-input="provider"]')`, 'the display options', 8000);
            for (const k of ['provider', 'model', 'thinking']) await ev(`(() => { const c = document.querySelector('${header} [data-display-input="${k}"]'); if (c.checked) c.click(); return true; })()`);
            await ev(`(() => { const c = document.querySelector('${header} [data-display-input="branches"]'); if (!c.checked) c.click(); return true; })()`);
            await panel(false);
            await gone('after hiding provider, model and thinking level');
            assert.equal(await dot(), false, at + 'no dot for show/hide display settings');
            assert.equal(await label(), 'Display options');

            // 3. a sort is not a filter: no dot
            await panel(true);
            await waitFor(`!!document.querySelector('${bar} select[aria-label="Sort agents"], ${bar} .list-bar-sort')`, 'the sort', 8000);
            await ev(`(() => { const s = document.querySelector('${bar} select[aria-label="Sort agents"], ${bar} .list-bar-sort'); s.value = 'name'; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
            await panel(false);
            await gone('after a sort');
            assert.equal(await dot(), false, at + 'a sort does not narrow the list: no dot');

            // 4. a real filter: the dot, and a button name that says so; still no line
            await panel(true);
            await ev(`(() => { const s = document.querySelector('${bar} select[id$="-state"]'); s.value = 'needs_input'; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
            await panel(false);
            await gone('with a filter on');
            assert.equal(await dot(), true, at + 'a filter that narrows the list shows the dot');
            assert.equal(await label(), 'Display options, 1 filter on');
            assert.equal(await ev(`document.querySelectorAll('#live-sessions-list .session-group-item, #mobile-session-list .session-group-item').length`), 1, at + 'the list is narrowed to the one waiting agent');
            await shot(`agent-list-header-filter-${w}`);

            // 5. the panel's own Clear filters takes it away again
            await panel(true);
            await ev(`document.querySelector('${bar} .list-bar-clear-all').click(); true`);
            await panel(false);
            assert.equal(await dot(), false, at + 'cleared: no dot');
            assert.equal(await label(), 'Display options');
            await shot(`agent-list-header-clear-${w}`);
        }
        console.log('PASS agent list header: no summary line, the dot only for a real filter');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
