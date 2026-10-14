// The board task list poll (#3262). A large board's task list can take longer to fetch
// and draw than the 10 s poll period; the poll used to start a new fetch every period
// regardless, so slow fetches piled up. Now:
//   - a tick never starts while the previous one runs (at most one fetch in flight);
//   - a hidden tab does not poll, and catches up at once when shown;
//   - selecting another board loads it at once, even while the old board's fetch runs,
//     and the old board's late answer never replaces the new board's tasks.
// The poll period is shortened through tasks.js boardTaskPollHooks; /tasks is stubbed
// with a controllable delay.
//
// Expects CORAL_TEST_URL + CDP_PORT from tests/frontend/run.sh. Exit 0 / 1.

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const CDP_PORT = parseInt(process.env.CDP_PORT || '9222', 10);

const results = [];
function check(label, cond, detail) {
    const v = cond ? 'PASS' : 'FAIL';
    results.push(v);
    console.log(`[${v}] ${label}${detail !== undefined ? ' — ' + detail : ''}`);
}
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

// /api/board/<name>/tasks answers after window.__delay[name] ms with one task titled after the board.
const STUB = `
  window.__delay = {}; window.__hits = []; window.__live = 0; window.__maxLive = 0;
  (function(){ const Real = window.WebSocket; function Dead(u){ this.url=String(u); this.readyState=0; } Dead.prototype.send=function(){}; Dead.prototype.close=function(){}; Dead.prototype.addEventListener=function(){}; Dead.prototype.removeEventListener=function(){};
    function W(u,p){ if (/\\/ws\\//.test(String(u))) return new Dead(u); return p===undefined? new Real(u): new Real(u,p); } W.prototype=Real.prototype; W.CONNECTING=0; W.OPEN=1; W.CLOSING=2; W.CLOSED=3; window.WebSocket=W; })();
  window.__origFetch = window.fetch.bind(window);
  window.fetch = (url, opts) => { const path = String(url).replace(/^https?:\\/\\/[^/]+/, '').split('?')[0];
    const m = path.match(/^\\/api\\/board\\/([^/]+)\\/tasks$/);
    if (!m || ((opts && opts.method) || 'GET').toUpperCase() !== 'GET') return window.__origFetch(url, opts);
    const name = decodeURIComponent(m[1]);
    window.__hits.push({ name, at: performance.now() }); window.__live++; window.__maxLive = Math.max(window.__maxLive, window.__live);
    return new Promise(r => setTimeout(r, window.__delay[name] || 0)).then(() => { window.__live--;
      return new Response(JSON.stringify({ tasks: [{ id: 1, title: 'task of ' + name, status: 'pending', priority: 'medium', created_at: '2026-10-01T00:00:00Z' }] }), { status: 200, headers: { 'Content-Type': 'application/json' } }); }); };`;

async function run() {
    const client = await CDP({ port: CDP_PORT });
    const { Page, Runtime } = client;
    await Promise.all([Page.enable(), Runtime.enable()]);
    await Page.addScriptToEvaluateOnNewDocument({ source: STUB });
    const exceptions = [];
    Runtime.exceptionThrown(e => exceptions.push((((e.exceptionDetails || {}).exception || {}).description || 'exception').slice(0, 200)));
    const ev = async (x) => { const r = await Runtime.evaluate({ expression: x, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 300)); return r.result.value; };
    const { waitFor } = require('./helpers/wait').bind(ev);
    const MODS = `const tasks = await import((window.__coralStatic||'/static/')+'tasks.js'); const { state } = await import((window.__coralStatic||'/static/')+'state.js');`;
    // (Re)start the poll on `team`, like selecting it.
    const select = (team, period) => ev(`(async () => { ${MODS} tasks.stopBoardTaskPoll(); tasks.boardTaskPollHooks.intervalMs = ${period};
        state.currentSession = null; state.selectedTeam = ${JSON.stringify(team)}; tasks.startBoardTaskPoll(); return true; })()`);
    const stop = () => ev(`(async () => { ${MODS} tasks.stopBoardTaskPoll(); return true; })()`);
    const reset = () => ev(`window.__hits = []; window.__maxLive = window.__live; true`);
    const hits = name => ev(`window.__hits.filter(h => h.name === ${JSON.stringify(name)}).length`);
    const shownTasks = () => ev(`(async () => { ${MODS} return (state.currentBoardTasks || []).map(t => t.title); })()`);
    const setHidden = hidden => ev(`Object.defineProperty(document, 'hidden', { configurable: true, get: () => ${hidden} });
        Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => ${hidden ? "'hidden'" : "'visible'"} });
        document.dispatchEvent(new Event('visibilitychange')); true`);

    try {
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await waitFor(`typeof window._coralSetLiveSessions === 'function'`, 'the app hooks', { timeout: 10000 });
        await stop(); await waitFor(`window.__live === 0`, 'no board fetch in flight', { timeout: 3000 });

        // ── A fetch slower than the period: never two in flight ──
        await ev(`window.__delay = { slow: 700 }; true`); await reset();
        await select('slow', 100);
        await sleep(2500); // sleep-ok: counts the ticks a slow fetch allows in a fixed window
        const slowHits = await hits('slow'); const maxLive = await ev('window.__maxLive');
        check('slow fetch (700 ms) on a 100 ms period: at most one in flight', maxLive === 1, `max in flight ${maxLive}`);
        check('slow fetch: ticks run back to back, not every period (3-4 in 2.5 s, not 25)', slowHits >= 3 && slowHits <= 4, `${slowHits} fetches`);
        await stop(); await waitFor(`window.__live === 0`, 'no board fetch in flight', { timeout: 3000 });

        // ── A fast fetch keeps the period ──
        await ev(`window.__delay = {}; true`); await reset();
        await select('fast', 100);
        await sleep(1050); // sleep-ok: counts the ticks a fast fetch allows in a fixed window
        const fastHits = await hits('fast');
        check('fast fetch: polls every period as before', fastHits >= 8, `${fastHits} fetches in 1.05 s`);

        // ── Hidden tab: no polls; shown: one at once ──
        await setHidden(true); await waitFor(`window.__live === 0`, 'no board fetch in flight', { timeout: 3000 }); await reset();
        await sleep(1000); // sleep-ok: proving a negative: a hidden tab does not poll for 10 periods
        check('hidden tab: no polls', (await hits('fast')) === 0, `${await hits('fast')} fetches`);
        await reset();
        const shownAt = await ev('performance.now()');
        await setHidden(false);
        await waitFor(`window.__hits.length > 0`, 'a poll on show', { timeout: 1000, soft: true });
        const first = await ev(`window.__hits[0] ? window.__hits[0].at : null`);
        check('shown again: polls at once, not a period later', first !== null && first - shownAt < 60, first === null ? 'no poll' : `${Math.round(first - shownAt)} ms after show`);
        await stop(); await waitFor(`window.__live === 0`, 'no board fetch in flight', { timeout: 3000 });

        // ── Switching boards while the old board's fetch runs ──
        await ev(`window.__delay = { old: 1500, new: 50 }; true`); await reset();
        await select('old', 10000);
        await waitFor(`window.__live === 1`, 'the old board fetch in flight', { timeout: 1000 });
        const switchedAt = await ev('performance.now()');
        await select('new', 10000);
        await waitFor(`window.__hits.some(h => h.name === 'new')`, 'the new board fetch', { timeout: 1000, soft: true });
        const newHit = await ev(`(window.__hits.find(h => h.name === 'new') || {}).at || null`);
        check('another board loads at once, while the old board fetch still runs', newHit !== null && newHit - switchedAt < 100 && (await ev('window.__live')) >= 1, newHit === null ? 'no fetch' : `${Math.round(newHit - switchedAt)} ms`);
        await waitFor(`window.__live === 0`, 'both answers', { timeout: 3000 });
        await sleep(100);
        check('the old board\'s late answer does not replace the new board\'s tasks', JSON.stringify(await shownTasks()) === JSON.stringify(['task of new']), JSON.stringify(await shownTasks()));
        await stop();
        check('no page exceptions', exceptions.length === 0, JSON.stringify(exceptions));
    } finally {
        await client.close();
    }
}

run().then(() => {
    const failed = results.filter(r => r === 'FAIL').length;
    console.log(`\n${results.length - failed}/${results.length} checks passed`);
    process.exit(failed ? 1 : 0);
}).catch(err => { console.error(err); process.exit(1); });
