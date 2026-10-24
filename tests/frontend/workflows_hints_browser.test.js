// The Workflows list says how its two ways to make a workflow differ and what starts one (#6988, UI Accessibility's #6962 LOWs): one short line each, as text (not a hover title), above the empty list; with workflows in the list they are a closed "How workflows start" disclosure (#7001); the two buttons
// name their line with aria-describedby. 1280 px, and 390 px at 200% text (nothing overflows sideways). Drives Chrome over CDP
// through agent_view_harness.js against the isolated dev server.
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const S = `(window.__coralStatic || '/static/')`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile, font] of [[1280, 900, false, ''], [390, 844, true, '32px']]) {
            const tag = `${w} px${font ? ', 200% text' : ''}`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled(mobile ? { enabled: true, maxTouchPoints: 5 } : { enabled: false });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`${font ? `document.documentElement.style.fontSize = '${font}';` : ''} window.switchNavTab('workflows'); true`);
            await waitFor(`!!document.getElementById('wf-hint-build') && document.getElementById('wf-hint-build').getClientRects().length > 0`, 'the Workflows page', 8000);
            await sleep(300); // sleep-ok: the page lays out
            const r = await ev(`(() => { const t = id => (document.getElementById(id) || {}).textContent || ''; const vis = id => { const e = document.getElementById(id); return !!e && e.getClientRects().length > 0; };
                const desc = id => { const b = document.getElementById(id); const d = b && b.getAttribute('aria-describedby'); return d ? t(d) : ''; };
                return { build: t('wf-hint-build'), neu: t('wf-hint-new'), start: t('wf-hint-start'), vis: ['wf-hint-build', 'wf-hint-new', 'wf-hint-start'].every(vis), buildDesc: desc('wf-build-agent-btn'), newDesc: desc('wf-create-btn'), over: document.documentElement.scrollWidth - document.documentElement.clientWidth }; })()`);
            check(`${tag}: one line says what Build with Agent does and one what New Workflow does, as visible text`, /Build with Agent.*agent.*builds the workflow with you/.test(r.build) && /New Workflow.*empty form.*write the steps yourself/.test(r.neu) && r.vis, JSON.stringify(r));
            check(`${tag}: one line says what starts a workflow (pressing Run, a job's schedule, the API)`, /runs when you press Run, when a job schedules it, or through the API/.test(r.start), r.start);
            check(`${tag}: each button is described by its own line`, r.buildDesc === r.build && r.newDesc === r.neu, JSON.stringify([r.buildDesc, r.newDesc]));
            check(`${tag}: nothing overflows sideways`, r.over <= 1, String(r.over));
            // with workflows in the list the lines are one closed disclosure, "How workflows start" (#7001)
            check(`${tag}: while the list is empty the lines are open, under their "How workflows start" heading`, await ev(`document.getElementById('wf-hints').open === true && document.getElementById('wf-hints').querySelector('summary').textContent.trim() === 'How workflows start'`));
            await ev(`(() => { const R = window.fetch; window.fetch = (u, o) => { const url = String(u && u.url || u); if (new URL(url, location.origin).pathname === '/api/workflows' && (!o || !o.method || o.method === 'GET')) return Promise.resolve(new Response(JSON.stringify({ workflows: [{ id: 1, name: 'Nightly build', description: 'Build and test', enabled: 1, step_count: 2, steps: [], last_run: null }] }), { status: 200, headers: { 'content-type': 'application/json' } })); return R(u, o); }; return true; })()`);
            await ev(`import(${S} + 'workflows.js').then(m => m.showWorkflowsTab()).then(() => true)`);
            await waitFor(`document.querySelectorAll('#wf-list .wf-card').length === 1`, 'a workflow in the list', 8000);
            const withList = await ev(`(() => { const d = document.getElementById('wf-hints'); const closed = !d.open && !document.getElementById('wf-hint-start').checkVisibility(); d.querySelector('summary').click(); return { closed, opened: d.open && document.getElementById('wf-hint-start').checkVisibility(), startText: document.getElementById('wf-hint-start').textContent, describedStill: document.getElementById('wf-build-agent-btn').getAttribute('aria-describedby') === 'wf-hint-build' }; })()`);
            check(`${tag}: once there are workflows the lines are a closed "How workflows start" disclosure that opens to the same three lines, and the buttons still name theirs`, withList.closed && withList.opened && /press Run/.test(withList.startText) && withList.describedStill, JSON.stringify(withList));
        }
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} workflows hint checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
