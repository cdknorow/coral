// What the dashboard loads and runs at startup (#5364), opt-in: it measures, it does not assert (the lazy modules' own suites and bundle_startup_requests_browser hold the lines).
//   PERF_PROBE=1 PERF_THROTTLE=4 RUNS=5 PERF_OUT=/tmp/x.json node run_cdp_suites.mjs --only startup_weight_probe --shards 1 --retries 0
// Bundled (the default): the bytes of every script and stylesheet the page fetched (decoded and transferred), the request count, and per run the time to the first agent list drawn, the main thread's script
// and task time, and the compile time (a trace's v8.compile / v8.parseOnBackground events). With CORAL_NO_BUNDLE=1 (run it once more): per module, the bytes of the file and the share of it that ran by the time
// the page was ready (precise coverage), which is what finds a startup module that is mostly needed later.
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
if (!process.env.PERF_PROBE) { console.log('startup_weight_probe: skipped (set PERF_PROBE=1 to measure)'); process.exit(0); }
const settle = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: the page's own startup timers have no event to wait for
const median = a => { if (!a.length) return null; const s = [...a].sort((x, y) => x - y); return Math.round(((s[(s.length - 1) >> 1] + s[s.length >> 1]) / 2) * 10) / 10; };
(async () => {
    const { client, Page, Emulation, ev, PRELOAD } = await connect();
    const { Performance, Profiler, Tracing } = client;
    const RUNS = Number(process.env.RUNS || 5), out = { runs: RUNS, noBundle: process.env.CORAL_NO_BUNDLE === '1', throttle: Number(process.env.PERF_THROTTLE || 4) };
    try {
        await Page.enable(); await Performance.enable();
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        if (out.throttle > 1) await Emulation.setCPUThrottlingRate({ rate: out.throttle });
        const metrics = async () => Object.fromEntries((await Performance.getMetrics()).metrics.map(m => [m.name, m.value]));
        const rows = [];
        for (let r = 0; r < RUNS; r++) {
            let events = []; Tracing.dataCollected(({ value }) => { events.push(...value); });
            await Page.navigate({ url: 'about:blank' });
            await Tracing.start({ categories: 'devtools.timeline,v8,disabled-by-default-v8.compile,toplevel', transferMode: 'ReportEvents' });
            const t0 = Date.now();
            await Page.navigate({ url: BASE }); await Page.loadEventFired();
            await ev(`window.__coralReady.startup('liveList')`);
            const wall = Date.now() - t0;
            const m = await metrics();
            await new Promise(async res => { Tracing.tracingComplete(() => res()); await Tracing.end(); });
            const comp = events.filter(e => e.ph === 'X' && /^v8\.(compile|parseOnBackground)|^V8\.(CompileCode|Compile|ParseFunction|ScriptCompiler)/i.test(e.name) && e.dur).reduce((t, e) => t + e.dur, 0) / 1000;
            const res = await ev(`(() => { const r = performance.getEntriesByType('resource'); const sum = t => r.filter(e => t(e.name)).reduce((o, e) => ({ n: o.n + 1, decoded: o.decoded + (e.decodedBodySize || 0), transfer: o.transfer + (e.transferSize || 0) }), { n: 0, decoded: 0, transfer: 0 });
                const first = performance.getEntriesByType('paint').find(p => p.name === 'first-contentful-paint'); return { js: sum(n => /\\.js(\\?|$)/.test(n)), css: sum(n => /\\.css(\\?|$)/.test(n)), other: sum(n => !/\\.(js|css)(\\?|$)/.test(n)), fcp: first ? Math.round(first.startTime) : null, list: window.__firstList || null }; })()`);
            rows.push({ wall, fcp: res.fcp, task: m.TaskDuration * 1000, script: m.ScriptDuration * 1000, layout: m.LayoutDuration * 1000, style: m.RecalcStyleDuration * 1000, compileMs: comp, ...res });
            if (r === 0) out.firstRun = { js: res.js, css: res.css, other: res.other };
        }
        out.median = { wallMs: median(rows.map(r => r.wall)), fcpMs: median(rows.map(r => r.fcp).filter(Boolean)), taskMs: median(rows.map(r => r.task)), scriptMs: median(rows.map(r => r.script)), compileMs: median(rows.map(r => r.compileMs)), layoutMs: median(rows.map(r => r.layout)), styleMs: median(rows.map(r => r.style)) };
        out.bytes = { jsDecoded: out.firstRun.js.decoded, jsFiles: out.firstRun.js.n, cssDecoded: out.firstRun.css.decoded, cssFiles: out.firstRun.css.n, jsTransfer: out.firstRun.js.transfer, cssTransfer: out.firstRun.css.transfer };
        if (out.noBundle) { // per module: bytes and the share that ran by the time the page was ready
            await Emulation.setCPUThrottlingRate({ rate: 1 });
            await Page.navigate({ url: 'about:blank' });
            await Profiler.enable(); await Profiler.startPreciseCoverage({ callCount: false, detailed: false });
            await Page.navigate({ url: BASE }); await Page.loadEventFired(); await ev(`window.__coralReady.startup('liveList')`);
            await settle(1500);
            const { result } = await Profiler.takePreciseCoverage();
            const src = {};
            const mods = [];
            for (const s of result) { if (!/\/static\//.test(s.url) || !/\.js(\?|$)/.test(s.url)) continue;
                const { scriptSource } = await Profiler.constructor && { scriptSource: '' };
                mods.push({ url: s.url.replace(/^.*\/static\/(b\/[^/]+\/)?/, ''), fns: s.functions }); }
            out.coverageScripts = mods.length;
            out.coverage = result.filter(s => /\/static\//.test(s.url) && /\.js(\?|$)/.test(s.url)).map(s => {
                const top = s.functions.find(f => f.functionName === '') || s.functions[0];
                const total = top && top.ranges[0] ? top.ranges[0].endOffset : 0;
                // bytes NOT executed: the ranges with count 0 (nested ranges refine their parents; count the outermost uncovered ones)
                let unused = 0; const ranges = s.functions.flatMap(f => f.ranges).sort((a, b) => a.startOffset - b.startOffset || b.endOffset - a.endOffset);
                let end = -1; for (const g of ranges) { if (g.count === 0 && g.startOffset >= end) { unused += g.endOffset - g.startOffset; end = g.endOffset; } }
                return { file: s.url.replace(/^.*\/static\/(b\/[^/]+\/)?/, ''), bytes: total, unusedBytes: unused, usedPct: total ? Math.round((1 - unused / total) * 100) : null };
            }).filter(x => x.bytes > 0).sort((a, b) => b.unusedBytes - a.unusedBytes);
        }
        const json = JSON.stringify(out, null, 1); console.log(json.slice(0, 3000)); if (process.env.PERF_OUT) fs.writeFileSync(process.env.PERF_OUT, json);
    } catch (e) { console.error('FAIL', e && e.stack || e); process.exitCode = 1; }
    await client.close().catch(() => {}); process.exit(process.exitCode || 0);
})();
