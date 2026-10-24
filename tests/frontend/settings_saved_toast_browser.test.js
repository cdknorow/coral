// The Save toast names what changed (#7041, UI/UX's #7036 finding 2): "Saved: Show scrollbars off, Git diff mode", not "Settings saved" for every change. Drives the real Save through
// agent_view_harness.js against the isolated dev server (the settings it writes are put back by the runner).
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: the dialog fills after it opens
    const toastText = () => ev(`[...document.querySelectorAll('.toast')].map(t => t.textContent.replace('×', '').trim()).filter(Boolean).join(' | ')`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        // two changes in two panes: a switch and a select
        await ev(`window.showSettingsModal('general'); true`);
        await waitFor(`!!document.getElementById('settings-show-scrollbars') && document.getElementById('settings-show-scrollbars').getClientRects().length > 0`, 'the General pane', 8000);
        await sleep(500); // sleep-ok: the dialog fills, and a Save that changes nothing shows no toast to wait for
        await ev(`(() => { const c = document.getElementById('settings-show-scrollbars'); c.click(); const g = document.getElementById('settings-git-diff-mode'); g.selectedIndex = (g.selectedIndex + 1) % g.options.length; g.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
        const nowOn = await ev(`document.getElementById('settings-show-scrollbars').checked`);
        await ev(`document.getElementById('settings-save-btn').click(); true`);
        await waitFor(`[...document.querySelectorAll('.toast')].some(t => /Saved/.test(t.textContent))`, 'the toast', 6000);
        const t = await toastText();
        assert.ok(t.includes(`Saved: Show scrollbars ${nowOn ? 'on' : 'off'}, Git diff mode`), 'the toast names the two rows: ' + t);
        // nothing changed: the plain answer stays
        await ev(`window.showSettingsModal('general'); true`); await sleep(600); // sleep-ok: the dialog fills, and a Save that changes nothing shows no toast to wait for
        await ev(`document.getElementById('settings-save-btn').click(); true`);
        await sleep(500); // sleep-ok: the dialog fills, and a Save that changes nothing shows no toast to wait for
        const t2 = await toastText();
        assert.ok(!/Saved: /.test(t2.split(' | ').pop()), 'a Save with nothing changed does not name rows: ' + t2);
        console.log('PASS settings saved toast');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
