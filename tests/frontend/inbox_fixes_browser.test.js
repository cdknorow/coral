// The Inbox (#7122, UI/UX's #7114 findings 7 to 11): a Needs you row says the team and the folder beside the agent (and the question when the agent sent one); Tasks are ordered by priority, then time, with a
// High chip and the number of high ones on the tab; the reply box says the task stays open and "Open the task" is a button; the same "the transcript is gone" sentence is said once for a list, not on every
// row; and the top bar's Saved count is not drawn beside the Inbox's own tab counts. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server (its rows are removed again).
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const { removeBoards } = require('./helpers/board_cleanup');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: the Inbox draws from several answers after a tab is chosen
    const api = (method, path, body) => ev(`fetch(${JSON.stringify(path)}, { method: ${JSON.stringify(method)}, headers: { 'Content-Type': 'application/json' }, body: ${body ? JSON.stringify(JSON.stringify(body)) : 'undefined'} }).then(async r => ({ status: r.status, body: await r.json().catch(() => null) }))`);
    const B = 'inbox-fixes-' + Date.now().toString(36);
    const savedIds = [];
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'liveListLoaded', 'settings')`);
        await api('POST', `/api/board/${B}/subscribe`, { subscriber_id: 'orch', job_title: 'Orchestrator', session_name: 'orch-s' });
        for (const [t, p] of [['Pick the name for the Goals tab', 'low'], ['Approve the design', 'medium'], ['Decide: ship 166 with the Windows trace pending?', 'high']]) { const r = await api('POST', `/api/board/${B}/tasks`, { title: t, assigned_to: 'Operator', created_by: 'orch', priority: p }); assert.ok(r.status < 300, JSON.stringify(r)); await sleep(1100); } // sleep-ok: the three tasks get different update times, oldest the high one last
        let ki = 0;
        for (const [sn, ti] of [['The retry wording is one sentence', 'rev'], ['Run the suite twice', 'qa'], ['Goals need a done-when line', 'fe']]) { const r = await api('POST', '/api/saved', { kind: 'agent', ref: ti + '-id', key: (++ki).toString(16).padStart(20, '0'), title: ti, snippet: sn, agent_type: 'claude', said_at: '2026-10-24T08:00:00Z', team: B }); assert.equal(r.status, 200, JSON.stringify(r)); savedIds.push(r.body.id); }
        await ev(`(() => { const waiting = ['rev-id', 'qa-id']; const entry = window.__m.entry.bind(window.__m);
            window.__m.entry = a => ({ ...entry(a), board_project: a.session_id === 'qa-id' ? ${JSON.stringify(B)} : null, board_job_title: { 'rev-id': 'Reviewer', 'qa-id': 'QA Engineer' }[a.session_id] || '', waiting_for_input: waiting.includes(a.session_id), waiting_kind: waiting.includes(a.session_id) ? 'permission' : null, waiting_summary: a.session_id === 'qa-id' ? 'Run npm test in the frontend folder' : '', working_directory: a.session_id === 'qa-id' ? '/work/coral/tests/frontend' : '/work/coral/coral-go' });
            window.__m.add('rev', 'R', 4, { sleeping: false, session_id: 'rev-id' }); window.__m.add('qa', 'Q', 4, { sleeping: false, session_id: 'qa-id' }); window._coralSetLiveSessions(Object.values(window.__m.agents).map(a => window.__m.entry(a))); return true; })()`);
        await ev(`document.getElementById('nav-tab-hub-inbox').click(); true`);
        await waitFor(`document.querySelectorAll('#inbox-needs-list .inbox-needs-item').length === 2`, 'two agents waiting', 10000);
        // Needs you: the folder beside the agent, the team where there is one, the question when there is one
        const needs = await ev(`[...document.querySelectorAll('#inbox-needs-list .inbox-needs-item')].map(b => ({ name: b.querySelector('.inbox-needs-name').textContent, folder: (b.querySelector('.inbox-needs-folder') || {}).textContent || '', team: (b.querySelector('.inbox-needs-team') || {}).textContent || '', ask: (b.querySelector('.inbox-needs-ask') || {}).textContent || '' }))`);
        const qa = needs.find(n => n.name === 'qa'), rev = needs.find(n => n.name === 'rev');
        assert.ok(qa && qa.folder === 'frontend' && qa.team && qa.ask === 'Run npm test in the frontend folder', 'the qa row says its folder, team and question: ' + JSON.stringify(needs));
        assert.ok(rev && rev.folder === 'coral-go' && rev.team === '', 'the rev row says its folder (no team, no question): ' + JSON.stringify(needs));
        // Tasks: priority first, a High chip, the count of high ones on the tab
        await ev(`document.getElementById('inbox-tab-tasks').click(); true`);
        await waitFor(`document.querySelectorAll('#inbox-panel-tasks .ibx-task').length === 3`, 'three tasks', 10000);
        const tasks = await ev(`({ titles: [...document.querySelectorAll('#inbox-panel-tasks .ibx-task-title')].map(t => t.textContent), chips: [...document.querySelectorAll('#inbox-panel-tasks .ibx-task')].map(li => !!li.querySelector('.ibx-chip-high')), tab: document.getElementById('inbox-tab-tasks').getAttribute('aria-label'), box: document.getElementById('inbox-tab-tasks-count').textContent })`);
        assert.deepEqual(tasks.titles, ['Decide: ship 166 with the Windows trace pending?', 'Approve the design', 'Pick the name for the Goals tab'], 'the tasks are ordered by priority: ' + JSON.stringify(tasks));
        assert.deepEqual(tasks.chips, [true, false, false], 'only the high one has a High chip: ' + JSON.stringify(tasks));
        assert.ok(/3/.test(tasks.tab) && /1 high/.test(tasks.tab) && /1 high/.test(tasks.box), 'the tab says 3 tasks and 1 high: ' + JSON.stringify(tasks));
        // a task opens: the reply box says the task stays open, and Open the task is a button
        await ev(`document.querySelector('#inbox-panel-tasks .ibx-task-head').click(); true`);
        await waitFor(`!!document.querySelector('#inbox-panel-tasks .ibx-reply-label')`, 'the reply box', 8000);
        const reply = await ev(`(() => { const o = document.querySelector('#inbox-panel-tasks .ibx-open'); return { label: document.querySelector('#inbox-panel-tasks .ibx-reply-label').textContent, oldNote: !!document.querySelector('#inbox-panel-tasks .ibx-note'), openTag: o && o.tagName, openBtn: o && o.classList.contains('btn'), openH: o && Math.round(o.getBoundingClientRect().height) }; })()`);
        assert.ok(/task stays open/.test(reply.label) && !reply.oldNote, 'the reply box says the task stays open (once): ' + JSON.stringify(reply));
        assert.ok(reply.openBtn && reply.openH >= 30, 'Open the task is drawn as a button: ' + JSON.stringify(reply));
        // Saved: the gone sentence once
        await ev(`document.getElementById('inbox-tab-saved').click(); true`);
        await waitFor(`document.querySelectorAll('#saved-list .saved-item').length === 3`, 'three saved', 10000);
        await sleep(400); // sleep-ok: the rows are marked gone from the list's own answer
        const saved = await ev(`(() => { const rows = [...document.querySelectorAll('#saved-list .saved-item.gone')]; return { gone: rows.length, said: rows.filter(r => { const w = r.querySelector('.why'); return w && !w.hidden && w.textContent.trim(); }).length, tips: rows.map(r => ((r.querySelector('.kind.gone') || {}).title || '').length > 10) }; })()`);
        assert.equal(saved.gone, 3, 'three gone rows: ' + JSON.stringify(saved));
        assert.equal(saved.said, 1, 'the sentence is said once for the list: ' + JSON.stringify(saved));
        assert.ok(saved.tips.every(Boolean), 'every Gone chip carries the reason as its tip: ' + JSON.stringify(saved));
        // the top bar's count is not drawn on the Inbox page, where the tabs have theirs
        const bar = await ev(`(() => { const c = document.querySelector('#saved-btn .saved-count'); return { exists: !!c, shown: !!c && c.getClientRects().length > 0, tab: document.getElementById('inbox-tab-saved-count').textContent }; })()`);
        assert.ok(bar.tab === '3' && !bar.shown, 'the top bar does not repeat the Saved count on the Inbox page: ' + JSON.stringify(bar));
        console.log('PASS inbox fixes');
    } finally {
        for (const id of savedIds) await api('DELETE', `/api/saved/${id}`).catch(() => {});
        await removeBoards(ev, [B]).catch(() => api('DELETE', `/api/board/${B}`).catch(() => {}));
        await client.close();
    }
})().catch(e => { console.error(e); process.exit(1); });
