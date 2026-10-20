// A phone's expanded artifact preview and the artifact viewer cover the whole screen, also after a pinch zoom (#5493; operator: expanding the artifact preview fills only the top half of the screen).
// mobile.js sets --visual-viewport-height from the visual viewport for the on-screen keyboard (body, the viewer and the comments sheet follow it), but a pinch zoom shrinks visualViewport.height
// too (at 2x it is half the screen's), so a zoomed page was half as tall. The height is now the visual viewport's times its scale, in the page's own pixels: a zoom changes nothing, a keyboard still does.
// At 390x844 and 320x568. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit } = require('./helpers/contrast');
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
    const setup = async (w, h, theme, mobile) => {
        await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 2, mobile });
        await Emulation.setTouchEmulationEnabled({ enabled: mobile });
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: theme }] });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await ev(`localStorage.clear(); true`);
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
        const settle = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a pinch zoom and a viewport change have no event for the page's own listeners to be done
        const geometry = () => ev(`(() => { const r = e => { const x = e && e.getBoundingClientRect(); return x ? [Math.round(x.top), Math.round(x.bottom)] : null; }; const v = document.querySelector('.artifact-viewer');
            return { H: innerHeight, vvar: document.documentElement.style.getPropertyValue('--visual-viewport-height'), body: Math.round(document.body.getBoundingClientRect().height), pane: r(document.getElementById('preview-pane')), viewer: v && !v.hidden ? r(v) : null }; })()`);
        for (const [W, H] of [[390, 844], [320, 568]]) {
            const at = `${W}x${H}`;
            await setup(W, H, 'dark', true);
            await ev(`document.getElementById('preview-max-btn').focus(); true`);
            await tap('#preview-max-btn');
            await waitFor(`document.getElementById('preview-pane').classList.contains('preview-phone-full')`, 'full screen', 3000);
            let g = await geometry();
            check(`${at}: expanded, the preview covers the viewport (${JSON.stringify(g.pane)}) and the page is the screen's height (${g.vvar})`, g.pane[0] === 0 && g.pane[1] === H && (g.vvar === '' || g.vvar === H + 'px') && g.body === H, JSON.stringify(g)); // (unset until the first viewport change: the page then follows 100dvh)

            // a pinch zoom is not a keyboard: the page keeps its whole screen
            await Emulation.setPageScaleFactor({ pageScaleFactor: 2 }); await settle(400);
            g = await geometry();
            check(`${at}: zoomed 2x, the page is still the screen's height (${g.vvar}, body ${g.body})`, g.vvar === H + 'px' && g.body === H && g.pane[0] === 0 && g.pane[1] === H, JSON.stringify(g));
            check(`${at}: zoomed, no keyboard is assumed`, !(await ev(`document.body.classList.contains('keyboard-open')`)));
            // the artifact viewer, opened zoomed, is the whole screen too
            await ev(`import(${S} + 'artifact_viewer.js').then(m => m.openArtifactViewer('a'.repeat(64), { name: 'a.md', mediaType: 'text/markdown' })); true`);
            await waitFor(`(() => { const v = document.querySelector('.artifact-viewer'); return !!v && !v.hidden && v.getBoundingClientRect().height > 100; })()`, 'the viewer', 8000);
            g = await geometry();
            check(`${at}: zoomed, the artifact viewer fills the screen (${JSON.stringify(g.viewer)})`, g.viewer && g.viewer[0] === 0 && g.viewer[1] === H, JSON.stringify(g));
            await Emulation.setPageScaleFactor({ pageScaleFactor: 1 }); await settle(400);
            g = await geometry();
            check(`${at}: back at 1x, the viewer fills the screen`, g.viewer && g.viewer[1] === H && g.vvar === H + 'px', JSON.stringify(g));

            // a keyboard still takes its part of the screen, and gives it back
            await Emulation.setDeviceMetricsOverride({ width: W, height: Math.round(H / 2), deviceScaleFactor: 2, mobile: true }); await settle(400);
            g = await geometry();
            check(`${at}: with the keyboard up (the viewport halved) the viewer follows what is left (${JSON.stringify(g.viewer)})`, g.vvar === Math.round(H / 2) + 'px' && g.viewer && g.viewer[1] === Math.round(H / 2), JSON.stringify(g));
            await Emulation.setDeviceMetricsOverride({ width: W, height: H, deviceScaleFactor: 2, mobile: true }); await settle(400);
            g = await geometry();
            check(`${at}: and gives it back`, g.vvar === H + 'px' && g.viewer && g.viewer[1] === H, JSON.stringify(g));
        }
        const failed = results.filter(r => !r).length;
        console.log(`${results.length - failed}/${results.length} checks passed`);
        if (failed) process.exitCode = 1;
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
