// Keyboard, motion and zoom checks that the dashboard keeps (#4026):
//   1. The Docs page list is operable by keyboard: each item is focusable and Enter or Space opens it (it was a plain list item).
//   2. Reduced motion: with prefers-reduced-motion set, no animation runs forever (the pulsing dots and spinners stop).
//   3. Reflow: at 640 px (a 1280 px screen at 200% zoom) and 320 px (400%) the home page, an agent's Chat view and Settings have
//      no horizontal page scroll and nothing wider than the screen.
// Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const sleep = ms => new Promise(r => setTimeout(r, ms));
    const key = async (k, code) => { const base = { key: k, code: k === ' ' ? 'Space' : k, windowsVirtualKeyCode: code }; await Input.dispatchKeyEvent({ type: 'keyDown', ...base, ...(k === ' ' ? { text: ' ' } : k === 'Enter' ? { text: '\r' } : {}) }); await Input.dispatchKeyEvent({ type: 'keyUp', ...base }); };
    const load = async (w, h, mobile = false) => {
        await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('docs-bot', 'A', 6, { sleeping: false }); window.__m.add('api-worker', 'B', 3, { sleeping: false }); true`);
        await sleep(600); // sleep-ok: the first live list renders from several answers; there is no single completion event
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
        await sleep(1200); // sleep-ok: the Chat view settles over several answers
        const forever = await ev(`document.getAnimations().filter(a => a.effect && a.effect.getTiming().iterations === Infinity).map(a => a.animationName || a.transitionProperty || 'animation')`);
        assert.deepEqual(forever, [], 'with reduced motion no animation runs forever');
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-reduced-motion', value: 'no-preference' }] });
        console.log('   ✓ no endless animation under prefers-reduced-motion');

        console.log('3. Reflow at 200% and 400% zoom');
        for (const w of [640, 320]) {
            await load(w, 800, w === 320);
            for (const scene of ['home', 'chat', 'settings']) {
                if (scene === 'chat') { await ev(`window.selectLiveSession('docs-bot', 'claude', 'docs-bot-id', 'local'); true`); await sleep(1200); } // sleep-ok: the Chat view settles over several answers
                if (scene === 'settings') { await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'modals.js'); await m.showSettingsModal(); return true; })()`); await sleep(1500); } // sleep-ok: Settings fills from several answers
                const r = await ev(`(() => { const de = document.documentElement; const wide = []; for (const e of document.querySelectorAll('body *')) { const b = e.getBoundingClientRect(); const cs = getComputedStyle(e); if (b.width > 0 && b.right > innerWidth + 2 && cs.position !== 'fixed' && cs.visibility !== 'hidden' && !e.closest('[hidden]')) wide.push((e.id ? '#' + e.id : e.tagName.toLowerCase() + '.' + String(e.className).split(' ')[0]) + ' ' + Math.round(b.right)); if (wide.length > 5) break; } return { over: de.scrollWidth - de.clientWidth, wide }; })()`);
                assert.equal(r.over, 0, `${scene} at ${w} px: no horizontal page scroll`);
                assert.deepEqual(r.wide, [], `${scene} at ${w} px: nothing wider than the screen`);
            }
        }
        console.log('   ✓ home, Chat and Settings reflow at 640 and 320 px');
        console.log('\nPASS keyboard flows: docs list, reduced motion, reflow');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
