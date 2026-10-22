import { historyFetch } from './history_source.js';
/* A past session's Activity in History: its search (#4785, on #4779): the shared event search (event_search.js) over GET /api/sessions/history/{id}/events, kept per session. With nothing asked
 * for the tab shows the session's last events, as before; a search shows its results in place of them and goes when another session is opened. */
import { createEventSearch } from './event_search.js';
import { historyEventsView } from './event_icons.js';

let sessionId = '';

const search = createEventSearch({
    idp: 'hev', hostId: 'history-event-search-bar', listId: 'history-events-list',
    key: () => sessionId,
    hidden: () => historyEventsView.hidden,
    sessionId: () => '', // the session is in the route
    request: params => historyFetch(`/api/sessions/history/${encodeURIComponent(sessionId)}/events?${params}`),
});

/** The session whose events are on screen (history_tabs.js loadHistoryEvents). */
export function setHistoryEventsSession(id) { sessionId = id || ''; }
export const syncHistoryEventSearch = search.sync;
