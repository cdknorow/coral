// The #6195 preload hands a suite a load that came before loadEventFired() was asked for. It must never answer for a page that has not loaded: a page whose body never finishes has no load event, and a wait for it
// must still run out (here a 3 s race), whether or not an earlier page's load went unasked just before.
// Known limit, measured: an unasked load less than a second old also answers a wait for a load the PAGE starts itself (location.assign) before its frameStartedLoading is heard.
const http = require('node:http');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const sleep = ms => new Promise(r => setTimeout(r, ms));
const heard = (p, ms) => Promise.race([p.then(() => true), sleep(ms).then(() => false)]);

(async () => {
    const sockets = new Set();
    const stall = http.createServer((req, res) => { res.writeHead(200, { 'Content-Type': 'text/html' }); res.write('<!doctype html><title>stalled</title><p>the body never ends'); });
    stall.on('connection', s => { sockets.add(s); s.on('close', () => sockets.delete(s)); });
    await new Promise(r => stall.listen(0, '127.0.0.1', r));
    const STALL = `http://127.0.0.1:${stall.address().port}/`;
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    try {
        await Page.enable(); await Runtime.enable();
        // 1. a page that never finishes loading is not heard
        await heard(Page.navigate({ url: STALL }), 5000);
        check('a page whose body never ends has no load to hear', !(await heard(Page.loadEventFired(), 3000)));

        // 2. an earlier page's load, unasked a moment ago, does not answer for the next navigate
        await Page.navigate({ url: BASE + '/static/theme_init.js' });
        await sleep(300); // sleep-ok: the first page has loaded and nobody asked: the load the preload remembers
        await heard(Page.navigate({ url: STALL + '?2' }), 5000);
        check('an unasked load, then a navigate to a page that never loads: the wait is not answered', !(await heard(Page.loadEventFired(), 3000)));

        // 3. a load a wait already heard is not kept for the next wait: the pattern `const loaded = Page.loadEventFired(); await Page.navigate(...); await loaded;` (servers_table's open(): about:blank, then the page)
        const blank = Page.loadEventFired(); await Page.navigate({ url: 'about:blank' }); await blank;
        const next = Page.loadEventFired(); // asked before its navigate, straight after a load that was heard: it is for the page that follows
        await heard(Page.navigate({ url: STALL + '?4' }), 5000);
        check('a wait made before its navigate is not answered by the load the last wait heard', !(await heard(next, 3000)));
    } finally { await client.close(); for (const s of sockets) s.destroy(); stall.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} never-loads checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
