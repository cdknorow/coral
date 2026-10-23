// The Agents list's menus for a person using only the keyboard or a screen reader (#6524): a team's and an agent's "More actions" button says whether its menu is open (aria-expanded), Enter opens it with focus on
// its first item, the items are menu items, Down and Up move between them, Escape closes it and puts focus back on the button, and Tab leaves it closed. The Display options button says the same. Desktop only (a phone
// has no sidebar). Drives Chrome over CDP through agent_view_harness.js with its mocked agents, against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    const key = async (k, code, vk, text) => { await client.Input.dispatchKeyEvent({ type: 'keyDown', key: k, code, windowsVirtualKeyCode: vk, ...(text ? { text } : {}) }); await client.Input.dispatchKeyEvent({ type: 'keyUp', key: k, code, windowsVirtualKeyCode: vk }); await new Promise(r => setTimeout(r, 250)); }; // sleep-ok: a menu draws after the key
    const active = () => ev(`(() => { const a = document.activeElement; return a ? { label: a.getAttribute('aria-label') || a.textContent.trim().slice(0, 30), role: a.getAttribute('role') || '' } : null; })()`);
    const openMenu = () => ev(`(() => { const m = [...document.querySelectorAll('.sidebar-kebab-menu')].find(x => getComputedStyle(x).display !== 'none'); return m ? { role: m.getAttribute('role'), label: m.getAttribute('aria-label'), items: [...m.querySelectorAll('button, a')].map(i => i.getAttribute('role') || '') } : null; })()`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setFocusEmulationEnabled({ enabled: true }); // the menus close on Tab only while the page has focus (as it does for a person)
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'liveListLoaded', 'settings')`);
        await ev(`window.__m.add('a1', 'A', 2, { sleeping: false, board_project: 'team-x' }); window.__m.add('a2', 'B', 2, { sleeping: true }); window._coralSetLiveSessions(Object.values(window.__m.agents).map(a => window.__m.entry(a))); true`);
        await new Promise(r => setTimeout(r, 1200)); // sleep-ok: the list draws from the mocked agents
        for (const [what, sel] of [['a team', '.group-kebab-btn'], ['an agent', '.session-group-item .sidebar-kebab-btn']]) {
            const btn = `document.querySelector(${JSON.stringify(sel)})`;
            await ev(`${btn}.focus(); true`);
            assert.equal(await ev(`${btn}.getAttribute('aria-expanded')`), 'false', `${what}: the button says its menu is closed`);
            await key('Enter', 'Enter', 13, '\r');
            assert.equal(await ev(`${btn}.getAttribute('aria-expanded')`), 'true', `${what}: and open once Enter opens it`);
            const m = await openMenu();
            assert.ok(m && m.role === 'menu' && m.label && m.items.length >= 3 && m.items.every(r => /^menuitem/.test(r)), `${what}: the menu is a named menu of menu items (${JSON.stringify(m)})`);
            const first = await active(); assert.equal(first.role, 'menuitem', `${what}: focus is on its first item (${JSON.stringify(first)})`);
            await key('ArrowDown', 'ArrowDown', 40); const second = await active(); assert.ok(second.role.startsWith('menuitem') && second.label !== first.label, `${what}: Down moves to the next item`);
            await key('ArrowUp', 'ArrowUp', 38); assert.equal((await active()).label, first.label, `${what}: Up moves back`);
            await key('Escape', 'Escape', 27);
            assert.equal(await openMenu(), null, `${what}: Escape closes the menu`);
            assert.equal(await ev(`${btn}.getAttribute('aria-expanded')`), 'false', `${what}: and the button says closed again`);
            assert.equal(await ev(`document.activeElement === ${btn}`), true, `${what}: focus is back on the button`);
            await key('Enter', 'Enter', 13, '\r'); assert.ok(await openMenu(), `${what}: it opens again`);
            await key('End', 'End', 35); await key('Tab', 'Tab', 9); // Tab walks the items (the design, keyboard_flows); out of the last one it closes the menu
            assert.equal(await openMenu(), null, `${what}: Tab out of the last item leaves the menu closed`);
            assert.equal(await ev(`${btn}.getAttribute('aria-expanded')`), 'false', `${what}: and the button says closed after Tab`);
        }
        // Display options: the button says whether its panel is open, Enter opens it, Escape closes it and focus returns to the button
        const disp = `document.querySelector('#sidebar-summary-btn')`, panel = () => ev(`(() => { const p = document.querySelector('[data-display-panel]'); return !!p && !p.hidden && getComputedStyle(p).display !== 'none' && p.getClientRects().length > 0; })()`);
        await ev(`${disp}.focus(); true`);
        assert.equal(await ev(`${disp}.getAttribute('aria-expanded')`), 'false', 'Display options: the button says its panel is closed');
        await key('Enter', 'Enter', 13, '\r');
        assert.equal(await panel(), true, 'Display options: Enter opens the panel');
        assert.equal(await ev(`${disp}.getAttribute('aria-expanded')`), 'true', 'Display options: and the button says open');
        await key('Escape', 'Escape', 27);
        assert.equal(await panel(), false, 'Display options: Escape closes the panel');
        assert.equal(await ev(`${disp}.getAttribute('aria-expanded')`), 'false', 'Display options: and the button says closed again');
        assert.equal(await ev(`document.activeElement === ${disp}`), true, 'Display options: focus is back on the button');
        console.log('PASS the Agents list menus: aria-expanded follows, menu items, arrows, Escape returns focus, Tab leaves it closed, Display options');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
