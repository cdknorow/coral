// The one "when" format of every list (#6961, #6986): under a minute "just now", under an hour "N min ago", then today and yesterday with the time of day in the viewer's clock (12 or 24 hour by locale),
// an older day by its date alone, the year when it is not this one; '' for nothing or a non-date.
import assert from 'node:assert/strict';
import { whenText } from '../../coral-go/pkg/frontend/static/when_text.js';

const now = new Date(2026, 9, 24, 15, 0, 0); // Oct 24 2026, 15:00 local
const at = (y, mo, d, h = 0, mi = 0, s = 0) => new Date(y, mo, d, h, mi, s).toISOString();
// recent: not a time of day
assert.equal(whenText(at(2026, 9, 24, 14, 59, 30), now), 'just now', 'half a minute ago');
assert.equal(whenText(at(2026, 9, 24, 15, 0, 20), now), 'just now', 'a clock a few seconds ahead is just now');
assert.equal(whenText(at(2026, 9, 24, 14, 55), now), '5 min ago');
assert.equal(whenText(at(2026, 9, 24, 14, 1), now), '59 min ago');
assert.equal(whenText(at(2026, 9, 24, 14, 0), now), 'today 2:00 PM', 'at an hour it becomes a time of day (en-US)');
assert.equal(whenText(at(2026, 9, 24, 14, 0), now, 'en-GB'), 'today 14:00', 'the 24-hour clock for a locale that uses it');
// a time of day, in the viewer's clock
assert.equal(whenText(at(2026, 9, 24, 9, 5), now, 'en-US'), 'today 9:05 AM');
assert.equal(whenText(at(2026, 9, 24, 9, 5), now, 'en-GB'), 'today 09:05');
assert.equal(whenText(at(2026, 9, 23, 17, 20), now, 'en-GB'), 'yesterday 17:20');
assert.equal(whenText(at(2026, 9, 23, 17, 20), now, 'en-US'), 'yesterday 5:20 PM');
assert.equal(whenText(at(2026, 9, 23, 0, 0), now, 'en-GB'), 'yesterday 00:00');
// older: by date alone
assert.equal(whenText(at(2026, 9, 17, 12, 0), now), 'Oct 17', 'an older day is its date alone, no time');
assert.equal(whenText(at(2026, 8, 18, 12, 0), now), 'Sep 18');
assert.equal(whenText(at(2025, 8, 18, 12, 0), now), 'Sep 18, 2025', 'the year only when it is not this one');
assert.equal(whenText(at(2025, 11, 31, 23, 0), new Date(2026, 0, 1, 1, 0), 'en-GB'), 'yesterday 23:00', 'across New Year: yesterday, not a date');
assert.equal(whenText(at(2025, 11, 31, 23, 30), new Date(2026, 0, 1, 0, 10)), '40 min ago', 'an hour or less across midnight is still "min ago"');
assert.equal(whenText('', now), ''); assert.equal(whenText(null, now), ''); assert.equal(whenText('not a date', now), '');
// the viewer's own locale is used when none is given: the result is one of the two clocks
assert.match(whenText(at(2026, 9, 24, 14, 0), now), /^today (\d{1,2}:\d\d( ?[AP]M)?)$/i);
console.log('when_text: ok (just now, N min ago, then a time of day in the viewer\'s clock, older by date, the year when it is not this one)');
