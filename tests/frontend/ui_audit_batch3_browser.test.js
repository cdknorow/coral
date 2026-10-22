// UI audit #5589, third batch (findings 16, 19, 21, 24, 27), on the real server and the real dialogs:
//   16  Settings > Terminal: each backend is one line, the whole explanation (Security's wording) opens under "Learn more";
//   19  Settings > General: the machine health is part of About;
//   21  Settings > Network and Hub, a server with no hub: the command to start with has a Copy button;
//   24  the launch chooser draws the robot the start page draws, in the page's font;
//   27  Settings' footer button says "Save settings", as Team settings does.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const shot = async (name, w, h) => { if (!process.env.CORAL_SCREENSHOT_DIR) return; fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); await new Promise(r => setTimeout(r, 300)); // sleep-ok: a screenshot settles layout, there is no event for it
        fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name}-${w}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 900, false], [390, 844, true]]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: mobile ? 2 : 1, mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'settings')`);
            const open = async group => { await ev(`(async () => { await window.showSettingsModal(${JSON.stringify(group)}); return true; })()`); await waitFor(`getComputedStyle(document.getElementById('settings-modal')).display !== 'none'`, 'Settings'); };

            // Windows has no tmux choice; keep the current non-Windows UI assertions.
            if (process.platform !== 'win32') {
                console.log(`1. (${w}) Terminal: one line each, the whole explanation under Learn more`);
                await open('terminal');
                await waitFor(`!document.getElementById('settings-terminal-backend').hidden && !document.getElementById('tb-choice').hidden`, 'the backend row', 10000);
                const tb = await ev(`(() => { const vis = e => e.checkVisibility({ checkVisibilityCSS: true }); return [...document.querySelectorAll('#settings-terminal-backend .tb-opt')].map(l => ({ short: l.querySelector('.tb-short').textContent, shortLen: l.querySelector('.tb-short').textContent.length, fullShown: vis(l.querySelector('.tb-d')), learn: l.querySelector('.tb-learn').textContent, expanded: l.querySelector('.tb-learn').getAttribute('aria-expanded'), full: l.querySelector('.tb-d').textContent.length })); })()`);
                assert.equal(tb.length, 2);
                for (const t of tb) { assert.ok(t.shortLen < 140 && !t.fullShown && t.learn === 'Learn more' && t.expanded === 'false' && t.full > 300, JSON.stringify(t)); }
                assert.match(tb[0].short, /^Agents keep running when Coral restarts or upgrades\.$/); assert.match(tb[1].short, /^Agents end when Coral restarts; Coral wakes them\.$/);
                assert.match(await ev(`document.getElementById('tb-d-tmux').textContent`), /any other program running as you, can find that socket/, 'the access warning moved into Learn more, whole');
                const os = await ev(`[...document.querySelectorAll('#settings-terminal-backend .tb-d')].map(d => ({ os: (d.querySelector('.tb-d-os') || {}).textContent || '', shown: d.checkVisibility({ checkVisibilityCSS: true }) }))`);
                assert.ok(os.length === 2 && os.every(o => !o.shown && o.os.length > 100), 'the operating system note ends each backend\'s Learn more, closed at first, said once (no third Learn more): ' + JSON.stringify(os));
                assert.equal(await ev(`document.querySelectorAll('#settings-terminal-backend .tb-learn').length`), 2);
                await shot('terminal-short', w);
                await ev(`document.querySelector('#tb-opt-pty').checked = false; document.querySelector('#settings-terminal-backend .tb-learn[aria-controls=tb-d-pty]').click(); true`);
                const opened = await ev(`(() => { const d = document.getElementById('tb-d-pty'); return { shown: d.checkVisibility({ checkVisibilityCSS: true }), text: d.textContent, label: document.querySelector('.tb-learn[aria-controls=tb-d-pty]').textContent, expanded: document.querySelector('.tb-learn[aria-controls=tb-d-pty]').getAttribute('aria-expanded'), radioChanged: document.getElementById('tb-opt-pty').checked }; })()`);
                assert.ok(opened.shown && opened.label === 'Show less' && opened.expanded === 'true' && /puts them to sleep first when it stops cleanly/.test(opened.text), JSON.stringify(opened).slice(0, 200));
                assert.equal(opened.radioChanged, false, 'Learn more does not choose the backend');
                await shot('terminal-learn', w);
                await ev(`window.hideSettingsModal(); true`);
            }

            console.log(`2. (${w}) General: the machine health is part of About`);
            await open('general');
            assert.deepEqual(await ev(`(() => { const a = document.getElementById('about-section'); const h = document.getElementById('machine-health-local'); return { inAbout: a.contains(h), order: [...a.children].map(c => c.id || c.className.split(' ')[0]) }; })()`).then(r => r.inAbout), true);
            await shot('general-about', w);

            console.log(`3. (${w}) Network and Hub: with no hub, the command has a Copy button`);
            await ev(`window.hideSettingsModal(); true`); await open('network');
            await waitFor(`!!document.querySelector('#settings-cloud [data-cloud=command-row]:not([hidden])') || /connect|configured|Ready/.test(document.getElementById('settings-cloud-status').textContent)`, 'the Coral Hub status', 10000);
            const hubOff = await ev(`/isn't configured/.test(document.getElementById('settings-cloud-status').textContent)`);
            if (hubOff) {
                const row = await ev(`(() => { const r = document.querySelector('#settings-cloud [data-cloud=command-row]'); return { shown: !r.hidden, cmd: r.querySelector('code').textContent, copy: r.querySelector('button').textContent }; })()`);
                assert.deepEqual(row, { shown: true, cmd: 'coral --cloud-url https://app.coralhub.dev\ncoral cloud connect', copy: 'Copy' }); // the sequence: batch 4 puts both steps in the row
                await ev(`window.__copied = []; Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: t => { window.__copied.push(t); return Promise.resolve(); } } }); document.querySelector('#settings-cloud [data-cloud=command-row] button').click(); true`);
                await waitFor(`window.__copied.length === 1`, 'the copy'); assert.deepEqual(await ev(`window.__copied`), ['coral --cloud-url https://app.coralhub.dev\ncoral cloud connect']);
                await shot('network-off', w);
            }
            console.log(`4. (${w}) Settings footer says Save settings`);
            assert.equal(await ev(`document.querySelector('#settings-modal [data-on-click=m-apply-settings]').textContent.trim()`), 'Save settings');
            await ev(`window.hideSettingsModal(); true`);

            console.log(`5. (${w}) The launch chooser: the start page's robot, in the page's font`);
            await ev(`(async () => { await window.showLaunchModal(); return true; })()`);
            await waitFor(`getComputedStyle(document.getElementById('launch-step-chooser')).display !== 'none'`, 'the chooser');
            const ch = await ev(`(() => { const c = document.querySelector('.launch-type-card'); const g = c.querySelector('.launch-type-glyph'); return { glyph: g && g.textContent, font: getComputedStyle(c).fontFamily === getComputedStyle(document.body).fontFamily, svg: c.querySelectorAll('svg').length }; })()`);
            assert.deepEqual(ch, { glyph: 'smart_toy', font: true, svg: 0 });
            await shot('chooser', w);
            await ev(`window.hideLaunchModal(); true`);
        }
        console.log('   ✓');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
