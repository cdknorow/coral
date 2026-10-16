/* Links to Coral Hub's own pages (#3406). When this dashboard is served by Coral Hub, the hub
 * may list pages of its own (its machines pages: /machines, /machines/keys, /machines/add) at
 * GET /hub/nav.json as [{label, path}]. They are shown as plain links at the top of the
 * Servers tab. The hub is trusted only this far, as agreed with coral-hub:
 *
 *   - asked only when the page is served by the hub (never on a local Coral), same origin,
 *     no credentials beyond the page's own; an answer over 4 KiB is ignored;
 *   - a link is kept only if its path matches ^/machines(/[a-z0-9-]+)*$ (so no scheme, host,
 *     query, fragment, '..', backslash or '//'), its label is 1-40 characters, and it is one
 *     of the first 8 such links; anything else is dropped;
 *   - labels are text (textContent), links are plain same-tab anchors.
 *
 * No machine-supplied string is involved: the list comes from the hub itself. */

import { isCloud } from './server_base.js';

 const HUB_NAV_URL = '/hub/nav.json';
export const HUB_NAV_MAX_BYTES = 4096;
 const HUB_NAV_MAX_ITEMS = 8;
 const HUB_NAV_MAX_LABEL = 40;
 const HUB_NAV_PATH = /^\/machines(\/[a-z0-9-]+)*$/;

/** The links a /hub/nav.json body allows ([] for anything that isn't a valid list). */
export function parseHubNav(text) {
    if (typeof text !== 'string' || new TextEncoder().encode(text).length > HUB_NAV_MAX_BYTES) return [];
    let data;
    try { data = JSON.parse(text); } catch { return []; }
    if (!Array.isArray(data)) return [];
    const out = [];
    for (const item of data) {
        if (out.length >= HUB_NAV_MAX_ITEMS) break;
        if (!item || typeof item !== 'object' || Array.isArray(item)) continue;
        const { label, path } = item;
        if (typeof path !== 'string' || !HUB_NAV_PATH.test(path)) continue;
        if (typeof label !== 'string') continue;
        const l = label.trim();
        const chars = [...l].length;
        if (chars < 1 || chars > HUB_NAV_MAX_LABEL) continue;
        out.push({ label: l, path });
    }
    return out;
}

/** Render links into nav (an element): plain same-tab anchors, text only. */
 function renderHubNav(nav, links) {
    if (!nav) return;
    nav.replaceChildren(...links.map(({ label, path }) => {
        const a = document.createElement('a');
        a.className = 'hub-nav-link';
        a.href = path;
        a.textContent = label;
        return a;
    }));
    nav.hidden = links.length === 0;
}

let _loaded = null;

/** Show the hub's links in the Servers tab (once per page; only when hub-served). */
export function loadHubNav() {
    const nav = document.getElementById('hub-nav');
    if (!nav || !isCloud()) { if (nav) nav.hidden = true; return Promise.resolve([]); }
    if (!_loaded) {
        _loaded = (async () => {
            try {
                const r = await fetch(HUB_NAV_URL, { credentials: 'same-origin', cache: 'no-store', redirect: 'error' });
                if (!r.ok) return [];
                const len = Number(r.headers.get('Content-Length'));
                if (Number.isFinite(len) && len > HUB_NAV_MAX_BYTES) return [];
                return parseHubNav(await r.text());
            } catch { return []; }
        })();
    }
    return _loaded.then(links => { renderHubNav(nav, links); return links; });
}

/** Tests: forget the loaded list. */
export function _resetHubNavForTests() { _loaded = null; }
