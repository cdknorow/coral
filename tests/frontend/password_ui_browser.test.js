// The browser-password screens on the dashboard (#3948): the one-time "Set a password" prompt and Settings > Browser sign-in,
// against a stubbed #3949 API. Checks: password-manager fields, Show/Hide, an advisory meter that never blocks, the operator's
// rule (any non-empty password, sent exactly as typed), "Not now" final on this computer only (the server's `local`), 409 and
// 429 replies, the sessions list rendered as text (a hostile User-Agent label), change and sign-out-others, hidden through
// the hub or when not signed in. Run against an isolated dev server.
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
        const out = await ev(`(async () => {
            const S = window.__coralStatic || '/static/';
            const sleep = ms => new Promise(r => setTimeout(r, ms));
            const waitFor = async (cond, what, ms = 4000) => { const end = performance.now() + ms; while (!cond()) { if (performance.now() > end) throw new Error('timed out: ' + what); await sleep(10); } };
            const RealFetch = window.fetch;
            const calls = [];
            let status = { authenticated: true, method: 'session', password_set: false, local: true };
            let reply = {};       // url -> {status, body, headers}
            let sessions = { sessions: [], recent: [] };
            window.fetch = async (u, o = {}) => {
                const url = String(u);
                if (url.startsWith('/api/system/auth-status')) return new Response(JSON.stringify(status), { status: 200 });
                if (url.startsWith('/api/auth/sessions') && !o.method) return window.__needSignIn ? new Response(JSON.stringify({ error: 'Sign in in this browser first', code: 'password_session_required' }), { status: 403 }) : new Response(JSON.stringify(sessions), { status: 200 });
                if (url === '/api/auth/hub-signin' && !o.method) return new Response(JSON.stringify({ enabled: false, linked: false, offer: false, owner_known: true }), { status: 200 });   // the app's own Hub notice check
                if (url.startsWith('/api/auth/')) {
                    calls.push({ url, body: o.body ? JSON.parse(o.body) : null });
                    const r = reply[url] || { status: 200, body: { ok: true } };
                    return new Response(JSON.stringify(r.body), { status: r.status, headers: r.headers || {} });
                }
                return RealFetch(u, o);
            };
            localStorage.removeItem('coral-password-not-now');
            const ui = await import(S + 'password_ui.js');
            const $ = id => document.getElementById(id);
            const type = (id, v) => { const i = $(id); i.value = v; i.dispatchEvent(new Event('input', { bubbles: true })); };
            const submit = () => $('set-password-form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
            const out = {};

            // 1. the prompt: password-manager fields, Show/Hide, advisory meter
            await ui.maybePromptSetPassword();
            await waitFor(() => $('set-password-modal'), 'the prompt');
            out.fields = { newAc: $('set-password-new').autocomplete, confAc: $('set-password-confirm').autocomplete, type: $('set-password-new').type,
                username: (() => { const u = document.querySelector('#set-password-form input[name=username]'); return u ? { ac: u.autocomplete, hidden: u.hidden } : null; })(),
                focused: document.activeElement.id };
            document.querySelector('#set-password-modal .pw-toggle').click();
            out.shown = $('set-password-new').type;
            document.querySelector('#set-password-modal .pw-toggle').click();
            out.hiddenAgain = $('set-password-new').type;
            type('set-password-new', 'a');
            out.weak = { level: document.querySelector('#set-password-modal .pw-strength').getAttribute('data-level'), disabled: $('set-password-save').disabled };
            // empty and mismatch send nothing
            type('set-password-new', ''); submit();
            out.emptyErr = $('set-password-error').textContent; out.callsAfterEmpty = calls.length;
            type('set-password-new', 'one'); type('set-password-confirm', 'two'); submit();
            out.mismatchErr = $('set-password-error').textContent; out.callsAfterMismatch = calls.length;
            // the rule: any non-empty password, exactly as typed (spaces kept, nothing trimmed)
            type('set-password-new', '  a b  '); type('set-password-confirm', '  a b  '); submit();
            await waitFor(() => !$('set-password-modal'), 'the prompt closes after a set (err=' + $('set-password-error').textContent + ', hidden=' + $('set-password-error').hidden + ', calls=' + JSON.stringify(calls) + ', btn=' + $('set-password-save').textContent + ')');
            out.setCall = calls[calls.length - 1];

            // 2. Not now: final on this computer, asked again from another
            status = { authenticated: true, method: 'session', password_set: false, local: true };
            await ui.maybePromptSetPassword(); await waitFor(() => $('set-password-modal'), 'prompt again');
            $('set-password-later').click();
            out.flagAfterLocalNotNow = localStorage.getItem('coral-password-not-now');
            await ui.maybePromptSetPassword(); await sleep(50);
            out.promptAfterLocalNotNow = !!$('set-password-modal');
            localStorage.removeItem('coral-password-not-now');
            status = { authenticated: true, method: 'key', password_set: false, local: false };
            await ui.maybePromptSetPassword(); await waitFor(() => $('set-password-modal'), 'remote prompt');
            document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));   // Escape is Not now
            out.flagAfterRemoteNotNow = localStorage.getItem('coral-password-not-now');
            await ui.maybePromptSetPassword(); await waitFor(() => $('set-password-modal'), 'remote asked again');
            out.remoteAskedAgain = true;
            // 409: a password already exists -> closes quietly; 429 shows the server's words and stays open
            reply['/api/auth/password/set'] = { status: 409, body: { code: 'password_set', error: 'A password is already set' } };
            type('set-password-new', 'x'); type('set-password-confirm', 'x'); submit();
            await waitFor(() => !$('set-password-modal'), '409 closes');
            out.closedOn409 = true;
            status = { authenticated: true, method: 'key', password_set: false, local: false };
            reply['/api/auth/password/set'] = { status: 429, body: { code: 'too_many_attempts', error: 'Too many attempts. Try again in 30 s.', retry_after: 30 }, headers: { 'Retry-After': '30' } };
            await ui.maybePromptSetPassword(); await waitFor(() => $('set-password-modal'), 'prompt for 429');
            type('set-password-new', 'x'); type('set-password-confirm', 'x'); submit();
            await waitFor(() => !$('set-password-error').hidden, '429 message');
            out.err429 = $('set-password-error').textContent; out.saveEnabledAfter429 = !$('set-password-save').disabled;
            $('set-password-later').click();
            // signed out or already set: no prompt
            for (const st of [{ authenticated: false, password_set: false, local: true }, { authenticated: true, password_set: true, local: true }]) {
                status = st; await ui.maybePromptSetPassword(); await sleep(30); if ($('set-password-modal')) out.promptWhen = JSON.stringify(st);
            }

            // 3. Settings > Browser sign-in
            document.body.insertAdjacentHTML('beforeend', '<div id="settings-security" class="tb-row sec-row" role="group" hidden></div>');
            const sec = await import(S + 'security_settings.js');
            const row = $('settings-security');
            status = { authenticated: false, password_set: true, local: true };
            await sec.loadSecurityRow(); out.hiddenSignedOut = row.hidden;
            status = { authenticated: true, method: 'localhost', password_set: false, local: true };
            await sec.loadSecurityRow(); out.noPassword = { hidden: row.hidden, btn: !!$('sec-set') };
            const hostile = '<img src=x onerror=window.__pwned=1>';
            sessions = { sessions: [
                { id: 'aaaaaaaaaaaa', label: 'Chrome on macOS', method: 'password', created_at: '2026-10-16T09:00:00Z', last_seen: new Date().toISOString(), expires_at: new Date(Date.now() + 29.5 * 86400000).toISOString(), persistent: true, current: true },
                { id: 'bbbbbbbbbbbb', label: hostile, method: 'link', created_at: '2026-10-10T09:00:00Z', last_seen: '2026-10-14T09:00:00Z', expires_at: new Date(Date.now() + 5 * 3600000).toISOString(), persistent: false, current: false } ],
                recent: [{ at: '2026-10-16T10:00:00Z', address: '10.0.0.5', label: hostile, ok: false }, { at: '2026-10-16T09:00:00Z', address: '127.0.0.1', label: 'Chrome on macOS', ok: true }] };
            status = { authenticated: true, method: 'session', password_set: true, local: true };
            await sec.loadSecurityRow();
            await waitFor(() => $('sec-sessions'), 'the sessions list');
            out.rows = [...document.querySelectorAll('#sec-sessions .sec-item')].map(li => li.textContent);
            out.noInjected = { img: !!row.querySelector('img'), pwned: window.__pwned === undefined, hostileShownAsText: row.textContent.includes(hostile) };
            out.changeFields = { cur: $('sec-current').autocomplete, nw: $('sec-new').autocomplete, user: !!row.querySelector('input[name=username][autocomplete=username]') };
            // change: wrong current password -> 403 text; right -> posts both and reports the other sessions ended
            reply['/api/auth/password/change'] = { status: 403, body: { code: 'wrong_password', error: 'The current password is wrong' } };
            row.querySelector('#sec-change').open = true;
            const setv = (id, v) => { $(id).value = v; $(id).dispatchEvent(new Event('input', { bubbles: true })); };
            const sub = () => row.querySelector('.pw-form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
            setv('sec-current', 'old pw'); setv('sec-new', 'n'); setv('sec-confirm', 'n'); sub();
            await waitFor(() => !$('sec-error').hidden, 'wrong current');
            out.wrongCurrent = $('sec-error').textContent;
            reply['/api/auth/password/change'] = { status: 200, body: { ok: true, signed_out: 2 } };
            const before = calls.length; sub();
            await waitFor(() => calls.length > before, 'the change request');
            out.changeCall = calls[calls.length - 1];
            await waitFor(() => [...document.querySelectorAll('.toast .toast-message')].some(t => /Password changed\\. 2 other browsers signed out/.test(t.textContent)), 'the change toast');
            // sign out others
            reply['/api/auth/sessions/sign-out-others'] = { status: 200, body: { ok: true, signed_out: 1 } };
            const n0 = calls.length; $('sec-signout-others').click();
            await waitFor(() => calls.length > n0, 'sign out others');
            out.signOutCall = calls[calls.length - 1].url;
            // a caller with no browser sign-in: the list says so inline, never "signed out" (a 403, not dashboard_auth_required)
            window.__needSignIn = true;
            await sec.loadSecurityRow(); await waitFor(() => $('settings-security') && !$('settings-security').hidden && /Sign in here to manage browsers/.test(row.textContent), 'the inline note');
            out.needSignInNote = /Sign in here to manage browsers\./.test(row.textContent);
            out.noSignedOutPanel = !document.getElementById('signed-out-panel') && !document.querySelector('dialog[open]');
            window.__needSignIn = false;
            window.fetch = RealFetch; $('set-password-modal')?.remove(); row.remove();
            return out;
        })()`);
        assert.equal(out.fields.newAc, 'new-password'); assert.equal(out.fields.confAc, 'new-password');
        assert.deepEqual(out.fields.username, { ac: 'username', hidden: true }, 'a username field for the password manager');
        assert.equal(out.fields.type, 'password'); assert.equal(out.fields.focused, 'set-password-new', 'focus moves to the password');
        assert.equal(out.shown, 'text'); assert.equal(out.hiddenAgain, 'password', 'Show then Hide');
        assert.deepEqual(out.weak, { level: 'weak', disabled: false }, 'the meter advises and never blocks');
        assert.equal(out.emptyErr, 'Enter a password.'); assert.equal(out.callsAfterEmpty, 0);
        assert.match(out.mismatchErr, /do not match/); assert.equal(out.callsAfterMismatch, 0, 'nothing is sent for a mismatch');
        assert.deepEqual(out.setCall, { url: '/api/auth/password/set', body: { password: '  a b  ' } }, 'sent exactly as typed, spaces kept');
        assert.equal(out.flagAfterLocalNotNow, '1', 'Not now is final on this computer'); assert.equal(out.promptAfterLocalNotNow, false);
        assert.equal(out.flagAfterRemoteNotNow, null, 'from another computer Not now is not remembered'); assert.ok(out.remoteAskedAgain);
        assert.ok(out.closedOn409);
        assert.equal(out.err429, 'Too many attempts. Try again in 30 s.'); assert.ok(out.saveEnabledAfter429);
        assert.equal(out.promptWhen, undefined, 'no prompt when signed out or a password is set: ' + out.promptWhen);
        assert.equal(out.hiddenSignedOut, true); assert.deepEqual(out.noPassword, { hidden: false, btn: true });
        assert.equal(out.rows.length, 2);
        assert.match(out.rows[0], /Chrome on macOS \(this browser\).*now.*29 days left/);
        assert.match(out.rows[1], /until it closes/);
        assert.deepEqual(out.noInjected, { img: false, pwned: true, hostileShownAsText: true }, 'a hostile label is text, never markup');
        assert.deepEqual(out.changeFields, { cur: 'current-password', nw: 'new-password', user: true });
        assert.equal(out.wrongCurrent, 'The current password is wrong');
        assert.deepEqual(out.changeCall, { url: '/api/auth/password/change', body: { current: 'old pw', password: 'n' } });
        assert.equal(out.signOutCall, '/api/auth/sessions/sign-out-others');
        assert.ok(out.needSignInNote, 'a 403 password_session_required shows an inline note'); assert.ok(out.noSignedOutPanel, 'and never the signed-out panel');
        console.log('PASS browser password: prompt, Not now rules, settings list as text, change, sign out others');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
