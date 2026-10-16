/* Back off when Coral (or Coral Hub) is overloaded (#3403).

   The dashboard polls on fixed intervals (the open chat every second, board chat, the
   servers list, tasks, the scheduler…). When the server sheds load it answers 429, 502 or
   503, and every open tab used to keep hammering at the same cadence. Now:

   - Every same-origin /api/ answer is watched (installOverloadWatch wraps fetch once).
     A 429/502/503 from Coral itself pauses every poller: the first time for the server's
     Retry-After (spread over 100-125%), or 2 s (100-150%) when it gives none (#3430). Only an
     overload after that pause has run out counts as the next in a row: the pause then doubles,
     up to 30 s (a longer Retry-After wins), spread over 100-150% so the tabs of one account don't
     come back together. Answers to requests already in flight when a pause began do not double
     it (a page that sent five requests used to jump to the fifth step at once); a longer
     Retry-After among them still extends it.
   - Through a hub, a 429/503 for one remote machine (/api/remote/<id>/…) pauses only the
     pollers for that machine, unless the hub marks it as its own congestion
     (X-Coral-Hub-Overload: 1), which pauses everything. A remote 502 means that machine is
     unreachable, not load: its views back off on their own.
   - Nothing shortens a pause that is running. A success (2xx/3xx) clears the failure count,
     but only once that pause has run out; any other answer changes nothing.
   - pollEvery(fn, ms, {whenHidden, machine}) is setInterval for pollers: it skips ticks while
     paused (for its machine too, when given) and, unless whenHidden, while the tab is hidden.
     Pollers that drive notifications, unread counts or the title keep running hidden.
     It returns the interval id, so clearInterval still stops it.
   - A 429 marked X-Coral-Limit: action is one action's own limit (too many workflow or task runs,
     a sender's cross-team rate, sign-in attempts), not load (#3435): nothing pauses, no "busy"
     message, and the action shows the server's own reason. The hub's own congestion
     (X-Coral-Hub-Overload: 1) is load whatever else the answer says.
   - What the user does is never dropped: an action the server refuses as busy says so
     ("Coral is busy…", or "That machine is busy or unreachable…" for a remote one), and
     retryOnce() retries a file load once after the pause. A view's first load that is refused
     as busy shows that it is retrying and runs again as soon as the pause ends (afterPause),
     not a poll interval later (#3430).

   The live feed (/ws/coral) has its own reconnect backoff (feed_pacing.js). */

import { importOnce } from './import_once.js';
import { agentViewVisible } from './agent_view.js';

const MIN_PAUSE_MS = 2000;
const MAX_BACKOFF_MS = 30000;
const MAX_RETRY_AFTER_MS = 5 * 60 * 1000; // a Retry-After beyond this is treated as this
const OVERLOAD = new Set([429, 502, 503]);

/** Test seams. */
export const overloadHooks = { now: () => Date.now(), rand: Math.random };

// The shared pause, and one per remote machine (a hub's /api/remote/<id>/…).
const _global = { until: 0, failures: 0 };
const _machines = new Map(); // machine id -> { until, failures }
const _scopeState = machine => {
    if (!machine) return _global;
    let st = _machines.get(machine);
    if (!st) _machines.set(machine, st = { until: 0, failures: 0 });
    return st;
};

/** Retry-After as milliseconds (seconds or an HTTP date), 0 when absent or unreadable. */
export function retryAfterMs(value, now = overloadHooks.now()) {
    if (value === null || value === undefined || value === '') return 0;
    const s = String(value).trim();
    if (/^\d+$/.test(s)) return Math.min(Number(s) * 1000, MAX_RETRY_AFTER_MS);
    const t = Date.parse(s);
    return Number.isFinite(t) ? Math.max(0, Math.min(t - now, MAX_RETRY_AFTER_MS)) : 0;
}

/** How long to pause after the `failures`-th overload answer in a row (1 = the first). The first
 *  honours the server's Retry-After as given (#3430), with a little spread; without one, and from
 *  the second on, it is max(Retry-After, 2 s doubling to 30 s) spread over 100-150%. */
export function pauseMs(retryAfter, failures, rand = overloadHooks.rand) {
    if (failures <= 1 && retryAfter > 0) return Math.round(retryAfter * (1 + 0.25 * rand()));
    const backoff = Math.min(MAX_BACKOFF_MS, MIN_PAUSE_MS * 2 ** Math.max(0, failures - 1));
    const base = Math.max(retryAfter || 0, backoff);
    return Math.round(base * (1 + 0.5 * rand()));
}

/** Whether a response is a per-action limit rather than load: a 429 marked X-Coral-Limit: action
 *  (#3435), unless the hub marks its own congestion. */
export function isActionLimit(resp) {
    if (!resp || resp.status !== 429 || !resp.headers) return false;
    return resp.headers.get('X-Coral-Limit') === 'action' && resp.headers.get('X-Coral-Hub-Overload') !== '1';
}

/** Whether an answer says the server is busy (429 or 503), so a first load waits and retries
 *  instead of showing an error. A 502 is a gateway or an unreachable machine: an error with Retry. */
export function isBusyStatus(status) {
    return status === 429 || status === 503;
}

/** The remote machine an /api/remote/<id>/… path is for, or '' for Coral itself. */
export function machineOf(path) {
    const m = /^\/api\/remote\/([^/]+)\//.exec(String(path || ''));
    return m ? decodeURIComponent(m[1]) : '';
}

/** What an answer means for the pause: 'global' (Coral, or the hub itself, is overloaded),
 *  'machine' (one remote machine is), or '' (nothing to do with load).
 *  - On Coral's own /api/…: 429, 502 and 503 are global.
 *  - On /api/remote/<id>/…: 429 and 503 are that machine's, unless the hub marks the answer as
 *    its own congestion (X-Coral-Hub-Overload: 1), which is global; a 502 there means that
 *    machine is unreachable (its views back off on their own), not load. */
export function overloadScope(path, status, hubOverload = false) {
    if (!OVERLOAD.has(status)) return '';
    if (!machineOf(path)) return 'global';
    if (hubOverload) return 'global';
    return status === 502 ? '' : 'machine';
}

/** Kept for callers and tests: whether the answer counts toward any pause. */
export function countsAsOverload(path, status, hubOverload = false) {
    return overloadScope(path, status, hubOverload) !== '';
}

/** Record one answer. machine: the remote machine it is for ('' = Coral itself). An overload
 *  pauses its scope for pauseMs, counting as the next in a row only once the running pause has
 *  run out: during it, it is an answer to a request already in flight, and only a longer
 *  Retry-After extends the pause (nothing shortens it). A success (2xx/3xx) clears the failure
 *  count, but only once that scope's pause has run out, so a cheap call answering fine in the
 *  meantime can't cut the pause short. Other answers change nothing. */
export function noteResponse(status, retryAfterValue, { machine = '', overload } = {}) {
    const isOverload = overload !== undefined ? overload : OVERLOAD.has(status);
    const st = _scopeState(machine);
    const now = overloadHooks.now();
    if (isOverload) {
        const retryAfter = retryAfterMs(retryAfterValue, now);
        if (now < st.until) {
            if (retryAfter > 0 && now + retryAfter > st.until) st.until = now + retryAfter;
            return;
        }
        st.failures++;
        st.until = now + pauseMs(retryAfter, st.failures);
        return;
    }
    if (status >= 200 && status < 400 && now >= st.until) st.failures = 0;
}

/** Whether polling is paused: for Coral as a whole, or (machine given) for that machine. */
export function isPaused(machine = '') {
    const now = overloadHooks.now();
    if (now < _global.until) return true;
    const st = machine ? _machines.get(machine) : null;
    return !!st && now < st.until;
}
export function pausedForMs(machine = '') {
    const now = overloadHooks.now();
    const st = machine ? _machines.get(machine) : null;
    return Math.max(0, _global.until - now, st ? st.until - now : 0);
}

/** Who is busy, for a first load's "Loading… (… busy, retrying)" line: 'Coral Hub' when the hub
 *  marks its own congestion, 'the machine' when one remote machine refused through a hub, and
 *  otherwise the server the page talks to ('Coral Hub' on a hub-served dashboard, else 'Coral').
 *  Without an answer (a pause already running), by the pause: Coral's own, or that machine's. */
export function busyWho(resp, { machine = '', hub = false } = {}) {
    const self = hub ? 'Coral Hub' : 'Coral';
    if (resp) {
        if (resp.headers && resp.headers.get('X-Coral-Hub-Overload') === '1') return 'Coral Hub';
        let m = '';
        try { m = resp.url ? machineOf(new URL(resp.url).pathname) : ''; } catch { /* not a URL */ }
        return m ? 'the machine' : self;
    }
    const m = machine === 'local' ? '' : String(machine || '');
    return m && overloadHooks.now() >= _global.until && isPaused(m) ? 'the machine' : self;
}

/** The text a first load refused as busy shows while it waits to retry. */
export function busyRetryingText(resp, opts) {
    return `Loading… (${busyWho(resp, opts)} busy, retrying)`;
}

/** Runs fn once the pause for Coral (or, given, that machine) has run out: at once when there is
 *  none. For a view's first load refused as busy, which should not wait for its next poll (a
 *  board polls every 10 s). Returns the timer id. */
export function afterPause(fn, machine = '') {
    const m = machine === 'local' ? '' : String(machine || '');
    return setTimeout(fn, pausedForMs(m) + 50);
}

/** Tests: forget every pause. */
export function _resetOverloadForTests() { _global.until = 0; _global.failures = 0; _machines.clear(); }

/** setInterval for a poller: its ticks are skipped while Coral is overloaded (or, with
 *  machine, while the machine it polls is), and, unless whenHidden, while the tab is hidden.
 *  machine is a function returning the remote machine id ('' or 'local' for Coral itself).
 *  agentView is for a poller that only serves the selected agent's page (#3719): true skips its ticks
 *  while that page is not on screen (another view, Back to the phone's list, a hidden tab); a number
 *  keeps a slow beat instead, running at most once per that many ms while it is off screen (for a poller
 *  whose answer also feeds an unread count or a notification).
 *  Returns the interval id (clearInterval stops it). */
export function pollEvery(fn, ms, { whenHidden = false, machine = null, agentView = false } = {}) {
    let lastRun = 0;
    return setInterval(() => {
        const m = machine ? String(machine() || '') : '';
        if (isPaused(m === 'local' ? '' : m)) return;
        if (!whenHidden && typeof document !== 'undefined' && document.hidden) return;
        if (agentView && !agentViewVisible()) {
            if (typeof agentView !== 'number' || Date.now() - lastRun < agentView) return;
        }
        lastRun = Date.now();
        fn();
    }, ms);
}

/** Runs request() (a fetch the user asked for, e.g. opening a file); on an overload answer it
 *  waits out the pause and tries once more, so the user isn't left with an error for a blip. */
export async function retryOnce(request) {
    const resp = await request();
    if (!resp || !OVERLOAD.has(resp.status)) return resp;
    await new Promise(r => setTimeout(r, Math.max(pausedForMs(), MIN_PAUSE_MS))); // sleep-ok: the server asked us to wait
    return request();
}

const _apiPath = input => {
    try {
        const u = new URL(typeof input === 'string' || input instanceof URL ? String(input) : input.url, location.href);
        return u.origin === location.origin && u.pathname.startsWith('/api/') ? u.pathname : '';
    } catch { return ''; }
};

const _lastBusyToast = new Map(); // message kind -> when it was last shown

/** Watch every same-origin /api/ response (once per page). An action (not a GET) refused as
 *  busy says so, at most once per pause. */
let _watching = false;
export function installOverloadWatch() {
    if (typeof window === 'undefined' || _watching) return;
    _watching = true;
    const orig = window.fetch.bind(window);
    window.fetch = async function (input, init) {
        const resp = await orig(input, init);
        const apiPath = _apiPath(input);
        // One action's own limit, not load (#3435): pollers keep running, the caller says why.
        if (apiPath && isActionLimit(resp)) return resp;
        if (apiPath) {
            const hubOverload = !!(resp.headers && resp.headers.get('X-Coral-Hub-Overload') === '1');
            const scope = overloadScope(apiPath, resp.status, hubOverload);
            const machine = scope === 'machine' || (!scope && machineOf(apiPath)) ? machineOf(apiPath) : '';
            // A remote 502 (machine unreachable) is not recorded at all.
            if (scope || !OVERLOAD.has(resp.status)) {
                noteResponse(resp.status, resp.headers && resp.headers.get('Retry-After'), { machine, overload: !!scope });
            }
            const method = String((init && init.method) || (input && input.method) || 'GET').toUpperCase();
            const remoteRefused = machineOf(apiPath) && !hubOverload && (resp.status === 502 || resp.status === 503 || resp.status === 429);
            const kind = remoteRefused ? 'machine:' + machineOf(apiPath) : 'coral';
            if (method !== 'GET' && (scope || remoteRefused) && overloadHooks.now() - (_lastBusyToast.get(kind) || 0) > MIN_PAUSE_MS) {
                _lastBusyToast.set(kind, overloadHooks.now());
                const text = remoteRefused
                    ? "That machine is busy or unreachable, and didn't do that. Try again in a moment."
                    : `Coral is busy and didn't do that. Try again in ${Math.ceil(pausedForMs() / 1000)} s.`;
                importOnce('./utils.js').then(u => u.showToast(text, true)).catch(() => {});
            }
        }
        return resp;
    };
}
