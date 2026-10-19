/* Whether an agent or a pinned folder passes the agent list's search and filters (moved out of render.js, #4996; no behaviour change). */
import { getAgentFilter, getListFilters } from './agent_filter.js';
import { matches as _matchesListFilters, isFiltered as _listFiltered } from './agent_list_filters.js';
import { FILTER_CTX } from './agent_list_bar.js';
import { resolveSessionIdentity } from './session_state.js';

// Matches the agent's name only (what the row label shows), not its team, folder or goal.
// While the list is filtered, an empty pinned folder shows only when its name matches.
export function _pinMatchesFilter(p) {
    return !getAgentFilter() || getAgentFilter().split(/\s+/).every(term => String(p.name || '').toLowerCase().includes(term));
}

/** The list's search text and the list bar's State, Board and Machine filters (#4744), as one test. */
export function _passesFilters(s) {
    if (getAgentFilter() && !_agentMatches(s, getAgentFilter())) return false;
    const f = getListFilters();
    return !_listFiltered(f) || _matchesListFilters(s, f, FILTER_CTX);
}

function _agentMatches(s, q) {
    const names = [resolveSessionIdentity(s), s.display_name, s.auto_name, s.board_job_title]
        .filter(Boolean).join(' ').toLowerCase();
    return q.split(/\s+/).every(term => names.includes(term));
}
