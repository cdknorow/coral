// The phone touch-target census, on this computer's own dashboard (#6130): at 390 px with touch, every visible control on each main screen is at least 44 x 44 px (helpers/phone_targets.js holds the measure and
// the reviewed allow list: a link inside running text is exempt, nothing else is). A stand-in agent and a team board give the screens real content; the Hub's screens are phone_targets_census_hub_browser's.
// PHONE_TARGETS_REPORT=<file> writes every scene's control count and the controls under 44 px to that file instead of failing.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { launchStandIn } = require('./helpers/stand_in_agent');
const { guardSettings } = require('./helpers/settings_guard');
const { STUB, HEX } = require('./helpers/comments_scale');
const { scene, tap: tapSel, problems, required } = require('./helpers/phone_targets');
const BASE = require('./test_base').BASE;
const TEAM = 'census-' + Date.now().toString(36);
const S = `(window.__coralStatic || '/static/')`;
// Data for the screens that are empty on a fresh server: a History chat, a workflow with a run, a scheduled job (the paths the dashboard's own suites stub).
const WF = { id: 7, name: 'Nightly build', description: 'Builds nightly', enabled: true, steps: [{ name: 's1', type: 'shell', command: 'echo hi' }] };
const RUN = { id: 31, workflow_id: 7, workflow_name: 'Nightly build', status: 'failed', trigger_type: 'manual', started_at: '2026-10-15T10:00:00Z', finished_at: '2026-10-15T10:01:00Z',
    steps: [{ index: 0, name: 's1', type: 'shell', status: 'failed', exit_code: 1, output_tail: 'boom' }] };
const JOB = { id: 5, name: 'Hourly job', cron_expr: '0 * * * *', enabled: true, agent_type: 'claude', prompt: 'p', repo_path: '/tmp', consecutive_failures: 0 };
const DATA_WORLD = `(() => {
    const inner = window.fetch;
    const json = b => Promise.resolve(new Response(JSON.stringify(b), { headers: { 'content-type': 'application/json' } }));
    window.fetch = (url, opts = {}) => {
        const u = String(url && url.url || url).replace(/^https?:\\/\\/[^/]+/, ''), path = u.split('?')[0], m = (opts.method || 'GET').toUpperCase();
        if (m === 'GET') {
            if (path === '/api/workflows') return json({ workflows: [${JSON.stringify(WF)}] });
            if (path === '/api/workflows/7') return json(${JSON.stringify(WF)});
            if (/^\\/api\\/workflows\\/7\\/runs/.test(path)) return json({ runs: [${JSON.stringify(RUN)}] });
            if (path === '/api/workflows/runs/31') return json(${JSON.stringify(RUN)});
            if (path === '/api/scheduled/jobs') return json({ jobs: [${JSON.stringify(JOB)}] });
            if (path === '/api/scheduled/jobs/5') return json(${JSON.stringify(JOB)});
            if (/^\\/api\\/scheduled\\/jobs\\/5\\/runs/.test(path)) return json({ runs: [] });
            if (path === '/api/sessions/history') return json({ sessions: [{ session_id: 'census-chat', summary_title: 'Release notes chat', source_type: 'claude' }], total: 1, page: 1, page_size: 50 });
            if (path === '/api/sessions/history/census-chat') return json({ agent_type: 'claude', messages: [{ type: 'user', content: 'hello' }, { type: 'assistant', text: 'Hi there.' }], total: 2, start: 0 });
            if (path === '/api/sessions/history/census-chat/notes') return json({ notes_md: 'A note' });
            if (path === '/api/sessions/history/census-chat/git') return json({ commits: [] });
            if (/^\\/api\\/sessions\\/history\\/census-chat\\/(tags|events|tasks|agent-notes|files)$/.test(path) || path === '/api/tags') return json([]);
        }
        return inner(url, opts);
    };
})();`;
async function httpJson(method, p, body) { const r = await fetch(BASE + p, { method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) }); let data = null; try { data = await r.json(); } catch { /* no body */ } return { status: r.status, data }; }

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const guard = await guardSettings(); let standIn = null;
    const results = [];
    const sc = (name, setup, opts) => scene(ev, results, name, setup, opts);
    const settleJob = () => new Promise(r => setTimeout(r, 800)); // sleep-ok: a selected job's detail draws
    const tap = sel => tapSel(ev, sel);
    const click = js => ev(`(${js}) ; true`);
    const load = async () => { await Page.navigate({ url: 'about:blank' }); await Page.loadEventFired(); await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install(); await ev(`window.__coralReady.then(() => true)`); };
    try {
        standIn = await launchStandIn(httpJson, { displayName: 'Census Agent' });
        await httpJson('POST', `/api/board/${TEAM}/subscribe`, { subscriber_id: 'Boss', job_title: 'Orchestrator', session_name: 'boss-session' });
        await httpJson('POST', `/api/board/${TEAM}/messages`, { subscriber_id: 'Boss', content: 'Please check the release notes.' });
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await Page.addScriptToEvaluateOnNewDocument({ source: DATA_WORLD });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true }); await Emulation.setTouchEmulationEnabled({ enabled: true });
        await load();
        await sc('home (agent list)', async () => { await ev(`window.switchNavTab('agents'); true`); }, { min: 5, expect: `document.getElementById('nav-select').textContent.includes('Agents')` });
        await sc('agent chat and composer', async () => {
            await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.selectLiveSession(${JSON.stringify(standIn.name)}, 'claude', ${JSON.stringify(standIn.sid)}, 'local'); true`);
            await waitFor(`!!document.querySelector('#command-input') && document.querySelector('.mobile-send-btn[data-on-click="cmd-send"]').getClientRects().length > 0`, 'composer', 20000);
            await ev(`(() => { const i = document.getElementById('command-input'); i.focus(); i.value = 'Is the build green?'; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
            await tap('.mobile-send-btn[data-on-click="cmd-send"]');
            await waitFor(`[...document.querySelectorAll('#live-history-messages .chat-bubble.human')].some(b => /build green/.test(b.textContent))`, 'my message', 15000);
        }, { min: 10, expect: `!!document.getElementById('command-input') && document.querySelectorAll('#live-history-messages .chat-bubble').length >= 1` });
        await sc('chat: find bar', async () => { await tap('#chat-find-btn'); await waitFor(`!document.querySelector('.chat-find-bar').hidden`, 'find bar', 5000); }, { min: 12, expect: `document.querySelector('.chat-find-input').getClientRects().length > 0` });
        await tap('.chat-find-close');
        await sc('terminal view', async () => { await tap('#live-view-btn-terminal'); }, { min: 8, expect: `document.getElementById('live-view-btn-terminal').getAttribute('aria-pressed') === 'true' || document.getElementById('live-view-btn-terminal').classList.contains('active')` });
        await tap('#live-view-btn-chat');
        await tap('#panel-toggle-btn'); await waitFor(`document.querySelector('.agentic-state-tabs').getClientRects().length > 0`, 'side panel', 8000);
        for (const tab of await ev(`[...document.querySelectorAll('.agentic-state-tabs .agentic-tab[data-tab]')].filter(e => e.getClientRects().length).map(e => e.dataset.tab)`)) await sc(`side panel: ${tab}`, async () => { await tap(`.agentic-state-tabs [data-tab="${tab}"]`); }, { min: 12, expect: `document.querySelector('.agentic-state-tabs [data-tab="${tab}"]').classList.contains('active')` });
        await sc('side panel: customize tabs', async () => { await tap('[aria-label="Customize tabs"]'); }, { min: 12, expect: `!!document.getElementById('tab-customize-title') && document.getElementById('tab-customize-title').getClientRects().length > 0` });
        await ev(`document.body.click(); true`);
        await sc('artifact, full screen', async () => {
            await ev(`(async () => { window.PP = await import(${S} + 'preview_pane.js'); return true; })()`);
            await ev(STUB({ filename: 'release-notes.md', content: '# Release notes\n\nThe build is green.\n', mediaType: 'text/markdown', comments: [{ id: 1, parent_id: null, anchor: { whole: true }, body: 'Please check.', author: { kind: 'owner', issuer: 'local', subject: 'Operator' }, created_at: '2026-10-22T00:00:00Z' }] }));
            await ev(`import(${S} + 'changed_files.js').then(m => m.openFilePreview('coral://artifacts/${HEX}')).then(() => true)`); await waitFor(`!!document.querySelector('#preview-toolbar .ac-toggle')`, 'artifact', 10000);
        }, { min: 5, expect: `document.getElementById('preview-pane').classList.contains('preview-phone-full') && /Release notes/.test(document.getElementById('preview-body').textContent)` });
        await sc('artifact: comments sheet', async () => { await tap('#preview-toolbar .ac-toggle'); }, { min: 8, expect: `document.querySelector('#preview-toolbar .ac-toggle').getAttribute('aria-expanded') === 'true' && /Please check/.test(document.querySelector('.ac-panel').textContent)` });
        await load();
        await sc('team settings: workflow', async () => { await ev(`import(${S} + 'team_working_mode.js').then(m => m.showTeamWorkingMode(${JSON.stringify(TEAM)}, { workspace: false })).then(() => true)`); await waitFor(`!!document.querySelector('dialog[open] [data-settings-tab=workflow]')`, 'team settings', 10000); }, { min: 8, expect: `!!document.querySelector('dialog[open] form.working-mode-form') && document.querySelector('dialog[open] [data-settings-tab=workflow]').getAttribute('aria-selected') !== 'false'` });
        for (const t of ['health', 'prompts']) await sc(`team settings: ${t}`, async () => { await tap(`dialog[open] [data-settings-tab=${t}]`); }, { min: 6, expect: `!!document.getElementById('team-settings-${t}') && !document.getElementById('team-settings-${t}').hidden` });
        await ev(`document.querySelectorAll('dialog[open]').forEach(d => d.close()); true`);
        await sc('history list', async () => { await ev(`window.switchNavTab('history'); true`); await waitFor(`document.getElementById('history-sessions-list').textContent.includes('Release notes chat')`, 'history', 10000); }, { min: 4, expect: `document.getElementById('nav-select').textContent.includes('Chats')` });
        await sc('history chat', async () => { await tap('#history-sessions-list [data-on-click="history-open"]'); await waitFor(`document.getElementById('history-messages').textContent.includes('Hi there')`, 'chat', 10000); }, { min: 10, expect: `document.getElementById('history-messages').getClientRects().length > 0` });
        for (const t of ['notes', 'activity', 'tasks', 'agent-notes', 'commits']) await sc(`history chat: ${t}`, async () => { await tap(`.history-tab-btn[data-tab="${t}"]`); }, { min: 10, expect: `document.querySelector('.history-tab-btn[data-tab="${t}"]').classList.contains('active')` });
        await tap('.mobile-history-back');
        await sc('board', async () => { await ev(`window.switchNavTab('board'); true`); }, { min: 2, expect: `document.getElementById('messageboard-view').getClientRects().length > 0` });
        await sc('board: a team board', async () => { await tap('#mb-projects-ul [data-project]'); await waitFor(`/Please check the release notes/.test(document.getElementById('mb-messages').textContent)`, 'board messages', 10000); }, { min: 8, expect: `/Please check the release notes/.test(document.getElementById('mb-messages').textContent)` });
        await sc('workflows list', async () => { await ev(`window.switchNavTab('workflows'); true`); await waitFor(`!!document.querySelector('#wf-list-container .wf-card[data-on-click="wf-select"]')`, 'workflows', 10000); }, { min: 4, expect: `/Nightly build/.test(document.getElementById('wf-list-container').textContent)` });
        await sc('workflow detail', async () => { await tap('.wf-card[data-on-click="wf-select"]'); await waitFor(`!!document.querySelector('#wf-detail-container tr[data-on-click="wf-run"]')`, 'runs', 10000); }, { min: 6, expect: `document.querySelector('#wf-detail-container tr[data-on-click="wf-run"]').getClientRects().length > 0` });
        await sc('workflow edit', async () => { await tap('[data-on-click="wf-edit"]'); await waitFor(`!!document.querySelector('#wf-create-modal [data-on-click="wf-add-step"]')`, 'editor', 10000); }, { min: 8, expect: `document.getElementById('wf-create-modal').getClientRects().length > 0 && !!document.querySelector('#wf-steps-editor .wf-step-editor-item')` });
        await ev(`document.querySelector('#wf-create-modal .modal-close').click(); true`); // (clean-up between scenes)
        await sc('workflow run detail', async () => { await tap('#wf-detail-container tr[data-on-click="wf-run"]'); await waitFor(`!!document.querySelector('#wf-run-detail-container [data-on-click="wf-output-toggle"]')`, 'run detail', 10000); }, { min: 6, expect: `/boom|failed/i.test(document.getElementById('wf-run-detail-container').textContent)` });
        await sc('jobs', async () => { await ev(`window.switchNavTab('jobs'); true`); }, { min: 3, expect: `document.getElementById('nav-select').textContent.includes('Jobs') && document.getElementById('scheduler-view').getClientRects().length > 0` });
        for (const [n, tab, label] of [['analytics', 'tokens', 'Analytics'], ['docs', 'docs', 'Docs'], ['inbox', 'hub-inbox', 'Inbox']]) await sc(n, async () => { await ev(`window.switchNavTab('${tab}'); true`); }, { min: 3, expect: `document.getElementById('nav-select').textContent.includes('${label}')` });
        await ev(`window.switchNavTab('agents'); true`);
        for (const g of ['general', 'agents', 'terminal', 'notifications', 'files', 'cleanup', 'network', 'security']) await sc(`settings: ${g}`, async () => { await ev(`(async () => { await window.showSettingsModal(); return true; })()`); await ev(`(() => { const p = document.getElementById('settings-group-select'); p.value = '${g}'; p.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`); }, { min: 8, expect: `document.querySelector('#settings-modal .settings-pane:not([hidden])').dataset.group === '${g}'` });
        await ev(`window.hideSettingsModal(); true`);
        await sc('launch: chooser', async () => { await ev(`(async () => { await window.showLaunchModal(); return true; })()`); }, { min: 5, expect: `document.querySelector('#launch-modal .modal-content').getClientRects().length > 0` });
        await sc('launch: new agent', async () => { await ev(`window._selectLaunchType('agent'); true`); }, { min: 5, expect: `/Working Directory/i.test(document.querySelector('#launch-modal .modal-content').textContent)` });
        await sc('launch: team', async () => { await ev(`window.showLaunchModal(); window._selectLaunchType('team'); true`); }, { min: 8, expect: `/team|role|member/i.test(document.querySelector('#launch-modal .modal-content').textContent) && document.querySelectorAll('#launch-modal input, #launch-modal select, #launch-modal textarea').length > 3` });
        await ev(`window.hideLaunchModal(); true`);
        await sc('create task', async () => { await ev(`(async () => { await window.showCreateTaskModal(); return true; })()`); }, { min: 8, expect: `document.getElementById('create-task-modal').getClientRects().length > 0 && /Create Task/.test(document.getElementById('create-task-modal').textContent)` });
        await ev(`window.hideCreateTaskModal(); true`);
        await sc('settings menu', async () => { await tap('[aria-controls="top-bar-settings-menu"]'); }, { min: 6, expect: `getComputedStyle(document.getElementById('top-bar-settings-menu')).display !== 'none'` });
        await ev(`document.body.click(); true`);
    } finally { await httpJson('DELETE', `/api/board/${TEAM}`).catch(() => {}); if (standIn) await standIn.stop().catch(() => {}); await guard.restore().catch(() => {}); await client.close(); }
    const bad = problems(results).concat(required(results, ['home (agent list)', 'agent chat and composer', 'chat: find bar', 'terminal view', 'side panel: files', 'side panel: browse', 'side panel: artifacts', 'side panel: customize tabs', 'artifact, full screen', 'artifact: comments sheet', 'team settings: workflow', 'history list', 'history chat', 'history chat: notes', 'history chat: commits', 'board: a team board', 'workflows list', 'workflow detail', 'workflow edit', 'workflow run detail', 'jobs', 'docs', 'settings: general', 'settings: agents', 'settings: terminal', 'settings: notifications', 'settings: files', 'settings: cleanup', 'settings: network', 'settings: security', 'launch: chooser', 'launch: new agent', 'launch: team', 'create task', 'settings menu']));
    if (process.env.PHONE_TARGETS_REPORT) { require('node:fs').writeFileSync(process.env.PHONE_TARGETS_REPORT, results.map(r => `${r.name}: ${r.error || r.n + ' controls, ' + r.small.length + ' small'}`).join('\n') + '\n--\n' + bad.join('\n') + '\n'); process.exit(0); }
    assert.deepEqual(bad, [], 'controls under 44 px on a phone:\n  ' + bad.join('\n  '));
    console.log(`PASS ${results.length} scenes: every control is at least 44 x 44 px`);
})().catch(e => { console.error(e); process.exit(1); });
