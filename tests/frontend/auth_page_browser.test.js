// The /auth page (#3948): sign in with the password, create the first password (with the setup code from another computer),
// the lockout countdown, the forgot-password help and the API key fallback. The page is the server's own markup (checked-in
// goldens, kept in step by internal/auth's TestAuthPageGolden), served through CDP request interception together with canned
// replies for #3949's routes; the script is the real /static/auth_page.js. Run against an isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const GOLD = n => fs.readFileSync(path.join(__dirname, '../../coral-go/internal/auth/testdata', `auth_page_${n}.html`), 'utf8');
(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Fetch, Emulation } = client;
    const ev = async x => { const r = await Runtime.evaluate({ expression: x, returnByValue: true, awaitPromise: true }); if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400)); return r.result.value; };
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: settle
    const waitFor = async (expr, what, ms = 6000) => { const end = Date.now() + ms; while (Date.now() < end) { if (await ev(expr)) return; await sleep(20); } throw new Error('timed out waiting for ' + what); };
    let page = 'signin';            // which golden /auth serves
    const posts = [];              // {url, body}
    let replies = [];              // queue of {status, body, headers} for the next POSTs
    const json = (status, body, headers = {}) => ({ status, body, headers });
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Fetch.enable({ patterns: [{ urlPattern: BASE + '/auth*' }, { urlPattern: BASE + '/zz-done*' }] });
        client.on('Fetch.requestPaused', async p => {
            const u = new URL(p.request.url);
            const fulfill = (status, body, type, headers = {}) => Fetch.fulfillRequest({ requestId: p.requestId, responseCode: status, responseHeaders: [{ name: 'Content-Type', value: type }, ...Object.entries(headers).map(([name, value]) => ({ name, value }))], body: Buffer.from(body).toString('base64') });
            if (u.pathname === '/zz-done') return fulfill(200, '<title>done</title><p id="done">done</p>', 'text/html');
            if (p.request.method === 'POST') {
                posts.push({ url: u.pathname, body: JSON.parse(p.request.postData || '{}') });
                const r = replies.shift() || json(200, { ok: true });
                return fulfill(r.status, JSON.stringify(r.body), 'application/json', r.headers);
            }
            return fulfill(200, GOLD(page), 'text/html; charset=utf-8');
        });
        const open = async (name, query = '?return_to=/zz-done') => {
            page = name; posts.length = 0; replies = [];
            await Page.navigate({ url: BASE + '/auth' + query }); await Page.loadEventFired();
            await waitFor(`!!document.getElementById('submit') || !!document.getElementById('key-submit')`, 'the page');
            await sleep(100);   // the module script runs after load
            assert.equal(await ev(`(() => { const i = document.querySelector('.brand img'); return !!i && i.complete && i.naturalWidth > 0; })()`), true, `${name}: Coral's logo loads (from /static, with no credential)`);
        };
        const $ = (id, prop = 'value') => ev(`document.getElementById('${id}').${prop}`);
        const type = (id, v) => ev(`(() => { const i = document.getElementById('${id}'); i.value = ${JSON.stringify(v)}; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
        const submit = id => ev(`document.getElementById('${id}').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); true`);
        const visible = id => ev(`(() => { const e = document.getElementById('${id}'); return !!e && !e.hidden && e.getClientRects().length > 0; })()`);
        const text = id => ev(`document.getElementById('${id}').textContent`);

        // ── sign in ──
        await open('signin');
        assert.equal(await ev(`document.activeElement && document.activeElement.id`), 'password', 'focus starts in the password');
        assert.ok(!(await visible('err')) && !(await visible('lock')), 'the error and lockout boxes take no room until they are needed');
        assert.equal(await $('keep', 'checked'), false, '"Keep me signed in" is off by default');
        await ev(`document.querySelector('[data-toggle=password]').click(); true`);
        assert.equal(await $('password', 'type'), 'text', 'Show reveals it'); await ev(`document.querySelector('[data-toggle=password]').click(); true`);
        assert.equal(await $('password', 'type'), 'password');
        await submit('signin-form');
        assert.equal(await text('err'), 'Enter your password.'); assert.equal(posts.length, 0, 'nothing is sent for an empty password');
        assert.ok(await visible('err'), 'and the message shows');
        replies = [json(401, { code: 'wrong_password', error: 'Wrong password' })];
        await type('password', ' wrong one '); await submit('signin-form');
        await waitFor(`document.getElementById('err').textContent === 'That password is not right.'`, 'the wrong-password message');
        assert.deepEqual(posts[0], { url: '/auth/password', body: { password: ' wrong one ', keep: false } }, 'sent exactly as typed');
        assert.ok(await ev(`document.getElementById('password').classList.contains('bad')`));
        assert.equal(await $('submit', 'disabled'), false, 'and you can try again');
        // lockout: a countdown, the form disabled, then it opens by itself
        replies = [json(429, { code: 'too_many_attempts', error: 'Too many attempts. Try again in 2 s.', retry_after: 2 }, { 'Retry-After': '2' })];
        await submit('signin-form');
        await waitFor(`document.getElementById('lock') && !document.getElementById('lock').hidden`, 'the lockout notice');
        assert.ok(await visible('lock'), 'the lockout notice shows');
        assert.equal(await text('count'), '0:02'); assert.equal(await $('password', 'disabled'), true); assert.equal(await $('submit', 'disabled'), true);
        await waitFor(`document.getElementById('lock').hidden && !document.getElementById('password').disabled`, 'the form opening again', 5000);
        // success: keep = true goes to return_to
        replies = [json(200, { ok: true })];
        await type('password', 'right'); await ev(`document.getElementById('keep').click(); true`); await submit('signin-form');
        await waitFor(`!!document.getElementById('done')`, 'the redirect to return_to');
        assert.deepEqual(posts[posts.length - 1], { url: '/auth/password', body: { password: 'right', keep: true } });
        // forgot password
        await open('signin');
        await ev(`document.getElementById('show-forgot').click(); true`);
        assert.ok(await visible('panel-forgot')); assert.ok(!(await visible('panel-signin')));
        assert.equal(await text('reset-cmd'), 'coral reset-password');
        await ev(`document.querySelector('#panel-forgot [data-back]').click(); true`);
        assert.ok(await visible('panel-signin'));
        // API key fallback
        await ev(`document.getElementById('show-key').click(); true`);
        assert.ok(await visible('panel-key'));
        replies = [json(401, { error: 'Invalid API key' })];
        await type('api-key', 'bad-key'); await submit('key-form');
        await waitFor(`document.getElementById('key-err').textContent === 'Invalid API key'`, 'key error');
        replies = [json(200, { ok: true })];
        await submit('key-form'); await waitFor(`!!document.getElementById('done')`, 'key redirect');
        assert.deepEqual(posts[posts.length - 1], { url: '/auth/key', body: { key: 'bad-key' } });
        // an unsafe return_to is ignored
        await open('signin', '?return_to=//evil.example');
        replies = [json(200, { ok: true })]; await type('password', 'x'); await submit('signin-form');
        await waitFor(`location.pathname === '/'`, 'a safe fallback for an unsafe return_to');
        assert.ok((await ev('location.host')).length > 0 && !(await ev('location.host')).includes('evil'));

        // ── create (this computer) ──
        await open('create');
        assert.equal(await ev(`!!document.getElementById('setup-code')`), false, 'no setup code from this computer');
        await type('password', 'a'); await ev(`document.getElementById('meter').getAttribute('data-level')`);
        assert.equal(await ev(`document.getElementById('meter').getAttribute('data-level')`), 'weak');
        assert.equal(await $('submit', 'disabled'), false, 'the meter never blocks');
        await type('confirm', 'b'); await submit('create-form');
        assert.match(await text('err'), /do not match/); assert.equal(posts.length, 0);
        await type('password', ''); await type('confirm', ''); await submit('create-form');
        assert.equal(await text('err'), 'Enter a password.');
        replies = [json(200, { ok: true })];
        await type('password', '  a b  '); await type('confirm', '  a b  '); await submit('create-form');
        await waitFor(`!!document.getElementById('done')`, 'redirect after creating');
        assert.deepEqual(posts[0], { url: '/auth/password/create', body: { password: '  a b  ', keep: false } }, 'any non-empty password, exactly as typed, no code');
        // 409: someone set one first -> the page reloads as the sign-in page
        await open('create');
        replies = [json(409, { code: 'password_set', error: 'A password is already set' })];
        page = 'signin';   // what the server serves after the race
        await type('password', 'x'); await type('confirm', 'x'); await submit('create-form');
        await waitFor(`!!document.getElementById('signin-form')`, 'the sign-in page after a 409');

        // ── create with the setup code (another computer) ──
        await open('setup');
        assert.equal(await ev(`!!document.getElementById('setup-code')`), true);
        await type('password', 'pw'); await type('confirm', 'pw'); await submit('create-form');
        assert.equal(await text('err'), 'Enter the setup code.'); assert.equal(posts.length, 0);
        replies = [json(401, { code: 'bad_code', error: 'That setup code is not right' })];
        await type('setup-code', ' abcd-1234 '); await submit('create-form');
        await waitFor(`document.getElementById('err').textContent === 'That setup code is not right'`, 'the bad code message');
        assert.deepEqual(posts[0], { url: '/auth/password/create', body: { password: 'pw', keep: false, code: 'ABCD-1234' } }, 'the code is normalised, the password is not touched');
        replies = [json(429, { code: 'too_many_attempts', retry_after: 1 }, { 'Retry-After': '1' })];
        await submit('create-form');
        await waitFor(`!document.getElementById('lock').hidden`, 'setup lockout'); assert.equal(await $('setup-code', 'disabled'), true);
        await waitFor(`document.getElementById('lock').hidden`, 'setup lockout ends', 4000);

        // ── phone width: no horizontal scroll ──
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        for (const n of ['signin', 'create', 'setup']) { await open(n); assert.equal(await ev(`document.documentElement.scrollWidth <= document.documentElement.clientWidth`), true, `${n}: fits a 390 px phone`); }
        console.log('PASS /auth page: sign in, lockout, forgot, API key, create, setup code, phone width');
    } finally { await Fetch.disable().catch(() => {}); await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
