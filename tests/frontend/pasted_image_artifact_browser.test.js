// A pasted or attached image is an artifact (#5284), against the real server and a real (stand-in) agent session, at 1280 and 390 px:
// the upload stores it as an artifact next to the file the agent reads; Send types BOTH the path and the link into the agent; the chat draws it as a thumbnail from the artifact route (which
// Coral Hub serves) with its file name as a chip and a "Remove shared copy" button that turns the thumbnail back into the chip; an old path-only message and a user's own link are left as they were;
// before the first paste after each Coral Hub connect a notice says the image is visible there, with the way to turn sharing off; the owner's switch.
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
    // the dashboard's own credential: the switch and the status are the owner's, a node-side call has none
    const pageJson = (method, p, body) => ev(`fetch(${JSON.stringify(p)}, { method: ${JSON.stringify(method)}, headers: { 'Content-Type': 'application/json' }, body: ${body === undefined ? 'undefined' : JSON.stringify(JSON.stringify(body))} }).then(async r => ({ status: r.status, data: await r.json().catch(() => null) }))`);
    let standIn = null;
    try {
        standIn = await launchStandIn(httpJson, { displayName: 'Pasted Image QA' });
        if (!standIn.sid) throw new Error('no stand-in agent: ' + standIn.diag);
        const sid = standIn.sid, name = standIn.name;
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 800, false], [390, 844, true]]) {
            const tag = `${w}px`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            // record the upload replies and what Send types into the agent (the send itself answers ok and goes nowhere); the Hub status is stubbed per test
            await ev(`(() => { const real = window.fetch; window.__rec = { uploads: [], sends: [], puts: [], deletes: [] }; window.__hub = { state: 'off' };
                window.fetch = async (u, o = {}) => { const url = String(u), m = (o.method || 'GET').toUpperCase();
                    if (url.endsWith('/api/cloud/status') && m === 'GET') return new Response(JSON.stringify({ state: window.__hub.state, connected_at: window.__hub.at || '' }), { status: 200, headers: { 'Content-Type': 'application/json' } });
                    if (/\\/send$/.test(url) && m === 'POST') { window.__rec.sends.push(JSON.parse(o.body)); return new Response(JSON.stringify({ ok: true }), { status: 200, headers: { 'Content-Type': 'application/json' } }); }
                    const resp = await real(u, o);
                    if (url.includes('/api/shared-images/') && m === 'DELETE') window.__rec.deletes.push({ url, status: resp.status, body: await resp.clone().json().catch(() => null) });
                    if (url.endsWith('/api/upload') && m === 'POST') window.__rec.uploads.push(await resp.clone().json());
                    return resp; }; return true; })()`);
            await ev(`window.selectLiveSession(${JSON.stringify(name)}, 'claude', ${JSON.stringify(sid)}, 'local'); true`);
            await waitFor(`!!window.T && T.state.currentSession && T.state.currentSession.session_id === ${JSON.stringify(sid)}`, 'the agent is open', { timeout: 15000 });
            const png = () => ev(`(async () => { const c = document.createElement('canvas'); c.width = 40; c.height = 30; const x = c.getContext('2d'); x.fillStyle = '#c33'; x.fillRect(0, 0, 40, 30); x.fillStyle = '#3c3'; x.fillRect(${Math.floor(Math.random() * 20)}, 0, 10, 30); const b = await new Promise(r => c.toBlob(r, 'image/png')); window.__file = new File([b], 'shot-' + Math.random().toString(36).slice(2, 8) + '.png', { type: 'image/png' }); return true; })()`);
            const upload = async () => { await png(); return ev(`import(${S} + 'controls.js').then(m => m.uploadAndInsertImage(window.__file))`); };

            // 1. a paste: the reply has the artifact; a strip shows the attachment
            await upload();
            const up = await ev(`window.__rec.uploads[window.__rec.uploads.length - 1]`);
            check(`${tag}: the upload answers with the path and an artifact`, up && up.ok && up.path && up.artifact && /^coral:\/\/artifacts\/[0-9a-f]{64}$/.test(up.artifact.uri) && up.artifact.inline === true && !up.artifact_error, JSON.stringify(up).slice(0, 200));
            check(`${tag}: the composer shows the attachment`, await ev(`document.querySelectorAll('#image-attachments .image-attachment').length === 1`));
            const comp = await ev(`(() => { const c = document.querySelector('#image-attachments .image-attachment'); const i = c.querySelector('img').getBoundingClientRect(), x = c.querySelector('.image-attachment-remove').getBoundingClientRect(), r = c.getBoundingClientRect();
                return { names: c.querySelectorAll('.image-attachment-name').length, label: c.querySelector('.image-attachment-remove').getAttribute('aria-label'), btnTitle: c.querySelector('.image-attachment-remove').title.replace(/^Remove /, ''), alt: c.querySelector('img').alt, title: c.title, chipH: Math.round(r.height), xW: Math.round(x.width), xH: Math.round(x.height), imgH: Math.round(i.height), side: x.left >= i.right - 1 }; })()`);
            check(`${tag}: the composer's chip is the thumbnail and a remove button, with no name pill; the button is named and no path shows`, comp.names === 0 && comp.label === 'Remove image' && comp.alt === 'Image' && comp.btnTitle === comp.title && comp.title.length > 0 && !/uploads|\//.test(comp.title) && comp.chipH <= 48 && comp.side && comp.xH === comp.imgH && (!mobile || comp.xW >= 44), JSON.stringify(comp));

            // 2. Send types ONLY the link and one hint (never the uploads path), then the text (#5481)
            await ev(`document.getElementById('command-input').value = 'what is this'; import(${S} + 'controls.js').then(m => m.sendCommand())`);
            await waitFor(`window.__rec.sends.length === 1`, 'the send', { timeout: 8000 });
            const typed = await ev(`window.__rec.sends[0].command`);
            const HINT = await ev(`import(${S} + 'shared_images.js').then(m => m.imageHint(${JSON.stringify([up.artifact.uri])}))`);
            check(`${tag}: the agent gets the link and the hint to fetch it, then the text`, typed === `${up.artifact.uri} ${HINT} what is this`, typed);
            check(`${tag}: the local uploads path never reaches the agent`, !typed.includes(up.path) && !/uploads/.test(typed) && typed.includes(`coral-agent artifact download ${up.artifact.uri}`), typed);

            // 3. the chat: a thumbnail from the artifact, the name as a chip, the text alone
            const render = (msgs) => ev(`import(${S} + 'live_chat.js').then(m => { document.getElementById('pia-box')?.remove(); const box = document.createElement('div'); box.id = 'pia-box'; box.className = 'chat-messages chat-prose';
                box.style.cssText = 'position:fixed;left:0;top:0;right:0;bottom:0;overflow:auto;background:var(--bg-primary);z-index:99999;padding:12px'; document.body.appendChild(box); m.renderTranscript(${JSON.stringify(msgs)}, box, 'claude'); return true; })`);
            await render([{ type: 'user', content: typed, key: 'k1', timestamp: '2026-10-19T00:00:00Z' }]);
            await waitFor(`(() => { const t = document.querySelector('#pia-box .chat-attachment-thumb[data-artifact]'); return t && !t.hidden && t.querySelector('img').naturalWidth > 0; })()`, `${tag}: the thumbnail loads from the artifact route`, { timeout: 10000 });
            const bub = await ev(`(() => { const b = document.querySelector('#pia-box .chat-bubble.human'); const t = b.querySelector('.chat-attachment-thumb'); const x = b.querySelector('.chat-attachment-unshare'); const xr = x && x.getBoundingClientRect();
                return { text: b.querySelector('.message-text').innerText.trim(), chip: (b.querySelector('.chat-attachment-chip') || {}).textContent, art: t.dataset.artifact, unshare: !!x && !x.hidden, unshareH: xr && Math.round(xr.height), raw: b.innerHTML.includes('coral://artifacts') || b.innerHTML.includes('/uploads/') }; })()`);
            check(`${tag}: the bubble shows the typed text alone, the file's name as a chip, a thumbnail of the artifact`, bub.text === 'what is this' && bub.chip === 'Pasted image' && bub.art === up.artifact.id && !bub.raw, JSON.stringify(bub));
            const geo = await ev(`(() => { const w = document.querySelector('#pia-box .chat-attachment-image'); const t = w.querySelector('.chat-attachment-thumb').getBoundingClientRect(), x = w.querySelector('.chat-attachment-unshare').getBoundingClientRect(), r = w.getBoundingClientRect(), n = getComputedStyle(w.querySelector('.chat-attachment-name')).display;
                return { nameShown: n !== 'none', chipH: Math.round(r.height), chipW: Math.round(r.width), thumbW: Math.round(t.width), xW: Math.round(x.width), xH: Math.round(x.height), thumbH: Math.round(t.height), xRightOfThumb: x.left >= t.right - 1, sameTop: Math.abs(x.top - t.top) < 2, boxed: getComputedStyle(w).borderTopWidth }; })()`);
            check(`${tag}: a pasted image is one compact chip: the thumbnail and its remove button side by side, no 'Pasted image' pill`, !geo.nameShown && geo.chipH <= 52 && geo.xRightOfThumb && geo.sameTop && geo.xH === geo.thumbH && parseFloat(geo.boxed) >= 1 && geo.chipW <= geo.thumbW + geo.xW + 4, JSON.stringify(geo));
            const names = await ev(`(() => { const b = document.querySelector('#pia-box .chat-bubble.human'); return { open: b.querySelector('.chat-attachment-thumb').getAttribute('aria-label'), alt: b.querySelector('.chat-attachment-thumb img').alt, remove: b.querySelector('.chat-attachment-unshare').getAttribute('aria-label') }; })()`);
            check(`${tag}: the thumbnail and the remove button are named by the image's place, as a sentence`, names.open === 'Open pasted image' && names.alt === 'pasted image' && names.remove === 'Remove the shared copy of pasted image', JSON.stringify(names));
            check(`${tag}: a "Remove shared copy" button is there${mobile ? ' and 44 px' : ''}`, bub.unshare && (!mobile || bub.unshareH >= 44), JSON.stringify(bub));

            // 4. old messages and a user's own link are left as they were
            // (a session's history from before #5481 has "<path> <link>" pairs: they still show as a thumbnail)
            await render([{ type: 'user', content: `${up.path} ${up.artifact.uri} older`, key: 'k0', timestamp: '2026-10-18T00:00:00Z' }]);
            await waitFor(`(() => { const t = document.querySelector('#pia-box .chat-attachment-thumb[data-artifact]'); return !!t && t.dataset.artifact === ${JSON.stringify(up.artifact.id)}; })()`, `${tag}: an older path-and-link message still shows its thumbnail`, { timeout: 10000 });
            await render([{ type: 'user', content: `${up.path} hello`, key: 'k2', timestamp: '2026-10-19T00:01:00Z' }, { type: 'user', content: `coral://artifacts/${up.artifact.id} what is this`, key: 'k3', timestamp: '2026-10-19T00:02:00Z' },
                { type: 'user', content: 'look at it', attachments: [{ name: '[Image #1]', file: up.path.split('/').pop() }], key: 'k4', timestamp: '2026-10-19T00:03:00Z' }]);
            await waitFor(`!!document.querySelector('#pia-box .chat-attachment-thumb[data-upload]:not([hidden])')`, `${tag}: an old-style attachment still gets its thumbnail by file name`, { timeout: 10000 });
            const old = await ev(`[...document.querySelectorAll('#pia-box .chat-bubble.human')].map(b => ({ text: b.querySelector('.message-text').innerText.trim(), thumbs: b.querySelectorAll('.chat-attachment-thumb[data-artifact]:not([data-artifact=""])').length, unshare: b.querySelectorAll('.chat-attachment-unshare').length }))`);
            check(`${tag}: an old path-only message keeps its text and has no artifact thumbnail`, old[0].text.includes(up.path.split('/').pop()) && old[0].thumbs === 0, JSON.stringify(old[0]));
            check(`${tag}: a user's own link at the start of a message is shown whole`, old[1].text.includes(`coral://artifacts/${up.artifact.id}`) && old[1].text.endsWith('what is this') && old[1].thumbs === 0, JSON.stringify(old[1]));
            check(`${tag}: an old image attachment has no remove button (it is no artifact)`, old[2].thumbs === 0 && old[2].unshare === 0, JSON.stringify(old[2]));

            // 5. the artifact route serves the image to this computer (through Coral Hub it is the owner's alone, #5575: the Go tests drive that)
            const served = await ev(`fetch('/api/artifacts/${up.artifact.id}?v=2').then(r => ({ status: r.status, type: r.headers.get('content-type') }))`);
            check(`${tag}: GET /api/artifacts/{id} serves the image`, served.status === 200 && served.type === 'image/png', JSON.stringify(served));

            // 6. Remove shared copy: the thumbnail goes back to the chip, the artifact is gone
            await render([{ type: 'user', content: typed, key: 'k5', timestamp: '2026-10-19T00:05:00Z' }]);
            await waitFor(`!!document.querySelector('#pia-box .chat-attachment-unshare:not([hidden])')`, `${tag}: the remove button`, { timeout: 10000 });
            await ev(`document.querySelector('#pia-box .chat-attachment-unshare').click(); true`);
            await waitFor(`document.getElementById('confirm-modal').style.display === 'flex'`, `${tag}: the confirmation`, { timeout: 5000 });
            check(`${tag}: removing asks first and says what is lost`, /removed from Coral Hub/.test(await ev(`document.getElementById('confirm-modal-message').textContent`)));
            await ev(`document.getElementById('confirm-modal-yes').click(); true`);
            await waitFor(`window.__rec.deletes.length === 1 && document.querySelector('#pia-box .chat-attachment-thumb').hidden === true`, `${tag}: the thumbnail turns back into the chip`, { timeout: 8000 });
            check(`${tag}: the chip ("Pasted image") stays`, (await ev(`(document.querySelector('#pia-box .chat-attachment-chip') || {}).textContent`)) === 'Pasted image');
            check(`${tag}: the shared copy is gone from the artifact route (asked past the browser's cache: an artifact is cached for good)`, (await ev(`fetch('/api/artifacts/${up.artifact.id}?v=2', { cache: 'no-store' }).then(r => r.status)`)) === 404, JSON.stringify(await ev(`window.__rec.deletes`)));

            // 6b. three images: each is named by its place (#5756), in the composer and in the chat; CORAL_SCREENSHOT_DIR=<dir> also writes pictures of them, dark and light
            const dir = process.env.CORAL_SCREENSHOT_DIR || '/tmp/qa5777shots'; // TRIAL: pictures on, so the light check runs
            const shot = async (n, clip) => { require('node:fs').mkdirSync(dir, { recursive: true }); require('node:fs').writeFileSync(require('node:path').join(dir, n + '.png'), Buffer.from((await Page.captureScreenshot(clip ? { format: 'png', clip } : { format: 'png' })).data, 'base64')); };
            const n1 = await ev(`window.__rec.uploads.length`);
            for (let k = 0; k < 3; k++) await upload();
            const ups = await ev(`window.__rec.uploads.slice(${n1})`);
            // the message as Send types it: the links, one hint, then the text (step 2); not the older path-and-link pairs, which the chat names by file
            const uris = ups.map(u => u.artifact.uri);
            const mk = `${uris.join(' ')} ${await ev(`import(${S} + 'shared_images.js').then(m => m.imageHint(${JSON.stringify(uris)}))`)} what are these three`;
            await ev(`document.getElementById('pia-box')?.remove(); true`);
            await waitFor(`document.querySelectorAll('#image-attachments img').length === 3 && [...document.querySelectorAll('#image-attachments img')].every(i => i.complete && i.naturalWidth > 0)`, `${tag}: three composer thumbnails`, { timeout: 10000 });
            const cnames = await ev(`[...document.querySelectorAll('#image-attachments .image-attachment')].map(c => ({ alt: c.querySelector('img').alt, remove: c.querySelector('.image-attachment-remove').getAttribute('aria-label'), title: c.title }))`);
            check(`${tag}: three attachments in the composer are named by their place, Image 1, 2 and 3, and keep their own file name as the tooltip`, JSON.stringify(cnames.map(c => [c.alt, c.remove])) === JSON.stringify([['Image 1', 'Remove image 1'], ['Image 2', 'Remove image 2'], ['Image 3', 'Remove image 3']]) && new Set(cnames.map(c => c.title)).size === 3 && cnames.every(c => c.title && !/uploads|\//.test(c.title)), JSON.stringify(cnames));
            await render([{ type: 'user', content: mk, key: 'k9', timestamp: '2026-10-19T00:09:00Z' }]);
            await waitFor(`document.querySelectorAll('#pia-box .chat-attachment-thumb:not([hidden])').length === 3`, `${tag}: three thumbnails`, { timeout: 10000 });
            const hnames = await ev(`[...document.querySelectorAll('#pia-box .chat-attachment-thumb')].map((t, i) => ({ open: t.getAttribute('aria-label'), alt: t.querySelector('img').alt, remove: document.querySelectorAll('#pia-box .chat-attachment-unshare')[i].getAttribute('aria-label') }))`);
            check(`${tag}: three images in a message are named by their place too: "Open pasted image 2", "Remove the shared copy of pasted image 2"`, JSON.stringify(hnames.map(h => [h.open, h.alt, h.remove])) === JSON.stringify([1, 2, 3].map(n => [`Open pasted image ${n}`, `pasted image ${n}`, `Remove the shared copy of pasted image ${n}`])), JSON.stringify(hnames));
            if (dir) {
                const { themeJs } = require('./helpers/theme');
                for (const theme of ['dark', 'light']) {
                    await ev(`${themeJs(theme)}; true`); // the helper clears the custom colour variables first, so a "light" picture is light (#5757)
                    await ev(`document.getElementById('pia-box')?.remove(); true`);
                    await waitFor(`document.querySelectorAll('#image-attachments img').length === 3`, `${tag}: composer thumbnails`, { timeout: 10000 });
                    const cr = await ev(`(() => { const r = document.getElementById('image-attachments').getBoundingClientRect(); return { x: Math.max(0, r.left - 20), y: Math.max(0, r.top - 20), width: Math.min(innerWidth, r.width + 40), height: 110, scale: 1 }; })()`);
                    await shot(`composer-${tag}-${theme}`, cr);
                    await render([{ type: 'user', content: mk, key: 'k9', timestamp: '2026-10-19T00:09:00Z' }]);
                    await waitFor(`document.querySelectorAll('#pia-box .chat-attachment-thumb:not([hidden])').length === 3`, `${tag}: three thumbnails`, { timeout: 10000 });
                    await shot(`chat-${tag}-${theme}`);
                    check(`${tag}: the ${theme} picture is really ${theme}: the chat box's background is ${theme}`, (await ev(`(() => { const c = getComputedStyle(document.getElementById('pia-box')).backgroundColor.match(/\\d+/g).map(Number); return (c[0] + c[1] + c[2]) / 3 > 128; })()`)) === (theme === 'light'));
                }
                await ev(`${themeJs('dark')}; true`);
            }
            // take the three out of the composer through their buttons, so the steps below start with an empty strip
            await ev(`document.getElementById('pia-box')?.remove(); for (let i = 0; i < 3; i++) document.querySelector('#image-attachments .image-attachment-remove')?.click(); true`);
            check(`${tag}: each remove button takes its image out of the strip`, await ev(`document.querySelectorAll('#image-attachments .image-attachment').length === 0`));

            // 7. no notice before a paste, whatever the Coral Hub state: pasted images are the owner's alone through the hub, so there is nothing to warn about or switch off (#5575)
            await ev(`document.getElementById('pia-box')?.remove(); sessionStorage.clear(); window.__hub = { state: 'connected', at: 'T1' }; true`);
            const n0 = await ev(`window.__rec.uploads.length`);
            await png();
            await ev(`import(${S} + 'controls.js').then(m => m.uploadAndInsertImage(window.__file))`);
            check(`${tag}: with Coral Hub connected a paste uploads at once and shows no notice`, (await ev(`window.__rec.uploads.length`)) === n0 + 1 && (await ev(`document.querySelectorAll('#image-attachments .share-notice').length`)) === 0);
            await ev(`window.__hub = { state: 'off' }; true`);
            // leave nothing attached
            await ev(`import(${S} + 'controls.js').then(m => { for (let i = m.getPendingAttachments().length - 1; i >= 0; i--) m.removeAttachment(i); return true; })`);
        }
    } finally {
        if (standIn) await standIn.stop().catch(() => {});
        await guard.restore().catch(() => {});
    }
    const failed = results.filter(r => !r).length;
    console.log(`pasted_image_artifact: ${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
