// "Team health checks" (board_health_monitor, #5128) is a switch of the main Settings, in Agents, not of the team panel:
//   - Settings > Agents has it, labelled as the operator asked with its one sentence, on for a server that has no value, and Apply saves the key (true and false);
//   - Team settings has no Global settings tab and no such checkbox (it is Workflow, Prompt inspection and, from the server, Health check);
//   - the Health check tab says it needs the switch when it is off, with a Settings link that opens Settings at Agents; with the switch on it says nothing.
// Real isolated server (the settings are put back at the end); drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const { removeBoards } = require('./helpers/board_cleanup');

const TEAM = 'hm-5128-' + Date.now().toString(36);
const SENTENCE = 'Applies to every team; each team still turns its own check on in Team settings. Takes effect after Coral restarts.';

(async () => {
    const { Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const guard = await require('./helpers/settings_guard').guardSettings();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const theme = name => ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.clearCustomThemeVars && m.clearCustomThemeVars(); m.applyTheme(${JSON.stringify(name)}); return true; })()`);
    const put = body => ev(`fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: ${JSON.stringify(JSON.stringify(body))} }).then(r => r.status)`);
    const saved = () => ev(`fetch('/api/settings').then(r => r.json()).then(x => String(x.settings.board_health_monitor).toLowerCase())`);
    const load = async () => {
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await require('./helpers/wait').bind(ev).coralReady('startup', 'settings');
    };
    const openSettings = async () => {
        await ev(`(async () => { await window.showSettingsModal('agents'); return true; })()`);
        await waitFor(`getComputedStyle(document.getElementById('settings-modal')).display !== 'none' && !!document.getElementById('settings-board-health-monitor')`, 'Settings');
    };
    const openTeam = async () => {
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'team_working_mode.js'); m.showTeamWorkingMode(${JSON.stringify(TEAM)}); return true; })()`);
        await waitFor(`!!document.getElementById('team-settings-tab-health') && document.querySelector('.working-mode-form').dataset.initialState !== undefined`, 'Team settings with its Health check tab');
    };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await load();

        // Settings > Agents
        await put({ board_health_monitor: '' }); // no saved value: the default
        await load(); await openSettings();
        const row = await ev(`(() => { const box = document.getElementById('settings-board-health-monitor'); const rowEl = box.closest('.settings-row'); const dt = rowEl.querySelector('dt'); const pane = box.closest('.settings-pane');
            return { label: dt.querySelector('label').textContent.trim(), desc: rowEl.querySelector('.settings-info-text').textContent.trim(), info: dt.querySelector('.settings-info-btn').getAttribute('aria-label'), forId: dt.querySelector('label').getAttribute('for'), checked: box.checked, group: pane.dataset.group, shown: !pane.hidden }; })()`);
        check('Settings > Agents has "Team health checks" with the operator\'s sentence (behind its (i) button), its label bound to the box', row.group === 'agents' && row.shown && row.label === 'Team health checks' && row.desc === SENTENCE && row.info === 'About Team health checks' && row.forId === 'settings-board-health-monitor', JSON.stringify(row));
        check('it is on when the server has no value', row.checked === true);
        await ev(`(() => { const r = document.querySelector('#settings-board-health-monitor').closest('.settings-row'); r.querySelector('.settings-info-btn').click(); r.scrollIntoView({ block: 'center' }); return true; })()`);
        for (const th of ['dark', 'light']) { await theme(th); await new Promise(r => setTimeout(r, 350)); await shot(`health-monitor-settings-${th}`); } // sleep-ok: the theme's colour transitions
        await theme('dark');
        await ev(`document.getElementById('settings-board-health-monitor').click(); document.querySelector('[data-on-click="m-apply-settings"]').click(); true`);
        await waitFor(`!document.getElementById('settings-modal') || getComputedStyle(document.getElementById('settings-modal')).display === 'none'`, 'Settings applied');
        check('turning it off and Apply saves board_health_monitor false', (await saved()) === 'false', String(await saved()));
        await load(); await openSettings();
        check('reopened, it shows off', (await ev(`document.getElementById('settings-board-health-monitor').checked`)) === false);

        // The team panel, switch off
        await ev(`(() => { const d = document.getElementById('settings-modal'); return true; })()`);
        await load(); await openTeam();
        const off = await ev(`(() => { const d = document.querySelector('#team-working-mode-dialog') || document; const tabs = [...d.querySelectorAll('[data-settings-tab]')].map(t => t.textContent.trim());
            return { tabs, global: !!d.querySelector('[data-settings-tab=global]'), box: !!d.querySelector('input[name=board_health_monitor]'), legends: [...d.querySelectorAll('.team-settings-section legend')].map(l => l.textContent), note: (d.querySelector('.health-global-off') || {}).textContent || null }; })()`);
        check('Team settings has no Global settings tab and no board_health_monitor checkbox', !off.global && !off.box && !off.tabs.includes('Global settings') && !off.legends.includes('Global settings'), JSON.stringify([off.tabs, off.legends]));
        check('with the switch off the Health check tab says it needs the switch, naming it', /turned off for all of Coral/.test(off.note || '') && /Team health checks/.test(off.note) && /after Coral restarts/.test(off.note), off.note);
        await ev(`document.getElementById('team-settings-tab-health').click(); true`);
        const vis = await ev(`(() => { const n = document.querySelector('.health-global-off'); const r = n.getBoundingClientRect(); return { shown: r.width > 0 && r.height > 0 && !n.closest('[hidden]'), link: n.querySelector('button.working-mode-settings-link').textContent, form: !!document.querySelector('#team-settings-health form.health-form') }; })()`);
        check('the note is on the Health check tab, above its form, with a Settings link', vis.shown && vis.link === 'Settings' && vis.form, JSON.stringify(vis));
        for (const th of ['dark', 'light']) { await theme(th); await new Promise(r => setTimeout(r, 350)); await shot(`health-monitor-team-note-${th}`); } // sleep-ok: the theme's colour transitions
        await theme('dark');
        await ev(`document.querySelector('.health-global-off .working-mode-settings-link').click(); true`);
        await waitFor(`!!document.getElementById('settings-modal') && getComputedStyle(document.getElementById('settings-modal')).display !== 'none' && !document.getElementById('settings-pane-agents').hidden`, 'Settings at Agents');
        check('the Settings link opens Settings at Agents, with the team dialog closed', (await ev(`!(document.querySelector('#team-working-mode-dialog') && document.querySelector('#team-working-mode-dialog').open)`)) === true);

        // switch on: no note
        await put({ board_health_monitor: 'true' });
        await load(); await openTeam();
        check('with the switch on the Health check tab has no such note', (await ev(`!document.querySelector('.health-global-off')`)) === true);
    } catch (e) {
        console.error(e && e.stack || e);
        results.push(false);
    } finally {
        await guard.restore().catch(() => {});
        await removeBoards(ev, [TEAM]).catch(() => {});
        try { await Page.close(); } catch (_) {}
    }
    const failed = results.filter(r => !r).length;
    console.log(failed ? `${failed} FAILED` : `PASS health monitor setting: ${results.length} checks`);
    process.exitCode = failed ? 1 : 0;
})();
