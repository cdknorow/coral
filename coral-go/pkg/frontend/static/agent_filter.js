/* The sidebar's agent search text (#3836). It lived in render.js as a mutable let, which kept every section that reads it (group
 * collapse, the pinned-folder filter) inside render.js. Other modules ask through these accessors instead. */

import { EMPTY, clean, isFiltered } from './agent_list_filters.js';

let _agentFilter = '';
let _listFilters = { ...EMPTY }; // the filters of the shared list bar (agent_list_bar.js, #4744): State, Board, Machine and the sort
let _comparator = null;       // the sort's comparator, null for the list's own order

/** The lower-cased, trimmed search text ('' when no search is on). */
export function getAgentFilter() { return _agentFilter; }

export function setAgentFilter(value) { _agentFilter = value; }

/** True while the sidebar search has text or a bar filter is on (a collapsed group shows its matches then). */
export function isAgentFiltering() { return !!_agentFilter || isFiltered(_listFilters); }

export function getListFilters() { return _listFilters; }
export function setListFilters(f) { _listFilters = clean(f); }
/** The comparator the chosen sort gives the rows of a group, or null (the order the person has dragged them to). */
export function getListComparator() { return _comparator; }
export function setListComparator(c) { _comparator = c || null; }
