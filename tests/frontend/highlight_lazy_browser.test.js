// highlight.js loads on first use (#3117):  run via run_cdp_suites.mjs
// A cold dashboard requests no highlight.js and defines no window.hljs. The first
// fenced code block with a language renders as plain code at once and is coloured
// when highlight.js arrives (fetched once, through the same sanitizer); a block
// without a language never loads it. If the load fails the code stays plain and
// readable, and the next user of it (Docs) loads it again. Highlighting keeps
// markup inside code as text.
const assert = require('node:assert/strict');
const { safePaused } = require('./helpers/paused');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const sleep = ms => new Promise(r => setTimeout(r, ms));

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Network, Fetch } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
        return r.result.value;
    };
    const waitFor = async (expr, what, ms = 8000) => {
        for (const t = Date.now(); Date.now() - t < ms; await sleep(100)) if (await ev(expr)) return;
        throw new Error('timed out waiting for ' + what);
    };
    const seen = [];      // highlight.js requests (file name)
    let failNext = false; // fail the next highlight.min.js request
    Network.requestWillBeSent(e => { const m = /\/vendor\/hljs\/([^?]+)/.exec(e.request.url); if (m) seen.push(m[1]); });
    safePaused(client, async e => {
        if (failNext && e.request.url.includes('/vendor/hljs/highlight.min.js')) { failNext = false; return Fetch.failRequest({ requestId: e.requestId, errorReason: 'Failed' }); }
        return Fetch.continueRequest({ requestId: e.requestId });
    });
    const fresh = async () => {
        seen.length = 0;
        await Page.navigate({ url: 'about:blank' }); await Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await waitFor(`typeof window.selectLiveSession === 'function'`, 'the dashboard');
        await ev(`(async () => { window.U = await import((window.__coralStatic || '/static/') + 'utils.js'); })()`);
    };
    // Render markdown the way chat does and put it on the page.
    const show = (md, id) => ev(`(() => {
        const d = document.createElement('div'); d.id = ${JSON.stringify(id)}; d.className = 'chat-prose';
        d.innerHTML = U.renderMarkdown(${JSON.stringify(md)}); document.body.appendChild(d);
        const c = d.querySelector('pre code'); return { cls: c.className, spans: c.querySelectorAll('span').length, text: c.textContent };
    })()`);
    const JS = '```js\nconst x = 1; // <img src=x onerror="window.__pwned=1">\n```';
    try {
        await Promise.all([Page.enable(), Runtime.enable(), Network.enable()]);
        await Network.setBypassServiceWorker({ bypass: true });
        await Network.setCacheDisabled({ cacheDisabled: true });
        await Fetch.enable({ patterns: [{ urlPattern: '*/vendor/hljs/*', requestStage: 'Request' }] });

        // 1. Cold load: nothing of highlight.js.
        await fresh();
        // Startup finished (#3118): whatever the page loads up front has been requested by now.
        await require('./helpers/wait').coralReady(ev);
        await require('./helpers/wait').nextFrame(ev);
        assert.deepEqual(seen, [], 'no highlight.js request on a cold load');
        assert.equal(await ev(`typeof window.hljs`), 'undefined', 'window.hljs is not defined');
        assert.equal(await ev(`[...document.querySelectorAll('script[src], link[href]')].some(e => /vendor\\/hljs\\//.test(e.src || e.href))`), false, 'no tag for it in the page');
        console.log('PASS cold load: no highlight.js');

        // 2. A block without a language never loads it.
        const plainOnly = await show('```\nno language here\n```', 'md-plain');
        await sleep(600); // sleep-ok: proves a negative, no request follows within this window
        assert.deepEqual(seen, [], 'a block without a language does not load highlight.js');
        assert.equal(plainOnly.text, 'no language here\n');
        console.log('PASS a block without a language: still nothing loaded');

        // 3. The first block with a language: plain at once, coloured when it lands.
        const first = await show(JS, 'md-js');
        assert.equal(first.spans, 0, 'rendered plain first');
        assert.match(first.cls, /language-js/);
        await waitFor(`!!document.querySelector('#md-js pre code.hljs span.hljs-keyword')`, 'the block to be coloured');
        assert.deepEqual([...new Set(seen)].sort(), ['github-dark.min.css', 'highlight.min.js'], 'script and theme fetched');
        assert.equal(seen.filter(x => x === 'highlight.min.js').length, 1, 'once');
        const after = await ev(`(() => { const c = document.querySelector('#md-js pre code'); return { text: c.textContent, img: !!c.querySelector('img'), pwned: !!window.__pwned }; })()`);
        assert.equal(after.text, first.text, 'the code text is unchanged by highlighting');
        assert.deepEqual([after.img, after.pwned], [false, false], 'markup inside code stays text');
        // Later renders are coloured synchronously, with no further request.
        const later = await show('```python\ndef f():\n    return 1\n```', 'md-py');
        assert.ok(later.spans > 0 && /hljs/.test(later.cls), 'a later block is coloured as it renders');
        assert.equal(seen.filter(x => x === 'highlight.min.js').length, 1, 'still fetched once');
        console.log('PASS first block: plain, then coloured (one fetch); later blocks coloured at once; markup stays text');

        // 4. A failed load leaves code plain and readable; the next user loads it again.
        await fresh();
        failNext = true;
        const failed = await show(JS, 'md-fail');
        for (let i = 0; i < 40 && !seen.includes('highlight.min.js'); i++) await sleep(100);
        assert.ok(seen.includes('highlight.min.js'), 'the block asked for highlight.js');
        await sleep(800); // sleep-ok: proves a negative, the block stays plain after the failed load
        const stillPlain = await ev(`(() => { const c = document.querySelector('#md-fail pre code'); return { spans: c.querySelectorAll('span').length, text: c.textContent, hljs: typeof window.hljs }; })()`);
        assert.equal(stillPlain.hljs, 'undefined', 'highlight.js did not load');
        assert.equal(stillPlain.spans, 0, 'the block stays plain');
        assert.equal(stillPlain.text, failed.text, 'and readable');
        assert.ok(!(await ev(`[...document.querySelectorAll('script[src*="vendor/hljs/"]')].length`)), 'the failed tag is removed');
        const retried = await ev(`(async () => {
            const { loadHighlight } = await import((window.__coralStatic || '/static/') + 'vendor_loader.js');
            const h = await loadHighlight(); return typeof h.highlightElement === 'function';
        })()`);
        assert.ok(retried, 'the next load (Docs, a file preview) fetches it again and works');
        assert.equal(seen.filter(x => x === 'highlight.min.js').length, 2, 'fetched again after the failure');
        console.log('PASS failed load: code stays plain; the next use loads it');
    } finally {
        await Fetch.disable().catch(() => {});
        await client.close();
    }
})().catch(e => {
    console.error('FAIL ' + e.message);
    process.exitCode = 1;
});
