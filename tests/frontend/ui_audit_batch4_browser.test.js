// UI audit #5589, fourth batch, on the real server and dialogs, at 1280 and 390 px:
//   - Settings > Network and Hub, a server with no hub: the sentence says it once and the row holds the whole sequence, which Copy takes;
//   - the launch chooser's Cancel is in view (it was clipped by the dialog's edge on a phone);
//   - Settings' footer shows "Unsaved changes" while Save would write something, and not otherwise;
//   - Settings > Agents: the CLI paths are behind one line, and Edit Default Prompts has an icon, not an emoji.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const guard = await require('./helpers/settings_guard').guardSettings(); // the setting this suite changes is put back
    const shot = async (name, w) => { if (!process.env.CORAL_SCREENSHOT_DIR) return; fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); await new Promise(r => setTimeout(r, 300)); // sleep-ok: a screenshot settles layout, there is no event for it
        fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name}-${w}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 900, false], [390, 844, true]]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: mobile ? 2 : 1, mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'settings')`);
            const open = async group => { await ev(`(async () => { await window.showSettingsModal(${JSON.stringify(group)}); return true; })()`); await waitFor(`getComputedStyle(document.getElementById('settings-modal')).display !== 'none'`, 'Settings'); };

            console.log(`1. (${w}) No hub: the sentence once, the whole sequence in the row, Copy takes it`);
            await open('network');
            await waitFor(`/Start Coral with the Hub address/.test(document.getElementById('settings-cloud-status').textContent)`, 'the no-hub status', 10000);
            const hub = await ev(`(() => { const r = document.querySelector('#settings-cloud [data-cloud=command-row]'); return { sentence: document.getElementById('settings-cloud-status').textContent, flag: /--cloud-url|CORAL_CLOUD_URL/.test(document.getElementById('settings-cloud-status').textContent), lines: r.querySelector('code').textContent.split('\\n'), shown: !r.hidden }; })()`);
            assert.ok(hub.shown && !hub.flag, 'the flag is in the row, not repeated in the sentence: ' + JSON.stringify(hub));
            assert.match(hub.sentence, /^Start Coral with the Hub address, then connect:$/, 'the sentence says what order (the two commands are in the row): ' + hub.sentence);
            assert.deepEqual(hub.lines, ['coral --cloud-url https://app.coralhub.dev', 'coral cloud connect']);
            await ev(`window.__copied = []; Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: t => { window.__copied.push(t); return Promise.resolve(); } } }); document.querySelector('#settings-cloud [data-cloud=command-row] button').click(); true`);
            await waitFor(`window.__copied.length === 1`, 'the copy');
            assert.deepEqual(await ev(`window.__copied`), ['coral --cloud-url https://app.coralhub.dev\ncoral cloud connect'], 'Copy takes the whole sequence');
            await shot('network-sequence', w);
            await ev(`window.hideSettingsModal(); true`);

            console.log(`2. (${w}) The chooser: Cancel is in view`);
            await ev(`(async () => { await window.showLaunchModal(); return true; })()`);
            await waitFor(`getComputedStyle(document.getElementById('launch-step-chooser')).display !== 'none'`, 'the chooser');
            const cancel = await ev(`(() => { const b = [...document.querySelectorAll('#launch-step-chooser .modal-actions button')].find(x => /Cancel/.test(x.textContent)); const r = b.getBoundingClientRect(); const d = document.querySelector('#launch-modal .modal-content').getBoundingClientRect(); const at = y => { const t = document.elementFromPoint(r.left + r.width / 2, y); return t === b || b.contains(t); }; return { inDialog: r.bottom <= d.bottom + 1 && r.top >= d.top, inView: r.bottom <= innerHeight + 1, hit: at(r.top + r.height / 2) && at(r.top + 3) && at(r.bottom - 3), h: Math.round(r.height) }; })()`); // the top, the middle and the lower edge all hit the button: a clipped edge hits what is behind it
            assert.ok(cancel.inDialog && cancel.inView && cancel.hit && cancel.h >= 28, 'Cancel is whole and reachable: ' + JSON.stringify(cancel));
            await shot('chooser-cancel', w);
            await ev(`window.hideLaunchModal(); true`);

            console.log(`3. (${w}) Unsaved changes`);
            await open('general');
            const note = () => ev(`!document.getElementById('settings-unsaved').hidden`);
            assert.equal(await note(), false, 'nothing changed: no note');
            const was = await ev(`document.getElementById('settings-show-scrollbars').checked`);
            await ev(`(() => { const c = document.getElementById('settings-show-scrollbars'); c.checked = !c.checked; c.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
            await waitFor(`!document.getElementById('settings-unsaved').hidden`, 'the note after a change', 5000);
            assert.equal(await ev(`document.getElementById('settings-unsaved').textContent`), 'Unsaved changes');
            await shot('unsaved', w);
            await ev(`(() => { const c = document.getElementById('settings-show-scrollbars'); c.checked = ${was}; c.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
            await waitFor(`document.getElementById('settings-unsaved').hidden`, 'the note goes when the change is undone', 5000);
            await ev(`(() => { const c = document.getElementById('settings-show-scrollbars'); c.checked = !c.checked; c.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
            await waitFor(`!document.getElementById('settings-unsaved').hidden`, 'the note again');
            await ev(`document.querySelector('#settings-modal [data-on-click=m-apply-settings]').click(); true`);
            await waitFor(`document.getElementById('settings-unsaved').hidden`, 'the note goes after Save', 10000);
            await ev(`(() => { const c = document.getElementById('settings-show-scrollbars'); c.checked = ${was}; c.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
            await ev(`document.querySelector('#settings-modal [data-on-click=m-apply-settings]').click(); true`); // put the setting back
            await waitFor(`document.getElementById('settings-unsaved').hidden`, 'and after the put-back', 10000);
            await ev(`window.hideSettingsModal(); true`);

            console.log(`4. (${w}) Agents: the CLI paths are behind one line; an icon, not an emoji`);
            await open('agents');
            const ag = await ev(`(() => { const d = document.querySelector('.settings-cli-details'); const vis = e => e.checkVisibility({ checkVisibilityCSS: true }); const btn = document.querySelector('.settings-prompts-btn'); return { open: d.open, summary: d.querySelector('summary').textContent, pathsShown: vis(document.getElementById('settings-cli-path-claude')), icon: !!btn.querySelector('.material-icons'), emoji: /\\u270f/i.test(btn.textContent) }; })()`);
            assert.deepEqual(ag, { open: false, summary: 'Set paths (empty uses PATH)', pathsShown: false, icon: true, emoji: false });
            await ev(`document.querySelector('.settings-cli-details > summary').click(); true`);
            assert.equal(await ev(`document.getElementById('settings-cli-path-claude').checkVisibility({ checkVisibilityCSS: true })`), true, 'the paths open');
            await shot('agents-cli', w);
            await ev(`window.hideSettingsModal(); true`);
        }
        console.log('   ✓');
    } finally { await guard.restore(); await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
