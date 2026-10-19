// requestJson and sendJson (#5162), driven directly in the page with a stubbed fetch: requestJson returns the parsed answer, sends a body as JSON only when there is one, reads an
// empty answer as {}, throws for a refusal with the server's own reason or "HTTP <status>" and the status and body on the error, and throws for a 200 whose body is not JSON; sendJson never throws for a
// refusal, reads a bad body as {}, carries Retry-After, sends a DELETE with no body; password_ui re-exports the same functions. Drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, ev, PRELOAD, install } = await connect();
    const ok = name => console.log(`   ✓ ${name}`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        const out = await ev(`(async () => {
            const S = window.__coralStatic || '/static/';
            const api = await import(S + 'api.js'); const pw = await import(S + 'password_ui.js');
            const real = window.fetch; const seen = [];
            let reply;
            window.fetch = async (url, init) => { seen.push({ url, method: init && init.method, headers: init && init.headers, body: init && init.body }); return reply(); };
            const res = (status, text, headers) => () => new Response(text, { status, headers: headers || {} });
            const r = {};
            try {
                reply = res(200, '{"a":1}'); r.get = await api.requestJson('GET', '/x'); r.getInit = seen[0];
                reply = res(200, '{"ok":true}'); r.post = await api.requestJson('POST', '/y', { ids: [1] }); r.postInit = seen[1];
                reply = res(200, ''); r.empty = await api.requestJson('POST', '/z');
                reply = res(200, 'not json'); try { await api.requestJson('GET', '/bad'); r.badOk = 'resolved'; } catch (e) { r.badOk = 'threw'; }
                reply = res(500, '{"error":"boom"}'); try { await api.requestJson('GET', '/e'); } catch (e) { r.err500 = [e.message, e.status, e.data]; }
                reply = res(502, '<html>bad gateway</html>'); try { await api.requestJson('GET', '/e'); } catch (e) { r.err502 = [e.message, e.status, e.data]; }
                reply = res(409, '{}'); try { await api.requestJson('POST', '/e', {}); } catch (e) { r.err409 = [e.message, e.status]; }
                reply = res(404, '{"error":42}'); try { await api.requestJson('GET', '/e'); } catch (e) { r.errNum = e.message; }
                reply = res(429, '{"error":"slow down"}', { 'Retry-After': '7' }); r.send429 = await api.sendJson('POST', '/s', { a: 1 });
                reply = res(200, 'nope'); r.sendBad = await api.sendJson('PUT', '/s', { a: 1 });
                seen.length = 0; reply = res(200, '{}'); await api.sendJson('DELETE', '/d'); r.del = seen[0];
                reply = res(200, '{"k":1}'); r.post2 = await api.postJson('/p', { b: 2 });
                r.same = pw.sendJson === api.sendJson && pw.postJson === api.postJson;
            } finally { window.fetch = real; }
            return r; })()`);
        assert.deepEqual(out.get, { a: 1 }); assert.equal(out.getInit.method, 'GET'); assert.equal(out.getInit.body, undefined); assert.equal(out.getInit.headers, undefined);
        ok('requestJson returns the parsed answer; a GET sends no body and no content type');
        assert.deepEqual(out.post, { ok: true }); assert.deepEqual(out.postInit.headers, { 'Content-Type': 'application/json' }); assert.equal(out.postInit.body, '{"ids":[1]}');
        assert.deepEqual(out.empty, {}); assert.equal(out.badOk, 'threw');
        ok('a body is sent as JSON; an empty answer is {}; a 200 that is not JSON throws');
        assert.deepEqual(out.err500, ['boom', 500, { error: 'boom' }]); assert.deepEqual(out.err502, ['HTTP 502', 502, {}]); assert.deepEqual(out.err409, ['HTTP 409', 409]); assert.equal(out.errNum, 'HTTP 404');
        ok('a refusal throws the server\'s reason or "HTTP <status>", with .status and .data; a non-string error is ignored');
        assert.deepEqual(out.send429, { ok: false, status: 429, data: { error: 'slow down' }, retryAfter: '7' }); assert.deepEqual(out.sendBad, { ok: true, status: 200, data: {}, retryAfter: null });
        assert.equal(out.del.method, 'DELETE'); assert.equal(out.del.body, undefined); assert.deepEqual(out.post2, { ok: true, status: 200, data: { k: 1 }, retryAfter: null });
        assert.equal(out.same, true);
        ok('sendJson never throws for a refusal, reads a bad body as {}, carries Retry-After, sends a DELETE bare; password_ui re-exports the same functions');
        console.log('request_json: ok');
        await client.close(); process.exit(0);
    } catch (e) { console.error('FAIL', e && e.stack || e); await client.close().catch(() => {}); process.exit(1); }
})();
