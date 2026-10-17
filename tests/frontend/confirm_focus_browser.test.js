// A confirmation gives focus back to what asked for it, however it closes (UX on #4500): asked from a button inside Settings,
// Escape, Cancel and Yes each close it, Settings stays open and focus is back on that button (it dropped to the page before,
// as dialog_focus.js kept focus out of any dialog still open). Asked from the page, focus goes back there as before. A dialog
// that opens another and closes (Settings to Default prompts) still does not pull focus behind the new one.
'use strict';

const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation, Input } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 600));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const shown = id => `(() => { const e = document.getElementById(${JSON.stringify(id)}); return !!e && getComputedStyle(e).display !== 'none' && e.getClientRects().length > 0; })()`;
    const escape = async () => {
        await Input.dispatchKeyEvent({ type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
        await Input.dispatchKeyEvent({ type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    };
    const ASKER = `document.querySelector('#settings-modal .settings-prompts-btn')`; // a button inside Settings that stays there
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' }); await loaded; await coralReady();
        await ev(`window.showSettingsModal(); true`);
        await waitFor(shown('settings-modal'), 'Settings');
        await waitFor(`!!${ASKER}`, 'a button inside Settings');

        const ask = async () => {
            await ev(`(() => { ${ASKER}.focus(); window.__asked = 0; window.showConfirmModal('Delete them?', 'This cannot be undone.', () => { window.__asked++; }); return true; })()`);
            await waitFor(shown('confirm-modal'), 'the confirmation');
            await waitFor(`document.getElementById('confirm-modal').contains(document.activeElement)`, 'focus in the confirmation');
        };
        const back = async how => {
            await waitFor(`!${shown('confirm-modal')}`, `${how} closes the confirmation`);
            assert.ok(await ev(shown('settings-modal')), `${how}: Settings stays open`);
            await waitFor(`document.activeElement === ${ASKER}`, `${how}: focus back on the button in Settings that asked`, { diag: `document.activeElement && (document.activeElement.id || document.activeElement.className || document.activeElement.tagName)` });
        };

        console.log('Escape');
        await ask(); await escape(); await back('Escape');
        console.log('Cancel');
        await ask(); await ev(`document.querySelector('#confirm-modal [data-on-click="m-hide-confirm-modal"]').click(); true`); await back('Cancel');
        console.log('Yes');
        await ask(); await ev(`document.getElementById('confirm-modal-yes').click(); true`); await back('Yes');
        assert.equal(await ev(`window.__asked`), 1, 'Yes ran the action once');

        console.log('Asked from the page: focus goes back there, as before');
        await ev(`window.hideSettingsModal(); true`);
        await waitFor(`!${shown('settings-modal')}`, 'Settings closed');
        await ev(`(() => { const b = document.createElement('button'); b.id = 'qa-asker'; b.textContent = 'ask'; b.style.cssText = 'position:fixed;top:0;left:0;z-index:5'; document.body.appendChild(b); b.focus();
            window.showConfirmModal('Sure?', 'x', () => {}); return true; })()`);
        await waitFor(shown('confirm-modal'), 'the confirmation from the page');
        await escape();
        await waitFor(`!${shown('confirm-modal')} && document.activeElement && document.activeElement.id === 'qa-asker'`, 'focus back on the page button');

        console.log('Settings opening Default prompts: focus is not pulled behind the new dialog');
        await ev(`window.showSettingsModal(); true`);
        await waitFor(shown('settings-modal'), 'Settings again');
        await ev(`${ASKER}.focus(); ${ASKER}.click(); true`); // hides Settings, opens Default prompts
        await waitFor(shown('default-prompts-modal'), 'Default prompts');
        await waitFor(`document.getElementById('default-prompts-modal').contains(document.activeElement)`, 'focus stays in Default prompts');

        console.log('confirm_focus: ok');
        await client.close();
        process.exit(0);
    } catch (e) {
        console.error('FAIL', e && e.stack || e);
        try { await client.close(); } catch { /* closing */ }
        process.exit(1);
    }
})();
