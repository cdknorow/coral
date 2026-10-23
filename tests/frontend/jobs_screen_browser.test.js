// The Jobs screen as the #6515 audit asked (findings 1 and 2, #6521): one list of every job with its schedule, last result and next run in words (no "No active jobs" over jobs that exist, a "Running now"
// group only while one runs); and a job page with the failure first, the schedule and the next run in words with their zone, "Does: Workflow <name>", the folder and limits in a closed Settings, the runs
// with their result in words, Delete behind More, no "enabled" pill. 1280 and 390 px, a stubbed server. Drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const S = `(window.__coralStatic || '/static/')`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; await new Promise(r => setTimeout(r, 400)); /* sleep-ok: a screenshot waits out transitions */ fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(path.join(d, name + '.png'), Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const STUB = `(() => {
        const real = window.fetch; const json = (o, status) => Promise.resolve(new Response(JSON.stringify(o), { status: status || 200, headers: { 'Content-Type': 'application/json' } }));
        const ago = h => new Date(Date.now() - h * 3600000).toISOString(), inn = h => new Date(Date.now() + h * 3600000).toISOString();
        // the next run of a daily "M H * * *" (UTC) job, as the scheduler computes it: the stub's next run follows its cron
        const nextDaily = (m, h) => { const n = new Date(); const t = new Date(Date.UTC(n.getUTCFullYear(), n.getUTCMonth(), n.getUTCDate(), h, m)); if (t <= n) t.setUTCDate(t.getUTCDate() + 1); return t.toISOString(); };
        window.__nextDaily = nextDaily;
        const base = { description: '', timezone: 'UTC', agent_type: 'claude', repo_path: '/work/project', base_branch: 'main', prompt: '', enabled: 1, max_duration_s: 3600, cleanup_worktree: 0, flags: '', job_type: 'workflow', workflow_id: null, created_at: ago(48), updated_at: ago(48), paused_reason: '', consecutive_failures: 0 };
        window.__jobs = [
            { ...base, id: 1, name: 'nightly-build-job', cron_expr: '0 2 * * *', workflow_id: 7, next_fire_at: nextDaily(0, 2), consecutive_failures: 1, last_run: { id: 11, status: 'failed', error_msg: 'step 1 (Test) failed: exit code 3', started_at: ago(8), finished_at: ago(8), scheduled_at: ago(8) } },
            { ...base, id: 2, name: 'summarize-the-day', cron_expr: '0 18 * * 1-5', job_type: 'prompt', prompt: 'Summarize what changed today.', next_fire_at: inn(30), last_run: { id: 12, status: 'completed', started_at: ago(20), finished_at: ago(20), scheduled_at: ago(20) } },
            { ...base, id: 3, name: 'weekly-cleanup', cron_expr: '0 9 * * 1', job_type: 'prompt', prompt: 'Tidy.', enabled: 0, next_fire_at: null, last_run: null },
        ];
        window.fetch = (u, o) => {
            const url = String(u), method = (o && o.method) || 'GET';
            if (method === 'GET' && /\\/api\\/scheduled\\/jobs$/.test(url)) return json({ jobs: window.__jobs });
            let m = /\\/api\\/scheduled\\/jobs\\/(\\d+)\\/runs/.exec(url);
            if (m) return json({ runs: m[1] === '1' ? [{ id: 11, job_id: 1, status: 'failed', error_msg: 'step 1 (Test) failed: exit code 3', started_at: ago(8), finished_at: ago(8), scheduled_at: ago(8) }, { id: 10, job_id: 1, status: 'completed', started_at: ago(32), finished_at: ago(31.9), scheduled_at: ago(32) }] : [] });
            if (method === 'GET' && /\\/api\\/workflows$/.test(url)) return json({ workflows: [{ id: 7, name: 'nightly-build', steps: [{ name: 'Install' }, { name: 'Test' }] }] });
            return real(u, o);
        };
        return true; })()`;
    const vis = sel => `(() => { const e = document.querySelector(${JSON.stringify(sel)}); return !!e && !e.hidden && e.getClientRects().length > 0; })()`;
    const text = sel => ev(`(document.querySelector(${JSON.stringify(sel)}) || {}).textContent`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await Page.addScriptToEvaluateOnNewDocument({ source: STUB }); // before the page's own first fetch of the jobs
        for (const [w, h, mobile] of [[1280, 820, false], [390, 844, true]]) {
            const at = `${w}px`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'settings')`);
            await ev(`window.switchNavTab('jobs'); true`);
            await waitFor(`document.querySelectorAll('#scheduled-jobs-list .sched-row').length === 3`, `${at}: the three jobs listed at once`, 8000);

            console.log(`${at}: one list, every job, in words`);
            assert.equal(await ev(`document.querySelectorAll('.sidebar-subtab').length`), 0, `${at}: no Active / Scheduled tabs`);
            assert.equal(await ev(`/No active jobs/.test(document.querySelector('[data-section="jobs"]').textContent)`), false, `${at}: nothing says "No active jobs" over jobs that exist`);
            assert.equal(await text('#jobs-count'), '3', `${at}: the count is the jobs`);
            const rows = await ev(`[...document.querySelectorAll('#scheduled-jobs-list .sched-row')].map(r => [r.querySelector('.sched-row-name').textContent.trim() + (r.querySelector('.sched-chip') ? ' ' + r.querySelector('.sched-chip').textContent.trim() : ''), r.querySelector('.sched-row-meta').textContent.replace(/\\s+/g, ' ').trim()])`);
            assert.equal(rows[0][0], 'nightly-build-job');
            assert.match(rows[0][1], /^Every day at 02:00 · last run failed · next /);
            assert.match(rows[1][1], /^Weekdays at 18:00 · last run passed · next /);
            assert.equal(rows[2][0], 'weekly-cleanup paused'); assert.match(rows[2][1], /^Mondays at 09:00 · never run$/);
            assert.equal(await ev(vis('#live-jobs-group')), false, `${at}: no "Running now" while nothing runs`);
            await ev(`import(${S} + 'live_jobs.js').then(m => m.renderLiveJobs([{ id: 99, job_name: 'summarize-the-day', status: 'running', started_at: new Date().toISOString(), session_id: 'abc', agent_type: 'claude', trigger_type: 'cron' }])).then(() => true)`);
            assert.equal(await ev(vis('#live-jobs-group')), true, `${at}: "Running now" while one runs`);
            assert.match(await text('#live-jobs-group'), /Running now/);
            assert.equal(await text('#jobs-count'), '3', `${at}: a running job is one of the jobs, not another`);
            assert.equal(await text('#live-jobs-status'), '1 job running now', `${at}: said once, politely`);
            await ev(`import(${S} + 'live_jobs.js').then(m => m.renderLiveJobs([])).then(() => true)`);
            assert.equal(await ev(vis('#live-jobs-group')), false);
            assert.equal(await text('#live-jobs-status'), '', `${at}: and cleared when it ends`);
            assert.deepEqual(await ev(`[...document.querySelectorAll('#scheduled-jobs-list .sched-dot')].map(d => [d.textContent, getComputedStyle(d).borderRadius, Math.round(d.getBoundingClientRect().width), getComputedStyle(d).backgroundImage])`), [['', '50%', 8, 'none'], ['', '50%', 8, 'none'], ['', '50%', 8, 'none']], `${at}: the dots are flat 8 px circles (no emoji, no gradient)`);
            if (mobile) {
                const left = await ev(`(() => { const h = [...document.querySelectorAll('h2')].find(e => e.textContent.trim() === 'Jobs' && e.getClientRects().length); const d = document.querySelector('#scheduled-jobs-list .sched-dot'); const pad = e => getComputedStyle(e).paddingLeft + '/' + getComputedStyle(e).marginLeft; const row = d.closest('li'); return [Math.round(h.getBoundingClientRect().left), Math.round(d.getBoundingClientRect().left), pad(row), pad(row.parentElement), pad(row.parentElement.parentElement), pad(row.parentElement.parentElement.parentElement), row.parentElement.parentElement.className, row.parentElement.parentElement.parentElement.className, row.parentElement.parentElement.parentElement.parentElement.className + '/' + pad(row.parentElement.parentElement.parentElement.parentElement)]; })()`);
                assert.ok(Math.abs(left[0] - left[1]) <= 4, `${at}: the rows start where the heading does (heading ${left[0]} px, first dot ${left[1]} px; paddings/margins li,ul,body,section ${left.slice(2)})`);
            }
            await shot(`jobs-list-${w}`);

            console.log(`${at}: the job page, failure first`);
            await ev(`document.querySelector('#scheduled-jobs-list .sched-row').click(); true`);
            await waitFor(vis('#scheduler-detail .sched-fail-banner'), `${at}: the job page`, 8000);
            const order = await ev(`[...document.querySelectorAll('#scheduler-detail > *')].map(e => e.className.split(' ')[0])`);
            assert.equal(order[0], 'sched-fail-banner', `${at}: the failure is the first thing on the page: ${JSON.stringify(order)}`);
            assert.match(await text('#scheduler-detail .sched-fail-banner'), /The last run failed.*step 2 \(Test\) exited with 3/s);
            assert.equal(await ev(`/\\benabled\\b/i.test(document.querySelector('#scheduler-detail .sched-header').textContent)`), false, `${at}: no "enabled" pill`);
            const facts = await ev(`[...document.querySelectorAll('#scheduler-detail > .sched-facts dt')].map(d => d.textContent.trim() + '=' + d.nextElementSibling.textContent.replace(/\\s+/g, ' ').trim())`);
            assert.equal(facts[0], 'Runs=Every day at 02:00 (UTC)');
            // Next agrees with the schedule: 02:00 UTC, in this browser's own time (a different zone would show 19:00 in Pacific)
            const expectNext = await ev(`(() => { const t = new Date(window.__nextDaily(0, 2)); const p = n => String(n).padStart(2, '0'); const day = d => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime(); const dd = Math.round((day(t) - day(new Date())) / 86400000); return (dd === 0 ? 'today ' : dd === 1 ? 'tomorrow ' : t.toLocaleDateString([], { month: 'short', day: 'numeric' }) + ' ') + p(t.getHours()) + ':' + p(t.getMinutes()); })()`);
            assert.equal(facts[1], `Next=${expectNext} your time`, `${at}: Next is the schedule's next 02:00 UTC in the viewer's time`);
            assert.match(facts[1], /^Next=(today|tomorrow|[A-Z][a-z]{2} \d+) \d\d:\d\d your time$/);
            assert.match(await ev(`document.querySelector('#scheduler-detail > .sched-facts dd:nth-of-type(2)').title`), /^in \d+ h$/, `${at}: the relative part is a title, the absolute time is the text`);
            assert.equal(facts[2], 'Does=Workflow nightly-build (2 steps)');
            assert.equal(facts.length, 3, `${at}: three facts, the rest is in Settings: ${JSON.stringify(facts)}`);
            assert.equal(await ev(`document.querySelector('#scheduler-detail .sched-settings').open`), false, `${at}: Settings is closed`);
            { const cs = await ev(`(() => { const c = getComputedStyle(document.querySelector('#scheduler-detail .sched-settings > summary')); return [c.display, c.listStyleType, c.listStylePosition]; })()`); assert.ok(cs[0] === 'list-item' || (cs[1] !== 'none'), `${at}: Settings is a disclosure with its arrow (${cs})`); }
            await shot(`jobs-page-closed-${w}`);
            assert.equal(await ev(`/Cleanup|Base branch/.test(document.querySelector('#scheduler-detail .sched-settings').textContent) && !/Cleanup/.test(document.querySelector('#scheduler-detail .sched-settings').textContent)`), false);
            await ev(`document.querySelector('#scheduler-detail .sched-settings > summary').click(); true`);
            assert.match(await text('#scheduler-detail .sched-settings'), /Time limit\s*1 hour/);
            assert.match(await text('#scheduler-detail .sched-settings'), /Folder\s*\/work\/project/);
            assert.deepEqual(await ev(`[...document.querySelectorAll('#scheduler-detail .sched-runs-table th')].map(t => t.textContent.trim())`), ['When', 'Result', 'Took']);
            assert.match(await ev(`document.querySelector('#scheduler-detail .sched-runs-table tbody tr').textContent.replace(/\\s+/g, ' ')`), /Failed: step 2 \(Test\) exited with 3/);
            assert.match(await ev(`document.querySelectorAll('#scheduler-detail .sched-runs-table tbody tr')[1].textContent.replace(/\\s+/g, ' ')`), /Passed/);

            console.log(`${at}: Delete is behind More`);
            assert.equal(await ev(vis('#scheduler-detail .sched-delete')), false, `${at}: Delete is not on the page until More opens`);
            await ev(`document.querySelector('#scheduler-detail .sched-more-btn').click(); true`);
            assert.equal(await ev(vis('#scheduler-detail .sched-delete')), true);
            await ev(`document.querySelector('#scheduler-detail .sched-more-btn').click(); true`);
            if (mobile) {
                assert.deepEqual(await ev(`[...document.querySelectorAll('#scheduler-detail .sched-actions .btn')].filter(b => b.getClientRects().length && b.getBoundingClientRect().height < 43.5).length`), 0, `${at}: 44 px targets`);
                assert.equal(await ev(`document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1`), true, `${at}: no sideways scroll`);
            }
            await shot(`jobs-page-${w}`);
        }
        console.log('jobs_screen: ok');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
