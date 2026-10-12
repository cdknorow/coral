/* Pure pacing rules for the live feed: how long to wait before reconnecting, and when a feed update changes nothing
 * the sidebar shows. No DOM, no state: unit-tested on their own (tests/frontend/feed_pacing.test.js). */

const RETRY_BASE_MS = 2000;
const RETRY_MAX_MS = 15000;

/** Delay before reconnect attempt number `failures` (1 = the first retry): base * 2^(n-1), capped, then spread over
 *  50-100% of that so the tabs of one account do not all retry in the same second after a hub restart. */
export function feedRetryDelay(failures, rand = Math.random) {
    const cap = Math.min(RETRY_MAX_MS, RETRY_BASE_MS * 2 ** Math.max(0, failures - 1));
    return Math.round(cap * (0.5 + 0.5 * rand()));
}

// The longest the sidebar may go without a re-render when the only thing the feed reports is agents getting
// older: the rows' "last action Ns ago" text is built at render time, so this bounds how stale that text can be.
export const DRIFT_RENDER_MAX_AGE_MS = 10000;

const _same = (a, b) => a === b || JSON.stringify(a) === JSON.stringify(b);

/** True when every changed row already exists in `rows` and differs from it in `staleness_seconds` only, and the
 *  diff removes nothing. `keyOf` maps a row to its identity. Such a diff changes no state, only an age. */
export function isStalenessDrift(rows, changed, keyOf) {
    if (!Array.isArray(changed) || !changed.length) return false;
    const byKey = new Map();
    for (const r of rows || []) byKey.set(keyOf(r), r);
    for (const c of changed) {
        const old = byKey.get(keyOf(c));
        if (!old) return false;
        for (const k of Object.keys(c)) {
            if (k !== 'staleness_seconds' && !_same(c[k], old[k])) return false;
        }
    }
    return true;
}
