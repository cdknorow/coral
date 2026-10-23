// A long artifact list does not stall the page (#5329). 300 rows built in one go were a 160 ms stall on a slow computer, so the list draws its first 40 rows at once and the rest in later frames
// (60 a frame), in the same order; a list the person is acting in (focus on a row far down) or that is drawn again mid-way is never left half drawn or doubled. Against a stubbed board route.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { framesJs } = require('./helpers/wait');
const N = 300, TEAM = 'll-team-' + Date.now().toString(36);
const items = Array.from({ length: N }, (_, i) => ({ name: `report-${String(i).padStart(3, '0')}.md`, media_type: 'text/markdown', size: 2048 + i, task_id: 100 + i, task_title: 'Task ' + i, produced_by: 'll-agent', created_at: new Date(Date.UTC(2026, 9, 10, 10, 0, i)).toISOString(), uri: 'coral://artifacts/' + String(i % 256).toString(16).padStart(2, '0').repeat(32).slice(0, 62) + String(1000 + i).slice(-2).padStart(2, '0'), available: true, inline: false }));

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const ROWS = `#agentic-panel-team-artifacts tr.team-artifact-row`;
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { const R = window.fetch; window.__items = ${JSON.stringify(items)};
            window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin); if (/^\\/api\\/board\\/[^/]+\\/artifacts$/.test(url.pathname) && (o.method || 'GET') === 'GET') { const body = new Response(JSON.stringify({ project: ${JSON.stringify(TEAM)}, artifacts: window.__items, has_more: false, truncated: false }), { status: 200, headers: { 'content-type': 'application/json' } }); return window.__delay ? new Promise(r => setTimeout(() => r(body), window.__delay)) : Promise.resolve(body); } // __delay: a slow answer (a frame passes while it loads, #6509)
                if (url.pathname === '/api/system/artifact-store/kept') return Promise.resolve(new Response(JSON.stringify({ kept: [] }), { status: 200, headers: { 'content-type': 'application/json' } })); return R(u, o); };
            return true; })()`);
        await ev(`window.__m.add('ll-bot', 'A', 4, { sleeping: false, board_project: ${JSON.stringify(TEAM)} }); true`);
        await waitFor(`T.state.liveSessions.some(s => s.name === 'll-bot')`, 'the agent', 6000);
        await ev(`window.selectLiveSession('ll-bot', 'claude', 'll-bot-id', 'local'); true`);
        await ev(`${framesJs(4)}`);

        console.log('1. rows are added a bounded number at a time');
        // the row count at every animation frame while the list draws
        const sample = `window.__counts = []; window.__mo && window.__mo.disconnect(); window.__mo = new MutationObserver(() => { const n = document.querySelectorAll('${ROWS}').length; if (!window.__counts.length || window.__counts[window.__counts.length - 1] !== n) window.__counts.push(n); }); window.__mo.observe(document.getElementById('agentic-panel-team-artifacts'), { childList: true, subtree: true }); true`;
        await ev(sample);
        await ev(`window.switchAgenticTab('team-artifacts', 'top'); true`);
        await waitFor(`document.querySelectorAll('${ROWS}').length === ${N}`, 'all 300 rows', 15000);
        await ev(`${framesJs(3)}`);
        const counts = (await ev(`window.__counts`)).filter(n => n > 0);
        const steps = counts.map((n, i) => n - (i ? counts[i - 1] : 0));
        check(`300 rows came a first 40, then at most 60 a frame (${JSON.stringify(counts)})`, counts[0] <= 40 && steps.slice(1).every(d => d > 0 && d <= 60) && counts[counts.length - 1] === N && counts.length >= 6);
        const names = await ev(`[...document.querySelectorAll('${ROWS} .team-artifact-name')].map(e => e.textContent.trim())`);
        check('all 300 are there, once each, in the list\'s order', names.length === N && new Set(names).size === N && names.every((n, i) => n === `report-${String(i).padStart(3, '0')}.md`), `${names.slice(0, 2)} ... ${names.slice(-1)}`);

        console.log('2. drawn again mid-way: no stale or doubled rows');
        await ev(`document.querySelector('.team-artifacts-refresh').click(); true`);
        await waitFor(`document.querySelectorAll('${ROWS}').length > 0 && document.querySelectorAll('${ROWS}').length < ${N}`, 'a list part-drawn', 3000).catch(() => {});
        await ev(`document.querySelector('.team-artifacts-refresh').click(); true`); // again, while the first is still drawing
        await waitFor(`document.querySelectorAll('${ROWS}').length === ${N}`, 'all 300 again', 15000);
        await ev(`${framesJs(6)}`);
        const again = await ev(`[...document.querySelectorAll('${ROWS} .team-artifact-name')].map(e => e.textContent.trim())`);
        check(`after two quick refreshes there are exactly ${N} rows, each once (${again.length}, ${new Set(again).size} distinct)`, again.length === N && new Set(again).size === N);

        console.log('2b. a redraw of a scrolled list keeps its place (QA, #5331)');
        const topRow = () => ev(`(() => { const v = document.getElementById('agentic-panel-team-artifacts-view'); const top = v.getBoundingClientRect().top; const r = [...document.querySelectorAll('${ROWS}')].find(e => e.getBoundingClientRect().bottom > top + 40); return r ? r.querySelector('.team-artifact-name').textContent.trim() : null; })()`);
        await ev(`(() => { const v = document.getElementById('agentic-panel-team-artifacts-view'); const row = document.querySelectorAll('${ROWS}')[150]; v.scrollTop += row.getBoundingClientRect().top - v.getBoundingClientRect().top; return v.scrollTop; })()`);
        await ev(`${framesJs(3)}`); // the scroll event of a scroll the person made has long been delivered when they press Refresh
        const before = await topRow(), st0 = await ev(`document.getElementById('agentic-panel-team-artifacts-view').scrollTop`);
        await ev(`window.__delay = 300; document.querySelector('.team-artifacts-refresh').click(); true`); // the answer takes 300 ms, so frames pass while the list loads (#6509: the refresh emptied the list, the box shrank and the browser clamped the scroll to 0)
        await waitFor(`document.getElementById('agentic-panel-team-artifacts-view').getAttribute('aria-busy') === 'false' && document.querySelectorAll('${ROWS}').length === ${N}`, 'the redraw', 15000);
        await ev(`${framesJs(4)}`);
        const after = await topRow(), st1 = await ev(`document.getElementById('agentic-panel-team-artifacts-view').scrollTop`);
        check(`scrolled to ${before} (scrollTop ${Math.round(st0)}), a refresh leaves ${after} on top (scrollTop ${Math.round(st1)})`, st0 > 1000 && before === after && Math.abs(st1 - st0) < 2);
        await ev(`window.__delay = 0; true`);

        console.log('3. a small list is drawn whole at once');
        await ev(`window.__items = window.__items.slice(0, 12); true`); await ev(`${framesJs(2)}`);
        await ev(sample); await ev(`document.querySelector('.team-artifacts-refresh').click(); true`);
        await waitFor(`document.querySelectorAll('${ROWS}').length === 12`, 'a short list', 6000);
        await ev(`${framesJs(3)}`);
        const small = (await ev(`window.__counts`)).filter(n => n > 0 && n !== N); // (the 300 still on screen when the refresh was pressed is not part of it)
        check(`12 rows are drawn whole, at once (${JSON.stringify(small)})`, small.length === 1 && small[0] === 12);
        const failed = results.filter(r => !r).length;
        console.log(`${results.length - failed} of ${results.length} checks passed`);
        await client.close(); process.exit(failed ? 1 : 0);
    } catch (e) { console.error('FAIL', e && e.stack || e); await client.close().catch(() => {}); process.exit(1); }
})();
