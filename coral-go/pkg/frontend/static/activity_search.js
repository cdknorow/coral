/* The live Activity panel's search (#4785): the shared event search (event_search.js) over GET /api/sessions/live/{name}/events, kept per agent. With nothing asked for the panel is the live
 * feed, as before; a search shows its results in place of it and goes when the agent on screen does. */
import { state } from './state.js';
import { serverFetch, serverForSession } from './server_base.js';
import { createEventSearch } from './event_search.js';

const current = () => state.currentSession;

const search = createEventSearch({
    idp: 'ev', hostId: 'event-search-bar', listId: 'agentic-state-events',
    key: () => { const s = current(); return s && s.name ? `${s.name}:${s.session_id || ''}` : ''; },
    hidden: () => (state.eventFiltersHidden ||= new Set()),
    sessionId: () => (current() && current().session_id) || '',
    request: params => { const s = current(); return serverFetch(serverForSession(s.name, s.session_id || ''), `/api/sessions/live/${encodeURIComponent(s.name)}/events?${params}`); },
});

/** Put the bar in the Activity panel for the current agent. Returns true when the results are on screen, so the caller leaves the list alone; redraw shows the live feed again, once cleared. */
export const syncActivitySearch = search.sync;
