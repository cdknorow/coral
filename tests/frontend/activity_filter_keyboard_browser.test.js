// The Activity panel's Filter dropdown by keyboard (#4026 follow-up): its rows were mouse-only <div>s in a list appended to the end of
// the page. Now Enter on Filter opens it with the focus on the first row, the arrows move, Space or Enter toggles a row (the focus stays
// on it through the redraw), Escape closes it and returns to Filter, and the button says whether the list is open.
// Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const press = async (k, code, vk) => { const base = { key: k, code, windowsVirtualKeyCode: vk }; await Input.dispatchKeyEvent({ type: 'keyDown', ...base, ...(k === 'Enter' ? { text: '\r' } : k === ' ' ? { text: ' ' } : {}) }); await Input.dispatchKeyEvent({ type: 'keyUp', ...base }); };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`window.__m.add('act-bot', 'A', 3, { sleeping: false }); true`);
        await new Promise(r => setTimeout(r, 600)); // sleep-ok: the first live list renders from several answers; no single completion event
        await ev(`window.selectLiveSession('act-bot', 'claude', 'act-bot-id', 'local'); window.switchAgenticTab('activity', 'top'); true`);
        await waitFor(`!!document.querySelector('#event-filters .filter-dropdown-btn')`, 'the Filter button', 8000);
        await new Promise(r => setTimeout(r, 1200)); // sleep-ok: the agent's events load and redraw the panel; no single completion event
        const btn = `document.querySelector('#event-filters .filter-dropdown-btn')`;
        const rows = `[...document.querySelectorAll('#filter-dropdown .filter-dropdown-item')]`;
        const where = () => ev(`(() => { const a = document.activeElement; return { key: a.dataset && a.dataset.key || null, cls: a.className, checked: a.getAttribute && a.getAttribute('aria-checked') }; })()`);

        console.log('1. Open with the keyboard');
        assert.equal(await ev(`${btn}.getAttribute('aria-expanded')`), 'false');
        assert.equal(await ev(`${btn}.getAttribute('aria-haspopup')`), 'menu');
        await ev(`${btn}.focus(); true`);
        await press('Enter', 'Enter', 13);
        await waitFor(`!!document.getElementById('filter-dropdown')`, 'the list', 4000);
        const n = await ev(`${rows}.length`);
        assert.ok(n >= 3, 'the list has rows: ' + n);
        assert.equal(await ev(`${btn}.getAttribute('aria-expanded')`), 'true', 'Filter says the list is open');
        assert.deepEqual(await ev(`${rows}.every(r => r.tabIndex === 0 && r.getAttribute('role') === 'menuitemcheckbox' && r.hasAttribute('aria-checked'))`), true, 'every row is a focusable menu checkbox');
        const first = await ev(`${rows}[0].dataset.key`);
        assert.equal((await where()).key, first, 'the focus is on the first row');

        console.log('2. Arrows move, Space and Enter toggle, the focus stays');
        await press('ArrowDown', 'ArrowDown', 40);
        const second = await ev(`${rows}[1].dataset.key`);
        assert.equal((await where()).key, second, 'ArrowDown moves to the next row');
        await press('ArrowUp', 'ArrowUp', 38);
        await press('ArrowUp', 'ArrowUp', 38);
        assert.equal((await where()).key, await ev(`${rows}[${n - 1}].dataset.key`), 'ArrowUp wraps to the last row');
        await press('ArrowDown', 'ArrowDown', 40);
        assert.equal((await where()).key, first, 'ArrowDown wraps to the first row');
        for (const [k, code, vk] of [[' ', 'Space', 32], ['Enter', 'Enter', 13]]) {
            const before = (await where()).checked;
            await press(k, code, vk);
            await waitFor(`document.activeElement.getAttribute('aria-checked') !== ${JSON.stringify(before)}`, `${JSON.stringify(k)} toggles the row`, 4000);
            const w = await where();
            assert.equal(w.key, first, 'the focus stays on the toggled row after the redraw');
        }
        // after two toggles it is back to where it started; one more hides the kind and the Filter badge appears
        await press(' ', 'Space', 32);
        await waitFor(`!!document.querySelector('#event-filters .filter-btn-badge')`, 'the badge for a hidden kind', 4000);
        assert.equal((await where()).key, first);
        await press(' ', 'Space', 32); // leave nothing hidden
        await waitFor(`!document.querySelector('#event-filters .filter-btn-badge')`, 'the badge to clear', 4000);
        assert.equal(await ev(`${btn}.getAttribute('aria-expanded')`), 'true', 'a redraw of the button keeps aria-expanded while the list is open');

        console.log('3. Escape closes and returns to Filter');
        await press('Escape', 'Escape', 27);
        assert.equal(await ev(`!!document.getElementById('filter-dropdown')`), false, 'the list is gone');
        assert.equal(await ev(`document.activeElement === ${btn}`), true, 'the focus is back on Filter');
        assert.equal(await ev(`${btn}.getAttribute('aria-expanded')`), 'false');
        console.log('   ✓ open, arrows, toggle, Escape');
        console.log('\nPASS activity filter by keyboard');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
