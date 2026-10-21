// The Board and the Team view for a screen reader and in both themes (#5791): a message that arrives while the board is at the bottom is announced once (who and the start of it;
// not the task queue's lines, not when read higher up, where the pill speaks); a message's delete button names the message; the Team view does not make the whole view a live
// region (a Refresh says what changed in one line) and its Remind buttons say whose; and what the audit found failing 4.5:1 in the real light theme holds: the board's Delete,
// a high-priority task's chip, the Free badge. Real board over the API, the Team view from a stubbed status. Drives Chrome over CDP against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const B = 'a11y-board-' + Date.now().toString(36);
    const api = (m, p, body) => ev(`fetch(${JSON.stringify(p)}, { method: ${JSON.stringify(m)}, headers: { 'Content-Type': 'application/json' }, body: ${body === undefined ? 'undefined' : JSON.stringify(JSON.stringify(body))} }).then(r => r.status)`);
    const shot = async n => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; require('node:fs').mkdirSync(d, { recursive: true }); require('node:fs').writeFileSync(`${d}/${n}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const said = () => ev(`document.getElementById('mb-announce').textContent.trim()`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await Emulation.setFocusEmulationEnabled({ enabled: true });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install(); await ev(`window.__coralReady.then(() => true)`);
        for (const [id, t] of [['Boss', 'Orchestrator'], ['dev-1', 'Developer']]) assert.equal(await api('POST', `/api/board/${B}/subscribe`, { subscriber_id: id, job_title: t, session_name: id + '-s' }), 200);
        for (let i = 1; i <= 8; i++) assert.equal(await api('POST', `/api/board/${B}/messages`, { subscriber_id: i % 2 ? 'dev-1' : 'Boss', content: `Message ${i}: ${'a line of status text '.repeat(i)}` }), 200);
        assert.equal(await api('POST', `/api/board/${B}/tasks`, { subscriber_id: 'Boss', title: 'Ship the queue', body: 'Do it.', priority: 'high', assignee: 'dev-1' }), 201);
        await Page.navigate({ url: `${BASE}/board/${B}` }); await Page.loadEventFired(); await install(); await ev(`window.__coralReady.then(() => true)`);
        await waitFor(`document.querySelectorAll('#mb-messages .mb-message').length >= 8`, 'the messages', 15000);

        console.log('1. the delete button names its message; the announcer is a polite status');
        const del = await ev(`(() => { const b = document.querySelector('#mb-messages .mb-delete-msg-btn'); const a = document.getElementById('mb-announce'); return { label: b.getAttribute('aria-label'), live: a.getAttribute('aria-live'), role: a.getAttribute('role') }; })()`);
        assert.ok(/^Delete the message from (Developer|Orchestrator), /.test(del.label), 'a delete button says whose message: ' + del.label);
        assert.equal(del.live, 'polite'); assert.equal(del.role, 'status');

        console.log('1b. the messages are a list: every direct child is an item (#5813)');
        const list = await ev(`(() => { const c = document.getElementById('mb-messages'); const kids = [...c.children]; return { role: c.getAttribute('role'), name: c.getAttribute('aria-label'), kids: kids.length, notItems: kids.filter(k => k.getAttribute('role') !== 'listitem').map(k => k.className), messages: c.querySelectorAll(':scope > .mb-message[role=listitem]').length }; })()`);
        assert.equal(list.role, 'list'); assert.equal(list.name, 'Messages');
        assert.deepEqual(list.notItems, [], 'every direct child of the list is an item: ' + JSON.stringify(list.notItems));
        assert.ok(list.messages >= 8, 'the messages are the items: ' + JSON.stringify(list));

        console.log('2. a message arriving at the bottom is announced once; a task-queue line is not');
        await ev(`window.__saidLog = []; new MutationObserver(() => window.__saidLog.push(document.getElementById('mb-announce').textContent.trim())).observe(document.getElementById('mb-announce'), { childList: true, characterData: true, subtree: true }); true`);
        assert.equal(await api('POST', `/api/board/${B}/messages`, { subscriber_id: 'dev-1', content: 'The build is green and the queue is moving again.' }), 200);
        await waitFor(`document.getElementById('mb-announce').textContent.includes('The build is green')`, 'the arrival is announced', 30000);
        assert.equal(await said(), 'New message from Developer: The build is green and the queue is moving again.');
        const before = await ev(`window.__saidLog.length`);
        assert.equal(await api('POST', `/api/board/${B}/tasks`, { subscriber_id: 'Boss', title: 'Another one', priority: 'low' }), 201);
        await waitFor(`document.getElementById('mb-messages').textContent.includes('Another one')`, 'the queue line shows', 30000);
        assert.equal(await ev(`window.__saidLog.length`), before, "the task queue's own line says nothing");

        console.log('3. read higher up: the pill speaks, the announcer does not');
        await ev(`const m = document.getElementById('mb-messages'); m.scrollTop = 0; m.dispatchEvent(new Event('scroll')); true`);
        const was = await said();
        assert.equal(await api('POST', `/api/board/${B}/messages`, { subscriber_id: 'Boss', content: 'A second update while you read up the page.' }), 200);
        await waitFor(`!document.querySelector('#mb-new-pill button').hidden`, 'the pill', 30000);
        assert.equal(await said(), was, 'the announcer stays as it was while the pill counts');

        console.log('4. the real light theme holds 4.5:1 for the board header and the task chips');
        await installAudit(ev);
        await ev(`document.getElementById('mb-messages').scrollTop = 1e6; true`);
        for (const theme of ['dark', 'light']) {
            await setTheme(ev, theme);
            const h = await auditRegion(ev, ['.mb-view-header']);
            assert.ok(h.checked > 0 && !h.fails.length, `${theme}: the board header (Delete) holds 4.5:1: ` + JSON.stringify(h.fails).slice(0, 400));
            await ev(`(() => { if (document.getElementById('agentic-state').getBoundingClientRect().width < 50) document.getElementById('panel-toggle-btn').click(); return true; })()`);
            await ev(`document.getElementById('agentic-tab-tasks').click(); true`);
            await waitFor(`document.querySelectorAll('#agentic-panel-tasks .board-task-item').length >= 1 && document.getElementById('agentic-panel-tasks').getBoundingClientRect().width > 50`, 'the tasks', 15000);
            const t = await auditRegion(ev, ['#agentic-panel-tasks']);
            assert.ok(t.checked > 0 && !t.fails.length, `${theme}: the task list (High chip) holds 4.5:1: ` + JSON.stringify(t.fails).slice(0, 400));
            if (theme === 'dark') {
                // a row is not a button (#5813): its one keyboard control is the native button with the task's name; the others are not tab stops of their own
                const row = await ev(`(() => { const r = [...document.querySelectorAll('#agentic-panel-tasks .board-task-item')].filter(x => x.querySelector('.board-task-open')); const first = r[0]; const stops = [...first.querySelectorAll('button, [tabindex], a[href], input, select')]; return { rows: r.length, roleOnRow: r.filter(x => x.hasAttribute('role') || x.hasAttribute('tabindex')).length, stops: stops.map(x => x.tagName + ':' + (x.getAttribute('aria-label') || x.className)), nestedInButton: !!first.closest('[role=button]') }; })()`);
                assert.equal(row.roleOnRow, 0, 'no row has a role or a tab stop of its own: ' + JSON.stringify(row));
                assert.equal(row.nestedInButton, false);
                assert.ok(row.stops.some(x => /^BUTTON:Open task #\d+: /.test(x)), 'the row has the native Open button: ' + JSON.stringify(row.stops));
                assert.ok(row.stops.length <= 2, 'one control, and at most the status detail: ' + JSON.stringify(row.stops));
                await ev(`window.__opened = 0; { const w = window.showTaskDetailModal; window.showTaskDetailModal = id => { window.__opened++; window.__openedId = id; }; } true`);
                await ev(`document.querySelector('#agentic-panel-tasks .board-task-open').focus(); true`);
                await Input.dispatchKeyEvent({ type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, text: '\r' }); await Input.dispatchKeyEvent({ type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
                await waitFor(`window.__opened >= 1`, 'Enter on the button opens the task', 4000);
                assert.equal(await ev(`window.__opened`), 1, 'Enter opens the task once');
                await ev(`document.querySelector('#agentic-panel-tasks .board-task-open').closest('.board-task-item').querySelector('.board-task-desc').click(); true`);
                assert.equal(await ev(`window.__opened`), 2, 'a click on the row still opens it');
            }
            await shot(`board-${theme}`);
        }
        await setTheme(ev, 'dark');

        console.log('5. the Team view: one status line, named Remind buttons, the Free badge in light');
        await ev(`(async () => {
            document.body.innerHTML = '<main id="agentic-state" class="agentic-state"><div class="agentic-block"></div><div id="agentic-panel-team-view" class="agentic-panel active" style="display:flex;flex:1;min-height:0"></div></main>'; const st = document.getElementById('agentic-state'); st.style.cssText = 'display:flex!important;position:fixed;left:0;top:0;height:100vh;width:760px';
            const real = window.fetch; const agents = [
              { name: 'Frontend Developer', agent_type: 'codex', role: 'Frontend Developer', availability: 'busy', available: false, reason: 'Working through the task list', tasks: [{ scope: 'board', id: 11, status: 'in_progress', title: 'Build the queue view' }] },
              { name: 'QA Engineer', agent_type: 'claude', role: 'QA', availability: 'available', available: true, reason: 'Nothing assigned', tasks: [] }];
            window.fetch = (u, o) => String(u).includes('/status') ? Promise.resolve(new Response(JSON.stringify({ summary: { busy: 1, available: 1 }, observed_at: new Date().toISOString(), agents, unassigned_tasks: [], health_report: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } })) : real(u, o);
            const m = await import((window.__coralStatic || '/static/') + 'team_availability.js'); await m.showTeamAvailability('qa-team', { workspace: true }); return true; })()`);
        await waitFor(`document.querySelectorAll('#agentic-panel-team-view .availability-agent').length === 2`, 'the cards', 10000);
        const tv = await ev(`(() => { const p = document.getElementById('agentic-panel-team-view'); return { liveOnContent: p.querySelector('.availability-content').hasAttribute('aria-live'), liveRegions: p.querySelectorAll('[aria-live], [role=status]').length, status: p.querySelector('.availability-status').getAttribute('role'), remind: [...p.querySelectorAll('.availability-agent > button')].map(b => b.getAttribute('aria-label')) }; })()`);
        assert.equal(tv.liveOnContent, false, 'the whole view is not a live region');
        assert.equal(tv.liveRegions, 1, 'one status line is: ' + JSON.stringify(tv));
        const names = await ev(`[...document.querySelectorAll('#agentic-panel-team-view .availability-agent')].map(c => { const id = c.getAttribute('aria-labelledby'); return [c.tagName, (document.getElementById(id) || {}).textContent]; })`);
        assert.deepEqual(names, [['ARTICLE', 'Frontend Developer'], ['ARTICLE', 'QA Engineer']], 'each card is named by its agent');
        assert.deepEqual(tv.remind, ['Remind agent for Frontend Developer', 'Remind agent for QA Engineer']);
        await ev(`document.querySelector('#agentic-panel-team-view .availability-toolbar button').click(); true`);
        await waitFor(`/Team view updated: 2 agents, 1 free, 1 busy\\./.test(document.querySelector('#agentic-panel-team-view .availability-status').textContent)`, 'a Refresh says what changed', 8000);
        await installAudit(ev);
        for (const theme of ['dark', 'light']) {
            await setTheme(ev, theme);
            const a = await auditRegion(ev, ['#agentic-panel-team-view']);
            assert.ok(a.checked > 0 && !a.fails.length, `${theme}: the Team view holds 4.5:1: ` + JSON.stringify(a.fails).slice(0, 400));
            await shot(`team-${theme}`);
        }
        console.log('   ✓');
    } finally { await removeBoards(ev, [B]); await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
