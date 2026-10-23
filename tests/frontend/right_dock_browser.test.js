// The right sidebar on every page (#4539): it is part of the app layout, not of the agent page. Each kind of page picks its tabs (the agent page keeps today's set; a board has
// Subscribers, Team artifacts, Tasks and Saved; Chats and every other page have none: Saved for everything is its own full-screen page, #4811) and keeps its own open or closed state, width and last tab, so a jump from Saved to a
// board message or a chat leaves each page's sidebar as the person had it. On a phone the top bar's button opens the side panel overlay on every page.
//
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name. The bookmarks, the board and its tasks are the real isolated server's.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install, select } = await connect();
    const shot = async (name) => {
        if (!process.env.CORAL_SCREENSHOT_DIR) return;
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); true`);
        await sleep(350); // sleep-ok: the tab strip fades its underline over 0.15 s; a picture taken mid-fade shows two tabs lit
        fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true });
        fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
    };
    const api = (method, path, body) => ev(`fetch(${JSON.stringify(path)}, { method: ${JSON.stringify(method)}, headers: { 'Content-Type': 'application/json' }, body: ${body ? JSON.stringify(JSON.stringify(body)) : 'undefined'} }).then(async r => ({ status: r.status, body: await r.json().catch(() => null) }))`);
    // The tabs the sidebar shows (in the order shown), which one is active, whether it is open, how wide it is, and which panel is visible.
    const dock = () => ev(`(() => { const d = document.getElementById('agentic-state'); const r = d.getBoundingClientRect(); const shown = e => getComputedStyle(e).display !== 'none';
        const act = d.querySelector('#agentic-block-top .agentic-tab.active');
        return { scope: document.body.getAttribute('data-saved-scope'), page: d.dataset.page, open: !d.classList.contains('collapsed') && r.width > 100, w: Math.round(r.width), tabs: [...d.querySelectorAll('.agentic-tab[data-tab]')].filter(shown).map(b => b.dataset.tab), active: act ? act.dataset.tab : null,
            panel: [...d.querySelectorAll('.agentic-panel.active')].filter(shown).map(p => p.id.replace('agentic-panel-', '')) }; })()`);
    const BOARD = 'dock-4539-' + Date.now().toString(36); // its own board per run: a crash before the finally cannot leave one for the next run on the same server (#4916)
    const toggle = () => ev(`document.getElementById('panel-toggle-btn').click(); true`);

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.clear(); localStorage.setItem('coral-live-view-mode', 'chat')`);
        for (const it of ((await api('GET', '/api/saved')).body.items || [])) await api('DELETE', `/api/saved/${it.id}`);
        await api('POST', `/api/board/${BOARD}/subscribe`, { subscriber_id: 'Dock One', job_title: 'Developer', session_name: 'claude-aaaaaaaa-0000-0000-0000-000000000001' });
        const posted = await api('POST', `/api/board/${BOARD}/messages`, { subscriber_id: 'Dock One', content: 'a board message to save' });
        assert.equal(posted.status, 200, JSON.stringify(posted));
        const made = await api('POST', `/api/board/${BOARD}/tasks`, { title: 'Dock task one', created_by: 'Operator' });
        assert.ok(made.status < 300, 'a board task: ' + JSON.stringify(made));
        await ev(`(() => { window.__m.add('dk-a', 'D', 30, { sleeping: false }); T.render.renderLiveSessions(T.state.liveSessions); })()`);

        console.log('1. The agent page keeps today\'s tabs and opens on Files; there is no Subscribers tab there');
        await select('dk-a');
        await waitFor(`document.getElementById('agentic-state').dataset.page === 'agent'`, 'the agent page');
        let d = await dock();
        assert.equal(d.page, 'agent'); assert.ok(d.open, JSON.stringify(d));
        for (const t of ['files', 'browse', 'tasks', 'notes', 'activity', 'saved']) assert.ok(d.tabs.includes(t), `the agent page has ${t}: ` + JSON.stringify(d));
        assert.ok(!d.tabs.includes('subscribers'), 'no Subscribers on the agent page: ' + JSON.stringify(d));
        assert.equal(d.active, 'files');

        console.log('2. A board: the sidebar is there with Subscribers, Team artifacts, Tasks and Saved, open on Subscribers, and the board has no column of its own');
        await ev(`window.selectBoardProject(${JSON.stringify(BOARD)}); true`);
        await waitFor(`document.querySelectorAll('#mb-messages .mb-message').length === 1`, 'the board rows', 8000);
        d = await dock();
        assert.deepEqual({ page: d.page, open: d.open, tabs: d.tabs, active: d.active, panel: d.panel }, { page: 'board', open: true, tabs: ['subscribers', 'team-artifacts', 'tasks', 'saved'], active: 'subscribers', panel: ['subscribers'] });
        assert.equal(await ev(`!!document.querySelector('#messageboard-view #mb-subscribers-panel')`), false, 'the column is gone from the board page');
        assert.equal(await ev(`!!document.querySelector('#agentic-panel-subscribers #mb-subscribers-list')`), true);
        await waitFor(`document.getElementById('mb-subscribers-list').textContent.includes('Developer')`, 'the subscribers', 6000);
        await shot('dock-board-subscribers');

        console.log('3. Subscribers: a name opens that agent\'s history (its action works from the sidebar)');
        await ev(`window.__histOpened = null; window.selectHistorySession = id => { window.__histOpened = id; }; document.querySelector('#mb-subscribers-list .subscriber-history-link').click(); true`);
        await waitFor(`window.__histOpened === 'aaaaaaaa-0000-0000-0000-000000000001'`, 'the subscriber\'s history to be opened', 6000, `String(window.__histOpened)`);

        console.log('4. Tasks shows this board\'s tasks (and "+ Task" is there); Team artifacts shows its panel');
        await ev(`window.selectBoardProject(${JSON.stringify(BOARD)}); true`);
        await waitFor(`document.querySelectorAll('#mb-messages .mb-message').length === 1`, 'the board again', 8000);
        await ev(`document.getElementById('agentic-tab-tasks').click(); true`);
        await waitFor(`document.getElementById('board-task-list').textContent.includes('Dock task one')`, 'the board\'s task in the sidebar', 8000, `document.getElementById('board-task-list').textContent.slice(0, 200)`);
        assert.equal(await ev(`getComputedStyle(document.querySelector('#agentic-panel-tasks .btn-create-task')).display !== 'none'`), true, '+ Task is reachable');
        await shot('dock-board-tasks');
        await ev(`document.getElementById('agentic-tab-team-artifacts').click(); true`);
        d = await dock();
        assert.deepEqual(d.panel, ['team-artifacts'], JSON.stringify(d));

        console.log('5. Saved on the board: a message saved from its row is listed in the sidebar');
        const rows = (await api('GET', `/api/board/${BOARD}/messages/all?limit=10`)).body;
        const mid = (rows.messages || rows).find(m => m.content === 'a board message to save').id;
        await ev(`document.querySelector('#mb-messages [data-message-id="${mid}"] .mb-save-btn').click(); true`);
        await waitFor(`document.querySelector('#mb-messages [data-message-id="${mid}"] .mb-save-btn').getAttribute('aria-pressed') === 'true'`, 'saved', 6000);
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'saved.js'); await m.openSaved(); return true; })()`); // the board's own Saved tab (the top bar's bookmark is the Inbox's, #4905)
        await waitFor(`document.getElementById('agentic-panel-saved').classList.contains('active') && document.querySelectorAll('#saved-list .saved-item').length === 1`, 'the saved item in the board\'s sidebar', 8000);
        d = await dock();
        assert.deepEqual({ page: d.page, open: d.open, active: d.active }, { page: 'board', open: true, active: 'saved' });
        await shot('dock-board-saved');

        console.log('6. Chats (no chat open) and the other pages: no sidebar at all; Saved for everything is its own page, which the top bar bookmark opens');
        await ev(`switchNavTab('history'); true`);
        await waitFor(`getComputedStyle(document.getElementById('chats-empty-view')).display !== 'none'`, 'Chats');
        d = await dock();
        assert.equal(d.page, 'chats');
        assert.deepEqual(d.tabs, [], 'no tab on Chats: ' + JSON.stringify(d));
        assert.equal(await ev(`getComputedStyle(document.getElementById('agentic-state')).display`), 'none', 'the sidebar is not shown on Chats');
        assert.equal(await ev(`getComputedStyle(document.getElementById('panel-toggle-btn')).display`), 'none', 'and its button is not either');
        await ev(`document.getElementById('saved-btn').click(); true`);
        await waitFor(`document.getElementById('saved-view').getClientRects().length > 0 && document.getElementById('saved-view').contains(document.getElementById('saved-ui')) && document.querySelectorAll('#saved-list .saved-item').length === 1`, 'the Saved page', 8000);
        assert.equal(await ev(`getComputedStyle(document.getElementById('agentic-state')).display`), 'none', 'the page has no sidebar beside it');
        await shot('saved-page-from-chats');
        await ev(`switchNavTab('history'); true`);

        console.log('6b. A shut sidebar whose tab is Saved reads nothing; opening it with the panel button reads the list (on a board: its Saved is the board\'s)');
        await ev(`window.selectBoardProject(${JSON.stringify(BOARD)}); true`);
        await waitFor(`document.getElementById('agentic-state').dataset.page === 'board' && document.querySelectorAll('#mb-messages .mb-message').length === 1`, 'the board', 8000);
        await ev(`window.toggleAgenticPanel(true); document.getElementById('agentic-tab-saved').click(); true`);
        await waitFor(`document.querySelectorAll('#saved-list .saved-item').length === 1 && document.getElementById('agentic-panel-saved').classList.contains('active')`, 'the board\'s Saved', 8000);
        await toggle(); // close it
        await waitFor(`document.getElementById('agentic-state').classList.contains('collapsed')`, 'closed');
        await ev(`document.getElementById('saved-list').replaceChildren(); true`);
        await toggle(); // open it with the panel button, not the Saved button
        await waitFor(`document.querySelectorAll('#saved-list .saved-item').length === 1`, 'the list read when the sidebar opened on Saved', 8000);

        console.log('7. Each kind of page keeps its own state: closing it on the board leaves Chats and the agent page as they were; coming back finds the board as left');
        await ev(`window.selectBoardProject(${JSON.stringify(BOARD)}); true`);
        await waitFor(`document.querySelectorAll('#mb-messages .mb-message').length === 1`, 'the board', 8000);
        d = await dock();
        assert.ok(d.open && d.active === 'saved', 'the board kept its last tab, Saved: ' + JSON.stringify(d));
        await toggle();
        assert.equal((await dock()).open, false, 'closed on the board');
        await select('dk-a');
        await waitFor(`document.getElementById('agentic-state').dataset.page === 'agent'`, 'the agent page again');
        d = await dock();
        assert.ok(d.open && d.active === 'files', 'the agent page is as it was: ' + JSON.stringify(d));
        await ev(`window.selectBoardProject(${JSON.stringify(BOARD)}); true`);
        await waitFor(`document.getElementById('agentic-state').dataset.page === 'board'`, 'the board');
        assert.equal((await dock()).open, false, 'the board is still closed');
        await toggle();

        console.log('8. The width is the page kind\'s own');
        await ev(`localStorage.setItem('coral-taskbar-width:board', '520'); true`);
        await select('dk-a');
        await waitFor(`document.getElementById('agentic-state').dataset.page === 'agent'`, 'the agent page');
        const wAgent = (await dock()).w;
        await ev(`window.selectBoardProject(${JSON.stringify(BOARD)}); true`);
        await waitFor(`document.getElementById('agentic-state').dataset.page === 'board'`, 'the board');
        const wBoard = (await dock()).w;
        assert.ok(wBoard === 520 && wAgent !== 520, `board ${wBoard}, agent ${wAgent}`);

        console.log('9. A jump from Saved leaves the sidebar of the page it lands on as that page had it, with the bar to come back');
        await ev(`document.getElementById('agentic-tab-subscribers').click(); true`);
        await ev(`switchNavTab('history'); true`);
        await waitFor(`document.getElementById('agentic-state').dataset.page === 'chats'`, 'Chats');
        await ev(`document.getElementById('saved-btn').click(); true`); // from Chats: the Saved page
        await waitFor(`document.getElementById('saved-view').getClientRects().length > 0 && document.querySelectorAll('#saved-list .saved-item').length === 1`, 'the item', 8000);
        await ev(`document.querySelector('#saved-list .saved-item .btn-primary, #saved-list .saved-item .saved-open').click(); true`);
        await waitFor(`document.getElementById('agentic-state').dataset.page === 'board' && !!document.querySelector('#mb-messages .chat-search-target')`, 'the jump to the board message', 10000);
        d = await dock();
        assert.deepEqual({ open: d.open, active: d.active }, { open: true, active: 'subscribers' }, 'the board\'s sidebar is as the person left it: ' + JSON.stringify(d));
        await waitFor(`!!document.getElementById('saved-jumpbar')`, 'the jump bar', 4000);
        await ev(`document.querySelector('#saved-jumpbar .btn').click(); true`);
        await waitFor(`document.getElementById('saved-view').getClientRects().length > 0 && document.getElementById('saved-view').contains(document.getElementById('saved-ui'))`, 'Back to Saved: the page it started from', 6000);

        console.log('10. A phone: the top bar button opens the side panel overlay on the board and in Chats, with that page\'s tabs, and X closes it; Chats has none');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true });
        await sleep(400); // sleep-ok: the layout settles over frames after a viewport change; there is no completion event
        for (const [name, go, page, tabs] of [
            ['the board', `window.selectBoardProject(${JSON.stringify(BOARD)})`, 'board', ['subscribers', 'team-artifacts', 'tasks', 'saved']],
        ]) {
            await ev(`switchNavTab('agents'); true`); // the Inbox (where Back to Saved left the page) is left the way a person leaves it
            await waitFor(`!document.getElementById('nav-tab-hub-inbox').classList.contains('active')`, 'the Inbox left');
            await ev(`${go}; true`);
            await waitFor(`document.getElementById('agentic-state').dataset.page === '${page}'`, name);
            assert.equal(await ev(`getComputedStyle(document.getElementById('panel-toggle-btn')).display !== 'none'`), true, `the side panel button is on ${name}`);
            await ev(`document.getElementById('panel-toggle-btn').click(); true`);
            await waitFor(`document.getElementById('agentic-state').classList.contains('mobile-panel-overlay') && getComputedStyle(document.getElementById('agentic-state')).display !== 'none'`, `the overlay on ${name}`);
            d = await dock();
            assert.deepEqual(d.tabs, tabs, `${name}: ` + JSON.stringify(d));
            assert.ok(d.panel.length === 1, `${name} shows one panel: ` + JSON.stringify(d));
            await shot('dock-phone-' + page);
            await ev(`document.getElementById('agentic-mobile-close-btn').click(); true`);
            await waitFor(`!document.getElementById('agentic-state').classList.contains('mobile-panel-overlay')`, `the overlay closed on ${name}`);
        }
        await ev(`switchNavTab('history'); true`);
        await waitFor(`document.getElementById('agentic-state').dataset.page === 'chats'`, 'Chats on the phone');
        assert.equal(await ev(`getComputedStyle(document.getElementById('panel-toggle-btn')).display`), 'none', 'Chats has no side panel to open: Saved for everything is its own page');
        await Emulation.setTouchEmulationEnabled({ enabled: false });

        console.log('11. A desktop board with the sidebar open uses the toolbar\'s More menu (Lead\'s #4538); closed, the buttons are inline');
        await Emulation.setDeviceMetricsOverride({ width: 1024, height: 800, deviceScaleFactor: 1, mobile: false });
        await ev(`window.selectBoardProject(${JSON.stringify(BOARD)}); true`);
        await waitFor(`document.getElementById('agentic-state').dataset.page === 'board' && document.getElementById('mb-select-btn').style.display !== 'none'`, 'the board toolbar');
        const bar = () => ev(`(() => { const shown = id => { const e = document.getElementById(id); return !!e && getComputedStyle(e).display !== 'none' && e.getClientRects().length > 0; };
            const h = document.querySelector('#messageboard-view .mb-header-actions').getBoundingClientRect();
            return { more: shown('mb-more-btn'), select: shown('mb-select-btn'), del: shown('mb-delete-btn'), open: document.body.dataset.dockOpen, rows: Math.round(h.height) }; })()`);
        await waitFor(`document.body.dataset.dockOpen === '1' && getComputedStyle(document.getElementById('mb-more-btn')).display !== 'none'`, 'More with the sidebar open', 4000);
        let b = await bar();
        assert.deepEqual({ more: b.more, select: b.select, del: b.del }, { more: true, select: false, del: false }, 'open sidebar: only More: ' + JSON.stringify(b));
        await ev(`document.getElementById('mb-more-btn').click(); true`);
        await waitFor(`document.getElementById('mb-select-btn').getClientRects().length > 0`, 'the menu items');
        assert.deepEqual(await ev(`[...document.querySelectorAll('#mb-more-menu .mb-action-btn')].filter(e => e.getClientRects().length).map(e => e.id)`), ['mb-select-btn', 'mb-export-btn', 'mb-pause-btn', 'mb-delete-btn'], 'Delete last');
        await shot('dock-board-more');
        await ev(`document.getElementById('mb-more-btn').click(); true`);
        await toggle(); // close the sidebar
        await waitFor(`document.body.dataset.dockOpen === '0' && getComputedStyle(document.getElementById('mb-select-btn')).display !== 'none' && getComputedStyle(document.getElementById('mb-more-menu')).display === 'contents'`, 'the buttons inline with the sidebar closed', 4000);
        b = await bar();
        assert.deepEqual({ more: b.more, select: b.select, del: b.del }, { more: true, select: true, del: false }, 'closed sidebar: Select, Export and Pause inline, Delete behind More (#6279): ' + JSON.stringify(b));

        console.log('11b. The tab strip is keyboard-operable: Left and Right move the focus, Home and End go to the ends');
        await Emulation.setDeviceMetricsOverride({ width: 1400, height: 800, deviceScaleFactor: 1, mobile: false });
        await ev(`switchNavTab('history'); true`);
        await waitFor(`document.getElementById('agentic-state').dataset.page === 'chats'`, 'Chats');
        await ev(`window.selectBoardProject(${JSON.stringify(BOARD)}); true`);
        await waitFor(`document.getElementById('agentic-state').dataset.page === 'board'`, 'the board');
        await ev(`window.toggleAgenticPanel(true); true`);
        await waitFor(`document.getElementById('agentic-state').getBoundingClientRect().width > 300`, 'the sidebar open');
        const press = k => ev(`(() => { const e = document.activeElement; e.dispatchEvent(new KeyboardEvent('keydown', { key: ${JSON.stringify(k)}, bubbles: true, cancelable: true })); return document.activeElement.dataset.tab || document.activeElement.id; })()`);
        await waitFor(`getComputedStyle(document.getElementById('agentic-tab-saved')).display !== 'none'`, 'the board\'s Saved tab shown (the scope follows the page)', 4000);
        await ev(`document.getElementById('agentic-tab-subscribers').focus(); true`);
        assert.equal(await press('ArrowRight'), 'team-artifacts');
        assert.equal(await press('End'), 'saved');
        assert.equal(await press('ArrowRight'), 'subscribers', 'wraps around');
        assert.equal(await press('ArrowLeft'), 'saved');
        assert.equal(await press('Home'), 'subscribers');
        console.log('   ✓');

        console.log('12. A board starts with its sidebar open only when its messages keep room: closed at 1024 px, open at 1400 px (until the person chooses)');
        await ev(`localStorage.removeItem('coral-agentic-collapsed:board'); true`);
        for (const [width, open] of [[1024, false], [1400, true]]) {
            await Emulation.setDeviceMetricsOverride({ width, height: 800, deviceScaleFactor: 1, mobile: false });
            await ev(`switchNavTab('history'); true`);
            await waitFor(`document.getElementById('agentic-state').dataset.page === 'chats'`, 'Chats');
            await ev(`window.selectBoardProject(${JSON.stringify(BOARD)}); true`);
            await waitFor(`document.getElementById('agentic-state').dataset.page === 'board' && document.body.dataset.dockOpen === '${open ? 1 : 0}'`, `the board's sidebar ${open ? 'open' : 'closed'} at ${width} px`, 4000, `JSON.stringify({ o: document.body.dataset.dockOpen, w: innerWidth })`);
            assert.equal((await dock()).open, open, `at ${width} px`);
            await ev(`localStorage.removeItem('coral-agentic-collapsed:board'); true`);
        }
        console.log('   ✓');
    } finally {
        await require('./helpers/saved_cleanup').clearSaved(api).catch(() => {}); // the bookmarks this suite saved are taken off again (#4916)
        await api('DELETE', `/api/board/${BOARD}`).catch(() => {}); // and the board it made
        await client.close();
    }
})().catch(e => { console.error(e); process.exit(1); });
