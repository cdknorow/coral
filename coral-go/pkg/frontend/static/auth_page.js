/* The /auth page's behaviour (#3948): sign in with the password, create the first password (with the setup code from another
 * computer), the forgot-password help, and the API key fallback. The markup is internal/auth/auth_page.go; the server picks the
 * state (data-password-set, data-local on <body>), so this script asks no status question. It talks to #3949's routes:
 * POST /auth/password, POST /auth/password/create, POST /auth/key. A password is any non-empty text and is sent exactly as typed. */

import { importOnce } from './import_once.js';
const { passwordProblem, strengthOf, retryAfterSeconds, formatCountdown } = await importOnce('./password_rules.js');

const $ = id => document.getElementById(id);
const passwordSet = document.body.dataset.passwordSet === 'true';

// A path on this origin only (auth.SafeReturnTo): one leading "/", no backslash ("/\host" is another host to a browser), no control character.
const params = new URLSearchParams(window.location.search);
const requestedReturnTo = params.get('return_to');
const returnTo = requestedReturnTo && requestedReturnTo.startsWith('/') && !requestedReturnTo.startsWith('//')
    && !requestedReturnTo.includes('\\') && !/[\u0000-\u001f\u007f]/.test(requestedReturnTo) ? requestedReturnTo : '/';

async function post(url, body) {
    const resp = await fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, credentials: 'same-origin', body: JSON.stringify(body) });
    let data = {};
    try { data = await resp.json(); } catch { /* an empty or non-JSON body */ }
    return { ok: resp.ok, status: resp.status, data: data || {}, retryAfter: resp.headers.get('Retry-After') };
}

const main = $('panel-signin') || $('panel-create');
function showPanel(panel) {
    for (const p of [main, $('panel-forgot'), $('panel-key'), $('panel-hublink'), $('panel-hubwait')]) if (p) p.hidden = p !== panel;
    const focus = panel && panel.querySelector('input:not([hidden]):not([type=checkbox])');
    if (focus) focus.focus();
}
function setError(el, text) { if (!el) return; el.hidden = !text; el.textContent = text || ''; }
function say(text) { $('live').textContent = text; }
function done() { say('Signed in. Opening Coral…'); window.location.replace(returnTo); }

// A paused form: counts down from the server's wait and opens again by itself.
let timer = null;
function lock(seconds) {
    const form = $('signin-form') || $('create-form');
    const box = $('lock'), count = $('count');
    let left = Math.max(1, Math.ceil(seconds));
    const set = on => { for (const el of form.querySelectorAll('input:not([hidden]), button')) el.disabled = on; };
    set(true); box.hidden = false; count.textContent = formatCountdown(left);
    clearInterval(timer);
    timer = setInterval(() => { // interval-ok: the lockout's one-second countdown on the sign-in page, which has no dashboard pollers to pause
        left -= 1; count.textContent = formatCountdown(left);
        if (left <= 0) { clearInterval(timer); set(false); box.hidden = true; const f = form.querySelector('#password'); if (f) f.focus(); }
    }, 1000);
}
const waitFor = res => (Number(res.data.retry_after) || retryAfterSeconds(res.retryAfter) || 60);
const NETWORK = 'Could not reach Coral. Check that it is running, then try again.';
const CROSS = 'Sign-in has to start from this page, at the address in the address bar.';

// Show / Hide on a password field
for (const b of document.querySelectorAll('[data-toggle]')) {
    b.addEventListener('click', () => {
        const input = $(b.dataset.toggle), show = input.type === 'password';
        input.type = show ? 'text' : 'password';
        b.textContent = show ? 'Hide' : 'Show';
        b.setAttribute('aria-pressed', String(show));
        b.setAttribute('aria-label', `${show ? 'Hide' : 'Show'} ${b.dataset.toggle === 'confirm' ? 'confirm password' : 'password'}`);
    });
}
for (const b of document.querySelectorAll('[data-back]')) b.addEventListener('click', () => showPanel(main));
if ($('show-forgot')) $('show-forgot').addEventListener('click', () => showPanel($('panel-forgot')));
if ($('show-key')) $('show-key').addEventListener('click', () => showPanel($('panel-key')));
if ($('copy-reset')) $('copy-reset').addEventListener('click', async () => {
    const btn = $('copy-reset');
    try { await navigator.clipboard.writeText($('reset-cmd').textContent); btn.textContent = 'Copied'; }
    catch { const r = document.createRange(); r.selectNodeContents($('reset-cmd')); const s = window.getSelection(); s.removeAllRanges(); s.addRange(r); btn.textContent = 'Press Ctrl+C'; }
    setTimeout(() => { btn.textContent = 'Copy'; }, 2000);
});

if (passwordSet) {
    $('signin-form').addEventListener('submit', async e => {
        e.preventDefault();
        const err = $('err'), pw = $('password');
        const problem = passwordProblem(pw.value);
        setError(err, problem === 'Enter a password.' ? 'Enter your password.' : ''); pw.classList.remove('bad');
        if (problem) return;
        $('submit').disabled = true;
        let res;
        try { res = await post('/auth/password', { password: pw.value, keep: $('keep').checked }); } catch { setError(err, NETWORK); $('submit').disabled = false; return; }
        if (res.ok) return done();
        $('submit').disabled = false;
        if (res.status === 429) { setError(err, ''); return lock(waitFor(res)); }
        if (res.status === 401) { setError(err, 'That password is not right.'); pw.classList.add('bad'); pw.focus(); pw.select(); return; }
        setError(err, res.status === 403 ? CROSS : (res.data.error || 'Could not sign in.'));
    });
} else {
    const pw = $('password'), confirm = $('confirm'), code = $('setup-code'), meter = $('meter');
    pw.addEventListener('input', () => {
        const s = strengthOf(pw.value);
        meter.setAttribute('data-level', s.level);
        meter.querySelector('i').style.width = `${s.pct}%`;
        const hint = $('strength'); hint.textContent = '';
        if (s.label) { const b = document.createElement('b'); b.textContent = s.label; hint.append(b); if (s.hint) hint.append(` ${s.hint}`); }
    });
    $('create-form').addEventListener('submit', async e => {
        e.preventDefault();
        const err = $('err');
        const setup = code ? code.value.trim().toUpperCase().replace(/\s+/g, '') : '';
        const problem = (code && !setup) ? 'Enter the setup code.' : passwordProblem(pw.value, confirm.value);
        setError(err, problem);
        if (problem) return;
        $('submit').disabled = true;
        const body = { password: pw.value, keep: $('keep').checked };
        if (code) body.code = setup;
        let res;
        try { res = await post('/auth/password/create', body); } catch { setError(err, NETWORK); $('submit').disabled = false; return; }
        if (res.ok) return done();
        $('submit').disabled = false;
        if (res.status === 409) { window.location.reload(); return; }   // someone set one first: this is now the sign-in page
        if (res.status === 429) { setError(err, ''); return lock(waitFor(res)); }
        if (res.data.code === 'bad_code') { setError(err, res.data.error || 'That setup code is not right.'); code.focus(); code.select(); return; }
        setError(err, res.status === 403 ? CROSS : (res.data.error || 'Could not create the password.'));
    });
}

// Sign in with Coral Hub (#3966). The server decides what is offered (data in the markup); an address only ever says what went
// wrong (?hub=unavailable, failed or disabled), is shown once and dropped from the address, and never ticks or fills anything.
const HUB_FLASH = {
    unavailable: 'Coral Hub could not start the sign-in. Use your password.',
    failed: 'Coral Hub could not sign you in. The sign-in did not finish, so nothing was signed in. It can happen if it took too long, if it was opened from another address, or if Coral Hub could not be reached. Try again, or use your password.',
};
const hubFlash = params.get('hub');
if (hubFlash) {
    params.delete('hub');
    const q = params.toString();
    history.replaceState(null, '', window.location.pathname + (q ? '?' + q : '') + window.location.hash);
    if (hubFlash === 'disabled' && $('hub-disabled')) $('hub-disabled').hidden = false;
    else setError($('hub-flash'), HUB_FLASH[hubFlash] || (hubFlash === 'disabled' ? 'Sign in with Coral Hub is off for this computer.' : ''));
}
const hubOnlyHttps = url => typeof url === 'string' && url.startsWith('https://');
function startHubSignIn() {   // the same plain POST the Sign in with Coral Hub button makes
    const f = document.createElement('form'); f.method = 'post'; f.action = '/auth/hub/start'; f.hidden = true;
    document.body.append(f); f.submit();
}
if ($('hub-link-open')) {
    let poll = null, flowUrl = '';
    const stopPoll = () => { clearInterval(poll); poll = null; };
    $('hub-link-open').addEventListener('click', () => showPanel($('panel-hublink')));
    $('hub-cancel').addEventListener('click', () => { stopPoll(); showPanel(main); });
    $('hub-reopen').addEventListener('click', () => { if (hubOnlyHttps(flowUrl)) window.open(flowUrl, '_blank', 'noopener'); });
    function waitForLink(data) {
        flowUrl = data.verification_url;
        if (!hubOnlyHttps(flowUrl)) { setError($('hub-link-err'), 'Coral Hub gave an address this page will not open. Try again, or use your password.'); return; }
        $('hub-code').textContent = String(data.user_code || '');
        setError($('hub-wait-err'), '');
        showPanel($('panel-hubwait'));
        window.open(flowUrl, '_blank', 'noopener');
        let tries = 0;
        stopPoll();
        poll = setInterval(async () => { // interval-ok: the link approval poll on the sign-in page, which has no dashboard pollers to pause
            tries += 1;
            let r;
            try { r = await fetch('/auth/hub/link/status', { credentials: 'same-origin', cache: 'no-store' }); r = r.ok ? await r.json() : null; } catch { r = null; }
            if (r && r.state === 'linked' && r.ready) { stopPoll(); startHubSignIn(); }
            else if (r && r.state === 'failed') { stopPoll(); setError($('hub-wait-err'), r.error || 'Linking did not finish. Try again, or use your password.'); }
            else if (tries >= 150) { stopPoll(); setError($('hub-wait-err'), 'Linking did not finish in five minutes. Try again, or use your password.'); }
        }, 2000);
    }
    $('hub-link-form').addEventListener('submit', async e => {
        e.preventDefault();
        const err = $('hub-link-err'), field = $('hub-setup-field');
        setError(err, '');
        const code = field.hidden ? '' : $('hub-setup-code').value.trim().toUpperCase().replace(/\s+/g, '');
        if (!field.hidden && !code) { setError(err, 'Enter the setup code.'); return; }
        const body = { enable_signin: $('hub-enable').checked };
        if (code) body.setup_code = code;
        $('hub-link-go').disabled = true;
        let res;
        try { res = await post('/auth/hub/link', body); } catch { setError(err, NETWORK); $('hub-link-go').disabled = false; return; }
        $('hub-link-go').disabled = false;
        if (res.ok) return waitForLink(res.data);
        if (res.status === 409) { window.location.reload(); return; }   // linked meanwhile: the page now offers the sign-in
        if (res.data.code === 'setup_code_required') { field.hidden = false; $('hub-setup-code').focus(); setError(err, 'Enter the setup code.'); return; }
        if (res.data.code === 'setup_code_wrong') { setError(err, 'That setup code is not right.'); $('hub-setup-code').select(); return; }
        if (res.status === 429) { setError(err, res.data.error || 'Too many attempts. Try again later.'); return; }
        setError(err, res.status === 403 ? CROSS : (res.data.error || 'Could not start linking.'));
    });
}

// The API key fallback; also the QR-code link, which fills and sends it, then drops it from the address. The key comes
// in the fragment (#api_key=...), which no server or log sees; ?api_key= from older QR codes still works (#3949).
const keyForm = $('key-form');
async function sendKey() {
    const key = $('api-key').value.trim();
    if (!key) return;
    $('key-submit').disabled = true; setError($('key-err'), '');
    let res;
    try { res = await post('/auth/key', { key }); } catch { setError($('key-err'), NETWORK); $('key-submit').disabled = false; return; }
    if (res.ok && res.data.ok !== false) return done();
    $('key-submit').disabled = false;
    setError($('key-err'), res.data.error || 'Invalid API key.');
}
keyForm.addEventListener('submit', e => { e.preventDefault(); sendKey(); });
const hashParams = new URLSearchParams(window.location.hash.slice(1));
const urlKey = hashParams.get('api_key') || params.get('api_key');
if (urlKey) {
    $('api-key').value = urlKey;
    params.delete('api_key');
    const safeQuery = params.toString();
    history.replaceState(null, '', window.location.pathname + (safeQuery ? '?' + safeQuery : ''));
    showPanel($('panel-key'));
    sendKey();
}

// The page's handlers are in place: a test (or a script) can wait for this instead of guessing how long a module takes to load.
document.documentElement.dataset.authReady = 'true';
