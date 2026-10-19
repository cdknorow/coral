// The message board's search marks (#4771), two things the main suite does not pin: a message whose body is several text nodes (paragraphs, bold, a list) has EVERY occurrence marked, not only
// the first node's (the marker's regular expression must not carry its position from one text node to the next), and the tools row fits a phone (First, Latest and Filters at touch size, the
// results line shown, nothing scrolling sideways). Real isolated server; the messages are made here.
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
const BOARD = 'bmarks-' + Date.now().toString(36);
// "alpha" is in 5 places of the message, in several text nodes of ONE message body: plain, bold, italic, a link and the tail.
const BODY = 'alpha plain, **alpha bold**, *alpha italic*, [alpha link](https://example.com/a) and alpha at the end';

async function run() {
    const client = await CDP({ port: CDP_PORT });
    const { Page, Runtime, Input, Emulation } = client;
    await Promise.all([Page.enable(), Runtime.enable()]);
    const ev = async (x) => { const r = await Runtime.evaluate({ expression: x, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 400)); return r.result.value; };
    const { waitFor } = require('./helpers/wait').bind(ev);
const { removeBoards } = require('./helpers/board_cleanup');
    const settle = (cond, label) => waitFor(cond, label, { timeout: 10000 });
    const type = async (sel, text) => { await ev(`document.querySelector(${JSON.stringify(sel)}).focus(); true`); await Input.insertText({ text }); };
    try {
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await waitFor(`typeof window._coralSetLiveSessions === 'function'`, 'the app hooks', { timeout: 10000 });
        await ev(`(async () => {
            const post = async content => { const r = await fetch('/api/board/${BOARD}/messages', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'dev-1', content }) }); if (!r.ok) throw new Error(await r.text()); };
            await post('an earlier message about beta'); await post(${JSON.stringify(BODY)}); await post('a later message about gamma');
            return true; })()`);
        await ev(`window.selectBoardProject(${JSON.stringify(BOARD)}); true`);
        await settle(`document.querySelectorAll('#mb-messages > .mb-message').length >= 3`, 'the messages');

        console.log('Every occurrence in a message of several text nodes is marked');
        await type('#bm-search', 'alpha');
        await settle(`document.querySelector('#mb-tools .mb-search-results')?.textContent === '1 of 1 matches' || /^1 of 1 match/.test(document.querySelector('#mb-tools .mb-search-results')?.textContent || '')`, 'the result');
        await settle(`document.querySelectorAll('#mb-messages mark.mb-hit').length > 0`, 'marks');
        const n = await ev(`document.querySelectorAll('#mb-messages mark.mb-hit').length`);
        check('all 5 occurrences of the search text are marked, in every text node of the message', n === 5, `${n} marked: ${await ev(`document.querySelector('#mb-messages .mb-message-current .mb-message-body').innerHTML.slice(0, 500)`)}`);
        check('and only the matching message carries the match class', (await ev(`document.querySelectorAll('#mb-messages .mb-message-current, #mb-messages .mb-message-match').length`)) === 1);
        check('the marks hold text only (no markup was made from the message)', (await ev(`[...document.querySelectorAll('#mb-messages mark.mb-hit')].every(m => m.children.length === 0 && /^alpha$/i.test(m.textContent))`)));

        console.log('One draw of a message with several matching text nodes marks them all');
        // highlightBoard runs after every draw of the rows; one call on freshly drawn rows must be enough (the board calls it once per draw).
        const once = await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'board_search.js');
            const id = m.boardSearchState().ids[0]; const c = document.createElement('div'); c.id = 'qa-once';
            c.innerHTML = '<div class="mb-message" data-message-id="' + id + '"><div class="mb-message-body"><p>alpha one <strong>alpha two</strong> <em>alpha three</em> and alpha four</p></div></div>';
            document.body.append(c); m.highlightBoard(c); const n = c.querySelectorAll('mark.mb-hit').length; c.remove(); return n; })()`);
        check('four occurrences in four text nodes of one body: four marks from one call', once === 4, `${once} marked`);

        console.log('A phone');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 800, deviceScaleFactor: 2, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
        await ev(`new Promise(r => setTimeout(r, 500))`); // sleep-ok: the layout settles after the resize
        const g = await ev(`(() => { const t = document.getElementById('mb-tools'); const h = sel => { const x = t.querySelector(sel); return x ? Math.round(x.getBoundingClientRect().height) : null; };
            const btns = [...t.querySelectorAll('.list-bar-controls button')].map(b => [b.textContent.trim().replace(/\\s*\\(\\d+\\)$/, ''), Math.round(b.getBoundingClientRect().height)]);
            const st = t.querySelector('.mb-search-status'); const steps = [...t.querySelectorAll('button[data-step], .mb-search-clear')].map(b => Math.round(b.getBoundingClientRect().height));
            return { shown: t.getClientRects().length > 0, search: h('.list-bar-search'), btns, status: !!st && !st.hidden, steps, tools: t.scrollWidth - t.clientWidth, page: document.documentElement.scrollWidth - document.documentElement.clientWidth }; })()`);
        check('the tools row shows with the search, Filters, First and Latest', g.shown && g.btns.map(b => b[0]).join() === 'Filters,First,Latest', JSON.stringify(g.btns));
        check('they are 44px targets, and so are the search box and the result buttons', g.search >= 44 && g.btns.every(b => b[1] >= 44) && g.steps.every(h => h >= 44), JSON.stringify([g.search, g.btns, g.steps]));
        check('the results line shows, and nothing scrolls sideways', g.status && g.tools <= 1 && g.page <= 0, JSON.stringify([g.status, g.tools, g.page]));
    } finally { await removeBoards(ev, [BOARD]); await client.close(); }
    const failed = results.filter(r => r === 'FAIL').length;
    console.log(failed ? `\n${failed} FAILED` : '\nALL PASS');
    process.exit(failed ? 1 : 0);
}
run().catch(e => { console.error(e); process.exit(1); });
