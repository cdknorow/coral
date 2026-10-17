// The phone's agent list is built once (#4497, UX's #4495 items 1 and 2): the sidebar's own list element sits in the Agents page at phone width (no hidden
// sidebar copy, no clone, so no id twice), goes back to the sidebar on a wide screen and returns on a rotate; each row's hover tooltip is built when the pointer
// first comes, from the session as it is then. Selecting, the row menus, the status updates of rows already shown and the tab order keep working.
//
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;
const fs = require('node:fs');

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();

    const shot = async (name) => {
        if (!process.env.CORAL_SCREENSHOT_DIR) return;
        fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true });
        fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
    };

    // What the page holds: the elements, the ids that appear twice, the rows of one agent, where the list is.
    const census = () => ev(`(() => {
        const ids = {}; for (const e of document.querySelectorAll('[id]')) ids[e.id] = (ids[e.id] || 0) + 1;
        const dup = Object.keys(ids).filter(k => ids[k] > 1);
        const rows = id => document.querySelectorAll('.session-group-item[data-session-id="' + id + '"]').length;
        const list = document.getElementById('live-sessions-list') || document.getElementById('mobile-session-list');
        return { all: document.getElementsByTagName('*').length, dup, rowsA: rows('pl-a-id'), listId: list && list.id, inMobilePage: !!list && !!list.closest('#mobile-agent-list'),
            inSidebar: !!list && !!list.closest('.sidebar'), rowsTotal: document.querySelectorAll('.session-group-item').length,
            header: document.querySelectorAll('#mobile-agent-list > .mobile-list-header').length, tips: [...document.querySelectorAll('.session-tooltip')].filter(t => t.firstElementChild).length };
    })()`);

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.clear(); localStorage.setItem('coral-live-view-mode', 'chat')`);
        await ev(`(() => { const m = window.__m; for (let i = 0; i < 20; i++) m.add('pl-' + String.fromCharCode(97 + i), 'P', 4, { sleeping: false, board_project: i % 2 ? 'team-x' : null }); T.render.renderLiveSessions(T.state.liveSessions); })()`);
        await waitFor(`document.querySelectorAll('.session-group-item').length >= 20`, 'twenty rows', 6000);

        console.log('1. Phone 390 px: the list is built once, in the Agents page');
        await sleep(400); // sleep-ok: the list settles for the screenshot
        await shot('phone-list');
        let c = await census();
        console.log('   elements:', c.all, JSON.stringify({ rows: c.rowsTotal, listId: c.listId }));
        assert.deepEqual(c.dup, [], 'no id appears twice: ' + JSON.stringify(c.dup));
        assert.equal(c.rowsA, 1, 'one row for an agent');
        assert.equal(c.rowsTotal, 20, 'one row per agent, no copy');
        assert.equal(c.listId, 'mobile-session-list'); assert.ok(c.inMobilePage && !c.inSidebar);
        assert.equal(c.header, 1, 'one header');
        assert.equal(c.tips, 0, 'no tooltip table built up front');
        assert.equal(await ev(`!!document.getElementById('welcome-template') && !document.querySelector('#welcome-screen .welcome-shell')`), true, 'the welcome screen is not built on a phone');

        console.log('2. A redraw keeps one list and one header, and selecting an agent works');
        await ev(`T.render.renderLiveSessions(T.state.liveSessions); true`);
        c = await census();
        assert.equal(c.rowsTotal, 20); assert.equal(c.header, 1); assert.deepEqual(c.dup, []);
        const order = await ev(`[...document.querySelectorAll('#mobile-agent-list > *')].map(e => e.className.split(' ')[0] || e.tagName)`);
        assert.ok(order[0] === 'mobile-list-header' && order.includes('session-list'), 'header first, then the list: ' + JSON.stringify(order));
        await ev(`document.querySelector('.session-group-item[data-session-id="pl-c-id"]').click(); true`);
        await waitFor(`!!window.T.state.currentSession && window.T.state.currentSession.session_id === 'pl-c-id'`, 'the agent to be selected', 6000);
        assert.equal(await ev(`getComputedStyle(document.getElementById('mobile-agent-list')).display`), 'none', 'the list gives way to the agent');
        await ev(`window.mobileBack && window.mobileBack(); true`);
        assert.equal(await ev(`getComputedStyle(document.getElementById('mobile-agent-list')).display !== 'none'`), true, 'Back shows the list again');
        console.log('   ✓');

        console.log('3. A status change on a row already shown updates that row');
        const before = await ev(`document.querySelector('.session-group-item[data-session-id="pl-a-id"]').dataset.state`);
        await ev(`window.__feed('pl-a-id', { sleeping: true }); true`);
        await waitFor(`(() => { const r = document.querySelector('.session-group-item[data-session-id="pl-a-id"]'); return !r || r.dataset.state !== '${before}'; })()`, 'the row to show the change', 6000, `({ n: document.querySelectorAll('.session-group-item').length, st: (document.querySelector('.session-group-item[data-session-id="pl-a-id"]')||{dataset:{}}).dataset.state })`);
        await ev(`window.__feed('pl-a-id', { sleeping: false }); true`);
        console.log('   ✓');

        console.log('4. Rotate to a wide screen and back');
        await Emulation.setDeviceMetricsOverride({ width: 1200, height: 800, deviceScaleFactor: 1, mobile: false });
        await waitFor(`!!document.getElementById('live-sessions-list')`, 'the list to return to the sidebar', 6000);
        c = await census();
        assert.ok(c.inSidebar && !c.inMobilePage && c.listId === 'live-sessions-list', JSON.stringify(c));
        assert.equal(c.rowsTotal, 20); assert.deepEqual(c.dup, []);
        const where = await ev(`(() => { const l = document.getElementById('live-sessions-list'); return { parent: l.parentElement.className, tag: l.tagName }; })()`);
        assert.equal(where.parent, 'sidebar-section-body', 'back where the sidebar has it: ' + JSON.stringify(where));
        // Shown on a wide window, the welcome screen is built then, once, and its home section follows the settings.
        const welcome = await ev(`(async () => { const u = await import((window.__coralStatic || '/static/') + 'utils.js'); u.showView('welcome-screen'); u.showView('welcome-screen');
            return { shells: document.querySelectorAll('#welcome-screen .welcome-shell').length, template: !!document.getElementById('welcome-template'), home: !!document.getElementById('editions-home'), launch: document.querySelectorAll('#welcome-screen .welcome-launch').length }; })()`);
        assert.deepEqual(welcome, { shells: 1, template: false, home: true, launch: 3 }, 'built once: ' + JSON.stringify(welcome));
        console.log('5. Wide: the tooltip is built on the first hover, from the session as it is then');
        const sel = '.session-group-item[data-session-id="pl-b-id"]';
        assert.equal(await ev(`document.querySelector('${sel} .session-tooltip').childElementCount`), 0, 'empty before the pointer comes');
        await ev(`window.__feed('pl-b-id', { summary: 'a goal set after the list was drawn' }); true`);
        await sleep(300); // sleep-ok: the feed's redraw
        await ev(`document.querySelector('${sel}').dispatchEvent(new MouseEvent('mouseenter')); true`);
        const tip = await ev(`(() => { const t = document.querySelector('${sel} .session-tooltip'); return { shown: getComputedStyle(t).display, text: t.textContent }; })()`);
        assert.equal(tip.shown, 'block'); assert.match(tip.text, /State/); assert.match(tip.text, /Agent/);
        await ev(`document.querySelector('${sel}').dispatchEvent(new MouseEvent('mouseleave')); true`);
        assert.equal(await ev(`getComputedStyle(document.querySelector('${sel} .session-tooltip')).display`), 'none');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await waitFor(`!!document.getElementById('mobile-session-list')`, 'the list to move into the phone page again', 6000);
        c = await census();
        assert.ok(c.inMobilePage && c.rowsTotal === 20 && c.header === 1 && c.dup.length === 0, JSON.stringify(c));
        console.log('   ✓ wide: the list is in the sidebar; phone again: in the page, one header, one row per agent');

        await Emulation.clearDeviceMetricsOverride();
        console.log('\nAll phone list checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
