// The terminal's fit counts the rows that fit INSIDE the container's padding (#4044). FitAddon counted rows from the container's computed
// height, which includes the padding (the page is border-box), so the last row could end inside the bottom padding, on the edge. With a
// generous bottom padding and a sweep of window heights (so every remainder of height over row height comes up), on the real xterm with a
// fake terminal socket: the last row ends inside the padding box, the fit used every row that fits, and the last resize sent to the
// server is the size on screen. Run against an isolated dev server.
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
    const GEOM = `(() => { const t = window.__term(); const c = document.getElementById('xterm-container'); const cs = getComputedStyle(c), cr = c.getBoundingClientRect();
        const screen = c.querySelector('.xterm-screen').getBoundingClientRect(); const rowH = screen.height / t.rows;
        const pt = parseFloat(cs.paddingTop) || 0, pb = parseFloat(cs.paddingBottom) || 0;
        return { cols: t.cols, rows: t.rows, rowH, contentH: c.clientHeight - pt - pb, screenH: screen.height, lastBottom: screen.bottom, innerBottom: cr.bottom - pb }; })()`;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: FAKE });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`localStorage.setItem('coral-live-view-mode', 'terminal'); window.__m.add('fit-bot', 'A', 3, { sleeping: false }); (async () => { const x = await import((window.__coralStatic || '/static/') + 'xterm_renderer.js'); window.__term = x.getTerminal; })(); true`);
        await sleep(700); // sleep-ok: layout settles over a few frames; there is no completion event
        await ev(`window.selectLiveSession('fit-bot', 'claude', 'fit-bot-id', 'local'); window.setLiveViewMode('terminal'); true`);
        await waitFor(`!!window.__tws && !!document.querySelector('#xterm-container .xterm')`, 'the terminal', 15000);
        await ev(`document.getElementById('xterm-container').style.paddingBottom = '20px'; true`);
        await sleep(600); // sleep-ok: layout settles over a few frames; there is no completion event
        await ev(`(() => { const b = new TextEncoder().encode(${JSON.stringify(text)}); window.__tws.onmessage({ data: b.buffer }); return true; })()`);
        await sleep(600); // sleep-ok: layout settles over a few frames; there is no completion event
        let checked = 0;
        for (let h = 700; h < 700 + 22; h += 2) {
            await Emulation.setDeviceMetricsOverride({ width: 1280, height: h, deviceScaleFactor: 1, mobile: false });
            await ev(`window.dispatchEvent(new Event('resize')); true`);
            await waitFor(`document.getElementById('xterm-container').getBoundingClientRect().height > 100`, 'the container', 4000);
            await sleep(250); // sleep-ok: the resize observer and fit run over a few frames; there is no completion event
            const g = await ev(GEOM);
            assert.ok(g.lastBottom <= g.innerBottom + 0.5, `window ${h} px: the last row ends inside the padding box (row ends ${g.lastBottom}, area ends ${g.innerBottom}, ${g.rows} rows of ${g.rowH}px)`);
            assert.ok(g.contentH - g.screenH < g.rowH + 0.5, `window ${h} px: the fit used every row that fits (${g.rows} rows of ${g.rowH}px in ${g.contentH}px)`);
            const sent = await ev(`window.__tws.sent.map(x => { try { return JSON.parse(x); } catch { return null; } }).filter(m => m && m.type === 'terminal_resize')`);
            if (sent.length) assert.deepEqual([sent[sent.length - 1].cols, sent[sent.length - 1].rows], [g.cols, g.rows], `window ${h} px: the last resize sent to the server is the size on screen`);
            checked++;
        }
        assert.ok(checked >= 10);
        console.log(`\nPASS terminal fit: ${checked} window heights, the last row always inside the padding, no row short`);
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
