// #4423: the scheduled-job editor opens the job as the server has it and saves only what the person changed, against the real server
// (another client is a request straight to it):
//   - a description another client changed while the editor was open survives a Save of the name;
//   - a workflow job opens as a workflow job with its workflow selected, even after the editor last showed an agent job,
//     and saved unchanged sends nothing;
//   - the editor shows a change made elsewhere after this page loaded.
// The job's cron is once a year (1 January), so it never runs during the test. Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const call = (path, method = 'GET', body) => ev(`fetch(${JSON.stringify(path)}, ${JSON.stringify({ method, headers: { 'Content-Type': 'application/json' }, ...(body ? { body: JSON.stringify(body) } : {}) })}).then(async r => ({ status: r.status, body: await r.json().catch(() => null) }))`);
    const MOD = `import((window.__coralStatic || '/static/') + 'scheduler.js')`;
    const stamp = Date.now();
    let jobId = null, wfId = null;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        const wf = await call('/api/workflows', 'POST', { name: 'stale-wf-' + stamp, steps: [{ name: 'one', type: 'shell', command: 'true' }] });
        assert.ok(wf.status < 300, JSON.stringify(wf));
        wfId = wf.body.id;
        const job = await call('/api/scheduled/jobs', 'POST', { name: 'stale-job-' + stamp, description: 'as created', cron_expr: '0 0 1 1 *', timezone: 'UTC', job_type: 'workflow', workflow_id: wfId });
        assert.ok(job.status < 300, JSON.stringify(job));
        jobId = job.body.id;
        // Every request the page sends, to see what a Save sends.
        await ev(`(() => { const R = window.fetch; window.__jobPuts = []; window.fetch = (u, o = {}) => { if ((o.method || '').toUpperCase() === 'PUT' && /\\/api\\/scheduled\\/jobs\\//.test(String(u))) window.__jobPuts.push(JSON.parse(o.body)); return R(u, o); }; return true; })()`);
        const field = id => ev(`document.getElementById(${JSON.stringify(id)}).value`);
        const open = async () => {
            await ev(`${MOD}.then(m => m.editScheduledJob(${jobId})).then(() => true)`);
            await waitFor(`document.getElementById('job-modal').style.display === 'flex' && document.getElementById('job-modal-workflow').value !== ''`, 'the editor with the job', 10000);
        };
        const save = async () => { await ev(`window.__jobPuts.length = 0; ${MOD}.then(m => m.saveScheduledJob()).then(() => true)`); };

        console.log('1. A change made elsewhere after the page loaded shows; one made while the editor is open survives a Save of the name');
        assert.equal((await call(`/api/scheduled/jobs/${jobId}`, 'PUT', { description: 'changed after the page loaded' })).status, 200);
        await ev(`${MOD}.then(m => m.switchJobType('prompt')).then(() => true)`); // the editor last showed an agent job
        await open();
        assert.equal(await field('job-modal-description'), 'changed after the page loaded', 'the editor opens with the server\'s job');
        assert.equal(await ev(`document.getElementById('job-modal-workflow-fields').style.display`), '', 'a workflow job opens as one');
        assert.equal(await field('job-modal-workflow'), String(wfId), 'with its workflow selected');
        assert.equal((await call(`/api/scheduled/jobs/${jobId}`, 'PUT', { description: 'from another client' })).status, 200);
        await ev(`document.getElementById('job-modal-name').value = 'renamed-${stamp}'; true`);
        await save();
        await waitFor(`window.__jobPuts.length === 1`, 'the save', 8000);
        assert.deepEqual(await ev(`window.__jobPuts[0]`), { name: 'renamed-' + stamp }, 'only the changed field is sent');
        const after = (await call(`/api/scheduled/jobs/${jobId}`)).body;
        assert.equal(after.name, 'renamed-' + stamp);
        assert.equal(after.description, 'from another client', 'the description changed elsewhere stays');
        assert.equal(after.job_type || 'workflow', 'workflow');
        assert.equal(after.workflow_id, wfId);

        console.log('2. Saved with no change: nothing is sent');
        await open();
        await save();
        await waitFor(`document.getElementById('job-modal').style.display === 'none'`, 'the editor closed', 8000);
        assert.equal(await ev(`window.__jobPuts.length`), 0);
        console.log('PASS scheduled-job editor saves only what changed');
    } finally {
        try {
            if (jobId) await call(`/api/scheduled/jobs/${jobId}`, 'DELETE');
            if (wfId) await call(`/api/workflows/${wfId}`, 'DELETE');
        } catch (_) { /* the page is gone */ }
        try { await client.close(); } catch (_) {}
    }
})().catch(error => { console.error(error); process.exit(1); });
