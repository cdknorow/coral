// The phone touch-target census, on this computer's own dashboard (#6130): at 390 px with touch, every visible control on each main screen is at least 44 x 44 px (helpers/phone_targets.js holds the measure and
// the reviewed allow list: a link inside running text is exempt, nothing else is). A stand-in agent and a team board give the screens real content; the Hub's screens are phone_targets_census_hub_browser's.
// PHONE_TARGETS_REPORT=<file> writes every scene's control count and the controls under 44 px to that file instead of failing.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { launchStandIn } = require('./helpers/stand_in_agent');
const { guardSettings } = require('./helpers/settings_guard');
const { STUB, HEX } = require('./helpers/comments_scale');
const { scene, problems } = require('./helpers/phone_targets');
const BASE = require('./test_base').BASE;
const TEAM = 'census-' + Date.now().toString(36);
const S = `(window.__coralStatic || '/static/')`;
async function httpJson(method, p, body) { const r = await fetch(BASE + p, { method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) }); let data = null; try { data = await r.json(); } catch { /* no body */ } return { status: r.status, data }; }

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const guard = await guardSettings(); let standIn = null;
    const results = [];
    const sc = (name, setup, opts) => scene(ev, results, name, setup, opts);
    const click = js => ev(`(${js}) ; true`);
    const load = async () => { await Page.navigate({ url: 'about:blank' }); await Page.loadEventFired(); await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install(); await ev(`window.__coralReady.then(() => true)`); };
    try {
        standIn = await launchStandIn(httpJson, { displayName: 'Census Agent' });
        await httpJson('POST', `/api/board/${TEAM}/subscribe`, { subscriber_id: 'Boss', job_title: 'Orchestrator', session_name: 'boss-session' });
        await httpJson('POST', `/api/board/${TEAM}/messages`, { from: 'Boss', content: 'Please check the release notes.' });
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true }); await Emulation.setTouchEmulationEnabled({ enabled: true });
        await load();
        await sc('home (agent list)', async () => { await ev(`window.switchNavTab('agents'); true`); }, { min: 5 });
        await sc('agent chat and composer', async () => { await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('bub', 'B', 4, { sleeping: false }); window.selectLiveSession('bub', 'claude', 'bub-id', 'local'); true`); await waitFor(`document.querySelectorAll('#live-history-messages .chat-bubble').length >= 2`, 'bubbles', 20000); }, { min: 10 });
        await sc('composer: model menu', async () => { await click(`(document.getElementById('model-chip') || {click(){}}).click()`); }, { min: 10 });
        await ev(`document.body.click(); true`);
        await sc('composer: send options', async () => { await click(`(document.querySelector('[data-on-click=cmd-send-menu]') || {click(){}}).click()`); }, { min: 10 });
        await ev(`document.body.click(); true`);
        await sc('chat: find bar', async () => { await click(`document.getElementById('chat-find-btn').click()`); await waitFor(`!document.querySelector('.chat-find-bar').hidden`, 'find bar', 5000); }, { min: 12 });
        await click(`document.querySelector('.chat-find-close').click()`);
        await sc('terminal view', async () => { await click(`document.getElementById('live-view-btn-terminal').click()`); }, { min: 8 });
        await click(`document.getElementById('live-view-btn-chat').click()`);
        await click(`document.getElementById('panel-toggle-btn').click()`); await waitFor(`document.querySelector('.agentic-state-tabs').getClientRects().length > 0`, 'side panel', 8000);
        for (const tab of ['files', 'browse', 'artifacts', 'tasks', 'notes', 'activity', 'agent-ui']) await sc(`side panel: ${tab}`, async () => { await click(`(document.querySelector('[data-tab="${tab}"], #agentic-tab-${tab}') || {click(){}}).click()`); }, { min: 12 });
        await sc('side panel: customize tabs', async () => { await click(`(document.querySelector('.agentic-tab-add, [aria-label="Customize tabs"]') || {click(){}}).click()`); }, { min: 12 });
        await ev(`document.body.click(); true`);
        await sc('artifact, full screen', async () => {
            await ev(`(async () => { window.PP = await import(${S} + 'preview_pane.js'); return true; })()`);
            await ev(STUB({ filename: 'release-notes.md', content: '# Release notes\n\nThe build is green.\n', mediaType: 'text/markdown', comments: [{ id: 1, parent_id: null, anchor: { whole: true }, body: 'Please check.', author: { kind: 'owner', issuer: 'local', subject: 'Operator' }, created_at: '2026-10-22T00:00:00Z' }] }));
            await ev(`import(${S} + 'changed_files.js').then(m => m.openFilePreview('coral://artifacts/${HEX}')).then(() => true)`); await waitFor(`!!document.querySelector('#preview-toolbar .ac-toggle')`, 'artifact', 10000);
        }, { min: 5 });
        await sc('artifact: comments sheet', async () => { await click(`document.querySelector('#preview-toolbar .ac-toggle').click()`); }, { min: 8 });
        await load();
        await sc('team settings: workflow', async () => { await ev(`import(${S} + 'team_working_mode.js').then(m => m.showTeamWorkingMode(${JSON.stringify(TEAM)}, { workspace: false })).then(() => true)`); await waitFor(`!!document.querySelector('dialog[open] [data-settings-tab=workflow]')`, 'team settings', 10000); }, { min: 8 });
        for (const t of ['health', 'prompts']) await sc(`team settings: ${t}`, async () => { await click(`document.querySelector('dialog[open] [data-settings-tab=${t}]').click()`); }, { min: 6 });
        await ev(`document.querySelectorAll('dialog[open]').forEach(d => d.close()); true`);
        await sc('history list', async () => { await ev(`window.switchNavTab('history'); true`); }, { min: 3 });
        await sc('board', async () => { await ev(`window.switchNavTab('board'); true`); }, { min: 2 });
        for (const [n, tab] of [['jobs', 'jobs'], ['workflows', 'workflows'], ['analytics', 'tokens'], ['docs', 'docs'], ['inbox', 'hub-inbox']]) await sc(n, async () => { await ev(`window.switchNavTab('${tab}'); true`); }, { min: 3 });
        await ev(`window.switchNavTab('agents'); true`);
        for (const g of ['general', 'agents', 'terminal', 'notifications', 'files', 'cleanup', 'network', 'security']) await sc(`settings: ${g}`, async () => { await ev(`(async () => { await window.showSettingsModal(); document.getElementById('settings-tab-${g}').click(); return true; })()`); }, { min: 8 });
        await ev(`window.hideSettingsModal(); true`);
        await sc('launch: chooser', async () => { await ev(`(async () => { await window.showLaunchModal(); return true; })()`); }, { min: 5 });
        await sc('launch: new agent', async () => { await ev(`window._selectLaunchType('agent'); true`); }, { min: 5 });
        await sc('launch: team', async () => { await ev(`window.showLaunchModal(); window._selectLaunchType('team'); true`); }, { min: 8 });
        await ev(`window.hideLaunchModal(); true`);
        await sc('create task', async () => { await ev(`(async () => { await window.showCreateTaskModal(); return true; })()`); }, { min: 8 });
        await ev(`window.hideCreateTaskModal(); true`);
        await sc('settings menu', async () => { await click(`document.querySelector('[aria-controls="top-bar-settings-menu"]').click()`); }, { min: 6 });
        await ev(`document.body.click(); true`);
    } finally { await httpJson('DELETE', `/api/board/${TEAM}`).catch(() => {}); if (standIn) await standIn.stop().catch(() => {}); await guard.restore().catch(() => {}); await client.close(); }
    const bad = problems(results);
    if (process.env.PHONE_TARGETS_REPORT) { require('node:fs').writeFileSync(process.env.PHONE_TARGETS_REPORT, results.map(r => `${r.name}: ${r.error || r.n + ' controls, ' + r.small.length + ' small'}`).join('\n') + '\n--\n' + bad.join('\n') + '\n'); process.exit(0); }
    assert.deepEqual(bad, [], 'controls under 44 px on a phone:\n  ' + bad.join('\n  '));
    console.log(`PASS ${results.length} scenes: every control is at least 44 x 44 px`);
})().catch(e => { console.error(e); process.exit(1); });
