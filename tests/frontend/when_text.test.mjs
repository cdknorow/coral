// The one "when" format of every list (#6961): today and yesterday with the 24-hour time, an older day by its date alone, the year when it is not this one; '' for nothing or a non-date.
import assert from 'node:assert/strict';
import { whenText } from '../../coral-go/pkg/frontend/static/when_text.js';

const now = new Date(2026, 9, 24, 15, 0, 0); // Oct 24 2026, 15:00 local
const at = (y, mo, d, h = 0, mi = 0) => new Date(y, mo, d, h, mi).toISOString();
assert.equal(whenText(at(2026, 9, 24, 14, 32), now), 'today 14:32');
assert.equal(whenText(at(2026, 9, 24, 9, 5), now), 'today 09:05', 'a single-digit hour and minute are padded');
assert.equal(whenText(at(2026, 9, 24, 23, 59), now), 'today 23:59', 'later today than now is still today');
assert.equal(whenText(at(2026, 9, 23, 17, 20), now), 'yesterday 17:20');
assert.equal(whenText(at(2026, 9, 23, 0, 0), now), 'yesterday 00:00');
assert.equal(whenText(at(2026, 9, 17, 12, 0), now), 'Oct 17', 'an older day is its date alone, no time');
assert.equal(whenText(at(2026, 8, 18, 12, 0), now), 'Sep 18');
assert.equal(whenText(at(2025, 8, 18, 12, 0), now), 'Sep 18, 2025', 'the year only when it is not this one');
assert.equal(whenText(at(2025, 11, 31, 23, 0), new Date(2026, 0, 1, 1, 0)), 'yesterday 23:00', 'across New Year: yesterday, not a date');
assert.equal(whenText('', now), ''); assert.equal(whenText(null, now), ''); assert.equal(whenText('not a date', now), '');
console.log('when_text: ok (today and yesterday with the time, older by date, the year when it is not this one)');
