// Customize the side panel's tab row (#4103): hide or show each tab, reorder them (buttons and Alt+arrows), a Reset, kept across a
// reload for every agent, Chat (the first tab) always shown, a hidden open tab falling back to it, counts moving with their tab, the
// phone sheet, and 200 percent text. Drives the real dashboard's tab row over CDP; run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { settle } = require('./helpers/wait');

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const { Input } = client;
    const key = async (k, mods = 0) => {
        const vk = { ArrowUp: 38, ArrowDown: 40, Escape: 27, ' ': 32 }[k];
        await Input.dispatchKeyEvent({ type: 'keyDown', key: k, code: k === ' ' ? 'Space' : k, windowsVirtualKeyCode: vk, modifiers: mods, text: k === ' ' ? ' ' : undefined });
        await Input.dispatchKeyEvent({ type: 'keyUp', key: k, code: k === ' ' ? 'Space' : k, windowsVirtualKeyCode: vk, modifiers: mods });
    };
    const ALT = 1;
    const row = () => ev(`[...document.querySelectorAll('#agentic-block-top .agentic-tab[data-tab]')].filter(b => getComputedStyle(b).display !== 'none').map(b => b.dataset.tab)`);
    const listed = () => ev(`[...document.querySelectorAll('#tab-customize .tc-row')].map(r => r.dataset.tab)`);
    const focusId = () => ev(`(() => { const a = document.activeElement; return a ? (a.className.split(' ')[0] + ':' + (a.dataset.tab || '') + (a.dataset.dir || '')) : null; })()`);
    const open = async () => { await ev(`document.getElementById('agentic-tab-customize').click(); true`); await waitFor(`!!document.getElementById('tab-customize')`, 'popover opens'); };
    const close = async () => { await ev(`document.getElementById('tab-customize')?.remove(); true`); };
    const ok = (name) => console.log(`   ✓ ${name}`);
    const load = async () => {
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install(); await ev(`window.__coralReady.then(() => true)`);
        await ev(`window.__m.add('tab-bot', 'A', 3, { sleeping: false }); true`);
        await settle(ev, 600); // sleep-ok: the first live list renders from several answers; no single completion event
        await ev(`window.selectLiveSession('tab-bot', 'claude', 'tab-bot-id', 'local'); true`);
        await ev(`(() => { const p = document.getElementById('agentic-state'); if (p.classList.contains('collapsed') || document.getElementById('agentic-tab-files').getBoundingClientRect().width === 0) window.toggleAgenticPanel(true); return true; })()`);
        await waitFor(`document.getElementById('agentic-tab-files').getBoundingClientRect().width > 0`, 'side panel open', 8000);
    };

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await load();
        await ev(`localStorage.removeItem('coral-agentic-tab-prefs'); true`);
        await load();

        const def = await row();
        assert.ok(def.includes('files') && def.includes('tasks') && def.includes('notes'), JSON.stringify(def));
        ok(`default row: ${def.join(', ')}`);

        // The control: named, a popup button, collapsed until used.
        const btn = await ev(`(() => { const b = document.getElementById('agentic-tab-customize'); return { name: b.getAttribute('aria-label'), pop: b.getAttribute('aria-haspopup'), exp: b.getAttribute('aria-expanded'), last: b.nextElementSibling && b.nextElementSibling.id } })()`);
        assert.deepEqual(btn, { name: 'Customize tabs', pop: 'dialog', exp: 'false', last: 'agentic-mobile-close-btn' });
        ok('a Customize tabs button ends the row (named, aria-haspopup, collapsed)');

        // Open; the first tab (no board in this page) is locked, and the first row's up and last row's down are aria-disabled.
        await open();
        const first = def[0];
        const st = await ev(`(() => { const rows = [...document.querySelectorAll('#tab-customize .tc-row')];
            const lock = rows[0].querySelector('.tc-check');
            return { expanded: document.getElementById('agentic-tab-customize').getAttribute('aria-expanded'), lockDisabled: lock.disabled, lockChecked: lock.checked, upDis: rows[0].querySelector('[data-dir="-1"]').getAttribute('aria-disabled'),
                downDis: rows[rows.length - 1].querySelector('[data-dir="1"]').getAttribute('aria-disabled'), midUp: rows[1].querySelector('[data-dir="-1"]').getAttribute('aria-disabled'), lockDown: rows[0].querySelector('[data-dir="1"]').getAttribute('aria-disabled'),
                focus: document.activeElement.className, names: rows.map(r => r.querySelector('.tc-check').getAttribute('aria-label')) } })()`);
        assert.equal(st.expanded, 'true'); assert.ok(st.lockDisabled && st.lockChecked, 'first tab locked and shown');
        assert.equal(st.upDis, 'true'); assert.equal(st.downDis, 'true'); assert.equal(st.midUp, null);
        assert.equal(st.lockDown, null, 'the locked tab can move down');
        assert.ok(st.names.every(n => /^Show /.test(n)), JSON.stringify(st.names));
        assert.equal(st.focus, 'tc-check', 'focus goes into the popover');
        ok('popover: first tab locked and checked, edge arrows aria-disabled, every row labelled, focus inside');

        // Hide Tasks by Space on its checkbox (keyboard), the row loses it, and the count of Files stays with Files.
        await ev(`document.querySelector('#tab-customize .tc-check[data-tab="tasks"]').focus(); true`);
        await key(' ');
        await waitFor(`!document.querySelector('#tab-customize .tc-check[data-tab="tasks"]').checked`, 'unchecked');
        let r = await row();
        assert.ok(!r.includes('tasks') && r.includes('notes'), JSON.stringify(r));
        assert.equal(await focusId(), 'tc-check:tasks', 'focus stays on the toggled checkbox');
        ok('Space on a checkbox hides that tab and keeps focus there');

        // Move Notes up by the button, then Alt+ArrowDown on its checkbox moves it back; focus stays on what was used.
        const before = await listed();
        await ev(`document.querySelector('#tab-customize .tc-move[data-tab="notes"][data-dir="-1"]').click(); true`);
        const after = await listed(); const i = before.indexOf('notes');
        assert.equal(after.indexOf('notes'), i - 1, JSON.stringify({ before, after }));
        const shown = await row(); // the popover also lists the hidden Tasks; the real row does not
        assert.deepEqual(shown, after.filter(t => shown.includes(t)), 'the real row follows the list');
        assert.equal(await focusId(), 'tc-move:notes-1', 'focus stays on the move button');
        await ev(`document.querySelector('#tab-customize .tc-check[data-tab="notes"]').focus(); true`);
        await key('ArrowDown', ALT);
        assert.deepEqual(await listed(), before, 'Alt+ArrowDown moved it back');
        assert.equal(await focusId(), 'tc-check:notes');
        ok('up/down buttons and Alt+Up/Down reorder the real row; focus stays on the control');

        // A count moves with its tab.
        const c = await ev(`(() => { const b = document.getElementById('agentic-tab-notes'); b.querySelector('.agentic-tab-count').textContent = '6'; const at = [...b.parentElement.children].indexOf(b);
            document.querySelector('#tab-customize .tc-move[data-tab="notes"][data-dir="-1"]').click();
            return { count: b.querySelector('.agentic-tab-count').textContent, moved: [...b.parentElement.children].indexOf(b) !== at, same: document.getElementById('agentic-tab-notes') === b } })()`);
        assert.deepEqual(c, { count: '6', moved: true, same: true });
        ok('a tab keeps its count when it moves');
        const custom = await row();

        // Reload keeps it (hidden Tasks, the new order), and applies without any agent chosen.
        await close();
        await load();
        assert.deepEqual(await row(), custom, 'the choice survives a reload');
        ok('a reload keeps the hidden tab and the order');

        // A hidden tab that is open falls back to the first tab: open Notes, hide it.
        await ev(`window.switchAgenticTab('notes', 'top'); true`);
        assert.equal(await ev(`document.querySelector('#agentic-block-top .agentic-tab.active').dataset.tab`), 'notes');
        await open();
        await ev(`document.querySelector('#tab-customize .tc-check[data-tab="notes"]').click(); true`);
        const act = await ev(`document.querySelector('#agentic-block-top .agentic-tab.active')?.dataset.tab`);
        assert.equal(act, first, 'falls back to ' + first);
        assert.equal(await ev(`document.getElementById('agentic-panel-notes').classList.contains('active')`), false);
        // And asking for a hidden tab by code opens the first tab too.
        await ev(`window.switchAgenticTab('tasks', 'top'); true`);
        assert.equal(await ev(`document.querySelector('#agentic-block-top .agentic-tab.active').dataset.tab`), first);
        ok('a hidden open tab (and a request for a hidden tab) falls back to the first tab');

        // The locked tab can't be hidden even by a stale saved list.
        await ev(`localStorage.setItem('coral-agentic-tab-prefs', JSON.stringify({ agent: { order: [], hidden: [${JSON.stringify(first)}] }, team: { order: [], hidden: [] } })); true`);
        await close(); await load();
        assert.ok((await row()).includes(first), 'the first tab is shown despite a saved hide');
        ok('the first tab stays visible even if a saved list hides it');

        // Corrupt storage is ignored.
        await ev(`localStorage.setItem('coral-agentic-tab-prefs', '{not json'); true`);
        await load();
        assert.deepEqual(await row(), def);
        ok('unreadable saved prefs fall back to the default row');

        // What is read back is checked (Security, #4103): unknown and repeated ids dropped, non-strings dropped, missing tabs last, the first tab first and shown.
        await ev(`localStorage.setItem('coral-agentic-tab-prefs', JSON.stringify({ agent: { order: ['notes', 'nope', 'notes', 42, null, { x: 1 }, 'activity', '<img src=x onerror=1>'], hidden: [${JSON.stringify(first)}, 'ghost', 'tasks', 'tasks', 7] }, team: 'junk' })); true`);
        await load();
        const cleaned = await row();
        assert.equal(cleaned[0], first, 'the first tab is first and shown');
        assert.deepEqual(cleaned, [first, 'notes', 'activity', ...def.filter(t => ![first, 'notes', 'activity', 'tasks'].includes(t))], JSON.stringify(cleaned));
        assert.ok(!cleaned.includes('tasks'), 'a valid hidden id still hides');
        await open();
        const listed2 = await listed();
        assert.deepEqual(listed2, [first, 'notes', 'activity', ...def.filter(t => ![first, 'notes', 'activity'].includes(t))], 'the list agrees: every tab can be shown again, none vanished');
        await ev(`document.querySelector('#tab-customize .tc-check[data-tab="notes"]').click(); true`); // a change rewrites the key from the cleaned value
        const stored = JSON.parse(await ev(`localStorage.getItem('coral-agentic-tab-prefs')`));
        const known = new Set(def.concat(['board', 'team-view', 'team-settings', 'team-artifacts', 'knowledge']));
        assert.ok([...(stored.agent.order || []), ...(stored.agent.hidden || []), ...(stored.team.order || []), ...(stored.team.hidden || [])].every(id => known.has(id)), 'only known tab ids are stored: ' + JSON.stringify(stored));
        await close();
        ok('stored lists are validated: unknown, repeated and non-string ids dropped, missing tabs last, the first tab first and shown');

        // Kept per account: with a Hub account the key carries the account tag and the default key is not read.
        await ev(`localStorage.clear(); true`);
        await load();
        await ev(`import((window.__coralStatic || '/static/') + 'local_store.js').then(async m => { m.setStoreOwner('0123456789abcdef'); const t = await import((window.__coralStatic || '/static/') + 'tab_customize.js'); t.applyTabPrefs(); return true; })`);
        await open();
        await ev(`document.querySelector('#tab-customize .tc-check[data-tab="tasks"]').click(); true`);
        assert.equal(await ev(`localStorage.getItem('coral-agentic-tab-prefs')`), null, 'not written under the shared key');
        assert.ok(await ev(`localStorage.getItem('coral-agentic-tab-prefs@0123456789abcdef')`), 'written under the account key');
        await ev(`import((window.__coralStatic || '/static/') + 'local_store.js').then(m => { m.setStoreOwner('fedcba9876543210'); return import((window.__coralStatic || '/static/') + 'tab_customize.js'); }).then(t => { t.applyTabPrefs(); return true; })`);
        assert.ok((await row()).includes('tasks'), 'another account does not read the first one’s choice');
        await close();
        ok('the choice is kept per account (storeKey); another account reads its own');

        // The locked tab can be put anywhere (the operator's call, #4103) but stays shown, in the saved place, across a reload.
        await ev(`localStorage.clear(); true`);
        await load();
        await open();
        await ev(`document.querySelector('#tab-customize .tc-move[data-tab="' + ${JSON.stringify(first)} + '"][data-dir="1"]').click(); true`);
        const moved1 = await row();
        assert.equal(moved1.indexOf(first), 1, 'moved down one: ' + JSON.stringify(moved1));
        assert.equal(await ev(`document.querySelector('#tab-customize .tc-check[data-tab="' + ${JSON.stringify(first)} + '"]').disabled`), true, 'still locked on');
        await ev(`localStorage.setItem('coral-agentic-tab-prefs', JSON.stringify({ agent: { order: ['notes', ${JSON.stringify(first)}], hidden: [${JSON.stringify(first)}] }, team: { order: [], hidden: [] } })); true`);
        await close(); await load();
        const placed = await row();
        assert.deepEqual(placed.slice(0, 2), ['notes', first], 'the saved place is kept after a reload');
        assert.ok(placed.includes(first), 'and the locked tab is shown even though the list says hidden');
        ok('the locked tab can be moved anywhere, stays shown, and keeps its place across a reload');

        // Reset removes the key.
        await ev(`localStorage.clear(); true`);
        await load();
        await open();
        await ev(`document.querySelector('#tab-customize .tc-check[data-tab="tasks"]').click(); true`);
        assert.ok(await ev(`localStorage.getItem('coral-agentic-tab-prefs')`), 'a choice is stored');
        await ev(`document.querySelector('#tab-customize .tc-reset').click(); true`);
        assert.equal(await ev(`localStorage.getItem('coral-agentic-tab-prefs')`), null, 'Reset removes the key');
        await close();
        ok('Reset removes the stored key');

        // Reset to default; Escape closes and returns focus to the button; click-away closes.
        await ev(`localStorage.setItem('coral-agentic-tab-prefs', JSON.stringify({ agent: { order: ['notes'], hidden: ['tasks'] }, team: { order: [], hidden: [] } })); true`);
        await load();
        assert.ok(!(await row()).includes('tasks'));
        await open();
        await ev(`document.querySelector('#tab-customize .tc-reset').click(); true`);
        assert.deepEqual(await row(), def, 'Reset restores the default');
        assert.equal(await ev(`localStorage.getItem('coral-agentic-tab-prefs')`), null, 'Reset leaves no key behind');
        assert.equal(await ev(`document.activeElement.classList.contains('tc-reset')`), true, 'focus stays on Reset');
        await key('Escape');
        assert.equal(await ev(`!!document.getElementById('tab-customize')`), false);
        assert.equal(await ev(`document.activeElement.id`), 'agentic-tab-customize');
        assert.equal(await ev(`document.getElementById('agentic-tab-customize').getAttribute('aria-expanded')`), 'false');
        await open();
        await sleep(150); // sleep-ok: the outside-click listener is added on a timeout after opening
        await ev(`document.body.click(); true`);
        await sleep(50);
        assert.equal(await ev(`!!document.getElementById('tab-customize')`), false, 'a click elsewhere closes it');
        ok('Reset restores the default; Escape closes and returns focus; click-away closes');

        // Right-click on the row opens it.
        await ev(`document.getElementById('agentic-tab-files').dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true })); true`);
        await waitFor(`!!document.getElementById('tab-customize')`, 'right-click opens');
        await close();
        ok('right-click on the row opens the popover');

        // Team context keeps its own list.
        await ev(`document.getElementById('agentic-state').classList.add('team-context'); true`);
        await sleep(50); // sleep-ok: the observer applies on a microtask; give it a tick
        await open();
        const teamIds = await listed();
        assert.ok(teamIds.includes('team-view') && !teamIds.includes('files'), JSON.stringify(teamIds));
        await ev(`document.querySelector('#tab-customize .tc-check[data-tab="knowledge"]').click(); true`);
        await close();
        await ev(`document.getElementById('agentic-state').classList.remove('team-context'); true`);
        await sleep(50); // sleep-ok: observer tick
        assert.deepEqual(await row(), def, 'the agent row is untouched by a team-only change');
        ok('team tabs have their own list; hiding one there leaves the agent row alone');
        await ev(`localStorage.removeItem('coral-agentic-tab-prefs'); true`);

        // Phone: a bottom sheet with 44px move buttons, no drag grip, inside the viewport; also at 200 percent text.
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
        await load();
        await ev(`document.getElementById('agentic-tab-customize').click(); true`);
        await waitFor(`!!document.getElementById('tab-customize')`, 'sheet opens');
        const ph = await ev(`(() => { const p = document.getElementById('tab-customize').getBoundingClientRect(); const m = document.querySelector('#tab-customize .tc-move').getBoundingClientRect();
            return { left: p.left, right: p.right, bottom: p.bottom, top: p.top, w: innerWidth, h: innerHeight, move: [m.width, m.height], grip: getComputedStyle(document.querySelector('#tab-customize .tc-grip')).display } })()`);
        assert.ok(ph.left >= 0 && ph.right <= ph.w + 1 && ph.bottom <= ph.h + 1 && ph.top >= 0, JSON.stringify(ph));
        assert.ok(ph.move[0] >= 44 && ph.move[1] >= 44, JSON.stringify(ph.move));
        assert.equal(ph.grip, 'none');
        // The phone row shows the same tabs the list offers: the team's tabs were visible in an agent's row there.
        const rowIds = await ev(`[...document.querySelectorAll('#agentic-block-top .agentic-tab[data-tab]')].filter(b => getComputedStyle(b).display !== 'none').map(b => b.dataset.tab)`);
        const listIds = await ev(`[...document.querySelectorAll('#tab-customize .tc-row')].map(r => r.dataset.tab)`);
        assert.deepEqual(rowIds.slice().sort(), listIds.slice().sort(), 'phone row and list agree');
        assert.ok(!rowIds.some(t => ['team-view', 'team-settings', 'team-artifacts', 'knowledge'].includes(t)), JSON.stringify(rowIds));
        assert.equal(await ev(`(() => { const t = document.getElementById('agentic-tab-customize').getBoundingClientRect().left; return [...document.querySelectorAll('#agentic-block-top .agentic-tab[data-tab]')].filter(b => getComputedStyle(b).display !== 'none').every(b => b.getBoundingClientRect().left < t); })()`), true, 'the tune button comes after every visible tab');
        const px = () => ev(`(() => { const f = s => parseFloat(getComputedStyle(document.querySelector(s)).fontSize); return { title: f('.tc-title'), note: f('.tc-note'), reset: f('.tc-reset'), name: f('.tc-name') }; })()`);
        const base = await px();
        ok(`phone: bottom sheet inside the screen, ${ph.move[0]}px move buttons, no drag grip`);
        await ev(`document.documentElement.style.fontSize = '200%'; true`);
        const z = await ev(`(() => { const p = document.getElementById('tab-customize'); const r = p.getBoundingClientRect(); const foot = p.querySelector('.tc-reset').getBoundingClientRect();
            return { fits: r.left >= 0 && r.right <= innerWidth + 1 && r.bottom <= innerHeight + 1 && r.top >= 0, scrolls: p.scrollHeight > p.clientHeight, resetVisible: foot.width > 0, overflowX: p.scrollWidth > p.clientWidth + 1 } })()`);
        assert.ok(z.fits && !z.overflowX, JSON.stringify(z));
        await sleep(300); // sleep-ok: buttons transition all properties over 0.15s, so a font-size change is measured after it settles
        const big = await px();
        for (const k of Object.keys(base)) assert.ok(big[k] >= base[k] * 1.9, `${k} follows the 200% text size: ${base[k]} -> ${big[k]}`);
        ok('at 200% text the sheet still fits the screen without sideways scroll' + (z.scrolls ? ' (rows scroll inside it)' : ''));
        console.log('\nPASS customize tabs');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
