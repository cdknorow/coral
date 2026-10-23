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
// Size the pane and wait until tmux says it has that size (a window that a browser tab was following can be sized back by that tab's last resize message: ask again until it holds, and fail if it never does).
function setPane(name, cols, rows) {
    let last = null;
    for (let i = 0; i < 30; i++) {
        tmux('resize-window', '-t', `=${name}:`, '-x', String(cols), '-y', String(rows));
        last = paneSize(name);
        if (last.cols === cols && last.rows === rows) return;
        execFileSync('sleep', ['0.1']); // sleep-ok: waiting for tmux, which has no event to wait for here
    }
    assert.deepEqual(last, { cols, rows }, 'tmux sized the pane');
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

const PHONE_BASE = 10; // a phone-wide window with no size chosen in Settings draws at 10 px (terminal_font.js, #6577); a desktop at 13

async function openTab({ contextId, width, height, mobile, fakeHubView = false, dpr = 1 }) {
    const b = await browserClient();
    const { targetId } = await b.Target.createTarget({ url: 'about:blank', browserContextId: contextId });
    await b.close();
    const client = await CDP({ port: CDP_PORT, target: targetId });
    const { Page, Runtime, Emulation } = client;
    await Promise.all([Page.enable(), Runtime.enable()]);
    await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD + (fakeHubView ? 'window.__fakeHubView = true;' : '') });
    await Emulation.setDeviceMetricsOverride({ width, height, deviceScaleFactor: dpr, mobile });
    await Emulation.setTouchEmulationEnabled({ enabled: !!mobile }); // a phone has a coarse pointer
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
    const metrics = (w, h) => Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: dpr, mobile });
    return { ev, waitFor, load, close, metrics };
}

// Shows agent `a` in the terminal view and waits until the xterm renders the pane (cols x rows).
async function show(tab, a, pane) {
    await tab.ev(`(() => { setLiveViewMode('terminal'); selectLiveSession(${JSON.stringify(a.name)}, 'terminal', ${JSON.stringify(a.sid)}); return true; })()`);
    if (!pane) return tab.waitFor(`(async () => { const v = await ${VIEW}; return v && v.wsOpen && v.cols > 0 && v.screenW > 0 ? v : null; })()`, 'the xterm'); // a window that owns the pane sizes it to itself
    return tab.waitFor(`(async () => { const v = await ${VIEW}; return v && v.wsOpen && v.cols === ${pane.cols} && v.rows === ${pane.rows} && v.screenW > 0 ? v : null; })()`, `the xterm at ${pane.cols}x${pane.rows}`);
}
const click = tab => tab.ev(`(() => { document.getElementById('terminal-fit-width-btn').click(); return true; })()`);
// Waits for the font to settle at a value `ok` accepts.
const settled = (tab, what, ok) => tab.waitFor(`(async () => { const v = await ${VIEW}; return v && (${ok}) ? v : null; })()`, what);

function assertFits(v, what) {
    assert.ok(v.font >= MIN_FONT && v.font < PHONE_BASE, `${what}: the font shrank from the phone's ${PHONE_BASE}, not below ${MIN_FONT} (${JSON.stringify(v)})`);
    assert.ok(!v.scrolls && v.screenW <= v.clientW, `${what}: every column fits (${JSON.stringify(v)})`);
}

(async () => {
    const contexts = [];
    const tabs = [];
    let A = null;
    const guard = await require('./helpers/settings_guard').guardSettings(); // terminal_font_size is written below: put back as found, not left as ''
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
        assert.equal(v.font, PHONE_BASE, 'no size chosen: a phone draws at its own default, 10 (#6577)');
        assert.ok(v.scrolls, `the pane is wider than the phone (${JSON.stringify(v)})`);
        v = await settled(phone, 'the toggle shown', 'v.btn && v.btn.shown');
        assert.equal(v.btn.tag, 'BUTTON');
        assert.equal(v.btn.pressed, 'false');
        assert.equal(v.btn.label, 'Fit the terminal to the screen width');
        await click(phone);
        v = await settled(phone, 'the font scaled', `v.font < ${PHONE_BASE} && v.btn.pressed === 'true'`);
        assertFits(v, 'fit on');
        assert.equal(v.btn.label, 'Show the terminal at full size (scroll sideways)');
        assert.deepEqual({ cols: v.cols, rows: v.rows }, narrow, 'the xterm keeps the pane\'s columns and rows');
        assert.deepEqual(paneSize(A.name), narrow, 'the pane is unchanged');
        assert.equal(v.sent, 0, 'no resize sent'); assert.equal(v.http, 0, 'no HTTP pane-width sync');
        const fitted = v.font;
        console.log(`   ✓ ${PHONE_BASE} px scrolls; on: ${fitted} px, ${v.screenW} <= ${v.clientW} px, pane still ${narrow.cols}x${narrow.rows}, nothing sent`);

        console.log('2. Remembered on this device');
        await phone.load();
        await show(phone, A, narrow);
        v = await settled(phone, 'fitted after a reload', `v.btn && v.btn.pressed === 'true' && v.font < ${PHONE_BASE}`);
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
        v = await settled(phone, 'the Settings font', `v.font === ${PHONE_BASE} && v.btn.pressed === 'false'`);
        assert.ok(v.scrolls);
        await phone.load();
        v = await show(phone, A, wide);
        v = await settled(phone, 'the toggle after a reload', 'v.btn && v.btn.shown');
        assert.equal(v.btn.pressed, 'false'); assert.equal(v.font, PHONE_BASE);
        console.log(`   ✓ ${PHONE_BASE} px and scrolling; still off after a reload`);

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
        v = await settled(hub, 'the font scaled', `v.font < ${PHONE_BASE} && v.btn.pressed === 'true'`);
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
        await desk.close(); tabs.pop(); // step 8 sizes the pane itself: this tab must not follow and resize it back

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

        console.log('9. A phone draws smaller by default, at 390 px with the pixel ratios of a phone (#6577)');
        const eighty = { cols: 80, rows: 24 };
        setPane(A.name, eighty.cols, eighty.rows);
        const cellPx = v => v.screenW / v.cols;
        const seen = {};
        for (const dpr of [1, 3]) {
            const ctx = await newContext(); contexts.push(ctx);
            const tab = await openTab({ contextId: ctx, width: 390, height: 844, mobile: true, dpr });
            tabs.push(tab);
            v = await show(tab, A, eighty);
            assert.equal(v.font, PHONE_BASE, `DPR ${dpr}: no size chosen, a phone draws at ${PHONE_BASE} px (was 13)`);
            const shown = Math.floor(v.clientW / cellPx(v));
            assert.ok(shown >= 58, `DPR ${dpr}: a 390 px phone shows about 60 columns of an 80-column pane at ${PHONE_BASE} px, not 46 (${shown}; ${JSON.stringify(v)})`);
            seen[dpr] = { font: v.font, cell: Math.round(cellPx(v) * 100) / 100, columns: shown };
            if (dpr === 1) { // turned to landscape (wider than a phone) and back: the font is read when the terminal is built, so nothing redraws it under the person
                await tab.metrics(844, 390); await sleep(500); // sleep-ok: no event for a window resize settling
                assert.equal((await tab.ev(VIEW)).font, PHONE_BASE, 'turning the phone to landscape does not change the font');
                await tab.metrics(390, 844); await sleep(500); // sleep-ok: as above
                assert.equal((await tab.ev(VIEW)).font, PHONE_BASE, 'and back again');
                setPane(A.name, eighty.cols, eighty.rows); // a window wide enough to own the pane sized it to itself: the next steps want the 80 columns back
            }
            await tab.close(); tabs.pop();
        }
        console.log(`   ✓ ${PHONE_BASE} px at DPR 1 and 3: ${JSON.stringify(seen)}`);

        console.log('10. A size chosen in Settings wins on the phone, and a desktop is unchanged');
        const put = body => httpJson('PUT', '/api/settings', body);
        try {
            assert.equal((await put({ terminal_font_size: '14' })).status, 200);
            const ctx = await newContext(); contexts.push(ctx);
            const tab = await openTab({ contextId: ctx, width: 390, height: 844, mobile: true });
            tabs.push(tab);
            v = await show(tab, A, eighty);
            assert.equal(v.font, 14, 'a chosen 14 is drawn at 14 on a phone');
            await tab.close(); tabs.pop();
            assert.equal((await put({ terminal_font_size: '13' })).status, 200);
            const ctx2 = await newContext(); contexts.push(ctx2);
            const tab2 = await openTab({ contextId: ctx2, width: 390, height: 844, mobile: true });
            tabs.push(tab2);
            v = await show(tab2, A, eighty);
            assert.equal(v.font, 13, 'a chosen 13 (the old default) is a choice: it stays 13 on a phone');
            await tab2.close(); tabs.pop();
        } finally { await guard.restore(); }
        console.log('   ✓ 14 and a chosen 13 hold on a phone; the desktop steps above keep 13');

        console.log('11. The canvas is drawn at CSS size x pixel ratio, so text is not 2-3x too large at DPR 2 and 3 (#6596)');
        // Chrome's device-metrics emulation tells xterm's own observer that the canvas is its CSS size at DPR 2 and 3, so the backing store stayed at 480 px with 18 px cells.
        const CANVAS = `(() => { const sc = document.querySelector('#xterm-container .xterm-screen'); const cs = [...document.querySelectorAll('#xterm-container canvas')];
            return { dpr: window.devicePixelRatio, cssW: sc.getBoundingClientRect().width, backing: cs.map(c => c.width), count: cs.length }; })()`;
        const scaled = async (tab, what) => {
            const c = await tab.waitFor(`(() => { const c = ${CANVAS}; return c.count > 0 && c.cssW > 0 ? c : null; })()`, `${what}: the canvases`);
            // a canvas's backing store is its CSS width x the pixel ratio (within one device pixel per column of xterm's own rounding)
            const bad = c.backing.filter(w => Math.abs(w - c.cssW * c.dpr) > 80);
            assert.deepEqual(bad, [], `${what}: every canvas is CSS ${c.cssW} px x DPR ${c.dpr} wide in device pixels (${JSON.stringify(c)})`);
            return c;
        };
        const sizes = {};
        for (const [label, w, h, mobile, dpr] of [['phone DPR 2', 390, 844, true, 2], ['phone DPR 3', 390, 844, true, 3], ['desktop DPR 2', 1280, 800, false, 2], ['phone DPR 1', 390, 844, true, 1]]) {
            if (mobile) setPane(A.name, eighty.cols, eighty.rows); // a phone follows the pane at its size: set it, since the desktop window before it (and the earlier steps) sized the pane to themselves
            const ctx = await newContext(); contexts.push(ctx);
            const tab = await openTab({ contextId: ctx, width: w, height: h, mobile, dpr });
            tabs.push(tab);
            await show(tab, A, mobile ? eighty : null); // the desktop window sizes the pane to itself (69 columns in a small window): any size will do for the canvas
            sizes[label] = (await scaled(tab, label)).backing[0];
            if (mobile) { // a font change redraws the canvas: the fit button shrinks the font, and the canvas stays at its device size
                await click(tab);
                await settled(tab, `${label}: fitted`, `v.btn.pressed === 'true' && v.font < ${PHONE_BASE}`);
                await scaled(tab, `${label} after the font changed`);
            }
            await tab.close(); tabs.pop();
        }
        console.log(`   ✓ the canvas backing store follows the pixel ratio, on load and after a font change: ${JSON.stringify(sizes)}`);

        console.log('12. A phone turned to landscape follows the pane at its size and never sets it (#6675)');
        // 844x390 and 800x360 are above the 767 px breakpoint, so they drew the desktop layout and sized the shared pane to themselves (80x24 became 53x15, and 53x3 with the keyboard up) for everyone watching.
        const landscape = {};
        for (const [label, w, h] of [['844x390', 844, 390], ['800x360', 800, 360], ['844x200 (keyboard open)', 844, 200]]) {
            setPane(A.name, eighty.cols, eighty.rows);
            const ctx = await newContext(); contexts.push(ctx);
            const tab = await openTab({ contextId: ctx, width: w, height: h, mobile: true });
            tabs.push(tab);
            await show(tab, A, null);
            await sleep(900); // sleep-ok: a resize that must NOT happen: nothing to wait for
            const after = paneSize(A.name);
            assert.deepEqual(after, eighty, `${label}: a landscape phone does not set the shared pane's size (it is ${after.cols}x${after.rows})`);
            const view = await tab.waitFor(`(async () => { const v = await ${VIEW}; return v && v.following && v.cols === ${eighty.cols} && v.rows === ${eighty.rows} ? v : null; })()`, `${label}: the xterm following the pane at 80x24`, 15000); // it catches up with the pane within a few seconds
            assert.deepEqual(paneSize(A.name), eighty, `${label}: and the pane is still 80x24 once it has`);
            assert.equal(view.sent + view.http, 0, `${label}: no resize was sent (${JSON.stringify(view)})`);
            landscape[label] = { pane: `${after.cols}x${after.rows}`, font: view.font };
            await tab.close(); tabs.pop();
        }
        console.log(`   ✓ the pane stays 80x24 for a phone in landscape: ${JSON.stringify(landscape)}`);

        console.log('\nAll terminal fit-to-width checks passed');
    } finally {
        for (const t of tabs) await t.close();
        for (const c of contexts) await disposeContext(c);
        await guard.restore().catch(() => {});
        if (A) await httpJson('POST', `/api/sessions/live/${encodeURIComponent(A.name)}/kill`, { agent_type: 'terminal', session_id: A.sid }).catch(() => {});
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
