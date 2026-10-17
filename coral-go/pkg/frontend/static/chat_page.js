/* The first page of a chat's history, and an early start for it (#4326).
 *
 * A page opened at /agents/<session id> (a reload, a bookmark, a shared link) names the session it will show, but the history request waits for
 * the live list to say which agent that is: the list answers, then the history is asked for, then it renders. startChatPrefetch asks for the
 * first page at once, naming the session by its id (the server answers a request named by the session's own id as it would the agent's name),
 * and the history loader takes that request over instead of making its own, when and only when it is the request it would have made.
 *
 * Handed over only on an exact match: this session id, the first page's own limit and trim, from offset 0, on the local server, outside the Coral
 * Hub. Anything else drops it (aborted). It is taken once, never kept as a cache, and ends: aborted with the reader's switch to another agent
 * (the loader ties its abort to the one it already has), and after PREFETCH_TTL_MS if nothing took it. */

// Messages in the first page, and the bytes each long tool field of a page carries: the history loader (live_chat.js) reads both from here, so the
// early request and the loader's own can never disagree.
export const FIRST_PAGE = 30;
export const CHAT_TRIM = 2048;

export const PREFETCH_TTL_MS = 15000;
const DEEP_LINK = /^\/agents\/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\/?$/i;

let early = null; // { sid, controller, promise, timer }

function drop() {
    if (!early) return;
    clearTimeout(early.timer);
    early.controller.abort();
    early = null;
}

/** Start the first page of the deep-linked session's history. Not for the Hub, the pop-out or a remote server's agent (none of those pages are
 *  /agents/<id> with a local session); `fetchFn` and `pathname` are for tests. Returns whether a request was started. */
export function startChatPrefetch({ pathname = location.pathname, fetchFn = (...a) => fetch(...a), ttlMs = PREFETCH_TTL_MS, cloud = false } = {}) {
    drop();
    const m = !cloud && DEEP_LINK.exec(pathname);
    if (!m) return false;
    const sid = m[1];
    const controller = new AbortController();
    const query = new URLSearchParams({ session_id: sid, after: '0', limit: String(FIRST_PAGE), offset: '0', trim: String(CHAT_TRIM) });
    const promise = Promise.resolve().then(() => fetchFn(`/api/sessions/live/${encodeURIComponent(sid)}/chat?${query}`, { signal: controller.signal }));
    promise.catch(() => {}); // a failed early request is the loader's to redo, not an unhandled rejection
    early = { sid, controller, promise, timer: setTimeout(drop, ttlMs) };
    return true;
}

/** The loader asks, with the session it is about to load and the parameters of the request it is about to make: the early request when that is the
 *  same request, else nothing (and the early one is dropped). Returns { promise, abort } and forgets the request: it is handed over once. */
export function takeChatPrefetch(session, params, { local = true, cloud = false } = {}) {
    if (!early) return null;
    const e = early;
    const same = !cloud && local && session && session.session_id === e.sid
        && params.get('session_id') === e.sid && params.get('after') === '0' && params.get('offset') === '0'
        && params.get('limit') === String(FIRST_PAGE) && params.get('trim') === String(CHAT_TRIM);
    if (!same) { drop(); return null; }
    clearTimeout(e.timer);
    early = null;
    return { promise: e.promise, abort: () => e.controller.abort() };
}

/** Forget any early request (aborting it): the page is not going to ask for it. */
export function dropChatPrefetch() { drop(); }
