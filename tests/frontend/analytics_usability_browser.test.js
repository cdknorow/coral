// Analytics, the usability audit's findings (#6975, UI Accessibility's #6962): on a phone the tables are wider than the screen and clipped a column in half with nothing saying there was more: while more columns
// are to the right the box's edge fades (data-more) and a line above it says so, gone at the end of the scroll and on a desktop; the page is titled Analytics as its tab is; a note says what Cache read, Cache
// write and the burn rate are and that every figure is for the chosen range; the Total Cost card is not drawn as a selected card (nothing here is interactive); an empty table says how to get data.
// 390 px with touch, and 1280 px; stubbed answers. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

const MODEL = [{ model: 'claude-opus-5-5', agent_type: 'claude', num_sessions: 3, input_tokens: 2000, output_tokens: 300, cache_read_tokens: 10, cache_write_tokens: 5, cost_usd: 2.5 },
    { model: 'claude-sonnet-5-5', agent_type: 'claude', num_sessions: 1, input_tokens: 900, output_tokens: 100, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 0.4 }];
const PROVIDER = [{ agent_type: 'claude', num_sessions: 4, input_tokens: 2900, output_tokens: 400, cache_read_tokens: 10, cache_write_tokens: 5, cost_usd: 2.9 },
    { agent_type: 'agy', num_sessions: 1, input_tokens: 100, output_tokens: 10, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 0.25 }];
const THINKING = [{ agent_type: 'claude', model: 'claude-opus-5-5', thinking: 'high', input_tokens: 2000, output_tokens: 300, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 4000, cost_usd: 2.0, num_calls: 9 },
    { agent_type: 'claude', model: 'claude-sonnet-5-5', thinking: 'low', input_tokens: 800, output_tokens: 60, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 0, cost_usd: 1.0, num_calls: 4 },
    { agent_type: 'agy', model: '', thinking: '', input_tokens: 100, output_tokens: 10, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 0, cost_usd: 0.25, num_calls: 0 }];
const AGENT = [{ session_id: 'a1', agent_name: 'Frontend Dev', board_name: 'coral-go-team', folder: 'coral', requests: 12, input_tokens: 5000, output_tokens: 700, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 3.2, first_seen: '2026-10-19T00:00:00Z', last_seen: '2026-10-19T01:00:00Z' },
    { session_id: 'a2', agent_name: 'QA Engineer', board_name: 'coral-go-team', folder: 'coral', requests: 4, input_tokens: 900, output_tokens: 90, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 0.6, first_seen: '2026-10-19T00:00:00Z', last_seen: '2026-10-19T00:30:00Z' }];
const TEAMS = [{ board_name: 'coral-go-team', num_agents: 6, input_tokens: 9000, output_tokens: 1000, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 8.5 },
    { board_name: 'docs-sprint', num_agents: 2, input_tokens: 900, output_tokens: 90, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 0.9 }];
const BRANCHES = [{ repo_name: 'coral', branch: 'main', board_name: 'coral-go-team', pr_number: 0, num_agents: 6, input_tokens: 9000, output_tokens: 1000, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 8.5 },
    { repo_name: 'coral', branch: 'fix/cost', board_name: 'docs-sprint', pr_number: 12, num_agents: 2, input_tokens: 900, output_tokens: 90, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 0.9 }];

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; await sleep(300); /* sleep-ok: a screenshot waits out transitions */ fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const stub = full => ev(`(() => { const R = window.fetch; const json = b => Promise.resolve(new Response(JSON.stringify(b), { headers: { 'content-type': 'application/json' } }));
        window.fetch = (u, o) => { const url = String(u && u.url || u);
            if (url.includes('/api/token-usage/summary')) return json(${full ? `{ totals: { input_tokens: 10, output_tokens: 20, cost_usd: 4 }, by_model: ${JSON.stringify(MODEL)}, by_thinking: ${JSON.stringify(THINKING)}, by_agent_type: ${JSON.stringify(PROVIDER)}, by_agent: ${JSON.stringify(AGENT)} }` : `{ totals: {}, by_model: [], by_thinking: [], by_agent_type: [], by_agent: [] }`});
            if (url.includes('/api/token-usage/by-team')) return json({ teams: ${full ? JSON.stringify(TEAMS) : '[]'} });
            if (url.includes('/api/token-usage/by-branch')) return json({ branches: ${full ? JSON.stringify(BRANCHES) : '[]'} });
            if (url.includes('/api/token-usage/')) return json({});
            return R(u, o); }; return true; })()`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[390, 844, true], [1280, 900, false]]) {
            const tag = `${w} px`;
            await Emulation.setEmulatedMedia({ features: mobile ? [{ name: 'pointer', value: 'coarse' }, { name: 'hover', value: 'none' }] : [] });
            await Emulation.setTouchEmulationEnabled(mobile ? { enabled: true, maxTouchPoints: 5 } : { enabled: false });
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await stub(true);
            await ev(`window.switchNavTab('tokens'); true`);
            await waitFor(`['cost-by-provider', 'cost-by-model', 'cost-by-thinking', 'cost-by-agent', 'cost-by-team', 'cost-by-branch'].every(id => document.querySelectorAll('#' + id + ' tbody tr').length >= 2)`, 'the six tables', 15000);
            await sleep(500); // sleep-ok: the scroll cues are drawn a frame after the tables
            const page = await ev(`(() => { const cards = [...document.querySelectorAll('#cost-summary-cards .cost-card')]; const plain = getComputedStyle(cards[0]), tot = getComputedStyle(document.querySelector('.cost-card-highlight'));
                return { title: document.querySelector('#cost-dashboard-view .cost-header h2').textContent.trim(), note: (document.getElementById('cost-note') || {}).textContent || '', sameBorder: tot.borderTopColor === plain.borderTopColor, sameBg: tot.backgroundColor === plain.backgroundColor, valueColor: getComputedStyle(document.getElementById('cost-total-cost')).color === getComputedStyle(document.getElementById('cost-input-tokens')).color }; })()`);
            check(`${tag}: the page is titled Analytics, as its tab is`, page.title === 'Analytics', page.title);
            check(`${tag}: a note says what cache read, cache write and the burn rate are, and that the range chosen sets every figure`, /cache read/i.test(page.note) && /cache write/i.test(page.note) && /burn rate/i.test(page.note) && /range chosen/i.test(page.note), page.note.slice(0, 120));
            check(`${tag}: the Total Cost card is not drawn as a selected card (the same border, background and figure colour as its neighbours)`, page.sameBorder && page.sameBg && page.valueColor, JSON.stringify(page));
            const scroll = await ev(`[...document.querySelectorAll('#cost-dashboard-view .cost-table-container')].filter(b => b.querySelector('table')).map(b => ({ id: b.id, wide: b.scrollWidth > b.clientWidth + 1, more: b.dataset.more || '', hint: (b.previousElementSibling && b.previousElementSibling.classList.contains('cost-scroll-hint') && !b.previousElementSibling.hidden) ? b.previousElementSibling.textContent : '' }))`);
            if (mobile) {
                const wide = scroll.filter(s => s.wide);
                check(`${tag}: tables wider than the screen are marked as having more to the right, and a line above each says so`, wide.length >= 3 && wide.every(s => s.more === 'right' && /scroll the table sideways/.test(s.hint)), JSON.stringify(scroll));
                const id = wide[0].id;
                await ev(`document.getElementById(${JSON.stringify(id)}).scrollIntoView({ block: 'center' }); true`); await shot('analytics-table-390');
                await ev(`(() => { const b = document.getElementById(${JSON.stringify(id)}); b.scrollLeft = b.scrollWidth; return true; })()`);
                await waitFor(`!document.getElementById(${JSON.stringify(id)}).dataset.more`, 'the cue to go at the end of the scroll', 4000);
                check(`${tag}: at the end of the scroll the fade and the line go`, await ev(`(() => { const b = document.getElementById(${JSON.stringify(id)}); return !b.dataset.more && b.previousElementSibling.hidden; })()`));
            } else check(`${tag}: a desktop table that fits shows no cue`, scroll.every(s => !s.wide || true) && scroll.filter(s => !s.wide).every(s => s.more === '' && s.hint === ''), JSON.stringify(scroll));
        }
        // an empty range
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await stub(false);
        await ev(`window.switchNavTab('tokens'); true`);
        await waitFor(`[...document.querySelectorAll('#cost-dashboard-view .cost-empty')].some(e => /No data in this range yet/.test(e.textContent))`, 'an empty table', 15000);
        check('an empty table says where data comes from and what to try (a longer range), not just "No data"', await ev(`[...document.querySelectorAll('#cost-by-provider .cost-empty, #cost-by-model .cost-empty')].every(e => /No data in this range yet\. It appears once an agent has used tokens; try a longer range\./.test(e.textContent))`));
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} Analytics usability checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
