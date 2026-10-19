// The Cost tables on a phone (#5056): at 390 px the tables scroll sideways inside their card and Cost, the figure the page is opened for, was the last column, off screen. On a phone Cost
// (and Share, where there is one) follow the columns that name the row, so they are on screen without a scroll; everything else keeps its order after them. A wider window puts the original
// order back (also while the page is open), the sort still works on the moved column. By Model, By Thinking Level, By Team, By Branch and By Agent, from stubbed answers.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const MODEL = [{ model: 'claude-opus-5-5', agent_type: 'claude', num_sessions: 3, input_tokens: 2000, output_tokens: 300, cache_read_tokens: 10, cache_write_tokens: 5, cost_usd: 2.5 },
    { model: 'claude-sonnet-5-5', agent_type: 'claude', num_sessions: 1, input_tokens: 900, output_tokens: 100, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 0.4 }];
const THINKING = [{ agent_type: 'claude', thinking: 'high', input_tokens: 2000, output_tokens: 300, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 4000, cost_usd: 2.0, num_calls: 9 },
    { agent_type: 'claude', thinking: 'low', input_tokens: 800, output_tokens: 60, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 0, cost_usd: 1.0, num_calls: 4 },
    { agent_type: 'agy', thinking: '', input_tokens: 100, output_tokens: 10, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 0, cost_usd: 0.25, num_calls: 0 }];
const AGENT = [{ session_id: 'a1', agent_name: 'Frontend Dev', board_name: 'coral-go-team', folder: 'coral', requests: 12, input_tokens: 5000, output_tokens: 700, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 3.2, first_seen: '2026-10-19T00:00:00Z', last_seen: '2026-10-19T01:00:00Z' },
    { session_id: 'a2', agent_name: 'QA Engineer', board_name: 'coral-go-team', folder: 'coral', requests: 4, input_tokens: 900, output_tokens: 90, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 0.6, first_seen: '2026-10-19T00:00:00Z', last_seen: '2026-10-19T00:30:00Z' }];
const TEAMS = [{ board_name: 'coral-go-team', num_agents: 6, input_tokens: 9000, output_tokens: 1000, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 8.5 },
    { board_name: 'docs-sprint', num_agents: 2, input_tokens: 900, output_tokens: 90, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 0.9 }];
const BRANCHES = [{ repo_name: 'coral', branch: 'main', board_name: 'coral-go-team', pr_number: 0, num_agents: 6, input_tokens: 9000, output_tokens: 1000, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 8.5 },
    { repo_name: 'coral', branch: 'fix/cost', board_name: 'docs-sprint', pr_number: 12, num_agents: 2, input_tokens: 900, output_tokens: 90, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 0.9 }];
// The original column order of each table, as a wide window shows it.
const WIDE = {
    'cost-by-model': ['Provider', 'Model', 'Requests', 'Input', 'Output', 'Cache Read', 'Cache Write', 'Cost'],
    'cost-by-thinking': ['Provider', 'Thinking level', 'Input', 'Output', 'Thinking', 'Cost', 'Share'],
    'cost-by-team': ['Team', 'Agents', 'Input', 'Output', 'Cache Read', 'Cache Write', 'Cost', 'Share'],
};

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        // A phone is a coarse pointer: its 44 px filter buttons are what make the name columns wide (#5053), so that is the case under test.
        await Emulation.setEmulatedMedia({ features: [{ name: 'pointer', value: 'coarse' }, { name: 'hover', value: 'none' }] });
        await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        const l = Page.loadEventFired(); await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await l; await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { const R = window.fetch; const json = b => Promise.resolve(new Response(JSON.stringify(b), { headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o) => { const url = String(u && u.url || u);
                if (url.includes('/api/token-usage/summary')) return json({ totals: { input_tokens: 10, output_tokens: 20, cost_usd: 4 }, by_model: ${JSON.stringify(MODEL)}, by_thinking: ${JSON.stringify(THINKING)}, by_agent: ${JSON.stringify(AGENT)} });
                if (url.includes('/api/token-usage/by-team')) return json({ teams: ${JSON.stringify(TEAMS)} });
                if (url.includes('/api/token-usage/by-branch')) return json({ branches: ${JSON.stringify(BRANCHES)} });
                if (url.includes('/api/token-usage/')) return json({});
                return R(u, o); }; return true; })()`);
        await ev(`window.switchNavTab('tokens'); true`);
        await waitFor(`['cost-by-model', 'cost-by-thinking', 'cost-by-agent', 'cost-by-team', 'cost-by-branch'].every(id => document.querySelectorAll('#' + id + ' tbody tr').length >= 2)`, 'the five tables', 15000);

        // The headings of each table, whether its Cost heading and every Cost cell sit inside the card, and the heading's index.
        const look = () => ev(`(() => { const out = {}; for (const id of ['cost-by-model', 'cost-by-thinking', 'cost-by-agent', 'cost-by-team', 'cost-by-branch']) {
            const box = document.getElementById(id), card = box.getBoundingClientRect();
            const heads = [...box.querySelectorAll('thead th')].map(th => th.textContent.replace(/[▲▼]|filter_list/g, '').trim());
            const ci = heads.indexOf('Cost');
            const inside = e => { const r = e.getBoundingClientRect(); return r.left >= card.left - 1 && r.right <= card.right + 1; };
            out[id] = { heads, ci, headInside: inside(box.querySelectorAll('thead th')[ci]), cellsInside: [...box.querySelectorAll('tbody tr')].every(tr => inside(tr.children[ci])), cellsText: [...box.querySelectorAll('tbody tr')].map(tr => tr.children[ci].textContent.trim()) };
        } return out; })()`);

        console.log('1. At 390 px Cost follows the columns that name the row, and is on screen without a scroll');
        let t = await look();
        const names = { 'cost-by-model': 2, 'cost-by-thinking': 2, 'cost-by-agent': 1, 'cost-by-team': 1, 'cost-by-branch': 1 };
        for (const [id, n] of Object.entries(names)) {
            assert.equal(t[id].ci, n, `${id}: Cost comes right after its ${n} name column(s): ${JSON.stringify(t[id].heads)}`);
            assert.ok(t[id].headInside && t[id].cellsInside, `${id}: the Cost heading and every Cost cell are inside the card without scrolling: ${JSON.stringify(t[id])}`);
        }
        assert.equal(t['cost-by-thinking'].heads[3], 'Share', 'Share follows Cost: ' + JSON.stringify(t['cost-by-thinking'].heads));
        console.log('   ✓');

        console.log('2. Every cell moved with its heading (the Cost cells hold costs), and sorting by the moved Cost column works');
        for (const id of Object.keys(names)) assert.ok(t[id].cellsText.every(x => /^\$/.test(x)), `${id}: the cells under Cost are costs: ${JSON.stringify(t[id].cellsText)}`);
        await ev(`[...document.querySelectorAll('#cost-by-thinking th')].find(th => /^Cost/.test(th.textContent.trim())).querySelector('button').click(); true`);
        t = await look();
        const dollars = t['cost-by-thinking'].cellsText.map(x => Number(x.replace(/[$,]/g, '')));
        assert.ok(dollars.every((v, i) => i === 0 || v <= dollars[i - 1]) || dollars.every((v, i) => i === 0 || v >= dollars[i - 1]), 'the rows are in cost order after the click: ' + JSON.stringify(dollars));
        assert.equal(t['cost-by-thinking'].ci, 2, 'and Cost stays where it was moved to');
        console.log('   ✓');

        console.log('3. A wider window puts the original order back, with the page open; narrowing moves them again');
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 844, deviceScaleFactor: 1, mobile: false });
        await waitFor(`[...document.querySelectorAll('#cost-by-thinking thead th')].map(th => th.textContent.replace(/[▲▼]|filter_list/g, '').trim()).join('|') === ${JSON.stringify(WIDE['cost-by-thinking'].join('|'))}`, 'the wide order', 6000);
        t = await look();
        for (const [id, heads] of Object.entries(WIDE)) assert.deepEqual(t[id].heads, heads, `${id}: the wide order is back`);
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        await waitFor(`document.querySelector('#cost-by-thinking thead th:nth-child(3)').textContent.trim().startsWith('Cost')`, 'the phone order again', 6000);
        console.log('   ✓');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
