// A dashboard left open for an hour (#5417): the long-session soak. An isolated server with 6 real agents (the stub CLI every test server uses, writing a transcript and a heartbeat to its board: steady
// output), the page cycling chat -> terminal -> board -> artifacts every SOAK_CYCLE_S (30 s) on the next agent, and every SOAK_SAMPLE_MIN (5) minutes a sample after a forced GC: JS heap, DOM nodes,
// event listeners, documents, live timers and sockets, the xterm elements and the terminal's buffer, and what the chat, the board and the artifact list hold. The curve goes to the log as one JSON line per
// sample and to SOAK_OUT=file; the guard compares the last sample with the one at a quarter of the run (the page's caches have filled by then) and fails on growth past the bounds below.
//   a normal run (the gate): a 45 s smoke of the same loop, no guard (too few samples). The dispatch of cdp-gate.yml (workflow_dispatch) is the hour: 60 min; SOAK_MINUTES=n overrides either.
//   SOAK_LEAK_SELFTEST=1 adds a deliberate leak per cycle (a node, a listener and a retained string): the guard must catch it.
// Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const { guardSettings } = require('./helpers/settings_guard');
const { removeBoards, removeTeams } = require('./helpers/board_cleanup');
const { framesJs } = require('./helpers/wait');

const DISPATCH = process.env.GITHUB_EVENT_NAME === 'workflow_dispatch';
const MINUTES = Number(process.env.SOAK_MINUTES || (DISPATCH ? 60 : 0.75));
const CYCLE_S = Number(process.env.SOAK_CYCLE_S || (MINUTES >= 10 ? 30 : 5));
const SAMPLE_MIN = Number(process.env.SOAK_SAMPLE_MIN || (MINUTES >= 10 ? 5 : MINUTES / 3));
const AGENTS = 6;
// detached: nodes the page no longer shows but something still holds (the browser's node count less a walk of the document); domAll: what the document holds, with chatNodes and boardNodes (the message lists) the part that follows what the agents say (they grow with what the agents say, and have their own bound)
// growth between the sample at a quarter of the run and the last one
const BOUNDS = { heapMB: 25, detached: 300, domAll: 12000, chatNodes: 3000, boardNodes: 3000, listeners: 80, documents: 0, intervals: 0, sockets: 1, xtermEls: 1, bufferLines: 25000 };
const settle = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a soak waits for time; there is no event for "30 more seconds"

(async () => {
    const { client, Page, Emulation, ev: rawEv, waitFor, PRELOAD, install } = await connect();
    const { HeapProfiler, Performance } = client;
    const S = `(window.__coralStatic || '/static/')`;
    const BASE = process.env.CORAL_TEST_URL;
    const api = async (method, p, body) => { const r = await fetch(BASE + p, { method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) }); let data = null; try { data = await r.json(); } catch { /* no body */ } return { status: r.status, data }; };
    const STEP_MS = 60000;
    let step = 'start';
    const ev = (expression) => { step = String(expression).replace(/\s+/g, ' ').slice(0, 100); let t; return Promise.race([rawEv(expression), new Promise((_, rej) => { t = setTimeout(() => rej(new Error(`stalled ${STEP_MS} ms in ${step}`)), STEP_MS); })]).finally(() => clearTimeout(t)); };
    const guard = await guardSettings();
    const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'coral-soak-'));
    let team = null, board = null;
    const samples = [];
    try {
        // agents that write transcripts: the stub CLI the server already points at, with MOCK_AGENT_TRANSCRIPT=1
        const mock = ((await api('GET', '/api/settings')).data.settings || {}).cli_path_claude;
        if (!mock) throw new Error('the test server has no stub CLI for claude (cli_path_claude)');
        const wrapper = path.join(tmp, 'claude'); fs.writeFileSync(wrapper, `#!/bin/sh\nMOCK_AGENT_TRANSCRIPT=1 exec "${mock}" "$@"\n`, { mode: 0o755 });
        await api('PUT', '/api/settings', { cli_path_claude: wrapper });
        const launched = await api('POST', '/api/sessions/launch-team', { board_name: 'soak-' + Date.now().toString(36), working_dir: tmp, agent_type: 'claude', agents: Array.from({ length: AGENTS }, (_, i) => ({ name: `Soak ${i + 1}`, prompt: 'work steadily' })) });
        if (launched.status !== 200 || !launched.data || !launched.data.agents) throw new Error('launch-team failed: ' + launched.status + ' ' + JSON.stringify(launched.data).slice(0, 200));
        team = launched.data; board = team.board;
        console.log(`soak: ${MINUTES} min, a cycle every ${CYCLE_S} s, a sample every ${SAMPLE_MIN} min, ${team.agents.length} agents on board ${board}`);
        await Page.enable(); await HeapProfiler.enable(); await Performance.enable();
        await client.Network.disable().catch(() => {}); // DevTools keeps every response body while its Network domain is on: the tool's memory, not the page's
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD + `;(() => {
            const T = window.__soak = { intervals: new Set(), ws: new Set() };
            const si = window.setInterval, ci = window.clearInterval; window.setInterval = (f, ms, ...a) => { const id = si(f, ms, ...a); T.intervals.add(id); return id; }; window.clearInterval = id => { T.intervals.delete(id); return ci(id); };
            const WS = window.WebSocket; window.WebSocket = class extends WS { constructor(...a) { super(...a); T.ws.add(this); this.addEventListener('close', () => T.ws.delete(this)); } };
        })();` });
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await waitFor(`T.state.liveSessions.filter(s => /^Soak/.test(s.display_name || s.name || '')).length >= ${AGENTS}`, 'the six agents in the list', 60000);
        // the artifacts tab shows a list of 150 (stubbed: a real team has none yet)
        await ev(`(() => { const R = window.fetch; const items = Array.from({ length: 150 }, (_, i) => ({ id: String(i).padStart(2, '0').repeat(32).slice(0, 64), name: 'report-' + i + '.md', media_type: 'text/markdown', size: 2048 + i, task_id: 100 + i, task_title: 'Task ' + i, produced_by: 'soak', created_at: new Date(Date.UTC(2026, 9, 10, 10, 0, i)).toISOString(), uri: 'coral://artifacts/' + String(i).padStart(2, '0').repeat(32).slice(0, 64), available: true, inline: false }));
            window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin); if (/\\/api\\/board\\/[^/]+\\/artifacts$/.test(url.pathname) || url.pathname === '/api/session-artifacts') return Promise.resolve(new Response(JSON.stringify({ project: ${JSON.stringify(board)}, artifacts: items, has_more: false, truncated: false }), { status: 200, headers: { 'content-type': 'application/json' } })); return R(u, o); }; return true; })()`);
        const sessions = await ev(`T.state.liveSessions.filter(s => /^Soak/.test(s.display_name || s.name || '')).map(s => ({ name: s.name, id: s.session_id }))`);

        const metrics = async (at) => {
            await HeapProfiler.collectGarbage(); await HeapProfiler.collectGarbage();
            const m = Object.fromEntries((await Performance.getMetrics()).metrics.map(x => [x.name, x.value]));
            const page = await ev(`(() => { const t = window.__coralTest && window.__coralTest.getTerminal ? window.__coralTest.getTerminal() : null; const q = s => document.querySelectorAll(s).length;
                const name = e => (e.id ? '#' + e.id : e.tagName.toLowerCase() + (e.className && e.className.baseVal === undefined ? '.' + String(e.className).trim().split(/\s+/)[0] : ''));
                const top = []; let el = document.getElementById('agentic-state'); for (let d = 0; d < 9 && el; d++) { const kids = [...el.children].map(c => [c, c.getElementsByTagName('*').length]).sort((x, y) => y[1] - x[1]); if (!kids.length) break; top.push(name(kids[0][0]) + ':' + kids[0][1] + (kids[1] ? ' (next ' + name(kids[1][0]) + ':' + kids[1][1] + ')' : '')); el = kids[0][0]; }
                return { domAll: (() => { let n = 0; const w = document.createTreeWalker(document, NodeFilter.SHOW_ALL); while (w.nextNode()) n++; return n; })(), top, intervals: window.__soak.intervals.size, sockets: [...window.__soak.ws].filter(w => w.readyState < 2).length, xtermEls: q('.xterm'), bufferLines: t ? t.buffer.active.length : 0, termSize: t ? t.cols + 'x' + t.rows : '',
                    chatNodes: q('#live-history-messages *'), boardNodes: q('#mb-messages *, #board-panel-msgs *'), artifactRows: q('tr.team-artifact-row'), liveRows: q('#live-sessions-list li') }; })()`);
            const row = { minute: Math.round(at / 600) / 100, heapMB: +(m.JSHeapUsedSize / 1048576).toFixed(1), nodes: m.Nodes, listeners: m.JSEventListeners, documents: m.Documents, ...page };
            row.detached = row.nodes - row.domAll; samples.push(row); console.log('SOAK ' + JSON.stringify(row));
            return row;
        };
        const start = Date.now(), end = start + MINUTES * 60000;
        let nextSample = start, cycle = 0, posts = 0;
        const view = async (v, i) => {
            const s = sessions[i % sessions.length];
            if (v === 'chat') { await ev(`window.switchNavTab('agents'); window.selectLiveSession(${JSON.stringify(s.name)}, 'claude', ${JSON.stringify(s.id)}, ''); window.setLiveViewMode('chat'); true`); }
            else if (v === 'terminal') { await ev(`window.setLiveViewMode('terminal'); true`); }
            else if (v === 'board') { await ev(`window.switchNavTab('board'); window.selectBoardProject(${JSON.stringify(board)}); true`); }
            else { await ev(`window.switchNavTab('agents'); window.setLiveViewMode('chat'); window.switchAgenticTab('artifacts', 'top'); true`); }
            await ev(`${framesJs(3)}`);
        };
        while (Date.now() < end) {
            const t0 = Date.now();
            for (const v of ['chat', 'terminal', 'board', 'artifacts']) { await view(v, cycle); await settle(Math.max(200, Math.min(2000, CYCLE_S * 1000 / 8))); }
            // some traffic of our own on the board, and the live list keeps its feed
            await api('POST', `/api/board/${encodeURIComponent(board)}/messages`, { subscriber_id: 'Operator', content: `soak ${++posts}: ` + 'x'.repeat(80) }).catch(() => {});
            if (process.env.SOAK_LEAK_SELFTEST) await ev(`(() => { const d = document.createElement('div'); d.innerHTML = '<i></i>'.repeat(60); (window.__leak = window.__leak || []).push(d, 'x'.repeat(1000000) + Math.random()); document.addEventListener('soak-leak', () => d); return true; })()`);
            cycle++;
            if (Date.now() >= nextSample) { await metrics(Date.now() - start); nextSample += SAMPLE_MIN * 60000; }
            const wait = CYCLE_S * 1000 - (Date.now() - t0); if (wait > 0 && Date.now() + wait < end + CYCLE_S * 1000) await settle(Math.min(wait, Math.max(0, end - Date.now())));
        }
        await metrics(Date.now() - start);
        console.log(`soak: ${cycle} cycles, ${posts} board posts, ${samples.length} samples`);
        if (process.env.SOAK_OUT) fs.writeFileSync(process.env.SOAK_OUT, JSON.stringify({ minutes: MINUTES, cycleS: CYCLE_S, samples }, null, 1));
        // the guard: the last sample against the one at a quarter of the run
        let verdict = 'no guard (a smoke run has too few samples)';
        if (samples.length >= 4) {
            const ref = samples.find(s => s.minute >= MINUTES / 4) || samples[1], last = samples[samples.length - 1];
            const grew = Object.fromEntries(Object.keys(BOUNDS).map(k => [k, +(last[k] - (k === 'xtermEls' || k === 'sockets' || k === 'bufferLines' ? 0 : ref[k])).toFixed(1)]));
            const over = Object.keys(BOUNDS).filter(k => grew[k] > BOUNDS[k]);
            console.log(`SOAK guard vs minute ${ref.minute}: ${JSON.stringify(grew)} bounds ${JSON.stringify(BOUNDS)}`);
            if (process.env.SOAK_LEAK_SELFTEST) { if (!over.includes('heapMB') && !over.includes('detached')) throw new Error(`the guard did not catch the deliberate leak: ${JSON.stringify(grew)}`); console.log('soak selftest: ok (the guard caught the leak: ' + over.join(', ') + ')'); process.exitCode = 0; verdict = 'selftest'; }
            else if (over.length) throw new Error(`grew past its bound: ${over.map(k => `${k} +${grew[k]} (bound ${BOUNDS[k]})`).join(', ')}`);
            else verdict = 'within bounds';
        }
        console.log(`soak: ok (${verdict})`);
    } catch (e) { console.error('FAIL', e && e.stack || e); process.exitCode = 1; if (process.env.SOAK_OUT && samples.length) fs.writeFileSync(process.env.SOAK_OUT, JSON.stringify({ samples }, null, 1)); }
    finally {
        try { if (team && board) await api('POST', `/api/sessions/live/team/${encodeURIComponent(board)}/kill`, {}); } catch { /* the runner stops what is left */ }
        try { await removeBoards(rawEv, [board]); await removeTeams(rawEv, [board]); } catch { /* ... */ }
        try { await guard.restore(); } catch { /* ... */ }
        try { fs.rmSync(tmp, { recursive: true, force: true }); } catch { /* ... */ }
        await client.close().catch(() => {});
    }
    process.exit(process.exitCode || 0);
})();
