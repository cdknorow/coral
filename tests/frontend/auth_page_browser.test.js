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
    let statusReplies = [];        // the link poll's GET /auth/hub/link/status answers, in order (the last one repeats)
    const json = (status, body, headers = {}) => ({ status, body, headers });
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Fetch.enable({ patterns: [{ urlPattern: BASE + '/auth*' }, { urlPattern: BASE + '/zz-done*' }] });
        client.on('Fetch.requestPaused', async p => {
            const u = new URL(p.request.url);
            const fulfill = (status, body, type, headers = {}) => Fetch.fulfillRequest({ requestId: p.requestId, responseCode: status, responseHeaders: [{ name: 'Content-Type', value: type }, ...Object.entries(headers).map(([name, value]) => ({ name, value }))], body: Buffer.from(body).toString('base64') });
            if (u.pathname === '/zz-done') return fulfill(200, '<title>done</title><p id="done">done</p>', 'text/html');
            if (u.pathname === '/auth/hub/link/status') { const r = statusReplies.length > 1 ? statusReplies.shift() : statusReplies[0]; return fulfill(200, JSON.stringify(r || { state: 'enrolling', ready: false, error: '' }), 'application/json'); }
            if (p.request.method === 'POST') {
                posts.push({ url: u.pathname, body: JSON.parse(p.request.postData || '{}') });
                const r = replies.shift() || json(200, { ok: true });
                return fulfill(r.status, JSON.stringify(r.body), 'application/json', r.headers);
            }
            return fulfill(200, GOLD(page), 'text/html; charset=utf-8');
        });
        let nextReplies = [];
        const openWith = (name, query, r) => { nextReplies = r; return open(name, query); };
        const open = async (name, query = '?return_to=/zz-done') => {
            page = name; posts.length = 0; replies = nextReplies; nextReplies = [];
            await Page.navigate({ url: BASE + '/auth' + query }); await Page.loadEventFired();
            await waitFor(`!!document.getElementById('submit') || !!document.getElementById('key-submit')`, 'the page');
            await waitFor(`document.documentElement.dataset.authReady === 'true'`, 'the page script');   // the module script runs after load
            assert.equal(await ev(`(() => { const i = document.querySelector('.brand img'); return !!i && i.complete && i.naturalWidth > 0; })()`), true, `${name}: Coral's logo loads (from /static, with no credential)`);
        };
        const $ = (id, prop = 'value') => ev(`document.getElementById('${id}').${prop}`);
        const type = (id, v) => ev(`(() => { const i = document.getElementById('${id}'); i.value = ${JSON.stringify(v)}; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
        const submit = id => ev(`document.getElementById('${id}').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); true`);
        const visible = id => ev(`(() => { const e = document.getElementById('${id}'); return !!e && !e.hidden && e.getClientRects().length > 0; })()`);
        const text = id => ev(`document.getElementById('${id}').textContent`);

        // ── sign in ──
        await open('signin');
        await waitFor(`document.activeElement && document.activeElement.id === 'password'`, 'focus to start in the password');   // autofocus lands a moment after the page script
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
        // a key in the address (the QR link): the fragment is read first, ?api_key= still works, both leave the address bar
        await openWith('signin', '#api_key=FRAGKEY', [json(401, { error: 'Invalid API key' })]);
        assert.ok(await visible('panel-key'), 'a key in the address opens the key panel');
        await waitFor(`!document.getElementById('panel-key').hidden && document.getElementById('api-key').value === 'FRAGKEY'`, 'the key is filled');
        assert.deepEqual(posts[0], { url: '/auth/key', body: { key: 'FRAGKEY' } }, 'the fragment key is sent');
        assert.equal(await ev('location.hash'), '', 'and the fragment is cleared from the address');
        replies = [json(401, { error: 'Invalid API key' })];
        await openWith('signin', '?api_key=OLDKEY', [json(401, { error: 'Invalid API key' })]);
        assert.deepEqual(posts[0], { url: '/auth/key', body: { key: 'OLDKEY' } }, 'an older ?api_key= link still works');
        assert.ok(!(await ev('location.search')).includes('api_key'), 'and is cleared from the address');
        await openWith('signin', '?api_key=OLDKEY#api_key=FRAGKEY', [json(401, { error: 'Invalid API key' })]);
        assert.equal(posts[0].body.key, 'FRAGKEY', 'the fragment wins when both are present');
        assert.equal(await ev('location.hash'), '');
        assert.ok(!(await ev('location.search')).includes('api_key'));
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


        // ── Sign in with Coral Hub (#3966): what the server offers is in the markup; the address only says what went wrong ──
        await open('hub-ready');
        assert.ok(await visible('hub-btn'), 'the Sign in with Coral Hub button shows when the server says ready');
        assert.ok(!(await visible('hub-disabled')), 'and the disabled note stays hidden');
        replies = [json(200, { ok: true })];
        await ev(`document.getElementById('hub-btn').click(); true`);
        await waitFor(`true`, 'the form posts');
        for (let i = 0; i < 100 && !posts.length; i++) await sleep(20);
        assert.deepEqual(posts[0], { url: '/auth/hub/start', body: {} }, 'the button is a plain form POST to /auth/hub/start');
        for (const [q, want] of [['unavailable', 'Coral Hub could not start the sign-in. Use your password.'], ['failed', 'Coral Hub could not sign you in.']]) {
            await open('hub-ready', `?hub=${q}`);
            assert.ok(await visible('hub-flash'), `?hub=${q} shows a message`);
            assert.ok((await text('hub-flash')).startsWith(want), `?hub=${q}: ${await text('hub-flash')}`);
            assert.ok(!(await ev('location.search')).includes('hub='), `?hub=${q} is dropped from the address`);
        }
        await open('hub-ready', '?hub=disabled');
        assert.ok(await visible('hub-disabled'), '?hub=disabled shows the note');
        assert.equal(await ev(`document.querySelector('#hub-disabled a').href`), 'https://app.coralhub.dev/', 'with a link to the Hub');
        await open('hub-ready', '?hub=bogus');
        assert.ok(!(await visible('hub-flash')), 'an unknown value shows nothing');

        await open('hub-disabled');
        assert.ok(await visible('hub-disabled'), 'when the Hub says its switch is off the note shows');
        assert.equal(await ev(`!!document.getElementById('hub-btn') || !!document.getElementById('hub-link-open') || !!document.getElementById('hub-start-form')`), false, 'and there is no sign-in action');

        // the link path, from this computer
        await open('hub-unpaired');
        await ev(`window.__opened = []; window.open = u => { window.__opened.push(u); return null; }; true`);
        assert.ok(await visible('hub-link-open'));
        await ev(`document.getElementById('hub-link-open').click(); true`);
        assert.ok(await visible('panel-hublink') && !(await visible('panel-signin')));
        assert.equal(await $('hub-enable', 'checked'), false, 'the checkbox is unticked');
        assert.ok(!(await visible('hub-setup-field')), 'from this computer there is no setup code field');
        await open('hub-unpaired', '?hub_enable=1&enable=1&enable_signin=true&checked=1');
        await ev(`document.getElementById('hub-link-open').click(); true`);
        assert.equal(await $('hub-enable', 'checked'), false, 'nothing in the address can tick or fill the checkbox');
        await ev(`window.__opened = []; window.open = u => { window.__opened.push(u); return null; }; true`);
        // a bad address from the server is refused before anything opens
        replies = [json(200, { verification_url: 'http://hub.example/verify', user_code: 'ABCD-EFGH' })];
        await submit('hub-link-form');
        await waitFor(`document.getElementById('hub-link-err').textContent.includes('will not open')`, 'a non-https verification address is refused');
        assert.ok(!(await visible('panel-hubwait')));
        assert.deepEqual(await ev('window.__opened'), []);
        // the real flow: link, show the code, open the Hub's page, poll until linked, then the sign-in POST
        await ev(`document.getElementById('hub-enable').click(); true`);
        replies = [json(200, { verification_url: 'https://hub.example/verify?c=1', user_code: 'ABCD-EFGH' })];
        statusReplies = [{ state: 'enrolling', ready: false, error: '' }, { state: 'linked', ready: true, error: '' }];
        posts.length = 0;
        await submit('hub-link-form');
        await waitFor(`!document.getElementById('panel-hubwait').hidden`, 'the waiting step');
        assert.deepEqual(posts[0], { url: '/auth/hub/link', body: { enable_signin: true } }, 'the ticked checkbox goes with the link request, and no setup code from this computer');
        assert.equal(await text('hub-code'), 'ABCD-EFGH');
        assert.deepEqual(await ev('window.__opened'), ['https://hub.example/verify?c=1'], "the Hub's own page is opened");
        replies = [json(200, { ok: true })];
        for (let i = 0; i < 400 && !posts.some(x => x.url === '/auth/hub/start'); i++) await sleep(20);
        assert.ok(posts.some(x => x.url === '/auth/hub/start'), 'once linked and ready, the page makes the sign-in POST by itself');
        // a failed link shows the server's words and stops
        await open('hub-unpaired');
        await ev(`document.getElementById('hub-link-open').click(); true`);
        replies = [json(200, { verification_url: 'https://hub.example/verify?c=2', user_code: 'WXYZ-1234' })];
        statusReplies = [{ state: 'failed', ready: false, error: 'The approval was refused in Coral Hub.' }];
        await ev(`window.open = () => null; true`);
        await submit('hub-link-form');
        await waitFor(`document.getElementById('hub-wait-err').textContent === 'The approval was refused in Coral Hub.'`, 'the failure message', 8000);
        await ev(`document.getElementById('hub-cancel').click(); true`);
        assert.ok(await visible('panel-signin'), 'Cancel goes back to the password');

        // from another computer: the setup code field, its errors, and an already-linked machine
        await open('hub-unpaired-remote');
        await ev(`document.getElementById('hub-link-open').click(); true`);
        assert.ok(await visible('hub-setup-field'));
        await submit('hub-link-form');
        assert.equal(await text('hub-link-err'), 'Enter the setup code.'); assert.equal(posts.length, 0, 'nothing is sent without a code');
        replies = [json(403, { code: 'setup_code_wrong' })];
        await type('hub-setup-code', ' abcd-1234 '); await submit('hub-link-form');
        await waitFor(`document.getElementById('hub-link-err').textContent === 'That setup code is not right.'`, 'a wrong code');
        assert.deepEqual(posts[0], { url: '/auth/hub/link', body: { enable_signin: false, setup_code: 'ABCD-1234' } }, 'the code is normalised; unticked means enable_signin false');
        page = 'hub-ready';
        replies = [json(409, { code: 'already_linked' })];
        await submit('hub-link-form');
        await waitFor(`!!document.getElementById('hub-start-form')`, 'a 409 reloads as the page that offers the sign-in');
        // this computer is asked for a code only when the server demands one
        await open('hub-unpaired');
        await ev(`document.getElementById('hub-link-open').click(); true`);
        replies = [json(403, { code: 'setup_code_required' })];
        await submit('hub-link-form');
        await waitFor(`!document.getElementById('hub-setup-field').hidden`, 'the code field appears when the server asks');
        assert.equal(await text('hub-link-err'), 'Enter the setup code.');

        // ── phone width: no horizontal scroll ──
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        for (const n of ['signin', 'create', 'setup', 'hub-ready', 'hub-unpaired', 'hub-unpaired-remote', 'hub-disabled']) { await open(n); assert.equal(await ev(`document.documentElement.scrollWidth <= document.documentElement.clientWidth`), true, `${n}: fits a 390 px phone`); }
        console.log('PASS /auth page: sign in, lockout, forgot, API key, create, setup code, phone width');
    } finally { await Fetch.disable().catch(() => {}); await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
