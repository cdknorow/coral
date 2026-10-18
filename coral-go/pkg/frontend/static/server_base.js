/* Server routing helpers for the multi-server hub.
 *
 * Identity of an agent, terminal or team is (server, name). `server` is the
 * registered remote's id, or "local" (the default) for the hub itself. Every
 * helper treats a missing/empty server as "local", so single-server behaviour
 * and legacy stored keys are unchanged.
 *
 * Kept dependency-free (apart from the shared state object) so it can be
 * imported from any module without creating import cycles. api.js re-exports
 * everything here. */

import { state } from './state.js';

export const LOCAL_SERVER = 'local';

/** Normalise a server id: undefined/null/""/"local" all mean the hub itself. */
export function normServer(server) {
    return (!server || server === LOCAL_SERVER) ? LOCAL_SERVER : String(server);
}

export function isLocalServer(server) {
    return normServer(server) === LOCAL_SERVER;
}

/** URL prefix for HTTP calls to `server`: "" for local, "/api/remote/{id}" otherwise. */
export function serverBase(server) {
    const s = normServer(server);
    return s === LOCAL_SERVER ? '' : `/api/remote/${encodeURIComponent(s)}`;
}

/** WebSocket origin + prefix for `server`, e.g. "wss://host/api/remote/ws1". */
export function serverWsBase(server) {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    return `${proto}//${location.host}${serverBase(server)}`;
}

/** Routes a connector must never be asked about from the Cloud Hub: they configure or
 *  administer the machine itself (the hub's route table refuses them anyway; this keeps the UI
 *  from even trying). */
const CLOUD_NEVER = /^\/api\/(system|settings|webhooks|servers)(\/|\?|$)/;
/** The one machine-settings route the hub does expose: the changed-files comparison base (Interact tier, three validated values). */
const CLOUD_ALLOWED = /^\/api\/settings\/git-diff-mode$/;

/** The dashboard's link to a Coral-managed artifact. The `?v=2` form (v1.4.12) is new on purpose:
 *  before 1.4.12 the bare /api/artifacts/<digest> was served inline without a sandbox and cached
 *  for a year, so a link made now never matches such a cache entry. The server accepts both forms. */
export function artifactPath(digest) {
    return `/api/artifacts/${digest}?v=2`;
}

/** Prefix an absolute-path API url ("/api/...") for `server`. In cloud mode a route from
 *  CLOUD_NEVER is never sent through /api/remote: it is pointed at a path the hub 404s. */
export function serverUrl(url, server) {
    if (isCloud() && !isLocalServer(server) && CLOUD_NEVER.test(url) && !CLOUD_ALLOWED.test(url)) return '/api/cloud-unavailable';
    const full = serverBase(server) + url;
    if (!isLocalServer(server)) assertInProxy(full, serverBase(server));
    return full;
}

/** Resolve dot segments in a path the way a server routes it (backslashes count as slashes). */
function resolveDots(path) {
    const out = [];
    for (const seg of path.replace(/\\/g, '/').split('/').slice(1)) {
        if (seg === '.') continue;
        if (seg === '..') { out.pop(); continue; }
        out.push(seg);
    }
    return '/' + out.join('/');
}

/** A request for a registered server must stay inside its proxy: its path, with dot segments resolved and percent-escapes decoded (up to three layers, so
 *  %252e%252e is seen too), still starts with /api/remote/<server>/. A value that came from another machine (an id, a name, a path) must never walk a hub request out of
 *  the proxy to the hub's own routes (/api/servers/..., /api/settings/...): this is the last check, whatever built the URL (Security, #4551). Throws when it does not. */
export function assertInProxy(full, base) {
    const raw = String(full);
    const refuse = why => { throw new Error(`refused a request that would leave the proxy of ${base || 'a server'} (${why})`); };
    if (!raw.startsWith(base + '/')) refuse('not under the proxy');
    let path = raw.split(/[?#]/)[0];
    let root = base;
    for (let layer = 0; layer < 4; layer++) {
        if (!resolveDots(path).startsWith(root + '/')) refuse('dot segments leave it');
        let nextPath, nextRoot;
        try { nextPath = decodeURIComponent(path); nextRoot = decodeURIComponent(root); } catch { refuse('a bad percent escape'); }
        if (nextPath === path) return full;
        path = nextPath;
        root = nextRoot;
    }
    refuse('too many layers of escaping');
}

/** fetch() against `server`. Same contract as fetch (returns the Response); a URL that would leave the proxy is refused, as a rejected promise. */
export function serverFetch(server, url, options) {
    let target;
    try { target = serverUrl(url, server); } catch (e) { return Promise.reject(e); }
    return fetch(target, options);
}

/** The server a session/agent object belongs to ("local" when untagged). */
export function sessionServer(session) {
    return normServer(session && session.server);
}

/** Stable string key for (server, name). Local keys are the bare name, so keys
 *  persisted before the hub existed keep working. Remote keys can never equal a
 *  local name because server ids are slugs and contain no "/" or "@". */
export function identityKey(server, name) {
    const s = normServer(server);
    return s === LOCAL_SERVER ? String(name ?? '') : `@${s}/${name ?? ''}`;
}

/** DOM-id/attribute-safe version of identityKey. */
export function identityDomId(server, name) {
    return identityKey(server, name).replace(/[^A-Za-z0-9_-]/g, '_');
}

/** Path of the single-agent popout page: /agent/{id} (local) or /agent/{server}/{id}. */
export function agentPath(server, id) {
    const s = normServer(server);
    return s === LOCAL_SERVER
        ? `/agent/${encodeURIComponent(id)}`
        : `/agent/${encodeURIComponent(s)}/${encodeURIComponent(id)}`;
}

/** Parse /agent/{id} or /agent/{server}/{id}. Returns {server, id} or null. */
export function parseAgentPath(pathname) {
    const m = String(pathname || '').match(/^\/agent\/([^/]+)(?:\/([^/]+))?\/?$/);
    if (!m) return null;
    try {
        if (m[2] === undefined) return { server: LOCAL_SERVER, id: decodeURIComponent(m[1]) };
        return { server: normServer(decodeURIComponent(m[1])), id: decodeURIComponent(m[2]) };
    } catch { return null; }
}

/** Server of the currently selected session. */
export function currentServer() {
    return sessionServer(state.currentSession);
}

/** Server of the currently open team/board context (set when a board is opened). */
export function boardServer() {
    return normServer(state.currentBoardServer);
}

/** Resolve the server for a live session by its (unique) session id, falling
 *  back to a unique name match, then to the current selection. */
export function serverForSession(name, sessionId) {
    const list = state.liveSessions || [];
    if (sessionId) {
        const byId = list.find(s => s.session_id === sessionId);
        if (byId) return sessionServer(byId);
    }
    if (name) {
        const cur = state.currentSession;
        if (cur && cur.name === name) return sessionServer(cur);
        const byName = list.filter(s => s.name === name);
        if (byName.length === 1) return sessionServer(byName[0]);
    }
    return currentServer();
}

/* ── Team / group keys ─────────────────────────────────────────────────
 * Teams (boards) and sidebar groups are per-server too, so the UI keys them
 * with identityKey(server, name). Handlers that receive such a key unpack it
 * with splitKey() to learn which server to call. Local keys are the bare name,
 * so everything stored before the hub existed (collapse state, group order,
 * accent colours, folder tags) keeps matching. */

// A legacy server slug (<= 32 chars) or a hub server id (a UUID). Without the UUID form a hub server's
// folder showed up as its raw key, "@<uuid>/folder", instead of the folder name.
const SERVER_ID_RE = /^(?:[a-z0-9-]{1,32}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$/;

/** Unpack a key made by identityKey(): {server, name}. */
export function splitKey(key) {
    const k = String(key ?? '');
    if (k.startsWith('@')) {
        const i = k.indexOf('/');
        if (i > 1 && SERVER_ID_RE.test(k.slice(1, i))) return { server: k.slice(1, i), name: k.slice(i + 1) };
    }
    return { server: LOCAL_SERVER, name: k };
}

/** Display name of a key (drops the server part). */
// Board labels (#3536): a board's KEY (its id; for a board made before ids, its name) is what
// the API, URLs and state use; its LABEL is what people read. The views note the labels that
// come with their payloads (board_label next to board_project / project), and keyLabel shows
// them. Keyed by identity key, since two servers can each have a board with the same id.
const _boardLabels = new Map();   // identity key -> label
const _labelKeys = new Map();     // label (lower-case) -> Set of identity keys carrying it

const _labelListeners = new Set();

/** Call fn() whenever a known board label changes (a view that showed a key before its label
 *  arrived shows the label then). */
export function onBoardLabelsChanged(fn) { _labelListeners.add(fn); }

function _labelsChanged() {
    for (const fn of _labelListeners) { try { fn(); } catch (e) { console.error('board labels', e); } }
}

/** Note the label a payload gave for a board key on server. A row without one (a board with
 *  no label) leaves what is known. */
export function noteBoardLabel(server, key, label) {
    if (_setBoardLabel(server, key, label)) _labelsChanged();
}

function _setBoardLabel(server, key, label) {
    if (!key || typeof label !== 'string' || !label) return false;
    const k = identityKey(server, key);
    const prev = _boardLabels.get(k);
    if (prev === label) return false;
    if (prev !== undefined) _labelKeys.get(prev.toLowerCase())?.delete(k);
    _boardLabels.set(k, label);
    const lk = label.toLowerCase();
    if (!_labelKeys.has(lk)) _labelKeys.set(lk, new Set());
    _labelKeys.get(lk).add(k);
    return true;
}

/** Note the labels of a list of rows: keyField holds the board key, board_label its label,
 *  and the row's server (or `server`) the server it is on. */
export function noteBoardLabels(rows, keyField = 'board_project', server = null) {
    let changed = false;
    for (const r of Array.isArray(rows) ? rows : []) {
        if (r && r[keyField] && _setBoardLabel(server ?? r.server, splitKey(r[keyField]).name, r.board_label)) changed = true;
    }
    if (changed) _labelsChanged();
}

/** Whether a label is known for this key. */
export function hasBoardLabel(key) {
    return _boardLabels.has(String(key ?? ''));
}

/** A board's key as data (the name part of an identity key): for comparing with another key,
 *  never for showing. */
export function boardKeyName(key) {
    return splitKey(key).name;
}

/** The short form of a board key, shown after a label two boards share: a new board's id is
 *  "b-" plus 10 characters, so its first 6 are plenty; an older board's key is its name. */
function shortKey(name) {
    return /^b-[a-z0-9]{6,}$/.test(name) ? name.slice(0, 6) : name;
}

/** A board's label as known (no short id), or the key's name. */
export function boardLabel(key) {
    const k = String(key ?? '');
    return _boardLabels.get(k) || splitKey(k).name;
}

/** What people read for a board or team key: its label, or the key's name when no label is
 *  known; with a short id when another known board has the same label (#3536). */
export function keyLabel(key) {
    const k = String(key ?? '');
    const name = splitKey(k).name;
    const label = _boardLabels.get(k) || name;
    const same = _labelKeys.get(label.toLowerCase());
    return same && same.size > 1 && label !== name ? `${label} (${shortKey(name)})` : label;
}

/** Team key of a session ("" when it is not on a board). */
export function sessionTeamKey(s) {
    return s && s.board_project ? identityKey(sessionServer(s), s.board_project) : '';
}

/** True when session `s` belongs to the team identified by `teamKey`. */
export function inTeam(s, teamKey) {
    return !!teamKey && sessionTeamKey(s) === String(teamKey);
}

/** Folder-group key of a session (standalone agents are grouped by folder name). */
export function sessionFolderKey(s) {
    return identityKey(sessionServer(s), (s && s.name) || 'unknown');
}

/** fetch for a board endpoint: boardFetch(teamKey, '/messages', opts) ->
 *  /api/board/{name}/messages on the team's server. */
export function boardFetch(teamKey, suffix, options) {
    const { server, name } = splitKey(teamKey);
    return fetch(serverUrl(`/api/board/${encodeURIComponent(name)}${suffix || ''}`, server), options);
}

// ── Validated GETs (#3439) ───────────────────────────────────────────────
// A poll whose answer is often unchanged sends the last answer's validator back (If-None-Match)
// and, on a 304, reuses that answer: no body crosses the network, through Coral Hub too (which
// drops ETag; Coral sends the same validator in X-Coral-Validator). The answer is kept as TEXT and
// parsed again for each caller, so a caller that changes the objects it got never changes what a
// later 304 hands back. A validator is used only when it is the opaque token Coral sends (at most
// 128 bytes of printable ASCII, coral-hub's rule).
const VALIDATOR_RE = /^[\x21-\x7e]{1,128}$/;
const VALIDATED_MAX = 32;
const _validated = new Map(); // key -> { tag, text }

/** fetchValidated(key, doFetch): doFetch(headers) makes the request. Resolves to
 *  { ok: true, text, notModified } or { ok: false, resp } for the caller's own error handling. */
export async function fetchValidated(key, doFetch) {
    const last = _validated.get(key);
    const resp = await doFetch(last ? { 'If-None-Match': last.tag } : {});
    if (resp.status === 304 && last) return { ok: true, text: last.text, notModified: true };
    if (!resp.ok) return { ok: false, resp };
    const text = await resp.text();
    const tag = resp.headers.get('ETag') || resp.headers.get('X-Coral-Validator') || '';
    _validated.delete(key);
    if (VALIDATOR_RE.test(tag)) {
        _validated.set(key, { tag, text });
        if (_validated.size > VALIDATED_MAX) _validated.delete(_validated.keys().next().value);
    }
    return { ok: true, text, notModified: false };
}

/** fetch for any team-scoped endpoint whose path embeds the bare board name:
 *  teamFetch(teamKey, n => `/api/teams/detail/${encodeURIComponent(n)}/knowledge`). */
export function teamFetch(teamKey, pathFn, options) {
    const { server, name } = splitKey(teamKey);
    return fetch(serverUrl(pathFn(name), server), options);
}

/** Server of whatever the user is looking at: the open team if any, else the
 *  selected agent. Use for resources (artifacts, files) that belong to the
 *  current view rather than to a named agent or team. */
export function contextServer() {
    return state.selectedTeam ? splitKey(state.selectedTeam).server : currentServer();
}

/* ── Hub mode ──────────────────────────────────────────────────────────
 * Hub features are a runtime switch on the server (coral --hub / CORAL_HUB=1).
 * /api/health reports {hub: bool}; it is read once at startup into state.hub.
 * With hub=false the UI never touches /api/servers or /api/remote/*. */

let _hubPromise = null;

/* ── Boot data ─────────────────────────────────────────────────────────
 * The server renders <script type="application/json" id="coral-boot">{"mode":...}</script>.
 * It is data, not code, so it works under a CSP without 'unsafe-inline'.
 * mode "cloud" = the Cloud Hub (account-owned servers, no local agents);
 * anything else = a standalone Coral or a self-hosted hub. */

let _boot = null;

/** Parsed boot data ({} when absent or malformed). */
 function bootConfig() {
    if (_boot) return _boot;
    _boot = {};
    try {
        const el = typeof document !== 'undefined' ? document.getElementById('coral-boot') : null;
        const v = el ? JSON.parse(el.textContent) : null;
        if (v && typeof v === 'object') _boot = v;
    } catch { /* keep {} */ }
    return _boot;
}

/** True on the Cloud Hub. */
export function isCloud() {
    return bootConfig().mode === 'cloud';
}

/** Read /api/health once and cache the flag in state.hub. Safe to call often.
 *  The Cloud Hub is always a hub; it does not need /api/health to say so. */
export function initHub() {
    if (isCloud()) { state.hub = true; state.cloud = true; return Promise.resolve(true); }
    if (!_hubPromise) {
        _hubPromise = fetch('/api/health')
            .then(r => (r.ok ? r.json() : {}))
            .then(d => {
                state.hub = d.hub === true;
                // This server's own version, for the Servers tab's Local row (#3152).
                state.localVersion = typeof d.version === 'string' ? d.version : '';
                return state.hub;
            })
            .catch(() => { state.hub = false; return false; });
    }
    return _hubPromise;
}

/** True when this server runs in hub mode (valid after initHub() resolved). */
export function isHub() {
    return state.hub === true;
}
