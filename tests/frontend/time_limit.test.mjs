// time_limit.js: the sentences and the seconds for the New job and New workflow forms' Time limit.
import assert from 'node:assert/strict';
import { limitSeconds, limitParts } from '../../coral-go/pkg/frontend/static/time_limit.js';

assert.deepEqual(limitSeconds('1', 3600), { seconds: 3600 });
assert.deepEqual(limitSeconds(' 90 ', 60), { seconds: 5400 });
assert.deepEqual(limitSeconds('60', 1), { seconds: 60 });
assert.match(limitSeconds('', 3600).error, /as a number/);
assert.match(limitSeconds('abc', 60).error, /as a number/);
assert.match(limitSeconds('0', 3600).error, /whole number of one or more/);
assert.match(limitSeconds('1.5', 3600).error, /whole number of one or more/);
assert.match(limitSeconds('-2', 60).error, /whole number of one or more/);
assert.match(limitSeconds('30', 1).error, /at least 1 minute/);
assert.match(limitSeconds('25', 3600, 86400).error, /at most 24 hours/);
assert.deepEqual(limitSeconds('24', 3600, 86400), { seconds: 86400 });
assert.deepEqual(limitSeconds('99', 3600), { seconds: 356400 }, 'no max, no ceiling');
assert.deepEqual(limitParts(3600), { n: 1, unit: 3600 });
assert.deepEqual(limitParts(5400), { n: 90, unit: 60 });
assert.deepEqual(limitParts(45), { n: 45, unit: 1 });
assert.deepEqual(limitParts(0), { n: 1, unit: 3600 }, 'no limit stored: the default hour');
assert.deepEqual(limitParts(86400), { n: 24, unit: 3600 });
console.log('time_limit: ok');
