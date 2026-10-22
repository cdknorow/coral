// In hub mode the Settings dialog keeps its layout: the hub note (applyCloudSettingsDialog) sits inside the content column, so the groups list and the pane keep their widths and the
// note is fully visible, at 390, 944 and 1280 px. The note used to be inserted beside .settings-shell in the dialog's row body and took the shell's width: the whole dialog collapsed to a few
// pixels on the hub (coral-hub's production report). Drives Chrome over CDP through agent_view_harness.js against an isolated dev server; run_cdp_suites.mjs finds it by name.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 800, false], [944, 800, false], [390, 844, true]]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
            await require('./helpers/wait').bind(ev).coralReady('startup', 'settings');
            await ev(`document.body.classList.add('cloud-mode'); (async () => { await window.showSettingsModal('general'); return true; })()`);
            await waitFor(`getComputedStyle(document.getElementById('settings-modal')).display !== 'none'`, 'Settings');
            await ev(`(async () => { (await import((window.__coralStatic || '/static/') + 'cloud_settings.js')).applyCloudSettingsDialog(); return true; })()`);
            const m = await ev(`(() => {
                const modal = document.getElementById('settings-modal'), r = e => e && e.getBoundingClientRect();
                const content = r(modal.querySelector('.modal-content')), nav = r(modal.querySelector('.settings-nav')), sel = r(document.getElementById('settings-group-select')), panes = r(modal.querySelector('.settings-panes'));
                const note = modal.querySelector('.settings-cloud-note'), n = r(note);
                const vis = e => !!e && e.width > 0 && e.height > 0;
                return { content: Math.round(content.width), nav: vis(nav) ? Math.round(nav.width) : null, select: vis(sel) ? Math.round(sel.width) : null, panes: Math.round(panes.width),
                    shell: Math.round(r(modal.querySelector('.settings-shell')).width), noteIn: !!note && !!note.closest('.settings-panes'), noteBox: n && { w: Math.round(n.width), h: Math.round(n.height), l: Math.round(n.left), rt: Math.round(n.right) },
                    noteClipped: !!note && note.scrollWidth > note.clientWidth + 1, vw: innerWidth, contentLeft: Math.round(content.left), contentRight: Math.round(content.right) };
            })()`);
            const tag = `${w}px`;
            assert.ok(m.noteIn, `${tag}: the hub note is inside the content column ${JSON.stringify(m)}`);
            assert.ok(m.shell >= m.content * 0.9, `${tag}: the shell fills the dialog (${m.shell} of ${m.content})`);
            assert.ok(m.panes >= (mobile ? m.content * 0.9 : 300), `${tag}: the content pane keeps its width (${m.panes})`);
            if (mobile) assert.ok(m.select >= 150, `${tag}: the groups selector keeps its width (${m.select})`);
            else assert.ok(m.nav >= 120, `${tag}: the groups list keeps its width (${m.nav})`);
            assert.ok(m.noteBox && m.noteBox.w >= Math.min(240, m.panes * 0.6) && m.noteBox.h > 0 && !m.noteClipped && m.noteBox.l >= m.contentLeft - 1 && m.noteBox.rt <= m.contentRight + 1, `${tag}: the note is fully visible ${JSON.stringify(m)}`);
            if (process.env.CORAL_SCREENSHOT_DIR) { fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/hub-settings-${w}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); }
            console.log(`   ok ${tag}: dialog ${m.content}, nav ${m.nav || m.select}, pane ${m.panes}, note ${m.noteBox.w}x${m.noteBox.h}`);
        }
        console.log('PASS the hub-mode Settings dialog keeps its layout');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
