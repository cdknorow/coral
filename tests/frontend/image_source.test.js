// Pure-node unit test for the hub's image sources (no browser needed):
//   node image_source.test.js
const assert = require('assert');
const path = require('path');
const { pathToFileURL } = require('url');

(async () => {
    const dir = path.resolve(__dirname, '../../coral-go/pkg/frontend/static');
    globalThis.window = globalThis; globalThis.location = { href: 'https://hub.example/', protocol: 'https:', host: 'hub.example' };
    const m = await import(pathToFileURL(path.join(dir, 'image_source.js')).href);

    assert.strictEqual(m.isSvg('logo.svg'), true);
    assert.strictEqual(m.isSvg('a/b/LOGO.SVG'), true);
    assert.strictEqual(m.isSvg('image/svg+xml; charset=utf-8'), true);
    assert.strictEqual(m.isSvg('photo.png'), false);
    assert.strictEqual(m.isSvg('svg.txt'), false);
    assert.strictEqual(m.isSvg(''), false);

    // The data: URL carries the text verbatim, escaped, and nothing executable on its own.
    const evil = '<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script><rect/></svg>';
    const u = m.svgDataUrl(evil);
    assert.ok(u.startsWith('data:image/svg+xml;charset=utf-8,'));
    assert.ok(!/[<>"]/.test(u.slice('data:image/svg+xml;charset=utf-8,'.length)));
    assert.strictEqual(decodeURIComponent(u.split(',').slice(1).join(',')), evil);

    assert.strictEqual(m.urlFileName('/api/remote/x/api/sessions/live/a/file-content?filepath=docs%2Flogo.svg&raw=1'), 'docs/logo.svg');
    assert.strictEqual(m.urlFileName('/api/artifacts/abc'), 'abc');

    // fetchImageSrc: no cache-buster is added, the request is no-store, SVG comes back as a data: URL.
    const calls = [];
    const respond = (body, type, status = 200) => ({
        ok: status < 300, status,
        headers: { get: k => ({ 'Content-Type': type, 'Content-Length': String(body.length) }[k] || null) },
        arrayBuffer: async () => new TextEncoder().encode(body).buffer,
    });
    globalThis.fetch = async (url, init) => { calls.push({ url, init }); return url.includes('missing') ? respond('', 'application/json', 404) : url.includes('.svg') ? respond('<svg/>', 'application/octet-stream') : respond('PNGDATA', 'application/octet-stream'); };
    let r = await m.fetchImageSrc('/api/remote/s/api/sessions/live/a/file-content?filepath=logo.svg&raw=1');
    assert.strictEqual(calls[0].init.cache, 'no-store');
    assert.ok(!/[?&]t=/.test(calls[0].url));
    assert.strictEqual(r.revoke, false);
    assert.strictEqual(decodeURIComponent(r.src.split(',')[1]), '<svg/>');

    // Non-SVG: a blob URL (URL.createObjectURL is stubbed) with a type taken from the extension, not the response.
    let blobType = null;
    globalThis.URL.createObjectURL = b => { blobType = b.type; return 'blob:x'; };
    r = await m.fetchImageSrc('/api/remote/s/api/sessions/live/a/file-content?filepath=photo.png&raw=1');
    assert.deepStrictEqual(r, { src: 'blob:x', revoke: true });
    assert.strictEqual(blobType, 'image/png');

    // No extension: the response type is used only if it is in the raster allowlist; never SVG, never unknown.
    globalThis.fetch = async (url) => respond('x', url.includes('svgtype') ? 'image/svg+xml' : url.includes('weird') ? 'text/html' : 'image/webp');
    r = await m.fetchImageSrc('/api/artifacts/abc');
    assert.strictEqual(blobType, 'image/webp');
    blobType = null;
    await assert.rejects(m.fetchImageSrc('/api/artifacts/svgtype'), /unsupported image type/);
    await assert.rejects(m.fetchImageSrc('/api/artifacts/weird'), /unsupported image type/);
    assert.strictEqual(blobType, null);

    // An error status rejects, so the pane shows its "missing" text.
    globalThis.fetch = async (url) => respond('', 'application/json', 404);
    await assert.rejects(m.fetchImageSrc('/x/missing.png'), /HTTP 404/);

    // Over-large SVG text is refused.
    globalThis.fetch = async () => ({ ok: true, status: 200, headers: { get: k => (k === 'Content-Length' ? String(3 * 1024 * 1024) : null) }, arrayBuffer: async () => new ArrayBuffer(0) });
    await assert.rejects(m.fetchImageSrc('/x/big.svg'), /too large/);

    console.log('image_source.test.js: ok');
})().catch(e => { console.error(e); process.exit(1); });
