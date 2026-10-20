/* The "Updating" badge in the Servers tab (#5355, coral-hub's spec #71333 with its section 4 correction #71360): a small read-only mark beside a machine's name that says the hub forwarded a relaunch for it a few
 * minutes ago and no outcome has been recorded yet. Behind the update flag (off).
 *
 * The one input is the top-level `relaunching` of GET /api/remote/<server>/api/system/update, and only the strict boolean true shows the badge: an absent field, false, a string, a number, null, a body that is not one
 * JSON object, and every failing read (401, 403, 404, 502, 503, the network) show nothing. Nothing else in the reply is read and no text from it is ever displayed (the words below are constants of this page). The
 * hub sets the flag itself and drops it in the very reply that carries THIS relaunch's own last_result, or after five minutes; this module never guesses: it mirrors the latest read.
 *
 * When to read: once when the Servers tab is opened, for each machine that is online; again when a machine comes back online; after this dashboard's own Relaunch got its 202 (noteRelaunchSent): at once, when the
 * machine is back online, and at the five-minute mark. Never on a timer per machine, never for an offline machine (the hub cannot answer for it). A read that says `relaunching: true` is read once more after five
 * minutes, so a flag the hub has aged out does not stay on screen.
 *
 * The one local rule: the dashboard that sent the Relaunch keeps showing the badge across the machine's own restart gap (it is offline, so it cannot be read), for at most five minutes from the 202, and stops at the
 * first of: a read with no `relaunching: true`, the machine online with no flag on its first read, or the five minutes.
 *
 * Known limit, by design: another dashboard sees the badge only if a read happens while the machine is online and the hub's flag is still set (before it goes offline, or after it reconnects and before the outcome is
 * recorded); it does not see it through the gap. Showing every viewer through the gap needs a hub change (a bounded `relaunching` in the machine list). */

import { updatesUiOn } from './update_flag.js';
import { serverBase } from './server_base.js';

export const BADGE_TEXT = 'Updating';
export const BADGE_NAME = 'Updating: Coral on this machine is restarting into an update. It reconnects on its own.'; // the accessible name starts with the visible word (UI/UX on #5355: label in name)
export const BADGE_TIP = 'Coral on this machine is restarting into an update. It reconnects on its own.';
export const AGE_OUT_MS = 5 * 60 * 1000;

const reads = new Map();   // server id -> { flag, at }: the latest read (flag is true only for relaunching: true), and when
const sent = new Map();    // server id -> { at, cleared, timer }: this dashboard's own relaunch
const recheck = new Map(); // server id -> the timer of the one re-read five minutes after a true read
const wasOnline = new Map();
let seenTab = false;
let onChange = () => {};

/** Whether a parsed reply says the machine is relaunching: the strict boolean on a single JSON object, nothing else. */
export function relaunchingOf(body) {
    return !!body && typeof body === 'object' && !Array.isArray(body) && body.relaunching === true;
}

/** One read of the machine's status through the hub: true only for the strict flag; false for anything else, a failure included. */
export async function readRelaunching(id) {
    let flag = false;
    try {
        const resp = await fetch(`${serverBase(id)}/api/system/update`, { headers: { Accept: 'application/json' }, cache: 'no-store' });
        if (resp.ok) flag = relaunchingOf(await resp.json());
    } catch { /* a failed read shows nothing */ }
    return flag;
}

async function readAndRecord(id) {
    if (!updatesUiOn()) return;
    const flag = await readRelaunching(id);
    reads.set(id, { flag, at: Date.now() });
    const own = sent.get(id);
    if (own && !flag) own.cleared = true; // a read after our own relaunch with no flag: it is over
    clearTimeout(recheck.get(id)); recheck.delete(id);
    if (flag && !own) recheck.set(id, setTimeout(() => { recheck.delete(id); readAndRecord(id).then(onChange); }, AGE_OUT_MS));
    if (own && own.cleared) { clearTimeout(own.timer); sent.delete(id); }
}

/** Whether the badge shows beside machine `id` now. */
export function badgeShown(id, now = Date.now()) {
    if (!updatesUiOn()) return false;
    const r = reads.get(id);
    if (r && r.flag && now - r.at < AGE_OUT_MS) return true; // (the hub's own flag is gone after five minutes: a read older than that says nothing)
    const own = sent.get(id);
    return !!own && !own.cleared && now - own.at < AGE_OUT_MS;
}

/** This dashboard's operator sent a Relaunch for machine `id` and got its 202. */
export function noteRelaunchSent(id) {
    if (!updatesUiOn() || !id) return;
    const prev = sent.get(id); if (prev) clearTimeout(prev.timer);
    const own = { at: Date.now(), cleared: false, timer: 0 };
    own.timer = setTimeout(async () => { // the five-minute mark: one more read (if it is online), then the badge is whatever the hub says
        if (wasOnline.get(id) === true) await readAndRecord(id);
        const cur = sent.get(id); if (cur === own) sent.delete(id);
        onChange();
    }, AGE_OUT_MS);
    sent.set(id, own);
    reads.delete(id);
    if (wasOnline.get(id) !== false) readAndRecord(id).then(onChange); // an online machine: now (an offline one is read when it comes back)
}

/** Called with every load of the machine list: reads the online machines once when the tab was opened, and any machine that has come back online. `rerender` redraws the list. */
export function syncRelaunchReads(servers, rerender) {
    onChange = rerender || onChange;
    if (!updatesUiOn()) return;
    const first = !seenTab; seenTab = true;
    for (const s of servers || []) {
        const id = s && s.id; if (!id) continue;
        const online = s.online === true, before = wasOnline.get(id);
        wasOnline.set(id, online);
        if (!online) continue;
        if (first || before === false) readAndRecord(id).then(onChange);
    }
}

/** The tab was opened afresh (or left): the next list is the first of a visit, read again. */
export function resetRelaunchReads() { seenTab = false; }

/** The badge element for machine `id`, or null. `h` is the page's element builder; the words are this module's constants. */
export function relaunchBadge(h, id) {
    if (!badgeShown(id)) return null;
    return h('span', { className: 'server-relaunch-badge', role: 'img', 'aria-label': BADGE_NAME, title: BADGE_TIP, text: BADGE_TEXT });
}
