// Saved for later is scoped to where it is opened from (#4792), against the real isolated server (the scope's parameters of GET /api/saved are the server's):
//   - on home it shows everything, headed "All saved"; in an agent's view only that agent's chat and the board messages it wrote ("Saved: <agent>"); in a team's view the team's board and
//     its agents' chats ("Saved: <team>"); the Open and Done counts and the dock badge are of the scope;
//   - switching to another agent (or into a team and back home) while the list is open updates it, without the last scope's conversation filter staying;
//   - a search works inside a scope, an empty scope says whom it is empty for, and on a phone the heading and the list fit.
// Two teams, a QA Engineer and a Frontend Dev on each (one name on two teams), seven bookmarks. Drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const { removeBoards } = require('./helpers/board_cleanup');

const hex = i => i.toString(16).padStart(20, '0');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const api = (method, path, body) => ev(`fetch(${JSON.stringify(path)}, { method: ${JSON.stringify(method)}, headers: { 'Content-Type': 'application/json' }, body: ${body ? JSON.stringify(JSON.stringify(body)) : 'undefined'} }).then(async r => ({ status: r.status, body: await r.json().catch(() => ({})) }))`);
    const settle = (cond, label) => waitFor(cond, label, 8000);
    // the sidebar slides open: a picture is taken once it has its width, with the Saved tab showing
    const shotSaved = async name => { await settle(`(() => { const ui = document.getElementById('saved-ui'); if (!ui || !ui.getClientRects().length) return false; const dock = document.getElementById('agentic-state'); if (dock && dock.contains(ui) && dock.getBoundingClientRect().width < Math.min(300, innerWidth - 40)) return false; const h = document.getElementById('saved-scope').getBoundingClientRect(), li = document.querySelector('#saved-list > li'); return (document.body.dataset.savedScope === 'all' || (h.width > 0 && h.top >= 0 && h.top < innerHeight / 2)) && !!li && li.getBoundingClientRect().top < innerHeight; })() /* in the Inbox the "All saved" heading is not drawn (the tab says it, #6300) */`, 'the Saved list on screen (the page on home, the sidebar for an agent or a team)'); await waitFor(`true`, 'a frame', 2000); await shot(name); };
    // what is on top at the middle of the heading is the Saved panel's own (a team's chat once covered the side panel on a phone)
    const onTop = () => ev(`(() => { const h = document.getElementById('saved-scope').getBoundingClientRect(); const e = document.elementFromPoint(h.left + Math.min(h.width, 40) / 2, h.top + h.height / 2); return !!e && !!e.closest('#agentic-panel-saved'); })()`);
    const snap = () => ev(`(() => ({
        heading: (document.getElementById('saved-scope') || {}).textContent,
        rows: [...document.querySelectorAll('#saved-list > li:not(.saved-empty):not(.saved-note)')].map(r => (r.querySelector('.snip') || {}).textContent).sort(),
        empty: (document.querySelector('#saved-list > .saved-empty') || {}).textContent || null,
        tabs: [document.getElementById('saved-count-open').textContent, document.getElementById('saved-count-done').textContent],
        badge: (document.getElementById('saved-tab-count') || {}).textContent || '',
        over: document.documentElement.scrollWidth - document.documentElement.clientWidth,
        headingFits: (() => { const h = document.getElementById('saved-scope'); return h ? h.scrollWidth <= h.clientWidth + 1 : false; })(),
    }))()`);
    const rowsAre = async (heading, rows, tabs, label) => {
        await settle(`document.getElementById('saved-scope').textContent === ${JSON.stringify(heading)} && ${JSON.stringify(rows)}.join('|') === [...document.querySelectorAll('#saved-list > li:not(.saved-empty):not(.saved-note)')].map(r => r.querySelector('.snip').textContent).sort().join('|') && document.getElementById('saved-count-open').textContent === ${JSON.stringify(tabs[0])}`, label)
            .catch(async e => { throw new Error(e.message + ' | ' + JSON.stringify(await snap()) + ' | ' + JSON.stringify(await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'saved_marks.js'); const { state } = await import((window.__coralStatic || '/static/') + 'state.js'); const d = await import((window.__coralStatic || '/static/') + 'dock_page.js'); return { showing: m.savedShowing(), scope: m.currentScope(), cur: state.currentSession && { n: state.currentSession.name, t: state.currentSession.type, bp: state.currentSession.board_project }, dock: d.dockKind(), ev: window.__rc }; })()`))); });
        const s = await snap();
        const openN = tabs[0].replace(/[()]/g, '');
        await settle(`document.getElementById('saved-tab-count').textContent === ${JSON.stringify(openN)}`, 'the dock badge of the scope').catch(() => {});
        const b = await snap();
        check(label + ' (badge ' + openN + ')', s.heading === heading && JSON.stringify(s.rows) === JSON.stringify(rows) && s.tabs.join() === tabs.join() && b.badge === openN, JSON.stringify([s.heading, s.rows, s.tabs, b.badge]));
        return s;
    };

    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        for (const it of (await api('GET', '/api/saved')).body.items) await api('DELETE', `/api/saved/${it.id}`);
        // A board bookmark's author is what the board says wrote the message (#4816), so these are real board messages, written by those names.
        const boardMsg = async (team, from, content) => { const r = await api('POST', `/api/board/${team}/messages`, { subscriber_id: from, content }); assert.ok(r.status < 300, JSON.stringify(r)); return String(r.body.id); };
        const add = async body => { const r = await api('POST', '/api/saved', { agent_type: 'claude', said_at: '2026-10-17T08:00:00Z', ...body }); assert.equal(r.status, 200, JSON.stringify(r)); return r.body; };
        // agents: qa-a (QA Engineer on team-a), fe-a (Frontend Dev on team-a), qa-b (QA Engineer on team-b)
        const done = await add({ kind: 'agent', ref: 'qa-a-id', key: hex(2), title: 'QA Alpha', snippet: 'qa-a chat, done', team: 'team-a' });
        await add({ kind: 'agent', ref: 'qa-a-id', key: hex(1), title: 'QA Alpha', snippet: 'qa-a chat', team: 'team-a' });
        await add({ kind: 'agent', ref: 'fe-a-id', key: hex(3), title: 'FE Alpha', snippet: 'fe-a chat', team: 'team-a' });
        await add({ kind: 'agent', ref: 'qa-b-id', key: hex(4), title: 'QA Beta', snippet: 'qa-b chat', team: 'team-b' });
        await add({ kind: 'board', ref: 'team-a', key: await boardMsg('team-a', 'QA Engineer', 'by the QA Engineer'), title: 'team-a', snippet: 'team-a board, by the QA Engineer', author: 'QA Engineer' });
        await add({ kind: 'board', ref: 'team-a', key: await boardMsg('team-a', 'Orchestrator', 'by the Orchestrator'), title: 'team-a', snippet: 'team-a board, by the Orchestrator', author: 'Orchestrator' });
        await add({ kind: 'board', ref: 'team-b', key: await boardMsg('team-b', 'QA Engineer', 'by the QA Engineer, team-b'), title: 'team-b', snippet: 'team-b board, by the QA Engineer', author: 'QA Engineer' });
        assert.equal((await api('PATCH', `/api/saved/${done.id}`, { done: true })).status, 200);

        const open = async (w, mobile) => {
            await Emulation.setDeviceMetricsOverride({ width: w, height: 900, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`document.addEventListener('coral:route-changed', () => { window.__rc = (window.__rc || 0) + 1; }); true`);
            await ev(`(() => { const jobs = { 'qa-a-id': 'QA Engineer', 'fe-a-id': 'Frontend Dev', 'qa-b-id': 'QA Engineer' };
                const entry = window.__m.entry.bind(window.__m); window.__m.entry = a => ({ ...entry(a), board_job_title: jobs[a.session_id] || '' });
                window.__m.add('QA Alpha', 'A', 4, { sleeping: false, board_project: 'team-a', session_id: 'qa-a-id' });
                window.__m.add('FE Alpha', 'B', 4, { sleeping: false, board_project: 'team-a', session_id: 'fe-a-id' });
                window.__m.add('QA Beta', 'C', 4, { sleeping: false, board_project: 'team-b', session_id: 'qa-b-id' });
                return true; })()`);
            await settle(`(window.__qaState && window.__qaState.liveSessions || []).length === 3`, 'three agents in the live list');
        };
        const openSaved = () => ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'saved.js'); await m.openSaved(); return true; })()`);
        // A person chooses an agent from the agent list, so the Agents tab is on screen (the Inbox page, where home's list was read, is left first).
        const toAgents = async () => { if (await ev(`!!document.getElementById('nav-tab-hub-inbox')?.classList.contains('active')`)) { await ev(`window.switchNavTab('agents'); true`); await settle(`!document.getElementById('nav-tab-hub-inbox').classList.contains('active')`, 'the Agents tab'); } };
        const select = async id => { await toAgents(); return ev(`window.selectLiveSession(${JSON.stringify(id.name)}, 'claude', ${JSON.stringify(id.sid)}, 'local'); true`); };
        const QA_A = { name: 'QA Alpha', sid: 'qa-a-id' }, FE_A = { name: 'FE Alpha', sid: 'fe-a-id' }, QA_B = { name: 'QA Beta', sid: 'qa-b-id' };

        for (const [dev, w, mobile] of [['desktop', 1280, false], ['phone', 390, true]]) {
            console.log(dev);
            await open(w, mobile);
            // home: everything
            await openSaved();
            let s = await rowsAre('All saved', ['fe-a chat', 'qa-a chat', 'qa-b chat', 'team-a board, by the Orchestrator', 'team-a board, by the QA Engineer', 'team-b board, by the QA Engineer'], ['(6)', '(1)'], `${dev}: home shows everything, headed All saved (6 open, 1 done)`);
            await shotSaved(`saved-scope-all-${dev}`);
            { // in the Inbox the tab names the list: no "All saved" heading; and Read the whole message is a quiet link, not a bordered button (#6300)
                const q = await ev(`(() => { const h = document.getElementById('saved-scope'), t = document.querySelector('#saved-list .ibx-saved-toggle'); const c = t && getComputedStyle(t); return { heading: getComputedStyle(h).display, toggle: !!t, border: c && c.borderTopColor, bg: c && c.backgroundColor, underline: c && c.textDecorationLine }; })()`);
                check(`${dev}: no "All saved" heading in the Inbox, and Read the whole message is a link`, q.heading === 'none' && q.toggle && q.bg === 'rgba(0, 0, 0, 0)' && q.border === 'rgba(0, 0, 0, 0)' && q.underline === 'underline', JSON.stringify(q));
            }

            // an agent
            await select(QA_A);
            await openSaved();
            s = await rowsAre('Saved: QA Alpha', ['qa-a chat', 'team-a board, by the QA Engineer'], ['(2)', '(1)'], `${dev}: QA Alpha: its chat and the board message the QA Engineer wrote, not the Orchestrator's, FE Alpha's or team-b's namesake`);
            // the done one is its own
            await ev(`document.getElementById('saved-tab-done').click(); true`);
            await rowsAre('Saved: QA Alpha', ['qa-a chat, done'], ['(2)', '(1)'], `${dev}: its Done tab has its one done message (counts of the scope: 2 open, 1 done)`);
            await ev(`document.getElementById('saved-tab-open').click(); true`);
            await rowsAre('Saved: QA Alpha', ['qa-a chat', 'team-a board, by the QA Engineer'], ['(2)', '(1)'], `${dev}: back on Open`);
            await shotSaved(`saved-scope-agent-${dev}`);

            // switching agents while the list is open
            await select(FE_A);
            if (mobile) await openSaved(); // on a phone the panel is an overlay that gives way when another page is chosen: it is opened again
            await rowsAre('Saved: FE Alpha', ['fe-a chat'], ['(1)', ''], `${dev}: switching to FE Alpha updates the list (its chat only: it wrote nothing on the board)`);
            await select(QA_B);
            if (mobile) await openSaved(); // on a phone the panel is an overlay that gives way when another page is chosen: it is opened again
            await rowsAre('Saved: QA Beta', ['qa-b chat', 'team-b board, by the QA Engineer'], ['(2)', ''], `${dev}: switching to QA Beta: its chat and team-b's message by a QA Engineer`);

            // a search inside the scope, and an empty result
            await ev(`(() => { const e = document.getElementById('sv-search'); e.focus(); return true; })()`);
            await ev(`(() => { const e = document.getElementById('sv-search'); e.value = 'board'; e.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
            await settle(`document.querySelectorAll('#saved-list > li:not(.saved-empty)').length === 1`, 'the search in the scope');
            s = await snap();
            check(`${dev}: a search inside the scope covers the scope (one match of two)`, s.rows.join() === 'team-b board, by the QA Engineer' && s.tabs[0] === '(2)', JSON.stringify([s.rows, s.tabs]));
            await ev(`document.querySelector('#saved-bar .list-bar-clear-q').click(); true`);

            // a team
            await toAgents();
            await ev(`Promise.resolve(window.enterTeamContext('team-a', { tab: 'team-view' })).then(() => true)`); // (the team view is a lazy module, #5364: the first call loads it, so wait for it)
            await openSaved(); // the team page opens on its own tab: the Saved tab is shown beside it // on a phone the panel is an overlay that gives way when another page is chosen: it is opened again
            await rowsAre('Saved: team-a', ['fe-a chat', 'qa-a chat', 'team-a board, by the Orchestrator', 'team-a board, by the QA Engineer'], ['(4)', '(1)'], `${dev}: team-a: its board's messages and its agents' chats, nothing of team-b`);
            await shotSaved(`saved-scope-team-${dev}`);
            s = await snap();
            check(`${dev}: the heading fits and the page does not scroll sideways`, s.headingFits && s.over <= 0, JSON.stringify([s.headingFits, s.over]));
            check(`${dev}: in the team's page the Saved panel is what is on screen, not the team's chat over it`, await onTop());

            // home again
            await ev(`window.exitTeamContext({ goBack: false, force: true }); true`);
            await settle(`!document.getElementById('agentic-state').classList.contains('team-context')`, 'left the team');
            await openSaved();
            await rowsAre('Saved: QA Beta', ['qa-b chat', 'team-b board, by the QA Engineer'], ['(2)', ''], `${dev}: leaving the team, the agent underneath is the scope again`);
            await ev(`window.selectLiveSession('', '', '', 'local'); true`).catch(() => {});
            await openSaved(); // nothing is chosen, so the scope is everything's and the list is the Inbox's: the bookmark opens it
            await rowsAre('All saved', ['fe-a chat', 'qa-a chat', 'qa-b chat', 'team-a board, by the Orchestrator', 'team-a board, by the QA Engineer', 'team-b board, by the QA Engineer'], ['(6)', '(1)'], `${dev}: home again shows everything`);
        }

        // an agent with nothing saved
        await open(1280, false);
        await ev(`window.__m.add('Lonely', 'L', 2, { sleeping: false, board_project: null, session_id: 'lonely-id' }); true`);
        await select({ name: 'Lonely', sid: 'lonely-id' });
        await openSaved();
        await settle(`!!document.querySelector('#saved-list > .saved-empty')`, 'the empty line');
        const s = await snap();
        check('an agent with nothing saved says whom it is empty for', s.heading === 'Saved: Lonely' && /Nothing saved from Lonely yet/.test(s.empty || ''), JSON.stringify([s.heading, s.empty]));
    } catch (e) {
        console.error(e && e.stack || e);
        results.push(false);
    } finally {
        await require('./helpers/saved_cleanup').clearSaved(api).catch(() => {}); // the bookmarks this suite saved are taken off again (#4916)
        await removeBoards(ev, ['team-a', 'team-b']);
        try { await client.close(); } catch (_) {}
    }
    const failed = results.filter(r => !r).length;
    console.log(failed ? `${failed} FAILED` : `PASS saved scope: ${results.length} checks`);
    process.exitCode = failed ? 1 : 0;
})();
