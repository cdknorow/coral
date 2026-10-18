// Collapse all / Expand all (#4746, the operator-approved mockup): two buttons at the bottom of the agent list's Display options panel, in the sidebar
// and on the phone (a 44 px target). With 10 teams: Collapse all folds every team, Expand all opens them; a team opened or closed by hand keeps its
// state until one of the buttons is used; the choice is stored like a single collapse (it survives a render and a reload); the buttons have names and
// the headers' aria-expanded follow. Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [dev, w, h, mobile] of [['desktop', 1280, 800, false], ['phone', 390, 844, true]]) {
            console.log(dev);
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            const root = dev === 'phone' ? '#mobile-agent-list' : '#live-sessions-list';
            const build = () => ev(`(() => { localStorage.removeItem('coral_collapsed_groups'); window.__m.agents = {};
                for (let t = 1; t <= 10; t++) for (let a = 1; a <= 2; a++) window.__m.add('t' + t + 'a' + a, 'A', 1, { sleeping: false, session_id: 'sid-' + t + '-' + a, board_project: 'team-' + t });
                window.__LIVE = Object.values(window.__m.agents).map(x => window.__m.entry(x)); T.state.liveSessions = window.__LIVE; T.render.renderLiveSessions(window.__LIVE);
                if (${dev === 'phone'}) window.syncMobileAgentList && window.syncMobileAgentList(); return true; })()`);
            const state = () => ev(`(() => { const r = document.querySelector(${JSON.stringify(root)}); const vis = e => e.getClientRects().length > 0 && getComputedStyle(e).display !== 'none'; const heads = [...r.querySelectorAll('.session-group-header[data-group-name]')];
                return { heads: heads.length, collapsed: heads.filter(h => h.getAttribute('aria-expanded') === 'false').length, rows: [...r.querySelectorAll('li[data-session-id]')].filter(vis).length, stored: JSON.parse(localStorage.getItem('coral_collapsed_groups') || '[]').length }; })()`);
            const panel = () => `${dev === 'phone' ? '.mobile-list-header' : '.sidebar-header'} .list-display-actions`;
            const press = async name => { await ev(`(() => { const d = document.querySelector('${dev === 'phone' ? '.mobile-list-header' : '.sidebar-header'} [data-list-display-menu]'); d.open = true; [...document.querySelectorAll(${JSON.stringify(panel())} + ' button')].find(b => b.textContent.trim() === ${JSON.stringify(name)}).click(); return true; })()`); };
            await build();
            let s = await state();
            assert.deepEqual([s.heads, s.collapsed, s.rows], [10, 0, 20], dev + ': 10 teams, all open: ' + JSON.stringify(s));

            const b = await ev(`(() => { document.querySelector('${dev === 'phone' ? '.mobile-list-header' : '.sidebar-header'} [data-list-display-menu]').open = true; return [...document.querySelectorAll(${JSON.stringify(panel())} + ' button')].map(x => ({ name: x.textContent.trim(), h: Math.round(x.getBoundingClientRect().height), w: Math.round(x.getBoundingClientRect().width) })); })()`);
            assert.deepEqual(b.map(x => x.name), ['Collapse all', 'Expand all'], dev + ': the two buttons are in the panel and named');
            if (dev === 'phone') assert.ok(b.every(x => x.h >= 44 && x.w >= 44), dev + ': 44 px targets ' + JSON.stringify(b));

            await ev(`window.toggleGroupCollapse(document.querySelector(${JSON.stringify(root)} + ' .session-group-header[data-group-name]').dataset.groupName); true`);
            s = await state(); assert.equal(s.collapsed, 1, dev + ': one team closed by hand');
            await press('Collapse all');
            s = await state(); assert.deepEqual([s.collapsed, s.rows, s.stored], [10, 0, 10], dev + ': Collapse all folds every team, headers say so: ' + JSON.stringify(s));
            await ev(`T.render.renderLiveSessions(window.__LIVE); window.syncMobileAgentList && window.syncMobileAgentList(); true`);
            s = await state(); assert.equal(s.collapsed, 10, dev + ': and it stays after a render');
            await press('Expand all');
            s = await state(); assert.deepEqual([s.collapsed, s.rows, s.stored], [0, 20, 0], dev + ': Expand all opens them all: ' + JSON.stringify(s));
            await ev(`window.toggleGroupCollapse(document.querySelectorAll(${JSON.stringify(root)} + ' .session-group-header[data-group-name]')[2].dataset.groupName); true`);
            s = await state(); assert.equal(s.collapsed, 1, dev + ': a hand choice after Expand all is kept');
            await press('Collapse all'); await press('Expand all');
            s = await state(); assert.equal(s.collapsed, 0, dev + ': Expand all is the way back from any state');
        }
        console.log('PASS');
    } catch (e) {
        console.error(e && e.stack || e);
        process.exitCode = 1;
    } finally {
        try { await client.close(); } catch (_) {}
    }
})();
