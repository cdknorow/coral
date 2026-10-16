/* Small formatters shared by several views (#3794). Each was copied into the modules that needed it; these are the copies that
 * were character for character the same. Pure functions: no DOM, no state. */

/** 1234 -> "1.2K", 2500000 -> "2.5M"; below 1000 the number as text. */
export function formatTokens(n) {
    if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M';
    if (n >= 1000) return (n / 1000).toFixed(1) + 'K';
    return String(n);
}

/** An ISO timestamp as the local clock time (hh:mm:ss), or '' for none or a bad one. */
export function formatClockTime(isoStr) {
    if (!isoStr) return '';
    try {
        const d = new Date(isoStr);
        return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    } catch {
        return '';
    }
}

// Date and time formatters (#3866). Date.prototype.toLocale*String builds a new Intl formatter on every call, which was the top
// idle cost in the dashboard profile (the task list and the board panel format a time per row on every redraw). These keep one
// Intl.DateTimeFormat per format and use it. Like toLocale*String they answer "Invalid Date" for a bad date rather than throwing.
const _formatters = {};
const _dtf = (key, opts) => _formatters[key] || (_formatters[key] = new Intl.DateTimeFormat(undefined, opts));
const _fmt = (key, opts, d) => (isNaN(d) ? 'Invalid Date' : _dtf(key, opts).format(d));

/** A Date as the local hour and minute (hh:mm). */
export function formatHourMinute(d) { return _fmt('hm', { hour: '2-digit', minute: '2-digit' }, d); }

/** A Date as "Oct 16 10:31": month, day, hour and minute. */
export function formatMonthDayHourMinute(d) {
    return _fmt('md', { month: 'short', day: 'numeric' }, d) + ' ' + formatHourMinute(d);
}

/** A Date as the locale's month, day, hour and minute (the task detail's dates). */
export function formatMonthDayTime(d) { return _fmt('mdt', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }, d); }
