// Toasts and notices on a phone do not sit over the controls (#6162, found on a Windows run: the data-folder warning covered Wake and a dialog's Cancel; #6160's toasts covered Send): at 390 px the update notice, the
// warnings and a short toast are at the top, the composer, Send and Wake at the bottom are all still hit-testable with all of them open; on a desktop they keep their corners (warnings and short toasts bottom right, the
// update notice top right). Drives Chrome over CDP through agent_view_harness.js against the isolated dev server; the notices come from the page's own functions.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { launchStandIn } = require('./helpers/stand_in_agent');
const { guardSettings } = require('./helpers/settings_guard');
const BASE = require('./test_base').BASE;

async function httpJson(method, p, body) { const r = await fetch(BASE + p, { method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) }); let data = null; try { data = await r.json(); } catch { /* no body */ } return { status: r.status, data }; }

(async () => {
    const { Page, Emulation, ev, waitFor, PRELOAD, install, select, firstPage } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const guard = await guardSettings(); let standIn = null;
    // every notice at once: the three warnings, the update notice, and two short toasts (one an error)
    const openAll = () => ev(`(async () => { const u = await import(${S} + 'update_check.js'), n = await import(${S} + 'notifications.js');
        document.querySelectorAll('.toast, .update-toast').forEach(e => e.remove());
        u.showSecurityWarning('WARNING: the data folder can be read by Users. Move the folder (CORAL_DATA_DIR) to one only you can read.'); u.showFirewallWarning('Windows Firewall lets other computers reach Coral on every port.'); u.showDatabaseWarning('The sessions database is damaged. Stop Coral and copy the data folder.');
        const { default: _x } = {}; await fetch('/api/system/update-check').catch(() => {});
        const t = document.createElement('div'); t.id = 'update-toast'; t.className = 'update-toast'; t.innerHTML = '<div class="update-toast-body"><div class="update-toast-header"><span class="update-toast-title">Coral v9.9.9 available</span><button class="update-toast-close" title="Dismiss">&times;</button></div></div>'; (document.getElementById('warning-toasts') || document.body).prepend(t);
        n.showToast('Attached shot-1.png', false, 600000); n.showToast('Could not do that', true, 600000); return true; })()`);
    const hits = sels => ev(`(() => { const out = {}; for (const [k, sel] of Object.entries(${JSON.stringify(sels)})) { const e = document.querySelector(sel); if (!e || !e.getClientRects().length) { out[k] = 'missing'; continue; } const b = e.getBoundingClientRect(); const t = document.elementFromPoint(b.left + b.width / 2, b.top + b.height / 2); out[k] = !!t && (t === e || e.contains(t)) ? 'ok' : 'covered by ' + (t ? t.tagName + '#' + t.id + '.' + String(t.className).slice(0, 40) : 'nothing'); } return out; })()`);
    try {
        standIn = await launchStandIn(httpJson, { displayName: 'Toast QA' });
        if (!standIn.sid) throw new Error('no stand-in agent: ' + standIn.diag);
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        // ---- a phone
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true }); await Emulation.setTouchEmulationEnabled({ enabled: true });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        // an awake agent: the composer and Send
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.selectLiveSession(${JSON.stringify(standIn.name)}, 'claude', ${JSON.stringify(standIn.sid)}, 'local'); true`);
        await waitFor(`!!document.querySelector('#command-input') && document.querySelector('.mobile-send-btn[data-on-click="cmd-send"]').getClientRects().length > 0`, 'the composer', 20000);
        await openAll();
        await waitFor(`document.querySelectorAll('.toast').length >= 2 && document.querySelectorAll('.update-toast').length >= 4`, 'the notices', 8000);
        const geo = await ev(`(() => { const r = e => { const b = e.getBoundingClientRect(); return { t: Math.round(b.top), b: Math.round(b.bottom), l: Math.round(b.left), r: Math.round(b.right) }; }; const st = document.getElementById('warning-toasts');
            return { bar: r(document.querySelector('header.top-bar')), notes: [...st.children].map(c => r(c)), stack: r(st), toasts: [...document.querySelectorAll('.toast')].map(r), update: r(document.getElementById('update-toast')), vh: innerHeight, vw: innerWidth, composer: r(document.getElementById('command-input')), send: r(document.querySelector('.mobile-send-btn[data-on-click="cmd-send"]')) }; })()`);
        check('phone: the notices start below the top bar, not over it', geo.stack.t >= geo.bar.b, JSON.stringify({ bar: geo.bar, stack: geo.stack }));
        check('phone: each notice is one line of about 50 px until opened (four notices about 200 px)', geo.notes.length >= 4 && geo.notes.every(n => n.b - n.t <= 58), JSON.stringify(geo.notes));
        const bar = await hits({ picker: '#nav-select', home: 'header.top-bar button:nth-of-type(1)' });
        const barAll = await ev(`[...document.querySelectorAll('header.top-bar button, header.top-bar a')].filter(e => e.getClientRects().length).map(e => { const b = e.getBoundingClientRect(); const t = document.elementFromPoint(b.left + b.width / 2, b.top + b.height / 2); return (t === e || e.contains(t)) ? 'ok' : (e.id || e.className || e.tagName) + ' covered by ' + (t ? t.tagName + '#' + t.id + '.' + String(t.className).slice(0, 30) : 'nothing'); })`);
        check('phone: every control in the top bar is hit-testable with all the notices open', barAll.length >= 4 && barAll.every(v => v === 'ok'), JSON.stringify(barAll));
        // a notice opens on its arrow: the details show, the arrow says so; folded again they are clipped but still in the page for a screen reader
        const open = await ev(`(() => { const n = document.getElementById('security-warning-toast'), m = n.querySelector('.update-toast-more'), t = n.querySelector('.security-warning-text'); const sz = () => { const b = t.getBoundingClientRect(); return Math.round(b.width) * Math.round(b.height); };
            const folded = { exp: m.getAttribute('aria-expanded'), area: sz(), text: t.textContent.length, name: m.getAttribute('aria-label'), w: Math.round(m.getBoundingClientRect().width), h: Math.round(m.getBoundingClientRect().height) };
            m.click(); const opened = { exp: m.getAttribute('aria-expanded'), area: sz(), name: m.getAttribute('aria-label'), expanded: n.classList.contains('expanded') }; m.click(); return { folded, opened, back: m.getAttribute('aria-expanded') }; })()`);
        check('phone: the arrow opens and folds a notice (44 px, named, aria-expanded), and the folded details stay in the page', open.folded.exp === 'false' && open.folded.area <= 4 && open.folded.text > 40 && open.folded.w >= 44 && open.folded.h >= 44 && open.opened.exp === 'true' && open.opened.area > 1000 && open.opened.name === 'Hide details' && open.back === 'false', JSON.stringify(open));
        check('phone: the column of notices is at the top, in the screen, and ends above the composer', geo.stack.t >= 0 && geo.stack.b < geo.composer.t && geo.stack.l >= 0 && geo.stack.r <= geo.vw, JSON.stringify(geo.stack));
        check('phone: the update notice is inside that column (not a second layer on top of it)', geo.update.t >= geo.stack.t - 1 && geo.update.b <= geo.stack.b + 1, JSON.stringify(geo.update));
        check('phone: the short toasts are at the top, inside the screen, above the composer', geo.toasts.length >= 2 && geo.toasts.every(t => t.t >= 0 && t.b < geo.composer.t && t.l >= 0 && t.r <= geo.vw), JSON.stringify(geo.toasts));
        check('phone: the short toasts are below the notices, not over them', geo.toasts.every(t => t.t >= geo.stack.b - 1), JSON.stringify({ stack: geo.stack, toasts: geo.toasts }));
        const noticeButtons = await ev(`[...document.querySelectorAll('#warning-toasts .update-toast-more, #warning-toasts .update-toast-close')].map(e => { const b = e.getBoundingClientRect(); const t = document.elementFromPoint(b.left + b.width / 2, b.top + b.height / 2); return (t === e || e.contains(t)) ? 'ok' : (e.className + ' covered by ' + (t ? t.tagName + '.' + String(t.className).slice(0, 30) : 'nothing')); })`);
        check('phone: every notice arrow and dismiss is hit-testable while short toasts are shown', noticeButtons.length >= 7 && noticeButtons.every(v => v === 'ok'), JSON.stringify(noticeButtons));
        const [a, b] = geo.toasts.slice().sort((x, y) => x.t - y.t);
        check('phone: two short toasts are one under the other, not on each other', !!b && a.b <= b.t + 1, JSON.stringify(geo.toasts));
        const seen = await hits({ toast: '.toast', errorToast: '.toast.error' });
        check('phone: the short toasts are on top of the notices, not hidden under them', Object.values(seen).every(v => v === 'ok'), JSON.stringify(seen));
        // a long toast is two lines, a tap shows the rest
        const LONG = 'Could not save the file because the folder it is in cannot be written to by this account, and the disk may be full or read-only; check the folder and try again later, or choose another place for it to go.';
        await ev(`(async () => { const n = await import(${S} + 'notifications.js'); n.showToast(${JSON.stringify(LONG)}, true, 600000); return true; })()`);
        const lng = await ev(`(() => { const t = [...document.querySelectorAll('.toast')].find(e => e.textContent.startsWith('Could not save')); const m = t.querySelector('.toast-message'); const r = () => Math.round(t.getBoundingClientRect().height); const h0 = r(); const full0 = m.textContent; t.click(); const h1 = r(); const ex = t.classList.contains('expanded'); t.click(); return { h0, h1, ex, back: r(), same: m.textContent === full0 && full0.length > 150 }; })()`);
        check('phone: a long toast is two lines, a tap shows the rest, another tap folds it, the text is whole either way', lng.h0 <= 90 && lng.h1 > lng.h0 + 20 && lng.ex && lng.back === lng.h0 && lng.same, JSON.stringify(lng));
        await ev(`[...document.querySelectorAll('.toast')].filter(e => e.textContent.startsWith('Could not save')).forEach(e => e.remove()); true`);
        const awake = await hits({ composer: '#command-input', send: '.mobile-send-btn[data-on-click="cmd-send"]', sendTeam: '.mobile-send-team-btn' });
        check('phone, an awake agent: the message box and both Send buttons are not covered', Object.values(awake).every(v => v === 'ok'), JSON.stringify(awake));
        // a sleeping agent: Wake and Restart
        await ev(`document.querySelectorAll('.toast, .update-toast').forEach(e => e.remove()); window.__m.add('qa-sleep-toast', 'S', 30, { sleeping: true }); true`);
        await select('qa-sleep-toast'); await firstPage();
        await waitFor(`!!document.getElementById('sleeping-wake-btn') && document.getElementById('sleeping-wake-btn').getClientRects().length > 0`, 'Wake', 10000);
        await openAll();
        const sleeping = await hits({ wake: '#sleeping-wake-btn', restart: '#sleeping-restart-btn' });
        check('phone, a sleeping agent: Wake and Restart are not covered', Object.values(sleeping).every(v => v === 'ok'), JSON.stringify(sleeping));
        // the launch chooser's Cancel with the notices open
        await ev(`(async () => { await window.showLaunchModal(); return true; })()`);
        await waitFor(`getComputedStyle(document.getElementById('launch-step-chooser')).display !== 'none'`, 'the chooser', 8000);
        const chooser = await hits({ cancel: '#launch-step-chooser .modal-actions button' });
        check("phone: the launch chooser's buttons are not covered", Object.values(chooser).every(v => v === 'ok'), JSON.stringify(chooser));
        // ---- a desktop keeps its corners
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false }); await Emulation.setTouchEmulationEnabled({ enabled: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await waitFor(`!!document.getElementById('agent-search-input')`, 'the page', 15000);
        await openAll();
        await waitFor(`document.querySelectorAll('.toast').length >= 2 && document.querySelectorAll('.update-toast').length >= 4`, 'the notices', 8000);
        const d = await ev(`(() => { const r = e => { const b = e.getBoundingClientRect(); return { t: Math.round(b.top), b: Math.round(b.bottom), l: Math.round(b.left), r: Math.round(b.right) }; };
            return { toast: r(document.querySelector('.toast')), update: r(document.getElementById('update-toast')), warn: r(document.getElementById('security-warning-toast')), vw: innerWidth, vh: innerHeight, clickable: getComputedStyle(document.getElementById('update-toast')).pointerEvents }; })()`);
        const two = await ev(`[...document.querySelectorAll('.toast')].map(t => { const b = t.getBoundingClientRect(); return { t: Math.round(b.top), b: Math.round(b.bottom) }; }).sort((x, y) => x.t - y.t)`);
        check('desktop: two short toasts are one above the other, not on each other', two.length >= 2 && two[0].b <= two[1].t + 1, JSON.stringify(two));
        const dd = await ev(`(() => { const r = e => { const b = e.getBoundingClientRect(); return { t: Math.round(b.top), b: Math.round(b.bottom) }; }; const w = document.getElementById('security-warning-toast'), t = [...document.querySelectorAll('.toast')]; return { warn: r(w), toasts: t.map(r) }; })()`);
        check('desktop: the short toasts are below the warnings, not over them', dd.toasts.every(t => t.t >= dd.warn.b - 1), JSON.stringify(dd));
        check('desktop: the update notice is still at the top right and clickable', d.update.t < 60 && d.update.r > d.vw - 40 && d.clickable === 'auto', JSON.stringify(d.update));
        check('desktop: the warnings and the short toasts are still one column at the bottom right (the toasts at the very bottom, the warnings above them)', d.warn.r > d.vw - 40 && d.toast.b > d.vh - 60 && d.toast.r > d.vw - 40 && d.warn.b <= d.toast.t + 1, JSON.stringify({ warn: d.warn, toast: d.toast }));
    } finally { if (standIn) await standIn.stop().catch(() => {}); await guard.restore().catch(() => {}); }
    const failed = results.filter(r => !r).length;
    console.log(`toasts_clear_of_controls_phone: ${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
