// Suites must not add fixed sleeps (#3118). A sleep long enough on a quiet machine fails at --shards 3 or
// under a CPU throttle; wait for the condition instead (helpers/wait.js: waitFor, waitStable, coralReady).
//
// Counts every fixed wait over 100 ms in tests/frontend: sleep(N)/pause(N)/wait(N)/delay(N) in Node, and
// `new Promise(r => setTimeout(r, N))` in Node or in page-evaluated strings. Each file may have at most
// its count in sleep_baseline.json. The baseline only goes down: a file below it fails too, until the
// baseline is lowered with `node sleep_hygiene.test.js --update`, so a migrated sleep can't quietly
// come back. A sleep that really is the thing under test (e.g. "nothing happens within 2 s") is allowed
// with a `// sleep-ok: <reason>` comment on its line.
const fs = require('node:fs');
const path = require('node:path');

const DIR = __dirname;
const BASELINE = path.join(DIR, 'sleep_baseline.json');
const MIN_MS = 100;
const PATTERNS = [
    /\b(?:sleep|pause|wait|delay)\(\s*(\d[\d_]*)\s*\)/g,
    /new Promise\(\s*\(?\s*(\w+)\s*\)?\s*=>\s*setTimeout\(\s*\1\s*,\s*(\d[\d_]*)\s*\)/g,
];

function count(src) {
    let n = 0;
    for (const line of src.split('\n')) {
        if (/sleep-ok:/.test(line)) continue;
        for (const re of PATTERNS) {
            for (const m of line.matchAll(re)) {
                const ms = Number(m[m.length - 1].replace(/_/g, ''));
                if (ms > MIN_MS) n++;
            }
        }
    }
    return n;
}

const files = fs.readdirSync(DIR).filter(f => /\.test\.m?js$/.test(f) || f === 'agent_view_harness.js').sort();
const counts = Object.fromEntries(files.map(f => [f, count(fs.readFileSync(path.join(DIR, f), 'utf8'))]).filter(([, n]) => n > 0));

if (process.argv.includes('--update')) {
    fs.writeFileSync(BASELINE, JSON.stringify(counts, null, 2) + '\n');
    console.log(`sleep_baseline.json: ${Object.keys(counts).length} files, ${Object.values(counts).reduce((a, b) => a + b, 0)} fixed sleeps`);
    process.exit(0);
}

const baseline = JSON.parse(fs.readFileSync(BASELINE, 'utf8'));
const problems = [];
for (const f of new Set([...Object.keys(counts), ...Object.keys(baseline)])) {
    const now = counts[f] || 0, allowed = baseline[f] || 0;
    if (now > allowed) problems.push(`${f}: ${now} fixed sleeps over ${MIN_MS}ms, baseline ${allowed}. Wait for the condition instead (helpers/wait.js), or mark a deliberate one with // sleep-ok: <reason>`);
    else if (now < allowed) problems.push(`${f}: ${now} fixed sleeps, baseline ${allowed}. Good. Lower the baseline: node sleep_hygiene.test.js --update`);
}
if (problems.length) { console.error(problems.join('\n')); process.exit(1); }
console.log(`sleep_hygiene.test.js: ok (${Object.values(counts).reduce((a, b) => a + b, 0)} fixed sleeps left in ${Object.keys(counts).length} files)`);
