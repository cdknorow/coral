// Jobs at 390 px and in general (#7121, UI/UX's #7114 findings 1 to 6): the New job form's flag chips wrap and never pass the dialog's edge, and there is no one-tap chip for --dangerously-skip-permissions (a
// person types it); a job on a phone has a "‹ Jobs" row back to the list; a run's When cell is drawn as the link it is (the row opens the run, as before); the Repository path shows its end; the worktree
// checkbox sits beside its label; and "The last 2 runs failed" is not "all failed". Drives Chrome over CDP through agent_view_harness.js against the isolated dev server; the jobs are stand-ins.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const S = `(window.__coralStatic || '/static/')`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: the dialog and the page scroll settle after the click
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile, font] of [[390, 844, true, ''], [390, 844, true, '32px'], [1280, 900, false, '']]) {
            const tag = `${w} px${font ? ', 200% text' : ''}`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled(mobile ? { enabled: true, maxTouchPoints: 5 } : { enabled: false });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'liveListLoaded', 'settings')`);
            if (font) await ev(`document.documentElement.style.fontSize = '${font}'; true`);
            await ev(`(() => { const real = window.fetch; const json = (o, status) => Promise.resolve(new Response(JSON.stringify(o), { status: status || 200, headers: { 'Content-Type': 'application/json' } }));
                const ago = h => new Date(Date.now() - h * 3600000).toISOString(); const nxt = h => new Date(Date.now() + h * 3600000).toISOString();
                const base = { description: '', timezone: 'UTC', agent_type: 'claude', repo_path: '/work/repo', base_branch: 'main', prompt: 'Tidy.', enabled: 1, max_duration_s: 3600, cleanup_worktree: 0, flags: '', consecutive_failures: 0 };
                const fail = id => ({ id, status: 'failed', exit_reason: 'step_failed', error_msg: 'step 1 (Test) failed: exit code 3', workflow_run_id: id, started_at: ago(id), finished_at: ago(id - 0.1) });
                window.__jobs = [ { ...base, id: 1, name: 'nightly-build', job_type: 'workflow', workflow_id: 1, cron_expr: '0 2 * * *', next_fire_at: nxt(9), last_run: fail(2), consecutive_failures: 2, workflow_runs: [fail(2), fail(3)] },
                    { ...base, id: 2, name: 'weekly-tidy', job_type: 'prompt', cron_expr: '30 9 * * 1', next_fire_at: nxt(80), last_run: null } ];
                window.fetch = (u, o) => { const url = String(u), m = (o && o.method) || 'GET';
                    if (m === 'GET' && /\\/api\\/scheduled\\/jobs$/.test(url)) return json({ jobs: window.__jobs });
                    if (/\\/api\\/scheduled\\/jobs\\/\\d+\\/runs/.test(url)) return json({ runs: [] });
                    if (m === 'GET' && /\\/api\\/workflows$/.test(url)) return json({ workflows: [{ id: 1, name: 'nightly-build', description: '', enabled: 1, steps: [{ name: 'Test' }], last_run: null }] });
                    return real(u, o); };
                return true; })()`);
            await ev(`import(${S} + 'scheduler.js').then(m => { window.__sched = m; return true; })`);
            await ev(`window.switchNavTab('jobs'); window.__sched.refreshJobs().then(() => true)`);
            await waitFor(`document.querySelectorAll('#scheduled-jobs-list .sched-row').length === 2`, 'two job rows', 10000);
            // the failure line and the badge's tip count the runs without "all"
            await ev(`document.querySelectorAll('#scheduled-jobs-list .sched-row')[0].querySelector('.row-btn').click(); true`);
            await waitFor(`!!document.querySelector('#scheduler-detail .sched-fail-banner')`, 'the failure banner', 8000);
            const words = await ev(`({ banner: document.querySelector('#scheduler-detail .sched-fail-banner strong').textContent, tip: document.querySelector('#scheduled-jobs-list .sched-fail-badge').title })`);
            assert.equal(words.banner, 'The last 2 runs failed', `${tag}: the banner: ` + words.banner);
            assert.equal(words.tip, 'The last 2 runs failed', `${tag}: the badge's tip: ` + words.tip);
            // a run's When is a link to the run; a click anywhere on its row opens it
            const run = await ev(`(() => { const b = document.querySelector('.sched-runs-table .row-open-btn'); const cs = getComputedStyle(b); window.__opened = []; window.selectWorkflowRun = id => window.__opened.push(id); const tr = b.closest('tr'); const took = tr.children[2]; const r = took.getBoundingClientRect(); took.scrollIntoView({ block: 'center' }); const r2 = took.getBoundingClientRect(); const hit = document.elementFromPoint(r2.left + 4, r2.top + r2.height / 2); hit.click(); return { underline: cs.textDecorationLine, color: cs.color, accent: getComputedStyle(document.documentElement).getPropertyValue('--accent').trim(), opened: window.__opened.length }; })()`);
            assert.equal(run.underline, 'underline', `${tag}: a run's When is drawn as a link: ` + JSON.stringify(run));
            assert.equal(run.opened, 1, `${tag}: a click on the run's row (its Took cell) opens the run: ` + JSON.stringify(run));
            // a phone: a back row to the list
            if (mobile) {
                const back = await ev(`(() => { const b = document.querySelector('#scheduler-detail .sched-back'); return b ? { shown: b.getClientRects().length > 0, text: b.textContent.trim(), h: Math.round(b.getBoundingClientRect().height) } : null; })()`);
                assert.ok(back && back.shown && /Jobs/.test(back.text) && back.h >= 44, `${tag}: a "‹ Jobs" row, 44 px high, is on the job: ` + JSON.stringify(back));
                await ev(`document.querySelector('#scheduler-detail .sched-back').click(); true`); await sleep(500); // sleep-ok: the page scrolls back to the list
                const list = await ev(`(() => { const r = document.getElementById('scheduled-jobs-list').getBoundingClientRect(); const a = document.activeElement; return { top: Math.round(r.top), inView: r.top >= -2 && r.top < innerHeight, focusInList: !!(a && a.closest && a.closest('#scheduled-jobs-list')) }; })()`);
                assert.ok(list.inView && list.focusInList, `${tag}: the back row shows the list and focuses the open job: ` + JSON.stringify(list));
            } else assert.equal(await ev(`(() => { const b = document.querySelector('#scheduler-detail .sched-back'); return !b || b.getClientRects().length === 0; })()`), true, `${tag}: no back row beside the list on a wide window`);
            // New job: the agent form
            await ev(`window.showJobModal(); true`);
            await waitFor(`!!document.getElementById('job-modal-repo') && document.getElementById('job-modal-repo').closest('#job-modal')`, 'the job form', 8000);
            await ev(`(() => { const b = [...document.querySelectorAll('#job-modal button')].find(x => /Agent Prompt/.test(x.textContent)); if (b) b.click(); const d = [...document.querySelectorAll('#job-modal details')].find(x => /More options/.test(x.textContent)); if (d) d.open = true; return true; })()`);
            await waitFor(`document.getElementById('job-modal-flags').getClientRects().length > 0`, 'More options open', 5000);
            await sleep(400); // sleep-ok: the form lays out after the tab and the disclosure change
            const form = await ev(`(() => { const body = document.querySelector('#job-modal .modal-body') || document.querySelector('#job-modal'); const br = body.getBoundingClientRect(); const chips = [...document.querySelectorAll('#job-modal .flag-shortcut-btn')];
                const lab = document.querySelector('#job-modal .job-cleanup-option'), box = lab.querySelector('input'); const rg = document.createRange(); rg.selectNodeContents(lab.lastChild && lab.lastChild.nodeType === 3 ? lab.lastChild : lab); const tr = rg.getClientRects()[0]; const bx = box.getBoundingClientRect();
                return { chips: chips.map(c => c.textContent.trim()), over: chips.filter(c => c.getBoundingClientRect().right > br.right + 0.5).map(c => c.textContent.trim()), sideways: body.scrollWidth - body.clientWidth,
                    box: { right: Math.round(bx.right), midY: Math.round(bx.top + bx.height / 2) }, text: tr ? { left: Math.round(tr.left), midY: Math.round(tr.top + tr.height / 2), h: Math.round(tr.height) } : null }; })()`);
            assert.ok(!form.chips.includes('--dangerously-skip-permissions'), `${tag}: no one-tap chip for --dangerously-skip-permissions: ` + JSON.stringify(form.chips));
            assert.deepEqual(form.over, [], `${tag}: no flag chip passes the dialog's edge: ` + JSON.stringify(form));
            assert.ok(form.sideways <= 1, `${tag}: the form does not scroll sideways: ` + JSON.stringify(form));
            assert.ok(form.text && form.box.right <= form.text.left + 2 && Math.abs(form.box.midY - form.text.midY) <= form.text.h, `${tag}: the worktree checkbox is beside the first line of its label: ` + JSON.stringify(form));
            // the Repository path shows its end when it is not being edited
            const path = await ev(`(() => { const i = document.getElementById('job-modal-repo'); i.value = '/a/very/long/path/that/goes/on/and/on/and/on/and/on/and/on/and/on/and/on/and/on/to/the-folder-name'; i.focus(); i.blur(); i.dispatchEvent(new Event('blur')); return { max: i.scrollWidth - i.clientWidth, left: Math.round(i.scrollLeft) }; })()`);
            assert.ok(path.max <= 0 || path.left >= path.max - 2, `${tag}: the Repository path shows its end (scrolled ${path.left} of ${path.max})`);
            await ev(`window.hideJobModal && window.hideJobModal(); true`);
        }
        console.log('PASS jobs phone fixes');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
