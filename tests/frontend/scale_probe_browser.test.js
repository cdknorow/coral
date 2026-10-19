// The dashboard at 10, 50 and 150 agents (#5149): load, idle CPU, long tasks, what a feed update costs, and the heap after a simulated 30 minutes. Opt-in (it measures, it does not
// assert; perf_budgets_browser and heap_growth_browser hold the lines):
//   SCALE_PROBE=1 SCALE_OUT=/tmp/x.json [SCALE_SIZES=10,50,150] [SCALE_THROTTLE=4] [SCALE_SOAK_SECS=120] node run_cdp_suites.mjs --only scale_probe --shards 1 --retries 0
// Fixture per size: agents in teams of eight (the board of each team is its group), 15% working, 5% needing input, 2% stuck, each with a 60-message chat; the first agent is open.
// The soak replays 30 minutes of feed in SCALE_SOAK_SECS: one diff of three agents a second (1800 in all) and a full update every 30 s (60), reading the heap after a forced GC
// at 0, 10, 20 and 30 simulated minutes, with the DOM nodes and the event listeners.
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

const fixture = n => `(() => {
  const mock = window.__m; const teams = Math.ceil(${n} / 8);
  const msgs = k => Array.from({ length: k }, (_, i) => i % 2 ? { type: 'assistant', text: 'Step ' + i + ': I read the handler and found the lock taken twice.\\n\\n- fix the error path\\n- add a test', key: 'k' + i, timestamp: '2026-10-19T00:01:00Z' } : { type: 'user', content: 'Please fix test number ' + i, key: 'k' + i, timestamp: '2026-10-19T00:00:00Z' });
  window.__st = i => ({ working: i % 20 < 3, waiting_for_input: i % 20 === 7, waiting_kind: i % 20 === 7 ? 'permission' : null, stuck: i % 50 === 11 });
  const entry = mock.entry.bind(mock);
  mock.entry = a => { const i = Number(String(a.session_id).replace('sid-', '')); return { ...entry(a), ...window.__st(isNaN(i) ? 0 : i) }; };
  for (let i = 0; i < ${n}; i++) { const a = mock.add('agent-' + i, 'A' + i + '_', 0, { sleeping: false, board_project: 'team-' + (i % teams), session_id: 'sid-' + i }); a.msgs = msgs(60); }
})();`;
const OBS = `(() => { window.__firstList = null; window.addEventListener('coral:live-list-rendered', () => { if (window.__firstList === null) window.__firstList = Math.round(performance.now()); }); window.__longtasks = [];
  try { new PerformanceObserver(l => { for (const e of l.getEntries()) window.__longtasks.push({ s: Math.round(e.startTime), d: Math.round(e.duration) }); }).observe({ type: 'longtask', buffered: true }); } catch {}
  window.__mut = 0; document.addEventListener('DOMContentLoaded', () => new MutationObserver(r => { window.__mut += r.length; }).observe(document.body, { subtree: true, childList: true, attributes: true, characterData: true })); })();`;
const pct = (a, p) => { if (!a.length) return null; const s = [...a].sort((x, y) => x - y); return Math.round(s[Math.min(s.length - 1, Math.floor(p * s.length))] * 10) / 10; };

if (!process.env.SCALE_PROBE) { console.log('scale_probe: skipped (set SCALE_PROBE=1 to measure)'); process.exit(0); }
(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Performance, HeapProfiler } = client;
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a measurement window; the page is meant to be left alone
    const out = {};
    const metrics = async () => { const { metrics: m } = await Performance.getMetrics(); return Object.fromEntries(m.map(x => [x.name, x.value])); };
    const delta = (a, b) => ({ task: Math.round((b.TaskDuration - a.TaskDuration) * 1000), script: Math.round((b.ScriptDuration - a.ScriptDuration) * 1000), layout: Math.round((b.LayoutDuration - a.LayoutDuration) * 1000), style: Math.round((b.RecalcStyleDuration - a.RecalcStyleDuration) * 1000) });
    const heap = async () => { await HeapProfiler.collectGarbage(); await HeapProfiler.collectGarbage(); const m = await metrics(); return { heapMB: Math.round(m.JSHeapUsedSize / 104857.6) / 10, nodes: m.Nodes, listeners: m.JSEventListeners, documents: m.Documents }; };
    const { Profiler } = client; const PROF = !!process.env.SCALE_PROFILE;
    const profStart = async () => { if (!PROF) return; await Profiler.enable(); await Profiler.setSamplingInterval({ interval: 200 }); await Profiler.start(); };
    const profStop = async name => { if (!PROF) return; const { profile } = await Profiler.stop(); const byId = new Map(profile.nodes.map(n => [n.id, n])); const self = new Map(); const dt = profile.timeDeltas;
        profile.samples.forEach((id, i) => { const n = byId.get(id); const k = `${n.callFrame.functionName || '(anon)'} ${String(n.callFrame.url).split('/').pop()}:${n.callFrame.lineNumber + 1}`; self.set(k, (self.get(k) || 0) + (dt[i] || 0)); });
        const tot = [...self.values()].reduce((a, b) => a + b, 0); out.profile = out.profile || {}; out.profile[name] = [...self].sort((a, b) => b[1] - a[1]).slice(0, 18).map(([k, us]) => Math.round(us / 1000) + 'ms ' + Math.round(us / tot * 100) + '% ' + k); };
    const sizes = (process.env.SCALE_SIZES || '10,50,150').split(',').map(Number);
    const thr = Number(process.env.SCALE_THROTTLE || 1);
    await Page.enable(); await Performance.enable(); await HeapProfiler.enable();
    if (thr > 1) await client.Emulation.setCPUThrottlingRate({ rate: thr });
    await Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
    let preload = null;
    for (const n of sizes) {
        console.log('agents', n);
        const r = {};
        if (preload) await Page.removeScriptToEvaluateOnNewDocument({ identifier: preload });
        preload = (await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD + OBS + fixture(n) })).identifier;
        // 1. load
        await Page.navigate({ url: 'about:blank' }); await sleep(300); // sleep-ok: a measurement window; the page is meant to be left alone
        const m0 = await metrics(); const t0 = Date.now();
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await ev(`window.__coralReady.startup('liveList')`);
        const m1 = await metrics();
        const nav = await ev(`(() => { const nv = performance.getEntriesByType('navigation')[0]; return { dcl: Math.round(nv.domContentLoadedEventEnd), listAt: window.__firstList, longtasks: window.__longtasks.length, longMs: window.__longtasks.reduce((s, x) => s + x.d, 0), maxLong: Math.max(0, ...window.__longtasks.map(x => x.d)) }; })()`);
        await install();
        if (process.env.SCALE_CSS) await ev(`(() => { const st = document.createElement('style'); st.textContent = ${JSON.stringify(process.env.SCALE_CSS)}; document.head.appendChild(st); return true; })()`);
        await ev(`window.selectLiveSession('agent-0', 'claude', 'sid-0', 'local'); true`);
        await sleep(2500); // sleep-ok: the first view of the open agent settles
        r.load = { wallMs: Date.now() - t0, ...nav, cpu: delta(m0, m1), rows: await ev(`document.querySelectorAll('#live-sessions-list li[data-session-id]').length`), ...await heap() };
        // 2. idle: 15 s, nothing happening (the feed is quiet)
        await ev(`window.__mut = 0; window.__lt0 = window.__longtasks.length; true`);
        const mi0 = await metrics(); await sleep(15000); // sleep-ok: a measurement window; the page is meant to be left alone
        const mi1 = await metrics(); const d = delta(mi0, mi1);
        r.idle = { secs: 15, cpuMsPerSec: Math.round(d.task / 15), scriptMsPerSec: Math.round(d.script / 15), mutations: await ev(`window.__mut`), longtasks: await ev(`window.__longtasks.length - window.__lt0`) };
        // 3. feed updates
        await profStart();
        const one = []; const ltA = await ev(`window.__longtasks.length`); const mu0 = await metrics();
        for (let i = 0; i < 100; i++) {
            const ms = await ev(`(async () => { const T = window.T; const k = ${i} % ${n}; const a = window.__m.agents['sid-' + k]; const e = window.__m.entry(a); e.summary = 'step ${i}'; e.working = ${i} % 2 === 0; const t = performance.now(); T.ws.handleCoralMessage({ type: 'coral_diff', changed: [e], removed: [] }); const sync = performance.now() - t; await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))); return [sync, performance.now() - t]; })()`); // raf-ok: the probe measures the time to the second frame; it runs on a visible page
            one.push(ms);
            await sleep(40); // sleep-ok: the feed's own pace; each diff is measured on its own
        }
        const mu1 = await metrics(); await profStop('diffs_' + n); await profStart();
        const full = [];
        for (let i = 0; i < 10; i++) {
            const ms = await ev(`(async () => { const T = window.T; const t = performance.now(); T.ws.handleCoralMessage({ type: 'coral_update', servers: [], sessions: Object.values(window.__m.agents).map(a => ({ ...window.__m.entry(a), working: ${i} % 2 === 0 })) }); const sync = performance.now() - t; await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))); return [sync, performance.now() - t]; })()`); // raf-ok: the probe measures the time to the second frame; it runs on a visible page
            full.push(ms); await sleep(120); // sleep-ok: the feed's own pace
        }
        await profStop('fulls_' + n);
        r.update = { diff_sync_p50: pct(one.map(x => x[0]), .5), diff_sync_p95: pct(one.map(x => x[0]), .95), diff_frame_p50: pct(one.map(x => x[1]), .5), diff_frame_p95: pct(one.map(x => x[1]), .95), diffCpuMsEach: Math.round(delta(mu0, mu1).task / 100 * 10) / 10,
            full_sync_p50: pct(full.map(x => x[0]), .5), full_sync_max: pct(full.map(x => x[0]), 1), full_frame_p50: pct(full.map(x => x[1]), .5), full_frame_max: pct(full.map(x => x[1]), 1), longtasks: (await ev(`window.__longtasks.length`)) - ltA };
        // 4. soak: 30 simulated minutes
        if (process.env.SCALE_NO_SOAK) { out['agents_' + n] = r; console.log(JSON.stringify(r)); if (process.env.SCALE_OUT) fs.writeFileSync(process.env.SCALE_OUT, JSON.stringify(out, null, 1)); continue; }
        const secs = Number(process.env.SCALE_SOAK_SECS || 120); const diffs = 1800; const gap = Math.max(1, Math.floor(secs * 1000 / diffs));
        const soak = [{ simMin: 0, ...await heap() }]; const ms0 = await metrics(); const lt1 = await ev(`window.__longtasks.length`);
        for (let i = 1; i <= diffs; i++) {
            await ev(`(() => { const T = window.T; const ch = []; for (let j = 0; j < 3; j++) { const a = window.__m.agents['sid-' + ((${i} * 3 + j) % ${n})]; const e = window.__m.entry(a); e.summary = 'soak ${i}'; e.working = (${i} + j) % 3 === 0; e.staleness_seconds = ${i} % 120; ch.push(e); } T.ws.handleCoralMessage({ type: 'coral_diff', changed: ch, removed: [] }); if (${i} % 30 === 0) T.ws.handleCoralMessage({ type: 'coral_update', servers: [], sessions: Object.values(window.__m.agents).map(a => window.__m.entry(a)) }); return true; })()`);
            if (i % 600 === 0) soak.push({ simMin: i / 60, ...await heap() }); else await sleep(gap);
        }
        const ms1 = await metrics();
        r.soak = { realSecs: secs, samples: soak, cpuMs: delta(ms0, ms1).task, longtasks: (await ev(`window.__longtasks.length`)) - lt1 };
        out['agents_' + n] = r;
        console.log(JSON.stringify(r));
        if (process.env.SCALE_OUT) fs.writeFileSync(process.env.SCALE_OUT, JSON.stringify(out, null, 1));
    }
    await client.close();
})().catch(e => { console.error(e); process.exit(1); });
