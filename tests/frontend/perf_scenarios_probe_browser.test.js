// The dashboard's four everyday waits at scale (#5329), opt-in: it measures, it does not assert. 50 mock agents in 5 teams, a real board with 2,000 messages on the isolated server, a team artifacts
// list of 300 (stubbed), Chat view. Each scenario runs 10 times and the median is reported (wall ms to the settled view, and the main thread's CPU: task, script, layout, style ms):
//   firstPaint  navigation to the first agent list drawn (and the browser's first contentful paint)
//   agentSwitch select the next agent, to the chat settled (coral:agent-view-ready, then two frames)
//   openBoard   the Message Board page opened on the 2,000-message board, to its first messages drawn and two frames
//   artifactsTab the Team Artifacts tab opened for a team's agent, to its rows drawn and two frames
//   PERF_PROBE=1 PERF_THROTTLE=4 PERF_OUT=/tmp/x.json node run_cdp_suites.mjs --only perf_scenarios_probe --shards 1 --retries 0
// PERF_PROFILE=1 adds a sampling profile of each scenario (top self-time functions; CORAL_NO_BUNDLE=1 keeps their names readable). RUNS=n changes the 10.
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const { framesJs } = require('./helpers/wait');
const { removeBoards } = require('./helpers/board_cleanup');

const AGENTS = 50, MESSAGES = 2000, ARTIFACTS = 300;
const BOARD = 'pf-board-' + Date.now().toString(36);
const FIXTURE = `(() => {
  const mock = window.__m; const teams = ['alpha', 'beta', 'gamma', 'delta', 'epsilon'];
  for (let i = 0; i < ${AGENTS}; i++) { const a = mock.add('agent-' + i, 'A' + i + '_', 0, { sleeping: false, board_project: i % 5 === 0 ? ${JSON.stringify(BOARD)} : teams[i % 5], session_id: 'sid-' + i });
    a.msgs = Array.from({ length: 40 }, (_, k) => k % 2 ? { type: 'assistant', text: 'Reply ' + k + ' with **markdown** and \\\`code\\\`.', key: 'k' + k } : { type: 'user', content: 'Question ' + k, key: 'k' + k }); }
})();`;
const OBS = `(() => { window.__firstList = null; window.addEventListener('coral:live-list-rendered', () => { if (window.__firstList === null) window.__firstList = Math.round(performance.now()); }); window.__longtasks = [];
  try { new PerformanceObserver(l => { for (const e of l.getEntries()) window.__longtasks.push(e.duration); }).observe({ type: 'longtask', buffered: true }); } catch {} })();`;
const median = a => { if (!a.length) return null; const s = [...a].sort((x, y) => x - y); return Math.round(((s[(s.length - 1) >> 1] + s[s.length >> 1]) / 2) * 10) / 10; };

if (!process.env.PERF_PROBE) { console.log('perf_scenarios_probe: skipped (set PERF_PROBE=1 to measure)'); process.exit(0); }
(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Performance } = client;
    const RUNS = Number(process.env.RUNS || 10), out = { runs: RUNS, throttle: Number(process.env.PERF_THROTTLE || 4), agents: AGENTS, messages: MESSAGES, artifacts: ARTIFACTS };
    const metrics = async () => Object.fromEntries((await Performance.getMetrics()).metrics.map(m => [m.name, m.value]));
    const delta = (a, b) => b.TaskDuration < a.TaskDuration ? { task: b.TaskDuration * 1000, script: b.ScriptDuration * 1000, layout: b.LayoutDuration * 1000, style: b.RecalcStyleDuration * 1000 } /* a new document: its counters start again */ : ({ task: (b.TaskDuration - a.TaskDuration) * 1000, script: (b.ScriptDuration - a.ScriptDuration) * 1000, layout: (b.LayoutDuration - a.LayoutDuration) * 1000, style: (b.RecalcStyleDuration - a.RecalcStyleDuration) * 1000 });
    const { Profiler } = client; const prof = !!process.env.PERF_PROFILE;
    const profStart = async () => { if (!prof) return; await Profiler.enable(); await Profiler.setSamplingInterval({ interval: 250 }); await Profiler.start(); };
    const profStop = async name => { if (!prof) return; const { profile } = await Profiler.stop(); const byId = new Map(profile.nodes.map(n => [n.id, n])); const self = new Map();
        profile.samples.forEach((id, i) => self.set(id, (self.get(id) || 0) + (profile.timeDeltas[i] || 0)));
        const agg = new Map(); for (const [id, us] of self) { const cf = byId.get(id).callFrame; const key = (cf.functionName || '(anon)') + ' ' + (cf.url || '').split('/static/').pop() + ':' + cf.lineNumber; agg.set(key, (agg.get(key) || 0) + us); }
        out.profileJsMs = out.profileJsMs || {}; out.profileJsMs[name] = Math.round([...agg].filter(([k]) => !/^\((idle|program|garbage)/.test(k)).reduce((t, [, us]) => t + us, 0) / 1000 / RUNS * 10) / 10; /* the JavaScript the page ran, per run */ if (process.env.PERF_CALLERS) { const parent = new Map(); for (const n of profile.nodes) for (const c of (n.children || [])) parent.set(c, n.id); const who = new Map();
            for (const [id, us] of self) { const n = byId.get(id); if (n.callFrame.functionName !== process.env.PERF_CALLERS) continue; const chain = []; for (let c = parent.get(id), k = 0; c && k < 5; c = parent.get(c), k++) { const cf = byId.get(c).callFrame; chain.push((cf.functionName || '(anon)') + ' ' + (cf.url || '').split('/static/').pop() + ':' + cf.lineNumber + ':' + cf.columnNumber); } const key = chain.join(' < '); who.set(key, (who.get(key) || 0) + us); }
            out.callers = out.callers || {}; out.callers[name] = [...who].sort((a, b) => b[1] - a[1]).slice(0, 4).map(([k, us]) => Math.round(us / 1000) + 'ms ' + k); }
        out.profile = out.profile || {}; out.profile[name] = [...agg].filter(([k]) => !/^\((idle|program)/.test(k)).sort((a, b) => b[1] - a[1]).slice(0, 14).map(([k, us]) => Math.round(us / 1000) + 'ms ' + k); };
    const stats = rows => ({ wallMs: median(rows.map(r => r.wall)), taskMs: median(rows.map(r => r.task)), scriptMs: median(rows.map(r => r.script)), layoutMs: median(rows.map(r => r.layout)), styleMs: median(rows.map(r => r.style)), longTasks: median(rows.map(r => r.lt)), all: rows.map(r => Math.round(r.wall)) });
    try {
        await Page.enable(); await Performance.enable();
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
        // the board's 2,000 messages, on the isolated server
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(async () => { const post = (p, b) => fetch(p, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(b) });
            await post('/api/board/${BOARD}/subscribe', { subscriber_id: 'Operator', job_title: 'Operator' }); await post('/api/board/${BOARD}/subscribe', { subscriber_id: 'Dev One', job_title: 'Developer' });
            for (let i = 0; i < ${MESSAGES}; i += 10) await Promise.all(Array.from({ length: 10 }, (_, k) => post('/api/board/${BOARD}/messages', { subscriber_id: i % 20 < 10 ? 'Operator' : 'Dev One', content: 'Board message ' + (i + k) + ' about handler_' + ((i + k) % 37) + ': the retry budget is per call, **not** per run; see \`internal/server/x.go\` for the loop.' })));
            return true; })()`);
        if (Number(process.env.PERF_THROTTLE || 4) > 1) await Emulation.setCPUThrottlingRate({ rate: Number(process.env.PERF_THROTTLE || 4) });
        const STUB_ART = `(() => { const R = window.fetch; const items = Array.from({ length: ${ARTIFACTS} }, (_, i) => ({ id: String(i).padStart(2, '0').repeat(32).slice(0, 64), name: 'report-' + i + '.md', media_type: 'text/markdown', size: 2048 + i, task_id: 100 + i, task_title: 'Task ' + i, produced_by: 'agent-' + (i % 50), created_at: new Date(Date.UTC(2026, 9, 10, 10, 0, i)).toISOString(), uri: 'coral://artifacts/' + String(i).padStart(2, '0').repeat(32).slice(0, 64), available: true, inline: false }));
            window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin); if (/^\\/api\\/board\\/[^/]+\\/artifacts$/.test(url.pathname) && (o.method || 'GET') === 'GET') return Promise.resolve(new Response(JSON.stringify({ project: ${JSON.stringify(BOARD)}, artifacts: items, has_more: false, truncated: false }), { status: 200, headers: { 'content-type': 'application/json' } })); return R(u, o); }; return true; })()`;
        // ---- 1. first paint
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD + OBS + FIXTURE });
        let rows = []; await profStart();
        for (let r = 0; r < RUNS; r++) {
            const m0 = await metrics();
            await Page.navigate({ url: BASE }); await Page.loadEventFired();
            await ev(`window.__coralReady.startup('liveList')`);
            const fp = await ev(`(() => { const p = performance.getEntriesByName('first-contentful-paint')[0]; return { fcp: p ? Math.round(p.startTime) : null, list: window.__firstList }; })()`);
            const m1 = await metrics();
            rows.push({ wall: fp.list != null ? fp.list : 0, fcp: fp.fcp, lt: await ev(`window.__longtasks.length`), ...delta(m0, m1) });
            await install();
        }
        await profStop('firstPaint'); out.firstPaint = { ...stats(rows), fcpMs: median(rows.map(r => r.fcp).filter(Boolean)) };
        await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(STUB_ART);
        // ---- 2. agent switch
        rows = []; await profStart();
        for (let k = 1; k <= RUNS; k++) {
            await ev(`window.__sw0 = performance.now(); window.__ltBefore = window.__longtasks.length; window.__readyP = new Promise(r => window.addEventListener('coral:agent-view-ready', e => { if (e.detail.name === 'agent-${k}') r(performance.now()); }, { once: false })); window.selectLiveSession('agent-${k}', 'claude', 'sid-${k}', ''); true`);
            const m = await metrics();
            const t = await ev(`Promise.race([window.__readyP.then(t => t - window.__sw0), new Promise(r => setTimeout(() => r(-1), 8000))])`);
            await ev(`${framesJs(2)}`);
            const m2 = await metrics();
            rows.push({ wall: t, lt: await ev(`window.__longtasks.length - window.__ltBefore`), ...delta(m, m2) });
        }
        await profStop('agentSwitch'); out.agentSwitch = stats(rows);
        // ---- 3. open the board
        rows = []; await profStart();
        for (let r = 0; r < RUNS; r++) {
            await ev(`window.switchNavTab('agents'); true`); await ev(`${framesJs(3)}`);
            const m = await metrics(); await ev(`window.__ltBefore = window.__longtasks.length; window.__b0 = performance.now(); window.switchNavTab('board'); window.selectBoardProject(${JSON.stringify(BOARD)}); true`);
            await waitFor(`document.querySelectorAll('#mb-board .board-msg-left, #mb-board .board-msg-right').length > 5`, 'the board messages', 20000);
            await ev(`${framesJs(2)}`); const t = await ev(`performance.now() - window.__b0`); // to the first frames painted with the messages in
            const m2 = await metrics(); rows.push({ wall: t, lt: await ev(`window.__longtasks.length - window.__ltBefore`), ...delta(m, m2) });
            out.boardRowsDrawn = await ev(`document.querySelectorAll('#mb-board .board-msg-left, #mb-board .board-msg-right').length`);
        }
        await profStop('openBoard'); out.openBoard = stats(rows);
        // ---- 4. the artifacts tab
        rows = []; const allRows = []; await profStart();
        await ev(`window.switchNavTab('agents'); true`);
        for (let r = 0; r < RUNS; r++) {
            await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install(); await ev(`window.__coralReady.then(() => true)`); await ev(STUB_ART); // a fresh page each run: the list is drawn for the first time (a list shown before is redrawn whole)
            await ev(`window.selectLiveSession('agent-0', 'claude', 'sid-0', ''); window.switchAgenticTab('files', 'top'); true`);
            await ev(`${framesJs(3)}`);
            const m = await metrics(); await ev(`window.__ltBefore = window.__longtasks.length; window.__a0 = performance.now(); window.switchAgenticTab('team-artifacts', 'top'); true`);
            await waitFor(`document.querySelectorAll('#agentic-panel-team-artifacts tr.team-artifact-row').length > 10`, 'the artifact rows', 20000);
            await ev(`${framesJs(2)}`); const t = await ev(`performance.now() - window.__a0`); // to the first frames painted with rows in
            await waitFor(`document.querySelectorAll('#agentic-panel-team-artifacts tr.team-artifact-row').length === ${ARTIFACTS}`, 'every artifact row', 20000);
            allRows.push(await ev(`performance.now() - window.__a0`));
            const m2 = await metrics(); rows.push({ wall: t, lt: await ev(`window.__longtasks.length - window.__ltBefore`), ...delta(m, m2) });
            out.artifactRowsDrawn = await ev(`document.querySelectorAll('#agentic-panel-team-artifacts tr.team-artifact-row').length`);
        }
        await profStop('artifactsTab'); out.artifactsTab = { ...stats(rows), allRowsMs: median(allRows) };
        const json = JSON.stringify(out, null, 1); console.log(json); if (process.env.PERF_OUT) fs.writeFileSync(process.env.PERF_OUT, json);
    } catch (e) { console.error('FAIL', e && e.stack || e); process.exitCode = 1; }
    finally { await removeBoards(ev, [BOARD]); await client.close().catch(() => {}); }
    process.exit(process.exitCode || 0);
})();
