'use strict';
// Switching from Chat to Terminal renders the terminal every time, and right away (#4518, the operator's blank terminal). On a real
// stand-in agent (a tmux pane, the real terminal socket and the real xterm.js):
//  1. Twenty toggles Chat -> Terminal in a row, at desktop width and at 390 px, with the wait in Chat view cycling 30 ms / 200 ms / 800 ms /
//     2 s: each time the terminal shows text within 500 ms of the switch. "Shows text" is both the buffer (the replay was written) and the
//     canvas (pixels were drawn): a renderer that was hidden can hold the text and draw nothing.
//  2. The same while the agent's pane streams output (lines typed into it every 80 ms), and after switching to another agent.
//  3. The first switch of a page that has not loaded xterm.js (it is only downloaded when the Terminal view is first shown, #3083), on a slow link
//     (150 ms, 1.5 Mbit; the measured cost was about a second): once the pointer is over the Terminal button, or Chat view has been showing
//     for the idle delay, xterm.js is fetched ahead and the switch itself shows text within 500 ms. Without either (the control) it does not
//     have to be, and is only reported.
//  4. A switch to Terminal and straight back to Chat while xterm.js is still downloading opens no terminal socket in Chat view (it used to
//     attach once the download finished); the next switch to Terminal does attach and shows text.
// Run against an isolated dev server and headless Chrome via CDP. The suite is seven files so the shards run them side by side (#5027, #5443): terminal_switch_<desktop|phone>
// (1 at 1280 px / 390 px, the idle agent), ..._stream (2, output streaming), ..._agent (after switching to another agent, and the switch made in Terminal view) and
// terminal_switch_cold (3 and 4); this module is their body, `parts` says which to run ('desktop:idle', 'phone:stream', 'desktop:agent', 'cold' ...).
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('../test_base').BASE;
const { launchStandIn } = require('./stand_in_agent');

const results = [];
const lines = [];
const check = (label, ok, detail) => { results.push(!!ok); const l = `[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`; lines.push(l); console.log(l); };
async function httpJson(method, path, body) {
    const r = await fetch(BASE + path, { method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
    let data = null; try { data = await r.json(); } catch { /* no body */ }
    return { status: r.status, data };
}
const MOD = `(window.__coralStatic || '/static/')`;
const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: timing is what is measured; there is no completion event for a repaint
const BUDGET_MS = 500;
const FIRST_BUDGET_MS = 1500; // the page's first terminal: xterm.js is loaded and the terminal attaches (the later switches only show it again); 440 to 580 ms on a machine 12 times slower, where the others take 20 to 170

module.exports = parts => (async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation, Network } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 500));
        return r.result.value;
    };
    const { coralReady } = require('./wait').bind(ev);
    const standIns = [];
    const guard = await require('./settings_guard').guardSettings(); // cli_path_claude and the rest are put back whatever path the suite ends on (#4849)
    // Pixels, not just the buffer: the canvas renderer can leave the screen blank while the buffer holds the text.
    const INK = `(() => { let n = 0; for (const c of document.querySelectorAll('#xterm-container canvas')) { if (!c.width || !c.height) continue; const g = c.getContext('2d'); if (!g) continue;
        const d = g.getImageData(0, 0, c.width, c.height).data; for (let i = 3; i < d.length; i += 4) if (d[i]) n++; } return n; })()`;
    const CHARS = `(() => { const t = window.__getTerm && window.__getTerm(); if (!t) return -1; const b = t.buffer.active; let n = 0;
        for (let i = 0; i < b.length; i++) { const l = b.getLine(i); if (l) n += l.translateToString(true).trim().length; } return n; })()`;
    const setup = async ({ width, height }) => {
        await Emulation.setDeviceMetricsOverride({ width, height, deviceScaleFactor: 1, mobile: width < 700 });
        if (process.env.CORAL_CPU_THROTTLE) await Emulation.setCPUThrottlingRate({ rate: Number(process.env.CORAL_CPU_THROTTLE) }); // a loaded runner, reproduced: CORAL_CPU_THROTTLE=8
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => { window.__tws = []; const R = window.WebSocket;
            window.WebSocket = function (url, p) { const s = p === undefined ? new R(url) : new R(url, p); if (/\\/ws\\/terminal\\//.test(String(url))) window.__tws.push(s); return s; };
            window.WebSocket.prototype = R.prototype; for (const k of ['CONNECTING', 'OPEN', 'CLOSING', 'CLOSED']) window.WebSocket[k] = R[k];
            localStorage.setItem('coral-live-view-mode', 'chat'); })();` });
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' }); await loaded; await coralReady();
        await ev(`(async () => { const x = await import(${MOD} + 'xterm_renderer.js'); window.__getTerm = x.getTerminal; })()`);
    };
    const select = s => ev(`window.selectLiveSession(${JSON.stringify(s.name)}, 'claude', ${JSON.stringify(s.sid)}, ''); true`);
    // Time from the switch to a terminal that has text in its buffer and ink on its canvas; -1 when it never does within the limit. The clock and the polling run in the page: a CDP round trip per probe
    // (three a poll, each with a full read of the canvas) is the runner's load, not the product's switch, and on a loaded Windows runner it was most of the 1.6 s one of twenty switches seemed to take (#6175).
    const PROBE = `(() => {
        const chars = () => ${CHARS.trim()};
        const ink = () => { let n = 0; for (const c of document.querySelectorAll('#xterm-container canvas')) { if (!c.width || !c.height) continue; const g = c.getContext('2d'); if (!g) continue;
            const d = g.getImageData(0, 0, c.width, c.height).data; for (let i = 3; i < d.length; i += 4) if (d[i] && ++n > 50) return n; } return n; };
        return { chars, ink }; })()`;
    const switchToTerminal = async (limitMs = 3000) => ev(`(async () => { const { chars, ink } = ${PROBE}; const t0 = performance.now(); window.setLiveViewMode('terminal');
        while (performance.now() - t0 < ${limitMs}) { if (chars() > 0 && ink() > 50) return performance.now() - t0; await new Promise(r => setTimeout(r, 4)); }
        return -1; })()`);
    try {
        for (const name of ['Switch A', 'Switch B'].slice(0, parts.some(x => x.endsWith(':agent')) ? 2 : 1)) { // only the 'agent' scenarios switch between two
            const si = await launchStandIn(httpJson, { displayName: name });
            standIns.push(si); // before the check: a stand-in that never got a session has still pointed cli_path_claude at its script, and only its stop() puts that back
            if (!si.sid) throw new Error('no stand-in agent: ' + si.diag);
        }
        await Page.enable();
        await Network.enable();
        const waits = [30, 200, 800, 2000];
        const toggles = async (label, streamFor) => {
            let stop = false, n = 0;
            const streamer = streamFor ? (async () => { while (!stop) { await httpJson('POST', `/api/sessions/live/${encodeURIComponent(streamFor.name)}/send`, { command: `echo tick-${++n}`, agent_type: 'claude', session_id: streamFor.sid }); await sleep(80); } })() : null;
            const ms = [];
            for (let i = 0; i < 20; i++) {
                await ev(`window.setLiveViewMode('chat'); true`);
                await sleep(waits[i % 4]);   // sleep-ok: how long Chat view has been showing before the switch is the variable under test (30 ms to 2 s)
                ms.push(Math.round(await switchToTerminal()));
            }
            stop = true; if (streamer) await streamer;
            const worst = Math.max(...ms);
            check(`${label}: all 20 switches show text within ${BUDGET_MS} ms (the first, which attaches the page's terminal, within ${FIRST_BUDGET_MS} ms)`, ms.every((v, i) => v >= 0 && v <= (i === 0 ? FIRST_BUDGET_MS : BUDGET_MS)), `worst ${worst} ms, ${ms.join(' ')}`);
        };

        // 1 and 2: warm switches at desktop or phone width; each part is one scenario at one width: 'desktop:idle', 'phone:stream', 'desktop:agent' ...
        for (const part of parts.filter(x => x !== 'cold')) {
            const [width, scenario] = part.split(':');
            const vp = { width: width === 'phone' ? 390 : 1280, height: 800 };
            await setup(vp);
            if (scenario === 'idle' || scenario === 'stream') {
                await select(standIns[0]);
                await sleep(1200);   // sleep-ok: lets the selected agent settle in Chat view before the timed switches; there is no completion event for "idle"
                if (scenario === 'idle') await toggles(`${vp.width} px, idle agent`);
                else await toggles(`${vp.width} px, output streaming`, standIns[0]);
            } else {
                await select(standIns[0]);
                await select(standIns[1]);
                await sleep(1200);   // sleep-ok: lets the selected agent settle in Chat view before the timed switches; there is no completion event for "idle"
                await toggles(`${vp.width} px, after switching to another agent`);
                // An agent switch made while the Terminal view is showing paints the new agent's terminal too.
                await ev(`window.setLiveViewMode('terminal'); true`);
                await sleep(600);   // sleep-ok: the agent switch must happen with the Terminal view already settled; a window for nothing to change, not a condition
                await select(standIns[0]);
                let t = -1;
                for (let k = 0; k < 100 && t < 0; k++) { if ((await ev(CHARS)) > 0 && (await ev(INK)) > 50) t = k * 5; else await sleep(5); }
                check(`${vp.width} px: selecting an agent while the Terminal view shows paints its terminal`, t >= 0 && t <= BUDGET_MS, `${t} ms`);
            }
        }

        if (parts.includes('cold')) {
        // 3: the first switch on a slow link.
        const slow = () => Network.emulateNetworkConditions({ offline: false, latency: 150, downloadThroughput: 1500 * 1024 / 8, uploadThroughput: 1500 * 1024 / 8 });
        const fast = () => Network.emulateNetworkConditions({ offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });
        // xterm.js must come over the network on these pages: with the browser cache on, it would already be here from the earlier ones.
        await Network.setCacheDisabled({ cacheDisabled: true });
        // idleMs is the Chat view's idle prefetch delay, set before the agent is selected (the timer starts then): short for the idle
        // scenario, out of reach for the others so only what they do themselves fetches xterm.js.
        const cold = async (label, ready, idleMs = 1e9) => {
            await fast();
            await setup({ width: 1280, height: 800 });
            await ev(`(async () => { const lc = await import(${MOD} + 'live_chat.js'); lc.terminalPrefetch.idleMs = ${idleMs}; })()`);
            await select(standIns[0]);
            await sleep(1000);   // sleep-ok: lets the page go idle before the link is throttled; there is no completion event for "idle"
            await slow();
            await ready();
            const ms = await switchToTerminal(8000);
            await fast();
            return ms;
        };
        const hovered = await cold('hover', async () => {
            await ev(`document.getElementById('live-view-btn-terminal').dispatchEvent(new PointerEvent('pointerover', { bubbles: true })); true`);
            for (let k = 0; k < 400 && !(await ev(`typeof window.Terminal !== 'undefined' && typeof window.CanvasAddon !== 'undefined'`)); k++) await sleep(50);
        });
        check(`slow link, the pointer was over the Terminal button: the first switch shows text within ${BUDGET_MS} ms`, hovered >= 0 && hovered <= BUDGET_MS, `${Math.round(hovered)} ms`);
        const idled = await cold('idle', async () => {
            for (let k = 0; k < 400 && !(await ev(`typeof window.Terminal !== 'undefined' && typeof window.CanvasAddon !== 'undefined'`)); k++) await sleep(50);
        }, 150);
        check(`slow link, Chat view showing for the idle delay: the first switch shows text within ${BUDGET_MS} ms`, idled >= 0 && idled <= BUDGET_MS, `${Math.round(idled)} ms`);
        const control = await cold('control', async () => { await sleep(300); }); // sleep-ok: the control waits a fixed time so nothing is fetched ahead, the thing it measures
        lines.push(`[info] slow link, nothing fetched ahead (the control): the first switch shows text after ${Math.round(control)} ms`);
        check('slow link, nothing fetched ahead: the switch is what downloads xterm.js (the control is slower than the prefetched ones)', control > Math.max(hovered, idled), `${Math.round(control)} vs ${Math.round(hovered)} and ${Math.round(idled)} ms`);

        // 4: Terminal and straight back to Chat while xterm.js downloads opens no socket in Chat view.
        await fast();
        await setup({ width: 1280, height: 800 });
        await select(standIns[0]);
        await sleep(1000);   // sleep-ok: lets the page go idle before the link is throttled; there is no completion event for "idle"
        await slow();
        await ev(`window.setLiveViewMode('terminal'); true`);
        await sleep(200);   // sleep-ok: the Terminal view is left after a fixed 200 ms, the interval under test
        await ev(`window.setLiveViewMode('chat'); true`);
        for (let k = 0; k < 400 && !(await ev(`typeof window.Terminal !== 'undefined' && typeof window.CanvasAddon !== 'undefined'`)); k++) await sleep(50);
        await sleep(600);   // sleep-ok: the agent switch must happen with the Terminal view already settled; a window for nothing to change, not a condition
        const sockets = await ev(`window.__tws.map(s => s.readyState)`);
        check('Terminal then straight back to Chat while xterm.js downloads: no terminal socket opens in Chat view', sockets.length === 0, `${sockets.length} sockets (states ${sockets.join(',')})`);
        await fast();
        const after = await switchToTerminal();
        check('the next switch to Terminal attaches and shows text', after >= 0 && after <= 2000, `${Math.round(after)} ms`);
        }
    } finally {
        for (const si of standIns) await si.stop().catch(e => console.error('stand-in stop: ' + (e && e.message)));
        await guard.restore().catch(e => console.error('settings restore: ' + (e && e.message)));
        await client.close();
    }
    if (process.env.CORAL_REPORT_FILE) require('node:fs').writeFileSync(process.env.CORAL_REPORT_FILE, lines.join('\n') + '\n');
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} checks passed`);
    if (failed) process.exitCode = 1;
})().catch(e => { console.error(e); process.exitCode = 1; });
