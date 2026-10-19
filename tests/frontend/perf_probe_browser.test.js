// The dashboard's speed probe (#4985), opt-in: it measures, it does not assert (perf_budgets_browser holds the lines). Run it on an isolated server, at a CPU slowdown that
// stands for a slow machine, and compare two trees:
//   PERF_PROBE=1 PERF_THROTTLE=4 PERF_OUT=/tmp/x.json node run_cdp_suites.mjs --only perf_probe --shards 1 --retries 0
// Fixture: 40 agents in 5 teams (the first has a 5000-message chat of prompts, markdown replies with code, tool calls and results; the others 300), Chat view. It records, as JSON:
//   firstLoad (to the first agent list: wall, requests, long tasks, CPU), switch (20 agents: time to the settled view, CPU), longChat (open the 5000-message chat, load three older
//   pages), typing (150 keys in the composer with that chat open: event durations, CPU per key), feedBurst (60 diffs and 5 full updates against the list).
// Extra: PERF_PROFILE=1 adds a sampling profile per scenario (top self and inclusive functions; run with CORAL_NO_BUNDLE=1 for readable names), PERF_TRACE=1 the main thread's
// time by trace event (layout, paint...), PERF_MICRO=1 per-call timings of a few functions, PERF_UNDER=fn the callees of fn, PERF_CSS='...' injects a stylesheet to try an idea.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const { framesJs } = require('./helpers/wait');

const FIXTURE = `(() => {
  const mock = window.__m;
  const md = i => '## Step ' + i + '\\n\\nI looked at **' + i + '** files and found the cause in \\\`handler_' + i + '.go\\\`:\\n\\n- the lock is taken twice\\n- the error path leaks a goroutine\\n- the test only covers the happy path\\n\\n\\\`\\\`\\\`go\\nfunc handle' + i + '(ctx context.Context) error {\\n\\tmu.Lock()\\n\\tdefer mu.Unlock()\\n\\treturn run(ctx, ' + i + ')\\n}\\n\\\`\\\`\\\`\\n\\nNext I will fix it and run the tests.';
  const tool = (i, name) => ({ id: 'tu-' + i + '-' + name, name, input: name === 'Bash' ? { command: 'go test ./internal/server/... -run Test' + i + ' -count=1' } : { file_path: '/repo/internal/server/handler_' + i + '.go' } });
  const out = i => 'ok  \\tgithub.com/example/coral/internal/server\\t' + (i % 9 + 1) + '.2s\\n' + 'x'.repeat(300 + (i * 37) % 900);
  window.__richMsgs = n => { const m = []; let t = 0; for (let i = 0; m.length < n; i++) {
      m.push({ type: 'user', content: 'Please fix the failing test number ' + i + ' and explain what was wrong in handler_' + i + '.', key: 'k' + (t++), timestamp: '2026-10-19T00:00:' + String(i % 60).padStart(2, '0') + 'Z' });
      const uses = [tool(i, 'Read'), tool(i, 'Bash')];
      m.push({ type: 'assistant', text: md(i), tool_uses: uses, key: 'k' + (t++), timestamp: '2026-10-19T00:01:00Z' });
      for (const u of uses) m.push({ type: 'tool_result', tool_use_id: u.id, tool_name: u.name, content: out(i) });
      m.push({ type: 'assistant', text: 'Done with ' + i + ': the test passes now.', key: 'k' + (t++), timestamp: '2026-10-19T00:02:00Z' });
  } return m.slice(0, n); };
  const teams = ['alpha', 'beta', 'gamma', 'delta', 'epsilon'];
  for (let i = 0; i < 40; i++) { const a = mock.add('agent-' + i, 'A' + i + '_', 0, { sleeping: false, board_project: teams[i % 5], session_id: 'sid-' + i }); a.msgs = window.__richMsgs(i === 0 ? 5000 : 300); }
})();`;
const OBS = `(() => { window.__longtasks = []; try { new PerformanceObserver(l => { for (const e of l.getEntries()) window.__longtasks.push({ s: Math.round(e.startTime), d: Math.round(e.duration) }); }).observe({ type: 'longtask', buffered: true }); } catch {}
  window.__events = []; try { new PerformanceObserver(l => { for (const e of l.getEntries()) window.__events.push({ n: e.name, d: e.duration, p: e.processingEnd - e.processingStart }); }).observe({ type: 'event', durationThreshold: 16, buffered: true }); } catch {} })();`;
const pct = (a, p) => { if (!a.length) return null; const s = [...a].sort((x, y) => x - y); return Math.round(s[Math.min(s.length - 1, Math.floor(p * s.length))] * 10) / 10; };

if (!process.env.PERF_PROBE) { console.log('perf_probe: skipped (set PERF_PROBE=1 to measure)'); process.exit(0); }
(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Performance, Input } = client;
    const out = {};
    const { Tracing } = client; let traceEvents = [];
    Tracing.dataCollected(({ value }) => { traceEvents.push(...value); });
    const traceStart = async () => { if (!process.env.PERF_TRACE) return; traceEvents = []; await Tracing.start({ categories: 'devtools.timeline', transferMode: 'ReportEvents' }); };
    const traceStop = async name => { if (!process.env.PERF_TRACE) return; await new Promise(async r => { Tracing.tracingComplete(() => r()); await Tracing.end(); });
        const main = traceEvents.filter(e => e.name === 'thread_name' && e.args && e.args.name === 'CrRendererMain').map(e => e.tid); const sum = new Map();
        for (const e of traceEvents) if (e.ph === 'X' && main.includes(e.tid) && e.dur) sum.set(e.name, (sum.get(e.name) || 0) + e.dur);
        out.trace = out.trace || {}; out.trace[name] = [...sum].sort((a, b) => b[1] - a[1]).slice(0, 14).map(([k, us]) => Math.round(us / 1000) + 'ms ' + k); };
    const { Profiler } = client; const prof = process.env.PERF_PROFILE ? { n: 0 } : null;
    const profStart = async () => { if (!prof) return; await Profiler.enable(); await Profiler.setSamplingInterval({ interval: 250 }); await Profiler.start(); };
    const profStop = async name => { if (!prof) return; const { profile } = await Profiler.stop(); const byId = new Map(profile.nodes.map(n => [n.id, n])); const self = new Map(); const dt = profile.timeDeltas; profile.samples.forEach((id, i) => { self.set(id, (self.get(id) || 0) + (dt[i] || 0)); });
        const agg = new Map(); for (const [id, us] of self) { const n = byId.get(id); const cf = n.callFrame; const key = (cf.functionName || '(anon)') + ' ' + (cf.url || '').split('/static/').pop() + ':' + cf.lineNumber; agg.set(key, (agg.get(key) || 0) + us); }
        const parent = new Map(); for (const n of profile.nodes) for (const c of (n.children || [])) parent.set(c, n.id);
        const keyOf = n => (n.callFrame.functionName || '(anon)') + ' ' + (n.callFrame.url || '').split('/static/').pop() + ':' + n.callFrame.lineNumber;
        const children = new Map(); for (const n of profile.nodes) children.set(n.id, n.children || []);
        const under = {}; if (process.env.PERF_UNDER) { const target = process.env.PERF_UNDER; const sub = new Map(); const walk = (id, inside) => { const n = byId.get(id); const k = keyOf(n); const here = inside || k.startsWith(target + ' '); if (inside) sub.set(k, (sub.get(k) || 0) + (self.get(id) || 0)); for (const c of children.get(id)) walk(c, here); }; const root = profile.nodes[0].id; walk(root, false); under[target] = [...sub].sort((a, b) => b[1] - a[1]).slice(0, 8).map(([k, us]) => Math.round(us / 1000) + 'ms self ' + k); }
        const incl = new Map(), callers = new Map();
        for (const [id, us] of self) { const seen = new Set(); let cur = id; const leaf = keyOf(byId.get(id)); const par = parent.get(id) ? keyOf(byId.get(parent.get(id))) : '-';
            const ck = leaf + ' <- ' + par; callers.set(ck, (callers.get(ck) || 0) + us);
            while (cur) { const k = keyOf(byId.get(cur)); if (!seen.has(k)) { seen.add(k); incl.set(k, (incl.get(k) || 0) + us); } cur = parent.get(cur); } }
        const total = [...self.values()].reduce((a, b) => a + b, 0); out.profile = out.profile || {}; out.profile[name] = { under, incl: [...incl].filter(([k]) => !/^\(/.test(k)).sort((a, b) => b[1] - a[1]).slice(0, 14).map(([k, us]) => Math.round(us / 1000) + 'ms ' + k), callers: [...callers].filter(([k]) => /^(querySelector|querySelectorAll|getBoundingClientRect|closest|matches|setItem|getComputedStyle|offsetHeight|scrollHeight)/.test(k)).sort((a, b) => b[1] - a[1]).slice(0, 6).map(([k, us]) => Math.round(us / 1000) + 'ms ' + k), totalMs: Math.round(total / 1000), top: [...agg].sort((a, b) => b[1] - a[1]).slice(0, 12).map(([k, us]) => Math.round(us / 1000) + 'ms ' + k) }; };
    const metrics = async () => Object.fromEntries((await Performance.getMetrics()).metrics.map(m => [m.name, m.value]));
    const delta = (a, b) => ({ task: Math.round((b.TaskDuration - a.TaskDuration) * 1000), script: Math.round((b.ScriptDuration - a.ScriptDuration) * 1000), layout: Math.round((b.LayoutDuration - a.LayoutDuration) * 1000), style: Math.round((b.RecalcStyleDuration - a.RecalcStyleDuration) * 1000), nodes: b.Nodes, heapMB: Math.round(b.JSHeapUsedSize / 1e5) / 10 });
    try {
        await Page.enable(); await Performance.enable(); const thr = Number(process.env.PERF_THROTTLE || 1); if (thr > 1) await client.Emulation.setCPUThrottlingRate({ rate: thr });
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD + OBS + FIXTURE + (process.env.PERF_CSS ? `document.addEventListener('DOMContentLoaded', () => { const st = document.createElement('style'); st.textContent = ${JSON.stringify(process.env.PERF_CSS)}; document.head.appendChild(st); });` : '') + `;localStorage.setItem('coral-live-view-mode', 'chat');` });
        // 1. first load
        const m0 = await metrics(); const t0 = Date.now();
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await ev(`window.__coralReady.startup('liveList')`);
        const m1 = await metrics();
        const nav = await ev(`(() => { const n = performance.getEntriesByType('navigation')[0]; const r = performance.getEntriesByType('resource'); return { domContentLoaded: Math.round(n.domContentLoadedEventEnd), load: Math.round(n.loadEventEnd), requests: r.length, kb: Math.round(r.reduce((s, e) => s + (e.transferSize || 0), 0) / 1024), scripts: r.filter(e => e.initiatorType === 'script' || /\\.js/.test(e.name)).length, fcp: Math.round((performance.getEntriesByName('first-contentful-paint')[0] || {}).startTime || 0), longtasks: window.__longtasks.slice(0, 10), rows: document.querySelectorAll('#live-sessions-list .session-group-item').length }; })()`);
        out.firstLoad = { wallMs: Date.now() - t0, ...nav, cpu: delta(m0, m1) };
        out.domBreakdown = await ev(`(() => { const c = e => e.querySelectorAll('*').length + 1; const top = [...document.body.children].map(e => [(e.id || e.className || e.tagName).toString().slice(0, 40), c(e)]).sort((a, b) => b[1] - a[1]).slice(0, 8); const rows = document.querySelectorAll('#live-sessions-list .session-group-item'); return { top, rows: rows.length, nodesPerRow: rows.length ? Math.round([...rows].reduce((s, r) => s + c(r), 0) / rows.length) : 0, listNodes: c(document.getElementById('live-sessions-list')) }; })()`);
        await install();
        if (process.env.PERF_MICRO) { out.micro = await ev(`(async () => { const sr = await import((window.__coralStatic || '/static/') + 'session_rows.js'); const r = {};
            const time = (name, fn, n = 200) => { const t = performance.now(); for (let i = 0; i < n; i++) fn(); r[name] = Math.round((performance.now() - t) / n * 1000) / 1000; };
            time('syncDisplayMenu', () => sr.syncDisplayMenu()); time('applySessionSummaryPref', () => sr.applySessionSummaryPref());
            time('docQSAattr', () => document.querySelectorAll('[data-display-input]')); time('hdrQSAattr', () => { for (const h of document.getElementsByClassName('sidebar-header')) h.querySelectorAll('[data-display-input]'); });
            time('isSessionSummaryShown', () => sr.isSessionSummaryShown()); time('isServerShownOnRows', () => sr.isServerShownOnRows && sr.isServerShownOnRows()); time('isGroupedByServer', () => sr.isGroupedByServer && sr.isGroupedByServer());
            time('serversShown', () => sr.serversShown && sr.serversShown());
            const v = await import((window.__coralStatic || '/static/') + 'vendor_loader.js'); await v.loadHighlight(); const u = await import((window.__coralStatic || '/static/') + 'utils.js');
            const code = n => Array.from({ length: 30 }, (_, i) => '\\tif err := step' + i + '(ctx, "name", ' + n + '); err != nil { return fmt.Errorf("step ' + i + ': %w", err) }').join('\\n');
            const F = String.fromCharCode(96).repeat(3); let k = 0; time('renderMarkdown_codeHeavy_ms', () => u.renderMarkdown('Fix ' + (k++) + ':\\n\\n' + F + 'go\\nfunc run(ctx context.Context) error {\\n' + code(k) + '\\n}\\n' + F + '\\n\\nthen test it ' + Math.random(), undefined, 'chat'), 60);
            time('renderMarkdown_typical_ms', () => u.renderMarkdown('## Step ' + (k++) + '\\n\\nI looked at **files** and found x:\\n\\n- the lock is taken twice\\n- the error path leaks\\n\\n' + F + 'go\\nfunc h() error {\\n\\treturn run()\\n}\\n' + F + '\\n\\nNext ' + Math.random(), undefined, 'chat'), 60);
            return r; })()`); }
        // 2. switches
        await profStart();
        const sw = [];
        for (let k = 1; k <= 20; k++) {
            const name = 'agent-' + k;
            await ev(`window.__sw0 = performance.now(); window.__ltBefore = window.__longtasks.length; window.__readyP = new Promise(r => window.addEventListener('coral:agent-view-ready', e => { if (e.detail.name === ${JSON.stringify(name)}) r(performance.now()); }, { once: false })); window.selectLiveSession(${JSON.stringify(name)}, 'claude', 'sid-${k}', 'local'); true`);
            const m = await metrics();
            const t = await ev(`window.__readyP.then(t => Math.round((t - window.__sw0) * 10) / 10)`);
            await ev(`${framesJs(2)}`);
            const m2 = await metrics();
            sw.push({ ms: t, ...delta(m, m2), lt: await ev(`window.__longtasks.length - window.__ltBefore`) });
        }
        await profStop('switch');
        out.switch = { ms_p50: pct(sw.map(x => x.ms), .5), ms_p95: pct(sw.map(x => x.ms), .95), task_p50: pct(sw.map(x => x.task), .5), task_p95: pct(sw.map(x => x.task), .95), longtasks: sw.reduce((s, x) => s + x.lt, 0), n: sw.length };
        // 3. long chat (5000 messages)
        const mA = await metrics();
        await ev(`window.__sw0 = performance.now(); window.__readyP = new Promise(r => window.addEventListener('coral:agent-view-ready', e => { if (e.detail.name === 'agent-0') r(performance.now()); })); window.selectLiveSession('agent-0', 'claude', 'sid-0', 'local'); true`);
        const openMs = await ev(`window.__readyP.then(t => Math.round(t - window.__sw0))`);
        await ev(`${framesJs(2)}`);
        const mB = await metrics();
        const cdom = await ev(`({ bubbles: document.querySelectorAll('#live-history-messages .chat-bubble, #live-history-messages .tool-call').length, nodes: document.querySelectorAll('#live-history-messages *').length })`);
        // scroll to top 3 times (older pages)
        await profStart();
        const pages = [];
        for (let i = 0; i < 3; i++) {
            const before = await ev(`document.querySelectorAll('#live-history-messages .chat-bubble').length`);
            const mm = await metrics(); const t = Date.now();
            await ev(`(() => { const c = document.getElementById('live-history-messages'); c.scrollTop = 0; c.dispatchEvent(new Event('scroll')); return true; })()`);
            await waitFor(`document.querySelectorAll('#live-history-messages .chat-bubble').length > ${before}`, 'older page', 8000).catch(() => {});
            await ev(`${framesJs(2)}`);
            pages.push({ ms: Date.now() - t, ...delta(mm, await metrics()) });
        }
        await profStop('olderPages');
        out.longChat = { openMs, cpu: delta(mA, mB), ...cdom, olderPages: pages };
        // 4. typing with the long chat open
        await ev(`document.getElementById('command-input').focus(); window.__events.length = 0; true`);
        const mT0 = await metrics(); await profStart(); await traceStart();
        const text = 'Please also check the retry path and add a regression test for it, thanks. '.repeat(2);
        const t1 = Date.now();
        for (const ch of text) { await Input.dispatchKeyEvent({ type: 'keyDown', key: ch, text: ch }); await Input.dispatchKeyEvent({ type: 'keyUp', key: ch }); await new Promise(r => setTimeout(r, 15)); }
        await ev(`${framesJs(2)}`);
        const mT1 = await metrics(); await profStop('typing'); await traceStop('typing'); const evs = await ev(`window.__events`);
        out.typing = { chars: text.length, wallMs: Date.now() - t1, slowEvents: evs.length, eventDur_p95: pct(evs.map(e => e.d), .95), eventDur_max: pct(evs.map(e => e.d), 1), cpuPerKey: Math.round(((mT1.TaskDuration - mT0.TaskDuration) * 1000) / text.length * 10) / 10, scriptPerKey: Math.round(((mT1.ScriptDuration - mT0.ScriptDuration) * 1000) / text.length * 10) / 10, layoutPerKey: Math.round(((mT1.LayoutDuration - mT0.LayoutDuration) * 1000) / text.length * 10) / 10 };
        // 5. feed burst (the agent list, with the long chat open)
        const mF0 = await metrics(); await profStart(); await traceStart();
        const burst = await ev(`(async () => { const T = window.T; let renders = 0; window.addEventListener('coral:live-list-rendered', () => renders++); const t = performance.now();
            for (let i = 0; i < 60; i++) { const a = window.__m.agents['sid-' + (i % 40)]; a.sleeping = false; const e = window.__m.entry(a); e.working = i % 2 === 0; e.summary = 'step ' + i; e.staleness_seconds = i; T.ws.handleCoralMessage({ type: 'coral_diff', changed: [e], removed: [] }); await new Promise(r => setTimeout(r, 40)); }
            for (let i = 0; i < 5; i++) { T.ws.handleCoralMessage({ type: 'coral_update', servers: [], sessions: Object.values(window.__m.agents).map(a => ({ ...window.__m.entry(a), working: i % 2 === 0 })) }); await new Promise(r => setTimeout(r, 90)); }
            return { wallMs: Math.round(performance.now() - t), renders }; })()`);
        await ev(`${framesJs(2)}`); await profStop('feedBurst'); await traceStop('feedBurst');
        out.feedBurst = { ...burst, cpu: delta(mF0, await metrics()), longtasks: await ev(`window.__longtasks.length`) };
        if (process.env.PERF_OUT) fs.writeFileSync(process.env.PERF_OUT, JSON.stringify(out, null, 1));
        console.log('PERF ' + JSON.stringify(out));
    } finally { try { await client.close(); } catch (_) {} }
})().catch(e => { console.error(e); process.exit(1); });
