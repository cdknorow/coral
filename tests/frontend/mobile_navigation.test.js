// Mobile views must be mutually exclusive, including home and breakpoint changes.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = process.env.CORAL_URL || 'http://127.0.0.1:8462';
if (/:8420(\/|$)/.test(BASE)) throw new Error('Use an isolated test server');
(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation } = client;
    const evaluate = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
        return r.result.value;
    };
    const size = async width => {
        await Emulation.setDeviceMetricsOverride({ width, height: 740, deviceScaleFactor: 1, mobile: width <= 767 });
        await new Promise(r => setTimeout(r, 100));
    };
    const visible = () => evaluate(`Array.from(document.querySelector('.main-content').children).filter(e => getComputedStyle(e).display !== 'none' && e.getBoundingClientRect().height > 0).map(e => e.id)`);
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await size(390);
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await evaluate(`new Promise(resolve => { const wait = () => typeof window.switchMobileTab === 'function' ? resolve() : setTimeout(wait, 50); wait(); })`);
        for (const width of [320, 390, 767]) {
            await size(width);
            await evaluate(`window._goHome()`);
            assert.deepEqual(await visible(), ['mobile-agent-list']);
            await evaluate(`window.switchNavTab('history')`);
            assert.deepEqual(await visible(), ['welcome-screen']);
            await evaluate(`window.switchNavTab('agents')`);
            assert.deepEqual(await visible(), ['mobile-agent-list']);
            await evaluate(`window.switchNavTab('jobs')`);
            assert.deepEqual(await visible(), ['scheduler-view']);
            await evaluate(`window.mobileBack()`);
            assert.deepEqual(await visible(), ['mobile-agent-list']);
            await evaluate(`window.switchNavTab('workflows'); window.switchMobileTab('agents')`);
            assert.deepEqual(await visible(), ['mobile-agent-list']);
            assert.ok(await evaluate(`(() => { const nav = document.querySelector('.top-nav-tabs'); nav.scrollLeft = nav.scrollWidth; const last = document.getElementById('nav-tab-docs').getBoundingClientRect(); const bounds = nav.getBoundingClientRect(); return document.documentElement.scrollWidth <= innerWidth && last.right <= bounds.right + 1 && bounds.right <= innerWidth; })()`), `tabs reachable at ${width}px`);
            console.log(`PASS ${width}px: exclusive views and reachable navigation`);
        }
        await size(1280);
        assert.deepEqual(await visible(), ['welcome-screen']);
        await size(390);
        assert.deepEqual(await visible(), ['mobile-agent-list']);
        console.log('PASS desktop/mobile breakpoint transitions');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
