// The history search's state in the dashboard URL (#3529): only what differs from the defaults,
// the page's other query keys kept, and old URLs (with ?page=1&page_size=50) still read.
import assert from 'node:assert/strict';
import { filterState, urlSearchFor, resetFilters, deserializeFromUrl } from '../../coral-go/pkg/frontend/static/search_filters.js';

resetFilters();
assert.equal(urlSearchFor(1, ''), '', 'a plain dashboard has no query at all');
assert.equal(urlSearchFor(1, '?page=1&page_size=50'), '', 'an old default query is cleaned away');
assert.equal(urlSearchFor(2, ''), '?page=2', 'a later page is kept, page_size never');
assert.equal(urlSearchFor(1, '?view=team&page=1&page_size=50'), '?view=team', "the page's other keys stay");

filterState.q = 'battle log';
filterState.chatType = 'agent';
filterState.tagIds = [3, 7];
filterState.tagLogic = 'OR';
const qs = urlSearchFor(3, '?view=team');
const p = new URLSearchParams(qs);
assert.deepEqual(Object.fromEntries(p), { view: 'team', page: '3', type: 'agent', q: 'battle log', tag_ids: '3,7', tag_logic: 'OR' }, qs);
assert.ok(!p.has('page_size'));

// It round-trips through the URL, and an old URL still reads.
globalThis.window = { location: { search: qs } };
resetFilters();
let r = deserializeFromUrl();
assert.equal(r.page, 3);
assert.equal(filterState.q, 'battle log'); assert.equal(filterState.chatType, 'agent');
assert.deepEqual(filterState.tagIds, [3, 7]); assert.equal(filterState.tagLogic, 'OR');
globalThis.window = { location: { search: '?page=1&page_size=50' } };
r = deserializeFromUrl();
assert.equal(r.page, 1); assert.equal(filterState.q, '');
console.log('history_url_state: ok');
