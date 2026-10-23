// The Docs page for a person using only the keyboard or a screen reader (#6586): the topics are a list box (the open one selected) and one Tab stop (the open one, or the first), and Up, Down, Home and End move between them, so the page's
// text is not 37 Tab presses away; opening a topic with Enter leaves focus on that topic (the list is drawn again, and focus used to fall to the page); and a polite status says which topic is showing
// ("Showing <title>"), since the text changes silently under a reader. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const key = async (k, vk) => { await client.Input.dispatchKeyEvent({ type: 'keyDown', key: k, code: k, windowsVirtualKeyCode: vk, ...(k === 'Enter' ? { text: '\r' } : {}) }); await client.Input.dispatchKeyEvent({ type: 'keyUp', key: k, code: k, windowsVirtualKeyCode: vk }); await new Promise(r => setTimeout(r, 250)); }; // sleep-ok: the handler runs and the list is drawn again
    const at = () => ev(`(() => { const a = document.activeElement; return a && a.closest('#docs-nav-list') ? [...document.querySelectorAll('#docs-nav-list [data-doc]')].indexOf(a) : -1; })()`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await Emulation.setFocusEmulationEnabled({ enabled: true });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install(); await ev(`window.__coralReady.then(() => true)`);
        await ev(`window.switchNavTab('docs'); true`);
        await waitFor(`document.querySelectorAll('#docs-nav-list [data-doc]').length >= 5 && document.querySelectorAll('#docs-content h1, #docs-content h2').length >= 1`, 'the topics and the first one', 15000);
        const n = await ev(`document.querySelectorAll('#docs-nav-list [data-doc]').length`);
        const stops = () => ev(`[...document.querySelectorAll('#docs-nav-list [data-doc]')].filter(e => e.tabIndex === 0).map(e => e.dataset.doc)`);
        assert.equal(await ev(`(() => { const l = document.getElementById('docs-nav-list'); return l.getAttribute('role') + ':' + l.getAttribute('aria-label'); })()`), 'listbox:Docs topics', 'the topics are a named list box');
        let s = await stops();
        assert.equal(s.length, 1, 'the topics are one Tab stop (' + s.length + ' of ' + n + ')');
        assert.equal(await ev(`document.querySelector('#docs-nav-list [data-doc][tabindex="0"]').getAttribute('aria-selected')`), 'true', 'and it is the open topic');
        await ev(`document.querySelector('#docs-nav-list [data-doc][tabindex="0"]').focus(); true`);
        const start = await at();
        await key('ArrowDown', 40); assert.equal(await at(), (start + 1) % n, 'Down moves to the next topic');
        assert.equal((await stops()).length, 1, 'and the one Tab stop moves with it');
        await key('ArrowUp', 38); assert.equal(await at(), start, 'Up moves back');
        await key('End', 35); assert.equal(await at(), n - 1, 'End goes to the last topic');
        await key('Home', 36); assert.equal(await at(), 0, 'Home goes to the first');
        // Enter opens the focused topic and focus stays on it
        await key('ArrowDown', 40); await key('ArrowDown', 40);
        const want = await ev(`document.activeElement.dataset.doc`);
        const title = await ev(`document.activeElement.textContent.trim()`);
        await key('Enter', 13);
        await waitFor(`document.querySelector('#docs-nav-list [data-doc][aria-selected="true"]') && document.querySelector('#docs-nav-list [data-doc][aria-selected="true"]').dataset.doc === ${JSON.stringify(want)}`, 'the topic opens', 8000);
        await new Promise(r => setTimeout(r, 400)); // sleep-ok: the page draws after the fetch
        const f = await ev(`(() => { const a = document.activeElement; return { doc: a && a.dataset && a.dataset.doc, inList: !!(a && a.closest && a.closest('#docs-nav-list')) }; })()`);
        assert.ok(f.inList && f.doc === want, 'after Enter focus is still on the topic that was opened (' + JSON.stringify(f) + ')');
        const st = await ev(`(() => { const e = document.getElementById('docs-status'); return e ? { role: e.getAttribute('role'), live: e.getAttribute('aria-live'), text: e.textContent.trim() } : null; })()`);
        assert.ok(st && st.role === 'status' && st.live === 'polite' && st.text === 'Showing ' + title, 'a polite status says which topic is showing (' + JSON.stringify(st) + ')');
        console.log('PASS Docs: one Tab stop for the topics with arrows, focus stays on the opened topic, a status says which is showing');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
