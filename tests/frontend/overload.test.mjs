// overload.js (#3403): the shared pause on 429/502/503, and the lint that keeps every poller on it.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { retryAfterMs, pauseMs, noteResponse, isPaused, pausedForMs, overloadHooks, _resetOverloadForTests, countsAsOverload, overloadScope, machineOf } from '../../coral-go/pkg/frontend/static/overload.js';

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

// Backoff: 2 s doubling to 30 s; a longer Retry-After wins; jitter x1.0-1.5.
assert.deepEqual([1, 2, 3, 4, 5, 6, 9].map(n => pauseMs(0, n, lo)), [2000, 4000, 8000, 16000, 30000, 30000, 30000], 'doubling, capped at 30 s');
assert.equal(pauseMs(1000, 1, lo), 2000, 'at least 2 s');
assert.equal(pauseMs(45000, 1, lo), 45000, 'a longer Retry-After wins');
assert.equal(pauseMs(0, 1, hi), 3000, 'jitter top is x1.5');
const spread = new Set(Array.from({ length: 300 }, () => pauseMs(0, 3)));
assert.ok(spread.size > 50, 'real jitter varies it');
for (const d of spread) assert.ok(d >= 8000 && d <= 12000, `pause ${d} within x1.0-1.5 of 8 s`);

// Two tabs hit by the same overload come back at different times (jitter).
{
    const tabs = Array.from({ length: 40 }, () => pauseMs(5000, 1));
    assert.ok(new Set(tabs).size > 20, 'the tabs spread out');
    assert.ok(Math.max(...tabs) - Math.min(...tabs) > 1000, 'over more than a second');
    for (const d of tabs) assert.ok(d >= 5000 && d <= 7500, `${d} within x1.0-1.5 of Retry-After 5 s`);
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
assert.equal(pausedForMs(), 5000, 'a shorter second pause does not shorten it either (2nd in a row: 4 s < 5 s)');
t += 5001;
assert.equal(isPaused(), false, 'the window ends');
noteResponse(429, null);
assert.equal(pausedForMs(), 8000, 'third overload in a row: 8 s (the 200 during the pause did not reset the count)');
t += 8001;
noteResponse(200, null);
noteResponse(503, null);
assert.equal(pausedForMs(), 2000, 'a 200 after the pause resets the doubling');
t += 2001;
noteResponse(500, null);
noteResponse(503, null);
assert.equal(pausedForMs(), 4000, 'a 500 is not a success: the count goes on');

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
