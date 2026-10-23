// New Agent's second step in Launch Team's style (#6457, the operator's 09:25Z request): no cards, the choices as compact rows like the members table: Provider (the radio group), Model, Thinking and Mode, each with the value that will be
// used and a Change that opens the section that sets it. 1280 and 390 px, real isolated server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const vis = sel => `(() => { const e = document.querySelector(${JSON.stringify(sel)}); return !!e && e.getClientRects().length > 0 && getComputedStyle(e).visibility !== 'hidden'; })()`;
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; await ev(`document.activeElement && document.activeElement.blur(); true`); await new Promise(r => setTimeout(r, 400)); /* sleep-ok: a screenshot waits out the section highlight's transition */ fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 800, false], [390, 844, true]]) {
            const at = `${w}px`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`(async () => { await window.showLaunchModal(); document.querySelector('#launch-step-chooser [data-arg="agent"]').click(); return true; })()`);
            await waitFor(`getComputedStyle(document.getElementById('launch-step-agent')).display !== 'none' && !!document.getElementById('launch-agent-next')`, `${at}: step 1`, 8000);
            assert.equal(await ev(vis('#launch-agent-summary')), false, `${at}: the rows are not in step 1`);
            await ev(`(() => { const i = document.getElementById('launch-dir'); i.value = '/tmp'; document.getElementById('launch-agent-next').click(); return true; })()`);
            await waitFor(`document.getElementById('launch-step-agent').dataset.stage === 'details' && !document.getElementById('launch-agent-pick').hidden`, `${at}: step 2`, 8000);

            console.log(`${at}: Provider, Model, Thinking and Mode as rows`);
            assert.deepEqual(await ev(`[...document.querySelectorAll('#launch-agent-summary tr > th')].map(t => t.textContent.trim())`), ['Provider', 'Model', 'Thinking', 'Mode']);
            assert.equal(await ev(`!!document.querySelector('#launch-agent-summary tr:first-child #launch-agent-pick')`), true, `${at}: the radio group is the Provider row`);
            assert.equal(await ev(`document.querySelectorAll('#launch-agent-summary input[type=radio]').length`), 4);
            const val = row => ev(`document.querySelector('#launch-agent-summary [data-row="${row}"] .las-value').textContent.replace(/\\s+/g, ' ').trim()`);
            assert.equal(await val('model'), 'CLI default', `${at}: no model chosen says what happens`);
            assert.equal(await val('mode'), await ev(`(() => { const s = document.querySelector('#launch-agent-acf .acf-permission-mode'); return s.options[s.selectedIndex].textContent.trim(); })()`), `${at}: Mode is the form's own Mode`);
            await shot(`rows-${w}`);

            console.log(`${at}: a value changed in the form shows in its row; Change opens the section`);
            await ev(`{ const m = document.querySelector('#launch-agent-acf .acf-model'); m.value = 'claude-opus-4-1'; m.dispatchEvent(new Event('input', { bubbles: true })); m.dispatchEvent(new Event('change', { bubbles: true })); } true`);
            assert.match(await val('model'), /Opus 4\.1/, `${at}: the model row follows the model`);
            await ev(`{ const m = document.querySelector('#launch-agent-acf .acf-permission-mode'); m.value = 'plan'; m.dispatchEvent(new Event('change', { bubbles: true })); } true`);
            assert.equal(await val('mode'), 'Plan');
            await ev(`document.querySelector('#launch-agent-summary [data-row="mode"] .las-change').click(); true`);
            const openSection = () => ev(`(() => { const t = document.querySelector('#launch-agent-more .settings-tabs [aria-selected=true] .settings-tab-name'); const sel = document.querySelector('#launch-agent-more .settings-group-pick select'); return t && t.getClientRects().length ? t.textContent.trim() : sel ? sel.options[sel.selectedIndex].textContent.trim() : ''; })()`);
            await waitFor(`(() => { const p = document.querySelector('#launch-agent-more .settings-pane:not([hidden])'); return !!p && !!p.querySelector('.acf-permission-mode'); })()`, `${at}: Change on Mode shows the Permissions section`, 4000);
            assert.equal(await openSection(), 'Permissions');
            assert.equal(await ev(`document.querySelector('#launch-agent-summary [data-row="mode"] .las-change').getAttribute('aria-label')`), 'Change the mode');
            await ev(`document.querySelector('#launch-agent-summary [data-row="model"] .las-change').click(); true`);
            await waitFor(`(() => { const p = document.querySelector('#launch-agent-more .settings-pane:not([hidden])'); return !!p && !!p.querySelector('.acf-model'); })()`, `${at}: Change on Model shows the Agent section`, 4000);
            assert.equal(await openSection(), 'Agent');
            if (mobile) {
                assert.deepEqual(await ev(`[...document.querySelectorAll('#launch-agent-summary .las-change')].filter(b => b.getBoundingClientRect().height < 44).length`), 0, `${at}: 44 px targets`);
                assert.equal(await ev(`document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1`), true, `${at}: no sideways scroll`);
            }
            await ev(`window.hideLaunchModal(); true`);
        }
        console.log('launch_agent_rows: ok');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
