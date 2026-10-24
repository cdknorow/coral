// Settings layout (#7040, UI/UX's #7036 audit): Agents has four headed parts (Defaults, Behaviour, Team and board, Tools); the render
// engine is a Terminal setting, Customize Theme sits in the Theme row, About and Connect to Coral Hub are in Network and Hub, "Log terminal
// input" is in Security and privacy, Notifications' first row says what it does, Network and Hub keeps one row order at every width, and
// the needless lines are gone. Drives Chrome over CDP through agent_view_harness.js against an isolated dev server; run_cdp_suites.mjs finds it by name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

const AGENT_PARTS = {
    'settings-agents-defaults': ['settings-permission-mode', 'settings-working-dir', 'settings-default-model-claude'],
    'settings-agents-behaviour': ['settings-auto-resume', 'settings-auto-goals', 'settings-agent-low-priority'],
    'settings-agents-team': ['settings-board-auto-attach'],
    'settings-agents-tools': ['settings-cli-path-claude'],
};

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const open = async (group, w = 1280, h = 800) => {
        await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await require('./helpers/wait').bind(ev).coralReady('startup', 'settings');
        await ev(`(async () => { await window.showSettingsModal(${JSON.stringify(group)}); return true; })()`);
        await waitFor(`getComputedStyle(document.getElementById('settings-modal')).display !== 'none'`, 'Settings');
    };
    const paneOf = id => ev(`(() => { const e = document.getElementById(${JSON.stringify(id)}); const p = e && e.closest('.settings-pane'); return p ? p.dataset.group : null; })()`);
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });

        console.log('1. Agents: four headed parts, each switch under its own heading');
        await open('agents');
        const heads = await ev(`[...document.querySelectorAll('#settings-pane-agents .settings-subhead')].map(h => h.id + '=' + h.textContent.trim())`);
        assert.deepEqual(heads, ['settings-agents-defaults=Defaults', 'settings-agents-behaviour=Behaviour', 'settings-agents-team=Team and board', 'settings-agents-tools=Tools']);
        for (const [head, ids] of Object.entries(AGENT_PARTS)) {
            const under = await ev(`${JSON.stringify(ids)}.map(id => { const e = document.getElementById(id); const s = e && e.closest('.settings-subsection'); return s ? s.getAttribute('aria-labelledby') : null; })`);
            assert.deepEqual(under, ids.map(() => head), `${head}: ${JSON.stringify(under)}`);
        }
        console.log('2. Terminal has the render engine; General has Customize Theme beside Theme and no About');
        assert.equal(await paneOf('settings-renderer-select'), 'terminal');
        await open('general');
        assert.equal(await ev(`(() => { const b = document.querySelector('.settings-theme-btn'); return !!b && b.closest('.settings-pane').dataset.group === 'general' && b.closest('dd') === document.getElementById('settings-theme').closest('dd'); })()`), true, 'Customize Theme shares the Theme row');
        assert.equal(await paneOf('about-section'), 'network', 'About moved to Network and Hub');
        assert.equal(await ev(`!document.querySelector('#settings-pane-general #about-section')`), true);
        console.log('3. Network and Hub keeps one row order at 1280 and at 390');
        for (const w of [1280, 390]) {
            await open('network', w, 800);
            const moved = await ev(`[...document.querySelectorAll('#settings-pane-network *')].filter(e => { const o = getComputedStyle(e).order; return o !== '0'; }).map(e => e.id || e.className)`);
            assert.deepEqual(moved, [], `at ${w}px no row is re-ordered: ${JSON.stringify(moved)}`);
        }
        console.log('4. Security has Log terminal input; Notifications first row; the needless lines are gone');
        assert.equal(await paneOf('settings-debug-input-log'), 'security');
        await open('security');
        assert.equal(await ev(`/Keep secret\\./.test(document.getElementById('settings-pane-security').textContent)`), false, '"Keep secret." is gone');
        assert.equal(await ev(`/Sign in here to manage browsers/.test(document.getElementById('settings-pane-security').textContent)`), false);
        assert.equal(await ev(`(() => { const h = document.getElementById('settings-telemetry-install-hint'); return !h || h.hidden || getComputedStyle(h).display === 'none' || !document.getElementById('settings-telemetry-install-id').textContent.includes('Loading') ; })()`), true, 'no deletion hint without an install ID');
        await open('notifications');
        assert.equal(await ev(`document.querySelector('#settings-pane-notifications label[for=settings-notify-needs-input], #settings-pane-notifications dt').textContent.trim().split('\\n')[0]`), 'Show notifications in Coral');
        console.log('PASS Settings layout');
    } finally { await client.close(); }
})().catch(err => { console.error(err); process.exit(1); });
