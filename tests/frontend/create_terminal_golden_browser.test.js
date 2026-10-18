// The terminal's creation (#4702): createTerminal (xterm_renderer.js) is a 224-line function that builds the xterm terminal (the settings' scrollback and font size, the theme, the addons), batches
// what is typed into the socket (text after 12 ms, a control or an escape at once and after any text), drops the answers xterm gives to the programme's queries, queues input while the socket is down,
// tracks focus, watches the container's size, keeps the prompt in view, tells the pane when xterm resizes, and is reused (reset, not rebuilt) for the next agent. Splitting it must change nothing. This
// creates the terminal in each situation and records its options, the frames sent over a fake socket, the focus class and the reuse. Compared with fixtures/create_terminal_golden.json (sha256 per step,
// with the readable lines). CORAL_UPDATE_GOLDEN=1 rewrites it. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server; the socket is a fake the test drives.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const GOLDEN = path.join(__dirname, 'fixtures', 'create_terminal_golden.json');
const sha = t => require('node:crypto').createHash('sha256').update(t).digest('hex');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const out = {};
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        await ev(`(async () => {
            const st = window.__coralStatic || '/static/';
            const T = window.T = { state: (await import(st + 'state.js')).state, x: await import(st + 'xterm_renderer.js') };
            await (await import(st + 'vendor_loader.js')).loadXterm(); // xterm.js and its add-ons load on first use of the Terminal view
            window.__sent = []; window.__sockets = [];
            class FakeWS { constructor(u) { this.url = String(u); this.readyState = 0; window.__sockets.push(this); this.closed = false;
                    setTimeout(() => { if (this.readyState === 0) { this.readyState = 1; this.onopen && this.onopen({}); } }, 5); }
                send(d) { window.__sent.push(String(d)); } close() { this.closed = true; this.readyState = 3; }
                addEventListener() {} removeEventListener() {} }
            FakeWS.CONNECTING = 0; FakeWS.OPEN = 1; FakeWS.CLOSING = 2; FakeWS.CLOSED = 3;
            const RealWS = window.WebSocket; window.WebSocket = function (u, p) { return /\\/ws\\/terminal\\//.test(String(u)) ? new FakeWS(u) : (p === undefined ? new RealWS(u) : new RealWS(u, p)); };
            Object.assign(window.WebSocket, { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 }); window.WebSocket.prototype = RealWS.prototype;
            let host = document.getElementById('qa-term-host'); if (!host) { host = document.createElement('div'); host.id = 'qa-term-host'; host.style.cssText = 'position:fixed;left:0;top:0;width:700px;height:400px;z-index:99999;background:#000'; document.body.appendChild(host); }
            window.__host = host;
            T.state.currentSession = { name: 't1', type: 'live', session_id: 'sess-t1', agent_type: 'claude' };
            return typeof Terminal !== 'undefined'; })()`);
        assert.equal(await ev(`typeof Terminal !== 'undefined'`), true, 'xterm is loaded');
        const sleep = ms => ev(`new Promise(r => setTimeout(r, ${ms}))`); // sleep-ok: the batching timer, the socket open and the fit land over frames; there is no completion event
        const fresh = async (settings = {}, live = true) => {
            await ev(`T.x.disposeTerminal(); window.__host.innerHTML = ''; window.__sent.length = 0; window.__sockets.length = 0;
                T.state.settings = Object.assign({}, ${JSON.stringify(settings)}); T.state.currentSession = ${live ? "{ name: 't1', type: 'live', session_id: 'sess-t1', agent_type: 'claude' }" : "{ name: 't1', type: 'history', session_id: 'sess-t1' }"}; true`);
            await ev(`window.__term = T.x.createTerminal(window.__host); true`);
        };
        const open = async () => { await ev(`T.x.connectTerminalWs('t1', 'claude', 'sess-t1', 'local'); true`); await sleep(60); await ev(`window.__sent.length = 0; true`); };
        const SNAP = `(() => { const t = window.__term, h = window.__host;
            return { has: !!t, scrollback: t && t.options.scrollback, fontSize: t && t.options.fontSize, cursorBlink: t && t.options.cursorBlink, contrast: t && t.options.minimumContrastRatio, family: t && t.options.fontFamily,
                dims: t ? [t.cols > 0, t.rows > 0] : null, container: h.className, hasXterm: !!h.querySelector('.xterm'), canvas: !!h.querySelector('canvas'), focused: h.classList.contains('xterm-focused'),
                sent: window.__sent.slice(), sockets: window.__sockets.map(s => s.url.replace(/^.*\\/ws\\//, '/ws/') + (s.closed ? ' closed' : '')), selecting: window.T.state.isSelecting === true }; })()`;
        const record = async name => { await sleep(80); out[name] = await ev(SNAP); };

        await fresh(); await record('a terminal with the default settings');
        await fresh({ terminal_scrollback: '5000', terminal_font_size: '15' }); await record('a terminal with the saved scrollback and font size');
        await fresh({ terminal_scrollback: 'x', terminal_font_size: '0' }); await record('unreadable settings fall back to the defaults');

        await fresh(); await open();
        await ev(`window.__term.input('h', true); window.__term.input('i', true); true`); await sleep(40); await record('typed text is sent in one batch');
        await ev(`window.__sent.length = 0; window.__term.input('ab', true); window.__term.input('\\x03', true); true`); await sleep(40); await record('a control character flushes the text before it and goes at once');
        await ev(`window.__sent.length = 0; window.__term.input('\\x1b[A', true); true`); await sleep(30); await record('an escape sequence goes at once');
        await ev(`window.__sent.length = 0; window.__term.write('\\x1b[c'); true`); await sleep(80); await record('what xterm answers to a query in the output is not forwarded');
        await ev(`window.__sent.length = 0; T.state.currentSession = { name: 't1', type: 'history', session_id: 'sess-t1' }; window.__term.input('z', true); true`); await sleep(40); await record('nothing is sent for an agent that is not live');
        await ev(`T.state.currentSession = { name: 't1', type: 'live', session_id: 'sess-t1', agent_type: 'claude' }; window.__term.focus(); true`); await record('focus puts the focus class on the container');
        await ev(`window.__term.blur(); true`); await record('blur takes it off');
        await ev(`window.__term.focus(); window.__term.textarea.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', ctrlKey: true, shiftKey: true, bubbles: true })); true`); await record('Ctrl+Shift+Escape lets go of the terminal');
        await ev(`window.__sent.length = 0; window.__term.resize(91, 26); true`); await sleep(60); await record('a resize of the terminal is told to the pane');

        await fresh(); await ev(`window.__term.input('q', true); true`); await sleep(40); await record('input before the socket is open is queued');
        await ev(`T.x.connectTerminalWs('t1', 'claude', 'sess-t1', 'local'); true`); await sleep(120); await record('and sent once it opens');

        await ev(`window.__sockets.length = 0; window.__sent.length = 0; window.__term2 = T.x.createTerminal(window.__host); window.__same = window.__term2 === window.__term; true`); await record('creating again reuses the terminal and lets go of the socket');
        assert.equal(await ev(`window.__same`), true);
        await ev(`T.x.disposeTerminal(); window.__host.innerHTML = ''; const real = window.Terminal; window.Terminal = undefined; window.__none = T.x.createTerminal(window.__host); window.Terminal = real; window.__term = null; true`);
        assert.equal(await ev(`window.__none`), null, 'without xterm there is no terminal');
        out['without xterm there is no terminal'] = { none: true };

        assert.ok(out['typed text is sent in one batch'].sent.join('').includes('hi'), JSON.stringify(out['typed text is sent in one batch'].sent));
        const hashes = {}, lines = {};
        for (const [name, s] of Object.entries(out)) { hashes[name] = sha(JSON.stringify(s)); lines[name] = JSON.stringify(s).slice(0, 420); }
        if (process.env.CORAL_UPDATE_GOLDEN) {
            fs.mkdirSync(path.dirname(GOLDEN), { recursive: true });
            fs.writeFileSync(GOLDEN, JSON.stringify({ about: 'what createTerminal built and sent, step by step, for create_terminal_golden_browser.test.js: sha256 of the terminal options, the frames over a fake socket, the focus class and the sockets, and the readable lines. CORAL_UPDATE_GOLDEN=1 rewrites it.', hashes, lines }, null, 2) + '\n');
            console.log('golden written: ' + Object.keys(hashes).length + ' steps');
            return;
        }
        const want = JSON.parse(fs.readFileSync(GOLDEN, 'utf8'));
        assert.deepEqual(Object.keys(hashes), Object.keys(want.hashes), 'the same steps');
        for (const k of Object.keys(want.hashes)) if (hashes[k] !== want.hashes[k]) assert.fail(`${k}: changed.\n  now:  ${lines[k]}\n  was:  ${want.lines[k]}`);
        console.log(`PASS create terminal golden: ${Object.keys(want.hashes).length} steps identical`);
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
