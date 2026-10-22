// The first-run "Set a password" prompt waits its turn (#294, QA on a fresh data folder): it opened over the New Agent dialog
// and covered its error, and it took the Esc a focused terminal was sent. Now it waits until no other dialog is on screen and,
// with the keyboard in a terminal, until the person has stopped typing for 3 s (people type there, so it never waits for the
// terminal to lose focus), asks the server again, and opens; with nothing in the way it opens at once, so a fresh install
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
        const dismiss = () => ev(`(() => { const b = document.getElementById('set-password-later'); if (b) b.click(); document.getElementById('set-password-modal')?.remove(); return true; })()`);
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

        console.log('2. typing in a terminal: the prompt waits while the person types, and opens there once they stop for 3 s');
        await ev(`(() => { const x = document.createElement('div'); x.className = 'xterm'; x.id = 'qa-xterm'; x.style.cssText = 'position:fixed;left:0;top:0;width:200px;height:100px';
            x.innerHTML = '<textarea id="qa-xterm-input"></textarea>'; document.body.appendChild(x); document.getElementById('qa-xterm-input').focus(); return true; })()`);
        await ask();
        const type = () => ev(`document.getElementById('qa-xterm-input').dispatchEvent(new KeyboardEvent('keydown', { key: 'a', bubbles: true })); true`);
        for (let i = 0; i < 10; i++) { await pause(500); await type(); } // 5 s of typing, a key every half second
        assert.equal(await shown(), false, 'not while the person types');
        assert.equal(await ev(`document.activeElement && document.activeElement.id`), 'qa-xterm-input', 'the terminal keeps the keyboard while they type');
        const stopped = Date.now();
        await waitFor(`!!document.getElementById('set-password-modal')`, 'the prompt once the typing stopped, the terminal still focused', 6000);
        const quiet = Date.now() - stopped;
        assert.ok(quiet >= 2500, `it opened ${quiet} ms after the last key: not mid-typing`);
        await dismiss();
        await ev(`document.getElementById('qa-xterm').remove(); true`);

        console.log('2b. a terminal that just has the keyboard (no typing): it still opens after the quiet spell');
        await ev(`(() => { const x = document.createElement('div'); x.className = 'xterm'; x.id = 'qa-xterm'; x.style.cssText = 'position:fixed;left:0;top:0;width:200px;height:100px';
            x.innerHTML = '<textarea id="qa-xterm-input"></textarea>'; document.body.appendChild(x); document.getElementById('qa-xterm-input').focus(); return true; })()`);
        await ask();
        await pause(1500);
        assert.equal(await shown(), false, 'not at once over a focused terminal');
        await waitFor(`!!document.getElementById('set-password-modal')`, 'the prompt after the quiet spell', 5000);
        await dismiss();
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
        console.log('PASS password prompt waits for open dialogs and for typing in a terminal to stop, and still opens on its own');
    } finally {
        await client.close();
    }
})().catch(e => { console.error(e.stack || String(e)); process.exit(1); });
