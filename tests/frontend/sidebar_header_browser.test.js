// The sidebar header and its server list (#4062): the collapse button is the last control of the row, and the search box, the server button
// and the icon buttons share one height, one centre line and even gaps; the server list lines its columns up (dot, name, update marker,
// status and count), the update marker is one size in one place on every row, "Unknown" is muted, "Open Servers" is the full-width footer;
// arrows and Escape work; a click or tap outside closes the server list, the server details and the display menu, and a click inside does
// not; and the collapsed rail still has its way back. Desktop and phone width. Run against an isolated dev server.
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
    // the switcher visible at this width (the sidebar's on desktop, the phone list's on a phone)
    const SW = `[...document.querySelectorAll('details[data-server-switcher]')].find(x => x.getClientRects().length)`;
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
            await waitFor(`!!(${SW})`, 'the server button', 6000);
            await sleep(500); // sleep-ok: a redraw and a layout settle over frames; no completion event

            // 1 + 2: order, heights, centre line, gaps
            const row = await ev(`(() => { const sw = ${SW}; const header = sw.closest('.sidebar-header, .mobile-list-header'); const r = e => { const b = e.getBoundingClientRect(); return { l: b.left, r: b.right, t: b.top, h: b.height, cy: b.top + b.height / 2 }; };
                const search = document.querySelector('.sidebar-header .sidebar-search-input'), pill = sw.querySelector('summary'), menu = header.querySelector('[data-list-display-menu] summary'), collapse = header.querySelector('.sidebar-collapse-btn:not([data-display-button])');
                const btn = header.querySelector('.btn');
                return { header: header.className, search: search && search.getClientRects().length ? r(search) : null, pill: r(pill), menu: r(menu), collapse: collapse && collapse.getClientRects().length ? r(collapse) : null, newBtn: btn && btn.getClientRects().length ? r(btn) : null, last: header.lastElementChild.className }; })()`);
            const ctl = [row.search, row.pill, row.menu, row.collapse, row.newBtn].filter(Boolean);
            assert.ok(ctl.length >= 3, `${dev}: controls found (${JSON.stringify(Object.keys(row).filter(k => row[k]))})`);
            for (const c of ctl) assert.ok(Math.abs(c.h - ctl[0].h) <= 0.5, `${dev}: one height (${ctl.map(x => x.h).join(', ')})`);
            for (const c of ctl) assert.ok(Math.abs(c.cy - ctl[0].cy) <= 1, `${dev}: one centre line (${ctl.map(x => x.cy).join(', ')})`);
            if (dev === 'desktop') {
                assert.ok(row.collapse && row.collapse.l > row.menu.r - 0.5 && row.collapse.l > row.pill.r, 'desktop: the collapse button is the last control, right of the display menu');
                assert.match(row.last, /sidebar-collapse-btn/, 'desktop: it is the last element of the header');
                const gaps = [row.search, row.pill, row.menu, row.collapse].map((c, i, a) => i ? Math.round(c.l - a[i - 1].r) : null).filter(g => g !== null);
                assert.ok(Math.max(...gaps) - Math.min(...gaps) <= 1, `desktop: even spacing (${gaps.join(', ')})`);
            }

            // 3: the list's columns
            await ev(`(${SW}).open = true; true`); await sleep(400); // sleep-ok: a redraw and a layout settle over frames; no completion event
            const cols = await ev(`(() => { const sw = ${SW}; const items = [...sw.querySelectorAll('.server-switcher-item')];
                return items.map(it => { const q = s => it.querySelector(s); const rr = e => { if (!e) return null; const b = e.getBoundingClientRect(); return { l: Math.round(b.left * 10) / 10, r: Math.round(b.right * 10) / 10, w: Math.round(b.width * 10) / 10, h: Math.round(b.height * 10) / 10 }; };
                    return { id: it.dataset.serverOpen, dot: rr(q('.server-dot')), name: rr(q('.server-switcher-name')), marker: rr(q('.server-update-marker')), meta: rr(q('.server-switcher-meta')), h: rr(it).h, metaColor: getComputedStyle(q('.server-switcher-meta')).color, nameColor: getComputedStyle(q('.server-switcher-name')).color }; }); })()`);
            assert.equal(cols.length, 4);
            for (const c of cols) { assert.equal(c.dot.l, cols[0].dot.l, `${dev}: dots line up`); assert.equal(c.name.l, cols[0].name.l, `${dev}: names line up`); assert.equal(c.meta.r, cols[0].meta.r, `${dev}: status and count are right-aligned in one column`); assert.equal(c.h, cols[0].h, `${dev}: one row height`); }
            const marked = cols.filter(c => c.marker);
            assert.equal(marked.length, 2);
            assert.deepEqual([marked[0].marker.l, marked[0].marker.w, marked[0].marker.h], [marked[1].marker.l, marked[1].marker.w, marked[1].marker.h], `${dev}: the update marker is one size in one place on every row`);
            const unknown = cols.find(c => c.id === 'm3'), online = cols.find(c => c.id === 'm1');
            assert.notEqual(unknown.metaColor, online.metaColor, `${dev}: Unknown is muted (${unknown.metaColor} vs ${online.metaColor})`);
            const foot = await ev(`(() => { const sw = ${SW}; const p = sw.querySelector('.server-switcher-panel').getBoundingClientRect(); const f = sw.querySelector('.server-open-btn').getBoundingClientRect(); return { pl: p.left, pr: p.right, pb: p.bottom, fl: f.left, fr: f.right, fb: f.bottom, text: sw.querySelector('.server-open-btn').textContent.trim(), onScreen: p.left >= 0 && p.right <= innerWidth }; })()`);
            assert.equal(foot.text, 'Open Servers');
            assert.ok(foot.fr - foot.fl >= foot.pr - foot.pl - 3, `${dev}: Open Servers spans the panel (${foot.fl}-${foot.fr} in ${foot.pl}-${foot.pr})`);
            assert.ok(foot.fb <= foot.pb + 0.5, `${dev}: it is the panel's footer`);
            assert.equal(foot.onScreen, true, `${dev}: the panel is on screen`);

            // 4: outside click, inside click, Escape, one popup at a time
            await ev(`${SW}.querySelector('.server-switcher-panel-list').dispatchEvent(new MouseEvent('click', { bubbles: true })); true`); await sleep(150); // sleep-ok: a redraw and a layout settle over frames; no completion event
            assert.equal(await ev(`${SW}.open`), true, `${dev}: a click inside leaves the list open`);
            await ev(`(document.getElementById('live-sessions-list') || document.getElementById('mobile-session-list')).closest('.sidebar-section, .mobile-agent-list, body').dispatchEvent(new MouseEvent('click', { bubbles: true })); true`); await sleep(150); // sleep-ok: a redraw and a layout settle over frames; no completion event
            assert.equal(await ev(`!!(${SW}) && ${SW}.open`), false, `${dev}: a click outside closes the list`);
            await ev(`(${SW}).open = true; true`); await sleep(200); // sleep-ok: a redraw and a layout settle over frames; no completion event
            await ev(`${SW}.querySelector('.server-switcher-item').focus(); true`);
            await key('Escape', 'Escape', 27); await sleep(150); // sleep-ok: a redraw and a layout settle over frames; no completion event
            assert.equal(await ev(`${SW}.open`), false, `${dev}: Escape closes the list`);
            assert.equal(await ev(`document.activeElement === ${SW}.querySelector('summary')`), true, `${dev}: and returns to the server button`);
            // one at a time: opening the display menu closes the server list
            await ev(`(${SW}).open = true; true`); await sleep(200); // sleep-ok: a redraw and a layout settle over frames; no completion event
            await ev(`(${SW}).closest('.sidebar-header, .mobile-list-header').querySelector('[data-list-display-menu] summary').click(); true`); await sleep(250); // sleep-ok: a redraw and a layout settle over frames; no completion event
            assert.equal(await ev(`${SW}.open`), false, `${dev}: opening another popup closes the server list`);
            assert.equal(await ev(`${SW}.closest('.sidebar-header, .mobile-list-header').querySelector('[data-list-display-menu]').open`), true, `${dev}: the display menu is open`);
            await ev(`document.body.dispatchEvent(new MouseEvent('click', { bubbles: true })); true`); await sleep(150); // sleep-ok: a redraw and a layout settle over frames; no completion event
            assert.equal(await ev(`${SW}.closest('.sidebar-header, .mobile-list-header').querySelector('[data-list-display-menu]').open`), false, `${dev}: a click outside closes the display menu too`);

            // 5: arrow keys
            await ev(`${SW}.querySelector('summary').focus(); true`);
            await key('ArrowDown', 'ArrowDown', 40); await sleep(300); // sleep-ok: a redraw and a layout settle over frames; no completion event
            assert.equal(await ev(`${SW}.open`), true, `${dev}: ArrowDown on the button opens the list`);
            const id = () => ev(`document.activeElement.dataset.serverOpen === '' ? 'footer' : document.activeElement.dataset.serverOpen`);
            assert.equal(await id(), 'm1', `${dev}: the first row has the focus`);
            await key('ArrowDown', 'ArrowDown', 40); assert.equal(await id(), 'm2');
            await key('End', 'End', 35); assert.equal(await id(), 'footer', `${dev}: End goes to Open Servers`);
            await key('ArrowUp', 'ArrowUp', 38); assert.equal(await id(), 'm4');
            await key('Home', 'Home', 36); assert.equal(await id(), 'm1');
            await ev(`(${SW}).open = false; true`);

            // 6: server details (the update notice popover) close on an outside click too
            await ev(`document.querySelectorAll('input[data-display-input="group"]').forEach(i => { if (!i.checked) i.click(); }); true`); await sleep(500); // sleep-ok: a redraw and a layout settle over frames; no completion event
            const hasMore = await ev(`!![...document.querySelectorAll('details[data-server-details]')].find(x => x.getClientRects().length)`);
            if (hasMore) {
                await ev(`[...document.querySelectorAll('details[data-server-details]')].find(x => x.getClientRects().length).open = true; true`); await sleep(200); // sleep-ok: a redraw and a layout settle over frames; no completion event
                await ev(`document.body.dispatchEvent(new MouseEvent('click', { bubbles: true })); true`); await sleep(200); // sleep-ok: a redraw and a layout settle over frames; no completion event
                assert.equal(await ev(`[...document.querySelectorAll('details[data-server-details][open]')].length`), 0, `${dev}: a click outside closes the server details`);
            }
            if (dev === 'desktop') {
                // 7: the collapsed rail has its way back
                await ev(`document.querySelector('.sidebar-header .sidebar-collapse-btn:not([data-display-button])').click(); true`); await sleep(500); // sleep-ok: a redraw and a layout settle over frames; no completion event
                assert.equal(await ev(`document.querySelector('.layout').classList.contains('sidebar-collapsed')`), true, 'desktop: collapsed');
                const back = await ev(`(() => { const b = document.querySelector('#sidebar-rail [data-on-click="sidebar-rail-toggle"]'); const r = b.getBoundingClientRect(); return r.width > 0 && r.height > 0; })()`);
                assert.equal(back, true, 'desktop: the rail has an Expand button');
                await ev(`document.querySelector('#sidebar-rail [data-on-click="sidebar-rail-toggle"]').click(); true`); await sleep(500); // sleep-ok: a redraw and a layout settle over frames; no completion event
                assert.equal(await ev(`document.querySelector('.layout').classList.contains('sidebar-collapsed')`), false, 'desktop: and it expands');
            }
        }
        console.log('\nPASS sidebar header: collapse last, one height, aligned server list, outside click and keys');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
