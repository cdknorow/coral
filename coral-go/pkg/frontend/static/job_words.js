/* The Jobs screens' words (#6515, #6521): a schedule, a next run, a last result and a time limit as a person says them, not as the scheduler stores them. Pure functions (node-tested). A schedule these do not
 * know how to say is shown as it is, never guessed. */

const pad = (n) => String(n).padStart(2, '0');
const DAYS = ['Sundays', 'Mondays', 'Tuesdays', 'Wednesdays', 'Thursdays', 'Fridays', 'Saturdays'];

/** "0 2 * * *" -> "Every day at 02:00"; the five-field cron the scheduler takes. Unknown shapes come back as `Schedule: <expr>` so nothing is lost. */
export function cronWords(expr) {
    const e = String(expr || '').trim();
    const f = e.split(/\s+/);
    if (f.length !== 5) return e ? `Schedule: ${e}` : '';
    const [min, hour, dom, mon, dow] = f;
    const num = (s) => /^\d+$/.test(s);
    if (min === '0' && hour === '*' && dom === '*' && mon === '*' && dow === '*') return 'Every hour';
    if (min === '*' && hour === '*' && dom === '*' && mon === '*' && dow === '*') return 'Every minute';
    let m = /^\*\/(\d+)$/.exec(min);
    if (m && hour === '*' && dom === '*' && mon === '*' && dow === '*') return `Every ${m[1]} minutes`;
    m = /^\*\/(\d+)$/.exec(hour);
    if (m && num(min) && dom === '*' && mon === '*' && dow === '*') return `Every ${m[1]} hours${min === '0' ? '' : ` at :${pad(min)}`}`;
    if (num(min) && num(hour) && dom === '*' && mon === '*') {
        const at = `${pad(hour)}:${pad(min)}`;
        if (dow === '*') return `Every day at ${at}`;
        if (dow === '1-5') return `Weekdays at ${at}`;
        if (dow === '0,6' || dow === '6,0') return `Weekends at ${at}`;
        if (num(dow) && +dow <= 7) return `${DAYS[+dow % 7]} at ${at}`;
    }
    if (num(min) && num(hour) && num(dom) && mon === '*' && dow === '*') return `Monthly on day ${dom} at ${pad(hour)}:${pad(min)}`;
    return `Schedule: ${e}`;
}

/** A coming time as "today 19:00", "tomorrow 02:00", "Oct 25 02:00": in the viewer's own time. `now` is for tests. */
export function atWords(iso, now = new Date()) {
    const t = new Date(iso);
    if (!iso || isNaN(t)) return '';
    const hm = `${pad(t.getHours())}:${pad(t.getMinutes())}`;
    const day = (d) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
    const dd = Math.round((day(t) - day(now)) / 86400000);
    return dd === 0 ? `today ${hm}` : dd === 1 ? `tomorrow ${hm}` : `${t.toLocaleDateString([], { month: 'short', day: 'numeric' })} ${hm}`;
}

/** How far off a time is: "in 20 min", "in 5 h", "in 4 days", "now". */
export function awayWords(iso, now = new Date()) {
    const t = new Date(iso);
    if (!iso || isNaN(t)) return '';
    const mins = Math.round((t - now) / 60000);
    return mins < 1 ? 'now' : mins < 60 ? `in ${mins} min` : mins < 36 * 60 ? `in ${Math.round(mins / 60)} h` : `in ${Math.round(mins / 1440)} days`;
}

/** A moment as "today 06:36", "yesterday 02:00", "Oct 21 23:44". */
export function whenWords(iso, now = new Date()) {
    const t = new Date(iso);
    if (!iso || isNaN(t)) return '';
    const hm = `${pad(t.getHours())}:${pad(t.getMinutes())}`;
    const day = (d) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
    const dd = Math.round((day(now) - day(t)) / 86400000);
    return dd === 0 ? `today ${hm}` : dd === 1 ? `yesterday ${hm}` : `${t.toLocaleDateString([], { month: 'short', day: 'numeric' })} ${hm}`;
}

/** The scheduler counts steps from 0 ("step 1 (Test) failed" for the second step): say them from 1, the way the page lists them. */
export function stepWords(msg) {
    return String(msg || '').replace(/\bstep (\d+) \(/g, (_, n) => `step ${Number(n) + 1} (`);
}

/** Why a run failed, in words: "step 2 (Test) exited with 3" (the scheduler's "step 1 (Test) failed: exit code 3"), or ''. */
export function reasonWords(run) {
    return run && run.error_msg ? stepWords(run.error_msg).replace(/ failed: exit code (\d+)/, ' exited with $1') : '';
}

/** A run's result in words: "Passed", "Failed: step 2 (Test) exited with 3", "Stopped", "Running", "Waiting to start". */
export function resultWords(run) {
    if (!run) return '';
    const why = reasonWords(run);
    switch (run.status) {
        case 'completed': return 'Passed';
        case 'failed': return why ? `Failed: ${why}` : 'Failed';
        case 'killed': return 'Stopped';
        case 'running': return 'Running';
        case 'pending': return 'Waiting to start';
        default: return run.status || '';
    }
}

/** A last result for a list row: "last run passed", "never run". */
export function lastWords(run) {
    if (!run) return 'never run';
    return { completed: 'last run passed', failed: 'last run failed', killed: 'last run stopped', running: 'running now', pending: 'waiting to start' }[run.status] || `last run ${run.status}`;
}

/** A time limit in seconds as "1 hour", "10 minutes", "90 seconds". */
export function limitWords(s) {
    const n = Number(s);
    if (!n || n < 0) return '';
    if (n % 3600 === 0) return `${n / 3600} hour${n === 3600 ? '' : 's'}`;
    if (n % 60 === 0) return `${n / 60} minute${n === 60 ? '' : 's'}`;
    return `${n} seconds`;
}
