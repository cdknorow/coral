// New Agent is two steps (#6333, the operator's request): step 1 is the working directory with Cancel and Next; step 2 is everything else (the folder as one line with Change, the agent, the sections: name and
// persona, agent, prompt, permissions, board) with Back and Launch. Enter in the folder is Next, an empty folder stops there and says so, focus moves to the first field of step 2 and back to the folder,
// the header arrow goes back one step first, a screen reader hears the step, and every target is 44 px on a phone. 1280 and 390 px, real isolated server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const key = async (k, code, vk, text) => { for (const type of ['keyDown', 'keyUp']) await Input.dispatchKeyEvent({ type, key: k, code, windowsVirtualKeyCode: vk, ...(type === 'keyDown' && text ? { text } : {}) }); };
    const vis = sel => `(() => { const e = document.querySelector(${JSON.stringify(sel)}); return !!e && e.getClientRects().length > 0 && getComputedStyle(e).visibility !== 'hidden'; })()`;
    const stage = () => ev(`document.getElementById('launch-step-agent').dataset.stage`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 800, false], [390, 844, true]]) {
            const at = `${w}px`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`(async () => { await window.showLaunchModal(); document.querySelector('#launch-step-chooser [data-arg="agent"]').click(); return true; })()`);
            await waitFor(`getComputedStyle(document.getElementById('launch-step-agent')).display !== 'none' && document.activeElement && document.activeElement.id === 'launch-dir'`, `${at}: the folder step with the keyboard in the folder`, 8000);

            // step 1: the folder, Cancel, Next; nothing else
            assert.equal(await stage(), 'folder');
            for (const sel of ['#launch-dir', '#btn-browse', '#launch-agent-next', '#launch-agent-cancel']) assert.ok(await ev(vis(sel)), `${at}: ${sel} is in step 1`);
            for (const sel of ['#launch-agent-go', '#launch-agent-back', '#launch-agent-pick', '#launch-agent-more', '#launch-dir-summary']) assert.ok(!(await ev(vis(sel))), `${at}: ${sel} is not in step 1`);

            // Enter with no folder stays here and flags the field
            await key('Enter', 'Enter', 13, '\r');
            await waitFor(`document.getElementById('launch-dir').getAttribute('aria-invalid') === 'true'`, `${at}: an empty folder is flagged`, 4000);
            assert.equal(await stage(), 'folder', `${at}: and stays on step 1`);

            // a folder, Enter: step 2
            await ev(`(() => { const i = document.getElementById('launch-dir'); i.value = '/work/repo'; i.dispatchEvent(new Event('input', { bubbles: true })); i.focus(); return true; })()`);
            await key('Enter', 'Enter', 13, '\r');
            await waitFor(`document.getElementById('launch-step-agent').dataset.stage === 'details'`, `${at}: Enter is Next`, 4000);
            for (const sel of ['#launch-dir-summary', '#launch-agent-go', '#launch-agent-back', '#launch-agent-more']) assert.ok(await ev(vis(sel)), `${at}: ${sel} is in step 2`);
            for (const sel of ['#launch-dir', '#btn-browse', '#launch-agent-next', '#launch-agent-cancel']) assert.ok(!(await ev(vis(sel))), `${at}: ${sel} is not in step 2`);
            assert.equal(await ev(`document.getElementById('launch-dir-summary-path').textContent`), '/work/repo', `${at}: the folder is one line`);
            assert.equal(await ev(`document.activeElement && document.activeElement.id`), 'launch-agent-name-input', `${at}: focus is in the first field of step 2`);
            assert.match(await ev(`document.getElementById('launch-stage-live').textContent`), /^Step 2 of 2/, `${at}: a screen reader hears the step`);
            if (mobile) {
                const small = await ev(`[...document.querySelectorAll('#launch-agent-back, #launch-agent-go, #launch-dir-summary .btn')].filter(b => { const r = b.getBoundingClientRect(); return r.height < 44; }).map(b => b.id || b.textContent)`);
                assert.deepEqual(small, [], `${at}: 44 px targets in step 2`);
            }

            // Back: the folder is kept, focus returns to it
            await ev(`document.getElementById('launch-agent-back').click(); true`);
            await waitFor(`document.getElementById('launch-step-agent').dataset.stage === 'folder' && document.activeElement.id === 'launch-dir'`, `${at}: Back returns to the folder with focus in it`, 4000);
            assert.equal(await ev(`document.getElementById('launch-dir').value`), '/work/repo', `${at}: the folder is kept`);

            // the header arrow goes back one step first, then to the choices
            await ev(`document.getElementById('launch-agent-next').click(); true`);
            await waitFor(`document.getElementById('launch-step-agent').dataset.stage === 'details'`, `${at}: Next`, 4000);
            await ev(`document.querySelector('#launch-step-agent .launch-back-btn').click(); true`);
            assert.equal(await stage(), 'folder', `${at}: the arrow goes back to the folder first`);
            await ev(`document.querySelector('#launch-step-agent .launch-back-btn').click(); true`);
            await waitFor(`getComputedStyle(document.getElementById('launch-step-chooser')).display !== 'none'`, `${at}: then to the choices`, 4000);

            // Change in step 2 is Back
            await ev(`document.querySelector('#launch-step-chooser [data-arg="agent"]').click(); true`);
            assert.equal(await stage(), 'folder', `${at}: a new New Agent opens on step 1`);
            await ev(`(() => { document.getElementById('launch-dir').value = '/work/repo'; document.getElementById('launch-agent-next').click(); document.querySelector('#launch-dir-summary .btn').click(); return true; })()`);
            assert.equal(await stage(), 'folder', `${at}: Change returns to the folder`);
            // reopening New Agent after a launch that reached step 2: step 1, and nothing stale in the live line (#6376)
            await ev(`(() => { document.getElementById('launch-dir').value = '/work/repo'; document.getElementById('launch-agent-next').click(); return true; })()`);
            assert.match(await ev(`document.getElementById('launch-stage-live').textContent`), /^Step 2 of 2/, `${at}: step 2 says so`);
            await ev(`window.hideLaunchModal(); true`);
            await ev(`(async () => { await window.showLaunchModal(); document.querySelector('#launch-step-chooser [data-arg="agent"]').click(); return true; })()`);
            await waitFor(`getComputedStyle(document.getElementById('launch-step-agent')).display !== 'none'`, `${at}: New Agent reopened`, 4000);
            assert.equal(await stage(), 'folder', `${at}: reopened on step 1`);
            assert.equal(await ev(`document.getElementById('launch-stage-live').textContent`), '', `${at}: and the live line is not left saying Step 2 of 2`);
            await ev(`window.hideLaunchModal(); true`);
        }
        console.log('PASS New Agent two steps: folder then details, Enter is Next, focus and the step are announced, 44 px on a phone');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
