// Team availability polling: one /status in flight per team, throttle measured
// from completion with latency backoff, hidden tabs skipped, change detection.
// Run: node team_availability_polling.test.mjs
import assert from 'node:assert/strict';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

installDom();
const realNow = Date.now;
let now = realNow();
Date.now = () => now;

let calls = 0;
let release = [];
let payload = { agents: [{ session_id: 's1', name: 'Dev', availability: 'available' }] };
let fail = false;
globalThis.fetch = window.fetch = (url) => {
    assert.match(String(url), /\/api\/board\/[^/]+\/status$/);
    calls++;
    const body = structuredClone(payload); // the server state when the request was made
    return new Promise(resolve => release.push(() => resolve(fail
        ? { ok: false, status: 503, json: async () => ({}) }
        : { ok: true, status: 200, json: async () => body })));
};
const settle = () => { const r = release; release = []; r.forEach(f => f()); return new Promise(res => setTimeout(res, 0)); };
let hidden = false;
Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden });

const { refreshTeamAvailability, refreshTeamAvailabilityIfChanged, getCachedAgentAvailability } = await importUi('team_availability');

// 1. Concurrent background polls share one request; only the starter gets data.
const first = refreshTeamAvailability('alpha');
const dupes = await Promise.all([1, 2, 3, 4].map(() => refreshTeamAvailability('alpha')));
assert.equal(calls, 1, 'duplicate polls must not start another /status');
assert.deepEqual(dupes, [null, null, null, null]);
now += 3000; // the request takes 3s
await settle();
assert.equal((await first).agents[0].name, 'Dev');

// 2. Throttle runs from completion, backed off to 2x latency (6s here, > 5s).
now += 5500;
assert.equal(await refreshTeamAvailability('alpha'), null);
assert.equal(calls, 1, 'still inside max(5s, 2x3s) after completion');
now += 600;
const second = refreshTeamAvailability('alpha');
assert.equal(calls, 2, 'poll allowed once the backoff elapses');
await settle(); await second;

// 3. Other teams are independent.
const beta = refreshTeamAvailability('beta');
assert.equal(calls, 3);
await settle(); await beta;

// 4. Hidden tabs serve the cache without fetching; force still fetches.
now += 60_000;
hidden = true;
assert.equal(await refreshTeamAvailability('alpha'), null);
assert.equal(calls, 3, 'hidden tab must not poll');
const forcedHidden = refreshTeamAvailability('alpha', true);
assert.equal(calls, 4, 'explicit refresh works while hidden');
await settle(); await forcedHidden;
hidden = false;

// 5. Change detection for the sidebar re-render.
now += 60_000;
let changed = refreshTeamAvailabilityIfChanged('alpha');
await settle();
assert.equal(await changed, false, 'same badges: no re-render');
now += 60_000;
payload = { agents: [{ session_id: 's1', name: 'Dev', availability: 'busy' }] };
changed = refreshTeamAvailabilityIfChanged('alpha');
await settle();
assert.equal(await changed, true, 'badge changed: re-render');
now += 60_000;
changed = refreshTeamAvailabilityIfChanged('alpha');
assert.equal(await refreshTeamAvailabilityIfChanged('alpha'), false, 'deduped caller reports no change');
await settle();
assert.equal(await changed, false);
const firstTeamGamma = refreshTeamAvailabilityIfChanged('gamma');
await settle();
assert.equal(await firstTeamGamma, true, 'first snapshot for a team renders');

// 6. force (team view Refresh) never joins a request in flight: that one may
// predate the change the user just made. Background polls still dedupe.
now += 60_000;
const base = calls;
const background = refreshTeamAvailability('alpha');
const fresh = refreshTeamAvailability('alpha', true);
assert.equal(calls, base + 2, 'force sends its own request');
assert.equal(await refreshTeamAvailability('alpha'), null);
assert.equal(calls, base + 2, 'background polls still skip while one is in flight');
await settle(); await background; await fresh;

// A slow background response landing after a newer forced one must not
// overwrite the cache (the sidebar would show the pre-change state).
now += 60_000;
payload = { agents: [{ session_id: 's1', name: 'Dev', availability: 'busy', reminder: true }] };
const slowBackground = refreshTeamAvailability('alpha');
const slowRelease = release; release = [];
payload = { agents: [{ session_id: 's1', name: 'Dev', availability: 'busy', reminder: false }] };
const newer = refreshTeamAvailability('alpha', true);
await settle(); await newer;          // forced (newer) response lands first
release = slowRelease; await settle(); // then the older background one
assert.equal((await slowBackground).agents[0].reminder, true, 'caller still gets its own response');
assert.equal(getCachedAgentAvailability({ session_id: 's1', name: 'Dev', board_project: 'alpha' }).reminder, false, 'older response must not overwrite newer cache');

// 7. Errors: background polls fall back to the cache and back off; force throws.
now += 60_000;
fail = true;
const failed = refreshTeamAvailability('alpha');
await settle();
assert.equal((await failed).agents[0].availability, 'busy', 'background poll serves cache on error');
const before = calls;
assert.equal(await refreshTeamAvailability('alpha'), null);
assert.equal(calls, before, 'a failed poll is throttled too');
const forcedFail = assert.rejects(refreshTeamAvailability('alpha', true), /503/);
await settle();
await forcedFail;

Date.now = realNow;
console.log('PASS team availability polling: in-flight dedupe, completion throttle + backoff, hidden tabs, change detection, errors');
process.exit(0);
