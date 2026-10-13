// On-demand editor and terminal libraries (#3083 phase 1): the page no longer loads CodeMirror or xterm.js;
// they load once, the first time a file editor or a terminal is shown. Pins: a cold dashboard requests
// neither; each vendor URL is fetched once however many callers ask; a switch during the load attaches only
// the agent still selected; a failed load (also a failed addon) shows an error with Retry in the pane, and
// Retry fetches again and then works; the loader only loads its fixed vendor files.
//
// Vendor requests are watched (Network) and failed or held (Fetch) through CDP.
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

// A fake terminal socket (records which agent it was opened for) and stubbed file endpoints.
const PAGE_PRELOAD = `(() => {
    window.__termSockets = [];
    const Real = window.WebSocket;
    function FakeTerm(u) { this.url = String(u); this.readyState = 0; window.__termSockets.push(this.url); setTimeout(() => { this.readyState = 1; this.onopen && this.onopen({}); }, 0); }
    FakeTerm.prototype.send = function () {}; FakeTerm.prototype.close = function () { this.readyState = 3; };
    FakeTerm.prototype.addEventListener = function () {}; FakeTerm.prototype.removeEventListener = function () {};
    function W(u, p) { return /\\/ws\\/terminal/.test(String(u)) ? new FakeTerm(u) : (p === undefined ? new Real(u) : new Real(u, p)); }
    W.prototype = Real.prototype; W.CONNECTING = 0; W.OPEN = 1; W.CLOSING = 2; W.CLOSED = 3;
    window.WebSocket = W;
    const inner = window.fetch;
    window.fetch = (url, opts) => {
        const p = new URL(String(url), location.origin).pathname;
        if (/\\/(file-content|file-original)$/.test(p)) return Promise.resolve(new Response(JSON.stringify({ content: 'line one\\nline two\\n' }), { status: 200, headers: { 'content-type': 'application/json' } }));
        return inner(url, opts);
    };
})();`;

(async () => {
    const { client, Page, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const { Network, Fetch } = client;

    // Every vendor request the page makes, and a per-file plan: 'fail' (once each), 'hold', or pass.
    const seen = [];
    // Condition waits (#3118). quietSeen(): no new intercepted request for 300 ms.
    const { waitStable, nextFrame, coralReady } = require('./helpers/wait').bind(ev);
    const quietSeen = async (ms = 300) => { let n = seen.length, since = Date.now(); const end = since + 8000;
        while (Date.now() - since < ms && Date.now() < end) { await sleep(20); if (seen.length !== n) { n = seen.length; since = Date.now(); } } };
    let plan = {};
    const held = [];
    Network.requestWillBeSent(e => { if (/\/vendor\//.test(e.request.url)) seen.push(e.request.url.replace(/^.*\/vendor\//, '')); });
    Fetch.requestPaused(async e => {
        const rel = e.request.url.replace(/^.*\/vendor\//, '');
        const p = plan[rel];
        if (p === 'fail') { delete plan[rel]; return Fetch.failRequest({ requestId: e.requestId, errorReason: 'Failed' }); }
        if (p === 'hold') { held.push(e.requestId); return; }
        return Fetch.continueRequest({ requestId: e.requestId });
    });
    const release = async () => { while (held.length) await Fetch.continueRequest({ requestId: held.shift() }); };
    const count = rel => seen.filter(x => x === rel).length;
    const fresh = async () => {
        seen.length = 0; plan = {};
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.setItem('coral-live-view-mode', 'terminal'); window.__m.add('t-a', 'A', 3, { sleeping: false }); window.__m.add('t-b', 'B', 3, { sleeping: false }); true`);
    };
    const select = name => ev(`(async () => { window.selectLiveSession(${JSON.stringify(name)}, 'claude', ${JSON.stringify(name + '-id')}, 'local'); return true; })()`);
    const pane = () => ev(`(() => {
        const c = document.getElementById('xterm-container') || document.querySelector('.xterm')?.parentElement;
        const n = c && c.querySelector(':scope > .vendor-load-notice');
        return { xterm: !!document.querySelector('.xterm'), notice: n ? { cls: n.className, role: n.getAttribute('role'), text: n.textContent.trim(), retry: !!n.querySelector('button') } : null, sockets: window.__termSockets.slice() };
    })()`);
    const XTERM = ['xterm/xterm.min.css', 'xterm/xterm.min.js', 'xterm/addon-fit.min.js', 'xterm/addon-web-links.min.js', 'xterm/addon-unicode-graphemes.min.js', 'xterm/addon-canvas.min.js'];

    try {
        await Promise.all([Page.enable(), Network.enable()]);
        await Network.setCacheDisabled({ cacheDisabled: true });
        await Network.setBypassServiceWorker({ bypass: true });
        await Fetch.enable({ patterns: [{ urlPattern: '*/vendor/xterm/*', requestStage: 'Request' }, { urlPattern: '*/vendor/codemirror/*', requestStage: 'Request' }] });
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: PAGE_PRELOAD });

        console.log('1. A cold dashboard loads neither the editor nor the terminal');
        await fresh();
        await coralReady(); await quietSeen(); // startup done and no vendor request follows
        assert.deepEqual(seen.filter(x => /^(xterm|codemirror)\//.test(x)), [], `no xterm/codemirror request on a cold load (${JSON.stringify(seen)})`);
        assert.equal(await ev(`typeof window.Terminal === 'undefined' && !window.CoralCM`), true, 'neither global exists');
        assert.equal(await ev(`[...document.querySelectorAll('script[src], link[href]')].some(e => /vendor\\/(xterm|codemirror)\\//.test(e.src || e.href))`), false, 'no tag for them in the page');
        assert.ok(!seen.some(x => x.startsWith('hljs/')), 'highlight.js is not loaded up front either (#3117)');
        console.log(`   ✓ 0 xterm/codemirror requests (${seen.length} vendor requests: ${[...new Set(seen)].join(', ')})`);

        console.log('2. The first terminal view loads xterm once; switching does not load it again');
        await select('t-a');
        await waitFor(`!!document.querySelector('.xterm')`, 'xterm mounted', 8000);
        for (const rel of XTERM) assert.equal(count(rel), 1, `${rel} fetched once`);
        assert.equal(await ev(`[...document.querySelectorAll('script[src*="vendor/xterm/xterm.min.js"]')].length`), 1, 'one script tag');
        assert.ok(await ev(`getComputedStyle(document.querySelector('.xterm')).position !== 'static' || !!document.querySelector('.xterm-screen')`), 'xterm css applied');
        await select('t-b'); await quietSeen(); await select('t-a'); await quietSeen();
        for (const rel of XTERM) assert.equal(count(rel), 1, `${rel} still fetched once after switching`);
        console.log('   ✓ 6 xterm files, 1 request each; mounted; no refetch on switch');

        console.log('3. A switch during the load attaches only the agent still selected');
        await fresh();
        plan['xterm/xterm.min.js'] = 'hold';
        await select('t-a');
        await waitFor(`!!document.querySelector('.vendor-load-notice')`, "'Loading the terminal…' notice", 3000);
        assert.match((await pane()).notice.text, /Loading the terminal/i);
        await select('t-b');
        await nextFrame();
        delete plan['xterm/xterm.min.js'];
        await release();
        await waitFor(`!!document.querySelector('.xterm')`, 'xterm mounted after release', 8000);
        await waitStable(`(window.__termSockets || []).length`, 'the terminal sockets', 300).catch(() => {}); await quietSeen();
        const sockets = (await pane()).sockets;
        assert.ok(sockets.length >= 1 && sockets.every(u => /t-b/.test(u)), `only t-b's terminal socket opened (${JSON.stringify(sockets)})`);
        assert.equal(count('xterm/xterm.min.js'), 1, 'still one xterm request for both selections');
        console.log(`   ✓ held load, switched A→B, released: sockets ${JSON.stringify(sockets.map(u => u.replace(/^.*\/ws\//, '')))}`);

        console.log('4. A failed terminal load shows Retry, and Retry fetches again and mounts');
        await fresh();
        plan['xterm/xterm.min.js'] = 'fail';
        await select('t-a');
        await waitFor(`!!document.querySelector('.vendor-load-error')`, 'terminal load error', 8000);
        let p = await pane();
        assert.equal(p.notice.cls, 'vendor-load-notice vendor-load-error');
        assert.equal(p.notice.role, 'alert');
        assert.match(p.notice.text, /^The terminal could not load\./);
        assert.ok(p.notice.retry, 'Retry button');
        assert.equal(p.xterm, false, 'no terminal mounted after a failure');
        assert.equal(await ev(`[...document.querySelectorAll('script[src*="vendor/xterm/xterm.min.js"]')].length`), 0, 'the failed tag was removed');
        await ev(`document.querySelector('.vendor-load-error button').click(); true`);
        await waitFor(`!!document.querySelector('.xterm')`, 'xterm mounted after Retry', 8000);
        assert.equal(count('xterm/xterm.min.js'), 2, 'Retry fetched xterm.min.js again (not the rejected promise)');
        assert.equal(await ev(`!!document.querySelector('.vendor-load-error')`), false, 'error notice gone');
        console.log('   ✓ alert + Retry; Retry → 2nd request → mounted');

        console.log('5. A failed addon or stylesheet: Retry fetches only that file and the terminal works');
        for (const [rel, global] of [['xterm/addon-fit.min.js', 'FitAddon'], ['xterm/xterm.min.css', null]]) {
            await fresh();
            plan[rel] = 'fail';
            await select('t-a');
            await waitFor(`!!document.querySelector('.vendor-load-error')`, `terminal load error (${rel})`, 8000);
            assert.equal(await ev(`!!document.querySelector('.xterm')`), false, `${rel}: nothing mounted while it is missing`);
            await ev(`document.querySelector('.vendor-load-error button').click(); true`);
            await waitFor(`!!document.querySelector('.xterm') || !!document.querySelector('.vendor-load-error')`, `mounted or error after Retry (${rel})`, 8000);
            await waitStable(`!!document.querySelector('.xterm') + ':' + !!document.querySelector('.vendor-load-error')`, 'the retry outcome', 300).catch(() => {});
            const st = await ev(`({ global: ${global ? `typeof window.${global} !== 'undefined'` : 'true'}, xterm: !!document.querySelector('.xterm'), error: !!document.querySelector('.vendor-load-error'),
                css: [...document.styleSheets].some(sh => (sh.href || '').includes('vendor/xterm/xterm.min.css')) })`);
            assert.equal(count(rel), 2, `${rel}: Retry fetched it again (${count(rel)})`);
            for (const other of XTERM.filter(x => x !== rel)) assert.equal(count(other), 1, `${rel}: ${other} not refetched`);
            assert.ok(st.global && st.xterm && !st.error && st.css, `${rel}: after Retry the terminal mounted with its css (${JSON.stringify(st)})`);
            console.log(`   ✓ ${rel} fails once → Retry refetches only it → mounted with css`);
        }

        console.log('6. The editor loads once; a failed editor load shows Retry in the preview pane');
        await fresh();
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); true`);
        await select('t-a');
        await quietSeen();
        const n3 = await ev(`(async () => { const v = await import((window.__coralStatic || '/static/') + 'vendor_loader.js'); const r = await Promise.all([v.loadCodeMirror(), v.loadCodeMirror(), v.loadCodeMirror()]); return r.every(x => x === window.CoralCM); })()`);
        assert.ok(n3, 'three concurrent loads resolve to the same CodeMirror');
        assert.equal(count('codemirror/codemirror-bundle.js'), 1, 'one request for three concurrent loads');
        await fresh();
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); true`);
        await select('t-a');
        await quietSeen();
        plan['codemirror/codemirror-bundle.js'] = 'fail';
        await ev(`(async () => { const pp = await import((window.__coralStatic || '/static/') + 'preview_pane.js'); pp.openPreviewTab('src/main.go', 1, { mode: 'edit' }); return true; })()`);
        await waitFor(`!!document.querySelector('#preview-pane .vendor-load-error')`, 'editor load error in the preview pane', 8000);
        const pe = await ev(`(() => { const n = document.querySelector('#preview-pane .vendor-load-error'); return { role: n.getAttribute('role'), text: n.textContent.trim(), retry: !!n.querySelector('button') }; })()`);
        assert.equal(pe.role, 'alert'); assert.match(pe.text, /^The editor could not load\./); assert.ok(pe.retry);
        await ev(`document.querySelector('#preview-pane .vendor-load-error button').click(); true`);
        await waitFor(`!!document.querySelector('#preview-pane .cm-editor')`, 'editor after Retry', 8000);
        assert.equal(count('codemirror/codemirror-bundle.js'), 2, 'Retry fetched the editor again');
        assert.match(await ev(`document.querySelector('#preview-pane .cm-content').textContent`), /line one/, 'the file is in the editor');
        console.log('   ✓ 3 concurrent → 1 request; failure → alert + Retry → 2nd request → editor with the file');

        console.log('7. The loader only loads its fixed vendor files');
        const before = seen.length;
        const refused = await ev(`(async () => {
            const v = await import((window.__coralStatic || '/static/') + 'vendor_loader.js');
            const out = {};
            for (const rel of ['../app.js', 'codemirror/../../app.js', 'xterm/evil.js', '/api/health', 'https://example.com/x.js', 'xterm/xterm.min.js?x=1', '']) {
                try { await v.loadScriptOnce(rel); out[rel] = 'loaded'; } catch (e) { out[rel] = e.message; }
            }
            return out;
        })()`);
        for (const [rel, msg] of Object.entries(refused)) assert.match(msg, /^Not a vendor file/, `refused: ${JSON.stringify(rel)} (${msg})`);
        await quietSeen(); // a request for a refused path would have started by now
        assert.equal(seen.length, before, 'no request made for a refused path');
        console.log(`   ✓ ${Object.keys(refused).length} non-listed paths refused, no requests`);

        console.log('\nAll on-demand vendor checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
