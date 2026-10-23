// The phone's artifact preview can take the whole screen (#5173; operator: "We need to be able to expand the artifact preview to full screen on mobile. It is stuck at the half of the bottom width of the side bar.").
// On a phone the preview sits at the bottom of the side panel, 280 px of it. Its tab bar has a 44 px full-screen toggle; touching it makes the pane cover the viewport (inside the safe-area insets), with a 44 px Back
// button in its tab bar, focus kept inside it (Tab cycles in the pane), the long content scrolling inside it and nothing sideways. Back, Escape and the browser's Back (the phone's back gesture) each close it and return to the
// sheet as it was, with focus back on the toggle. At 390 and 320, both themes; a desktop window shows no toggle and no change. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(path.join(d, name + '.png'), Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const rect = sel => ev(`(() => { const e = document.querySelector(${JSON.stringify(sel)}); if (!e) return null; const r = e.getBoundingClientRect(); return { l: Math.round(r.left), t: Math.round(r.top), r: Math.round(r.right), b: Math.round(r.bottom), w: Math.round(r.width), h: Math.round(r.height) }; })()`);
    const tap = async sel => { const r = await rect(sel); assert.ok(r && r.w > 0 && r.h > 0, `${sel} can be tapped: ` + JSON.stringify(r)); const x = (Math.max(r.l, 0) + Math.min(r.r, 9999)) / 2, y = (r.t + r.b) / 2;
        await Input.dispatchTouchEvent({ type: 'touchStart', touchPoints: [{ x, y }] }); await Input.dispatchTouchEvent({ type: 'touchEnd', touchPoints: [] }); };
    const key = async (k, code, vk) => { for (const type of ['keyDown', 'keyUp']) await Input.dispatchKeyEvent({ type, key: k, code, windowsVirtualKeyCode: vk }); };
    const isFull = () => ev(`document.getElementById('preview-pane').classList.contains('preview-phone-full')`);
    const setup = async (w, h, theme, mobile, savedHeight) => {
        await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 2, mobile });
        await Emulation.setTouchEmulationEnabled({ enabled: mobile });
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: theme }] });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await ev(`localStorage.clear(); ${savedHeight ? `localStorage.setItem('coral-preview-pane-height', '${savedHeight}');` : ''} true`);
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await installAudit(ev); await setTheme(ev, theme);
        await ev(`(async () => { const st = ${S}; const { state } = await import(st + 'state.js'); window.PP = await import(st + 'preview_pane.js'); const { showView } = await import(st + 'utils.js');
            { const real = window.fetch; window.fetch = (u, o) => /\\/api\\/artifacts\\/a{64}/.test(String(u)) ? Promise.resolve(new Response('# Release notes\\n\\nThe preview shows a **markdown** artifact.\\n\\n' + 'A line of the notes that is long enough to wrap on a phone, and then some more words.\\n\\n'.repeat(40) + '\`\`\`\\n' + 'x'.repeat(300) + '\\n\`\`\`\\n', { status: 200, headers: { 'Content-Type': 'text/markdown' } })) : real(u, o); }
            window.__m.add('pv-a', 'P', 4, { sleeping: false, session_id: 'pv-a-id' });
            state.currentSession = { type: 'live', name: 'pv-a', session_id: 'pv-a-id', agent_type: 'claude' }; showView('live-session-view');
            window.PP.setPreviewSession('pv-a-id');
            window.PP.openArtifactTab('coral://artifacts/' + 'a'.repeat(64), { name: 'a.md', mediaType: 'text/markdown' }); return true; })()`);
        await waitFor(`getComputedStyle(document.getElementById('preview-pane')).display !== 'none'`, 'the preview pane', 8000);
        await waitFor(`!!document.querySelector('#preview-body .preview-pane-markdown, #preview-body h1')`, 'the artifact drawn', 8000);
    };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [W, H] of [[390, 844], [320, 640]]) for (const theme of ['dark', 'light']) {
            const at = `${W}px, ${theme}`;
            await setup(W, H, theme, true);
            let pane = await rect('#preview-pane');
            check(`${at}: before, the preview is the sheet's lower part (${pane.w}x${pane.h} at ${pane.t}), not the screen`, pane.h < H * 0.6 && pane.t > 100);
            const btn = await rect('#preview-max-btn');
            check(`${at}: the full-screen toggle is on the tab bar and 44 px (${btn && btn.w}x${btn && btn.h})`, btn && btn.w >= 44 && btn.h >= 44 && btn.r <= W);
            await shot(`preview-phone-before-${W}-${theme}`);
            await ev(`document.getElementById('preview-max-btn').focus(); true`);
            await tap('#preview-max-btn');
            await waitFor(`document.getElementById('preview-pane').classList.contains('preview-phone-full')`, 'full screen', 3000);
            pane = await rect('#preview-pane');
            check(`${at}: the pane covers the viewport (${JSON.stringify(pane)})`, pane.l === 0 && pane.t === 0 && pane.r === W && pane.b === H);
            const st = await ev(`(() => { const p = document.getElementById('preview-pane'); const body = document.getElementById('preview-body'); const top = document.elementFromPoint(${W} / 2, 4); const mid = document.elementFromPoint(${W} / 2, ${H} / 2);
                const back = document.getElementById('preview-full-back'); const br = back.getBoundingClientRect();
                return { role: p.getAttribute('role'), modal: p.getAttribute('aria-modal'), pressed: document.getElementById('preview-max-btn').getAttribute('aria-pressed'), hist: !!(history.state && history.state.coralOverlay), topIn: p.contains(top), midIn: p.contains(mid),
                    backShown: br.width > 0, backH: Math.round(br.height), backW: Math.round(br.width), focusIn: p.contains(document.activeElement), focusId: document.activeElement && document.activeElement.id, over: document.documentElement.scrollWidth - document.documentElement.clientWidth,
                    bodyScrolls: body.scrollHeight > body.clientHeight + 5, bodyOverflowY: getComputedStyle(body).overflowY, sideOver: p.scrollWidth - p.clientWidth, docScrolls: document.documentElement.scrollHeight > innerHeight + 1 }; })()`);
            check(`${at}: it is a modal layer with a history entry, over the top bar, with a 44 px Back button`, st.role === 'dialog' && st.modal === 'true' && st.pressed === 'true' && st.hist && st.topIn && st.midIn && st.backShown && st.backH >= 44 && st.backW >= 44, JSON.stringify(st));
            check(`${at}: focus moved inside (${st.focusId}), nothing scrolls sideways, and the long content scrolls inside the pane`, st.focusIn && st.over === 0 && st.sideOver <= 0 && st.bodyScrolls && /auto|scroll/.test(st.bodyOverflowY), JSON.stringify(st));
            await ev(`document.getElementById('preview-body').scrollTop = 400; true`);
            check(`${at}: the content scrolls (scrollTop ${await ev(`document.getElementById('preview-body').scrollTop`)})`, (await ev(`document.getElementById('preview-body').scrollTop`)) > 0);
            let stayed = true;
            for (let i = 0; i < 14; i++) { await key('Tab', 'Tab', 9); if (!(await ev(`document.getElementById('preview-pane').contains(document.activeElement)`))) { stayed = false; break; } }
            check(`${at}: Tab 14 times never leaves the full-screen preview`, stayed);
            await shot(`preview-phone-full-${W}-${theme}`);
            const audit = await auditRegion(ev, ['#preview-pane']);
            check(`${at}: the full-screen preview reads at 4.5:1`, audit.fails.length === 0, JSON.stringify(audit.fails.slice(0, 2)));

            // Escape
            await key('Escape', 'Escape', 27);
            await waitFor(`!document.getElementById('preview-pane').classList.contains('preview-phone-full')`, 'Escape closing it', 3000);
            pane = await rect('#preview-pane');
            check(`${at}: Escape returns to the sheet (${pane.h} high at ${pane.t}), focus on the toggle, no history entry left`, pane.h < H * 0.6 && (await ev(`document.activeElement && document.activeElement.id`)) === 'preview-max-btn' && !(await ev(`!!(history.state && history.state.coralOverlay)`)));
            // Back button
            await tap('#preview-max-btn'); await waitFor(`document.getElementById('preview-pane').classList.contains('preview-phone-full')`, 'full again', 3000);
            await tap('#preview-full-back');
            await waitFor(`!document.getElementById('preview-pane').classList.contains('preview-phone-full')`, 'Back closing it', 3000);
            check(`${at}: the Back button returns to the sheet`, (await rect('#preview-pane')).h < H * 0.6);
            // the browser's Back
            await tap('#preview-max-btn'); await waitFor(`document.getElementById('preview-pane').classList.contains('preview-phone-full')`, 'full a third time', 3000);
            const before = await ev(`location.pathname`);
            await ev(`history.back(); true`);
            await waitFor(`!document.getElementById('preview-pane').classList.contains('preview-phone-full')`, 'the browser Back closing it', 3000);
            check(`${at}: the browser's Back (the phone's back gesture) closes it and stays on the same page`, (await ev(`location.pathname`)) === before && (await rect('#preview-pane')).h < H * 0.6);
        }

        // A pane height a person saved (dragging the handle, or on the desktop) is set inline on the pane: full screen must still be the whole screen (#6237: it drew only that many px from the top)
        for (const [W, H] of [[390, 844], [430, 932]]) for (const theme of ['dark', 'light']) {
            await setup(W, H, theme, true, 300);
            const small = await rect('#preview-pane');
            check(`saved height, ${W}px, ${theme}: before, the pane is the saved 300 px (${small.h})`, small.h >= 295 && small.h <= 305);
            await tap('#preview-max-btn');
            await waitFor(`document.getElementById('preview-pane').classList.contains('preview-phone-full')`, 'full with a saved height', 3000);
            await shot(`preview-phone-saved-height-${W}-${theme}`);
            const full = await rect('#preview-pane'), body = await rect('#preview-body');
            check(`saved height, ${W}px, ${theme}: expanded, the pane is the viewport (${full.h} of ${H} px) and its body fills it (${body.h} px)`, full.h >= H * 0.95 && full.t === 0 && body.h >= H * 0.8);
            await key('Escape', 'Escape', 27);
            await waitFor(`!document.getElementById('preview-pane').classList.contains('preview-phone-full')`, 'closing', 3000);
            const back = await rect('#preview-pane');
            check(`saved height, ${W}px, ${theme}: closed, the saved height is back (${back.h})`, back.h >= 295 && back.h <= 305);
        }

        // Safe-area insets: the layer stays inside them
        await setup(390, 844, 'dark', true);
        let insets = true;
        try { await Emulation.setSafeAreaInsetsOverride({ insets: { top: 47, bottom: 34, left: 0, right: 0 } }); } catch { insets = false; }
        if (insets) {
            await tap('#preview-max-btn'); await waitFor(`document.getElementById('preview-pane').classList.contains('preview-phone-full')`, 'full with insets', 3000);
            const g = await ev(`(() => { const tb = document.querySelector('.preview-tabbar').getBoundingClientRect(); const bd = document.getElementById('preview-body').getBoundingClientRect(); return { tabTop: Math.round(tb.top), bodyBottom: Math.round(bd.bottom) }; })()`);
            check(`safe-area insets: the tab bar starts below the top inset (${g.tabTop} >= 47) and the body ends above the bottom one (${g.bodyBottom} <= ${844 - 34})`, g.tabTop >= 47 && g.bodyBottom <= 844 - 34, JSON.stringify(g));
            await key('Escape', 'Escape', 27);
            await Emulation.setSafeAreaInsetsOverride({ insets: { top: 0, bottom: 0, left: 0, right: 0 } });
        } else console.log('   (this Chrome has no safe-area override: the insets are checked by the CSS only)');

        // A desktop window: no toggle, no change
        await setup(1280, 900, 'dark', false);
        const d = await ev(`(() => { const b = document.getElementById('preview-max-btn'); const p = document.getElementById('preview-pane'); return { hidden: b.hidden || getComputedStyle(b).display === 'none', full: p.classList.contains('preview-phone-full'), pos: getComputedStyle(p).position, back: getComputedStyle(document.getElementById('preview-full-back')).display }; })()`);
        check('a desktop window: no phone toggle, no full-screen layer, no Back button', d.hidden && !d.full && d.pos !== 'fixed' && d.back === 'none', JSON.stringify(d));
    } catch (e) {
        console.error(e && e.stack || e);
        results.push(false);
    } finally { try { await client.close(); } catch (_) {} }
    const failed = results.filter(r => !r).length;
    console.log(failed ? `${failed} FAILED` : `PASS phone preview full screen: ${results.length} checks`);
    process.exitCode = failed ? 1 : 0;
})();
