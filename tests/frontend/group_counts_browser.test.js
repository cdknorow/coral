// A count of the agents a group holds sits on its header, in the sidebar and the phone list, for folders, teams and (grouped by
// server) servers (#4005). It used to be hidden in the sidebar, so a collapsed folder said nothing about what was inside. Checks:
// a collapsed folder shows its number; starting or stopping an agent changes it on the next render; expanding shows the agents (and
// the number stays); a team's header and a server group's header carry theirs; the header's other controls are still there. 390 px
// too. Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        const SERVERS = [{ id: 'm1', label: 'studio-mac', status: 'online' }, { id: 'm2', label: 'workstation', status: 'online' }];
        for (const [dev, w, h, mobile] of [['desktop', 1280, 800, false], ['phone', 390, 844, true]]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            const list = dev === 'phone' ? '#mobile-agent-list' : '#live-sessions-list';
            // One synchronous step: build the list, render it, read the headers (the page's own poll cannot slip in between).
            const run = (agentsInA, collapsedGroups, grouped = false) => ev(`(() => {
                localStorage.setItem('coral_collapsed_groups', ${JSON.stringify(JSON.stringify(collapsedGroups))});
                if (${grouped}) localStorage.setItem('coral-group-by-server', 'true'); else localStorage.removeItem('coral-group-by-server');
                T.state.hub = true; T.state.cloud = true; T.state.servers = ${JSON.stringify(SERVERS)};
                window.__m.agents = {};
                const add = (name, sid, extra = {}) => window.__m.add(name, 'A', 1, { sleeping: false, server: 'm1', session_id: sid, ...extra });
                for (let i = 0; i < ${agentsInA}; i++) add('proj-a', 'sid-a' + i);
                add('proj-b', 'sid-b0'); add('t1', 'sid-t1', { board_project: 'team-x' }); add('t2', 'sid-t2', { board_project: 'team-x' }); add('t3', 'sid-t3', { board_project: 'team-x' });
                add('w1', 'sid-w1', { server: 'm2' });
                window.__LIVE = Object.values(window.__m.agents).map(x => window.__m.entry(x));
                T.state.liveSessions = window.__LIVE;
                T.render.renderLiveSessions(window.__LIVE);
                if (${dev === 'phone'}) window.syncMobileAgentList && window.syncMobileAgentList();
                const root = document.querySelector(${JSON.stringify(list)});
                const vis = e => !!e && e.getClientRects().length > 0 && getComputedStyle(e).display !== 'none';
                const heads = {};
                for (const hd of root.querySelectorAll('.session-group-header, .server-group-toggle')) {
                    const key = hd.dataset.groupName || ('server:' + (hd.dataset.serverToggle || ''));
                    const pill = hd.querySelector('.session-group-count');
                    heads[key] = { count: pill ? pill.textContent.trim() : null, shown: pill ? vis(pill) : false, expanded: hd.getAttribute('aria-expanded'), kebab: !!hd.querySelector('.group-kebab-btn, .board-card-info-btn, button, svg') };
                }
                const rowsOf = n => [...root.querySelectorAll('li[data-session-id]')].filter(li => (li.dataset.sessionId || '').startsWith(n)).length;
                return { heads, aRows: rowsOf('sid-a') };
            })()`);
            // a collapsed folder shows its number, and the agents are hidden
            const folder = k => Object.keys(k).find(n => /proj-a$/.test(n));
            let v = await run(4, ['m1:proj-a', 'proj-a'].concat([]));
            let key = Object.keys(v.heads).find(n => /proj-a/.test(n));
            assert.ok(key, `${dev}: the folder header is in the list: ${Object.keys(v.heads)}`);
            // collapse by the header's own group name, then render again
            v = await run(4, [key]);
            assert.equal(v.heads[key].expanded, 'false', `${dev}: the folder is collapsed`);
            assert.deepEqual([v.heads[key].count, v.heads[key].shown], ['4', true], `${dev}: a collapsed folder with 4 agents shows 4`);
            assert.equal(v.aRows, 0, `${dev}: and its agents are folded away`);
            v = await run(5, [key]); assert.equal(v.heads[key].count, '5', `${dev}: starting an agent makes it 5`);
            v = await run(3, [key]); assert.equal(v.heads[key].count, '3', `${dev}: stopping two makes it 3`);
            v = await run(3, []);
            assert.equal(v.heads[key].expanded, 'true'); assert.deepEqual([v.heads[key].count, v.heads[key].shown], ['3', true], `${dev}: expanded, the number stays`);
            assert.equal(v.aRows, 3, `${dev}: and the agents show`);
            const team = Object.keys(v.heads).find(n => /team-x/.test(n));
            assert.deepEqual([v.heads[team].count, v.heads[team].shown], ['3', true], `${dev}: the team header says 3`);
            assert.ok(v.heads[team].kebab, `${dev}: the team header keeps its other controls`);
            // grouped by server: each server's header carries its agents
            v = await run(3, [], true);
            const servers = Object.entries(v.heads).filter(([n]) => n.startsWith('server:'));
            assert.ok(servers.length >= 2, `${dev}: two server groups (${Object.keys(v.heads)})`);
            assert.ok(servers.every(([, s]) => s.shown && /^\d+$/.test(s.count)), `${dev}: each server group header shows a count: ${JSON.stringify(servers)}`);
            await ev(`localStorage.removeItem('coral_collapsed_groups'); localStorage.removeItem('coral-group-by-server'); true`);
        }
        console.log('PASS group counts: collapsed folder shows N, follows start and stop, stays when expanded; teams and server groups too; sidebar and phone');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
