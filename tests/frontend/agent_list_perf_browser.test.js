// Agent list at scale (#3121): 250 agents over 5 servers. A one-agent feed change
// must cost a small fraction of a full rebuild (both measured in this run, so the
// budget holds on any machine), replace almost nothing, and a burst of feed
// messages must render once. Loose budgets: this guards against regressions to
// the old rebuild-everything behaviour, not against small slowdowns.
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;
const { framesJs } = require('./helpers/wait');

(async () => {
    const { client, Page, ev, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD + `;window.__renders = 0; window.addEventListener('coral:live-list-rendered', () => window.__renders++);` });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);

        const r = await ev(`(async () => {
            const st = window.__coralStatic || '/static/';
            const ws = await import(st + 'websocket.js'); const render = await import(st + 'render.js');
            const { state } = await import(st + 'state.js'); const morph = await import(st + 'dom_morph.js');
            if (state.coralWs) { state.coralWs.onclose = null; state.coralWs.onmessage = null; try { state.coralWs.close(); } catch {} }
            state.hub = true;
            const servers = ['local', 's1', 's2', 's3', 's4'];
            const serverList = servers.map(id => ({ id, label: 'box-' + id, status: 'online' }));
            const mk = i => ({ name: 'p-' + i, session_id: 'p-' + i, agent_type: 'claude', server: servers[i % 5], status: 'working', summary: 'Task ' + i,
                board_project: 'team-' + (i % 10), display_name: 'Agent ' + i, working_directory: '/repo/team-' + (i % 10), branch: 'main' });
            ws.handleCoralMessage({ type: 'coral_update', servers: serverList, sessions: Array.from({ length: 250 }, (_, i) => mk(i)) });
            render.flushLiveSessionsRender();
            const list = document.getElementById('live-sessions-list');
            const median = a => a.sort((x, y) => x - y)[a.length >> 1];
            const time = f => { const t = performance.now(); f(); void list.offsetHeight; return performance.now() - t; }; // includes layout

            // Full rebuild: forget what the morph knows, so the whole list is re-parsed.
            const fullMs = [];
            for (let k = 0; k < 5; k++) fullMs.push(time(() => { morph.resetMorph(list); render.renderLiveSessions(state.liveSessions); }));
            // One agent changes.
            const oneMs = []; let replaced = 0, full = 0;
            for (let k = 0; k < 9; k++) {
                const b = { ...morph.morphStats };
                const i = (k * 37) % 250;
                oneMs.push(time(() => { ws.handleCoralMessage({ type: 'coral_diff', changed: [{ name: 'p-' + i, session_id: 'p-' + i, server: servers[i % 5], summary: 'changed ' + k }] }); render.flushLiveSessionsRender(); }));
                replaced += morph.morphStats.replaced - b.replaced; full += morph.morphStats.full - b.full;
            }
            // A burst of feed messages renders once.
            const before = window.__renders;
            for (let k = 0; k < 20; k++) ws.handleCoralMessage({ type: 'coral_diff', changed: [{ name: 'p-3', session_id: 'p-3', server: servers[3], summary: 'burst ' + k }] });
            await ${framesJs(2)};
            return { rows: list.querySelectorAll('.session-group-item[data-session-id]').length, nodes: list.getElementsByTagName('*').length,
                fullMs: median(fullMs), oneMs: median(oneMs), replacedPerChange: replaced / 9, fullRebuilds: full, burstRenders: window.__renders - before };
        })()`);
        console.log(`   250 agents, ${r.nodes} nodes: full rebuild ${r.fullMs.toFixed(1)} ms, one-agent change ${r.oneMs.toFixed(1)} ms (${(100 * r.oneMs / r.fullMs).toFixed(0)}%), ${r.replacedPerChange.toFixed(1)} nodes replaced per change, burst of 20 -> ${r.burstRenders} render(s)`);
        assert.equal(r.rows, 250, 'all rows rendered');
        assert.ok(r.oneMs < 0.5 * r.fullMs, `a one-agent change costs well under a full rebuild (${r.oneMs.toFixed(1)} vs ${r.fullMs.toFixed(1)} ms)`);
        assert.equal(r.fullRebuilds, 0, 'no full rebuild for a one-agent change');
        assert.ok(r.replacedPerChange <= 3, `a one-agent change replaces at most a few nodes (${r.replacedPerChange})`);
        assert.equal(r.burstRenders, 1, `a burst of feed messages renders once (${r.burstRenders})`);
        // A row's action menu is drawn when it opens (#4611): none of its items are in the list until then, one menu at a time is drawn, and its keyboard and click behaviour are the same.
        const m = await ev(`(() => {
            const list = document.getElementById('live-sessions-list');
            const rows = [...list.querySelectorAll('.session-group-item[data-session-id]')];
            const out = { perRow: Math.round(list.getElementsByTagName('*').length / rows.length), idle: list.querySelectorAll('.sidebar-kebab-menu[data-lazy-menu] *').length };
            const btn = r => r.querySelector('.sidebar-kebab-btn');
            btn(rows[0]).click();
            const a = rows[0].querySelector('.sidebar-kebab-menu');
            out.expOpen = btn(rows[0]).getAttribute('aria-expanded'); out.itemRoles = [...a.querySelectorAll('.overflow-menu-item')].every(i => /^menuitem/.test(i.getAttribute('role'))); out.named = /^More actions for /.test(btn(rows[0]).getAttribute('aria-label'));
            out.openItems = a.querySelectorAll('.overflow-menu-item').length; out.openShown = getComputedStyle(a).display !== 'none'; out.kill = !!a.querySelector('[data-on-click="agent-kill"]');
            btn(rows[1]).click();
            const b = rows[1].querySelector('.sidebar-kebab-menu');
            out.secondItems = b.querySelectorAll('.overflow-menu-item').length; out.firstEmptied = a.children.length === 0 && getComputedStyle(a).display === 'none'; out.expFirstAfter = btn(rows[0]).getAttribute('aria-expanded');
            document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); list.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
            out.escapeClosed = getComputedStyle(b).display === 'none' && btn(rows[1]).getAttribute('aria-expanded') === 'false';
            btn(rows[1]).click(); btn(rows[1]).click();
            out.toggledClosed = getComputedStyle(b).display === 'none';
            return out;
        })()`);
        assert.ok(m.perRow < 40, `a row costs well under its old 70 elements: ${m.perRow}`);
        assert.equal(m.idle, 0, 'no menu item is in the list until a menu opens');
        assert.ok(m.openItems >= 5 && m.openShown && m.kill, 'the menu that opens has its items: ' + JSON.stringify(m));
        assert.ok(m.secondItems >= 5 && m.firstEmptied, 'opening another row\'s menu draws it and takes the first one out: ' + JSON.stringify(m));
        assert.ok(m.expOpen === 'true' && m.expFirstAfter === 'false' && m.itemRoles && m.named, 'aria-expanded follows the menu, its items are menuitems, the button names the agent: ' + JSON.stringify(m));
        assert.ok(m.escapeClosed && m.toggledClosed, 'Escape and the button close it: ' + JSON.stringify(m));
        console.log('PASS agent_list_perf_browser');
    } finally {
        await client.close();
    }
})().catch(e => { console.error(e); process.exit(1); });
