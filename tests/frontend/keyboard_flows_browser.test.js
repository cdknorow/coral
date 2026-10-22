// Keyboard, motion and zoom checks that the dashboard keeps (#4026):
//   1. The Docs page list is operable by keyboard: each item is focusable and Enter or Space opens it (it was a plain list item).
//   2. Reduced motion: with prefers-reduced-motion set, no animation runs forever (the pulsing dots and spinners stop).
//   3. Reflow: at 640 px (a 1280 px screen at 200% zoom) and 320 px (400%) the home page, an agent's Chat view and Settings have
//      no horizontal page scroll and nothing wider than the screen.
//   4. The Chat composer's tab order: the text box first, then Stop, Send and Send options (the toolbar used to come first in the DOM).
//   5. An agent row's action menu (#4621): the button names the agent and says it opens a menu and whether it is open; Enter moves the focus to the first item, Down and Up move
//      between items, Escape closes it with the focus back on the button, and a redraw of the list while it is open puts the focus on the button, not the row.
// Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { settle } = require('./helpers/wait');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const sleep = ms => new Promise(r => setTimeout(r, ms));
    const key = async (k, code, shift = false) => { const base = { key: k, code: k === ' ' ? 'Space' : k, windowsVirtualKeyCode: code, modifiers: shift ? 8 : 0 }; await Input.dispatchKeyEvent({ type: 'keyDown', ...base, ...(k === ' ' ? { text: ' ' } : k === 'Enter' ? { text: '\r' } : {}) }); await Input.dispatchKeyEvent({ type: 'keyUp', ...base }); };
    const load = async (w, h, mobile = false) => {
        await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('docs-bot', 'A', 6, { sleeping: false }); window.__m.add('api-worker', 'B', 3, { sleeping: false }); true`);
        await settle(ev, 600); // sleep-ok: the first live list renders from several answers; there is no single completion event
    };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });

        console.log('1. Docs list by keyboard');
        await load(1280, 800);
        await ev(`window.switchNavTab('docs'); true`);
        await waitFor(`document.querySelectorAll('#docs-nav-list li').length >= 2`, 'the docs list', 8000);
        const items = await ev(`[...document.querySelectorAll('#docs-nav-list li')].map(li => ({ tab: li.tabIndex, role: li.getAttribute('role') }))`);
        assert.ok(items.every(i => i.tab === 0 && i.role === 'button'), 'every doc item is focusable and says it is a button');
        for (const [k, code] of [['Enter', 13], [' ', 32]]) {
            const want = k === 'Enter' ? 1 : 2;   // a different item each time, so the change is the key's
            await ev(`document.querySelectorAll('#docs-nav-list li')[${want}].focus(); true`);
            await key(k, code);
            await waitFor(`document.querySelectorAll('#docs-nav-list li')[${want}].getAttribute('aria-current') === 'true'`, `${JSON.stringify(k)} opens the focused doc`, 5000);
            assert.equal(await ev(`document.querySelectorAll('#docs-nav-list li[aria-current]').length`), 1, 'one item is current');
        }
        console.log('   ✓ Enter and Space open the focused doc; the open one is aria-current');

        console.log('2. Reduced motion');
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-reduced-motion', value: 'reduce' }] });
        await load(1280, 800);
        await ev(`window.selectLiveSession('docs-bot', 'claude', 'docs-bot-id', 'local'); true`);
        await settle(ev, 1200); // sleep-ok: the Chat view settles over several answers
        const forever = await ev(`document.getAnimations().filter(a => a.effect && a.effect.getTiming().iterations === Infinity).map(a => a.animationName || a.transitionProperty || 'animation')`);
        assert.deepEqual(forever, [], 'with reduced motion no animation runs forever');
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-reduced-motion', value: 'no-preference' }] });
        console.log('   ✓ no endless animation under prefers-reduced-motion');

        console.log('3. Reflow at 200% and 400% zoom');
        for (const w of [640, 320]) {
            await load(w, 800, w === 320);
            for (const scene of ['home', 'chat', 'settings']) {
                if (scene === 'chat') { await ev(`window.selectLiveSession('docs-bot', 'claude', 'docs-bot-id', 'local'); true`); await settle(ev, 1200); } // sleep-ok: the Chat view settles over several answers
                if (scene === 'settings') { await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'modals.js'); await m.showSettingsModal(); return true; })()`); await settle(ev, 1500); } // sleep-ok: Settings fills from several answers
                const r = await ev(`(() => { const de = document.documentElement; const wide = []; for (const e of document.querySelectorAll('body *')) { const b = e.getBoundingClientRect(); const cs = getComputedStyle(e); if (b.width > 0 && b.right > innerWidth + 2 && cs.position !== 'fixed' && cs.visibility !== 'hidden' && !e.closest('[hidden]')) wide.push((e.id ? '#' + e.id : e.tagName.toLowerCase() + '.' + String(e.className).split(' ')[0]) + ' ' + Math.round(b.right)); if (wide.length > 5) break; } return { over: de.scrollWidth - de.clientWidth, wide }; })()`);
                assert.equal(r.over, 0, `${scene} at ${w} px: no horizontal page scroll`);
                assert.deepEqual(r.wide, [], `${scene} at ${w} px: nothing wider than the screen`);
            }
        }
        console.log('   ✓ home, Chat and Settings reflow at 640 and 320 px');

        console.log('4. Chat composer tab order');
        const focusLabel = `(() => { const a = document.activeElement; return a ? (a.getAttribute('aria-label') || a.id || a.tagName) : ''; })()`;
        const tabs = async (n, shift = false) => { const seen = []; for (let i = 0; i < n; i++) { await Input.dispatchKeyEvent({ type: 'keyDown', key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9, modifiers: shift ? 8 : 0 }); await Input.dispatchKeyEvent({ type: 'keyUp', key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9, modifiers: shift ? 8 : 0 }); seen.push(await ev(focusLabel)); } return seen; };
        for (const [w, mobile] of [[1280, false], [390, true]]) {
            await load(w, 844, mobile);
            await ev(`window.selectLiveSession('docs-bot', 'claude', 'docs-bot-id', 'local'); true`);
            await waitFor(`!!document.querySelector('#command-input') && document.querySelector('#command-toolbar button, .mobile-send-btn')`, 'the composer', 8000);
            await settle(ev, 800); // sleep-ok: the toolbar fills after the first live answers
            // Tab into the composer from the control before it: the first stop in the pane is the text box
            await ev(`(() => { const t = document.getElementById('command-input'); const f = [...document.querySelectorAll('button, a[href], input, textarea, select, [tabindex="0"]')].filter(e => e.offsetParent && !e.disabled && e.tabIndex >= 0); const i = f.indexOf(t); f[i - 1].focus(); return true; })()`);
            const into = await tabs(1);
            assert.equal(into[0], 'Enter command or paste image', `${w} px: Tab from the control before the composer reaches the text box first (got ${into[0]})`);
            const after = await tabs(mobile ? 3 : 12);
            if (mobile) {
                assert.deepEqual(after, ['Add image', 'Send with team reminder', 'Send'], `${w} px: after the text box come Add image and the two send buttons`);
            } else {
                const iStop = after.indexOf('Stop (Escape)'), iSend = after.indexOf('Send command'), iOpts = after.indexOf('Send options');
                assert.ok(iStop >= 0 && iSend > iStop && iOpts > iSend, `${w} px: after the text box Tab reaches Stop, then Send, then Send options (got ${JSON.stringify(after)})`);
            }
            // Shift+Tab from the text box leaves the composer instead of landing on its toolbar
            await ev(`document.getElementById('command-input').focus(); true`);
            const back = await tabs(1, true);
            assert.equal(await ev(`!!document.activeElement.closest('#command-toolbar')`), false, `${w} px: Shift+Tab from the text box does not land on the toolbar (got ${back[0]})`);
        }
        console.log('   ✓ the text box comes first, then Stop, Send and Send options, at 1280 and 390 px');
        console.log('5. An agent row\'s action menu by keyboard');
        await load(1280, 800);
        await ev(`T.render.renderLiveSessions(T.state.liveSessions); true`);
        await waitFor(`!!document.querySelector('#live-sessions-list [data-session-id] .sidebar-kebab-btn')`, 'the agent rows', 6000);
        const BTN = `document.querySelector('#live-sessions-list [data-session-id] .sidebar-kebab-btn')`;
        const attrs = await ev(`(() => { const b = ${BTN}; const m = b.nextElementSibling; return { label: b.getAttribute('aria-label'), popup: b.getAttribute('aria-haspopup'), expanded: b.getAttribute('aria-expanded'), role: m.getAttribute('role'), agent: b.closest('[data-session-id]').getAttribute('aria-label') }; })()`);
        assert.match(attrs.label, /^More actions for .+/, 'the name carries the agent'); assert.equal(attrs.popup, 'menu'); assert.equal(attrs.expanded, 'false'); assert.equal(attrs.role, 'menu');
        assert.ok(attrs.agent.startsWith(attrs.label.replace('More actions for ', '')), 'the agent in the name is the row\'s own: ' + JSON.stringify(attrs));
        const where = () => ev(`(() => { const a = document.activeElement; const b = ${BTN}; return { onButton: a === b, inMenu: !!a.closest('.sidebar-kebab-menu'), onRow: a.classList.contains('session-group-item'), text: (a.textContent || '').replace(/\\s+/g, ' ').trim().slice(0, 30), expanded: b.getAttribute('aria-expanded'), shown: getComputedStyle(b.nextElementSibling).display !== 'none' }; })()`);
        await ev(`${BTN}.focus(); true`);
        await key('Enter', 13);
        await waitFor(`document.activeElement.closest('.sidebar-kebab-menu') !== null`, 'Enter moves the focus into the menu', 4000);
        let w = await where(); assert.ok(w.inMenu && w.expanded === 'true' && w.shown, JSON.stringify(w));
        const first = w.text;
        await key('ArrowDown', 40); w = await where(); assert.ok(w.inMenu && w.text !== first, 'Down moves to the next item: ' + JSON.stringify(w));
        await key('ArrowUp', 38); w = await where(); assert.equal(w.text, first, 'Up moves back');
        await key('End', 35); const last = (await where()).text; assert.notEqual(last, first, 'End goes to the last item');
        await key('Home', 36); assert.equal((await where()).text, first, 'Home goes to the first');
        await key('Escape', 27); w = await where();
        assert.ok(w.onButton && !w.shown && w.expanded === 'false', 'Escape closes it with the focus on the button: ' + JSON.stringify(w));
        await key(' ', 32); await waitFor(`document.activeElement.closest('.sidebar-kebab-menu') !== null`, 'Space opens it too', 4000);
        const ctl = await ev(`(() => { const b = ${BTN}; const id = b.getAttribute('aria-controls'); const m = document.getElementById(id); return { id, owns: !!m && m === b.nextElementSibling, unique: document.querySelectorAll('[id="' + id + '"]').length, allDistinct: new Set([...document.querySelectorAll('#live-sessions-list .sidebar-kebab-btn')].map(x => x.getAttribute('aria-controls'))).size === document.querySelectorAll('#live-sessions-list .sidebar-kebab-btn').length }; })()`);
        assert.ok(ctl.id && ctl.owns && ctl.unique === 1 && ctl.allDistinct, 'aria-controls names the button\'s own menu, by a unique id: ' + JSON.stringify(ctl));
        // Tab out of the last item closes the menu; Shift+Tab from the first item goes back to the button.
        await key('End', 35); await key('Tab', 9);
        await waitFor(`${BTN}.getAttribute('aria-expanded') === 'false' && getComputedStyle(${BTN}.nextElementSibling).display === 'none'`, 'Tab out of the last item closes the menu', 4000);
        assert.equal(await ev(`!!document.activeElement.closest('.sidebar-kebab-menu')`), false, 'and the focus is not in a hidden menu');
        await ev(`${BTN}.focus(); true`); await key('Enter', 13);
        await waitFor(`document.activeElement.closest('.sidebar-kebab-menu') !== null`, 'Enter opens it again', 4000);
        await key('Tab', 9, true);
        await waitFor(`document.activeElement === ${BTN}`, 'Shift+Tab from the first item goes back to the button (the menu stays open beside it)', 4000);
        await key('Escape', 27); assert.equal(await ev(`${BTN}.getAttribute('aria-expanded')`), 'false', 'Escape on the button closes it');
        await ev(`${BTN}.focus(); true`); await key(' ', 32); await waitFor(`document.activeElement.closest('.sidebar-kebab-menu') !== null`, 'Space opens it for the redraw check', 4000);
        // A redraw of the list while it is open (the feed): the focus lands on the button, not the row.
        await ev(`(() => { const s = T.state.liveSessions[0]; s.summary = 'changed while the menu is open'; s.status = 'working'; s.staleness_seconds = 99; T.render.renderLiveSessions(T.state.liveSessions); return true; })()`);
        await settle(ev, 300); // sleep-ok: the redraw is scheduled on a frame; there is no completion event
        w = await where();
        assert.ok(!w.onRow && (w.onButton || w.inMenu), 'after a redraw the focus is on the button (or still in the menu), never dropped to the row: ' + JSON.stringify(w));
        // The group menus (a team's, a folder's) are the same pattern: named for the group, a menu with menu items, and the same keys.
        await ev(`window.__m.add('team-a-1', 'T', 2, { sleeping: false, board_project: 'team-a' }); window.__m.add('team-a-2', 'T', 2, { sleeping: false, board_project: 'team-a' }); T.render.renderLiveSessions(T.state.liveSessions); true`);
        await waitFor(`document.querySelectorAll('#live-sessions-list .group-kebab-btn').length >= 1`, 'a group menu button', 6000);
        const groups = await ev(`[...document.querySelectorAll('#live-sessions-list .group-kebab-btn')].map(b => ({ label: b.getAttribute('aria-label'), popup: b.getAttribute('aria-haspopup'), expanded: b.getAttribute('aria-expanded'), role: b.nextElementSibling.getAttribute('role'), menuLabel: b.nextElementSibling.getAttribute('aria-label') }))`);
        assert.ok(groups.length >= 1 && groups.every(g => /^More actions for .+/.test(g.label) && g.popup === 'menu' && g.expanded === 'false' && g.role === 'menu' && /^Actions for .+/.test(g.menuLabel)), 'every group menu button is named, a menu button, closed: ' + JSON.stringify(groups));
        const GB = `document.querySelector('#live-sessions-list .group-kebab-btn')`;
        await ev(`${GB}.focus(); true`);
        await key('Enter', 13);
        await waitFor(`document.activeElement.closest('.sidebar-kebab-menu') !== null`, 'Enter moves the focus into the group menu', 4000);
        const gItems = await ev(`[...document.activeElement.closest('.sidebar-kebab-menu').querySelectorAll('.overflow-menu-item')].map(i => i.getAttribute('role'))`);
        assert.ok(gItems.length >= 2 && gItems.every(r => r === 'menuitem'), 'the group menu items are menu items: ' + JSON.stringify(gItems));
        const g1 = await ev(`document.activeElement.textContent.replace(/\\s+/g, ' ').trim()`);
        await key('ArrowDown', 40); assert.notEqual(await ev(`document.activeElement.textContent.replace(/\\s+/g, ' ').trim()`), g1, 'Down moves in a group menu');
        await key('Escape', 27);
        assert.ok(await ev(`document.activeElement === ${GB} && ${GB}.getAttribute('aria-expanded') === 'false'`), 'Escape returns the focus to the group button');
        console.log('   ✓ the button names the agent, says it opens a menu and whether it is open; Enter and Space open it onto the first item, arrows, Home and End move, Escape returns to the button');
        console.log('\nPASS keyboard flows: docs list, reduced motion, reflow, composer tab order, row menu');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
