// overload.js (#3403): the shared pause on 429/502/503, and the lint that keeps every poller on it.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { retryAfterMs, pauseMs, noteResponse, isPaused, pausedForMs, overloadHooks, _resetOverloadForTests, countsAsOverload, overloadScope, machineOf, afterPause, isBusyStatus, isActionLimit } from '../../coral-go/pkg/frontend/static/overload.js';

const lo = () => 0, hi = () => 0.999999;

// Retry-After: seconds or an HTTP date; capped at 5 min; junk is 0.
const now = Date.parse('2026-10-14T12:00:00Z');
assert.equal(retryAfterMs('7', now), 7000);
assert.equal(retryAfterMs(' 0 ', now), 0);
assert.equal(retryAfterMs(new Date(now + 9000).toUTCString(), now), 9000, 'an HTTP date');
assert.equal(retryAfterMs(new Date(now - 9000).toUTCString(), now), 0, 'a date in the past');
assert.equal(retryAfterMs('100000', now), 300000, 'capped at 5 min');
assert.equal(retryAfterMs('soon', now), 0);
assert.equal(retryAfterMs(null, now), 0);

// Backoff: the first overload honours Retry-After (x1.0-1.25, #3430); without one, and from the
// second on, 2 s doubling to 30 s; a longer Retry-After wins; jitter x1.0-1.5.
assert.deepEqual([1, 2, 3, 4, 5, 6, 9].map(n => pauseMs(0, n, lo)), [2000, 4000, 8000, 16000, 30000, 30000, 30000], 'doubling, capped at 30 s');
assert.equal(pauseMs(1000, 1, lo), 1000, 'the first pause is the Retry-After the server gave (#3430)');
assert.equal(pauseMs(1000, 1, hi), 1250, 'spread up to x1.25');
assert.equal(pauseMs(1000, 2, lo), 4000, 'the second in a row doubles: at least 4 s');
assert.equal(pauseMs(45000, 1, lo), 45000, 'a longer Retry-After wins');
assert.equal(pauseMs(45000, 3, lo), 45000, 'also later on');
assert.equal(pauseMs(0, 1, hi), 3000, 'jitter top is x1.5');
const spread = new Set(Array.from({ length: 300 }, () => pauseMs(0, 3)));
assert.ok(spread.size > 50, 'real jitter varies it');
for (const d of spread) assert.ok(d >= 8000 && d <= 12000, `pause ${d} within x1.0-1.5 of 8 s`);

// Two tabs hit by the same overload come back at different times (jitter).
{
    const tabs = Array.from({ length: 40 }, () => pauseMs(5000, 1));
    assert.ok(new Set(tabs).size > 20, 'the tabs spread out');
    assert.ok(Math.max(...tabs) - Math.min(...tabs) > 600, 'over more than half a second');
    for (const d of tabs) assert.ok(d >= 5000 && d <= 6250, `${d} within x1.0-1.25 of Retry-After 5 s`);
}

// The shared state (v2): an overload pauses; NOTHING shortens a running pause; a success clears
// the failure count only once the pause has run out; other answers change nothing.
let t = now;
overloadHooks.now = () => t; overloadHooks.rand = lo;
_resetOverloadForTests();
noteResponse(200, null);
assert.equal(isPaused(), false);
noteResponse(503, '5');
assert.equal(pausedForMs(), 5000, 'Retry-After 5 s');
noteResponse(404, null);
assert.equal(pausedForMs(), 5000, 'a 404 between two 503s does not shorten it');
noteResponse(200, null);
assert.equal(pausedForMs(), 5000, 'nor does a 200 from a cheap call meanwhile');
noteResponse(503, null);
assert.equal(pausedForMs(), 5000, 'an answer to a request in flight does not shorten it');
noteResponse(503, '9');
assert.equal(pausedForMs(), 9000, 'nor double it: only a longer Retry-After among them extends it (#3430)');
t += 9001;
assert.equal(isPaused(), false, 'the window ends');
noteResponse(429, null);
assert.equal(pausedForMs(), 4000, 'the next overload after the pause is the 2nd in a row: 4 s (the answers during the pause and the 200 did not count or reset)');
t += 4001;
noteResponse(503, null);
assert.equal(pausedForMs(), 8000, 'still failing after that pause: 3rd in a row, 8 s');
t += 8001;
noteResponse(200, null);
noteResponse(503, null);
assert.equal(pausedForMs(), 2000, 'a 200 after the pause resets the doubling');
t += 2001;
noteResponse(500, null);
noteResponse(503, null);
assert.equal(pausedForMs(), 4000, 'a 500 is not a success: the count goes on');

// A page that sent several requests at once, all refused: one pause of the Retry-After, not the
// fifth step of the doubling (#3430: a first chat load waited up to 30 s).
_resetOverloadForTests();
for (let i = 0; i < 5; i++) noteResponse(503, '1');
assert.equal(pausedForMs(), 1000, 'five answers to one burst: Retry-After 1 s, once');
t += 1001;
noteResponse(200, null);
noteResponse(503, '1');
assert.equal(pausedForMs(), 1000, 'a success after the pause: the next overload is a first again');

// afterPause runs once the pause is over (at once with none); isBusyStatus (a 502 is not busy:
// a gateway or an unreachable machine, which a first load shows as an error with Retry).
assert.deepEqual([429, 502, 503, 500, 200].map(isBusyStatus), [true, false, true, false, false]);
{
    const realSetTimeout = globalThis.setTimeout;
    const delays = [];
    globalThis.setTimeout = (fn, ms) => { delays.push(ms); return 0; };
    _resetOverloadForTests();
    afterPause(() => {});
    noteResponse(503, '2');
    afterPause(() => {});
    noteResponse(503, '3', { machine: 'box-b' });
    afterPause(() => {}, 'box-b');
    afterPause(() => {}, 'local');
    globalThis.setTimeout = realSetTimeout;
    assert.deepEqual(delays, [50, 2050, 3050, 2050], 'no pause: next tick; Coral paused 2 s; that machine 3 s; local = Coral');
}

// Per machine (through a hub): a remote 429/503 pauses only that machine's pollers, unless the
// hub marks the answer as its own congestion; a remote 502 (machine unreachable) is not load.
assert.equal(machineOf('/api/remote/box-b/api/sessions/live'), 'box-b');
assert.equal(machineOf('/api/sessions/live'), '');
assert.equal(overloadScope('/api/sessions/live', 503), 'global');
assert.equal(overloadScope('/api/sessions/live', 502), 'global');
assert.equal(overloadScope('/api/remote/box-b/api/x', 503), 'machine');
assert.equal(overloadScope('/api/remote/box-b/api/x', 429), 'machine');
assert.equal(overloadScope('/api/remote/box-b/api/x', 502), '', 'one remote down');
assert.equal(overloadScope('/api/remote/box-b/api/x', 503, true), 'global', "the hub's own congestion");
assert.equal(overloadScope('/api/remote/box-b/api/x', 502, true), 'global');
assert.equal(overloadScope('/api/sessions/live', 500), '', 'a 500 is a bug, not load');
assert.equal(countsAsOverload('/api/remote/box-b/api/x', 502), false);
_resetOverloadForTests();
noteResponse(503, '3', { machine: 'box-b' });
assert.equal(isPaused('box-b'), true, 'that machine pauses');
assert.equal(isPaused(''), false, 'Coral itself does not');
assert.equal(isPaused('box-c'), false, 'nor another machine');
noteResponse(503, '3');
assert.equal(isPaused('box-c'), true, 'a global pause covers every machine');
_resetOverloadForTests();

// A 429 marked X-Coral-Limit: action is one action's own limit, not load (#3435); the hub's own
// congestion stays load whatever else the answer carries.
{
    const r = (status, headers) => new Response('{}', { status, headers });
    assert.equal(isActionLimit(r(429, { 'X-Coral-Limit': 'action', 'Retry-After': '30' })), true, 'a marked 429');
    assert.equal(isActionLimit(r(429, {})), false, 'an unmarked 429 is load');
    assert.equal(isActionLimit(r(503, { 'X-Coral-Limit': 'action' })), false, 'only a 429');
    assert.equal(isActionLimit(r(429, { 'X-Coral-Limit': 'other' })), false, 'only the action value');
    assert.equal(isActionLimit(r(429, { 'X-Coral-Limit': 'action', 'X-Coral-Hub-Overload': '1' })), false, "the hub's congestion wins");
    assert.equal(isActionLimit(null), false);
}

// Lint: no poller on a bare fixed interval. Every setInterval in static/ goes through
// pollEvery, or says on the line why it is not a poller (// interval-ok: …).
const staticDir = path.join(path.dirname(fileURLToPath(import.meta.url)), '../../coral-go/pkg/frontend/static');
const offenders = [];
for (const f of fs.readdirSync(staticDir)) {
    if (!f.endsWith('.js') || f === 'overload.js') continue;
    fs.readFileSync(path.join(staticDir, f), 'utf8').split('\n').forEach((line, i) => {
        if (/\bsetInterval\s*\(/.test(line) && !/interval-ok:/.test(line)) offenders.push(`${f}:${i + 1}: ${line.trim()}`);
    });
}
assert.deepEqual(offenders, [], 'use pollEvery (overload.js) for a poller, or mark the line // interval-ok: <why>');
console.log('overload: ok');
