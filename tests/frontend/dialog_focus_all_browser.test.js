// Focus in, Tab trap, Escape, focus back, for EVERY dialog in the page (#4026), not only the ones whose code calls attachDialogFocus.
// Each dialog is opened the way the page opens them (display flex, from a control that has focus); then: focus moves inside, Tab and
// Shift+Tab stay inside, Escape closes it, and focus returns to the control that opened it. A dialog with no focusable control
// (filled in by code when it opens) is listed, not failed. Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, ev, waitFor, PRELOAD } = await connect();
    const { Input } = client;
    const sleep = ms => new Promise(r => setTimeout(r, ms));
    const key = async (k, shift = false) => {
        const code = { Tab: 9, Escape: 27 }[k];
        const base = { key: k, code: k, windowsVirtualKeyCode: code, modifiers: shift ? 8 : 0 };
        await Input.dispatchKeyEvent({ type: 'keyDown', ...base }); await Input.dispatchKeyEvent({ type: 'keyUp', ...base });
    };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`window.__coral.buildAllLazy(); true`); // the dialogs built on first open (#4500), each with the same focus handling
        const ids = await ev(`[...document.querySelectorAll('.modal[role=dialog]')].map(d => d.id)`);
        assert.ok(ids.length >= 18, `the page has its dialogs (${ids.length})`);
        const problems = [], skipped = [];
        for (const id of ids) {
            // an opener: a real, visible, focusable control outside every dialog
            const opener = await ev(`(() => { document.querySelectorAll('.modal').forEach(m => { if (getComputedStyle(m).display !== 'none') m.style.display = 'none'; });
                const o = [...document.querySelectorAll('button, a[href]')].find(b => !b.closest('.modal') && b.getClientRects().length > 0 && !b.disabled);
                if (!o) return null; o.setAttribute('data-zz-opener', '1'); o.focus(); return { tag: o.tagName, ok: document.activeElement === o }; })()`);
            assert.ok(opener && opener.ok, 'an opener control has focus');
            const focusables = await ev(`(() => { const m = document.getElementById(${JSON.stringify(id)}); m.style.display = 'flex'; return [...m.querySelectorAll('button:not([disabled]), input:not([disabled]):not([type=hidden]), textarea:not([disabled]), select:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])')].filter(e => e.getClientRects().length > 0).length; })()`);
            if (!focusables) { skipped.push(id); await ev(`document.getElementById(${JSON.stringify(id)}).style.display = 'none'; document.querySelector('[data-zz-opener]').removeAttribute('data-zz-opener'); true`); continue; }
            await sleep(120); // sleep-ok: focus moves on the frame after the dialog shows
            const inside = () => ev(`(() => { const m = document.getElementById(${JSON.stringify(id)}); return m.contains(document.activeElement); })()`);
            if (!(await inside())) { problems.push(`${id}: focus did not move inside when it opened`); }
            // Tab around the dialog more times than it has controls: focus never leaves
            let left = false;
            for (let i = 0; i < focusables + 2; i++) { await key('Tab'); if (!(await inside())) { left = true; break; } }
            await key('Tab', true); if (!(await inside())) left = true;
            if (left) problems.push(`${id}: Tab left the dialog`);
            await key('Escape');
            // A lazy dialog's hide function is a stub that loads the dialog's code first (the dialogs here are forced open without it, which the page never does), so on a cold or slow machine
            // the close arrives after the module has loaded, not on the next frame: wait for it (up to 5 s) rather than for 120 ms.
            await waitFor(`getComputedStyle(document.getElementById(${JSON.stringify(id)})).display === 'none'`, `${id} to close on Escape`, 5000).catch(() => {});
            await sleep(120); // sleep-ok: the focus return runs on the frame after the close
            const closed = await ev(`getComputedStyle(document.getElementById(${JSON.stringify(id)})).display === 'none'`);
            if (!closed) { problems.push(`${id}: Escape did not close it`); await ev(`document.getElementById(${JSON.stringify(id)}).style.display = 'none'; true`); }
            const back = await ev(`document.activeElement && document.activeElement.hasAttribute('data-zz-opener')`);
            if (closed && !back) problems.push(`${id}: focus did not return to the control that opened it`);
            await ev(`document.querySelectorAll('[data-zz-opener]').forEach(e => e.removeAttribute('data-zz-opener')); true`);
        }
        assert.deepEqual(problems, [], 'dialog focus problems:\n' + problems.join('\n'));
        console.log(`PASS ${ids.length - skipped.length} dialogs: focus in, Tab trapped, Escape closes, focus back (${skipped.length} with no controls until filled: ${skipped.join(', ')})`);
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
