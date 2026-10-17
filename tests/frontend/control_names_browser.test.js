// Every form control in the Settings dialog, the default-prompts dialog and the generic prompt dialog has an accessible name (a label tied
// to it, aria-label or aria-labelledby): the Default Working Directory box, the four default-prompt boxes and the prompt dialog's input
// had none (#3981 audit, point 8). Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`window.__coral.buildAllLazy(); true`); // the dialogs are built on first open (#4500): check every control in them
        const unnamed = await ev(`(() => { const t = s => (s || '').replace(/\\s+/g, ' ').trim(); const out = [];
            for (const root of ['settings-modal', 'default-prompts-modal', 'prompt-modal']) {
                for (const el of document.querySelectorAll('#' + root + ' input, #' + root + ' select, #' + root + ' textarea')) {
                    if (el.type === 'hidden') continue;
                    let name = t(el.getAttribute('aria-label'));
                    const by = el.getAttribute('aria-labelledby');
                    if (!name && by) name = t(by.split(/\\s+/).map(i => (document.getElementById(i) || {}).textContent).join(' '));
                    if (!name && el.labels && el.labels.length) name = t([...el.labels].map(l => l.textContent).join(' '));
                    if (!name) out.push(root + ' #' + el.id);
                } }
            return out; })()`);
        assert.deepEqual(unnamed, [], 'form controls with no accessible name: ' + unnamed.join(', '));
        const count = await ev(`document.querySelectorAll('#settings-modal input, #settings-modal select, #default-prompts-modal textarea, #prompt-modal input').length`);
        assert.ok(count > 20, 'the dialogs have controls to check (' + count + ')');
        console.log(`PASS ${count} form controls in Settings, default prompts and the prompt dialog all have a name`);
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
