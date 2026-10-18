// The task detail (#4702): _taskDetailHtml (tasks.js) is a 230-line function that writes the whole detail of a task, board or agent: its lifecycle, priority and result, the instruction revision and its
// amendments, the description, the workflow (gates, results, inputs, completion candidate and its artifacts), who and when, what blocks it, its cost and its running cost. Splitting it must change nothing.
// This opens the detail of a task of every shape (each status, a failed result, a review pending, a completion candidate with a released slot, amendments, a workflow with everything, every kind of
// dependency, a cost over a dollar, a running cost, text with markup in it, and an agent's own task) and records the detail and its footer. Compared with fixtures/task_detail_golden.json (sha256 per task,
// with the readable text). CORAL_UPDATE_GOLDEN=1 rewrites it. Dates are read in UTC and "(… ago)" is normalised. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const GOLDEN = path.join(__dirname, 'fixtures', 'task_detail_golden.json');
const sha = t => require('node:crypto').createHash('sha256').update(t).digest('hex');
const D = '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef';

const base = { priority: 'medium', created_by: 'Orchestrator', created_at: '2026-10-01T09:00:00Z' };
const TASKS = {
    'a new task': { ...base, id: 1, title: 'Plain', status: 'pending' },
    'a draft': { ...base, id: 2, title: 'Draft one', status: 'draft', body: 'Not published yet.', priority: 'low' },
    'in progress, claimed': { ...base, id: 3, title: 'Doing it', status: 'in_progress', priority: 'high', assigned_to: 'Dev', claimed_at: '2026-10-01T10:00:00Z', body: 'Line one.\nLine two.' },
    'blocked, every kind of dependency': { ...base, id: 4, title: 'Waits', status: 'blocked', priority: 'critical', blocked_by: [
        { task_id: 10, status: 'completed', title: 'Done one' }, { task_id: 11, status: 'in_progress', title: 'Running' }, { task_id: 12, status: 'skipped' }, { task_id: 13, status: 'blocked', condition: 'any' },
        { task_id: 14, status: 'draft', required_artifacts: ['report.md', 'plan.md'] }, { task_id: 15, status: 'pending', satisfied: false, blocked_reason: 'its artifact is missing' },
        { task_id: 16, status: 'pending', board_id: 'other-board', board_label: 'Other board', title: 'Elsewhere' }, { task_id: 17, status: 'completed', board_id: 'team-a' }] },
    'completed with a message and a cost': { ...base, id: 5, title: 'Finished work', status: 'completed', assigned_to: 'Dev', claimed_at: '2026-10-01T10:00:00Z', completed_at: '2026-10-01T12:00:00Z', completed_by: 'Dev',
        completion_message: 'All done, see <b>notes</b>.', cost_usd: 0.4321, input_tokens: 1234, output_tokens: 56789, cache_read_tokens: 1234567, cache_write_tokens: 0 },
    'completed over a dollar': { ...base, id: 6, title: 'Pricey', status: 'completed', completed_at: '2026-10-01T12:00:00Z', completed_by: 'QA', cost_usd: 12.5, input_tokens: 5, output_tokens: 6, cache_read_tokens: 7, cache_write_tokens: 8 },
    'failed without a reason': { ...base, id: 7, title: 'Broke', status: 'completed', completed_at: '2026-10-01T12:00:00Z', completed_by: 'Dev', workflow: { outcome: 'failed' } },
    'failed with a reason': { ...base, id: 8, title: 'Broke loudly', status: 'completed', completed_at: '2026-10-01T12:00:00Z', completed_by: 'Dev', completion_message: 'tests red', workflow: { outcome: 'failed' } },
    'cancelled': { ...base, id: 9, title: 'Dropped', status: 'skipped', completed_at: '2026-10-01T12:00:00Z', completed_by: 'Operator', completion_message: 'not needed' },
    'review pending, slot released': { ...base, id: 20, title: 'In review', status: 'review_pending', assigned_to: 'Dev', claimed_at: '2026-10-01T10:00:00Z', workflow: { completion_review: {
        submitted_by: 'Dev', submitted_at: '2026-10-01T11:00:00Z', proposed_outcome: 'success', reason: 'all gates pass', message: 'see artifact', released_by: 'Orchestrator', released_at: '2026-10-01T11:05:00Z', release_reason: 'reviewing',
        artifacts: [{ name: 'report', revision: 'r2', digest: 'sha256:abc', uri: 'coral://artifacts/' + D }, { name: 'page', uri: 'https://example.com/a?b=1&c="2"' }, { name: 'note', uri: 'file:///x', content: 'raw <text>' }] } } },
    'completion review, slot occupied': { ...base, id: 21, title: 'Candidate', status: 'in_progress', assigned_to: 'Dev', claimed_at: '2026-10-01T10:00:00Z', workflow: { completion_review: {
        submitted_by: 'Dev', submitted_at: '2026-10-01T11:00:00Z', proposed_outcome: 'failed', reason: 'cannot', artifacts: [] } } },
    'a workflow with everything': { ...base, id: 22, title: 'Workflow task', status: 'in_progress', assigned_to: 'Dev', claimed_at: '2026-10-01T10:00:00Z', revision: 1, workflow: {
        name: 'build', stage: 'verify', instructions: 'Do the <thing>.', required_outputs: ['report', 'diff'], completion_gates: [{ type: 'artifact', artifact: 'report' }, { name: 'tests', type: 'check', check_id: 'go-test' }],
        candidate_revision: 'abc123', gate_results: [{ name: 'tests', passed: true }, { type: 'artifact', passed: false, details: 'missing report' }], parent_task_id: 4, retry_of: 3,
        inputs: [{ board_id: 'team-a', task_id: 30, outcome: 'success', artifacts: [{ name: 'in', uri: 'https://example.com/in' }] }, { board_id: 'team-b', task_id: 31, outcome: 'failed', artifacts: [] }],
        artifacts: [{ name: 'out', digest: 'sha256:def', uri: 'coral://artifacts/' + D }] } },
    'amended twice': { ...base, id: 23, title: 'Changed', status: 'pending', revision: 3, body: 'Now this.', workflow: { amendments: [
        { revision: 2, actor: 'Planner', created_at: '2026-10-02T09:00:00Z', reason: 'clarify', changes: { workflow_instructions: 'new' }, previous_snapshot: { workflow_instructions: 'old '.repeat(80) }, effective_snapshot: { workflow_instructions: 'new text' } },
        { revision: 3, created_at: '2026-10-03T09:00:00Z', changes: { body: 'x' }, previous_snapshot: {}, effective_snapshot: {} }] } },
    'markup in every text': { ...base, id: 24, title: '<img src=x onerror=alert(1)> "quoted" & more', status: 'in_progress', priority: 'high', assigned_to: '<i>me</i>', created_by: 'a"b', claimed_at: '2026-10-01T10:00:00Z', body: '<script>1</script>' },
    'running, with a live cost': { ...base, id: 25, title: 'Costing now', status: 'in_progress', assigned_to: 'Dev', session_id: 'sess-25', claimed_at: '2026-10-01T10:00:00Z' },
};
const AGENT_TASK = { id: 40, title: 'My own task', agent_name: 'solo', started_at: '2026-10-01T10:00:00Z', created_at: '2026-10-01T09:00:00Z', completed: false, cost_usd: 2.25, input_tokens: 10, output_tokens: 20, cache_read_tokens: 30, cache_write_tokens: 40 };

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const out = {};
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setTimezoneOverride({ timezoneId: 'UTC' }); await Emulation.setLocaleOverride({ locale: 'en-US' });
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        await ev(`(async () => {
            const st = window.__coralStatic || '/static/';
            const T = window.T = { state: (await import(st + 'state.js')).state, tasks: await import(st + 'tasks.js') };
            const FROZEN = Date.parse('2026-10-18T12:00:00Z'); Date.now = () => FROZEN; // the claimed-for time reads the clock: frozen, the trace does not drift with the hour
            T.state.currentSession = { name: 'solo', type: 'live', board_project: 'team-a', session_id: 'sid-solo' }; T.state.selectedTeam = null;
            window.__tasks = ${JSON.stringify(TASKS)}; window.__agentTask = ${JSON.stringify(AGENT_TASK)};
            const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
            const real = window.fetch;
            window.fetch = (u, o = {}) => { const p = new URL(String(u), location.href);
                if (/\\/tasks\\/25\\/cost$/.test(p.pathname)) return json({ cost_usd: 1.5, input_tokens: 11, output_tokens: 22, cache_read_tokens: 33, cache_write_tokens: 44, request_count: 7 });
                if (/\\/tasks\\/\\d+$/.test(p.pathname) || /\\/tasks$/.test(p.pathname) || /\\/subscribers$/.test(p.pathname)) return json(/\\/tasks$/.test(p.pathname) ? { tasks: T.state.currentBoardTasks } : (/\\/subscribers$/.test(p.pathname) ? [] : (T.state.currentBoardTasks.find(t => p.pathname.endsWith('/' + t.id)) || {})));
                return real(u, o); };
            return true; })()`);
        const SNAP = `(() => { const c = document.getElementById('task-detail-content'), f = document.getElementById('task-detail-modal-footer');
            const norm = s => s.replace(/\\(\\d+[a-z]+(?: \\d+[a-z]+)? ago\\)/g, '(… ago)');
            return { title: document.getElementById('task-detail-modal-title').textContent, html: norm(c.innerHTML), text: norm(c.textContent.replace(/\\s+/g, ' ').trim()), footer: f ? f.innerHTML.replace(/\\s+/g, ' ').trim() : null }; })()`;
        for (const [name, task] of Object.entries(TASKS)) {
            if (task.id === 25) {
                await ev(`T.state.currentBoardTasks = [window.__tasks[${JSON.stringify(name)}]]; T.state.selectedTeam = 'team-a'; T.tasks.startBoardTaskPoll(); true`);
                await waitFor(`!!document.querySelector('.board-task-cost-live')`, 'the live cost in the list', { timeout: 8000 });
                await ev(`T.tasks.stopBoardTaskPoll(); T.state.selectedTeam = null; true`);
            }
            await ev(`T.state.currentBoardTasks = [window.__tasks[${JSON.stringify(name)}]]; T.tasks.hideTaskDetailModal && T.tasks.hideTaskDetailModal(); true`);
            await ev(`T.tasks.showTaskDetailModal(${task.id}); true`);
            await waitFor(`document.getElementById('task-detail-content').textContent.includes(${JSON.stringify('#' + task.id)})`, name);
            out[name] = await ev(SNAP);
        }
        assert.match(out['running, with a live cost'].text, /Running Cost/);
        await ev(`T.state.currentAgentTasks = [window.__agentTask]; T.tasks.hideTaskDetailModal(); T.tasks.showAgentTaskDetailModal(40); true`);
        await waitFor(`document.getElementById('task-detail-content').textContent.includes('#40')`, 'the agent task');
        out['an agent task'] = await ev(SNAP);
        assert.match(out['an agent task'].text, /My own task/);
        const hashes = {}, lines = {};
        for (const [name, s] of Object.entries(out)) { hashes[name] = sha(JSON.stringify(s)); lines[name] = `${s.title} | ${s.text.slice(0, 700)} | footer ${s.footer.replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ').trim()}`; }
        if (process.env.CORAL_UPDATE_GOLDEN) {
            fs.mkdirSync(path.dirname(GOLDEN), { recursive: true });
            fs.writeFileSync(GOLDEN, JSON.stringify({ about: 'what the task detail shows for each task, for task_detail_golden_browser.test.js: sha256 of the detail html, footer and title, and the readable text. CORAL_UPDATE_GOLDEN=1 rewrites it.', hashes, lines }, null, 2) + '\n');
            console.log('golden written: ' + Object.keys(hashes).length + ' tasks');
            return;
        }
        const want = JSON.parse(fs.readFileSync(GOLDEN, 'utf8'));
        assert.deepEqual(Object.keys(hashes), Object.keys(want.hashes), 'the same tasks');
        for (const k of Object.keys(want.hashes)) if (hashes[k] !== want.hashes[k]) assert.fail(`${k}: changed.\n  now:  ${lines[k]}\n  was:  ${want.lines[k]}`);
        console.log(`PASS task detail golden: ${Object.keys(want.hashes).length} tasks identical`);
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
