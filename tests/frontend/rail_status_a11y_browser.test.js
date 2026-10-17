// The collapsed sidebar's (icon rail's) agent tiles expose their status (#4312, from the #4276 audit): the dot is colour only, so the tile's
// own accessible name carries the state in the words the sidebar uses ("qa, Working"), the dot is hidden from assistive technology so the
// state is read once, not twice, and the tile's tip says the same. Checked in the page and in the browser's accessibility tree.
// Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    const { Accessibility, DOM } = client;
    const ok = name => console.log(`   ✓ ${name}`);
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        // One agent per state the rail can show; the fields are the ones the sidebar's own resolver reads.
        const STATES = [['w-working', { working: true }, 'Working'], ['w-needs', { waiting_for_input: true }, 'Needs input'], ['w-stuck', { stuck: true }, 'Stuck'],
            ['w-idle', {}, 'Idle'], ['w-your', { awaiting_user: true }, 'Ready for input']];  // a sleeping agent is left out of the rail
        await ev(`(() => {
            window.__m.agents = {};
            for (const [name, extra] of ${JSON.stringify(STATES.map(s => [s[0], s[1]]))}) window.__m.add(name, 'A', 1, { sleeping: false, session_id: 'sid-' + name });
            const live = Object.values(window.__m.agents).map(x => window.__m.entry(x));
            for (const [name, extra] of ${JSON.stringify(STATES.map(s => [s[0], s[1]]))}) Object.assign(live.find(s => s.session_id === 'sid-' + name), extra);
            window.__RAIL_LIVE = live; return true; })()`);
        await ev(`(() => { localStorage.setItem('coral-group-by-team', 'false'); const layout = document.querySelector('.layout'); if (!layout.classList.contains('sidebar-collapsed')) window.toggleSidebarRail();
            T.state.liveSessions = window.__RAIL_LIVE; T.render.renderLiveSessions(window.__RAIL_LIVE); return true; })()`);
        const tiles = await ev(`Object.fromEntries(${JSON.stringify(STATES.map(s => s[0]))}.map(n => { const t = document.querySelector('#sidebar-rail-list .sidebar-rail-item[data-session-id="sid-' + n + '"]'); if (!t) return [n, null];
            const dot = t.querySelector('.rail-tile-dot'); return [n, { label: t.getAttribute('aria-label'), title: t.getAttribute('title'), dotHidden: dot ? dot.getAttribute('aria-hidden') : 'no dot' }]; }))`);
        for (const [name, , word] of STATES) {
            const t = tiles[name];
            assert.ok(t, `a tile for ${name}`);
            assert.equal(t.label, `${name}, ${word}`, `${name}: the tile's name carries the state`);
            assert.equal(t.title, t.label, `${name}: its tip says the same`);
            if (t.dotHidden !== 'no dot') assert.equal(t.dotHidden, 'true', `${name}: the colour dot is hidden from assistive technology`);
        }
        ok('each rail tile\'s name ends with its state in the sidebar\'s words, its tip matches, and the dot is aria-hidden');

        // The accessibility tree: the tile is one named button; the state is in its name once.
        await DOM.enable(); await Accessibility.enable();
        const doc = await DOM.getDocument({ depth: -1 });
        const { nodeId } = await DOM.querySelector({ nodeId: doc.root.nodeId, selector: '#sidebar-rail-list .sidebar-rail-item[data-session-id="sid-w-working"]' });
        const { nodes } = await Accessibility.getPartialAXTree({ nodeId, fetchRelatives: false });
        const tile = nodes[0];
        assert.equal(tile.role.value, 'button'); assert.equal(tile.name.value, 'w-working, Working');
        assert.equal((tile.name.value.match(/Working/g) || []).length, 1, 'the state is in the name once');
        const kids = await Accessibility.getPartialAXTree({ nodeId, fetchRelatives: true });
        const dotNodes = kids.nodes.filter(n => !n.ignored && n.name && /working|dot/i.test(n.name.value || '') && n.nodeId !== tile.nodeId && n.role.value !== 'button');
        assert.deepEqual(dotNodes.map(n => n.name.value), [], 'nothing else in the tile repeats the state');
        ok('in the accessibility tree the tile is one button named "w-working, Working"');
    } catch (e) {
        console.error(e);
        process.exitCode = 1;
    } finally {
        await client.close();
    }
})();
