// Top bar vs fixed overlays (#3096, v1.4.7 hotfix):  run via run_cdp_suites.mjs
// On a coarse pointer the 44px touch rules make the top bar taller than its
// desktop 49px. The phone side-panel overlay (and the tablet sidebar and its
// backdrop) must start below the bar's real bottom, so the panel's tabs stay
// tappable, and must follow the bar when its height changes.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const sleep = ms => new Promise(r => setTimeout(r, ms));

// Visible fixed-position elements (other than the bar itself) whose top edge
// lies inside the bar, i.e. that the bar covers.
const UNDER_BAR = `(() => {
    const bar = document.querySelector('header.top-bar').getBoundingClientRect();
    const out = [];
    for (const el of document.querySelectorAll('body *')) {
        if (el.closest('header.top-bar')) continue;
        const cs = getComputedStyle(el);
        if (cs.position !== 'fixed' || cs.display === 'none' || cs.visibility === 'hidden') continue;
        const r = el.getBoundingClientRect();
        if (!r.width || !r.height || r.right <= 0 || r.left >= innerWidth || r.bottom <= 0 || r.top >= innerHeight) continue;
        if (r.top > 0.5 && r.top < bar.bottom - 0.5) out.push((el.id ? '#' + el.id : el.className) + ' top=' + Math.round(r.top) + ' bar=' + Math.round(bar.bottom));
    }
    return out;
})()`;

// The element at the centre of the tab is the tab (nothing covers it).
const HIT = id => `(() => {
    const t = document.getElementById(${JSON.stringify(id)}); const r = t.getBoundingClientRect();
    const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    return { ok: !!at && t.contains(at), at: at ? (at.id || at.className || at.tagName) : null, x: r.left + r.width / 2, y: r.top + r.height / 2 };
})()`;

(async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'coral-topbar-test-'));
    const api = async (route, method = 'GET', body) => {
        const r = await fetch(BASE + route, { method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
        if (!r.ok) throw new Error(`${r.status}: ${await r.text()}`);
        return r.json();
    };
    const launch = await api('/api/sessions/launch', 'POST', { working_dir: dir, agent_type: 'terminal', display_name: 'Top Bar QA' });
    const sid = launch.session_id;
    const sname = launch.session_name || launch.name || '';

    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation, Input } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
        return r.result.value;
    };
    const coarse = async on => {
        await Emulation.setEmulatedMedia({ features: on ? [{ name: 'pointer', value: 'coarse' }, { name: 'hover', value: 'none' }] : [{ name: 'pointer', value: 'fine' }, { name: 'hover', value: 'hover' }] });
        await Emulation.setTouchEmulationEnabled({ enabled: on, maxTouchPoints: on ? 5 : 1 });
    };
    const open = async (width, height, mobile) => {
        await Emulation.setDeviceMetricsOverride({ width, height, deviceScaleFactor: 1, mobile });
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await ev(`new Promise(r => { const w = () => typeof window.selectLiveSession === 'function' ? r() : setTimeout(w, 50); w(); })`);
        await ev(`new Promise(r => { const t = Date.now(); const w = () => (window._coralGetLiveSessions?.() || []).some(s => s.session_id === ${JSON.stringify(sid)}) || Date.now() - t > 8000 ? r() : setTimeout(w, 100); w(); })`);
        await ev(`window.selectLiveSession(${JSON.stringify(sname)}, 'terminal', ${JSON.stringify(sid)})`);
        await sleep(800);
    };
    const tap = async (x, y) => {
        await Input.dispatchTouchEvent({ type: 'touchStart', touchPoints: [{ x, y }] });
        await Input.dispatchTouchEvent({ type: 'touchEnd', touchPoints: [] });
        await sleep(300);
    };
    let savedTabs = null;
    const barBottom = () => ev(`document.querySelector('header.top-bar').getBoundingClientRect().bottom`);
    const panelTop = () => ev(`document.getElementById('agentic-state').getBoundingClientRect().top`);
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);

        // Phone, coarse pointer: the bar is taller than 49px; the open side
        // panel starts at its bottom and its tabs take taps.
        await coarse(true);
        await open(390, 844, true);
        // Tapping tabs persists the selection (coral-agentic-tab-*); put it back
        // afterwards so later suites in this browser start from the same state.
        savedTabs = await ev(`Object.fromEntries(Object.keys(localStorage).filter(k => k.startsWith('coral-agentic-tab-')).map(k => [k, localStorage.getItem(k)]))`);
        await ev(`toggleAgenticPanel(true)`); await sleep(400);
        assert.ok(await ev(`document.getElementById('agentic-state').classList.contains('mobile-panel-overlay')`), 'the side panel is open as the phone overlay');
        const bar = await barBottom();
        assert.ok(bar > 49, `the coarse-pointer top bar is taller than the desktop 49px (bottom ${bar})`);
        assert.ok(Math.abs(await panelTop() - bar) <= 1, `the overlay starts at the bar's bottom (${await panelTop()} vs ${bar})`);
        for (const id of ['agentic-tab-browse', 'agentic-tab-artifacts']) {
            const h = await ev(HIT(id));
            assert.ok(h.ok, `${id} is not covered (elementFromPoint at its centre is ${h.at})`);
            await tap(h.x, h.y);
            assert.ok(await ev(`document.getElementById(${JSON.stringify(id)}).classList.contains('active')`), `tapping ${id} selects it`);
        }
        assert.deepEqual(await ev(UNDER_BAR), [], 'phone: no fixed element starts under the top bar');
        console.log(`PASS phone 390 coarse: bar bottom ${bar}px, overlay below it, Browse and Artifacts tappable`);

        // The bar's height changes (fine pointer: smaller buttons): the overlay follows.
        await coarse(false); await sleep(400);
        const fine = await barBottom();
        assert.ok(fine < bar, `the fine-pointer bar is shorter (${fine} < ${bar})`);
        assert.ok(Math.abs(await panelTop() - fine) <= 1, `the overlay follows the bar (${await panelTop()} vs ${fine})`);
        assert.deepEqual(await ev(UNDER_BAR), [], 'phone fine: no fixed element starts under the top bar');
        console.log(`PASS phone 390 fine: overlay follows the bar to ${fine}px`);

        // iPad with touch: the tablet sidebar and its backdrop start below the bar.
        await coarse(true);
        await open(820, 1180, false);
        await ev(`document.querySelector('.tablet-sidebar-toggle')?.click()`); await sleep(400);
        const tb = await barBottom();
        const side = await ev(`(() => { const s = document.querySelector('.sidebar').getBoundingClientRect(); const b = document.querySelector('.tablet-sidebar-backdrop').getBoundingClientRect(); return { open: document.querySelector('.sidebar').classList.contains('tablet-open'), s: s.top, b: b.top, bh: b.height }; })()`);
        assert.ok(side.open, 'the tablet sidebar is open');
        assert.ok(Math.abs(side.s - tb) <= 1, `the tablet sidebar starts at the bar's bottom (${side.s} vs ${tb})`);
        if (side.bh) assert.ok(Math.abs(side.b - tb) <= 1, `the backdrop starts at the bar's bottom (${side.b} vs ${tb})`);
        assert.deepEqual(await ev(UNDER_BAR), [], 'iPad: no fixed element starts under the top bar');
        console.log(`PASS iPad 820 coarse: sidebar and backdrop start at ${tb}px`);
    } finally {
        if (savedTabs) await ev(`(saved => { for (const k of Object.keys(localStorage)) if (k.startsWith('coral-agentic-tab-')) localStorage.removeItem(k); for (const [k, v] of Object.entries(saved)) localStorage.setItem(k, v); })(${JSON.stringify(savedTabs)})`).catch(() => {});
        await client.close();
        await api(`/api/sessions/live/${encodeURIComponent(sname)}/kill`, 'POST', { agent_type: 'terminal', session_id: sid }).catch(() => {});
        fs.rmSync(dir, { recursive: true, force: true });
    }
})().catch(e => {
    console.error('FAIL ' + e.message);
    process.exitCode = 1;
});
