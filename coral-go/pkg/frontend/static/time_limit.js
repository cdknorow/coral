/* A time limit as a person types it: a number and a unit ("1 hours", "90 minutes"), kept by the server in seconds. The New job and New workflow forms share it, and each says what is wrong in a sentence
 * (pure functions, node-tested). */
import { limitWords } from './job_words.js';

const MIN_SECONDS = 60;

/** The number and unit fields as seconds, or the sentence for what is wrong. `raw` is the number's text, `unit` the seconds in one unit (3600, 60 or 1); `max` is the most the server takes, in seconds (or 0). */
export function limitSeconds(raw, unit, max = 0) {
    const text = String(raw == null ? '' : raw).trim();
    const n = Number(text);
    const u = Number(unit) || 1;
    if (!text || !Number.isFinite(n)) return { error: 'Enter the time limit as a number.' };
    if (!Number.isInteger(n) || n < 1) return { error: 'Use a whole number of one or more.' };
    if (n * u < MIN_SECONDS) return { error: 'The time limit must be at least 1 minute.' };
    if (max && n * u > max) return { error: `The time limit can be at most ${limitWords(max)}.` };
    return { seconds: n * u };
}

/** Seconds as the number and unit the form shows: 5400 is 90 minutes, 3600 is 1 hour, 45 is 45 seconds. */
export function limitParts(seconds) {
    const s = Number(seconds) > 0 ? Number(seconds) : 3600;
    const unit = s % 3600 === 0 ? 3600 : s % 60 === 0 ? 60 : 1;
    return { n: s / unit, unit };
}
