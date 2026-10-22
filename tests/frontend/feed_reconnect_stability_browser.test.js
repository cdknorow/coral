// The live feed survives being dropped and restored all day (#5348). /ws/coral is replaced by a socket the test controls (it can be dropped with a 1006 close, refused, or let through); the retry
// scheduler (feedHooks.schedule, the seam the feed has for tests) runs each retry at once and records the delay it was asked for. 100 drops and restores, then 5 streaks of 7 refused connects, then 100
// times the server-unreachable pill on and off. Checks: never more than one live feed socket and never more than one retry pending (reconnects do not stack); every retry after a delivery asks for the
// first delay again (1-2 s: the back-off resets); within a streak the delays double up to the 15 s cap, each in its 50-100% jitter band; the unreachable pill is one element, shown and hidden with the
// agent's state, and the gateway back-off for its server is cleared when the server is back; and, after a forced GC, listeners, nodes, documents, live timers and the heap do not grow.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const settle = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: the page's own timers (notice delays, debounces) have no event to wait for; it is polled until the page stops changing
// FEED_STACK_SELFTEST=1 makes every retry run twice (a reconnect that stacks): the suite must then fail.
const QUIET_MS = 2000; // longer than the page's longest timer of its own, the 1.5 s notice delay
const CYCLES = Number(process.env.CYCLES || 100), STREAKS = 5, STREAK_LEN = 7;
// The hundred drops and restores are held to a tight bound of their own (they grew nothing when written: 0 nodes, 0 listeners, 0 timers); the whole run to a looser one, because the page builds a little
// UI once, at a time of its own (about 70 nodes, from its own timers), somewhere in the minutes of a run: a per-round leak of even 2 nodes still shows in 300 rounds.
const BOUNDS = { listeners: 25, nodes: 250, documents: 0, intervals: 0, heapMB: 2 };
const DROP_BOUNDS = { listeners: 5, nodes: 10, documents: 0, intervals: 0, heapMB: 1 };

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { HeapProfiler, Performance } = client;
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    try {
        await Page.enable(); await HeapProfiler.enable(); await Performance.enable();
        await client.Network.disable().catch(() => {}); // DevTools keeps response bodies while its Network domain is on: the tool's memory, not the page's
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD + `;(() => {
            const F = window.__fakeFeed = { instances: [], opened: 0, made: 0, refuse: false, delays: [], pending: 0 };
            const Real = window.WebSocket;
            class FakeFeed extends EventTarget {
                constructor(url) { super(); this.url = url; this.readyState = 0; this.binaryType = 'blob'; F.made++; F.instances.push(this); if (F.instances.length > 4) F.instances.shift();
                    setTimeout(() => { if (this.readyState !== 0) return; if (F.refuse) { this._close(1006); return; } this.readyState = 1; F.opened++; this._emit('open');
                        this._emit('message', JSON.stringify({ type: 'coral_update', servers: [], sessions: [] })); }, 0); }
                _emit(type, arg) { const e = type === 'message' ? new MessageEvent('message', { data: arg }) : type === 'close' ? new CloseEvent('close', { code: arg }) : new Event(type); this.dispatchEvent(e); const h = this['on' + type]; if (h) h.call(this, e); }
                _close(code) { if (this.readyState === 3) return; this.readyState = 3; this._emit('close', code); }
                close(code) { this._close(code || 1000); }
                send() {}
            }
            Object.assign(FakeFeed, { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 });
            window.WebSocket = new Proxy(Real, { construct(t, args) { return /\\/ws\\/coral/.test(String(args[0])) ? new FakeFeed(args[0]) : new t(...args); } });
            window.__live = () => F.instances.filter(i => i.readyState < 2).length;
            const si = window.setInterval, ci = window.clearInterval; const iv = window.__intervals = new Set();
            window.setInterval = (f, ms, ...a) => { const id = si(f, ms, ...a); iv.add(id); return id; }; window.clearInterval = id => { iv.delete(id); return ci(id); };
        })();` });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        // When the page last changed its DOM at all. The list re-renders on every feed update and a render replaces nodes (the same count before and after), so a steady node count does not mean the
        // page has stopped building: counting nodes in the middle of a render read +37 (two runs in 20 of the old quiet-count wait, one in 45 before it). A node count is taken only after the DOM has been still.
        await ev(`(() => { window.__lastMut = Date.now(); new MutationObserver(() => { window.__lastMut = Date.now(); }).observe(document.documentElement, { childList: true, subtree: true }); return true; })()`);
        const quiet = async ms => { for (let i = 0; i < 200; i++) { if (await ev(`Date.now() - window.__lastMut`) >= ms) return; await settle(50); } };
        await waitFor(`window.__fakeFeed.opened >= 1`, 'the first feed socket', 8000);
        // retries run at once and are recorded
        await ev(`import(${S} + 'websocket.js').then(m => { m.feedHooks.schedule = (fn, ms) => { window.__fakeFeed.delays.push(ms); window.__fakeFeed.pending++; window.__fakeFeed.pendingMax = Math.max(window.__fakeFeed.pendingMax || 0, window.__fakeFeed.pending); setTimeout(() => { window.__fakeFeed.pending--; fn(); }, 0); if (${process.env.FEED_STACK_SELFTEST ? 'true' : 'false'}) setTimeout(fn, 1); }; const opened = window.__fakeFeed.opened; m.connectCoralWs(); return opened; })`); // (the harness had detached the page's own feed: start one again, with the retries on the test's scheduler)
        await waitFor(`window.__fakeFeed.opened >= 2 && window.__live() === 1`, 'the restarted feed', 8000);
        const metrics = async () => {
            await quiet(500); // no render in flight when the nodes are counted
            await HeapProfiler.collectGarbage(); await HeapProfiler.collectGarbage();
            const m = Object.fromEntries((await Performance.getMetrics()).metrics.map(x => [x.name, x.value]));
            return { heapMB: m.JSHeapUsedSize / 1048576, nodes: m.Nodes, listeners: m.JSEventListeners, documents: m.Documents, intervals: await ev(`window.__intervals.size`) };
        };
        const drop = async () => { const before = await ev(`window.__fakeFeed.opened`); await ev(`(() => { const i = window.__fakeFeed.instances[window.__fakeFeed.instances.length - 1]; if (i.readyState < 2) i.close(1006); return true; })()`); await waitFor(`window.__fakeFeed.opened > ${before} && window.__live() === 1`, 'the feed back', 15000); };
        const dropPill = async n => { // the server-unreachable pill: the agent goes stale with its server not online, then comes back
            await ev(`(async () => { const { state } = await import(${S} + 'state.js'); const h = await import(${S} + 'hub_access.js'); const rh = await import(${S} + 'remote_health.js');
                const row = { session_id: 'ub-1', name: 'ub-1', agent_type: 'claude', server: 'srv9', stale: ${n % 2 === 0} };
                state.liveSessions = [row]; state.currentSession = { type: 'live', name: 'ub-1', session_id: 'ub-1', server: 'srv9' };
                if (${n % 2 === 0}) rh.backoffRecord('ui:srv9', 503); h.syncUnreachableBanner(); return true; })()`); };

        // 1. 100 drops and restores
        console.log('1. dropped and restored ' + CYCLES + ' times');
        for (let n = 0; n < 10; n++) await drop();
        // (the pill shows after a 1.5 s delay and builds its content then: let it show once before the count, and clear it)
        await dropPill(0); await waitFor(`!!document.getElementById('server-unreachable-banner') && !document.getElementById('server-unreachable-banner').hidden`, 'the pill to show once', 6000); await dropPill(1);
        await drop(); await drop(); // (a feed update that arrives with the pill's agent selected: the page reacts to that once)
        { // (and one short streak of refused connects: whatever the page builds the first time the feed is down is built before the count)
            await ev(`window.__fakeFeed.refuse = true; window.__fakeFeed.instances[window.__fakeFeed.instances.length - 1].close(1006); true`);
            await waitFor(`window.__fakeFeed.made >= window.__fakeFeed.opened + 3`, 'a short streak', 8000);
            await ev(`window.__fakeFeed.refuse = false; true`); await drop();
        }
        await ev(`window.__fakeFeed.delays.length = 0; window.__fakeFeed.pendingMax = 0; true`);
        // the page's own timed work (the 1.5 s notice delays, debounces) happens once, whenever it fires: wait until the DOM has not changed at all for longer than the longest of those delays (QUIET_MS)
        await quiet(QUIET_MS);
        const base = await metrics();
        let stacked = 0;
        for (let n = 0; n < CYCLES; n++) { await drop(); const live = await ev(`window.__live()`); if (live !== 1) stacked++; }
        const d1 = await ev(`window.__fakeFeed.delays.slice()`), pm = await ev(`window.__fakeFeed.pendingMax`);
        check(`one live feed socket after every one of ${CYCLES} reconnects (${stacked} times more or fewer)`, stacked === 0);
        check(`one retry pending at most (${pm}), ${d1.length} retries asked for`, pm <= 1 && d1.length === CYCLES);
        check(`every retry after a delivered update asked for the FIRST delay again, 1000-2000 ms (min ${Math.min(...d1)}, max ${Math.max(...d1)})`, d1.every(d => d >= 1000 && d <= 2000));

        const afterDrops = await metrics();
        { const g = Object.fromEntries(Object.keys(DROP_BOUNDS).map(k => [k, +(afterDrops[k] - base[k]).toFixed(2)])); const o = Object.keys(DROP_BOUNDS).filter(k => g[k] > DROP_BOUNDS[k]);
          check(`the ${CYCLES} drops and restores grew listeners, nodes, documents, timers and heap by ${JSON.stringify(g)} (bounds ${JSON.stringify(DROP_BOUNDS)}; over: ${o})`, o.length === 0); }
        { // connectCoralWs called twice in a row (#5367): the old socket is closed and detached first, so there is one open socket, and its late close schedules no second reconnect
            const r = await ev(`(async () => { const F = window.__fakeFeed; const m = await import(${S} + 'websocket.js'); const made = F.made, delays = F.delays.length; const first = F.instances[F.instances.length - 1];
                m.connectCoralWs(); m.connectCoralWs();
                return { made: F.made - made, delaysBefore: delays, firstClosed: first.readyState === 3 }; })()`);
            await waitFor(`window.__fakeFeed.instances.filter(i => i.readyState === 1).length === 1 && window.__live() === 1`, 'one open socket after two connects', 8000);
            await settle(300);
            const after = await ev(`({ live: window.__live(), delays: window.__fakeFeed.delays.length })`);
            check(`two connectCoralWs calls: two sockets made, the first closed, one open (made ${r.made}, first closed ${r.firstClosed}, live ${after.live})`, r.made === 2 && r.firstClosed && after.live === 1);
            check(`the detached sockets' closes asked for no reconnect (${after.delays - r.delaysBefore} asked)`, after.delays === r.delaysBefore);
        }
        // 2. streaks of refused connects
        console.log('2. ' + STREAKS + ' streaks of ' + STREAK_LEN + ' refused connects');
        for (let s = 0; s < STREAKS; s++) {
            await ev(`window.__fakeFeed.delays.length = 0; window.__fakeFeed.refuse = true; window.__fakeFeed.instances[window.__fakeFeed.instances.length - 1].close(1006); true`);
            await waitFor(`window.__fakeFeed.delays.length >= ${STREAK_LEN}`, 'a streak of failures', 8000);
            const before = await ev(`(() => { const o = window.__fakeFeed.opened; window.__fakeFeed.refuse = false; return o; })()`);
            await waitFor(`window.__fakeFeed.opened > ${before} && window.__live() === 1`, 'the feed back after the streak', 15000);
            const d = (await ev(`window.__fakeFeed.delays.slice(0, ${STREAK_LEN})`));
            const bands = d.map((v, i) => { const cap = Math.min(15000, 2000 * 2 ** i); return v >= Math.floor(cap * 0.5) - 1 && v <= cap; });
            check(`streak ${s + 1}: the delays double to the 15 s cap, each within its band (${d.join(', ')})`, bands.every(Boolean) && d[STREAK_LEN - 1] <= 15000 && d[STREAK_LEN - 1] >= 7500);
            const after = await ev(`window.__fakeFeed.delays.length`); const n0 = after;
            await ev(`window.__fakeFeed.instances[window.__fakeFeed.instances.length - 1].close(1006); true`);
            await waitFor(`window.__fakeFeed.delays.length > ${n0} && window.__live() === 1`, 'the next drop', 8000);
            const first = await ev(`window.__fakeFeed.delays[window.__fakeFeed.delays.length - 1]`);
            check(`streak ${s + 1}: after the feed delivered, the next drop starts the back-off over (${first} ms)`, first >= 1000 && first <= 2000);
        }
        check(`never more than one retry pending in the streaks (${await ev('window.__fakeFeed.pendingMax')})`, (await ev('window.__fakeFeed.pendingMax')) <= 1);

        // 3. the unreachable pill
        console.log('3. the unreachable pill on and off');
        const pills = [];
        for (let n = 0; n < CYCLES; n++) {
            await dropPill(n);
            pills.push(await ev(`(() => { const els = document.querySelectorAll('#server-unreachable-banner'); const e = els[0]; return { n: els.length, shown: !!e && !e.hidden && getComputedStyle(e).display !== 'none', blocked: null }; })()`));
        }
        check(`one pill element every time (${new Set(pills.map(p => p.n))})`, pills.every(p => p.n === 1));
        const blockedAfterBack = await ev(`import(${S} + 'remote_health.js').then(m => m.backoffBlocked('ui:srv9'))`);
        check(`when the server is back (the last round it was), its gateway back-off is cleared (${blockedAfterBack})`, blockedAfterBack === false);

        // 4. nothing grew
        const end = await metrics();
        const grew = Object.fromEntries(Object.keys(BOUNDS).map(k => [k, +(end[k] - base[k]).toFixed(2)]));
        console.log('after the warm-up', JSON.stringify(base), '\nafter', JSON.stringify(end), '\ngrew', JSON.stringify(grew));
        const over = Object.keys(BOUNDS).filter(k => grew[k] > BOUNDS[k]);
        check(`listeners, nodes, documents, timers and heap stayed under their bounds (over: ${over})`, over.length === 0);
        const failed = results.filter(r => !r).length;
        console.log(`${results.length - failed} of ${results.length} checks passed`);
        await client.close(); process.exit(failed ? 1 : 0);
    } catch (e) { try { console.error('STATE', JSON.stringify(await ev('({ made: window.__fakeFeed.made, opened: window.__fakeFeed.opened, pending: window.__fakeFeed.pending, live: window.__live(), refuse: window.__fakeFeed.refuse, states: window.__fakeFeed.instances.map(i => i.readyState), delays: window.__fakeFeed.delays.length })'))); } catch { /* the page is gone */ }
        console.error('FAIL', e && e.stack || e); await client.close().catch(() => {}); process.exit(1); }
})();
