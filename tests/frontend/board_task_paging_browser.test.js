// The board task list is paged (#3269), against the real isolated server (no stubbed API):
//   - a load shows every open task and the newest 50 done tasks, as light items (no body,
//     no workflow); the counts are the server's totals, not the loaded rows;
//   - "Load more" pages the older done tasks, once each, and a poll keeps them;
//   - the detail view and Edit use the full task, fetched on open (never the preview);
//   - a fetch that never answers ends at the timeout, and a failed refresh keeps the list.
//
// Expects CORAL_TEST_URL + CDP_PORT from tests/frontend/run.sh. Exit 0 / 1.

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const CDP_PORT = parseInt(process.env.CDP_PORT || '9222', 10);

const results = [];
function check(label, cond, detail) {
    const v = cond ? 'PASS' : 'FAIL';
    results.push(v);
    console.log(`[${v}] ${label}${detail !== undefined ? ' — ' + detail : ''}`);
}

const BOARD = 'paging-' + Date.now().toString(36);
const DONE = 60;
const MARKER = 'END-OF-THE-FULL-BODY';
const LONG_BODY = 'Start of the body. ' + 'x'.repeat(900) + ' ' + MARKER;

// Requests for a board in window.__hang never answer (they reject when aborted, as fetch does).
const HANG = `
  window.__hang = new Set();
  (function(){ const Real = window.WebSocket; function Dead(u){ this.url=String(u); this.readyState=0; } Dead.prototype.send=function(){}; Dead.prototype.close=function(){}; Dead.prototype.addEventListener=function(){}; Dead.prototype.removeEventListener=function(){};
    function W(u,p){ if (/\\/ws\\//.test(String(u))) return new Dead(u); return p===undefined? new Real(u): new Real(u,p); } W.prototype=Real.prototype; W.CONNECTING=0; W.OPEN=1; W.CLOSING=2; W.CLOSED=3; window.WebSocket=W; })();
  window.__origFetch = window.fetch.bind(window);
  window.fetch = (url, opts) => { const m = String(url).match(/\\/api\\/board\\/([^/?]+)\\/tasks/);
    if (m && window.__hang.has(decodeURIComponent(m[1]))) return new Promise((_, reject) => {
      const s = opts && opts.signal; if (s) s.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError'))); });
    return window.__origFetch(url, opts); };`;

async function run() {
    const client = await CDP({ port: CDP_PORT });
    const { Page, Runtime } = client;
    await Promise.all([Page.enable(), Runtime.enable()]);
    await Page.addScriptToEvaluateOnNewDocument({ source: HANG });
    const exceptions = [];
    Runtime.exceptionThrown(e => exceptions.push((((e.exceptionDetails || {}).exception || {}).description || 'exception').slice(0, 200)));
    const ev = async (x) => { const r = await Runtime.evaluate({ expression: x, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 300)); return r.result.value; };
    const { waitFor } = require('./helpers/wait').bind(ev);
    const MODS = `const tasks = await import((window.__coralStatic||'/static/')+'tasks.js'); const { state } = await import((window.__coralStatic||'/static/')+'state.js');`;
    const listed = () => ev(`(async () => { ${MODS} return (state.currentBoardTasks || []).map(t => ({ id: t.id, status: t.status, hasBody: 'body' in t, hasWorkflow: 'workflow' in t })); })()`);
    const load = () => ev(`(async () => { ${MODS} await tasks.loadBoardTasks(${JSON.stringify(BOARD)}); return true; })()`);
    const dom = () => ev(`({ more: document.querySelector('#board-task-list .board-task-load-more')?.textContent.trim() || null,
        count: document.getElementById('task-bar-count')?.textContent || '',
        hide: document.querySelector('.board-task-hide-toggle')?.textContent.trim() || '' })`);

    try {
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await waitFor(`typeof window._coralSetLiveSessions === 'function'`, 'the app hooks', { timeout: 10000 });

        // A board with 60 done tasks and 3 open ones, through the real API.
        const made = await ev(`(async () => {
            const post = async (path, body) => { const r = await fetch('/api/board/${BOARD}' + path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
                if (!r.ok) throw new Error(path + ': ' + await r.text()); return r.json(); };
            for (let i = 0; i < ${DONE}; i++) {
                const t = await post('/tasks', { title: 'done ' + i, body: 'body ' + i, created_by: 'Operator' });
                await post('/tasks/claim', { subscriber_id: 'worker-' + i, task_id: t.id });
                await post('/tasks/' + t.id + '/complete', { subscriber_id: 'worker-' + i, message: 'finished ' + i });
            }
            const open = [];
            for (let i = 0; i < 3; i++) open.push((await post('/tasks', { title: 'open ' + i, body: ${JSON.stringify(LONG_BODY)}, created_by: 'Operator' })).id);
            return open;
        })()`);
        const openIds = made;
        await ev(`(async () => { ${MODS} tasks.stopBoardTaskPoll(); state.currentSession = null; state.selectedTeam = ${JSON.stringify(BOARD)};
            const sec = document.getElementById('board-tasks-section'); if (sec) sec.style.display = ''; return true; })()`);

        // ── First page ──
        await load();
        let rows = await listed();
        const done = rows.filter(r => r.status === 'completed');
        check('a load lists every open task and the newest 50 done tasks', rows.length === 53 && done.length === 50 && openIds.every(id => rows.some(r => r.id === id)), `${rows.length} rows, ${done.length} done`);
        check('list items are light: no body, no workflow', rows.every(r => !r.hasBody && !r.hasWorkflow));
        let d = await dom();
        check('"Load more" offers the 10 done tasks not loaded', !!d.more && /10 not shown/.test(d.more), JSON.stringify(d.more));
        check('the counts are the board totals (60 done of 63), not the loaded rows', d.count === '60/63' && /\(60\)/.test(d.hide), JSON.stringify(d));

        // ── Load more ──
        await ev(`(async () => { ${MODS} await tasks.loadMoreBoardTasks(); return true; })()`);
        rows = await listed();
        const ids = rows.map(r => r.id);
        check('"Load more" adds the older done tasks, each once', rows.length === 63 && new Set(ids).size === 63, `${rows.length} rows, ${new Set(ids).size} distinct`);
        d = await dom();
        check('no "Load more" once every done task is loaded', d.more === null, JSON.stringify(d.more));

        // ── A poll refreshes the first page and keeps the older pages; a task finished meanwhile moves to page one ──
        await ev(`fetch('/api/board/${BOARD}/tasks/claim', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'late', task_id: ${openIds[2]} }) })
            .then(() => fetch('/api/board/${BOARD}/tasks/${openIds[2]}/complete', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'late', message: 'late' }) })).then(r => r.ok)`);
        await load();
        rows = await listed();
        check('a refresh keeps the loaded older pages, without duplicates', rows.length === 63 && new Set(rows.map(r => r.id)).size === 63, `${rows.length} rows`);
        check('the task finished meanwhile is listed as done', rows.some(r => r.id === openIds[2] && r.status === 'completed'));
        d = await dom();
        check('counts follow (61 done of 63)', d.count === '61/63', d.count);

        // ── Detail and Edit use the full task ──
        await ev(`(async () => { ${MODS} tasks.showTaskDetailModal(${openIds[0]}); return true; })()`);
        await waitFor(`(document.getElementById('task-detail-content')?.textContent || '').includes(${JSON.stringify(MARKER)})`, 'the full body in the detail view', { timeout: 5000 })
            .then(() => check('the detail view shows the full task, fetched on open', true))
            .catch(e => check('the detail view shows the full task, fetched on open', false, e.message));
        await ev(`(async () => { ${MODS} await tasks.enableTaskEditMode(${openIds[1]}); return true; })()`);
        const editBody = await ev(`document.getElementById('task-edit-body')?.value || ''`);
        check('Edit on a task never opened edits the full body, never the preview', editBody === LONG_BODY, `${editBody.length} chars`);
        await ev(`(async () => { ${MODS} tasks.cancelTaskEdit && tasks.cancelTaskEdit(${openIds[1]}); tasks.hideTaskDetailModal(); return true; })()`);

        // ── A fetch that never answers ends at the timeout; a failed refresh keeps the list ──
        await ev(`(async () => { ${MODS} tasks.boardTaskFetchHooks.timeoutMs = 300; window.__hang.add(${JSON.stringify(BOARD)}); return true; })()`);
        const t0 = Date.now();
        await load();
        const took = Date.now() - t0;
        rows = await listed();
        check('a hung fetch ends at the timeout', took < 3000, `${took} ms`);
        check('a failed refresh keeps the last good list', rows.length === 63, `${rows.length} rows`);
        await ev(`(async () => { ${MODS} tasks.boardTaskFetchHooks.timeoutMs = 20000; window.__hang.clear(); return true; })()`);

        check('no page exceptions', exceptions.length === 0, exceptions.join(' | '));
    } catch (e) {
        check('test ran', false, e.stack || String(e));
    } finally {
        await client.close();
    }
    const failed = results.filter(r => r === 'FAIL').length;
    console.log(`\n${results.length - failed}/${results.length} checks passed`);
    process.exit(failed ? 1 : 0);
}

run().catch(e => { console.error(e); process.exit(1); });
