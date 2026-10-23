// The board toolbar on a phone (#4538, UX #4536 proposal 1, operator-approved): Subscribers stays, and Select, Export, Pause and
// Delete are in a More menu, so the header keeps one row at any text size (at 200% the five buttons wrapped to three rows). At
// 390 px with 100% and 200% text: the toolbar is one row and the header no taller than about one, the menu opens from the
// keyboard with focus on its first item, arrow keys, Home and End move, Escape closes it with focus back on More, items are
// 44 px, Delete is last and red, and every action works from it (select mode, export, pause and resume with its label, delete
// with its confirmation, cancelled); an outside click closes it. On a desktop nothing changes: no More, the four buttons inline.
'use strict';

const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const B = 'more-4538';

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation, Input } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 600));
        return r.result.value;
    };
    const { waitFor, waitStable, coralReady } = require('./helpers/wait').bind(ev);
    const key = async (k, code = k, vk = 0, text) => {
        await Input.dispatchKeyEvent({ type: 'keyDown', key: k, code, windowsVirtualKeyCode: vk, ...(text ? { text } : {}) });
        await Input.dispatchKeyEvent({ type: 'keyUp', key: k, code, windowsVirtualKeyCode: vk });
    };
    const shown = sel => `(() => { const e = document.querySelector(${JSON.stringify(sel)}); return !!e && e.getClientRects().length > 0 && getComputedStyle(e).visibility !== 'hidden'; })()`;
    const open = () => ev(`document.getElementById('mb-more').classList.contains('open')`);
    const active = () => ev(`document.activeElement && document.activeElement.id`);
    const openMenu = async () => {
        await ev(`document.getElementById('mb-more-btn').click(); true`);
        await waitFor(shown('#mb-select-btn'), 'the More menu open');
    };
    const clickItem = id => ev(`document.getElementById(${JSON.stringify(id)}).click(); true`);
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        let l = Page.loadEventFired(); await Page.navigate({ url: BASE + '/' }); await l; await coralReady();
        await ev(`fetch('/api/board/${B}/messages', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'qa', as: 'QA Engineer', content: 'hello from the toolbar test' }) }).then(r => r.status)`);

        console.log('Desktop: Select, Export and Pause inline; Delete alone behind More (#6279)');
        l = Page.loadEventFired(); await Page.navigate({ url: `${BASE}/board/${B}` }); await l; await coralReady();
        // A board opens with the right sidebar open when the window is wide enough, and then the toolbar uses More too (right_dock_browser checks that): the sidebar is closed for the inline toolbar (#4539).
        await waitFor(`document.body.hasAttribute('data-dock-open')`, 'the sidebar state');
        if (await ev(`document.body.dataset.dockOpen === '1'`)) await ev(`document.getElementById('panel-toggle-btn').click(); true`);
        await waitFor(`document.body.dataset.dockOpen === '0' && document.getElementById('agentic-state').getBoundingClientRect().width < 1 `, 'the sidebar closed');
        await waitFor(shown('#mb-pause-btn'), 'the toolbar on a desktop');
        const desk = await ev(`(() => { const ids = ['mb-select-btn', 'mb-export-btn', 'mb-pause-btn'];
            const r = ids.map(id => document.getElementById(id).getBoundingClientRect());
            return { more: getComputedStyle(document.getElementById('mb-more-btn')).display, shown: r.every(x => x.width > 0), oneRow: r.every(x => Math.abs(x.top - r[0].top) < 2),
                delHidden: document.getElementById('mb-delete-btn').getBoundingClientRect().width === 0, group: getComputedStyle(document.getElementById('mb-more-menu')).display }; })()`);
        assert.match(desk.more, /flex/, 'More on a desktop: ' + JSON.stringify(desk));
        assert.ok(desk.shown && desk.oneRow, 'Select, Export and Pause inline in one row');
        assert.ok(desk.delHidden, 'Delete is not inline: the destructive button sits behind More');
        assert.equal(desk.group, 'contents', 'their group adds no box');
        await ev(`document.getElementById('mb-more-btn').click(); true`);
        await waitFor(shown('#mb-delete-btn'), 'Delete in the More popover');
        await ev(`document.getElementById('mb-more-btn').focus(); true`);
        await key('Escape', 'Escape', 27);
        await waitFor(`document.getElementById('mb-delete-btn').getBoundingClientRect().width === 0`, 'Escape closes the popover');
        // from the keyboard: Enter on More puts focus on Delete, the one item
        await ev(`document.getElementById('mb-more-btn').focus(); true`);
        await key('Enter', 'Enter', 13, '\r');
        await waitFor(`document.activeElement && document.activeElement.id === 'mb-delete-btn'`, 'focus on Delete');
        await key('Escape', 'Escape', 27);
        await waitFor(`document.activeElement && document.activeElement.id === 'mb-more-btn'`, 'focus back on More');

        for (const font of ['', '200%']) {
            const name = `phone 390${font ? ', 200% text' : ''}`;
            await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
            await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
            await Emulation.setEmulatedMedia({ features: [{ name: 'pointer', value: 'coarse' }, { name: 'hover', value: 'none' }] });
            l = Page.loadEventFired(); await Page.navigate({ url: `${BASE}/board/${B}` }); await l; await coralReady();
            await ev(`document.documentElement.style.fontSize = ${JSON.stringify(font)}; true`);
            await waitFor(shown('#mb-more-btn'), `${name}: the More button`);
            await waitStable(`document.querySelector('#messageboard-view .session-header').getBoundingClientRect().height`, `${name}: the header`, 200);

            console.log(`${name}: one toolbar row, the four actions behind More`);
            const bar = await ev(`(() => { const s = document.getElementById('mb-subscribers-btn').getBoundingClientRect(), m = document.getElementById('mb-more-btn').getBoundingClientRect();
                const h = document.querySelector('#messageboard-view .session-header').getBoundingClientRect();
                return { oneRow: Math.abs(s.top - m.top) < 2, more: [Math.round(m.width), Math.round(m.height)], header: Math.round(h.height), rootPx: parseFloat(getComputedStyle(document.documentElement).fontSize),
                    hidden: ['mb-select-btn', 'mb-export-btn', 'mb-pause-btn', 'mb-delete-btn'].every(id => !document.getElementById(id).getClientRects().length) }; })()`);
            assert.ok(bar.oneRow, `${name}: Subscribers and More on one row`);
            assert.ok(bar.hidden, `${name}: Select, Export, Pause and Delete are in the menu, not the toolbar`);
            assert.ok(bar.more[0] >= 44 && bar.more[1] >= 44, `${name}: More is a 44 px target (${bar.more})`);
            // The title row and one toolbar row (UX measured 103 px at 200% text, 198 px with the wrapped toolbar).
            assert.ok(bar.header <= bar.rootPx * 7.5, `${name}: the header is about two rows (${bar.header} px at ${bar.rootPx} px text)`);

            console.log(`${name}: keyboard: Enter opens on the first item, arrows, Home, End, Escape back to More`);
            await ev(`document.getElementById('mb-more-btn').focus(); true`);
            await key('Enter', 'Enter', 13, '\r');
            await waitFor(`document.activeElement && document.activeElement.id === 'mb-select-btn'`, `${name}: focus on the first item`);
            assert.equal(await ev(`document.getElementById('mb-more-btn').getAttribute('aria-expanded')`), 'true');
            await key('ArrowDown', 'ArrowDown', 40); assert.equal(await active(), 'mb-export-btn');
            await key('End', 'End', 35); assert.equal(await active(), 'mb-delete-btn');
            await key('ArrowDown', 'ArrowDown', 40); assert.equal(await active(), 'mb-select-btn', 'wraps to the first');
            await key('ArrowUp', 'ArrowUp', 38); assert.equal(await active(), 'mb-delete-btn', 'wraps to the last');
            await key('Home', 'Home', 36); assert.equal(await active(), 'mb-select-btn');
            const items = await ev(`[...document.querySelectorAll('#mb-more-menu .mb-action-btn')].filter(b => b.getClientRects().length).map(b => ({ id: b.id, h: Math.round(b.getBoundingClientRect().height), danger: b.classList.contains('mb-action-danger') }))`);
            assert.deepEqual(items.map(i => i.id), ['mb-select-btn', 'mb-export-btn', 'mb-pause-btn', 'mb-delete-btn'], 'the four, Delete last');
            assert.ok(items.every(i => i.h >= 44), `${name}: 44 px items (${items.map(i => i.h)})`);
            assert.equal(items[3].danger, true, 'Delete keeps its danger style');
            assert.ok(await ev(`(() => { const r = document.getElementById('mb-more-menu').getBoundingClientRect(); return r.left >= 0 && r.right <= document.documentElement.clientWidth; })()`), `${name}: the menu is on the screen`);
            await key('Escape', 'Escape', 27);
            assert.equal(await open(), false, 'Escape closes it');
            assert.equal(await active(), 'mb-more-btn', 'focus back on More');

            console.log(`${name}: every action from the menu`);
            await openMenu(); await clickItem('mb-select-btn');
            await waitFor(`document.getElementById('mb-select-btn').classList.contains('active') && !document.getElementById('mb-more').classList.contains('open')`, `${name}: select mode on, the menu closed`);
            await ev(`document.querySelector('[data-on-click="mb-select-cancel"]').click(); true`);
            await openMenu(); await clickItem('mb-export-btn');
            await waitFor(shown('#mb-export-modal'), `${name}: the export dialog`);
            assert.equal(await open(), false);
            await key('Escape', 'Escape', 27);
            await waitFor(`!${shown('#mb-export-modal')}`, `${name}: the export dialog closed`);
            await waitFor(`document.activeElement && document.activeElement.id === 'mb-more-btn'`, `${name}: focus back on More after the dialog`);
            await openMenu(); await clickItem('mb-pause-btn');
            await waitFor(`document.getElementById('mb-pause-btn').textContent.trim() === 'Resume Board'`, `${name}: paused (its label says Resume)`);
            // Focus is back on More after the action; a tap (not the keyboard) opens the menu without moving focus to an item.
            await waitFor(`document.activeElement && document.activeElement.id === 'mb-more-btn'`, `${name}: focus back on More after an item`);
            const c = await ev(`(() => { const r = document.getElementById('mb-more-btn').getBoundingClientRect(); return { x: r.x + r.width / 2, y: r.y + r.height / 2 }; })()`);
            await Input.dispatchTouchEvent({ type: 'touchStart', touchPoints: [c] });
            await Input.dispatchTouchEvent({ type: 'touchEnd', touchPoints: [] });
            await waitFor(shown('#mb-select-btn'), `${name}: a tap opens the menu`);
            assert.equal(await active(), 'mb-more-btn', 'a tap leaves focus on More');
            assert.equal(await ev(`document.getElementById('mb-pause-btn').textContent.trim()`), 'Resume Board', 'the menu shows the current label');
            // Red is Delete's alone in the menu: paused, Resume Board is in the warning colour (UX's NIT on #4538).
            const colors = await ev(`(() => { const c = id => getComputedStyle(document.getElementById(id)).color, probe = document.createElement('span');
                probe.style.color = 'var(--warning)'; document.body.appendChild(probe); const w = getComputedStyle(probe).color; probe.remove();
                return { pause: c('mb-pause-btn'), del: c('mb-delete-btn'), warning: w }; })()`);
            assert.equal(colors.pause, colors.warning, `${name}: Resume Board is in the warning colour (${JSON.stringify(colors)})`);
            assert.notEqual(colors.pause, colors.del, `${name}: not Delete's red`);
            await clickItem('mb-pause-btn');
            await waitFor(`document.getElementById('mb-pause-btn').textContent.trim() === 'Pause Board'`, `${name}: resumed`);
            await openMenu(); await clickItem('mb-delete-btn');
            await waitFor(shown('#confirm-modal'), `${name}: Delete asks first`);
            assert.match(await ev(`document.getElementById('confirm-modal-title').textContent`), /Delete Board/);
            await ev(`document.querySelector('#confirm-modal [data-on-click="m-hide-confirm-modal"]').click(); true`);
            await waitFor(`!${shown('#confirm-modal')}`, `${name}: cancelled`);
            assert.equal(await ev(`fetch('/api/board/${B}/messages/all?limit=1&offset=0').then(r => r.ok)`), true, 'the board is still there');

            console.log(`${name}: an outside click closes it`);
            await openMenu();
            await ev(`document.getElementById('mb-messages').click(); true`);
            assert.equal(await open(), false);
        }
        await ev(`document.documentElement.style.fontSize = ''; fetch('/api/board/${B}', { method: 'DELETE' }).then(r => r.status)`);
        console.log('board_more_menu: ok');
        await client.close();
        process.exit(0);
    } catch (e) {
        console.error('FAIL', e && e.stack || e);
        try { await ev(`fetch('/api/board/${B}', { method: 'DELETE' }).then(r => r.status)`); } catch { /* best effort */ }
        try { await client.close(); } catch { /* closing */ }
        process.exit(1);
    }
})();
