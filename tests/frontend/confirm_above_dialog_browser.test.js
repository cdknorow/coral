// A confirmation asked from the Settings dialog (Prune, Start automatic cleanup, Delete received files, Delete terminal logs) is on top of it: it comes earlier in the page than
// Settings and had the same layer, so it was painted over by the dialog that asked, and what showed was the dimmed page. The test asks what is at the confirmation's own centre.
// Run against an isolated dev server. CORAL_SCREENSHOT_DIR=<dir> saves a screenshot.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: the dialog fades in; no completion event
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        const onTop = () => ev(`(() => { const c = document.querySelector('#confirm-modal .modal-content'); const r = c.getBoundingClientRect(); const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
            return { shown: getComputedStyle(document.getElementById('confirm-modal')).display !== 'none', inside: !!at && !!at.closest('#confirm-modal'), what: at && (at.id || at.className || at.tagName) }; })()`);
        for (const [name, open] of [['Settings', `window.showSettingsModal()`]]) {
            await ev(`${open}; true`);
            await waitFor(`getComputedStyle(document.getElementById('settings-modal')).display !== 'none'`, name, 8000);
            await ev(`window.showConfirmModal('Delete terminal logs', 'Delete 12 terminal logs of ended agents (45 MiB)? This can not be undone.', () => {}); true`);
            await waitFor(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'the confirmation', 8000);
            await sleep(500); // sleep-ok: the confirmation fades in; no completion event
            const t = await onTop();
            assert.equal(t.shown, true);
            assert.equal(t.inside, true, `a confirmation asked from ${name} is above it (what is at its centre: ${t.what})`);
            const d = process.env.CORAL_SCREENSHOT_DIR; if (d) { fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(path.join(d, 'confirm-above-settings.png'), Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); }
            await ev(`document.querySelector('#confirm-modal .modal-actions .btn:not(#confirm-modal-yes)').click(); window.hideSettingsModal(); true`);
        }
        console.log('PASS');
    } catch (e) { console.error(e && e.stack || e); process.exitCode = 1; } finally { try { await client.close(); } catch (_) {} }
})();
