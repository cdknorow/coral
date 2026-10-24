// The startup warnings never sit over an open dialog (#313 row 3): at the real app's default 1200x800 they covered the right end of the first-run dialog's "Set password"
// button and its Confirm "Show". While a dialog is open the warnings step aside; closed again, they are back. A short toast fired while a dialog is open is still on top
// and can be clicked (QA's #344: lowering the whole column put an upload's Retry and Settings' cleanup results under the dialog's backdrop). Drives Chrome over CDP
// against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const STATIC = "(window.__coralStatic || '/static/')";

const SECURITY = 'WARNING: Coral\'s data folder C:\\Users\\someone\\.coral can be read by Users. It holds the API key, the command-line token and the databases.';
const DATABASE = 'The sessions database is damaged (page 12 is corrupt). Stop Coral, copy the data folder somewhere safe, then start Coral again to rebuild it from the logs.';

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        // The warnings are in #notices, the page's fixed column of notices (main's #6162): its z-index is what puts them above the page or below a dialog.
        for (const [w, h] of [[1200, 800], [1200, 600], [390, 844]]) {
            console.log(`${w}x${h}`);
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile: false });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await waitFor(`!!document.getElementById('agent-search-input')`, 'the page', 20000);
            await ev(`document.getElementById('warning-toasts')?.remove(); document.querySelectorAll('.security-warning-toast').forEach(t => t.remove()); true`);
            await ev(`(async () => { const m = await import(${STATIC} + 'update_check.js'); m.showSecurityWarning(${JSON.stringify(SECURITY)}); m.showDatabaseWarning(${JSON.stringify(DATABASE)}); return true; })()`);
            await waitFor(`document.querySelectorAll('.security-warning-toast').length >= 2`, 'the warnings');
            const before = await ev(`parseInt(getComputedStyle(document.getElementById('notices')).zIndex, 10)`);
            assert.ok(before >= 300, 'with no dialog open the warnings are above the page: ' + before);

            // a dialog opens: Settings is wide enough to reach the right edge at 1200
            await ev(`(async () => { await window.showSettingsModal('general'); return true; })()`);
            await waitFor(`!!document.querySelector('#settings-modal') && getComputedStyle(document.querySelector('#settings-modal')).display !== 'none'`, 'the Settings dialog');
            const open = await ev(`(() => {
                const col = document.getElementById('warning-toasts');
                return { shown: col.getClientRects().length > 0, warnings: [...document.querySelectorAll('.security-warning-toast')].filter(w => w.getClientRects().length).length };
            })()`);
            assert.deepEqual(open, { shown: false, warnings: 0 }, 'a dialog is open: no warning is drawn over it');

            // a short toast with an action, fired while the dialog is open (an upload's Retry): it is on top, and its click runs the action
            await ev(`(async () => { const m = await import(${STATIC} + 'notifications.js'); window.__retried = 0; m.showToast('Upload failed', true, undefined, { action: { label: 'Retry', onClick: () => { window.__retried++; } } }); return true; })()`);
            await waitFor(`!!document.querySelector('#toast-host .toast-action')`, 'the toast');
            const top = await ev(`(() => {
                const b = document.querySelector('#toast-host .toast-action').getBoundingClientRect();
                const x = b.left + b.width / 2, y = b.top + b.height / 2;
                const e = document.elementFromPoint(x, y);
                return { onScreen: b.width > 0 && x < innerWidth && y < innerHeight, isButton: !!e && !!e.closest('.toast-action'), x, y };
            })()`);
            assert.ok(top.onScreen && top.isButton, "the toast's Retry is what is on top under the pointer while the dialog is open: " + JSON.stringify(top));
            await client.Input.dispatchMouseEvent({ type: 'mousePressed', x: top.x, y: top.y, button: 'left', clickCount: 1 });
            await client.Input.dispatchMouseEvent({ type: 'mouseReleased', x: top.x, y: top.y, button: 'left', clickCount: 1 });
            await waitFor(`window.__retried === 1`, 'the Retry click to run its action');

            // it closes: the warnings are above the page again
            await ev(`(async () => { window.hideSettingsModal && window.hideSettingsModal(); return true; })()`);
            await waitFor(`getComputedStyle(document.getElementById('settings-modal')).display === 'none'`, 'the dialog to close');
            const after = await ev(`({ z: parseInt(getComputedStyle(document.getElementById('notices')).zIndex, 10), warnings: [...document.querySelectorAll('.security-warning-toast')].filter(w => w.getClientRects().length).length })`);
            assert.ok(after.z >= 300 && after.warnings >= 2, 'the dialog is closed: the warnings are back, above the page: ' + JSON.stringify(after));
        }
        console.log('   ✓');
    } finally { await Emulation.clearDeviceMetricsOverride().catch(() => {}); await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
