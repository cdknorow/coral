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
