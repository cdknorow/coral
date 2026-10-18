// The shared waits (helpers/wait.js) look again when a poll lands while the page is navigating (#4879, #4858): a stub ev that throws Chrome's navigation errors twice and then answers, for waitFor and
// waitStable; any other error still throws; the deadline still holds when the page never stops navigating; an expression that legitimately answers undefined is not taken for a navigation.
//   node waitfor_helper.test.js
'use strict';
const assert = require('node:assert/strict');
const { waitFor, waitStable, isNavigationError } = require('./helpers/wait');

(async () => {
    for (const m of ['Inspected target navigated or closed', 'Execution context was destroyed, most likely because of a navigation.', 'Cannot find context with specified id']) assert.equal(isNavigationError(new Error(m)), true, m);
    assert.equal(isNavigationError(new Error('page eval: {"exceptionDetails":1}')), false);
    assert.equal(isNavigationError(undefined), false);

    // waitFor: two refused polls, then the answer.
    let calls = 0;
    const flaky = async () => { calls++; if (calls <= 2) throw new Error('Inspected target navigated or closed'); return 'done'; };
    assert.equal(await waitFor(flaky, 'x', 'the page', { timeout: 2000 }), 'done'); assert.equal(calls, 3);

    // Another error is not swallowed.
    await assert.rejects(waitFor(async () => { throw new Error('page eval: boom'); }, 'x', 'a thing', { timeout: 500 }), /boom/);

    // A page that never stops navigating: the wait gives up at its own deadline (with the last real answer, none here), not hangs.
    const t0 = Date.now();
    await assert.rejects(waitFor(async () => { throw new Error('Execution context was destroyed'); }, 'x', 'a stuck page', { timeout: 300 }), /timed out after 300ms waiting for a stuck page/);
    assert.ok(Date.now() - t0 < 2000);

    // Soft: returns the last value instead of throwing.
    assert.equal(await waitFor(async () => { throw new Error('Cannot find context with specified id'); }, 'x', 'soft', { timeout: 100, soft: true }), undefined);

    // waitStable: refused polls, then a value that holds.
    calls = 0;
    const flakyStable = async () => { calls++; if (calls <= 2) throw new Error('Inspected target navigated or closed'); return { n: 1 }; };
    assert.deepEqual(await waitStable(flakyStable, 'x', 'a list', 60, { timeout: 2000 }), { n: 1 });
    await assert.rejects(waitStable(async () => { throw new Error('page eval: bad'); }, 'x', 'a list', 60, { timeout: 300 }), /bad/);
    await assert.rejects(waitStable(async () => { throw new Error('Execution context was destroyed'); }, 'x', 'a stuck list', 60, { timeout: 200 }), /timed out after 200ms waiting for a stuck list to settle/);
    // An expression that answers undefined is a value (stable), not a navigation.
    assert.equal(await waitStable(async () => undefined, 'x', 'nothing', 40, { timeout: 1000 }), null);
    console.log('waitfor_helper: ok');
})().catch(e => { console.error(e); process.exit(1); });
