// Where a CDP run's time goes in fixed waits (#4988). An opt-in measuring tool, not part of any suite: it counts every setTimeout of 20 ms or more in a suite's
// Node process by call site, split into polls (inside waitFor-style helpers) and fixed waits, and for each the time the timers that FIRED asked for (a timeout that
// is cleared, such as a 60 s safety net, is not a wait and is not counted).
//
//   SLEEP_TALLY_DIR=/tmp/tally NODE_OPTIONS="--require $PWD/helpers/sleep_tally.js" node run_cdp_suites.mjs --only some_suites --shards 1
//   node helpers/sleep_tally.js --report /tmp/tally        # suites by fixed-wait seconds, then the biggest call sites
//
// A call site is the first two frames in tests/frontend that asked for the timer ("sleep" helper line < the caller's line when the async stack kept it).
'use strict';
const fs = require('node:fs');
const path = require('node:path');

if (require.main === module) {
    const dir = process.argv[3];
    if (process.argv[2] !== '--report' || !dir) { console.error('usage: node helpers/sleep_tally.js --report DIR'); process.exit(2); }
    const per = []; const sites = [];
    for (const f of fs.readdirSync(dir).filter(x => x.endsWith('.json'))) {
        const d = JSON.parse(fs.readFileSync(path.join(dir, f), 'utf8'));
        let fixed = 0, calls = 0, poll = 0;
        for (const [k, v] of Object.entries(d.sites)) {
            if (k.startsWith('fixed')) { fixed += v.firedMs || 0; calls += v.fired || 0; sites.push([v.firedMs || 0, v.fired || 0, d.suite, k.slice(6)]); } else poll += v.firedMs || 0;
        }
        per.push([fixed, calls, poll, d.suite]);
    }
    per.sort((a, b) => b[0] - a[0]); sites.sort((a, b) => b[0] - a[0]);
    console.log(`${per.length} suites, ${(per.reduce((n, p) => n + p[0], 0) / 1000).toFixed(0)} s of fixed waits that fired`);
    for (const [f, c, p, s] of per.slice(0, 30)) console.log(`${s.padEnd(50)} fixed ${(f / 1000).toFixed(1).padStart(6)} s (${String(c).padStart(4)} calls)  poll ${(p / 1000).toFixed(1).padStart(5)} s`);
    console.log('\nbiggest call sites');
    for (const [f, c, s, site] of sites.slice(0, 25)) console.log(`${(f / 1000).toFixed(1).padStart(6)} s ${String(c).padStart(4)}x ${s.padEnd(40)} ${site}`);
} else {
    const dir = process.env.SLEEP_TALLY_DIR;
    const main = path.basename(process.argv[1] || 'node');
    if (dir && /\.(test\.)?(js|mjs)$/.test(main) && !/run_cdp_suites|keep_page_visible/.test(main)) {
        const sites = {}; let lastKey = '';
        const orig = global.setTimeout;
        global.setTimeout = function (fn, ms, ...rest) {
            if (typeof ms === 'number' && ms >= 20 && ms < 120000) {
                const frames = new Error().stack.split('\n').slice(2, 12).map(s => s.trim());
                const poll = frames.some(f => /\b(waitFor|waitStable|pollUntil|pollEv|tick|until|settled|quiet)\b/.test(f));
                const mine = frames.filter(f => /tests\/frontend\//.test(f) && !/sleep_tally/.test(f)).map(f => (f.match(/tests\/frontend\/([^:)]+:\d+)/) || [])[1]).filter(Boolean);
                lastKey = (poll ? 'poll ' : 'fixed ') + (mine.slice(0, 2).join(' < ') || 'other');
                const s = sites[lastKey] || (sites[lastKey] = { n: 0, ms: 0 }); s.n++; s.ms += ms;
                if (typeof fn === 'function') {
                    const k = lastKey, f0 = fn;
                    fn = function (...a) { const t = sites[k]; t.fired = (t.fired || 0) + 1; t.firedMs = (t.firedMs || 0) + ms; return f0.apply(this, a); };
                }
            }
            return orig.call(this, fn, ms, ...rest);
        };
        process.on('exit', () => { try { fs.writeFileSync(path.join(dir, `${main}-${process.pid}.json`), JSON.stringify({ suite: main, sites })); } catch { /* best effort */ } });
    }
}
