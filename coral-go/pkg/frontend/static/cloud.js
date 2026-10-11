/* Cloud Hub integration: account menu, CSRF on state-changing requests, and
 * redirect-to-sign-in. Active only when the page's boot data says mode "cloud".
 *
 * Everything here builds DOM with textContent / properties (never innerHTML with
 * data) and registers its own listeners, so it works under a CSP without
 * 'unsafe-inline'. */

import { state } from './state.js';
import { isCloud } from './server_base.js';

/** GET paths that only a machine's own dashboard serves: license, privacy state, custom themes, agent panels. */
export const NO_HUB_ENDPOINT = /^\/api\/(license\/status|system\/privacy|themes\/(?!variables$)[^/]+|agent\/ui(\/.*)?)$/;

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
export function csrfToken() { return (_session && _session.csrf) || ''; }

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

let _lastExplained = 0;
/** Tell the user why the hub or their machine refused a request (JSON 403 bodies only), once per few seconds. */
function explainRefusal(resp) {
    if (Date.now() - _lastExplained < 3000) return;
    if (!/json/i.test(resp.headers.get('Content-Type') || '')) return;
    resp.clone().json().then(async body => {
        const { refusalMessage } = await import('./cloud_servers.js');
        const msg = refusalMessage(resp.status, body);
        if (!msg) return;
        _lastExplained = Date.now();
        const { showToast } = await import('./utils.js');
        showToast(msg, true, 8000);
    }).catch(() => {});
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
            // '/api/cloud-unavailable' is where server_base.serverUrl() points a machine-only route (settings, license, ...)
            // so it fails cleanly; any method, never sent.
            if (p === '/api/cloud-unavailable' || (method === 'GET' && NO_HUB_ENDPOINT.test(p))) {
                return new Response('{"error":"not available through the hub"}', { status: 404, headers: { 'Content-Type': 'application/json' } });
            }
        }
        if (!SAFE.has(method) && sameOrigin(input)) {
            if (!csrfToken()) await loadSession();
            const headers = new Headers((init && init.headers) || (input instanceof Request ? input.headers : undefined));
            if (csrfToken()) headers.set('X-CSRF-Token', csrfToken());
            init = { ...(init || {}), headers, credentials: 'same-origin' };
        }
        const resp = await orig(input, init);
        if (resp.status === 403 && !SAFE.has(method)) _session = null; // token may have rotated; refetch next time
        // `quiet: true` marks a background request the user did not ask for (e.g. fitting the pane width on open):
        // a refusal there is not worth a toast, only an action the user took is.
        if (resp.status === 403 && sameOrigin(input) && !(init && init.quiet)) explainRefusal(resp);
        if (resp.status === 401 && sameOrigin(input) && !/^\/(signin|auth|enroll)/.test(location.pathname)) {
            location.replace(signInUrl());
        }
        return resp;
    };
}

async function signOut() {
    try { await fetch('/auth/logout', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' }); } catch { /* ignore */ }
    location.replace('/signin');
}

function buildAccountMenu(email) {
    const menu = h('div', { className: 'cloud-account-menu', id: 'cloud-account-menu', hidden: true, role: 'menu' },
        h('div', { className: 'cloud-account-email', text: email, title: email }),
        h('button', { type: 'button', className: 'overflow-menu-item', role: 'menuitem', text: 'Servers', onclick: () => { menu.hidden = true; import('./cloud_servers.js').then(m => m.showCloudServers()); } }),
        h('button', { type: 'button', className: 'overflow-menu-item', role: 'menuitem', text: 'Sign out', onclick: signOut }),
    );
    const btn = h('button', {
        type: 'button', className: 'top-bar-btn cloud-account-btn', title: email, 'aria-label': 'Account menu', 'aria-haspopup': 'menu',
        onclick: e => { e.stopPropagation(); menu.hidden = !menu.hidden; },
    }, h('span', { className: 'material-icons top-bar-icon', 'aria-hidden': 'true', text: 'account_circle' }));
    document.addEventListener('click', () => { menu.hidden = true; });
    document.addEventListener('keydown', e => { if (e.key === 'Escape') menu.hidden = true; });
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
    return true;
}
