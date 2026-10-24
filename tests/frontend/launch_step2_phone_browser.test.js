// New Agent's second step on a phone (#7066, UI/UX's #7059 findings 1, 2 and 10): the summary (Folder, Provider, Model, Thinking, Mode) and the section picker left the form a sliver at 390 px, at
// 200% text the Change buttons ran past the edge, and the step opened on optional fields. Now: a summary row is one whole-row target (a tap anywhere on it opens its section), Change wraps under its value
// and never leaves the screen, Name stays one optional field, and the Persona, the section picker and Export/import sit under a closed "More options". The chooser's first card keeps its whole focus ring.
// Drives Chrome over CDP through agent_view_harness.js against the isolated dev server; nothing is launched.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: the step draws from several answers after Next
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const font of ['', '32px']) {
            const tag = font ? '390 px, 200% text' : '390 px';
            await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true }); await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'liveListLoaded', 'settings')`);
            if (font) await ev(`document.documentElement.style.fontSize = '${font}'; true`);
            await ev(`window.showLaunchModal(); true`);
            await waitFor(`!!document.querySelector('.launch-type-card') && document.querySelector('.launch-type-card').getClientRects().length > 0`, 'the chooser', 15000);
            // the chooser's first card: its focus ring is not cut by what clips it
            const ring = await ev(`(() => { const c = document.querySelector('.launch-type-card'); c.focus(); const cs = getComputedStyle(c); const w = parseFloat(cs.outlineWidth) || 0, off = parseFloat(cs.outlineOffset) || 0; let clip = c.parentElement; while (clip && getComputedStyle(clip).overflow === 'visible' && getComputedStyle(clip).overflowX === 'visible') clip = clip.parentElement;
                const r = c.getBoundingClientRect(), cr = clip.getBoundingClientRect(); return { room: Math.round((r.left - cr.left) - (w + Math.max(off, 0))), top: Math.round((r.top - cr.top) - (w + Math.max(off, 0))) }; })()`);
            assert.ok(ring.room >= 0, `${tag}: the first card's focus ring is whole on its left (${ring.room} px short)`);
            await ev(`window._selectLaunchType('agent'); true`);
            await waitFor(`!!document.getElementById('launch-dir') && document.getElementById('launch-dir').getClientRects().length > 0`, 'step 1', 8000);
            await ev(`(() => { const i = document.getElementById('launch-dir'); i.value = '/tmp'; i.dispatchEvent(new Event('input', { bubbles: true })); i.dispatchEvent(new Event('change', { bubbles: true })); document.getElementById('launch-agent-next').click(); return true; })()`);
            await waitFor(`document.getElementById('launch-agent-summary') && document.getElementById('launch-agent-summary').getClientRects().length > 0`, 'step 2', 10000);
            await sleep(900); // sleep-ok: the provider row and the section list fill from several answers
            // nothing runs past the screen or scrolls sideways
            const fit = await ev(`(() => { const mc = document.querySelector('#launch-modal .modal-content'); const out = [...mc.querySelectorAll('button, input, select, textarea, a, th, td')].filter(e => e.getClientRects().length).filter(e => e.getBoundingClientRect().right > innerWidth + 0.5).map(e => (e.getAttribute('aria-label') || e.textContent.trim()).slice(0, 24) + ' ' + Math.round(e.getBoundingClientRect().right - innerWidth));
                const scrollers = [mc, ...mc.querySelectorAll('*')].filter(e => e.scrollWidth > e.clientWidth + 1 && ['auto', 'scroll'].includes(getComputedStyle(e).overflowX)).map(e => e.className.toString().slice(0, 30)); return { out, scrollers }; })()`);
            assert.deepEqual(fit.out, [], `${tag}: no control runs past the screen: ` + JSON.stringify(fit.out));
            assert.deepEqual(fit.scrollers, [], `${tag}: nothing scrolls sideways: ` + JSON.stringify(fit.scrollers));
            // a summary row is one whole target: a tap on its value (not on the Change text) opens its section
            const row = await ev(`(() => { const tr = document.querySelector('#launch-agent-summary [data-row="model"]'); tr.scrollIntoView({ block: 'center' }); const v = tr.querySelector('.las-value').getBoundingClientRect(); const el = document.elementFromPoint(v.left + 4, v.top + v.height / 2); return { onChange: !!(el && el.closest('.las-change')) }; })()`);
            assert.equal(row.onChange, true, `${tag}: a tap on the Model row's value reaches its Change button (the row is the target)`);
            // Name is one optional field in view; the rest is under a closed More options
            const more = await ev(`(() => { const d = document.getElementById('launch-more-options'); const vis = e => !!e && e.getClientRects().length > 0 && !e.closest('details:not([open]) > :not(summary)');
                const name = document.getElementById('launch-agent-name-input'); const nr = name.getBoundingClientRect();
                return { exists: !!d, open: d && d.open, summary: d && d.querySelector(':scope > summary') && d.querySelector(':scope > summary').textContent.trim(), nameVisible: vis(name), nameOutside: !!name && !name.closest('#launch-more-options'), nameBottom: Math.round(nr.bottom), personaVisible: vis(document.getElementById('agent-preset-selector')), pickerVisible: vis(document.querySelector('#launch-agent-more .settings-group-pick select')), launchBottom: Math.round(document.getElementById('launch-agent-go').getBoundingClientRect().bottom), vh: innerHeight }; })()`);
            assert.ok(more.exists && more.open === false && /more options/i.test(more.summary || ''), `${tag}: Persona and the sections are under a closed "More options": ` + JSON.stringify(more));
            assert.ok(more.nameVisible && more.nameOutside, `${tag}: Name stays one field in view, outside the disclosure: ` + JSON.stringify(more));
            assert.ok(!more.personaVisible && !more.pickerVisible, `${tag}: Persona and the section picker are not shown until More options opens: ` + JSON.stringify(more));
            if (!font) assert.ok(more.nameBottom < more.launchBottom && more.launchBottom <= more.vh, `${tag}: Name and Launch are on screen together (at 200% the step scrolls): ` + JSON.stringify(more));
            await ev(`document.querySelector('#launch-more-options > summary').click(); true`);
            await waitFor(`document.getElementById('agent-preset-selector').getClientRects().length > 0 || document.querySelector('#launch-agent-more .settings-group-pick select').getClientRects().length > 0`, 'More options open', 4000);
        }
        console.log('PASS launch step 2 phone');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
