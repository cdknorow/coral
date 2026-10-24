/* The dashboard's routes (#3532): page paths <-> route objects. Pure (no DOM, no state), so
   the parser can be tested on its own and nav.js is the only place that touches history.

   A route is { view, server?, id?, sub?, run?, query? }:
     /agents                              { view: 'agents' }
     /agents/[@<server>/]<session>[/<sub>]  { view: 'agents', server, id, sub }
     /agents/[@<server>/]<session>[/<sub>]?view=terminal  the same, plus terminal: true (the link opens the agent on its terminal; coral-hub, #6752)
     /chats[/@<server>][?filters]         { view: 'chats', query }
     /chats/[@<server>/]<id>              { view: 'chats', id }
     /team/[@<server>/]<team>[/settings]  { view: 'team', server, id, sub }
     /board/[@<server>/]<name>            { view: 'board', server, id }
     /workflows[/<id>[/runs/<run>]]       { view: 'workflows', id, run }
     /jobs, /analytics, /docs[/<name>], /servers[/<id>]

   A remote object's server is the RAW segment "@<server>". A name that starts with "@" is written
   as %40, so the marker is tested before decoding. Every segment is decoded once and must match
   its own pattern; anything else is not a route (parse returns null and the router goes to /).
   Ids are never built into an /api path unless they passed these patterns (coral-hub Security). */

 const SERVER_RE = /^(?:[a-z0-9-]{1,32}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$/;
 const SESSION_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
// A history chat id: the agent's own session id (a UUID for most agents). No "/", "?", "#",
// "%" or "..", so it can't steer an API path.
 const HISTORY_ID_RE = /^(?!.*\.\.)[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
// An agent doc's name: its file name without .md (README, coral-board, ...).
 const DOC_RE = /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/;
 const INT_RE = /^[1-9][0-9]{0,15}$/;
// An agent's own tabs (live_session.html #agentic-tab-<id>); the team-scoped ones belong to /team.
export const AGENT_SUBTABS = ['board', 'tasks', 'notes', 'files', 'browse', 'artifacts', 'activity', 'agent-ui'];
const NAME_MAX = 200; // board.MaxIdentityRunes

// The query keys the Chats tab owns (search_filters.js), kept on /chats only.
 const CHATS_QUERY_KEYS = ['page', 'type', 'q', 'fts_mode', 'tag_ids', 'tag_logic', 'source_types',
    'date_from', 'date_to', 'min_duration_sec', 'max_duration_sec'];

// eslint-disable-next-line no-control-regex
const BAD_NAME_RE = /[\u0000-\u001f\u007f-\u009f\u200b-\u200f\u202a-\u202e\u2060-\u2064\ufeff]/;

/** A board or team name from a URL segment: decoded once, no control or formatting
 *  characters, at most NAME_MAX characters, at least one visible character, and not "." or
 *  "..": the views put the name in an /api path (boardFetch), where a dot segment would climb
 *  out of /api/board/ (coral-hub Security). New names like these are refused at creation (#3534). */
function name(raw) {
    const v = decode(raw);
    if (v === null || !v.trim() || v === '.' || v === '..' || BAD_NAME_RE.test(v) || [...v].length > NAME_MAX) return null;
    return v;
}

/** decodeURIComponent once, or null for a bad percent sequence. */
function decode(raw) {
    try { return decodeURIComponent(raw); } catch { return null; }
}

/** A server from a raw "@<server>" segment, or null when the segment is not a marker. */
function serverOf(raw) {
    if (!raw || raw[0] !== '@') return null;
    const d = decode(raw.slice(1));
    const s = d === null ? null : d.toLowerCase(); // "@M2" is server m2 (QA)
    return s !== null && SERVER_RE.test(s) ? s : undefined; // undefined: a bad marker
}

/** parse(pathname, search) returns a route, or null when the path is not a valid route. */
export function parse(pathname, search = '') {
    const segs = String(pathname || '/').split('/').slice(1);
    if (segs.length && segs[segs.length - 1] === '') segs.pop(); // a trailing slash
    const [top, ...rest] = segs;
    const take = () => {
        const srv = serverOf(rest[0]);
        if (srv === undefined) return { bad: true };
        if (srv) rest.shift();
        return { server: srv || null };
    };
    switch (top) {
        case undefined: case '': case 'agents': {
            if (!rest.length) return { view: 'agents' };
            const t = take();
            if (t.bad || !rest.length || rest.length > 2) return null;
            const id = decode(rest[0]);
            if (id === null || !SESSION_RE.test(id)) return null;
            const sub = rest[1] === undefined ? null : rest[1];
            if (sub !== null && !AGENT_SUBTABS.includes(sub)) return null;
            const route = { view: 'agents', server: t.server, id: id.toLowerCase(), sub };
            if (terminalQuery(search)) route.terminal = true; // read once from the URL: build() writes the path without it, so the address bar loses it as the page starts
            return route;
        }
        case 'chats': {
            const t = take();
            if (t.bad) return null;
            const source = t.server ? { server: t.server } : {};
            if (!rest.length) return { view: 'chats', ...source, query: chatsQuery(search) };
            if (rest.length !== 1) return null;
            const id = decode(rest[0]);
            return id !== null && HISTORY_ID_RE.test(id) ? { view: 'chats', ...source, id } : null;
        }
        case 'team': case 'board': {
            const t = take();
            if (t.bad || !rest.length) return null;
            const n = name(rest[0]);
            if (n === null) return null;
            if (top === 'board') return rest.length === 1 ? { view: 'board', server: t.server, id: n } : null;
            if (rest.length === 1) return { view: 'team', server: t.server, id: n, sub: null };
            return rest.length === 2 && rest[1] === 'settings' ? { view: 'team', server: t.server, id: n, sub: 'settings' } : null;
        }
        case 'workflows': {
            if (!rest.length) return { view: 'workflows' };
            if (!INT_RE.test(rest[0])) return null;
            if (rest.length === 1) return { view: 'workflows', id: Number(rest[0]) };
            if (rest.length === 3 && rest[1] === 'runs' && INT_RE.test(rest[2])) return { view: 'workflows', id: Number(rest[0]), run: Number(rest[2]) };
            return null;
        }
        case 'jobs': case 'analytics':
            return rest.length ? null : { view: top };
        case 'docs': {
            if (!rest.length) return { view: 'docs' };
            return rest.length === 1 && DOC_RE.test(rest[0]) ? { view: 'docs', id: rest[0] } : null;
        }
        case 'servers': {
            if (!rest.length) return { view: 'servers' };
            const id = rest.length === 1 ? decode(rest[0]) : null;
            return id !== null && SERVER_RE.test(id) ? { view: 'servers', id } : null;
        }
        default:
            return null;
    }
}

/** Whether a query string asks for the terminal: exactly one view parameter, and its value is exactly "terminal". Any other value, a second view, junk or none is no. Other keys are not read. */
function terminalQuery(search) {
    const v = new URLSearchParams(search || '').getAll('view');
    return v.length === 1 && v[0] === 'terminal';
}

/** The Chats tab's filters from a query string (only its own keys). */
export function chatsQuery(search) {
    const p = new URLSearchParams(search || '');
    const out = new URLSearchParams();
    for (const k of CHATS_QUERY_KEYS) if (p.has(k) && p.get(k) !== '') out.set(k, p.get(k));
    if (out.get('page') === '1') out.delete('page');
    return out.toString();
}

/** One name segment: encoded, with a leading "@" written as %40 so it can't read as a server. */
function seg(v) {
    const e = encodeURIComponent(String(v));
    return e.startsWith('@') ? '%40' + e.slice(1) : e;
}

const srvSeg = server => (server ? `@${encodeURIComponent(server)}/` : '');

/** build(route) returns the path (and query) for a route. */
export function build(r) {
    switch (r && r.view) {
        case 'agents':
            return r.id ? `/agents/${srvSeg(r.server)}${seg(r.id)}${r.sub ? '/' + r.sub : ''}` : '/agents';
        case 'chats':
            return r.id ? `/chats/${srvSeg(r.server)}${seg(r.id)}` : `/chats${r.server ? '/' + srvSeg(r.server).replace(/\/$/, '') : ''}${r.query ? '?' + r.query : ''}`;
        case 'team':
            return `/team/${srvSeg(r.server)}${seg(r.id)}${r.sub === 'settings' ? '/settings' : ''}`;
        case 'board':
            return `/board/${srvSeg(r.server)}${seg(r.id)}`;
        case 'workflows':
            return r.id ? `/workflows/${r.id}${r.run ? '/runs/' + r.run : ''}` : '/workflows';
        case 'jobs': case 'analytics':
            return '/' + r.view;
        case 'docs':
            return r.id ? `/docs/${seg(r.id)}` : '/docs';
        case 'servers':
            return r.id ? `/servers/${seg(r.id)}` : '/servers';
        default:
            return '/';
    }
}

/** The route an old hash link meant (#chat/, #session/, #history/, #team-view=, #team-settings=,
 *  #servers, #tokens, #board/, #agents), or null when the hash is not a legacy view link.
 *  The result goes through build() and parse() again, so a bad id is dropped like any other. */
export function fromLegacyHash(hash) {
    const h = String(hash || '').replace(/^#/, '');
    if (!h || h === 'agents') return { view: 'agents' };
    const team = /^team-(view|settings)=(.+)$/.exec(h);
    if (team) {
        const key = decode(team[2]);
        if (key === null) return null;
        const m = /^@([^/]+)\/(.+)$/.exec(key);
        return { view: 'team', server: m ? m[1] : null, id: m ? m[2] : key, sub: team[1] === 'settings' ? 'settings' : null };
    }
    const [v, ...parts] = h.split('/');
    const d = parts.map(decode);
    if (d.some(x => x === null)) return null;
    switch (v) {
        case 'chat':
            return d.length === 2 ? { view: 'agents', server: d[0], id: d[1] } : d.length === 1 ? { view: 'agents', id: d[0] } : null;
        case 'session': case 'history':
            return d.length === 1 ? { view: 'chats', id: d[0] } : d.length === 0 ? { view: 'chats', query: '' } : null;
        case 'servers':
            return d.length ? { view: 'servers', id: d[0] } : { view: 'servers' };
        case 'tokens': case 'analytics':
            return { view: 'analytics' };
        case 'workflows': case 'jobs': case 'docs':
            return d.length ? null : { view: v };
        case 'chats':
            return d.length ? null : { view: 'chats', query: '' };
        case 'board':
            return d.length === 1 ? { view: 'board', id: d[0] } : null;
        default:
            return null;
    }
}

/** The route as the parser reads it back from its own URL, or null: the ONLY form handlers
 *  ever see, so a field build() ignores (a stray sub, markup) can't reach a view, and an old or
 *  tampered history.state is checked like a typed URL (Security's review of 1b part 1). */
export function normalize(route) {
    if (!route || typeof route !== 'object') return null;
    const built = build(route);
    let url;
    try { url = new URL(built, 'http://x'); } catch { return null; }
    // A path the URL parser rewrote (a dot segment: "/team/./settings" is "/team/settings") is not
    // the route that was asked for.
    if (url.pathname !== built.split('?')[0]) return null;
    const back = parse(url.pathname, url.search);
    return back && back.view === route.view ? back : null;
}

/** Is this route valid? */
export function valid(route) { return normalize(route) !== null; }

// ── Routes from the dashboard's own keys ────────────────────────────────
// The views name a team or board by its identity key (server_base.js identityKey: "name" on this
// Coral, "@<server>/name" on another), and the local server is "local"; a route has server null.

const LOCAL = 'local';
const routeServer = s => (!s || s === LOCAL ? null : String(s));

/** The route of a live agent: its server ("local" or empty for this Coral) and session id. */
export function liveRoute(server, sessionId, sub = null) {
    return { view: 'agents', server: routeServer(server), id: sessionId, sub: sub || null };
}

/** The route of a team or board from its identity key. */
export function keyRoute(view, key, sub = null) {
    const k = String(key ?? '');
    // As splitKey reads it: "@<server>/name" only when <server> is a server id ("@x!/y" is a name).
    const m = /^@([^/]+)\/(.+)$/.exec(k);
    const remote = m && SERVER_RE.test(m[1]);
    const r = { view, server: remote ? routeServer(m[1]) : null, id: remote ? m[2] : k };
    if (view === 'team') r.sub = sub === 'settings' ? 'settings' : null;
    return r;
}

/** The identity key a team or board route names (the inverse of keyRoute). */
export function routeKey(route) {
    return route.server ? `@${route.server}/${route.id}` : route.id;
}
