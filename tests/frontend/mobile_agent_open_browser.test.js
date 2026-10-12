// A phone-width tap on an agent must open it (T121: on the Cloud Hub it did nothing). The mobile agent list is a
// clone of the sidebar rows; it used to strip each row's inline onclick and re-run the text through eval(), which
// the hub's CSP (script-src 'self', no unsafe-eval) refuses, and the stripped row left csp_compat nothing to run.
// Real touch events on iPhone- and Android-sized viewports; eval and Function are made to throw so a regression to
// either shows up here, on a plain local Coral, the same way the hub's CSP would show it.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = process.env.CORAL_URL || 'http://127.0.0.1:8462';
if (/:8420(\/|$)/.test(BASE)) throw new Error('Use an isolated test server');
const DEVICES = [
    { name: 'iPhone 14', width: 390, height: 844, ua: 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1' },
    { name: 'Pixel 7', width: 412, height: 915, ua: 'Mozilla/5.0 (Linux; Android 14; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Mobile Safari/537.36' },
    { name: 'small phone', width: 320, height: 640, ua: '' },
];
(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation, Input } = client;
    const evaluate = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
        return r.result.value;
    };
    const pause = ms => new Promise(r => setTimeout(r, ms));
    const tap = async selector => {
        const p = await evaluate(`(() => { const e = document.querySelector(${JSON.stringify(selector)}); if (!e) return null; e.scrollIntoView({block:'center'}); const b = e.getBoundingClientRect(); return {x: b.x + b.width/2, y: b.y + b.height/2}; })()`);
        assert.ok(p, `${selector} exists`);
        await Input.dispatchTouchEvent({ type: 'touchStart', touchPoints: [p] });
        await Input.dispatchTouchEvent({ type: 'touchEnd', touchPoints: [] });
        await pause(250);
    };
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        for (const d of DEVICES) {
            await Emulation.setDeviceMetricsOverride({ width: d.width, height: d.height, deviceScaleFactor: 2, mobile: true });
            await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
            if (d.ua) await Emulation.setUserAgentOverride({ userAgent: d.ua });
            await Page.navigate({ url: BASE });
            await Page.loadEventFired();
            await evaluate(`new Promise(resolve => { const wait = () => typeof window.selectLiveSession === 'function' && document.getElementById('live-sessions-list') ? resolve() : setTimeout(wait, 50); wait(); })`);
            // Two sidebar rows, shaped like render.js writes them, and a recorder in front of the real selectLiveSession.
            await evaluate(`(() => {
                window.__eval = 0; window.__picked = [];
                window.eval = function () { window.__eval++; window.__evalAt = new Error().stack; throw new EvalError('eval is not allowed (CSP)'); };
                const orig = window.selectLiveSession;
                window.selectLiveSession = function (...a) { window.__picked.push(a[0]); try { return orig.apply(this, a); } catch (e) { /* the fake session has no server */ } };
                const list = document.getElementById('live-sessions-list');
                list.innerHTML = ['alpha', 'beta'].map(n => '<li class="session-group-item" data-session-id="sid-' + n + '" onclick="selectLiveSession(\\'' + n + '\\', \\'terminal\\', \\'sid-' + n + '\\', \\'local\\')"><span class="session-label">' + n + '</span></li>').join('');
                return import('/static/mobile.js').then(m => m.syncMobileAgentList());
            })()`);
            await pause(300);
            const rows = await evaluate(`[...document.querySelectorAll('#mobile-session-list .session-group-item')].map(e => ({ onclick: e.getAttribute('onclick'), label: e.textContent.trim() }))`);
            assert.equal(rows.length, 2, `${d.name}: both rows are in the mobile list`);
            assert.ok(rows.every(r => /^selectLiveSession\(/.test(r.onclick || '')), `${d.name}: each cloned row keeps its inline onclick (csp_compat runs it on the hub)`);
            assert.equal(await evaluate(`(() => { const t = document.querySelector('#mobile-agent-list .mobile-list-title'); return !!t && !t.hasAttribute('style') && getComputedStyle(t).fontWeight === '600'; })()`), true, `${d.name}: the list title is styled by a class, not a style attribute the hub's style-src refuses`);

            await tap('#mobile-session-list .session-group-item[data-session-id="sid-beta"]');
            assert.deepEqual(await evaluate(`window.__picked`), ['beta'], `${d.name}: a tap selects the agent that was tapped`);
            assert.equal(await evaluate(`getComputedStyle(document.getElementById('mobile-agent-list')).display`), 'none', `${d.name}: the list gives way to the agent`);
            assert.equal(await evaluate(`window.__eval`), 0, `${d.name}: nothing went through eval(): ${await evaluate('window.__evalAt')}`);
            assert.equal(await evaluate(`document.documentElement.scrollWidth <= innerWidth`), true, `${d.name}: no sideways page scroll`);

            // The settings menu must open above the side panel, not behind it.
            await evaluate(`window.toggleAgenticPanel(true)`);
            await tap('.top-bar-settings-wrapper button, #top-bar-settings-btn');
            const over = await evaluate(`(() => { const m = document.querySelector('.top-bar-settings-menu'); if (!m) return 'no menu'; const b = m.getBoundingClientRect(); const top = document.elementFromPoint(b.x + b.width/2, b.y + 12); return m.contains(top) ? 'ok' : 'covered by ' + (top && (top.id || top.className)); })()`);
            assert.equal(over, 'ok', `${d.name}: settings menu is on top of the open panel`);
            console.log(`PASS ${d.name} ${d.width}x${d.height}: tap opens the agent, no eval, menu over the panel`);
        }
    } finally {
        await Emulation.clearDeviceMetricsOverride().catch(() => {});
        await Emulation.setTouchEmulationEnabled({ enabled: false }).catch(() => {});
        await Emulation.setUserAgentOverride({ userAgent: '' }).catch(() => {});
        await client.close();
    }
})().catch(e => { console.error(e); process.exitCode = 1; });
