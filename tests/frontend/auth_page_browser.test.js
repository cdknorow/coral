// The /auth page (#3948): sign in with the password, create the first password (with the setup code from another computer),
// the lockout countdown, the forgot-password help and the API key fallback. The page is the server's own markup (checked-in
// goldens, kept in step by internal/auth's TestAuthPageGolden), served through CDP request interception together with canned
// replies for #3949's routes; the script is the real /static/auth_page.js. Run against an isolated dev server.
const assert = require('node:assert/strict');
const { safePaused } = require('./helpers/paused');
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
    // A poll that lands while the page is navigating is refused by Chrome: the wait looks again on the new document (helpers/wait.js isNavigationError, #4858, #4879).
    const { isNavigationError } = require('./helpers/wait');
    const waitFor = async (expr, what, ms = 6000) => {
        const end = Date.now() + ms;
        while (Date.now() < end) {
            let ok = false;
            try { ok = await ev(expr); } catch (e) { if (!isNavigationError(e)) throw e; }
            if (ok) return;
            await sleep(20);
        }
        throw new Error('timed out waiting for ' + what);
    };
    // The redirect under test is a navigation to return_to, so the wait is for the address, not for the stub's body: the stub answers /zz-done, but in cdp-gate 39053381027 the
    // browser's navigation to it was answered by the real server (it is in the navigation log and not in the served one) and a wait for the stub's #done timed out on a page that
    // was at /zz-done, complete.
    const DONE = `location.pathname === '/zz-done' && document.readyState === 'complete'`;
    let navSeq = 0;                // the navigation open() last started: the stamp on the page served for it
    let page = 'signin';            // which golden /auth serves
    const posts = [];              // {url, body}
    let replies = [];              // queue of {status, body, headers} for the next POSTs
    let statusReplies = [];        // the link poll's GET /auth/hub/link/status answers, in order (the last one repeats)
    const json = (status, body, headers = {}) => ({ status, body, headers });
    try {
        await Promise.all([Page.enable(), Runtime.enable(), client.Network.enable()]);
        // The dashboard's service worker (scope '/') answers page navigations with its own fetch, which this page's Fetch
        // interception never sees, so once it is active /auth and /zz-done reach the real server: no stamped page ("timed out
        // waiting for this navigation's page (create)") and the "stub never saw /zz-done" of cdp-gate 39053381027. It installs
        // when any suite in the shard loads '/', so whether it was active depended on the order. Make it active here, always,
        // and bypass it: every navigation below goes through the interception whatever ran before.
        await Page.navigate({ url: BASE + '/' });
        await waitFor(`!!navigator.serviceWorker && Promise.race([navigator.serviceWorker.ready.then(r => !!r.active && !!navigator.serviceWorker.controller), new Promise(r => setTimeout(() => r(false), 100))])`, 'the dashboard\'s service worker to control the page', 10000);
        await client.Network.setBypassServiceWorker({ bypass: true });
        // What navigated the page, for a failure's report (the replaced-page flake): every document request the browser made (who started it, whether it is a redirect hop and
        // from where) and every request this test served, newest last.
        const navLog = []; const servedLog = [];
        const keep = (log, x) => { log.push(x); if (log.length > 14) log.shift(); };
        client.Network.requestWillBeSent(e => { if (e.type === 'Document') keep(navLog, `${e.request.method} ${e.request.url.replace(BASE, '')} by ${e.initiator.type}${e.redirectResponse ? ' redirect-from ' + e.redirectResponse.url.replace(BASE, '') + ' ' + e.redirectResponse.status : ''} @${navSeq}`); });
        await Fetch.enable({ patterns: [{ urlPattern: BASE + '/auth*' }, { urlPattern: BASE + '/zz-done*' }] });
        safePaused(client, async p => {
            const u = new URL(p.request.url);
            keep(servedLog, `${p.request.method} ${u.pathname}${u.search} @${navSeq}${p.redirectedRequestId ? ' (redirect hop)' : ''}`);
            const fulfill = (status, body, type, headers = {}) => Fetch.fulfillRequest({ requestId: p.requestId, responseCode: status, responseHeaders: [{ name: 'Content-Type', value: type }, ...Object.entries(headers).map(([name, value]) => ({ name, value }))], body: Buffer.from(body).toString('base64') });
            if (u.pathname === '/zz-done') return fulfill(200, '<title>done</title><p id="done">done</p>', 'text/html');
            if (u.pathname === '/auth/hub/link/status') { const r = statusReplies.length > 1 ? statusReplies.shift() : statusReplies[0]; return fulfill(200, JSON.stringify(r || { state: 'enrolling', ready: false, error: '' }), 'application/json'); }
            if (p.request.method === 'POST') {
                posts.push({ url: u.pathname, body: JSON.parse(p.request.postData || '{}') });
                const r = replies.shift() || json(200, { ok: true });
                return fulfill(r.status, JSON.stringify(r.body), 'application/json', r.headers);
            }
            // Each page served is stamped with the navigation it was served for (open's navSeq), so a test
            // waits for ITS page, never for whichever document happens to be loaded.
            const gold = GOLD(page);
            return fulfill(200, gold.includes('<head>') ? gold.replace('<head>', `<head><meta name="zz-nav" content="${navSeq}">`) : `<meta name="zz-nav" content="${navSeq}">` + gold, 'text/html; charset=utf-8');
        });
        // Say what the page was when a wait gave up, so a rare failure on a slow runner names its cause: this document or a replacement, what it was sent, and what the
        // browser navigated to and the test served (the last 14 of each). open()'s waits use it too: the create-page flake failed inside open() with no report (#4493).
        const failReport = async (e) => {
            const d = await ev(`({ href: location.href, ready: document.readyState, doc: window.__zzDoc || 'replaced', stamp: (document.querySelector('meta[name=zz-nav]') || {}).content || '', pw: (document.getElementById('password') || {}).value, err: (document.getElementById('err') || {}).textContent })`).catch(x => ({ evalError: String(x.message).slice(0, 120) }));
            throw new Error(`${e.message}: ${JSON.stringify({ ...d, navSeq, posts: posts.map(p => p.url), navLog, servedLog })}`);
        };
        const waitR = async (expr, what, ms) => { try { await waitFor(expr, what, ms); } catch (e) { await failReport(e); } };
        let nextReplies = [];
        const openWith = (name, query, r) => { nextReplies = r; return open(name, query); };
        const open = async (name, query = '?return_to=/zz-done') => {
            page = name; posts.length = 0; replies = nextReplies; nextReplies = [];
            // The previous step may leave a navigation in flight (a redirect to '/' that has committed, its page's
            // own loads). Wait for the document served for THIS navigation, by the stamp it carries: a load event
            // cannot say which document it was for, and a late one from the old page let the checks below run on
            // it (the flake: "false !== true" on the setup page, "timed out waiting for redirect after creating").
            const seq = ++navSeq;
            await Page.navigate({ url: BASE + '/auth' + query });
            await waitR(`document.readyState === 'complete' && !!document.querySelector('meta[name=zz-nav]') && document.querySelector('meta[name=zz-nav]').content === '${seq}'`, `this navigation's page (${name})`);
            await waitR(`!!document.getElementById('submit') || !!document.getElementById('key-submit')`, 'the page');
            await waitR(`document.documentElement.dataset.authReady === 'true'`, 'the page script');   // the module script runs after load
            await ev(`window.__zzDoc = '${seq}'; true`);   // survives only while this document does: a replacement by a late navigation shows in a failure's report
            assert.equal(await ev(`(() => { const i = document.querySelector('.brand img'); return !!i && i.complete && i.naturalWidth > 0; })()`), true, `${name}: Coral's logo loads (from /static, with no credential)`);
        };
        // A 409 makes the page reload itself. A test that goes on after seeing the markup it expects can be looking at the OLD document (the hub page already holds the
        // form a 409 reloads to) or at the reload while it is still loading, and the next open()'s navigation then races it: two documents carry the same stamp, the
        // first one satisfies the wait and the second replaces it under the test. So mark the document before the submit, and wait for a NEW one that is fully loaded and
        // has run its script, then for the markup (#4414).
        const markOld = () => ev(`window.__zzOld = true; true`);
        const reloaded = (cond, what) => waitFor(`!window.__zzOld && document.readyState === 'complete' && document.documentElement.dataset.authReady === 'true' && (${cond})`, what);
        const $ = (id, prop = 'value') => ev(`document.getElementById('${id}').${prop}`);
        const type = (id, v) => ev(`(() => { const i = document.getElementById('${id}'); i.value = ${JSON.stringify(v)}; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
        const submit = id => ev(`document.getElementById('${id}').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); true`);
        const visible = id => ev(`(() => { const e = document.getElementById('${id}'); return !!e && !e.hidden && e.getClientRects().length > 0; })()`);
        const text = id => ev(`document.getElementById('${id}').textContent`);

        // ── sign in ──
        await open('signin');
        await waitFor(`document.activeElement && document.activeElement.id === 'password'`, 'focus to start in the password');
        // text contrast on the card, both themes: the links and the Show button must reach AA (4.5:1); the light theme's accent was 4.4:1 (#4023)
        const ratio = theme => ev(`(() => { if (!document.getElementById('zz-notransition')) { const st = document.createElement('style'); st.id = 'zz-notransition'; st.textContent = '* { transition: none !important }'; document.head.appendChild(st); } document.documentElement.setAttribute('data-theme', '${theme}');
            const parse = c => c.match(/[\\d.]+/g).map(Number); const lum = ([r, g, b]) => { const f = v => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; }; return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b); };
            const bg = parse(getComputedStyle(document.querySelector('.card')).backgroundColor); const out = {};
            for (const sel of ['#show-forgot', '#show-key', '.pw-toggle']) { const e = document.querySelector(sel); const fg = parse(getComputedStyle(e).color); const [x, y] = [lum(fg), lum(bg)].sort((p, q) => q - p); out[sel] = Math.round(((x + 0.05) / (y + 0.05)) * 100) / 100; }
            return out; })()`);
        for (const theme of ['dark', 'light']) {
            const r = await ratio(theme);
            for (const [sel, v] of Object.entries(r)) assert.ok(v >= 4.5, `${theme}: ${sel} is ${v}:1 on the card, AA needs 4.5`);
        }
        await ev(`document.documentElement.setAttribute('data-theme', 'dark'); true`);   // autofocus lands a moment after the page script
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
        assert.match(await text('count'), /^0:0[12]$/, 'the countdown shows (a slow runner reads it a second in)'); assert.equal(await $('password', 'disabled'), true); assert.equal(await $('submit', 'disabled'), true);
        await waitFor(`document.getElementById('lock').hidden && !document.getElementById('password').disabled`, 'the form opening again', 5000);
        // success: keep = true goes to return_to
        replies = [json(200, { ok: true })];
        await type('password', 'right'); await ev(`document.getElementById('keep').click(); true`); await submit('signin-form');
        await waitFor(DONE, 'the redirect to return_to');
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
        await submit('key-form'); await waitFor(DONE, 'key redirect');
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
        // The fallback is the navigation to '/' (the dashboard then routes its address to a view, /agents, as soon as it has
        // loaded: waiting for location.pathname === '/' raced that), so wait for the browser's own navigation to it.
        { const end = Date.now() + 6000; while (!navLog.some(x => x.startsWith('GET / ') && x.endsWith(`@${navSeq}`))) { if (Date.now() > end) await failReport(new Error('timed out waiting for a safe fallback for an unsafe return_to')); await sleep(20); } }
        assert.ok((await ev('location.host')).length > 0 && !(await ev('location.host')).includes('evil'));
        // That safe fallback is the real dashboard at '/', still loading. It can redirect again (to /auth, which this test serves) a moment later, and a late
        // redirect like that replaced the NEXT step's page under it with a freshly stamped one (the flake: 'timed out waiting for redirect after creating'). End it
        // here: go to the page this test serves for done, which has no script, and wait for it (#4414).
        await Page.navigate({ url: BASE + '/zz-done' });
        await waitFor(DONE, 'the dashboard page to be replaced by the done page');

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
        try { await waitFor(DONE, 'redirect after creating'); } catch (e) { await failReport(e); }
        assert.deepEqual(posts[0], { url: '/auth/password/create', body: { password: '  a b  ', keep: false } }, 'any non-empty password, exactly as typed, no code');
        // 409: someone set one first -> the page reloads as the sign-in page
        await open('create');
        replies = [json(409, { code: 'password_set', error: 'A password is already set' })];
        page = 'signin';   // what the server serves after the race
        await type('password', 'x'); await type('confirm', 'x'); await markOld(); await submit('create-form');
        await reloaded(`!!document.getElementById('signin-form')`, 'the sign-in page after a 409');

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
        await markOld(); await submit('hub-link-form');
        await reloaded(`!!document.getElementById('hub-start-form')`, 'a 409 reloads as the page that offers the sign-in');
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
