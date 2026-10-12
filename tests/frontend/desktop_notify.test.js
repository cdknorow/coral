// Unit test for the opt-in desktop alert (jsdom, no browser):
//   node desktop_notify.test.js
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const { pathToFileURL } = require('url');
const { JSDOM } = require('jsdom');

(async () => {
    const dir = path.resolve(__dirname, '../../coral-go/pkg/frontend/static');
    const dom = new JSDOM('<!doctype html><body></body>', { url: 'https://hub.example/' });
    for (const k of ['window', 'document', 'localStorage']) Object.defineProperty(globalThis, k, { value: dom.window[k], configurable: true, writable: true });
    const shown = [];
    let perm = 'default', asked = 0, hidden = true;
    class FakeNotification {
        constructor(title, opts) { this.title = title; this.opts = opts; this.closed = false; shown.push(this); }
        close() { this.closed = true; }
        static get permission() { return perm; }
        static async requestPermission() { asked++; perm = 'granted'; return perm; }
    }
    Object.defineProperty(globalThis, 'Notification', { value: FakeNotification, configurable: true, writable: true });
    Object.defineProperty(dom.window.document, 'hidden', { get: () => hidden, configurable: true });
    const m = await import(pathToFileURL(path.join(dir, 'desktop_notify.js')).href);

    // default off: nothing shown, no permission prompt
    assert.strictEqual(m.desktopNotifyEnabled(), false);
    assert.strictEqual(m.notifyNeedsInput({ agent: 'a', tag: 't' }), null);
    assert.strictEqual(asked, 0);

    // opt in (a click): permission asked once, remembered in this browser only
    assert.strictEqual(await m.enableDesktopNotify(), true);
    assert.strictEqual(asked, 1);
    assert.strictEqual(dom.window.localStorage.getItem(m.DESKTOP_NOTIFY_KEY), '1');
    assert.strictEqual(m.desktopNotifyEnabled(), true);

    // content-free text, background tab only
    const n = m.notifyNeedsInput({ agent: 'Worker 1', where: 'laptop', tag: 'srv:abc' });
    assert.ok(n);
    assert.strictEqual(n.title, 'Coral');
    assert.strictEqual(n.opts.body, 'Worker 1 needs input on laptop');
    assert.strictEqual(n.opts.tag, 'needs-input:srv:abc');
    assert.deepStrictEqual(Object.keys(n.opts).sort(), ['body', 'silent', 'tag']); // no icon, data or image
    hidden = false;
    assert.strictEqual(m.notifyNeedsInput({ agent: 'a', tag: 't' }), null, 'visible page: toast only');
    hidden = true;

    // hostile names are reduced to short plain text
    const t = m.alertText('evil‮\n\u0000name' + 'x'.repeat(200), 'a\tb');
    assert.ok(!/[\u0000-\u001f‮]/.test(t.body));
    assert.ok(t.body.length < 160);
    assert.strictEqual(m.alertText('', '').body, 'An agent needs input');

    dom.window.focus = () => {};
    // click focuses and opens the session
    let opened = 0;
    const n2 = m.notifyNeedsInput({ agent: 'b', tag: 'x', onClick: () => opened++ });
    n2.onclick();
    assert.strictEqual(opened, 1);
    assert.ok(n2.closed);

    // permission revoked in the browser: off again even though the flag is stored
    perm = 'denied';
    assert.strictEqual(m.desktopNotifyEnabled(), false);
    assert.strictEqual(m.notifyNeedsInput({ agent: 'a', tag: 't' }), null);
    // opt-in refused: stored off
    perm = 'default'; asked = 0;
    FakeNotification.requestPermission = async () => { asked++; perm = 'denied'; return perm; };
    assert.strictEqual(await m.enableDesktopNotify(), false);
    assert.strictEqual(dom.window.localStorage.getItem(m.DESKTOP_NOTIFY_KEY), null);

    // no service worker or push anywhere in the module
    const src = fs.readFileSync(path.join(dir, 'desktop_notify.js'), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');
    assert.ok(!/serviceWorker|pushManager|PushManager|showNotification|fetch\(|XMLHttpRequest|sendBeacon/.test(src));

    // the wiring: the alert is raised from the rising edge in websocket.js only, summaries are not passed
    const ws = fs.readFileSync(path.join(dir, 'websocket.js'), 'utf8');
    const call = ws.match(/notifyNeedsInput\(\{[^}]*\}\)/)[0];
    assert.ok(!/summary|detail/.test(call), call);
    console.log('desktop_notify: ok');
})().catch(e => { console.error(e); process.exit(1); });
