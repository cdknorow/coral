// How long an agent has waited (#7004): minutes, then hours, then days; nothing when the start is unknown.
import assert from 'node:assert/strict';
import { waitingText } from '../../coral-go/pkg/frontend/static/waiting_text.js';

const now = Date.parse('2026-10-24T15:00:00Z');
const ago = ms => new Date(now - ms).toISOString();
const MIN = 60000;
assert.equal(waitingText(ago(4 * MIN), now), 'waiting 4 min');
assert.equal(waitingText(ago(4 * MIN + 59000), now), 'waiting 4 min', 'whole minutes, rounded down');
assert.equal(waitingText(ago(30000), now), 'waiting under 1 min');
assert.equal(waitingText(ago(-5 * MIN), now), 'waiting under 1 min', 'a start in the future reads as just begun');
assert.equal(waitingText(ago(59 * MIN), now), 'waiting 59 min');
assert.equal(waitingText(ago(60 * MIN), now), 'waiting 1 h');
assert.equal(waitingText(ago(125 * MIN), now), 'waiting 2 h 5 min');
assert.equal(waitingText(ago(50 * 60 * MIN), now), 'waiting 2 d');
assert.equal(waitingText(null, now), '', 'null: nothing is said');
assert.equal(waitingText(undefined, now), ''); assert.equal(waitingText('', now), ''); assert.equal(waitingText('not a date', now), '');
console.log('waiting_text: ok');
