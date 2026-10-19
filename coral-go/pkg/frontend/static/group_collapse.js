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

/** Collapse every team and folder group in the list, or open them all (the header's one toggle). A group the person
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

const GROUP_HEADERS = '.session-group-header[data-group-name]';

/** Whether the list has groups, and whether every one of them is closed (read off the headers as drawn, which already account for a search that opens them). */
function groupState() {
    const headers = [...document.querySelectorAll(GROUP_HEADERS)];
    return { any: headers.length > 0, allClosed: headers.length > 0 && headers.every(h => h.getAttribute('aria-expanded') === 'false') };
}

/** The one Collapse / Expand toggle in the list header (#4947): closes every group, or, when they are all closed already, opens them. */
export function toggleAllGroups() { setAllGroupsCollapsed(!groupState().allClosed); }

/** Called after every render of the list: the header toggle is there while the list has groups, and says (name, icon) what it does next. */
export function syncGroupToggle() {
    const { any, allClosed } = groupState();
    for (const b of document.querySelectorAll('[data-group-toggle]')) {
        b.hidden = !any;
        const label = allClosed ? 'Expand all groups' : 'Collapse all groups';
        b.title = label; b.setAttribute('aria-label', label);
        b.dataset.next = allClosed ? 'expand' : 'collapse';
    }
}

export function toggleGroupCollapse(groupName) {
    const groups = _getCollapsedGroups();
    const idx = groups.indexOf(groupName);
    if (idx >= 0) groups.splice(idx, 1);
    else groups.push(groupName);
    browserLocal.set('coral_collapsed_groups', JSON.stringify(groups));
    redrawLiveList();
}
