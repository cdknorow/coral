// #3707: a page stays visible, and keeps running animation frames, after key events. On the Mac,
// headless Chrome hides the page after an Input.dispatchKeyEvent (Enter and Escape did it), and a hidden
// page runs no animation-frame callbacks, so suites that wait on the app's frame-deferred work failed
// there and passed on Linux. run_cdp_suites.mjs preloads helpers/keep_page_visible.js into every suite to
// prevent that; this suite proves the page is visible and drawing after the keys suites really send.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Input } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { coralReady } = require('./helpers/wait').bind(ev);
    // Frames that really ran in a short window, counted by a callback that re-arms itself.
    const framesRan = () => ev(`new Promise(res => { let n = 0, stop = false; const f = () => { n++; if (!stop) requestAnimationFrame(f); }; f(); setTimeout(() => { stop = true; res(n); }, 300); })`); // raf-ok: counting the frames the page really runs is what this suite tests
    const state = () => ev(`({ visible: document.visibilityState, focus: document.hasFocus() })`);
    const press = async (key, code, vk) => {
        await Input.dispatchKeyEvent({ type: 'keyDown', key, code, windowsVirtualKeyCode: vk, nativeVirtualKeyCode: vk });
        await Input.dispatchKeyEvent({ type: 'keyUp', key, code, windowsVirtualKeyCode: vk, nativeVirtualKeyCode: vk });
    };
    try {
        check('the runner preloads the visibility helper into this suite', CDP.keepsPagesVisible === true);
        await Page.enable();
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();
        const before = await state();
        check('the page starts visible and focused', before.visible === 'visible' && before.focus, JSON.stringify(before));
        for (const [name, key, code, vk] of [['Enter', 'Enter', 'Enter', 13], ['an arrow key', 'ArrowRight', 'ArrowRight', 39], ['Escape', 'Escape', 'Escape', 27], ['Space', ' ', 'Space', 32], ['Tab', 'Tab', 'Tab', 9]]) {
            await press(key, code, vk);
            // visibilityState turns 'hidden' a moment after the key; look until it has had time to.
            await new Promise(r => setTimeout(r, 150)); // sleep-ok: visibilityState flips a moment after the key, and there is no event for a state that must stay unchanged
            const after = await state();
            const frames = await framesRan();
            check(`after ${name} the page is still visible, focused and drawing frames`, after.visible === 'visible' && after.focus && frames >= 3, JSON.stringify({ ...after, frames }));
        }
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
