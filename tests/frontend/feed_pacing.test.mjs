// feed_pacing.js: reconnect backoff with jitter, and which feed diffs change nothing the sidebar shows.
import assert from 'node:assert/strict';
import { feedRetryDelay, isStalenessDrift, DRIFT_RENDER_MAX_AGE_MS } from '../../coral-go/pkg/frontend/static/feed_pacing.js';

// Backoff: doubles from 2 s, caps at 15 s; jitter spreads each delay over 50-100% of it.
const lo = () => 0, hi = () => 0.999999;
const caps = [1, 2, 3, 4, 5, 6, 20].map(n => feedRetryDelay(n, hi));
assert.deepEqual(caps, [2000, 4000, 8000, 15000, 15000, 15000, 15000], 'exponential, capped');
assert.deepEqual([1, 2, 3, 4].map(n => feedRetryDelay(n, lo)), [1000, 2000, 4000, 7500], 'jitter floor is half');
assert.equal(feedRetryDelay(0, hi), 2000, 'no failures counts as the first retry');
const spread = new Set(Array.from({ length: 200 }, () => feedRetryDelay(3)));
assert.ok(spread.size > 50, 'real jitter varies the delay');
for (const d of spread) assert.ok(d >= 4000 && d <= 8000, `delay ${d} within 50-100% of the 8 s step`);

// Drift: only staleness_seconds moved.
const key = s => s.server + '/' + s.session_id;
const row = (id, extra = {}) => ({ server: 's1', session_id: id, name: 'n' + id, working: false, staleness_seconds: 10, commands: { clear: '/clear' }, ...extra });
const rows = [row('a'), row('b')];
assert.equal(isStalenessDrift(rows, [row('a', { staleness_seconds: 11.5 })], key), true, 'age only');
assert.equal(isStalenessDrift(rows, [row('a', { staleness_seconds: 11 }), row('b', { staleness_seconds: 12 })], key), true, 'several rows, age only');
assert.equal(isStalenessDrift(rows, [{ server: 's1', session_id: 'a', staleness_seconds: 99 }], key), true, 'a partial row with only the age');
assert.equal(isStalenessDrift(rows, [row('a', { working: true })], key), false, 'a status flip is a change');
assert.equal(isStalenessDrift(rows, [row('a', { staleness_seconds: 12, summary: 'x' })], key), false, 'a new field is a change');
assert.equal(isStalenessDrift(rows, [row('a', { commands: { clear: '/other' } })], key), false, 'a nested change is a change');
assert.equal(isStalenessDrift(rows, [row('c')], key), false, 'a new session is a change');
assert.equal(isStalenessDrift(rows, [row('a', { staleness_seconds: 12 }), row('c')], key), false, 'one real change among drifting rows');
assert.equal(isStalenessDrift(rows, [], key), false, 'nothing changed is not a drift to skip on (no diff)');
assert.equal(isStalenessDrift(rows, undefined, key), false);
assert.equal(isStalenessDrift(undefined, [row('a')], key), false, 'no rows yet');
assert.equal(DRIFT_RENDER_MAX_AGE_MS, 10000, 'the sidebar is re-rendered at least every 10 s');
console.log('feed_pacing.test.mjs: ok');
