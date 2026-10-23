// New Agent's first step helps with the folder (#6370 item 3, Codex Test's first-run walk): a new person, with no folder ever used, reads one line ("Pick the folder your project lives in");
// a person who has launched before sees their last folders as buttons under the field, so a repeat launch is one click. Neither is in step 2. 1280 and 390 px, real isolated server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const fs = require('node:fs');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const vis = sel => `(() => { const e = document.querySelector(${JSON.stringify(sel)}); return !!e && e.getClientRects().length > 0 && getComputedStyle(e).visibility !== 'hidden'; })()`;
    const shot = async (Page, name) => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const open = async () => {
        await ev(`(async () => { await window.showLaunchModal(); document.querySelector('#launch-step-chooser [data-arg="agent"]').click(); return true; })()`);
        await waitFor(`getComputedStyle(document.getElementById('launch-step-agent')).display !== 'none' && document.activeElement && document.activeElement.id === 'launch-dir'`, 'the folder step', 8000);
    };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 800, false], [390, 844, true]]) {
            const at = `${w}px`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`(async () => { const m = await import(${S} + 'launch_server.js'); const k = m.recentDirsKey('local'); localStorage.removeItem(k); return true; })()`);

            console.log(`${at}: no folder ever used: one line of help`);
            await open();
            assert.equal(await ev(vis('#launch-dir-hint')), true, `${at}: the hint shows`);
            assert.equal(await ev(`document.getElementById('launch-dir-hint').textContent`), 'Pick your project folder.');
            assert.equal(await ev(`document.querySelectorAll('#launch-dir-recents button').length`), 0);
            await shot(Page, `folder-hint-${w}`);
            await ev(`window.hideLaunchModal(); true`);

            console.log(`${at}: folders used before: the last three as buttons, the hint gone`);
            await ev(`(async () => { const b = await import(${S} + 'browser.js'); for (const d of ['/work/one', '/work/two', '/work/three', '/work/four']) b.recordRecentDir(d); return true; })()`);
            await open();
            assert.equal(await ev(vis('#launch-dir-hint')), false, `${at}: no hint once there are folders`);
            assert.deepEqual(await ev(`[...document.querySelectorAll('#launch-dir-recents button')].map(b => b.textContent + '|' + b.title)`), ['four|/work/four', 'three|/work/three', 'two|/work/two'], `${at}: the three most recent, newest first, named by the folder`);
            if (mobile) assert.deepEqual(await ev(`[...document.querySelectorAll('#launch-dir-recents button')].filter(b => b.getBoundingClientRect().height < 44).length`), 0, `${at}: 44 px targets`);
            await shot(Page, `folder-recents-${w}`);
            await ev(`document.querySelector('#launch-dir-recents button:nth-child(2)').click(); true`);
            assert.equal(await ev(`document.getElementById('launch-dir').value`), '/work/three', `${at}: a button fills the folder`);
            assert.equal(await ev(`document.getElementById('launch-step-agent').dataset.stage`), 'folder', `${at}: and stays on step 1 (Next is the person's)`);
            await ev(`document.getElementById('launch-agent-next').click(); true`);
            await waitFor(`document.getElementById('launch-step-agent').dataset.stage === 'details'`, `${at}: Next`, 4000);
            assert.equal(await ev(vis('#launch-dir-recents')), false, `${at}: the buttons are not in step 2`);
            await ev(`window.hideLaunchModal(); true`);
        }
        console.log('launch_folder_help: ok');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
