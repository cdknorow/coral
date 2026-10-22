// Jobs on a phone (#6139): the Jobs list used to live only in the sidebar, which a phone does not show, so a person could not pick a job or edit it. At 390 px with touch the list is in the Jobs page, a tap on a job shows
// its detail with Edit, and Edit opens the editor; on a wide screen the list stays in the sidebar. The scheduled-jobs routes are stubbed (the paths the dashboard's own suites stub).
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { tap } = require('./helpers/phone_targets');
const BASE = require('./test_base').BASE;
const JOB = { id: 5, name: 'Hourly job', cron_expr: '0 * * * *', enabled: true, agent_type: 'claude', prompt: 'p', repo_path: '/tmp', consecutive_failures: 0 };
const STUB = `(() => {
    const inner = window.fetch;
    const json = b => Promise.resolve(new Response(JSON.stringify(b), { headers: { 'content-type': 'application/json' } }));
    window.fetch = (url, opts = {}) => {
        const path = String(url && url.url || url).replace(/^https?:\\/\\/[^/]+/, '').split('?')[0], m = (opts.method || 'GET').toUpperCase();
        if (m === 'GET' && path === '/api/scheduled/jobs') return json({ jobs: [${JSON.stringify(JOB)}] });
        if (m === 'GET' && path === '/api/scheduled/jobs/5') return json(${JSON.stringify(JOB)});
        if (m === 'GET' && /^\\/api\\/scheduled\\/jobs\\/5\\/runs/.test(path)) return json({ runs: [] });
        return inner(url, opts);
    };
})();`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await Page.addScriptToEvaluateOnNewDocument({ source: STUB });
        for (const [name, w, h, phone] of [['phone', 390, 844, true], ['desktop', 1280, 800, false]]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile: phone }); await Emulation.setTouchEmulationEnabled({ enabled: phone });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install(); await ev(`window.__coralReady.then(() => true)`);
            await ev(`window.switchNavTab('jobs'); true`);
            const where = await ev(`(() => { const s = document.querySelector('[data-section="jobs"]'); return { inPage: !!s.closest('#mobile-jobs-list'), inSidebar: !!s.closest('.sidebar') }; })()`);
            if (!phone) { assert.deepEqual(where, { inPage: false, inSidebar: true }, 'desktop: the list stays in the sidebar'); console.log('ok desktop'); continue; }
            assert.equal(where.inPage, true, 'phone: the Jobs list is in the Jobs page');
            assert.equal(await ev(`[...document.querySelectorAll('#mobile-jobs-list .sidebar-section-header')].some(h => h.getClientRects().length > 0)`), false, 'phone: the moved section does not say "Jobs" a second time under the page title');
            await tap(ev, '[data-subtab="scheduled"]');
            await waitFor(`!!document.querySelector('#scheduled-jobs-list [data-on-click="job-select"]') && document.querySelector('#scheduled-jobs-list [data-on-click="job-select"]').getClientRects().length > 0`, 'the job in the list', 10000);
            await tap(ev, '#scheduled-jobs-list [data-on-click="job-select"]');
            await waitFor(`/Hourly job/.test(document.getElementById('scheduler-detail').textContent) && !!document.querySelector('#scheduler-detail [data-on-click="job-edit"]')`, 'the job detail', 10000);
            const detail = await ev(`(() => { const d = document.getElementById('scheduler-detail').getBoundingClientRect(); return { top: Math.round(d.top), vh: innerHeight }; })()`);
            assert.ok(detail.top < detail.vh * 0.5, `phone: the picked job's detail is brought into view (${JSON.stringify(detail)})`);
            await tap(ev, '#scheduler-detail [data-on-click="job-edit"]');
            await waitFor(`[...document.querySelectorAll('.modal, dialog')].some(m => m.getClientRects().length > 0 && /cron|schedule/i.test(m.textContent))`, 'the job editor', 10000);
            console.log('ok phone');
        }
        console.log('PASS Jobs on a phone: the list, a job and its editor are reachable');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
