// The terminal view's controls on a phone are 44 x 44 targets whatever the pointer says (#6689, the phone terminal audit #6669 finding 4): the header's Fit width, Open Agent Tab and side-panel buttons and the composer's
// Add image and Send were 22 to 36 px unless the browser reported a coarse pointer (a phone-width window with a mouse, or a phone browser that does not say so), the same width rule the rest of the phone layout follows
// (max-width: 767px). Here the pointer is fine and the window is 390 or 360 px wide; with a coarse pointer the sizes were already 44 (touch.css). The header does not crowd: nothing overlaps and nothing scrolls sideways.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); true`); fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h] of [[390, 844], [360, 800]]) {
            const at = `at ${w}px, fine pointer: `;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 2, mobile: false }); await Emulation.setTouchEmulationEnabled({ enabled: false });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            assert.equal(await ev(`matchMedia('(pointer: coarse)').matches`), false, at + 'the pointer is fine');
            await ev(`window.__m.add('th-a', 'A', 4, { sleeping: false }); true`); await select('th-a');
            await ev(`window.setLiveViewMode('terminal'); true`);
            await waitFor(`window.__vis('#terminal-statusbar')`, 'the terminal view', 8000);
            const SEL = ['#terminal-fit-width-btn', '#terminal-open-window-link', '#popout-panel-toggle-btn', '.attach-image-btn', '.mobile-send-btn:not(.mobile-send-team-btn)', '.mobile-send-team-btn'];
            // Fit width shows when the pane is wider than the window: it is made to show, and measured in the same turn (the page hides it again when it measures the pane)
            const boxes = await ev(`(() => { const f = document.getElementById('terminal-fit-width-btn'); if (f) f.hidden = false; return ${JSON.stringify(SEL)}.map(s => { const e = document.querySelector(s); if (!e || !e.checkVisibility()) return { s, missing: true }; const b = e.getBoundingClientRect(); return { s, w: Math.round(b.width), h: Math.round(b.height), l: Math.round(b.left), t: Math.round(b.top), r: Math.round(b.right), b: Math.round(b.bottom) }; }); })()`);
            const missing = boxes.filter(x => x.missing).map(x => x.s);
            assert.deepEqual(missing, [], at + 'every control is on the screen: ' + JSON.stringify(boxes));
            const small = boxes.filter(x => x.w < 44 || x.h < 44).map(x => `${x.s} ${x.w}x${x.h}`);
            assert.deepEqual(small, [], at + 'every control is a 44 px target: ' + JSON.stringify(boxes));
            // the header does not crowd: its buttons do not overlap each other and nothing scrolls sideways
            const hdr = boxes.slice(0, 3);
            for (let i = 0; i < hdr.length; i++) for (let j = i + 1; j < hdr.length; j++) assert.ok(hdr[i].r <= hdr[j].l || hdr[j].r <= hdr[i].l || hdr[i].b <= hdr[j].t || hdr[j].b <= hdr[i].t, at + `${hdr[i].s} and ${hdr[j].s} overlap: ` + JSON.stringify([hdr[i], hdr[j]]));
            assert.equal(await ev(`document.documentElement.scrollWidth <= innerWidth`), true, at + 'no sideways scroll');
            assert.ok(hdr.every(x => x.r <= w), at + 'the header buttons are all on the screen: ' + JSON.stringify(hdr.map(x => x.r)));
            const names = await ev(`['#terminal-fit-width-btn', '#terminal-open-window-link', '#popout-panel-toggle-btn', '.attach-image-btn', '.mobile-send-btn:not(.mobile-send-team-btn)'].map(s => { const e = document.querySelector(s); return (e.getAttribute('aria-label') || e.textContent || '').trim(); })`);
            assert.ok(names.every(n => n.length > 0), at + 'each has a name: ' + JSON.stringify(names));
            await shot(`terminal-header-${w}`);
        }
        console.log('PASS terminal header targets: 44 px on a phone-width window with any pointer');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
