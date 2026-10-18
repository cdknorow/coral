/* The link to one saved message (#4596), for Coral Hub's /saved page to link into a machine's dashboard:
 *
 *   /?saved=<key>&kind=agent|board&ref=<conversation>[&server=<server id>]
 *
 *   key     the message: 20 lowercase hex for an agent's message, digits for a board's
 *   kind    agent (a live or ended agent's chat) or board (a message board)
 *   ref     the conversation: the agent's session id, or the board's name or id (what Saved lists as the item's ref); no "/", "\\" or "..", so it is one path segment
 *   server  the machine that holds it, on the Coral Hub only; absent (or "local") is this computer
 *
 * Pure (no DOM, no state), and never trusted: every part is checked against the shape it has before it becomes a request, as the Saved tab's own items are (validSavedItem),
 * and the server must look like a server id (the request then goes through serverFetch, which keeps it inside that server's proxy, #4551). Anything else is no link at all. */

const SERVER_RE = /^(?:[a-z0-9-]{1,32}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$/;
const KEY_RE = { agent: /^[0-9a-f]{20}$/, board: /^[1-9][0-9]{0,15}$/ };
// eslint-disable-next-line no-control-regex
const BAD_REF_RE = /[\u0000-\u001f\u007f-\u009f​-‏‪-‮⁠-⁤﻿]/;

/** The saved item a URL query means ({kind, ref, key, server}), or null when it has none or any part is not shaped right. `hub` is whether this dashboard is the Coral Hub's. */
export function parseSavedLink(search, hub = false) {
    const p = new URLSearchParams(search || '');
    if (!p.has('saved')) return null;
    const kind = p.get('kind');
    const key = p.get('saved');
    const ref = p.get('ref');
    if (!Object.hasOwn(KEY_RE, kind) || !KEY_RE[kind].test(key || '')) return null;
    if (!ref || [...ref].length > 200 || ref === '.' || ref.includes('..') || /[\\/]/.test(ref) || BAD_REF_RE.test(ref) || !ref.trim()) return null;
    let server = 'local';
    if (hub && p.has('server') && p.get('server') !== 'local') {
        server = (p.get('server') || '').toLowerCase();
        if (!SERVER_RE.test(server)) return null;
    }
    return { kind, ref, key, server };
}

/** The URL (path and query) for a saved item, the inverse of parseSavedLink. */
export function savedLinkUrl(item) {
    const q = new URLSearchParams({ saved: item.key, kind: item.kind, ref: item.ref });
    if (item.server && item.server !== 'local') q.set('server', item.server);
    return '/?' + q.toString();
}
