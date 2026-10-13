// Agent list incremental DOM updates (#3121 step 2b, dom_morph.js). The list is
// rebuilt as one HTML string per render, but only changed nodes are replaced.
// Gate: after every step of a long random sequence of feed changes (status,
// summaries with markup characters, waiting/sleeping/archived, agents added,
// removed, moved between teams and servers, group-by-server toggled), the live
// DOM equals a fresh parse of that render's HTML. Also: unchanged rows keep
// their DOM nodes, a row is replaced only when it changed, and kept rows don't
// collect duplicate listeners.
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Runtime, ev, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);

        // Set up: modules, a seeded random generator, the fixture list.
        await ev(`(async () => {
            const st = window.__coralStatic || '/static/';
            const T = window.__mt = {
                ws: await import(st + 'websocket.js'), render: await import(st + 'render.js'),
                state: (await import(st + 'state.js')).state, morph: await import(st + 'dom_morph.js'),
            };
            if (T.state.coralWs) { T.state.coralWs.onclose = null; T.state.coralWs.onmessage = null; try { T.state.coralWs.close(); } catch {} }
            let seed = 3121; T.rnd = n => { seed = (seed * 1103515245 + 12345) & 0x7fffffff; return seed % n; };
            T.servers = ['local', 'm1', 'm2'];
            T.state.hub = true;
            T.serverList = T.servers.map(id => ({ id, label: id === 'local' ? 'Local' : 'Box ' + id, status: 'online' }));
            const teams = ['alpha', 'beta', 'gamma', 'delta', 'eps', 'zeta'];
            T.teams = teams;
            T.mk = i => ({ name: 'mo-' + i, session_id: 'mo-' + i, agent_type: i % 4 === 0 ? 'codex' : 'claude', server: T.servers[i % 3],
                status: 'working', summary: 'Task ' + i, display_name: 'Agent ' + i, working_directory: '/repo/' + teams[i % 6],
                board_project: i % 9 === 8 ? null : teams[i % 6], workflow_name: i % 13 === 0 ? 'nightly' : null,
                waiting_for_input: false, sleeping: false, archived: false, branch: 'main' });
            T.next = 60;
            T.ws.handleCoralMessage({ type: 'coral_update', servers: T.serverList, sessions: Array.from({ length: 60 }, (_, i) => T.mk(i)) });
            T.render.flushLiveSessionsRender();
            // What the DOM must equal: a fresh parse of the last render's markup. Late-filled
            // token usage is cleared on both sides (it is filled in asynchronously).
            T.check = () => {
                const list = document.getElementById('live-sessions-list');
                const fresh = document.createElement('ul');
                fresh.innerHTML = T.render.lastLiveListHtml();
                const norm = el => { const c = el.cloneNode(true); for (const t of c.querySelectorAll('.team-token-usage')) t.textContent = ''; for (const t of c.querySelectorAll('.session-tooltip')) t.removeAttribute('style'); return c.innerHTML; };
                return norm(list) === norm(fresh);
            };
            return true;
        })()`);
        assert.ok(await ev(`window.__mt.check()`), 'the first render equals a fresh parse');

        // 1. A long random sequence of feed changes: the DOM equals a fresh parse after every step.
        //    The same seeded sequence runs twice: morphing, then with the kill switch
        //    (localStorage 'coral-no-morph' = '1': plain innerHTML). Both must give the same DOM at
        //    every step, and the switched-off run must not touch the morph at all.
        await ev(`(() => {
            const T = window.__mt;
            T.hash = str => { let h = 2166136261; for (let k = 0; k < str.length; k++) { h ^= str.charCodeAt(k); h = Math.imul(h, 16777619); } return (h >>> 0).toString(16) + ':' + str.length; };
            T.runSeq = (noMorph) => {
                try { if (noMorph) localStorage.setItem('coral-no-morph', '1'); else localStorage.removeItem('coral-no-morph'); localStorage.removeItem('coral-group-by-server'); } catch {}
                let seed = 3121; T.rnd = n => { seed = (seed * 1103515245 + 12345) & 0x7fffffff; return seed % n; };
                T.next = 60;
                T.ws.handleCoralMessage({ type: 'coral_update', servers: T.serverList, sessions: Array.from({ length: 60 }, (_, i) => T.mk(i)) });
                T.render.flushLiveSessionsRender();
                const stats0 = { ...T.morph.morphStats };
                const log = [], hashes = [];
                const live = () => T.state.liveSessions.filter(s => s.session_id.startsWith('mo-'));
                const pick = () => { const l = live(); return l[T.rnd(l.length)]; };
                const key = s => ({ name: s.name, session_id: s.session_id, server: s.server });
                const marks = ['<b>bold</b> & "quotes" \\'single\\'', 'a < b > c', 'emoji 🚀 ✓', '', 'plain'];
                const list = document.getElementById('live-sessions-list');
                const norm = el => { const c = el.cloneNode(true); for (const t of c.querySelectorAll('.team-token-usage')) t.textContent = ''; for (const t of c.querySelectorAll('.session-tooltip')) t.removeAttribute('style'); return c.innerHTML; };
                let failedAt = null, failedOp = null;
                for (let step = 0; step < 80; step++) {
                    const op = T.rnd(11); const s = pick(); let msg;
                    if (op === 0) msg = { type: 'coral_diff', changed: [{ ...key(s), status: ['working', 'idle', 'waiting', 'done'][T.rnd(4)] }] };
                    else if (op === 1) msg = { type: 'coral_diff', changed: [{ ...key(s), summary: marks[T.rnd(marks.length)] + ' ' + step }] };
                    else if (op === 2) msg = { type: 'coral_diff', changed: [{ ...key(s), waiting_for_input: !s.waiting_for_input }] };
                    else if (op === 3) msg = { type: 'coral_diff', changed: [{ ...key(s), sleeping: !s.sleeping }] };
                    else if (op === 4) msg = { type: 'coral_diff', changed: [{ ...key(s), archived: !s.archived }] };
                    else if (op === 5) { const n = T.mk(T.next++); msg = { type: 'coral_diff', changed: [n] }; }
                    else if (op === 6) msg = { type: 'coral_diff', changed: [], removed: s.server === 'local' ? [s.session_id] : [], removed_remote: s.server === 'local' ? [] : [{ server: s.server, key: s.session_id }] };
                    else if (op === 7) msg = { type: 'coral_diff', changed: [{ ...key(s), board_project: T.teams[T.rnd(T.teams.length)] }] };
                    else if (op === 8) msg = { type: 'coral_diff', changed: [{ ...key(s), display_name: 'Renamed ' + step }] };
                    else if (op === 9) msg = { type: 'coral_diff', changed: Array.from({ length: 5 }, () => { const x = pick(); return { ...key(x), summary: 'batch ' + step }; }) };
                    else { try { localStorage.setItem('coral-group-by-server', T.rnd(2) ? '1' : '0'); } catch {} msg = { type: 'coral_diff', changed: [{ ...key(s), summary: 'regroup ' + step }] }; }
                    T.ws.handleCoralMessage(msg);
                    T.render.flushLiveSessionsRender();
                    if (failedAt === null && !T.check()) { failedAt = step; failedOp = op; }
                    hashes.push(T.hash(norm(list)));
                    log.push(op);
                }
                try { localStorage.removeItem('coral-group-by-server'); localStorage.removeItem('coral-no-morph'); } catch {}
                const d = k => T.morph.morphStats[k] - stats0[k];
                return { failedAt, failedOp, steps: log.length, ops: [...new Set(log)].sort((a, b) => a - b), hashes, stats: { full: d('full'), replaced: d('replaced'), kept: d('kept') } };
            };
            return true;
        })()`);
        const seq = await ev(`window.__mt.runSeq(false)`);
        assert.equal(seq.failedAt, null, `the DOM equals a fresh parse after every step (failed at step ${seq.failedAt}, op ${seq.failedOp})`);
        assert.deepEqual(seq.ops, [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10], 'every kind of change was exercised');
        assert.ok(seq.stats.kept > seq.stats.replaced, `unchanged nodes were kept (${JSON.stringify(seq.stats)})`);
        console.log(`   ✓ ${seq.steps} random feed changes: DOM == fresh parse after each (${JSON.stringify(seq.stats)})`);

        const off = await ev(`window.__mt.runSeq(true)`);
        assert.equal(off.failedAt, null, `kill switch: the DOM equals a fresh parse after every step (failed at step ${off.failedAt})`);
        assert.deepEqual(off.stats, { full: 0, replaced: 0, kept: 0 }, 'kill switch: the morph is not used at all');
        const firstDiff = seq.hashes.findIndex((h, k) => h !== off.hashes[k]);
        assert.equal(firstDiff, -1, `kill switch: the same DOM as the morphing run at every step (first difference at step ${firstDiff})`);
        console.log(`   ✓ kill switch (coral-no-morph=1): plain innerHTML path, identical DOM at all ${off.steps} steps`);

        // 2. Only the changed row is replaced; every other row keeps its node.
        const keep = await ev(`(() => {
            const T = window.__mt;
            T.ws.handleCoralMessage({ type: 'coral_update', servers: T.serverList, sessions: Array.from({ length: 60 }, (_, i) => T.mk(i)) });
            T.render.flushLiveSessionsRender();
            const rows = () => new Map([...document.querySelectorAll('#live-sessions-list .session-group-item[data-session-id]')].map(r => [r.dataset.sessionId, r]));
            const before = rows();
            T.ws.handleCoralMessage({ type: 'coral_diff', changed: [{ name: 'mo-5', session_id: 'mo-5', server: T.servers[5 % 3], summary: 'only me' }] });
            T.render.flushLiveSessionsRender();
            const after = rows();
            const changed = [...after].filter(([id, el]) => before.get(id) !== el).map(([id]) => id);
            return { rows: after.size, changed, updated: after.get('mo-5').textContent.includes('only me'), ok: T.check() };
        })()`);
        assert.ok(keep.rows >= 50, 'rows rendered: ' + keep.rows);
        // The changed row is either replaced or (same row element, new contents) patched inside;
        // no other row gets a new node.
        assert.deepEqual(keep.changed.filter(id => id !== 'mo-5'), [], 'no other row got a new node: ' + keep.changed);
        assert.ok(keep.updated, 'the changed row shows the new summary');
        assert.ok(keep.ok, 'and the DOM equals a fresh parse');
        console.log(`   ✓ one changed agent: ${keep.rows - 1} of ${keep.rows} rows kept their nodes (changed row ${keep.changed.length ? 'replaced' : 'patched inside'})`);

        // 3. Kept rows don't collect listeners across renders.
        await ev(`(() => { const T = window.__mt; for (let k = 0; k < 15; k++) { T.ws.handleCoralMessage({ type: 'coral_diff', changed: [{ name: 'mo-7', session_id: 'mo-7', server: T.servers[7 % 3], summary: 'tick ' + k }] }); T.render.flushLiveSessionsRender(); } return true; })()`);
        const { result } = await Runtime.evaluate({ expression: `document.querySelector('#live-sessions-list .session-group-item[data-session-id="mo-10"]')` });
        assert.ok(result.objectId, 'row mo-10 exists');
        const { listeners } = await client.DOMDebugger.getEventListeners({ objectId: result.objectId });
        const count = type => listeners.filter(l => l.type === type).length;
        assert.equal(count('mouseenter'), 1, `one tooltip mouseenter listener on a kept row (${count('mouseenter')})`);
        assert.ok(count('dragstart') <= 1, `at most one dragstart listener (${count('dragstart')})`);
        console.log('   ✓ kept rows keep exactly one set of listeners after 15 renders');

        // 4. The URL form of the kill switch: ?nomorph=1.
        await Page.navigate({ url: BASE + (BASE.includes('?') ? '&' : '?') + 'nomorph=1' });
        await Page.loadEventFired();
        await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        const url = await ev(`(async () => {
            const st = window.__coralStatic || '/static/';
            const morph = await import(st + 'dom_morph.js'); const ws = await import(st + 'websocket.js'); const render = await import(st + 'render.js');
            const b = { ...morph.morphStats };
            ws.handleCoralMessage({ type: 'coral_update', sessions: [{ name: 'u-1', session_id: 'u-1', agent_type: 'claude', server: 'local', status: 'working', summary: 'url switch' }] });
            render.flushLiveSessionsRender();
            return { disabled: morph.morphDisabled(), touched: morph.morphStats.full - b.full + morph.morphStats.replaced - b.replaced + morph.morphStats.kept - b.kept,
                shown: document.getElementById('live-sessions-list').textContent.includes('url switch') };
        })()`);
        assert.deepEqual(url, { disabled: true, touched: 0, shown: true }, '?nomorph=1 forces the plain path: ' + JSON.stringify(url));
        console.log('   ✓ ?nomorph=1 forces the plain innerHTML path');
        console.log('PASS agent_list_morph_browser');
    } finally {
        await client.close();
    }
})().catch(e => { console.error(e); process.exit(1); });
