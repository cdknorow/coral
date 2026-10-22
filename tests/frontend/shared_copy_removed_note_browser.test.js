// Remove shared copy says what happened (#6167): the server answers {removed: true, artifact_removed: false} when another agent's record holds the same image (it stays), true when the image went too.
// The confirm no longer promises the image goes from Coral Hub unconditionally, and the toast after says which: "...so it stays there" for a shared image, "...and from Coral Hub" when it is gone.
// Drives Chrome over CDP through agent_view_harness.js (the dashboard's own chat renderer, thumbnail and removal flow; the two routes stood in); against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const HEX = 'c'.repeat(64);
const PNG = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==';

(async () => {
    const { Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { const real = window.fetch; window.__deletes = [];
            window.fetch = async (u, o = {}) => { const p = String(u), m = (o.method || 'GET').toUpperCase();
                if (m === 'DELETE' && p.includes('/api/shared-images/${HEX}')) { window.__deletes.push(p); return new Response(window.__answer === 'nobody' ? '' : JSON.stringify(window.__answer), { status: window.__status || 200, headers: { 'Content-Type': 'application/json' } }); }
                if (m === 'GET' && p.includes('/api/artifacts/${HEX}')) { const b = Uint8Array.from(atob('${PNG}'), c => c.charCodeAt(0)); return new Response(b, { status: 200, headers: { 'Content-Type': 'image/png' } }); }
                return real(u, o); };
            T.state.currentSession = { session_id: 'sess-shared', name: 'sess-shared', agent_type: 'claude', type: 'live', server: 'local' }; return true; })()`);
        const HINT = await ev(`import(${S} + 'shared_images.js').then(m => m.imageHint(['coral://artifacts/${HEX}']))`);
        const typed = `coral://artifacts/${HEX} ${HINT} what is this`;
        const cases = [
            ['shared with another agent', { removed: true, artifact_removed: false }, 'Removed from this chat. The same image is still shared elsewhere, so it stays on Coral Hub.'],
            ['the image went too', { removed: true, artifact_removed: true }, 'Removed from this chat and from Coral Hub.'],
            ['an answer that does not say', 'nobody', 'Removed from this chat.'],
        ];
        for (const [name, answer, toast] of cases) {
            await ev(`window.__answer = ${JSON.stringify(answer)}; window.__deletes = []; document.querySelectorAll('.toast').forEach(t => t.remove()); true`);
            await ev(`import(${S} + 'live_chat.js').then(m => { document.getElementById('scn-box')?.remove(); const box = document.createElement('div'); box.id = 'scn-box'; box.className = 'chat-messages chat-prose'; box.style.cssText = 'position:fixed;left:0;top:0;right:0;bottom:0;overflow:auto;background:var(--bg-primary);z-index:99999;padding:12px'; document.body.appendChild(box);
                m.renderTranscript(${JSON.stringify([{ type: 'user', content: typed, key: 'k1', timestamp: '2026-10-19T00:00:00Z' }])}, box, 'claude'); return true; })`);
            await waitFor(`(() => { const x = document.querySelector('#scn-box .chat-attachment-unshare'); return !!x && !x.hidden; })()`, `${name}: the remove-copy button is there`, 10000);
            await ev(`document.querySelector('#scn-box .chat-attachment-unshare').click(); true`);
            await waitFor(`document.getElementById('confirm-modal').style.display === 'flex'`, `${name}: the confirmation`, 5000);
            const asked = await ev(`document.getElementById('confirm-modal-message').textContent`);
            check(`${name}: the confirm does not promise the image leaves Coral Hub whatever happens`, /unless the same image is still shared elsewhere/.test(asked) && !/^The image is removed from Coral Hub/.test(asked), asked);
            await ev(`document.getElementById('confirm-modal-yes').click(); true`);
            await waitFor(`window.__deletes.length === 1 && [...document.querySelectorAll('.toast')].some(t => /^Removed/.test(t.textContent.trim()))`, `${name}: the result is said`, 8000);
            const said = await ev(`[...document.querySelectorAll('.toast')].map(t => t.textContent.replace(/\\u00d7/g, '').trim())`);
            check(`${name}: the toast says "${toast}"`, said.includes(toast), JSON.stringify(said));
            check(`${name}: the thumbnail goes back to the file's chip`, await ev(`document.querySelector('#scn-box .chat-attachment-thumb').hidden === true && document.querySelector('#scn-box .chat-attachment-unshare').hidden === true`));
        }
        // a refusal keeps the thumbnail and says so
        await ev(`window.__answer = { error: 'no' }; window.__status = 500; window.__deletes = []; document.querySelectorAll('.toast').forEach(t => t.remove()); import(${S} + 'live_chat.js').then(m => { document.getElementById('scn-box')?.remove(); const box = document.createElement('div'); box.id = 'scn-box'; box.className = 'chat-messages chat-prose'; box.style.cssText = 'position:fixed;left:0;top:0;right:0;bottom:0;overflow:auto;background:var(--bg-primary);z-index:99999;padding:12px'; document.body.appendChild(box); m.renderTranscript(${JSON.stringify([{ type: 'user', content: typed, key: 'k2', timestamp: '2026-10-19T00:01:00Z' }])}, box, 'claude'); return true; })`);
        await waitFor(`(() => { const x = document.querySelector('#scn-box .chat-attachment-unshare'); return !!x && !x.hidden; })()`, 'refusal: the button', 10000);
        await ev(`document.querySelector('#scn-box .chat-attachment-unshare').click(); true`);
        await waitFor(`document.getElementById('confirm-modal').style.display === 'flex'`, 'refusal: the confirmation', 5000);
        await ev(`document.getElementById('confirm-modal-yes').click(); true`);
        await waitFor(`[...document.querySelectorAll('.toast')].some(t => /Could not remove the shared copy/.test(t.textContent))`, 'refusal: said', 8000);
        check('a refused removal keeps the thumbnail and says it could not be removed', await ev(`document.querySelector('#scn-box .chat-attachment-thumb').hidden === false`));
    } finally { /* the harness closes the page */ }
    const failed = results.filter(r => !r).length;
    console.log(`shared_copy_removed_note: ${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
