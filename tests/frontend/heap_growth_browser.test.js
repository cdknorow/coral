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
const { removeBoards } = require('./helpers/board_cleanup');
const { launchStandIn } = require('./helpers/stand_in_agent');
const BASE = require('./test_base').BASE;
async function httpJson(method, path, body) {
    const r = await fetch(BASE + path, { method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
    let data = null; try { data = await r.json(); } catch { /* no body */ }
    return { status: r.status, data };
}

(async () => {
    const standIns = [];
    // A step that never finishes ends the suite naming itself, not at the runner's 180 s timeout with no output (#5107: the gate's runs hung 4 times in 20 with nothing to say where).
    let stepName = 'start', stepSince = Date.now();
    const mark = s => { stepName = s; stepSince = Date.now(); };
    const bound = ms => new Promise(r => setTimeout(r, ms)); // a limit raced against a call that may never answer, not a wait for a condition
    const STEP_MS = Number(process.env.HEAP_STEP_MS || 45000);
    // Is the page running script (a loop in the app) or not (stuck in the browser)? A pause request interrupts a running script; a page that is not running one never pauses.
    const diagnose = async () => {
        if (!conn) return 'the page had not connected yet: the stall is in starting Chrome or the CDP socket';
        try {
            // A plain evaluation answers when the page's main thread is free, whatever its pending promises wait for.
            const probe = await Promise.race([conn.Runtime.evaluate({ expression: `JSON.stringify({ visibility: document.visibilityState, focus: document.hasFocus(), terminal: !!document.querySelector('#xterm-container canvas'), view: document.body.className.slice(0, 80) })`, returnByValue: true }).then(r => r.result.value), bound(3000).then(() => 'the page did not answer a plain evaluation in 3 s: its main thread is blocked')]);
            const paused = new Promise(r => conn.Debugger.paused(r));
            await Promise.race([conn.Debugger.enable(), bound(2000)]);
            await Promise.race([conn.Debugger.pause(), bound(1000)]);
            const e = await Promise.race([paused, bound(3000).then(() => null)]);
            return `probe: ${probe}; ` + (e ? 'the page is running script, paused in: ' + e.callFrames.slice(0, 8).map(f => `${f.functionName || '(anon)'} ${String(f.url).split('/').pop()}:${f.location.lineNumber + 1}`).join(' < ') : 'the page did not pause: it is not running script');
        } catch (e) { return 'no diagnosis: ' + e.message; }
    };
    let watching = false;
    setInterval(async () => {
        if (watching || Date.now() - stepSince <= STEP_MS) return;
        watching = true;
        const secs = Math.round((Date.now() - stepSince) / 1000), where = stepName;
        console.error(`HANG heap_growth: no progress for ${secs} s in the step "${where}"; ${await diagnose()}`);
        if (!process.env.HEAP_RETRIED) {
            // The GitHub runners stall a page now and then (#5107: about one run in five, in the terminal step, with the server idle; the cause is not proven). Once, on a fresh page:
            // the stand-ins stopped first, the board kept by name so the second attempt removes it, and the stall stays in this log.
            console.error('RETRY heap_growth: once more on a fresh page');
            await Promise.race([Promise.all(standIns.map(si => si.stop().catch(() => {}))), bound(10000)]);
            const r = require('./helpers/child_process.js').spawnSync(process.execPath, [__filename], { stdio: 'inherit', env: { ...process.env, HEAP_RETRIED: '1', HEAP_BOARD: boardName } });
            process.exit(r.status === null ? 1 : r.status);
        }
        console.error(`FAIL heap_growth: it stalled twice`);
        process.exit(1);
    }, 1000).unref();
    const boardName = process.env.HEAP_BOARD || 'hg-board-' + Date.now().toString(36); // removed again in the finally (the gate fails a suite that leaves a board)
    // Everything above runs before the page connects, so a stall in connecting (Chrome or the CDP socket) is named too (#5189).
    mark('connecting to Chrome');
    let conn = null; // set once connected; the diagnosis needs it
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    const { HeapProfiler, Performance } = client;
    conn = client; mark('start');
    const CYCLES = Number(process.env.HEAP_CYCLES || 60), WARM = Math.min(25, Math.floor(CYCLES / 3)); // the terminals' xterms and the board are built on first use
    const AGENTS = 20;
    const metrics = async () => {
        mark('metrics: collectGarbage'); await HeapProfiler.collectGarbage(); await HeapProfiler.collectGarbage();
        mark('metrics: Performance.getMetrics'); const m = Object.fromEntries((await Performance.getMetrics()).metrics.map(x => [x.name, x.value]));
        return { heapMB: m.JSHeapUsedSize / 1048576, nodes: m.Nodes, listeners: m.JSEventListeners, documents: m.Documents };
    };
    const snapshot = async name => {
        if (!process.env.HEAP_SNAPSHOT_DIR) return;
        fs.mkdirSync(process.env.HEAP_SNAPSHOT_DIR, { recursive: true });
        const chunks = []; const on = e => chunks.push(e.chunk); HeapProfiler.addHeapSnapshotChunk(on);
        mark('snapshot: takeHeapSnapshot'); await HeapProfiler.takeHeapSnapshot({ reportProgress: false });
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
        const BOARD = boardName;
        const parts = (process.env.HEAP_PARTS || 'terminal,resize,board,history').split(',');
        const terminalPart = async n => {
            for (const si of parts.includes('terminal') ? standIns : []) {
                mark(`round ${n}: ${si.name}: opening it in the terminal view`);
                await ev(`window.selectLiveSession(${JSON.stringify(si.name)}, 'claude', ${JSON.stringify(si.sid)}, ''); window.setLiveViewMode('terminal'); true`);
                mark(`round ${n}: ${si.name}: waiting for its terminal canvas`);
                await waitFor(`!!document.querySelector('#xterm-container canvas')`, 'the terminal', 10000);
                mark(`round ${n}: ${si.name}: two frames in the terminal view`);
                await ev(`(async () => { await ${framesJs(2)}; return true; })()`);
                mark(`round ${n}: ${si.name}: back to the chat view`);
                await ev(`window.setLiveViewMode('chat'); true`);
            }
            mark(`round ${n}: resize, board and history`);
            if (parts.includes('resize')) await Emulation.setDeviceMetricsOverride({ width: n % 2 ? 1100 : 1280, height: 900, deviceScaleFactor: 1, mobile: false });
            if (parts.includes('board') || parts.includes('history')) await ev(`(async () => { const post = (p, b) => fetch(p, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(b) });
                if (${parts.includes('board')}) { if (${n} <= 5) { await post('/api/board/${BOARD}/subscribe', { subscriber_id: 'Operator', job_title: 'Operator' }); await post('/api/board/${BOARD}/messages', { subscriber_id: 'Operator', content: 'round ${n} ' + 'y'.repeat(120) }); } // (the board's own messages are bounded: it shows a window of the newest)
                window.switchNavTab('board'); if (window.selectBoardProject) await window.selectBoardProject(${JSON.stringify(BOARD)}); await ${framesJs(2)}; }
                if (${parts.includes('history')}) { window.switchNavTab('history'); await ${framesJs(2)}; }
                window.switchNavTab('agents'); await ${framesJs(1)}; return true; })()`);
        };
        const round = async n => {
            mark(`round ${n}: select`); await select('hg-' + (n % AGENTS));
            if (process.env.HEAP_STALL_TEST && n === 3 && !process.env.HEAP_RETRIED) await ev(`new Promise(() => {})`); // the retry's own test: a step that never finishes
            if (process.env.HEAP_LEAK_SELFTEST) await ev(`(() => { document.addEventListener('hg-leak', () => document.body); for (let i = 0; i < 3; i++) (window.__leak = window.__leak || []).push(document.body.appendChild(document.createElement('i'))); return true; })()`);
            mark(`round ${n}: feed diffs and a full update`);
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
            mark(`round ${n}: terminals`); if (withTerminals) await terminalPart(n);
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
        await ev(`(() => { const R = window.fetch; window.__failed = []; window.fetch = (u, o) => { const url0 = String(u && u.url || u); const pn = new URL(url0, location.origin).pathname; const board = pn.startsWith('/api/board/') && pn.endsWith('/tasks') && (!o || !o.method || o.method === 'GET'); // the board's tasks poll answers 200 here, so it is made to fail like the others: a real Response with a body that nobody reads
            return (board ? Promise.resolve(new Response(JSON.stringify({ error: 'no such board' }), { status: 404, headers: { 'Content-Type': 'application/json' } })) : R(u, o)).then(r => { const url = url0; if (!r.ok && r.status !== 304) window.__failed.push({ url, r }); return r; }); }; window.__R = R; return true; })()`);
        for (let n = 0; n < 6; n++) await round(1000 + n);
        const unreadInfo = await ev(`(() => { const FIVE = ['/api/agent/ui', '/poll', '/subagents', '/files', '/tasks']; const path = x => new URL(x.url, location.origin).pathname;
            const bad = window.__failed.filter(x => FIVE.some(f => path(x).endsWith(f)) && !x.r.bodyUsed).map(x => path(x).replace(/hg-[0-9]+/g, 'NAME'));
            const seen = FIVE.filter(f => window.__failed.some(x => path(x).endsWith(f))); window.fetch = window.__R; window.__failed = null; return { bad: [...new Set(bad)], seen }; })()`);
        const { bad: unread, seen } = unreadInfo;
        assert.deepEqual(seen.sort(), ['/api/agent/ui', '/files', '/poll', '/subagents', '/tasks'], 'the check saw all five pollers fail (a poller that stops failing in this window would otherwise pass it by default)');
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
    } finally { mark('cleanup: removeBoards'); await removeBoards(ev, [boardName]); mark('cleanup: stopping the stand-in agents'); for (const si of standIns) await si.stop().catch(() => {}); mark('cleanup: closing the page'); await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
