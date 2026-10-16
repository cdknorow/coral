// #3816: the top navigation at every width (the operator's pick: today's text tabs, spaced; a dropdown on phones).
//  - 1440, 1024, 900 and 768 px: one row of plain text tabs with their normal 24 px gap, never wrapped, never touching, clear of
//    the brand and the action buttons, the active tab marked by its underline (no pill, no icons); when the row is too tight
//    (large text, eight tabs) the last tabs, never the active one, go under a More menu and stay reachable.
//  - 390 px: no tab row and no bottom bar; one 44 px dropdown in the top bar names the current view, opens the list of all views,
//    switches the view, and closes on Escape and on a click outside; the page opened by a link shows its view; no sideways scroll.
//  - Nothing here carries an inline handler.
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    const { Emulation, Input } = client;
    const { waitStable } = require('./helpers/wait').bind(ev);
    const open = async (w, h, { extraTabs = false, bigText = false, path = '/' } = {}) => {
        await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile: w < 500 });
        // A phone is a touch screen: its targets are the touch size (touch.css); wider windows are mouse-driven.
        await Emulation.setEmulatedMedia({ features: w < 500 ? [{ name: 'pointer', value: 'coarse' }, { name: 'hover', value: 'none' }] : [{ name: 'pointer', value: 'fine' }, { name: 'hover', value: 'hover' }] });
        await Emulation.setTouchEmulationEnabled({ enabled: w < 500, maxTouchPoints: w < 500 ? 5 : 1 });
        await Page.navigate({ url: BASE + path });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('nv-a', 'A', 3, { sleeping: false }); true`);
        if (extraTabs) await ev(`document.getElementById('nav-tab-servers').hidden = false; const b = document.getElementById('nav-tab-board'); b.style.display = ''; true`);
        // Large text (a browser zoom or a bigger font setting) is what makes eight tabs too wide for 768 px.
        if (bigText) await ev(`const st = document.createElement('style'); st.textContent = '.top-nav-tab { font-size: 19px !important; }'; document.head.appendChild(st); window.dispatchEvent(new Event('resize')); true`);
        await waitFor(`!!document.querySelector('.top-nav-tab.active')`, 'the nav', 8000);
        await waitStable(`document.querySelectorAll('.top-nav-tab.nav-overflowed').length + ':' + document.getElementById('nav-select-label').textContent`, 'the nav layout', 300).catch(() => {});
    };
    const rects = sel => ev(`[...document.querySelectorAll(${JSON.stringify(sel)})].filter(n => { const r = n.getBoundingClientRect(); return r.width > 0 && r.height > 0; }).map(n => { const r = n.getBoundingClientRect(); return { id: n.id || n.textContent.trim(), left: r.left, right: r.right, top: r.top, bottom: r.bottom, w: r.width, h: r.height }; })`);
    const checkRow = async label => {
        const tabs = await rects('.top-nav-tabs > .top-nav-tab:not(.nav-overflowed), .top-nav-tabs .nav-more-btn');
        assert.ok(tabs.length >= 3, `${label}: tabs are shown (${tabs.length})`);
        const tops = tabs.map(t => Math.round(t.top));
        assert.ok(Math.max(...tops) - Math.min(...tops) <= 2, `${label}: one row, none wrapped (tops ${JSON.stringify(tops)})`);
        for (let i = 1; i < tabs.length; i++) assert.ok(tabs[i].left - tabs[i - 1].right >= 20, `${label}: ${tabs[i - 1].id} and ${tabs[i].id} keep their gap (${(tabs[i].left - tabs[i - 1].right).toFixed(1)} px)`);
        const nav = (await rects('.top-nav-tabs'))[0];
        assert.ok(tabs[tabs.length - 1].right <= nav.right + 1, `${label}: the last tab is inside the row`);
        const actions = (await rects('.top-bar-actions'))[0], brand = (await rects('.top-bar-brand'))[0] || { right: 0 };
        assert.ok(tabs[0].left >= brand.right - 1 && tabs[tabs.length - 1].right <= actions.left + 1, `${label}: clear of the brand and the action buttons`);
        assert.equal(await ev(`!!document.querySelector('.top-nav-tab.active:not(.nav-overflowed)') || !!document.querySelector('.nav-more-btn.active')`), true, `${label}: the active tab is visible`);
        return tabs;
    };
    // Today's look: plain text, the active tab underlined, no pill and no icon in the row.
    const checkLook = async label => {
        const look = await ev(`(() => { const a = document.querySelector('.top-nav-tab.active'), p = document.querySelector('.top-nav-tab:not(.active):not(.nav-more-btn)'); const cs = getComputedStyle(a), cp = getComputedStyle(p);
            return { bg: cs.backgroundColor, shadow: cs.boxShadow, underline: cs.borderBottomWidth + ' ' + cs.borderBottomStyle, padX: cp.paddingLeft + '/' + cp.paddingRight, icon: getComputedStyle(p.querySelector('.material-icons') || p).display, weight: cs.fontWeight }; })()`);
        assert.equal(look.bg, 'rgba(0, 0, 0, 0)', `${label}: no pill behind the active tab`);
        assert.equal(look.shadow, 'none', `${label}: no inset shadow`);
        assert.equal(look.underline, '2px solid', `${label}: the active tab is underlined`);
        assert.equal(look.padX, '0px/0px', `${label}: the tabs are plain text (no side padding)`);
    };
    const reachable = async () => {
        const row = await ev(`[...document.querySelectorAll('.top-nav-tabs > .top-nav-tab[data-arg]:not(.nav-overflowed)')].filter(n => !n.hidden && n.style.display !== 'none').map(n => n.dataset.arg)`);
        const hasMore = await ev(`!!document.querySelector('.top-nav-tabs .nav-more-btn')`);
        let more = [];
        if (hasMore) {
            await ev(`document.querySelector('.top-nav-tabs .nav-more-btn').click(); true`);
            await waitFor(`!document.getElementById('nav-more-menu').hidden`, 'the More menu', 3000);
            more = await ev(`[...document.querySelectorAll('#nav-more-menu .nav-more-item')].map(n => n.dataset.arg)`);
            await ev(`document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); true`);
            await waitFor(`document.getElementById('nav-more-menu').hidden`, 'the menu to close on Escape', 3000);
        }
        const all = await ev(`[...document.querySelectorAll('.top-nav-tabs > .top-nav-tab[data-arg]')].filter(n => !n.hidden && n.style.display !== 'none').map(n => n.dataset.arg)`);
        assert.deepEqual([...row, ...more].sort(), all.sort(), 'every tab is in the row or under More');
        return { row, more };
    };
    const menuOpen = () => ev(`!document.getElementById('nav-more-menu').hidden`);

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });

        console.log('1. 1440, 1024, 900 and 768 px: today\'s plain tabs, spaced, one row, clear of the buttons');
        for (const w of [1440, 1024, 900, 768]) {
            await open(w, 860, { extraTabs: w === 768 });
            await checkRow(`${w}px`);
            await checkLook(`${w}px`);
            const r = await reachable();
            assert.equal(r.more.length, 0, `${w}px: all tabs fit, no More`);
            assert.equal(await ev(`getComputedStyle(document.getElementById('nav-select')).display`), 'none', `${w}px: no phone dropdown`);
            assert.equal(await ev(`!!document.getElementById('bottom-nav')`), false, `${w}px: no bottom bar exists`);
        }
        console.log('   ✓ 24 px gaps, no wrap or touching, underline only, all tabs in the row (768 with Servers and Board too)');

        console.log('2. 768 px with large text: the last tabs go under More, the active tab stays');
        await open(768, 800, { extraTabs: true, bigText: true });
        const tight = await checkRow('768px, eight tabs, large text');
        let r = await reachable();
        assert.ok(r.more.length > 0, 'some tabs are under More');
        assert.ok(!r.more.includes('agents'), 'the first tab stays in the row');
        const pick = r.more[r.more.length - 1];
        await ev(`document.querySelector('.top-nav-tabs .nav-more-btn').click(); true`);
        await waitFor(`!document.getElementById('nav-more-menu').hidden`, 'the More menu', 3000);
        assert.equal(await ev(`getComputedStyle(document.querySelector('#nav-more-menu .nav-more-item .material-icons')).display`), 'none', 'the row\'s More list is text only');
        await ev(`document.querySelector('#nav-more-menu [data-arg=${JSON.stringify(pick)}]').click(); true`);
        await waitFor(`document.getElementById('nav-more-menu').hidden`, 'the menu to close after a choice', 3000);
        await waitFor(`document.querySelector('.top-nav-tab.active').dataset.arg === ${JSON.stringify(pick)}`, `${pick} to become active`, 5000);
        await waitStable(`document.querySelectorAll('.top-nav-tab.nav-overflowed').length`, 'the row to settle', 300).catch(() => {});
        await checkRow('768px after choosing from More');
        assert.equal(await ev(`document.querySelector('.top-nav-tab.active').classList.contains('nav-overflowed')`), false, 'the chosen tab is in the row (the active tab is never hidden)');
        console.log(`   ✓ ${tight.length} shown; More lists ${r.more.join(', ')}; choosing ${pick} activates it and keeps it visible`);

        console.log('3. 390 px: a 44 px current-view dropdown replaces the row');
        await open(390, 844);
        assert.equal(await ev(`getComputedStyle(document.querySelector('.top-nav-tabs')).display`), 'none', 'the tab row is gone');
        assert.equal(await ev(`!!document.getElementById('bottom-nav')`), false, 'no bottom bar');
        const sel = (await rects('#nav-select'))[0];
        assert.ok(sel && sel.h >= 44, `the dropdown is at least 44 px tall on a touch screen (${sel && sel.h})`);
        const actions = (await rects('.top-bar-actions'))[0];
        assert.ok(sel.right <= actions.left + 1 && sel.left >= 0, 'the dropdown sits clear of the action buttons and inside the screen');
        assert.equal(await ev(`document.getElementById('nav-select-label').textContent`), 'Agents', 'it names the current view');
        assert.equal(await ev(`document.getElementById('nav-select').getAttribute('aria-expanded')`), 'false');
        console.log(`   ✓ ${sel.h.toFixed(0)} px tall, names "Agents", clear of the buttons`);

        console.log('4. The dropdown opens the list of all views, switches, and closes on Escape and outside click');
        await ev(`document.getElementById('nav-select').click(); true`);
        await waitFor(`!document.getElementById('nav-more-menu').hidden`, 'the list', 3000);
        assert.equal(await ev(`document.getElementById('nav-select').getAttribute('aria-expanded')`), 'true');
        const items = await ev(`[...document.querySelectorAll('#nav-more-menu .nav-more-item')].map(n => [n.dataset.arg, Math.round(n.getBoundingClientRect().height), n.classList.contains('active'), n.getAttribute('aria-current')])`);
        assert.deepEqual(items.map(i => i[0]), ['agents', 'history', 'workflows', 'jobs', 'tokens', 'docs'], 'every view is listed');
        assert.ok(items.every(i => i[1] >= 44), 'every item is at least 44 px tall');
        assert.deepEqual(items.filter(i => i[2]).map(i => i[0]), ['agents'], 'the current view is marked');
        const box = await ev(`(() => { const m = document.getElementById('nav-more-menu').getBoundingClientRect(), b = document.getElementById('nav-select').getBoundingClientRect(); return { top: m.top, bottom: m.bottom, left: m.left, right: m.right, btnBottom: b.bottom, h: innerHeight }; })()`);
        assert.ok(box.top >= box.btnBottom && box.left >= 0 && box.right <= 390 && box.bottom <= box.h, `the list opens under the dropdown, inside the screen (${JSON.stringify(box)})`);
        await ev(`document.querySelector('#nav-more-menu [data-arg=docs]').click(); true`);
        await waitFor(`document.getElementById('nav-more-menu').hidden`, 'the list to close after a choice', 3000);
        await waitFor(`document.querySelector('.top-nav-tab.active').dataset.arg === 'docs' && document.getElementById('nav-select-label').textContent === 'Docs'`, 'Docs to be current and named', 5000);
        // Escape closes it (and returns focus to the dropdown).
        await ev(`document.getElementById('nav-select').click(); true`);
        await waitFor(`!document.getElementById('nav-more-menu').hidden`, 'the list again', 3000);
        await Input.dispatchKeyEvent({ type: 'rawKeyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
        await Input.dispatchKeyEvent({ type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
        await waitFor(`document.getElementById('nav-more-menu').hidden`, 'Escape to close the list', 3000);
        assert.equal(await ev(`document.activeElement && document.activeElement.id`), 'nav-select', 'focus returns to the dropdown');
        assert.equal(await ev(`document.getElementById('nav-select').getAttribute('aria-expanded')`), 'false');
        // A click outside closes it without switching.
        await ev(`document.getElementById('nav-select').click(); true`);
        await waitFor(`!document.getElementById('nav-more-menu').hidden`, 'the list a third time', 3000);
        await Input.dispatchMouseEvent({ type: 'mousePressed', x: 200, y: 600, button: 'left', clickCount: 1 });
        await Input.dispatchMouseEvent({ type: 'mouseReleased', x: 200, y: 600, button: 'left', clickCount: 1 });
        await waitFor(`document.getElementById('nav-more-menu').hidden`, 'an outside click to close the list', 3000);
        assert.equal(await ev(`document.querySelector('.top-nav-tab.active').dataset.arg`), 'docs', 'closing by an outside click changes nothing');
        console.log('   ✓ lists all six with the current marked, 44 px items, inside the screen; Docs switches; Escape and outside click close');

        console.log('5. A page opened by a link names its view, in both themes, with no sideways scroll');
        for (const theme of ['dark', 'light']) {
            await ev(`localStorage.setItem('coral-theme', ${JSON.stringify(theme)}); true`);
            await open(390, 844, { path: '/docs' });
            await waitFor(`document.getElementById('nav-select-label').textContent === 'Docs'`, `${theme}: the dropdown names Docs after opening /docs`, 8000);
            const s = await ev(`({ page: document.documentElement.scrollWidth, win: innerWidth })`);
            assert.ok(s.page <= s.win, `${theme}: no sideways page scroll (${s.page} > ${s.win})`);
        }
        await ev(`localStorage.removeItem('coral-theme'); true`);
        console.log('   ✓ /docs opens with "Docs" named, dark and light, no sideways scroll');

        console.log('6. No inline handlers');
        await open(390, 844);
        await ev(`document.getElementById('nav-select').click(); true`);
        const inline = await ev(`[...document.querySelectorAll('#nav-select, #nav-select *, #nav-more-menu, #nav-more-menu *, .top-nav-tabs, .top-nav-tabs *')].flatMap(n => [...n.attributes].filter(a => /^on/i.test(a.name)).map(a => n.tagName + '.' + a.name))`);
        assert.deepEqual(inline, [], 'no on* attribute in the navigation');
        console.log('   ✓ none');

        console.log('\nAll navigation checks passed');
    } finally {
        await Emulation.clearDeviceMetricsOverride().catch(() => {});
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
