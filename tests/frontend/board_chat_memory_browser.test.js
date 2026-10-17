// #4318 (UX's #4276 audit): an idle agent page on a busy board grew its JS heap from 6.6 MB to
// 109 MB in 10 minutes, accelerating, with the DOM +1,600 nodes a minute. Two causes:
//   - morphHtml kept each top-level part's source as a slice of the markup it came from, and in V8
//     a slice keeps the whole string alive: a list that grows a node at a time (the board panel)
//     held one copy of its full markup per update. 40 MB of strings in 6 minutes.
//   - the board panel kept every message it ever received, and re-templated all of them for each
//     new one.
// Here: (A) morphing a list that grows by one node, 400 times, retains about the list itself after
// a GC, not hundreds of copies of it; (B) the board panel fed 1,200 messages, 20 per load, keeps a
// window of the newest (the rest behind "Load earlier", which counts them), and its heap and DOM
// stay bounded. The board's replies come from a stub of its /messages/all route.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, HeapProfiler, Performance } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const S = `(window.__coralStatic||'/static/')`;
    const heapMB = async () => {
        await HeapProfiler.collectGarbage(); await HeapProfiler.collectGarbage();
        const m = Object.fromEntries((await Performance.getMetrics()).metrics.map(x => [x.name, x.value]));
        return { heap: m.JSHeapUsedSize / 1048576, nodes: m.Nodes };
    };

    try {
        await Promise.all([Page.enable(), Runtime.enable(), HeapProfiler.enable(), Performance.enable()]);
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await coralReady();

        // ── (A) morphHtml on a list growing one node at a time ─────────────
        await ev(`(async () => {
            const morph = await import(${S} + 'dom_morph.js');
            const box = document.createElement('div'); box.id = 'morph-4318'; document.body.appendChild(box);
            const pad = 'x'.repeat(600);
            window.__grow = n => { let h = ''; for (let i = 0; i < n; i++) h += '<div class="m" data-i="' + i + '">message ' + i + ' ' + pad + '</div>'; morph.morphHtml(box, h); };
            return true;
        })()`);
        const a0 = await heapMB();
        await ev(`(() => { for (let n = 1; n <= 400; n++) window.__grow(n); return document.getElementById('morph-4318').children.length; })()`);
        const a1 = await heapMB();
        const retainedA = a1.heap - a0.heap;
        // The final list is 400 x ~640 B = 0.25 MB of markup (its nodes and parts a few times that);
        // the old slices kept every update's full markup: about 400 x 400 / 2 x 640 B = 50 MB.
        check('(A) a list morphed 400 times, one node more each time, retains a few MB at most after GC', retainedA < 8,
            `${retainedA.toFixed(1)} MB retained (${a0.heap.toFixed(1)} -> ${a1.heap.toFixed(1)} MB)`);
        await ev(`(async () => { const morph = await import(${S} + 'dom_morph.js'); const b = document.getElementById('morph-4318'); morph.resetMorph(b); b.remove(); delete window.__grow; return true; })()`);

        // ── (B) the board panel on a busy board ────────────────────────────
        const board = `mem-4318-${Date.now()}`;
        await ev(`(() => {
            // The board: total messages so far, each ~300 characters; replies as the Go route gives them.
            const board = ${JSON.stringify(board)};
            const st = window.__board4318 = { total: 0 };
            const msg = id => ({ id, project: board, subscriber_id: 'agent-' + (id % 7), job_title: 'Agent ' + (id % 7), session_id: '', content: 'Message ' + id + ': ' + 'the handler reads the cache, then writes it back. '.repeat(6), created_at: new Date(1800000000000 + id * 1000).toISOString() });
            const range = (lo, hi) => { const out = []; for (let id = lo; id <= hi; id++) out.push(msg(id)); return out; };
            const realFetch = window.fetch;
            window.fetch = (input, init) => {
                const url = String(typeof input === 'string' ? input : input.url);
                if (!url.includes('/api/board/' + board + '/')) return realFetch(input, init);
                const u = new URL(url, location.href), q = u.searchParams;
                let body = { messages: [], total: st.total };
                if (u.pathname.endsWith('/messages/all')) {
                    const limit = Number(q.get('limit') || 50);
                    if (q.has('after_id')) body.messages = range(Number(q.get('after_id')) + 1, Math.min(st.total, Number(q.get('after_id')) + limit));
                    else if (q.has('before_id')) { const hi = Number(q.get('before_id')) - 1, lo = Math.max(1, hi - limit + 1); body.messages = range(lo, hi); body.before_count = lo - 1; }
                    else { const lo = Math.max(1, st.total - limit + 1); body.messages = range(lo, st.total); body.before_count = lo - 1; }
                }
                return Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }));
            };
            const agentView = document.getElementById('live-session-view');
            if (agentView) agentView.style.display = '';
            if (!document.getElementById('agentic-panel-board')) { const p = document.createElement('div'); p.id = 'agentic-panel-board'; document.body.appendChild(p); }
            return true;
        })()`);
        const shown = `document.querySelectorAll('#board-panel-msgs .mb-message').length`;
        // Each showBoardChatTab of the same board runs one load at once (the panel is kept): 20 new messages per load.
        const feed = async loads => {
            for (let k = 0; k < loads; k++) {
                const want = await ev(`(async () => { const st = window.__board4318; st.total += 20; const m = await import(${S} + 'render.js'); m.showBoardChatTab(${JSON.stringify(board)}); return st.total; })()`);
                await waitFor(`(() => { const last = [...document.querySelectorAll('#board-panel-msgs .mb-message-body')].at(-1); return !!last && last.textContent.startsWith('Message ' + window.__board4318.total + ':'); })()`, `message ${want} shown`, { timeout: 5000, soft: true });
            }
        };
        await feed(3); // 60 messages: the first page and two loads after it
        const b0 = await heapMB();
        await feed(57); // 1,200 in all
        const b1 = await heapMB();
        const n = await ev(shown);
        const total = await ev(`window.__board4318.total`);
        check('(B) the panel received every message up to the newest', total === 1200 && (await ev(`[...document.querySelectorAll('#board-panel-msgs .mb-message-body')].at(-1)?.textContent.startsWith('Message 1200:')`)), String(total));
        check('(B) it keeps a window of the newest messages, not all 1,200', n >= 300 && n <= 350, `${n} shown`);
        const earlier = await ev(`document.querySelector('#board-panel-msgs [data-on-click="board-chat-earlier"]')?.textContent.trim() || ''`);
        check('(B) "Load earlier" counts the rest', earlier.includes(`(${1200 - n} remaining)`), earlier);
        check('(B) its DOM stays bounded while 1,140 more messages arrive', b1.nodes - b0.nodes < 8000, `${b0.nodes} -> ${b1.nodes} nodes`);
        check('(B) and so does its heap', b1.heap - b0.heap < 10, `${b0.heap.toFixed(1)} -> ${b1.heap.toFixed(1)} MB`);

        // Scrolled up, the reader's messages are not cut from under them.
        // (A hidden panel has no scroll position and counts as at the bottom: shown here, on top of the page.)
        const scrolls = await ev(`(() => { const p = document.getElementById('agentic-panel-board'); document.body.appendChild(p);
            p.style.cssText = 'display:block;position:fixed;left:0;top:0;width:420px;height:320px;z-index:99999;background:#fff';
            const el = document.getElementById('board-panel-msgs'); el.style.height = '200px'; el.style.overflow = 'auto'; el.scrollTop = 0;
            return el.scrollHeight > el.clientHeight; })()`);
        check('(B) the shown panel scrolls', scrolls);
        // Fed by the panel's own poll (every 3 s): showing the tab again would scroll it to the bottom.
        const before = await ev(shown);
        for (let k = 0; k < 3; k++) {
            await ev(`(window.__board4318.total += 20, true)`);
            await waitFor(`(() => { const last = [...document.querySelectorAll('#board-panel-msgs .mb-message-body')].at(-1); return !!last && last.textContent.startsWith('Message ' + window.__board4318.total + ':'); })()`, 'the poll brings them', { timeout: 10000, soft: true });        }
        const after = await ev(shown);
        const top = await ev(`document.getElementById('board-panel-msgs').scrollTop`);
        check('(B) scrolled up, new messages are added and none is dropped', after === before + 60 && top < 50, `${before} -> ${after}, scrollTop ${top}`);

        // "Load earlier" brings older ones back.
        await ev(`document.querySelector('#board-panel-msgs [data-on-click="board-chat-earlier"]').click()`);
        await waitFor(`${shown} === ${after + 50}`, 'an earlier page', { timeout: 5000, soft: true });
        check('(B) "Load earlier" brings back a page of the older messages', (await ev(shown)) === after + 50, String(await ev(shown)));
    } catch (e) {
        check('no exception', false, e.message);
    } finally {
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed} passed, ${failed} failed`);
    process.exit(failed ? 1 : 0);
})();
