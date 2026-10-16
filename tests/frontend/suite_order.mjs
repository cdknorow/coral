// The order the CDP runner queues suites in (#3916). Shards take the next suite from one shared queue, so a long suite that comes late in
// an alphabetical queue starts last and the other shards sit idle while it finishes. Queuing the longest first (from the durations of a
// previous run, suite_durations.json) keeps every shard busy to the end. Suites without a recorded duration count as the median, so a
// new suite neither jumps the queue nor falls to the back; ties and a missing file keep alphabetical order.
import fs from 'node:fs';

/** The recorded durations ({ 'name.test.js': seconds }), or {} when the file is missing or unreadable. */
export function loadDurations(file) {
    try {
        const d = JSON.parse(fs.readFileSync(file, 'utf8'));
        return d && typeof d === 'object' && !Array.isArray(d) ? d : {};
    } catch { return {}; }
}

/** `tests` longest first by `durations`; the order of `tests` itself (alphabetical) for ties. */
export function orderLongestFirst(tests, durations) {
    const known = tests.map(t => durations[t]).filter(v => typeof v === 'number' && v >= 0).sort((a, b) => a - b);
    if (!known.length) return tests.slice();
    const median = known[Math.floor(known.length / 2)];
    const secs = t => (typeof durations[t] === 'number' && durations[t] >= 0 ? durations[t] : median);
    return tests.map((t, i) => ({ t, i })).sort((a, b) => secs(b.t) - secs(a.t) || a.i - b.i).map(x => x.t);
}

/** Durations from a runner results.json: each main-queue suite's first attempt, only when it passed, in seconds with one decimal. */
export function durationsFromResults(results) {
    const out = {};
    for (const r of Array.isArray(results) ? results : (results && results.results) || []) {
        const name = r && (r.test || r.name);
        const a = r && Array.isArray(r.attempts) && r.attempts.length ? r.attempts[0] : null;
        const secs = a ? Number(a.secs) : Number(r && r.secs);
        // The strict phase's runs ('strict: x') and the sequence group are not the main queue's entries.
        if (!name || /^(strict|sequence): /.test(String(name)) || (a ? a.status : r.status) !== 'pass' || !Number.isFinite(secs)) continue;
        out[String(name)] = Math.round(secs * 10) / 10;
    }
    return out;
}
