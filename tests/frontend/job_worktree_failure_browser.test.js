// A job whose worktree or branch fails (#6707, UI Accessibility's #6702 finding 3): the form's Base branch starts empty (the repo's current branch, said in its placeholder) instead of "main", and a save the
// server refuses for the branch (400, field base_branch) is said under the field as an alert with More options opened and the focus on it, not in a toast; a run that failed for it says so on the job's row in the
// server's plain sentence (not "never run" with the cause out of sight) and is announced once, in the status that is always in the page, when it happens while the page is open (what failed before the page opened is
// on the row, not announced). The stub's last_run carries error_msg and error_code (Backend's #6701). 1280 and 390 px. Drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const S = `(window.__coralStatic || '/static/')`;
const MISSING = 'The base branch main does not exist in /work/repo. Its branches: master. Choose one under More options > Base Branch.';

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 900, false], [390, 844, true]]) {
            const tag = `${w}px`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`(() => { const real = window.fetch; const json = (o, status) => Promise.resolve(new Response(JSON.stringify(o), { status: status || 200, headers: { 'Content-Type': 'application/json' } }));
                window.__saves = []; window.__saveAnswer = null;
                const job = { id: 5, name: 'nightly-agent', description: '', timezone: 'UTC', agent_type: 'claude', repo_path: '/work/repo', base_branch: 'main', prompt: 'Tidy.', enabled: 1, max_duration_s: 3600, cleanup_worktree: 0, flags: '', job_type: 'prompt', cron_expr: '0 2 * * *', next_fire_at: new Date(Date.now() + 3600000).toISOString(), consecutive_failures: 0, last_run: { id: 1, status: 'completed', started_at: new Date(Date.now() - 3600000).toISOString() } };
                window.__jobs = [job];
                window.fetch = (u, o) => { const url = String(u), m = (o && o.method) || 'GET';
                    if (m === 'GET' && /\\/api\\/scheduled\\/jobs$/.test(url)) return json({ jobs: window.__jobs });
                    if (m === 'POST' && /\\/api\\/scheduled\\/jobs$/.test(url)) { window.__saves.push(JSON.parse(o.body)); return window.__saveAnswer ? json(window.__saveAnswer, 400) : json({ id: 9 }); }
                    if (/\\/api\\/scheduled\\/jobs\\/\\d+\\/runs/.test(url)) return json({ runs: [] });
                    if (m === 'GET' && /\\/api\\/workflows$/.test(url)) return json({ workflows: [] });
                    return real(u, o); };
                return true; })()`);
            const sched = await ev(`import(${S} + 'scheduler.js').then(m => { window.__sched = m; return Object.keys(m).includes('refreshJobs'); })`);
            assert.ok(sched, 'scheduler.js exports refreshJobs');
            await ev(`window.switchNavTab('jobs'); window.__sched.refreshJobs().then(() => true)`);
            await waitFor(`!!document.querySelector('#scheduled-jobs-list .sched-row')`, 'the job row', 8000);

            console.log(`${tag}: the form`);
            await ev(`window.showJobModal(); window.switchJobType('prompt'); true`);
            const f = await ev(`({ value: document.getElementById('job-modal-branch').value, placeholder: document.getElementById('job-modal-branch').placeholder })`);
            check(`${tag}: Base branch starts empty, and its placeholder says the repo's current branch`, f.value === '' && /current branch/.test(f.placeholder), JSON.stringify(f));
            await ev(`{ const set = (id, v) => { const i = document.getElementById(id); i.value = v; i.dispatchEvent(new Event('input', { bubbles: true })); }; set('job-modal-name', 'agent-job'); set('job-modal-repo', '/work/repo'); set('job-modal-prompt', 'Tidy the repo.'); document.querySelector('.sched-preset').click(); } true`);
            await ev(`window.__saveAnswer = { error: ${JSON.stringify(MISSING)}, code: 'base_branch_missing', field: 'base_branch', branches: ['master'] }; document.querySelectorAll('.toast').forEach(t => t.remove()); document.getElementById('job-modal-more').open = false; true`);
            await ev(`window.saveScheduledJob(); true`);
            await waitFor(`!document.getElementById('job-modal-branch-msg').hidden`, 'the inline error', 6000);
            await sleep(300); // sleep-ok: the absence of a toast
            const e = await ev(`(() => { const i = document.getElementById('job-modal-branch'), m = document.getElementById('job-modal-branch-msg');
                return { text: m.textContent, role: m.getAttribute('role'), invalid: i.getAttribute('aria-invalid'), open: document.getElementById('job-modal-more').open, focus: document.activeElement === i, toasts: [...document.querySelectorAll('.toast')].map(t => t.textContent.trim()), sent: window.__saves.map(s => s.base_branch) }; })()`);
            check(`${tag}: a refused branch is said under the field (alert), the field marked, More options opened, the focus on it, no toast`, e.text === MISSING && e.role === 'alert' && e.invalid === 'true' && e.open && e.focus && e.toasts.length === 0, JSON.stringify(e));
            check(`${tag}: the Base branch field is described by the message (a person who comes back to the field hears it again, not only when it first appears)`, (await ev(`document.getElementById('job-modal-branch').getAttribute('aria-describedby') || ''`)).split(' ').includes('job-modal-branch-msg'));
            check(`${tag}: and the empty field was sent as empty (the server's default, not "main")`, e.sent[0] === '', JSON.stringify(e.sent));
            await ev(`{ const i = document.getElementById('job-modal-branch'); i.value = 'master'; i.dispatchEvent(new Event('input', { bubbles: true })); } true`);
            check(`${tag}: editing the field takes the line and the mark away`, await ev(`document.getElementById('job-modal-branch-msg').hidden && !document.getElementById('job-modal-branch').hasAttribute('aria-invalid')`));
            await ev(`window.hideJobModal(); true`);

            console.log(`${tag}: a run that fails for its worktree`);
            const quiet = await ev(`document.getElementById('jobs-live').textContent`);
            check(`${tag}: the status is in the page and empty while nothing failed`, quiet === '' && await ev(`document.getElementById('jobs-live').getAttribute('role') === 'status' && !document.getElementById('jobs-live').closest('[hidden]')`));
            await ev(`window.__jobs[0] = { ...window.__jobs[0], last_run: { id: 2, status: 'failed', started_at: new Date().toISOString(), error_msg: ${JSON.stringify(MISSING)}, error_code: 'base_branch_missing' }, consecutive_failures: 1 }; window.__sched.refreshJobs().then(() => true)`);
            await waitFor(`!!document.querySelector('#scheduled-jobs-list .sched-row-problem')`, 'the failure on the row', 6000);
            const r = await ev(`({ problem: document.querySelector('#scheduled-jobs-list .sched-row-problem').textContent, dot: document.querySelector('#scheduled-jobs-list .sched-dot').getAttribute('aria-label'), live: document.getElementById('jobs-live').textContent })`);
            check(`${tag}: the row says the run failed and why, in the server's sentence, and the dot says "Last run failed"`, r.problem === MISSING && r.dot === 'Last run failed', JSON.stringify(r));
            check(`${tag}: and it is announced once, with the job's name, in the status`, r.live === `nightly-agent: the last run failed. ${MISSING}`, JSON.stringify(r.live));
            await ev(`document.getElementById('jobs-live').textContent = ''; window.__sched.refreshJobs().then(() => true)`);
            await sleep(300); // sleep-ok: a second look at the same failed run must say nothing
            check(`${tag}: the same failed run is not announced again on the next look`, (await ev(`document.getElementById('jobs-live').textContent`)) === '');
        }
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} job worktree failure checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
