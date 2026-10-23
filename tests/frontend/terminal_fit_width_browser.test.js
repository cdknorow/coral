// Terminal fit to width (#3455): a window that renders the shared pane at the pane's own size (a
// phone, a View-only hub) can scale the xterm font so every column fits, instead of scrolling
// sideways. Only the font changes: the pane keeps its size and no resize is sent. A real terminal
// agent on the isolated test server; its pane is sized with tmux on the test server's own socket
// (CORAL_TEST_TMUX_SOCKET), so no owner window is involved.
//   1. a phone, 80-col pane: the toggle shows (labelled, not pressed), the pane scrolls; on: the
//      font shrinks (not below 7 px) until all columns fit; the pane is unchanged, nothing is sent;
//   2. remembered on this device: a reload comes back fitted;
//   3. a 200-col pane: it stops at 7 px and scrolls the rest;
//   4. off: the Settings font and scrolling again, still off after a reload;
//   5. the toggle is terminal-only: hidden in the chat view;
//   6. a phone through a View-only Coral Hub (in-page fake hub, as in terminal_pane_size): fits too;
//   7. a desktop window that sets the pane's size never shows it and keeps the Settings font, even
//      with the setting on.
//
// Drives Chrome over CDP (chrome-remote-interface); run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
require('./helpers/requires_tmux')('terminal_fit_width_browser');
const { execFileSync } = require('./helpers/child_process.js');
const CDP = require('chrome-remote-interface');

const BASE = require('./test_base').BASE;
const CDP_PORT = Number(process.env.CDP_PORT || 9222);
const SOCK = process.env.CORAL_TEST_TMUX_SOCKET || '';
const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: poll interval inside condition waits
const MIN_FONT = 7;          // a phone's (its own choice)
const DESKTOP_MIN_FONT = 11; // a window with room: readable at a glance (#6231)

if (!SOCK || /\/\.coral\//.test(SOCK)) {
    console.error(`CORAL_TEST_TMUX_SOCKET must name the test server's own tmux socket (got ${JSON.stringify(SOCK)}); run through run.sh or run_cdp_suites.mjs`);
    process.exit(2);
}

function tmux(...args) {
    const env = { ...process.env }; delete env.TMUX; delete env.TMUX_PANE; // never the agent shell's server
    return execFileSync('tmux', ['-S', SOCK, ...args], { env, encoding: 'utf8' });
}
function paneSize(name) {
    const [cols, rows] = tmux('display-message', '-p', '-t', `=${name}:`, '#{pane_width} #{pane_height}').trim().split(/\s+/).map(Number);
    return { cols, rows };
}
function setPane(name, cols, rows) {
    tmux('resize-window', '-t', `=${name}:`, '-x', String(cols), '-y', String(rows));
    assert.deepEqual(paneSize(name), { cols, rows }, 'tmux sized the pane');
}

async function httpJson(method, path, body) {
    const r = await fetch(BASE + path, { method, headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
    const text = await r.text();
    let data = null; try { data = JSON.parse(text); } catch { data = text; }
    return { status: r.status, data };
}

async function browserClient() {
    const info = await (await fetch(`http://127.0.0.1:${CDP_PORT}/json/version`)).json();
    return CDP({ target: info.webSocketDebuggerUrl });
}
async function newContext() { const b = await browserClient(); const { browserContextId } = await b.Target.createBrowserContext(); await b.close(); return browserContextId; }
async function disposeContext(id) { try { const b = await browserClient(); await b.Target.disposeBrowserContext({ browserContextId: id }); await b.close(); } catch { /* already gone */ } }

// Records the resizes a window sends; window.__fakeHubView makes it a View-only Coral Hub (every
// frame but a ping dropped and answered with hub_read_only, as pkg/hubproxy does).
const PRELOAD = `(() => {
    const RealWS = window.WebSocket;
    window.__termSockets = []; window.__resizesSent = 0; window.__httpResizes = 0;
    function W(u, p) {
        const ws = p === undefined ? new RealWS(u) : new RealWS(u, p);
        if (String(u).includes('/ws/terminal/')) {
            window.__termSockets.push(ws);
            const send = ws.send.bind(ws);
            ws.send = data => {
                let m = null; try { m = JSON.parse(data); } catch {}
                if (m && m.type === 'terminal_resize') window.__resizesSent++;
                if (window.__fakeHubView && !(m && m.type === 'ping')) {
                    const notice = { type: 'hub_read_only', required: 'interact', hub_mode: 'view' };
                    if (m && Number.isInteger(m.id)) notice.id = m.id;
                    setTimeout(() => ws.dispatchEvent(new MessageEvent('message', { data: JSON.stringify(notice) })), 0);
                    return;
                }
                return send(data);
            };
        }
        return ws;
    }
    W.prototype = RealWS.prototype; W.CONNECTING = 0; W.OPEN = 1; W.CLOSING = 2; W.CLOSED = 3;
    window.WebSocket = W;
    const realFetch = window.fetch.bind(window);
    window.fetch = (input, init) => {
        try {
            if (String(input && input.url || input).split('?')[0].endsWith('/resize')) {
                window.__httpResizes++;
                if (window.__fakeHubView) return Promise.resolve(new Response(JSON.stringify({ error: 'local_mode', required: 'interact' }), { status: 403, headers: { 'content-type': 'application/json' } }));
            }
        } catch {}
        return realFetch(input, init);
    };
})();`;

// The xterm, its font, whether the container scrolls sideways, and the toggle.
const VIEW = `(async () => {
    const m = await import((window.__coralStatic || '/static/') + 'xterm_renderer.js');
    const t = m.getTerminal();
    const c = document.getElementById('xterm-container');
    const b = document.getElementById('terminal-fit-width-btn');
    const screen = c && c.querySelector('.xterm-screen');
    return t ? { cols: t.cols, rows: t.rows, font: m.terminalFontSize(), following: m.isFollowingPane(), wsOpen: m.isTerminalWsOpen(),
        scrolls: c.scrollWidth > c.clientWidth + 1, screenW: screen ? Math.round(screen.getBoundingClientRect().width) : 0, clientW: c.clientWidth,
        btn: b ? { shown: !b.hidden && b.offsetParent !== null, pressed: b.getAttribute('aria-pressed'), label: b.getAttribute('aria-label'), tag: b.tagName } : null,
        sent: window.__resizesSent, http: window.__httpResizes } : null;
})()`;

async function openTab({ contextId, width, height, mobile, fakeHubView = false }) {
    const b = await browserClient();
    const { targetId } = await b.Target.createTarget({ url: 'about:blank', browserContextId: contextId });
    await b.close();
    const client = await CDP({ port: CDP_PORT, target: targetId });
    const { Page, Runtime, Emulation } = client;
    await Promise.all([Page.enable(), Runtime.enable()]);
    await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD + (fakeHubView ? 'window.__fakeHubView = true;' : '') });
    await Emulation.setDeviceMetricsOverride({ width, height, deviceScaleFactor: 1, mobile });
    await Page.bringToFront();
    const ev = async expr => {
        const r = await Runtime.evaluate({ expression: expr, awaitPromise: true, returnByValue: true });
        if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const waitFor = async (expr, what, ms = 10000) => {
        const end = Date.now() + ms;
        let last;
        for (;;) {
            try { last = await ev(expr); if (last) return last; } catch { /* page still loading */ }
            if (Date.now() > end) throw new Error(`timed out waiting for ${what} (last: ${JSON.stringify(last)}; view: ${JSON.stringify(await ev(VIEW).catch(e => String(e)))})`);
            await sleep(50);
        }
    };
    const load = async () => {
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await waitFor(`typeof window.selectLiveSession === 'function' && typeof window.setLiveViewMode === 'function'`, 'the dashboard');
    };
    // Leave the dashboard before closing: a closing tab loses its emulated size (#3422 test notes).
    const close = async () => {
        try { await Page.navigate({ url: 'about:blank' }); await Page.loadEventFired(); } catch {}
        try { const c = await browserClient(); await c.Target.closeTarget({ targetId }); await c.close(); } catch {}
        try { await client.close(); } catch {}
    };
    await load();
    return { ev, waitFor, load, close };
}

// Shows agent `a` in the terminal view and waits until the xterm renders the pane (cols x rows).
async function show(tab, a, pane) {
    await tab.ev(`(() => { setLiveViewMode('terminal'); selectLiveSession(${JSON.stringify(a.name)}, 'terminal', ${JSON.stringify(a.sid)}); return true; })()`);
    return tab.waitFor(`(async () => { const v = await ${VIEW}; return v && v.wsOpen && v.cols === ${pane.cols} && v.rows === ${pane.rows} && v.screenW > 0 ? v : null; })()`, `the xterm at ${pane.cols}x${pane.rows}`);
}
const click = tab => tab.ev(`(() => { document.getElementById('terminal-fit-width-btn').click(); return true; })()`);
// Waits for the font to settle at a value `ok` accepts.
const settled = (tab, what, ok) => tab.waitFor(`(async () => { const v = await ${VIEW}; return v && (${ok}) ? v : null; })()`, what);

function assertFits(v, what) {
    assert.ok(v.font >= MIN_FONT && v.font < 13, `${what}: the font shrank, not below ${MIN_FONT} (${JSON.stringify(v)})`);
    assert.ok(!v.scrolls && v.screenW <= v.clientW, `${what}: every column fits (${JSON.stringify(v)})`);
}

(async () => {
    const contexts = [];
    const tabs = [];
    let A = null;
    try {
        const r = await httpJson('POST', '/api/sessions/launch', { working_dir: '/tmp', agent_type: 'terminal', display_name: 'Fit width' });
        assert.equal(r.status, 200, `launch: ${JSON.stringify(r.data)}`);
        A = { name: r.data.session_name, sid: r.data.session_id };
        const narrow = { cols: 80, rows: 24 };
        setPane(A.name, narrow.cols, narrow.rows);

        const phoneCtx = await newContext(); contexts.push(phoneCtx);
        const phone = await openTab({ contextId: phoneCtx, width: 390, height: 844, mobile: true });
        tabs.push(phone);

        console.log('1. A phone on an 80-column pane');
        let v = await show(phone, A, narrow);
        assert.equal(v.following, true, 'the phone renders the pane at its size');
        assert.equal(v.font, 13, 'the Settings font');
        assert.ok(v.scrolls, `the pane is wider than the phone (${JSON.stringify(v)})`);
        v = await settled(phone, 'the toggle shown', 'v.btn && v.btn.shown');
        assert.equal(v.btn.tag, 'BUTTON');
        assert.equal(v.btn.pressed, 'false');
        assert.equal(v.btn.label, 'Fit the terminal to the screen width');
        await click(phone);
        v = await settled(phone, 'the font scaled', `v.font < 13 && v.btn.pressed === 'true'`);
        assertFits(v, 'fit on');
        assert.equal(v.btn.label, 'Show the terminal at full size (scroll sideways)');
        assert.deepEqual({ cols: v.cols, rows: v.rows }, narrow, 'the xterm keeps the pane\'s columns and rows');
        assert.deepEqual(paneSize(A.name), narrow, 'the pane is unchanged');
        assert.equal(v.sent, 0, 'no resize sent'); assert.equal(v.http, 0, 'no HTTP pane-width sync');
        const fitted = v.font;
        console.log(`   ✓ 13 px scrolls; on: ${fitted} px, ${v.screenW} <= ${v.clientW} px, pane still ${narrow.cols}x${narrow.rows}, nothing sent`);

        console.log('2. Remembered on this device');
        await phone.load();
        await show(phone, A, narrow);
        v = await settled(phone, 'fitted after a reload', `v.btn && v.btn.pressed === 'true' && v.font < 13`);
        assertFits(v, 'after a reload');
        assert.equal(v.font, fitted, 'the same size as before');
        console.log(`   ✓ ${v.font} px after a reload`);

        console.log('3. A 200-column pane stops at the minimum and scrolls');
        const wide = { cols: 200, rows: 50 };
        setPane(A.name, wide.cols, wide.rows);
        await phone.load();
        await show(phone, A, wide);
        v = await settled(phone, 'the minimum font', `v.font === ${MIN_FONT}`);
        assert.ok(v.scrolls, `the rest scrolls (${JSON.stringify(v)})`);
        assert.equal(v.btn.pressed, 'true');
        assert.deepEqual(paneSize(A.name), wide);
        assert.equal(v.sent, 0);
        console.log(`   ✓ ${MIN_FONT} px, ${v.screenW} px wide in ${v.clientW}: scrolls`);

        console.log('4. Off: the Settings font and scrolling, kept off after a reload');
        await click(phone);
        v = await settled(phone, 'the Settings font', `v.font === 13 && v.btn.pressed === 'false'`);
        assert.ok(v.scrolls);
        await phone.load();
        v = await show(phone, A, wide);
        v = await settled(phone, 'the toggle after a reload', 'v.btn && v.btn.shown');
        assert.equal(v.btn.pressed, 'false'); assert.equal(v.font, 13);
        console.log('   ✓ 13 px and scrolling; still off after a reload');

        console.log('5. Terminal view only');
        // A terminal agent has no transcript, so it never gets the chat view: set the class the chat
        // view puts on the wrapper (live_chat.js applyLiveViewMode).
        const chat = on => phone.ev(`document.getElementById('capture-wrapper').classList.toggle('chat-mode', ${on}); true`);
        await chat(true);
        v = await settled(phone, 'the toggle hidden in chat', 'v.btn && !v.btn.shown');
        await chat(false);
        await settled(phone, 'the toggle back in the terminal', 'v.btn && v.btn.shown');
        console.log('   ✓ hidden in chat, back in the terminal');
        await phone.close(); tabs.pop();

        console.log('6. A phone through a View-only hub');
        setPane(A.name, narrow.cols, narrow.rows);
        const hubCtx = await newContext(); contexts.push(hubCtx);
        const hub = await openTab({ contextId: hubCtx, width: 390, height: 844, mobile: true, fakeHubView: true });
        tabs.push(hub);
        v = await show(hub, A, narrow);
        await settled(hub, 'the toggle shown', 'v.btn && v.btn.shown');
        await click(hub);
        v = await settled(hub, 'the font scaled', `v.font < 13 && v.btn.pressed === 'true'`);
        assertFits(v, 'through the hub');
        assert.deepEqual(paneSize(A.name), narrow);
        console.log(`   ✓ ${v.font} px through the hub, pane unchanged`);
        await hub.close(); tabs.pop();

        console.log('7. A desktop window that sets the size: no toggle, the Settings font');
        const deskCtx = await newContext(); contexts.push(deskCtx);
        const desk = await openTab({ contextId: deskCtx, width: 1400, height: 900, mobile: false });
        tabs.push(desk);
        await desk.ev(`localStorage.setItem('coral-terminal-fit-width', '1'); true`);
        await desk.load();
        await desk.ev(`(() => { setLiveViewMode('terminal'); selectLiveSession(${JSON.stringify(A.name)}, 'terminal', ${JSON.stringify(A.sid)}); return true; })()`);
        v = await desk.waitFor(`(async () => { const v = await ${VIEW}; return v && v.wsOpen && !v.following && v.sent > 0 ? v : null; })()`, 'the desktop sizing the pane');
        assert.equal(v.font, 13);
        assert.equal(v.btn.shown, false, 'no toggle where the window fits the pane');
        console.log(`   ✓ ${v.cols} cols at 13 px, toggle hidden`);

        console.log('8. A desktop viewing a pane wider than its window (a View-only hub): fit never goes below a readable font (#6231)');
        const wider = { cols: 120, rows: 24 };
        setPane(A.name, wider.cols, wider.rows);
        const deskHubCtx = await newContext(); contexts.push(deskHubCtx);
        const deskHub = await openTab({ contextId: deskHubCtx, width: 1280, height: 900, mobile: false, fakeHubView: true });
        tabs.push(deskHub);
        await deskHub.ev(`localStorage.setItem('coral-terminal-fit-width', '1'); true`);
        await deskHub.load();
        await deskHub.ev(`(() => { setLiveViewMode('terminal'); selectLiveSession(${JSON.stringify(A.name)}, 'terminal', ${JSON.stringify(A.sid)}); return true; })()`);
        v = await deskHub.waitFor(`(async () => { const v = await ${VIEW}; return v && v.wsOpen && v.following && v.btn && v.btn.pressed === 'true' ? v : null; })()`, 'the desktop following a pane with fit on');
        assert.ok(v.font >= DESKTOP_MIN_FONT, `a desktop never fits below ${DESKTOP_MIN_FONT} px (it was ${v.font}, ${v.cols} columns in ${v.clientW} px)`);
        console.log(`   ✓ ${v.font} px at 1280 px with ${v.cols} columns in ${v.clientW} px (${v.scrolls ? 'the rest scrolls' : 'all fit'}), never below ${DESKTOP_MIN_FONT}`);
        await deskHub.close(); tabs.pop();

        console.log('\nAll terminal fit-to-width checks passed');
    } finally {
        for (const t of tabs) await t.close();
        for (const c of contexts) await disposeContext(c);
        if (A) await httpJson('POST', `/api/sessions/live/${encodeURIComponent(A.name)}/kill`, { agent_type: 'terminal', session_id: A.sid }).catch(() => {});
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
