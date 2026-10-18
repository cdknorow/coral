// A terminal that follows a pane taller than its window (Coral Hub's View mode, a viewer, a phone) keeps the prompt line in view (#4586): at 1280x756 and on a phone, the pane's
// rows arrive (terminal_size, 60 rows), and the cursor line is inside the container's visible area, stays there as output moves it, is left alone once the person scrolls it out of
// view, and comes back when they type. On the real xterm with a fake terminal socket. Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: layout and scroll settle over a few frames; there is no completion event
    const FAKE = `(() => { const R = window.WebSocket; class Fake { constructor(url) { this.url = url; this.readyState = 0; this.binaryType = 'arraybuffer'; this.sent = []; window.__tws = this; setTimeout(() => { this.readyState = 1; this.onopen && this.onopen({}); }, 50); }
        send(d) { this.sent.push(d); } close() { this.readyState = 3; } addEventListener() {} removeEventListener() {} }
        const W = function (url, p) { return /\\/ws\\/terminal\\//.test(String(url)) ? new Fake(url) : new R(url, p); };
        W.OPEN = 1; W.CONNECTING = 0; W.CLOSING = 2; W.CLOSED = 3; W.prototype = R.prototype; window.WebSocket = W; })()`;
    const out = n => Array.from({ length: n }, (_, i) => `line ${i}\r\n`).join('') + '$ ';
    const write = s => ev(`(() => { const b = new TextEncoder().encode(${JSON.stringify(s)}); window.__tws.onmessage({ data: b.buffer }); return true; })()`);
    // Where the cursor row sits against the container's visible area.
    const VIEW = `(() => { const t = window.__term(); const c = document.getElementById('xterm-container'); const cr = c.getBoundingClientRect();
        const s = c.querySelector('.xterm-screen').getBoundingClientRect(); const cell = s.height / t.rows; const top = s.top + t.buffer.active.cursorY * cell;
        return { top, bottom: top + cell, viewTop: cr.top, viewBottom: cr.top + c.clientHeight, rows: t.rows, scrollTop: c.scrollTop, scrollH: c.scrollHeight, clientH: c.clientHeight, follow: c.classList.contains('xterm-follow-pane') }; })()`;
    const visible = async why => { const v = await ev(VIEW); assert.ok(v.top >= v.viewTop - 0.5 && v.bottom <= v.viewBottom + 0.5, `${why}: the cursor line is in view ${JSON.stringify(v)}`); return v; };
    const hidden = async why => { const v = await ev(VIEW); assert.ok(v.bottom > v.viewBottom + 0.5 || v.top < v.viewTop - 0.5, `${why}: the cursor line stays where the person left it ${JSON.stringify(v)}`); };
    const open = async (w, h) => {
        await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile: w < 700 });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`localStorage.setItem('coral-live-view-mode', 'terminal'); window.__m.add('cur-bot', 'A', 3, { sleeping: false }); (async () => { const x = await import((window.__coralStatic || '/static/') + 'xterm_renderer.js'); window.__term = x.getTerminal; window.__stateMod = await import((window.__coralStatic || '/static/') + 'state.js'); })(); true`);
        await sleep(700); // sleep-ok: layout settles over a few frames; there is no completion event
        await ev(`window.selectLiveSession('cur-bot', 'claude', 'cur-bot-id', 'local'); window.setLiveViewMode('terminal'); true`);
        await waitFor(`!!window.__tws && !!document.querySelector('#xterm-container .xterm')`, 'the terminal', 15000);
        // A viewer (as the hub's View mode makes one) cannot resize the pane, so the terminal follows the pane's own size.
        await ev(`window.__stateMod.state.currentSession.hubReadOnly = true; true`);
        await ev(`window.__tws.onmessage({ data: JSON.stringify({ type: 'terminal_size', cols: 100, rows: 60, seq: 0, initial: true }) }); true`);
        await waitFor(`document.getElementById('xterm-container').classList.contains('xterm-follow-pane') && window.__term().rows === 60`, 'following the 60-row pane', 6000);
        await write(out(58));
        await sleep(400); // sleep-ok: the scroll to the cursor runs on an animation frame
    };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: FAKE });
        for (const [w, h, name] of [[1280, 756, 'desktop 1280x756'], [390, 756, 'phone 390x756']]) {
            console.log(`${name}: a 60-row pane`);
            await open(w, h);
            const v = await visible('the pane\'s rows arrived');
            assert.ok(v.scrollH > v.clientH + 50, `the pane is taller than the window, so the container scrolls ${JSON.stringify(v)}`);
            assert.ok(v.scrollTop > 0, 'it scrolled to the cursor');
            await write('more\r\nmore\r\nmore\r\n$ '); await sleep(300); // sleep-ok: the scroll runs on an animation frame
            await visible('output moved the cursor');
            await Emulation.setDeviceMetricsOverride({ width: w, height: h - 120, deviceScaleFactor: 1, mobile: w < 700 });
            await ev(`window.dispatchEvent(new Event('resize')); true`); await sleep(500); // sleep-ok: the resize observer runs over a few frames
            await visible('the window got shorter');
            // The person scrolls up to read: left alone while output arrives, back when they type.
            await ev(`(() => { const c = document.getElementById('xterm-container'); c.scrollTop = 0; c.dispatchEvent(new Event('scroll')); return true; })()`); await sleep(300); // sleep-ok: the scroll event settles over a frame
            await write('later\r\n$ '); await sleep(300); // sleep-ok: the scroll runs on an animation frame
            await hidden('they scrolled up to read');
            await ev(`window.__term().input('x', true); true`); await sleep(400); // sleep-ok: the scroll runs on an animation frame
            await visible('they typed');
        }
        console.log('\nPASS terminal follow cursor: the prompt line is in view at 1280x756 and on a phone, left alone when scrolled away, back on typing');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
