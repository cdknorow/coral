// Nothing is left running when a view, panel or dialog closes (#5310). A long-lived dashboard opens and shuts the same things all day: Settings (every pane), the artifact viewer,
// the agent panel (switching agents and between chat and terminal), the agent list rebuilding as agents come and go, preview tabs (an artifact, files, a diff) and every dialog that
// opens without arguments. After a warm-up, 50 rounds of all of it may not grow what a missed teardown grows: live setInterval timers, long setTimeouts, open WebSockets,
// observers still observing (counted by wrapping the constructors in the page), and, after a forced GC, event listeners, DOM nodes, documents and the heap. Each is compared with
// its value after the warm-up. Measured when written (50 rounds): every count flat except the heap (+0.5 MB) and 8 listeners from a first use; the bounds below sit
// above that noise and well under what one missed removeEventListener per round (+50) or one stray interval per round (+50) adds.
// CYCLES=n runs more rounds; TEARDOWN_LEAK_SELFTEST=1 adds a deliberate leak (a listener on window, an interval, a ResizeObserver per round) the bounds must catch.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const HEX = 'a'.repeat(64);
const MD = '# Title\n\ntext [link](https://example.test)\n\n```js\nconst x = 1;\n```\n';
const PANES = ['general', 'agents', 'files', 'terminal', 'notifications', 'network', 'security', 'cleanup', 'updates'];
const DIALOGS = ['showLaunchModal', 'showCreateTaskModal', 'showDefaultPromptsModal', 'showServersModal', 'showOpenFolderModal', 'showTeamWizardModal', 'showJobModal', 'showAgentTaskDetailModal', 'showTaskDetailModal'];
const CYCLES = Number(process.env.CYCLES || 50), SELFTEST = !!process.env.TEARDOWN_LEAK_SELFTEST;
const BOUNDS = { listeners: 30, nodes: 60, documents: 0, intervals: 0, timeouts: 0, ws: 0, obs: 0, heapMB: 2 };
const wait = ms => new Promise(r => setTimeout(r, ms)); // a settle for the page's own debounces, not a condition

(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    const { HeapProfiler, Performance } = client;
    const S = `(window.__coralStatic || '/static/')`;
    try {
        await Page.enable(); await HeapProfiler.enable(); await Performance.enable();
        await client.Network.disable().catch(() => {}); // DevTools keeps every response body while its Network domain is on: the tool's memory, not the page's
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD + `;(() => {
            const T = window.__t = { intervals: new Set(), timeouts: new Set(), ws: new Set(), obs: new Set() };
            const si = window.setInterval, ci = window.clearInterval, st = window.setTimeout, ct = window.clearTimeout;
            window.setInterval = (f, ms, ...a) => { const id = si(f, ms, ...a); T.intervals.add(id); return id; };
            window.clearInterval = id => { T.intervals.delete(id); return ci(id); };
            window.setTimeout = (f, ms, ...a) => { const id = st(function () { T.timeouts.delete(id); return typeof f === 'function' ? f.apply(this, arguments) : undefined; }, ms, ...a); if (ms > 5000) T.timeouts.add(id); return id; };
            window.clearTimeout = id => { T.timeouts.delete(id); return ct(id); };
            for (const K of ['ResizeObserver', 'MutationObserver', 'IntersectionObserver']) {
                const O = window[K];
                window[K] = class extends O { constructor(...a) { super(...a); this.__live = 0; T.obs.add(this); }
                    observe(t, ...r) { this.__live++; return super.observe(t, ...r); } disconnect() { this.__live = 0; T.obs.delete(this); return super.disconnect(); } };
            }
            const WS = window.WebSocket;
            window.WebSocket = class extends WS { constructor(...a) { super(...a); T.ws.add(this); this.addEventListener('close', () => T.ws.delete(this)); } };
        })();` });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { for (let i = 0; i < 6; i++) window.__m.add('td-' + i, String.fromCharCode(65 + i), 50, { sleeping: false }); T.render.renderLiveSessions(T.state.liveSessions);
            const real = window.fetch; window.fetch = (u, o) => { const s = String(u && u.url || u); if (s.includes('/api/artifacts/${HEX}')) return Promise.resolve(new Response(${JSON.stringify(MD)}, { headers: { 'Content-Type': 'text/markdown' } })); return real(u, o); }; return true; })()`);
        const metrics = async () => {
            await HeapProfiler.collectGarbage(); await HeapProfiler.collectGarbage();
            const m = Object.fromEntries((await Performance.getMetrics()).metrics.map(x => [x.name, x.value]));
            const t = await ev(`(() => { const T = window.__t; return { intervals: T.intervals.size, timeouts: T.timeouts.size, ws: [...T.ws].filter(w => w.readyState < 2).length, obs: [...T.obs].filter(o => o.__live > 0).length }; })()`);
            return { heapMB: m.JSHeapUsedSize / 1048576, nodes: m.Nodes, listeners: m.JSEventListeners, documents: m.Documents, ...t };
        };
        const cycle = async n => {
            for (const p of PANES) { await ev(`window.showSettingsModal(${JSON.stringify(p)}); true`); await wait(10); }
            await ev(`window.hideSettingsModal(); true`);
            await ev(`import(${S} + 'artifact_viewer.js').then(m => m.openArtifactViewer(${JSON.stringify(HEX)}, { filename: 'a.md' })); true`); await wait(40);
            await ev(`document.querySelector('.artifact-viewer-close').click(); true`);
            await ev(`(() => { const s = T.state.liveSessions[${n} % 6]; window.selectLiveSession(s.name, 'claude', s.session_id, ''); return true; })()`);
            for (const v of ['chat', 'terminal', 'chat']) { await ev(`window.setLiveViewMode(${JSON.stringify(v)}); true`); await wait(15); }
            await ev(`(async () => { const m = await import(${S} + 'preview_pane.js');
                m.openArtifactTab('coral://artifacts/${HEX}', { filename: 'a.md' }); m.openPreviewTab('/tmp/x/readme.md', 1); m.openPreviewTab('/tmp/x/code.js', 3, { diff: true });
                await new Promise(r => setTimeout(r, 50)); m.closeAllTabs(); return true; })()`);
            await ev(`(async () => { for (const f of ${JSON.stringify(DIALOGS)}) {
                try { const p = window[f] && window[f](); if (p && p.then) await Promise.race([p, new Promise(r => setTimeout(r, 30))]); } catch { /* one that needs arguments */ }
                await new Promise(r => setTimeout(r, 10));
                for (let i = 0; i < 2; i++) document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
                document.querySelectorAll('.modal-overlay.active, .modal.active, dialog[open]').forEach(e => { if (e.close) e.close(); else e.classList.remove('active'); }); }
                return true; })()`);
            await ev(`(() => { T.state.liveSessions = []; window.__m.agents = {}; T.render.renderLiveSessions(T.state.liveSessions);
                for (let i = 0; i < 6; i++) window.__m.add('td-' + i, String.fromCharCode(65 + i), 50, { sleeping: false }); T.render.renderLiveSessions(T.state.liveSessions); return true; })()`);
            if (SELFTEST) await ev(`(() => { window.addEventListener('resize', () => {}); setInterval(() => {}, 60000); new ResizeObserver(() => {}).observe(document.body); return true; })()`);
        };
        for (let n = 0; n < 10; n++) await cycle(n);
        const base = await metrics();
        for (let n = 0; n < CYCLES; n++) await cycle(n);
        const end = await metrics();
        const grew = Object.fromEntries(Object.keys(BOUNDS).map(k => [k, +(end[k] - base[k]).toFixed(2)]));
        console.log('after the warm-up', JSON.stringify(base), '\nafter', CYCLES, 'more rounds', JSON.stringify(end), '\ngrew', JSON.stringify(grew));
        const over = Object.keys(BOUNDS).filter(k => grew[k] > BOUNDS[k]);
        if (SELFTEST) { assert.ok(over.includes('listeners') && over.includes('intervals') && over.includes('obs'), `the deliberate leak is caught (over: ${over})`); console.log('teardown_stability selftest: ok'); }
        else assert.deepEqual(over, [], `grew past its bound: ${over.map(k => `${k} +${grew[k]} (bound ${BOUNDS[k]})`).join(', ')}`);
        console.log('teardown_stability: ok');
        await client.close(); process.exit(0);
    } catch (e) { console.error('FAIL', e && e.stack || e); await client.close().catch(() => {}); process.exit(1); }
})();
