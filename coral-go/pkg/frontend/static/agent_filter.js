/* The sidebar's agent search text (#3836). It lived in render.js as a mutable let, which kept every section that reads it (group
 * collapse, the pinned-folder filter) inside render.js. Other modules ask through these accessors instead. */

let _agentFilter = '';

/** The lower-cased, trimmed search text ('' when no search is on). */
export function getAgentFilter() { return _agentFilter; }

export function setAgentFilter(value) { _agentFilter = value; }

/** True while the sidebar search has text. */
export function isAgentFiltering() { return !!_agentFilter; }
