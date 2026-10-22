/* Advanced search filter state and URL serialization */

import { note, currentRoute } from './nav.js';

export const filterState = {
    q: '',
    ftsMode: 'or',        // 'phrase' | 'and' | 'or'; broad matches are ranked by relevance
    chatType: 'all',         // 'all' | 'agent' | 'group'
    tagIds: [],              // array of int
    tagLogic: 'AND',         // 'AND' | 'OR'
    sourceTypes: [],         // array of string; empty = all
    dateFrom: '',            // 'YYYY-MM-DD' or ''
    dateTo: '',              // 'YYYY-MM-DD' or ''
    minDurationSec: null,    // int or null
    maxDurationSec: null,    // int or null
};

export function buildApiParams(page, pageSize) {
    const p = new URLSearchParams({ page, page_size: pageSize });
    if (filterState.chatType && filterState.chatType !== 'all')
        p.set('type', filterState.chatType);
    if (filterState.q)
        p.set('q', filterState.q);
    if (filterState.q && filterState.ftsMode !== 'or')
        p.set('fts_mode', filterState.ftsMode);
    if (filterState.tagIds.length)
        p.set('tag_ids', filterState.tagIds.join(','));
    if (filterState.tagIds.length > 1)
        p.set('tag_logic', filterState.tagLogic);
    if (filterState.sourceTypes.length)
        p.set('source_types', filterState.sourceTypes.join(','));
    if (filterState.dateFrom)
        p.set('date_from', filterState.dateFrom);
    if (filterState.dateTo)
        p.set('date_to', filterState.dateTo);
    if (filterState.minDurationSec != null)
        p.set('min_duration_sec', String(filterState.minDurationSec));
    if (filterState.maxDurationSec != null)
        p.set('max_duration_sec', String(filterState.maxDurationSec));
    return p;
}

// The query keys the history search owns in the page's URL (#3529).
const URL_FILTER_KEYS = ['page', 'page_size', 'type', 'q', 'fts_mode', 'tag_ids', 'tag_logic', 'source_types',
    'date_from', 'date_to', 'min_duration_sec', 'max_duration_sec'];

/** The page's query string for this search state: only what differs from the defaults (no
 *  page 1, never page_size, no empty filter), the page's other query keys kept, and '' when
 *  nothing is left, so a plain dashboard URL has no ?page=1&page_size=50 (#3529). */
export function urlSearchFor(page, existingSearch) {
    const out = new URLSearchParams(existingSearch || '');
    for (const k of URL_FILTER_KEYS) out.delete(k);
    const state = buildApiParams(page, 50);
    state.delete('page_size');
    if (!(Number(page) > 1)) state.delete('page');
    for (const [k, v] of state) out.set(k, v);
    const qs = out.toString();
    return qs ? '?' + qs : '';
}

/** Write the search to the Chats list's URL (a replace: filters refine the page, they are not a
 *  new one). Only while the Chats list is the page: elsewhere the query is not the view's (#3532). */
export function serializeToUrl(page) {
    const r = currentRoute();
    if (!r || r.view !== 'chats' || r.id) return;
    note({ view: 'chats', ...(r.server ? { server: r.server } : {}), query: urlSearchFor(page, '').replace(/^\?/, '') }, { replace: true });
}

export function deserializeFromUrl() {
    const p = new URLSearchParams(window.location.search);
    filterState.q = p.get('q') || '';
    filterState.chatType = ['all', 'agent', 'group'].includes(p.get('type'))
        ? p.get('type') : 'all';
    filterState.ftsMode = ['phrase', 'and', 'or'].includes(p.get('fts_mode'))
        ? p.get('fts_mode') : 'or';
    filterState.tagIds = (p.get('tag_ids') || '')
        .split(',').filter(Boolean).map(Number).filter(n => !isNaN(n));
    filterState.tagLogic = p.get('tag_logic') === 'OR' ? 'OR' : 'AND';
    filterState.sourceTypes = (p.get('source_types') || '')
        .split(',').filter(Boolean);
    filterState.dateFrom = p.get('date_from') || '';
    filterState.dateTo = p.get('date_to') || '';
    filterState.minDurationSec = p.has('min_duration_sec')
        ? parseInt(p.get('min_duration_sec')) : null;
    filterState.maxDurationSec = p.has('max_duration_sec')
        ? parseInt(p.get('max_duration_sec')) : null;

    // Restore page number (default 1)
    const pageVal = parseInt(p.get('page'));
    return { filterState, page: isNaN(pageVal) || pageVal < 1 ? 1 : pageVal };
}

export function resetFilters() {
    filterState.q = '';
    filterState.chatType = 'all';
    filterState.ftsMode = 'or';
    filterState.tagIds = [];
    filterState.tagLogic = 'AND';
    filterState.sourceTypes = [];
    filterState.dateFrom = '';
    filterState.dateTo = '';
    filterState.minDurationSec = null;
    filterState.maxDurationSec = null;
}

export function hasActiveFilters() {
    return (filterState.chatType && filterState.chatType !== 'all')
        || filterState.tagIds.length > 0
        || filterState.sourceTypes.length > 0
        || !!filterState.dateFrom
        || !!filterState.dateTo
        || filterState.minDurationSec != null
        || filterState.maxDurationSec != null;
}

export function countActiveFilters() {
    let n = 0;
    if (filterState.chatType && filterState.chatType !== 'all') n++;
    if (filterState.tagIds.length) n++;
    if (filterState.sourceTypes.length) n++;
    if (filterState.dateFrom || filterState.dateTo) n++;
    if (filterState.minDurationSec != null || filterState.maxDurationSec != null) n++;
    return n;
}
