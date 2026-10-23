// shortWhen: the artifacts list's short Created (#6751).
import assert from 'node:assert/strict';
import { shortWhen } from '../../coral-go/pkg/frontend/static/artifact_when.js';
const now = new Date(2026, 9, 23, 15, 30);
assert.equal(shortWhen(new Date(2026, 9, 23, 9, 46).toISOString(), now), 'today 9:46');
assert.equal(shortWhen(new Date(2026, 9, 23, 16, 4).toISOString(), now), 'today 16:04');
assert.equal(shortWhen(new Date(2026, 9, 22, 23, 59).toISOString(), now), 'yesterday');
assert.equal(shortWhen(new Date(2026, 9, 21, 9, 46).toISOString(), now), 'Oct 21');
assert.equal(shortWhen(new Date(2025, 11, 31, 9, 46).toISOString(), now), 'Dec 31, 2025');
assert.equal(shortWhen('', now), '—');
assert.equal(shortWhen('not a date', now), '—');
console.log('artifact_when: ok');
