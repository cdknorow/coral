// The sidebar header (#4062): the collapse button is the last control of the row, and the search box, the Display-options button and the
// icon buttons share one height, one centre line and even gaps. A click or tap outside closes the display menu and a server header's details,
// and Escape closes the display menu; and the collapsed rail still has its way back. Desktop and phone width. Run against an isolated dev server.
// (#4932 removed the search-row server pill; the server list it opened is gone, so its column/footer/arrow-key checks went with it. The server
//  group headers, Show server, Group by server and the Servers tab are unchanged and covered by server_switcher_browser and server_menu.)
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const NOW = new Date().toISOString();
const SERVERS = [
    { id: 'm1', label: 'studio-mac.local', status: 'online', version: '1.4.69', last_seen: NOW },
    { id: 'm2', label: 'workstation.lan', status: 'online', version: '1.4.2', update: 'available', latest_coral: '1.4.69', last_seen: NOW },
    { id: 'm3', label: 'Old Laptop', status: 'unknown', version: '', last_seen: null },
    { id: 'm4', label: 'build-box', status: 'online', version: '1.3.1', update: 'required', latest_coral: '1.4.69', coral_version: '1.3.1', last_seen: NOW },
];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a redraw and a layout settle over frames; there is no completion event
    const key = async (k, code, vk) => { const b = { key: k, code, windowsVirtualKeyCode: vk }; await Input.dispatchKeyEvent({ type: 'keyDown', ...b }); await Input.dispatchKeyEvent({ type: 'keyUp', ...b }); };
    // the display menu visible at this width (the sidebar's on desktop, the phone list's on a phone)
    const DM = `[...document.querySelectorAll('details[data-list-display-menu]')].find(x => x.getClientRects().length)`;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [dev, w, h, mobile] of [['desktop', 1280, 800, false], ['phone', 390, 844, true]]) {
            console.log(dev);
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`T.state.hub = true; T.state.cloud = true; T.state.servers = ${JSON.stringify(SERVERS)}; T.state.liveSessions = []; window.__m.agents = {};
                ${JSON.stringify(SERVERS)}.forEach(s => { window.__m.add('a-' + s.id, 'S', 3, { sleeping: false, server: s.id }); window.__m.add('b-' + s.id, 'T', 3, { sleeping: false, server: s.id }); });
                T.render.renderLiveSessions(T.state.liveSessions); true`);
            await waitFor(`!!(${DM})`, 'the display menu', 6000);
            await sleep(500); // sleep-ok: a redraw and a layout settle over frames; no completion event

            // 1 + 2: order, heights, centre line, gaps (the search box, the Display-options button and the icon buttons)
            const row = await ev(`(() => { const dm = ${DM}; const header = dm.closest('.sidebar-header, .mobile-list-header'); const r = e => { const b = e.getBoundingClientRect(); return { l: b.left, r: b.right, t: b.top, h: b.height, cy: b.top + b.height / 2 }; };
                const search = document.querySelector('.sidebar-header .sidebar-search-input'), menu = header.querySelector('[data-list-display-menu] summary'), collapse = header.querySelector('.sidebar-collapse-btn:not([data-display-button])');
                const btn = header.querySelector(':scope > .btn'); // the header's own + New, not a button inside the Display options panel (#4746)
                return { header: header.className, search: search && search.getClientRects().length ? r(search) : null, menu: r(menu), collapse: collapse && collapse.getClientRects().length ? r(collapse) : null, newBtn: btn && btn.getClientRects().length ? r(btn) : null, last: header.lastElementChild.className }; })()`);
            const ctl = [row.search, row.menu, row.collapse, row.newBtn].filter(Boolean);
            // desktop has search + display menu + collapse + New; the phone list header has the display menu + New (the search-row pill was removed, #4932)
            assert.ok(ctl.length >= (dev === 'desktop' ? 3 : 2), `${dev}: controls found (${JSON.stringify(Object.keys(row).filter(k => row[k]))})`);
            for (const c of ctl) assert.ok(Math.abs(c.h - ctl[0].h) <= 0.5, `${dev}: one height (${ctl.map(x => x.h).join(', ')})`);
            for (const c of ctl) assert.ok(Math.abs(c.cy - ctl[0].cy) <= 1, `${dev}: one centre line (${ctl.map(x => x.cy).join(', ')})`);
            if (dev === 'desktop') {
                assert.ok(row.collapse && row.collapse.l > row.menu.r - 0.5, 'desktop: the collapse button is the last control, right of the display menu');
                assert.match(row.last, /sidebar-collapse-btn/, 'desktop: it is the last element of the header');
                const gaps = [row.search, row.menu, row.collapse].map((c, i, a) => i ? Math.round(c.l - a[i - 1].r) : null).filter(g => g !== null);
                assert.ok(Math.max(...gaps) - Math.min(...gaps) <= 1, `desktop: even spacing (${gaps.join(', ')})`);
            }

            // 3: the display menu closes on an outside click and on Escape; a click inside leaves it open
            await ev(`(${DM}).open = true; true`); await sleep(200); // sleep-ok: a redraw and a layout settle over frames; no completion event
            assert.equal(await ev(`(${DM}).open`), true, `${dev}: the display menu opens`);
            await ev(`(${DM}).querySelector('.list-display-panel, [class*="panel"]').dispatchEvent(new MouseEvent('click', { bubbles: true })); true`); await sleep(150); // sleep-ok: a click and a layout settle over frames; no completion event
            assert.equal(await ev(`(${DM}).open`), true, `${dev}: a click inside leaves the display menu open`);
            await ev(`(document.getElementById('live-sessions-list') || document.getElementById('mobile-session-list')).closest('.sidebar-section, .mobile-agent-list, body').dispatchEvent(new MouseEvent('click', { bubbles: true })); true`); await sleep(150); // sleep-ok: a click and a layout settle over frames; no completion event
            assert.equal(await ev(`(${DM}).open`), false, `${dev}: a click outside closes it`);
            await ev(`(${DM}).open = true; true`); await sleep(150); // sleep-ok: a redraw and a layout settle over frames; no completion event
            await ev(`(${DM}).querySelector('summary').focus(); true`);
            await key('Escape', 'Escape', 27); await sleep(150); // sleep-ok: a redraw and a layout settle over frames; no completion event
            assert.equal(await ev(`(${DM}).open`), false, `${dev}: Escape closes the display menu`);

            // 4: a server header's details (the update-notice popover) close on an outside click
            await ev(`document.querySelectorAll('input[data-display-input="group"]').forEach(i => { if (!i.checked) i.click(); }); true`); await sleep(500); // sleep-ok: a redraw and a layout settle over frames; no completion event
            const hasMore = await ev(`!![...document.querySelectorAll('details[data-server-details]')].find(x => x.getClientRects().length)`);
            if (hasMore) {
                await ev(`[...document.querySelectorAll('details[data-server-details]')].find(x => x.getClientRects().length).open = true; true`); await sleep(200); // sleep-ok: a redraw and a layout settle over frames; no completion event
                await ev(`document.body.dispatchEvent(new MouseEvent('click', { bubbles: true })); true`); await sleep(200); // sleep-ok: a redraw and a layout settle over frames; no completion event
                assert.equal(await ev(`[...document.querySelectorAll('details[data-server-details][open]')].length`), 0, `${dev}: a click outside closes the server details`);
            }
            if (dev === 'desktop') {
                // 5: the collapsed rail has its way back
                await ev(`document.querySelector('.sidebar-header .sidebar-collapse-btn:not([data-display-button])').click(); true`); await sleep(500); // sleep-ok: a redraw and a layout settle over frames; no completion event
                assert.equal(await ev(`document.querySelector('.layout').classList.contains('sidebar-collapsed')`), true, 'desktop: collapsed');
                const back = await ev(`(() => { const b = document.querySelector('#sidebar-rail [data-on-click="sidebar-rail-toggle"]'); const r = b.getBoundingClientRect(); return r.width > 0 && r.height > 0; })()`);
                assert.equal(back, true, 'desktop: the rail has an Expand button');
                await ev(`document.querySelector('#sidebar-rail [data-on-click="sidebar-rail-toggle"]').click(); true`); await sleep(500); // sleep-ok: a redraw and a layout settle over frames; no completion event
                assert.equal(await ev(`document.querySelector('.layout').classList.contains('sidebar-collapsed')`), false, 'desktop: and it expands');
            }
        }
        console.log('\nPASS sidebar header: collapse last, one height, display menu + server details close on outside click, Escape, collapsed rail');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
