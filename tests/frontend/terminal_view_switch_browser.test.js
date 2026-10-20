// Chat -> Terminal -> Chat on an agent, over and over (#5396, from the teardown_stability find on the 131 gate and #5107): on a Linux runner about 1 round in 7 left the page's renderer answering nothing, running
// no script and showing no dialog, for 20 s, right after the switch back to Chat. This suite is that loop alone, with every page call raced against a step limit; when a step stalls it ends the browser's trace
// (started before the loop, with the main thread's work in it) and prints what the renderer was busy with: the longest tasks of its main thread by name and, for a function call, its script, so the next stall names
// its cause. ROUNDS=n (default 40), TERMINAL_STEP_MS=n (default 15000). A real agent is not needed: the mock agent the other suites use has no terminal, and the stall was seen with it.
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const THROTTLE = Number(process.env.THROTTLE || 1);
const ROUNDS = Number(process.env.ROUNDS || 40), STEP_MS = Number(process.env.TERMINAL_STEP_MS || 15000);
const wait = ms => new Promise(r => setTimeout(r, ms)); // a settle for the page's own debounces, not a condition

const logs = []; const say = (...a) => { const l = a.join(' '); logs.push(l); console.log(l); if (process.env.TV_OUT) fs.appendFileSync(process.env.TV_OUT, l + '\n'); };
(async () => {
    const { client, Page, Emulation, ev: rawEv, waitFor, PRELOAD, install } = await connect();
    const { Tracing, Profiler } = client;
    const slow = []; // [round, view, ms] of every switch that took over 500 ms
    let events = [], round = -1, last = 'start';
    Tracing.dataCollected(({ value }) => { events.push(...value); });
    const bound = (p, ms) => { let t; return Promise.race([p, new Promise((_, rej) => { t = setTimeout(() => rej(new Error('limit')), ms); })]).finally(() => clearTimeout(t)); };
    const analyse = async () => {
        try {
            await bound(new Promise(async res => { Tracing.tracingComplete(() => res()); await Tracing.end(); }), 20000);
        } catch (e) { return 'the trace did not end (' + e.message + '); the renderer or the browser is not answering at all'; }
        const mains = events.filter(e => e.name === 'thread_name' && e.args && e.args.name === 'CrRendererMain').map(e => `${e.pid}:${e.tid}`);
        const onMain = events.filter(e => e.ph === 'X' && mains.includes(`${e.pid}:${e.tid}`) && e.dur);
        const top = onMain.filter(e => !/RunTask$/.test(e.name)).sort((a, b) => b.dur - a.dur).slice(0, 14).map(e => `${Math.round(e.dur / 1000)} ms ${e.name}${e.args && e.args.data && (e.args.data.url || e.args.data.functionName) ? ' ' + (e.args.data.functionName || '') + ' ' + String(e.args.data.url || '').split('/').pop() + ':' + (e.args.data.lineNumber || '') + ':' + (e.args.data.columnNumber || '') : ''}`);
        const tasks = onMain.filter(e => /RunTask$/.test(e.name)).sort((a, b) => b.dur - a.dur).slice(0, 5).map(e => `${Math.round(e.dur / 1000)} ms task at ${Math.round((e.ts - (onMain[0] ? onMain[0].ts : 0)) / 1000)} ms`);
        const names = {}; for (const e of events) if (e.ph === 'X' && mains.includes(`${e.pid}:${e.tid}`)) names[e.name] = (names[e.name] || 0) + e.dur;
        return `main-thread events ${onMain.length}; longest tasks: ${tasks.join(', ') || 'none'}; longest events: ${top.join(' | ') || 'none'}; slow switches: ${slow.join(', ') || 'none'}; time by event: ${Object.entries(names).sort((a, b) => b[1] - a[1]).slice(0, 8).map(([k, v]) => Math.round(v / 1000) + 'ms ' + k).join(', ')}`;
    };
    const ev = (expression, ...rest) => {
        last = `round ${round}: ${String(expression).replace(/\s+/g, ' ').slice(0, 100)}`;
        let timer;
        const stalled = new Promise((_, reject) => { timer = setTimeout(async () => reject(new Error(`stalled ${STEP_MS} ms in ${last}; ${await analyse()}`)), STEP_MS); });
        return Promise.race([rawEv(expression, ...rest), stalled]).finally(() => clearTimeout(timer));
    };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await rawEv(`window.__coralReady.then(() => true)`);
        await rawEv(`(() => { for (let i = 0; i < 3; i++) window.__m.add('tv-' + i, String.fromCharCode(65 + i), 30, { sleeping: false }); T.render.renderLiveSessions(T.state.liveSessions); return true; })()`);
        if (THROTTLE > 1) await Emulation.setCPUThrottlingRate({ rate: THROTTLE });
        await Profiler.enable(); await Profiler.setSamplingInterval({ interval: 500 }); await Profiler.start();
        await Tracing.start({ categories: 'devtools.timeline,v8,disabled-by-default-devtools.timeline,toplevel,blink,cc', transferMode: 'ReportEvents' });
        for (let n = 0; n < ROUNDS; n++) {
            round = n;
            await ev(`(() => { const s = T.state.liveSessions[${n} % 3]; window.selectLiveSession(s.name, 'claude', s.session_id, ''); return true; })()`);
            for (const v of ['chat', 'terminal', 'chat']) { const t0 = Date.now(); await ev(`window.setLiveViewMode(${JSON.stringify(v)}); true`); const dt = Date.now() - t0; if (dt > 500) slow.push(`round ${n} ${v} ${dt} ms`); await wait(15); }
            if (n === 0 || n === 1) await rawEv(`(() => { const t = window.__coralTest.getTerminal(); if (!t || t.__wrapped) return false; t.__wrapped = true; window.__tt = window.__tt || []; for (const k of ['resize', 'refresh', 'clear', 'reset']) { const f = t[k].bind(t); t[k] = (...a) => { const s = performance.now(); const r = f(...a); window.__tt.push(k + ' ' + a.join(',') + ' ' + Math.round(performance.now() - s) + 'ms'); return r; }; } return true; })()`);
            const sizes = await ev(`(() => { const t = window.__coralTest && window.__coralTest.getTerminal ? window.__coralTest.getTerminal() : null; return t ? t.cols + 'x' + t.rows + ' scrollback ' + t.options.scrollback + ' calls ' + JSON.stringify((window.__tt || []).splice(0).slice(-6)) : 'no terminal'; })()`); say(`round ${n}: terminal ${sizes}`);
            // a terminal is never taller or wider than its window can show (xterm_renderer's fit caps it): the stall was a fit that grew it to 113,987 rows
            const dim = await ev(`(() => { const t = window.__coralTest.getTerminal(); return t ? { rows: t.rows, cols: t.cols, maxRows: Math.ceil(innerHeight / 6) + 2, maxCols: Math.ceil(innerWidth / 3) + 2 } : null; })()`);
            if (dim && (dim.rows > dim.maxRows || dim.cols > dim.maxCols)) throw new Error(`round ${n}: the terminal is ${dim.cols}x${dim.rows}, more than the window can show (${dim.maxCols}x${dim.maxRows})`);
        }
        const { profile } = await Profiler.stop();
        const byId = new Map(profile.nodes.map(x => [x.id, x])), self = new Map();
        profile.samples.forEach((id, i) => self.set(id, (self.get(id) || 0) + (profile.timeDeltas[i] || 0)));
        const agg = new Map(); for (const [id, us] of self) { const cf = byId.get(id).callFrame; const k = `${cf.functionName || '(anon)'} ${String(cf.url || '').split('/static/').pop()}:${cf.lineNumber}:${cf.columnNumber}`; agg.set(k, (agg.get(k) || 0) + us); }
        say('PROFILE top self time: ' + [...agg].filter(([k]) => !/^\((idle|program)/.test(k)).sort((a, b) => b[1] - a[1]).slice(0, 12).map(([k, us]) => Math.round(us / 1000) + 'ms ' + k).join(' | '));
        say(`slow switches: ${slow.join(', ') || 'none'}`);
        if (slow.length) throw new Error(`a switch took over 500 ms: ${slow.join(', ')}`);
        console.log(`terminal_view_switch: ok (${ROUNDS} rounds)`);
        await client.close(); process.exit(0);
    } catch (e) { if (process.env.TV_OUT) fs.appendFileSync(process.env.TV_OUT, 'FAIL ' + (e && e.message) + '\n'); console.error('FAIL', e && e.stack || e); await client.close().catch(() => {}); process.exit(1); }
})();
