/* Custom views run in an opaque-origin sandbox.
 *
 * A saved view is HTML someone (the user, or any process that can reach the local API) wrote. It is never given the
 * dashboard's origin: the frame is `sandbox="allow-scripts"` only (no allow-same-origin), and its CSP is
 * `default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:`, so the view can run its
 * own script and draw, but cannot fetch, load anything, or reach the dashboard's DOM, storage or cookies.
 *
 * What a view needs from Coral (live sessions, tasks, board reads ...) comes through a postMessage bridge:
 *   - the view calls `coral.get('/api/sessions/live')` (or, for existing views, plain `fetch('/api/sessions/live')`,
 *     which the prelude routes through the bridge);
 *   - the parent answers ONLY a GET for a path on VIEW_READ_PATHS, with a bounded query, and caps the response size.
 *     Writes, other paths and anything else are refused. The bridge never forwards a method other than GET.
 * CORAL_CONTEXT (the selected agent) is injected by the prelude as before.
 *
 * Navigation: nothing in a CSP stops a document from navigating ITSELF to http://host/?data, so the view does not run
 * in the dashboard's frame directly. It runs in a child frame of a small trusted HOST frame (static/view_host.html) whose
 * CSP is `frame-src about:`: the host's policy governs the child's navigations, so location.href, meta refresh, links and
 * form submits to a URL are refused by the browser. The host also removes the child if it ever loads a second time, and
 * relays the bridge messages. The dashboard needs no CSP of its own for this (its linked artifact previews frame
 * external pages, so a dashboard-wide frame-src would break them).
 *
 * Why the host is a URL and not srcdoc (#3645): a srcdoc document inherits the embedding page's CSP, and the dashboard's
 * script-src 'self' would stop the host's script and every view script. A document loaded by URL has its own policy. The
 * dashboard posts the view's document to the host once the host says it is ready (attachViewBridge).
 *
 * Tiers: a view created in the dashboard ('ui') may read the full allowlist; a view created by an automated caller
 * ('api') may only read the agent list, tasks, tags, board names and the history list (no terminal output, files, git,
 * notes or events), and the confirm gate says so. */

export const VIEW_CSP = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; form-action 'none'; base-uri 'none'";
/** The host's own CSP (view_host.html's meta tag; a test keeps the two in step). */
export const HOST_CSP = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; frame-src about:; form-action 'none'; base-uri 'none'";
/** A fixed URL (outside the versioned static prefix), as panel_host.html. */
 const VIEW_HOST_URL = '/static/view_host.html';
export const MAX_BRIDGE_RESPONSE_BYTES = 1024 * 1024;
const MAX_PATH_LENGTH = 300;

// Read-only endpoints a view may call (the ones api-spec.json documents for views, minus anything that holds a secret
// or configures the machine: no /api/settings, /api/system/*, /api/views, /api/agent/*, no writes).
const SEG = '[A-Za-z0-9._~-]{1,200}';
// What any view may read: the agent list, per-agent tasks, the history list, board names and tags.
const BASIC_READ_PATHS = [
    '/api/sessions/live',
    `/api/sessions/live/${SEG}/tasks`,
    '/api/sessions/history',
    '/api/board/projects',
    '/api/tags',
].map(p => new RegExp(`^${p}$`));
// Extra reads for a view created in the dashboard: agent detail, terminal output, files, git, notes, events, board messages.
const EXTRA_READ_PATHS = [
    `/api/sessions/live/${SEG}`,
    `/api/sessions/live/${SEG}/(events|events/counts|files|git|notes|capture|info)`,
    `/api/sessions/history/${SEG}/(tasks|git)`,
    `/api/board/${SEG}/(messages/all|subscribers)`,
].map(p => new RegExp(`^${p}$`));
const VIEW_READ_PATHS = [...BASIC_READ_PATHS, ...EXTRA_READ_PATHS];

/** What a view of this provenance will be able to see, for the confirm gate. */
export const API_VIEW_SEES = 'the list of agents, their tasks, tags, board names and the chat-history list';

const QUERY_RE = /^[A-Za-z0-9_.,~=&%+:-]{0,200}$/;

/** Is `path` (path and optional ?query) a request a view may make? Only a GET for a listed read endpoint; a view created
 *  by an automated caller (tier 'api') gets the basic list only. */
export function isAllowedViewRequest(method, path, tier = 'ui') {
    if (String(method || 'GET').toUpperCase() !== 'GET') return false;
    if (typeof path !== 'string' || path.length > MAX_PATH_LENGTH || !path.startsWith('/api/')) return false;
    const q = path.indexOf('?');
    const pathname = q < 0 ? path : path.slice(0, q);
    const query = q < 0 ? '' : path.slice(q + 1);
    if (pathname.includes('%') || pathname.includes('//') || pathname.includes('..')) return false;
    if (!QUERY_RE.test(query)) return false;
    return (tier === 'api' ? BASIC_READ_PATHS : VIEW_READ_PATHS).some(re => re.test(pathname));
}

// Runs inside the frame, before the view's own markup. Kept dependency-free.
function prelude(ctx) {
    return `<script>(function(){
var pending = {}, seq = 0;
window.CORAL_CONTEXT = ${JSON.stringify(ctx).replace(/</g, '\\u003c')};
window.addEventListener('message', function (e) {
  var m = e.data;
  if (e.source !== window.parent || !m || m.type !== 'coral-view-response' || !pending[m.id]) return;
  var p = pending[m.id]; delete pending[m.id];
  if (m.error) p.reject(new Error(m.error)); else p.resolve(m);
});
function bridge(path) {
  return new Promise(function (resolve, reject) {
    var id = ++seq; pending[id] = { resolve: resolve, reject: reject };
    window.parent.postMessage({ type: 'coral-view-get', id: id, path: String(path) }, '*');
    setTimeout(function () { if (pending[id]) { delete pending[id]; reject(new Error('Coral did not answer')); } }, 15000);
  });
}
window.coral = {
  context: window.CORAL_CONTEXT,
  get: function (path) { return bridge(path).then(function (m) { return JSON.parse(m.body); }); }
};
// Views written for the dashboard origin call fetch('/api/...'): only the allowlisted GETs still work.
window.fetch = function (input, init) {
  var method = (init && init.method) || (input && input.method) || 'GET';
  var url = typeof input === 'string' ? input : (input && input.url) || '';
  if (String(method).toUpperCase() !== 'GET') return Promise.reject(new TypeError('Views are read-only'));
  return bridge(url).then(function (m) { return new Response(m.body, { status: m.status, headers: { 'Content-Type': 'application/json' } }); });
};
})();<\/script>`;
}

/** The srcdoc for a view: CSP meta, the bridge prelude, then the view's HTML. */
export function viewDocument(html, ctx) {
    return `<!doctype html><meta http-equiv="Content-Security-Policy" content="${VIEW_CSP}">${prelude(ctx)}${html}`;
}

/** Lock a view's host frame: scripts only, an opaque origin, no referrer, and the host page by URL. No csp attribute: a
 *  document loaded by URL would have to opt in to one; the host's own CSP is in view_host.html (and the view's frame,
 *  which the host builds, carries VIEW_CSP). */
export function lockViewFrame(frame) {
    frame.setAttribute('sandbox', 'allow-scripts');
    frame.setAttribute('allow', ''); // no camera, microphone, geolocation, clipboard...
    frame.referrerPolicy = 'no-referrer';
    frame.src = VIEW_HOST_URL;
    return frame;
}

/** The parent side of the bridge for one host frame: hands the host `doc` (viewDocument) when it says it is ready, then
 *  answers its reads. `fetchFn` defaults to fetch. Returns a function that detaches it. */
export function attachViewBridge(frame, { doc, fetchFn, tier = 'ui', onNavigated } = {}) {
    const doFetch = fetchFn || ((u, i) => fetch(u, i));
    let sent = false;
    const handler = async (e) => {
        if (e.source !== frame.contentWindow) return; // only this frame
        const m = e.data;
        if (m && m.type === 'coral-view-navigated') { if (onNavigated) onNavigated(); return; }
        if (m && m.type === 'coral-view-host-ready') {
            // Once: a second ready means the host reloaded, which the dashboard treats as a navigation (custom_views.js).
            if (!sent && typeof doc === 'string') { sent = true; frame.contentWindow.postMessage({ type: 'coral-view-load', doc }, '*'); }
            return;
        }
        if (!m || m.type !== 'coral-view-get' || typeof m.id !== 'number') return;
        const reply = payload => { try { frame.contentWindow.postMessage({ type: 'coral-view-response', id: m.id, ...payload }, '*'); } catch { /* frame gone */ } };
        if (!isAllowedViewRequest('GET', m.path, tier)) { reply({ error: 'This endpoint is not available to views' }); return; }
        try {
            const resp = await doFetch(m.path, { method: 'GET', credentials: 'same-origin', cache: 'no-store' });
            const declared = Number(resp.headers.get('Content-Length'));
            if (Number.isFinite(declared) && declared > MAX_BRIDGE_RESPONSE_BYTES) { reply({ error: 'Response too large for a view' }); return; }
            const text = await resp.text();
            if (text.length > MAX_BRIDGE_RESPONSE_BYTES) { reply({ error: 'Response too large for a view' }); return; }
            reply({ status: resp.status, body: text });
        } catch {
            reply({ error: 'Request failed' });
        }
    };
    window.addEventListener('message', handler);
    return () => window.removeEventListener('message', handler);
}
