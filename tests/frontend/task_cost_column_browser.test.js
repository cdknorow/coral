// The Tasks list's Cost column in a dock (#4877): the full row needs about 790px, so a narrower dock wraps each row (the title on its own line, Type and Created dropped, #4716), and Cost went with them: a 680px
// dock, which has room for it on the first line, showed no Cost at all. From 560px of list width Cost stays, at the end of the first line and whole inside the list; the sidebar's width (under 560px) keeps the
// wrapped row without it. A cost the agent has run up shows in it (and the header sorts by it). Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const PANELS = require('./helpers/list_bars_panels');
const BASE = require('./test_base').BASE;
const { removeBoards } = require('./helpers/board_cleanup');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    let ctx;
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        ctx = { ev, waitFor, Page, Emulation, client, install, BASE, assert, settle: (c, l) => waitFor(c, l, 10000) };
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
        const tasks = PANELS.find(p => p.key === 'tasks');
        await tasks.prepare(ctx);
        await tasks.show(ctx, { w: 1440, theme: 'dark', phone: false }); // the Tasks tab in the dock, 680px wide
        const measure = () => ev(`(() => { const list = document.getElementById('board-task-list'); const lr = list.getBoundingClientRect();
            const box = e => { const r = e.getBoundingClientRect(); return { shown: getComputedStyle(e).display !== 'none' && r.width > 0, inside: r.left >= lr.left - 1 && r.right <= lr.right + 1, w: Math.round(r.width) }; };
            const head = list.querySelector('.board-task-header .board-task-cost'), row = list.querySelector('.board-task-item:not(.board-task-header) .board-task-cost');
            const first = list.querySelector('.board-task-item:not(.board-task-header)'); const desc = first.querySelector('.board-task-desc').getBoundingClientRect(), cost = row.getBoundingClientRect(), id = first.querySelector('.board-task-id').getBoundingClientRect();
            return { list: Math.round(lr.width), head: box(head), row: box(row), sameLine: Math.abs((cost.top + cost.height / 2) - (id.top + id.height / 2)) < 10, titleBelow: desc.top >= id.bottom - 1, scrolls: list.scrollWidth - list.clientWidth }; })()`);
        const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; require('node:fs').mkdirSync(d, { recursive: true }); require('node:fs').writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
        await ev(`document.querySelector('#board-task-bar .list-bar-toggle') && document.querySelector('#board-task-bar .list-bar-menu:not([hidden])') && document.querySelector('#board-task-bar .list-bar-toggle').click(); true`); // the filter menu out of the way of the rows
        await shot('tasks-dock-680-cost');
        let m = await measure();
        assert.ok(m.list >= 560 && m.list < 790, 'the dock is the width in question: ' + m.list);
        assert.ok(m.head.shown && m.head.inside && m.row.shown && m.row.inside, 'Cost, in the header and in a row, is shown and whole inside the list: ' + JSON.stringify(m));
        assert.ok(m.sameLine, 'on the first line of the row, with the id'); assert.ok(m.titleBelow, 'the title keeps its own line below'); assert.equal(m.scrolls <= 1, true, 'the list does not scroll sideways: ' + m.scrolls);
        await ev(`document.getElementById('agentic-state').style.width = '420px'; true`);
        await waitFor(`document.getElementById('board-task-list').getBoundingClientRect().width < 460`, 'the narrow dock');
        await shot('tasks-dock-420-no-cost');
        m = await measure();
        assert.ok(m.list < 560, 'a narrow list: ' + m.list); assert.equal(m.head.shown || m.row.shown, false, 'Cost stays out of a narrow list (the sidebar): ' + JSON.stringify(m));
        assert.ok(m.scrolls <= 1, 'and it does not scroll sideways');
        console.log('PASS task cost column');
    } finally { await removeBoards(ev, [ctx && ctx.TBOARD, ctx && ctx.BBOARD]); try { await client.close(); } catch (_) {} } // the panel helper's boards (themes-t-, themes-b-)
})().catch(e => { console.error(e); process.exit(1); });
