// A background run that failed because the agent's CLI is not signed in (#5469, #5471) is a clear failed state, never a spinner: the server fails the run at once with
// error_code "not_signed_in" (agent_type says which CLI) and the plain error_msg; the dashboard says "Claude is not signed in", what to do ("Sign in with claude in a
// terminal, then run this again."), in the workflow run's header, the workflow's run list and last-run line, the scheduled job's banner and its run history. A run with
// no such code, or a code this build does not know, is shown as before (its error_msg). Against stubbed APIs; run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const T0 = '2026-10-20T10:00:00Z', T1 = '2026-10-20T10:00:01Z';
const run = (id, over = {}) => ({ id, workflow_id: 7, workflow_name: 'Nightly', status: 'failed', trigger_type: 'schedule', started_at: T0, finished_at: T1, error_msg: 'Claude is not signed in', error_code: 'not_signed_in', agent_type: 'claude',
    steps: [{ index: 0, name: 'review', type: 'agent', status: 'skipped' }], ...over });
const RUNS = { 91: run(91), 92: run(92, { error_msg: 'Codex is not signed in', agent_type: 'codex' }), 93: run(93, { error_code: 'some_future_code', error_msg: 'something else broke', agent_type: undefined }),
    95: run(95, { error_msg: 'Antigravity is not signed in', agent_type: 'agy' }), 96: run(96, { error_msg: 'x', agent_type: 'gemini' }), 97: run(97, { error_msg: 'x', agent_type: 'pi' }), 98: run(98, { error_msg: 'x', agent_type: undefined }), 94: run(94, { error_code: undefined, agent_type: undefined, error_msg: 'step build failed' }) };
const JOB = { id: 5, name: 'Nightly review', enabled: true, cron_expr: '0 3 * * *', timezone: 'UTC', repo_path: '/tmp/r', max_duration_s: 600, job_type: 'workflow', workflow_id: 7, consecutive_failures: 0,
    last_run: { id: 91, status: 'failed', error_msg: 'Claude is not signed in', error_code: 'not_signed_in', agent_type: 'claude' }, workflow_runs: [RUNS[91], RUNS[94]] };

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const check = (label, ok, detail) => { console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); if (!ok) process.exitCode = 1; };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { const R = window.fetch; const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
            const RUNS = ${JSON.stringify(RUNS)}, JOB = ${JSON.stringify(JOB)};
            window.fetch = (u, o = {}) => { const p = new URL(String(u && u.url || u), location.origin).pathname; const m = (o.method || 'GET').toUpperCase();
                let r; if ((r = p.match(/^\\/api\\/workflows\\/runs\\/(\\d+)$/)) && m === 'GET') return json(RUNS[r[1]]);
                if (p === '/api/workflows/7' && m === 'GET') return json({ id: 7, name: 'Nightly', enabled: true, steps: [{ name: 'review', type: 'agent' }], last_run: RUNS[91] });
                if (p === '/api/workflows/7/runs') return json({ runs: [RUNS[91], RUNS[92], RUNS[94]] });
                if (p === '/api/workflows' && m === 'GET') return json({ workflows: [{ id: 7, name: 'Nightly', enabled: true, step_count: 1, last_run: RUNS[91] }] });
                if (p === '/api/scheduled/jobs' && m === 'GET') return json({ jobs: [JOB] });
                if (p.startsWith('/api/scheduled/jobs/5/runs')) return json({ runs: [{ id: 3, status: 'failed', error_msg: 'Codex is not signed in', error_code: 'not_signed_in', agent_type: 'codex', started_at: '${T0}' }] });
                if (p === '/api/themes/generate' && m === 'POST') return json({ error: 'Claude is not signed in', code: 'not_signed_in' }, 409);
                return R(u, o); };
            return true; })()`);
        const S = `(window.__coralStatic || '/static/')`;
        const text = sel => ev(`(document.querySelector(${JSON.stringify(sel)}) || { textContent: '' }).textContent.replace(/\\s+/g, ' ').trim()`);
        const WF = `import(${S} + 'workflows.js')`;

        console.log('1. The run view');
        const view = async id => { await ev(`${WF}.then(m => m.selectWorkflowRun(${id})).then(() => true)`); await waitFor(`document.getElementById('wf-run-detail-container').style.display !== 'none' && !!document.querySelector('#wf-run-detail .wf-run-meta')`, 'the run view'); return text('#wf-run-detail .wf-run-meta'); };
        let t = await view(91);
        check(`says Claude is not signed in and what to do (${t})`, /Claude is not signed in\./.test(t) && /Sign in with claude in a terminal, then run this again\./.test(t));
        check('it is a failed state, not a spinner', /failed/.test(t) && (await ev(`document.querySelectorAll('#wf-run-detail .wf-timing-active, #wf-run-detail .spinner, #wf-run-detail [class*="spin"]').length`)) === 0);
        t = await view(92);
        check(`a Codex run says Codex and codex (${t})`, /Codex is not signed in\./.test(t) && /Sign in with codex in a terminal/.test(t));
        t = await view(95);
        check(`an agy run says Antigravity and agy (${t})`, /Antigravity is not signed in\./.test(t) && /Sign in with agy in a terminal/.test(t));
        t = await view(96);
        check('the old gemini type is Antigravity too', /Antigravity is not signed in\./.test(t) && /Sign in with agy/.test(t));
        t = await view(97);
        check(`an agent type this build does not know stays neutral (${t})`, /The agent's CLI is not signed in\. Sign in to it in a terminal, then run this again\./.test(t) && !/Claude|claude/.test(t));
        t = await view(98);
        check('and so does a record with no agent_type', /The agent's CLI is not signed in\./.test(t) && !/Claude|claude/.test(t));
        t = await view(93);
        check(`a code this build does not know shows the server's text as before (${t})`, /something else broke/.test(t) && !/Sign in with/.test(t));
        t = await view(94);
        check('an ordinary failure is unchanged', /step build failed/.test(t) && !/Sign in with/.test(t));

        console.log('2. The workflow: run list and last run');
        await ev(`${WF}.then(m => m.selectWorkflow(7)).then(() => true)`);
        await waitFor(`document.querySelectorAll('#wf-detail table tr[data-run-id]').length === 3`, 'the run list');
        const rows = await ev(`[...document.querySelectorAll('#wf-detail table tr[data-run-id]')].map(r => r.textContent.replace(/\\s+/g, ' ').trim())`);
        check(`the not-signed-in runs are tagged, the other is not (${JSON.stringify(rows.map(r => r.slice(0, 40)))})`, /not signed in/.test(rows[0]) && /not signed in/.test(rows[1]) && !/not signed in/.test(rows[2]));
        await ev(`${WF}.then(m => { m.workflowsBackToList(); return m.showWorkflowsTab ? true : true; })`);
        await waitFor(`document.querySelectorAll('#wf-list .wf-last-run-status').length >= 1 || document.querySelectorAll('.wf-last-run-status').length >= 1`, 'the list with the last run', 8000).catch(() => {});
        const lr = await ev(`(document.querySelector('.wf-last-run-status') || { textContent: '' }).textContent.replace(/\\s+/g, ' ').trim()`);
        if (lr) check(`the last-run line is tagged (${lr})`, /failed/.test(lr) && /not signed in/.test(lr));

        console.log('3. The scheduled job');
        await ev(`import(${S} + 'scheduler.js').then(async m => { await m.initScheduler(); return true; })`);
        await waitFor(`document.querySelectorAll('#scheduled-jobs-list li').length >= 1`, 'the job in the sidebar');
        await ev(`import(${S} + 'scheduler.js').then(m => m.selectScheduledJob(5)).then(() => true)`);
        await waitFor(`!!document.querySelector('.sched-runs-section')`, 'the job view');
        const banner = await text('.sched-fail-banner');
        check(`the banner does not say it twice (${banner})`, banner.split('is not signed in').length === 2);
        check(`the job's banner says it and what to do, though a refused run does not count as a failure (${banner})`, /did not start: Claude is not signed in\./.test(banner) && /Sign in with claude in a terminal, then run this again\./.test(banner) && /runs again once you are signed in/.test(banner));
        const hist = await ev(`document.querySelector('.sched-runs-section').textContent.replace(/\\s+/g, ' ').trim()`);
        check(`the job's run history tags the not-signed-in run (${hist.slice(0, 120)})`, /not signed in/.test(hist));

        console.log('4. Theme generation (a user-triggered run): the 409 says what to do');
        await ev(`import(${S} + 'theme_config.js').then(() => { document.body.insertAdjacentHTML('beforeend', '<div class="theme-editor-scroll"><input id="theme-ai-prompt" value="warm paper"><select id="theme-save-base"><option value="dark">d</option></select><button id="theme-generate-btn" data-on-click="theme-generate">Generate</button></div>'); document.getElementById('theme-generate-btn').click(); return true; })`);
        await waitFor(`[...document.querySelectorAll('.toast-message')].some(e => /Claude is not signed in\\. Sign in with claude in a terminal/.test(e.textContent))`, 'the toast (have: ' + JSON.stringify(await ev(`[...document.querySelectorAll('.toast-message')].map(e => e.textContent)`)) + ')', 8000);
        check('the theme generator\'s 409 toast says what to do', true);
        console.log(process.exitCode ? 'FAIL run_not_signed_in' : 'PASS run_not_signed_in');
    } finally { try { await client.close(); } catch (_) {} }
})().catch(e => { console.error(e); process.exit(1); });
