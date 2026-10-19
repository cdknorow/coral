// The dashboard's URL carries only real state (#3529): a plain load, and switching views, leave
// no ?page=1&page_size=50; an old URL with them is cleaned; a filtered history search keeps its
// filters and page in the URL (without page_size), and they survive a reload. Since #3532 the
// view is the path (/agents, /chats), and old hash links become paths.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
const sleep = ms => new Promise(r => setTimeout(r, ms));

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async e => (await Runtime.evaluate({ expression: e, returnByValue: true, awaitPromise: true })).result.value;
    const open = async url => {
        await Page.enable();
        const loaded = Page.loadEventFired();
        await Page.navigate({ url });
        await loaded;
        for (const end = Date.now() + 10000; Date.now() < end && !(await ev(`typeof window.switchNavTab === 'function'`)); await sleep(100)) { /* booting */ }
    };
    // The URL settles once the history list has loaded (serializeToUrl runs then).
    const settled = async () => {
        let last = null;
        for (const end = Date.now() + 5000; Date.now() < end; await sleep(100)) {
            const now = await ev('location.pathname + location.search + location.hash');
            if (now === last) return now;
            last = now;
        }
        return last;
    };
    try {
        await open(BASE + '/');
        await settled();
        const here = () => ev('location.pathname + location.search + location.hash');
        check('a plain dashboard load is /agents, with no query', (await here()) === '/agents', await here());
        await ev(`switchNavTab('history'); true`);
        await settled();
        check('switching to Chats is /chats, with no ?page=1&page_size=50', (await here()) === '/chats', await here());
        await ev(`switchNavTab('agents'); true`);
        check('and back to Agents is /agents', (await here()) === '/agents', await here());

        await open(BASE + '/?page=1&page_size=50#agents');
        await settled();
        check('an old URL with page=1&page_size=50#agents is cleaned to /agents', (await here()) === '/agents', await here());

        await open(BASE + '/?view=x&q=battle&type=agent&page=2&page_size=50#history');
        await settled();
        const p = await ev(`Object.fromEntries(new URLSearchParams(location.search))`);
        check('an old filtered Chats link is /chats with its filters and page, not page_size or other keys',
            (await ev('location.pathname')) === '/chats' && p.q === 'battle' && p.type === 'agent' && p.page === '2' && !('page_size' in p) && !('view' in p) && (await ev('location.hash')) === '',
            await here());
        const loaded = Page.loadEventFired();
        await Page.reload({});
        await loaded;
        await settled();
        const r = await ev(`Object.fromEntries(new URLSearchParams(location.search))`);
        check('and survives a reload', (await ev('location.pathname')) === '/chats' && r.q === 'battle' && r.type === 'agent' && r.page === '2', JSON.stringify(r));
        check('the search box shows the filter after the reload', (await ev(`(document.getElementById('history-search') || {}).value`)) === 'battle');

        // A page button (window.loadHistoryPage, what data-action="history-page" calls) remembers its page: the URL carries it with the filters, and a new search starts again at page 1.
        const query = async () => { await settled(); return ev(`Object.fromEntries(new URLSearchParams(location.search))`); };
        await ev(`window.loadHistoryPage(3); true`);
        let q = await query();
        check('a page button puts its page in the URL beside the filters', q.page === '3' && q.q === 'battle' && q.type === 'agent', JSON.stringify(q));
        await ev(`(() => { const i = document.getElementById('history-search'); i.value = 'coral'; i.dispatchEvent(new Event('input', { bubbles: true })); i.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })); return true; })()`);
        for (const end = Date.now() + 5000; Date.now() < end && (await query()).q !== 'coral'; await sleep(100)) { /* debounce */ }
        q = await query();
        check('a new search term starts again at page 1 (no page in the URL)', q.q === 'coral' && !('page' in q), JSON.stringify(q));
        await ev(`window.loadHistoryPage(4); true`);
        q = await query();
        check('and the next page button puts page 4 beside the new term', q.page === '4' && q.q === 'coral', JSON.stringify(q));
    } catch (e) {
        check('no error', false, e.message);
    } finally {
        await client.close();
    }
    const failed = results.filter(x => !x).length;
    console.log(`\n${results.length - failed}/${results.length} checks passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(2); });
