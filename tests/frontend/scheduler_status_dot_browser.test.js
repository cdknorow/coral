// The Scheduler's job list draws each job's last run as a coloured circle (#5735): it has words for a screen reader and a tooltip, so colour is not all that says it.
// The list is built from /api/scheduled/jobs, answered here with one job per case (the scheduler's own render, as initScheduler runs it): a completed, a running, a failed,
// a killed and a never-run job. Drives Chrome over CDP against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await waitFor(`!!document.getElementById('scheduled-jobs-list')`, 'the jobs list', 15000);
        const jobs = [['done', 'completed'], ['going', 'running'], ['broke', 'failed'], ['stopped', 'killed'], ['fresh', '']].map(([name, status], i) => ({ id: 9000 + i, name, enabled: true, consecutive_failures: 0, last_run: status ? { status } : null }));
        const got = await ev(`(async () => { const real = window.fetch; window.fetch = (u, o) => String(u).endsWith('/api/scheduled/jobs') ? Promise.resolve(new Response(JSON.stringify({ jobs: ${JSON.stringify(jobs)} }), { status: 200, headers: { 'Content-Type': 'application/json' } })) : real(u, o);
            const m = await import((window.__coralStatic || '/static/') + 'scheduler.js'); m.initScheduler();
            for (let i = 0; i < 100 && document.querySelectorAll('#scheduled-jobs-list .sched-dot').length < 5; i++) await new Promise(r => setTimeout(r, 50)); // sleep-ok: waiting for the render the call above started
            return [...document.querySelectorAll('#scheduled-jobs-list li')].map(li => { const d = li.querySelector('.sched-dot'); return { name: li.querySelector('.sched-row-name').textContent.trim(), role: d.getAttribute('role'), label: d.getAttribute('aria-label'), title: d.getAttribute('title') }; }); })()`);
        const want = { done: 'Last run completed', going: 'Last run running', broke: 'Last run failed', stopped: 'Last run killed', fresh: 'Not run yet' };
        assert.equal(got.length, 5, 'five jobs listed: ' + JSON.stringify(got));
        for (const g of got) {
            assert.equal(g.role, 'img', g.name + ': the dot is an image with a name');
            assert.equal(g.label, want[g.name], g.name + ': says what the dot means');
            assert.equal(g.title, want[g.name], g.name + ': and as a tooltip');
        }
        console.log('   \u2713');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
