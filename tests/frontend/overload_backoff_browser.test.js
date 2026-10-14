// The dashboard backs off when Coral is overloaded (#3403). In the real page, with the watch
// app.js installs: a 503 with Retry-After pauses every pollEvery poller for that window, then
// they resume; another answer ends a pause early; an action the server refuses as busy says
// so ("Coral is busy…", or for a remote machine "That machine is busy or unreachable…");
// a 200 meanwhile doesn't cut a pause short; a remote machine's 503 pauses only that
// machine; a file load is retried once.
// /api/health and /api/qa-overload/* are answered by a stub installed before the app runs.
//
// Expects CORAL_TEST_URL + CDP_PORT from tests/frontend/run.sh. Drives Chrome over CDP
// (chrome-remote-interface). Exit 0 / 1.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const S = `(window.__coralStatic||'/static/')`;
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        // Under the app's own fetch wrappers: they see these answers as the server's.
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            const real = window.fetch.bind(window);
            window.__qa = { status: 0, retryAfter: '', calls: 0 };
            window.fetch = (url, opts) => {
                const p = new URL(String(url), location.href).pathname;
                if (p.includes('/qa-overload/')) {
                    window.__qa.calls++;
                    const st = window.__qa.status || 200;
                    if (st !== 200) window.__qa.status = window.__qa.once ? 0 : st;
                    return Promise.resolve(new Response('{"error":"busy"}', { status: st, headers: window.__qa.retryAfter ? { 'Retry-After': window.__qa.retryAfter } : {} }));
                }
                return real(url, opts);
            };
        })();` });
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await coralReady();

        // A probe poller, as every app poller now is (pollEvery), ticking every 100 ms.
        await ev(`(async () => { const o = await import(${S}+'overload.js'); window.__o = o; window.__ticks = 0;
            window.__probe = o.pollEvery(() => { window.__ticks++; }, 100); return true; })()`);
        await waitFor(`window.__ticks >= 3`, 'the probe ticking');

        console.log('1. A 503 with Retry-After: 1 pauses every poller for the window, then they resume');
        await ev(`(async () => { window.__qa.status = 503; window.__qa.retryAfter = '1'; await fetch('/api/qa-overload/poll'); window.__at = window.__ticks; return true; })()`);
        assert.equal(await ev(`window.__o.isPaused()`), true, 'paused');
        const pausedMs = await ev(`window.__o.pausedForMs()`);
        assert.ok(pausedMs >= 950 && pausedMs <= 1300, 'the first pause is the Retry-After 1 s the server gave, with jitter x1.0-1.25 (#3430): ' + pausedMs);
        await new Promise(r => setTimeout(r, 600)); // sleep-ok: proving the probe stays silent inside the (1-1.25 s) window
        assert.equal(await ev(`window.__ticks - window.__at`), 0, 'no ticks while paused');
        await waitFor(`!window.__o.isPaused()`, 'the window to end', { timeout: 5000 });
        await waitFor(`window.__ticks - window.__at >= 3`, 'the probe resuming');
        console.log('   ✓ silent for the window, then resumed');

        console.log('2. A cheap call answering fine meanwhile does not cut the pause short');
        await ev(`(async () => { window.__o._resetOverloadForTests(); window.__qa.status = 503; window.__qa.retryAfter = '20'; await fetch('/api/qa-overload/poll'); return true; })()`);
        const before = await ev(`window.__o.pausedForMs()`);
        await ev(`(async () => { window.__qa.status = 200; await fetch('/api/qa-overload/poll'); await fetch('/api/health'); return true; })()`);
        assert.equal(await ev(`window.__o.isPaused()`), true, 'still paused after 200s');
        assert.ok((await ev(`window.__o.pausedForMs()`)) > before - 2000, 'not shortened');
        await ev(`window.__o._resetOverloadForTests(); true`);
        console.log('   ✓ a 200 during the pause neither ends nor shortens it');

        console.log('3. An action refused as busy says so; a GET poll does not toast');
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); true`);
        await ev(`(async () => { window.__qa.status = 503; window.__qa.retryAfter = '3'; await fetch('/api/qa-overload/poll'); return true; })()`);
        const quiet = await ev(`[...document.querySelectorAll('.toast .toast-message')].map(t => t.textContent)`);
        assert.deepEqual(quiet, [], 'a background GET never toasts');
        await ev(`(async () => { window.__qa.status = 503; window.__qa.retryAfter = '3'; await fetch('/api/qa-overload/act', { method: 'POST', body: '{}' }); return true; })()`);
        await waitFor(`[...document.querySelectorAll('.toast .toast-message')].some(t => /Coral is busy and didn't do that/.test(t.textContent))`, 'the busy message');
        console.log('   ✓ the busy message for the action, none for the poll');

        console.log('3b. Through a hub, a remote machine refusing a write pauses only that machine, and names it');
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); window.__o._resetOverloadForTests(); true`);
        await ev(`(async () => { window.__qa.status = 503; window.__qa.retryAfter = '3'; await fetch('/api/remote/box-b/api/qa-overload/act', { method: 'POST', body: '{}' }); return true; })()`);
        assert.equal(await ev(`window.__o.isPaused('box-b')`), true, 'that machine pauses');
        assert.equal(await ev(`window.__o.isPaused('')`), false, 'Coral itself does not');
        await waitFor(`[...document.querySelectorAll('.toast .toast-message')].some(t => /That machine is busy or unreachable/.test(t.textContent))`, 'the machine message');
        await ev(`window.__o._resetOverloadForTests(); true`);
        console.log('   ✓ machine-scoped pause and message');

        console.log('4. A file load is retried once after the pause');
        const r = await ev(`(async () => { window.__o._resetOverloadForTests(); window.__qa.calls = 0; window.__qa.once = true; window.__qa.status = 503; window.__qa.retryAfter = '0';
            const t0 = Date.now(); const resp = await window.__o.retryOnce(() => fetch('/api/qa-overload/file')); window.__qa.once = false;
            return { status: resp.status, calls: window.__qa.calls, waited: Date.now() - t0 }; })()`);
        assert.equal(r.status, 200, 'the retry got through'); assert.equal(r.calls, 2, 'exactly one retry');
        assert.ok(r.waited >= 1900, 'after the pause: ' + r.waited);
        console.log('   ✓ one retry, after the pause');
        await ev(`clearInterval(window.__probe); window.__o._resetOverloadForTests(); true`);
        console.log('PASS overload_backoff_browser');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
