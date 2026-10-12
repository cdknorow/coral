// Unit test (no browser) for the Agent UI panel sandbox helpers and the static host document.
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const { pathToFileURL } = require('url');

(async () => {
    const dir = path.resolve(__dirname, '../../coral-go/pkg/frontend/static');
    globalThis.document = { addEventListener() {}, getElementById() { return null; } };
    const m = await import(pathToFileURL(path.join(dir, 'panel_sandbox.js')).href);

    // The panel event: one shape, validated field by field.
    const ok = { type: 'coral-ui-event', action: 'choose-1', requestId: '7', payload: { a: 1 } };
    assert.ok(m.isPanelEvent(ok));
    assert.ok(m.isPanelEvent({ ...ok, payload: undefined }));
    for (const bad of [null, 'x', 1, [], {}, { ...ok, type: 'coral-view-get' }, { ...ok, type: 'coral-ui-result' }, { ...ok, action: '' }, { ...ok, action: 'a b' }, { ...ok, action: 'a/b' },
        { ...ok, action: 'x'.repeat(65) }, { ...ok, action: 5 }, { ...ok, requestId: '' }, { ...ok, requestId: 'r'.repeat(65) }, { ...ok, requestId: 5 },
        { ...ok, payload: 'x'.repeat(m.MAX_EVENT_BYTES + 1) }]) {
        assert.ok(!m.isPanelEvent(bad), JSON.stringify(bad).slice(0, 80));
    }
    const cyclic = {}; cyclic.self = cyclic;
    assert.ok(!m.isPanelEvent({ ...ok, payload: cyclic }), 'a payload that cannot be serialised is refused');

    // The host and panel policies: nothing can be fetched, nothing can be framed, no navigation target, no form.
    for (const csp of [m.PANEL_CSP, m.PANEL_HOST_CSP]) {
        assert.match(csp, /default-src 'none'/);
        assert.match(csp, /connect-src 'none'/);
        assert.match(csp, /object-src 'none'/);
        assert.match(csp, /base-uri 'none'/);
        assert.match(csp, /form-action 'none'/);
        assert.doesNotMatch(csp, /allow-same-origin/);
        assert.doesNotMatch(csp, /(script|connect|img|frame)-src[^;]*\*|https?:/, 'no wildcard or remote source');
    }
    assert.match(m.PANEL_CSP, /frame-src 'none'/);
    assert.match(m.PANEL_HOST_CSP, /frame-src about:/, 'the host lets only its own child load, which is what stops the panel navigating');
    assert.match(m.PANEL_HOST_CSP, /frame-ancestors 'self'/);
    assert.match(m.PANEL_HOST_CSP, /sandbox allow-scripts$/);
    assert.strictEqual(m.MAX_PANELS, 20);
    assert.ok(m.MAX_PANEL_BYTES >= 2 * 1024 * 1024 && m.MAX_PANEL_BYTES < 2.1 * 1024 * 1024);

    // The static host document is what the hub serves with that policy: check it says the same thing and is self-contained.
    const host = fs.readFileSync(path.join(dir, 'panel_host.html'), 'utf8');
    assert.match(host, /f\.setAttribute\('sandbox', 'allow-scripts'\)/);
    assert.doesNotMatch(host, /allow-same-origin|allow-top-navigation|allow-popups|allow-forms|allow-modals/);
    assert.match(host, /f\.setAttribute\('csp', PANEL_CSP\)/);
    assert.match(host, /frame-src about:/);
    assert.doesNotMatch(host, /<script[^>]*\ssrc=/i, 'no external script: a sandboxed (opaque-origin) host would be refused by the hub CORP');
    assert.doesNotMatch(host, /fetch\(|XMLHttpRequest|WebSocket|importScripts|eval\(|new Function/);
    // the host's inline policy matches the exported one (minus the directives a meta element cannot carry)
    const meta = host.match(/http-equiv="Content-Security-Policy" content="([^"]+)"/)[1];
    assert.strictEqual(meta, m.PANEL_HOST_CSP.replace("; frame-ancestors 'self'; sandbox allow-scripts", ''));
    // it relays exactly two message types, and rebuilds the event
    const relays = [...host.matchAll(/'coral-[a-z-]+'/g)].map(x => x[0]).filter((v, i, a) => a.indexOf(v) === i).sort();
    assert.deepStrictEqual(relays, ["'coral-panel-host-ready'", "'coral-panel-load'", "'coral-panel-navigated'", "'coral-ui-event'", "'coral-ui-result'"]);
    // F1: a host frame that reloads (Expand moves the iframe) announces itself again and gets the cached HTML again.
    {
        const listeners = [], sent = [];
        globalThis.window = { addEventListener(_, f) { listeners.push(f); }, removeEventListener() {} };
        const win = { postMessage(msg) { sent.push(msg); } };
        const frame = { contentWindow: win };
        let ready = 0;
        const panel = m.openPanel(frame, { onReady: () => ready++ });
        const fire = type => listeners.forEach(f => f({ source: win, data: { type } }));
        panel.setHtml('<p>hi</p>');
        assert.strictEqual(sent.length, 0, 'not before the host is ready');
        fire('coral-panel-host-ready');
        assert.strictEqual(sent.length, 1);
        fire('coral-panel-host-ready'); // reload
        assert.strictEqual(sent.length, 2, 'the cached html is handed over again');
        assert.deepStrictEqual(sent[1], { type: 'coral-panel-load', html: '<p>hi</p>' });
        listeners.forEach(f => f({ source: {}, data: { type: 'coral-panel-host-ready' } }));
        assert.strictEqual(sent.length, 2, 'only this host counts');
    }

    // F2: per-panel throttle: one in flight, ~2/s, ~100/min.
    {
        let t = 1000;
        const th = m.createEventThrottle(() => t);
        assert.strictEqual(th.tryStart(), '');
        assert.match(th.tryStart(), /previous/, 'one in flight');
        th.done(); t += 100;
        assert.match(th.tryStart(), /slow down/, 'at most about 2 a second');
        t += 500;
        assert.strictEqual(th.tryStart(), ''); th.done();
        let allowed = 2;
        for (let i = 0; i < 300; i++) { t += 500; if (th.tryStart() === '') { allowed++; th.done(); } if (t - 1000 > 59000) break; }
        assert.ok(allowed <= 100, 'no more than 100 in a minute: ' + allowed);
        t += 60000;
        assert.strictEqual(th.tryStart(), '', 'the window slides');
    }
    console.log('panel_sandbox.test.js: ok');
})().catch(e => { console.error(e); process.exit(1); });
