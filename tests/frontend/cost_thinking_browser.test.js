// The Cost view's "By Thinking Level" table (#4969), against a stubbed /api/token-usage/summary: a row per provider and level the
// transcripts recorded, and "Not reported" rows (dimmed, no thinking-token count) for the usage whose turn recorded no level; the
// share of cost; sorting; and a 390x844 phone without a page-wide horizontal scroll (the table scrolls in its card).
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const row = (agent_type, model, thinking, input, output, think, cost, calls) => ({ agent_type, model, thinking, input_tokens: input, output_tokens: output, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: think, cost_usd: cost, num_calls: calls });
    const SUMMARY = {
        totals: { input_tokens: 10, output_tokens: 20, cost_usd: 4.5 },
        by_agent_type: [
            { agent_type: 'claude', num_sessions: 4, input_tokens: 4100, output_tokens: 500, cache_read_tokens: 0, cache_write_tokens: 0, total_tokens: 4600, cost_usd: 4.25 },
            { agent_type: 'agy', num_sessions: 1, input_tokens: 100, output_tokens: 10, cache_read_tokens: 0, cache_write_tokens: 0, total_tokens: 110, cost_usd: 0.25 },
        ],
        by_model: [
            { agent_type: 'claude', model: 'claude-opus-5-5', num_sessions: 3, input_tokens: 3300, output_tokens: 400, cache_read_tokens: 0, cache_write_tokens: 0, total_tokens: 3700, cost_usd: 3.75 },
            { agent_type: 'claude', model: 'claude-sonnet-5-5', num_sessions: 1, input_tokens: 800, output_tokens: 100, cache_read_tokens: 0, cache_write_tokens: 0, total_tokens: 900, cost_usd: 0.5 },
            { agent_type: 'agy', model: '', num_sessions: 1, input_tokens: 100, output_tokens: 10, cache_read_tokens: 0, cache_write_tokens: 0, total_tokens: 110, cost_usd: 0.25 },
        ],
        by_agent: [],
        by_thinking: [
            row('claude', 'claude-opus-5-5', 'high', 2000, 300, 4000, 2.0, 9),
            row('claude', 'claude-opus-5-5', 'low', 800, 60, 0, 1.0, 4),
            row('claude', 'claude-opus-5-5', '', 500, 40, 0, 0.75, 0),   // opus's remainder: Not reported
            row('claude', 'claude-sonnet-5-5', 'medium', 800, 100, 0, 0.5, 5),
            row('agy', '', '', 100, 10, 0, 0.25, 0),                     // a provider that records neither
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
        await waitFor(`document.querySelectorAll('#cost-by-thinking tbody tr').length === 5`, 'the thinking table', 8000);

        const table = () => ev(`[...document.querySelectorAll('#cost-by-thinking tbody tr')].map(tr => ({ cells: [...tr.children].map(td => td.textContent.trim()), dim: tr.classList.contains('cost-row-unreported') }))`);
        const run = async (label) => {
            console.log(`${label}: the levels recorded and the Not reported rows`);
            let rows = await table();
            // columns: Provider, Model, Thinking level, Calls, Input, Output, Cache Read, Cache Write, Thinking, Cost, Share (the phone moves Cost and Share up, by cell index, so read by heading)
            const cols = await ev(`[...document.querySelectorAll('#cost-by-thinking thead th')].map(th => th.textContent.replace(/[\\u25B2\\u25BC]|filter_list/g, '').trim())`);
            const at = h => cols.indexOf(h);
            assert.ok(['Provider', 'Model', 'Thinking level', 'Calls', 'Input', 'Output', 'Thinking', 'Cost', 'Share'].every(h => at(h) >= 0), 'the columns: ' + JSON.stringify(cols));
            const cell = (r, h) => r.cells[at(h)];
            assert.deepEqual(rows.map(r => [cell(r, 'Provider'), cell(r, 'Thinking level')].join('/')), ['claude/High', 'claude/Low', 'claude/Not reported', 'claude/Medium', 'agy/Not reported'], 'dearest first: ' + JSON.stringify(rows));
            assert.deepEqual(rows.map(r => cell(r, 'Model')).map(m => /Opus 5\.5/.test(m) ? 'opus' : /Sonnet 5\.5/.test(m) ? 'sonnet' : m), ['opus', 'opus', 'opus', 'sonnet', 'Not reported'], 'a row per provider, model and level; a model that was not recorded is Not reported');
            assert.equal(cell(rows[0], 'Thinking'), '4.0K', 'the thinking tokens the CLI reported, as a number: ' + cell(rows[0], 'Thinking'));
            assert.equal(cell(rows[2], 'Thinking'), '–', 'Not reported has no thinking-token count');
            assert.equal(cell(rows[0], 'Calls'), '9');
            assert.deepEqual(rows.map(r => cell(r, 'Share')), ['44%', '22%', '17%', '11%', '6%'], 'shares of the cost');
            assert.deepEqual(rows.map(r => r.dim), [false, false, true, false, true], 'Not reported levels are dimmed');
            assert.ok(rows.every(r => !/launch/i.test(r.cells.join(' '))), 'never the launched level');
            console.log('   ✓ rows, shares, Not reported');

            console.log(`${label}: By Provider and By Model, the same columns`);
            const tbl = id => ev(`(() => { const heads = [...document.querySelectorAll('#${id} thead th')].map(th => th.textContent.replace(/[\\u25B2\\u25BC]|filter_list/g, '').trim());
                return { heads, rows: [...document.querySelectorAll('#${id} tbody tr')].map(tr => [...tr.children].map(td => td.textContent.trim())) }; })()`);
            const prov = await tbl('cost-by-provider'), mod = await tbl('cost-by-model');
            for (const t of [prov, mod]) assert.ok(['Requests', 'Input', 'Output', 'Cost', 'Share'].every(h => t.heads.includes(h)), 'the columns: ' + JSON.stringify(t.heads));
            const pc = h => prov.heads.indexOf(h), mc = h => mod.heads.indexOf(h);
            assert.deepEqual(prov.rows.map(r => r[pc('Provider')]), ['claude', 'agy'], 'By Provider, dearest first');
            assert.deepEqual(prov.rows.map(r => r[pc('Share')]), ['94%', '6%'], 'provider shares: ' + JSON.stringify(prov.rows));
            assert.deepEqual(mod.rows.map(r => r[mc('Provider')]), ['claude', 'claude', 'agy'], 'By Model: provider and model');
            assert.deepEqual(mod.rows.map(r => r[mc('Share')]), ['83%', '11%', '6%'], 'model shares: ' + JSON.stringify(mod.rows));
            assert.ok(mod.heads.indexOf('Model') > mod.heads.indexOf('Provider'));
            console.log('   ✓ By Provider, By Model');

            console.log(`${label}: sorting by level`);
            const levelAt = cols.indexOf('Thinking level');
            const header = `[...document.querySelectorAll('#cost-by-thinking th.cost-th-sort')].find(th => th.textContent.trim().startsWith('Thinking level'))`;
            await ev(`${header}.click(); true`);
            await waitFor(`/[\\u25B2\\u25BC]/.test(${header}.textContent)`, 'the sort arrow', 4000);
            await ev(`${header}.click(); true`);
            await waitFor(`/\\u25B2/.test(${header}.textContent)`, 'ascending', 4000);
            rows = await table();
            assert.equal(rows[rows.length - 1].cells[levelAt], 'Not reported', 'Not reported sorts after the levels: ' + JSON.stringify(rows.map(r => r.cells[levelAt])));
            await ev(`${header}.click(); ${header}.click(); true`); // back to a known order is not needed: the next scene re-reads
            await shot('cost-thinking-' + label.split(' ')[0].toLowerCase());
            console.log('   ✓ sorted');
        };
        await run('Desktop');

        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true });
        await ev(`window.switchNavTab('tokens'); true`);
        await waitFor(`document.querySelectorAll('#cost-by-thinking tbody tr').length === 5`, 'the table on the phone', 8000);
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
