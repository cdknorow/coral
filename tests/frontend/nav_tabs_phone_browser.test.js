// At phone width every top nav tab is reachable (#3608): the strip scrolls sideways with no
// scrollbar (#3152), so a tab past the edge has to come into view the ways a person reaches it:
// a swipe, a mouse wheel (a desktop window that narrow), keyboard focus, and the page it opens
// (a link to /docs shows Docs as the current tab). The page itself never scrolls sideways.
// Checked at 390x844 in dark and light. NAV_TABS_SHOTS=<dir> also saves the top bar.
'use strict';

const CDP = require('chrome-remote-interface');
const fs = require('node:fs');
const path = require('node:path');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation, Input } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const settle = (ms = 300) => ev(`new Promise(r => setTimeout(() => r(true), ${ms}))`); // sleep-ok: scroll and layout settle
    const shots = process.env.NAV_TABS_SHOTS || '';
    const shot = async name => {
        if (!shots) return;
        fs.mkdirSync(shots, { recursive: true });
        const s = await Page.captureScreenshot({ format: 'png', clip: { x: 0, y: 0, width: 390, height: 120, scale: 2 } });
        fs.writeFileSync(path.join(shots, `nav-tabs-${name}.png`), Buffer.from(s.data, 'base64'));
    };
    // The visible tabs, and whether each lies wholly inside the strip and the window.
    const TABS = `[...document.querySelectorAll('.top-nav-tabs .top-nav-tab')].filter(b => !b.hidden && getComputedStyle(b).display !== 'none')`;
    const inView = `(b => { const n = document.querySelector('.top-nav-tabs').getBoundingClientRect(), r = b.getBoundingClientRect();
        return r.left >= n.left - 1 && r.right <= n.right + 1 && r.right <= innerWidth + 1 && r.width > 0; })`;
    const state = () => ev(`(() => { const n = document.querySelector('.top-nav-tabs');
        return { tabs: ${TABS}.map(b => b.textContent.trim()), seen: ${TABS}.filter(${inView}).map(b => b.textContent.trim()),
            scrollLeft: Math.round(n.scrollLeft), max: n.scrollWidth - n.clientWidth,
            pageScroll: document.documentElement.scrollWidth - document.documentElement.clientWidth,
            navRight: Math.round(n.getBoundingClientRect().right), innerWidth }; })()`);
    const navCenter = () => ev(`(() => { const r = document.querySelector('.top-nav-tabs').getBoundingClientRect(); return { x: Math.round(r.left + r.width / 2), y: Math.round(r.top + r.height / 2) }; })()`);
    const open = async (route, touch) => {
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: touch });
        await Emulation.setTouchEmulationEnabled({ enabled: touch, maxTouchPoints: touch ? 5 : 1 });
        const l = Page.loadEventFired();
        await Page.navigate({ url: BASE + route });
        await l;
        await coralReady();
        await waitFor(`${TABS}.length >= 5`, 'the nav tabs', { timeout: 8000 });
    };
    const theme = async t => {
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.clearCustomThemeVars(); m.applyTheme('${t}'); return true; })()`);
        await settle();
    };
    try {
        await Page.enable();
        for (const t of ['dark', 'light']) {
            // A swipe on a phone.
            await open('/', true);
            await theme(t);
            let s = await state();
            check(`${t}: the strip is wider than the phone (the case under test)`, s.max > 2, JSON.stringify(s));
            check(`${t}: the page doesn't scroll sideways and the strip ends inside the window`, s.pageScroll <= 0 && s.navRight <= s.innerWidth, JSON.stringify(s));
            await shot(`${t}-start`);
            const seen = new Set(s.seen);
            const c = await navCenter();
            for (let i = 0; i < 8 && s.scrollLeft < s.max - 1; i++) {
                await Input.synthesizeScrollGesture({ x: c.x, y: c.y, xDistance: -120, yDistance: 0, gestureSourceType: 'touch', speed: 1200 });
                await settle(150);
                s = await state();
                s.seen.forEach(x => seen.add(x));
            }
            check(`${t}: swiping brings every tab fully into view`, s.tabs.every(x => seen.has(x)), JSON.stringify({ missing: s.tabs.filter(x => !seen.has(x)), s }));
            check(`${t}: after the swipes the page still doesn't scroll sideways`, s.pageScroll <= 0, JSON.stringify(s));
            await shot(`${t}-swiped`);

            // A page link: the current tab is in view.
            for (const [route, label] of [['/docs', 'Docs'], ['/analytics', 'Analytics']]) {
                await open(route, true);
                await theme(t);
                await waitFor(`!!document.querySelector('.top-nav-tab.active') && document.querySelector('.top-nav-tab.active').textContent.trim() === '${label}'`, `${label} current`, { timeout: 8000, soft: true });
                const a = await ev(`(() => { const b = document.querySelector('.top-nav-tab.active'); return { tab: b && b.textContent.trim(), inView: !!b && ${inView}(b) }; })()`);
                check(`${t}: opening ${route} shows ${label} as the current tab, in view`, a.tab === label && a.inView, JSON.stringify(a));
                if (route === '/docs') await shot(`${t}-docs-link`);
            }
            // A switch made elsewhere in the page (a docs link, the back button) while the strip is at
            // its start: the new current tab comes into view.
            await open('/', true);
            await ev(`window.switchNavTab('docs'); true`);
            await settle(500);
            const chosen = await ev(`${inView}(document.getElementById('nav-tab-docs'))`);
            check(`${t}: switching to Docs from elsewhere scrolls its tab into view`, chosen === true, String(chosen));

            // The keyboard: each tab, once focused, is in view.
            await open('/', true);
            await ev(`${TABS}[0].focus(); true`);
            const tabs = (await state()).tabs;
            const focused = [];
            for (let i = 0; i < tabs.length - 1; i++) {
                await Input.dispatchKeyEvent({ type: 'keyDown', key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9 });
                await Input.dispatchKeyEvent({ type: 'keyUp', key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9 });
                await settle(150);
                focused.push(await ev(`(() => { const b = document.activeElement; return b && b.classList.contains('top-nav-tab') ? { tab: b.textContent.trim(), inView: ${inView}(b) } : { tab: null }; })()`));
            }
            check(`${t}: tabbing through the strip, each focused tab is in view`, focused.length === tabs.length - 1 && focused.every(f => f.tab && f.inView), JSON.stringify(focused));

            // A desktop window that narrow: a plain (vertical) mouse wheel over the strip scrolls it.
            await open('/', false);
            await theme(t);
            const d = await navCenter();
            const seenW = new Set((await state()).seen);
            let w = await state();
            for (let i = 0; i < 8 && w.scrollLeft < w.max - 1; i++) {
                await Input.dispatchMouseEvent({ type: 'mouseWheel', x: d.x, y: d.y, deltaX: 0, deltaY: 120 });
                await settle(150);
                w = await state();
                w.seen.forEach(x => seenW.add(x));
            }
            check(`${t}: in a narrow desktop window the mouse wheel brings every tab into view`, w.tabs.every(x => seenW.has(x)) && w.pageScroll <= 0, JSON.stringify({ missing: w.tabs.filter(x => !seenW.has(x)), w }));
        }
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        await Emulation.setTouchEmulationEnabled({ enabled: false }).catch(() => {});
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
