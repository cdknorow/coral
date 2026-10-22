// The keyboard's way out of the agent's terminal (#287, QA's #278 row 2): the terminal keeps Tab, Shift+Tab and Esc for the agent,
// and its only way out was Ctrl+Shift+Esc, which Windows always gives to Task Manager, so a keyboard-only Windows user was trapped.
// F6 now moves the focus to the next control of the page and Shift+F6 to the one before; neither reaches the agent. Tab,
// Shift+Tab, Esc (and Esc Esc) still go to the agent, and Ctrl+Shift+Esc still lets go where the system allows it. The keys are
// sent as real key events (Input.dispatchKeyEvent), into a terminal between two buttons, with a fake socket recording what is sent.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    const { Input } = client;
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        await ev(`(async () => {
            const st = window.__coralStatic || '/static/';
            const T = window.T = { state: (await import(st + 'state.js')).state, x: await import(st + 'xterm_renderer.js'), bar: await import(st + 'terminal_statusbar.js') };
            await (await import(st + 'vendor_loader.js')).loadXterm();
            window.__sent = [];
            class FakeWS { constructor(u) { this.url = String(u); this.readyState = 0;
                    setTimeout(() => { if (this.readyState === 0) { this.readyState = 1; this.onopen && this.onopen({}); } }, 5); }
                send(d) { window.__sent.push(String(d)); } close() { this.readyState = 3; }
                addEventListener() {} removeEventListener() {} }
            FakeWS.CONNECTING = 0; FakeWS.OPEN = 1; FakeWS.CLOSING = 2; FakeWS.CLOSED = 3;
            const RealWS = window.WebSocket; window.WebSocket = function (u, p) { return /\\/ws\\/terminal\\//.test(String(u)) ? new FakeWS(u) : (p === undefined ? new RealWS(u) : new RealWS(u, p)); };
            Object.assign(window.WebSocket, { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 }); window.WebSocket.prototype = RealWS.prototype;
            // A modal-like strip on top of the page: a button, the terminal, a button. inert keeps the rest of the page out of the order.
            for (const el of document.body.children) el.inert = true;
            const strip = document.createElement('div'); strip.style.cssText = 'position:fixed;inset:0;z-index:99999;background:#000;display:flex;flex-direction:column;gap:8px;padding:8px';
            strip.innerHTML = '<button id="qa-before">before</button><div id="qa-term-host" style="width:700px;height:300px"></div><button id="qa-after">after</button>';
            document.body.appendChild(strip);
            T.state.currentSession = { name: 't1', type: 'live', session_id: 'sess-t1', agent_type: 'claude' };
            T.x.disposeTerminal();
            window.__term = T.x.createTerminal(document.getElementById('qa-term-host'));
            T.x.connectTerminalWs('t1', 'claude', 'sess-t1', 'local');
            return true; })()`);
        await ev(`new Promise(r => setTimeout(r, 120))`); // sleep-ok: the fake socket opens on a timer
        const active = () => ev(`(() => { const a = document.activeElement; return a ? (a.id || (window.__term.element.contains(a) ? 'terminal' : a.tagName)) : null; })()`);
        const VK = { F6: 117, Tab: 9, Escape: 27 };
        const press = async (key, mods = {}) => {
            const modifiers = (mods.alt ? 1 : 0) | (mods.ctrl ? 2 : 0) | (mods.shift ? 8 : 0);
            const base = { key, code: key, windowsVirtualKeyCode: VK[key], nativeVirtualKeyCode: VK[key], modifiers };
            // A plain Esc is a keyDown carrying its text, as a real key press sends it (a rawKeyDown Escape reaches xterm as no input at all).
            const plainEsc = key === 'Escape' && !modifiers;
            await Input.dispatchKeyEvent(plainEsc ? { type: 'keyDown', ...base, text: '', unmodifiedText: '' } : { type: 'rawKeyDown', ...base });
            await Input.dispatchKeyEvent({ type: 'keyUp', ...base });
            await ev(`new Promise(r => setTimeout(r, 40))`); // sleep-ok: the typed-input batch is flushed on a short timer
        };
        const intoTerminal = async () => { await ev(`window.__term.focus(); window.__sent.length = 0; true`); assert.equal(await active(), 'terminal', 'the terminal has the focus'); };
        // What reached the agent: the data of the terminal_input frames, joined.
        const sent = () => ev(`window.__sent.map(f => { try { const m = JSON.parse(f); return m.type === 'terminal_input' ? m.data : ''; } catch { return f; } }).join('')`);

        // The first-run "set a password" dialog takes the page's first Esc. The runner sets a password so it never shows, but Coral
        // refuses the runner's password file when other accounts can read it (on Windows, a run folder whose access list grants
        // them, #293): then close the dialog first, so the Escs below are the terminal's.
        await ev(`document.activeElement && document.activeElement.blur(); true`);
        if (await ev(`!!document.getElementById('set-password-modal')?.getClientRects().length`)) await press('Escape');
        assert.equal(await ev(`!!document.getElementById('set-password-modal')?.getClientRects().length`), false, 'the first-run password dialog is closed');

        await intoTerminal();
        await press('F6');
        assert.equal(await active(), 'qa-after', 'F6 moves the focus to the next control');
        assert.equal(await sent(), '', 'F6 is not sent to the agent');

        await intoTerminal();
        await press('F6', { shift: true });
        assert.equal(await active(), 'qa-before', 'Shift+F6 moves the focus to the control before');
        assert.equal(await sent(), '', 'Shift+F6 is not sent to the agent');

        for (const [label, key, mods, frame] of [['Tab', 'Tab', {}, '\\t'], ['Shift+Tab (Claude\'s mode toggle)', 'Tab', { shift: true }, '\\u001b[Z'], ['Esc', 'Escape', {}, '\\u001b']]) {
            await intoTerminal();
            await press(key, mods);
            assert.equal(await active(), 'terminal', `${label} keeps the focus in the terminal`);
            assert.equal(await sent(), JSON.parse(`"${frame}"`), `${label} goes to the agent (sent ${JSON.stringify(await sent())}, focus ${await active()})`);
        }
        await intoTerminal();
        await press('Escape'); await press('Escape');
        assert.equal(await active(), 'terminal', 'Esc Esc (Claude\'s rewind) keeps the focus in the terminal');
        assert.equal((await sent()).split('\u001b').length - 1, 2, `both Escs go to the agent: ${JSON.stringify(await sent())}`);

        await intoTerminal();
        await press('Escape', { ctrl: true, shift: true });
        assert.notEqual(await active(), 'terminal', 'Ctrl+Shift+Esc still lets go of the terminal where the system leaves it to the page');

        const hints = await ev(`({ win: T.bar.leaveHint('Win32'), mac: T.bar.leaveHint('MacIntel'), linux: T.bar.leaveHint('Linux x86_64') })`);
        assert.equal(hints.win, 'Click to type · F6 to leave the terminal', 'Windows names F6 only (Task Manager takes Ctrl+Shift+Esc)');
        assert.equal(hints.mac, 'Click to type · Ctrl+Shift+Esc or F6 to leave the terminal');
        assert.equal(hints.linux, hints.mac);
        console.log('PASS terminal leave key: F6 and Shift+F6 leave the terminal; Tab, Shift+Tab, Esc and Esc Esc reach the agent; the hint names the key');
    } finally {
        await client.close();
    }
})().catch(e => { console.error(e.stack || String(e)); process.exit(1); });
