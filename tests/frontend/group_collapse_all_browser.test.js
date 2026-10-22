// Collapse / Expand all (#4746; one toggle since #4947, the operator-approved option B): one icon button in the agent list's header beside Display, in the sidebar
// and on the phone (a 44 px target). With 10 teams: it folds every team, and when they are all folded it opens them; a team opened or closed by hand keeps its
// state until the toggle is used; the choice is stored like a single collapse (it survives a render and a reload); the button's name says what it does next
// and the headers' aria-expanded follow. It is absent when the list has no groups. Run against an isolated dev server.
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
                return { heads: heads.length, collapsed: heads.filter(h => (h.getAttribute('aria-expanded') ?? h.querySelector('.group-chevron')?.getAttribute('aria-expanded')) === 'false').length, rows: [...r.querySelectorAll('li[data-session-id]')].filter(vis).length, stored: JSON.parse(localStorage.getItem('coral_collapsed_groups') || '[]').length }; })()`);
            const toggle = `document.querySelector('${dev === 'phone' ? '.mobile-list-header' : '.sidebar-header'} [data-group-toggle]')`;
            const label = () => ev(`${toggle}.getAttribute('aria-label')`);
            // Pressing it says what it will do; the name changes once the list is redrawn.
            const press = async name => { assert.equal(await label(), name, dev + ': the toggle offers ' + name); await ev(`${toggle}.click(); true`); };
            const after = name => waitFor(`${toggle}.getAttribute('aria-label') === ${JSON.stringify(name)}`, 'the toggle now offers ' + name);
            await build();
            let s = await state();
            assert.deepEqual([s.heads, s.collapsed, s.rows], [10, 0, 20], dev + ': 10 teams, all open: ' + JSON.stringify(s));

            const b = await ev(`(() => { const x = ${toggle}; const r = x.getBoundingClientRect(); return { shown: !x.hidden && r.width > 0, name: x.getAttribute('aria-label'), title: x.title, h: Math.round(r.height), w: Math.round(r.width), icons: [...x.querySelectorAll('svg')].filter(i => getComputedStyle(i).display !== 'none').length }; })()`);
            assert.deepEqual([b.shown, b.name, b.title, b.icons], [true, 'Collapse all groups', 'Collapse all groups', 1], dev + ': one visible toggle beside Display, named, with one icon: ' + JSON.stringify(b));
            if (dev === 'phone') assert.ok(b.h >= 44 && b.w >= 44, dev + ': 44 px target ' + JSON.stringify(b));

            await ev(`window.toggleGroupCollapse(document.querySelector(${JSON.stringify(root)} + ' .session-group-header[data-group-name]').dataset.groupName); true`);
            s = await state(); assert.equal(s.collapsed, 1, dev + ': one team closed by hand');
            await press('Collapse all groups');
            s = await state(); assert.deepEqual([s.collapsed, s.rows, s.stored], [10, 0, 10], dev + ': it folds every team, headers say so: ' + JSON.stringify(s));
            await after('Expand all groups');
            await ev(`T.render.renderLiveSessions(window.__LIVE); window.syncMobileAgentList && window.syncMobileAgentList(); true`);
            s = await state(); assert.equal(s.collapsed, 10, dev + ': and it stays after a render');
            await press('Expand all groups');
            s = await state(); assert.deepEqual([s.collapsed, s.rows, s.stored], [0, 20, 0], dev + ': pressed again it opens them all: ' + JSON.stringify(s));
            await after('Collapse all groups');
            await ev(`window.toggleGroupCollapse(document.querySelectorAll(${JSON.stringify(root)} + ' .session-group-header[data-group-name]')[2].dataset.groupName); true`);
            s = await state(); assert.equal(s.collapsed, 1, dev + ': a hand choice after Expand all is kept');
            await after('Collapse all groups'); // one team shut by hand: still "collapse", the rest are open
            await press('Collapse all groups'); await after('Expand all groups'); await press('Expand all groups');
            s = await state(); assert.equal(s.collapsed, 0, dev + ': pressing twice is the way back from any state');
            // No groups, no toggle: it is noise on a list with nothing to fold.
            await ev(`window.__m.agents = {}; window.__LIVE = []; T.state.liveSessions = []; T.render.renderLiveSessions([]); true`);
            assert.equal(await ev(`${toggle}.hidden`), true, dev + ': hidden when the list has no groups');
        }
        console.log('PASS');
    } catch (e) {
        console.error(e && e.stack || e);
        process.exitCode = 1;
    } finally {
        try { await client.close(); } catch (_) {}
    }
})();
