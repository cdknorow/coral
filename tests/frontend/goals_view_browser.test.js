// The local Goals view (#7113, UI/UX's #7091 layout A) against stubbed routes (the page's fetch answers the goals, a goal's tasks and the create): the Messages | Goals switch, the list with filter
// chips (counts, pressed, announced once), the rollup bar and its sentence, Waiting on you, the open goal with its tasks, Open task and Open the agent (and "Not running here"), the phone's two screens
// with a back row, the empty, filtered and failed states, and New goal with a refused title. 1280 and 390 px, 100% and 200% text. A real isolated server for the rest of the page.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const S = `(window.__coralStatic || '/static/')`;

const { GOALS, GOALS_STUB } = require('./helpers/goals_stub');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const board = `goals-view-${Date.now()}`;
    const shown = sel => ev(`(() => { const e = document.querySelector(${JSON.stringify(sel)}); return !!e && e.getClientRects().length > 0; })()`);
    const texts = sel => ev(`[...document.querySelectorAll(${JSON.stringify(sel)})].map(e => e.textContent.replace(/\\s+/g, ' ').trim())`);
    const STUB = GOALS_STUB;
    const open = async (w, h, mobile, zoom) => {
        await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`localStorage.setItem('coral.board.face', 'messages'); true`);
        await ev(`window.__coralReady.then(() => true)`);
        if (zoom) await ev(`document.documentElement.style.fontSize = '${zoom}'; true`);
        await ev(STUB);
        await ev(`fetch('/api/board/${board}/messages', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'qa', as: 'QA', content: 'hello' }) }).then(() => true)`);
        await ev(`window.__shownTasks = []; window.showTaskDetailModal = id => window.__shownTasks.push(id); (async () => { const m = await import(${S} + 'message_board.js'); const { showView } = await import(${S} + 'utils.js'); showView('messageboard-view'); m.selectBoardProject(${JSON.stringify(board)}); return true; })()`);
        await waitFor(`document.getElementById('mb-post-input').getClientRects().length > 0`, 'the board', 10000);
    };
    const toGoals = async () => { await ev(`document.getElementById('mb-face-goals').click(); true`); await waitFor(`document.querySelectorAll('#mb-goals .goal-card').length > 0 || !!document.querySelector('#mb-goals .goals-empty, #mb-goals .goals-failed')`, 'the goals', 8000); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile, zoom] of [[1280, 800, false, ''], [390, 844, true, ''], [390, 844, true, '200%']]) {
            const at = `${w}px${zoom ? ' ' + zoom : ''}`;
            console.log(`${at}: the switch and the list`);
            await open(w, h, mobile, zoom);
            assert.equal(await shown('#mb-face-switch'), true, `${at}: the switch is there with a board open`);
            assert.deepEqual(await ev(`['mb-face-messages', 'mb-face-goals'].map(id => document.getElementById(id).getAttribute('aria-pressed'))`), ['true', 'false']);
            await toGoals();
            assert.equal(await shown('#mb-board'), false, `${at}: Messages are out of the way`);
            assert.deepEqual(await ev(`['mb-face-messages', 'mb-face-goals'].map(id => document.getElementById(id).getAttribute('aria-pressed'))`), ['false', 'true']);
            assert.equal(await ev(`localStorage.getItem('coral.board.face')`), 'goals', `${at}: the choice is kept`);
            assert.equal(await ev(`window.__goalsListQuery`), '?state=proposed,active,blocked,done,dropped', `${at}: the list asks for every state (the route answers only the open ones by default)`);
            assert.equal((await texts('.goals-sub-line'))[0], 'This board only · 5 goals, 1 waiting on you');
            assert.deepEqual(await texts('.goals-chip'), ['All 5', 'Active 1', 'Blocked 1', 'Proposed 1', 'Done 1', 'Dropped 1']);
            assert.deepEqual(await texts('.goal-card-title'), ['Ship the new installer', 'Fix the flaky suites', 'Docs pass', 'Old cleanup', 'A retired idea'], `${at}: newest activity first`);
            assert.equal((await texts('.goal-state-blocked'))[0], 'Blocked on the Operator');
            assert.equal(await ev(`document.querySelector('.goal-card[data-goal-id="1"] .goal-rollup').getAttribute('aria-label')`), '9 tasks: 5 done, 2 active, 1 blocked, 1 queued', `${at}: the bar's name is the sentence`);
            assert.equal((await texts('.goal-card[data-goal-id="3"] .goal-card-sentence'))[0], 'No tasks yet');
            assert.equal(await shown('.goals-waiting'), true, `${at}: Waiting on you shows when something waits`);
            assert.equal(await ev(`document.querySelector('.goals-ul').getAttribute('aria-label')`), 'Goals, 5');
            console.log(`${at}: a filter says so once`);
            await ev(`(() => { const c = document.querySelector('.goals-chip[data-filter="blocked"]'); c.focus(); c.click(); })(); true`);
            assert.equal(await ev(`document.activeElement.dataset.filter`), 'blocked', `${at}: the chip that was pressed keeps the keyboard (its list was drawn again)`);
            assert.equal(await ev(`document.activeElement.getAttribute('aria-pressed')`), 'true');
            assert.deepEqual(await texts('.goal-card-title'), ['Fix the flaky suites']);
            assert.equal(await ev(`document.querySelector('.goals-chip[data-filter="blocked"]').getAttribute('aria-pressed')`), 'true');
            assert.equal((await texts('.goals-status'))[0], 'Showing 1 blocked goal');
            await ev(`document.querySelector('.goals-chip[data-filter="all"]').click(); true`);
            console.log(`${at}: a goal opens`);
            await ev(`document.querySelector('.goal-card[data-goal-id="1"]').click(); true`);
            await waitFor(`document.querySelectorAll('#mb-goals .goal-task').length === 3`, 'the goal\'s tasks', 8000);
            assert.equal(await ev(`document.activeElement && document.activeElement.id`), 'goals-detail-title', `${at}: focus moves to the goal's heading`);
            assert.equal((await texts('.goals-detail-title'))[0], 'Ship the new installer');
            assert.equal(await ev(`document.querySelector('.goal-item.active .goal-card').getAttribute('aria-current')`), 'true');
            assert.deepEqual(await texts('.goals-sub'), ['Outcome', 'Done when', 'Tasks']);
            assert.deepEqual(await texts('.goal-task-id'), ['#101', '#102', '#103']);
            assert.deepEqual(await texts('.goal-task-sub'), ['goal-qa · signing', 'nobody-here']);
            assert.equal(await ev(`document.querySelectorAll('.goal-open-agent.is-off').length`), 3, `${at}: no agent runs here: all three say so`);
            assert.deepEqual(await texts('.goal-task-why'), ['Not running here', 'Not running here', 'Not running here']);
            await ev(`document.querySelector('.goal-open-task').click(); true`);
            assert.deepEqual(await ev(`window.__shownTasks`), [101], `${at}: Open task opens that task`);
            if (mobile && !zoom && process.env.CORAL_SCREENSHOT_DIR) { const fs = require('node:fs'); fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); await new Promise(r => setTimeout(r, 300)); /* sleep-ok: a screenshot waits out transitions */ fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/goals-390-detail.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); }
            if (mobile) {
                assert.equal(await shown('.goals-list-col'), false, `${at}: the list gives way to the goal on a phone`);
                assert.equal(await shown('.goals-back'), true);
                const small = await ev(`[...document.querySelectorAll('#mb-goals button')].filter(b => b.getClientRects().length && (b.getBoundingClientRect().height < 43.5)).map(b => b.textContent.trim())`);
                assert.deepEqual(small, [], `${at}: every button is 44 px tall: ${JSON.stringify(small)}`);
                assert.equal(await shown('.goals-head'), false, `${at}: an open goal is a screen of its own: the page heading and New goal are not above it`);
                assert.ok(await ev(`document.querySelector('.goals-back').getBoundingClientRect().top < document.getElementById('goals-detail-title').getBoundingClientRect().top && document.querySelector('.goals-back').getBoundingClientRect().top - document.querySelector('.goals-page').getBoundingClientRect().top < 24`), `${at}: the back row comes first`);
                await ev(`document.querySelector('.goals-back').click(); true`);
                assert.equal(await shown('.goals-list-col'), true, `${at}: back to the list`);
                assert.equal(await ev(`document.activeElement.dataset.goalId`), '1', `${at}: focus returns to the card`);
            } else {
                assert.equal(await shown('.goals-list-col'), true, `${at}: the list stays beside the goal`);
                assert.equal(await shown('.goals-back'), false);
            }
            console.log(`${at}: a goal with many tasks pages them`);
            await ev(`(() => { const rows = (from, n, st) => Array.from({ length: n }, (_, i) => ({ id: from + i, title: 'Task ' + (from + i), status: st }));
                window.__goals = [...window.__goals, { id: 6, title: 'Many done', state: 'active', rollup: { done: 120, active: 0, blocked: 0, queued: 0 }, updated_at: new Date().toISOString() }, { id: 7, title: 'Capped', state: 'active', rollup: { done: 130, active: 0, blocked: 0, queued: 0 }, updated_at: new Date().toISOString() }, { id: 8, title: 'Cursor', state: 'active', rollup: { done: 80, active: 0, blocked: 0, queued: 0 }, updated_at: new Date().toISOString() }];
                window.__tasks[6] = rows(1000, 120, 'completed'); window.__tasks[7] = rows(2000, 120, 'completed'); window.__tasks[8] = rows(3000, 50, 'completed'); window.__cursors = { 8: 'c1' }; window.__page2 = { 8: rows(3050, 30, 'completed') }; return true; })()`);
            await ev(`(async () => { (await import(${S} + 'goals_view.js')).loadGoals(); return true; })()`);
            await waitFor(`document.querySelectorAll('.goal-card').length === 8`, 'the extra goals', 6000);
            if (mobile) await ev(`document.querySelector('.goals-back') && document.querySelector('.goals-back').click(); true`);
            await ev(`document.querySelector('.goal-card[data-goal-id="6"]').click(); true`);
            await waitFor(`document.querySelectorAll('#mb-goals .goal-task').length === 50`, 'the first 50 tasks', 6000);
            assert.deepEqual(await texts('.goals-more'), ['Show 50 more'], `${at}: the button says how many`);
            assert.equal((await texts('.goals-note')).filter(t => /Showing 50 of 120 tasks/.test(t)).length, 1);
            if (mobile) assert.ok(await ev(`document.querySelector('.goals-more').getBoundingClientRect().height >= 43.5`), `${at}: the button is 44 px tall`);
            await ev(`document.querySelector('.goals-more').focus(); true`);
            await ev(`document.querySelector('.goals-more').click(); true`);
            await waitFor(`document.querySelectorAll('#mb-goals .goal-task').length === 100`, '100 tasks', 4000);
            assert.equal(await ev(`document.activeElement.classList.contains('goal-task') && document.activeElement.querySelector('.goal-task-id').textContent`), '#1050', `${at}: focus goes to the first task that was added`);
            assert.equal((await texts('.goals-status'))[0], 'Showing 100 of 120 tasks', `${at}: said once`);
            assert.deepEqual(await texts('.goals-more'), ['Show 20 more']);
            await ev(`window.__tasksReads = 0; (async () => { (await import(${S} + 'goals_view.js')).loadGoals(); return true; })()`); // a refresh of the open goal
            await waitFor(`window.__tasksReads >= 1`, 'the refresh to read the tasks again', 6000);
            await ev(`new Promise(r => setTimeout(r, 150))`); // sleep-ok: the redraw after the read
            assert.equal(await ev(`document.querySelectorAll('#mb-goals .goal-task').length`), 100, `${at}: a refresh keeps how many tasks were shown (it went back to 50)`);
            await ev(`document.querySelector('.goals-more').click(); true`);
            await waitFor(`document.querySelectorAll('#mb-goals .goal-task').length === 120`, 'all 120', 4000);
            assert.equal(await ev(`document.querySelectorAll('.goals-more').length`), 0, `${at}: nothing more to show, no button`);
            assert.equal(await ev(`document.querySelectorAll('#mb-goals .goal-open-agent, #mb-goals .goal-task-why').length`), 0, `${at}: a finished task draws no Open the agent or Not running here`);
            assert.equal(await ev(`document.querySelectorAll('#mb-goals .goal-open-task').length`), 120, `${at}: it still has Open task`);
            assert.equal((await texts('.goals-note')).filter(t => /Showing/.test(t)).length, 0);
            console.log(`${at}: a route that sent fewer tasks than the goal counts says so`);
            if (mobile) await ev(`document.querySelector('.goals-back').click(); true`);
            await ev(`document.querySelector('.goal-card[data-goal-id="7"]').click(); true`);
            await waitFor(`document.querySelectorAll('#mb-goals .goal-task').length === 50`, 'goal 7 first page', 6000);
            await ev(`document.querySelector('.goals-more').click(); document.querySelector('.goals-more').click(); true`);
            await waitFor(`document.querySelectorAll('#mb-goals .goal-task').length === 120`, 'goal 7 all rows', 4000);
            assert.deepEqual(await texts('.goals-note').then(a => a.filter(t => /Showing/.test(t))), ['Showing 120 of 130 tasks. The rest are on the Tasks panel.'], `${at}: honest about the route's cap`);
            console.log(`${at}: a cursor from the route is followed`);
            if (mobile) await ev(`document.querySelector('.goals-back').click(); true`);
            await ev(`document.querySelector('.goal-card[data-goal-id="8"]').click(); true`);
            await waitFor(`document.querySelectorAll('#mb-goals .goal-task').length === 50`, 'goal 8 first page', 6000);
            assert.deepEqual(await texts('.goals-more'), ['Show 30 more']);
            await ev(`document.querySelector('.goals-more').click(); true`);
            await waitFor(`document.querySelectorAll('#mb-goals .goal-task').length === 80`, 'the second page by cursor', 4000);
            assert.equal(await ev(`document.querySelectorAll('.goals-more').length`), 0);
            await ev(`window.__goals = window.__goals.filter(g => g.id < 6); (async () => { (await import(${S} + 'goals_view.js')).loadGoals(); return true; })()`);
            await waitFor(`document.querySelectorAll('.goal-card').length === 5`, 'back to five goals', 6000);
            if (mobile) await ev(`document.querySelector('.goals-back') && document.querySelector('.goals-back').click(); true`);
            console.log(`${at}: a goal waiting on the person`);
            await ev(`document.querySelector('.goal-card[data-goal-id="2"]').click(); true`);
            await waitFor(`document.querySelector('.goals-waiting-full') !== null && document.querySelectorAll('#mb-goals .goals-note').length > 0`, 'the waiting card', 8000);
            const waitText = (await texts('.goals-waiting-full .goals-text'));
            assert.deepEqual(waitText, ['Spend a day on the slowest suite?', 'Yes: it fails one run in ten.', 'The suite stays as it is.']);
            assert.ok((await texts('.goals-note')).some(t => /No tasks in this goal yet\. Add one with coral-board task add "title" --goal 2\./.test(t)), `${at}: a goal with no tasks says how to add one`);
            assert.equal(await ev(`document.documentElement.scrollWidth > innerWidth + 1`), false, `${at}: nothing scrolls sideways`);
            console.log(`${at}: New goal`);
            if (mobile) await ev(`document.querySelector('.goals-back').click(); true`);
            await ev(`document.querySelector('.goals-new').click(); true`);
            await waitFor(`!!document.querySelector('dialog.goals-dialog[open]')`, 'the dialog', 3000);
            const box = await ev(`(() => { const r = document.querySelector('dialog.goals-dialog').getBoundingClientRect(); return { cx: r.left + r.width / 2, cy: r.top + r.height / 2, w: r.width, h: r.height, vw: innerWidth, vh: innerHeight, top: r.top }; })()`);
            if (!mobile) assert.ok(Math.abs(box.cx - box.vw / 2) <= 4 && Math.abs(box.cy - box.vh / 2) <= 4, `${at}: the dialog is centred in the window, not in the corner: ${JSON.stringify(box)}`);
            else assert.ok(box.w >= box.vw - 1 && box.h >= box.vh - 1 && box.top <= 1, `${at}: a phone gets a full-screen sheet (100dvh): ${JSON.stringify(box)}`);
            await waitFor(`!document.getElementById('goal-new-driver').disabled`, 'the board\'s agents', 4000);
            assert.deepEqual(await ev(`[...document.getElementById('goal-new-driver').options].map(o => o.textContent)`), ['Orchestrator (orch)', 'QA (qa-eng)', 'No driver yet'], `${at}: Driver is a select of this board's agents, the Orchestrator first`);
            assert.equal(await ev(`document.getElementById('goal-new-driver').value`), 'orch', `${at}: the Orchestrator is the default`);
            assert.deepEqual(await texts('.goals-field > span'), ['Title (required)', 'Done when (optional): what a person can check', 'Driver (optional)'], `${at}: which fields are needed`);
            await ev(`document.querySelector('.goals-dialog button[type=submit]').click(); true`);
            assert.equal((await texts('.goals-field-error'))[0], 'A goal needs a title.', `${at}: a refused title says why, next to the field`);
            await ev(`window.__goalsMode = 'refuse'; document.getElementById('goal-new-title').value = 'Brand new goal'; document.getElementById('goal-new-done').value = 'it works'; document.querySelector('.goals-dialog button[type=submit]').click(); true`);
            await waitFor(`/was not created: That is too long: shorten it and try again\./.test(document.querySelector('.goals-field-error').textContent)`, 'the refusal', 4000);
            await ev(`window.__goalsMode = 'ok'; document.querySelector('.goals-dialog button[type=submit]').click(); true`);
            await waitFor(`!document.querySelector('dialog.goals-dialog')`, 'the dialog to close', 4000);
            assert.deepEqual((await ev(`window.__goalsPosts`)).at(-1), { title: 'Brand new goal', subscriber_id: 'Operator', done_when: 'it works', driver: 'orch' }, `${at}: the create body`);
            await waitFor(`document.querySelectorAll('.goal-card').length === 6`, 'the new goal in the list', 4000);
            assert.equal(await ev(`document.activeElement.classList.contains('goals-new')`), true, `${at}: focus returns to New goal`);
        }

        console.log('Open the agent reaches a running agent');
        await open(1280, 800, false, '');
        await toGoals();
        await ev(`window.__opened = []; window.selectLiveSession = (...a) => window.__opened.push(a); window.switchNavTab = () => {}; window.__m.add('goal-qa', 'A', 3, { sleeping: false, session_id: 'goal-qa-id', board_project: ${JSON.stringify(board)} }); true`);
        await waitFor(`(window.__qaState && window.__qaState.liveSessions || []).some(s => s.session_id === 'goal-qa-id')`, 'the live agent', 8000);
        await ev(`document.querySelector('.goal-card[data-goal-id="1"]').click(); true`);
        await waitFor(`document.querySelectorAll('#mb-goals .goal-task').length === 3`, 'the tasks', 8000);
        assert.equal(await ev(`document.querySelectorAll('.goal-open-agent.is-off').length`), 2, 'the running agent\'s task is no longer "not running here"');
        await ev(`document.querySelector('.goal-open-agent:not(.is-off)').click(); true`);
        assert.equal(await ev(`window.__opened.length`), 1); assert.equal(await ev(`window.__opened[0][2]`), 'goal-qa-id', 'it opens that agent');
        const shotDir = process.env.CORAL_SCREENSHOT_DIR;
        if (shotDir) { const fs = require('node:fs'); fs.mkdirSync(shotDir, { recursive: true }); await new Promise(r => setTimeout(r, 300)); /* sleep-ok: a screenshot waits out transitions */ fs.writeFileSync(`${shotDir}/goals-1280.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
            await ev(`document.documentElement.setAttribute('data-theme', 'light'); true`); await new Promise(r => setTimeout(r, 300)); /* sleep-ok: the theme repaints */ fs.writeFileSync(`${shotDir}/goals-1280-light.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); await ev(`document.documentElement.setAttribute('data-theme', 'dark'); true`); }

        console.log('empty, filtered-empty and failed states');
        await open(1280, 800, false, '');
        await ev(`window.__goals = []; true`); await toGoals();
        assert.equal((await texts('.goals-empty p'))[0], 'No goals yet. A goal says what the team is for and when it is done; tasks belong to it.');
        assert.equal((await texts('.goals-cmd'))[0], 'coral-board goal add "title" --done-when "..."');
        assert.equal((await texts('.goals-sub-line'))[0], 'This board only · No goals yet');
        await ev(`window.__goals = ${JSON.stringify(GOALS.slice(0, 4))}; document.querySelector('.goals-new') && 0; true`);
        await ev(`(async () => { (await import(${S} + 'goals_view.js')).loadGoals(); return true; })()`);
        await waitFor(`document.querySelectorAll('.goal-card').length === 4`, 'four goals', 4000);
        await ev(`document.querySelector('.goals-chip[data-filter="dropped"]').click(); true`);
        assert.equal((await texts('.goals-empty p'))[0], 'No dropped goals');
        assert.equal((await texts('.goals-status'))[0], 'No dropped goals');
        await ev(`(() => { const b = [...document.querySelectorAll('.goals-empty .btn')].find(b => /Show all 4/.test(b.textContent)); b.focus(); b.click(); })(); true`);
        assert.equal(await ev(`document.activeElement.classList.contains('goals-chip') && document.activeElement.getAttribute('aria-pressed') === 'true' && document.activeElement.dataset.filter`), 'all', 'Show all goes away when it works: focus goes to the pressed chip, not the page');
        assert.equal(await ev(`document.querySelectorAll('.goal-card').length`), 4, 'Show all 4 brings them back');
        await ev(`window.__goalsMode = 'fail'; (async () => { (await import(${S} + 'goals_view.js')).loadGoals(); return true; })()`);
        await waitFor(`!!document.querySelector('.goals-failed')`, 'the failure', 4000);
        assert.match((await texts('.goals-failed span'))[0], /^Goals could not be read: boom \(as of \S+/, 'the last list stays, marked with its time');
        assert.equal(await ev(`document.querySelectorAll('.goal-card').length`), 4);
        await ev(`window.__goalsMode = 'ok'; (() => { const b = [...document.querySelectorAll('.goals-failed .btn')].find(b => /Try again/.test(b.textContent)); b.focus(); b.click(); })(); true`);
        await waitFor(`!document.querySelector('.goals-failed')`, 'Try again to work', 4000);
        assert.ok(await ev(`document.activeElement !== document.body && !!document.activeElement.closest('#mb-goals')`), 'Try again goes away when it works: focus stays in the Goals page');
        console.log('a failed task list: Try again leaves the focus on the goal\'s heading');
        await ev(`window.__tasksFail = true; document.querySelector('.goal-card[data-goal-id="1"]').click(); true`);
        await waitFor(`!!document.querySelector('.goals-detail-col .goals-failed')`, 'the task list failure', 6000);
        await ev(`(() => { const b = [...document.querySelectorAll('.goals-detail-col .goals-failed .btn')].find(b => /Try again/.test(b.textContent)); b.focus(); window.__tasksFail = false; b.click(); })(); true`);
        await waitFor(`!document.querySelector('.goals-detail-col .goals-failed') && document.querySelectorAll('.goals-detail-col .goal-task').length === 3`, 'Try again to work in the detail', 6000);
        assert.equal(await ev(`document.activeElement.id`), 'goals-detail-title', 'Try again went away: focus is on the goal\'s heading, not the page');
        await ev(`window.__requireGoal = true; (async () => { (await import(${S} + 'goals_view.js')).loadGoals(); return true; })()`);
        await waitFor(`!!document.querySelector('.goals-require')`, 'the require-a-goal note', 4000);
        assert.equal((await texts('.goals-require'))[0], 'Tasks without a goal are refused on this board (require a goal is on).');
        console.log('PASS the Goals view');
    } finally { await require('./helpers/board_cleanup').removeBoards(ev, [board]).catch(() => {}); await client.close(); }
})().catch(err => { console.error(err); process.exit(1); });
