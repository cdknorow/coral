// A message with pasted images, while it is still pending (#6199, Codex Test's first-run walk): its bubble showed the coral:// links and the download instructions typed for the agent until the turn finished, then turned into a clean
// thumbnail. The pending bubble shows the same thumbnail chips as the sent one, and the person's own words, never the links or the hint. The artifact route is stood in with a small PNG; the page's own pending-message code.
// Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const H1 = 'a'.repeat(64), H2 = 'b'.repeat(64);
const PNG = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==';

(async () => {
    const { Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 800, false], [390, 844, true]]) {
            const tag = `${w}px`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`(() => { const real = window.fetch; window.fetch = async (u, o = {}) => { const p = String(u); if (/\\/api\\/artifacts\\/[ab]{64}/.test(p)) { const b = Uint8Array.from(atob('${PNG}'), c => c.charCodeAt(0)); return new Response(b, { status: 200, headers: { 'Content-Type': 'image/png' } }); } return real(u, o); }; return true; })()`);
            const SID = 'sess-pending-' + w;
            const typed = await ev(`(async () => { const m = await import(${S} + 'shared_images.js'); const links = ['coral://artifacts/${H1}', 'coral://artifacts/${H2}']; return links.join(' ') + ' ' + m.imageHint(links) + ' what are these two?'; })()`);
            await ev(`(async () => { const m = await import(${S} + 'pending_messages.js'); document.getElementById('pend-box')?.remove(); const box = document.createElement('div'); box.id = 'pend-box'; box.className = 'chat-messages chat-prose';
                box.style.cssText = 'position:fixed;left:0;top:0;right:0;bottom:0;overflow:auto;background:var(--bg-primary);z-index:99999;padding:12px'; document.body.appendChild(box);
                m.addPendingMessage(${JSON.stringify(SID)}, ${JSON.stringify(typed)}, Date.now(), { sending: true }); m.syncPendingBubbles(box, ${JSON.stringify(SID)}); return true; })()`);
            await waitFor(`document.querySelectorAll('#pend-box .pending-messages .chat-attachment-image').length === 2`, `${tag}: two thumbnail chips in the pending bubble`, 8000);
            await waitFor(`[...document.querySelectorAll('#pend-box .chat-attachment-thumb')].filter(t => !t.hidden && t.querySelector('img').naturalWidth > 0).length === 2`, `${tag}: both thumbnails loaded`, 8000);
            const b = await ev(`(() => { const e = document.querySelector('#pend-box .pending-messages .chat-bubble.human'); return { text: e.querySelector('.message-text').innerText.trim(), all: e.innerText, html: e.innerHTML, label: e.querySelector('.pending-label').textContent.trim(), thumbs: e.querySelectorAll('.chat-attachment-thumb').length }; })()`);
            check(`${tag}: the pending bubble says only the person's words`, b.text === 'what are these two?', JSON.stringify(b.text));
            check(`${tag}: no link and no download instructions anywhere in it`, !/coral:\/\/|coral-agent|artifact download|To read the image/.test(b.all + b.html.replace(/data-artifact="[0-9a-f]+"/g, '')), b.all.slice(0, 200));
            check(`${tag}: two thumbnails, and it says it is sending`, b.thumbs === 2 && /Sending/.test(b.label), JSON.stringify([b.thumbs, b.label]));
            // the same message once the agent has it and the turn is in the transcript: the same chips (the final renderer), so the bubble does not change shape
            const fin = await ev(`(async () => { const m = await import(${S} + 'live_chat.js'); document.getElementById('fin-box')?.remove(); const box = document.createElement('div'); box.id = 'fin-box'; box.className = 'chat-messages chat-prose'; box.style.cssText = 'position:fixed;left:0;top:0;width:1px;height:1px;overflow:hidden';
                document.body.appendChild(box); m.renderTranscript([{ type: 'user', content: ${JSON.stringify(typed)}, key: 'k1', timestamp: '2026-10-19T00:00:00Z' }], box, 'claude'); const e = box.querySelector('.chat-bubble.human'); return { images: e.querySelectorAll('.chat-attachment-image').length, text: e.querySelector('.message-text').innerText.trim() }; })()`);
            check(`${tag}: the sent message shows the same two chips and words`, fin.images === 2 && fin.text === b.text, JSON.stringify(fin));
        }
    } finally { /* the harness closes the page */ }
    const failed = results.filter(r => !r).length;
    console.log(`pending_image_bubble: ${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
