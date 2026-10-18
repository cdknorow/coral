/* The search box, filter menu, sort and chips of an events list (#4785): its words, fields and sorts on the shared list bar (list_bar.js), and the results it shows in place of the list. The live
 * Activity panel (activity_search.js) and a past session's Activity in History (history_events_search.js) are two of it: the same search (GET .../events with type, q, since, until, sort and
 * cursor: #4745, #4779), the same words, a bar of each with its own ids. The server does the work: the search covers every event, in pages ("Load more" follows the cursor), and says so when it
 * stopped at its bound (N+). With nothing asked for the list is what it was (the live feed, the session's last events); the search belongs to the one on screen and goes when it does.
 *
 *   createEventSearch({ idp, hostId, listId, key(), request(params), sessionId(), hidden() })
 *     key()      who is on screen now ('' for nobody): another one starts with no search
 *     request()  the GET for these URLSearchParams (the caller knows the route and the server)
 *     sessionId() the session_id parameter for the request ('' for none)
 *     hidden()   the Set of tool-group keys the list hides (Read, Bash ...): it stays where the page keeps it, the bar shows it and changes it in place. Tool groups narrow the rows loaded, the server is not asked.
 *   returns { isSearching(), sync(redraw), loadMore() }. */
import { morphHtml } from './dom_morph.js';
import { escapeHtml, escapeAttr } from './utils.js';
import { registerActions } from './actions.js';
import { buildListBar } from './list_bar.js';
import { eventRowHtml } from './event_row.js';
import { FILTER_GROUPS, eventVisible } from './event_icons.js';
import { EMPTY, TYPES, TYPE_LABEL, SORTS, SORT_LABEL, clean, chips, isFiltered, isQuery, without, toParams, countText, groupField, hiddenGroups, withHidden, queryKey } from './activity_filters.js';

const SEARCHES = new Map(); // idp -> its search, for the Load more button (one action serves every list)
registerActions({
    'event-search-more': el => { const s = SEARCHES.get(el.dataset.search); if (s) s.loadMore(); },
});

const NONE = { events: [], total: 0, next: '', busy: false, more: false, error: '', truncated: false };

export function createEventSearch(cfg) {
    const spec = {
        idp: cfg.idp,
        noun: 'ev' + 'ent', // spelled in two parts: the icon-name lint reads that quoted word as an icon name
        label: 'Search and filter activity',
        searchLabel: 'Search activity',
        placeholder: 'Search activity',
        sorts: { values: SORTS, labels: SORT_LABEL, aria: 'Sort activity' },
        fields: [
            { type: 'select', key: 'type', id: `${cfg.idp}-type`, label: 'Type', values: TYPES, labels: TYPE_LABEL, any: 'Any type' },
            { type: 'date', key: 'from', id: `${cfg.idp}-from`, label: 'From' },
            { type: 'date', key: 'to', id: `${cfg.idp}-to`, label: 'Until' },
            // the tool groups, one checkbox each (checked: that kind of event shows)
            ...FILTER_GROUPS.map(g => ({ type: 'check', key: groupField(g.key), id: `${cfg.idp}-g-${g.key}`, label: `Show ${g.title}`, wrapClass: 'list-bar-field list-bar-kept' })),
        ],
        model: { EMPTY, clean, chips, isFiltered, without },
    };
    let bar = null, key = '', filters = { ...EMPTY }, redraw = null, list = { ...NONE }, seq = 0, shown = '';

    const searching = () => !!key && key === cfg.key() && isQuery(filters);

    function counts() {
        if (!isQuery(filters) || (list.busy && !list.events.length) || list.error) return null;
        return { text: countText(list.total, list.truncated) };
    }

    /** What the person sees in the events list while searching. */
    function resultsHtml() {
        if (list.error) return `<div class="event-empty">${escapeHtml(list.error)}</div>`;
        if (list.busy && !list.events.length) return '<div class="event-empty">Searching…</div>';
        if (!list.events.length) return '<div class="event-empty">No events match</div>';
        const visible = list.events.filter(e => eventVisible(e, cfg.hidden()));
        if (!visible.length) return '<div class="event-empty">All events filtered out</div>';
        const more = list.next ? `<button class="btn btn-small event-more" type="button" data-on-click="event-search-more" data-search="${escapeAttr(cfg.idp)}"${list.more ? ' disabled' : ''}>${list.more ? 'Loading…' : 'Load more'}</button>` : '';
        return visible.map(eventRowHtml).join('') + more;
    }

    /** The bar shows the filters and the count (only when they changed: a live feed redraws the panel on every event). */
    function show() {
        if (!bar) return;
        filters = withHidden(filters, cfg.hidden()); // the page's Set is the truth for the tool groups
        const c = counts(), sig = JSON.stringify([filters, c]);
        if (sig !== shown) { shown = sig; bar.set(filters, c); }
    }

    function draw() {
        const container = document.getElementById(cfg.listId);
        if (container && searching()) morphHtml(container, resultsHtml());
        show();
    }

    async function fetchPage(cursor) {
        const params = new URLSearchParams(toParams(filters, { sessionId: cfg.sessionId(), cursor }));
        const resp = await cfg.request(params);
        if (!resp.ok) {
            let why = '';
            try { why = (await resp.json()).error || ''; } catch { /* no JSON body */ }
            throw new Error(why || `the search failed (${resp.status})`);
        }
        return resp.json();
    }

    async function run() {
        const mine = ++seq, who = key;
        if (!isQuery(filters)) { list = { ...NONE }; draw(); if (redraw) redraw(); return; }
        list = { ...NONE, busy: true };
        draw();
        try {
            const d = await fetchPage('');
            if (mine !== seq || who !== key) return;
            const events = d.events || [];
            list = { ...NONE, events, total: d.total ?? events.length, next: d.next_cursor || '', truncated: !!d.truncated };
        } catch (e) {
            if (mine !== seq || who !== key) return;
            list = { ...NONE, error: e && e.message ? e.message : 'the search failed' };
        }
        draw();
    }

    /** The bar changed: a tool group only narrows what is loaded (the page's Set is changed, the list drawn again); anything else is a new query. */
    function onBar(next) {
        const was = filters;
        filters = clean(next);
        const hidden = cfg.hidden();
        hidden.clear(); for (const k of hiddenGroups(filters)) hidden.add(k);
        if (queryKey(was) !== queryKey(filters)) { run(); return; }
        shown = '';
        if (searching()) draw(); else { show(); if (redraw) redraw(); }
    }

    /** The next page of the results (the "Load more" button). */
    async function loadMore() {
        if (!searching() || !list.next || list.more) return;
        const mine = seq, who = key, cursor = list.next;
        list = { ...list, more: true }; draw();
        try {
            const d = await fetchPage(cursor);
            if (mine !== seq || who !== key) return;
            list = { ...list, events: list.events.concat(d.events || []), total: d.total ?? list.total, next: d.next_cursor || '', more: false, truncated: list.truncated || !!d.truncated };
        } catch {
            if (mine !== seq || who !== key) return;
            list = { ...list, more: false };
        }
        draw();
    }

    /** Put the bar in its host and make it the one on screen's (a search of another goes). Returns true when the results are on screen, so the caller leaves the list alone.
     *  redraw is what shows the list again, once the search is cleared. */
    function sync(redrawList) {
        redraw = redrawList;
        const host = document.getElementById(cfg.hostId);
        if (!host) return false;
        const now = cfg.key();
        if (now !== key) { key = now; seq++; filters = withHidden({ ...EMPTY }, cfg.hidden()); list = { ...NONE }; shown = ''; }
        if (!bar) bar = buildListBar(spec, onBar);
        if (bar.node.parentElement !== host) host.append(bar.node);
        host.hidden = !now;
        if (!searching()) {
            show();
            return false;
        }
        draw();
        return true;
    }

    const api = { isSearching: searching, sync, loadMore };
    SEARCHES.set(cfg.idp, api);
    return api;
}
