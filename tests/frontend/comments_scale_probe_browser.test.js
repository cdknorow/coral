// Artifact comments at the cap (#5255): 500 comments on a 1 MB Markdown file and on a 5,000-line code file, desktop and a 390 px phone. Opt-in (it measures, it does not assert;
// comments_scale_browser holds the line the fix draws):
//   COMMENTS_PROBE=1 COMMENTS_OUT=/tmp/x.json [COMMENTS_THROTTLE=4] node run_cdp_suites.mjs --only comments_scale_probe --shards 1 --retries 0
// Fixture: 400 threads each on a quote found once in the file, spread evenly through it, 100 replies, a fifth of the threads resolved. The comments routes are stubbed (the server's own
// cost is the store's and has its own tests); what is measured is the viewer: the time from openArtifactViewer to the document, the panel with its cards and the highlights, the long
// tasks, the click on a card, the filter, a posted comment, and the frames of a two-second scroll.
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { fixture, STUB } = require('./helpers/comments_scale');
const { framesJs } = require('./helpers/wait');
const BASE = require('./test_base').BASE;

if (!process.env.COMMENTS_PROBE) { console.log('comments_scale_probe: skipped (set COMMENTS_PROBE=1 to measure)'); process.exit(0); }
(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Performance, HeapProfiler } = client;
    const S = `(window.__coralStatic || '/static/')`;
    const thr = Number(process.env.COMMENTS_THROTTLE || 1);
    const out = {};
    const metrics = async () => { const { metrics: m } = await Performance.getMetrics(); return Object.fromEntries(m.map(x => [x.name, x.value])); };
    const heap = async () => { await HeapProfiler.collectGarbage(); await HeapProfiler.collectGarbage(); const m = await metrics(); return { heapMB: Math.round(m.JSHeapUsedSize / 104857.6) / 10, nodes: m.Nodes }; };
    await Page.enable(); await Performance.enable(); await HeapProfiler.enable();
    await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
    if (thr > 1) await client.Emulation.setCPUThrottlingRate({ rate: thr });
    const OBS = `(() => { window.__longtasks = []; try { new PerformanceObserver(l => { for (const e of l.getEntries()) window.__longtasks.push(Math.round(e.duration)); }).observe({ type: 'longtask', buffered: true }); } catch {} return true; })()`;
    for (const kind of ['markdown', 'code']) {
        const fx = fixture(kind);
        for (const view of [{ name: 'desktop', w: 1280, h: 800, mobile: false }, { name: 'phone', w: 390, h: 844, mobile: true }]) {
            const tag = `${kind}_${view.name}`;
            console.log(tag, `(${Math.round(fx.size / 1024)} KB, ${fx.comments.length} comments)`);
            await Emulation.setDeviceMetricsOverride({ width: view.w, height: view.h, deviceScaleFactor: 1, mobile: view.mobile });
            await Emulation.setTouchEmulationEnabled({ enabled: view.mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(OBS);
            await ev(STUB(fx));
            const r = {};
            const h0 = await heap(); const m0 = await metrics();
            // 1. open: to the document, to the panel's cards, to the highlights
            r.open = await ev(`(async () => {
                const v = await import(${S} + 'artifact_viewer.js'); const t0 = performance.now(); const at = {};
                v.openArtifactViewer(window.__HEX, { filename: ${JSON.stringify(fx.filename)} });
                await new Promise((res, rej) => { const end = t0 + 60000; const tick = () => {
                    const doc = document.querySelector('#artifact-viewer .artifact-viewer-body pre, #artifact-viewer .artifact-viewer-markdown'); if (doc && at.doc === undefined) at.doc = Math.round(performance.now() - t0);
                    const n = document.querySelectorAll('.ac-card').length; if (n && at.cards === undefined) at.cards = Math.round(performance.now() - t0);
                    const hl = CSS.highlights && CSS.highlights.get('ac-comment'); if (hl && hl.size && at.highlights === undefined) at.highlights = Math.round(performance.now() - t0);
                    if (at.doc !== undefined && at.cards !== undefined && at.highlights !== undefined) return res(); if (performance.now() > end) return rej(new Error('open timed out ' + JSON.stringify(at))); setTimeout(tick, 2); }; tick(); });
                await ${framesJs(2, 10000)};
                return { ...at, settled: Math.round(performance.now() - t0), cards: document.querySelectorAll('.ac-card').length, highlights: CSS.highlights.get('ac-comment').size, domNodes: document.getElementById('artifact-viewer').getElementsByTagName('*').length };
            })()`);
            r.open.longtasks = await ev(`window.__longtasks.slice()`);
            const m1 = await metrics();
            r.open.cpuMs = Math.round((m1.TaskDuration - m0.TaskDuration) * 1000); r.open.scriptMs = Math.round((m1.ScriptDuration - m0.ScriptDuration) * 1000);
            Object.assign(r.open, { heapMB: (await heap()).heapMB - h0.heapMB });
            // 2. a click on a card (the card and the highlight follow each other), on ten cards spread through the list
            r.click = await ev(`(async () => {
                const open = () => document.querySelector('#artifact-viewer'); const cs = [...document.querySelectorAll('.ac-card')]; const ms = [];
                for (let i = 0; i < 10; i++) { const c = cs[Math.floor(i * cs.length / 10)]; const t0 = performance.now(); c.click(); const sync = performance.now() - t0; await ${framesJs(2, 10000)}; ms.push({ sync: Math.round(sync), frame: Math.round(performance.now() - t0) }); }
                const f = ms.map(x => x.frame).sort((a, b) => a - b), s = ms.map(x => x.sync).sort((a, b) => a - b); return { syncMedian: s[5], syncMax: s[9], frameMedian: f[5], frameMax: f[9] };
            })()`);
            // 3. the Show resolved toggle: on, then off
            r.filter = await ev(`(async () => {
                const out = {}; for (const label of ['Show resolved', 'Hide resolved']) { const b = document.querySelector('.ac-showresolved'); const t0 = performance.now(); b.click(); const sync = performance.now() - t0; await ${framesJs(2, 10000)}; out[label] = { sync: Math.round(sync), frame: Math.round(performance.now() - t0), cards: document.querySelectorAll('.ac-card').length }; }
                return out; })()`);
            // 4. a posted comment, on the whole document: click, type, submit, until the card is there
            r.post = await ev(`(async () => {
                document.querySelector('.ac-whole').click(); const ta = document.querySelector('.ac-input'); ta.value = 'A probe comment on the whole document.'; ta.dispatchEvent(new Event('input'));
                const t0 = performance.now(); document.querySelector('.ac-composer').requestSubmit();
                await new Promise((res, rej) => { const end = t0 + 20000; const tick = () => { if ([...document.querySelectorAll('.ac-card .ac-body')].some(p => p.textContent === 'A probe comment on the whole document.')) return res(); if (performance.now() > end) return rej(new Error('post timed out')); setTimeout(tick, 2); }; tick(); });
                const shown = Math.round(performance.now() - t0); await ${framesJs(2, 10000)}; return { shown, frame: Math.round(performance.now() - t0) };
            })()`);
            // 4b. a selection in the middle of the document: from the selection to the Comment button being there, and the freeze on the way (the anchor is built from the text before and after it)
            r.select = await ev(`(async () => {
                const body = document.querySelector('#artifact-viewer .artifact-viewer-body'); const w = document.createTreeWalker(body, NodeFilter.SHOW_TEXT); const ns = []; for (let n = w.nextNode(); n; n = w.nextNode()) ns.push(n);
                const n = ns.slice(Math.floor(ns.length * 0.7)).find(x => x.nodeValue.trim().length >= 30); const range = document.createRange(); range.setStart(n, 0); range.setEnd(n, 30);
                const lt0 = window.__longtasks.length; const t0 = performance.now(); const sel = getSelection(); sel.removeAllRanges(); sel.addRange(range);
                await new Promise((res, rej) => { const end = t0 + 20000; const tick = () => { const b = document.querySelector('.ac-selbtn'); if (b && !b.hidden) return res(); if (performance.now() > end) return rej(new Error('no Comment button')); setTimeout(tick, 2); }; tick(); });
                const shown = Math.round(performance.now() - t0); sel.removeAllRanges(); return { shown, longtasks: window.__longtasks.slice(lt0) };
            })()`);
            // 5. a two-second scroll of the document
            r.scroll = await ev(`(async () => {
                const body = document.querySelector('#artifact-viewer .artifact-viewer-body'); const sc = [body, body.parentElement, document.querySelector('.artifact-viewer-content')].find(e => e && e.scrollHeight > e.clientHeight + 50) || body;
                const ds = []; let last = performance.now(); const end = last + 2000; sc.scrollTop = 0;
                await new Promise(res => { const step = (t) => { ds.push(t - last); last = t; sc.scrollTop += 60; if (t < end) requestAnimationFrame(step); else res(); }; requestAnimationFrame(step); }); // raf-ok: the frames of a scroll are what is measured (a visible page; this suite is opt-in)
                ds.shift(); const s = [...ds].sort((a, b) => a - b); return { frames: ds.length, p50: Math.round(s[Math.floor(s.length * 0.5)]), p95: Math.round(s[Math.floor(s.length * 0.95)]), max: Math.round(s[s.length - 1]), over50: ds.filter(d => d > 50).length, scrolled: Math.round(sc.scrollTop) };
            })()`);
            r.longtasksTotal = await ev(`window.__longtasks.length`); r.longMsTotal = await ev(`window.__longtasks.reduce((a, b) => a + b, 0)`);
            r.heap = await heap();
            out[tag] = r;
            console.log(JSON.stringify(r));
            await ev(`import(${S} + 'artifact_viewer.js').then(() => document.querySelector('#artifact-viewer .artifact-viewer-close').click()); true`);
        }
    }
    if (process.env.COMMENTS_OUT) fs.writeFileSync(process.env.COMMENTS_OUT, JSON.stringify(out, null, 1));
    console.log('comments_scale_probe: done');
    process.exit(0);
})().catch(e => { console.error(e); process.exit(1); });
