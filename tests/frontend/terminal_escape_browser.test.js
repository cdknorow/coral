// What a hostile byte stream in a terminal can make the viewer send (#3686, terminal audit
// #3684 findings 1 and 3). Real isolated server, a real terminal agent; the page's terminal
// socket is wrapped only to record the frames it sends.
//   - Queries in the output (device attributes, status and cursor reports, a colour query) make
//     xterm answer through onData: nothing is sent, and the window doesn't take the pane. A real
//     key still goes out and does take it.
//   - A paste carrying ESC[201~ in bracketed paste mode: the ESC is stripped, so the text stays
//     inside the one paste (a single end bracket, at the end). C1 controls and BEL go too.
//   - Output that turns bracketed paste off: a multi-line paste asks first ("Paste 2 lines?"),
//     sends nothing on Cancel, and the lines on Paste. One line without a newline goes at once.
//
// Expects CORAL_TEST_URL + CDP_PORT from the runner. Exit 0 / 1.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const CDP_PORT = parseInt(process.env.CDP_PORT || '9222', 10);

const results = [];
function check(label, cond, detail) {
    const v = cond ? 'PASS' : 'FAIL';
    results.push(v);
    console.log(`[${v}] ${label}${detail !== undefined ? ' — ' + detail : ''}`);
}

async function httpJson(method, path, body) {
    const r = await fetch(BASE + path, { method, headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
    let data = null; try { data = await r.json(); } catch { /* no body */ }
    return { status: r.status, data };
}

// window.__sent: every frame the page sends on the terminal socket (parsed).
const WRAP = `(function(){ const Real = window.WebSocket; window.__sent = [];
  function W(u, p) { const s = p === undefined ? new Real(u) : new Real(u, p);
    if (!/\\/ws\\/terminal/.test(String(u))) return s;
    const send = s.send.bind(s);
    s.send = d => { let m = null; try { m = JSON.parse(d); } catch {} window.__sent.push(m || { raw: String(d) }); return send(d); };
    return s; }
  W.prototype = Real.prototype; W.CONNECTING = 0; W.OPEN = 1; W.CLOSING = 2; W.CLOSED = 3; window.WebSocket = W; })();`;

async function run() {
    const launched = [];
    const client = await CDP({ port: CDP_PORT });
    const { Page, Runtime, Input } = client;
    await Promise.all([Page.enable(), Runtime.enable()]);
    await Page.addScriptToEvaluateOnNewDocument({ source: WRAP });
    const ev = async (x) => { const r = await Runtime.evaluate({ expression: x, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 300)); return r.result.value; };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const until = (expression, what, timeout = 8000) => waitFor(expression, what, { soft: true, timeout });
    const XT = `(await import((window.__coralStatic||'/static/')+'xterm_renderer.js'))`;
    const OWN = `(await import((window.__coralStatic||'/static/')+'ownership.js'))`;
    const pause = ms => ev(`new Promise(r => setTimeout(r, ${ms}))`); // sleep-ok: proves a negative (nothing sent)
    // Write output into the viewer's xterm and wait until xterm has parsed it.
    const output = s => ev(`(async () => { const t = ${XT}.getTerminal(); await new Promise(r => t.write(${JSON.stringify(s)}, r)); return true; })()`);
    const inputs = () => ev(`window.__sent.filter(m => m.type === 'terminal_input').map(m => m.data).join('')`);
    const paste = text => ev(`(async () => { const t = ${XT}.getTerminal(); const dt = new DataTransfer(); dt.setData('text/plain', ${JSON.stringify(text)});
        t.textarea.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true })); return true; })()`);
    const modal = () => ev(`(() => { const m = document.getElementById('confirm-modal'); return m && getComputedStyle(m).display !== 'none' ? document.getElementById('confirm-modal-title').textContent : null; })()`);
    try {
        await Page.navigate({ url: BASE + '/' });
        await coralReady();
        const r = await httpJson('POST', '/api/sessions/launch', { working_dir: '/tmp', agent_type: 'terminal', display_name: 'qa-escape' });
        if (!r.data || !r.data.session_id) throw new Error(`launch: ${r.status} ${JSON.stringify(r.data)}`);
        const s = r.data; launched.push(s);
        await until(`(window._coralGetLiveSessions ? window._coralGetLiveSessions() : []).some(x => x.session_id === ${JSON.stringify(s.session_id)})`, 'the agent listed');
        await ev(`window.selectLiveSession(${JSON.stringify(s.session_name)}, 'terminal', ${JSON.stringify(s.session_id)}, ''); true`);
        await until(`(async () => ${XT}.sendTerminalInputWs(''))()`, 'the terminal socket open');
        await until(`(async () => { const t = ${XT}.getTerminal(); return !!t && !!t.textarea; })()`, 'the terminal');
        await pause(1500); // sleep-ok: the shell's first prompt and its mode switches arrive with no event to wait for

        // ── Finding 3: query replies ───────────────────────────────────
        await ev(`(async () => { ${OWN}.releaseOwnership(); window.__sent = []; return true; })()`);
        const ownedBefore = await ev(`(async () => ${OWN}.isInteractiveOwner())()`);
        await output('\x1b[c\x1b[>c\x1b[5n\x1b[6n\x1b[?6n\x1b]11;?\x07\x1b[?2004$p');
        await pause(400); // sleep-ok: proves a negative, xterm's replies get this long to be sent
        const afterQueries = await ev(`window.__sent.map(m => m.type + ':' + JSON.stringify(m.data || '')).slice(0, 6)`);
        const ownedAfter = await ev(`(async () => ${OWN}.isInteractiveOwner())()`);
        check('queries in the output send nothing (no reply, no resize)', afterQueries.length === 0, JSON.stringify(afterQueries));
        check('and don’t take the pane from another window', ownedBefore === false && ownedAfter === false, JSON.stringify({ ownedBefore, ownedAfter }));

        await ev(`(async () => { ${XT}.getTerminal().focus(); window.__sent = []; return true; })()`);
        await Input.dispatchKeyEvent({ type: 'keyDown', key: 'q', code: 'KeyQ', text: 'q', windowsVirtualKeyCode: 81 });
        await Input.dispatchKeyEvent({ type: 'keyUp', key: 'q', code: 'KeyQ', windowsVirtualKeyCode: 81 });
        await until(`window.__sent.some(m => m.type === 'terminal_input')`, 'the typed key', 4000);
        check('a real key still goes out', (await inputs()) === 'q', JSON.stringify(await inputs()));
        check('and takes the pane', await ev(`(async () => ${OWN}.isInteractiveOwner())()`));
        await ev(`(async () => { ${XT}.sendTerminalInputWs('\\x15'); return true; })()`); // clear the typed q (Ctrl+U)

        // ── Finding 1: paste ───────────────────────────────────────────
        await output('\x1b[?2004h');
        await ev(`window.__sent = []; true`);
        await paste('a\x1b[201~rm -rf x\nb\x9b\x07');
        await until(`window.__sent.some(m => m.type === 'terminal_input')`, 'the paste', 4000);
        await pause(100);
        const bracketed = await inputs();
        check('bracketed paste: an embedded ESC[201~ stays text inside the one paste',
            bracketed === '\x1b[200~a[201~rm -rf x\rb\x1b[201~' && bracketed.split('\x1b[201~').length === 2, JSON.stringify(bracketed));

        await output('\x1b[?2004l');
        await ev(`window.__sent = []; true`);
        await paste('one\ntwo\n');
        await until(`(() => { const m = document.getElementById('confirm-modal'); return !!m && getComputedStyle(m).display !== 'none'; })()`, 'the confirm', 4000);
        const title = await modal();
        await pause(200); // sleep-ok: proves a negative, nothing is sent while the question is open
        check('paste mode off: a multi-line paste asks first, and nothing is sent yet', title === 'Paste 2 lines?' && (await inputs()) === '', JSON.stringify({ title, sent: await inputs() }));
        await ev(`document.getElementById('confirm-modal-no') ? document.getElementById('confirm-modal-no').click() : window.hideConfirmModal(); true`);
        await pause(200); // sleep-ok: proves a negative, Cancel sends nothing
        check('Cancel sends nothing', (await inputs()) === '' && (await modal()) === null, JSON.stringify(await inputs()));
        await paste('one\ntwo\n');
        await until(`(() => { const m = document.getElementById('confirm-modal'); return !!m && getComputedStyle(m).display !== 'none'; })()`, 'the confirm again', 4000);
        await ev(`document.getElementById('confirm-modal-yes').click(); true`);
        await until(`window.__sent.some(m => m.type === 'terminal_input')`, 'the confirmed paste', 4000);
        await pause(100);
        check('Paste sends the lines', (await inputs()) === 'one\rtwo\r', JSON.stringify(await inputs()));

        await ev(`window.__sent = []; true`);
        await paste('echo one-line');
        await until(`window.__sent.some(m => m.type === 'terminal_input')`, 'the one-line paste', 4000);
        // The shell's new prompt (after the lines above ran) may have turned bracketed paste back on.
        const one = (await inputs()).replace(/^\x1b\[200~/, '').replace(/\x1b\[201~$/, '');
        check('one line without a newline goes at once, no question', one === 'echo one-line' && (await modal()) === null, JSON.stringify(await inputs()));
        await ev(`(async () => { ${XT}.sendTerminalInputWs('\\x15'); return true; })()`);

        // ── Finding 4: regression pins for Security's #3684 audit run ───
        // These assert what held when the audit ran against the vendored xterm 5.5.0; an xterm
        // upgrade that changes any of them trips the version pin at the end (#3684/#3688).

        // OSC 52 clipboard (write and read): inert — no OSC handler, no clipboard addon — so no
        // reply byte and no clipboard write.
        await ev(`window.__clipW = 0; try { const c = navigator.clipboard; if (c) { const o = c.writeText && c.writeText.bind(c);
            Object.defineProperty(c, 'writeText', { configurable: true, value: (...a) => { window.__clipW++; return o ? o(...a) : Promise.resolve(); } }); } } catch (e) {} true`);
        await ev(`window.__sent = []; true`);
        await output('\x1b]52;c;aGVsbG8=\x07\x1b]52;c;?\x07');
        await pause(200); // sleep-ok: proves a negative, any OSC 52 reply gets time to be sent
        check('OSC 52 is inert: no reply and no clipboard write',
            (await inputs()) === '' && (await ev(`window.__clipW`)) === 0, JSON.stringify({ sent: await inputs(), clipW: await ev(`window.__clipW`) }));

        // Window and icon titles (OSC 0/1/2, CSI 21t): Coral does not subscribe to onTitleChange,
        // so document.title never changes, and nothing is echoed back into the pane.
        const titleBefore = await ev(`document.title`);
        await ev(`window.__sent = []; true`);
        await output('\x1b]0;pwned-0\x07\x1b]1;pwned-1\x07\x1b]2;pwned-2\x07\x1b[21t');
        await pause(200); // sleep-ok: proves a negative
        check('titles (OSC 0/1/2, CSI 21t) don’t change document.title and send nothing',
            (await ev(`document.title`)) === titleBefore && (await inputs()) === '',
            JSON.stringify({ titleBefore, titleAfter: await ev(`document.title`), sent: await inputs() }));

        // XTVERSION and window reports (resize/size/push): xterm's window operations are off, so no reply.
        await ev(`window.__sent = []; true`);
        await output('\x1b[>q\x1b[18t\x1b[22t\x1b[8t');
        await pause(200); // sleep-ok: proves a negative
        check('XTVERSION and window reports (CSI 18t/22t/8t) get no reply', (await inputs()) === '', JSON.stringify(await inputs()));

        // (A link provider is asked for an absolute buffer line, not a screen row: with scrollback above the screen, as a Windows PowerShell session has, the row alone asks about another line.)
        // OSC 8 hyperlinks: only http(s) targets (any case) become links; every other scheme, a
        // relative path and a UNC path produce no link. Queried through xterm's link PROVIDER (the
        // activation boundary, which validates the URL's protocol), not the raw OSC-link store.
        const osc8Cases = [
            { marker: 'AOK0', uri: 'http://example.com/a' },
            { marker: 'AOK1', uri: 'https://example.com/b' },
            { marker: 'AOK2', uri: 'HTTPS://EXAMPLE.COM/c' },
            { marker: 'DENY0', uri: 'javascript:alert(1)' },
            { marker: 'DENY1', uri: 'data:text/html,x' },
            { marker: 'DENY2', uri: 'file:///etc/passwd' },
            { marker: 'DENY3', uri: 'blob:http://x/y' },
            { marker: 'DENY4', uri: 'about:blank' },
            { marker: 'DENY5', uri: 'ftp://h/f' },
            { marker: 'DENY6', uri: 'mailto:a@b.c' },
            { marker: 'DENY7', uri: 'ssh://h' },
            { marker: 'DENY8', uri: 'vscode://x' },
            { marker: 'DENY9', uri: 'coral://x' },
            { marker: 'DENY10', uri: 'ws://h' },
            { marker: 'DENY11', uri: 'chrome://x' },
            { marker: 'DENY12', uri: '/relative/path' },
            { marker: 'DENY13', uri: '\\\\host\\share' },
        ];
        const osc8 = await ev(`(async () => {
            const t = ${XT}.getTerminal();
            const svc = t._core && t._core._linkProviderService;
            const providers = (svc && svc.linkProviders) || [];
            const buf = () => t.buffer.active;
            const out = {};
            for (const c of ${JSON.stringify(osc8Cases)}) {
                await new Promise(r => t.write('\\r\\n\\x1b]8;;' + c.uri + '\\x07' + c.marker + '\\x1b]8;;\\x07', r));
                const baseY = buf().baseY; let row = -1;
                for (let y = 1; y <= t.rows; y++) { const ln = buf().getLine(baseY + y - 1); const s = ln ? ln.translateToString(true) : ''; if (s.indexOf(c.marker) !== -1) { row = y; break; } }
                let has = false;
                if (row !== -1) { const arrs = await Promise.all(providers.map(p => new Promise(res => { try { p.provideLinks(baseY + row, ls => res(ls || [])); } catch (e) { res([]); } }))); has = arrs.flat().length > 0; }
                out[c.marker] = { found: row !== -1, has };
            }
            return { providers: providers.length, out };
        })()`);
        check('OSC 8: a link provider is present', osc8.providers >= 1, `providers=${osc8.providers}`);
        const osc8Bad = osc8Cases.filter(c => { const r = osc8.out[c.marker] || {}; const allow = c.marker.startsWith('AOK'); return !r.found || r.has !== allow; })
            .map(c => `${c.marker}(${c.uri})=${JSON.stringify(osc8.out[c.marker])}`);
        check('OSC 8: only http(s) become links, not other schemes/relative/UNC', osc8Bad.length === 0, JSON.stringify(osc8Bad));

        // Version pin: an xterm upgrade must re-audit these behaviors (#3684/#3688).
        const xtermVer = await ev(`(async () => { try { const r = await fetch((window.__coralStatic||'/static/')+'vendor/xterm/xterm.min.js'); const t = await r.text(); const m = t.match(/@xterm\\/xterm@(\\d+\\.\\d+\\.\\d+)/); return m ? m[1] : 'no-version-header'; } catch (e) { return 'fetch-failed:' + String(e); } })()`);
        check(`vendored xterm is the audited version (pin) — ${xtermVer}`, xtermVer === '5.5.0',
            `xterm upgraded from 5.5.0 to ${xtermVer}: re-audit the terminal escape behaviors (#3684/#3688)`);
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        for (const s of launched) await httpJson('POST', `/api/sessions/live/${encodeURIComponent(s.session_name)}/kill`, { agent_type: 'terminal', session_id: s.session_id }).catch(() => {});
        await client.close();
    }
    const failed = results.filter(r => r === 'FAIL').length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
}

run().catch(err => { console.error(err); process.exit(1); });
