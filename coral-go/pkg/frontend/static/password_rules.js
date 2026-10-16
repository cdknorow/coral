/* Pure rules for the browser-password screens (#3948). No DOM, no state: unit-tested on their own
 * (tests/frontend/password_rules.test.mjs).
 *
 * The operator's rule: a password is any non-empty text. No minimum length, no common-password or name checks, no
 * composition rules, no trimming (spaces count), and a long or pasted one is fine up to the server's 4 KiB cap. The
 * strength meter is advice only and never blocks anything. */

export const MAX_PASSWORD_BYTES = 4096;

/** The only checks a password gets: not empty, matches its confirmation, not past the server's cap. Returns an
 *  error text, or '' when the form may be sent. */
export function passwordProblem(password, confirm) {
    if (!password) return 'Enter a password.';
    if (new TextEncoder().encode(password).length > MAX_PASSWORD_BYTES) return 'That password is too long (the limit is 4096 bytes).';
    if (confirm !== undefined && password !== confirm) return 'The two passwords do not match.';
    return '';
}

/** Advisory strength: {level, pct, label, hint}. Length is what counts; words with spaces are as good as symbols. */
export function strengthOf(password) {
    const n = [...(password || '')].length;
    if (!n) return { level: 'empty', pct: 0, label: '', hint: '' };
    const words = (password.match(/\S+/g) || []).length;
    const effective = words >= 3 ? n + 4 : n;
    if (effective < 8) return { level: 'weak', pct: 25, label: 'Short.', hint: 'Longer is stronger, but any password works.' };
    if (effective < 12) return { level: 'fair', pct: 50, label: 'Fair.', hint: 'Longer is stronger: a few words with spaces works well.' };
    if (effective < 16) return { level: 'good', pct: 75, label: 'Good.', hint: 'Longer is stronger: a few words with spaces works well.' };
    return { level: 'strong', pct: 100, label: 'Strong.', hint: '' };
}

/** Seconds from a retry_after value or Retry-After header (whole seconds; Coral sends no HTTP date). 0 when absent or bad. */
export function retryAfterSeconds(value) {
    const n = Number(String(value == null ? '' : value).trim());
    return Number.isFinite(n) && n > 0 ? Math.ceil(n) : 0;
}

/** 872 -> "14:32"; 3725 -> "1:02:05". */
export function formatCountdown(seconds) {
    const s = Math.max(0, Math.floor(seconds));
    const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), r = s % 60;
    const two = x => String(x).padStart(2, '0');
    return h ? `${h}:${two(m)}:${two(r)}` : `${m}:${two(r)}`;
}

/** "28 days left" / "23 hours left" / "less than an hour left" for a session's expiry (ms epoch), or ''. */
export function timeLeft(expiresAtMs, nowMs = Date.now()) {
    if (!Number.isFinite(expiresAtMs)) return '';
    const h = (expiresAtMs - nowMs) / 3600000;
    if (h <= 0) return 'expired';
    if (h < 1) return 'less than an hour left';
    if (h < 48) return `${Math.floor(h)} hour${Math.floor(h) === 1 ? '' : 's'} left`;
    return `${Math.floor(h / 24)} days left`;
}

/** A short "2 days ago" style age for a past time (ms epoch). */
export function ageText(thenMs, nowMs = Date.now()) {
    if (!Number.isFinite(thenMs)) return '';
    const s = Math.max(0, (nowMs - thenMs) / 1000);
    if (s < 60) return 'now';
    if (s < 3600) return `${Math.floor(s / 60)} min ago`;
    if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
    const d = Math.floor(s / 86400);
    return d === 1 ? 'yesterday' : `${d} days ago`;
}

/** Milliseconds since the epoch for one of the server's RFC3339 times, NaN when it is not one. */
export function timeMs(rfc3339) {
    return typeof rfc3339 === 'string' ? Date.parse(rfc3339) : NaN;
}
