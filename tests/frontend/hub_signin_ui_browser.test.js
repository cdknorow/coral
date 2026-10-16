// Sign in with Coral Hub on the dashboard (#3966): Settings > Browser sign-in (the switch, the return addresses, the active Hub
// sign-ins) and the one-time notice, against a stubbed #3966 API. Checks: nothing shows until the computer is linked; the switch writes
// only PUT /api/auth/hub-signin (never the settings save) and the state shown is what the server then says; return-address
// errors come from the server; Sign out is DELETE /api/auth/sessions/<id>; Hub labels are text, never markup; the notice is offered
// once, after the set-a-password prompt, and Not now is final. Run against an isolated dev server.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async x => { const r = await Runtime.evaluate({ expression: x, returnByValue: true, awaitPromise: true }); if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400)); return r.result.value; };
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await ev(`new Promise(r => { const w = () => typeof window.selectLiveSession === 'function' ? r() : setTimeout(w, 50); w(); })`);
        // the app's own startup (its set-a-password and Coral Hub prompts) must be over before the stubs go in
        await ev(`(async () => { await window.__coralReady; if (window.__coralReady.startup) await window.__coralReady.startup('liveListLoaded'); await new Promise(r => setTimeout(r, 400)); const later = document.getElementById('set-password-later'); if (later) later.click(); /* the first-run prompt of a server with no password */ return true; })()`); // sleep-ok: the app's own startup prompts have no completion event to wait for
        const out = await ev(`(async () => {
            const S = window.__coralStatic || '/static/';
            const sleep = ms => new Promise(r => setTimeout(r, ms));
            const waitFor = async (cond, what, ms = 4000) => { const end = performance.now() + ms; while (!cond()) { if (performance.now() > end) throw new Error('timed out: ' + what); await sleep(10); } };
            const RealFetch = window.fetch;
            const calls = [];
            const hostile = '<img src=x onerror=window.__pwned=1>';
            const world = { hub: { enabled: false, linked: true, offer: false, owner_known: true }, origins: ['https://dev-mac.tailnet.example:8420'], failPut: null, originError: null,
                sessions: [
                  { id: 'aaaaaaaaaaaa', label: 'Chrome on macOS', method: 'password', created_at: '2026-10-16T09:00:00Z', last_seen: new Date().toISOString(), expires_at: new Date(Date.now() + 29.5 * 86400000).toISOString(), persistent: true, current: true },
                  { id: 'bbbbbbbbbbbb', label: 'Coral Hub: ' + hostile, method: 'hub', created_at: '2026-10-16T09:00:00Z', last_seen: new Date(Date.now() - 720000).toISOString(), expires_at: new Date(Date.now() + 7 * 3600000).toISOString(), persistent: false, current: false },
                  { id: 'cccccccccccc', label: 'Coral Hub: Ada Example', method: 'hub', created_at: '2026-10-16T08:00:00Z', last_seen: new Date(Date.now() - 3 * 3600000).toISOString(), expires_at: new Date(Date.now() + 5 * 3600000).toISOString(), persistent: false, current: false } ] };
            const json = (status, body) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
            window.fetch = async (u, o = {}) => {
                const url = String(u), m = (o.method || 'GET').toUpperCase();
                if (url.startsWith('/api/system/auth-status')) return json(200, { authenticated: true, method: 'session', password_set: true, local: true });
                if (url.startsWith('/api/auth/') || url.startsWith('/api/settings') && m === 'PUT') {
                    const body = o.body ? JSON.parse(o.body) : null;
                    if (m !== 'GET') calls.push({ m, url, body });
                    if (url === '/api/auth/hub-signin' && m === 'GET') return json(200, world.hub);
                    if (url === '/api/auth/hub-signin' && m === 'PUT') { if (world.failPut) return json(world.failPut.status, world.failPut.body); world.hub.enabled = body.enabled; return json(200, { ok: true }); }
                    if (url === '/api/auth/hub-signin/dismiss') { world.hub.offer = false; return json(200, { ok: true }); }
                    if (url === '/api/auth/hub-origins' && m === 'GET') return json(200, { origins: world.origins });
                    if (url === '/api/auth/hub-origins' && m === 'PUT') { if (world.originError) return json(400, world.originError); world.origins = body.origins; return json(200, { ok: true }); }
                    if (url === '/api/auth/sessions' && m === 'GET') return json(200, { sessions: world.sessions, recent: [] });
                    if (m === 'DELETE' && url.startsWith('/api/auth/sessions/')) { const id = url.split('/').pop(); world.sessions = world.sessions.filter(s => s.id !== id); return json(200, { ok: true }); }
                    return json(404, { error: 'no stub for ' + m + ' ' + url });
                }
                return RealFetch(u, o);
            };
            document.body.insertAdjacentHTML('beforeend', '<div id="settings-security" class="tb-row sec-row" role="group" hidden></div>');
            const sec = await import(S + 'security_settings.js');
            const row = document.getElementById('settings-security');
            const $ = id => document.getElementById(id);
            const out = {};
            const toasts = () => [...document.querySelectorAll('.toast .toast-message')].map(t => t.textContent);

            // 1. not linked: no Hub block
            world.hub.linked = false;
            await sec.loadSecurityRow(); await sleep(80);
            out.unlinked = !!row.querySelector('#sec-hub');
            // 2. linked, off
            world.hub.linked = true;
            await sec.loadSecurityRow(); await waitFor(() => $('sec-hub'), 'the Hub block');
            out.initial = { checked: $('sec-hub-enabled').checked, warn: $('sec-hub-warn').textContent, origins: [...document.querySelectorAll('#sec-hub-origins .sec-item')].map(li => li.textContent.replace(/\\s+/g, ' ').trim()),
                hubRows: [...document.querySelectorAll('#sec-hub-sessions .sec-item')].map(li => li.textContent.replace(/\\s+/g, ' ').trim()),
                genericHasHub: [...document.querySelectorAll('#sec-sessions .sec-item')].some(li => /Coral Hub/.test(li.textContent)),
                heading: [...row.querySelectorAll('.sec-sub-head')].map(h => h.textContent) };
            out.noInjected = { img: !!row.querySelector('img'), pwned: window.__pwned === undefined, text: row.textContent.includes(hostile) };
            // 3. the switch: PUT only through /api/auth/hub-signin, and the page then shows what the server says
            calls.length = 0;
            $('sec-hub-enabled').click();
            await waitFor(() => calls.some(c => c.m === 'PUT'), 'the switch request');
            await waitFor(() => $('sec-hub-enabled') && $('sec-hub-enabled').checked === true && !$('sec-hub-enabled').disabled, 'the switch reads back on');
            out.on = { calls: calls.filter(c => c.m !== 'GET').map(c => c.m + ' ' + c.url + ' ' + JSON.stringify(c.body)), settingsPuts: calls.filter(c => c.url.startsWith('/api/settings')).length };
            world.failPut = { status: 409, body: { code: 'owner_unknown', error: 'Coral Hub has not reported which account linked this computer.' } };
            $('sec-hub-enabled').click();
            await waitFor(() => toasts().some(t => /has not reported/.test(t)), 'the refusal toast');
            await waitFor(() => $('sec-hub-enabled') && !$('sec-hub-enabled').disabled, 'the switch is back');
            out.afterRefusal = $('sec-hub-enabled').checked;   // the server still says on: shows on
            world.failPut = null; world.hub.enabled = false; await sec.loadSecurityRow(); await waitFor(() => $('sec-hub') && !$('sec-hub-enabled').checked, 'off again');
            // 4. return addresses
            calls.length = 0;
            $('sec-hub-origin-add').click();
            out.emptyErr = $('sec-hub-origin-error').textContent; out.emptyCalls = calls.length;
            $('sec-hub-origin').value = 'ftp://nas.local';
            world.originError = { code: 'bad_origin', index: 1, error: 'Use scheme://host:port with https; http only for this computer or a private network address.' };
            $('sec-hub-origin-add').click();
            await waitFor(() => !$('sec-hub-origin-error').hidden, 'the origin error');
            out.badOrigin = { text: $('sec-hub-origin-error').textContent, put: calls[calls.length - 1], listed: document.querySelectorAll('#sec-hub-origins .sec-item').length };
            world.originError = null;
            $('sec-hub-origin').value = ' https://nas.tailnet.example:8420 ';
            $('sec-hub-origin-add').click();
            await waitFor(() => document.querySelectorAll('#sec-hub-origins .sec-item').length === 5, 'the origin is listed');
            out.added = calls[calls.length - 1];
            document.querySelector('#sec-hub-origins .sec-item button').click();
            await waitFor(() => document.querySelectorAll('#sec-hub-origins .sec-item').length === 4, 'the origin is removed');
            out.removed = calls[calls.length - 1];
            // 5. sign out one Hub sign-in
            calls.length = 0;
            [...document.querySelectorAll('#sec-hub-sessions .sec-item button')][0].click();
            await waitFor(() => document.querySelectorAll('#sec-hub-sessions .sec-item').length === 1, 'the session is gone');
            out.signOut = calls[0];

            // 6. the one-time notice
            const hn = await import(S + 'hub_notice.js');
            world.hub = { enabled: false, linked: true, offer: false, owner_known: true };
            await hn.maybeOfferHubSignin(); await sleep(60);
            out.noOffer = !!$('hub-signin-notice');
            world.hub.offer = true; calls.length = 0;
            await hn.maybeOfferHubSignin(); await waitFor(() => $('hub-signin-notice'), 'the notice');
            out.notice = { text: $('hub-signin-notice').textContent, focus: document.activeElement.id };
            $('hub-notice-no').click();
            await waitFor(() => calls.some(c => c.url.endsWith('/dismiss')), 'dismiss'); out.notNow = calls.map(c => c.m + ' ' + c.url); out.closed = !$('hub-signin-notice');
            world.hub.offer = true; calls.length = 0;
            await hn.maybeOfferHubSignin(); await waitFor(() => $('hub-signin-notice'), 'the notice again');
            document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
            await waitFor(() => calls.some(c => c.url.endsWith('/dismiss')), 'Escape is Not now'); out.escape = true;
            world.hub.offer = true; world.hub.enabled = false; calls.length = 0;
            await hn.maybeOfferHubSignin(); await waitFor(() => $('hub-signin-notice'), 'the notice a third time');
            $('hub-notice-yes').click();
            await waitFor(() => calls.some(c => c.url.endsWith('/dismiss')), 'yes then dismiss');
            out.yes = calls.map(c => c.m + ' ' + c.url + ' ' + JSON.stringify(c.body));
            // after the set-a-password prompt, never over it
            const pu = await import(S + 'password_ui.js');
            world.hub.offer = true; world.hub.enabled = false;
            pu.showSetPasswordModal({ local: true });
            await hn.maybeOfferHubSignin(); await sleep(80);
            out.waitsForPassword = !$('hub-signin-notice');
            $('set-password-later').click();
            await waitFor(() => $('hub-signin-notice'), 'the notice after the password prompt closes'); out.afterPassword = true;
            $('hub-notice-no').click();
            window.fetch = RealFetch; row.remove(); document.querySelectorAll('.modal').forEach(m => { if (/notice|set-password/.test(m.id)) m.remove(); });
            return out;
        })()`);
        assert.equal(out.unlinked, false, 'no Hub block until the computer is linked');
        assert.equal(out.initial.checked, false, 'off by default');
        assert.match(out.initial.warn, /This lets your Coral Hub account run commands on this computer\./, 'the plain warning is visible with the switch');
        assert.deepEqual(out.initial.origins.slice(0, 3).map(t => t.replace(/:\d+always$/, ':PORTalways')), ['http://localhost:PORTalways', 'http://127.0.0.1:PORTalways', 'http://[::1]:PORTalways'], 'the three loopback origins are always return addresses: ' + out.initial.origins.slice(0, 3));
        assert.ok(out.initial.origins[3].startsWith('https://dev-mac.tailnet.example:8420'));
        assert.equal(out.initial.hubRows.length, 2, 'both active Hub sign-ins are listed');
        assert.equal(out.initial.genericHasHub, false, 'and not twice');
        assert.deepEqual(out.noInjected, { img: false, pwned: true, text: true }, 'a hostile Hub display name is text');
        assert.deepEqual(out.on.calls, ['PUT /api/auth/hub-signin {"enabled":true}'], 'the switch writes only through PUT /api/auth/hub-signin');
        assert.equal(out.on.settingsPuts, 0, 'never through the settings save');
        assert.equal(out.afterRefusal, true, 'after a refusal the page shows what the server says (still on)');
        assert.equal(out.emptyErr, 'Enter an address.'); assert.equal(out.emptyCalls, 0);
        assert.match(out.badOrigin.text, /Use scheme:\/\/host:port with https/); assert.equal(out.badOrigin.listed, 4, 'a refused address is not listed');
        assert.deepEqual(out.badOrigin.put.body, { origins: ['https://dev-mac.tailnet.example:8420', 'ftp://nas.local'] });
        assert.deepEqual(out.added.body, { origins: ['https://dev-mac.tailnet.example:8420', 'https://nas.tailnet.example:8420'] }, 'the address is trimmed and added to the list');
        assert.deepEqual(out.removed.body, { origins: ['https://nas.tailnet.example:8420'] }, 'Remove sends the list without it');
        assert.deepEqual([out.signOut.m, out.signOut.url, out.signOut.body], ['DELETE', '/api/auth/sessions/bbbbbbbbbbbb', null], 'Sign out is DELETE on the listed id, with no body');
        assert.equal(out.noOffer, false, 'no notice unless the server offers it');
        assert.match(out.notice.text, /full control of this computer/); assert.equal(out.notice.focus, 'hub-notice-yes');
        assert.deepEqual(out.notNow, ['POST /api/auth/hub-signin/dismiss'], 'Not now only dismisses, it turns nothing on'); assert.ok(out.closed);
        assert.ok(out.escape);
        assert.deepEqual(out.yes, ['PUT /api/auth/hub-signin {"enabled":true}', 'POST /api/auth/hub-signin/dismiss {}']);
        assert.ok(out.waitsForPassword, 'the notice waits while the set-a-password prompt is open'); assert.ok(out.afterPassword);
        await ev(`localStorage.removeItem('coral-password-not-now'); true`);
        console.log('PASS Hub sign-in UI: Settings block, switch, return addresses, sign out, one-time notice');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
