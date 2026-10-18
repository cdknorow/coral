// #4706 (UX scale pass): what a long list does to three panels, against stubbed answers.
//  - Team Artifacts names the board (its label, never its id) and says when the list is only the newest part: "500+ artifacts", "Showing 100 of 500+"
//  - Servers: with more than five machines "Add server" and a count sit above the list, not screens below it; with a few it stays under the list
//  - the task list in a narrow panel shows a long title on two lines, not cut at the first
// Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const BOARD = 'b-scale' + Date.now().toString(36);
    const items = n => Array.from({ length: n }, (_, i) => ({ id: 'uri:https://example.invalid/' + i, name: `report-${i}.txt`, media_type: 'text/plain', size: null, task_id: 20 + i, task_title: 'T' + i, created_at: '2026-10-01T10:00:00Z', uri: 'https://example.invalid/' + i, available: true, inline: false, reference_count: 1 }));
    const servers = n => [{ id: 'local', label: 'studio', local: true, status: 'online' }, ...Array.from({ length: n }, (_, i) => ({ id: 'm' + (i + 1), label: 'machine-' + (i + 1), url: 'http://10.0.0.' + (i + 11) + ':8420', status: i % 4 === 3 ? 'unreachable' : 'online' }))];
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1200, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { const R = window.fetch; window.__servers = ${JSON.stringify(servers(3))};
            const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o = {}) => { const p = new URL(String(u), location.origin).pathname;
                if (p.startsWith('/api/board/') && p.endsWith('/artifacts')) return json({ project: ${JSON.stringify(BOARD)}, artifacts: ${JSON.stringify(items(3))}, has_more: true, truncated: true, total: 500, all: 500 });
                if (p === '/api/servers' && (o.method || 'GET') === 'GET') return json(window.__servers);
                if (p === '/api/health') return json({ status: 'ok', hub: true, version: '1.4.92' });
                if (p === '/api/system/artifact-store/kept') return json({ kept: [] });
                return R(u, o); };
            return true; })()`);

        console.log('1. Team Artifacts: the board by its name, and a list that is only the newest part says so');
        await ev(`(async () => { const base = (window.__coralStatic || '/static/'); const {state} = await import(base + 'state.js'); const sb = await import(base + 'server_base.js');
            sb.noteBoardLabels([{ board_id: ${JSON.stringify(BOARD)}, board_label: 'perf-board' }], 'board_id');
            state.currentSession = { type: 'live', name: 'ux-ui', session_id: 'ux-a', board_project: ${JSON.stringify(BOARD)} };
            const {showView} = await import(base + 'utils.js'); showView('live-session-view'); const files = await import(base + 'changed_files.js');
            const side = document.getElementById('agentic-state'); side.classList.remove('collapsed'); side.style.width = '680px'; files.initFileSearch(); files.syncFilesViewerSession(); return true; })()`);
        await ev(`localStorage.removeItem('coral-agentic-tab-top'); document.getElementById('agentic-tab-team-artifacts').click(); true`);
        await waitFor(`document.querySelectorAll('tr.team-artifact-row').length === 3`, 'the three rows', 8000);
        const v = '#agentic-panel-team-artifacts-view';
        assert.equal(await ev(`document.querySelector('${v} .team-artifacts-heading span').textContent`), 'Team Artifacts · perf-board');
        assert.equal(await ev(`document.querySelector('${v} .list-bar-count').textContent`), '500+ artifacts');
        assert.equal(await ev(`document.querySelector('${v} .team-artifacts-status').textContent`), 'Showing 3 of 500+');

        console.log('2. Servers: Add server above a long list, under a short one');
        const where = () => ev(`(() => { const b = document.querySelector('#servers-view [data-action="add"]'); const l = document.querySelector('#servers-view .servers-list'); if (!b || !l) return null; return { above: !!(b.compareDocumentPosition(l) & Node.DOCUMENT_POSITION_FOLLOWING), n: document.querySelectorAll('#servers-view [data-action="add"]').length, summary: (document.querySelector('#servers-view .servers-summary') || {}).textContent || '' }; })()`);
        await ev(`(async () => { const base = (window.__coralStatic || '/static/'); const s = await import(base + 'state.js'); s.state.hub = true; s.state.servers = window.__servers; const t = await import(base + 'servers_tab.js'); t.updateServersTabVisibility(); return true; })()`);
        await ev(`window.switchNavTab('servers'); true`);
        await waitFor(`!!document.querySelector('#server-row-m3')`, 'a few servers');
        assert.deepEqual(await where(), { above: false, n: 1, summary: '' }, 'three machines: the button stays under the list, no count line');
        await ev(`window.__servers = ${JSON.stringify(servers(8))}; (async () => { const s = await import((window.__coralStatic || '/static/') + 'state.js'); s.state.servers = window.__servers; const t = await import((window.__coralStatic || '/static/') + 'servers_tab.js'); await t.showServersTabContent(); })(); true`);
        await waitFor(`!!document.querySelector('#server-row-m8')`, 'eight servers');
        assert.deepEqual(await where(), { above: true, n: 1, summary: '8 servers, 6 online, 2 not' }, 'eight machines: one button, above the list, with the count');
        assert.equal(await ev(`document.querySelector('#servers-view .servers-summary').hasAttribute('role')`), false, 'the count is plain text: the list is rebuilt on every refresh, a live region would be read out each time');
        const top = await ev(`document.querySelector('#servers-view [data-action="add"]').getBoundingClientRect().top < innerHeight`);
        assert.equal(top, true, 'the button is on the first screen');

        console.log('3. A narrow task list shows a long title on two lines');
        await ev(`window.switchNavTab('agents'); true`);
        const lines = await ev(`(() => { const host = document.createElement('div'); host.id = 'scale-probe'; host.style.cssText = 'position:fixed;left:0;top:0;width:360px;background:#000;z-index:9999';
            host.innerHTML = '<div class="board-task-list" style="width:360px"><div class="board-task-item"><span class="board-task-status-col">Open</span><span class="board-task-id">#1</span><span class="board-task-desc">' + 'A long title that keeps going and going so that it needs more than one line to be read. '.repeat(3) + '</span></div></div>';
            document.body.append(host); const d = host.querySelector('.board-task-desc'); const cs = getComputedStyle(d); const lh = parseFloat(cs.lineHeight) || 16; const r = { clamp: cs.webkitLineClamp, ws: cs.whiteSpace, lines: Math.round(d.getBoundingClientRect().height / lh) }; host.remove(); return r; })()`);
        assert.equal(lines.clamp, '2'); assert.equal(lines.ws, 'normal'); assert.ok(lines.lines === 2, 'the title is two lines tall (' + lines.lines + ')');
        console.log('PASS');
    } catch (e) {
        console.error(e && e.stack || e);
        process.exitCode = 1;
    } finally {
        try { await client.close(); } catch (_) {}
    }
})();
