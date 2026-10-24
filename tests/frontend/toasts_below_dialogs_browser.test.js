// The startup warnings never sit over an open dialog (#313 row 3): at the real app's default 1200x800 they covered the right end of the first-run dialog's "Set password"
// button and its Confirm "Show". While a dialog is open the column of warnings is painted below it; closed again, it is back above the page. Drives Chrome over CDP against
// the isolated dev server.
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
                const modal = document.getElementById('settings-modal'), content = modal.querySelector('.modal-content') || modal;
                const col = document.getElementById('warning-toasts');
                const zCol = parseInt(getComputedStyle(document.getElementById('notices')).zIndex, 10), zModal = parseInt(getComputedStyle(modal).zIndex, 10);
                const a = col.getBoundingClientRect(), b = content.getBoundingClientRect();
                const l = Math.max(a.left, b.left), r = Math.min(a.right, b.right), t = Math.max(a.top, b.top), bt = Math.min(a.bottom, b.bottom);
                let hit = null;
                if (r > l + 2 && bt > t + 2) { const e = document.elementFromPoint((l + r) / 2, (t + bt) / 2); hit = e && { inColumn: !!e.closest('#warning-toasts'), inModal: !!e.closest('#settings-modal') }; }
                return { zCol, zModal, overlaps: r > l + 2 && bt > t + 2, hit };
            })()`);
            assert.ok(open.zCol < open.zModal, `a dialog is open: the warnings (z ${open.zCol}) are below it (z ${open.zModal})`);
            if (open.overlaps) {
                assert.ok(open.hit && open.hit.inModal && !open.hit.inColumn, 'where the warnings and the dialog overlap, the dialog is what is on top: ' + JSON.stringify(open.hit));
            }

            // it closes: the warnings are above the page again
            await ev(`(async () => { window.hideSettingsModal && window.hideSettingsModal(); return true; })()`);
            await waitFor(`getComputedStyle(document.getElementById('settings-modal')).display === 'none'`, 'the dialog to close');
            const after = await ev(`parseInt(getComputedStyle(document.getElementById('notices')).zIndex, 10)`);
            assert.ok(after >= 300, 'the dialog is closed: the warnings are above the page again: ' + after);
        }
        console.log('   ✓');
    } finally { await Emulation.clearDeviceMetricsOverride().catch(() => {}); await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
