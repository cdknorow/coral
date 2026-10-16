// Switching agents, the view and a dialog over and over must not grow the page (#4036 stability): after a warm-up, 80 more rounds leave the
// DOM node count, the JS event listeners, the listeners on window and document, the live intervals and timeouts, the open WebSockets and the
// live observers where they were (measured after a forced GC). Counters for timers, window/document listeners, sockets and observers are
// installed before the page loads. Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const COUNTERS = `(() => {
    const S = window.__soak = { intervals: new Set(), timeouts: new Set(), listeners: new Map(), ws: [], obs: [] };
    const si = window.setInterval, ci = window.clearInterval, st = window.setTimeout, ct = window.clearTimeout;
    window.setInterval = function (fn, ms, ...a) { const id = si.call(this, fn, ms, ...a); S.intervals.add(id); return id; };
    window.clearInterval = id => { S.intervals.delete(id); return ci.call(window, id); };
    window.setTimeout = function (fn, ms, ...a) { if (typeof fn !== 'function') return st.call(this, fn, ms, ...a); let id; id = st.call(this, function () { S.timeouts.delete(id); return fn.apply(this, arguments); }, ms, ...a); S.timeouts.add(id); return id; };
    window.clearTimeout = id => { S.timeouts.delete(id); return ct.call(window, id); };
    const ids = new WeakMap(); let nid = 0;
    const label = t => t === window ? 'window' : t === document ? 'document' : t === document.documentElement ? 'html' : t === document.body ? 'body' : null;
    const add = EventTarget.prototype.addEventListener, rem = EventTarget.prototype.removeEventListener;
    EventTarget.prototype.addEventListener = function (type, fn, opts) { const l = label(this); if (l && fn && !(opts && opts.signal)) { if (!ids.has(fn)) ids.set(fn, ++nid); S.listeners.set(l + '|' + type + '|' + (opts === true || (opts && opts.capture) ? 'c' : '') + '|' + ids.get(fn), l + ' ' + type); } return add.call(this, type, fn, opts); };
    EventTarget.prototype.removeEventListener = function (type, fn, opts) { const l = label(this); if (l && fn && ids.has(fn)) S.listeners.delete(l + '|' + type + '|' + (opts === true || (opts && opts.capture) ? 'c' : '') + '|' + ids.get(fn)); return rem.call(this, type, fn, opts); };
    const WS = window.WebSocket; window.WebSocket = class extends WS { constructor(...a) { super(...a); S.ws.push(new WeakRef(this)); } };
    for (const N of ['MutationObserver', 'ResizeObserver', 'IntersectionObserver']) {
        const O = window[N]; if (!O) continue;
        window[N] = class extends O { constructor(...a) { super(...a); this.__n = 0; S.obs.push(new WeakRef(this)); }
            observe(t, ...r) { this.__n++; return super.observe(t, ...r); } unobserve(...r) { this.__n = Math.max(0, this.__n - 1); return super.unobserve(...r); } disconnect() { this.__n = 0; return super.disconnect(); } };
    }
    S.snapshot = () => ({ intervals: S.intervals.size, timeouts: S.timeouts.size, windowDocListeners: S.listeners.size,
        wsOpen: S.ws.filter(w => { const s = w.deref(); return s && s.readyState < 2; }).length, observers: S.obs.filter(w => { const o = w.deref(); return o && o.__n > 0; }).length });
})();`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { HeapProfiler, Performance } = client;
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a round's async work (history fetch, redraws) has no single completion event
    try {
        await Page.enable(); await Performance.enable(); await HeapProfiler.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: COUNTERS });
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); for (let i = 0; i < 8; i++) window.__m.add('stab-' + i, 'S' + i + '_', 30, { sleeping: false }); true`);
        await sleep(800); // sleep-ok: a round's async work has no single completion event
        const select = i => ev(`window.selectLiveSession('stab-${i % 8}', 'claude', 'stab-${i % 8}-id', 'local'); true`);
        let n = 0;
        const round = async () => {
            await select(n++); await sleep(180); // sleep-ok: a round's async work has no single completion event
            if (n % 4 === 0) { await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'modals.js'); await m.showSettingsModal(); return true; })()`); await sleep(250); await ev(`window.hideSettingsModal(); true`); } // sleep-ok: a round's async work has no single completion event
            if (n % 5 === 0) { await ev(`window.switchAgenticTab('tasks', 'top'); true`); await sleep(120); await ev(`window.switchAgenticTab('files', 'top'); true`); } // sleep-ok: a round's async work has no single completion event
        };
        const sample = async () => {
            for (let i = 0; i < 3; i++) { await HeapProfiler.collectGarbage(); await sleep(200); } // sleep-ok: a round's async work has no single completion event
            const m = Object.fromEntries((await Performance.getMetrics()).metrics.map(x => [x.name, x.value]));
            return { nodes: m.Nodes, listeners: m.JSEventListeners, ...(await ev(`window.__soak.snapshot()`)) };
        };
        for (let i = 0; i < 24; i++) await round();
        const a = await sample();
        for (let i = 0; i < 80; i++) await round();
        const b = await sample();
        console.log('after warm-up:', JSON.stringify(a)); console.log('after 80 more rounds:', JSON.stringify(b));
        assert.ok(b.nodes <= a.nodes * 1.02 + 40, `DOM nodes stay flat: ${a.nodes} -> ${b.nodes}`);
        assert.ok(b.listeners <= a.listeners * 1.02 + 10, `JS event listeners stay flat: ${a.listeners} -> ${b.listeners}`);
        assert.equal(b.windowDocListeners, a.windowDocListeners, 'listeners on window and document stay put');
        assert.ok(b.intervals <= a.intervals, `live intervals do not grow: ${a.intervals} -> ${b.intervals}`);
        assert.ok(b.timeouts <= a.timeouts + 3, `pending timeouts do not grow: ${a.timeouts} -> ${b.timeouts}`);
        assert.ok(b.wsOpen <= a.wsOpen, 'open sockets do not grow');
        assert.ok(b.observers <= a.observers + 1, `live observers do not grow: ${a.observers} -> ${b.observers}`);
        console.log('\nPASS 80 agent switches with views and dialogs leave the page flat');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
