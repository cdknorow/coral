// UI audit #5589, fifth batch (findings 18 and 20), on the real server and dialog, at 1280 and 390 px:
//   18  Settings > Agents reads in groups: the defaults (permission mode, folder, thinking), the models (default, custom), the
//       switches, and last the CLI paths;
//   20  Settings > Storage and cleanup: the unit of a limit is beside its field, not in its label, and is read with it.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const shot = async (name, w) => { if (!process.env.CORAL_SCREENSHOT_DIR) return; fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); await new Promise(r => setTimeout(r, 300)); // sleep-ok: a screenshot settles layout, there is no event for it
        fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name}-${w}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 900, false], [390, 844, true]]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: mobile ? 2 : 1, mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'settings')`);
            const open = async group => { await ev(`(async () => { await window.showSettingsModal(${JSON.stringify(group)}); return true; })()`); await waitFor(`getComputedStyle(document.getElementById('settings-modal')).display !== 'none'`, 'Settings'); };

            console.log(`1. (${w}) Agents: defaults, models, switches, then the CLI paths`);
            await open('agents');
            const names = await ev(`[...document.querySelectorAll('#settings-pane-agents .settings-row')].map(r => { const c = (r.querySelector('dt label, dt') || document.createElement('i')).cloneNode(true); c.querySelectorAll('.settings-info-btn').forEach(b => b.remove()); return c.textContent.trim().replace(/\\s+/g, ' ').slice(0, 40); })`);
            assert.deepEqual(names.slice(0, 5), ['Default permission mode', 'Default working directory', 'Default thinking', 'Default models', 'Custom models'], JSON.stringify(names));
            assert.equal(names[names.length - 1], 'CLI paths', 'the CLI paths come last: ' + JSON.stringify(names));
            assert.ok(names.slice(5, -1).length >= 4 && names.slice(5, -1).every(n => n !== 'CLI paths'), 'the switches are between: ' + JSON.stringify(names));
            await ev(`(() => { for (const id of ['settings-working-dir', 'settings-cli-path-claude', 'settings-cli-path-codex', 'settings-cli-path-agy', 'settings-cli-path-pi']) { const e = document.getElementById(id); if (e) e.value = id === 'settings-working-dir' ? '/work/project' : ''; } return true; })()`); // for the picture only (the dialog is closed unsaved): the test server's own folders are not shown
            await shot('agents-order', w);
            await ev(`window.hideSettingsModal(); true`);

            console.log(`2. (${w}) Storage: the unit beside its field, read with it`);
            await open('cleanup');
            const st = await ev(`['settings-artifact-store-max', 'settings-received-max', 'settings-retention-days'].map(id => { const i = document.getElementById(id); const u = document.getElementById(id + '-unit'); const l = document.querySelector('label[for=' + id + ']').textContent.trim(); const ir = i.getBoundingClientRect(), ur = u.getBoundingClientRect();
                return { id, label: l, unit: u.textContent, described: (i.getAttribute('aria-describedby') || '').split(' ').includes(id + '-unit'), beside: ur.left >= ir.right - 1 && Math.abs((ur.top + ur.height / 2) - (ir.top + ir.height / 2)) < 20, w: Math.round(ir.width) }; })`);
            assert.deepEqual(st.map(x => [x.label, x.unit]), [['Artifact store limit', 'GiB'], ['Received files limit', 'GiB'], ['Automatic cleanup', 'days']], JSON.stringify(st));
            assert.ok(st.every(x => x.described && x.beside && x.w <= 120), 'unit described, beside, field not full width: ' + JSON.stringify(st));
            await shot('storage-units', w);
            await ev(`window.hideSettingsModal(); true`);
        }
        console.log('   ✓');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
