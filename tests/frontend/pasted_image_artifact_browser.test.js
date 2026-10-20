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
            await pageJson('PUT', '/api/cloud/share-pasted-images', { share: true });
            // record the upload replies and what Send types into the agent (the send itself answers ok and goes nowhere); the Hub status is stubbed per test
            await ev(`(() => { const real = window.fetch; window.__rec = { uploads: [], sends: [], puts: [], deletes: [] }; window.__hub = { state: 'off' };
                window.fetch = async (u, o = {}) => { const url = String(u), m = (o.method || 'GET').toUpperCase();
                    if (url.endsWith('/api/cloud/status') && m === 'GET') return new Response(JSON.stringify({ state: window.__hub.state, connected_at: window.__hub.at || '', share_pasted_images: true }), { status: 200, headers: { 'Content-Type': 'application/json' } });
                    if (/\\/send$/.test(url) && m === 'POST') { window.__rec.sends.push(JSON.parse(o.body)); return new Response(JSON.stringify({ ok: true }), { status: 200, headers: { 'Content-Type': 'application/json' } }); }
                    if (url.includes('/api/cloud/share-pasted-images') && m === 'PUT') window.__rec.puts.push(JSON.parse(o.body));
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

            // 2. Send types the path AND the link, then the text
            await ev(`document.getElementById('command-input').value = 'what is this'; import(${S} + 'controls.js').then(m => m.sendCommand())`);
            await waitFor(`window.__rec.sends.length === 1`, 'the send', { timeout: 8000 });
            const typed = await ev(`window.__rec.sends[0].command`);
            check(`${tag}: the agent gets the path and the link, then the text`, typed === `${up.path} ${up.artifact.uri} what is this`, typed);

            // 3. the chat: a thumbnail from the artifact, the name as a chip, the text alone
            const render = (msgs) => ev(`import(${S} + 'live_chat.js').then(m => { document.getElementById('pia-box')?.remove(); const box = document.createElement('div'); box.id = 'pia-box'; box.className = 'chat-messages chat-prose';
                box.style.cssText = 'position:fixed;left:0;top:0;right:0;bottom:0;overflow:auto;background:var(--bg-primary);z-index:99999;padding:12px'; document.body.appendChild(box); m.renderTranscript(${JSON.stringify(msgs)}, box, 'claude'); return true; })`);
            await render([{ type: 'user', content: typed, key: 'k1', timestamp: '2026-10-19T00:00:00Z' }]);
            await waitFor(`(() => { const t = document.querySelector('#pia-box .chat-attachment-thumb[data-artifact]'); return t && !t.hidden && t.querySelector('img').naturalWidth > 0; })()`, `${tag}: the thumbnail loads from the artifact route`, { timeout: 10000 });
            const bub = await ev(`(() => { const b = document.querySelector('#pia-box .chat-bubble.human'); const t = b.querySelector('.chat-attachment-thumb'); const x = b.querySelector('.chat-attachment-unshare'); const xr = x && x.getBoundingClientRect();
                return { text: b.querySelector('.message-text').innerText.trim(), chip: (b.querySelector('.chat-attachment-chip') || {}).textContent, art: t.dataset.artifact, unshare: !!x && !x.hidden, unshareH: xr && Math.round(xr.height), raw: b.innerHTML.includes('coral://artifacts') || b.innerHTML.includes('/uploads/') }; })()`);
            check(`${tag}: the bubble shows the typed text alone, the file's name as a chip, a thumbnail of the artifact`, bub.text === 'what is this' && bub.chip === up.path.split('/').pop() && bub.art === up.artifact.id && !bub.raw, JSON.stringify(bub));
            check(`${tag}: a "Remove shared copy" button is there${mobile ? ' and 44 px' : ''}`, bub.unshare && (!mobile || bub.unshareH >= 44), JSON.stringify(bub));

            // 4. old messages and a user's own link are left as they were
            await render([{ type: 'user', content: `${up.path} hello`, key: 'k2', timestamp: '2026-10-19T00:01:00Z' }, { type: 'user', content: `coral://artifacts/${up.artifact.id} what is this`, key: 'k3', timestamp: '2026-10-19T00:02:00Z' },
                { type: 'user', content: 'look at it', attachments: [{ name: '[Image #1]', file: up.path.split('/').pop() }], key: 'k4', timestamp: '2026-10-19T00:03:00Z' }]);
            await waitFor(`!!document.querySelector('#pia-box .chat-attachment-thumb[data-upload]:not([hidden])')`, `${tag}: an old-style attachment still gets its thumbnail by file name`, { timeout: 10000 });
            const old = await ev(`[...document.querySelectorAll('#pia-box .chat-bubble.human')].map(b => ({ text: b.querySelector('.message-text').innerText.trim(), thumbs: b.querySelectorAll('.chat-attachment-thumb[data-artifact]:not([data-artifact=""])').length, unshare: b.querySelectorAll('.chat-attachment-unshare').length }))`);
            check(`${tag}: an old path-only message keeps its text and has no artifact thumbnail`, old[0].text.includes(up.path.split('/').pop()) && old[0].thumbs === 0, JSON.stringify(old[0]));
            check(`${tag}: a user's own link at the start of a message is shown whole`, old[1].text.includes(`coral://artifacts/${up.artifact.id}`) && old[1].text.endsWith('what is this') && old[1].thumbs === 0, JSON.stringify(old[1]));
            check(`${tag}: an old image attachment has no remove button (it is no artifact)`, old[2].thumbs === 0 && old[2].unshare === 0, JSON.stringify(old[2]));

            // 5. the artifact route serves the image; the owner's switch off keeps it local
            const served = await ev(`fetch('/api/artifacts/${up.artifact.id}?v=2').then(r => ({ status: r.status, type: r.headers.get('content-type') }))`);
            check(`${tag}: GET /api/artifacts/{id} serves the image`, served.status === 200 && served.type === 'image/png', JSON.stringify(served));
            const sw = await pageJson('PUT', '/api/cloud/share-pasted-images', { share: false });
            check(`${tag}: the switch is saved and reported by the hub status (share_pasted_images false)`, sw.status === 200 && sw.data && sw.data.share_pasted_images === false, JSON.stringify(sw));
            check(`${tag}: with it off this computer still serves the image`, (await ev(`fetch('/api/artifacts/${up.artifact.id}?v=2').then(r => r.status)`)) === 200);
            await pageJson('PUT', '/api/cloud/share-pasted-images', { share: true });

            // 6. Remove shared copy: the thumbnail goes back to the chip, the artifact is gone
            await render([{ type: 'user', content: typed, key: 'k5', timestamp: '2026-10-19T00:05:00Z' }]);
            await waitFor(`!!document.querySelector('#pia-box .chat-attachment-unshare:not([hidden])')`, `${tag}: the remove button`, { timeout: 10000 });
            await ev(`document.querySelector('#pia-box .chat-attachment-unshare').click(); true`);
            await waitFor(`document.getElementById('confirm-modal').style.display === 'flex'`, `${tag}: the confirmation`, { timeout: 5000 });
            check(`${tag}: removing asks first and says what is lost`, /Coral Hub viewers lose/.test(await ev(`document.getElementById('confirm-modal-message').textContent`)));
            await ev(`document.getElementById('confirm-modal-yes').click(); true`);
            await waitFor(`window.__rec.deletes.length === 1 && document.querySelector('#pia-box .chat-attachment-thumb').hidden === true`, `${tag}: the thumbnail turns back into the chip`, { timeout: 8000 });
            check(`${tag}: the chip with the file name stays`, (await ev(`(document.querySelector('#pia-box .chat-attachment-chip') || {}).textContent`)) === up.path.split('/').pop());
            check(`${tag}: the shared copy is gone from the artifact route (asked past the browser's cache: an artifact is cached for good)`, (await ev(`fetch('/api/artifacts/${up.artifact.id}?v=2', { cache: 'no-store' }).then(r => r.status)`)) === 404, JSON.stringify(await ev(`window.__rec.deletes`)));

            // 7. the notice: once for each Coral Hub connect, with the way to turn sharing off
            await ev(`document.getElementById('pia-box')?.remove(); sessionStorage.clear(); window.__rec.puts = []; window.__hub = { state: 'connected', at: 'T1' }; true`);
            const n0 = await ev(`window.__rec.uploads.length`);
            const pending = png().then(() => ev(`import(${S} + 'controls.js').then(m => { window.__done = false; return m.uploadAndInsertImage(window.__file).then(() => { window.__done = true; }); })`));
            await waitFor(`!!document.querySelector('#image-attachments .share-notice')`, `${tag}: the notice appears before the upload`, { timeout: 8000 });
            check(`${tag}: nothing is uploaded until it is answered`, (await ev(`window.__rec.uploads.length`)) === n0, String(n0));
            const n1 = await ev(`(() => { const n = document.querySelector('#image-attachments .share-notice'); return { text: n.textContent, role: n.getAttribute('role'), focus: document.activeElement && document.activeElement.textContent }; })()`);
            check(`${tag}: it says the image is visible through Coral Hub and cannot be hidden, as an alert dialog with the focus on OK`, /visible to anyone who can view this Coral through Coral Hub/.test(n1.text) && /cannot be hidden/.test(n1.text) && n1.role === 'alertdialog' && n1.focus === 'OK', JSON.stringify(n1));
            await ev(`[...document.querySelectorAll('#image-attachments .share-notice button')].find(b => b.textContent === 'OK').click(); true`);
            await pending; await waitFor(`window.__done === true`, `${tag}: the upload goes on after OK`, { timeout: 10000 });
            await upload();
            check(`${tag}: a second paste on the same connect shows no notice`, (await ev(`document.querySelectorAll('#image-attachments .share-notice').length`)) === 0);
            await ev(`window.__hub = { state: 'connected', at: 'T2' }; true`);
            const again = png().then(() => ev(`import(${S} + 'controls.js').then(m => m.uploadAndInsertImage(window.__file))`));
            await waitFor(`!!document.querySelector('#image-attachments .share-notice')`, `${tag}: a new connect asks again`, { timeout: 8000 });
            await ev(`[...document.querySelectorAll('#image-attachments .share-notice button')].find(b => b.textContent === "Don't share pasted images").click(); true`);
            await again;
            const puts = await ev(`window.__rec.puts`);
            check(`${tag}: "Don't share pasted images" turns the switch off`, puts.length === 1 && puts[0].share === false, JSON.stringify(puts));
            check(`${tag}: the status now says sharing is off`, (await pageJson('GET', '/api/cloud/status?now=1')).data.share_pasted_images === false);
            await pageJson('PUT', '/api/cloud/share-pasted-images', { share: true });
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
