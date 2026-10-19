// The Cost view's "By Thinking Level" table (#4969), against a stubbed /api/token-usage/summary: a row per provider and level the
// transcripts recorded, and "Not reported" rows (dimmed, no thinking-token count) for the usage whose turn recorded no level; the
// share of cost; sorting; and a 390x844 phone without a page-wide horizontal scroll (the table scrolls in its card).
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const SUMMARY = {
        totals: { input_tokens: 10, output_tokens: 20, cost_usd: 4 },
        by_model: [], by_agent: [],
        by_thinking: [
            { agent_type: 'claude', thinking: 'high', input_tokens: 2000, output_tokens: 300, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 4000, cost_usd: 2.0, num_calls: 9 },
            { agent_type: 'claude', thinking: 'low', input_tokens: 800, output_tokens: 60, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 0, cost_usd: 1.0, num_calls: 4 },
            { agent_type: 'claude', thinking: '', input_tokens: 500, output_tokens: 40, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 0, cost_usd: 0.75, num_calls: 0 },
            { agent_type: 'agy', thinking: '', input_tokens: 100, output_tokens: 10, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 0, cost_usd: 0.25, num_calls: 0 },
        ],
    };
    const shot = async (name) => {
        if (!process.env.CORAL_SCREENSHOT_DIR) return;
        fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true });
        fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
    };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { const R = window.fetch; window.fetch = (u, o) => { const url = String(u && u.url || u);
            const json = b => Promise.resolve(new Response(JSON.stringify(b), { headers: { 'content-type': 'application/json' } }));
            if (url.includes('/api/token-usage/summary')) return json(${JSON.stringify(SUMMARY)});
            if (url.includes('/api/token-usage/')) return json({});
            return R(u, o); }; return true; })()`);
        await ev(`window.switchNavTab('tokens'); true`);
        await waitFor(`document.querySelectorAll('#cost-by-thinking tbody tr').length === 4`, 'the thinking table', 8000);

        const table = () => ev(`[...document.querySelectorAll('#cost-by-thinking tbody tr')].map(tr => ({ cells: [...tr.children].map(td => td.textContent.trim()), dim: tr.classList.contains('cost-row-unreported') }))`);
        const run = async (label) => {
            console.log(`${label}: the levels recorded and the Not reported rows`);
            let rows = await table();
            assert.deepEqual(rows.map(r => r.cells[0] + '/' + r.cells[1]), ['claude/High', 'claude/Low', 'claude/Not reported', 'agy/Not reported'], 'dearest first: ' + JSON.stringify(rows));
            assert.equal(rows[0].cells[4], '4.0K', 'the thinking tokens the CLI reported, as a number: ' + rows[0].cells[4]);
            assert.equal(rows[2].cells[4], '–', 'Not reported has no thinking-token count');
            assert.deepEqual(rows.map(r => r.cells[6]), ['50%', '25%', '19%', '6%'], 'shares of the cost');
            assert.deepEqual(rows.map(r => r.dim), [false, false, true, true], 'Not reported rows are dimmed');
            assert.ok(rows.every(r => !/launch/i.test(r.cells.join(' '))), 'never the launched level');
            console.log('   ✓ rows, shares, Not reported');

            console.log(`${label}: sorting by level`);
            const header = `[...document.querySelectorAll('#cost-by-thinking th.cost-th-sort')].find(th => th.textContent.trim().startsWith('Thinking level'))`;
            await ev(`${header}.click(); true`);
            await waitFor(`/[\\u25B2\\u25BC]/.test(${header}.textContent)`, 'the sort arrow', 4000);
            await ev(`${header}.click(); true`);
            await waitFor(`/\\u25B2/.test(${header}.textContent)`, 'ascending', 4000);
            rows = await table();
            assert.equal(rows[rows.length - 1].cells[1], 'Not reported', 'Not reported sorts after the levels: ' + JSON.stringify(rows.map(r => r.cells[1])));
            await ev(`${header}.click(); ${header}.click(); true`); // back to a known order is not needed: the next scene re-reads
            await shot('cost-thinking-' + label.split(' ')[0].toLowerCase());
            console.log('   ✓ sorted');
        };
        await run('Desktop');

        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true });
        await ev(`window.switchNavTab('tokens'); true`);
        await waitFor(`document.querySelectorAll('#cost-by-thinking tbody tr').length === 4`, 'the table on the phone', 8000);
        const overflow = await ev(`({ page: document.documentElement.scrollWidth - document.documentElement.clientWidth, container: (() => { const c = document.getElementById('cost-by-thinking'); return c.scrollWidth > c.clientWidth ? 'scrolls inside its card' : 'fits'; })() })`);
        assert.ok(overflow.page <= 1, 'no page-wide horizontal scroll on the phone: ' + JSON.stringify(overflow));
        await shot('cost-thinking-phone');
        console.log('Phone 390x844: ' + overflow.container + ', no page scroll\n   ✓');
        await Emulation.clearDeviceMetricsOverride();
        await Emulation.setTouchEmulationEnabled({ enabled: false });
        console.log('\nPASS cost by thinking level');
    } finally {
        await client.close();
    }
})().catch(e => { console.error(e); process.exitCode = 1; });
