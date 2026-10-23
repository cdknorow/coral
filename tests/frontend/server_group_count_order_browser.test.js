// A server group's agent count sits right after the server's name, as the team row has it (#6241):
// name, count, then the Update chip; the row's right edge keeps only the ⋮ menu. The count keeps
// its accessible name ("N agents", in the toggle's label), and a long name truncates before the count.
// Desktop and a 390 px phone.
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

const SERVERS = [
    { id: 'srv-short', label: 'frontier', status: 'online', update: 'available', coral_version: '1.4.3', latest_coral: '1.5.0' },
    { id: 'srv-long', label: 'a-very-long-workstation-name-that-cannot-fit-in-the-sidebar-row', status: 'online', update: 'required', coral_version: '1.3.9', latest_coral: '1.5.0' },
    { id: 'srv-tiny', label: 'pc', status: 'online', update: 'current' },
    { id: 'srv-plain', label: 'plain', status: 'online', update: 'current', coral_version: '1.5.0', latest_coral: '1.5.0' },
];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();

    // Each server header's children in document order, and where the count and the menu sit.
    const rows = scope => ev(`(() => {
        const out = {};
        for (const h of document.querySelectorAll('${scope} li.server-group-header[data-server-group]')) {
            const t = h.querySelector('.server-group-toggle');
            const q = s => t.querySelector(s);
            const r = e => e ? e.getBoundingClientRect() : null;
            const name = q('.server-group-name'), count = q('.session-group-count'), marker = q('.server-update-marker');
            const more = h.querySelector('.server-group-more-btn');
            out[h.dataset.serverGroup] = {
                order: [...t.children].map(c => c.classList.contains('server-group-name') ? 'name' : c.classList.contains('session-group-count') ? 'count' : c.classList.contains('server-update-marker') ? 'update' : c.classList.contains('server-group-icon') ? 'icon' : c.className),
                count: count && count.textContent,
                aria: t.getAttribute('aria-label'),
                gap: count && name ? Math.round(r(count).left - r(name).right) : null,
                countRight: count ? Math.round(r(count).right) : null,
                countVisible: !!count && r(count).width > 0,
                toggleRight: Math.round(r(t).right),
                moreLeft: more ? Math.round(r(more).left) : null,
                toggleWidth: Math.round(r(t).width),
                markerVisible: marker ? r(marker).width > 0 : null,
                nameWidth: Math.round(r(name).width),
                nameClipped: name.scrollWidth > name.clientWidth,
            };
        }
        return out;
    })()`);

    const check = (where, n) => {
        for (const sv of SERVERS) assert.ok(n[sv.id], `${where}: ${sv.id} has a server header`);
        assert.deepEqual(n['srv-short'].order, ['icon', 'name', 'count', 'update'], `${where}: name, count, then Update`);
        assert.deepEqual(n['srv-long'].order, ['icon', 'name', 'count', 'update'], `${where}: long name: name, count, then Update`);
        assert.deepEqual(n['srv-plain'].order, ['icon', 'name', 'count'], `${where}: no Update chip: name, then count`);
        assert.ok(n['srv-tiny'].gap <= 8, `${where}: a two-letter name has no padding before its count (${n['srv-tiny'].gap}px)`);
        for (const sv of SERVERS) {
            const g = n[sv.id];
            assert.ok(g.countVisible, `${where}: ${sv.id} count is visible (a long name or the chip never pushes it out)`);
            assert.ok(g.gap >= 0 && g.gap <= 8, `${where}: ${sv.id} count sits right after the name (gap ${g.gap}px)`);
            assert.ok(g.countRight <= g.moreLeft, `${where}: ${sv.id} count is left of the ⋮ menu`);
            assert.match(g.aria, /, 3 agents$/, `${where}: ${sv.id} keeps its accessible count (${g.aria})`);
        }
        // The short name's count is nowhere near the row's right edge: the toggle is wider than name + count + chip.
        assert.ok(n['srv-plain'].toggleRight - n['srv-plain'].countRight > 40, `${where}: the count is not pushed to the right edge`);
        assert.ok(n['srv-long'].nameClipped, `${where}: a long name truncates (ellipsis) rather than hiding the count`);
    };

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.clear(); true`);

        console.log('1. Desktop sidebar');
        await ev(`(() => {
            T.state.hub = true;
            T.state.servers = ${JSON.stringify(SERVERS)};
            localStorage.setItem('coral-group-by-server', 'true');
            for (const s of ['srv-short', 'srv-long', 'srv-plain', 'srv-tiny']) for (let i = 0; i < 3; i++) window.__m.add(s + '-' + i, 'A' + i, 3, { sleeping: false, server: s });
            T.render.renderLiveSessions(T.state.liveSessions);
            return true;
        })()`);
        await waitFor(`!!document.querySelector('li.server-group-header[data-server-group="srv-plain"]')`, 'desktop server headers', 3000);
        check('desktop', await rows('#live-sessions-list'));
        console.log('   ✓ name, count, Update; count right after the name; long name truncates; count named');

        console.log('2. Phone width (390 px)');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`(() => {
            T.state.hub = true;
            T.state.servers = ${JSON.stringify(SERVERS)};
            localStorage.setItem('coral-group-by-server', 'true');
            for (const s of ['srv-short', 'srv-long', 'srv-plain', 'srv-tiny']) for (let i = 0; i < 3; i++) window.__m.add(s + '-' + i, 'A' + i, 3, { sleeping: false, server: s });
            T.render.renderLiveSessions(T.state.liveSessions);
            return true;
        })()`);
        await ev(`(async () => { const mob = await import((window.__coralStatic||'/static/')+'mobile.js'); mob.syncMobileAgentList(); return true; })()`);
        await waitFor(`!!document.querySelector('#mobile-agent-list li.server-group-header[data-server-group="srv-plain"]')`, 'phone server headers', 3000);
        check('phone', await rows('#mobile-agent-list'));
        await Emulation.clearDeviceMetricsOverride();
        console.log('   ✓ same order and truncation at 390 px');

        console.log('\nAll server group count order checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
