// The agent list's filters and sort (#4744 stage 3, Search everywhere): on the shared list bar under the list's search box (sidebar and phone), from the feed the page already holds.
// State (needs input, working, idle, asleep), Board, Machine (a hub), a Sort (list order, name, recent activity, cost), the count ("N agents", "M of N agents"), and clearing.
// #4947: the fields and the sort are rows of the Display expander (the ☰ button opens it in place); shut, a dot on the button says a filter is on (no summary line, #6597).
// 30 agents: five teams, standalone folders and a second machine. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

const uid = n => '0000' + String(n).padStart(4, '0') + '-0000-4000-8000-000000000000';
const TEAMS = ['team-a', 'team-b', 'team-c', 'team-d', 'team-e'];
const SESSIONS = Array.from({ length: 30 }, (_, i) => {
    const n = i + 1;
    const o = { name: 'ag-' + n, session_id: uid(n), agent_type: 'claude', server: n % 7 === 0 ? 's1' : 'local', status: 'idle', summary: 'Task ' + n, display_name: 'Agent ' + String(n).padStart(2, '0'),
        working_directory: '/repo/' + (n <= 25 ? TEAMS[(n - 1) % 5] : 'folder' + (n - 25)), branch: 'main', staleness_seconds: 1000 - n * 17, token_cost_usd: ((n * 37) % 11) + n / 100, working: false, waiting_for_input: false, sleeping: false };
    if (n <= 25) o.board_project = TEAMS[(n - 1) % 5];
    if (n % 6 === 1) o.waiting_for_input = true;           // needs input: 1, 7, 13, 19, 25
    else if (n % 3 === 0) o.working = true;                 // working: 3, 9, 15, 21, 27, 30 (and 12, 18, 24 ...)
    else if (n % 5 === 0) o.sleeping = true;               // asleep: 5, 10, 20, 35...
    return o;
});
SESSIONS[28].display_name = 'A very long agent name that goes on and on and on and on and on to see how the row and the bar cope with it';
const stateOf = s => s.waiting_for_input ? 'needs_input' : s.sleeping ? 'asleep' : s.working ? 'working' : 'idle';

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    // (the fixture's needs-input toast would cover the header, so a shot removes it first)
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; await ev(`document.querySelectorAll('.notification-toast, .toast').forEach(e => e.remove()); true`); fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        const open = async (w, mobile, hub) => {
            await Emulation.setDeviceMetricsOverride({ width: w, height: 900, deviceScaleFactor: 1, mobile });
            await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'settings')`);
            await ev(`(async () => {
                const st = window.__coralStatic || '/static/';
                const ws = await import(st + 'websocket.js'); const render = await import(st + 'render.js'); const { state } = await import(st + 'state.js');
                if (state.coralWs) { state.coralWs.onclose = null; state.coralWs.onmessage = null; try { state.coralWs.close(); } catch {} }
                localStorage.clear(); localStorage.setItem('coral-sleeping-expanded:all', 'true');
                state.hub = ${!!hub};
                window.__T = { ws, render, state };
                ws.handleCoralMessage({ type: 'coral_update', servers: ${hub ? "[{ id: 'local', label: 'here', status: 'online' }, { id: 's1', label: 'box-one', status: 'online' }]" : '[]'}, sessions: ${JSON.stringify(SESSIONS.map(x => (hub ? x : { ...x, server: 'local' })))} });
                render.flushLiveSessionsRender(); render.renderLiveSessions(state.liveSessions);
                return true; })()`);
        };
        const ids = () => ev(`[...document.querySelectorAll('#live-sessions-list li.session-group-item[data-session-id], #mobile-session-list li.session-group-item[data-session-id]')].map(li => li.dataset.sessionId)`);
        const count = () => ev(`(document.querySelector('#agent-list-bar .list-bar-count, #mobile-agent-list-bar .list-bar-count') || {}).textContent`);
        const bar = which => which === 'phone' ? '#mobile-agent-list-bar' : '#agent-list-bar';
        const set = (sel, value, which = 'desktop') => ev(`(() => { const e = document.querySelector('${bar(which)} ${sel}'); e.value = ${JSON.stringify(value)}; e.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
        const header = which => which === 'phone' ? '.mobile-list-header' : '.sidebar-header';
        // The filters that are on (the list's own model of them; the sort is not one). There is no summary line under the search (#6597): the dot on the Display button says that one narrows the list.
        const chips = async () => ev(`(async () => { const st = window.__coralStatic || '/static/'; const f = (await import(st + 'agent_filter.js')).getListFilters(); const m = await import(st + 'agent_list_filters.js'); const sb = await import(st + 'server_base.js'); const ls = await import(st + 'launch_server.js'); const { state } = await import(st + 'state.js');
            return m.makeModel({ boardLabel: sb.keyLabel, machineLabel: v => ls.serverLabel(v, state.servers) }).chips(f).map(c => c.label); })()`);
        const noSummary = (which = 'desktop') => ev(`document.querySelector('${header(which)} [data-agent-list-summary]') === null && document.querySelector('${header(which)} .agent-list-summary-clear') === null`);
        const badge = (which = 'desktop') => ev(`(() => { const b = document.querySelector('${header(which)} [data-display-badge]'); return b.hidden ? '' : b.dataset.count; })()`);
        const panelOpen = (which = 'desktop') => ev(`document.querySelector('${header(which)} [data-display-button]').getAttribute('aria-expanded') === 'true' && !document.querySelector('${header(which)} [data-display-panel]').hidden`);
        const toggleBtn = (which = 'desktop') => ev(`document.querySelector('${header(which)} [data-display-button]').click(); true`);
        const settle = () => waitFor(`true`, 'a frame'); // the list redraws synchronously on a change; the next read sees it
        const expectIds = (got, wantSessions, what) => assert.deepEqual([...got].sort(), wantSessions.map(s => s.session_id).sort(), what);
        const byState = st => SESSIONS.filter(s => stateOf(s) === st);
        const sleepKeys = Object.assign({}, ...[...TEAMS, '/repo/folder1', '/repo/folder2', '/repo/folder3', '/repo/folder4', '/repo/folder5'].map(g => ({ ['coral-sleeping-expanded:' + g]: 'true' })));

        console.log('1. The bar under the search box: 30 agents, no filter, the list\'s own order');
        await open(1440, false, false);
        await ev(`for (const [k, v] of Object.entries(${JSON.stringify(sleepKeys)})) localStorage.setItem(k, v); window.__T.render.flushLiveSessionsRender(); window.__T.render.renderLiveSessions(window.__T.state.liveSessions); true`);
        await waitFor(`document.querySelectorAll('#live-sessions-list .session-group-item').length === 30`, 'the list with its 30 agents', { timeout: 8000 });
        assert.equal(await ev(`document.getElementById('agent-list-bar').childElementCount`), 0, 'the filter bar is not drawn while the Display panel stays shut (#4995)');
        assert.equal(await badge(), ''); assert.deepEqual(await chips(), []); assert.equal(await noSummary(), true, 'no summary line under the search, ever');
        assert.equal(await panelOpen(), false, 'the Display expander is shut');
        assert.equal(await ev(`document.querySelector('.sidebar-header [data-display-button]').getAttribute('aria-controls') === document.querySelector('.sidebar-header [data-display-panel]').id`), true, 'the button names its panel');
        assert.equal(await ev(`document.querySelector('.sidebar-header .list-bar-filter-btn')`), null, 'no Filters button any more: the fields are rows of the panel');
        await toggleBtn(); assert.equal(await panelOpen(), true, 'the button opens it in place');
        await waitFor(`!!document.querySelector('#agent-list-bar #al-sort') && document.querySelector('#agent-list-bar .list-bar-count').textContent === '30 agents'`, 'the bar, drawn as the panel opens, with its count', { timeout: 8000 });
        assert.equal(await ev(`document.querySelector('#agent-list-bar #al-sort').value`), 'default');
        assert.equal(await ev(`(() => { const p = document.querySelector('.sidebar-header [data-display-panel]'); return getComputedStyle(p).position === 'static' && p.getBoundingClientRect().width > 150; })()`), true, 'a row of the header, not a popover');
        assert.deepEqual(await ev(`[...document.querySelectorAll('#agent-list-bar .list-bar-field-label')].filter(e => e.getClientRects().length).map(e => e.textContent)`), ['State', 'Board', 'Sort'], 'State and Board (Machine only on a hub), then Sort');
        assert.equal((await ids()).length, 30, 'every agent is in the list (sleeping ones open)');
        const labels = await ev(`[...document.querySelectorAll('#agent-list-bar #al-state option')].map(o => o.textContent)`);
        assert.deepEqual(labels, ['Any state', 'Needs input', 'Working', 'Idle', 'Asleep']);
        assert.equal(await ev(`getComputedStyle(document.querySelector('#agent-list-bar #al-machine').closest('label')).display`), 'none', 'a local Coral has one machine: no Machine field');
        assert.deepEqual(await ev(`[...document.querySelectorAll('#agent-list-bar #al-board option')].map(o => o.value)`), ['', ...TEAMS], 'the boards there are');

        console.log('2. Each state, with the count and the chip');
        for (const [val, label, st] of [['needs_input', 'Needs input', 'needs_input'], ['working', 'Working', 'working'], ['idle', 'Idle', 'idle'], ['asleep', 'Asleep', 'asleep']]) {
            await set('#al-state', val);
            expectIds(await ids(), byState(st), `state ${val}`);
            assert.equal(await count(), `${byState(st).length} of 30 agents`, `the count for ${val}`);
            assert.deepEqual(await chips(), ['State: ' + label]); assert.equal(await badge(), '1');
        }
        assert.ok(byState('needs_input').length >= 4 && byState('working').length >= 4 && byState('idle').length >= 4 && byState('asleep').length >= 3, 'the fixture has agents in every state');

        // The count is plain text; a hidden status region says it only after the person changed a filter, and not again when the list changes under them (agents coming and going).
        assert.equal(await ev(`document.querySelector('#agent-list-bar .list-bar-count').getAttribute('role')`), null, 'the count is not a live region');
        await waitFor(`document.querySelector('#agent-list-bar .list-bar-live').textContent === ${JSON.stringify(`${byState('asleep').length} of 30 agents`)}`, 'the region says the count after the last filter change', { timeout: 4000 });
        await ev(`window.__live = 0; new MutationObserver(m => { window.__live += m.length; }).observe(document.querySelector('#agent-list-bar .list-bar-live'), { childList: true, characterData: true, subtree: true });
            window.__T.render.flushLiveSessionsRender(); window.__T.render.renderLiveSessions(window.__T.state.liveSessions); true`);
        await settle();
        assert.equal(await ev(`window.__live`), 0, 'a redraw of the list without a change by the person is not announced again');
        await shot('agent-list-filters-open-desktop');
        await toggleBtn(); assert.equal(await noSummary(), true, 'shut with a filter on: no summary line'); assert.equal(await badge(), '1', 'the dot says a filter is on');
        await shot('agent-list-filters-closed-desktop');
        await toggleBtn();
        console.log('3. A board, and a board with a state');
        await set('#al-state', '');
        await set('#al-board', 'team-b');
        const teamB = SESSIONS.filter(s => s.board_project === 'team-b');
        expectIds(await ids(), teamB, 'board team-b'); assert.deepEqual(await chips(), ['Board: team-b']);
        await set('#al-state', 'working');
        expectIds(await ids(), teamB.filter(s => stateOf(s) === 'working'), 'team-b and working');
        assert.deepEqual(await chips(), ['State: Working', 'Board: team-b']); assert.equal(await badge(), '2');
        assert.equal(await count(), `${teamB.filter(s => stateOf(s) === 'working').length} of 30 agents`);

        console.log('4. The search text and the filters together');
        await ev(`{ const i = document.getElementById('agent-search-input'); i.value = 'agent 1'; i.dispatchEvent(new Event('input', { bubbles: true })); } true`);
        const withText = SESSIONS.filter(s => s.board_project === 'team-b' && stateOf(s) === 'working' && s.display_name.toLowerCase().includes('agent 1'));
        expectIds(await ids(), withText, 'text + state + board');
        assert.equal(await count(), `${withText.length} of 30 agents`);
        await ev(`{ const i = document.getElementById('agent-search-input'); i.value = ''; i.dispatchEvent(new Event('input', { bubbles: true })); } true`);

        console.log('5. Sort: name, recent activity, cost (within each group)');
        await ev(`document.querySelector('#agent-list-bar .list-bar-clear-all').click(); true`);
        assert.equal(await count(), '30 agents'); assert.deepEqual(await chips(), []);
        // A team's awake agents come first and its sleeping ones after (a section of their own): each part is in the chosen order.
        const parts = async team => { const all = await ids(); const mine = new Set(SESSIONS.filter(s => s.board_project === team).map(s => s.session_id)); const list = all.filter(id => mine.has(id)).map(id => SESSIONS.find(s => s.session_id === id)); return [list.filter(x => !x.sleeping), list.filter(x => x.sleeping)]; };
        const within = async team => (await parts(team))[0];
        const nonDecreasing = (arr, key, what) => arr.forEach((x, i) => { if (i) assert.ok(key(arr[i - 1]) <= key(x), `${what}: ${i}`); });
        const eachPart = async (key, what) => { for (const t of TEAMS) for (const part of await parts(t)) nonDecreasing(part, key, what + ' in ' + t); };
        await set('#al-sort', 'name');
        await eachPart(x => x.display_name.toLowerCase(), 'by name');
        const byNameFirst = (await within('team-a')).map(x => x.display_name);
        assert.deepEqual(byNameFirst, [...byNameFirst].sort((a, b) => a.toLowerCase().localeCompare(b.toLowerCase())));
        await set('#al-sort', 'recent');
        await eachPart(x => x.staleness_seconds, 'most recent first');
        await set('#al-sort', 'cost');
        await eachPart(x => -x.token_cost_usd, 'dearest first');
        assert.equal(await count(), '30 agents', 'a sort does not change the count'); assert.deepEqual(await chips(), [], 'and is not a filter');
        assert.equal(await badge(), '', 'a sort does not narrow the list: no dot');
        await set('#al-sort', 'default');
        const defaultOrder = await ids();
        await set('#al-sort', 'cost'); assert.notDeepEqual(await ids(), defaultOrder, 'the sort changed the order');
        await set('#al-sort', 'default'); assert.deepEqual(await ids(), defaultOrder, 'and the list order comes back');

        console.log('6. Clearing: the panel\'s Clear filters (keeps the sort) and the summary line\'s Clear (everything)');
        await set('#al-state', 'working'); await set('#al-board', 'team-a');
        assert.deepEqual(await chips(), ['State: Working', 'Board: team-a']);
        assert.equal(await ev(`getComputedStyle(document.querySelector('#agent-list-bar .list-bar-clear-all')).display`) !== 'none', true, 'Clear filters shows while a filter is on');
        await set('#al-sort', 'cost');
        await ev(`document.querySelector('#agent-list-bar .list-bar-clear-all').click(); true`);
        assert.deepEqual(await chips(), []); assert.equal(await count(), '30 agents'); assert.equal((await ids()).length, 30);
        assert.equal(await ev(`document.querySelector('#agent-list-bar #al-state').value + '|' + document.querySelector('#agent-list-bar #al-board').value`), '|', 'the fields are empty again');
        assert.equal(await ev(`document.querySelector('#agent-list-bar #al-sort').value`), 'cost', 'Clear filters keeps the sort (it is not a filter)');
        assert.equal(await ev(`getComputedStyle(document.querySelector('#agent-list-bar .list-bar-clear-all')).display`), 'none', 'and goes away with nothing to clear');
        await set('#al-state', 'asleep'); await toggleBtn();
        assert.equal(await badge(), '1', 'shut: the dot counts the filter, not the sort'); assert.equal(await noSummary(), true);
        await toggleBtn();
        await ev(`document.querySelector('#agent-list-bar .list-bar-clear-all').click(); true`);
        assert.deepEqual(await chips(), []); assert.equal(await count(), '30 agents'); assert.equal(await badge(), '', 'cleared: no dot');
        await set('#al-sort', 'default');

        console.log('7. With Collapse all: a filter shows its matches in collapsed groups, and clearing it folds them again');
        const groupToggle = `document.querySelector('.sidebar-header [data-group-toggle]')`;
        assert.equal(await ev(`${groupToggle}.hidden === false && ${groupToggle}.getAttribute('aria-label')`), 'Collapse all groups', 'one toggle in the header, beside the Display button');
        await ev(`${groupToggle}.click(); true`);
        assert.equal(await ev(`document.querySelectorAll('#live-sessions-list .session-group-header[aria-expanded="true"]').length`), 0, 'every group is folded');
        await waitFor(`${groupToggle}.getAttribute('aria-label') === 'Expand all groups'`, 'the toggle now offers the way back');
        await set('#al-state', 'needs_input');
        assert.equal(await ev(`document.querySelectorAll('#live-sessions-list .session-group-header[aria-expanded="false"]').length`), 0, 'while a filter is on its matches show');
        expectIds(await ids(), byState('needs_input'), 'the needs-input agents are all in the list');
        await set('#al-state', '');
        assert.equal(await ev(`document.querySelectorAll('#live-sessions-list .session-group-header[aria-expanded="true"]').length`), 0, 'cleared: folded again');
        await ev(`${groupToggle}.click(); true`);
        assert.equal(await ev(`document.querySelectorAll('#live-sessions-list .session-group-header[aria-expanded="false"]').length`), 0, 'the second press opens them all');

        console.log('8. Long names: the row and the bar do not scroll sideways');
        const over = await ev(`(() => { const l = document.getElementById('live-sessions-list'); const b = document.getElementById('agent-list-bar'); const sb = document.querySelector('.sidebar'); return { list: l.scrollWidth - l.clientWidth, bar: b.scrollWidth - b.clientWidth, sidebar: sb ? sb.scrollWidth - sb.clientWidth : 0 }; })()`);
        assert.ok(over.list <= 1 && over.bar <= 1 && over.sidebar <= 1, JSON.stringify(over));
        await set('#al-state', 'working');
        assert.ok((await ev(`document.getElementById('agent-list-bar').scrollWidth - document.getElementById('agent-list-bar').clientWidth`)) <= 1, 'with a filter on too');
        await set('#al-state', '');

        console.log('9. A hub: the Machine field, and a machine with a board');
        await open(1440, false, true);
        await ev(`for (const [k, v] of Object.entries(${JSON.stringify(sleepKeys)})) localStorage.setItem(k, v); window.__T.render.flushLiveSessionsRender(); window.__T.render.renderLiveSessions(window.__T.state.liveSessions); true`);
        await waitFor(`document.querySelector('#agent-list-bar .list-bar-count') && document.querySelector('#agent-list-bar .list-bar-count').textContent === '30 agents'`, 'the hub bar', { timeout: 8000 });
        assert.equal(await ev(`getComputedStyle(document.querySelector('#agent-list-bar #al-machine').closest('label')).display`) !== 'none', true, 'the Machine field shows on a hub');
        assert.deepEqual(await ev(`[...document.querySelectorAll('#agent-list-bar #al-machine option')].map(o => o.value)`), ['', 'local', 's1']);
        assert.deepEqual(await ev(`[...document.querySelectorAll('#agent-list-bar #al-machine option')].map(o => o.textContent)`), ['Any machine', 'Local', 'box-one']);
        await set('#al-machine', 's1');
        const onS1 = SESSIONS.filter(s => s.server === 's1');
        expectIds(await ids(), onS1, 'machine s1'); assert.deepEqual(await chips(), ['Machine: box-one']); assert.equal(await count(), `${onS1.length} of 30 agents`);
        await set('#al-state', 'working');
        expectIds(await ids(), onS1.filter(s => stateOf(s) === 'working'), 'machine and state');
        await set('#al-machine', 'local'); await set('#al-state', '');
        expectIds(await ids(), SESSIONS.filter(s => s.server !== 's1'), 'machine local');

        console.log('10. A phone: the same expander in the Agents page\'s header, at touch size');
        await open(390, true, false);
        await ev(`window.syncMobileAgentList && window.syncMobileAgentList(); window.__T.render.flushLiveSessionsRender(); window.__T.render.renderLiveSessions(window.__T.state.liveSessions); true`);
        await waitFor(`!!document.querySelector('.mobile-list-header [data-display-button]') && !!document.getElementById('mobile-agent-list-bar')`, 'the phone header', { timeout: 8000 });
        assert.equal(await ev(`document.getElementById('mobile-agent-list-bar').childElementCount`), 0, 'the phone\'s bar is not drawn either until the panel opens');
        assert.equal(await panelOpen('phone'), false); await toggleBtn('phone'); assert.equal(await panelOpen('phone'), true);
        await waitFor(`!!document.querySelector('#mobile-agent-list-bar #alm-sort')`, 'the phone bar, drawn as the panel opens', { timeout: 8000 });
        assert.equal(await ev(`document.querySelector('#mobile-agent-list-bar .list-bar-count').textContent`), '30 agents');
        const touch = await ev(`(() => { const r = e => Math.round(document.querySelector(e).getBoundingClientRect().height); return { button: r('.mobile-list-header [data-display-button]'), toggle: r('.mobile-list-header [data-group-toggle]'), state: r('#alm-state'), sort: r('#alm-sort'), option: r('.mobile-list-header .list-display-option') }; })()`);
        assert.ok(Object.values(touch).every(h => h >= 44), 'targets at least 44 px: ' + JSON.stringify(touch));
        await set('#alm-state', 'needs_input', 'phone');
        assert.equal(await count(), `${byState('needs_input').length} of 30 agents`); assert.deepEqual(await chips('phone'), ['State: Needs input']);
        await shot('agent-list-filters-open-phone');
        await toggleBtn('phone'); assert.equal(await noSummary('phone'), true, 'shut: no summary line'); assert.equal(await badge('phone'), '1', 'the dot says a filter is on'); await shot('agent-list-filters-closed-phone');
        assert.equal(await ev(`document.documentElement.scrollWidth - document.documentElement.clientWidth`) <= 0, true, 'nothing scrolls sideways on the phone');
        await toggleBtn('phone');
        await ev(`document.querySelector('#mobile-agent-list-bar .list-bar-clear-all').click(); true`);
        assert.equal(await count(), '30 agents');
        console.log('11. Large text (200%) in the sidebar: the count dot stays off the icon, each select sits under its label');
        await open(1440, false, false);
        await ev(`(() => { const st = document.createElement('style'); st.textContent = 'html{font-size:200% !important}'; document.head.appendChild(st); })()`);
        await ev(`for (const [k, v] of Object.entries(${JSON.stringify(sleepKeys)})) localStorage.setItem(k, v); window.__T.render.flushLiveSessionsRender(); window.__T.render.renderLiveSessions(window.__T.state.liveSessions); true`);
        await toggleBtn(); // the bar is drawn as the panel opens (#4995)
        await waitFor(`!!document.querySelector('#agent-list-bar #al-state')`, 'the bar at 200%', { timeout: 8000 });
        await set('#al-state', 'needs_input'); await set('#al-board', 'team-a');
        await toggleBtn(); // shut: the dot is what is measured first (there is no summary line, #6597)
        const big = await ev(`(() => { const r = e => { const b = e.getBoundingClientRect(); return { l: b.left, t: b.top, r: b.right, b: b.bottom }; };
            const btn = document.querySelector('.sidebar-header [data-display-button]'); const icon = btn.querySelector('.material-icons'); const dot = btn.querySelector('[data-display-badge]');
            const a = r(icon), d = r(dot); const ow = Math.max(0, Math.min(a.r, d.r) - Math.max(a.l, d.l)), oh = Math.max(0, Math.min(a.b, d.b) - Math.max(a.t, d.t));
            return { dotShown: !dot.hidden && d.r > d.l, overlap: Math.round(ow * oh) / Math.round((a.r - a.l) * (a.b - a.t)), name: btn.getAttribute('aria-label') }; })()`);
        assert.ok(big.dotShown, 'the count dot is shown'); assert.ok(big.overlap < 0.15, 'and does not cover the icon: ' + big.overlap);
        assert.equal(big.name, 'Display options, 2 filters on', 'the button names how many filters are on');
        await toggleBtn();
        const stacked = await ev(`(() => [...document.querySelectorAll('#agent-list-bar .list-bar-field')].filter(f => f.getClientRects().length).map(f => { const l = f.querySelector('.list-bar-field-label').getBoundingClientRect(), s = f.querySelector('select').getBoundingClientRect(), p = document.querySelector('.sidebar-header [data-display-panel]').getBoundingClientRect();
            return { under: s.top >= l.bottom - 1, wide: s.width, inside: s.left >= p.left - 1 && s.right <= p.right + 1 }; }))()`);
        assert.ok(stacked.length >= 3 && stacked.every(x => x.under && x.inside && x.wide >= 150), 'each select is under its label, whole and wide enough to read: ' + JSON.stringify(stacked));
        await shot('agent-list-filters-open-200');
        await toggleBtn(); await shot('agent-list-filters-closed-200');
        console.log('PASS agent list filters');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
