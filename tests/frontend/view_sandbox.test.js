// Unit test for the custom-view sandbox and its read-only bridge (jsdom, no browser):
//   node view_sandbox.test.js
const assert = require('assert');
const path = require('path');
const { pathToFileURL } = require('url');
const { JSDOM } = require('jsdom');

(async () => {
    const dir = path.resolve(__dirname, '../../coral-go/pkg/frontend/static');
    const dom = new JSDOM('<!doctype html><body></body>', { url: 'http://127.0.0.1:8420/' });
    for (const k of ['window', 'document']) Object.defineProperty(globalThis, k, { value: dom.window[k], configurable: true, writable: true });
    const m = await import(pathToFileURL(path.join(dir, 'view_sandbox.js')).href);

    // ── allowlist: only GETs for listed read endpoints ──
    const ok = ['/api/sessions/live', '/api/sessions/live/claude-abc', '/api/sessions/live/claude-abc/tasks', '/api/sessions/live/a/events/counts?session_id=x-1',
        '/api/sessions/history', '/api/sessions/history/abc-1/git', '/api/board/projects', '/api/board/dev-board/messages/all?limit=50', '/api/board/dev/subscribers', '/api/tags'];
    for (const p of ok) assert.ok(m.isAllowedViewRequest('GET', p), p);
    assert.ok(m.isAllowedViewRequest('get', '/api/tags'));
    const no = ['/api/settings', '/api/system/api-key', '/api/system/status', '/api/views', '/api/views/1', '/api/agent/ui', '/api/license/status', '/api/webhooks',
        '/api/sessions/launch', '/api/sessions/live/a/send', '/api/sessions/live/a/keys', '/api/sessions/live/a/file-content?filepath=x', '/api/filesystem/list?path=/',
        '/api/sessions/live/../settings', '/api/sessions/live/%2e%2e/x', '/api/sessions//live', '/api/sessions/live/a/tasks?x=<script>', '/api/sessions/live/a/tasks?' + 'a'.repeat(300),
        'https://evil.example/api/tags', '//evil.example/api/tags', 'api/tags', '/api/tags/', '/api/tags#x', '/x/api/tags', '', null, undefined, 42, '/api/sessions/live/a/capture/x'];
    for (const p of no) assert.ok(!m.isAllowedViewRequest('GET', p), String(p));
    for (const method of ['POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS']) assert.ok(!m.isAllowedViewRequest(method, '/api/tags'), method);

    // ── tiers: a view created by an automated caller gets the basic list only ──
    for (const p of ['/api/sessions/live', '/api/sessions/live/a/tasks', '/api/sessions/history', '/api/board/projects', '/api/tags']) {
        assert.ok(m.isAllowedViewRequest('GET', p, 'api'), 'api tier: ' + p);
        assert.ok(m.isAllowedViewRequest('GET', p, 'ui'), 'ui tier: ' + p);
    }
    for (const p of ['/api/sessions/live/a/capture', '/api/sessions/live/a/files', '/api/sessions/live/a/git', '/api/sessions/live/a/notes', '/api/sessions/live/a/events',
        '/api/sessions/live/a/events/counts', '/api/sessions/live/a/info', '/api/sessions/live/a', '/api/sessions/history/x/tasks', '/api/sessions/history/x/git',
        '/api/board/dev/messages/all', '/api/board/dev/subscribers']) {
        assert.ok(!m.isAllowedViewRequest('GET', p, 'api'), 'api tier must not read ' + p);
        assert.ok(m.isAllowedViewRequest('GET', p, 'ui'), 'ui tier reads ' + p);
    }
    assert.ok(m.isAllowedViewRequest('GET', '/api/tags'), 'the default tier is ui');
    assert.ok(/agents, their tasks/.test(m.API_VIEW_SEES));

    // ── frame and document ──
    const fr = dom.window.document.createElement('iframe');
    m.lockViewFrame(fr);
    assert.strictEqual(fr.getAttribute('sandbox'), 'allow-scripts');
    assert.ok(!fr.getAttribute('sandbox').includes('same-origin'));
    assert.strictEqual(fr.getAttribute('csp'), "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; form-action 'none'; base-uri 'none'");
    const doc = m.viewDocument('<h1>x</h1>', { agentName: 'a</script><script>alert(1)', sessionId: 's' });
    assert.ok(doc.startsWith('<!doctype html><meta http-equiv="Content-Security-Policy" content="default-src \'none\'; script-src \'unsafe-inline\''));
    assert.ok(doc.includes('<h1>x</h1>'));
    assert.ok(!doc.includes('a</script><script>alert(1)'), 'context cannot break out of the prelude script');
    assert.ok(doc.includes('window.coral') && doc.includes('window.fetch'));

    // ── host frame: its CSP is what refuses a view's navigation; one load only; relays only bridge messages ──
    const host = m.viewHostDocument('<h1>x</h1><script>1<\/script></body>');
    assert.ok(host.startsWith('<!doctype html><meta http-equiv="Content-Security-Policy" content="default-src \'none\'; script-src \'unsafe-inline\'; style-src \'unsafe-inline\'; frame-src about:;'));
    assert.ok(host.includes("sandbox', 'allow-scripts'") && !host.includes('allow-same-origin'));
    assert.ok(host.includes('loads > 1') && host.includes('coral-view-navigated'));
    assert.ok(host.split('<script>').length === 2 && !host.includes('</script><script>'), 'the view html cannot close the host script');

    // ── bridge ──
    const posted = [];
    const frame = { contentWindow: { postMessage: (msg) => posted.push(msg) } };
    const other = { postMessage() {} };
    const calls = [];
    const text = JSON.stringify([{ session_id: 'a' }]);
    const detach = m.attachViewBridge(frame, { fetchFn: async (u, i) => { calls.push({ u, i }); return { headers: { get: () => null }, text: async () => text, status: 200 }; } });
    const send = (data, source = frame.contentWindow) => { dom.window.dispatchEvent(new dom.window.MessageEvent('message', { data, source })); return new Promise(r => setTimeout(r, 15)); };

    await send({ type: 'coral-view-get', id: 1, path: '/api/sessions/live' });
    assert.deepStrictEqual(posted.pop(), { type: 'coral-view-response', id: 1, status: 200, body: text });
    assert.strictEqual(calls[0].i.method, 'GET');
    assert.strictEqual(calls[0].i.cache, 'no-store');

    // refused: not allowlisted, writes smuggled in, other senders, malformed
    calls.length = 0;
    await send({ type: 'coral-view-get', id: 2, path: '/api/settings' });
    assert.match(posted.pop().error, /not available to views/);
    await send({ type: 'coral-view-get', id: 3, path: '/api/sessions/live', method: 'POST' });
    assert.strictEqual(posted.pop().status, 200); // the method field is ignored: the bridge only ever sends GET
    assert.strictEqual(calls.pop().i.method, 'GET');
    await send({ type: 'coral-view-get', id: 4, path: '/api/sessions/live' }, other);
    assert.strictEqual(posted.length, 0, 'a message from another window is ignored');
    await send({ type: 'coral-view-get', id: 'x', path: '/api/sessions/live' });
    await send({ type: 'something-else', id: 5, path: '/api/sessions/live' });
    await send('nope');
    assert.strictEqual(posted.length, 0);
    assert.strictEqual(calls.length, 0);

    // size cap by header and by body
    const big = m.MAX_BRIDGE_RESPONSE_BYTES + 1;
    m.attachViewBridge(frame, { fetchFn: async () => ({ headers: { get: () => String(big) }, text: async () => 'x', status: 200 }) });
    await send({ type: 'coral-view-get', id: 6, path: '/api/tags' });
    assert.ok(posted.some(p => p.id === 6 && /too large/.test(p.error)));
    posted.length = 0;
    m.attachViewBridge(frame, { fetchFn: async () => ({ headers: { get: () => null }, text: async () => 'x'.repeat(big), status: 200 }) });
    await send({ type: 'coral-view-get', id: 7, path: '/api/tags' });
    assert.ok(posted.some(p => p.id === 7 && /too large/.test(p.error)));

    // api tier: refused paths are not fetched; navigation notice reaches the callback only from the frame
    let navigated = 0; const fetched = [];
    m.attachViewBridge(frame, { tier: 'api', onNavigated: () => navigated++, fetchFn: async (u) => { fetched.push(u); return { headers: { get: () => null }, text: async () => '[]', status: 200 }; } });
    posted.length = 0;
    await send({ type: 'coral-view-get', id: 9, path: '/api/sessions/live/a/capture' });
    assert.ok(posted.some(p => p.id === 9 && /not available/.test(p.error)));
    assert.ok(!fetched.includes('/api/sessions/live/a/capture'));
    await send({ type: 'coral-view-get', id: 10, path: '/api/sessions/live/a/tasks' });
    assert.ok(fetched.includes('/api/sessions/live/a/tasks'));
    await send({ type: 'coral-view-navigated' });
    assert.strictEqual(navigated, 1);
    await send({ type: 'coral-view-navigated' }, other);
    assert.strictEqual(navigated, 1, 'only the frame itself can report a navigation');

    // detach stops answering
    posted.length = 0; detach();
    await send({ type: 'coral-view-get', id: 8, path: '/api/tags' });
    assert.ok(!posted.some(p => p.id === 8 && p.body === text));

    console.log('view_sandbox.test.js: ok');
})().catch(e => { console.error(e); process.exit(1); });
