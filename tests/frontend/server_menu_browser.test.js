// The server row's menu in the agent list (Group by server, #4185): its kebab sits in the same column, at the same size,
// as the team and folder rows' kebabs (desktop and a 375 px phone), and its menu opens with Launch team, Open folder and
// Launch agent, each opening the launch flow with that server already chosen; a server that cannot launch has them
// disabled with the reason (an unreachable one; Open folder for a remote, whose pins live on this Coral).
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

const NOW = new Date().toISOString();
const SERVERS = [
    { id: 'm1', label: 'studio-mac.local', status: 'online', version: '1.4.6', last_seen: NOW },
    { id: 'm2', label: 'workstation.lan', status: 'online', version: '1.4.6', last_seen: NOW, update: 'required', latest_coral: '1.5.0', coral_version: '1.3.9' },
    { id: 'm3', label: 'Old Laptop', status: 'unreachable', version: '1.3.9', last_seen: '2026-10-01T00:00:00Z', last_error: 'dial tcp: timeout' },
];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();

    // cloud=true: the list is about the machines only. cloud=false: the hub itself is a server too (the Open folder case).
    const scene = (cloud) => ev(`(async () => {
        T.state.hub = true; T.state.cloud = ${cloud};
        T.state.servers = ${JSON.stringify(SERVERS)};
        T.state.liveSessions = []; window.__m.agents = {};
        ${JSON.stringify(SERVERS.map(s => s.id))}.concat(${cloud ? '[]' : "['local']"}).forEach(id => {
            window.__m.add('solo-' + id, 'S', 3, { sleeping: false, server: id });
            window.__m.add('team-' + id, 'T', 3, { sleeping: false, server: id, board_project: 'team-' + id });
        });
        localStorage.setItem('coral-group-by-server', 'true');
        T.render.renderLiveSessions(T.state.liveSessions);
        return true;
    })()`);

    // The kebab of the server, team and folder header under `root`, as boxes.
    const kebabs = (root) => ev(`(() => {
        const box = el => { if (!el) return null; const r = el.getBoundingClientRect(); return { left: Math.round(r.left * 10) / 10, right: Math.round(r.right * 10) / 10, width: Math.round(r.width * 10) / 10, height: Math.round(r.height * 10) / 10 }; };
        const root = document.querySelector(${JSON.stringify(root)});
        const q = sel => box(root.querySelector(sel));
        const agent = inTeam => box([...root.querySelectorAll('li.session-group-item')].find(x => !!x.closest('.board-card-agents') === inTeam)?.querySelector('.sidebar-kebab-btn'));
        return {
            teamAgent: agent(true), folderAgent: agent(false),
            server: q('li.server-group-header[data-server-group="m1"] .server-group-more-btn'),
            team: q('.session-group-header.board-card-header .group-kebab-btn'),
            folder: q('.session-group-header:not(.board-card-header) .group-kebab-btn'),
        };
    })()`);

    const closeFlows = () => ev(`(() => { window.hideLaunchModal(); window.hideOpenFolderModal(); document.querySelectorAll('details[data-server-details]').forEach(d => { d.open = false; }); return true; })()`);
    const openMenu = (id) => ev(`(() => { const d = document.querySelector('#live-sessions-list li.server-group-header[data-server-group="${id}"] details[data-server-details]'); d.open = true; return true; })()`);
    const launchState = () => ev(`(() => {
        const shown = id => { const e = document.getElementById(id); return !!e && getComputedStyle(e).display !== 'none'; };
        return { launch: shown('launch-modal'), team: shown('launch-step-team'), agent: shown('launch-step-agent'), chooser: shown('launch-step-chooser'),
            server: document.getElementById('launch-server').value, folder: shown('open-folder-modal') };
    })()`);
    const click = async (id, kind) => {
        await ev(`(() => { document.querySelector('#live-sessions-list li.server-group-header[data-server-group="${id}"] [data-server-launch="${kind}"]').click(); return true; })()`);
        await waitFor(`['launch-modal', 'open-folder-modal'].some(id => { const e = document.getElementById(id); return !!e && getComputedStyle(e).display !== 'none'; })`, 'a launch flow opens', 3000);
    };

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await require('./helpers/wait').bind(ev).coralReady('startup', 'settings');
        await ev(`localStorage.clear(); true`);

        console.log('1. Desktop: the server kebab is in the same column, same size, as the team and folder kebabs');
        await scene(true);
        let k = await kebabs('#live-sessions-list');
        assert.ok(k.server && k.team && k.folder, `all three kebabs present (${JSON.stringify(k)})`);
        assert.deepEqual([k.server.right, k.server.width, k.server.height], [k.team.right, k.team.width, k.team.height], `server vs team kebab (${JSON.stringify(k)})`);
        assert.deepEqual([k.server.right, k.server.width, k.server.height], [k.folder.right, k.folder.width, k.folder.height], `server vs folder kebab (${JSON.stringify(k)})`);
        assert.equal(k.server.left, k.team.left, 'the same x position');
        // An agent row's kebab is a wider button: its centre is in the column too.
        const centre = b => (b.left + b.right) / 2;
        for (const a of ['teamAgent', 'folderAgent']) assert.ok(Math.abs(centre(k[a]) - centre(k.server)) <= 1, `${a} kebab centre ${centre(k[a])} vs server ${centre(k.server)}`);
        console.log(`   ✓ x ${k.server.left}, ${k.server.width} x ${k.server.height} px for all three`);

        console.log('2. The menu opens with one launch row (Launch agent, Launch team, Open in Servers), a hint for the folder, then the details');
        await openMenu('m1');
        const menu = await ev(`(() => { const p = document.querySelector('#live-sessions-list li.server-group-header[data-server-group="m1"] .server-switcher-panel');
            return { first: [...p.children].map(c => c.className.split(' ')[0]), actions: [...p.querySelectorAll('[data-server-launch]')].map(b => ({ kind: b.dataset.serverLaunch, label: b.firstChild.textContent.trim(), disabled: b.disabled })), row: [...p.querySelector('.server-launch-actions').children].map(b => b.textContent.trim()), hint: (p.querySelector('.server-launch-hint') || {}).textContent || '', collapse: [...p.querySelectorAll('button')].some(b => /^(Collapse|Expand)$/.test(b.textContent.trim())), hasDetails: !!p.querySelector('.server-details-list') }; })()`);
        assert.equal(menu.first[0], 'server-launch-actions', `the launch actions come first (${menu.first})`);
        assert.deepEqual(menu.actions.map(a => a.label), ['New agent', 'New team']);
        assert.deepEqual(menu.actions.map(a => a.kind), ['agent', 'team']);
        assert.deepEqual(menu.row, ['New agent', 'New team', 'Open in Servers'], 'one row: the two launches and Open in Servers as its last item');
        assert.equal(menu.hint, 'To use a folder, choose it when you launch an agent.', 'a remote has no Open folder: a one-line hint says where the folder is chosen');
        assert.equal(menu.collapse, false, 'no Collapse button in the menu: the header toggles the group');
        assert.ok(menu.hasDetails, 'the details stay');
        console.log('   ✓ agent, team and Open in Servers in one row, the folder hint, no Collapse; details after');

        console.log('3. Launch team and Launch agent open the launch flow with that server chosen');
        await click('m1', 'team');
        let st = await launchState();
        assert.deepEqual([st.launch, st.team, st.chooser, st.server], [true, true, false, 'm1'], `Launch team on m1 (${JSON.stringify(st)})`);
        await closeFlows();
        await openMenu('m2');
        await click('m2', 'agent');
        st = await launchState();
        assert.deepEqual([st.launch, st.agent, st.chooser, st.server], [true, true, false, 'm2'], `Launch agent on m2 (${JSON.stringify(st)})`);
        assert.equal(await ev(`document.querySelector('li.server-group-header[data-server-group="m2"] details').open`), false, 'the menu closes when an action is chosen');
        await closeFlows();
        console.log('   ✓ team on m1, agent on m2: the dropdown shows the chosen server, past the chooser');

        console.log('4. A server that cannot launch has the launches disabled and the reason said once; a click does nothing');
        await openMenu('m3');
        const off = await ev(`[...document.querySelectorAll('#live-sessions-list li.server-group-header[data-server-group="m3"] [data-server-launch]')].map(b => ({ kind: b.dataset.serverLaunch, disabled: b.disabled, aria: b.getAttribute('aria-disabled'), why: b.getAttribute('title') || '' }))`);
        assert.equal(off.length, 2);
        assert.ok(off.every(o => o.disabled && o.aria === 'true' && /unavailable \(offline\)/.test(o.why)), `both launches disabled, with the reason (${JSON.stringify(off)})`);
        assert.match(await ev(`document.querySelector('#live-sessions-list li.server-group-header[data-server-group="m3"] .server-launch-note').textContent`), /unavailable \(offline\)/, 'the reason is said once under the row');
        assert.equal(await ev(`document.querySelector('#live-sessions-list li.server-group-header[data-server-group="m3"] .server-open-btn').disabled`), false, 'Open in Servers still works for an offline server');
        await ev(`document.querySelector('#live-sessions-list li.server-group-header[data-server-group="m3"] [data-server-launch="team"]').click(); true`);
        st = await launchState();
        assert.equal(st.launch, false, 'nothing opened');
        // Open folder is not offered for a remote: pins live on the hub, so a hint replaces the button even when the server is online.
        await closeFlows();
        await openMenu('m1');
        assert.equal(await ev(`document.querySelector('#live-sessions-list li.server-group-header[data-server-group="m1"] [data-server-launch="folder"]')`), null, 'no Open folder on a remote');
        await closeFlows();
        console.log('   ✓ offline server: both launches disabled, the reason once; no Open folder on a remote');

        console.log('5. The hub itself: Open folder opens the folder dialog; Launch team preselects it');
        await scene(false);
        await openMenu('local');
        const lf = await ev(`document.querySelector('#live-sessions-list li.server-group-header[data-server-group="local"] [data-server-launch="folder"]').disabled`);
        assert.equal(lf, false, 'Open folder is enabled on the hub');
        await click('local', 'folder');
        st = await launchState();
        assert.equal(st.folder, true, `the Open folder dialog is shown (${JSON.stringify(st)})`);
        await closeFlows();
        await openMenu('local');
        await click('local', 'team');
        st = await launchState();
        assert.deepEqual([st.launch, st.team, st.server], [true, true, 'local'], `Launch team on the hub (${JSON.stringify(st)})`);
        await closeFlows();
        console.log('   ✓ folder dialog and team form, hub chosen');

        console.log('6. Phone 375 px: the same column and size, 44 px, the menu fits');
        await Emulation.setDeviceMetricsOverride({ width: 375, height: 812, deviceScaleFactor: 2, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
        await Emulation.setEmitTouchEventsForMouse({ enabled: true, configuration: 'mobile' });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await scene(true);
        await ev(`(async () => { const mob = await import((window.__coralStatic || '/static/') + 'mobile.js'); mob.syncMobileAgentList(); return true; })()`);
        await waitFor(`!!document.querySelector('#mobile-agent-list li.server-group-header')`, 'phone server header', 3000);
        k = await kebabs('#mobile-agent-list');
        assert.ok(k.server && k.team && k.folder, `phone: all three kebabs present (${JSON.stringify(k)})`);
        assert.deepEqual([k.server.right, k.server.width, k.server.height], [k.team.right, k.team.width, k.team.height], `phone: server vs team kebab (${JSON.stringify(k)})`);
        assert.deepEqual([k.server.right, k.server.width, k.server.height], [k.folder.right, k.folder.width, k.folder.height], `phone: server vs folder kebab (${JSON.stringify(k)})`);
        for (const a of ['teamAgent', 'folderAgent']) assert.ok(Math.abs((k[a].left + k[a].right) / 2 - (k.server.left + k.server.right) / 2) <= 1, `phone: ${a} kebab in the column (${JSON.stringify(k)})`);
        assert.ok(k.server.width >= 44 && k.server.height >= 44, `phone: a 44 px hit area (${JSON.stringify(k.server)})`);
        const fit = await ev(`(() => {
            document.querySelector('#mobile-agent-list li.server-group-header[data-server-group="m1"] details').open = true;
            const p = document.querySelector('#mobile-agent-list li.server-group-header[data-server-group="m1"] .server-switcher-panel').getBoundingClientRect();
            const btns = [...document.querySelectorAll('#mobile-agent-list li.server-group-header[data-server-group="m1"] [data-server-launch]')].map(b => Math.round(b.getBoundingClientRect().height));
            return { left: p.left, right: p.right, vw: innerWidth, btns, overflow: document.documentElement.scrollWidth - innerWidth };
        })()`);
        assert.ok(fit.left >= 0 && fit.right <= fit.vw, `phone: the menu is on screen (${JSON.stringify(fit)})`);
        assert.ok(fit.btns.length === 2 && fit.btns.every(h => h >= 44), `phone: each action is 44 px tall (${fit.btns})`);
        assert.equal(fit.overflow, 0, 'phone: no horizontal overflow');
        await Emulation.setTouchEmulationEnabled({ enabled: false });
        await Emulation.clearDeviceMetricsOverride();
        console.log(`   ✓ kebabs ${k.server.width} x ${k.server.height} px in one column; menu on screen; actions ${fit.btns.join('/')} px`);

        console.log('\nAll server menu checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
