// Settings is a wide dialog with its groups down the left, one pane at a time, and every explanation behind an (i) button (#4566).
// Checks: the 8 groups and where each control lives, the tab list's roles and arrow keys, the last group remembered, an (i) button
// that opens its text in place (aria-expanded, named, hidden by default), and the narrow layout (a selector instead of the list, rows
// stacked, 44 px targets) on a phone and at 200% text with a mouse. Drives Chrome over CDP through agent_view_harness.js against an
// isolated dev server; run_cdp_suites.mjs finds it by name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

const GROUPS = {
    general: ['settings-theme', 'settings-agent-type', 'settings-group-by-team', 'settings-show-summary', 'settings-show-scrollbars', 'settings-check-updates'], // Render engine moved to Terminal, About to Network and Hub (#7040)
    agents: ['settings-permission-mode', 'settings-working-dir', 'settings-cli-path-claude', 'settings-default-model-claude', 'settings-auto-resume', 'settings-auto-goals', 'settings-agent-low-priority', 'settings-board-auto-attach', 'settings-board-health-monitor'],
    terminal: ['settings-terminal-font-size', 'settings-terminal-scrollback', 'settings-fit-pane-width', 'settings-terminal-backend', 'settings-renderer-select'],
    notifications: ['settings-notify-needs-input', 'settings-desktop-notify'],
    files: ['settings-file-search-mode', 'settings-file-search-limit', 'settings-git-diff-mode', 'settings-git-poll-interval'],
    cleanup: ['settings-artifact-store-max', 'settings-received-max', 'settings-prune-days', 'settings-retention-days', 'settings-terminal-logs-delete'],
    network: ['settings-remote-access-enabled', 'settings-cloud', 'settings-hub-mode-enabled', 'app-version-label', 'machine-health-local'],
    security: ['settings-api-key', 'settings-security', 'settings-database-encryption', 'settings-telemetry-enabled', 'settings-debug-input-log'],
};

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const Input = client.Input;
    const open = async (group, w = 1280, h = 800, mobile = false) => {
        await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
        await Emulation.setTouchEmulationEnabled({ enabled: mobile });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await require('./helpers/wait').bind(ev).coralReady('startup', 'settings');
        await ev(`(async () => { await window.showSettingsModal(${group ? JSON.stringify(group) : ''}); return true; })()`);
        await waitFor(`getComputedStyle(document.getElementById('settings-modal')).display !== 'none'`, 'Settings');
    };
    const shownGroup = () => ev(`[...document.querySelectorAll('#settings-modal .settings-pane')].filter(p => !p.hidden).map(p => p.dataset.group)`);
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });

        console.log('1. Eight groups; each control is in its own group and none is lost');
        await open('cleanup');
        assert.deepEqual(await shownGroup(), ['cleanup'], 'the group asked for is the one shown');
        await waitFor(`document.activeElement && document.activeElement.id === 'settings-tab-cleanup'`, "focus on the open group's tab, not the first tab");
        const tabs = await ev(`[...document.querySelectorAll('#settings-tabs [role=tab]')].map(t => ({ id: t.id, name: t.querySelector('.settings-tab-name').textContent.trim(), sel: t.getAttribute('aria-selected'), ctl: t.getAttribute('aria-controls'), ti: t.tabIndex }))`);
        assert.deepEqual(tabs.map(t => t.name), ['General', 'Agents', 'Terminal', 'Notifications', 'Files and Git', 'Storage and cleanup', 'Network and Hub', 'Security and privacy']);
        assert.deepEqual(tabs.filter(t => t.sel === 'true').map(t => t.id), ['settings-tab-cleanup'], 'one tab selected');
        assert.deepEqual(tabs.filter(t => t.ti === 0).map(t => t.id), ['settings-tab-cleanup'], 'only the selected tab is in the Tab order');
        for (const [g, ids] of Object.entries(GROUPS)) {
            const where = await ev(`${JSON.stringify(ids)}.map(id => { const e = document.getElementById(id); const p = e && e.closest('.settings-pane'); return p ? p.dataset.group : null; })`);
            assert.deepEqual(where, ids.map(() => g), `${g}: every control sits in its pane ${JSON.stringify(where)}`);
        }
        const names = await ev(`[...document.querySelectorAll('#settings-modal .settings-pane')].map(p => document.getElementById(p.getAttribute('aria-labelledby')) ? p.dataset.group : 'unlabelled:' + p.dataset.group)`);
        assert.equal(names.length, 8); assert.ok(names.every(n => !n.startsWith('unlabelled')), 'each pane is named by its tab: ' + names);
        console.log('   ✓ 8 groups, controls where they belong');

        console.log('2. The tab list: click, arrow keys, Home and End; the last group is remembered');
        await ev(`document.getElementById('settings-tab-terminal').click(); true`);
        assert.deepEqual(await shownGroup(), ['terminal']);
        await ev(`document.getElementById('settings-tab-terminal').focus(); true`);
        const key = async k => { await Input.dispatchKeyEvent({ type: 'keyDown', key: k, code: k }); await Input.dispatchKeyEvent({ type: 'keyUp', key: k, code: k }); };
        await key('ArrowDown'); assert.deepEqual(await shownGroup(), ['notifications'], 'ArrowDown');
        assert.equal(await ev(`document.activeElement.id`), 'settings-tab-notifications', 'focus follows');
        await key('End'); assert.deepEqual(await shownGroup(), ['security'], 'End');
        await key('ArrowDown'); assert.deepEqual(await shownGroup(), ['general'], 'ArrowDown wraps');
        await key('ArrowUp'); assert.deepEqual(await shownGroup(), ['security'], 'ArrowUp wraps');
        await key('Home'); assert.deepEqual(await shownGroup(), ['general'], 'Home');
        await ev(`document.getElementById('settings-tab-files').click(); window.hideSettingsModal(); window.showSettingsModal(); true`);
        await waitFor(`getComputedStyle(document.getElementById('settings-modal')).display !== 'none'`, 'Settings again');
        assert.deepEqual(await shownGroup(), ['files'], 'Settings reopens on the group last used');
        console.log('   ✓ keys and memory');

        console.log('3. Explanations are behind (i) buttons: hidden, named, one tap opens them in place');
        await ev(`document.getElementById('settings-tab-cleanup').click(); true`);
        const info = await ev(`(() => { const b = document.querySelector('#settings-pane-cleanup .settings-info-btn'); const t = document.getElementById(b.getAttribute('aria-controls')); return { name: b.getAttribute('aria-label'), exp: b.getAttribute('aria-expanded'), hidden: t.hidden, text: t.textContent.length, vis: t.getClientRects().length }; })()`);
        assert.equal(info.exp, 'false'); assert.equal(info.hidden, true); assert.equal(info.vis, 0); assert.ok(info.text > 40, 'the explanation is there');
        assert.match(info.name, /^About /, 'the button is named after its setting: ' + info.name);
        await ev(`document.querySelector('#settings-pane-cleanup .settings-info-btn').click(); true`);
        const opened = await ev(`(() => { const b = document.querySelector('#settings-pane-cleanup .settings-info-btn'); const t = document.getElementById(b.getAttribute('aria-controls')); return { exp: b.getAttribute('aria-expanded'), vis: t.getClientRects().length > 0 }; })()`);
        assert.deepEqual(opened, { exp: 'true', vis: true });
        await ev(`document.querySelector('#settings-pane-cleanup .settings-info-btn').click(); true`);
        assert.equal(await ev(`document.querySelector('#settings-pane-cleanup .settings-info-btn').getAttribute('aria-expanded')`), 'false', 'a second tap closes it');
        const loose = await ev(`[...document.querySelectorAll('#settings-modal dt .toggle-desc')].length`);
        assert.equal(loose, 0, 'no explanation left in the open under a label');
        const unnamed = await ev(`[...document.querySelectorAll('#settings-modal .settings-info-btn')].filter(b => !b.getAttribute('aria-label')).length`);
        assert.equal(unnamed, 0);
        console.log('   ✓ (i) buttons');

        console.log('4. Wide: the list is on the left, no selector; rows are label and control side by side');
        const wide = await ev(`(() => { const r = e => e.getBoundingClientRect(); const m = r(document.querySelector('#settings-modal .settings-modal-content')); const nav = r(document.getElementById('settings-tabs')); const row = document.querySelector('#settings-pane-cleanup .settings-row'); const dt = r(row.querySelector('dt')), dd = r(row.querySelector('dd'));
            return { w: Math.round(m.width), tabs: nav.width > 0, pick: document.querySelector('#settings-modal .settings-group-pick').getClientRects().length, sideBySide: dd.left > dt.right - 2 && Math.abs(dd.top - dt.top) < 40, over: document.documentElement.scrollWidth - document.documentElement.clientWidth }; })()`);
        assert.ok(wide.w >= 950, 'the dialog is wide (the one dialog size rule: 960 at most): ' + wide.w); assert.equal(wide.tabs, true); assert.equal(wide.pick, 0); assert.equal(wide.sideBySide, true); assert.equal(wide.over, 0);
        console.log(`   ✓ ${wide.w} px dialog`);

        for (const [label, w, h, mobile, font] of [['phone 390 px', 390, 844, true, ''], ['phone 390 px at 200% text', 390, 844, true, '32px'], ['desktop at 200% text', 1280, 800, false, '32px']]) {
            console.log(`5. ${label}: a selector replaces the list, rows stack, targets are 44 px, nothing overflows`);
            await open('agents', w, h, mobile);
            if (font) { await ev(`document.documentElement.style.fontSize = ${JSON.stringify(font)}; true`); await waitFor(`document.querySelector('#settings-modal .settings-group-pick').getClientRects().length > 0`, 'the selector'); await waitFor(`document.querySelector('#settings-pane-agents .settings-info-btn').getBoundingClientRect().height >= 44`, 'buttons to finish their size transition'); }
            const m = await ev(`(() => { const r = e => e.getBoundingClientRect(); const row = document.querySelector('#settings-pane-agents .settings-row'); const dt = r(row.querySelector('dt')), dd = r(row.querySelector('dd')); const pick = document.getElementById('settings-group-select'); const info = document.querySelector('#settings-pane-agents .settings-info-btn');
                return { tabsShown: document.getElementById('settings-tabs').getClientRects().length, pickH: Math.round(r(pick).height), stacked: dd.top >= dt.bottom - 2, infoH: info ? Math.round(r(info).height) : 0, over: document.documentElement.scrollWidth - document.documentElement.clientWidth, modalInside: r(document.querySelector('#settings-modal .settings-modal-content')).right <= innerWidth + 1 }; })()`);
            assert.equal(m.tabsShown, 0, 'the list is gone'); assert.ok(m.pickH >= 44, 'selector height ' + m.pickH); assert.equal(m.stacked, true, 'label above its control'); assert.ok(m.infoH <= 24.5 * (font ? parseInt(font) / 16 : 1), 'info button is the 24 px circle, its 44 px target is the ::after (#6750) ' + m.infoH);
            assert.equal(m.over, 0, 'no sideways scroll'); assert.equal(m.modalInside, true);
            await ev(`(() => { const p = document.getElementById('settings-group-select'); p.value = 'notifications'; p.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
            assert.deepEqual(await shownGroup(), ['notifications'], 'the selector changes the group');
            const small = await ev(`[...document.querySelectorAll('#settings-pane-notifications select, #settings-pane-notifications input, #settings-pane-notifications button, #settings-pane-notifications .check-hit')].filter(e => e.getClientRects().length && !e.classList.contains('settings-info-btn')).map(e => Math.round((e.closest('label') || e).getBoundingClientRect().height)).filter(h => h < 44)`); // (the (i) is 24 px with a 44 px ::after target, #6750; info_buttons_browser checks it)
            assert.deepEqual(small, [], 'every control in a pane is 44 px tall: ' + small);
            console.log('   ✓');
        }

        console.log('6. On the Coral Hub dashboard only the groups with a per-user setting are listed');
        await open('general');
        // A hub page never loads the machine-only rows that fill themselves (browser sign-in, terminal backend); this local page did. They fill from fetches that answer when they answer (loadSecurityRow
        // shows its row after /api/system/auth-status): wait for each to have filled, THEN hide it, or a slow fill (a throttled CPU) shows the row again after the hiding and its tab joins the list (#5602).
        for (const row of ['settings-security', 'settings-terminal-backend']) await waitFor(`!document.getElementById('${row}').hidden`, `the ${row} row has filled`, 10000).catch(() => {}); // a row that never fills (nothing to show here) is hidden already
        await ev(`document.getElementById('settings-security').hidden = true; document.getElementById('settings-terminal-backend').hidden = true; true`);
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'cloud_settings.js'); m.applyCloudSettingsDialog(); return true; })()`);
        const listed = await ev(`[...document.querySelectorAll('#settings-tabs [role=tab]')].filter(t => !t.hidden).map(t => t.id.replace('settings-tab-', ''))`);
        assert.deepEqual(listed, ['general', 'agents', 'terminal', 'notifications', 'files'], 'groups with no hub setting leave the list: ' + listed);
        const opts = await ev(`[...document.querySelectorAll('#settings-group-select option')].filter(o => !o.hidden).map(o => o.value)`);
        assert.deepEqual(opts, listed, 'and the phone selector');
        console.log('   ✓ ' + listed.join(', '));

        console.log('\nAll Settings group checks passed');
    } finally { await client.close(); }
})().catch(err => { console.error(err); process.exit(1); });
