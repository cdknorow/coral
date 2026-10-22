// The Add image button (#6088), against the real server and a stand-in agent, at 1280 px (a mouse) and 390 px (a phone with touch):
// the button is there and named at each size, 44 px on a phone; picking files uploads them one after another through the existing strip; a photo over 2 MB is made smaller in the browser and a PNG
// is not; an image over 20 MB (16 MB through Coral Hub) is refused before any request; a HEIC is refused in words a person can act on; the picker offers no SVG and no HEIC; the server's retry time is
// shown in the viewer's own time. (The Launch tier through the hub is hub_blocked_controls_browser's.)
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { launchStandIn } = require('./helpers/stand_in_agent');
const { guardSettings } = require('./helpers/settings_guard');
const BASE = require('./test_base').BASE;

async function httpJson(method, p, body, headers = {}) {
    const r = await fetch(BASE + p, { method, headers: { 'Content-Type': 'application/json', ...headers }, body: body === undefined ? undefined : JSON.stringify(body) });
    let data = null; try { data = await r.json(); } catch { /* no body */ }
    return { status: r.status, data };
}

(async () => {
    const { Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const guard = await guardSettings();
    let standIn = null;
    try {
        standIn = await launchStandIn(httpJson, { displayName: 'Attach Image QA' });
        if (!standIn.sid) throw new Error('no stand-in agent: ' + standIn.diag);
        const sid = standIn.sid, name = standIn.name;
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 800, false], [390, 844, true]]) {
            const tag = `${w}px`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            // record each upload request (start and end, the fields, the file), and the toasts
            await ev(`(() => { const real = window.fetch; window.__rec = { ups: [], toasts: [] }; window.__upMode = 'real';
                window.fetch = async (u, o = {}) => { const url = String(u), m = (o.method || 'GET').toUpperCase();
                    if (url.endsWith('/api/upload') && m === 'POST') {
                        const f = o.body.get('file'); const r = { start: performance.now(), name: f.name, type: f.type, size: f.size, session: o.body.get('session_id') }; window.__rec.ups.push(r);
                        if (window.__upMode === 'empty') return new Response('', { status: 200 });
                        if (typeof window.__upMode === 'number') return new Response(JSON.stringify({ error: 'request too large' }), { status: window.__upMode, headers: { 'Content-Type': 'application/json' } });
                        if (window.__upMode === 'throw') throw new TypeError('network down');
                        try { return await real(u, o); } finally { r.end = performance.now(); } }
                    return real(u, o); };
                new MutationObserver(() => { for (const t of document.querySelectorAll('.toast, [role=status].toast, #toast-container *')) { const x = (t.textContent || '').trim(); if (x && !window.__rec.toasts.includes(x)) window.__rec.toasts.push(x); } }).observe(document.body, { childList: true, subtree: true, characterData: true });
                return true; })()`);
            await ev(`window.selectLiveSession(${JSON.stringify(name)}, 'claude', ${JSON.stringify(sid)}, 'local'); true`);
            await waitFor(`!!window.T && T.state.currentSession && T.state.currentSession.session_id === ${JSON.stringify(sid)}`, 'the agent is open', { timeout: 15000 });

            // 1. the button: there, named, a real size, and only the right one is shown
            const btn = await ev(`(() => { const b = document.querySelector('.attach-image-btn'); const r = b.getBoundingClientRect(), cs = getComputedStyle(b); const ta = document.getElementById('command-input').getBoundingClientRect(); const tcs = getComputedStyle(document.getElementById('command-input'));
                const input = document.getElementById('attach-image-input');
                return { shown: r.width > 0 && cs.display !== 'none' && cs.visibility !== 'hidden', name: b.getAttribute('aria-label'), title: b.title, w: Math.round(r.width), h: Math.round(r.height), insideBox: r.left >= ta.left && r.right <= ta.right && r.bottom <= ta.bottom + 1 && r.top >= ta.top,
                    padLeft: parseFloat(tcs.paddingLeft), clear: r.right <= ta.left + parseFloat(tcs.paddingLeft) + 1, accept: input.accept, multiple: input.multiple, hidden: input.hidden, inTab: input.tabIndex, count: document.querySelectorAll('[data-on-click=attach-image]').length }; })()`);
            check(`${tag}: one Add image button, named and with a tooltip, inside the corner of the text box and clear of its text`, btn.shown && btn.count === 1 && btn.name === 'Add image' && btn.title === 'Add image' && btn.insideBox && btn.clear, JSON.stringify(btn));
            check(`${tag}: it is ${mobile ? '44 px (a touch screen)' : '32 px (a mouse)'}`, mobile ? btn.w >= 44 && btn.h >= 44 : btn.w === 32 && btn.h === 32, `${btn.w}x${btn.h}`);
            check(`${tag}: the picker takes several pictures, is hidden from the page and offers no SVG or HEIC`, btn.multiple && btn.hidden && btn.inTab === -1 && !/svg|heic|heif/i.test(btn.accept) && /image\/jpeg/.test(btn.accept), btn.accept);

            // 2. picking two files: both go up, one after the other, with the session; the strip shows both
            const mk = (nameJs, w2, h2, type) => `(async () => { const c = document.createElement('canvas'); c.width = ${w2}; c.height = ${h2}; const x = c.getContext('2d'); x.fillStyle = '#' + Math.floor(Math.random() * 0xffffff).toString(16).padStart(6, '0'); x.fillRect(0, 0, ${w2}, ${h2}); x.fillStyle = '#fff'; x.fillRect(${Math.floor(Math.random() * 10)}, 0, 7, ${h2});
                const b = await new Promise(r => c.toBlob(r, ${JSON.stringify(type)})); return new File([b], ${nameJs}, { type: ${JSON.stringify(type)} }); })()`;
            // the change listener is bound on the first press of the button: press it once (its picker is stopped, no dialog opens), as a person does before choosing
            await ev(`(() => { const i = document.getElementById('attach-image-input'); const stop = e => e.preventDefault(); i.addEventListener('click', stop, { once: true }); document.querySelector('.attach-image-btn').click(); return true; })()`);
            const pick = files => ev(`(async () => { const fs = await Promise.all([${files.join(',')}]); const dt = new DataTransfer(); fs.forEach(f => dt.items.add(f)); const input = document.getElementById('attach-image-input'); input.files = dt.files; input.dispatchEvent(new Event('change', { bubbles: true })); return fs.length; })()`);
            await pick([mk(`'one.png'`, 40, 30, 'image/png'), mk(`'two.jpg'`, 41, 31, 'image/jpeg')]);
            await waitFor(`window.__rec.ups.length === 2 && document.querySelectorAll('#image-attachments .image-attachment').length === 2`, `${tag}: both pictures are attached`, { timeout: 15000 });
            const ups = await ev(`window.__rec.ups`);
            check(`${tag}: two picked pictures go up one after the other, each with the agent's session`, ups[0].name === 'one.png' && ups[1].name === 'two.jpg' && ups[1].start >= ups[0].end && ups.every(u => u.session === sid), JSON.stringify(ups));
            check(`${tag}: the picker is cleared, so the same picture can be chosen again`, await ev(`document.getElementById('attach-image-input').value === ''`));
            await ev(`import(${S} + 'controls.js').then(m => { for (let i = m.getPendingAttachments().length - 1; i >= 0; i--) m.removeAttachment(i); })`);

            // 3. a big photo is made smaller; a PNG is not
            const big = await ev(`(async () => { const m = await import(${S} + 'image_attach.js');
                const noise = (w2, h2, type, q) => { const c = document.createElement('canvas'); c.width = w2; c.height = h2; const x = c.getContext('2d'); const d = x.createImageData(w2, h2); for (let i = 0; i < d.data.length; i += 4) { d.data[i] = Math.random() * 255; d.data[i + 1] = Math.random() * 255; d.data[i + 2] = Math.random() * 255; d.data[i + 3] = 255; } x.putImageData(d, 0, 0);
                    return new Promise(r => c.toBlob(r, type, q)); };
                const jb = await noise(3200, 2000, 'image/jpeg', 0.95); const jf = new File([jb], 'IMG_1.jpeg', { type: 'image/jpeg' });
                const pb = await noise(900, 900, 'image/png'); const pf = new File([pb], 'shot.png', { type: 'image/png' });
                const pj = await m.prepareImage(jf), pp = await m.prepareImage(pf);
                const bmp = await createImageBitmap(pj.file);
                return { jpegBefore: jf.size, jpegAfter: pj.file.size, jpegType: pj.file.type, jpegName: pj.file.name, long: Math.max(bmp.width, bmp.height), short: Math.min(bmp.width, bmp.height), pngBefore: pf.size, pngAfter: pp.file.size, pngSame: pp.file === pf }; })()`);
            check(`${tag}: a ${(big.jpegBefore / 1048576).toFixed(1)} MB photo is made smaller (${(big.jpegAfter / 1048576).toFixed(1)} MB): a JPEG, long side 2560 px, the shape kept`, big.jpegBefore > 2 * 1048576 && big.jpegAfter < big.jpegBefore && big.jpegType === 'image/jpeg' && big.jpegName === 'IMG_1.jpg' && big.long === 2560 && Math.abs(big.short - 1600) <= 1, JSON.stringify(big));
            check(`${tag}: a ${(big.pngBefore / 1048576).toFixed(1)} MB PNG screenshot goes up as it is`, big.pngBefore > 2 * 1048576 && big.pngSame && big.pngAfter === big.pngBefore, JSON.stringify(big));

            // 4. the limits: 20 MB here, 16 MB through Coral Hub; before any request
            const n0 = await ev(`window.__rec.ups.length`);
            await ev(`window.__rec.toasts.length = 0; true`);
            await ev(`(async () => { const f = new File([new Uint8Array(21 * 1048576)], 'huge.png', { type: 'image/png' }); const dt = new DataTransfer(); dt.items.add(f); const i = document.getElementById('attach-image-input'); i.files = dt.files; i.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
            await waitFor(`window.__rec.toasts.some(t => /over 20 MB/.test(t))`, `${tag}: the 21 MB image is refused in words`, { timeout: 8000 });
            check(`${tag}: a 21 MB image makes no request`, (await ev(`window.__rec.ups.length`)) === n0);
            const hub = await ev(`(async () => { const m = await import(${S} + 'image_attach.js'); const mkf = mbs => new File([new Uint8Array(Math.round(mbs * 1048576))], 'x.png', { type: 'image/png' });
                const a = await m.prepareImage(mkf(17), { viaHub: true }), b = await m.prepareImage(mkf(15), { viaHub: true }), c = await m.prepareImage(mkf(17), { viaHub: false });
                return { a: a.error || '', b: !!b.file, c: !!c.file }; })()`);
            check(`${tag}: through Coral Hub the limit is 16 MB (17 refused with the reason, 15 passes); on this computer 17 passes`, /over 16 MB/.test(hub.a) && /Coral Hub/.test(hub.a) && hub.b && hub.c, JSON.stringify(hub));

            // 5. a HEIC: refused in words, before any request, by name and by type
            await ev(`window.__rec.toasts.length = 0; true`);
            await ev(`(async () => { const f = new File([new Uint8Array(1000)], 'IMG_0042.HEIC', { type: 'image/heic' }); const dt = new DataTransfer(); dt.items.add(f); const i = document.getElementById('attach-image-input'); i.files = dt.files; i.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
            await waitFor(`window.__rec.toasts.some(t => /HEIC/.test(t))`, `${tag}: the HEIC refusal`, { timeout: 8000 });
            const heic = await ev(`window.__rec.toasts.find(t => /HEIC/.test(t))`);
            check(`${tag}: a HEIC is refused with what to do (JPEG or PNG, the iPhone setting), and makes no request`, /JPEG or PNG/.test(heic) && /Most Compatible/.test(heic) && (await ev(`window.__rec.ups.length`)) === n0, heic);

            // 6. the server's retry time is said in the viewer's own time
            const said = await ev(`import(${S} + 'image_attach.js').then(m => m.artifactErrorText({ artifact_error: 'the hourly limit for shared images is used up; try again in 12m0s', artifact_retry_at: '2026-10-22T14:30:00Z' }))`);
            const want = await ev(`new Date('2026-10-22T14:30:00Z').toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })`);
            check(`${tag}: "try again at HH:MM" is in the viewer's own time, not "in 12m0s"`, said === `the hourly limit for shared images is used up; try again at ${want}`, said);
            check(`${tag}: with no retry time the server's words stand`, (await ev(`import(${S} + 'image_attach.js').then(m => m.artifactErrorText({ artifact_error: 'the disk is almost full' }))`)) === 'the disk is almost full');

            // 8. an upload Coral Hub cut short answers an EMPTY 200: that is a failed upload, with Retry; 413 and 408 say what happened in plain words; a throw says it did not finish
            const toastOf = re => `(() => { const t = [...document.querySelectorAll('.toast')].reverse().find(x => ${re}.test(x.textContent)); return t ? { text: t.querySelector('.toast-message').textContent, retry: !!t.querySelector('.toast-action'), retryLabel: (t.querySelector('.toast-action') || {}).textContent } : null; })()`;
            const attached = () => ev(`document.querySelectorAll('#image-attachments .image-attachment').length`);
            const a0 = await attached();
            await ev(`window.__upMode = 'empty'; true`);
            await pick([mk(`'cut.png'`, 40, 30, 'image/png')]);
            await waitFor(toastOf(`/didn't finish/`), `${tag}: the empty 200 is reported`, { timeout: 8000 });
            let tt = await ev(toastOf(`/didn't finish/`));
            check(`${tag}: an empty 200 is a failed upload: "Upload didn't finish. Try again." with Retry, and nothing is attached`, tt.text === "Upload didn't finish. Try again." && tt.retry && tt.retryLabel === 'Retry' && (await attached()) === a0, JSON.stringify(tt));
            await ev(`window.__upMode = 'real'; const n = window.__rec.ups.length; document.querySelector('.toast-action').click(); window.__nBefore = n; true`);
            await waitFor(`window.__rec.ups.length === window.__nBefore + 1 && document.querySelectorAll('#image-attachments .image-attachment').length === ${a0 + 1}`, `${tag}: Retry uploads the same picture again`, { timeout: 10000 });
            check(`${tag}: Retry sends the same picture again and it is attached`, (await ev(`window.__rec.ups[window.__rec.ups.length - 1].name`)) === 'cut.png' && (await attached()) === a0 + 1);
            await ev(`import(${S} + 'controls.js').then(m => { for (let i = m.getPendingAttachments().length - 1; i >= 0; i--) m.removeAttachment(i); })`);
            for (const [mode, re, want, retry] of [[413, '/too big/', 'That image is too big to send. Pick a smaller one.', false], [408, '/took too long/', 'The upload took too long. Try again, on a faster connection if you can.', true], ['throw', "/didn't finish/", "Upload didn't finish. Try again.", true]]) {
                await ev(`window.__upMode = ${typeof mode === 'number' ? mode : JSON.stringify(mode)}; document.querySelectorAll('.toast').forEach(t => t.remove()); true`);
                await pick([mk(`'x.png'`, 40, 30, 'image/png')]);
                await waitFor(toastOf(re), `${tag}: the ${mode} message`, { timeout: 8000 });
                tt = await ev(toastOf(re));
                check(`${tag}: ${mode} says "${want}"${retry ? ' with Retry' : ' (no Retry: the same file would fail again)'}`, tt.text === want && tt.retry === retry, JSON.stringify(tt));
            }
            await ev(`window.__upMode = 'real'; document.querySelectorAll('.toast').forEach(t => t.remove()); true`);

            // 7. the button opens the picker (a click on a hidden input is the browser's own: here it is counted)
            await ev(`window.__clicks = 0; document.getElementById('attach-image-input').addEventListener('click', e => { window.__clicks++; e.preventDefault(); }); true`);
            await ev(`document.querySelector('.attach-image-btn').click(); true`);
            check(`${tag}: pressing the button opens the picker`, (await ev(`window.__clicks`)) === 1);

            // 9. pictures of the composer (CORAL_SCREENSHOT_DIR=<dir>): empty, with a picture attached, and dimmed as the hub shows it to an account that may not launch; dark and light
            const dir = process.env.CORAL_SCREENSHOT_DIR;
            if (dir) {
                const { themeJs } = require('./helpers/theme');
                const shot = async (n) => {
                    await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); true`);
                    const r = await ev(`(() => { const b = document.getElementById('command-pane').getBoundingClientRect(); return { x: Math.max(0, b.left - 8), y: Math.max(0, b.top - 8), width: Math.min(innerWidth, b.width + 16), height: Math.min(innerHeight, b.height + 16), scale: 1 }; })()`);
                    require('node:fs').mkdirSync(dir, { recursive: true });
                    require('node:fs').writeFileSync(require('node:path').join(dir, `attach-${n}.png`), Buffer.from((await Page.captureScreenshot({ format: 'png', clip: r })).data, 'base64'));
                };
                for (const theme of ['dark', 'light']) {
                    await ev(`${themeJs(theme)}; true`);
                    await ev(`document.querySelector('.attach-image-btn').classList.remove('hub-blocked'); true`);
                    await shot(`${tag}-${theme}-empty`);
                    await pick([mk(`'photo.jpg'`, 120, 90, 'image/jpeg')]);
                    await waitFor(`document.querySelectorAll('#image-attachments img').length === 1 && [...document.querySelectorAll('#image-attachments img')].every(i => i.complete && i.naturalWidth > 0)`, `${tag}: a picture is attached`, { timeout: 10000 });
                    await shot(`${tag}-${theme}-attached`);
                    await ev(`import(${S} + 'controls.js').then(m => { for (let i = m.getPendingAttachments().length - 1; i >= 0; i--) m.removeAttachment(i); }); document.querySelector('.attach-image-btn').classList.add('hub-blocked'); true`);
                    await shot(`${tag}-${theme}-dimmed`);
                    await ev(`document.querySelector('.attach-image-btn').classList.remove('hub-blocked'); true`);
                }
                await ev(`${themeJs('dark')}; true`);
            }
        }
    } finally {
        if (standIn) await standIn.stop().catch(() => {});
        await guard.restore().catch(() => {});
    }
    const failed = results.filter(r => !r).length;
    console.log(`attach_image: ${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
