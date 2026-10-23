// A toast that carries a button stays until it is used or dismissed (#6192: the "upload stalled" toast expired before Retry could be clicked, and a keyboard-only person is about 80 Tab presses from the toast, so no timer is long
// enough; WCAG 2.2.1): no timer and no fade. A toast with no button keeps its timer (4 s) and fades. The notices column is a region named "Notifications". The timers are recorded in the page, so the test does not wait. Drives
// Chrome over CDP through agent_view_harness.js against the isolated dev server; the page's own showToast.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { Page, Emulation, ev, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        const out = await ev(`(async () => { const n = await import(${S} + 'notifications.js');
            const realSet = window.setTimeout; const delays = [];
            window.setTimeout = (fn, ms, ...a) => { delays.push(ms); return realSet(() => {}, 1e9, ...a); };   // never fires: only the delays matter
            const res = {};
            const made = (msg, err, ms, action) => { delays.length = 0; n.showToast(msg, err, ms, action ? { action: { label: action, onClick() {} } } : {}); const t = [...document.querySelectorAll('.toast')].pop(); const mine = delays.filter(d => ![0, 100, 900].includes(d)); /* the notices' own announcement timers are not the toast's */ return { t, timers: mine.length, ms: mine[0] }; };
            const retry = made('Upload stalled', true, 10000, 'Retry'); res.retry = { timers: retry.timers, hasAction: retry.t.classList.contains('has-action'), button: !!retry.t.querySelector('.toast-action') };
            const undo = made('Removed from saved.', false, 6000, 'Undo'); res.undo = { timers: undo.timers, hasAction: undo.t.classList.contains('has-action') };
            const plain = made('Sent: hi', false); res.plain = { timers: plain.timers, ms: plain.ms, hasAction: plain.t.classList.contains('has-action') };
            // Escape dismisses a focused toast with a button
            retry.t.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); res.escape = !retry.t.isConnected;
            const col = document.getElementById('notices'); res.region = { role: col.getAttribute('role'), name: col.getAttribute('aria-label') };
            document.querySelectorAll('.toast').forEach(e => e.remove()); window.setTimeout = realSet; return res; })()`);
        check('an error toast with Retry has no timer and does not fade', out.retry.timers === 0 && out.retry.hasAction && out.retry.button, JSON.stringify(out.retry));
        check('any toast with a button has no timer (Undo too)', out.undo.timers === 0 && out.undo.hasAction, JSON.stringify(out.undo));
        check('a toast with no button keeps its timer (4 s) and fades', out.plain.timers === 1 && out.plain.ms === 4000 && !out.plain.hasAction, JSON.stringify(out.plain));
        check('Escape dismisses a toast with a button', out.escape);
        check('the notices column is a region named "Notifications"', out.region.role === 'region' && out.region.name === 'Notifications', JSON.stringify(out.region));
    } finally { /* the harness closes the page */ }
    const failed = results.filter(r => !r).length;
    console.log(`toast_action_timing: ${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
