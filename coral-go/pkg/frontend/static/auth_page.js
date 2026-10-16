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
    for (const p of [main, $('panel-forgot'), $('panel-key')]) if (p) p.hidden = p !== panel;
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
