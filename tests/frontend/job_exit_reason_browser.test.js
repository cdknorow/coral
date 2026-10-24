// A run's exit_reason in words (#6907, from #6898): a run whose agent ended while Coral was stopped has status completed and exit_reason ended_while_coral_was_down, and no screen read exit_reason, so it read
// as a plain pass. The Jobs row says "last run completed (result not seen)" with its sentence under the name (text, not hover alone) and a grey dot, not the green of a pass; the Runs table's status cell says
// "Completed (result not seen)" with the sentence on hover; a run interrupted by a restart (failed, interrupted_by_restart) says Coral restarted. Other exit reasons change nothing. 1280 and 390 px.
// The runs and jobs answers are stubbed (Backend's #6898 shape: exit_reason a plain string or null). Drives Chrome over CDP through agent_view_harness.js.
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const S = `(window.__coralStatic || '/static/')`;
const UNSEEN = "Coral was stopped when this run's agent ended, so it could not see the result.";
const RESTART = 'Coral restarted before this run launched its agent.';

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
                const base = { description: '', timezone: 'UTC', agent_type: 'claude', repo_path: '/work/repo', base_branch: 'main', prompt: 'Tidy.', enabled: 1, max_duration_s: 3600, cleanup_worktree: 0, flags: '', job_type: 'prompt', cron_expr: '0 2 * * *', next_fire_at: new Date(Date.now() + 3600000).toISOString(), consecutive_failures: 0 };
                const run = (id, status, exit_reason, extra) => ({ id, status, exit_reason, started_at: new Date(Date.now() - 7200000).toISOString(), finished_at: new Date(Date.now() - 3600000).toISOString(), ...extra });
                window.__runs = { 1: [run(11, 'completed', 'ended_while_coral_was_down'), run(12, 'completed', 'agent_done')], 2: [run(21, 'failed', 'interrupted_by_restart', { error_msg: 'Coral restarted before this run launched its agent' })], 3: [run(31, 'completed', 'agent_done')], 4: [run(41, 'completed', 'timeout_reap')] };
                window.__jobs = [{ ...base, id: 1, name: 'seen-not', last_run: window.__runs[1][0] }, { ...base, id: 2, name: 'restarted', last_run: window.__runs[2][0] }, { ...base, id: 3, name: 'plain-pass', last_run: window.__runs[3][0] }, { ...base, id: 4, name: 'reaped', last_run: window.__runs[4][0] }];
                window.fetch = (u, o) => { const url = String(u), m = (o && o.method) || 'GET';
                    if (m === 'GET' && /\\/api\\/scheduled\\/jobs$/.test(url)) return json({ jobs: window.__jobs });
                    const r = url.match(/\\/api\\/scheduled\\/jobs\\/(\\d+)\\/runs/); if (r) return json({ runs: window.__runs[r[1]] || [] });
                    if (m === 'GET' && /\\/api\\/workflows$/.test(url)) return json({ workflows: [] });
                    return real(u, o); };
                return true; })()`);
            await ev(`import(${S} + 'scheduler.js').then(m => { window.__sched = m; return true; })`);
            await ev(`window.switchNavTab('jobs'); window.__sched.refreshJobs().then(() => true)`);
            await waitFor(`document.querySelectorAll('#scheduled-jobs-list .sched-row').length === 4`, 'the four job rows', 8000);
            const rows = await ev(`[...document.querySelectorAll('#scheduled-jobs-list .sched-row')].map(r => ({ name: r.querySelector('.sched-row-name').textContent, meta: r.querySelector('.sched-row-meta').textContent, note: (r.querySelector('.sched-row-note') || r.querySelector('.sched-row-problem') || {}).textContent || '', dot: r.querySelector('.sched-dot').className.replace('sched-dot ', ''), dotWords: r.querySelector('.sched-dot').getAttribute('aria-label') }))`);
            const by = n => rows.find(r => r.name === n);
            check(`${tag}: a completed run whose agent ended while Coral was stopped says "result not seen" on its row, with its sentence as text, a grey dot and a dot name that says so`, /last run completed \(result not seen\)/.test(by('seen-not').meta) && by('seen-not').note === UNSEEN && by('seen-not').dot === 'sched-dot-none' && by('seen-not').dotWords === 'Last run completed, result not seen', JSON.stringify(by('seen-not')));
            check(`${tag}: a run interrupted by a restart says Coral restarted on its row`, /^Coral restarted before this run launched its agent/.test(by('restarted').note), JSON.stringify(by('restarted')));
            check(`${tag}: a plain pass and a reaped run read as before (no note, the green dot for the pass)`, by('plain-pass').note === '' && /last run passed/.test(by('plain-pass').meta) && by('plain-pass').dot === 'sched-dot-ok' && by('reaped').note === '', JSON.stringify([by('plain-pass'), by('reaped')]));
            // the Runs table
            const table = async id => { await ev(`document.querySelector('#scheduled-jobs-list .sched-row[data-job-id="${id}"]').click(); true`); await waitFor(`!!document.querySelector('.sched-runs-section .sched-status')`, 'the runs table', 6000); await sleep(300); /* sleep-ok: the runs answer replaces the first draw */ return ev(`[...document.querySelectorAll('.sched-runs-section .sched-status')].map(s => ({ text: s.textContent, title: s.getAttribute('title') || '', cell: s.closest('td').textContent.replace(/\\s+/g, ' ').trim(), cls: s.className }))`); };
            let t = await table(1);
            check(`${tag}: the Runs table says "Completed (result not seen)" in a neutral colour with the sentence as text, and a plain pass stays "Passed" in green with none`, t[0].text === 'Completed (result not seen)' && t[0].cell.includes(UNSEEN) && !/status-ok/.test(t[0].cls) && t[1].text === 'Passed' && t[1].cell === 'Passed' && /status-ok/.test(t[1].cls), JSON.stringify(t));
            t = await table(2);
            check(`${tag}: a restart-interrupted run says why in the table (its error as the status words)`, /^Failed: Coral restarted before this run launched its agent/.test(t[0].text), JSON.stringify(t));
        }
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} exit reason checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
