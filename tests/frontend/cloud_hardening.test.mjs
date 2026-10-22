// Cloud-mode hardening checks (review #2785 items 3, 5, 6, 7):  node cloud_hardening.test.mjs
import assert from 'node:assert';
import fs from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { installDom, importUi, STATIC, FRONTEND } from './helpers/jsdom_env.mjs';

const dom = installDom({ mode: 'cloud' });
const w = dom.window;

// ── 3. sanitisation fails closed, and the real DOMPurify config strips on*/style ──
const utils = await importUi('utils');
{
    // Without DOMPurify nothing is inserted as HTML.
    delete globalThis.DOMPurify; delete w.DOMPurify;
    const evil = '<img src=x onerror=PWNED()><b>hi</b>';
    assert.strictEqual(utils.sanitizeHtml(evil), utils.escapeHtml(evil), 'no DOMPurify: markup must be shown as text');
    globalThis.marked = w.marked = { parse: s => `<p>${s}</p>` };
    assert.ok(!/<img/.test(utils.sanitizeMarkdown(evil)), 'no DOMPurify: markdown must not yield elements');
    // With the real vendored DOMPurify:
    const src = fs.readFileSync(path.join(STATIC, 'vendor/purify.min.js'), 'utf8');
    w.eval(src);
    globalThis.DOMPurify = w.DOMPurify;
    assert.ok(typeof DOMPurify.sanitize === 'function', 'vendored DOMPurify loads');
    const dirty = '<img src=x onerror=PWNED() onload=1 style="x:y"><a href="javascript:PWNED()" onclick="PWNED()" style="color:red">l</a><form action=//evil><input name=p></form><iframe src=//evil></iframe><style>*{}</style><svg onload=PWNED()></svg><b onmouseover=PWNED()>b</b>';
    const clean = utils.sanitizeHtml(dirty, utils.DOMPURIFY_CONFIG);
    const frag = w.document.createElement('div'); frag.innerHTML = clean;
    for (const el of frag.querySelectorAll('*')) {
        for (const a of el.attributes) assert.ok(!/^on/.test(a.name) && a.name !== 'style', `sanitized output kept ${a.name} on <${el.tagName}>`);
    }
    for (const t of ['form', 'iframe', 'style', 'script']) assert.strictEqual(frag.querySelectorAll(t).length, 0, `<${t}> survived`);
    assert.ok(!/javascript:/i.test(clean), 'javascript: URL survived');
}

// Every markdown/preview path goes through the fail-closed helpers, never raw marked output.
for (const f of fs.readdirSync(STATIC).filter(x => x.endsWith('.js'))) {
    const src = fs.readFileSync(path.join(STATIC, f), 'utf8');
    assert.ok(!/DOMPurify\s*!==?\s*'undefined'\s*\?[^:]*:\s*(html|marked\.parse)/.test(src), `${f}: falls back to raw HTML when DOMPurify is missing`);
}
const indexHtml = fs.readFileSync(path.join(FRONTEND, 'templates/index.html'), 'utf8');
assert.ok(indexHtml.includes('{{ $base }}vendor/purify.min.js') && indexHtml.includes('{{ $base }}vendor/marked.min.js'), 'index.html loads marked and DOMPurify');

// ── 5. ?next= (safeNext) ──────────────────────────────────────────────
const { safeNext } = await import(pathToFileURL(STATIC + 'cloud/common.js').href);
for (const bad of ['//evil.example', '/\\evil.example', '/%2f/evil.example', '\\\\evil.example', 'javascript:alert(1)', 'https://evil.example/', 'http://hub.example/x', '/ok\nSet-Cookie:x', '/ok\tx', '/ok\rx', '/a\u0000b', '', null, undefined, 'evil', '//', '/\\', '///evil']) {
    assert.strictEqual(safeNext(bad, '/home'), '/home', `safeNext accepted ${JSON.stringify(bad)}`);
}
assert.strictEqual(safeNext('/enroll?code=ABCD-2345', '/'), '/enroll?code=ABCD-2345');
assert.strictEqual(safeNext('/agent/x/y#a', '/'), '/agent/x/y#a');

// ── 6. cloud mode never asks a connector for system/settings/webhooks/servers ──
const base = await importUi('server_base');
const NEVER = /^\/api\/(system|settings|webhooks|servers)(\/|$)/;
assert.ok(NEVER.test('/api/system/api-key') && NEVER.test('/api/settings'));
{
    const urls = [];
    globalThis.fetch = w.fetch = async (u) => { urls.push(String(u)); return { ok: true, status: 200, json: async () => ({}), text: async () => '' }; };
    // Every route the review lists is refused at the routing helper, for any server id.
    for (const p of ['/api/system/editors', '/api/system/cli-check?binary=/bin/sh&type=claude', '/api/system/api-key', '/api/settings', '/api/settings/prompt-inspection?board=x',
        '/api/webhooks', '/api/webhooks/1/test', '/api/servers', '/api/servers/abc/test']) {
        const u = base.serverUrl(p, 'srv-1');
        assert.ok(!u.startsWith('/api/remote/'), `${p} would be sent to a connector: ${u}`);
        await base.serverFetch('srv-1', p);
        assert.ok(!urls[urls.length - 1].includes('/api/remote/'), `serverFetch sent ${p} through /api/remote`);
    }
    // The one /api/system route the hub exposes (Launch tier, #4607): the CLI check by agent type, never with a binary path (cli_check_cloud.test.js has the whole matrix).
    assert.ok(base.serverUrl('/api/system/cli-check?type=claude&source=cli_recheck', 'srv-1').startsWith('/api/remote/srv-1/'), 'the CLI check by type reaches the machine');
    assert.ok(base.serverUrl('/api/sessions/x/send', 'srv-1').startsWith('/api/remote/srv-1/'), 'normal routes still go to the server');
    // And at run time: the routing helper only prefixes what it is given; never for local routes.
    assert.strictEqual(base.serverUrl('/api/system/status', 'local'), '/api/system/status');
}

// ── 7. the CSRF token never reaches the DOM, a URL, or another origin ──
const cloud = await importUi('cloud');
{
    const sent = [];
    const TOKEN = 'TOKEN-SECRET-123';
    globalThis.fetch = w.fetch = async (u, init = {}) => {
        const url = typeof u === 'string' ? u : u.url;
        if (url.endsWith('/auth/session')) return { ok: true, status: 200, json: async () => ({ authenticated: true, email: 'a@x.test', csrf: TOKEN }) };
        sent.push({ url, headers: new Headers(init.headers || {}), method: init.method || 'GET' });
        return { ok: true, status: 200, json: async () => ({}), text: async () => '' };
    };
    w.__coralCloudFetch = false;
    cloud.installCloudFetch();
    await cloud.loadSession(true);
    await w.fetch('/api/remote/abc/api/sessions/x/send', { method: 'POST', body: '{}' });
    await w.fetch('https://evil.example/collect', { method: 'POST', body: '{}' });
    await w.fetch('/api/servers', { method: 'GET' });
    const same = sent.find(s => s.url === '/api/remote/abc/api/sessions/x/send');
    assert.strictEqual(same.headers.get('X-CSRF-Token'), TOKEN, 'same-origin writes carry the token');
    const cross = sent.find(s => s.url.startsWith('https://evil.example'));
    assert.strictEqual(cross.headers.get('X-CSRF-Token'), null, 'the token must never go to another origin');
    assert.strictEqual(sent.find(s => s.method === 'GET').headers.get('X-CSRF-Token'), null, 'reads do not need it');
    for (const s of sent) assert.ok(!s.url.includes(TOKEN), 'the token must never be in a URL');
    assert.ok(!w.document.documentElement.outerHTML.includes(TOKEN), 'the token must never be in the DOM');
    assert.ok(!JSON.stringify(Object.entries(w.localStorage)).includes(TOKEN), 'nor in localStorage');
}

// ── #3718: a reply refused as too large to check says so, never a blank pane ──
{
    const replies = {
        '/api/big': { status: 502, body: { error: 'too large', code: 'hub_reply_too_large' } },
        '/api/other502': { status: 502, body: { error: 'bad gateway' } },
        '/api/fine': { status: 200, body: { ok: true } },
    };
    globalThis.fetch = w.fetch = async (u) => {
        const url = typeof u === 'string' ? u : u.url;
        const r = replies[url] || { status: 200, body: {} };
        return new Response(JSON.stringify(r.body), { status: r.status, headers: { 'Content-Type': 'application/json' } });
    };
    w.__coralCloudFetch = false;
    cloud.installCloudFetch();
    const toasts = () => [...w.document.querySelectorAll('.toast')].map(t => t.textContent);
    const settle = () => new Promise(r => setTimeout(r, 50));
    const before = toasts().length;
    await w.fetch('/api/fine'); await w.fetch('/api/other502'); await settle();
    assert.strictEqual(toasts().length, before, 'no toast for a normal reply or another 502');
    await w.fetch('/api/big'); await settle();
    const shown = toasts().slice(before);
    assert.strictEqual(shown.length, 1, 'one toast for a reply too large to check');
    assert.ok(/too large to show through Coral Hub; open it on the computer/.test(shown[0]), shown[0]);
    await w.fetch('/api/big'); await settle();
    assert.strictEqual(toasts().length - before, 1, 'and not a second one at once');
}

console.log('cloud_hardening.test.mjs: ok');
process.exit(0);
