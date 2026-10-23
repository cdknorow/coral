// A long error toast on a phone is readable (#6727, UI/UX's note on the phone chat audit: the kill toast was cut at two lines): an error toast with no duration of its own stays long enough to read (8 s or
// more, longer for long text, where it was 4 s), shows four lines before the "tap for the rest" cut, and a tap that opens the rest keeps it on screen (the timer is cleared: a person who is reading is not
// timed out); an explicit duration still holds for a short toast. Drives Chrome over CDP through agent_view_harness.js at 390 px.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a toast's own timer is the thing measured
    const S = `(window.__coralStatic || '/static/')`;
    const LONG = 'The team stopped, but its worktree could not be removed: git worktree remove failed because the folder is in use by another process. It is kept at /tmp/example-worktrees/b-abcdef1234 on branch coral-team/b-abcdef1234; remove it by hand when the other process has ended.';
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true }); await Emulation.setTouchEmulationEnabled({ enabled: true });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        const show = (text, err, dur) => ev(`import(${S} + 'notifications.js').then(m => { const t = m.showToast(${JSON.stringify(text)}, ${err}, ${dur === undefined ? 'undefined' : dur}); t.id = 'probe-toast'; return true; })`);
        const state = () => ev(`(() => { const t = document.getElementById('probe-toast'); if (!t) return null; const m = t.querySelector('.toast-message'); const cs = getComputedStyle(m); return { clamp: cs.webkitLineClamp, expanded: t.classList.contains('expanded'), lines: Math.round(m.getBoundingClientRect().height / parseFloat(cs.lineHeight || '18')) }; })()`);
        // an error toast with no duration of its own: four lines, and still there after the old 4 s
        await show(LONG, true);
        const first = await state();
        assert.equal(first.clamp, '4', `an error toast shows four lines before the cut (${JSON.stringify(first)})`);
        await sleep(5000); // sleep-ok: longer than the old four-second toast
        assert.ok(await state(), 'and it is still on screen after the old 4 s');
        // a tap that opens the rest keeps it: an explicit short duration is cleared by the tap
        await ev(`document.getElementById('probe-toast').remove(); true`);
        await show(LONG, true, 1500);
        await sleep(400); // sleep-ok: before its timer ends
        await ev(`document.getElementById('probe-toast').querySelector('.toast-message').click(); true`);
        assert.equal((await state()).expanded, true, 'a tap opens the rest');
        await sleep(2200); // sleep-ok: past the 1.5 s the toast was given
        assert.ok(await state(), 'and a toast that is being read is not timed out');
        // an explicit duration still holds for a toast nobody touched
        await ev(`document.getElementById('probe-toast').remove(); true`);
        await show('Saved', false, 800);
        await sleep(1300); // sleep-ok: past the 0.8 s the toast was given
        assert.equal(await state(), null, 'a short toast with its own duration still goes when it said it would');
        console.log('PASS the phone toast: an error stays long enough to read, shows four lines, and a tap that opens the rest keeps it');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
