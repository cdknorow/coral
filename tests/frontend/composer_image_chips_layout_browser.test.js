// Pending image chips in the phone composer (#6137, found by Codex Test in #6136): with two chips the message box was squeezed to a vertical strip, because the chip bar sat beside the box in one row.
// The chips are their own row above the box and the box keeps the width it had without them: 1, 2 and 5 chips at 390 and 320 px, light and dark, and nothing scrolls sideways.
// Drives Chrome over CDP through agent_view_harness.js against the real server and a stand-in agent; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { launchStandIn } = require('./helpers/stand_in_agent');
const { guardSettings } = require('./helpers/settings_guard');
const { themeJs } = require('./helpers/theme');
const BASE = require('./test_base').BASE;

async function httpJson(method, p, body) {
    const r = await fetch(BASE + p, { method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
    let data = null; try { data = await r.json(); } catch { /* no body */ }
    return { status: r.status, data };
}

// the same markup renderAttachments writes (controls.js), with a small real image so the chip has its 40 px thumbnail
const PNG = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==';
const chips = n => Array.from({ length: n }, (_, i) => `<div class="image-attachment" title="p${i}.png"><img src="${PNG}" alt="Image ${i + 1}" /><button type="button" class="image-attachment-remove" aria-label="Remove image ${i + 1}">×</button></div>`).join('');

(async () => {
    const { Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const guard = await guardSettings();
    let standIn = null;
    try {
        standIn = await launchStandIn(httpJson, { displayName: 'Chip Layout QA' });
        if (!standIn.sid) throw new Error('no stand-in agent: ' + standIn.diag);
        const sid = standIn.sid, name = standIn.name;
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        const measure = () => ev(`(() => { const r = e => e.getBoundingClientRect(); const ta = r(document.getElementById('command-input')), bar = document.getElementById('image-attachments'), b = r(bar), pane = r(document.getElementById('command-pane'));
            const send = [...document.querySelectorAll('.mobile-send-btn')].filter(x => x.getClientRects().length).map(r);
            return { ta: Math.round(ta.width), taTop: ta.top, barBottom: b.bottom, barW: Math.round(b.width), barShown: b.width > 0 && getComputedStyle(bar).display !== 'none', pane: Math.round(pane.width), paneRight: pane.right,
                sendInside: send.every(s => s.right <= innerWidth + 0.5 && s.left >= 0), overflow: document.documentElement.scrollWidth - innerWidth, rm: [...document.querySelectorAll('.image-attachment-remove')].map(x => { const q = r(x); return Math.round(q.width) + 'x' + Math.round(q.height); }), chipRight: Math.max(0, ...[...bar.children].map(c => r(c).right)) }; })()`);
        for (const w of [390, 320]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: 844, deviceScaleFactor: 1, mobile: true });
            await Emulation.setTouchEmulationEnabled({ enabled: true });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`window.selectLiveSession(${JSON.stringify(name)}, 'claude', ${JSON.stringify(sid)}, 'local'); true`);
            await waitFor(`!!window.T && T.state.currentSession && T.state.currentSession.session_id === ${JSON.stringify(sid)}`, 'the agent is open', { timeout: 15000 });
            // the "Sent" toast of a message that carried two images, a long question and the hint for the agent is one short line (#6137)
            await ev(`(async () => { const U = await import((window.__coralStatic || '/static/') + 'utils.js'), SI = await import((window.__coralStatic || '/static/') + 'shared_images.js');
                const h = 'a'.repeat(64), g = 'b'.repeat(64), links = ['coral://artifacts/' + h, 'coral://artifacts/' + g];
                U.showToast(SI.sentToastText(links.join(' ') + ' ' + SI.imageHint(links) + ' what do these two screenshots show, and is the second one the same page as the first? ' + 'more words '.repeat(20)));
                return true; })()`);
            await waitFor(`!!document.querySelector('.toast')`, 'the toast', { timeout: 5000 });
            const toast = await ev(`(() => { const t = [...document.querySelectorAll('.toast')].pop(); const b = t.getBoundingClientRect(), lh = parseFloat(getComputedStyle(t).lineHeight) || 20; t.remove();
                return { text: t.textContent.trim(), h: Math.round(b.height), w: Math.round(b.width), lineHeight: lh, right: b.right }; })()`);
            check(`${w}px: the Sent toast is short (no links, no hint), one line and inside the screen`, !/artifact|coral-agent|download/.test(toast.text) && toast.text.length <= 36 && toast.h <= 72 && toast.right <= w + 0.5, JSON.stringify(toast));
            for (const theme of ['light', 'dark']) {
                await ev(`${themeJs(theme)}; true`);
                await ev(`(() => { const c = document.getElementById('image-attachments'); c.innerHTML = ''; c.style.display = 'none'; return true; })()`);
                const empty = await measure();
                for (const n of [1, 2, 5]) {
                    await ev(`(() => { const c = document.getElementById('image-attachments'); c.style.display = 'flex'; c.innerHTML = ${JSON.stringify(chips(n))}; return true; })()`);
                    await waitFor(`[...document.querySelectorAll('#image-attachments img')].every(i => i.complete)`, 'chips drawn', { timeout: 5000 });
                    const m = await measure();
                    const tag = `${w}px ${theme}, ${n} chip${n > 1 ? 's' : ''}`;
                    check(`${tag}: the message box keeps its width (${m.ta} px; ${empty.ta} px with no chips)`, m.ta >= empty.ta - 2, `${m.ta} vs ${empty.ta}`);
                    check(`${tag}: the chips are a row above the box, not beside it`, m.barShown && m.barBottom <= m.taTop + 0.5, `bar bottom ${Math.round(m.barBottom)}, box top ${Math.round(m.taTop)}`);
                    check(`${tag}: the chips stay inside the screen and nothing scrolls sideways`, m.chipRight <= w + 0.5 && m.overflow <= 0 && m.sendInside, `chips to ${Math.round(m.chipRight)}, overflow ${m.overflow}`);
                    check(`${tag}: every remove button is a 44 px target`, m.rm.length === n && m.rm.every(d => { const [a, b] = d.split('x').map(Number); return a >= 44 && b >= 44; }), m.rm.join(' '));
                }
            }
        }
    } finally {
        if (standIn) await standIn.stop().catch(() => {});
        await guard.restore().catch(() => {});
    }
    const failed = results.filter(r => !r).length;
    console.log(`composer_image_chips_layout: ${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
