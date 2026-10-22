// The (i) buttons of Team settings open and close their text in the sidebar's Team settings tab (workspace mode) as well as in the dialog (#6012: the Working mode (i) was only bound in the modal branch).
// Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const B = 'info-ws-' + Date.now().toString(36);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`fetch('/api/board/${B}/subscribe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'boss', job_title: 'Orchestrator', session_name: 'boss-s' }) }).then(r => r.status)`);
        for (const workspace of [false, true]) {
            const mode = workspace ? 'workspace' : 'dialog';
            if (workspace) await ev(`(async () => { document.getElementById('team-working-mode-dialog')?.remove(); await window.enterTeamContext(${JSON.stringify(B)}, { tab: 'team-settings', silent: true }); return true; })()`); // the sidebar's Team settings tab, as a person opens it
            else await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'team_working_mode.js'); await m.showTeamWorkingMode(${JSON.stringify(B)}, { workspace: false }); return true; })()`);
            await waitFor(`!!document.getElementById('team-settings-tab-health')`, mode + ': the tabs', 15000);
            const toggles = [['workflow', 'wm-info'], ['health', null]];
            for (const [tab, boxId] of toggles) {
                await ev(`document.getElementById('team-settings-tab-${tab}').click(); true`);
                const sel = boxId ? `#team-settings-${tab} .settings-info-btn[aria-controls="${boxId}"]` : `#team-settings-${tab} .settings-info-btn`;
                await waitFor(`!!document.querySelector('${sel}') && document.querySelector('${sel}').getClientRects().length > 0`, `${mode}: the (i) on ${tab}`, 8000);
                assert.equal(await ev(`document.querySelector('${sel}').getAttribute('aria-expanded')`), 'false');
                await ev(`document.querySelector('${sel}').click(); true`);
                assert.equal(await ev(`document.querySelector('${sel}').getAttribute('aria-expanded')`), 'true', `${mode}: the (i) on ${tab} opens its text`);
                assert.equal(await ev(`document.getElementById(document.querySelector('${sel}').getAttribute('aria-controls')).hidden`), false);
                await ev(`document.querySelector('${sel}').click(); true`);
                assert.equal(await ev(`document.querySelector('${sel}').getAttribute('aria-expanded')`), 'false', `${mode}: and closes it`);
            }
        }
        console.log('PASS team settings (i) buttons: dialog and sidebar tab, Workflow and Health check');
    } finally { await ev(`fetch('/api/board/${B}', { method: 'DELETE' }).then(r => r.status)`).catch(() => {}); await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
