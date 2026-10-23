/* Cloud Hub integration: account menu, CSRF on state-changing requests, and
 * redirect-to-sign-in. Active only when the page's boot data says mode "cloud".
 *
 * Everything here builds DOM with textContent / properties (never innerHTML with
 * data) and registers its own listeners, so it works under a CSP without
 * 'unsafe-inline'. */

import { importOnce } from './import_once.js';
import { state } from './state.js';
import { isCloud } from './server_base.js';
import { filterSettingsPayload, settingsErrorMessage } from './cloud_settings.js';
import { signOutThisBrowser } from './local_store.js';
import { OUTCOME_UNKNOWN_LINE, isOutcomeUnknown } from './outcome_unknown.js';

/** GET paths that only a machine's own dashboard serves: privacy state and agent panels. (Themes,
 *  settings, tags, folder tags and views are the hub's own per-user data.) */
export const NO_HUB_ENDPOINT = /^\/api\/(system\/privacy|agent\/ui(\/.*)?)$/;

let _session = null;      // {authenticated, email, csrf}
let _loading = null;

/** Create an element: h('button', {className, onclick, dataset:{}}, ...children). */
export function h(tag, props, ...kids) {
    const el = document.createElement(tag);
    for (const [k, v] of Object.entries(props || {})) {
        if (v == null || v === false) continue;
        if (k === 'dataset') Object.assign(el.dataset, v);
        else if (k === 'text') el.textContent = v;
        else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
        else if (k in el) el[k] = v;
        else el.setAttribute(k, v);
    }
    for (const kid of kids.flat()) {
        if (kid == null || kid === false) continue;
        el.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
    }
    return el;
}

/** Fetch (and cache) /auth/session. */
export function loadSession(force = false) {
    if (force) { _session = null; _loading = null; }
    if (_session) return Promise.resolve(_session);
    if (!_loading) {
        _loading = fetch('/auth/session', { credentials: 'same-origin', headers: { Accept: 'application/json' } })
            .then(r => (r.ok ? r.json() : { authenticated: false }))
            .catch(() => ({ authenticated: false }))
            .then(s => { _session = s; _loading = null; return s; });
    }
    return _loading;
}

/** The current CSRF token ('' until the session has loaded). */
 function csrfToken() { return (_session && _session.csrf) || ''; }

/** Where the sign-in page sends the user back to afterwards (same-origin path only). */
export function signInUrl() {
    const here = location.pathname + location.search;
    return '/signin' + (here && here !== '/' ? '?next=' + encodeURIComponent(here) : '');
}

const SAFE = new Set(['GET', 'HEAD', 'OPTIONS']);

function sameOrigin(input) {
    try {
        const u = new URL(typeof input === 'string' || input instanceof URL ? String(input) : input.url, location.href);
        return u.origin === location.origin;
    } catch { return false; }
}

function settingsPath(input) {
    try { return new URL(typeof input === 'string' || input instanceof URL ? String(input) : input.url, location.href).pathname; } catch { return ''; }
}

/** Tell the user why the hub refused a settings or theme write (400/413 JSON bodies). */
function explainSettingsRefusal(resp) {
    if (!/json/i.test(resp.headers.get('Content-Type') || '')) return;
    resp.clone().json().then(async body => {
        const msg = settingsErrorMessage(resp.status, body);
        if (!msg) return;
        const { showToast } = await importOnce('./utils.js');
        showToast(msg, true, 8000);
    }).catch(() => {});
}

let _lastExplained = 0;
/** Tell the user why the hub or their machine refused a request (JSON 403 bodies only), once per few seconds. */
function explainRefusal(resp) {
    if (Date.now() - _lastExplained < 3000) return;
    // A launch says its own refusal, with the machine's name (launch_server.js): one toast, not two (#4586).
    if (/\/api\/sessions\/launch(-team)?$/.test(settingsPath(resp.url || ''))) return;
    if (!/json/i.test(resp.headers.get('Content-Type') || '')) return;
    resp.clone().json().then(async body => {
        const { refusalMessage } = await importOnce('./cloud_servers.js');
        const msg = refusalMessage(resp.status, body);
        if (!msg) return;
        _lastExplained = Date.now();
        const { showToast } = await importOnce('./utils.js');
        showToast(msg, true, 8000);
    }).catch(() => {});
}

// A reply the computer refused to send because it is too large to check for secrets through
// Coral Hub (#3718: a JSON reply over 2 MiB). Whatever view asked, the user is told why it is
// empty instead of seeing a blank pane: the dashboard pages what can be that big (history, the
// board export), so this is a rare reply, and one toast at a time says it.
let _lastTooLarge = 0;
function explainTooLarge(resp) {
    if (!/json/i.test(resp.headers.get('Content-Type') || '')) return;
    resp.clone().json().then(async body => {
        if (!body || body.code !== 'hub_reply_too_large' || Date.now() - _lastTooLarge < 5000) return;
        _lastTooLarge = Date.now();
        const { showToast } = await importOnce('./utils.js');
        showToast('This is too large to show through Coral Hub; open it on the computer.', true, 8000);
    }).catch(() => {});
}

/** A write the hub could not finish telling (502 outcome_unknown, #6342): the same response with its words replaced by the one line, so a path that shows the server's words shows that line and not a hint to send it again.
 *  Returns null for any other 502 and for a launch. */
async function sayOutcomeUnknown(resp, url) {
    if (!/json/i.test(resp.headers.get('Content-Type') || '')) return null;
    let body = null;
    try { body = await resp.clone().json(); } catch { return null; }
    if (!isOutcomeUnknown(resp.status, body, url || resp.url)) return null;
    return new Response(JSON.stringify({ ...body, error: OUTCOME_UNKNOWN_LINE }), { status: resp.status, statusText: resp.statusText, headers: resp.headers });
}

/** Wrap window.fetch: add X-CSRF-Token on same-origin state-changing calls and
 *  send the user to sign-in when the session has ended. */
export function installCloudFetch() {
    if (window.__coralCloudFetch) return;
    window.__coralCloudFetch = true;
    const orig = window.fetch.bind(window);
    window.fetch = async function (input, init) {
        const method = String((init && init.method) || (input && input.method) || 'GET').toUpperCase();
        // Endpoints that exist only on a machine's own dashboard are not reachable through the hub
        // (and the hub has no stand-in). Answer them locally with a 404 instead of sending a request
        // that can only fail, which would put a network error in the console on every page load.
        if (sameOrigin(input)) {
            const p = new URL(typeof input === 'string' || input instanceof URL ? String(input) : input.url, location.href).pathname;
            // '/api/cloud-unavailable' is where server_base.serverUrl() points a machine-only route (settings, privacy, ...)
            // so it fails cleanly; any method, never sent.
            if (p === '/api/cloud-unavailable' || (method === 'GET' && NO_HUB_ENDPOINT.test(p))) {
                return new Response('{"error":"not available through the hub"}', { status: 404, headers: { 'Content-Type': 'application/json' } });
            }
        }
        // The hub stores only per-user keys and refuses a whole /api/settings write that has any other key, so the
        // machine-local keys other callers still include (terminal size, CLI paths, ...) are dropped here.
        if (method === 'PUT' && sameOrigin(input) && settingsPath(input) === '/api/settings' && init && typeof init.body === 'string') {
            try {
                const { kept, dropped } = filterSettingsPayload(JSON.parse(init.body));
                if (dropped.length) {
                    console.debug('[coral] settings kept on the machine, not sent to the hub:', dropped.join(', '));
                    if (!Object.keys(kept).length) return new Response('{"ok":true}', { status: 200, headers: { 'Content-Type': 'application/json' } });
                    init = { ...init, body: JSON.stringify(kept) };
                }
            } catch { /* not JSON: let the hub answer */ }
        }
        if (!SAFE.has(method) && sameOrigin(input)) {
            if (!csrfToken()) await loadSession();
            const headers = new Headers((init && init.headers) || (input instanceof Request ? input.headers : undefined));
            if (csrfToken()) headers.set('X-CSRF-Token', csrfToken());
            init = { ...(init || {}), headers, credentials: 'same-origin' };
        }
        const resp = await orig(input, init);
        if (resp.status === 403 && !SAFE.has(method)) _session = null; // token may have rotated; refetch next time
        if (resp.status === 502 && sameOrigin(input)) explainTooLarge(resp);
        if (resp.status === 502 && !SAFE.has(method) && sameOrigin(input)) { const said = await sayOutcomeUnknown(resp, typeof input === 'string' || input instanceof URL ? String(input) : (input && input.url) || ''); if (said) return said; }
        // `quiet: true` marks a background request the user did not ask for (e.g. fitting the pane width on open):
        // a refusal there is not worth a toast, only an action the user took is.
        if (resp.status === 403 && sameOrigin(input) && !(init && init.quiet)) explainRefusal(resp);
        if ((resp.status === 400 || resp.status === 413) && method === 'PUT' && sameOrigin(input) && /^\/api\/(settings$|themes\/)/.test(settingsPath(input))) explainSettingsRefusal(resp);
        if (resp.status === 401 && sameOrigin(input) && !/^\/(signin|auth|enroll)/.test(location.pathname)) {
            location.replace(signInUrl());
        }
        return resp;
    };
}

const signOut = signOutThisBrowser; // drafts, pending messages, recent folders, stars and panel requests go before the session does (#3681, #4164)

function buildAccountMenu(email) {
    const menu = h('div', { className: 'cloud-account-menu', id: 'cloud-account-menu', hidden: true, role: 'menu' },
        h('div', { className: 'cloud-account-email', text: email, title: email }),
        h('button', { type: 'button', className: 'overflow-menu-item', role: 'menuitem', text: 'Servers', onclick: () => { setOpen(false); importOnce('./cloud_servers.js', 'the server list').then(m => m.showCloudServers(), () => {}); } }),
        // The hub's own pages, on this origin (the dashboard is served by the hub): plain links, same tab. Account is coral-go#1, Saved for later the hub's roll-up of every machine's saved messages (#4596).
        h('a', { className: 'overflow-menu-item', role: 'menuitem', href: '/account', text: 'Account' }),
        h('a', { className: 'overflow-menu-item', role: 'menuitem', href: '/saved', text: 'Saved for later' }),
        h('button', { type: 'button', className: 'overflow-menu-item', role: 'menuitem', text: 'Sign out', onclick: signOut }),
    );
    const setOpen = open => { menu.hidden = !open; btn.setAttribute('aria-expanded', open ? 'true' : 'false'); };
    const btn = h('button', {
        type: 'button', className: 'top-bar-btn cloud-account-btn', title: email, 'aria-label': 'Account menu', 'aria-haspopup': 'menu', 'aria-expanded': 'false',
        onclick: e => { e.stopPropagation(); setOpen(menu.hidden); },
    }, h('span', { className: 'material-icons top-bar-icon', 'aria-hidden': 'true', text: 'account_circle' }));
    document.addEventListener('click', () => setOpen(false));
    document.addEventListener('keydown', e => { if (e.key === 'Escape') setOpen(false); });
    return h('div', { className: 'cloud-account-wrapper' }, btn, menu);
}

/** Start cloud mode. Resolves true when signed in; otherwise redirects to sign-in. */
export async function initCloud() {
    if (!isCloud()) return false;
    document.body.classList.add('cloud-mode');
    state.cloud = true;
    state.hub = true;
    installCloudFetch();
    const s = await loadSession();
    if (!s.authenticated) { location.replace(signInUrl()); return false; }
    state.account = { email: s.email };
    const actions = document.querySelector('.top-bar-actions');
    if (actions && !document.getElementById('cloud-account-menu')) actions.append(buildAccountMenu(s.email));
    importOnce('./artifacts_tab.js').then(m => m.initArtifactsTab()).catch(() => {}); // not waited for: the Artifacts tab appears when the account says it has one (#6236)
    return true;
}
