// showToast accessibility (#3068 C4):  node toast.test.mjs
// Roles and live regions, dismissal, the optional action, the opt-in "Blocked:"
// prefix, and backward compatibility for the existing call shapes.
import assert from 'node:assert';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

const dom = installDom({ mode: 'local' });
const w = dom.window;
const { showToast } = await importUi('utils');
const wait = (ms) => new Promise(r => setTimeout(r, ms));
const toasts = () => Array.from(w.document.querySelectorAll('.toast'));
const clear = () => toasts().forEach(t => t.remove());

// Info: no live role of its own (the notices' polite region says it once, #6213), text as given, a close button.
{
    const t = showToast('Sent: hello');
    assert.strictEqual(t.getAttribute('role'), null);
    assert.strictEqual(t.getAttribute('aria-live'), null);
    await wait(150); // sleep-ok: the announcement is timed
    assert.strictEqual(w.document.getElementById('notice-live').textContent.trim(), 'Sent: hello', 'said once by the notices\' polite region');
    const quiet = showToast('Quiet', false, 4000, { silent: true });
    assert.strictEqual(quiet.querySelector('.toast-message').textContent, 'Quiet', 'a silent toast is still drawn');
    quiet.remove();
    await wait(1100); // sleep-ok: the announcement is timed
    assert.strictEqual(w.document.getElementById('notice-live').textContent.trim(), 'Sent: hello', 'a silent toast says nothing');
    assert.strictEqual(t.querySelector('.toast-message').textContent, 'Sent: hello');
    assert.ok(!t.classList.contains('error'));
    const close = t.querySelector('button.toast-close');
    assert.ok(close, 'every toast can be dismissed');
    assert.strictEqual(close.getAttribute('aria-label'), 'Dismiss: Sent: hello', 'named by its toast, so stacked toasts are different buttons (#6171)');
    close.click();
    assert.strictEqual(toasts().length, 0, 'close removes it');
}

// Errors: said assertively by the notices' alert region. Existing callers pass true or the string "error".
for (const flag of [true, 'error']) {
    const t = showToast('Server error 500', flag);
    assert.strictEqual(t.getAttribute('role'), null, String(flag));
    await wait(1000); // sleep-ok: the announcement is timed
    assert.strictEqual(w.document.getElementById('notice-alert').textContent.replace(/\u00a0/g, '').trim(), 'Server error 500', 'said by the alert region');
    assert.ok(t.classList.contains('error'));
    assert.strictEqual(t.querySelector('.toast-message').textContent, 'Server error 500', 'no prefix unless asked');
    clear();
}

// opts.blocked prefixes "Blocked: " exactly once.
{
    assert.strictEqual(showToast('Hub: View', true, 4000, { blocked: true }).querySelector('.toast-message').textContent, 'Blocked: Hub: View');
    assert.strictEqual(showToast('Blocked: already', true, 4000, { blocked: true }).querySelector('.toast-message').textContent, 'Blocked: already');
    clear();
}

// Text is never parsed as HTML.
{
    const t = showToast('<img src=x onerror=PWNED()>');
    assert.strictEqual(t.querySelector('img'), null);
    assert.strictEqual(t.querySelector('.toast-message').textContent, '<img src=x onerror=PWNED()>');
    clear();
}

// Escape while focused dismisses.
{
    const t = showToast('esc me');
    t.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    assert.strictEqual(toasts().length, 0);
}

// The timer removes a plain toast after its duration.
{
    showToast('short', false, 20);
    await wait(60);
    assert.strictEqual(toasts().length, 0, 'removed after its duration');
}

// opts.action: a labelled button; clicking runs the callback and dismisses.
// An action toast lasts at least 10s and does not fade.
{
    let ran = 0;
    const t = showToast('Blocked: Hub: View', true, 20, { action: { label: 'Open Servers', onClick: () => { ran++; } } });
    const btn = t.querySelector('button.toast-action');
    assert.ok(btn);
    assert.strictEqual(btn.textContent, 'Open Servers');
    assert.ok(t.classList.contains('has-action'));
    await wait(60);
    assert.strictEqual(toasts().length, 1, 'an action toast outlives a short duration');
    btn.click();
    assert.strictEqual(ran, 1);
    assert.strictEqual(toasts().length, 0, 'the action dismisses the toast');
}

// A throwing action still dismisses; an action without onClick is ignored.
{
    const orig = console.error; console.error = () => {};
    const t = showToast('x', false, 4000, { action: { label: 'Boom', onClick: () => { throw new Error('boom'); } } });
    t.querySelector('button.toast-action').click();
    console.error = orig;
    assert.strictEqual(toasts().length, 0);
    const u = showToast('y', false, 4000, { action: { label: 'No handler' } });
    assert.strictEqual(u.querySelector('button.toast-action'), null);
    clear();
}

console.log('toast: ok');
process.exit(0);
