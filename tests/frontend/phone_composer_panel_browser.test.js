// The phone's agent view (#7063, UI/UX's #7059 findings 4, 5 and 6): one side-panel toggle (the agent bar's) while an agent is on screen, the team reminder behind Send options
// (a caret beside the one round Send, as on a desktop), the model and thinking chips saying what they hold when it is a default ("Model: ...", "Thinking: Default"), and the panel's open tab
// saying its name beside its icon. Drives Chrome over CDP through agent_view_harness.js against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const S = `(window.__coralStatic || '/static/')`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const shown = sel => ev(`(() => { const e = document.querySelector(${JSON.stringify(sel)}); return !!e && e.getClientRects().length > 0 && getComputedStyle(e).visibility !== 'hidden'; })()`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true }); await Emulation.setTouchEmulationEnabled({ enabled: true });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        console.log('1. one side-panel toggle: the agent bar\'s while an agent is on screen');
        await ev(`window.__m.add('pc-a', 'A', 3, { sleeping: false, session_id: 'pc-a-id' }); window.selectLiveSession('pc-a', 'claude', 'pc-a-id', 'local'); true`);
        await waitFor(`document.querySelector('#mobile-send-btn')?.getClientRects().length > 0`, 'the agent view', 10000);
        assert.equal(await shown('#popout-panel-toggle-btn'), true, 'the agent bar has the toggle');
        assert.equal(await shown('#panel-toggle-btn'), false, 'and the top bar\'s is gone');
        const toggles = await ev(`[...document.querySelectorAll('#panel-toggle-btn, #popout-panel-toggle-btn, .session-panel-toggle-btn')].filter(e => e.getClientRects().length > 0).length`);
        assert.equal(toggles, 1, 'one toggle on screen');
        console.log('2. the team reminder is behind Send options');
        assert.equal(await ev(`!document.querySelector('.mobile-send-team-btn')`), true, 'no second round button beside Send');
        assert.equal(await shown('.mobile-send-options-btn'), true);
        assert.equal(await ev(`document.querySelector('.mobile-send-options-btn').getAttribute('aria-label')`), 'Send options');
        assert.equal(await shown('.mobile-send-menu'), false, 'closed');
        await ev(`document.querySelector('.mobile-send-options-btn').click(); true`);
        await waitFor(`getComputedStyle(document.querySelector('.mobile-send-menu')).display !== 'none'`, 'the menu', 3000);
        assert.deepEqual(await ev(`[...document.querySelectorAll('.mobile-send-menu .send-menu-item')].map(b => b.textContent.trim())`), ['Send with team reminder']);
        assert.equal(await ev(`(() => { const m = document.querySelector('.mobile-send-menu').getBoundingClientRect(); return m.left >= 0 && m.right <= innerWidth && m.top >= 0; })()`), true, 'inside the screen');
        await ev(`document.querySelector('.mobile-send-options-btn').click(); true`);
        console.log('2b. at 200% text the menu stays on the screen and wraps');
        await ev(`document.documentElement.style.fontSize = '32px'; true`);
        await ev(`document.querySelector('.mobile-send-options-btn').click(); true`);
        await waitFor(`getComputedStyle(document.querySelector('.mobile-send-menu')).display !== 'none'`, 'the menu at 200%', 3000);
        const big = await ev(`(() => { const m = document.querySelector('.mobile-send-menu').getBoundingClientRect(); const b = document.querySelector('.mobile-send-options-btn').getBoundingClientRect(); const i = document.querySelector('.mobile-send-menu .send-menu-item'); return { left: m.left, right: m.right, top: m.top, vw: innerWidth, btnRight: b.right, itemW: i.getBoundingClientRect().width, scrollW: i.scrollWidth, text: i.textContent.trim() }; })()`);
        assert.ok(big.left >= 0 && big.right <= big.vw && big.top >= 0, 'the menu is wholly on the screen at 200% text: ' + JSON.stringify(big));
        assert.ok(big.scrollW <= big.itemW + 1, 'its words wrap inside it: ' + JSON.stringify(big));
        assert.equal(big.text, 'Send with team reminder');
        await ev(`document.querySelector('.mobile-send-options-btn').click(); document.documentElement.style.fontSize = ''; true`);
        console.log('3. the chips say what they hold when it is a default');
        await ev(`(async () => { const m = await import(${S} + 'model_chip.js'); m.updateModelChip({ session_id: 'pc-a-id', agent_type: 'claude', model: '', thinking: '', thinking_actual: '' }, 'local'); return true; })()`);
        await waitFor(`!document.getElementById('model-chip-bar').hidden`, 'the chip bar', 5000);
        const chips = await ev(`['model-chip', 'thinking-chip'].map(id => { const c = document.getElementById(id); const k = c.querySelector('.model-chip-key'); return { id, key: k && getComputedStyle(k).display !== 'none' ? k.textContent : '', text: c.querySelector('.model-chip-text').textContent, name: c.getAttribute('aria-label'), shown: c.getClientRects().length > 0 }; })`);
        const th = chips.find(c => c.id === 'thinking-chip');
        if (th.shown) { assert.equal(th.key, 'Thinking: ', JSON.stringify(th)); assert.ok(th.name.includes(th.key.trim()) && th.name.includes(th.text), 'label in name: ' + JSON.stringify(th)); }
        const mc = chips.find(c => c.id === 'model-chip');
        assert.equal(mc.text, 'CLI default', 'no model set reads CLI default, not the bare word Model: ' + JSON.stringify(mc));
        assert.ok(/default/i.test(mc.text) ? mc.key === 'Model: ' : mc.key === '', 'a default model says "Model:", a named one does not: ' + JSON.stringify(mc));
        console.log('4. the open tab of the panel says its name');
        await ev(`document.getElementById('popout-panel-toggle-btn').click(); true`);
        await waitFor(`document.querySelector('.agentic-state.mobile-panel-overlay') !== null`, 'the panel', 5000);
        const tab = await ev(`(() => { const t = document.querySelector('.agentic-state.mobile-panel-overlay .agentic-tab.active'); if (!t) return null; const a = getComputedStyle(t, '::after'); return { name: t.getAttribute('aria-label'), content: a.content, w: Math.round(t.getBoundingClientRect().width) }; })()`);
        assert.ok(tab, 'a tab is open');
        assert.ok(tab.content.startsWith('"' + tab.name + '"'), `its name is drawn beside its icon: ${JSON.stringify(tab)}`);
        console.log('PASS phone composer and panel');
    } finally { await client.close(); }
})().catch(err => { console.error(err); process.exit(1); });
