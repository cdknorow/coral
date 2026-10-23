// Unchanged terminal polls are 304s (#3439). A real terminal agent on the isolated test server:
//   1. an idle terminal's polls come back 304 with no body (seen on the wire), and the screen stays
//      right;
//   2. typing shows on the very next poll once the pane has printed it (a 200 with the new screen);
//   3. after that, idle again: 304s.
// The poll is run directly (capture.js refreshCapture) so each step is one poll.
//
// Drives Chrome over CDP (chrome-remote-interface); run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');

const BASE = require('./test_base').BASE;
const { removeBoards } = require('./helpers/board_cleanup');
const CDP_PORT = Number(process.env.CDP_PORT || 9222);

async function httpJson(method, path, body) {
    const r = await fetch(BASE + path, { method, headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
    const text = await r.text();
    let data = null; try { data = JSON.parse(text); } catch { data = text; }
    return { status: r.status, data };
}

(async () => {
    const client = await CDP({ port: CDP_PORT });
    const { Page, Runtime, Network } = client;
    const ev = async expr => {
        const r = await Runtime.evaluate({ expression: expr, awaitPromise: true, returnByValue: true });
        if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const waitFor = async (expr, what, ms = 10000) => {
        const end = Date.now() + ms;
        for (;;) {
            try { if (await ev(expr)) return; } catch { /* loading */ }
            if (Date.now() > end) throw new Error('timed out waiting for ' + what);
            await new Promise(r => setTimeout(r, 50));
        }
    };
    // What the wire saw for /poll: status and bytes.
    const wire = [];
    const byId = new Map();
    const other = []; // the live list and the board's tasks
    Network.responseReceived(({ requestId, response }) => {
        if (/\/poll(\?|$)/.test(response.url)) { const w = { status: response.status, bytes: null }; byId.set(requestId, w); wire.push(w); }
        else if (/\/api\/sessions\/live$|\/api\/board\/[^/]+\/tasks\?/.test(response.url)) other.push({ url: response.url.replace(/^https?:\/\/[^/]+/, ''), status: response.status });
    });
    Network.loadingFinished(({ requestId, encodedDataLength }) => { const w = byId.get(requestId); if (w) { w.bytes = encodedDataLength; byId.delete(requestId); } });
    const capture = `(async () => (await import((window.__coralStatic || '/static/') + 'capture.js')))()`;
    const poll = () => ev(`(async () => { const c = await ${capture}; await c.refreshCapture(); return { stats: c.capturePollStats(), text: document.getElementById('pane-capture').dataset.captureState + '|' + (document.getElementById('pane-capture')._lastCapture || '') }; })()`);
    let sid = null, sname = null;
    try {
        await Promise.all([Page.enable(), Runtime.enable(), Network.enable()]);
        const launch = await httpJson('POST', '/api/sessions/launch', { working_dir: require('node:os').tmpdir(), agent_type: 'terminal', display_name: 'Poll 304' });
        assert.equal(launch.status, 200, JSON.stringify(launch.data));
        sid = launch.data.session_id; sname = launch.data.session_name;
        await Page.navigate({ url: BASE + '/' });
        await Page.loadEventFired();
        await waitFor(`typeof window.selectLiveSession === 'function'`, 'the dashboard');
        await ev(`(() => { setLiveViewMode('terminal'); selectLiveSession(${JSON.stringify(sname)}, 'terminal', ${JSON.stringify(sid)}); return true; })()`);
        // The shell has printed its prompt and the screen stopped changing.
        const settled = async prefix => {
            let last = '';
            for (let i = 0, same = 0; same < 3; i++) {
                assert.ok(i < 60, 'the terminal settles');
                const p = await poll();
                same = p.text === last && p.text.startsWith(prefix) ? same + 1 : 0;
                last = p.text;
                await new Promise(r => setTimeout(r, 200)); // sleep-ok: spacing polls while the shell settles
            }
        };
        await settled('ok|');

        console.log('1. An idle terminal: 304s, no body, the screen unchanged');
        const before = await poll();
        wire.length = 0;
        for (let i = 0; i < 5; i++) await poll();
        const after = await poll();
        assert.ok(after.stats.notModified - before.stats.notModified >= 5, `idle polls are 304s: ${JSON.stringify({ before: before.stats, after: after.stats })}`);
        assert.equal(after.text, before.text, 'the screen stays as it was');
        for (let i = 0; i < 40 && wire.filter(w => w.status === 304 && w.bytes !== null).length < 5; i++) await new Promise(r => setTimeout(r, 50)); // sleep-ok: CDP network events trail the page
        const w304 = wire.filter(w => w.status === 304);
        assert.ok(w304.length >= 5, `the wire saw 304s: ${JSON.stringify(wire)}`);
        assert.ok(w304.every(w => w.bytes === null || w.bytes < 1024), `a 304 is headers only: ${JSON.stringify(w304)}`);
        console.log(`   ✓ ${w304.length} polls as 304 (${Math.max(0, ...w304.map(w => w.bytes || 0))} bytes at most on the wire), screen unchanged`);

        console.log('2. Typing shows on the next poll once the pane printed it');
        const send = await httpJson('POST', `/api/sessions/live/${encodeURIComponent(sname)}/send`, { command: 'echo POLL_$((6*7))', agent_type: 'terminal', session_id: sid });
        assert.equal(send.status, 200, JSON.stringify(send.data));
        // The server has it (asked without a validator, from outside the page).
        for (let i = 0; ; i++) {
            const s = await httpJson('GET', `/api/sessions/live/${encodeURIComponent(sname)}/poll?agent_type=terminal&session_id=${sid}`);
            if (JSON.stringify(s.data).includes('POLL_42')) break;
            assert.ok(i < 100, 'the pane prints it');
            await new Promise(r => setTimeout(r, 100)); // sleep-ok: waiting for the shell to run the command
        }
        const fullBefore = (await ev(`(async () => (await ${capture}).capturePollStats())()`)).full;
        const p = await poll();
        assert.ok(p.text.includes('POLL_42'), `one poll shows it: ${p.text.slice(-200)}`);
        assert.equal(p.stats.full, fullBefore + 1, 'as a full answer');
        console.log('   ✓ shown on the next poll (200)');

        console.log('3. Idle again: 304s');
        await settled(''); // the shell prints its prompt after the command's output (one more changed screen, slow on Windows, #6506): idle means the screen has stopped changing
        const b2 = await poll();
        await poll(); await poll();
        const a2 = await poll();
        assert.ok(a2.stats.notModified - b2.stats.notModified >= 3, `304s again: ${JSON.stringify({ b2: b2.stats, a2: a2.stats })}`);
        assert.ok(a2.text.includes('POLL_42'), 'the screen keeps the output');
        console.log('   ✓ 304s, output still shown');

        console.log('4. The live list and the board\'s tasks: 304s too, and a fresh copy each time');
        await httpJson('POST', '/api/board/poll-304-board/tasks', { title: 'a task', subscriber_id: 'Operator', created_by: 'Operator' });
        other.length = 0;
        const lists = await ev(`(async () => {
            const api = await import((window.__coralStatic || '/static/') + 'api.js');
            const tasks = await import((window.__coralStatic || '/static/') + 'tasks.js');
            const { state } = await import((window.__coralStatic || '/static/') + 'state.js');
            await api.loadLiveSessions();
            const n1 = state.liveSessions.length;
            state.liveSessions.forEach(s => { s.__mutated = true; }); // a caller changing what it got
            await api.loadLiveSessions();
            const fresh = state.liveSessions.every(s => !s.__mutated);
            await tasks.loadBoardTasks('poll-304-board');
            await tasks.loadBoardTasks('poll-304-board');
            return { n1, n2: state.liveSessions.length, fresh, tasks: (state.currentBoardTasks || []).length };
        })()`);
        for (let i = 0; i < 40 && other.length < 4; i++) await new Promise(r => setTimeout(r, 50)); // sleep-ok: CDP network events trail the page
        const live = other.filter(o => o.url === '/api/sessions/live').map(o => o.status);
        const bt = other.filter(o => /\/tasks\?/.test(o.url)).map(o => o.status);
        // The page loaded the list at start, so both loads here already carry its validator.
        assert.deepEqual(live, [304, 304], `the live list, unchanged since the page loaded it: 304s (${JSON.stringify(other)})`);
        assert.deepEqual(bt, [200, 304], `the board's tasks: then 304 (${JSON.stringify(other)})`);
        assert.equal(lists.n2, lists.n1, 'the same list after the 304');
        assert.equal(lists.fresh, true, 'a 304 hands back a fresh copy, not the objects a caller changed');
        assert.equal(lists.tasks, 1, 'the board\'s task is still shown');
        console.log(`   ✓ live list ${live.join(' → ')}, board tasks ${bt.join(' → ')}, fresh copies`);

        console.log('\nAll capture poll 304 checks passed');
    } finally {
        if (sname) await httpJson('POST', `/api/sessions/live/${encodeURIComponent(sname)}/kill`, { agent_type: 'terminal', session_id: sid }).catch(() => {});
        await removeBoards(ev, ['poll-304-board']);
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
