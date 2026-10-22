// What 150 and 151 added was first looked at only in the dark theme (#5781); with the real theme swap (helpers/theme.js, #5757) this holds the light theme to the same 4.5:1
// as the dark one, with the contrast audit (helpers/contrast.js): the warning toasts (security, firewall, database: #5737) and the agent hover card with its Thinking row (#5742).
// The comments cards, the Inbox panel and the agent line are held in light by their own suites; the pasted-image chips by pasted_image_artifact. With LIGHT_REPORT=<dir> a
// picture of each view in each theme and the audit's findings are written there. Drives Chrome over CDP against the isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const BASE = require('./test_base').BASE;

const FIREWALL = 'Windows Firewall lets other computers reach Coral on every port, more than it needs. Coral never makes firewall rules: Windows made these when someone clicked Allow on its prompt. To limit each rule to Private networks, open PowerShell as an administrator and run:\n- Coral (C:\\Coral\\coral.exe) on the Public profile, for every port:\nSet-NetFirewallRule -Name \'TCP Query User{26C1}C:\\Coral\\coral.exe\' -Profile Private\nOr, to remove the rule instead:\nRemove-NetFirewallRule -Name \'TCP Query User{26C1}C:\\Coral\\coral.exe\'';
const SECURITY = 'Coral\'s data folder C:\\Users\\someone\\.coral can be read by Users. It holds the API key, the command-line token and the databases. Move the folder (CORAL_DATA_DIR) to one only you can read, or remove those groups from its permissions (Properties > Security).';
const DATABASE = 'The sessions database is damaged (page 12 is corrupt). Stop Coral, copy the data folder somewhere safe, then start Coral again to rebuild it from the logs.';

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const out = process.env.LIGHT_REPORT; const report = [];
    const shot = async n => { if (!out) return; fs.mkdirSync(out, { recursive: true }); fs.writeFileSync(`${out}/${n}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await Emulation.setFocusEmulationEnabled({ enabled: true });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`); await installAudit(ev);
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'update_check.js'); m.showSecurityWarning(${JSON.stringify(SECURITY)}); await m.showFirewallWarning(${JSON.stringify(FIREWALL)}); m.showDatabaseWarning(${JSON.stringify(DATABASE)}); return true; })()`);
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'session_state.js'); const tip = document.createElement('div'); tip.className = 'session-tooltip'; tip.id = 'lt-tip'; tip.style.cssText = 'display:block;left:40px;top:60px';
            tip.innerHTML = m.buildSessionTooltip({ name: 'lt', session_id: 'lt', agent_type: 'claude', model: 'claude-opus-4-1', thinking_actual: 'high', thinking: 'medium', summary: 'Work the queue', status: 'Reading', branch: 'main', staleness_seconds: 30, working_directory: '/work/x', token_usage: {} });
            document.body.append(tip); return true; })()`);
        for (const theme of ['dark', 'light']) {
            await setTheme(ev, theme);
            for (const [name, roots] of [['toasts', ['#warning-toasts']], ['hover card', ['#lt-tip']]]) {
                const au = await auditRegion(ev, roots);
                report.push({ theme, name, checked: au.checked, fails: au.fails });
                assert.ok(au.checked > 0, `${theme}: ${name}: the audit looked at something`);
            }
            await ev(`document.getElementById('lt-tip').style.left = '40px'; true`);
            await shot(`light-pass-${theme}`);
        }
        if (out) fs.writeFileSync(`${out}/report.json`, JSON.stringify(report, null, 1));
        for (const r of report) assert.deepEqual(r.fails, [], `${r.theme}: ${r.name} holds 4.5:1: ${JSON.stringify(r.fails).slice(0, 600)}`);
        console.log('   \u2713');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
