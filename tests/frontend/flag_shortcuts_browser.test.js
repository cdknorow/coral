// The flag shortcut buttons (#4684): a button next to a flags box adds or removes its flag in the box (toggleFlag, reached through the flag-toggle action), the button shows whether its flag is in the
// box and follows what is typed (the box's own input events), the permission buttons rewrite a box's permission flags for the agent type and mode (_setPermFlagsForInput), and the Job dialog's box
// is wired once that dialog is built. It was a block of modals.js and is its own module now; this pins what it does. Drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        // The dialogs' module loads on first use; build the Job dialog too (its box is wired when it is built).
        await ev(`(async () => { const st = window.__coralStatic || '/static/'; await window.showLaunchModal(); window.hideLaunchModal(); (await import(st + 'lazy_dom.js')).ensureBuilt('job-modal'); return true; })()`);
        await waitFor(`!!document.getElementById('job-modal-flags') && !!window._setPermFlagsForInput`, 'the dialogs and the helper');
        const boxes = await ev(`[...new Set([...document.querySelectorAll('.flag-shortcut-btn[data-flag-input]')].map(b => b.dataset.flagInput))].filter(id => document.getElementById(id))`);
        assert.ok(boxes.includes('job-modal-flags'), 'flag boxes with shortcut buttons: ' + JSON.stringify(boxes));
        for (const id of boxes) {
            console.log(id + ': a shortcut adds and removes its flag, and follows typing');
            const probe = await ev(`(() => { const box = document.getElementById(${JSON.stringify(id)}); const btn = [...document.querySelectorAll('.flag-shortcut-btn[data-flag-input="${id}"]')].find(b => /^--[a-z0-9-]+$/.test(b.dataset.flag)); if (!btn) return null; box.value = ''; box.dispatchEvent(new Event('input'));
                const flag = btn.dataset.flag; const state = () => ({ value: box.value, active: btn.classList.contains('active') });
                const out = { flag, start: state() };
                btn.click(); out.added = state();
                btn.click(); out.removed = state();
                box.value = '--other ' + flag; box.dispatchEvent(new Event('input')); out.typedIn = state();
                box.value = '--other'; box.dispatchEvent(new Event('input')); out.typedOut = state();
                window.toggleFlag(${JSON.stringify(id)}, flag); out.byFunction = state();
                box.value = ''; box.dispatchEvent(new Event('input'));
                return out; })()`);
            assert.ok(probe, id + ': a shortcut button for it');
            assert.deepEqual(probe.start, { value: '', active: false });
            assert.deepEqual(probe.added, { value: probe.flag, active: true }, id + ': clicking adds it');
            assert.deepEqual(probe.removed, { value: '', active: false }, id + ': clicking again removes it');
            assert.deepEqual(probe.typedIn, { value: '--other ' + probe.flag, active: true }, id + ': typing it lights the button');
            assert.deepEqual(probe.typedOut, { value: '--other', active: false }, id + ': taking it out puts the light out');
            assert.deepEqual(probe.byFunction, { value: '--other ' + probe.flag, active: true }, id + ': toggleFlag adds it to what is there');
        }
        console.log('The permission flags of a box, for an agent type and mode');
        const perm = await ev(`(() => { const box = document.getElementById('job-modal-flags'); box.value = '--keep-me'; box.dispatchEvent(new Event('input'));
            const seen = []; box.addEventListener('input', () => seen.push(box.value), { once: false });
            window._setPermFlagsForInput(box.id, 'claude', 'bypassPermissions'); const bypass = box.value;
            window._setPermFlagsForInput(box.id, 'claude', 'acceptEdits'); const accept = box.value;
            window._setPermFlagsForInput('no-such-box', 'claude', 'acceptEdits');
            return { bypass, accept, events: seen.length }; })()`);
        assert.match(perm.bypass, /--keep-me/); assert.match(perm.bypass, /dangerously-skip-permissions|bypass/i);
        assert.match(perm.accept, /--keep-me/); assert.notEqual(perm.accept, perm.bypass, 'the mode changes the permission flags');
        assert.equal(perm.events, 2, 'each rewrite fires the box\'s input event (so the buttons follow)');
        console.log('PASS flag shortcuts');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
