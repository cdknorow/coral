// The panels list_bars_themes_browser.test.js puts on screen (#4851): each has prepare(ctx) (once: seeds the isolated server) and show(ctx, { w, theme, phone }) (a fresh page, the panel open with its
// bar's menu and a chip showing) that returns { roots: [selectors to audit], focus: [selectors whose focus ring is audited] }. Set-ups are the panels' own suites' (task_search, board_search,
// team_artifacts_search, agent_list_filters, activity_search, history_events_search, servers_search, inbox_local), cut to what is needed to show the bar.
'use strict';
const { launchStandIn } = require('./stand_in_agent');

const uid = n => '0000' + String(n).padStart(4, '0') + '-0000-4000-8000-000000000000';
const hex = i => i.toString(16).padStart(20, '0');
const STAMP = Date.now().toString(36);

async function fresh(ctx) {
    await ctx.ev(`(() => { try { localStorage.clear(); } catch (_) {} return true; })()`).catch(() => {}); // the dock's open state and the last tab are per kind of page: each panel starts as a first visit
    const loaded = ctx.Page.loadEventFired();
    await ctx.Page.navigate({ url: ctx.BASE + '/' });
    await loaded;
    await ctx.install();
    await ctx.ev(`window.__coralReady.then(() => true)`);
}
const api = (ctx, method, path, body) => ctx.ev(`fetch(${JSON.stringify(path)}, { method: ${JSON.stringify(method)}, headers: { 'Content-Type': 'application/json' }, body: ${body ? JSON.stringify(JSON.stringify(body)) : 'undefined'} }).then(async r => ({ status: r.status, body: await r.json().catch(() => ({})) }))`);
const pick = (ctx, sel, value) => ctx.ev(`(() => { const s = document.querySelector(${JSON.stringify(sel)}); s.value = ${JSON.stringify(value)}; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
const typeInto = (ctx, sel, text) => ctx.ev(`(() => { const e = document.querySelector(${JSON.stringify(sel)}); e.focus(); e.value = ${JSON.stringify(text)}; e.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
const openMenu = (ctx, bar) => ctx.ev(`(() => { const m = document.querySelector('${bar} .list-bar-menu'); if (m && m.hidden) document.querySelector('${bar} .list-bar-filter-btn, ${bar} .list-bar-toggle').click(); return true; })()`);
const dockOpen = ctx => ctx.ev(`(async () => { const side = document.getElementById('agentic-state'); side.classList.remove('collapsed'); side.style.width = '680px'; return true; })()`);
const mockAgents = (ctx, list) => ctx.ev(`(() => { const jobs = ${JSON.stringify(Object.fromEntries(list.map(a => [a.id, a.job || ''])))}; const waiting = ${JSON.stringify(list.filter(a => a.waiting).map(a => a.id))};
    const entry = window.__m.entry.bind(window.__m); window.__m.entry = a => ({ ...entry(a), board_job_title: jobs[a.session_id] || '', waiting_for_input: waiting.includes(a.session_id), waiting_kind: waiting.includes(a.session_id) ? 'permission' : null });
    ${list.map(a => `window.__m.add(${JSON.stringify(a.name)}, 'A', 4, { sleeping: false, board_project: ${JSON.stringify(a.team)}, session_id: ${JSON.stringify(a.id)} });`).join('\n')}
    window.__qaState.liveSessions = window.__qaState.liveSessions.map(s => window.__m.agents[s.session_id] ? { ...s, ...window.__m.entry(window.__m.agents[s.session_id]) } : s); return true; })()`); // (the stand-in agents of the Activity and History panels are real sessions of the server, not mocks)

const PANELS = [];

// ── Inbox, Saved for later tab (the Saved bar's menu open, a filter chip showing; the tabs are one panel at a time, #5126) ──
PANELS.push({
    key: 'inbox',
    async prepare(ctx) {
        await fresh(ctx);
        for (const it of (await api(ctx, 'GET', '/api/saved')).body.items) await api(ctx, 'DELETE', `/api/saved/${it.id}`);
        const msg = async (team, from, content) => String((await api(ctx, 'POST', `/api/board/${team}/messages`, { subscriber_id: from, content })).body.id);
        const add = body => api(ctx, 'POST', '/api/saved', { agent_type: 'claude', said_at: '2026-10-17T08:00:00Z', ...body });
        await add({ kind: 'agent', ref: 'qa-a-id', key: hex(1), title: 'QA Alpha', snippet: 'a chat message worth coming back to', team: 'th-a' });
        await add({ kind: 'agent', ref: 'fe-a-id', key: hex(2), title: 'FE Alpha', snippet: 'the retry budget is per request', team: 'th-a' });
        await add({ kind: 'board', ref: 'th-a', key: await msg('th-a', 'QA Engineer', 'by the QA Engineer'), title: 'th-a', snippet: 'a board message, by the QA Engineer', author: 'QA Engineer' });
        await add({ kind: 'board', ref: 'th-b', key: await msg('th-b', 'Orchestrator', 'by the Orchestrator'), title: 'th-b', snippet: 'a board message, by the Orchestrator', author: 'Orchestrator' });
    },
    async show(ctx, { phone }) {
        await fresh(ctx);
        await mockAgents(ctx, [{ id: 'qa-a-id', name: 'QA Alpha', team: 'th-a', job: 'QA Engineer', waiting: true }, { id: 'fe-a-id', name: 'FE Alpha', team: 'th-a', job: 'Frontend Dev' }]);
        await ctx.settle(`['qa-a-id', 'fe-a-id'].every(id => (window.__qaState.liveSessions || []).some(s => s.session_id === id))`, 'agents');
        await ctx.ev(`import((window.__coralStatic || '/static/') + 'inbox_tab.js').then(m => m.updateInboxTab()); true`);
        if (phone) { await ctx.ev(`window.switchNavTab('hub-inbox'); true`); } else await ctx.ev(`document.getElementById('nav-tab-hub-inbox').click(); true`);
        await ctx.ev(`document.getElementById('inbox-tab-saved').click(); true`);
        await ctx.settle(`document.querySelectorAll('#saved-list > li:not(.saved-empty)').length >= 4 && document.querySelectorAll('#inbox-needs-list .inbox-needs-item').length === 1`, 'the Inbox');
        await pick(ctx, '#sv-kind', 'board'); // a Type chip
        await ctx.settle(`!!document.querySelector('#saved-bar .list-bar-chip')`, 'a chip');
        await openMenu(ctx, '#saved-bar');
        return { roots: ['#inbox-view'], focus: ['#sv-search', '#inbox-tab-needs', '#saved-bar .list-bar-filter-btn'] };
    },
});

// ── Inbox, Needs you tab (the hub's tabs and the waiting agent's row) ──
PANELS.push({
    key: 'inbox-needs',
    async show(ctx, { phone }) {
        await fresh(ctx);
        await ctx.ev(`localStorage.removeItem('coral.inbox.tab'); true`);
        await mockAgents(ctx, [{ id: 'qa-a-id', name: 'QA Alpha', team: 'th-a', job: 'QA Engineer', waiting: true }, { id: 'fe-a-id', name: 'FE Alpha', team: 'th-a', job: 'Frontend Dev' }]);
        await ctx.settle(`['qa-a-id', 'fe-a-id'].every(id => (window.__qaState.liveSessions || []).some(s => s.session_id === id))`, 'agents');
        await ctx.ev(`import((window.__coralStatic || '/static/') + 'inbox_tab.js').then(m => m.updateInboxTab()); true`);
        if (phone) { await ctx.ev(`window.switchNavTab('hub-inbox'); true`); } else await ctx.ev(`document.getElementById('nav-tab-hub-inbox').click(); true`);
        await ctx.settle(`document.querySelectorAll('#inbox-needs-list .inbox-needs-item').length === 1 && document.getElementById('inbox-needs-list').getClientRects().length > 0`, 'Needs you');
        return { roots: ['#inbox-view'], focus: ['.inbox-needs-item', '#inbox-tab-saved'] };
    },
});

// ── The Saved sidebar of an agent's page ──
PANELS.push({
    key: 'saved',
    async show(ctx, { phone }) {
        await fresh(ctx);
        await mockAgents(ctx, [{ id: 'qa-a-id', name: 'QA Alpha', team: 'th-a', job: 'QA Engineer' }]);
        await ctx.settle(`(window.__qaState.liveSessions || []).some(s => s.session_id === 'qa-a-id')`, 'agent');
        await ctx.ev(`window.selectLiveSession('QA Alpha', 'claude', 'qa-a-id', 'local'); true`);
        // the dock's own Saved tab (the top bar's bookmark is the Inbox's since #4905), the way the toast's link opens it
        await ctx.ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'saved.js'); await m.openSaved(); return true; })()`);
        await ctx.settle(`document.getElementById('saved-scope').textContent === 'Saved: QA Alpha' && document.querySelectorAll('#saved-list > li').length > 0`, 'the scoped list');
        await pick(ctx, '#sv-kind', 'agent');
        await ctx.settle(`!!document.querySelector('#saved-bar .list-bar-chip')`, 'a chip');
        await openMenu(ctx, '#saved-bar');
        return { roots: ['#agentic-panel-saved'], focus: ['#sv-search', '#saved-bar .list-bar-filter-btn'] };
    },
});

// ── Servers (a hub: eight remotes) ──
PANELS.push({
    key: 'servers',
    async prepare(ctx) {
        await ctx.Page.addScriptToEvaluateOnNewDocument({ source: `(() => { const inner = window.fetch; window.fetch = (u, o = {}) => { const p = new URL(String(u), location.origin).pathname; const m = (o.method || 'GET').toUpperCase();
            const json = b => Promise.resolve(new Response(JSON.stringify(b), { status: 200, headers: { 'content-type': 'application/json' } }));
            if (window.__stub && p === '/api/servers' && m === 'GET') return json(window.__servers || []);
            if (window.__stub && p === '/api/health') return json({ status: 'ok', hub: true, version: '1.4.92' });
            return inner(u, o); }; })();` });
    },
    async show(ctx) {
        await fresh(ctx);
        const remotes = Array.from({ length: 8 }, (_, i) => ({ id: 'm' + (i + 1), label: 'machine-' + (i + 1), url: 'http://10.0.0.' + (i + 11) + ':8420', status: i % 4 === 3 ? 'unreachable' : 'online' }));
        await ctx.ev(`(async () => { window.__stub = true; window.__servers = ${JSON.stringify([{ id: 'local', label: 'studio', local: true, status: 'online' }, ...remotes])}; const base = (window.__coralStatic || '/static/');
            const s = await import(base + 'state.js'); s.state.hub = true; s.state.servers = window.__servers; const t = await import(base + 'servers_tab.js'); t.updateServersTabVisibility(); return true; })()`);
        await ctx.ev(`window.switchNavTab('servers'); true`);
        await ctx.settle(`!!document.querySelector('#server-row-m8') && !!document.getElementById('sb-search')`, 'the servers and their box');
        await typeInto(ctx, '#sb-search', 'machine-1');
        await ctx.settle(`document.querySelector('#servers-bar .list-bar-count').textContent === '1 of 8 servers'`, 'the search');
        return { roots: ['#servers-view'], focus: ['#sb-search'] };
    },
});

// ── The agent list: the Display panel open with a State filter on, and (agents-closed) shut with the summary line (#4947) ──
const agentsPanel = (key, openPanel) => ({
    key,
    async show(ctx, { phone }) {
        await fresh(ctx);
        const TEAMS = ['team-a', 'team-b', 'team-c'];
        const sessions = Array.from({ length: 12 }, (_, i) => { const n = i + 1; const o = { name: 'ag-' + n, session_id: uid(n), agent_type: 'claude', server: 'local', status: 'idle', summary: 'Task ' + n, display_name: 'Agent ' + String(n).padStart(2, '0'), working_directory: '/repo/' + TEAMS[i % 3], branch: 'main', staleness_seconds: 1000 - n * 17, token_cost_usd: n / 10, working: n % 3 === 0, waiting_for_input: n % 6 === 1, board_project: TEAMS[i % 3] }; return o; });
        await ctx.ev(`(async () => { const st = window.__coralStatic || '/static/'; const ws = await import(st + 'websocket.js'); const render = await import(st + 'render.js'); const { state } = await import(st + 'state.js');
            if (state.coralWs) { state.coralWs.onclose = null; state.coralWs.onmessage = null; try { state.coralWs.close(); } catch {} }
            window.__T = { ws, render, state };
            ws.handleCoralMessage({ type: 'coral_update', servers: [], sessions: ${JSON.stringify(sessions)}, session_ids: [] }); render.flushLiveSessionsRender(); render.renderLiveSessions(state.liveSessions); return true; })()`);
        const bar = phone ? '#mobile-agent-list-bar' : '#agent-list-bar';
        const head = phone ? '.mobile-list-header' : '.sidebar-header';
        if (phone) await ctx.ev(`window.switchNavTab('agents'); true`);
        // the bar is drawn when the Display panel first opens (#4995): open it, set the filter, then leave the panel as the panel under test wants it
        await ctx.ev(`(() => { const b = document.querySelector('${head} [data-display-button]'); if (b.getAttribute('aria-expanded') !== 'true') b.click(); return true; })()`);
        await ctx.settle(`!!document.querySelector('${bar} select[id$="state"]')`, 'the bar');
        await pick(ctx, `${bar} #al-state, ${bar} select[id$="state"]`, 'working');
        await ctx.settle(`!document.querySelector('${head} [data-display-badge]').hidden`, 'the filter dot');
        await ctx.ev(`(() => { const b = document.querySelector('${head} [data-display-button]'); if ((b.getAttribute('aria-expanded') === 'true') !== ${openPanel}) b.click(); return true; })()`);
        return { roots: [phone ? '#mobile-agent-list' : '.sidebar'], focus: [phone ? '.mobile-search-input' : '#agent-search-input', ...(openPanel ? [`${bar} select[id$="state"]`] : []), `${head} [data-display-button]`] };
    },
});
PANELS.push(agentsPanel('agents', true), agentsPanel('agents-closed', false));

// ── Tasks (a board's tasks in the dock; the menu open, a status chip) ──
PANELS.push({
    key: 'tasks',
    async prepare(ctx) {
        await fresh(ctx);
        ctx.TBOARD = 'themes-t-' + STAMP;
        await ctx.ev(`(async () => { const post = async (path, body) => { const r = await fetch('/api/board/${ctx.TBOARD}' + path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }); if (!r.ok) throw new Error(path + ': ' + await r.text()); return r.json(); };
            for (let i = 0; i < 4; i++) { const t = await post('/tasks', { title: 'gamma done ' + i, created_by: 'Operator', priority: i % 2 ? 'high' : 'low' }); await post('/tasks/' + t.id + '/complete', { subscriber_id: 'worker', message: 'finished ' + i }); }
            const first = await post('/tasks', { title: 'alpha first', created_by: 'Operator', priority: 'critical' }); await post('/tasks', { title: 'alpha blocked', created_by: 'Operator', blocked_by: [first.id] });
            for (let i = 0; i < 4; i++) await post('/tasks', { title: 'gamma open ' + i, created_by: 'Operator' }); return true; })()`);
    },
    async show(ctx, { phone }) {
        await fresh(ctx);
        const B = ctx.TBOARD;
        await ctx.ev(`(async () => { const tasks = await import((window.__coralStatic||'/static/')+'tasks.js'); const { state } = await import((window.__coralStatic||'/static/')+'state.js'); tasks.stopBoardTaskPoll();
            state.currentSession = { type: 'live', name: 'tk-bot', session_id: 'tk-bot-id', board_project: ${JSON.stringify(B)} }; state.selectedTeam = ${JSON.stringify(B)}; const sec = document.getElementById('board-tasks-section'); if (sec) sec.style.display = '';
            const { showView } = await import((window.__coralStatic||'/static/')+'utils.js'); showView('live-session-view'); const side = document.getElementById('agentic-state'); side.classList.remove('collapsed'); side.style.width = '680px'; return true; })()`);
        if (phone) await ctx.ev(`window.toggleAgenticPanel(true); true`);
        await ctx.settle(`document.getElementById('agentic-tab-tasks').getClientRects().length > 0`, 'the dock');
        await ctx.ev(`document.getElementById('agentic-tab-tasks').click(); true`);
        await ctx.ev(`(async () => { const tasks = await import((window.__coralStatic||'/static/')+'tasks.js'); await tasks.loadBoardTasks(${JSON.stringify(B)}); return true; })()`);
        await ctx.settle(`!!document.querySelector('#board-task-bar .list-bar-search') && document.querySelectorAll('#board-task-list .board-task-item').length > 2`, 'the bar and the tasks');
        await pick(ctx, '#tk-status', 'open').catch(() => {});
        await ctx.ev(`(() => { const s = document.querySelector('#board-task-bar select'); return !!s; })()`);
        await openMenu(ctx, '#board-task-bar');
        await ctx.ev(`new Promise(r => setTimeout(r, 400))`);
        return { roots: ['#board-task-bar', '#board-task-list'], focus: ['#tk-search', '#board-task-bar .list-bar-toggle'] };
    },
});

// ── Board search (the tools row: search, Filters menu open, a From chip) ──
PANELS.push({
    key: 'board',
    async prepare(ctx) {
        await fresh(ctx);
        ctx.BBOARD = 'themes-b-' + STAMP;
        await ctx.ev(`(async () => { for (let i = 0; i < 24; i++) { const r = await fetch('/api/board/${ctx.BBOARD}/messages', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'dev-' + (i % 4), content: i % 3 === 0 ? 'Status ' + i + ': finished the review of retry budget' : (i % 5 === 0 ? '@Operator can you look at item ' + i + '?' : 'Update ' + i + ' on the retry helper') }) }); if (!r.ok) throw new Error(await r.text()); } return true; })()`);
    },
    async show(ctx) {
        await fresh(ctx);
        await ctx.ev(`window.selectBoardProject(${JSON.stringify(ctx.BBOARD)}); true`);
        await ctx.settle(`document.querySelectorAll('#mb-messages > .mb-message').length >= 20 && !!document.querySelector('#mb-tools .list-bar-search')`, 'the board and its tools');
        await typeInto(ctx, '#bm-search', 'retry');
        await ctx.settle(`/matches/.test((document.querySelector('#mb-tools .mb-search-results') || {}).textContent || '') && document.querySelectorAll('#mb-messages mark.mb-hit').length > 0`, 'the matches marked');
        await openMenu(ctx, '#mb-tools');
        await pick(ctx, '#mb-tools select[id$="from"], #bm-from', 'dev-2').catch(() => {});
        await ctx.ev(`new Promise(r => setTimeout(r, 500))`);
        return { roots: ['#messageboard-view'], focus: ['#bm-search', '#mb-tools .list-bar-filter-btn'] };
    },
});

// ── Team Artifacts (a stand-in for the route, 14 artifacts; the menu open, an Origin chip) ──
const HEX = n => n.toString(16).padStart(2, '0').repeat(32);
const TA_DATA = (() => { const kinds = [['image/png', 'png'], ['application/pdf', 'pdf'], ['text/plain', 'txt'], ['application/zip', 'zip']]; const names = ['alpha', 'bravo', 'charlie', 'delta', 'echo', 'foxtrot', 'golf', 'hotel', 'india', 'juliet', 'kilo', 'lima', 'zebra-plan', 'mike'];
    return names.map((n, i) => { const [mt, ext] = kinds[i % 4]; const source = i % 5 === 3 ? 'received' : i % 5 === 4 ? 'shared' : 'task';
        return { id: HEX(i + 1), name: `${n}.${ext}`, media_type: mt, size: (i + 1) * 100, task_id: 100 + i, task_title: 'Task ' + (i + 1), created_at: new Date(Date.UTC(2026, 9, 1 + (13 - i), 10, 0, 0)).toISOString(), uri: 'coral://artifacts/' + HEX(i + 1), available: true, inline: false, reference_count: 1, source, produced_by: i % 2 ? 'Dev One' : 'Dev Two', sent_by: source !== 'task' ? 'Dev One' : undefined }; }); })();
PANELS.push({
    key: 'artifacts',
    async show(ctx) {
        await fresh(ctx);
        await ctx.ev(`(() => { const R = window.fetch; window.__data = ${JSON.stringify(TA_DATA)}; const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin); const m = url.pathname.match(/^\\/api\\/board\\/([^/]+)\\/artifacts$/);
                if (m && (o.method || 'GET').toUpperCase() === 'GET') { const q = url.searchParams; let items = window.__data.slice(); if (q.get('origin')) items = items.filter(i => i.source === q.get('origin')); if (q.get('q')) { const t = q.get('q').toLowerCase(); items = items.filter(i => i.name.includes(t)); }
                    const page = items.slice(0, 5); const body = { project: decodeURIComponent(m[1]), artifacts: page, has_more: items.length > 5, truncated: false }; if (['q', 'kind', 'origin', 'by', 'kept', 'since', 'until', 'sort', 'cursor'].some(k => q.has(k))) { body.total = items.length; body.all = window.__data.length; } return json(body); }
                if (url.pathname === '/api/system/artifact-store/kept') return json({ kept: [] });
                if (url.pathname.startsWith('/api/') && !url.pathname.startsWith('/api/saved') && /artifacts/.test(url.pathname)) return json({});
                return R(u, o); }; return true; })()`);
        await ctx.ev(`(async () => { const {state} = await import((window.__coralStatic||'/static/')+'state.js'); const files = await import((window.__coralStatic||'/static/')+'changed_files.js');
            state.currentSession = { type: 'live', name: 'ta-ui', session_id: 'ta-a', board_project: 'themes-ta' }; const {showView} = await import((window.__coralStatic||'/static/')+'utils.js'); showView('live-session-view');
            const side = document.getElementById('agentic-state'); side.classList.remove('collapsed'); side.style.width = '680px'; files.initFileSearch(); files.syncFilesViewerSession(); return true; })()`);
        if (ctx.__phone) await ctx.ev(`window.toggleAgenticPanel(true); true`);
        await ctx.ev(`document.getElementById('agentic-tab-team-artifacts').click(); true`);
        await ctx.settle(`!!document.querySelector('#agentic-panel-team-artifacts .list-bar-search') && document.querySelectorAll('tr.team-artifact-row').length > 0`, 'the list and its search bar');
        await openMenu(ctx, '#agentic-panel-team-artifacts');
        await pick(ctx, '#ta-origin', 'received');
        await ctx.settle(`!!document.querySelector('#agentic-panel-team-artifacts .list-bar-chip')`, 'a chip');
        await ctx.ev(`new Promise(r => setTimeout(r, 300))`);
        return { roots: ['#agentic-panel-team-artifacts'], focus: ['#agentic-panel-team-artifacts .list-bar-search', '#agentic-panel-team-artifacts .list-bar-toggle'] };
    },
});

// ── Activity and History events (a stand-in agent with 40 events; the menu open, a Type chip) ──
const EVENTS = [...Array.from({ length: 20 }, (_, i) => ({ event_type: 'tool_use', tool_name: 'Edit', summary: `Edit file-${String(i).padStart(2, '0')}.go` })),
    ...Array.from({ length: 10 }, (_, i) => ({ event_type: 'tool_use', tool_name: 'Read', summary: `Read notes-${i}.md` })), ...Array.from({ length: 5 }, (_, i) => ({ event_type: 'prompt_submit', tool_name: '', summary: `Fix the login bug ${i}` })),
    ...Array.from({ length: 5 }, (_, i) => ({ event_type: 'notification', tool_name: '', summary: `Heads up ${i}` }))];
async function standIn(ctx, label) {
    const httpJson = async (method, path, body, headers = {}) => { const r = await fetch(ctx.BASE + path, { method, headers: { 'Content-Type': 'application/json', ...headers }, body: body === undefined ? undefined : JSON.stringify(body) }); let data = null; try { data = await r.json(); } catch { /* none */ } return { status: r.status, data }; };
    const s = await launchStandIn(httpJson, { displayName: label });
    (ctx.cleanup = ctx.cleanup || []).push(() => s.stop()); // before the check: a stand-in with no session has still set cli_path_claude (#5077)
    if (!s.sid) throw new Error('no stand-in agent: ' + s.diag);
    const list = await (await fetch(ctx.BASE + '/api/sessions/live')).json().catch(() => []);
    const name = ((Array.isArray(list) ? list : list.sessions || []).find(x => x.session_id === s.sid) || {}).name || s.name;
    for (const e of EVENTS) { const r = await httpJson('POST', `/api/sessions/live/${encodeURIComponent(name)}/events`, { ...e, session_id: s.sid }, { 'X-Coral-Session-Token': s.token }); if (r.status >= 300) throw new Error('seed ' + r.status); }
    return { sid: s.sid, name };
}
PANELS.push({
    key: 'activity',
    async prepare(ctx) { ctx.ACT = await standIn(ctx, 'Themes Activity'); },
    async show(ctx, { phone }) {
        await fresh(ctx);
        await ctx.ev(`(async () => { const st = window.__coralStatic || '/static/'; const T = window.T = { state: (await import(st + 'state.js')).state, as: await import(st + 'agentic_state.js') };
            T.state.currentSession = { name: '${ctx.ACT.name}', type: 'live', session_id: '${ctx.ACT.sid}', agent_type: 'claude' }; (await import(st + 'utils.js')).showView('live-session-view'); T.as.switchAgenticTab('activity', 'top'); const side = document.getElementById('agentic-state'); side.classList.remove('collapsed'); side.style.width = '680px';
            if (${phone}) window.toggleAgenticPanel && window.toggleAgenticPanel(true); await T.as.loadAgentEvents('${ctx.ACT.name}', '${ctx.ACT.sid}'); return true; })()`);
        await ctx.settle(`!!document.querySelector('#event-search-bar .list-bar-search') && document.querySelectorAll('#agentic-state-events .event-item').length > 5`, 'the bar and the feed');
        await openMenu(ctx, '#event-search-bar');
        await pick(ctx, '#ev-type', 'prompt');
        await ctx.settle(`!!document.querySelector('#event-search-bar .list-bar-chip')`, 'a chip');
        await ctx.ev(`new Promise(r => setTimeout(r, 400))`);
        return { roots: ['#event-search-bar', '#agentic-state-events'], focus: ['#ev-search', '#event-search-bar .list-bar-filter-btn'] };
    },
});
PANELS.push({
    key: 'history',
    async prepare(ctx) { ctx.HIS = await standIn(ctx, 'Themes History'); },
    async show(ctx) {
        await fresh(ctx);
        await ctx.ev(`(async () => { const st = window.__coralStatic || '/static/'; window.T = { ht: await import(st + 'history_tabs.js') }; document.getElementById('history-session-view').style.display = 'flex'; document.getElementById('history-tab-activity').classList.add('active'); await T.ht.loadHistoryEvents('${ctx.HIS.sid}'); return true; })()`);
        await ctx.settle(`!!document.querySelector('#history-event-search-bar .list-bar-search') && document.querySelectorAll('#history-events-list .event-item').length > 5`, 'the bar and the events');
        await openMenu(ctx, '#history-event-search-bar');
        await pick(ctx, '#hev-type', 'prompt');
        await ctx.settle(`!!document.querySelector('#history-event-search-bar .list-bar-chip')`, 'a chip');
        await ctx.ev(`new Promise(r => setTimeout(r, 400))`);
        return { roots: ['#history-session-view'], focus: ['#hev-search', '#history-event-search-bar .list-bar-filter-btn'] };
    },
});

// ── The dock's tab strip with a count on every tab (QA's census: #saved-tab-count read 4.4:1 in two a11y_dashboard runs); each tab active in turn ──
for (const tab of ['files', 'tasks', 'saved', 'activity']) {
    PANELS.push({
        key: 'tabs-' + tab,
        async show(ctx, { phone }) {
            await fresh(ctx);
            await mockAgents(ctx, [{ id: 'qa-a-id', name: 'QA Alpha', team: 'th-a', job: 'QA Engineer' }]);
            await ctx.settle(`(window.__qaState.liveSessions || []).some(s => s.session_id === 'qa-a-id')`, 'agent');
            await ctx.ev(`window.selectLiveSession('QA Alpha', 'claude', 'qa-a-id', 'local'); true`);
            await ctx.ev(`document.getElementById('agentic-state').classList.remove('collapsed'); if (${phone}) window.toggleAgenticPanel(true); true`);
            await ctx.ev(`window.switchAgenticTab(${JSON.stringify(tab)}, 'top'); for (const el of document.querySelectorAll('.agentic-tab-count')) if (!el.textContent) el.textContent = '3'; true`);
            await ctx.ev(`new Promise(r => setTimeout(r, 300))`);
            return { roots: ['#agentic-state .agentic-state-tabs, #agentic-block-top .agentic-tabs, #agentic-state'], focus: [] };
        },
    });
}

// ── The dialogs with the most controls (#4877): Settings, the Launch dialog's agent form and Restart, in the open dialog's own box ──
const modalShown = (ctx, id) => ctx.settle(`!!document.getElementById('${id}') && getComputedStyle(document.getElementById('${id}')).display !== 'none'`, `the ${id} dialog`);
PANELS.push({
    key: 'dialog-settings',
    async show(ctx) {
        await fresh(ctx);
        await ctx.ev(`window.showSettingsModal(); true`);
        await modalShown(ctx, 'settings-modal');
        return { roots: ['#settings-modal .modal-content'] };
    },
}, {
    key: 'dialog-launch',
    async show(ctx) {
        await fresh(ctx);
        await ctx.ev(`window.showLaunchModal(); window._selectLaunchType('agent'); true`);
        await ctx.settle(`!!document.querySelector('#launch-agent-acf .acf-agent-type')`, 'the agent form');
        return { roots: ['#launch-modal .modal-content'] };
    },
}, {
    key: 'dialog-restart',
    async show(ctx) {
        await fresh(ctx);
        await ctx.ev(`T.state.currentSession = { name: 'ag', display_name: 'Agent', session_id: 'sid-r', type: 'live', agent_type: 'claude', model: '', prompt: 'x', capabilities: null }; window.restartSession(); true`);
        await ctx.settle(`!!document.querySelector('#restart-acf .acf-agent-type')`, 'the restart form');
        return { roots: ['#restart-modal .modal-content'] };
    },
});

module.exports = PANELS;
module.exports.helpers = { fresh, api, pick, typeInto, openMenu, dockOpen, mockAgents, launchStandIn };
