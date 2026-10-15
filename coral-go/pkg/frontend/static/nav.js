/* The dashboard's one owner of browser history (#3532). Every entry it writes carries
   { coralRoute } in history.state; views register a handler per route view (onRoute) and call
   go() (a push: a tab or an object chosen) or go(route, {replace: true}) (a refinement: filters,
   paging). One popstate listener shows the route of the entry the user went back or forward to.
   Paths come from routes.js (parse/build); old hash links are read once at startup and replaced. */

import { parse, build, fromLegacyHash, normalize, chatsQuery } from './routes.js';

const handlers = new Map();
let current = null;
let started = false;

/** Register the function that shows a route of this view: fn(route, { fromHistory }). */
export function onRoute(view, fn) { handlers.set(view, fn); }

/** The route on screen. */
export function currentRoute() { return current; }

function write(route, replace) {
    const url = build(route);
    const st = { coralRoute: route };
    if (replace || url === location.pathname + location.search) history.replaceState(st, '', url);
    else history.pushState(st, '', url);
    current = route;
}

/** Go to a route: write it to history (a push unless replace) and show it. An invalid route
 *  goes home. */
export function go(route, { replace = false } = {}) {
    route = normalize(route) || { view: 'agents' };
    write(route, replace);
    return show(route, { fromHistory: false });
}

/** Record the route of a view that is already on screen (no show): a view the user opened by
 *  clicking calls this, so the click and Back stay in step without showing it twice. */
export function note(route, { replace = false } = {}) {
    route = normalize(route);
    if (!route) return;
    const same = current && build(current) === build(route);
    write(route, replace || same);
}

async function show(route, opts) {
    const fn = handlers.get(route.view);
    if (!fn) return;
    try { await fn(route, opts); } catch (e) { console.error('route', build(route), e); }
}

/** The route the page was opened at: an old hash link, or Chats filters left on "/" by an
 *  older Coral, become their path; anything else is parsed; an invalid path is home. */
export function startRoute(loc = location) {
    if (loc.hash && loc.hash !== '#') {
        const legacy = normalize(fromLegacyHash(loc.hash));
        if (legacy) return legacy;
    }
    if ((loc.pathname === '/' || loc.pathname === '') && chatsQuery(loc.search)) {
        return normalize({ view: 'chats', query: chatsQuery(loc.search) }) || { view: 'agents' };
    }
    return parse(loc.pathname, loc.search) || { view: 'agents' };
}

/** Start: write the start route as the current entry (history.state set even when the page was
 *  opened with a hash), listen for Back and Forward, and show it. Returns the route. */
export function startNav() {
    if (started) return current;
    started = true;
    const route = startRoute();
    write(route, true);
    window.addEventListener('popstate', () => {
        // The URL is the truth: the route is parsed from the location every time, never taken
        // from history.state, which may come from an older Coral, a restored session or a
        // tampered entry (Security; an old-shape state would otherwise read as a different view).
        const r = startRoute();
        current = r;
        show(r, { fromHistory: true });
    });
    show(route, { fromHistory: true, startup: true });
    return route;
}
