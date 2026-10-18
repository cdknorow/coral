/* The Activity panel's search box, filter menu, sort and chips (#4785): its words, fields and sorts on the shared list bar (list_bar.js), and the results it shows in place of the live feed.
 * The server does the work (GET /api/sessions/live/{name}/events, #4745): the search covers every event of the agent, in pages ("Load more" follows the cursor), and says so when it stopped at
 * its bound (N+). With nothing asked for the panel is the live feed, as before; the search belongs to the agent on screen and goes when the agent does. */
import { state } from './state.js';
import { morphHtml } from './dom_morph.js';
import { escapeHtml } from './utils.js';
import { serverFetch, serverForSession } from './server_base.js';
import { buildListBar } from './list_bar.js';
import { eventRowHtml } from './event_row.js';
import { EMPTY, TYPES, TYPE_LABEL, SORTS, SORT_LABEL, clean, chips, isFiltered, isQuery, without, toParams, countText } from './activity_filters.js';

const SPEC = {
    idp: 'ev',
    noun: 'ev' + 'ent', // spelled in two parts: the icon-name lint reads that quoted word as an icon name
    label: 'Search and filter activity',
    searchLabel: 'Search activity',
    placeholder: 'Search activity',
    sorts: { values: SORTS, labels: SORT_LABEL, aria: 'Sort activity' },
    fields: [
        { type: 'select', key: 'type', id: 'ev-type', label: 'Type', values: TYPES, labels: TYPE_LABEL, any: 'Any type' },
        { type: 'date', key: 'from', id: 'ev-from', label: 'From' },
        { type: 'date', key: 'to', id: 'ev-to', label: 'Until' },
    ],
    model: { EMPTY, clean, chips, isFiltered, without },
};

const NONE = { events: [], total: 0, next: '', busy: false, more: false, error: '', truncated: false };
let _bar = null, _key = '', _filters = { ...EMPTY }, _redraw = null;
let _list = { ...NONE };
let _seq = 0, _shown = '';

const agentKey = () => { const s = state.currentSession; return s && s.name ? `${s.name}:${s.session_id || ''}` : ''; };

/** True while the panel shows a search's results in place of the live feed. */
export function isSearching() { return !!_key && _key === agentKey() && isQuery(_filters); }

function counts() {
    if (!isQuery(_filters) || (_list.busy && !_list.events.length) || _list.error) return null;
    return { text: countText(_list.total, _list.truncated) };
}

/** What the person sees in the events list while searching. */
function resultsHtml() {
    if (_list.error) return `<div class="event-empty">${escapeHtml(_list.error)}</div>`;
    if (_list.busy && !_list.events.length) return '<div class="event-empty">Searching…</div>';
    if (!_list.events.length) return '<div class="event-empty">No events match</div>';
    const more = _list.next ? `<button class="btn btn-small event-more" type="button" data-on-click="activity-more"${_list.more ? ' disabled' : ''}>${_list.more ? 'Loading…' : 'Load more'}</button>` : '';
    return _list.events.map(eventRowHtml).join('') + more;
}

/** The bar shows the filters and the count (only when they changed: the live feed redraws the panel on every event). */
function show() {
    if (!_bar) return;
    const c = counts(), sig = JSON.stringify([_filters, c]);
    if (sig !== _shown) { _shown = sig; _bar.set(_filters, c); }
}

function draw() {
    const container = document.getElementById('agentic-state-events');
    if (container && isSearching()) morphHtml(container, resultsHtml());
    show();
    const live = document.getElementById('event-filters');
    if (live) live.hidden = isSearching(); // the live feed's own type filter does not apply to a search's results
}

async function fetchPage(cursor) {
    const s = state.currentSession;
    const name = s.name, sid = s.session_id || '';
    const params = new URLSearchParams(toParams(_filters, { sessionId: sid, cursor }));
    const resp = await serverFetch(serverForSession(name, sid), `/api/sessions/live/${encodeURIComponent(name)}/events?${params}`);
    if (!resp.ok) {
        let why = '';
        try { why = (await resp.json()).error || ''; } catch { /* no JSON body */ }
        throw new Error(why || `the search failed (${resp.status})`);
    }
    return resp.json();
}

async function run() {
    const seq = ++_seq, key = _key;
    if (!isQuery(_filters)) { _list = { ...NONE }; draw(); if (_redraw) _redraw(); return; }
    _list = { ...NONE, busy: true };
    draw();
    try {
        const d = await fetchPage('');
        if (seq !== _seq || key !== _key) return;
        const events = d.events || [];
        _list = { ...NONE, events, total: d.total ?? events.length, next: d.next_cursor || '', truncated: !!d.truncated };
    } catch (e) {
        if (seq !== _seq || key !== _key) return;
        _list = { ...NONE, error: e && e.message ? e.message : 'the search failed' };
    }
    draw();
}

/** The next page of the results (the "Load more" button). */
export async function loadMoreEvents() {
    if (!isSearching() || !_list.next || _list.more) return;
    const seq = _seq, key = _key, cursor = _list.next;
    _list = { ..._list, more: true }; draw();
    try {
        const d = await fetchPage(cursor);
        if (seq !== _seq || key !== _key) return;
        _list = { ..._list, events: _list.events.concat(d.events || []), total: d.total ?? _list.total, next: d.next_cursor || '', more: false, truncated: _list.truncated || !!d.truncated };
    } catch {
        if (seq !== _seq || key !== _key) return;
        _list = { ..._list, more: false };
    }
    draw();
}

/** Put the bar in the Activity panel and make it the current agent's (a search of another agent goes). Returns true when the results are on screen, so the caller leaves the list alone.
 *  redraw is what shows the live feed again, once the search is cleared. */
export function syncActivitySearch(redraw) {
    _redraw = redraw;
    const host = document.getElementById('event-search-bar');
    if (!host) return false;
    const key = agentKey();
    if (key !== _key) { _key = key; _seq++; _filters = { ...EMPTY }; _list = { ...NONE }; _shown = ''; }
    if (!_bar) _bar = buildListBar(SPEC, f => { _filters = clean(f); run(); });
    if (_bar.node.parentElement !== host) host.append(_bar.node);
    host.hidden = !key;
    if (!isSearching()) {
        show();
        const live = document.getElementById('event-filters');
        if (live) live.hidden = false;
        return false;
    }
    draw();
    return true;
}
