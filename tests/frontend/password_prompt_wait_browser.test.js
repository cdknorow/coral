// The first-run "Set a password" prompt waits its turn (#294, QA on a fresh data folder): it opened over the New Agent dialog
// and covered its error, it took the Esc a focused terminal was sent, and then the words a person typed after a pause. Now it
// waits until no other dialog is on screen and asks the server again. With the keyboard in a terminal it shows a card that never
// takes the focus (Tab or F6 reach its button, which opens the dialog). Otherwise the dialog opens at once, so a fresh install
// still sees it. The server's /api/system/auth-status is stubbed on the page. Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        // local: false, so a "Not now" here is not remembered and each case can ask again.
        await ev(`(() => { const R = window.fetch; window.__ps = false;
            window.fetch = (u, o = {}) => String(u).startsWith('/api/system/auth-status')
                ? Promise.resolve(new Response(JSON.stringify({ authenticated: true, method: 'session', password_set: window.__ps, local: false }), { status: 200 }))
                : R(u, o);
            return true; })()`);
        const shown = () => ev(`!!document.getElementById('set-password-modal')`);
        const dismiss = () => ev(`(() => { const b = document.getElementById('set-password-later'); if (b) b.click(); document.getElementById('set-password-modal')?.remove(); document.getElementById('set-password-card')?.remove(); return true; })()`);
        // The prompt as the dashboard asks for it; __settled says whether the promise (which the Coral Hub question waits on) has settled.
        const ask = () => ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'password_ui.js'); window.__ps = false; window.__settled = false;
            m.maybePromptSetPassword().then(() => { window.__settled = true; }); return true; })()`);
        const pause = ms => ev(`new Promise(r => setTimeout(r, ${ms}))`); // sleep-ok: the point is that nothing opens during it
        await dismiss(); // whatever the page's own startup check put up

        console.log('1. an open dialog: the prompt waits, then opens when the dialog closes');
        await ev(`(() => { const d = document.createElement('div'); d.className = 'modal'; d.id = 'qa-other-dialog'; d.style.cssText = 'display:flex;position:fixed;inset:20%;background:#333;z-index:1000';
            d.innerHTML = '<p>Launch failed: the folder does not exist</p><button id="qa-dialog-btn">OK</button>'; document.body.appendChild(d); document.getElementById('qa-dialog-btn').focus(); return true; })()`);
        await ask();
        await pause(1800);
        assert.equal(await shown(), false, 'not over another dialog');
        assert.equal(await ev(`window.__settled`), false, 'still waiting (the next question waits behind it)');
        assert.equal(await ev(`document.activeElement && document.activeElement.id`), 'qa-dialog-btn', 'the dialog keeps the focus');
        await ev(`document.getElementById('qa-other-dialog').remove(); true`);
        await waitFor(`!!document.getElementById('set-password-modal')`, 'the prompt once the dialog closed', 4000);
        await waitFor(`window.__settled === true`, 'the promise settled once it opened', 2000);
        await dismiss();

        console.log('2. the keyboard in a terminal: a card that never takes the focus; the person keeps typing; its button opens the dialog');
        await ev(`(() => { const x = document.createElement('div'); x.className = 'xterm'; x.id = 'qa-xterm'; x.style.cssText = 'position:fixed;left:0;top:0;width:200px;height:100px';
            x.innerHTML = '<textarea id="qa-xterm-input"></textarea>'; document.body.appendChild(x); document.getElementById('qa-xterm-input').focus(); return true; })()`);
        // Keys as the terminal sees them: counted at the textarea, wherever the focus is.
        await ev(`window.__keys = 0; document.getElementById('qa-xterm-input').addEventListener('keydown', () => { window.__keys++; }); true`);
        // What the card held the moment it was added: empty, so a screen reader announces the words that follow a tick later.
        await ev(`window.__cardAtInsert = null; new MutationObserver((ms, o) => { const c = document.getElementById('set-password-card'); if (c) { window.__cardAtInsert = c.textContent; o.disconnect(); } }).observe(document.body, { childList: true, subtree: true }); true`);
        await ask();
        await waitFor(`!!document.getElementById('set-password-card-open')`, 'the card with its words', 3000);
        assert.equal(await ev(`window.__cardAtInsert`), '', 'the status region is added empty, then filled');
        assert.match(await ev(`document.getElementById('set-password-card').textContent`), /Set a password for Coral/);
        assert.equal(await ev(`(() => { const r = document.getElementById('set-password-card').getBoundingClientRect(); return r.width > 0 && r.top >= 0 && r.right <= innerWidth && r.bottom <= innerHeight; })()`), true, 'drawn inside the window');
        assert.equal(await shown(), false, 'no dialog over the terminal');
        assert.equal(await ev(`window.__settled`), true, 'settled: the card is up');
        assert.equal(await ev(`document.getElementById('set-password-card').getAttribute('role')`), 'status');
        assert.equal(await ev(`document.activeElement && document.activeElement.id`), 'qa-xterm-input', 'the terminal keeps the keyboard');
        // QA's case: the person types again right after it appears ("yes", Enter): every key reaches the terminal, none the card.
        for (const key of ['y', 'e', 's', 'Enter']) {
            await ev(`(() => { const a = document.activeElement; a.dispatchEvent(new KeyboardEvent('keydown', { key: ${JSON.stringify(key)}, bubbles: true })); return true; })()`);
            await pause(150);
        }
        assert.equal(await ev(`window.__keys`), 4, 'all four keys reached the terminal');
        assert.equal(await ev(`document.activeElement && document.activeElement.id`), 'qa-xterm-input', 'still in the terminal');
        await pause(3500);
        assert.equal(await ev(`document.activeElement && document.activeElement.id`), 'qa-xterm-input', 'the card never takes the focus, however long it waits');
        assert.equal(await shown(), false, 'and no dialog opens by itself');
        // Tab or F6 reach the button: it is the next control after the terminal in the page's order. F6 is #121's: with it, F6's own
        // step (focusBesideTerminal) from the terminal lands on the button; without it (before #121 merges) the order is checked.
        assert.equal(await ev(`(() => { const b = document.getElementById('set-password-card-open'); return b.tabIndex >= 0 && !!(document.getElementById('qa-xterm').compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING); })()`), true, 'its button is reachable after the terminal');
        const f6 = await ev(`(async () => { const x = await import((window.__coralStatic || '/static/') + 'xterm_renderer.js'); if (typeof x.focusBesideTerminal !== 'function') return 'no F6 (#121 not merged)';
            x.focusBesideTerminal(document.getElementById('qa-xterm'), false); const a = document.activeElement; const id = a && a.id; document.getElementById('qa-xterm-input').focus(); return id; })()`);
        console.log('   F6 from the terminal: ' + f6);
        if (f6 !== 'no F6 (#121 not merged)') assert.equal(f6, 'set-password-card-open', 'F6 from the terminal lands on the card');
        await ev(`document.getElementById('set-password-card-open').click(); true`);
        await waitFor(`!!document.getElementById('set-password-modal')`, 'the dialog from the card', 2000);
        assert.equal(await ev(`!!document.getElementById('set-password-card')`), false, 'the card goes when the dialog opens');
        assert.equal(await ev(`!!(document.activeElement && document.getElementById('set-password-modal').contains(document.activeElement))`), true, 'the dialog has the focus, as before');
        await dismiss();
        await ev(`document.getElementById('qa-xterm').remove(); true`);

        console.log('2b. the card goes once a password is set (another browser or Settings)');
        await ev(`(() => { const x = document.createElement('div'); x.className = 'xterm'; x.id = 'qa-xterm'; x.style.cssText = 'position:fixed;left:0;top:0;width:200px;height:100px';
            x.innerHTML = '<textarea id="qa-xterm-input"></textarea>'; document.body.appendChild(x); document.getElementById('qa-xterm-input').focus(); return true; })()`);
        await ask();
        await waitFor(`!!document.getElementById('set-password-card')`, 'the card', 3000);
        await ev(`window.__ps = true; window.dispatchEvent(new Event('focus')); true`);
        await waitFor(`!document.getElementById('set-password-card')`, 'the card gone once a password is set', 3000);
        await ev(`document.getElementById('qa-xterm').remove(); true`);

        console.log('3. nothing in the way: it opens at once (a fresh install still sees it)');
        await ask();
        await waitFor(`!!document.getElementById('set-password-modal')`, 'the prompt with nothing in the way', 1500);
        await dismiss();

        console.log('4. a password set while it waited: it does not open');
        await ev(`(() => { const d = document.createElement('div'); d.className = 'modal'; d.id = 'qa-other-dialog'; d.style.cssText = 'display:flex;position:fixed;inset:20%;z-index:1000'; document.body.appendChild(d); return true; })()`);
        await ask();
        await pause(600);
        await ev(`window.__ps = true; document.getElementById('qa-other-dialog').remove(); true`);
        await waitFor(`window.__settled === true`, 'the promise settled', 4000);
        assert.equal(await shown(), false, 'no prompt once a password is set');
        console.log('PASS password prompt waits for open dialogs, asks with a card that never takes the keyboard from the terminal, and still opens on its own');
    } finally {
        await client.close();
    }
})().catch(e => { console.error(e.stack || String(e)); process.exit(1); });
