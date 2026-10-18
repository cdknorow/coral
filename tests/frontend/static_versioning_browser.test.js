// Build-versioned static URLs (#3079, #3092):  run via run_cdp_suites.mjs
// The shell loads every stylesheet, script, module and font from one
// /static/b/<build>/ prefix, served immutable; app.js is the one module tag
// (actions.js, which every module imports, is fetched once); a revisit fetches
// none of them from the network. The service worker is bypassed so the HTTP cache
// is what is measured.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const sleep = ms => new Promise(r => setTimeout(r, ms));
const UNVERSIONED_OK = new Set(['/static/sw.js', '/static/panel_host.html', '/static/view_host.html']);

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Network } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
        return r.result.value;
    };
    let responses = [];
    Network.responseReceived(e => responses.push({ url: e.response.url, status: e.response.status, cache: e.response.headers['cache-control'] || e.response.headers['Cache-Control'] || '', fromCache: !!(e.response.fromDiskCache || e.response.fromMemoryCache || e.response.fromPrefetchCache) }));
    const load = async (url = BASE + '/') => {
        responses = [];
        await Page.navigate({ url }); await Page.loadEventFired();
        await ev(`new Promise(r => { const w = () => typeof window.selectLiveSession === 'function' ? r() : setTimeout(w, 50); w(); })`);
        await ev('document.fonts.ready.then(() => true)');
        await sleep(300);
        return responses.filter(r => new URL(r.url).pathname.startsWith('/static/'));
    };
    try {
        await Promise.all([Page.enable(), Runtime.enable(), Network.enable()]);
        await Network.setBypassServiceWorker({ bypass: true });
        await Network.clearBrowserCache();

        // Cold load.
        const cold = await load();
        const appSrc = await ev(`document.querySelector('script[type=module][src$="app.js"]').src`);
        const m = new URL(appSrc).pathname.match(/^\/static\/bb?\/([0-9a-f]{12})\/app\.js$/); // /static/b/ or the bundled family's /static/bb/ (#4228)
        assert.ok(m, 'app.js loads from /static/b/<build>/: ' + appSrc);
        const prefix = new URL(appSrc).pathname.replace(/app\.js$/, '');
        assert.equal(await ev('window.__coralStatic'), prefix, 'the page knows its static prefix');

        const order = await ev(`Array.from(document.querySelectorAll('script[type=module]')).map(s => s.src)`);
        assert.deepEqual(order.map(u => new URL(u).pathname.split('/').pop()), ['app.js'], 'app.js is the one module tag (csp_compat.js is gone, #3645)');
        // The startup graph is one file (the bundled family, #4228): its modules are not requests of their own, and a module that
        // is loaded later (importOnce) is the original file at the same prefix, importing the bundle's instances through its stub.
        const acts = cold.filter(r => r.url.endsWith('/actions.js'));
        assert.equal(acts.length, 0, 'actions.js is inside app.js, not a request: ' + JSON.stringify(acts.map(r => r.url)));
        assert.equal(cold.filter(r => r.url === appSrc).length, 1, 'app.js is fetched once');
        assert.equal(await ev(`(async () => { const a = await import(window.__coralStatic + 'state.js'); const b = await import(window.__coralStatic + 'state.js'); return a.state === b.state && !!a.state; })()`), true, 'a module imported by its URL is the bundle\'s instance');

        // Only the files that must keep a fixed URL (the service worker and the
        // panel and view sandbox hosts) are fetched outside the prefix; images included.
        const unversioned = rs => rs.filter(r => !new URL(r.url).pathname.startsWith(prefix) && !UNVERSIONED_OK.has(new URL(r.url).pathname)).map(r => r.url);
        assert.deepEqual(unversioned(cold), [], 'no static file from an unversioned path');
        // The manifest is linked under the prefix and names its icons relatively,
        // so an install (or the browser's icon fetch) stays under it too.
        const man = await Page.getAppManifest();
        assert.equal(new URL(man.url).pathname, prefix + 'manifest.json', 'the manifest is linked under the prefix');
        const icons = JSON.parse(man.data).icons.map(i => new URL(i.src, man.url).pathname);
        assert.ok(icons.includes(prefix + 'icon-512.png'), 'icon-512.png resolves under the prefix: ' + JSON.stringify(icons));
        assert.deepEqual(icons.filter(p => !p.startsWith(prefix)), [], 'every manifest icon resolves under the prefix');
        const versioned = cold.filter(r => new URL(r.url).pathname.startsWith(prefix));
        assert.ok(versioned.length >= 3 && versioned.length < 40, 'one script and one stylesheet, not 160 files, load under the prefix: ' + versioned.length);
        assert.ok(versioned.some(r => r.url.includes('/vendor/fonts/')), 'fonts (relative url() in fonts.css) load under the prefix');
        for (const r of versioned) assert.match(r.cache, /immutable/, 'immutable: ' + r.url);
        console.log(`PASS cold: ${versioned.length} files under ${prefix}, immutable; app.js once`);

        // A revisit (a new navigation, not a reload): nothing under the prefix
        // comes from the network.
        await Page.navigate({ url: 'about:blank' }); await sleep(200);
        const warm = await load(BASE + '/?visit=2');
        const fetched = warm.filter(r => new URL(r.url).pathname.startsWith(prefix) && !r.fromCache);
        assert.deepEqual(fetched.map(r => r.url), [], 'a revisit fetches no versioned file from the network');
        assert.ok(warm.filter(r => r.status === 304).length === 0, 'and revalidates none');
        assert.deepEqual(unversioned(warm), [], 'nor any unversioned static file');
        console.log(`PASS warm: ${warm.filter(r => new URL(r.url).pathname.startsWith(prefix)).length} versioned files from cache, 0 requests`);

        // With the service worker in control, versioned files never land in Cache
        // Storage (each build would add a copy nothing evicts), and the old cache is gone.
        await Network.setBypassServiceWorker({ bypass: false });
        await load(BASE + '/?sw=1');
        await ev(`navigator.serviceWorker.ready.then(() => true)`);
        await Page.navigate({ url: 'about:blank' }); await sleep(200);
        await load(BASE + '/?sw=2');
        assert.ok(await ev(`!!navigator.serviceWorker.controller`), 'the service worker controls the page');
        await ev(`document.fonts.ready.then(() => true)`); await sleep(300);
        const stored = await ev(`(async () => { const out = []; for (const n of await caches.keys()) { const c = await caches.open(n); for (const r of await c.keys()) out.push(n + ' ' + new URL(r.url).pathname); } return out; })()`);
        assert.deepEqual(stored.filter(e => e.split(' ')[1].startsWith('/static/b/') || e.split(' ')[1].startsWith('/static/bb/')), [], 'no versioned file in Cache Storage');
        assert.ok(!stored.some(e => e.startsWith('coral-v3 ')), 'the pre-versioning cache was dropped');
        console.log(`PASS service worker: ${stored.length} Cache Storage entries, none versioned`);
    } finally { await client.close(); }
})().catch(e => {
    console.error('FAIL ' + e.message + (e.actual !== undefined ? ' | actual ' + JSON.stringify(e.actual).slice(0, 600) + ' | expected ' + JSON.stringify(e.expected).slice(0, 300) : ''));
    process.exitCode = 1;
});
