// The Tasks panel's search (#4744 stage 2), two things the main suite does not pin: a poll leaves a filtered list the person has paged further (a refresh would drop them back to the first page), and
// the bar on a phone (above the list, at touch size, nothing scrolling sideways). Real isolated server; 130 tasks all matching "delta".
//
// Expects CORAL_TEST_URL + CDP_PORT from the runner. Exit 0 / 1.
'use strict';
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const CDP_PORT = parseInt(process.env.CDP_PORT || '9222', 10);

const results = [];
function check(label, cond, detail) {
    const v = cond ? 'PASS' : 'FAIL';
    results.push(v);
    console.log(`[${v}] ${label}${detail !== undefined ? ' — ' + detail : ''}`);
}
const BOARD = 'poll-' + Date.now().toString(36);
const N = 130;

async function run() {
    const client = await CDP({ port: CDP_PORT });
    const { Page, Runtime, Input, Emulation } = client;
    await Promise.all([Page.enable(), Runtime.enable()]);
    const ev = async (x) => { const r = await Runtime.evaluate({ expression: x, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 300)); return r.result.value; };
    const { waitFor } = require('./helpers/wait').bind(ev);
    const MODS = `const tasks = await import((window.__coralStatic||'/static/')+'tasks.js'); const { state } = await import((window.__coralStatic||'/static/')+'state.js');`;
    const rows = () => ev(`document.querySelectorAll('#board-task-list .board-task-item:not(.board-task-header)').length`);
    const load = () => ev(`(async () => { ${MODS} await tasks.loadBoardTasks(${JSON.stringify(BOARD)}); return true; })()`);
    try {
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await waitFor(`typeof window._coralSetLiveSessions === 'function'`, 'the app hooks', { timeout: 10000 });
        await ev(`localStorage.removeItem('coral-task-filters:${BOARD}'); true`);
        await ev(`(async () => {
            const post = async (path, body) => { const r = await fetch('/api/board/${BOARD}' + path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
                if (!r.ok) throw new Error(path + ': ' + await r.text()); return r.json(); };
            for (let i = 0; i < ${N}; i++) await post('/tasks', { title: 'delta ' + i, created_by: 'Operator' });
            return true; })()`);
        await ev(`(async () => { ${MODS} tasks.stopBoardTaskPoll(); state.currentSession = { type: 'live', name: 'tk-bot', session_id: 'tk-bot-id', board_project: ${JSON.stringify(BOARD)} }; state.selectedTeam = null; return true; })()`);
        await ev(`(async () => { const base = (window.__coralStatic || '/static/'); const { showView } = await import(base + 'utils.js'); showView('live-session-view');
            const side = document.getElementById('agentic-state'); side.classList.remove('collapsed'); side.style.width = '680px'; return true; })()`);
        await waitFor(`document.getElementById('agentic-tab-tasks').getClientRects().length > 0`, 'the dock is open', { timeout: 8000 });
        await ev(`document.getElementById('agentic-tab-tasks').click(); true`);
        await load();

        console.log('A poll leaves a filtered list that was paged further');
        await ev(`document.getElementById('tk-search').focus(); true`); await Input.insertText({ text: 'delta' });
        await waitFor(`document.querySelector('#board-task-bar .list-bar-count').textContent === '${N} of ${N}'`, 'the count of the search', { timeout: 8000 });
        const first = await rows();
        check('the first page is shown (not all 130)', first > 0 && first < N, String(first));
        for (let i = 0; i < 3 && (await rows()) < N; i++) {
            const before = await rows();
            await ev(`document.querySelector('#board-task-list .board-task-load-more').click(); true`);
            await waitFor(`document.querySelectorAll('#board-task-list .board-task-item:not(.board-task-header)').length > ${before}`, 'the next page', { timeout: 8000 });
        }
        const paged = await rows();
        check('Load more paged past the first page (over 50 rows)', paged > 50, String(paged));
        const idsBefore = await ev(`[...document.querySelectorAll('#board-task-list .board-task-item:not(.board-task-header)')].map(r => r.querySelector('.board-task-id').textContent).join()`);
        await load(); await load(); // what the 10 s poll does
        await ev(`new Promise(r => setTimeout(r, 400))`); // sleep-ok: a refresh, if it came, would redraw the list within this
        check('a poll leaves the list as it was (same rows, same order)', (await rows()) === paged && (await ev(`[...document.querySelectorAll('#board-task-list .board-task-item:not(.board-task-header)')].map(r => r.querySelector('.board-task-id').textContent).join()`)) === idsBefore, `${await rows()} vs ${paged}`);
        check('and the count is still the whole match', (await ev(`document.querySelector('#board-task-bar .list-bar-count').textContent`)) === `${N} of ${N}`);

        console.log('A phone');
        await ev(`document.querySelector('#board-task-bar .list-bar-chips .list-bar-chip-all, #board-task-bar .list-bar-clear-all') && true; true`);
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 800, deviceScaleFactor: 2, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
        await ev(`(() => { const side = document.getElementById('agentic-state'); side.style.width = ''; side.classList.add('mobile-panel-overlay'); side.classList.remove('collapsed'); return true; })()`);
        await ev(`new Promise(r => setTimeout(r, 500))`); // sleep-ok: the layout settles after the resize
        const g = await ev(`(() => { const b = document.getElementById('board-task-bar'); const r = e => { const x = b.querySelector(e); return x ? Math.round(x.getBoundingClientRect().height) : null; }; const l = document.getElementById('board-task-list');
            return { shown: b.getClientRects().length > 0, filters: r('.list-bar-filter-btn'), sort: r('.list-bar-sort select'), search: r('.list-bar-search'), above: b.getBoundingClientRect().bottom <= l.getBoundingClientRect().top + 1,
              bar: b.scrollWidth - b.clientWidth, list: l.scrollWidth - l.clientWidth, page: document.documentElement.scrollWidth - document.documentElement.clientWidth }; })()`);
        check('the bar shows above the list on a phone', g.shown && g.above, JSON.stringify(g));
        check('its controls are 44px targets', g.filters >= 44 && g.sort >= 44 && g.search >= 44, JSON.stringify([g.filters, g.sort, g.search]));
        check('nothing scrolls sideways', g.bar <= 1 && g.list <= 1 && g.page <= 0, JSON.stringify([g.bar, g.list, g.page]));
    } finally { await client.close(); }
    const failed = results.filter(r => r === 'FAIL').length;
    console.log(failed ? `\n${failed} FAILED` : '\nALL PASS');
    process.exit(failed ? 1 : 0);
}
run().catch(e => { console.error(e); process.exit(1); });
