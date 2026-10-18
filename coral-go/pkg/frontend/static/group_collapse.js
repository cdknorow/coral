/* Moved out of render.js (#3836): group collapse. It asks agent_filter.js whether a search is on, rather than reading render.js's
 * mutable filter. */
import { escapeAttr } from './utils.js';
import { redrawLiveList } from './list_hooks.js';
import { isAgentFiltering } from './agent_filter.js';
import { browserLocal } from './safe_storage.js';

// ── Group collapse state ─────────────────────────────────────────────
function _getCollapsedGroups() {
    try { return JSON.parse(browserLocal.get('coral_collapsed_groups') || '[]'); }
    catch { return []; }
}

export function _isGroupCollapsed(groupName) {
    // While searching, matches inside a collapsed group must stay visible.
    if (isAgentFiltering()) return false;
    return _getCollapsedGroups().includes(groupName);
}

/** A team or folder header collapses its group when clicked; without a chevron (#3507 v4),
 *  it says so to keyboard and screen-reader users: a button with aria-expanded, toggled with
 *  Enter or Space. */
export function _collapseAttrs(collapsed) {
    return ` role="button" tabindex="0" aria-expanded="${escapeAttr(collapsed ? 'false' : 'true')}" data-on-keydown="group-header-key"`;
}

/** Enter or Space on a focused team or folder header toggles its group (not a key meant for a
 *  button inside the header). */
// header: the group header the key went to. The delegated action passes it (#3645): under one
// document listener, event.currentTarget is the document, not the header.
export function groupHeaderKey(event, header = event.currentTarget) {
    if (event.target !== header || (event.key !== 'Enter' && event.key !== ' ')) return;
    event.preventDefault();
    toggleGroupCollapse(header.dataset.groupName);
}

/** Collapse every team and folder group in the list, or open them all: the two buttons of the Display options panel. A group the person
 *  opened or closed by hand keeps its state until one of these is used; the result is stored the way a single collapse is. */
export function setAllGroupsCollapsed(collapsed) {
    if (!collapsed) browserLocal.set('coral_collapsed_groups', '[]');
    else {
        const names = new Set(_getCollapsedGroups());
        for (const h of document.querySelectorAll('.session-group-header[data-group-name]')) names.add(h.dataset.groupName);
        browserLocal.set('coral_collapsed_groups', JSON.stringify([...names]));
    }
    redrawLiveList();
}

export function toggleGroupCollapse(groupName) {
    const groups = _getCollapsedGroups();
    const idx = groups.indexOf(groupName);
    if (idx >= 0) groups.splice(idx, 1);
    else groups.push(groupName);
    browserLocal.set('coral_collapsed_groups', JSON.stringify(groups));
    redrawLiveList();
}
