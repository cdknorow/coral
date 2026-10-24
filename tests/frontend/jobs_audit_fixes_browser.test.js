// The Jobs fixes from UI Accessibility's #6954 audit (#6956): a failed job's row says why it failed (any failed run's error, not only the coded ones); the Runs table's "Completed (result not seen)" is
// neutral like the row's dot, not the green of a pass, with its sentence as text; a schedule that is not one of the common shapes is said in words in the list, not "Schedule: */5 9-17 * * 1-5"; the row's
// schedule names its zone; the dot of a pass is named "passed" like the text; Jobs is titled once; an empty pane says to choose a job. 1280 and 390 px; the scheduler's answers are stubbed.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const S = `(window.__coralStatic || '/static/')`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const shot = async n => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${n}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: the runs answer replaces the first draw; layout settles
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 900, false], [390, 844, true]]) {
            const at = `${w}px: `;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`(() => { const real = window.fetch; const json = (o, status) => Promise.resolve(new Response(JSON.stringify(o), { status: status || 200, headers: { 'Content-Type': 'application/json' } }));
                const base = { description: '', timezone: 'UTC', agent_type: 'claude', repo_path: '/work/repo', base_branch: 'main', prompt: 'Tidy.', enabled: 1, max_duration_s: 3600, cleanup_worktree: 0, flags: '', job_type: 'prompt', cron_expr: '0 2 * * *', next_fire_at: new Date(Date.now() + 3600000).toISOString(), consecutive_failures: 0 };
                const run = (id, status, exit_reason, extra) => ({ id, status, exit_reason, started_at: new Date(Date.now() - 7200000).toISOString(), finished_at: new Date(Date.now() - 3600000).toISOString(), ...extra });
                window.__runs = { 1: [run(11, 'failed', 'agent_error', { error_msg: 'The agent exited with code 1' })], 2: [run(21, 'completed', 'ended_while_coral_was_down'), run(22, 'completed', 'agent_done')], 3: [run(31, 'completed', 'agent_done')], 4: [] };
                window.__jobs = [{ ...base, id: 1, name: 'fails-plain', consecutive_failures: 3, last_run: window.__runs[1][0] }, { ...base, id: 2, name: 'unseen', last_run: window.__runs[2][0] }, { ...base, id: 3, name: 'passes', last_run: window.__runs[3][0] }, { ...base, id: 4, name: 'odd-cron', cron_expr: '*/5 9-17 * * 1-5', last_run: null }];
                window.fetch = (u, o) => { const url = String(u), m = (o && o.method) || 'GET';
                    if (m === 'GET' && /\\/api\\/scheduled\\/jobs$/.test(url)) return json({ jobs: window.__jobs });
                    const r = url.match(/\\/api\\/scheduled\\/jobs\\/(\\d+)\\/runs/); if (r) return json({ runs: window.__runs[r[1]] || [] });
                    if (m === 'GET' && /\\/api\\/workflows$/.test(url)) return json({ workflows: [] });
                    return real(u, o); };
                return true; })()`);
            await ev(`import(${S} + 'scheduler.js').then(m => { window.__sched = m; return true; })`);
            await ev(`window.switchNavTab('jobs'); window.__sched.refreshJobs().then(() => true)`);
            await waitFor(`document.querySelectorAll('#scheduled-jobs-list .sched-row').length === 4`, 'the four job rows', 8000);
            const rows = await ev(`Object.fromEntries([...document.querySelectorAll('#scheduled-jobs-list .sched-row')].map(r => [r.querySelector('.sched-row-name').textContent, { meta: r.querySelector('.sched-row-meta').textContent, problem: (r.querySelector('.sched-row-problem') || {}).textContent || '', dotWords: r.querySelector('.sched-dot').getAttribute('aria-label') }]))`);
            assert.match(rows['fails-plain'].problem, /The agent exited with code 1/, at + 'a generic failed run says why on its row: ' + JSON.stringify(rows['fails-plain']));
            assert.equal(rows['passes'].dotWords, 'Last run passed', at + 'the dot of a pass is named as its text says');
            assert.match(rows['odd-cron'].meta, /^Every 5 minutes, from 09:00 to 17:59, weekdays \(UTC\)/, at + 'an unusual schedule is in words, with its zone: ' + rows['odd-cron'].meta);
            assert.match(rows['passes'].meta, /^Every day at 02:00 \(UTC\)/, at + 'the row names the schedule\'s zone: ' + rows['passes'].meta);
            await shot(`jobs-list-${w}`);
            // Jobs is titled once; an empty pane says to choose a job
            const heads = await ev(`[...document.querySelectorAll('h1, h2, h3')].filter(h => h.textContent.replace(/\\s+/g, ' ').trim() === 'Jobs' || /^Jobs \\d*$/.test(h.textContent.replace(/\\s+/g, ' ').trim())).filter(h => h.checkVisibility() && h.getBoundingClientRect().width > 4 && h.getBoundingClientRect().height > 4).length`);
            assert.ok(heads <= 1, at + `Jobs is titled once on screen (${heads})`);
            if (!mobile) assert.match(await ev(`document.getElementById('scheduler-detail').textContent`), /Choose a job/, at + 'an empty pane says to choose a job');
            // the Runs table: the unseen result is neutral, with its sentence as text
            await ev(`document.querySelector('#scheduled-jobs-list .sched-row[data-job-id="2"]').click(); true`);
            await waitFor(`!!document.querySelector('.sched-runs-section .sched-status')`, 'the runs table', 6000); await sleep(300); // sleep-ok: the runs answer replaces the first draw
            const cells = await ev(`[...document.querySelectorAll('.sched-runs-section .sched-status')].map(s => ({ text: s.textContent.trim(), cls: s.className, cellText: s.closest('td').textContent.replace(/\\s+/g, ' ').trim() }))`);
            assert.ok(!/status-ok/.test(cells[0].cls), at + 'the unseen result is not drawn in the green of a pass: ' + JSON.stringify(cells[0]));
            assert.match(cells[0].cellText, /Coral was stopped when this run's agent ended/, at + 'its sentence is text in the table: ' + JSON.stringify(cells[0]));
            assert.ok(/status-ok/.test(cells[1].cls), at + 'a real pass stays green');
            await shot(`jobs-runs-${w}`);
            console.log('PASS ' + at.trim());
        }
        console.log('PASS jobs audit fixes');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
