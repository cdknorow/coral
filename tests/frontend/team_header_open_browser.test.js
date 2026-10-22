// A team's header opens the team (#5989): clicking the name, or Enter on the focused header, enters the team; the chevron before the name is the one control that
// collapses its group (a button with aria-expanded, a 44 px target on a phone). A folder header still collapses as a whole. Run against an isolated dev server.
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
            await ev(`(() => { localStorage.removeItem('coral_collapsed_groups'); window.__m.agents = {};
                for (let a = 1; a <= 2; a++) window.__m.add('ta' + a, 'A', 1, { sleeping: false, session_id: 'sid-' + a, board_project: 'team-one' });
                window.__LIVE = Object.values(window.__m.agents).map(x => window.__m.entry(x)); T.state.liveSessions = window.__LIVE; T.render.renderLiveSessions(window.__LIVE);
                if (${dev === 'phone'}) window.syncMobileAgentList && window.syncMobileAgentList();
                window.__entered = []; window.enterTeamContext = (b) => { window.__entered.push(b); return true; }; return true; })()`);
            const head = `document.querySelector(${JSON.stringify(root)} + ' .session-group-header[data-group="team-one"]')`;
            const chev = `${head}.querySelector('.group-chevron')`;
            const b = await ev(`(() => { const c = ${chev}, r = c.getBoundingClientRect(); return { tag: c.tagName, expanded: c.getAttribute('aria-expanded'), label: c.getAttribute('aria-label'), w: Math.round(r.width), h: Math.round(r.height), headerExpanded: ${head}.getAttribute('aria-expanded'), headerLabel: ${head}.getAttribute('aria-label') }; })()`);
            assert.deepEqual([b.tag, b.expanded, b.label, b.headerExpanded, b.headerLabel], ['BUTTON', 'true', 'Collapse team-one', null, 'Open team team-one'], dev + ': ' + JSON.stringify(b));
            if (dev === 'phone') assert.ok(b.w >= 44 && b.h >= 44, dev + ': 44 px chevron ' + JSON.stringify(b));

            await ev(`${head}.querySelector('.group-name-text').click(); true`);
            assert.deepEqual(await ev(`window.__entered`), ['team-one'], dev + ': the name opens the team');
            assert.equal(await ev(`${chev}.getAttribute('aria-expanded')`), 'true', dev + ': and does not collapse it');

            await ev(`${chev}.click(); true`);
            await waitFor(`${chev}.getAttribute('aria-expanded') === 'false'`, 'the chevron collapses the group');
            assert.deepEqual(await ev(`window.__entered`), ['team-one'], dev + ': the chevron does not open the team');
            assert.equal(await ev(`${chev}.getAttribute('aria-label')`), 'Expand team-one');
            assert.equal(await ev(`document.querySelectorAll(${JSON.stringify(root)} + ' .session-group-item:not([hidden])').length > -1 && getComputedStyle(${chev}.querySelector('svg')).transform !== 'none'`), true, dev + ': it turns');

            await ev(`${head}.focus(); ${head}.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })); true`);
            assert.deepEqual(await ev(`window.__entered`), ['team-one', 'team-one'], dev + ': Enter on the header opens the team');
            await ev(`${chev}.click(); true`);
            await waitFor(`${chev}.getAttribute('aria-expanded') === 'true'`, 'the chevron opens it again');
        }
        console.log('PASS team header: the name opens the team, the chevron collapses, Enter opens');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
