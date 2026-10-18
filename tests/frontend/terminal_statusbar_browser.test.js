// The bar under the Terminal view (#4044), with a fake terminal socket on the page (the real xterm and the real fit):
//   - the bar is under the terminal, in the Terminal view only, and the terminal ends above it;
//   - the last row is wholly visible: the fit's rows are the rows that fit (no half line cut off, and not one row short), at 1280, 900
//     and 390 px and with the phone keyboard open;
//   - the size it shows is the terminal's columns and rows; the state follows the socket (connected, reconnecting, session ended);
//   - the typing hint is in the bar, no longer floating over the last line.
// Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: layout settles over a few frames; there is no completion event
    const FAKE = `(() => { const R = window.WebSocket; class Fake { constructor(url) { this.url = url; this.readyState = 0; this.binaryType = 'arraybuffer'; this.sent = []; window.__tws = this; setTimeout(() => { this.readyState = 1; this.onopen && this.onopen({}); }, 50); }
        send(d) { this.sent.push(d); } close() { this.readyState = 3; } addEventListener() {} removeEventListener() {} }
        const W = function (url, p) { return /\\/ws\\/terminal\\//.test(String(url)) ? new Fake(url) : new R(url, p); };
        W.OPEN = 1; W.CONNECTING = 0; W.CLOSING = 2; W.CLOSED = 3; W.prototype = R.prototype; window.WebSocket = W; })()`;
    const text = Array.from({ length: 120 }, (_, i) => `line ${i} of the build output\r\n`).join('');
    const GEOM = `(() => { const t = window.__term(); const c = document.getElementById('xterm-container'), bar = document.getElementById('terminal-statusbar');
        const cs = getComputedStyle(c), cr = c.getBoundingClientRect(), br = bar.getBoundingClientRect();
        const screen = c.querySelector('.xterm-screen').getBoundingClientRect(); const rowH = screen.height / t.rows; const last = { bottom: screen.bottom };  // the last row ends where the screen ends
        const inner = cr.bottom - (parseFloat(cs.paddingBottom) || 0);
        return { cols: t.cols, rows: t.rows, rowH, contentH: c.clientHeight - (parseFloat(cs.paddingTop) || 0) - (parseFloat(cs.paddingBottom) || 0), screenH: screen.height,
            lastBottom: last.bottom, innerBottom: inner, contBottom: cr.bottom, barTop: br.top, barBottom: br.bottom, barVisible: br.height > 0, vh: innerHeight, size: document.getElementById('terminal-statusbar-size').textContent,
            state: bar.dataset.state, stateText: bar.querySelector('.terminal-statusbar-state-text').textContent, hintInBar: !!bar.querySelector('.xterm-focus-hint'), hintInContainer: !!c.querySelector('.xterm-focus-hint') }; })()`;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: FAKE });
        for (const [label, w, h, mobile, keyboard] of [['desktop 1280', 1280, 800, false, false], ['900 px', 900, 800, false, false], ['phone 390', 390, 844, true, false], ['phone 390, keyboard open', 390, 420, true, true]]) {
            console.log(label);
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`localStorage.setItem('coral-live-view-mode', 'terminal'); window.__m.add('bar-bot', 'A', 3, { sleeping: false }); (async () => { const x = await import((window.__coralStatic || '/static/') + 'xterm_renderer.js'); window.__term = x.getTerminal; })(); true`);
            await sleep(700); // sleep-ok: layout settles over a few frames; there is no completion event
            await ev(`window.selectLiveSession('bar-bot', 'claude', 'bar-bot-id', 'local'); window.setLiveViewMode('terminal'); true`);
            await waitFor(`!!window.__tws && !!document.querySelector('#xterm-container .xterm')`, 'the terminal', 15000);
            if (keyboard) await ev(`document.body.classList.add('keyboard-open'); window.dispatchEvent(new Event('resize')); true`);
            await sleep(600); // sleep-ok: layout settles over a few frames; there is no completion event
            await ev(`(() => { const b = new TextEncoder().encode(${JSON.stringify(text)}); window.__tws.onmessage({ data: b.buffer }); return true; })()`);
            await sleep(700); // sleep-ok: layout settles over a few frames; there is no completion event
            await ev(`window.dispatchEvent(new Event('resize')); true`); await sleep(500); // sleep-ok: layout settles over a few frames; there is no completion event
            const g = await ev(GEOM);
            assert.equal(g.barVisible, true, label + ': the bar is shown in the Terminal view');
            assert.ok(g.contBottom <= g.barTop + 0.5, `${label}: the terminal ends above the bar (${g.contBottom} vs ${g.barTop})`);
            assert.ok(g.lastBottom <= g.innerBottom + 0.5, `${label}: the last row is wholly inside the terminal's padding box (row ends ${g.lastBottom}, area ends ${g.innerBottom})`);
            assert.ok(g.contentH - g.screenH < g.rowH + 0.5, `${label}: the fit used every row that fits (${g.rows} rows of ${g.rowH}px in ${g.contentH}px)`);
            assert.ok(g.screenH <= g.contentH + 0.5, `${label}: no half line (screen ${g.screenH}px in ${g.contentH}px)`);
            assert.equal(g.size, `${g.cols} × ${g.rows}`, label + ': the bar shows the terminal size');
            const sent = await ev(`window.__tws.sent.map(x => { try { return JSON.parse(x); } catch { return null; } }).filter(m => m && m.type === 'terminal_resize')`);
            if (sent.length) assert.deepEqual([sent[sent.length - 1].cols, sent[sent.length - 1].rows], [g.cols, g.rows], label + ': the last resize sent to the server is the size on screen');
            assert.equal(g.state, 'connected', label + ': connected');
            assert.equal(g.stateText, 'Terminal connected', label + ': named as the terminal\'s state (#4587)');
            assert.equal(g.hintInContainer, false, label + ': the hint no longer floats in the terminal');
            assert.equal(g.hintInBar, true);
            assert.ok(g.barBottom <= g.vh + 0.5, label + ': the bar is on screen');
            // state follows the socket
            await ev(`window.__tws.onmessage({ data: JSON.stringify({ type: 'terminal_closed' }) }); true`);
            assert.equal(await ev(`document.getElementById('terminal-statusbar').dataset.state`), 'ended', label + ': the bar says the session ended');
            // chat view: no bar
            await ev(`window.setLiveViewMode('chat'); true`); await sleep(500); // sleep-ok: layout settles over a few frames; there is no completion event
            assert.equal(await ev(`getComputedStyle(document.getElementById('terminal-statusbar')).display !== 'none' && document.getElementById('terminal-statusbar').getBoundingClientRect().height > 0`), false, label + ': no terminal bar in the Chat view (#4587: it said Connecting there, with no terminal socket)');
        }
        console.log('\nPASS terminal bottom bar: under the terminal, last row fully visible, size and state shown');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
