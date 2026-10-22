// CSP stage 2, slice 6 (#3645): the sidebar, its menus, the history list and the board chat panel on
// delegated data-on-* actions, with a real terminal agent on a real team (launched, then killed).
//  - An agent row opens the agent; its menu (⋮) opens, and each item calls the function its inline
//    handler did with the same arguments (spied, so nothing is killed or renamed here) and closes
//    the menu; a bad mode or agent type does nothing;
//  - the team and group menus likewise (board / group / folder arguments);
//  - Kill, Restart, Rename, a group's and a team's Kill and Reset are on the shared destructive list;
//  - the group header collapses its group; a folder's copy button copies the exact path;
//  - the history list: a row opens its session and is the one marked active (updateSidebarActive now
//    reads data-session-id, not the onclick text); Next/Prev pages;
//  - no inline handler is left in the sidebar.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

const SPIED = ['showInfoDirect', 'killSessionDirect', 'restartDirect', 'renameAgent', 'setAgentIcon', 'attachDirect', 'toggleAgentSleep', 'toggleAgentArchive',
    'moveSessionUp', 'moveSessionDown', 'killBoard', 'resetTeam', 'killGroup', 'toggleTeamSleep', 'shareAgentTeam', 'saveTeamFromSidebar', 'distillTeamKnowledge',
    'showTeamTokenUsage', 'showTeamAvailabilityWorkspace', 'showTeamWorkingModeWorkspace', 'moveGroupUp', 'moveGroupDown', 'showAddAgentToBoard', 'launchTerminalToBoard',
    'selectHistorySession', 'loadHistoryPage', 'toggleGroupCollapse', 'enterTeamContext', 'setBoardAccentColor'];

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
const { removeBoards, removeTeams } = require('./helpers/board_cleanup');
    const until = (expr, what, ms = 10000) => waitFor(expr, what, { timeout: ms, soft: true });
    const MOD = `(window.__coralStatic||'/static/')`;
    let agent = null;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            window.__copied = [];
            Object.defineProperty(navigator, 'clipboard', { value: { writeText: t => { window.__copied.push(t); return Promise.resolve(); } }, configurable: true });
        })();` });
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();

        agent = await ev(`fetch('/api/sessions/launch-team', { method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ board_name: 'sidebar-3645', working_dir: ${JSON.stringify(require('node:os').tmpdir())}, agent_type: 'terminal', agents: [{ name: 'Sidebar QA', prompt: 'x' }] }) })
            .then(r => r.json()).then(j => ({ ...j.agents[0], board: j.board }))`);
        const row = `document.querySelector('#live-sessions-list [data-on-click="session-open"][data-session-id="${agent.session_id}"]')`;
        await until(`!!${row}`, 'the agent row');

        // ── The row opens the agent ────────────────────────────────────────
        await ev(`${row}.click(); true`);
        await until(`import(${MOD} + 'state.js').then(({ state }) => state.currentSession && state.currentSession.session_id === ${JSON.stringify(agent.session_id)})`, 'the agent selected');
        check('an agent row opens the agent', await ev(`import(${MOD} + 'state.js').then(({ state }) => state.currentSession && state.currentSession.session_id === ${JSON.stringify(agent.session_id)})`));

        // Spy the functions the menus call (nothing destructive runs).
        await ev(`(() => { window.__calls = []; window.__real = {}; for (const n of ${JSON.stringify(SPIED)}) { window.__real[n] = window[n]; window[n] = (...a) => { window.__calls.push([n, ...a]); }; } return true; })()`);

        // ── The agent's menu ───────────────────────────────────────────────
        const agentItem = act => `[...document.querySelectorAll('#live-sessions-list [data-on-click="${act}"][data-session-id="${agent.session_id}"]')][0]`;
        await ev(`(() => { const li = ${row}; li.querySelector('[data-on-click="kebab-toggle"]').click(); return true; })()`);
        // The menu is the ⋮ button's next sibling (toggleSidebarKebab shows it with display: block).
        const opened = await ev(`(() => { const m = ${row}.querySelector('[data-on-click="kebab-toggle"]').nextElementSibling; return !!m && m.classList.contains('sidebar-kebab-menu') && m.style.display === 'block'; })()`);
        check('the ⋮ button opens the agent\'s menu', opened);
        // The menu passes the agent's name as the list has it (s.name), its type and its id.
        const liveName = await ev(`import(${MOD} + 'state.js').then(({ state }) => (state.liveSessions.find(s => s.session_id === ${JSON.stringify(agent.session_id)}) || {}).name)`);
        const want = [liveName, 'terminal', agent.session_id];
        const calls = {};
        for (const act of ['agent-info', 'agent-kill', 'agent-restart', 'agent-rename', 'agent-icon']) {
            calls[act] = await ev(`(() => { window.__calls.length = 0; const el = ${agentItem(act)}; if (!el) return 'missing'; el.click(); return window.__calls.slice(); })()`);
        }
        const expect = { 'agent-info': 'showInfoDirect', 'agent-kill': 'killSessionDirect', 'agent-restart': 'restartDirect', 'agent-rename': 'renameAgent', 'agent-icon': 'setAgentIcon' };
        const ok = Object.entries(expect).every(([a, fn]) => Array.isArray(calls[a]) && calls[a].length === 1 && calls[a][0][0] === fn && JSON.stringify(calls[a][0].slice(1)) === JSON.stringify(want));
        check('each agent menu item calls its function with the agent\'s name, type and id', ok, JSON.stringify(calls));
        const menuClosed = await ev(`(() => { const m = ${row}.querySelector('[data-on-click="kebab-toggle"]').nextElementSibling; return m.style.display === 'none'; })()`);
        check('a menu item closes the menu', menuClosed);
        const sleep = await ev(`(() => { window.__calls.length = 0; const el = ${agentItem('agent-sleep')}; if (!el) return 'missing'; el.click(); const good = window.__calls.slice();
            window.__calls.length = 0; const bad = el.cloneNode(true); bad.dataset.mode = 'kill'; document.body.appendChild(bad); bad.click(); bad.remove();
            const t = el.cloneNode(true); t.dataset.agentType = 'a b'; document.body.appendChild(t); t.click(); t.remove();
            return { good, bad: window.__calls.slice() }; })()`);
        check('Sleep/Wake passes its mode; a mode or agent type outside its pattern does nothing',
            Array.isArray(sleep.good) && sleep.good.length === 1 && ['sleep', 'wake'].includes(sleep.good[0][4]) && sleep.bad.length === 0, JSON.stringify(sleep));

        // ── The team's menu and its group ──────────────────────────────────
        const teamItem = act => `document.querySelector('[data-on-click="${act}"][data-board="${agent.board}"]')`;
        const team = {};
        for (const act of ['team-kill', 'team-share', 'team-save', 'team-tokens']) {
            team[act] = await ev(`(() => { window.__calls.length = 0; const el = ${teamItem(act)}; if (!el) return 'missing'; el.click(); return window.__calls.slice(); })()`);
        }
        const teamFn = { 'team-kill': 'killBoard', 'team-share': 'shareAgentTeam', 'team-save': 'saveTeamFromSidebar', 'team-tokens': 'showTeamTokenUsage' };
        check('each team menu item calls its function with the team\'s board', Object.entries(teamFn).every(([a, fn]) => Array.isArray(team[a]) && team[a].length === 1 && team[a][0][0] === fn && team[a][0][1] === agent.board), JSON.stringify(team));
        const collapse = await ev(`(() => { window.__calls.length = 0; const h = document.querySelector('[data-on-click="group-collapse"][data-group="${agent.board}"], [data-on-click="group-collapse"][data-group$="${agent.board}"]'); if (!h) return 'missing'; h.click(); return window.__calls.slice(); })()`);
        check('the team\'s header collapses its group', Array.isArray(collapse) && collapse.length === 1 && collapse[0][0] === 'toggleGroupCollapse', JSON.stringify(collapse));

        const listed = await ev(`import(${MOD} + 'actions.js').then(a => ['agent-kill', 'agent-restart', 'agent-rename', 'group-kill', 'team-kill', 'team-reset'].every(n => a.DESTRUCTIVE_ACTIONS.has(n)))`);
        check('Kill, Restart, Rename, a group\'s and a team\'s Kill and Reset are on the shared destructive list', listed);

        // ── A folder's copy button copies the exact path ───────────────────
        await ev(`(() => { const b = document.createElement('button'); b.setAttribute('data-on-click', 'folder-copy'); b.dataset.dir = "/tmp/a&b <c>'d"; document.body.appendChild(b); b.click(); b.remove(); return true; })()`);
        await until(`window.__copied.length > 0`, 'the copied path', 3000);
        check('a folder copy button copies the exact path', (await ev(`window.__copied`)).includes("/tmp/a&b <c>'d"), JSON.stringify(await ev(`window.__copied`)));

        // ── The history list ───────────────────────────────────────────────
        const hist = await ev(`(async () => {
            const render = await import(${MOD} + 'render.js'); const controls = await import(${MOD} + 'controls.js'); const { state } = await import(${MOD} + 'state.js');
            const sessions = [1, 2].map(i => ({ session_id: 'hist-3645-' + i, title: 'Old ' + i, updated_at: '2026-10-15T0' + i + ':00:00Z', type: 'session', message_count: 3, first_message: 'x', summary: 'y', agent_type: 'claude' }));
            render.renderHistorySessions(sessions, 150, 2, 50); // page 2 of 3: Prev and Next both live
            const li = document.querySelector('#history-sessions-list [data-on-click="history-open"][data-session-id="hist-3645-2"]');
            if (!li) return { li: false };
            window.__calls.length = 0; li.click(); const opened = window.__calls.slice();
            state.currentSession = { type: 'history', name: 'hist-3645-2' }; controls.updateSidebarActive();
            const active = [...document.querySelectorAll('#history-sessions-list li.active')].map(l => l.dataset.sessionId);
            window.__calls.length = 0; document.querySelector('[data-on-click="history-page"][data-page="3"]')?.click(); document.querySelector('[data-on-click="history-page"][data-page="1"]')?.click();
            return { li: true, opened, active, pages: window.__calls.slice() };
        })()`);
        check('a history row opens its session and is the one marked active', hist.li && JSON.stringify(hist.opened) === JSON.stringify([['selectHistorySession', 'hist-3645-2']]) && JSON.stringify(hist.active) === JSON.stringify(['hist-3645-2']), JSON.stringify(hist));
        check('Next and Prev load their pages', JSON.stringify(hist.pages) === JSON.stringify([['loadHistoryPage', 3], ['loadHistoryPage', 1]]), JSON.stringify(hist.pages));

        await ev(`(() => { for (const n of Object.keys(window.__real)) window[n] = window.__real[n]; return true; })()`);
        const inline = await ev(`[...document.querySelectorAll('#sidebar *, .sidebar *, #live-sessions-list *, #history-sessions-list *')].flatMap(el => [...el.attributes].filter(a => /^on/i.test(a.name)).map(a => el.tagName + '.' + el.className + ' ' + a.name))`);
        check('no inline handler left in the sidebar', inline.length === 0, JSON.stringify(inline.slice(0, 5)));
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        if (agent && agent.session_name) {
            try { await ev(`fetch('/api/sessions/live/' + encodeURIComponent(${JSON.stringify(agent.session_name)}) + '/kill', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ agent_type: 'terminal', session_id: ${JSON.stringify(agent.session_id)} }) }).then(() => true)`); } catch { /* best effort */ }
        }
        await removeBoards(ev, [agent && agent.board]); await removeTeams(ev, [agent && agent.board]); // the team record launch-team made (#5343)
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
