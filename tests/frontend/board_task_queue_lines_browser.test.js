// #3343: in the board chat, each run of consecutive "Coral Task Queue" lines is one muted,
// collapsible row, so what people and agents wrote isn't buried. The messages themselves are
// unchanged (the @mention notifications they carry are server-side and untouched); select mode
// shows every line so each can still be selected.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');

const BASE = require('./test_base').BASE;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation } = client;
    const ev = async expression => {
        const result = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
        return result.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const S = `(window.__coralStatic||'/static/')`;
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await coralReady();

        console.log('The grouping');
        const g = await ev(`(async () => {
            const m = await import(${S}+'board_system_lines.js');
            const Q = 'Coral Task Queue';
            const msgs = [
                { id: 1, subscriber_id: 'QA', job_title: 'QA', content: 'hello' },
                { id: 2, subscriber_id: Q, is_system: true, content: '@notify-all [New Task #7 (low)] Fix it — run claim' },
                { id: 3, subscriber_id: Q, is_system: true, content: '[Task #7 claimed by Backend Dev] Fix it' },
                { id: 4, subscriber_id: Q, is_system: true, content: '[Task #7 completed by Backend Dev] Outcome: success' },
                { id: 5, subscriber_id: 'Lead', job_title: 'Lead', content: 'done?' },
                { id: 6, subscriber_id: Q, is_system: true, content: '@Backend Dev [Task #8 (high)] <b>x</b> — assigned to you' },
            ];
            const items = m.groupTaskQueueRuns(msgs);
            const box = document.createElement('div');
            box.innerHTML = items.map(it => it.kind === 'queue' ? m.renderTaskQueueRun(it, () => 'T') : '<div class="plain">' + it.m.content + '</div>').join('');
            document.body.appendChild(box);
            const runs = [...box.querySelectorAll('.mb-queue-run')];
            const out = {
                kinds: items.map(it => it.kind + (it.msgs ? ':' + it.msgs.length : '')),
                labels: runs.map(r => r.querySelector('.mb-queue-label').textContent),
                lines: runs[0].querySelectorAll('.mb-queue-line').length,
                ids: [...runs[0].querySelectorAll('.mb-queue-line')].map(l => l.dataset.messageId),
                openAtFirst: runs[0].open,
                escaped: !runs[1].querySelector('b'),
                noCollapse: m.groupTaskQueueRuns(msgs, false).every(it => it.kind === 'msg'),
            };
            // Opened rows stay open across a re-render (the chat re-renders on every poll):
            // opened the way a click does (no inline handler: the Hub's CSP strips those).
            await new Promise(res => { runs[0].addEventListener('toggle', res, { once: true }); runs[0].open = true; });
            box.innerHTML = m.groupTaskQueueRuns(msgs).map(it => it.kind === 'queue' ? m.renderTaskQueueRun(it, () => 'T') : '').join('');
            out.openAfter = box.querySelector('.mb-queue-run[data-queue-run="2"]').open;
            out.otherClosed = !box.querySelector('.mb-queue-run[data-queue-run="6"]').open;
            // The row grows at both ends (a newer line; older ones from Load earlier): still open.
            const grown = [{ id: 1.5, subscriber_id: Q, is_system: true, content: '[Task #6 claimed by QA] y' }, ...msgs.slice(1, 4), { id: 4.5, subscriber_id: Q, is_system: true, content: '[Task #9 claimed by QA] z' }];
            box.innerHTML = m.groupTaskQueueRuns(grown).map(it => m.renderTaskQueueRun(it, () => 'T')).join('');
            out.grownOpen = box.querySelector('.mb-queue-run').open && box.querySelectorAll('.mb-queue-line').length === 5;
            // Closing it forgets it.
            const row = box.querySelector('.mb-queue-run');
            await new Promise(res => { row.addEventListener('toggle', res, { once: true }); row.open = false; });
            box.innerHTML = m.groupTaskQueueRuns(msgs).map(it => it.kind === 'queue' ? m.renderTaskQueueRun(it, () => 'T') : '').join('');
            out.closedAfter = !box.querySelector('.mb-queue-run[data-queue-run="2"]').open;
            out.noInlineHandler = !box.innerHTML.includes('ontoggle');
            box.remove();
            return out;
        })()`);
        assert.deepEqual(g.kinds, ['msg', 'queue:3', 'msg', 'queue:1']);
        assert.equal(g.labels[0], '3 task updates · Task #7 completed by Backend Dev · Task #7 claimed by Backend Dev · New Task #7 (low)');
        assert.equal(g.labels[1], 'Task #8 (high)', 'a single line: its own summary');
        assert.equal(g.lines, 3); assert.deepEqual(g.ids, ['2', '3', '4']);
        assert.equal(g.openAtFirst, false, 'collapsed by default');
        assert.ok(g.escaped, 'content is text, never markup');
        assert.ok(g.noCollapse, 'select mode: nothing collapsed');
        assert.ok(g.openAfter && g.otherClosed, 'an opened row stays open across re-renders');
        assert.ok(g.grownOpen, 'and when it grows at either end');
        assert.ok(g.closedAfter, 'a closed row stays closed');
        assert.ok(g.noInlineHandler, 'no inline handler (the Hub CSP strips them)');
        console.log('   ✓ runs grouped, summarised, escaped, open state kept');

        console.log('The team chat');
        await ev(`(() => {
            const real = window.fetch.bind(window);
            const json = body => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
            const Q = 'Coral Task Queue';
            const msgs = [];
            for (let i = 1; i <= 30; i++) {
                const queue = i % 5 !== 0;
                msgs.push({ id: i, project: 'beta', subscriber_id: queue ? Q : 'Lead Developer', is_system: queue, job_title: queue ? '' : 'Lead Developer',
                    content: queue ? '[Task #' + i + ' claimed by QA] t' : 'human ' + i, created_at: new Date(Date.UTC(2026, 9, 14, 8, 0, i)).toISOString() });
            }
            window.fetch = async (url, opts) => {
                const u = String(url);
                if (u.includes('/api/board/beta/messages/all')) {
                    const limit = Number(new URL(u, location.href).searchParams.get('limit') || 50);
                    return json({ total: msgs.length, messages: msgs.slice(-limit) });
                }
                if (u.includes('/api/board/beta/status')) return json({ summary: { available: 1 }, observed_at: new Date().toISOString(), agents: [], unassigned_tasks: [], health_report: [] });
                if (u.includes('/api/board/beta/working-mode/presets')) return json({ presets: [{ id: 'none', name: 'None', builtin: true, instructions: '' }] });
                if (u.includes('/api/board/beta/working-mode')) return json({ mode: 'none', dependency_guidance: false, custom_instructions: '' });
                if (u.includes('/api/board/beta/tasks')) return json([]);
                if (u.includes('/api/board/beta/paused')) return json({ paused: false });
                if (u.includes('/api/board/beta/subscribers')) return json([]);
                return real(url, opts);
            };
            return true;
        })()`);
        await ev(`(async () => {
            const render = await import(${S}+'render.js');
            render.renderLiveSessions([{ session_id: 'beta-1', name: 'beta-1', agent_type: 'claude', board_project: 'beta', display_name: 'Beta agent', working_directory: '/tmp/beta' }]);
            document.querySelector('.board-card-header[data-on-click="team-name-open"]').click();
            return true;
        })()`);
        await waitFor(`!!document.querySelector('#board-panel-msgs .mb-queue-run')`, 'the team chat with collapsed task-queue rows');
        const panel = await ev(`(() => { const p = document.getElementById('board-panel-msgs');
            return { runs: p.querySelectorAll('.mb-queue-run').length, bubbles: p.querySelectorAll('.mb-message').length,
                     humans: [...p.querySelectorAll('.mb-message-body')].map(b => b.textContent.trim()),
                     firstRun: p.querySelector('.mb-queue-label').textContent }; })()`);
        assert.equal(panel.bubbles, 6, 'only the 6 human messages are full messages');
        assert.equal(panel.runs, 6, 'each run of 4 task-queue lines between them is one row');
        assert.deepEqual(panel.humans, ['human 5', 'human 10', 'human 15', 'human 20', 'human 25', 'human 30']);
        assert.match(panel.firstRun, /^4 task updates · Task #4 claimed by QA/);
        console.log('   ✓ 24 task-queue lines are 6 rows; the 6 human messages stand out');
        console.log('PASS board task-queue lines are collapsed');
    } finally { await client.close(); }
})().catch(error => { console.error(error); process.exit(1); });
