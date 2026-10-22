// Ctrl+C and Ctrl+V in the agent's terminal on Windows (#312, QA's #311 in the real Coral app), as Windows Terminal has them:
//  - Ctrl+C with a selection copies it (to the clipboard) and clears it, and sends nothing; with no selection it is ^C to the agent;
//  - Ctrl+V pastes the clipboard's text through the terminal's paste path (bracketed when the program asked for it);
//  - Ctrl+V with an image and no text sends the agent's image-paste key: Alt+V (ESC v) to Claude Code, ^V to Codex, nothing to others;
//  - on another platform (Linux here) Ctrl+C stays ^C even with a selection, and Ctrl+V does not paste.
// The page is told it runs on Windows (or Linux) before it loads; keys are real key events (Input.dispatchKeyEvent; a paste
// key carries the editing command a real press runs). A fake socket records what reaches the agent.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input, Browser } = client;
    let restore = null; // puts the person's clipboard back (set once it is saved)
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Browser.grantPermissions({ permissions: ['clipboardReadWrite', 'clipboardSanitizedWrite'], origin: new URL(BASE).origin }).catch(() => {});
        const setup = async platform => {
            // navigator.platform and navigator.userAgentData.platform both (the page reads the latter first)
            await Emulation.setUserAgentOverride({ userAgent: (await Browser.getVersion()).userAgent, platform,
                userAgentMetadata: { platform: /^Win/.test(platform) ? 'Windows' : 'Linux', platformVersion: '', architecture: 'x86', model: '', mobile: false } });
            await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
            await Emulation.setFocusEmulationEnabled({ enabled: true }); // the clipboard answers only a focused page; headless is never focused otherwise
            await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'settings')`);
            await ev(`(async () => {
                const st = window.__coralStatic || '/static/';
                const T = window.T = { state: (await import(st + 'state.js')).state, x: await import(st + 'xterm_renderer.js') };
                await (await import(st + 'vendor_loader.js')).loadXterm();
                window.__sent = [];
                class FakeWS { constructor(u) { this.url = String(u); this.readyState = 0; setTimeout(() => { if (this.readyState === 0) { this.readyState = 1; this.onopen && this.onopen({}); } }, 5); }
                    send(d) { window.__sent.push(String(d)); } close() { this.readyState = 3; } addEventListener() {} removeEventListener() {} }
                FakeWS.CONNECTING = 0; FakeWS.OPEN = 1; FakeWS.CLOSING = 2; FakeWS.CLOSED = 3;
                const RealWS = window.WebSocket; window.WebSocket = function (u, p) { return /\\/ws\\/terminal\\//.test(String(u)) ? new FakeWS(u) : (p === undefined ? new RealWS(u) : new RealWS(u, p)); };
                Object.assign(window.WebSocket, { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 }); window.WebSocket.prototype = RealWS.prototype;
                for (const el of document.body.children) el.inert = true;
                const host = document.createElement('div'); host.id = 'qa-term-host'; host.style.cssText = 'position:fixed;left:0;top:0;width:700px;height:300px;z-index:99999;background:#000';
                document.body.appendChild(host);
                T.state.currentSession = { name: 't1', type: 'live', session_id: 'sess-t1', agent_type: 'claude' };
                T.x.disposeTerminal();
                window.__term = T.x.createTerminal(host);
                T.x.connectTerminalWs('t1', 'claude', 'sess-t1', 'local');
                return true; })()`);
            await ev(`new Promise(r => setTimeout(r, 150))`); // sleep-ok: the fake socket opens on a timer
            await ev(`new Promise(r => window.__term.write('hello coral world\\r\\n', () => r(true)))`);
            await Page.bringToFront();
            console.log('   focused: ' + await ev(`window.focus(); window.__term.focus(); document.hasFocus()`));
        };
        // What reached the agent: the data of the terminal_input frames, joined.
        const sent = () => ev(`window.__sent.map(f => { try { const m = JSON.parse(f); return m.type === 'terminal_input' ? m.data : ''; } catch { return f; } }).join('')`);
        const fresh = () => ev(`window.__term.focus(); window.__sent.length = 0; true`);
        const press = async (key, { ctrl = false, shift = false, commands } = {}) => {
            const vk = key.toUpperCase().charCodeAt(0);
            const base = { key, code: 'Key' + key.toUpperCase(), windowsVirtualKeyCode: vk, nativeVirtualKeyCode: vk, modifiers: (ctrl ? 2 : 0) | (shift ? 8 : 0) };
            await Input.dispatchKeyEvent({ type: 'rawKeyDown', ...base, ...(commands ? { commands } : {}) });
            await Input.dispatchKeyEvent({ type: 'keyUp', ...base });
            await ev(`new Promise(r => setTimeout(r, 80))`); // sleep-ok: the clipboard write and the input batch settle
        };
        const writeClipboard = text => ev(`navigator.clipboard.writeText(${JSON.stringify(text)}).then(() => true)`);
        const readClipboard = () => ev(`navigator.clipboard.readText()`);
        // The clipboard is the person's own (the Orchestrator's rule): what it holds is saved before the first write and put back at
        // the end, its text, HTML and PNG (the kinds a page can write back).
        const KEEP = ['text/plain', 'text/html', 'image/png'];
        const saveClipboard = () => ev(`navigator.clipboard.read().then(items => Promise.all(items.map(async it => {
            const out = {};
            for (const t of it.types.filter(t => ${JSON.stringify(KEEP)}.includes(t))) {
                const b = await it.getType(t);
                out[t] = await new Promise(r => { const f = new FileReader(); f.onload = () => r(String(f.result).split(',')[1] || ''); f.readAsDataURL(b); });
            }
            return out;
        }))).catch(() => null)`);
        const restoreClipboard = saved => ev(`(async () => {
            const saved = ${JSON.stringify(saved)};
            if (!saved) return false;
            const items = saved.filter(o => Object.keys(o).length).map(o => new ClipboardItem(Object.fromEntries(Object.entries(o).map(([t, b64]) =>
                [t, new Blob([Uint8Array.from(atob(b64), c => c.charCodeAt(0))], { type: t })]))));
            if (items.length) await navigator.clipboard.write(items); else await navigator.clipboard.writeText('');
            return true; })()`);

        console.log('0. Linux (first, on a fresh page): Ctrl+C stays ^C with a selection, and Ctrl+V does not paste');
        await setup('Linux x86_64');
        const saved = await saveClipboard();
        if (saved) restore = () => restoreClipboard(saved);
        await writeClipboard('unchanged');
        await fresh();
        await ev(`window.__term.select(6, 0, 5); true`);
        await press('c', { ctrl: true });
        assert.equal(await sent(), '\u0003', '^C, as before: ' + JSON.stringify({ sent: await sent(), platform: await ev(`navigator.platform + ' / ' + (navigator.userAgentData && navigator.userAgentData.platform)`) }));
        assert.equal(await readClipboard(), 'unchanged');
        await fresh();
        await press('v', { ctrl: true });
        assert.equal(await sent(), '\u0016', '^V, as before');
        console.log('1. Windows: Ctrl+C with a selection copies it and clears it, and sends nothing');
        await setup('Win32');
        await writeClipboard('before');
        await fresh();
        await ev(`window.__term.select(6, 0, 5); true`); // "coral"
        assert.equal(await ev(`window.__term.getSelection()`), 'coral');
        await press('c', { ctrl: true });
        assert.equal(await readClipboard(), 'coral', 'the selection is on the clipboard');
        assert.equal(await ev(`window.__term.hasSelection()`), false, 'and the selection is cleared');
        assert.equal(await sent(), '', 'nothing reached the agent');

        console.log('2. Windows: Ctrl+C with no selection is ^C to the agent');
        await fresh();
        await press('c', { ctrl: true });
        assert.equal(await sent(), '\u0003');
        assert.equal(await readClipboard(), 'coral', 'the clipboard is left alone');

        console.log('3. Windows: Ctrl+V pastes the clipboard\'s text, bracketed when the program asked for it');
        await ev(`new Promise(r => window.__term.write('\\x1b[?2004h', () => r(true)))`);
        await writeClipboard('pasted text');
        await fresh();
        await press('v', { ctrl: true, commands: ['paste'] });
        assert.equal(await sent(), '\u001b[200~pasted text\u001b[201~', 'a bracketed paste, not ^V');

        console.log('3b. Windows: a pasted end-of-paste marker loses its ESC, so no text breaks out and runs (Security on #312)');
        await writeClipboard('a\u001b[201~echo hi\n');
        await fresh();
        await press('v', { ctrl: true, commands: ['paste'] });
        // a paste's newline reaches the program as Enter (CR), as in any terminal; inside the brackets it is text, not a command
        assert.equal(await sent(), '\u001b[200~a[201~echo hi\r\u001b[201~','the inner marker is plain text inside the one paste; the line is pasted, not run');

        console.log('4. Windows: Ctrl+V with an image and no text sends the agent\'s own image-paste key, by its type (QA on #138)');
        await ev(`(async () => { const c = document.createElement('canvas'); c.width = c.height = 2; const blob = await new Promise(r => c.toBlob(r, 'image/png'));
            await navigator.clipboard.write([new ClipboardItem({ 'image/png': blob })]); return true; })()`);
        // the type comes from the session the socket was opened for (each case is a new session, so a new socket)
        for (const [type, key] of [['claude', '\u001bv'], ['codex', '\u0016'], ['agy', ''], ['', '']]) {
            await ev(`window.T.x.connectTerminalWs('t1', ${JSON.stringify(type)}, 'sess-img-${type || 'none'}', 'local'); new Promise(r => setTimeout(r, 60))`); // sleep-ok: the fake socket opens on a timer
            await fresh();
            await press('v', { ctrl: true, commands: ['paste'] });
            assert.equal(await sent(), key, `the image key for agent type '${type}' (Claude Code: Alt+V; Codex: ^V; any other: nothing)`);
        }

        console.log('5. Ctrl+Shift+C and Ctrl+Shift+V are not taken (they keep their own behaviour)');
        await writeClipboard('kept');
        await fresh();
        await ev(`window.__term.select(0, 0, 5); true`);
        await press('c', { ctrl: true, shift: true });
        assert.equal(await sent(), '', 'Ctrl+Shift+C sends nothing to the agent');

        console.log('6. Windows: an empty clipboard pastes nothing and sends nothing');
        await writeClipboard('');
        await fresh();
        await press('v', { ctrl: true, commands: ['paste'] });
        assert.equal(await sent(), '');
        console.log('PASS terminal copy and paste: Windows Terminal\'s Ctrl+C and Ctrl+V on Windows, the old keys elsewhere');
    } finally {
        if (restore) await restore().catch(e => console.error('clipboard not restored: ' + e.message));
        await client.close();
    }
})().catch(e => { console.error(e.stack || String(e)); process.exit(1); });
