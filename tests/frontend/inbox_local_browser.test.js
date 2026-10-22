// The Inbox on a regular (local) Coral (#4811), against the real isolated server: the dashboard's own Inbox: two tabs, Needs you and Saved for later (one panel at a time, as the hub's), no frame.
//   - the Inbox tab is on a plain local Coral, right after Agents, with the number of agents waiting for input as its badge; its page lists those agents under "Needs you" (each opens that agent) and
//     everything saved under "Saved for later" (the one Saved list, moved in: #saved-view inside #inbox-local), with no right-hand panel;
//   - the top bar's bookmark, where the scope is all (home and any page without an agent or team), opens the Inbox at its Saved part; Escape and a second press leave it for the page it was opened from;
//   - an agent's page and a team's page keep the scoped list in the right sidebar, and the list goes back to the sidebar when the Inbox is left;
//   - Open board from the page lands at the message, the jump bar's Back to Saved returns to the Inbox; on a phone the Inbox is in the view dropdown.
// The hub path (the frame) is inbox_tab_browser's. Seven bookmarks on two teams, three mock agents (one waiting). Drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const { removeBoards } = require('./helpers/board_cleanup');

const tabBtn = name => `document.getElementById('inbox-tab-${name}').click(); true`; // the Inbox's two tabs (as the hub's): one panel shows at a time

const hex = i => i.toString(16).padStart(20, '0');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const api = (method, path, body) => ev(`fetch(${JSON.stringify(path)}, { method: ${JSON.stringify(method)}, headers: { 'Content-Type': 'application/json' }, body: ${body ? JSON.stringify(JSON.stringify(body)) : 'undefined'} }).then(async r => ({ status: r.status, body: await r.json().catch(() => ({})) }))`);
    const settle = (cond, label) => waitFor(cond, label, 8000);
    const key = async (k, code) => { await Input.dispatchKeyEvent({ type: 'keyDown', key: k, code: k, windowsVirtualKeyCode: code }); await Input.dispatchKeyEvent({ type: 'keyUp', key: k, code: k, windowsVirtualKeyCode: code }); };
    const state = () => ev(`(() => { const vis = id => { const e = document.getElementById(id); return !!e && getComputedStyle(e).display !== 'none' && e.getClientRects().length > 0; };
        const ui = document.getElementById('saved-ui'); const dock = document.getElementById('agentic-state'); const tabs = [...document.querySelectorAll('.top-nav-tabs > .top-nav-tab[data-arg]')].filter(t => !t.hidden && t.style.display !== 'none').map(t => t.dataset.arg);
        return { inbox: vis('inbox-view'), local: vis('inbox-local'), frame: !!document.getElementById('inbox-frame'), saved: vis('saved-view'), uiIn: ui && (ui.parentElement.id), heading: (document.getElementById('saved-scope') || {}).textContent,
            rows: [...document.querySelectorAll('#saved-list > li:not(.saved-empty):not(.saved-note)')].map(r => r.querySelector('.snip').textContent).sort(),
            needs: [...document.querySelectorAll('#inbox-needs-list .inbox-needs-item')].map(b => b.textContent.replace(/\\s+/g, ' ').trim()), needsEmpty: vis('inbox-needs-empty'),
            navActive: document.querySelector('.top-nav-tab.active') && document.querySelector('.top-nav-tab.active').dataset.arg, tabs, navBadge: (document.getElementById('nav-tab-hub-inbox-badge') || {}).textContent || '', navLabel: document.getElementById('nav-tab-hub-inbox').getAttribute('aria-label'),
            dockShown: !!dock && getComputedStyle(dock).display !== 'none' && dock.getClientRects().length > 0 && !dock.classList.contains('collapsed') || (!!dock && dock.classList.contains('mobile-panel-overlay')), dockHasSavedTab: vis('agentic-tab-saved'),
            savedBtn: document.getElementById('saved-btn').getAttribute('aria-pressed'), over: document.documentElement.scrollWidth - document.documentElement.clientWidth }; })()`);
    let BOARD = ''; // the board this suite makes, taken away again in the finally (#4959)
    const ALL_ROWS = ['fe-a chat', 'qa-a chat', 'qa-b chat', 'team-a board, by the Orchestrator', 'team-a board, by the QA Engineer', 'team-b board, by the QA Engineer'];

    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        for (const it of (await api('GET', '/api/saved')).body.items) await api('DELETE', `/api/saved/${it.id}`);
        // A board bookmark's author is what the board says wrote the message (#4816): real board messages, written by those names.
        const boardMsg = async (team, from, content) => { const r = await api('POST', `/api/board/${team}/messages`, { subscriber_id: from, content }); assert.ok(r.status < 300, JSON.stringify(r)); return String(r.body.id); };
        const add = async body => { const r = await api('POST', '/api/saved', { agent_type: 'claude', said_at: '2026-10-17T08:00:00Z', ...body }); assert.equal(r.status, 200, JSON.stringify(r)); return r.body; };
        const done = await add({ kind: 'agent', ref: 'qa-a-id', key: hex(2), title: 'QA Alpha', snippet: 'qa-a chat, done', team: 'team-a' });
        await add({ kind: 'agent', ref: 'qa-a-id', key: hex(1), title: 'QA Alpha', snippet: 'qa-a chat', team: 'team-a' });
        await add({ kind: 'agent', ref: 'fe-a-id', key: hex(3), title: 'FE Alpha', snippet: 'fe-a chat', team: 'team-a' });
        await add({ kind: 'agent', ref: 'qa-b-id', key: hex(4), title: 'QA Beta', snippet: 'qa-b chat', team: 'team-b' });
        await add({ kind: 'board', ref: 'team-a', key: await boardMsg('team-a', 'QA Engineer', 'by the QA Engineer'), title: 'team-a', snippet: 'team-a board, by the QA Engineer', author: 'QA Engineer' });
        await add({ kind: 'board', ref: 'team-a', key: await boardMsg('team-a', 'Orchestrator', 'by the Orchestrator'), title: 'team-a', snippet: 'team-a board, by the Orchestrator', author: 'Orchestrator' });
        await add({ kind: 'board', ref: 'team-b', key: await boardMsg('team-b', 'QA Engineer', 'by the QA Engineer, team-b'), title: 'team-b', snippet: 'team-b board, by the QA Engineer', author: 'QA Engineer' });
        assert.equal((await api('PATCH', `/api/saved/${done.id}`, { done: true })).status, 200);

        const open = async (w, mobile, waiting = ['qa-b-id']) => {
            await Emulation.setDeviceMetricsOverride({ width: w, height: 900, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`localStorage.removeItem('coral.inbox.tab'); true`); // a fresh browser: the Inbox opens on Needs you
            await ev(`(() => { const jobs = { 'qa-a-id': 'QA Engineer', 'fe-a-id': 'Frontend Dev', 'qa-b-id': 'QA Engineer' }; const waiting = ${JSON.stringify(waiting)};
                const entry = window.__m.entry.bind(window.__m); window.__m.entry = a => ({ ...entry(a), board_job_title: jobs[a.session_id] || '', waiting_for_input: waiting.includes(a.session_id), waiting_kind: waiting.includes(a.session_id) ? 'permission' : null });
                window.__m.add('QA Alpha', 'A', 4, { sleeping: false, board_project: 'team-a', session_id: 'qa-a-id' });
                window.__m.add('FE Alpha', 'B', 4, { sleeping: false, board_project: 'team-a', session_id: 'fe-a-id' });
                window.__m.add('QA Beta', 'C', 4, { sleeping: false, board_project: 'team-b', session_id: 'qa-b-id' });
                return true; })()`);
            await settle(`(window.__qaState && window.__qaState.liveSessions || []).length === 3`, 'three agents in the live list');
            await ev(`window.__qaState.liveSessions = window.__qaState.liveSessions.map(s => ({ ...s, ...window.__m.entry(window.__m.agents[s.session_id]) })); import((window.__coralStatic || '/static/') + 'inbox_tab.js').then(m => m.updateInboxTab()); true`);
        };
        const rowsShown = (heading, label) => settle(`document.getElementById('saved-scope').textContent === ${JSON.stringify(heading)} && document.querySelectorAll('#saved-list > li:not(.saved-empty):not(.saved-note)').length > 0 && document.getElementById('saved-count-open').textContent !== ''`, label);

        for (const [dev, w, mobile] of [['desktop', 1280, false], ['phone', 390, true]]) {
            console.log(dev);
            await open(w, mobile);
            await settle(`document.getElementById('nav-tab-hub-inbox-badge').textContent === '1'`, 'the waiting badge');
            let s = await state();
            check(`${dev}: the Inbox tab is on a plain local Coral, right after Agents, with the number waiting (1) and says so`, s.tabs.slice(0, 2).join() === 'agents,hub-inbox' && s.navBadge === '1' && s.navLabel === 'Inbox, 1 waiting for input', JSON.stringify([s.tabs, s.navBadge, s.navLabel]));
            check(`${dev}: and no Saved tab of its own`, !s.tabs.includes('saved'), JSON.stringify(s.tabs));

            // the Inbox, from the nav tab (a click, or the phone's dropdown)
            if (mobile) {
                await ev(`document.getElementById('nav-select').click(); true`);
                await settle(`!!document.querySelector('#nav-more-menu [data-arg="hub-inbox"]')`, 'Inbox in the view dropdown');
                const item = await ev(`(() => { const b = document.querySelector('#nav-more-menu [data-arg="hub-inbox"]'); return { text: b.textContent.trim(), badge: (b.querySelector('.nav-tab-badge') || {}).textContent || '' }; })()`);
                check('phone: the view dropdown lists Inbox, with the number waiting', /Inbox/.test(item.text) && item.badge === '1', JSON.stringify(item));
                await ev(`document.querySelector('#nav-more-menu [data-arg="hub-inbox"]').click(); true`);
            } else {
                await ev(`document.getElementById('nav-tab-hub-inbox').click(); true`);
            }
            await settle(`document.getElementById('inbox-needs-list').getClientRects().length > 0`, 'the Inbox, on Needs you');
            s = await state();
            check(`${dev}: the Inbox opens on its Needs you tab, and Saved for later is not on screen`, s.inbox && s.local && !s.saved, JSON.stringify([s.inbox, s.local, s.saved]));
            await ev(tabBtn('saved'));
            await rowsShown('All saved', 'the Inbox with Saved');
            s = await state();
            check(`${dev}: the Inbox is the dashboard's own (no frame): Needs you lists the waiting agent, Saved for later lists all six open items`, s.inbox && s.local && !s.frame && s.needs.length === 1 && /QA Beta/.test(s.needs[0]) && /Needs input/.test(s.needs[0]) && JSON.stringify(s.rows) === JSON.stringify(ALL_ROWS) && s.uiIn === 'saved-view', JSON.stringify([s.inbox, s.local, s.frame, s.needs, s.rows.length, s.uiIn]));
            check(`${dev}: there is no right-hand panel and the page does not scroll sideways`, !s.dockShown && !s.dockHasSavedTab && s.over <= 0, JSON.stringify([s.dockShown, s.dockHasSavedTab, s.over]));
            await shot(`inbox-local-${dev}`);
            // Needs you opens the agent
            await ev(tabBtn('needs'));
            await settle(`document.getElementById('inbox-needs-list').getClientRects().length > 0`, 'Needs you');
            await shot(`inbox-local-needs-${dev}`);
            await ev(`document.querySelector('#inbox-needs-list .inbox-needs-item').click(); true`);
            await settle(`window.T && T.state.currentSession && T.state.currentSession.session_id === 'qa-b-id' && document.querySelector('.top-nav-tab.active').dataset.arg === 'agents'`, 'the waiting agent opened');
            check(`${dev}: a Needs you row opens that agent (the Agents tab, the agent selected)`, true);
            await ev(`window.selectLiveSession('', '', '', 'local'); true`);

            // the bookmark, on home: the Inbox at its Saved part; Escape and the bookmark leave it
            await ev(`window.switchNavTab('agents'); true`);
            await settle(`document.querySelector('.top-nav-tab.active').dataset.arg === 'agents'`, 'home');
            await ev(`document.getElementById('saved-btn').click(); true`);
            await rowsShown('All saved', 'the Inbox from the bookmark');
            s = await state();
            check(`${dev}: home: the bookmark opens the Inbox at Saved for later, the bookmark pressed, the Inbox tab current`, s.inbox && s.saved && s.navActive === 'hub-inbox' && s.savedBtn === 'true' && s.heading === 'All saved', JSON.stringify([s.inbox, s.saved, s.navActive, s.savedBtn]));
            await ev(`document.activeElement && document.activeElement.blur(); true`);
            await key('Escape', 27);
            await settle(`document.querySelector('.top-nav-tab.active').dataset.arg === 'agents'`, 'Escape leaves');
            s = await state();
            check(`${dev}: Escape leaves the Inbox for the tab it was opened from, and the list is back in the sidebar`, !s.inbox && s.navActive === 'agents' && s.uiIn === 'agentic-panel-saved' && s.savedBtn === 'false', JSON.stringify([s.inbox, s.navActive, s.uiIn, s.savedBtn]));
            await ev(`document.getElementById('saved-btn').click(); true`);
            await rowsShown('All saved', 'the Inbox again');
            await ev(`document.getElementById('saved-btn').click(); true`);
            await settle(`document.querySelector('.top-nav-tab.active').dataset.arg === 'agents'`, 'the bookmark leaves');
            check(`${dev}: pressing the bookmark again leaves it too`, true);
            await ev(`window.switchNavTab('agents'); true`);
        }

        // Open board from the page: the board opens at the message, "Back to Saved" returns to the Inbox, Remove stays on it
        console.log('open and back');
        BOARD = 'inbox-local-' + Date.now().toString(36);
        const post = await api('POST', `/api/board/${BOARD}/messages`, { subscriber_id: 'sv-dev', content: 'the message to go back to' });
        assert.ok(post.status < 300, JSON.stringify(post));
        const msgId = String(post.body.id);
        await add({ kind: 'board', ref: BOARD, key: msgId, title: BOARD, snippet: 'the real board message', author: 'sv-dev' });
        await open(1280, false);
        await ev(`document.getElementById('nav-tab-hub-inbox').click(); true`);
        await ev(tabBtn('saved'));
        await settle(`!!document.querySelector('#saved-list .saved-open')`, 'an item that can be opened');
        await ev(`[...document.querySelectorAll('#saved-list .saved-item')].find(l => l.textContent.includes('the real board message')).querySelector('.saved-open').click(); true`);
        await settle(`!!document.getElementById('saved-jumpbar') && !!document.querySelector('#mb-messages [data-message-id="${msgId}"]')`, 'the board open at the message with the jump bar');
        let s = await state();
        check('Open board from the Inbox: the board is on screen at the message, the Inbox is not', !s.inbox && (await ev(`!!document.querySelector('#mb-messages .saved-jump, #mb-messages .chat-search-target')`)), JSON.stringify([s.inbox, s.navActive]));
        await ev(`document.querySelector('#saved-jumpbar .btn').click(); true`);
        await rowsShown('All saved', 'back on the Inbox');
        s = await state();
        check('"Back to Saved" returns to the Inbox (everything), not to a scoped list in the sidebar', s.inbox && s.navActive === 'hub-inbox' && s.uiIn === 'saved-view' && s.heading === 'All saved' && s.rows.length === 7, JSON.stringify([s.inbox, s.navActive, s.uiIn, s.heading, s.rows.length]));
        await ev(`document.querySelector('#saved-list .saved-item .saved-remove').click(); true`);
        await settle(`document.querySelectorAll('#saved-list > li:not(.saved-empty)').length === 6`, 'a removal in the Inbox');
        s = await state();
        check('Remove in the Inbox removes the item and stays on it', s.inbox, JSON.stringify([s.inbox]));

        // nothing waiting: Needs you says so
        console.log('nothing waiting');
        await open(1280, false, []);
        await ev(`document.getElementById('nav-tab-hub-inbox').click(); true`);
        await ev(tabBtn('needs'));
        await settle(`document.getElementById('inbox-needs-empty') && !document.getElementById('inbox-needs-empty').hidden`, 'the empty line');
        s = await state();
        check('with nothing waiting Needs you says so, the badge is empty, Saved for later still lists everything', s.needsEmpty && s.needs.length === 0 && s.navBadge === '' && s.navLabel === null, JSON.stringify([s.needsEmpty, s.needs, s.navBadge, s.navLabel]));

        // The top bar's bookmark is always the global one (#4905); the dock's Saved tab keeps the scope of the page
        console.log('scoped pages');
        const openDockSaved = () => ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'saved.js'); await m.openSaved(); return true; })()`);
        const badge = () => ev(`(document.querySelector('#saved-btn .saved-count') || {}).textContent || ''`);
        await open(1280, false);
        await ev(`window.selectLiveSession('QA Alpha', 'claude', 'qa-a-id', 'local'); true`);
        await settle(`document.getElementById('saved-btn').getAttribute('aria-label') === 'Saved for later, 6 open'`, 'the top bar count of everything');
        check('on an agent\'s page the top bar badge is the count of everything (6), not the agent\'s (2)', (await badge()) === '6', await badge());
        await ev(`document.getElementById('saved-btn').click(); true`);
        await settle(`document.getElementById('saved-scope').textContent === 'All saved' && document.querySelectorAll('#saved-list > li').length === 6`, 'the Inbox at everything');
        s = await state();
        check('an agent\'s page: the bookmark opens the Inbox at Saved for later, all of it', s.inbox && s.saved && s.uiIn === 'saved-view' && s.navActive === 'hub-inbox' && s.savedBtn === 'true' && s.heading === 'All saved' && JSON.stringify(s.rows) === JSON.stringify(ALL_ROWS), JSON.stringify([s.inbox, s.uiIn, s.navActive, s.savedBtn, s.heading, s.rows]));
        check('and the badge is still everything', (await badge()) === '6', await badge());
        await shot('inbox-from-agent-desktop');
        await ev(`document.getElementById('saved-btn').click(); true`);   // a second press goes back to the page it came from
        await settle(`document.querySelector('.top-nav-tab.active')?.dataset.arg === 'agents' && !document.getElementById('nav-tab-hub-inbox').classList.contains('active')`, 'back on the agent');
        check('a second press goes back to the agent\'s page', (await ev(`document.getElementById('session-name')?.textContent || ''`)).includes('QA Alpha'), await ev(`document.getElementById('session-name')?.textContent || ''`));
        await openDockSaved();
        await settle(`document.getElementById('saved-scope').textContent === 'Saved: QA Alpha' && document.querySelectorAll('#saved-list > li').length > 0`, 'the agent panel');
        s = await state();
        check('the dock\'s Saved tab on the same page is still the agent\'s: scoped, in the right sidebar', !s.inbox && s.uiIn === 'agentic-panel-saved' && s.dockShown && s.heading === 'Saved: QA Alpha' && JSON.stringify(s.rows) === JSON.stringify(['qa-a chat', 'team-a board, by the QA Engineer']), JSON.stringify([s.inbox, s.uiIn, s.dockShown, s.heading, s.rows]));
        check('its tab says 2, the top bar still 6, and the Inbox tab counts the agents waiting (1)', s.navBadge === '1' && (await ev(`document.getElementById('saved-tab-count').textContent`)) === '2' && (await badge()) === '6', JSON.stringify([s.navBadge, await badge()]));
        await shot('inbox-scoped-agent-desktop');
        await ev(`Promise.resolve(window.enterTeamContext('team-a', { tab: 'team-view' })).then(() => true)`); // (the team view is a lazy module, #5364: the first call loads it, so wait for it)
        await settle(`document.getElementById('saved-btn').getAttribute('aria-label') === 'Saved for later, 6 open'`, 'the count on the team page');
        check('on a team\'s page the top bar badge is the count of everything (6)', (await badge()) === '6', await badge());
        await ev(`document.getElementById('saved-btn').click(); true`);
        await settle(`document.getElementById('saved-scope').textContent === 'All saved' && document.querySelectorAll('#saved-list > li').length === 6`, 'the Inbox at everything, from a team');
        s = await state();
        check('a team\'s page: the bookmark opens the Inbox at everything too', s.inbox && s.uiIn === 'saved-view' && s.heading === 'All saved', JSON.stringify([s.inbox, s.uiIn, s.heading]));
        await ev(`document.getElementById('saved-btn').click(); true`);
        await settle(`!document.getElementById('nav-tab-hub-inbox').classList.contains('active')`, 'back on the team page');
        await openDockSaved();
        await settle(`document.getElementById('saved-scope').textContent === 'Saved: team-a' && document.querySelectorAll('#saved-list > li').length > 0`, 'the team panel');
        s = await state();
        check('a team\'s page: the dock\'s Saved tab is the team\'s, scoped', !s.inbox && s.uiIn === 'agentic-panel-saved' && s.dockShown && s.rows.length === 4, JSON.stringify([s.inbox, s.uiIn, s.dockShown, s.rows.length]));
        await ev(`window.exitTeamContext({ goBack: false, force: true }); window.selectLiveSession('', '', '', 'local'); true`);
        // from the Inbox, then an agent: the list is in the sidebar again
        await ev(`document.getElementById('nav-tab-hub-inbox').click(); true`);
        await ev(tabBtn('saved'));
        await rowsShown('All saved', 'the Inbox');
        await ev(`window.switchNavTab('agents'); window.selectLiveSession('FE Alpha', 'claude', 'fe-a-id', 'local'); true`);
        await openDockSaved();
        await settle(`document.getElementById('saved-scope').textContent === 'Saved: FE Alpha' && document.querySelectorAll('#saved-list > li:not(.saved-empty)').length === 1`, 'the next agent');
        s = await state();
        check('after the Inbox, an agent\'s panel has the list again (it was moved back)', s.uiIn === 'agentic-panel-saved' && s.dockShown && s.rows.join() === 'fe-a chat', JSON.stringify([s.uiIn, s.dockShown, s.rows]));
    } catch (e) {
        console.error(e && e.stack || e);
        results.push(false);
    } finally {
        await require('./helpers/saved_cleanup').clearSaved(api).catch(() => {}); // the bookmarks this suite saved are taken off again (#4916)
        await removeBoards(ev, [BOARD, 'team-a', 'team-b']);
        try { await client.close(); } catch (_) {}
    }
    const failed = results.filter(r => !r).length;
    console.log(failed ? `${failed} FAILED` : `PASS inbox local: ${results.length} checks`);
    process.exitCode = failed ? 1 : 0;
})();
