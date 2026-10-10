// Real-browser check of the request headers Coral's file-bounds rely on (localDashboard): a same-origin <img>
// and fetch() carry Sec-Fetch-Site: same-origin; a page on another origin (127.0.0.1 vs localhost) carries
// cross-site; an <img> sends no Origin header. If a browser or desktop webview stopped doing this, the local
// dashboard would silently lose the relaxed file bounds (fails closed, but breaks previews).
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = process.env.CORAL_URL;
if (!BASE || /:8420(\/|$)/.test(BASE)) throw new Error('isolated server required');

(async () => {
  const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
  const { Page, Runtime, Network } = client;
  const seen = {};
  Network.requestWillBeSentExtraInfo(e => { seen[e.requestId] = e.headers; });
  const byUrl = {};
  Network.requestWillBeSent(e => { byUrl[e.request.url] = e.requestId; });
  const header = (url, name) => { const h = seen[byUrl[url]] || {}; const k = Object.keys(h).find(x => x.toLowerCase() === name); return k ? h[k] : undefined; };
  const wait = ms => new Promise(r => setTimeout(r, ms));
  try {
    await Promise.all([Page.enable(), Runtime.enable(), Network.enable()]);
    await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await wait(1500);
    const img = `${BASE}/api/sessions/live/nosuch/file-content?filepath=a.png&raw=1`;
    await Runtime.evaluate({ expression: `(() => { const i = new Image(); i.src = ${JSON.stringify(img)}; document.body.appendChild(i); })()` });
    await Runtime.evaluate({ expression: `fetch(${JSON.stringify(BASE + '/api/health?same')}).then(r => r.status)`, awaitPromise: true });
    await wait(800);
    assert.equal(header(img, 'sec-fetch-site'), 'same-origin', '<img> same-origin');
    assert.equal(header(img, 'sec-fetch-dest'), 'image');
    assert.equal(header(img, 'origin'), undefined, 'an <img> sends no Origin header');
    assert.equal(header(`${BASE}/api/health?same`, 'sec-fetch-site'), 'same-origin', 'fetch same-origin');

    // Another origin on the same machine (localhost <-> 127.0.0.1) must read as cross-site, never same-origin.
    const u = new URL(BASE); const other = `${u.protocol}//${u.hostname === 'localhost' ? '127.0.0.1' : 'localhost'}:${u.port}`;
    await Page.navigate({ url: other + '/' }); await Page.loadEventFired(); await wait(1000);
    const cross = `${BASE}/api/health?cross`;
    await Runtime.evaluate({ expression: `fetch(${JSON.stringify(cross)}, {mode:'no-cors'}).then(() => 1).catch(() => 0)`, awaitPromise: true });
    await wait(800);
    const site = header(cross, 'sec-fetch-site');
    assert.ok(site === 'cross-site' || site === 'same-site', `other origin must not be same-origin, got ${site}`);
    assert.notEqual(site, 'same-origin');
    console.log(`PASS fetch metadata: same-origin img/fetch = same-origin, other origin = ${site}, img has no Origin`);
  } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
