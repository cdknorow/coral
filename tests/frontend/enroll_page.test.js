// Pure-node test of the Coral Hub /enroll page (the hub renders our template and our enroll.js):
//   node enroll_page.test.js
// 1. The page says "Connect to Coral Hub" (title and heading) and the short intro.
// 2. ?code= still prefills the code field and looks it up; without it the field is focused.
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const dir = path.resolve(__dirname, '../../coral-go/pkg/frontend');

// ── 1. template text ─────────────────────────────────────────────────
const tpl = fs.readFileSync(path.join(dir, 'templates/cloud/enroll.html'), 'utf8');
assert.match(tpl, /<title>Connect to Coral Hub<\/title>/, 'title');
assert.match(tpl, /<h1>Connect to Coral Hub<\/h1>/, 'heading');
assert.match(tpl, /<p>Enter the code shown on your machine\.<\/p>/, 'intro');
assert.ok(!/Cloud Hub/i.test(tpl), 'no "Cloud Hub" on the page');

// ── 2. prefill ───────────────────────────────────────────────────────
function fakeEl() {
    const el = { value: '', hidden: false, textContent: '', disabled: false, focused: 0, listeners: {} };
    el.focus = () => { el.focused++; };
    el.addEventListener = (ev, fn) => { el.listeners[ev] = fn; };
    el.replaceChildren = () => {};
    el.append = () => {};
    return el;
}

async function runEnroll(search) {
    let src = fs.readFileSync(path.join(dir, 'static/cloud/enroll.js'), 'utf8');
    src = src.replace(/^import \{[^}]*\} from '\.\/common\.js';/m, '');
    const els = {};
    const calls = [];
    const sandbox = {
        document: { getElementById: id => (els[id] = els[id] || fakeEl()), createElement: () => fakeEl() },
        location: { pathname: '/enroll', search, replace() {} },
        URLSearchParams, Date, Math, Number, String,
        getSession: async () => ({ authenticated: true, csrf: 'csrf' }),
        postJSON: async (p, body) => { calls.push([p, body]); return { status: 200, ok: true, data: { nonce: 'n', requester: { ip: '1.2.3.4', hostname: 'box', coral_version: '1.4.15', requested_at: new Date().toISOString() } } }; },
        setStatus: () => {},
        safeNext: (raw, fb) => fb,
    };
    vm.runInNewContext(src, sandbox);
    for (let i = 0; i < 20; i++) await new Promise(r => setImmediate(r));
    return { els, calls };
}

(async () => {
    const withCode = await runEnroll('?code=abcd2345');
    assert.strictEqual(withCode.els.code.value, 'ABCD-2345', '?code= prefills the (normalised) code');
    assert.strictEqual(JSON.stringify(withCode.calls), JSON.stringify([['/api/enroll/preview', { user_code: 'ABCD-2345' }]]), '?code= is looked up at once');

    const without = await runEnroll('');
    assert.strictEqual(without.els.code.value, '', 'no code, nothing prefilled');
    assert.strictEqual(without.calls.length, 0, 'no lookup without a code');
    assert.strictEqual(without.els.code.focused, 1, 'the field is focused');
    console.log('PASS enroll page: Coral Hub wording, ?code= prefill');
})().catch(e => { console.error(e); process.exit(1); });
