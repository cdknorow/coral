// Terminal pane size (#3422): the tmux pane has one size and every window shows it. Real terminal
// agents on the isolated test server; the pane's size is read from tmux itself ('tmux -S' on the test
// server's own socket, CORAL_TEST_TMUX_SOCKET), the window's from its xterm.
//
// The window that sets the size (the interactive owner, Interact, desktop width) keeps the pane equal
// to its xterm:
//   1. on open;  2. after window resizes;  3. after chat -> terminal (resized while in chat);
//   4. after another agent and back;  5. after a terminal socket reconnect while the server is busy
//      (503 on /resize and /poll during it);  6. when the terminal mounts hidden (zero size) and is
//      shown later.
// A window that does not set it renders the xterm at the pane's size, never its own, and the pane
// keeps the owner's size:
//   7. a same-browser viewer (the popout, not the owner);
//   8. a phone (another device: its own browser profile), with Interact;
//   9. a phone through a View-only Coral Hub (the in-page fake hub drops every frame but pings and
//      answers hub_read_only, as pkg/hubproxy does; TestTerminalSizeFramesPassAViewOnlyHub covers
//      the real proxy passing terminal_size);
//  10. a desktop through a View-only hub.
// After each of 7-10 the owner's window still matches the pane.
//
// Drives Chrome over CDP (chrome-remote-interface); run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const { safePaused } = require('./helpers/paused');
const { execFileSync } = require('./helpers/child_process.js');
const CDP = require('chrome-remote-interface');

const BASE = require('./test_base').BASE;
const CDP_PORT = Number(process.env.CDP_PORT || 9222);
const SOCK = process.env.CORAL_TEST_TMUX_SOCKET || '';
const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: poll interval inside condition waits

if (!SOCK || /\/\.coral\//.test(SOCK)) {
    console.error(`CORAL_TEST_TMUX_SOCKET must name the test server's own tmux socket (got ${JSON.stringify(SOCK)}); run through run.sh or run_cdp_suites.mjs`);
    process.exit(2);
}

// The pane's size from tmux on the test socket ($TMUX dropped: never the agent shell's server).
function paneSize(tmuxName) {
    const env = { ...process.env }; delete env.TMUX; delete env.TMUX_PANE;
    const out = execFileSync('tmux', ['-S', SOCK, 'display-message', '-p', '-t', `=${tmuxName}:`, '#{pane_width} #{pane_height}'], { env, encoding: 'utf8' });
    const [cols, rows] = out.trim().split(/\s+/).map(Number);
    return { cols, rows };
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

// Installed before the app: records the terminal sockets and the resizes each window sends, and
// (window.__fakeHubView) behaves as a View-only Coral Hub on them: every frame but a ping is dropped
// and answered with hub_read_only, the frame pkg/hubproxy sends.
const PRELOAD = `(() => {
    const RealWS = window.WebSocket;
    window.__termSockets = []; window.__resizesSent = []; window.__resizesDropped = 0;
    function W(u, p) {
        const ws = p === undefined ? new RealWS(u) : new RealWS(u, p);
        if (String(u).includes('/ws/terminal/')) {
            window.__termSockets.push(ws);
            const send = ws.send.bind(ws);
            ws.send = data => {
                let m = null; try { m = JSON.parse(data); } catch {}
                if (window.__fakeHubView && !(m && m.type === 'ping')) {
                    if (m && m.type === 'terminal_resize') window.__resizesDropped++;
                    const notice = { type: 'hub_read_only', required: 'interact', hub_mode: 'view' };
                    if (m && Number.isInteger(m.id)) notice.id = m.id;
                    setTimeout(() => ws.dispatchEvent(new MessageEvent('message', { data: JSON.stringify(notice) })), 0);
                    return;
                }
                if (m && m.type === 'terminal_resize') window.__resizesSent.push({ cols: m.cols, rows: m.rows, at: Date.now() % 100000, stack: (new Error().stack || '').split('\\n').slice(2, 6).map(l => l.trim().replace(/^at /, '').replace(/https?:[^ )]*\\//g, '')).join(' < ') });
                return send(data);
            };
        }
        return ws;
    }
    W.prototype = RealWS.prototype; W.CONNECTING = 0; W.OPEN = 1; W.CLOSING = 2; W.CLOSED = 3;
    window.WebSocket = W;
    // The capture's pane-width sync resizes over HTTP (POST .../resize): recorded too.
    window.__httpResizes = [];
    const realFetch = window.fetch.bind(window);
    window.fetch = (input, init) => {
        try {
            const u = String(input && input.url || input);
            if (u.split('?')[0].endsWith('/resize')) {
                window.__httpResizes.push({ at: Date.now() % 100000, url: u.split('/live/')[1], body: init && init.body });
                // The View-only hub refuses it, as the real one does for an interact route.
                if (window.__fakeHubView) return Promise.resolve(new Response(JSON.stringify({ error: 'local_mode', required: 'interact' }), { status: 403, headers: { 'content-type': 'application/json' } }));
            }
        } catch {}
        return realFetch(input, init);
    };
})();`;

const XTERM = `(async () => {
    const m = await import((window.__coralStatic || '/static/') + 'xterm_renderer.js');
    const t = m.getTerminal();
    const c = document.getElementById('xterm-container');
    return t ? { cols: t.cols, rows: t.rows, following: typeof m.isFollowingPane === 'function' ? m.isFollowingPane() : null, wsOpen: m.isTerminalWsOpen(),
        scrolls: !!c && c.scrollWidth > c.clientWidth + 2, shown: !!c && c.offsetWidth > 0 } : null;
})()`;

async function openTab(url, { contextId, width = 1400, height = 900, mobile = false, fakeHubView = false } = {}) {
    const b = await browserClient();
    const { targetId } = await b.Target.createTarget(Object.assign({ url: 'about:blank' }, contextId ? { browserContextId: contextId } : {}));
    await b.close();
    const client = await CDP({ port: CDP_PORT, target: targetId });
    const { Page, Runtime, Emulation, Fetch } = client;
    await Promise.all([Page.enable(), Runtime.enable()]);
    await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD + (fakeHubView ? 'window.__fakeHubView = true;' : '') });
    const size = async (w, h) => Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
    await size(width, height);
    const ev = async expr => {
        const r = await Runtime.evaluate({ expression: expr, awaitPromise: true, returnByValue: true });
        if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const waitFor = async (expr, what, ms = 10000) => {
        const end = Date.now() + ms;
        for (;;) {
            try { if (await ev(expr)) return; } catch { /* page still loading */ }
            if (Date.now() > end) throw new Error('timed out waiting for ' + what);
            await sleep(50);
        }
    };
    await Page.navigate({ url }); await Page.loadEventFired();
    // Leave the dashboard before closing: closing (or detaching) drops the emulated size, and a phone
    // tab relaid out at the browser window's desktop size would resize the pane on its way out.
    const close = async () => {
        try { await Page.navigate({ url: 'about:blank' }); await Page.loadEventFired(); } catch {}
        try { const c = await browserClient(); await c.Target.closeTarget({ targetId }); await c.close(); } catch {}
        try { await client.close(); } catch {}
    };
    return { client, ev, waitFor, size, close, Fetch };
}

// Waits until the window's xterm and the tmux pane agree and stay agreed for 600 ms (sizes from
// before a resize also agree, until the window refits); returns both. A mismatch past the deadline
// fails with both sizes.
async function matches(tab, tmuxName, what, ms = 10000) {
    const end = Date.now() + ms;
    let x = null, p = null, since = 0, key = '';
    for (;;) {
        x = await tab.ev(XTERM); p = paneSize(tmuxName);
        const k = x ? `${x.cols}x${x.rows}/${p.cols}x${p.rows}` : '';
        if (x && x.cols === p.cols && x.rows === p.rows) {
            if (k !== key) { key = k; since = Date.now(); }
            if (Date.now() - since >= 600) return { x, p };
        } else key = '';
        if (Date.now() > end) assert.fail(`${what}: xterm ${x && x.cols}x${x && x.rows} but the tmux pane is ${p.cols}x${p.rows} (${JSON.stringify(x)})`);
        await sleep(100);
    }
}

async function select(tab, a) {
    await tab.ev(`(() => { setLiveViewMode('terminal'); selectLiveSession(${JSON.stringify(a.name)}, 'terminal', ${JSON.stringify(a.sid)}); return true; })()`);
    await tab.waitFor(`(async () => { const x = await ${XTERM}; return !!x && x.wsOpen; })()`, `the terminal socket for ${a.name}`);
    // A window decides whether it is the session's interactive owner over 200 ms (ownership.js OWNER_PING_MS).
    await sleep(400); // sleep-ok: no event says the 200 ms ownership ping has settled
}

(async () => {
    const launched = [];
    const contexts = [];
    const tabs = [];
    try {
        for (const display of ['Pane A', 'Pane B']) {
            const r = await httpJson('POST', '/api/sessions/launch', { working_dir: '/tmp', agent_type: 'terminal', display_name: display });
            assert.equal(r.status, 200, `launch ${display}: ${JSON.stringify(r.data)}`);
            launched.push({ name: r.data.session_name, sid: r.data.session_id });
        }
        const [A, B] = launched;
        for (const a of launched) paneSize(a.name); // both panes exist on the test socket

        const main = await openTab(BASE + '/');
        tabs.push(main);
        await main.waitFor(`typeof window.selectLiveSession === 'function' && typeof window.setLiveViewMode === 'function'`, 'the dashboard');

        console.log('1. The owner window on open');
        await select(main, A);
        let m = await matches(main, A.name, 'on open');
        assert.equal(m.x.following, false, `the owner fits its own window (${JSON.stringify({ m, sent: await main.ev('window.__resizesSent') })})`);
        console.log(`   ✓ pane ${m.p.cols}x${m.p.rows} = xterm`);

        console.log('2. Window resizes');
        const before = m.p.cols;
        await main.size(1000, 700);
        m = await matches(main, A.name, 'after narrowing the window');
        assert.ok(m.p.cols < before, `narrower: ${m.p.cols} < ${before}`);
        await main.size(1600, 950);
        m = await matches(main, A.name, 'after widening the window');
        assert.ok(m.p.cols > before, `wider: ${m.p.cols} > ${before}`);
        console.log(`   ✓ ${before} -> narrower -> ${m.p.cols} cols, pane follows each`);

        console.log('3. Chat -> terminal, resized while in chat');
        const wide = m.p.cols;
        await main.ev(`setLiveViewMode('chat'); true`);
        await main.size(1200, 800);
        await sleep(300); // sleep-ok: negative check, the hidden terminal must not be fitted to zero meanwhile
        await main.ev(`setLiveViewMode('terminal'); true`);
        m = await matches(main, A.name, 'after chat -> terminal');
        assert.ok(m.p.cols < wide && m.p.cols > 20, `the terminal's new width (${m.p.cols}), not the old (${wide}) or a zero-size one`);
        console.log(`   ✓ pane ${m.p.cols} cols after returning to the terminal`);

        console.log('4. Another agent and back');
        await select(main, B);
        await matches(main, B.name, 'agent B');
        await main.size(1100, 760);
        const bAfter = (await matches(main, B.name, 'agent B after a resize')).p;
        assert.notEqual(paneSize(A.name).cols, bAfter.cols, 'A still has the older width while B is shown');
        await select(main, A);
        m = await matches(main, A.name, 'back on agent A');
        assert.equal(m.p.cols, bAfter.cols, 'A is resized to the window on return');
        console.log(`   ✓ B sized, window resized, A re-sized to ${m.p.cols} on return`);

        console.log('5. Socket reconnect while the server is busy');
        await main.Fetch.enable({ patterns: [{ urlPattern: '*/resize*', requestStage: 'Request' }, { urlPattern: '*/poll*', requestStage: 'Request' }] });
        let busy = 0;
        safePaused(main, e => {
            busy++;
            main.Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 503, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }, { name: 'Retry-After', value: '1' }],
                body: Buffer.from(JSON.stringify({ error: 'busy' })).toString('base64') }).catch(() => {});
        });
        // The live feed renames the selected agent to its list name, which for a terminal agent is
        // not its tmux session's: the reconnect must still go to the tmux session (#3469). Wait for
        // that rename first, as a page that has been open a while has it.
        const listName = `(async () => { const { state } = await import((window.__coralStatic || '/static/') + 'state.js'); const s = state.currentSession; const l = s && state.liveSessions.find(x => x.session_id === s.session_id); return s && l && s.name === l.name ? s.name : null; })()`;
        await main.waitFor(`(async () => !!(await ${listName}))()`, 'the live feed to sync the selected agent', 15000);
        const synced = await main.ev(listName);
        assert.notEqual(synced, A.name, 'a terminal agent is listed under another name than its tmux session (else this step tests nothing)');
        const socketsBefore = await main.ev(`window.__termSockets.length`);
        await main.ev(`(() => { const s = window.__termSockets[window.__termSockets.length - 1]; s.close(); return true; })()`);
        await main.size(1300, 820); // resized while the socket is down
        const tmuxPath = '/ws/terminal/' + encodeURIComponent(A.name) + '?';
        await main.waitFor(`window.__termSockets.length > ${socketsBefore} && window.__termSockets[window.__termSockets.length - 1].readyState === 1 && window.__termSockets[window.__termSockets.length - 1].url.includes(${JSON.stringify(tmuxPath)})`, 'the reconnected terminal socket', 15000)
            .catch(async e => { throw new Error(`${e.message}: sockets since the close ${JSON.stringify(await main.ev(`window.__termSockets.slice(${socketsBefore}).map(s => s.readyState + ' ' + s.url.split('/ws/terminal/')[1].split('?')[0])`))}`); });
        m = await matches(main, A.name, 'after the reconnect');
        await main.Fetch.disable();
        console.log(`   ✓ pane ${m.p.cols} cols after reconnecting (503s answered meanwhile: ${busy})`);

        console.log('5b. Another agent selected while a reconnect is pending');
        const aBefore = paneSize(A.name);
        const nBefore = await main.ev(`window.__termSockets.length`);
        await main.ev(`(() => { const s = window.__termSockets[window.__termSockets.length - 1]; s.close(); return true; })()`);
        await main.ev(`(() => { selectLiveSession(${JSON.stringify(B.name)}, 'terminal', ${JSON.stringify(B.sid)}); return true; })()`);
        await sleep(4000); // sleep-ok: negative check, past A's 3 s reconnect timer
        const since = await main.ev(`window.__termSockets.slice(${nBefore}).map(s => ({ state: s.readyState, target: decodeURIComponent(s.url.split('/ws/terminal/')[1].split('?')[0]) }))`);
        assert.ok(since.length >= 1, 'B was attached');
        assert.deepEqual(since.filter(x => x.target !== B.name), [], `after the switch only B's terminal is opened, never A's again (${JSON.stringify(since)})`);
        assert.equal(since[since.length - 1].state, 1, `B's socket is open (${JSON.stringify(since)})`);
        m = await matches(main, B.name, 'B after the switch');
        assert.deepEqual(paneSize(A.name), aBefore, "A's pane is not resized by B's view");
        await select(main, A);
        await matches(main, A.name, 'back on A');
        console.log(`   ✓ only B attached (${since.length} socket(s)); A's pane kept ${aBefore.cols}x${aBefore.rows}`);

        console.log('6. Terminal mounted hidden, shown later');
        await main.ev(`(() => { const w = document.querySelector('.capture-wrapper') || document.getElementById('xterm-container').parentElement; w.dataset.paneTestHidden = '1'; w.style.display = 'none'; return true; })()`);
        await main.ev(`(() => { selectLiveSession(${JSON.stringify(B.name)}, 'terminal', ${JSON.stringify(B.sid)}); return true; })()`);
        await sleep(500); // sleep-ok: negative check, a hidden mount must not size the pane meanwhile
        await main.size(1250, 800);
        const hiddenX = await main.ev(XTERM);
        assert.ok(!hiddenX || !hiddenX.shown, 'the terminal is hidden');
        const bHidden = paneSize(B.name);
        assert.ok(bHidden.cols >= 20, `a hidden terminal never sizes the pane to zero (${bHidden.cols})`);
        await main.ev(`(() => { const w = document.querySelector('[data-pane-test-hidden]'); w.style.display = ''; delete w.dataset.paneTestHidden; return true; })()`);
        m = await matches(main, B.name, 'after showing the hidden terminal');
        console.log(`   ✓ hidden: pane kept ${bHidden.cols}; shown: pane ${m.p.cols} = xterm`);
        await select(main, A);
        const owner = (await matches(main, A.name, 'owner back on A')).p;

        console.log('7. A same-browser viewer (the popout)');
        const pop = await openTab(`${BASE}/agent/${A.sid}`, { width: 820, height: 640 });
        tabs.push(pop);
        await pop.waitFor(`(async () => { const x = await ${XTERM}; return !!x && x.wsOpen; })()`, 'the popout terminal');
        m = await matches(pop, A.name, 'the popout viewer');
        assert.deepEqual(m.p, owner, `the viewer leaves the pane at the owner's size (${JSON.stringify({ x: m.x, popSent: await pop.ev('window.__resizesSent'), mainSent: (await main.ev('window.__resizesSent')).slice(-4), mainX: await main.ev(XTERM), mainWs: await main.ev('window.__termSockets.map(s => s.url.split("/ws/terminal/")[1].slice(0, 30) + " " + s.readyState)'), paneA: paneSize(A.name), paneB: paneSize(B.name), A: A.name, owner, http: await main.ev('window.__httpResizes'), popHttp: await pop.ev('window.__httpResizes') })})`);
        assert.equal(m.x.following, true, 'the viewer follows the pane');
        assert.ok(m.x.scrolls, 'the viewer scrolls the wider pane instead of wrapping it');
        assert.equal((await pop.ev(`window.__resizesSent.length`)), 0, 'the viewer sent no resize');
        await matches(main, A.name, 'the owner after the popout opened');
        await pop.close(); tabs.pop();
        console.log(`   ✓ popout renders ${m.x.cols} cols with horizontal scroll; pane unchanged`);

        for (const [label, opts] of [
            ['8. A phone (another device), Interact', { width: 390, height: 844, mobile: true }],
            ['9. A phone through a View-only hub', { width: 390, height: 844, mobile: true, fakeHubView: true }],
            ['10. A desktop through a View-only hub', { width: 1000, height: 760, fakeHubView: true }],
        ]) {
            console.log(label);
            const ctx = await newContext(); contexts.push(ctx);
            const t = await openTab(BASE + '/', { ...opts, contextId: ctx });
            tabs.push(t);
            await t.waitFor(`typeof window.selectLiveSession === 'function'`, 'the dashboard');
            await select(t, A);
            m = await matches(t, A.name, label);
            assert.deepEqual(m.p, owner, `the pane keeps the owner's size (${JSON.stringify({ x: m.x, mainSent: (await main.ev('window.__resizesSent')).slice(-3), mainX: await main.ev(XTERM), mainVis: await main.ev('document.visibilityState + " " + innerWidth + "x" + innerHeight'), tSent: await t.ev('window.__resizesSent'), tDropped: await t.ev('window.__resizesDropped'), tHttp: await t.ev('window.__httpResizes.length') })})`);
            assert.equal(m.x.following, true, 'it renders at the pane\'s size');
            if (opts.width < owner.cols * 6) assert.ok(m.x.scrolls, 'and scrolls a pane wider than the window');
            const sent = await t.ev(`window.__resizesSent.length`);
            assert.equal(sent, 0, 'no resize reached the server');
            if (!opts.fakeHubView) assert.equal(await t.ev(`window.__httpResizes.length`), 0, 'nor an HTTP pane-width sync');
            // Resizing this window changes nothing for the pane.
            await t.size(opts.width - 40, opts.height - 40);
            await sleep(400); // sleep-ok: negative check, give a wrong resize time to reach the pane
            m = await matches(t, A.name, label + ' after its own resize');
            assert.deepEqual(m.p, owner, 'still the owner\'s size');
            await matches(main, A.name, 'the owner after ' + label);
            const dropped = await t.ev(`window.__resizesDropped`);
            console.log(`   ✓ renders ${m.x.cols}x${m.x.rows} = pane, scrolls: ${m.x.scrolls}, resizes sent 0${opts.fakeHubView ? `, dropped by the hub ${dropped}` : ''}`);
            await t.close(); tabs.pop();
        }

        console.log('11. Another desktop resizes the pane: the owner follows, then takes it back');
        {
            const ctx = await newContext(); contexts.push(ctx);
            const other = await openTab(BASE + '/', { width: 900, height: 700, contextId: ctx });
            tabs.push(other);
            await other.waitFor(`typeof window.selectLiveSession === 'function'`, 'the dashboard');
            await select(other, A);
            const theirs = (await matches(other, A.name, 'the other desktop')).p;
            assert.notDeepEqual(theirs, owner, 'the other desktop set its own size (last resize wins)');
            m = await matches(main, A.name, 'the owner after the other desktop resized');
            assert.equal(m.x.following, true, 'the owner follows the change instead of fighting it');
            assert.deepEqual(m.p, theirs);
            // The user resizes the window they are looking at: a background tab is not rendered (no
            // ResizeObserver) until it is in front.
            await main.client.Page.bringToFront();
            await main.size(1350, 860);
            m = await matches(main, A.name, 'the owner after its own resize');
            assert.equal(m.x.following, false, `resizing the owner window sets the size again (${JSON.stringify({ m, theirs, sent: (await main.ev('window.__resizesSent')).slice(-3), mainHttp: (await main.ev('window.__httpResizes')).slice(-3).map(h => [h.at, h.body.slice(0, 16)]), otherSent: await other.ev('window.__resizesSent'), otherHttp: (await other.ev('window.__httpResizes')).map(h => [h.at, h.body.slice(0, 16)]), now: Date.now() % 100000, diag: await main.ev(`(async () => { const x = await import((window.__coralStatic || '/static/') + 'xterm_renderer.js'); const o = await import((window.__coralStatic || '/static/') + 'ownership.js'); return { may: x.mayResizePane(), owner: o.isInteractiveOwner(), joined: o.ownershipSessionId(), vis: document.visibilityState, w: innerWidth }; })()`) })})`);
            assert.notDeepEqual(m.p, theirs);
            const otherNow = await matches(other, A.name, 'the other desktop now follows');
            assert.equal(otherNow.x.following, true);
            console.log(`   ✓ other desktop set ${theirs.cols}, owner followed; owner resized -> ${m.p.cols}, other follows`);
            await other.close(); tabs.pop();
        }

        console.log('12. Restart from the dashboard, then a socket drop: it reconnects to the new tmux session');
        {
            await select(main, A);
            await matches(main, A.name, 'A before the restart');
            const st = `(async () => { const { state } = await import((window.__coralStatic || '/static/') + 'state.js'); return state.currentSession && state.currentSession.session_id; })()`;
            await main.ev(`(async () => { window.restartSession(); return true; })()`);
            await main.waitFor(`document.getElementById('restart-modal') && document.getElementById('restart-modal').style.display === 'flex'`, 'the restart dialog');
            await main.ev(`(async () => { window.confirmRestart(); return true; })()`);
            await main.waitFor(`(async () => { const s = await ${st}; return !!s && s !== ${JSON.stringify(A.sid)}; })()`, 'the restarted session id', 20000);
            const newSid = await main.ev(st);
            const live = await httpJson('GET', '/api/sessions/live');
            const list = Array.isArray(live.data) ? live.data : (live.data.sessions || []);
            const R = list.find(x => x.session_id === newSid);
            assert.ok(R && R.tmux_session, `the restarted agent is live with a tmux session (${JSON.stringify(R)})`);
            launched.push({ name: R.tmux_session, sid: newSid });
            const rPath = '/ws/terminal/' + encodeURIComponent(R.tmux_session) + '?';
            const lastOpenOn = p => `window.__termSockets.length > 0 && window.__termSockets[window.__termSockets.length - 1].readyState === 1 && window.__termSockets[window.__termSockets.length - 1].url.includes(${JSON.stringify(p)})`;
            await main.waitFor(lastOpenOn(rPath), 'the terminal on the restarted session', 30000)
                .catch(async e => { throw new Error(`${e.message}: R ${JSON.stringify({ name: R.name, tmux: R.tmux_session })} sockets ${JSON.stringify(await main.ev(`window.__termSockets.slice(-4).map(s => s.readyState + ' ' + s.url.split('/ws/terminal/')[1].split('?')[0])`))} current ${JSON.stringify(await main.ev(`(async () => { const { state } = await import((window.__coralStatic || '/static/') + 'state.js'); const s = state.currentSession; return s && { name: s.name, tmux: s.tmux_session, sid: s.session_id }; })()`))}`); });
            // Let the feed sync the restarted agent, then drop the socket.
            await main.waitFor(`(async () => { const { state } = await import((window.__coralStatic || '/static/') + 'state.js'); const s = state.currentSession; const l = s && state.liveSessions.find(x => x.session_id === s.session_id); return !!(s && l && s.name === l.name); })()`, 'the feed to sync the restarted agent', 15000);
            const nR = await main.ev(`window.__termSockets.length`);
            await main.ev(`(() => { const s = window.__termSockets[window.__termSockets.length - 1]; s.close(); return true; })()`);
            await main.waitFor(`window.__termSockets.length > ${nR} && (${lastOpenOn(rPath)})`, 'the reconnect to the restarted session', 15000)
                .catch(async e => { throw new Error(`${e.message}: sockets since the close ${JSON.stringify(await main.ev(`window.__termSockets.slice(${nR}).map(s => s.readyState + ' ' + s.url.split('/ws/terminal/')[1].split('?')[0])`))}`); });
            console.log(`   ✓ restarted as ${R.tmux_session}; reconnected there after a drop`);
        }

        console.log('\nAll terminal pane size checks passed');
    } finally {
        for (const t of tabs) await t.close();
        for (const c of contexts) await disposeContext(c);
        for (const a of launched) await httpJson('POST', `/api/sessions/live/${encodeURIComponent(a.name)}/kill`, { agent_type: 'terminal', session_id: a.sid }).catch(() => {});
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
