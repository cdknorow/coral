// The text-contrast census on a desktop (#6171): at 1280 px, on each main screen of this computer's own dashboard, every visible text and meaningful icon meets WCAG AA (4.5:1 text, 3:1 large text and
// icons) against what is behind it, in the light and the dark theme (helpers/contrast.js does the measuring; helpers/phone_targets.js runs a scene in both themes and holds the reviewed allow list,
// empty). The phone's screens and the Hub's are phone_targets_census_browser's and its Hub twin. A stand-in agent and a team board give the screens real content; the chat's bubbles and the toasts are
// drawn as the dashboard draws them. PHONE_TARGETS_REPORT=<file> writes the failures to that file instead of failing.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { launchStandIn } = require('./helpers/stand_in_agent');
const { guardSettings } = require('./helpers/settings_guard');
const { scene, tap: tapSel, problems, required, useScaleShot, openBoardComposer, BOARD_COMPOSER_UP, showUpdateReady, UPDATE_NOTICE_UP, UPDATE_DIALOG_UP } = require('./helpers/phone_targets');
const { requiredScenes } = require('./helpers/census_required'); // the scenes the suite must visit: one file each, so commits that add scenes do not conflict
const { INBOX_WORLD } = require('./helpers/inbox_world');
const { STUB, HEX } = require('./helpers/comments_scale');
const { DATA_WORLD, httpJson, UPDATE_STAGED, CLOUD_LINKED } = require('./helpers/census_world');
const BASE = require('./test_base').BASE;
const TEAM = 'census-d-' + Date.now().toString(36);
const S = `(window.__coralStatic || '/static/')`;
const BUBBLES = [{ type: 'user', content: 'Is the build green?', key: 'c1', timestamp: '2026-10-19T00:00:00Z' }, { type: 'assistant', text: 'Yes. **All 12 checks pass.**\n\n- the unit tests\n- the lint\n\n`go test ./...` is clean, see [the run](https://example.com/run/1).\n\n```sh\ngo build ./...\n```', key: 'c2', timestamp: '2026-10-19T00:00:05Z' }];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    if (process.env.SCALE_SHOT_DIR) useScaleShot(async name => require('node:fs').writeFileSync(`${process.env.SCALE_SHOT_DIR}/${name.replace(/[^a-z0-9]+/gi, '-')}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'))); // SCALE_SHOT_DIR=<dir>: a picture of each scene with 200% findings
    const guard = await guardSettings(); let standIn = null;
    const results = [];
    const sc = (name, setup, opts) => scene(ev, results, name, setup, { contrastOnly: true, ...opts });
    const tap = sel => tapSel(ev, sel);
    const load = async () => { await Page.navigate({ url: 'about:blank' }); await Page.loadEventFired(); await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install(); await ev(`window.__coralReady.startup('liveList', 'settings')`); };
    try {
        standIn = await launchStandIn(httpJson, { displayName: 'Census Agent' });
        await httpJson('POST', `/api/board/${TEAM}/subscribe`, { subscriber_id: 'Boss', job_title: 'Orchestrator', session_name: 'boss-session' });
        for (const m of ['Please check the release notes.', 'Then post the summary.']) await httpJson('POST', `/api/board/${TEAM}/messages`, { subscriber_id: 'Boss', content: m });
        for (const [title, priority] of [['Ship the release notes', 'high'], ['Check the migration', 'medium'], ['Tidy the changelog', 'low']]) await httpJson('POST', `/api/board/${TEAM}/tasks`, { subscriber_id: 'Boss', title, body: 'A task for the census.', priority, assignee: 'Boss' });
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await Page.addScriptToEvaluateOnNewDocument({ source: DATA_WORLD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false }); await Emulation.setTouchEmulationEnabled({ enabled: false });
        await load();
        await sc('home (agent list)', async () => { await ev(`window.__m.add('rowa', 'Row A', 2, {}); window.__m.add('rowb', 'Row B', 2, {}); window.switchNavTab('agents'); true`); await waitFor(`document.querySelectorAll('#live-sessions-list li, [data-section="live-sessions"] .session-list > li').length >= 2`, 'agent rows', 10000); }, { min: 5 });
        // the Agents list's menus and the agent view's other states (#6524): a row's menu, the Display options, a sleeping agent (Wake), and an agent whose automated messages are held
        const vis = `e => e.getClientRects().length > 0 && getComputedStyle(e).visibility !== 'hidden'`;
        await ev(`document.body.click(); true`);
        await sc('agents list: display options open', async () => { await ev(`(() => { const b = [...document.querySelectorAll('#sidebar-summary-btn, .mobile-list-header [data-display-button]')].find(${vis}); if (!b) return false; if (b.getAttribute('aria-expanded') !== 'true') b.click(); return true; })()`); await waitFor(`[...document.querySelectorAll('[data-display-panel]')].some(e => !e.hidden && e.getClientRects().length)`, 'the Display panel', 5000); }, { min: 4, expect: `[...document.querySelectorAll('[data-display-panel]')].some(e => !e.hidden && e.getClientRects().length)` });
        await ev(`(() => { const b = [...document.querySelectorAll('#sidebar-summary-btn, .mobile-list-header [data-display-button]')].find(${vis}); if (b && b.getAttribute('aria-expanded') === 'true') b.click(); return true; })()`);
        await sc('agent view: asleep', async () => { await ev(`window.selectLiveSession('rowa', 'claude', 'rowa-id', 'local'); true`); await waitFor(`!!document.getElementById('agent-state-status') || /asleep|wake/i.test(document.getElementById('live-session-view') ? document.getElementById('live-session-view').textContent : document.body.textContent)`, 'the sleeping agent', 8000); }, { min: 4, expect: `/wake/i.test(document.body.textContent)` });
        await sc('agent chat and composer', async () => {
            await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.selectLiveSession(${JSON.stringify(standIn.name)}, 'claude', ${JSON.stringify(standIn.sid)}, 'local'); true`);
            await waitFor(`!!document.querySelector('#command-input') && document.querySelector('#command-input').getClientRects().length > 0`, 'composer', 20000);
            await ev(`(() => { const i = document.getElementById('command-input'); i.focus(); i.value = 'Is the build green?'; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
            await ev(`document.querySelector('[data-on-click="cmd-send"]').click(); true`);
            await waitFor(`[...document.querySelectorAll('#live-history-messages .chat-bubble.human')].some(b => /build green/.test(b.textContent))`, 'my message', 15000);
        }, { min: 10, expect: `document.querySelectorAll('#live-history-messages .chat-bubble').length >= 1` });
        await sc('composer: model menu', async () => {
            await ev(`import(${S} + 'model_chip.js').then(m => m.updateModelChip({ session_id: ${JSON.stringify(standIn.sid)}, name: ${JSON.stringify(standIn.name)}, agent_type: 'claude', model: 'claude-sonnet-5-5', sleeping: false, archived: false }, 'local')).then(() => true)`);
            await waitFor(`document.getElementById('model-chip').getClientRects().length > 0`, 'the model chip', 10000);
            await tap('#model-chip');
        }, { min: 10, expect: `!document.getElementById('model-chip-menu').hidden` });
        await ev(`document.body.click(); true`);
        await sc('terminal view', async () => { await tap('#live-view-btn-terminal'); }, { min: 8, expect: `document.getElementById('live-view-btn-terminal').classList.contains('active') || document.getElementById('live-view-btn-terminal').getAttribute('aria-pressed') === 'true'` });
        await tap('#live-view-btn-chat');
        await ev(`window.toggleAgenticPanel && window.toggleAgenticPanel(true); true`);
        for (const tab of await ev(`[...document.querySelectorAll('#agentic-block-top .agentic-tab[data-tab]')].filter(e => e.getClientRects().length).map(e => e.dataset.tab)`)) await sc(`side panel: ${tab}`, async () => { await ev(`window.switchAgenticTab(${JSON.stringify(tab)}, 'top'); true`); }, { min: 5, expect: `document.querySelector('#agentic-block-top .agentic-tab[data-tab="${tab}"]').classList.contains('active')` });
        await sc('side panel: board composer', async () => { await openBoardComposer(ev, waitFor, TEAM); }, { min: 5, expect: BOARD_COMPOSER_UP }); // the team's message box (#6239)
        await load();
        await sc('history list', async () => { await ev(`window.switchNavTab('history'); true`); await waitFor(`document.getElementById('history-sessions-list').textContent.includes('Release notes chat')`, 'history', 10000); }, { min: 4 });
        // the page's search: the results with their excerpts, the advanced filters, a search that finds nothing, and opening one hit (#6288)
        const histType = text => ev(`(() => { const i = document.getElementById('history-search'); i.focus(); i.value = ${JSON.stringify(text)}; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
        await sc('history: search results', async () => { await histType('release'); await waitFor(`document.querySelectorAll('#chat-search-results .chat-search-hit').length === 3`, 'search hits', 10000); }, { min: 4, expect: `document.querySelectorAll('#chat-search-results .chat-search-hit').length === 3` });
        await sc('history: search, advanced filters', async () => { await tap('#hf-adv-toggle'); await waitFor(`document.getElementById('hf-advanced').style.display !== 'none'`, 'advanced filters', 5000); }, { min: 8, expect: `document.getElementById('hf-advanced').style.display !== 'none'` });
        await tap('#hf-adv-toggle');
        await sc('history: search finds nothing', async () => { await histType('zzz'); await waitFor(`/No matching chats/.test(document.getElementById('chat-search-results').textContent)`, 'no results', 10000); }, { min: 3, expect: `/No matching chats/.test(document.getElementById('chat-search-results').textContent)` });
        await histType('release'); await waitFor(`document.querySelectorAll('#chat-search-results .chat-search-hit').length === 3`, 'search hits again', 10000);
        await sc('history: a search hit is opened', async () => { await tap('#chat-search-results .chat-search-hit'); await waitFor(`document.getElementById('history-messages').textContent.includes('Hi there')`, 'the chat', 10000); }, { min: 5, expect: `document.getElementById('history-messages').getClientRects().length > 0` });
        await histType(''); await waitFor(`!document.getElementById('history-sessions-list').hidden`, 'the chat list', 10000);
        await sc('history chat', async () => { await tap('#history-sessions-list [data-on-click="history-open"]'); await waitFor(`document.getElementById('history-messages').textContent.includes('Hi there')`, 'chat', 10000); }, { min: 5, expect: `document.getElementById('history-messages').getClientRects().length > 0` });
        for (const t of ['notes', 'activity', 'tasks', 'agent-notes', 'commits']) await sc(`history chat: ${t}`, async () => { await tap(`.history-tab-btn[data-tab="${t}"]`); }, { min: 5, expect: `document.querySelector('.history-tab-btn[data-tab="${t}"]').classList.contains('active')` });
        await sc('board', async () => { await ev(`window.switchNavTab('board'); true`); }, { min: 2, expect: `document.getElementById('messageboard-view').getClientRects().length > 0` });
        await sc('board: a team board', async () => { await tap('#mb-projects-ul [data-project]'); await waitFor(`/Please check the release notes/.test(document.getElementById('mb-messages').textContent)`, 'board messages', 10000); }, { min: 5, expect: `/Please check the release notes/.test(document.getElementById('mb-messages').textContent)` });
        // the team board's own controls and its task list (#6566): the More menu, the subscribers sheet, the Tasks panel with tasks (its filters open, one task opened)
        const keyEsc = async () => { await client.Input.dispatchKeyEvent({ type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 }); await client.Input.dispatchKeyEvent({ type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 }); await new Promise(r => setTimeout(r, 300)); }; // sleep-ok: the close transition
        await sc('board: more actions menu', async () => { await tap('#mb-more-btn'); }, { expect: `document.getElementById('mb-more-btn').getAttribute('aria-expanded') === 'true'` });
        await keyEsc();
        await sc('board: tasks', async () => { await ev(`window.toggleAgenticPanel && window.toggleAgenticPanel(true); window.switchAgenticTab('tasks', 'top'); true`); await waitFor(`document.querySelectorAll('#board-task-list button[aria-label^="Open task"]').length >= 3`, 'the board tasks', 15000); }, { expect: `document.querySelectorAll('#board-task-list button[aria-label^="Open task"]').length >= 3` });
        await sc('board: tasks, filters open', async () => { await ev(`true`); await tap('#board-task-bar .list-bar-toggle'); }, { expect: `document.querySelector('#board-task-bar .list-bar-toggle').getAttribute('aria-expanded') === 'true'` });
        await ev(`document.querySelector('#board-task-bar .list-bar-toggle')?.click(); true`);
        await sc('board: a task opened', async () => { await tap('#board-task-list button[aria-label^="Open task"]'); await waitFor(`getComputedStyle(document.getElementById('task-detail-modal')).display !== 'none'`, 'the task', 10000); }, { expect: `getComputedStyle(document.getElementById('task-detail-modal')).display !== 'none'` });
        await keyEsc(); // the task dialog, before the artifact dialog opens over it (two dialogs left open made later scenes see two Edit and two Close buttons)
        // the plain artifact viewer, the dialog a coral://artifacts link opens (#6586): Download and Close, then its Comments panel
        await sc('artifact dialog', async () => {
            await ev(STUB({ filename: 'release-notes.md', content: '# Release notes\n\nThe build is green.\n', mediaType: 'text/markdown', comments: [{ id: 1, parent_id: null, anchor: { whole: true }, body: 'Please check.', author: { kind: 'owner', issuer: 'local', subject: 'Operator' }, created_at: '2026-10-22T00:00:00Z' }] }));
            await ev(`import(${S} + 'artifact_viewer.js').then(m => m.openArtifactViewer('${HEX}', { filename: 'release-notes.md' })).then(() => true)`); await waitFor(`!document.getElementById('artifact-viewer').hidden && /Release notes/.test(document.querySelector('#artifact-viewer .artifact-viewer-body').textContent)`, 'the artifact', 15000); }, { expect: `!document.getElementById('artifact-viewer').hidden` });
        await sc('artifact dialog: comments', async () => { await tap('#artifact-viewer .ac-toggle'); }, { expect: `!!document.querySelector('#artifact-viewer .ac-panel') && /Please check/.test(document.querySelector('#artifact-viewer .ac-panel').textContent)` });
        await keyEsc();
        await sc('workflows list', async () => { await ev(`window.switchNavTab('workflows'); true`); await waitFor(`!!document.querySelector('#wf-list-container .wf-card[data-on-click="wf-select"]')`, 'workflows', 10000); }, { min: 4 });
        await sc('workflow detail', async () => { await tap('.wf-card[data-on-click="wf-select"]'); await waitFor(`!!document.querySelector('#wf-detail-container tr[data-on-click="wf-run"]')`, 'runs', 10000); }, { min: 5 });
        await sc('workflow run detail', async () => { await tap('#wf-detail-container tr[data-on-click="wf-run"]'); await waitFor(`!!document.querySelector('#wf-run-detail-container [data-on-click="wf-output-toggle"]')`, 'run detail', 10000); }, { min: 4 });
        await sc('jobs list', async () => { await ev(`window.switchNavTab('jobs'); true`); await tap('[data-subtab="scheduled"]'); await waitFor(`!!document.querySelector('#scheduled-jobs-list [data-on-click="job-select"]') && document.querySelector('#scheduled-jobs-list [data-on-click="job-select"]').getClientRects().length > 0`, 'jobs', 10000); }, { min: 4 });
        await sc('job detail', async () => { await tap('#scheduled-jobs-list [data-on-click="job-select"]'); await waitFor(`!!document.querySelector('#scheduler-detail [data-on-click="job-edit"]')`, 'detail', 10000); }, { min: 5 });
        await sc('job edit', async () => { await tap('#scheduler-detail [data-on-click="job-edit"]'); }, { min: 5, expect: `[...document.querySelectorAll('.modal, dialog')].some(m => m.getClientRects().length > 0 && /cron|schedule/i.test(m.textContent))` });
        await ev(`document.querySelectorAll('.modal.show, dialog[open]').forEach(m => { m.classList.remove('show'); if (m.close) m.close(); }); true`);
        await load();
        for (const [n, tab, label] of [['analytics', 'tokens', 'Analytics'], ['docs', 'docs', 'Docs'], ['inbox', 'hub-inbox', 'Inbox']]) await sc(n, async () => { await ev(`window.switchNavTab('${tab}'); true`); }, { min: 3 });
        // the Inbox's three tabs with something in each (#6541): an agent waiting, the open tasks (one opened to its reply box), the saved messages (their filters open)
        await sc('inbox: needs you, an agent waiting', async () => {
            await ev(INBOX_WORLD);
            await ev(`localStorage.removeItem('coral.inbox.tab'); window.__m.add('Waiting Agent', 'W', 1, { sleeping: false, session_id: 'wait-1' }); window.__qaState.liveSessions = window.__qaState.liveSessions.map(s => s.session_id === 'wait-1' ? { ...s, waiting_for_input: true, waiting_kind: 'permission' } : s); window.switchNavTab('hub-inbox'); import(${S} + 'inbox_tab.js').then(m => m.updateInboxTab()); true`);
            await waitFor(`document.querySelectorAll('#inbox-needs-list .inbox-needs-item').length === 1`, 'the waiting agent', 10000);
        }, { min: 3, expect: `document.querySelectorAll('#inbox-needs-list .inbox-needs-item').length === 1` });
        await sc('inbox: tasks for you', async () => { await tap('#inbox-tab-tasks'); await waitFor(`document.querySelectorAll('#inbox-tasks-host .ibx-task').length === 3`, 'three tasks', 10000); }, { min: 5, expect: `document.querySelectorAll('#inbox-tasks-host .ibx-task').length === 3` });
        await sc('inbox: a task opened', async () => { await tap('#inbox-tasks-host .ibx-task-head'); await waitFor(`!!document.querySelector('#inbox-tasks-host .ibx-reply-input')`, 'the reply box', 10000); }, { min: 7, expect: `!!document.querySelector('#inbox-tasks-host .ibx-reply-input')` });
        await sc('inbox: saved for later', async () => { await tap('#inbox-tab-saved'); await waitFor(`document.querySelectorAll('#saved-list .saved-item').length === 4`, 'four saved items', 10000); }, { min: 8, expect: `document.querySelectorAll('#saved-list .saved-item').length === 4` });
        await sc('inbox: saved, filters open', async () => { await tap('#saved-view .list-bar-filter-btn'); }, { min: 8, expect: `!document.getElementById('sv-menu').hidden` });
        await ev(`localStorage.removeItem('coral.inbox.tab'); window.switchNavTab('agents'); true`);
        // a Docs topic opened (#6586): its headings, links and code, and the topics list with the open one marked
        await sc('docs: a topic opened', async () => { await ev(`window.switchNavTab('docs'); true`); await waitFor(`document.querySelectorAll('#docs-nav-list [data-on-click="doc-open"]').length >= 3`, 'the topics', 15000); await tap('#docs-nav-list [data-on-click="doc-open"]'); await waitFor(`document.querySelectorAll('#docs-content h1, #docs-content h2').length >= 1`, 'the topic', 15000); }, { expect: `document.querySelectorAll('#docs-content h1, #docs-content h2').length >= 1` });
        await sc('analytics, with data', async () => { await ev(`window.switchNavTab('tokens'); true`); await waitFor(`['cost-by-provider', 'cost-by-model', 'cost-by-thinking', 'cost-by-agent', 'cost-by-team', 'cost-by-branch'].every(id => document.querySelectorAll('#' + id + ' tbody tr').length > 0) && !!document.querySelector('#cost-spend-chart svg')`, 'the analytics data', 15000); }, { min: 10, expect: `document.getElementById('cost-total-cost').textContent.includes('6.4')` });
        await ev(`window.switchNavTab('agents'); true`);
        // the update prompt (#6292, #6306): the ready notice, the confirmation it opens, and Settings > Updates below (the flag is on from here)
        await sc('update: ready notice', async () => { await showUpdateReady(ev, waitFor, UPDATE_STAGED); }, { min: 3, expect: UPDATE_NOTICE_UP });
        await sc('update: confirmation', async () => { await tap('.update-ready-review'); await waitFor(UPDATE_DIALOG_UP, 'the confirmation', 5000); }, { min: 3, expect: UPDATE_DIALOG_UP });
        await ev(`document.querySelector('#update-confirm-modal .update-later').click(); true`);
        await sc('settings: network, linked to Coral Hub', async () => { await ev(`window.__cloudStatus = ${JSON.stringify(CLOUD_LINKED)}; (async () => { await window.showSettingsModal('network'); return true; })()`); await waitFor(`!!document.getElementById('settings-cloud-hub-relaunch-state') && document.getElementById('settings-cloud-hub-relaunch-state').textContent.length > 10 && document.getElementById('settings-cloud-hub-relaunch-state').getClientRects().length > 0`, 'the hub relaunch sentence', 10000); }, { min: 8, expect: `/Coral Hub/.test(document.getElementById('settings-cloud-hub-relaunch-state').textContent)` });
        await ev(`window.hideSettingsModal && window.hideSettingsModal(); window.__cloudStatus = null; true`);
        for (const g of ['general', 'agents', 'terminal', 'notifications', 'files', 'cleanup', 'network', 'security', 'updates']) await sc(`settings: ${g}`, async () => { await ev(`(async () => { await window.showSettingsModal(); return true; })()`); await ev(`(() => { const p = document.getElementById('settings-group-select'); if (p) { p.value = '${g}'; p.dispatchEvent(new Event('change', { bubbles: true })); } else { const t = document.querySelector('#settings-modal [data-group="${g}"], #settings-modal [data-settings-group="${g}"]'); if (t) t.click(); } return true; })()`); }, { min: 5, expect: `document.querySelector('#settings-modal .settings-pane:not([hidden])').dataset.group === '${g}'` });
        await sc('settings: agents, adding a custom model', async () => { await ev(`(async () => { await window.showSettingsModal('agents'); return true; })()`); await waitFor(`!!document.querySelector('#settings-custom-models [data-cm-add]') && document.querySelector('#settings-custom-models [data-cm-add]').getClientRects().length > 0`, 'Add a model', 8000); for (let i = 0; i < 3 && !(await ev(`!!document.querySelector('#settings-custom-models input')`)); i++) { await tap('#settings-custom-models [data-cm-add]'); await new Promise(r => setTimeout(r, 1200)); } /* sleep-ok: the form draws after the click; a click the list's own refresh swallowed is made again */ await waitFor(`!!document.querySelector('#settings-custom-models input')`, 'the add form', 5000); }, { min: 6, expect: `!!document.querySelector('#settings-custom-models input') && document.querySelector('#settings-custom-models input').getClientRects().length > 0` });
        await ev(`window.hideSettingsModal && window.hideSettingsModal(); true`);
        await ev(`window.hideSettingsModal(); true`);
        await sc('launch: chooser', async () => { await ev(`(async () => { await window.showLaunchModal(); return true; })()`); }, { min: 5, expect: `document.querySelector('#launch-modal .modal-content').getClientRects().length > 0` });
        await sc('launch: new agent', async () => { await ev(`window._selectLaunchType('agent'); true`); }, { min: 5, expect: `/Working Directory/i.test(document.querySelector('#launch-modal .modal-content').textContent)` });
        await sc('launch: new agent, step 2', async () => { await ev(`(() => { const i = document.getElementById('launch-dir'); i.value = '/tmp/census-project'; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`); await tap('#launch-agent-next'); await waitFor(`document.getElementById('launch-step-agent').dataset.stage === 'details'`, 'step 2', 8000); }, { min: 8, expect: `document.getElementById('launch-step-agent').dataset.stage === 'details' && /Step 2 of 2/.test(document.getElementById('launch-stage-live').textContent)` });
        await sc('launch: team', async () => { await ev(`window.showLaunchModal(); window._selectLaunchType('team'); true`); await waitFor(`document.querySelectorAll('#team-template-list [role="option"]').length > 0`, 'the team templates', 8000); }, { min: 8, expect: `document.getElementById('team-tab-team').getAttribute('aria-selected') === 'true' && /Step 1 of 3/.test(document.getElementById('team-step-live').textContent)` });
        await sc('launch: team, no template matches', async () => { await ev(`(() => { const i = document.getElementById('team-template-search'); i.focus(); i.value = 'zzzz-no-such-template'; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`); await waitFor(`!document.getElementById('team-template-none').hidden`, 'the empty picker', 5000); }, { min: 8, expect: `!document.getElementById('team-template-none').hidden && document.querySelectorAll('#team-template-list [role="option"]').length === 0` });
        await ev(`(() => { const i = document.getElementById('team-template-search'); i.value = ''; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
        await waitFor(`document.querySelectorAll('#team-template-list [role="option"]').length > 0`, 'the templates again', 5000);
        await ev(`document.querySelector('#team-template-list [role="option"]').click(); true`);
        await sc('launch: team, members', async () => { await tap('#team-tab-members'); await waitFor(`!document.getElementById('team-panel-members').hidden && document.querySelectorAll('#team-agents-list tr').length > 0`, 'members', 8000); }, { min: 8, expect: `document.getElementById('team-tab-members').getAttribute('aria-selected') === 'true' && document.querySelectorAll('#team-agents-list tr').length > 0` });
        await sc('launch: team, start', async () => { await tap('#team-tab-start'); await waitFor(`!document.getElementById('team-panel-start').hidden`, 'start', 5000); }, { min: 8, expect: `document.getElementById('team-tab-start').getAttribute('aria-selected') === 'true' && /Step 3 of 3/.test(document.getElementById('team-step-live').textContent)` });
        await ev(`window.hideLaunchModal(); true`);
        await sc('create task', async () => { await ev(`(async () => { await window.showCreateTaskModal(); return true; })()`); }, { min: 5, expect: `document.getElementById('create-task-modal').getClientRects().length > 0` });
        await ev(`window.hideCreateTaskModal(); true`);
        await sc('chat bubbles', async () => {
            await ev(`import(${S} + 'live_chat.js').then(m => { document.getElementById('census-box')?.remove(); const box = document.createElement('div'); box.id = 'census-box'; box.className = 'chat-messages chat-prose';
                box.style.cssText = 'position:fixed;left:0;top:0;right:0;bottom:0;overflow:auto;background:var(--bg-primary);z-index:99999;padding:12px'; document.body.appendChild(box);
                m.renderTranscript(${JSON.stringify(BUBBLES)}, box, 'claude'); return true; })`);
            await ev(`import(${S} + 'saved_marks.js').then(m => { document.querySelectorAll('#census-box .chat-save-btn').forEach(b => m.nameSaveButton(b)); return true; })`);
        }, { min: 1, expect: `document.querySelectorAll('#census-box .chat-bubble').length >= 2` });
        await ev(`document.getElementById('census-box')?.remove(); true`);
        await sc('toasts', async () => {
            await ev(`import(${S} + 'utils.js').then(m => { m.showToast('Saved to the board.', false, 60000); m.showToast('Could not reach the agent.', true, 60000); m.showToast('Upload failed.', true, 60000, { action: { label: 'Retry', onClick() {} } }); return true; })`);
        }, { min: 3, expect: `document.querySelectorAll('.toast').length >= 3` });
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); true`);
        await sc('settings menu', async () => { await tap('[aria-controls="top-bar-settings-menu"]'); }, { min: 3, expect: `getComputedStyle(document.getElementById('top-bar-settings-menu')).display !== 'none'` });
        await ev(`document.body.click(); true`);
    } finally { await httpJson('DELETE', `/api/board/${TEAM}`).catch(() => {}); if (standIn) await standIn.stop().catch(() => {}); await guard.restore().catch(() => {}); await client.close(); }
    const bad = problems(results).concat(required(results, requiredScenes('contrast')));
    if (process.env.PHONE_TARGETS_REPORT) { require('node:fs').writeFileSync(process.env.PHONE_TARGETS_REPORT, results.map(r => `${r.name}: ${r.error || r.contrastChecked + ' looked at'}`).join('\n') + '\n--\n' + bad.join('\n') + '\n'); process.exit(0); }
    assert.deepEqual(bad, [], 'text or icons under their WCAG AA contrast on a desktop:\n  ' + bad.join('\n  '));
    console.log(`PASS ${results.length} scenes: every visible text and icon meets AA in both themes`);
})().catch(e => { console.error(e); process.exit(1); });
