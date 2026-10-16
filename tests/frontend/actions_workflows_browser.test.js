// CSP stage 2, slice 2 (#3645): the Workflows view and scheduled jobs on delegated data-on-*
// actions. Each moved action still does what its inline handler did, seen in the request it makes
// or the view it opens; destructive ones keep their confirmation (refused here, so nothing is
// sent); an action given a bad id (a non-number, a non-UUID) does nothing; and no inline handler
// is left in what the two views render. The APIs are stubbed in the page.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

const SID = '0b2f6c1e-3d4a-4b5c-8d6e-000000003645';
const WF = { id: 7, name: 'Nightly "build"', description: 'd', enabled: true, steps: [{ name: 's1', type: 'shell', command: 'echo hi' }] };
const RUNS = { runs: [{ id: 31, workflow_id: 7, status: 'failed', trigger_type: 'manual', started_at: '2026-10-15T10:00:00Z', finished_at: '2026-10-15T10:01:00Z' }] };
const RUN = { id: 31, workflow_id: 7, workflow_name: 'Nightly', status: 'running', trigger_type: 'manual', started_at: '2026-10-15T10:00:00Z',
    steps: [{ index: 0, name: 's1', type: 'shell', status: 'failed', exit_code: 1, output_tail: 'boom' },
        { index: 1, name: 'agent step', type: 'agent', status: 'completed', session_id: SID, session_name: 'claude-' + SID }] };
const JOB = { id: 5, name: 'Hourly "job"', cron_expr: '0 * * * *', enabled: true, workflow_id: 7, workflow_name: 'Nightly', consecutive_failures: 0,
    workflow_runs: [{ id: 31, status: 'completed', current_step: 0, started_at: '2026-10-15T10:00:00Z', finished_at: '2026-10-15T10:01:00Z' }] };
// An agent job: its runs table links each run's session.
const AGENT_JOB = { id: 6, name: 'Agent job', cron_expr: '0 * * * *', enabled: true, agent_type: 'claude', prompt: 'p', repo_path: '/tmp', consecutive_failures: 0 };
const JOB_RUNS = { runs: [{ id: 44, status: 'completed', scheduled_at: '2026-10-15T10:00:00Z', started_at: '2026-10-15T10:00:00Z', finished_at: '2026-10-15T10:01:00Z', session_id: SID }] };

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const until = (expr, what, ms = 10000) => waitFor(expr, what, { timeout: ms, soft: true });
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: let a click's request start
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            window.__req = [];
            const inner = window.fetch;
            const json = b => Promise.resolve(new Response(JSON.stringify(b), { headers: { 'content-type': 'application/json' } }));
            window.fetch = (url, opts = {}) => {
                const u = String(url && url.url || url).replace(/^https?:\\/\\/[^/]+/, ''), m = (opts.method || 'GET').toUpperCase();
                const hit = r => { window.__req.push(m + ' ' + u.split('?')[0]); return json(r); };
                if (u === '/api/workflows' && m === 'GET') return hit({ workflows: [${JSON.stringify(WF)}] });
                if (/^\\/api\\/workflows\\/7\\/runs/.test(u)) return hit(${JSON.stringify(RUNS)});
                if (u === '/api/workflows/7' && m === 'GET') return hit(${JSON.stringify(WF)});
                if (u === '/api/workflows/runs/31' && m === 'GET') return hit(${JSON.stringify(RUN)});
                if (/^\\/api\\/workflows\\//.test(u) || u === '/api/workflows') return hit({ ok: true, run_id: 32 });
                if (u === '/api/scheduled/jobs' && m === 'GET') return hit({ jobs: [${JSON.stringify(JOB)}, ${JSON.stringify(AGENT_JOB)}] });
                if (/^\\/api\\/scheduled\\/jobs\\/[56]\\/runs/.test(u)) return hit(${JSON.stringify(JOB_RUNS)});
                if (/^\\/api\\/scheduled\\//.test(u)) return hit({ ok: true });
                if (u === '/api/sessions/launch' && m === 'POST') return hit({ ok: true, session_id: '${SID}', session_name: 'claude-${SID}' });
                return inner(url, opts);
            };
        })();` });
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();
        const reqs = () => ev(`window.__req.slice()`);
        const clear = () => ev(`window.__req.length = 0; true`);
        const click = sel => ev(`(() => { const el = document.querySelector(${JSON.stringify(sel)}); if (!el) return false; el.click(); return true; })()`);
        const sent = async (want, what) => { await until(`window.__req.includes(${JSON.stringify(want)})`, what, 5000); return (await reqs()).includes(want); };

        // ── Workflows ──────────────────────────────────────────────────────
        await ev(`window.switchNavTab('workflows'); true`);
        await until(`!!document.querySelector('#wf-list-container .wf-card[data-on-click="wf-select"]')`, 'the workflow list');
        await clear();
        await click('.wf-card[data-on-click="wf-select"]');
        check('a workflow card opens the workflow', await sent('GET /api/workflows/7', 'the workflow') && await ev(`document.getElementById('wf-detail-container').style.display !== 'none'`));
        await until(`!!document.querySelector('#wf-detail-container tr[data-on-click="wf-run"]')`, 'its runs');

        await clear();
        await click('#wf-detail-container [data-on-click="wf-trigger"]');
        check('Run now triggers it', await sent('POST /api/workflows/7/trigger', 'the trigger'));

        await ev(`window.confirm = m => { window.__confirm = m; return false; }; true`);
        await clear();
        await click('#wf-detail-container [data-on-click="wf-delete"]');
        await sleep(300); // sleep-ok: shows that nothing is sent (no condition to wait for)
        check('Delete still asks first (with the name), and a No sends nothing',
            /Nightly "build"/.test(await ev(`window.__confirm || ''`)) && !(await reqs()).some(r => r.startsWith('DELETE')), JSON.stringify(await reqs()));

        await clear();
        await click('#wf-detail-container [data-on-click="wf-edit"]');
        await until(`getComputedStyle(document.getElementById('wf-create-modal')).display !== 'none'`, 'the edit dialog');
        check('Edit opens the dialog with the workflow', await ev(`document.getElementById('wf-input-name').value`) === WF.name);
        const steps0 = await ev(`document.querySelectorAll('#wf-steps-editor .wf-step-editor-item').length`);
        await click('#wf-create-modal [data-on-click="wf-add-step"]');
        const steps1 = await ev(`document.querySelectorAll('#wf-steps-editor .wf-step-editor-item').length`);
        const typed = await ev(`(() => { const items = document.querySelectorAll('#wf-steps-editor .wf-step-editor-item'); const it = items[items.length - 1];
            const sel = it.querySelector('select[data-on-change="wf-step-type"]'); sel.value = 'agent'; sel.dispatchEvent(new Event('change', { bubbles: true }));
            return it.querySelector('.wf-step-content').placeholder; })()`);
        await ev(`(() => { const items = document.querySelectorAll('#wf-steps-editor .wf-step-editor-item'); items[items.length - 1].querySelector('[data-on-click="wf-step-remove"]').click(); return true; })()`);
        const steps2 = await ev(`document.querySelectorAll('#wf-steps-editor .wf-step-editor-item').length`);
        check('Add step adds one, its type change updates it, and its remove button removes it', steps1 === steps0 + 1 && typed === 'Agent prompt...' && steps2 === steps0, `${steps0}/${steps1}/${steps2} ${typed}`);
        await clear();
        await click('#wf-create-modal [data-on-click="wf-save"]');
        check('Save sends the edited workflow', await sent('PUT /api/workflows/7', 'the save'), JSON.stringify(await reqs()));
        await ev(`window.showWorkflowCreateModal(); true`);
        await click('#wf-create-modal .modal-content');
        const stillOpen = await ev(`getComputedStyle(document.getElementById('wf-create-modal')).display !== 'none'`);
        await ev(`document.getElementById('wf-create-modal').dispatchEvent(new MouseEvent('click', { bubbles: true })); true`);
        const closedByBackdrop = await ev(`getComputedStyle(document.getElementById('wf-create-modal')).display === 'none'`);
        check('the dialog closes on its backdrop, not on a click inside it', stillOpen && closedByBackdrop, `${stillOpen}/${closedByBackdrop}`);
        await ev(`window.showWorkflowCreateModal(); true`);
        await click('#wf-create-modal .modal-close[data-on-click="wf-modal-close"]');
        check('the dialog\'s close button closes it', await ev(`getComputedStyle(document.getElementById('wf-create-modal')).display === 'none'`));

        await clear();
        await click('#wf-detail-container tr[data-on-click="wf-run"]');
        check('a run row opens the run', await sent('GET /api/workflows/runs/31', 'the run'));
        await until(`!!document.querySelector('#wf-run-detail-container [data-on-click="wf-output-toggle"]')`, 'the run detail');
        const toggle = await ev(`(() => { const b = document.querySelector('#wf-run-detail-container [data-on-click="wf-output-toggle"]'); const out = document.getElementById(b.getAttribute('aria-controls'));
            const a = [b.getAttribute('aria-expanded'), out.classList.contains('wf-expanded')]; b.click(); return [a, [b.getAttribute('aria-expanded'), out.classList.contains('wf-expanded')]]; })()`);
        check('a step\'s Output button toggles its output and aria-expanded', toggle[0][1] !== toggle[1][1] && toggle[1][0] === String(toggle[1][1]), JSON.stringify(toggle));
        await clear();
        await click('#wf-run-detail-container [data-on-click="wf-kill-run"]');
        check('Kill stops the run', await sent('POST /api/workflows/runs/31/kill', 'the kill'));
        await click('[data-on-click="wf-back"]');
        check('Back returns to the list', await ev(`document.getElementById('wf-list-container').style.display !== 'none'`));

        // A bad id does nothing.
        await clear();
        await ev(`(() => { const b = document.createElement('div'); b.setAttribute('data-on-click', 'wf-select'); b.dataset.workflowId = '7 OR 1'; document.body.appendChild(b); b.click(); b.remove(); return true; })()`);
        await sleep(300); // sleep-ok: shows that nothing is sent (no condition to wait for)
        check('an action given a bad id does nothing', (await reqs()).length === 0, JSON.stringify(await reqs()));

        const wfInline = await ev(`[...document.querySelectorAll('#workflows-view *, #wf-create-modal *')].flatMap(el => [...el.attributes].filter(a => /^on/i.test(a.name)).map(a => el.tagName + ' ' + a.name))`);
        check('the Workflows view and its dialog have no inline handler left', wfInline.length === 0, JSON.stringify(wfInline.slice(0, 5)));

        // ── Scheduled jobs ─────────────────────────────────────────────────
        await ev(`window.switchNavTab('jobs'); true`);
        await until(`!!document.querySelector('#scheduled-jobs-list [data-on-click="job-select"]')`, 'the jobs list');
        await clear();
        await click('#scheduled-jobs-list [data-on-click="job-select"]');
        await until(`!!document.querySelector('#scheduler-view [data-on-click="job-toggle"]')`, 'the job');
        check('a job opens it', await ev(`!!document.querySelector('#scheduler-view [data-on-click="job-edit"]')`));
        await clear();
        await click('#scheduler-view [data-on-click="job-toggle"]');
        check('Pause/Resume toggles it', await sent('POST /api/scheduled/jobs/5/toggle', 'the toggle'));
        await ev(`window.__confirmModal = null; window.showConfirmModal = (t, m) => { window.__confirmModal = t; }; true`);
        await clear();
        await click('#scheduler-view [data-on-click="job-delete"]');
        await sleep(300); // sleep-ok: shows that nothing is sent (no condition to wait for)
        check('Delete still asks first, and nothing is sent without a Yes', (await ev(`window.__confirmModal`)) === 'Delete Job' && !(await reqs()).some(r => r.startsWith('DELETE')), JSON.stringify(await reqs()));
        await ev(`window.__hist = []; window.selectHistorySession = id => window.__hist.push(id); true`);
        await click('#scheduled-jobs-list [data-on-click="job-select"][data-job-id="6"]');
        await until(`!!document.querySelector('#scheduler-view [data-on-click="job-run-session"]')`, 'the agent job\'s runs', 5000);
        await click('#scheduler-view [data-on-click="job-run-session"]');
        check('a run\'s session link opens that session\'s history', JSON.stringify(await ev(`window.__hist`)) === JSON.stringify([SID]), JSON.stringify(await ev(`window.__hist`)));
        await ev(`(() => { const b = document.createElement('a'); b.setAttribute('data-on-click', 'job-run-session'); b.dataset.sessionId = '../../etc'; document.body.appendChild(b); b.click(); b.remove(); return true; })()`);
        check('...and a session link that is not a session id does nothing', (await ev(`window.__hist.length`)) === 1);
        await click('#scheduled-jobs-list [data-on-click="job-select"][data-job-id="5"]');
        await until(`!!document.querySelector('#scheduler-view tr[data-on-click="job-run"]')`, 'the workflow job again', 5000);
        await clear();
        const rowSel = '#scheduler-view tr[data-on-click="job-run"]';
        check('the job shows its workflow runs', await ev(`!!document.querySelector(${JSON.stringify(rowSel)})`));
        await click(rowSel);
        check('a workflow run row opens the run', await sent('GET /api/workflows/runs/31', 'the run'));
        await clear();
        await click('#scheduler-view [data-on-click="job-workflow"]');
        check('the job\'s workflow link opens the workflow', await sent('GET /api/workflows/7', 'the workflow'));
        await ev(`window.switchNavTab('jobs'); true`);
        await click('#scheduled-jobs-list [data-on-click="job-select"][data-job-id="5"]');
        await until(`!!document.querySelector('#scheduler-view [data-on-click="job-edit"]')`, 'the job again');
        await click('#scheduler-view [data-on-click="job-edit"]');
        check('Edit opens the job dialog', await until(`(() => { const m = document.getElementById('job-modal'); return !!m && getComputedStyle(m).display !== 'none'; })()`, 'the job dialog', 5000).then(() => ev(`(() => { const m = document.getElementById('job-modal'); return !!m && getComputedStyle(m).display !== 'none'; })()`)));
        await ev(`window.hideJobModal && window.hideJobModal(); true`);
        await click('#scheduler-view [data-on-click="job-new"]');
        check('New job opens an empty job dialog', await until(`(() => { const m = document.getElementById('job-modal'); return !!m && getComputedStyle(m).display !== 'none'; })()`, 'the new job dialog', 5000).then(() => ev(`(() => { const m = document.getElementById('job-modal'); return !!m && getComputedStyle(m).display !== 'none'; })()`)));
        await ev(`window.hideJobModal && window.hideJobModal(); true`);

        const jobInline = await ev(`[...document.querySelectorAll('#scheduled-jobs-list *, #scheduler-view *')].flatMap(el => [...el.attributes].filter(a => /^on/i.test(a.name)).map(a => el.tagName + ' ' + a.name))`);
        check('the jobs list and job view have no inline handler left', jobInline.length === 0, JSON.stringify(jobInline.slice(0, 5)));
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
