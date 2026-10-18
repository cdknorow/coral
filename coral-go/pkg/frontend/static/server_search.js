/* The Servers list's search box (#4778): the shared list bar with only a search and a count, above the list, shown from six machines up (below that the whole list is on one screen).
 * The two lists (servers.js on a self-hosted hub, cloud_servers.js on the hosted one) call syncServersBar(all, noun) as they draw and keep what serverVisible says. The list is the one the
 * tab holds: nothing is asked of the server. */
import { buildListBar } from './list_bar.js';
import { EMPTY, clean, chips, isFiltered, without, matches, countText } from './server_filters.js';

const MANY = 5;
const SPEC = {
    idp: 'sb', noun: 'server', label: 'Search the list', searchLabel: 'Search the list', placeholder: 'Search by name or address', sorts: null, fields: [],
    model: { EMPTY, clean, chips, isFiltered, without },
};

let _bar = null, _filters = { ...EMPTY }, _redraw = null;

/** Whether a server stays in the list for the search in the box. */
export const serverVisible = s => matches(s, _filters);
export const serversFiltered = () => isFiltered(_filters);

/** Called by each draw of a list: the box exists when there are many servers, says how many match, and redraws the list when the text changes. */
export function syncServersBar(all, noun, redraw) {
    const host = document.getElementById('servers-bar');
    if (!host) return;
    _redraw = redraw;
    if (all.length <= MANY && !isFiltered(_filters)) { if (_bar && _bar.node.parentElement === host) _bar.node.remove(); host.hidden = true; return; }
    host.hidden = false;
    if (!_bar) _bar = buildListBar(SPEC, next => { _filters = clean(next); if (_redraw) _redraw(); });
    if (_bar.node.parentElement !== host) host.append(_bar.node);
    const shown = all.filter(serverVisible).length;
    _bar.set(_filters, { text: countText(shown, all.length, noun, isFiltered(_filters)) });
}

/** Leaving the tab: the box and its text go (a search is not kept). */
export function resetServersBar() {
    _filters = { ...EMPTY };
    if (_bar) { _bar.dispose(); _bar = null; }
    const host = document.getElementById('servers-bar');
    if (host) host.hidden = true;
}
