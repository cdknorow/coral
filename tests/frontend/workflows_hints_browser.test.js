// The Workflows list says how its two ways to make a workflow differ and what starts one (#6988, UI Accessibility's #6962 LOWs): one short line each, as text (not a hover title), above the list; the two buttons
// name their line with aria-describedby. 1280 px, and 390 px at 200% text (nothing overflows sideways). Drives Chrome over CDP
// through agent_view_harness.js against the isolated dev server.
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

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
            check(`${tag}: one line says what starts a workflow (you, a job's schedule, the API)`, /runs when you run it, when a job schedules it, or through the API/.test(r.start), r.start);
            check(`${tag}: each button is described by its own line`, r.buildDesc === r.build && r.newDesc === r.neu, JSON.stringify([r.buildDesc, r.newDesc]));
            check(`${tag}: nothing overflows sideways`, r.over <= 1, String(r.over));
        }
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} workflows hint checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
