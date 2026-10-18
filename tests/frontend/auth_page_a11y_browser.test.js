// The first screen a browser user sees (#4360, from the #4356 first-run audit): the sign-in and create-password pages. A refused field is marked
// aria-invalid, described by its message and focused (focus used to stay on the page body); the first field has focus when the page opens; the page has
// an icon (no /favicon.ico 404); with the root font doubled at 390 px the text grows and nothing scrolls sideways; and every control is a 44 px target.
// The page is the server's own markup (the checked-in goldens, kept in step by internal/auth's TestAuthPageGolden) served through CDP request interception,
// the script and styles are the real /static files. SHOT_DIR=<dir> saves the create page at 200%. Run against an isolated dev server.
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
    const ev = async x => { const r = await Runtime.evaluate({ expression: x, returnByValue: true, awaitPromise: true }); if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails)); return r.result.value; };
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: layout settles after a font-size change; there is no completion event
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
    // The focus lands when the page script has run, a moment after the load event: wait for it (bounded) instead of reading it once, which lost the race on a slower runner.
    const focusOn = async (id, what) => { try { await waitFor(`document.activeElement && document.activeElement.id === ${JSON.stringify(id)}`, what, 4000); } catch (e) { assert.equal(await ev(`document.activeElement ? document.activeElement.id : ''`), id, what); } };
    const ok = name => console.log(`   ✓ ${name}`);
    let page = 'signin';
    let reply = { status: 401, body: { error: 'wrong' } };
    try {
        await Promise.all([Page.enable(), Runtime.enable(), client.Network.enable()]);
        // The dashboard's service worker answers page navigations with its own fetch, which the Fetch interception below never
        // sees: once any suite in the shard has loaded '/', /auth would reach the real server (auth_page_browser's create-page
        // flake). Bypass it.
        await client.Network.setBypassServiceWorker({ bypass: true });
        await Fetch.enable({ patterns: [{ urlPattern: BASE + '/auth*' }] });
        safePaused(client, p => {
            const u = new URL(p.request.url);
            if (p.request.method === 'POST') return Fetch.fulfillRequest({ requestId: p.requestId, responseCode: reply.status, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }], body: Buffer.from(JSON.stringify(reply.body)).toString('base64') });
            if (u.pathname !== '/auth') return Fetch.continueRequest({ requestId: p.requestId });
            return Fetch.fulfillRequest({ requestId: p.requestId, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'text/html; charset=utf-8' }], body: Buffer.from(GOLD(page)).toString('base64') });
        });
        const open = async (name, w = 390) => {
            page = name;
            await Emulation.setDeviceMetricsOverride({ width: w, height: 844, deviceScaleFactor: 1, mobile: true });
            await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
            await Page.navigate({ url: BASE + '/auth' }); await Page.loadEventFired();
            await waitFor(`document.documentElement.dataset.authReady === 'true'`, 'the page script');
        };
        const active = () => ev(`document.activeElement ? document.activeElement.id : ''`);
        const state = id => ev(`(() => { const e = document.getElementById('${id}'); return { invalid: e.getAttribute('aria-invalid'), by: e.getAttribute('aria-describedby') }; })()`);
        const type = (id, v) => ev(`(() => { const i = document.getElementById('${id}'); i.value = ${JSON.stringify(v)}; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
        const submit = id => ev(`document.getElementById('${id}').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); true`);

        // ── sign in ──
        await open('signin');
        await focusOn('password', 'the password has focus when the page opens');
        assert.equal(await ev(`document.head.querySelector('link[rel=icon]') && document.head.querySelector('link[rel=icon]').getAttribute('href')`), '/static/favicon.png', 'the page names its icon');
        await ev(`document.getElementById('show-key').focus(); true`);
        await submit('signin-form');
        assert.equal(await ev(`document.getElementById('err').textContent`), 'Enter your password.');
        assert.equal(await active(), 'password', 'an empty submit puts focus on the password field');
        assert.deepEqual(await state('password'), { invalid: 'true', by: 'err' }, 'and marks it invalid, described by the message');
        await type('password', 'a');
        assert.equal((await state('password')).invalid, null, 'typing clears the mark');
        reply = { status: 401, body: { error: 'wrong' } };
        await ev(`document.getElementById('show-key').focus(); true`);
        await submit('signin-form');
        await waitFor(`document.getElementById('err').textContent === 'That password is not right.'`, 'the wrong-password message');
        assert.equal(await active(), 'password'); assert.equal((await state('password')).invalid, 'true');
        ok('sign in: focus starts in the password; an empty or wrong password marks it invalid, describes it and focuses it, typing clears it; the page names its icon');

        // ── the API key panel ──
        await ev(`document.getElementById('show-key').click(); true`);
        await submit('key-form');
        assert.equal(await ev(`document.getElementById('key-err').textContent`), 'Paste the API key.');
        assert.equal(await active(), 'api-key'); assert.deepEqual(await state('api-key'), { invalid: 'true', by: 'key-err' });
        ok('the API key panel: an empty submit says so and focuses the key field');

        // ── create a password ──
        await open('create');
        await focusOn('password', 'a local create page opens on the password');
        await submit('create-form');
        assert.equal(await active(), 'password'); assert.deepEqual(await state('password'), { invalid: 'true', by: 'strength err' });
        await type('password', 'a-password'); await type('confirm', 'different');
        await submit('create-form');
        assert.equal(await ev(`document.getElementById('err').textContent`), 'The two passwords do not match.');
        assert.equal(await active(), 'confirm', 'a mismatch focuses the confirmation'); assert.equal((await state('confirm')).invalid, 'true');
        ok('create: focus starts in the password; an empty password focuses it, a mismatch focuses and marks the confirmation');
        await open('setup');
        await focusOn('setup-code', 'a remote create page opens on the setup code');
        await submit('create-form');
        assert.equal(await ev(`document.getElementById('err').textContent`), 'Enter the setup code.'); assert.equal(await active(), 'setup-code');
        ok('create from another computer: focus starts in the setup code; an empty one is marked and focused');

        // ── 200% text at 390 px ──
        for (const name of ['signin', 'create']) {
            await open(name);
            const sizes = async () => ev(`(() => { const f = s => { const e = document.querySelector(s); return e ? parseFloat(getComputedStyle(e).fontSize) : null; }; return { h1: f('h1'), sub: f('.sub'), label: f('.pw-label'), btn: f('.btn.wide'), note: f('.note'), root: parseFloat(getComputedStyle(document.documentElement).fontSize) }; })()`);
            const at100 = await sizes();
            await ev(`document.documentElement.style.fontSize = '32px'; true`); await sleep(400); // sleep-ok: layout settles after a font-size change; there is no completion event
            const at200 = await sizes();
            for (const k of ['h1', 'sub', 'btn']) assert.ok(at200[k] / at100[k] >= 1.9, `${name}: ${k} grows with the root font (${at100[k]} -> ${at200[k]})`);
            const over = await ev(`document.documentElement.scrollWidth - innerWidth`);
            assert.ok(over <= 0, `${name}: no sideways scroll at 200% (${over})`);
            if (name === 'create' && process.env.SHOT_DIR) fs.writeFileSync(`${process.env.SHOT_DIR}/create-200.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
            await ev(`document.documentElement.style.fontSize = ''; true`); await sleep(300); // sleep-ok: layout settles after a font-size change; there is no completion event
            const small = await ev(`[...document.querySelectorAll('button, a[href], input:not([type=hidden]):not([type=checkbox]), select')].filter(e => e.getClientRects().length && !e.disabled && !e.closest('[hidden]')).map(e => { const b = e.getBoundingClientRect(); return { id: e.id || e.className, w: Math.round(b.width), h: Math.round(b.height) }; }).filter(x => x.h < 43.5 || x.w < 43.5)`);
            assert.deepEqual(small, [], `${name}: every control is 44 px at 390 px: ${JSON.stringify(small)}`);
        }
        ok('at 390 px the text grows with the root font (200%), nothing scrolls sideways, and every control is a 44 px target');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
