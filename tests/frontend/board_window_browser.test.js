// The board view appends what a poll brings and keeps a window of about 300 messages (#4492): message rows already shown keep
// their nodes, a task-queue run and a sender's group continue across the poll, and the result is the same as a whole render.
// Pinned at the bottom it follows new posts; reading higher up nothing moves. Over 800 new posts the DOM stays flat; the oldest
// go only while you are at the bottom, and scrolling up brings them back without moving what you read. Select mode, a search
// jump to an old message and a deleted message (gone from the rows shown, at the bottom) still work. The board's messages are
// served by the test (CDP Fetch) from a model, so ids, totals and deletes are exact.
'use strict';

const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const B = 'window-4492';
const Q = 'Coral Task Queue';
const AGENTS = ['Lead Developer', 'QA Engineer', 'Orchestrator', 'Backend Dev'];
const model = [];
let nextId = 1;
function add(n, { agent, queue = false } = {}) {
    for (let i = 0; i < n; i++) {
        const id = nextId++;
        const created_at = new Date(Date.UTC(2026, 9, 17, 8, 0, 0) + id * 1000).toISOString();
        if (queue) model.push({ id, subscriber_id: Q, is_system: true, job_title: Q, content: `[Task #${id} claimed by QA Engineer] item ${id}`, created_at });
        else {
            const who = agent || AGENTS[Math.floor(id / 3) % AGENTS.length];
            model.push({ id, subscriber_id: who.toLowerCase().replace(/ /g, '-'), job_title: who, content: `message ${id} with **bold**\nand a second line`, created_at });
        }
    }
}
function remove(id) { const i = model.findIndex(m => m.id === id); assert.ok(i >= 0, `model has ${id}`); model.splice(i, 1); }
function page(q) {
    const limit = Number(q.get('limit')) || 50;
    let rows;
    if (q.has('after_id')) rows = model.filter(m => m.id > Number(q.get('after_id'))).slice(0, limit);
    else if (q.has('before_id')) rows = model.filter(m => m.id < Number(q.get('before_id'))).slice(-limit);
    else rows = model.slice(-limit);
    const before = rows.length ? model.filter(m => m.id < rows[0].id).length : 0;
    return { messages: rows, total: model.length, before_count: before };
}

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation, Fetch } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 600));
        return r.result.value;
    };
    const { waitFor, waitStable, coralReady } = require('./helpers/wait').bind(ev);
    const { framesJs } = require('./helpers/wait');
    const MB = `(await import((window.__coralStatic || '/static/') + 'message_board.js'))`;
    const C = `document.getElementById('mb-messages')`;
    const ROWS = `[...${C}.querySelectorAll(':scope > .mb-message, :scope > .mb-queue-run')]`;
    const shown = () => ev(`(() => { const t = ${C}.querySelector('.mb-message-count'); return t ? Number(t.textContent.match(/Showing (\\d+)/)[1]) : 0; })()`);
    const atBottom = () => ev(`(() => { const c = ${C}; return c.scrollHeight - c.scrollTop - c.clientHeight < 50; })()`);
    const toBottom = () => ev(`(() => { const c = ${C}; c.scrollTop = c.scrollHeight; return ${framesJs(2)}; })()`);
    // The app's own 10 s poll can be in flight when the test asks for one (then that call returns at once): ask until it shows.
    const pollUntil = async (expr, what) => {
        for (let i = 0; i < 40; i++) {
            await ev(`window.__coral.pollBoardNow()`);
            if (await ev(expr)) return;
            await ev(`new Promise(r => setTimeout(() => r(true), 50))`); // sleep-ok: between poll attempts, see above
        }
        throw new Error(`poll: ${what}`);
    };
    // A whole render of the same messages, to compare with what the polls built (cancelSelectMode renders everything).
    const rowsHtml = `${ROWS}.map(r => r.outerHTML).join('')`;
    const wholeRender = () => ev(`(async () => { ${MB}.cancelSelectMode(); return ${rowsHtml}; })()`);

    try {
        add(997); add(3, { queue: true }); // the newest rows are a task-queue run
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
        await Fetch.enable({ patterns: [{ urlPattern: `*/api/board/${B}/messages/all*` }] });
        Fetch.requestPaused(({ requestId, request }) => {
            const body = Buffer.from(JSON.stringify(page(new URL(request.url).searchParams))).toString('base64');
            Fetch.fulfillRequest({ requestId, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }], body }).catch(() => {});
        });
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: `${BASE}/board/${B}` });
        await loaded;
        await coralReady();
        await waitFor(`(${C}.querySelector('.mb-message-count') || {}).textContent?.includes('Showing 50 of 1000')`, 'the latest page');
        await waitFor(`(() => { const c = ${C}; return c.scrollHeight > c.clientHeight * 2 && c.scrollHeight - c.scrollTop - c.clientHeight < 50; })()`, 'a scrolled board, at the bottom');

        console.log('A poll appends; a task-queue run continues across it; rows shown keep their nodes');
        await ev(`${ROWS}.forEach(r => { r.__old = true; }); true`);
        const before = await ev(`${ROWS}.length`);
        add(2, { queue: true }); add(3, { agent: 'QA Engineer' });
        await pollUntil(`${C}.querySelector('[data-message-id="1005"]')`, 'message 1005');
        const a = await ev(`(() => { const rows = ${ROWS}; const runs = rows.filter(r => r.matches('.mb-queue-run'));
            const last = runs[runs.length - 1];
            return { n: rows.length, kept: rows.filter(r => r.__old).length, runLines: last.querySelectorAll('.mb-queue-line').length, runKey: last.dataset.queueRun,
                tail: rows.slice(-3).map(r => r.className.match(/mb-message-(first|grouped)/)[1]), runIsNew: !last.__old }; })()`);
        assert.equal(a.runLines, 5, 'the run gained the two new lines (998-1002 in one row, not two)');
        assert.equal(a.runKey, '998');
        assert.equal(a.n, before + 3, 'three new message rows');
        assert.equal(a.kept, before - 1, 'every row shown keeps its node except the run at the end, drawn again');
        assert.ok(a.runIsNew);
        assert.deepEqual(a.tail, ['first', 'grouped', 'grouped'], 'a new sender group starts after the run');
        assert.ok(await atBottom(), 'pinned at the bottom: follows the new posts');
        const appended = await ev(rowsHtml);
        assert.equal(appended, await wholeRender(), 'the same as a whole render');

        console.log('A sender group continues across a poll');
        add(2, { agent: 'QA Engineer' });
        await pollUntil(`${C}.querySelector('[data-message-id="1007"]')`, 'message 1007');
        assert.deepEqual(await ev(`${ROWS}.slice(-2).map(r => r.classList.contains('mb-message-grouped'))`), [true, true]);
        assert.equal(await ev(rowsHtml), await wholeRender());

        console.log('Reading higher up: a poll moves nothing');
        await ev(`(() => { const c = ${C}; c.scrollTop = Math.round(c.scrollHeight / 2); return true; })()`);
        await ev(framesJs(2));
        const anchor = await ev(`(() => { const c = ${C}; const top = c.getBoundingClientRect().top + 100;
            const row = ${ROWS}.find(r => r.getBoundingClientRect().bottom > top); return { id: row.dataset.messageId, y: row.getBoundingClientRect().top, st: c.scrollTop }; })()`);
        add(3);
        await pollUntil(`${C}.querySelector('[data-message-id="1010"]')`, 'message 1010');
        const after = await ev(`({ y: ${C}.querySelector('[data-message-id="${anchor.id}"]').getBoundingClientRect().top, st: ${C}.scrollTop })`);
        assert.equal(after.st, anchor.st, 'the scroll position is kept');
        assert.ok(Math.abs(after.y - anchor.y) < 1, `the row read stays put (${anchor.y} -> ${after.y})`);

        console.log('800 new posts at the bottom: about 300 kept, the DOM stays flat');
        await toBottom();
        const series = [];
        for (let i = 0; i < 20; i++) {
            add(40);
            const want = nextId - 1;
            await pollUntil(`${C}.querySelector('[data-message-id="${want}"]')`, `message ${want}`);
            series.push(await ev(`({ nodes: ${C}.getElementsByTagName('*').length, rows: ${ROWS}.length, bottom: ${C}.scrollHeight - ${C}.scrollTop - ${C}.clientHeight < 50 })`));
        }
        const s = await shown();
        assert.ok(s >= 300 && s <= 350, `messages loaded stay between 300 and 350 (${s})`);
        assert.ok(series.every(x => x.rows <= 351 && x.bottom), `rows bounded and still pinned: ${JSON.stringify(series.map(x => x.rows))}`);
        // Without the window the count grows by about 400 nodes a poll for good (8,000 more here); with it, once the oldest start
        // to go, it swings within one page.
        const late = series.slice(10).map(x => x.nodes);
        assert.ok(Math.max(...late) <= Math.min(...late) * 1.2 && series[19].nodes <= series[9].nodes * 1.2, `DOM nodes flat: ${JSON.stringify(series.map(x => x.nodes))}`);
        const header = await ev(`({ earlier: ${C}.querySelector('.mb-load-earlier')?.textContent.replace(/\\s+/g, ' ').trim(), count: ${C}.querySelector('.mb-message-count').textContent.replace(/\\s+/g, ' ').trim(),
            firstClass: ${ROWS}[0].className })`);
        assert.equal(header.count, `Showing ${s} of ${model.length} messages`);
        assert.equal(header.earlier, `Load 50 earlier messages (${model.length - s} remaining)`);
        assert.ok(/mb-message-first|mb-queue-run/.test(header.firstClass), 'the first row shown starts its group');
        assert.equal(await ev(rowsHtml), await wholeRender(), 'after trimming, still the same as a whole render');
        await toBottom();

        console.log('Not trimmed while reading higher up; trimmed again back at the bottom');
        await ev(`(() => { const c = ${C}; c.scrollTop = Math.round(c.scrollHeight / 2); return true; })()`);
        add(60);
        await pollUntil(`${C}.querySelector('[data-message-id="${nextId - 1}"]')`, 'the 60 new posts (two polls)');
        assert.ok(await shown() > 350, `nothing dropped while reading higher up (${await shown()})`);
        await toBottom();
        add(1);
        await pollUntil(`${C}.querySelector('[data-message-id="${nextId - 1}"]')`, 'one more');
        assert.ok(await shown() <= 301, `trimmed back at the bottom (${await shown()})`);

        console.log('Scrolling to the top loads the page before, and what you read stays put');
        const top0 = await ev(`(() => { const c = ${C}; c.scrollTop = 0; const r = ${ROWS}[0]; return { id: r.dataset.messageId || r.dataset.queueRun, y: r.getBoundingClientRect().top, n: Number(c.querySelector('.mb-message-count').textContent.match(/Showing (\\d+)/)[1]) }; })()`);
        await waitFor(`Number(${C}.querySelector('.mb-message-count').textContent.match(/Showing (\\d+)/)[1]) >= ${top0.n + 50}`, 'the earlier page, from scrolling');
        const top1 = await ev(`(() => { const r = ${C}.querySelector('[data-message-id="${top0.id}"], [data-queue-run="${top0.id}"]'); return { y: r.getBoundingClientRect().top, st: ${C}.scrollTop }; })()`);
        assert.ok(Math.abs(top1.y - top0.y) < 2 && top1.st > 0, `the row that was at the top stays there (${top0.y} -> ${top1.y})`);

        console.log('Select mode: new rows can be selected, a selection stays, nothing is dropped');
        await toBottom();
        await ev(`(async () => { ${MB}.toggleSelectMode(); return true; })()`);
        const picked = await ev(`(() => { const cb = ${ROWS}.slice(-1)[0].querySelector('.mb-select-checkbox'); cb.click(); return Number(cb.dataset.msgId); })()`);
        await toBottom(); // the select bar takes room from the board: at the bottom again, where trimming would happen
        assert.ok(await atBottom());
        const n0 = await shown();
        add(70);
        await pollUntil(`${C}.querySelector('[data-message-id="${nextId - 1}"]')`, 'the 70 new posts in select mode');
        const sel = await ev(`(() => { const p = ${C}.querySelector('[data-message-id="${picked}"]');
            return { checked: p.querySelector('.mb-select-checkbox').checked, cls: p.classList.contains('mb-message-selected'),
                newBoxes: ${ROWS}.slice(-70).every(r => r.querySelector('.mb-select-checkbox')), runs: ${C}.querySelectorAll('.mb-queue-run').length,
                count: document.querySelector('.mb-select-count')?.textContent }; })()`);
        assert.ok(sel.checked && sel.cls, 'the selected message stays selected');
        assert.ok(sel.newBoxes, 'new rows have their checkbox');
        assert.equal(sel.runs, 0, 'select mode shows every line');
        assert.equal(sel.count, '1 selected');
        assert.equal(await shown(), n0 + 70, 'nothing dropped while selecting');
        await ev(`(async () => { ${MB}.cancelSelectMode(); return true; })()`);
        await toBottom();

        console.log('A search jump to an old message, and a poll after it');
        await ev(`(async () => { ${MB}.selectBoardProject(${JSON.stringify(B)}, 200); return true; })()`);
        await waitFor(`${C}.querySelector('.chat-search-target[data-message-id="200"]')`, 'the jump to message 200', { timeout: 30000 });
        add(5);
        await pollUntil(`${C}.querySelector('[data-message-id="${nextId - 1}"]')`, 'new posts after the jump');
        assert.ok(await ev(`!!${C}.querySelector('[data-message-id="200"]')`), 'the message jumped to is still there after a poll');

        console.log('A deleted message goes from the rows shown (at the bottom; when you come back down)');
        await waitStable(`${C}.scrollTop`, 'the jump\'s smooth scroll', 200); // a scroll set during it would be undone
        await toBottom();
        await waitFor(`(() => { const c = ${C}; return c.scrollHeight - c.scrollTop - c.clientHeight < 50; })()`, 'at the bottom');
        let gone = nextId - 3;
        remove(gone);
        await pollUntil(`!${C}.querySelector('[data-message-id="${gone}"]')`, `deleted ${gone} gone`);
        assert.ok(await ev(`(${C}.querySelector('.mb-message-count').textContent).includes('of ${model.length} messages')`));
        gone = nextId - 2; remove(gone); add(1); // deleted and arrived in one tick: the total alone doesn't change
        await pollUntil(`!${C}.querySelector('[data-message-id="${gone}"]') && !!${C}.querySelector('[data-message-id="${nextId - 1}"]')`, `deleted ${gone} gone, the new one in`);
        await ev(`(() => { const c = ${C}; c.scrollTop = Math.round(c.scrollHeight / 2); return true; })()`);
        await ev(framesJs(2));
        const st = await ev(`${C}.scrollTop`);
        gone = nextId - 1; remove(gone);
        await ev(`window.__coral.pollBoardNow()`); await ev(`window.__coral.pollBoardNow()`);
        assert.ok(await ev(`!!${C}.querySelector('[data-message-id="${gone}"]')`), 'reading higher up: not reloaded under you');
        assert.equal(await ev(`${C}.scrollTop`), st);
        await toBottom();
        await pollUntil(`!${C}.querySelector('[data-message-id="${gone}"]')`, `deleted ${gone} gone once at the bottom`);

        console.log('board_window: ok');
        await client.close();
        process.exit(0);
    } catch (e) {
        console.error('FAIL', e && e.stack || e);
        try { await client.close(); } catch { /* closing */ }
        process.exit(1);
    }
})();
