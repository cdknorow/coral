/* How long an agent has waited for input, for a Needs you row (#7004, from the live row's waiting_since, #6981): "waiting 4 min", "waiting 2 h 5 min", "waiting 3 d". Text, never a hover title.
 * '' when the start is unknown (null: nothing is said, no guess) or not a date. A start in the future (a clock a little behind the server's) reads as just begun. No imports. */
export function waitingText(since, now = Date.now()) {
    if (!since) return '';
    const t = new Date(since).getTime();
    if (isNaN(t)) return '';
    const mins = Math.max(0, Math.floor((now - t) / 60000));
    if (mins < 1) return 'waiting under 1 min';
    if (mins < 60) return `waiting ${mins} min`;
    const h = Math.floor(mins / 60), m = mins % 60;
    if (h < 24) return m ? `waiting ${h} h ${m} min` : `waiting ${h} h`;
    return `waiting ${Math.floor(h / 24)} d`;
}
