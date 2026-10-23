// Analytics (the Cost view) for a person using a screen reader (#6432): the spend chart has a text alternative that says what it shows; the small sparklines in the cards are not read (each card says its number); every
// table is named by its heading and its sort and filter buttons are named for their table, so "Cost" and "Filter Cost" are not repeated six times with nothing to tell them apart. Desktop and a 390 px phone.
// Drives Chrome over CDP through agent_view_harness.js with the census data world (helpers/census_world.js).
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { DATA_WORLD } = require('./helpers/census_world');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await Page.addScriptToEvaluateOnNewDocument({ source: DATA_WORLD });
        for (const [w, h, mobile, tag] of [[1280, 800, false, 'desktop'], [390, 844, true, 'phone']]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'liveListLoaded', 'settings')`);
            await ev(`window.switchNavTab('tokens'); true`);
            await waitFor(`['cost-by-provider', 'cost-by-model', 'cost-by-thinking', 'cost-by-agent', 'cost-by-team', 'cost-by-branch'].every(id => document.querySelectorAll('#' + id + ' tbody tr').length > 0) && !!document.querySelector('#cost-spend-chart svg')`, `${tag}: the analytics data`, 15000);
            const d = await ev(`(() => {
                const name = e => e.getAttribute('aria-label') || e.textContent.replace(/\\s+/g, ' ').trim();
                const sorts = [...document.querySelectorAll('#cost-dashboard-view .cost-sort-btn')].map(name), filters = [...document.querySelectorAll('#cost-dashboard-view .cost-col-filter')].map(name);
                const dup = l => l.filter((n, i) => l.indexOf(n) !== i);
                const svg = document.querySelector('#cost-spend-chart svg');
                const tables = [...document.querySelectorAll('#cost-dashboard-view .cost-section')].map(sec => { const t = sec.querySelector('table'), h = sec.querySelector('.cost-section-title'); if (!t || !h) return null; const l = t.getAttribute('aria-labelledby'); return { heading: h.textContent.trim(), named: l ? (document.getElementById(l) || {}).textContent.trim() : null }; }).filter(Boolean);
                return { dupSorts: [...new Set(dup(sorts))], dupFilters: [...new Set(dup(filters))], chart: svg ? { role: svg.getAttribute('role'), label: svg.getAttribute('aria-label') } : null,
                    sparks: [...document.querySelectorAll('.cost-card-sparkline')].filter(e => e.querySelector('svg')).map(e => e.getAttribute('aria-hidden')), tables };
            })()`);
            assert.deepEqual(d.dupSorts, [], `${tag}: no two sort buttons share a name (${JSON.stringify(d.dupSorts)})`);
            assert.deepEqual(d.dupFilters, [], `${tag}: no two filter buttons share a name (${JSON.stringify(d.dupFilters)})`);
            assert.ok(d.chart && d.chart.role === 'img' && /cumulative spend/i.test(d.chart.label || '') && /\$\d/.test(d.chart.label || ''), `${tag}: the spend chart says what it shows with its total (${JSON.stringify(d.chart)})`);
            assert.ok(d.sparks.length >= 3 && d.sparks.every(a => a === 'true'), `${tag}: the sparklines are hidden from a screen reader (${JSON.stringify(d.sparks)})`);
            assert.ok(d.tables.length >= 5 && d.tables.every(t => t.named === t.heading), `${tag}: each table is named by its heading (${JSON.stringify(d.tables)})`);
            // a sort still sorts and keeps its place
            await ev(`document.querySelector('#cost-by-provider .cost-sort-btn').click(); true`);
            await new Promise(r => setTimeout(r, 300)); // sleep-ok: the table redraws
            assert.equal(await ev(`document.querySelector('#cost-by-provider th[aria-sort="ascending"], #cost-by-provider th[aria-sort="descending"]') !== null`), true, `${tag}: aria-sort says which column is sorted`);
            assert.ok(await ev(`document.querySelector('#cost-by-provider table').getAttribute('aria-labelledby') === document.querySelector('#cost-by-provider').closest('.cost-section').querySelector('.cost-section-title').id`), `${tag}: a redrawn table is named again`);
            console.log(`   ✓ ${tag}`);
        }
        console.log('PASS Analytics: the chart has a text alternative, sparklines are hidden, tables and their sort and filter buttons are named');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
