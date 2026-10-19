// The dashboard stays small when it is left open and used (#5048): 20 agents with chats, and a loop of what an operator does all day — switch to the next agent,
// a burst of feed diffs and a full update now and then, scroll the chat, open and shut the Display panel, open and shut a dialog — for many rounds. After the first
// rounds (caches fill, the page warms) the heap after a forced GC, the DOM nodes, the event listeners and the documents may not keep growing: each is compared with
// its value after the warm-up and must stay under a bound. HEAP_CYCLES=n runs longer (the hour on epoch is HEAP_CYCLES=3000 or so), HEAP_OUT=file writes the numbers as
// JSON, HEAP_FETCHES=file counts the page's fetches by URL and whether the body was read, HEAP_LEAK_SELFTEST=1 adds a deliberate leak (a listener and three nodes a round) that the bounds must catch, HEAP_SNAPSHOT_DIR=dir takes a heap snapshot after the warm-up and at the end (for the retainer paths of what grew).
// Part 2 (#5050): every round also opens three real terminals in turn (each an xterm and a /ws/terminal socket) and goes back to the chat, resizes the window, opens the
// board view and posts to it, and pages History. HEAP_TERMINAL=0 leaves that part out.
// Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { framesJs } = require('./helpers/wait');
const { launchStandIn } = require('./helpers/stand_in_agent');
const BASE = require('./test_base').BASE;
async function httpJson(method, path, body) {
    const r = await fetch(BASE + path, { method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
    let data = null; try { data = await r.json(); } catch { /* no body */ }
    return { status: r.status, data };
}

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    const { HeapProfiler, Performance } = client;
    const standIns = [];
    const CYCLES = Number(process.env.HEAP_CYCLES || 60), WARM = Math.min(25, Math.floor(CYCLES / 3)); // the terminals' xterms and the board are built on first use
    const AGENTS = 20;
    const metrics = async () => {
        await HeapProfiler.collectGarbage(); await HeapProfiler.collectGarbage();
        const m = Object.fromEntries((await Performance.getMetrics()).metrics.map(x => [x.name, x.value]));
        return { heapMB: m.JSHeapUsedSize / 1048576, nodes: m.Nodes, listeners: m.JSEventListeners, documents: m.Documents };
    };
    const snapshot = async name => {
        if (!process.env.HEAP_SNAPSHOT_DIR) return;
        fs.mkdirSync(process.env.HEAP_SNAPSHOT_DIR, { recursive: true });
        const chunks = []; const on = e => chunks.push(e.chunk); HeapProfiler.addHeapSnapshotChunk(on);
        await HeapProfiler.takeHeapSnapshot({ reportProgress: false });
        fs.writeFileSync(`${process.env.HEAP_SNAPSHOT_DIR}/${name}.heapsnapshot`, chunks.join(''));
    };
    const withTerminals = process.env.HEAP_TERMINAL !== '0';
    try {
        if (withTerminals) for (const name of ['HG Term A', 'HG Term B', 'HG Term C']) { const si = await launchStandIn(httpJson, { displayName: name }); if (!si.sid) throw new Error('no stand-in agent: ' + si.diag); standIns.push(si); }
        await Page.enable(); await HeapProfiler.enable(); await Performance.enable();
        // DevTools keeps the body of every response it saw (blink::RawResource, NetworkResourcesData) while its Network domain is on: that is the tool's memory, not the page's.
        await client.Network.disable().catch(() => {});
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`localStorage.clear(); localStorage.setItem('coral-live-view-mode', 'chat'); true`);
        await ev(`(() => { for (let i = 0; i < ${AGENTS}; i++) window.__m.add('hg-' + i, String.fromCharCode(65 + i), 200, { sleeping: false, board_project: 'team-' + (i % 4) });
            T.render.renderLiveSessions(T.state.liveSessions); window.__round = 0; return true; })()`);
        if (process.env.HEAP_FETCHES) await ev(`(() => { const R = window.fetch; window.__f = {}; window.fetch = (u, o) => { const k = (o && o.method || 'GET') + ' ' + String(u && u.url || u).replace(/[0-9a-f-]{8,}/g, '#').split('?')[0]; const e = window.__f[k] = window.__f[k] || { n: 0, settled: 0, bodyRead: 0 }; e.n++;
            return R(u, o).then(r => { e.settled++; const j = r.json.bind(r), t = r.text.bind(r), b = r.blob.bind(r); r.json = (...a) => { e.bodyRead++; return j(...a); }; r.text = (...a) => { e.bodyRead++; return t(...a); }; r.blob = (...a) => { e.bodyRead++; return b(...a); }; return r; }); }; return true; })()`);
        const BOARD = 'hg-board-' + Date.now().toString(36);
        const parts = (process.env.HEAP_PARTS || 'terminal,resize,board,history').split(',');
        const terminalPart = async n => {
            for (const si of parts.includes('terminal') ? standIns : []) {
                await ev(`window.selectLiveSession(${JSON.stringify(si.name)}, 'claude', ${JSON.stringify(si.sid)}, ''); window.setLiveViewMode('terminal'); true`);
                await waitFor(`!!document.querySelector('#xterm-container canvas')`, 'the terminal', 10000);
                await ev(`(async () => { await ${framesJs(2)}; window.setLiveViewMode('chat'); return true; })()`);
            }
            if (parts.includes('resize')) await Emulation.setDeviceMetricsOverride({ width: n % 2 ? 1100 : 1280, height: 900, deviceScaleFactor: 1, mobile: false });
            if (parts.includes('board') || parts.includes('history')) await ev(`(async () => { const post = (p, b) => fetch(p, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(b) });
                if (${parts.includes('board')}) { if (${n} <= 5) { await post('/api/board/${BOARD}/subscribe', { subscriber_id: 'Operator', job_title: 'Operator' }); await post('/api/board/${BOARD}/messages', { subscriber_id: 'Operator', content: 'round ${n} ' + 'y'.repeat(120) }); } // (the board's own messages are bounded: it shows a window of the newest)
                window.switchNavTab('board'); if (window.selectBoardProject) await window.selectBoardProject(${JSON.stringify(BOARD)}); await ${framesJs(2)}; }
                if (${parts.includes('history')}) { window.switchNavTab('history'); await ${framesJs(2)}; }
                window.switchNavTab('agents'); await ${framesJs(1)}; return true; })()`);
        };
        const round = async n => {
            await select('hg-' + (n % AGENTS));
            if (process.env.HEAP_LEAK_SELFTEST) await ev(`(() => { document.addEventListener('hg-leak', () => document.body); for (let i = 0; i < 3; i++) (window.__leak = window.__leak || []).push(document.body.appendChild(document.createElement('i'))); return true; })()`);
            await ev(`(async () => { const rnd = i => 'hg-' + ((i * 7 + ${n}) % ${AGENTS}) + '-id';
                for (let i = 0; i < 10; i++) { const a = window.__m.agents[rnd(i)]; T.ws.handleCoralMessage({ type: 'coral_diff', changed: [{ ...window.__m.entry(a), summary: 'step ' + ${n} + '.' + i, working: i % 2 === 0, staleness_seconds: i }] }); }
                if (${n} % 5 === 0) T.ws.handleCoralMessage({ type: 'coral_update', servers: [], sessions: Object.values(window.__m.agents).map(a => ({ ...window.__m.entry(a), summary: 'full ' + ${n} })) });
                T.render.flushLiveSessionsRender();
                { const cur = window.__m.agents['hg-' + (${n} % ${AGENTS}) + '-id']; if (cur) { for (let k = 0; k < 3; k++) cur.msgs.push(k % 2 ? { type: 'assistant', text: 'live ' + ${n} + '.' + k + ' ' + 'x'.repeat(200) } : { type: 'user', content: 'ask ' + ${n} + '.' + k }); }
                  const lc = await import((window.__coralStatic || '/static/') + 'live_chat.js'); if (lc.refreshLiveHistory) await lc.refreshLiveHistory(); }
                if (window.showSettingsModal) { await window.showSettingsModal(); await ${framesJs(1)}; window.hideSettingsModal && window.hideSettingsModal(); }
                const c = document.getElementById('live-history-messages'); if (c) { c.scrollTop = 0; c.dispatchEvent(new Event('scroll')); c.scrollTop = c.scrollHeight; c.dispatchEvent(new Event('scroll')); }
                const b = document.querySelector('.sidebar-header [data-display-button]'); if (b) { b.click(); await ${framesJs(1)}; b.click(); }
                if (window.showLaunchModal) { await window.showLaunchModal(); await ${framesJs(1)}; window.hideLaunchModal(); }
                await ${framesJs(2)}; return true; })()`);
            if (withTerminals) await terminalPart(n);
        };
        // HEAP_LISTENERS=1: the event listeners on window, document, body and every element, by type, at the warm-up and at the end
        const listenerCensus = async () => {
            const out = {};
            for (const [label, expr] of [['window', 'window'], ['document', 'document'], ['body', 'document.body']]) {
                const { result } = await client.Runtime.evaluate({ expression: expr, objectGroup: 'census' });
                const { listeners } = await client.DOMDebugger.getEventListeners({ objectId: result.objectId });
                for (const l of listeners) out[`${label} ${l.type}`] = (out[`${label} ${l.type}`] || 0) + 1;
            }
            const { result: all } = await client.Runtime.evaluate({ expression: 'Array.from(document.querySelectorAll("*"))', objectGroup: 'census' });
            const { result: props } = await client.Runtime.getProperties({ objectId: all.objectId, ownProperties: true, objectGroup: 'census' });
            for (const p of props) {
                if (!/^\d+$/.test(p.name) || !p.value || !p.value.objectId) continue;
                const { listeners } = await client.DOMDebugger.getEventListeners({ objectId: p.value.objectId });
                for (const l of listeners) { const k = `element ${l.type}`; out[k] = (out[k] || 0) + 1; }
            }
            await client.Runtime.releaseObjectGroup({ objectGroup: 'census' }); // (the remote handles would keep every node alive)
            return out;
        };
        let lw = null;
        // The pollers that fail (here the dev server has no such agents: 404) give up the body they will not read, so no loader and buffer waits for the collector (#5050).
        await ev(`(() => { const R = window.fetch; window.__failed = []; window.fetch = (u, o) => R(u, o).then(r => { const url = String(u && u.url || u); if (!r.ok && r.status !== 304) window.__failed.push({ url, r }); return r; }); window.__R = R; return true; })()`);
        for (let n = 0; n < 6; n++) await round(1000 + n);
        const unread = await ev(`(() => { const FIVE = ['/api/agent/ui', '/poll', '/subagents', '/files', '/tasks']; const path = x => new URL(x.url, location.origin).pathname;
            const bad = window.__failed.filter(x => FIVE.some(f => path(x).endsWith(f)) && !x.r.bodyUsed).map(x => path(x).replace(/hg-[0-9]+/g, 'NAME')); window.fetch = window.__R; window.__failed = null; return [...new Set(bad)]; })()`);
        assert.deepEqual(unread, [], 'a failed poll leaves its response body unread (cancel it, or read it): ' + unread.join(', '));
        const series = [];
        let base = null;
        for (let n = 1; n <= CYCLES; n++) {
            await round(n);
            if (n === WARM) { base = await metrics(); series.push({ n, ...base }); await snapshot('warm'); if (process.env.HEAP_LISTENERS) lw = await listenerCensus(); }
            else if (n > WARM && (n % Math.max(5, Math.floor(CYCLES / 12)) === 0 || n === CYCLES)) series.push({ n, ...(await metrics()) });
        }
        await snapshot('end');
        if (process.env.HEAP_LISTENERS) { const le = await listenerCensus(); const keys = [...new Set([...Object.keys(lw), ...Object.keys(le)])]; console.log('LISTENERS grown: ' + keys.map(k => [k, (le[k] || 0) - (lw[k] || 0)]).filter(x => x[1] !== 0).sort((a, b) => b[1] - a[1]).map(x => x.join('=')).join(', ')); }
        if (process.env.HEAP_FETCHES) fs.writeFileSync(process.env.HEAP_FETCHES, JSON.stringify(await ev(`window.__f`), null, 1));
        const end = series[series.length - 1];
        const row = s => `n=${String(s.n).padStart(5)} heap=${s.heapMB.toFixed(1)} MB nodes=${s.nodes} listeners=${s.listeners} documents=${s.documents}`;
        console.log(series.map(row).join('\n'));
        if (process.env.HEAP_OUT) fs.writeFileSync(process.env.HEAP_OUT, JSON.stringify({ cycles: CYCLES, warm: WARM, series }, null, 1));
        const per = CYCLES - WARM;
        const grew = (a, b) => b - a;
        // bounds per round after the warm-up (a leak of a node, a listener or a few KB a round shows over the rounds; caches that are bounded do not)
        assert.ok(grew(base.heapMB, end.heapMB) <= 3 + 0.05 * per, `the heap after GC grew ${grew(base.heapMB, end.heapMB).toFixed(1)} MB over ${per} rounds (bound ${(3 + 0.05 * per).toFixed(1)}): ${row(base)} -> ${row(end)}`);
        assert.ok(grew(base.nodes, end.nodes) <= 100 + per / 2, `DOM nodes grew ${grew(base.nodes, end.nodes)} over ${per} rounds`);
        assert.ok(grew(base.listeners, end.listeners) <= 40 + per / 5, `event listeners grew ${grew(base.listeners, end.listeners)} over ${per} rounds`);
        assert.ok(grew(base.documents, end.documents) <= 3, `documents grew ${grew(base.documents, end.documents)} (detached frames or windows)`);
        console.log(`PASS heap growth: ${per} rounds after a ${WARM}-round warm-up: heap ${grew(base.heapMB, end.heapMB).toFixed(1)} MB, nodes ${grew(base.nodes, end.nodes)}, listeners ${grew(base.listeners, end.listeners)}`);
    } finally { for (const si of standIns) await si.stop().catch(() => {}); await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
