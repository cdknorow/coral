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

// Each entry also records the URL of the entry it was pushed from (prev), so a "back" control
// can go back in history when that is where it leads, instead of pushing a copy of it.
function write(route, replace) {
    const url = build(route);
    const here = location.pathname + location.search;
    if (replace || url === here) {
        const prev = history.state && typeof history.state.prev === 'string' ? history.state.prev : null;
        history.replaceState({ coralRoute: route, prev }, '', url);
    } else {
        history.pushState({ coralRoute: route, prev: here }, '', url);
    }
    current = route;
}

/** Whether the entry before this one (the one Back returns to) is this route's page. */
export function backIs(route) {
    const r = normalize(route);
    return !!r && !!history.state && history.state.prev === build(r);
}

/** Go to a route: write it to history (a push unless replace) and show it. An invalid route
 *  goes home. */
export function go(route, { replace = false } = {}) {
    route = normalize(route) || { view: 'agents' };
    write(route, replace);
    return show(route, { fromHistory: false }).finally(routeChanged);
}

/** Record the route of a view that is already on screen (no show): a view the user opened by
 *  clicking calls this, so the click and Back stay in step without showing it twice. Nothing is
 *  written before startNav(): the pop-out (/agent/<id>) never starts it, and its URL is its
 *  identity, so the views it shares with the dashboard can't rewrite it. */
export function note(route, { replace = false } = {}) {
    try {
        if (!started) return;
        route = normalize(route);
        if (!route) return;
        const same = current && build(current) === build(route);
        write(route, replace || same);
    } finally {
        routeChanged(); // what is on screen changed whether or not a history entry was written (the pop-out writes none)
    }
}

/** Tells the modules that follow what is on screen (the Saved list and its count are of the agent or team shown, #4792) that the route changed; they read the page's own state. */
function routeChanged() { if (typeof document === 'undefined') return; const w = document.defaultView || window; document.dispatchEvent(new w.CustomEvent('coral:route-changed')); } // the page's own CustomEvent (a test document has its own)

async function show(route, opts) {
    const fn = handlers.get(route.view);
    if (!fn) return;
    try { await fn(route, opts); } catch (e) { console.error('route', build(route), e); }
}

/** The route the page was opened at: an old hash link, or Chats filters left on "/" by an
 *  older Coral, become their path; anything else is parsed; an invalid path is home. */
export function startRoute(loc = location) {
    if (loc.hash && loc.hash !== '#') {
        const old = fromLegacyHash(loc.hash);
        // "/?q=x#history": the Chats list with the filters an older Coral kept in the query.
        if (old && old.view === 'chats' && !old.id) old.query = chatsQuery(loc.search);
        const legacy = normalize(old);
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
        // Old hash links are read only at the first load (startRoute): after that a hash is a
        // fragment, never a route, so a #team-view=x link clicked in a post can't drive the
        // dashboard (Security, #3532). Only the path and query are read here, and a change that
        // leaves them as they are (a fragment click) shows nothing new.
        const r = parse(location.pathname, location.search) || { view: 'agents' };
        if (current && build(r) === build(current)) return;
        current = r;
        show(r, { fromHistory: true }).finally(routeChanged);
    });
    show(route, { fromHistory: true, startup: true });
    return route;
}
