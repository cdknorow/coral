// #4589: the agent hover shows the Model (the friendly name, hidden when unknown) and Analytics > By Model lists real models with a
// Provider column: a session that switched models is in two rows, the table sorts and filters, and it fits a phone. The server's fields
// (model on the session, by_model in the summary) are stood in for by a page-side stub. Drives Chrome over CDP through
// agent_view_harness.js against an isolated dev server; run_cdp_suites.mjs finds it by name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

const row = (agent_type, model, n, cost, extra = {}) => ({ agent_type, model, num_sessions: n, input_tokens: n * 1000, output_tokens: n * 400, cache_read_tokens: n * 100, cache_write_tokens: n * 50, cost_usd: cost, ...extra });
const SUMMARY = {
    totals: { input_tokens: 9000, output_tokens: 3600, cost_usd: 9.2, num_sessions: 8 },
    by_model: [row('claude', 'claude-opus-5-5', 3, 5.1), row('claude', 'claude-sonnet-5-5', 2, 1.4), row('codex', 'gpt-5-codex', 2, 2.5), row('agy', '', 1, 0.2), row('claude', 'claude-x-9', 1, 0, { model_display: 'Server Named' })],
    by_agent: [],
};

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => { const inner = window.fetch; window.fetch = (url, opts) => { const u = String(url && url.url || url);
            const json = b => Promise.resolve(new Response(JSON.stringify(b), { headers: { 'content-type': 'application/json' } }));
            if (u.includes('/api/token-usage/summary')) return json(${JSON.stringify(SUMMARY)}); if (u.includes('/api/token-usage/')) return json({}); return inner(url, opts); }; })();` });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await require('./helpers/wait').bind(ev).coralReady('startup');
        const MOD = `(window.__coralStatic || '/static/')`;

        console.log('1. The hover: a Model row under Agent, the friendly name, the id behind it; no row while unknown');
        const tips = await ev(`(async () => { const { buildSessionTooltip } = await import(${MOD} + 'session_state.js'); const base = { session_id: 'x', agent_type: 'claude', staleness_seconds: 5 };
            const rows = h => { const d = document.createElement('div'); d.innerHTML = h; return [...d.querySelectorAll('tr')].map(r => [r.children[0].textContent, r.children[1].textContent, r.children[1].getAttribute('title')]); };
            return { known: rows(buildSessionTooltip({ ...base, model: 'claude-opus-5-5' })), named: rows(buildSessionTooltip({ ...base, model: 'claude-x-9', model_display: 'Server Named' })),
                none: rows(buildSessionTooltip(base)), empty: rows(buildSessionTooltip({ ...base, model: '' })), blank: rows(buildSessionTooltip({ ...base, model: '  ' })), markup: rows(buildSessionTooltip({ ...base, model: '<b>x</b>' })) }; })()`);
        const labels = r => r.map(x => x[0]);
        const at = tips.known.findIndex(r => r[0] === 'Model');
        assert.ok(at > 0 && tips.known[at - 1][0] === 'Agent', 'the Model row is right under Agent: ' + labels(tips.known));
        assert.deepEqual(tips.known[at].slice(1), ['Opus 5.5', 'claude-opus-5-5'], 'friendly name, id as the tooltip');
        assert.equal(tips.named.find(r => r[0] === 'Model')[1], 'Server Named', "the server's name wins");
        for (const k of ['none', 'empty', 'blank']) assert.ok(!labels(tips[k]).includes('Model'), `${k}: no Model row`);
        assert.equal(tips.markup.find(r => r[0] === 'Model')[1], '<b>x</b>', 'an id with markup is text, not markup');
        console.log('   ✓ shown, hidden, escaped');

        console.log('2. By Model: a Provider and a Model column, one row per model and provider');
        await ev(`(async () => { const m = await import(${MOD} + 'cost_dashboard.js'); m.showCostDashboard(); return true; })()`);
        await waitFor(`document.querySelectorAll('#cost-by-model tbody tr').length === 5`, 'the table');
        const table = () => ev(`(() => ({ head: [...document.querySelectorAll('#cost-by-model thead th')].map(th => th.textContent.replace(/[▲▼↑↓]/g, '').trim().replace(/\\s*filter_list|\\s*filter_alt/g, '')), rows: [...document.querySelectorAll('#cost-by-model tbody tr')].map(tr => [tr.children[0].textContent.trim(), tr.children[1].textContent.trim(), tr.children[1].getAttribute('title')]) }))()`);
        let t = await table();
        assert.deepEqual(t.head.slice(0, 3), ['Provider', 'Model', 'Requests'], 'columns: ' + t.head);
        const byCost = t.rows.map(r => r.slice(0, 2).join(' / '));
        assert.deepEqual(byCost, ['claude / Opus 5.5', 'codex / GPT-5 Codex', 'claude / Sonnet 5.5', 'agy / unknown', 'claude / Server Named'], 'by cost, friendly names, unknown shown as unknown: ' + byCost);
        assert.equal(t.rows.filter(r => r[0] === 'claude').length, 3, 'one provider, a session that switched models is in two model rows (and a third model)');
        assert.equal(t.rows[0][2], 'claude-opus-5-5', 'the id is the tooltip');
        console.log('   ✓ columns and rows');

        console.log('3. Sort by Provider and by Model; filter by Provider');
        const cmp = (a, b) => a.localeCompare(b);
        const click = f => ev(`document.querySelector('#cost-by-model th[data-field=${f}]').click(); true`);
        await click('agent_type'); t = await table(); let col = t.rows.map(r => r[0]);
        assert.deepEqual(col, [...col].sort(cmp).reverse(), 'a first click sorts the new column high to low (the table\'s rule): ' + col);
        await click('agent_type'); t = await table(); col = t.rows.map(r => r[0]);
        assert.deepEqual(col, [...col].sort(cmp), 'a second click reverses it: ' + col);
        await click('model'); t = await table(); col = t.rows.map(r => r[1]);
        assert.deepEqual(col, [...col].sort(cmp).reverse(), 'Model sorts by the name shown: ' + col);
        await click('model'); t = await table(); col = t.rows.map(r => r[1]);
        assert.deepEqual(col, [...col].sort(cmp), 'and back: ' + col);
        await ev(`document.querySelector('#cost-by-model th[data-field=agent_type] .cost-col-filter').click(); true`);
        await waitFor(`!!document.querySelector('.cost-filter-dropdown')`, 'the Provider filter');
        const opts = await ev(`[...document.querySelectorAll('.cost-filter-dropdown .cost-filter-items label, .cost-filter-dropdown .cost-filter-items li, .cost-filter-dropdown .cost-filter-items div')].map(e => e.textContent.trim()).filter(Boolean)`);
        assert.ok(['agy', 'claude', 'codex'].every(p => opts.some(o => o.includes(p))), 'the Provider filter lists the providers: ' + opts);
        await ev(`document.querySelector('.cost-filter-dropdown [data-action=none]').click(); const boxes = [...document.querySelectorAll('.cost-filter-dropdown input[type=checkbox]')]; const c = boxes.find(b => b.closest('label, div, li').textContent.trim() === 'codex'); c.click(); true`);
        await waitFor(`document.querySelectorAll('#cost-by-model tbody tr').length === 1`, 'only codex');
        t = await table(); assert.deepEqual(t.rows.map(r => r[0] + ' / ' + r[1]), ['codex / GPT-5 Codex']);
        console.log('   ✓ sort and filter');

        console.log('4. A phone: the table fits (it scrolls inside its box, the page does not)');
        await ev(`document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })); true`);
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await require('./helpers/wait').bind(ev).coralReady('startup');
        await ev(`(async () => { const m = await import(${MOD} + 'cost_dashboard.js'); m.showCostDashboard(); return true; })()`);
        await waitFor(`document.querySelectorAll('#cost-by-model tbody tr').length === 5`, 'the table on a phone');
        const phone = await ev(`(() => { const c = document.getElementById('cost-by-model'); const t = c.querySelector('table'); return { over: document.documentElement.scrollWidth - document.documentElement.clientWidth, boxScrolls: c.scrollWidth > c.clientWidth + 1, boxOverflowX: getComputedStyle(c).overflowX, cut: t.getBoundingClientRect().right > c.getBoundingClientRect().right + 1 && !['auto', 'scroll'].includes(getComputedStyle(c).overflowX) }; })()`);
        assert.equal(phone.over, 0, 'no sideways scroll on the page: ' + JSON.stringify(phone));
        assert.equal(phone.cut, false, 'no column cut off (the box scrolls): ' + JSON.stringify(phone));
        console.log('   ✓ ' + JSON.stringify(phone));

        console.log('\nAll model display checks passed');
    } finally { await client.close(); }
})().catch(err => { console.error(err); process.exit(1); });
