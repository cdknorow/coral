// The dashboard's URL carries only real state (#3529): a plain load, and switching views, leave
// no ?page=1&page_size=50; an old URL with them is cleaned; a filtered history search keeps its
// filters and page in the URL (without page_size), and they survive a reload.
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
            const now = await ev('location.search + location.hash');
            if (now === last) return now;
            last = now;
        }
        return last;
    };
    try {
        await open(BASE + '/');
        await settled();
        check('a plain dashboard load has no query', (await ev('location.search')) === '', await ev('location.href'));
        await ev(`switchNavTab('history'); true`);
        await settled();
        check('switching to Chats adds no ?page=1&page_size=50', (await ev('location.search')) === '', await ev('location.href'));
        await ev(`switchNavTab('agents'); true`);

        await open(BASE + '/?page=1&page_size=50#agents');
        await settled();
        check('an old URL with page=1&page_size=50 is cleaned (the hash kept)', (await ev('location.search')) === '' && (await ev('location.hash')) === '#agents', await ev('location.href'));

        await open(BASE + '/?view=x&q=battle&type=agent&page=2&page_size=50#history');
        await settled();
        const p = await ev(`Object.fromEntries(new URLSearchParams(location.search))`);
        check('a filtered search keeps its filters and page, not page_size, nor drops other keys',
            p.q === 'battle' && p.type === 'agent' && p.page === '2' && !('page_size' in p) && p.view === 'x', JSON.stringify(p));
        const loaded = Page.loadEventFired();
        await Page.reload({});
        await loaded;
        await settled();
        const r = await ev(`Object.fromEntries(new URLSearchParams(location.search))`);
        check('and survives a reload', r.q === 'battle' && r.type === 'agent' && r.page === '2', JSON.stringify(r));
    } catch (e) {
        check('no error', false, e.message);
    } finally {
        await client.close();
    }
    const failed = results.filter(x => !x).length;
    console.log(`\n${results.length - failed}/${results.length} checks passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(2); });
