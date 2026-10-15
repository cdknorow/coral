// #3170 (sweep 2): the board/team frontend batch.
//   #3  the team view is reachable by keyboard: its button is always shown and focusable; the group
//       kebab shows when it has keyboard focus
//   #4  closing the team view while its Tasks tab is open leaves only Tasks in the sidebar, not the
//       team chat stacked under it
//   #5  entering the team view opens the group chat at the newest message, also on re-entry
//   #8  a failed workflow step shows its error output; the run header quotes it; a collapsed output
//       box shows nothing (it used to leave a clipped strip of its first line)
//   #9  the tasks panel in a narrow sidebar shows each title without sideways scrolling; priority
//       reads High/Medium/Low; a blocked task reads Blocked; a task opens from the keyboard
//   #11 a long unbroken word wraps instead of widening the board feed; a fenced block is a code block
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');

const BASE = require('./test_base').BASE;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Input, Emulation } = client;
    const ev = async expression => {
        const result = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
        return result.result.value;
    };
    const { waitFor, waitStable, coralReady, nextFrame } = require('./helpers/wait').bind(ev);
    // A real key press: a native button activates on Enter's character, so keyDown carries text.
    const key = async (k, code = k, keyCode = 0) => {
        await Input.dispatchKeyEvent({ type: 'keyDown', key: k, code, windowsVirtualKeyCode: keyCode, nativeVirtualKeyCode: keyCode, text: k === 'Enter' ? '\r' : undefined });
        await Input.dispatchKeyEvent({ type: 'keyUp', key: k, code, windowsVirtualKeyCode: keyCode, nativeVirtualKeyCode: keyCode });
    };
    const LONG = 'a'.repeat(160);
    // #3590: a board post is an avatar row (the sender's initials in their palette colour, no box or
    // bar), a single newline in it is a line break, and a second post in a row shares the avatar.
    const rowCheck = async (feedId, where) => {
        const r = await ev(`(async () => {
            const { getAgentColor, hexToRgba } = await import((window.__coralStatic||'/static/')+'utils.js');
            const feed = document.getElementById(${JSON.stringify(feedId)});
            const msgs = [...feed.querySelectorAll('.mb-message')];
            const i = msgs.findIndex(m => m.textContent.includes('first line'));
            if (i < 0) return null;
            const msg = msgs[i], next = msgs[i + 1];
            const body = msg.querySelector('.mb-message-body');
            const p = body.querySelector('p');
            const av = msg.querySelector('.mb-avatar');
            const probe = document.createElement('div'); probe.style.color = getAgentColor('QA Engineer'); probe.style.background = hexToRgba(getAgentColor('QA Engineer'), 0.18); document.body.appendChild(probe);
            const want = { color: getComputedStyle(probe).color, bg: getComputedStyle(probe).backgroundColor }; probe.remove();
            const cs = getComputedStyle(msg), acs = av && getComputedStyle(av);
            const ar = av && av.getBoundingClientRect(), br = body.getBoundingClientRect();
            const nav = next && next.querySelector('.mb-avatar');
            return { br: !!p && !!p.querySelector('br'), lines: p ? p.innerText.split('\\n') : [], items: body.querySelectorAll('li').length,
                borderLeft: cs.borderLeftWidth, borderBottom: cs.borderBottomWidth, bg: cs.backgroundColor,
                initials: av && av.textContent, avChildren: av ? av.children.length : -1, avColor: acs && acs.color, avBg: acs && acs.backgroundColor, want,
                bodyRightOfAvatar: !!ar && br.left >= ar.right,
                nextGrouped: !!next && next.classList.contains('mb-message-grouped') && next.textContent.includes('a follow-up'),
                nextAvatarHidden: !!nav && getComputedStyle(nav).visibility === 'hidden',
                nextNameHidden: !!next && getComputedStyle(next.querySelector('.mb-agent-name')).display === 'none' };
        })()`);
        assert.ok(r, `${where}: the two-line post rendered`);
        assert.ok(r.br, `${where}: a single newline is a <br>`);
        assert.deepEqual(r.lines, ['first line', 'second line'], `${where}: the two lines stay separate`);
        assert.equal(r.items, 2, `${where}: the list still renders`);
        assert.equal(r.borderLeft, '0px', `${where}: no colour bar`);
        assert.equal(r.borderBottom, '0px', `${where}: no underline`);
        assert.equal(r.bg, 'rgba(0, 0, 0, 0)', `${where}: no box (the page's background)`);
        assert.equal(r.initials, 'QE', `${where}: the avatar shows the initials`);
        assert.equal(r.avChildren, 0, `${where}: the avatar is text only`);
        assert.equal(r.avColor, r.want.color, `${where}: the initials are in the sender's palette colour`);
        assert.equal(r.avBg, r.want.bg, `${where}: the avatar's tint is that colour`);
        assert.ok(r.bodyRightOfAvatar, `${where}: the body sits right of the avatar`);
        assert.ok(r.nextGrouped, `${where}: the next post from the same sender is grouped`);
        assert.ok(r.nextAvatarHidden, `${where}: a grouped post shares the avatar`);
        assert.ok(r.nextNameHidden, `${where}: a grouped post shares the name`);
    };
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await coralReady();

        // The board 'alpha' answers from a stub: 60 messages, the newest last.
        await ev(`(() => {
            const real = window.fetch.bind(window);
            const json = body => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
            const msgs = Array.from({ length: 60 }, (_, i) => ({ id: i + 1, project: 'alpha', subscriber_id: i % 2 ? 'Lead Developer' : 'QA Engineer', job_title: i % 2 ? 'Lead Developer' : 'QA Engineer', content: 'message ' + (i + 1), created_at: new Date(Date.UTC(2026, 9, 13, 10, 0, i)).toISOString() }));
            msgs.push({ id: 61, project: 'alpha', subscriber_id: 'Lead Developer', job_title: 'Lead Developer', content: 'long ${LONG} end', created_at: '2026-10-13T10:01:01Z' });
            msgs.push({ id: 62, project: 'alpha', subscriber_id: 'Lead Developer', job_title: 'Lead Developer', content: 'A block:\\n\\n\\u0060\\u0060\\u0060go\\nfunc main() {}\\n\\u0060\\u0060\\u0060\\n', created_at: '2026-10-13T10:01:02Z' });
            msgs.push({ id: 63, project: 'alpha', subscriber_id: 'QA Engineer', job_title: 'QA Engineer', content: 'first line\\nsecond line\\n\\n- one\\n- two', created_at: '2026-10-13T10:01:03Z' });
            msgs.push({ id: 64, project: 'alpha', subscriber_id: 'QA Engineer', job_title: 'QA Engineer', content: 'a follow-up', created_at: '2026-10-13T10:01:04Z' });
            window.__boardMsgs = msgs;
            window.fetch = async (url, opts) => {
                const u = String(url);
                if (u.includes('/api/board/alpha/messages/all')) {
                    const limit = Number(new URL(u, location.href).searchParams.get('limit') || 50);
                    return json({ total: msgs.length, messages: msgs.slice(-limit) });
                }
                if (u.includes('/api/board/alpha/status')) return json({ summary: { available: 1 }, observed_at: new Date().toISOString(), agents: [], unassigned_tasks: [], health_report: [] });
                if (u.includes('/api/board/alpha/working-mode/presets')) return json({ presets: [{ id: 'none', name: 'None', builtin: true, instructions: '' }] });
                if (u.includes('/api/board/alpha/working-mode')) return json({ mode: 'none', dependency_guidance: false, custom_instructions: '' });
                if (u.includes('/api/board/alpha/tasks')) return json([]);
                if (u.includes('/api/board/alpha/paused')) return json({ paused: false });
                if (u.includes('/api/board/alpha/subscribers')) return json([]);
                return real(url, opts);
            };
            return true;
        })()`);
        const S = `(window.__coralStatic||'/static/')`;
        await ev(`(async () => {
            const { state } = await import(${S}+'state.js');
            const render = await import(${S}+'render.js');
            const tc = await import(${S}+'team_context.js');
            window.__tc = tc; window.__render = render; window.__state = state;
            render.renderLiveSessions([{ session_id: 'alpha-1', name: 'alpha-1', agent_type: 'claude', board_project: 'alpha', display_name: 'Alpha agent', working_directory: '/tmp/alpha' }]);
            return true;
        })()`);

        console.log('#3 The team view is reachable by keyboard');
        const btn = await ev(`(() => { const b = document.querySelector('.team-open-btn'); if (!b) return null; b.focus(); const cs = getComputedStyle(b);
            return { display: cs.display, width: b.offsetWidth, focused: document.activeElement === b, label: b.getAttribute('aria-label') }; })()`);
        assert.ok(btn, 'the team header has an Open team view button');
        assert.notEqual(btn.display, 'none', 'shown without hovering');
        assert.ok(btn.width > 0, 'it takes space');
        assert.ok(btn.focused, 'it takes keyboard focus');
        assert.equal(btn.label, 'Open team view for alpha');
        await key('Enter', 'Enter', 13);
        await waitFor(`window.__state.selectedTeam === 'alpha' && !document.getElementById('team-center-view').hidden`, 'the team view after Enter');
        const kebab = await ev(`(() => { const k = document.querySelector('.session-group-header .group-kebab-btn'); k.focus(); return { focused: document.activeElement === k }; })()`);
        assert.ok(kebab.focused, 'the group kebab takes focus');
        await waitFor(`getComputedStyle(document.querySelector('.session-group-header .group-kebab-btn')).opacity === '1'`, 'the focused kebab to be visible', { diag: `getComputedStyle(document.querySelector('.session-group-header .group-kebab-btn')).opacity` });
        console.log('   ✓ button shown and focusable, Enter opens the team view; kebab visible on focus');

        console.log('#5 Entering the team view opens the group chat at the newest message');
        const atBottom = `(() => { const m = document.getElementById('board-panel-msgs'); return !!m && m.scrollHeight > m.clientHeight + 50 && m.scrollHeight - m.scrollTop - m.clientHeight < 24 && m.textContent.includes('second line'); })()`;
        const scrollDiag = `(() => { const m = document.getElementById('board-panel-msgs'); return m && { top: m.scrollTop, h: m.scrollHeight, c: m.clientHeight }; })()`;
        await waitFor(atBottom, 'the newest message in view on first entry', { diag: scrollDiag });
        // With one of the team's agents selected the chat stays loaded across leaving and coming back,
        // so re-entry renders nothing new: only the move resets the scroll (the sweep's case).
        await ev(`window.__state.currentSession = { type: 'live', name: 'alpha-1', session_id: 'alpha-1', agent_type: 'claude', board_project: 'alpha' }; true`);
        // Leave and come back the way a user does: the close button, then the team button.
        await ev(`document.querySelector('.team-center-close').click(); true`);
        await waitFor(`!window.__state.selectedTeam && document.getElementById('team-center-view').hidden`, 'the team view closed');
        // Another sidebar tab hides the chat panel. Selecting another of the team's agents reloads the chat
        // there, and scrolling a hidden element to its end does nothing (it has no height), so the moved
        // panel would open at its oldest message.
        await ev(`window.switchAgenticTab('tasks', 'top'); document.getElementById('board-panel-msgs').dataset.rendered = ''; window.__render.showBoardChatTab('alpha'); true`);
        await waitFor(`document.getElementById('board-panel-msgs')?.dataset.rendered === '1' && getComputedStyle(document.getElementById('agentic-panel-board')).display === 'none'`, 'the chat reloaded while hidden');
        await new Promise(r => setTimeout(r, 120)); // sleep-ok: leaving switched the sidebar to Board, whose 50 ms scroll timer (agentic_state.js) must fire before re-entry, as it does for a person
        // Open it on a team tab, as the Team Artifacts and Knowledge links do (no board-tab scroll runs then).
        await ev(`window.__tc.enterTeamContext('alpha', { tab: 'team-artifacts' }); true`);
        await waitFor(`window.__state.selectedTeam === 'alpha' && !document.getElementById('team-center-view').hidden`, 'the team view again');
        await nextFrame(); await nextFrame();
        assert.ok(await ev(atBottom), 'the newest message in view on re-entry (nothing new to render): ' + JSON.stringify(await ev(scrollDiag)));
        console.log('   ✓ newest message in view on entry and on re-entry');

        console.log('#11 Long words wrap; a fenced block is a code block');
        const feed = await ev(`(() => { const m = document.getElementById('board-panel-msgs'); return { sw: m.scrollWidth, cw: m.clientWidth, pre: !!m.querySelector('.mb-message-body pre code'), long: [...m.querySelectorAll('.mb-message-body')].some(b => b.textContent.includes('${LONG.slice(0, 40)}')) }; })()`);
        assert.ok(feed.long, 'the long message rendered');
        assert.ok(feed.sw <= feed.cw + 1, `the feed does not scroll sideways (scrollWidth ${feed.sw} > clientWidth ${feed.cw})`);
        assert.ok(feed.pre, 'a fenced block renders as pre > code');
        console.log('   ✓ no sideways scroll; fence renders as a code block');

        console.log('#3590 Team chat: avatar rows, a single newline is a line break');
        await rowCheck('board-panel-msgs', 'team chat');
        console.log('   ✓ initials in the palette colour, no box, <br> between lines, a run shares the avatar');

        console.log('#4 Closing the team view from its Tasks tab leaves only Tasks in the sidebar');
        await ev(`document.querySelector('.team-center-close').click(); true`);
        await waitFor(`!window.__state.selectedTeam`, 'the team view closed');
        await ev(`(async () => {
            window.__state.currentSession = { type: 'live', name: 'alpha-1', session_id: 'alpha-1', agent_type: 'claude', board_project: 'alpha' };
            window.__render.showBoardChatTab('alpha'); window.switchAgenticTab('board', 'top'); return true; })()`);
        await ev(`document.querySelector('.team-open-btn').click(); true`);
        await waitFor(`window.__state.selectedTeam === 'alpha' && !document.getElementById('team-center-view').hidden`, 'the team view');
        await ev(`window.switchAgenticTab('tasks', 'top'); true`);
        await ev(`document.querySelector('.team-center-close').click(); true`);
        await waitFor(`!window.__state.selectedTeam`, 'the team view closed');
        await nextFrame();
        const side = await ev(`({ active: document.querySelector('#agentic-state .agentic-tab.active')?.id,
            shown: [...document.querySelectorAll('#agentic-block-top > .agentic-panel')].filter(p => p.offsetParent && getComputedStyle(p).display !== 'none').map(p => p.id) })`);
        assert.equal(side.active, 'agentic-tab-tasks', 'Tasks stays the active tab');
        assert.deepEqual(side.shown, ['agentic-panel-tasks'], `only the Tasks panel is shown (${JSON.stringify(side.shown)})`);
        await ev(`window.switchAgenticTab('board', 'top'); true`);
        await waitFor(`!!document.getElementById('agentic-panel-board').offsetParent && document.getElementById('agentic-panel-board').classList.contains('active')`, 'the board tab still opens normally');
        console.log('   ✓ team chat not stacked under Tasks; the board tab still works');

        console.log('#9 Tasks panel in a narrow sidebar');
        await ev(`(async () => {
            const tasks = await import(${S}+'tasks.js');
            window.__opened = null; window.showTaskDetailModal = id => { window.__opened = id; };
            document.getElementById('qa-narrow')?.remove();
            document.getElementById('board-tasks-section')?.remove();
            document.getElementById('board-task-list')?.remove();
            document.body.insertAdjacentHTML('beforeend', '<div id="qa-narrow" style="position:fixed;left:0;top:0;width:420px;height:600px;overflow:auto;z-index:99999;background:#111"><section id="board-tasks-section" style="display:block"><div class="board-tasks-header"><span></span></div><div id="board-task-hide-toggle-container"></div><div id="board-task-list" class="board-task-list"></div></section></div>');
            window.__state.currentAgentTasks = []; window.__state.currentSubagents = [];
            window.__state.currentBoardTasks = [
                { id: 1, title: 'Build the login page with validation', status: 'in_progress', priority: 'high', assigned_to: 'Lead Developer', claimed_at: '2026-10-13T10:00:00Z' },
                { id: 2, title: 'Write login tests for the happy and error paths', status: 'blocked', priority: 'medium', assigned_to: 'QA Engineer', blocked_by: [1] },
                { id: 3, title: 'Backlog item', status: 'pending', priority: 'low' },
            ];
            tasks.renderBoardTaskList();
            return true;
        })()`);
        const t = await ev(`(() => {
            const box = document.getElementById('qa-narrow').getBoundingClientRect();
            const list = document.getElementById('board-task-list');
            const rows = [...list.querySelectorAll('.board-task-item:not(.board-task-header)')];
            return { sideways: list.scrollWidth > list.clientWidth + 1 || document.getElementById('qa-narrow').scrollWidth > 421,
                rows: rows.map(r => { const d = r.querySelector('.board-task-desc').getBoundingClientRect();
                    return { id: r.querySelector('.board-task-id').textContent.trim(), titleVisible: d.width > 150 && d.left >= box.left && d.right <= box.right + 1,
                        priority: r.querySelector('.board-task-priority').textContent.trim(), status: r.querySelector('.board-task-status-label')?.textContent.trim(),
                        idTag: r.querySelector('.board-task-id').tagName }; }) };
        })()`);
        assert.equal(t.sideways, false, 'no sideways scroll in a 420px panel');
        for (const r of t.rows) assert.ok(r.titleVisible, `${r.id}: title inside the panel (${JSON.stringify(t.rows)})`);
        assert.deepEqual(t.rows.map(r => r.priority), ['High', 'Medium', 'Low'], 'priority as words');
        assert.equal(t.rows.find(r => r.id === '#2').status, 'Blocked', 'a blocked task reads Blocked');
        assert.ok(t.rows.every(r => r.idTag === 'BUTTON'), 'each task id is a button');
        await ev(`document.querySelector('#board-task-list .board-task-item:not(.board-task-header) .board-task-open').focus(); true`);
        assert.equal(await ev(`document.activeElement?.classList.contains('board-task-open')`), true, 'the task button takes focus');
        await key('Enter', 'Enter', 13);
        await waitFor(`window.__opened === 1`, 'task #1 opened from the keyboard');
        await ev(`document.getElementById('qa-narrow').remove(); true`);
        console.log('   ✓ titles visible, High/Medium/Low, Blocked, opens with Enter');

        console.log('#8 A failed workflow step shows why');
        await ev(`(() => {
            const prev = window.fetch;
            const json = body => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
            window.fetch = async (url, opts) => {
                const u = String(url);
                if (/\\/api\\/workflows\\/runs\\/77(\\?|$)/.test(u)) return json({ id: 77, workflow_id: 5, workflow_name: 'fail-flow', status: 'failed', trigger_type: 'api',
                    started_at: '2026-10-13T10:00:00Z', finished_at: '2026-10-13T10:00:01Z', error_msg: 'step 1 (two) failed: exit code 3',
                    steps: [
                        { index: 0, name: 'one', type: 'shell', status: 'completed', exit_code: 0, output_tail: 'first line of output\\nsecond line', files: ['stdout.txt'], started_at: '2026-10-13T10:00:00Z', finished_at: '2026-10-13T10:00:00Z' },
                        { index: 1, name: 'two', type: 'shell', status: 'failed', exit_code: 3, error_tail: 'warming up\\nboom: config missing\\n', files: ['stderr.txt'], started_at: '2026-10-13T10:00:00Z', finished_at: '2026-10-13T10:00:01Z' },
                    ] });
                if (/\\/api\\/workflows\\/5(\\?|$)/.test(u)) return json({ id: 5, name: 'fail-flow', description: '', steps: [], enabled: 1 });
                if (/\\/api\\/workflows\\/5\\/runs/.test(u)) return json({ runs: [] });
                return prev(url, opts);
            };
            return true;
        })()`);
        await ev(`window.switchNavTab('workflows'); true`);
        await waitFor(`typeof window.selectWorkflowRun === 'function'`, 'workflows loaded');
        await ev(`window.selectWorkflowRun(77); true`);
        await waitFor(`!!document.querySelector('#workflows-view .wf-run-step-error')`, 'the failed step error output', { diag: `document.getElementById('workflows-view')?.innerText.slice(0, 300)` });
        const run = await ev(`(() => {
            const v = document.getElementById('workflows-view');
            const err = v.querySelector('.wf-run-step-error');
            const collapsed = v.querySelector('.wf-run-step-output:not(.wf-expanded)');
            const toggle = v.querySelector('.wf-run-step-output-toggle');
            return { error: err.textContent, header: v.querySelector('.wf-run-error')?.textContent.trim(),
                collapsedHeight: collapsed ? collapsed.getBoundingClientRect().height : null,
                toggleTag: toggle?.tagName, toggleExpanded: toggle?.getAttribute('aria-expanded') };
        })()`);
        assert.equal(run.error, 'warming up\nboom: config missing', 'the failed step shows its stderr');
        assert.match(run.header, /step 1 \(two\) failed: exit code 3: boom: config missing$/, 'the run header quotes the last stderr line');
        assert.equal(run.collapsedHeight, 0, 'a collapsed output box shows nothing');
        assert.equal(run.toggleTag, 'BUTTON', 'the Output toggle is a button');
        assert.equal(run.toggleExpanded, 'false');
        await ev(`document.querySelector('#workflows-view .wf-run-step-output-toggle').click(); true`);
        await waitStable(`document.querySelector('#workflows-view .wf-run-step-output').getBoundingClientRect().height`, 'the expanded output', 150);
        assert.equal(await ev(`document.querySelector('#workflows-view .wf-run-step-output-toggle').getAttribute('aria-expanded')`), 'true');
        assert.ok(await ev(`document.querySelector('#workflows-view .wf-run-step-output').getBoundingClientRect().height > 20`), 'expanded output is shown');
        console.log('   ✓ stderr inline, header excerpt, no clipped strip, toggle is a button');

        console.log('#3590 Board view: avatar rows, a single newline is a line break');
        await ev(`window.selectBoardProject('alpha'); true`);
        await waitFor(`[...document.querySelectorAll('#mb-messages .mb-message')].some(m => m.textContent.includes('first line'))`, 'the board view');
        await rowCheck('mb-messages', 'board view');
        console.log('   ✓ initials in the palette colour, no box, <br> between lines, a run shares the avatar');

        console.log('PASS board_team_ux_browser');
    } finally {
        await client.close();
    }
})().catch(err => { console.error(err); process.exit(1); });
