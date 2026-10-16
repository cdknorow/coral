/* What the dashboard keeps in this browser of what people typed and where they worked (#3681,
 * storage audit #3680 findings 2 and 3): unsent drafts, pending (sent, not yet answered) messages,
 * recent launch folders and starred files. They were kept forever and outlived a sign-out.
 *
 *   - On Coral Hub the dashboard runs on the Hub's origin, so the same browser holds what every
 *     account that signs in there typed. Each kind is kept under the signed-in account
 *     (storeKey: "<kind>@<tag>", the tag a hash of the account's email, never the email), so a
 *     different account never reads another's even if a tab missed a cleanup; and when the
 *     account changes, the previous account's are removed (setStoreOwner).
 *   - Sign-out removes all of them (purgeStored), for every account, and the panel-request drafts a tab keeps in sessionStorage (#4116).
 *   - At start, then once a day in a tab left open (ageOutStored), drafts untouched for DRAFT_MAX_AGE_MS go, the draft of a live
 *     session that has ended and was untouched for a day goes (checked when a live list arrives,
 *     at most hourly), the drafts are capped at
 *     DRAFTS_MAX_BYTES (oldest first), and star lists untouched for STARS_MAX_AGE_MS go. Recent
 *     folders are already a short list (6 per server); they go on sign-out and account change.
 *   - A local dashboard has no account: its keys stay as they were (nothing is lost on update). */

import { browserLocal, browserSession } from './safe_storage.js';

const STORED_KINDS = ['coral-input-drafts', 'coral-pending-messages', 'coral-recent-dirs', 'coral-starred-files-'];
const OWNER_KEY = 'coral-store-owner';
// What a page keeps in sessionStorage of what people typed (#4116): the agent UI's panel-request draft, per session id. Not tied to an
// account (the tab is), so sign-out and an account change remove it like the kinds above.
const SESSION_KINDS = ['coral-panel-request:'];
 const DRAFT_MAX_AGE_MS = 3 * 24 * 3600 * 1000;
 const ENDED_GRACE_MS = 24 * 3600 * 1000;
export const DRAFTS_MAX_BYTES = 256 * 1024;
 const STARS_MAX_AGE_MS = 30 * 24 * 3600 * 1000;
export const DAY_MS = 24 * 3600 * 1000;
let _lastLivePrune = 0;

let _tag = '';


/** The storage key of `base` for the signed-in account (base itself on a local dashboard). */
export function storeKey(base) {
    return _tag ? `${base}@${_tag}` : base;
}

/** The account tag for an email: the first 16 hex digits of its SHA-256, lower-cased first. */
export async function accountTag(email) {
    const e = String(email || '').trim().toLowerCase();
    if (!e) return '';
    const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(e));
    return [...new Uint8Array(digest)].slice(0, 8).map(b => b.toString(16).padStart(2, '0')).join('');
}

function isStored(key) {
    // A kind ending in '-' is a prefix (a star list per session); the others are a name, optionally
    // with ":<server>" (recent folders of a remote) and "@<tag>" (an account).
    return STORED_KINDS.some(k => k.endsWith('-') ? key.startsWith(k) : key === k || key.startsWith(k + '@') || key.startsWith(k + ':'));
}

function ownerOf(key) {
    const at = key.lastIndexOf('@');
    // "coral-recent-dirs:@srv" style names hold an '@' of their own: an owner tag is hex after the last '@'.
    if (at < 0) return '';
    const t = key.slice(at + 1);
    return /^[0-9a-f]{16}$/.test(t) ? t : '';
}

// What a page holds in memory for the signed-in account (the chat pages kept for a quick switch back, #3726) goes when
// the account does: modules register what to forget here, and sign-out and an account change run it.
const _forgetters = new Set();
export function onAccountForgotten(fn) { _forgetters.add(fn); }
/** Forget what is held in memory for the account. Sign-out and an account change call it, next to purgeStored. */
export function forgetAccountMemory() {
    for (const fn of _forgetters) { try { fn(); } catch { /* one forgetter's failure must not keep the rest */ } }
}

/** Remove the stored kinds: all of them, or those `which(key, owner)` picks. */
export function purgeStored(which = () => true) {
    const keys = browserLocal.keys().filter(k => isStored(k) && which(k, ownerOf(k)));
    for (const k of keys) browserLocal.remove(k);
    const drafts = browserSession.keys().filter(k => SESSION_KINDS.some(p => k.startsWith(p)) && which(k, ''));
    for (const k of drafts) browserSession.remove(k);
    return keys.length + drafts.length;
}

/** Set the account whose data this page reads and writes. A change of account (or the first Hub
 *  sign-in on a browser that held data from before this rule) removes everyone else's. */
export function setStoreOwner(tag) {
    _tag = tag || '';
    const prev = browserLocal.get(OWNER_KEY);
    if (prev !== _tag) { purgeStored((k, owner) => owner !== _tag); forgetAccountMemory(); }
    browserLocal.set(OWNER_KEY, _tag);
}

/** Drafts as stored: { key: { t: text, at: ms } }. Older drafts were { key: text }; they load as
 *  touched now, so they age out from today. */
export function readDrafts() {
    const raw = browserLocal.get(storeKey('coral-input-drafts'));
    let obj = {};
    try { obj = raw ? JSON.parse(raw) : {}; } catch { obj = {}; }
    const out = {};
    const now = Date.now();
    for (const [k, v] of Object.entries(obj && typeof obj === 'object' ? obj : {})) {
        if (typeof v === 'string' && v) out[k] = { t: v, at: now };
        else if (v && typeof v.t === 'string' && v.t) out[k] = { t: v.t, at: Number(v.at) || now };
    }
    return out;
}

export function writeDrafts(drafts) {
    browserLocal.set(storeKey('coral-input-drafts'), JSON.stringify(drafts));
}

/** Age the drafts: drop those older than DRAFT_MAX_AGE_MS, those of a live session (`live:<id>`)
 *  not in `liveIds` and untouched for ENDED_GRACE_MS, and the oldest beyond DRAFTS_MAX_BYTES.
 *  Returns the drafts kept. */
export function ageDrafts(drafts, { now = Date.now(), liveIds = null } = {}) {
    let entries = Object.entries(drafts).filter(([k, d]) => {
        if (now - d.at > DRAFT_MAX_AGE_MS) return false;
        const m = /^live:(.+)$/.exec(k);
        if (liveIds && m && !liveIds.has(m[1]) && now - d.at > ENDED_GRACE_MS) return false;
        return true;
    });
    entries.sort((a, b) => b[1].at - a[1].at); // newest first
    let size = 2;
    const kept = {};
    for (const [k, d] of entries) {
        size += JSON.stringify(k).length + JSON.stringify(d).length + 2;
        if (size > DRAFTS_MAX_BYTES) break;
        kept[k] = d;
    }
    return kept;
}

/** A session's starred files as stored: { files: [...], at: ms }. Older lists were a bare array;
 *  they read as touched now. */
export function readStars(key) {
    let v = null;
    try { v = JSON.parse(browserLocal.get(key) || 'null'); } catch { v = null; }
    if (Array.isArray(v)) return { files: v.filter(f => typeof f === 'string'), at: Date.now() };
    if (v && Array.isArray(v.files)) return { files: v.files.filter(f => typeof f === 'string'), at: Number(v.at) || Date.now() };
    return { files: [], at: Date.now() };
}

export function writeStars(key, files) {
    if (files.length) browserLocal.set(key, JSON.stringify({ files, at: Date.now() }));
    else browserLocal.remove(key);
}

/** Star lists of this account untouched for STARS_MAX_AGE_MS. A bare (older) list is kept. */
function pruneStars(now) {
    purgeStored((k, owner) => {
        if (owner !== _tag || !k.startsWith('coral-starred-files-')) return false;
        let v = null;
        try { v = JSON.parse(browserLocal.get(k) || 'null'); } catch { return true; }
        return !!v && !Array.isArray(v) && now - (Number(v.at) || 0) > STARS_MAX_AGE_MS;
    });
}

/** The age-out. Without `liveIds` it applies the age and size rules (at start, and daily in a tab
 *  left open). With `liveIds` (the session ids of a live list just fetched) it also drops the
 *  drafts of ended sessions, at most hourly (returns null when skipped). Returns the drafts kept. */
export function ageOutStored({ liveIds = null, now = Date.now() } = {}) {
    if (liveIds) {
        if (now - _lastLivePrune < 3600 * 1000) return null;
        _lastLivePrune = now;
    }
    const drafts = readDrafts();
    const kept = ageDrafts(drafts, { now, liveIds });
    if (Object.keys(kept).length !== Object.keys(drafts).length) writeDrafts(kept);
    pruneStars(now);
    return kept;
}

/** For tests: forget when the ended-session check last ran. */
export function _resetLivePrune() { _lastLivePrune = 0; }
