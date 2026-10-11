// Unit test for hub artifact downloads and the HTML artifact frame (jsdom, no browser):
//   node artifact_download.test.js
const assert = require('assert');
const path = require('path');
const { pathToFileURL } = require('url');
const { JSDOM } = require('jsdom');

(async () => {
    const dir = path.resolve(__dirname, '../../coral-go/pkg/frontend/static');
    const dom = new JSDOM('<!doctype html><body><script type="application/json" id="coral-boot">{"mode":"cloud"}</script></body>', { url: 'https://hub.example/' });
    for (const k of ['window', 'document', 'location', 'Blob', 'URL', 'HTMLElement', 'Event']) {
        Object.defineProperty(globalThis, k, { value: dom.window[k], configurable: true, writable: true });
    }
    const m = await import(pathToFileURL(path.join(dir, 'artifact_download.js')).href);
    const f = await import(pathToFileURL(path.join(dir, 'sandbox_frame.js')).href);

    // ── file names ──
    const s = m.sanitizeDownloadName;
    assert.strictEqual(s('report.html'), 'report.html');
    assert.strictEqual(s('../../etc/passwd'), 'passwd');
    assert.strictEqual(s('C:\\Users\\x\\evil.exe'), 'evil.exe');
    assert.strictEqual(s('.hidden'), 'hidden');
    assert.strictEqual(s('...'), 'artifact');
    assert.strictEqual(s(''), 'artifact');
    assert.strictEqual(s(null), 'artifact');
    assert.strictEqual(s('a\u0000b\r\nc\u202e.txt'), 'abc.txt');
    assert.strictEqual(s('x'.repeat(300)).length, 100);
    assert.strictEqual(s('dir/'), 'artifact');
    // trailing dots/spaces, reserved characters, Windows device names
    assert.strictEqual(s('report.txt.'), 'report.txt');
    assert.strictEqual(s('report.txt . . '), 'report.txt');
    assert.strictEqual(s('a:b*c?d"e<f>g|h.txt'), 'abcdefgh.txt');
    assert.strictEqual(s('::**'), 'artifact');
    for (const r of ['CON', 'con', 'NUL', 'prn', 'AUX', 'COM1', 'com9', 'LPT1', 'lpt9', 'CON.txt', 'nul.tar.gz']) assert.strictEqual(s(r), '_' + r, r);
    for (const ok of ['COM0', 'COM10', 'LPT0', 'console.txt', 'nullable', 'conx.txt', 'my.con']) assert.strictEqual(s(ok), ok, ok);
    assert.strictEqual(s('x'.repeat(99) + '. . '), 'x'.repeat(99));
    assert.strictEqual(s('a'.repeat(98) + ' .txt').length <= 100, true);

    // ── download: blob type, anchor, revoke, no navigation ──
    const body = new TextEncoder().encode('%PDF-fake').buffer;
    const mk = (over = {}) => ({ ok: true, status: 200, headers: { get: k => (over.len !== undefined && k === 'Content-Length' ? String(over.len) : null) }, arrayBuffer: async () => over.buf || body });
    let fetched = null, blobs = [], clicked = [], revoked = [], opened = 0;
    globalThis.fetch = async (u, init) => { fetched = { u, init }; return fetched.next || mk(); };
    dom.window.URL.createObjectURL = b => { blobs.push(b); return 'blob:https://hub.example/1'; };
    dom.window.URL.revokeObjectURL = u => revoked.push(u);
    dom.window.open = () => { opened++; };
    dom.window.HTMLAnchorElement.prototype.click = function () { clicked.push({ href: this.href, download: this.download, inDom: this.isConnected }); };

    await m.downloadArtifact('/api/remote/s/api/artifacts/' + 'a'.repeat(64), '../weird name.pdf');
    assert.strictEqual(fetched.init.method, 'GET');
    assert.strictEqual(fetched.init.cache, 'no-store');
    assert.strictEqual(blobs.length, 1);
    assert.strictEqual(blobs[0].type, 'application/octet-stream');
    assert.deepStrictEqual(clicked, [{ href: 'blob:https://hub.example/1', download: 'weird name.pdf', inDom: true }]);
    await new Promise(r => setTimeout(r, 10));
    assert.deepStrictEqual(revoked, ['blob:https://hub.example/1']);
    assert.strictEqual(opened, 0);
    assert.strictEqual(dom.window.document.querySelectorAll('a').length, 0, 'the temporary anchor is removed');

    // declared type never reaches the blob
    fetched = null; blobs.length = 0;
    globalThis.fetch = async () => ({ ok: true, status: 200, headers: { get: k => (k === 'Content-Type' ? 'text/html' : null) }, arrayBuffer: async () => body });
    await m.downloadArtifact('/api/artifacts/' + 'b'.repeat(64), 'page.html');
    assert.strictEqual(blobs[0].type, 'application/octet-stream');

    // cross-origin and failures
    await assert.rejects(m.downloadArtifact('https://evil.example/x', 'x'), /same-origin/);
    await assert.rejects(m.downloadArtifact('/static/app.js', 'x'), /same-origin API/);
    globalThis.fetch = async () => ({ ok: false, status: 403, headers: { get: () => null } });
    await assert.rejects(m.downloadArtifact('/api/artifacts/' + 'c'.repeat(64), 'x'), /403/);

    // size cap: by header, and by actual bytes when no Content-Length reaches the hub
    const big = m.MAX_DOWNLOAD_BYTES + 1;
    assert.strictEqual(m.MAX_DOWNLOAD_BYTES, 25 * 1024 * 1024);
    blobs.length = 0;
    globalThis.fetch = async () => mk({ len: big });
    await assert.rejects(m.downloadArtifact('/api/artifacts/' + 'd'.repeat(64), 'x'), /too large/);
    globalThis.fetch = async () => mk({ buf: new ArrayBuffer(big) });
    await assert.rejects(m.downloadArtifact('/api/artifacts/' + 'e'.repeat(64), 'x'), /too large/);
    globalThis.fetch = async () => mk({ buf: new ArrayBuffer(m.MAX_DOWNLOAD_BYTES) });
    await m.downloadArtifact('/api/artifacts/' + 'f'.repeat(64), 'x');
    assert.strictEqual(blobs.length, 1, 'exactly the cap is allowed');

    // ── bindDownload / wireDownloads on the hub: no href, click fetches ──
    globalThis.fetch = async (u) => { fetched = { u }; return mk(); };
    const a = dom.window.document.createElement('a');
    a.href = '/api/remote/s/api/artifacts/' + '1'.repeat(64); a.setAttribute('download', 'n'); a.target = '_blank';
    m.bindDownload(a, '/api/remote/s/api/artifacts/' + '1'.repeat(64), 'n.bin');
    assert.strictEqual(a.hasAttribute('href'), false);
    assert.strictEqual(a.hasAttribute('target'), false);
    assert.strictEqual(a.getAttribute('role'), 'button');
    clicked.length = 0;
    const ev = new dom.window.Event('click', { cancelable: true });
    a.dispatchEvent(ev);
    assert.strictEqual(ev.defaultPrevented, true);
    await new Promise(r => setTimeout(r, 20));
    assert.strictEqual(clicked.length, 1);
    assert.strictEqual(clicked[0].download, 'n.bin');

    const root = dom.window.document.createElement('div');
    root.innerHTML = '<a href="/x" download data-artifact-download="/api/remote/s/api/artifacts/' + '2'.repeat(64) + '" data-artifact-name="z.txt">Download</a><a href="https://e/x">other</a>';
    m.wireDownloads(root);
    assert.strictEqual(root.querySelector('a[data-artifact-download]').hasAttribute('href'), false);
    assert.strictEqual(root.querySelectorAll('a')[1].getAttribute('href'), 'https://e/x');

    // ── HTML artifact frame: nothing is granted ──
    const fr = dom.window.document.createElement('iframe');
    f.lockFrame(fr);
    assert.strictEqual(fr.getAttribute('sandbox'), '');
    for (const bad of ['allow-scripts', 'allow-same-origin', 'allow-top-navigation', 'allow-forms', 'allow-popups']) assert.ok(!fr.getAttribute('sandbox').includes(bad));
    assert.strictEqual(fr.getAttribute('csp'), "default-src 'none'; img-src data:; style-src 'unsafe-inline'");
    const doc = f.sandboxedDoc('<h1>x</h1><script>alert(1)</script>');
    assert.ok(doc.startsWith('<!doctype html><meta http-equiv="Content-Security-Policy" content="default-src \'none\''));
    assert.ok(doc.includes("form-action 'none'") && doc.includes("base-uri 'none'"));

    console.log('artifact_download.test.js: ok');
})().catch(e => { console.error(e); process.exit(1); });
