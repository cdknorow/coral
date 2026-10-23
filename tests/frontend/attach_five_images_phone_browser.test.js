// Attaching images on a phone (#6160, found by Codex Test in #6157): every file raised an "Attached: x.png" toast, and five of them covered Send and the message box at 390 px. The chips
// already show what was attached, so the toasts are gone; a screen reader is told in a hidden status line ("Attached x.png", "5 images attached"). Real server and a stand-in agent, the
// real upload route, five real images attached one after another at 390 px: no toast, Send is what a tap on it hits, the status line says so.
// The images are their own size and colours: the same bytes as another suite's would make one shared artifact that suite's Remove shared copy no longer deletes.
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const { connect } = require('./agent_view_harness');
const { launchStandIn } = require('./helpers/stand_in_agent');
const { guardSettings } = require('./helpers/settings_guard');
const BASE = require('./test_base').BASE;

async function httpJson(method, p, body) {
    const r = await fetch(BASE + p, { method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
    let data = null; try { data = await r.json(); } catch { /* no body */ }
    return { status: r.status, data };
}

(async () => {
    const { Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const guard = await guardSettings();
    let standIn = null;
    try {
        standIn = await launchStandIn(httpJson, { displayName: 'Attach Five QA' });
        if (!standIn.sid) throw new Error('no stand-in agent: ' + standIn.diag);
        const sid = standIn.sid, name = standIn.name;
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`window.selectLiveSession(${JSON.stringify(name)}, 'claude', ${JSON.stringify(sid)}, 'local'); true`);
        await waitFor(`!!window.T && T.state.currentSession && T.state.currentSession.session_id === ${JSON.stringify(sid)}`, 'the agent is open', { timeout: 15000 });
        const S = `(window.__coralStatic || '/static/')`;
        const attach = i => ev(`(async () => { const c = document.createElement('canvas'); c.width = 37; c.height = 29; const x = c.getContext('2d'); x.fillStyle = '#36c'; x.fillRect(0, 0, 37, 29); x.fillStyle = '#fc3'; x.fillRect(${i * 6}, 0, 7, 29);
            const b = await new Promise(r => c.toBlob(r, 'image/png')); const f = new File([b], 'shot-${i + 1}.png', { type: 'image/png' }); const m = await import(${S} + 'controls.js'); return m.uploadAndInsertImage(f); })()`);
        const state = () => ev(`(() => { const status = document.getElementById('image-attach-status'); const sends = [...document.querySelectorAll('.mobile-send-btn')].filter(b => b.getClientRects().length);
            const hit = sends.map(b => { const r = b.getBoundingClientRect(); const e = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2); return !!e && (e === b || b.contains(e)); });
            const ta = document.getElementById('command-input').getBoundingClientRect(), tr = document.getElementById('command-input'); const e = document.elementFromPoint(ta.left + ta.width / 2, ta.top + ta.height / 2);
            return { uploading: (document.querySelector('#image-attachments .image-uploading') || {}).textContent || '', toasts: [...document.querySelectorAll('.toast')].map(t => t.textContent.trim()), chips: document.querySelectorAll('#image-attachments .image-attachment').length, status: status ? status.textContent : null, role: status && status.getAttribute('role'),
                sends: sends.length, sendHit: hit.length > 0 && hit.every(Boolean), boxHit: e === tr || tr.contains(e) }; })()`);

        check('the first image: the ok reply, a chip, the status line says which file, no toast', await attach(0) === true, 'uploaded');
        let s = await state();
        check('one image: a chip, "Attached shot-1.png" for a screen reader, no toast', s.chips === 1 && s.status === 'Attached shot-1.png' && s.role === 'status' && s.toasts.length === 0, JSON.stringify(s));
        for (let i = 1; i < 5; i++) await attach(i);
        s = await state();
        check('five images: five chips and "5 images attached" in the status line', s.chips === 5 && s.status === '5 images attached', JSON.stringify(s));
        check('nothing is left saying Uploading once they are in', s.uploading === '', JSON.stringify(s.uploading));
        check('five images: not one toast on screen (none covers Send or the message box)', s.toasts.length === 0, JSON.stringify(s.toasts));
        check('five images at 390 px: a tap on Send hits Send, a tap in the message box hits the box', s.sends >= 1 && s.sendHit && s.boxHit, JSON.stringify(s));

        // While uploads are in flight the chip row says so (#6160; #6196's audit: nothing asserted this, and the line could go unnoticed): two uploads are held in the page, the row says
        // "Uploading 2 images…" with the five chips still there, and once they finish it is gone and seven chips are in.
        await ev(`(() => { const real = window.fetch; window.__gate = new Promise(r => { window.__release = r; });
            window.fetch = async (u, o = {}) => { if (String(u).endsWith('/api/upload') && (o.method || 'GET').toUpperCase() === 'POST') await window.__gate; return real(u, o); }; return true; })()`);
        const hold = i => ev(`(() => { window.__ups = (window.__ups || []).concat([(async () => { const c = document.createElement('canvas'); c.width = 41 + ${i}; c.height = 29; const x = c.getContext('2d'); x.fillStyle = '#6c3'; x.fillRect(0, 0, 41, 29); x.fillStyle = '#c36'; x.fillRect(${i}, 0, 6, 29);
            const b = await new Promise(r => c.toBlob(r, 'image/png')); const f = new File([b], 'held-${i}.png', { type: 'image/png' }); const m = await import(${S} + 'controls.js'); return m.uploadAndInsertImage(f); })()]); return true; })()`);
        await hold(1); await hold(2);
        await waitFor(`(document.querySelector('#image-attachments .image-uploading') || {}).textContent === 'Uploading 2 images…'`, 'the Uploading line while two uploads are held', 8000);
        s = await state();
        check('two uploads in flight: the chip row says "Uploading 2 images…" and the five chips are still there', s.uploading === 'Uploading 2 images…' && s.chips === 5, JSON.stringify(s));
        await ev(`window.__release(); Promise.all(window.__ups).then(() => true)`);
        s = await state();
        check('once they are in: no Uploading line, seven chips and "7 images attached"', s.uploading === '' && s.chips === 7 && s.status === '7 images attached', JSON.stringify(s));
    } finally {
        if (standIn) await standIn.stop().catch(() => {});
        await guard.restore().catch(() => {});
    }
    const failed = results.filter(r => !r).length;
    console.log(`attach_five_images_phone: ${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
