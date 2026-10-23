// A suite that awaits Page.loadEventFired() after Page.navigate() hung to the runner's timeout when the load event came BEFORE the second call asked for it (#6195): the event is delivered once, to whoever is listening, so
// a page that loads in the time between the two calls is never heard, and nothing in the suite times out on its own. Windows' CDP runs lost a suite that way now and then (a loaded, idle page and an idle server in the
// evidence, update_channel_browser, hub_pages_theme_browser, phone_pass_130_browser, list_bars_themes_browser). helpers/keep_page_visible.js (preloaded into every suite) now remembers a load that came after a navigate
// and hands it to the next loadEventFired(); this suite is the proof: a load heard late is still heard, a reload that is not a navigate is still waited for, and a second navigate waits for its own load.
// Run with CDP_KEEP_VISIBLE=0 (the preload off) the first case hangs: that is the failing-first.
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const sleep = ms => new Promise(r => setTimeout(r, ms));
const heard = (p, ms) => Promise.race([p.then(() => true), sleep(ms).then(() => false)]);

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    try {
        await Page.enable(); await Runtime.enable();
        // 1. the load came while the suite did something else before asking
        await Page.navigate({ url: BASE + '/static/theme_init.js' });
        await sleep(500); // sleep-ok: the page has loaded before loadEventFired is asked for: the race itself
        check('a load that came before loadEventFired() was asked for is still heard', await heard(Page.loadEventFired(), 4000));

        // 2. the ordinary order
        await Page.navigate({ url: BASE + '/static/theme_init.js?b' });
        check('navigate, then loadEventFired() at once, is heard', await heard(Page.loadEventFired(), 8000));

        // 3. a reload that is not a navigate is waited for, not answered by the earlier load
        await Page.navigate({ url: BASE + '/static/theme_init.js?c' });
        await sleep(1500); // sleep-ok: the navigation's load has come, nobody asked for it, and it is too old to be the one a later wait means
        await Runtime.evaluate({ expression: 'window.__marker = 1; true' });
        await Runtime.evaluate({ expression: 'setTimeout(() => location.reload(), 300); true' });
        const t0 = Date.now();
        const reloaded = await heard(Page.loadEventFired(), 8000);
        const marker = (await Runtime.evaluate({ expression: 'typeof window.__marker', returnByValue: true })).result.value;
        check('after a navigate nobody waited for, a later reload is waited for (its own load, not the old one)', reloaded && marker === 'undefined' && Date.now() - t0 >= 250, `reloaded ${reloaded}, marker ${marker}, ${Date.now() - t0} ms`);

        // 4. two navigates in a row, each waited for
        await Page.navigate({ url: BASE + '/static/theme_init.js?d' });
        const first = await heard(Page.loadEventFired(), 8000);
        await Page.navigate({ url: BASE + '/static/theme_init.js?e' });
        await sleep(500); // sleep-ok: the second page loads before it is asked for
        const second = await heard(Page.loadEventFired(), 4000);
        check('two navigates, each waited for (the second one late), both heard', first && second);

        // 5. a reload whose load came before it was asked for is heard too (`await Page.reload(); await Page.loadEventFired();`, #6528)
        await Page.navigate({ url: BASE + '/static/theme_init.js?f' }); await Page.loadEventFired();
        await Page.reload({});
        await sleep(500); // sleep-ok: the reloaded page has loaded before loadEventFired is asked for: the race itself
        check('a reload\'s load that came before loadEventFired() was asked for is still heard', await heard(Page.loadEventFired(), 4000));
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} navigation race checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
