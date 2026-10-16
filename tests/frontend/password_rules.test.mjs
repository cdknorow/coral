// The browser-password rules (#3948): any non-empty password is accepted (no length, list or composition rule, no trimming),
// the strength meter only advises, and the small formatting helpers. node password_rules.test.mjs
import assert from 'node:assert/strict';
import { passwordProblem, strengthOf, timeMs, retryAfterSeconds, formatCountdown, timeLeft, ageText, MAX_PASSWORD_BYTES } from '../../coral-go/pkg/frontend/static/password_rules.js';

// the operator's rule: non-empty is the only requirement
assert.equal(passwordProblem(''), 'Enter a password.');
for (const pw of ['a', '1', ' ', '  spaces  ', 'password', 'coral', 'Coral', 'qwerty123', 'pässwörd', '\u{1F511}'.repeat(10), 'x'.repeat(4000)]) {
    assert.equal(passwordProblem(pw, pw), '', `accepted as typed: ${JSON.stringify(pw.slice(0, 20))}`);
}
assert.equal(passwordProblem('one', 'two'), 'The two passwords do not match.');
assert.equal(passwordProblem('a'), '', 'confirmation is optional (sign-in has none)');
assert.match(passwordProblem('x'.repeat(MAX_PASSWORD_BYTES + 1)), /too long/);
assert.equal(passwordProblem('x'.repeat(MAX_PASSWORD_BYTES)), '', 'exactly at the cap is fine');
assert.match(passwordProblem('ä'.repeat(MAX_PASSWORD_BYTES / 2 + 1)), /too long/, 'the cap counts bytes, as the server does');

// the meter is advice: every password, even a weak one, has a level and none is an error
assert.equal(strengthOf('').level, 'empty');
assert.equal(strengthOf('abc').level, 'weak');
assert.equal(strengthOf('abcdefgh').level, 'fair');
assert.equal(strengthOf('abcdefghijkl').level, 'good');
assert.equal(strengthOf('abcdefghijklmnop').level, 'strong');
assert.equal(strengthOf('abcde').level, 'weak');
assert.equal(strengthOf('a b c').level, 'fair', 'the same length as words ranks higher');
assert.ok(strengthOf('a').pct > 0 && strengthOf('a').pct < strengthOf('abcdefghijklmnop').pct);
assert.equal(passwordProblem('a', 'a'), '', 'and a weak password is never refused');

assert.equal(timeMs('2026-10-16T12:00:00Z'), Date.UTC(2026, 9, 16, 12));
assert.ok(Number.isNaN(timeMs(undefined)) && Number.isNaN(timeMs('soon')));
assert.equal(retryAfterSeconds('872'), 872);
assert.equal(retryAfterSeconds('1.2'), 2);
assert.equal(retryAfterSeconds(null), 0);
assert.equal(retryAfterSeconds('soon'), 0);
assert.equal(retryAfterSeconds('-5'), 0);
assert.equal(formatCountdown(872), '14:32');
assert.equal(formatCountdown(5), '0:05');
assert.equal(formatCountdown(3725), '1:02:05');
assert.equal(formatCountdown(-3), '0:00');
const now = Date.UTC(2026, 9, 16, 12);
assert.equal(timeLeft(now + 28 * 86400000, now), '28 days left');
assert.equal(timeLeft(now + 5 * 3600000, now), '5 hours left');
assert.equal(timeLeft(now + 1800000, now), 'less than an hour left');
assert.equal(timeLeft(now - 1, now), 'expired');
assert.equal(timeLeft(NaN, now), '');
assert.equal(ageText(now - 30000, now), 'now');
assert.equal(ageText(now - 5 * 60000, now), '5 min ago');
assert.equal(ageText(now - 3 * 3600000, now), '3 h ago');
assert.equal(ageText(now - 86400000, now), 'yesterday');
assert.equal(ageText(now - 4 * 86400000, now), '4 days ago');
console.log('PASS password_rules');
