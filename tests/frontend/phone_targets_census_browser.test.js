// The phone touch-target census, on this computer's own dashboard (#6130): at 390 px with touch, every visible control on each main screen is at least 44 x 44 px (helpers/phone_targets.js holds the measure and
// the reviewed allow list: a link inside running text is exempt, nothing else is). A stand-in agent and a team board give the screens real content; the Hub's screens are phone_targets_census_hub_browser's.
// PHONE_TARGETS_REPORT=<file> writes every scene's control count and the controls under 44 px to that file instead of failing.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { launchStandIn } = require('./helpers/stand_in_agent');
const { guardSettings } = require('./helpers/settings_guard');
const { STUB, HEX } = require('./helpers/comments_scale');
const { scene, tap: tapSel, problems, required, useScaleShot, openBoardComposer, BOARD_COMPOSER_UP } = require('./helpers/phone_targets');
const BASE = require('./test_base').BASE;
const TEAM = 'census-' + Date.now().toString(36);
const S = `(window.__coralStatic || '/static/')`;
const { DATA_WORLD, httpJson } = require('./helpers/census_world');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    if (process.env.SCALE_SHOT_DIR) useScaleShot(async name => require('node:fs').writeFileSync(`${process.env.SCALE_SHOT_DIR}/${name.replace(/[^a-z0-9]+/gi, '-')}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'))); // SCALE_SHOT_DIR=<dir>: a picture of each scene with 200% findings
    const guard = await guardSettings(); let standIn = null;
    const results = [];
    const sc = (name, setup, opts) => scene(ev, results, name, setup, opts);
    const settleJob = () => new Promise(r => setTimeout(r, 800)); // sleep-ok: a selected job's detail draws
    const tap = sel => tapSel(ev, sel);
    const click = js => ev(`(${js}) ; true`);
    const load = async () => { await Page.navigate({ url: 'about:blank' }); await Page.loadEventFired(); await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install(); await ev(`window.__coralReady.startup('liveList', 'liveListLoaded', 'settings')`); };
    try {
        standIn = await launchStandIn(httpJson, { displayName: 'Census Agent' });
        await httpJson('POST', `/api/board/${TEAM}/subscribe`, { subscriber_id: 'Boss', job_title: 'Orchestrator', session_name: 'boss-session' });
        await httpJson('POST', `/api/board/${TEAM}/messages`, { subscriber_id: 'Boss', content: 'Please check the release notes.' });
        await httpJson('POST', `/api/board/${TEAM}/messages`, { subscriber_id: 'Boss', content: 'Then post the summary.' });
        await httpJson('POST', `/api/board/${TEAM}/messages`, { subscriber_id: 'Boss', content: 'And tell me when it is done.' });
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await Page.addScriptToEvaluateOnNewDocument({ source: DATA_WORLD });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true }); await Emulation.setTouchEmulationEnabled({ enabled: true });
        await load();
        await sc('home (agent list)', async () => { await ev(`window.__m.add('rowa', 'Row A', 2, {}); window.__m.add('rowb', 'Row B', 2, {}); window.__m.add('rowc', 'Row C', 2, {}); window.switchNavTab('agents'); Promise.resolve(window._coralLoadLiveSessions && window._coralLoadLiveSessions()).then(() => true)`); await waitFor(`document.querySelectorAll('#mobile-agent-list .sidebar-kebab-btn').length >= 3`, 'three agent rows', 10000); }, { min: 5, expect: `document.getElementById('nav-select').textContent.includes('Agents')` });
        await sc('agent chat and composer', async () => {
            await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.selectLiveSession(${JSON.stringify(standIn.name)}, 'claude', ${JSON.stringify(standIn.sid)}, 'local'); true`);
            await waitFor(`!!document.querySelector('#command-input') && document.querySelector('.mobile-send-btn[data-on-click="cmd-send"]').getClientRects().length > 0`, 'composer', 20000);
            await ev(`(() => { const i = document.getElementById('command-input'); i.focus(); i.value = 'Is the build green?'; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
            await tap('.mobile-send-btn[data-on-click="cmd-send"]');
            await waitFor(`[...document.querySelectorAll('#live-history-messages .chat-bubble.human')].some(b => /build green/.test(b.textContent))`, 'my message', 15000);
        }, { min: 10, expect: `!!document.getElementById('command-input') && document.querySelectorAll('#live-history-messages .chat-bubble').length >= 1` });
        // The chip is drawn from the agent's row in the page's live list, and this harness serves that list from its mocked agents (agent_view_harness PRELOAD), of which there are none here (the agent is a real
        // stand-in): the page's list is empty, so the chip only drew by chance. The row is handed to the chip directly, so the chip and its menu are the real ones.
        await sc('composer: model menu', async () => {
            await ev(`import(${S} + 'model_chip.js').then(m => m.updateModelChip({ session_id: ${JSON.stringify(standIn.sid)}, name: ${JSON.stringify(standIn.name)}, agent_type: 'claude', model: 'claude-sonnet-5-5', sleeping: false, archived: false }, 'local')).then(() => true)`);
            await waitFor(`document.getElementById('model-chip').getClientRects().length > 0`, 'the model chip', 10000);
            await tap('#model-chip');
        }, { min: 10, expect: `!document.getElementById('model-chip-menu').hidden` });
        await ev(`document.body.click(); true`);
        await sc('chat: find bar', async () => { await tap('#chat-find-btn'); await waitFor(`!document.querySelector('.chat-find-bar').hidden`, 'find bar', 5000); }, { min: 12, expect: `document.querySelector('.chat-find-input').getClientRects().length > 0` });
        await tap('.chat-find-close');
        await sc('terminal view', async () => { await tap('#live-view-btn-terminal'); }, { min: 8, expect: `document.getElementById('live-view-btn-terminal').getAttribute('aria-pressed') === 'true' || document.getElementById('live-view-btn-terminal').classList.contains('active')` });
        await tap('#live-view-btn-chat');
        await tap('#panel-toggle-btn'); await waitFor(`document.querySelector('.agentic-state-tabs').getClientRects().length > 0`, 'side panel', 8000);
        for (const tab of await ev(`[...document.querySelectorAll('.agentic-state-tabs .agentic-tab[data-tab]')].filter(e => e.getClientRects().length).map(e => e.dataset.tab)`)) await sc(`side panel: ${tab}`, async () => { await tap(`.agentic-state-tabs [data-tab="${tab}"]`); }, { min: 12, expect: `document.querySelector('.agentic-state-tabs [data-tab="${tab}"]').classList.contains('active')` });
        await sc('side panel: customize tabs', async () => { await tap('[aria-label="Customize tabs"]'); }, { min: 12, expect: `!!document.getElementById('tab-customize-title') && document.getElementById('tab-customize-title').getClientRects().length > 0` });
        await ev(`document.body.click(); true`);
        await sc('side panel: board composer', async () => {
            await ev(`window.__m.add('board-agent', 'B', 3, { sleeping: false, board_project: ${JSON.stringify(TEAM)} }); window.selectLiveSession('board-agent', 'claude', 'board-agent-id', 'local'); true`);
            await new Promise(r => setTimeout(r, 600)); // sleep-ok: the phone closes its side panel when another agent is picked
            if (!(await ev(`!!document.querySelector('.agentic-state-tabs') && document.querySelector('.agentic-state-tabs').getClientRects().length > 0`))) await tap('#panel-toggle-btn');
            await openBoardComposer(ev, waitFor, TEAM, false);
        }, { min: 5, expect: BOARD_COMPOSER_UP }); // the team's message box (#6239)
        await ev(`window.selectLiveSession(${JSON.stringify(standIn.name)}, 'claude', ${JSON.stringify(standIn.sid)}, 'local'); true`);
        await new Promise(r => setTimeout(r, 600)); // sleep-ok: the stand-in's chat draws again
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
        // the page's search: the results with their excerpts, the advanced filters, a search that finds nothing, and opening one hit (#6288)
        const histType = text => ev(`(() => { const i = document.getElementById('history-search'); i.focus(); i.value = ${JSON.stringify(text)}; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
        await sc('history: search results', async () => { await histType('release'); await waitFor(`document.querySelectorAll('#chat-search-results .chat-search-hit').length === 3`, 'search hits', 10000); }, { min: 4, expect: `document.querySelectorAll('#chat-search-results .chat-search-hit').length === 3` });
        await sc('history: search, advanced filters', async () => { await tap('#hf-adv-toggle'); await waitFor(`document.getElementById('hf-advanced').style.display !== 'none'`, 'advanced filters', 5000); }, { min: 8, expect: `document.getElementById('hf-advanced').style.display !== 'none'` });
        await tap('#hf-adv-toggle');
        await sc('history: search finds nothing', async () => { await histType('zzz'); await waitFor(`/No matching chats/.test(document.getElementById('chat-search-results').textContent)`, 'no results', 10000); }, { min: 3, expect: `/No matching chats/.test(document.getElementById('chat-search-results').textContent)` });
        await histType('release'); await waitFor(`document.querySelectorAll('#chat-search-results .chat-search-hit').length === 3`, 'search hits again', 10000);
        await sc('history: a search hit is opened', async () => { await tap('#chat-search-results .chat-search-hit'); await waitFor(`document.getElementById('history-messages').textContent.includes('Hi there')`, 'the chat', 10000); }, { min: 5, expect: `document.getElementById('history-messages').getClientRects().length > 0` });
        await tap('.mobile-history-back');
        await histType(''); await waitFor(`!document.getElementById('history-sessions-list').hidden`, 'the chat list', 10000);
        await sc('history chat', async () => { await tap('#history-sessions-list [data-on-click="history-open"]'); await waitFor(`document.getElementById('history-messages').textContent.includes('Hi there')`, 'chat', 10000); }, { min: 10, expect: `document.getElementById('history-messages').getClientRects().length > 0` });
        for (const t of ['notes', 'activity', 'tasks', 'agent-notes', 'commits']) await sc(`history chat: ${t}`, async () => { await tap(`.history-tab-btn[data-tab="${t}"]`); }, { min: 10, expect: `document.querySelector('.history-tab-btn[data-tab="${t}"]').classList.contains('active')` });
        await tap('.mobile-history-back');
        await sc('board', async () => { await ev(`window.switchNavTab('board'); true`); }, { min: 2, expect: `document.getElementById('messageboard-view').getClientRects().length > 0` });
        await sc('board: a team board', async () => { await tap('#mb-projects-ul [data-project]'); await waitFor(`/Please check the release notes/.test(document.getElementById('mb-messages').textContent)`, 'board messages', 10000); }, { min: 8, expect: `/Please check the release notes/.test(document.getElementById('mb-messages').textContent)` });
        await sc('workflows list', async () => { await ev(`window.switchNavTab('workflows'); true`); await waitFor(`!!document.querySelector('#wf-list-container .wf-card[data-on-click="wf-select"]')`, 'workflows', 10000); }, { min: 4, expect: `/Nightly build/.test(document.getElementById('wf-list-container').textContent)` });
        await sc('workflow detail', async () => { await tap('.wf-card[data-on-click="wf-select"]'); await waitFor(`!!document.querySelector('#wf-detail-container tr[data-on-click="wf-run"]')`, 'runs', 10000); }, { min: 6, expect: `document.querySelector('#wf-detail-container tr[data-on-click="wf-run"]').getClientRects().length > 0` });
        await sc('workflow edit', async () => { await tap('[data-on-click="wf-edit"]'); await waitFor(`!!document.querySelector('#wf-create-modal [data-on-click="wf-add-step"]')`, 'editor', 10000); }, { min: 8, expect: `document.getElementById('wf-create-modal').getClientRects().length > 0 && !!document.querySelector('#wf-steps-editor .wf-step-editor-item')` });
        await ev(`document.querySelector('#wf-create-modal .modal-close-btn').click(); true`); // (clean-up between scenes)
        await sc('workflow run detail', async () => { await tap('#wf-detail-container tr[data-on-click="wf-run"]'); await waitFor(`!!document.querySelector('#wf-run-detail-container [data-on-click="wf-output-toggle"]')`, 'run detail', 10000); }, { min: 6, expect: `/boom|failed/i.test(document.getElementById('wf-run-detail-container').textContent)` });
        await sc('jobs list', async () => { await ev(`window.switchNavTab('jobs'); true`); await tap('[data-subtab="scheduled"]'); await waitFor(`!!document.querySelector('#scheduled-jobs-list [data-on-click="job-select"]') && document.querySelector('#scheduled-jobs-list [data-on-click="job-select"]').getClientRects().length > 0`, 'jobs', 10000); }, { min: 5, expect: `!!document.querySelector('#mobile-jobs-list [data-on-click="job-select"]') && /Hourly job/.test(document.getElementById('mobile-jobs-list').textContent)` });
        await sc('job detail', async () => { await tap('#scheduled-jobs-list [data-on-click="job-select"]'); await waitFor(`!!document.querySelector('#scheduler-detail [data-on-click="job-edit"]')`, 'detail', 10000); }, { min: 10, expect: `/Hourly job/.test(document.getElementById('scheduler-detail').textContent) && document.querySelector('#scheduler-detail [data-on-click="job-edit"]').getClientRects().length > 0` });
        await sc('job edit', async () => { await tap('#scheduler-detail [data-on-click="job-edit"]'); }, { min: 10, expect: `[...document.querySelectorAll('.modal, dialog')].some(m => m.getClientRects().length > 0 && /cron|schedule/i.test(m.textContent))` });
        await ev(`document.querySelectorAll('.modal.show, dialog[open]').forEach(m => { m.classList.remove('show'); if (m.close) m.close(); }); true`);
        await load(); // a fresh page: nothing of the editors above is left open under the dialogs below
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
        // Message bubbles as the chat draws them (a person's, an agent's with markdown: code, a link, a list) and the toasts a person meets: their colours are part of the contrast pass (#6171).
        const BUBBLES = [{ type: 'user', content: 'Is the build green?', key: 'c1', timestamp: '2026-10-19T00:00:00Z' }, { type: 'assistant', text: 'Yes. **All 12 checks pass.**\n\n- the unit tests\n- the lint\n\n`go test ./...` is clean, see [the run](https://example.com/run/1).\n\n```sh\ngo build ./...\n```', key: 'c2', timestamp: '2026-10-19T00:00:05Z' }];
        await sc('chat bubbles', async () => {
            await ev(`import(${S} + 'live_chat.js').then(m => { document.getElementById('census-box')?.remove(); const box = document.createElement('div'); box.id = 'census-box'; box.className = 'chat-messages chat-prose';
                box.style.cssText = 'position:fixed;left:0;top:0;right:0;bottom:0;overflow:auto;background:var(--bg-primary);z-index:99999;padding:12px'; document.body.appendChild(box);
                m.renderTranscript(${JSON.stringify(BUBBLES)}, box, 'claude'); return true; })`);
            // the dashboard names each Save by its message when it syncs a watched chat; this box is not one of those
            await ev(`import(${S} + 'saved_marks.js').then(m => { document.querySelectorAll('#census-box .chat-save-btn').forEach(b => m.nameSaveButton(b)); return true; })`);
        }, { min: 1, expect: `document.querySelectorAll('#census-box .chat-bubble').length >= 2` });
        await ev(`document.getElementById('census-box')?.remove(); true`);
        await sc('toasts', async () => {
            await ev(`import(${S} + 'utils.js').then(m => { m.showToast('Saved to the board.', false, 60000); m.showToast('Could not reach the agent.', true, 60000); m.showToast('Upload failed.', true, 60000, { action: { label: 'Retry', onClick() {} } }); return true; })`);
        }, { min: 3, expect: `document.querySelectorAll('.toast').length >= 3` });
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); true`);
        await sc('settings menu', async () => { await tap('[aria-controls="top-bar-settings-menu"]'); }, { min: 6, expect: `getComputedStyle(document.getElementById('top-bar-settings-menu')).display !== 'none'` });
        await ev(`document.body.click(); true`);
    } finally { await httpJson('DELETE', `/api/board/${TEAM}`).catch(() => {}); if (standIn) await standIn.stop().catch(() => {}); await guard.restore().catch(() => {}); await client.close(); }
    const bad = problems(results).concat(required(results, ['side panel: board composer', 'chat bubbles', 'toasts', 'home (agent list)', 'agent chat and composer', 'composer: model menu', 'chat: find bar', 'terminal view', 'side panel: files', 'side panel: browse', 'side panel: artifacts', 'side panel: customize tabs', 'artifact, full screen', 'artifact: comments sheet', 'team settings: workflow', 'history list', 'history chat', 'history chat: notes', 'history chat: commits', 'board: a team board', 'workflows list', 'workflow detail', 'workflow edit', 'workflow run detail', 'jobs list', 'job detail', 'job edit', 'docs', 'settings: general', 'settings: agents', 'settings: terminal', 'settings: notifications', 'settings: files', 'settings: cleanup', 'settings: network', 'settings: security', 'launch: chooser', 'launch: new agent', 'launch: team', 'create task', 'settings menu']));
    if (process.env.PHONE_TARGETS_REPORT) { require('node:fs').writeFileSync(process.env.PHONE_TARGETS_REPORT, results.map(r => `${r.name}: ${r.error || r.n + ' controls, ' + r.small.length + ' small'}`).join('\n') + '\n--\n' + bad.join('\n') + '\n'); process.exit(0); }
    assert.deepEqual(bad, [], 'controls under 44 px on a phone:\n  ' + bad.join('\n  '));
    console.log(`PASS ${results.length} scenes: every control is at least 44 x 44 px`);
})().catch(e => { console.error(e); process.exit(1); });
