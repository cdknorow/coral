/* Moved out of render.js as it was (#3795): no behaviour change. */
import { redrawLiveList, liveListEl } from './list_hooks.js';
import { state } from './state.js';
import { getListComparator } from './agent_filter.js';

/** The board whose chat the panel currently shows (or null). */
   // older messages not loaded yet
// The board the message state above belongs to, and whether its latest page has loaded. The state
// outlives the panel, so switching between agents of one board doesn't load the page again (#3722).
   // the panel generation whose load is in flight (0: none)
  // a load was asked for while one was in flight (after a send)
// Loads what's new on the board into the panel. Once the latest page is in, the board is asked
// only for the messages after the newest one shown (after_id), so a steady poll costs one small
// request that returns nothing (#3722). The latest page is loaded again when the total says
// messages were deleted, or a page or more arrived at once.
// Board chat @mention autocomplete
// ── Session ordering ──────────────────────────────────────────────────
function _getSessionOrder() {
    try {
        return JSON.parse(state.settings.session_order || "[]");
    } catch { return []; }
}

export function _sortByOrder(sessions) {
    const cmp = getListComparator(); // a sort chosen on the list bar orders the rows; with none, the order they were dragged to
    if (cmp) return [...sessions].sort(cmp);
    const order = _getSessionOrder();
    if (!order.length) return sessions;
    const posMap = {};
    order.forEach((sid, i) => { posMap[sid] = i; });
    return [...sessions].sort((a, b) => {
        const pa = posMap[a.session_id] ?? 9999;
        const pb = posMap[b.session_id] ?? 9999;
        return pa - pb;
    });
}

export async function _saveSessionOrder(orderedIds) {
    state.settings.session_order = JSON.stringify(orderedIds);
    try {
        await fetch("/api/settings", {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ session_order: state.settings.session_order }),
        });
    } catch (e) {
        console.error("Failed to save session order:", e);
    }
}

// ── Group ordering ───────────────────────────────────────────────────

export function _getGroupOrder() {
    try {
        return JSON.parse(state.settings.group_order || "[]");
    } catch { return []; }
}

export function _sortGroups(groupEntries) {
    const order = _getGroupOrder();
    if (!order.length) return groupEntries;
    const posMap = {};
    order.forEach((name, i) => { posMap[name] = i; });
    return [...groupEntries].sort((a, b) => {
        const pa = posMap[a[0]] ?? 9999;
        const pb = posMap[b[0]] ?? 9999;
        return pa - pb;
    });
}

async function _saveGroupOrder(orderedNames) {
    state.settings.group_order = JSON.stringify(orderedNames);
    try {
        await fetch("/api/settings", {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ group_order: state.settings.group_order }),
        });
    } catch (e) {
        console.error("Failed to save group order:", e);
    }
}

function _getCurrentGroupNames() {
    // Get current group names from the rendered sidebar
    const headers = document.querySelectorAll('.session-group-header[data-group-name]');
    return Array.from(headers).map(el => el.dataset.groupName);
}

export async function moveGroupUp(groupName) {
    const names = _getCurrentGroupNames();
    const idx = names.indexOf(groupName);
    if (idx <= 0) return;
    [names[idx - 1], names[idx]] = [names[idx], names[idx - 1]];
    await _saveGroupOrder(names);
    redrawLiveList();
}

export async function moveGroupDown(groupName) {
    const names = _getCurrentGroupNames();
    const idx = names.indexOf(groupName);
    if (idx < 0 || idx >= names.length - 1) return;
    [names[idx], names[idx + 1]] = [names[idx + 1], names[idx]];
    await _saveGroupOrder(names);
    redrawLiveList();
}

// ── Session reorder (Move Up/Down) ───────────────────────────────────

export function moveSessionUp(sessionId) {
    _moveSession(sessionId, -1);
}

export function moveSessionDown(sessionId) {
    _moveSession(sessionId, 1);
}

function _moveSession(sessionId, direction) {
    const list = liveListEl();
    if (!list) return;
    const items = [...list.querySelectorAll(".session-group-item")];
    const ids = items.map(el => el.dataset.sessionId);
    const idx = ids.indexOf(sessionId);
    if (idx < 0) return;
    const targetIdx = idx + direction;
    if (targetIdx < 0 || targetIdx >= ids.length) return;
    [ids[idx], ids[targetIdx]] = [ids[targetIdx], ids[idx]];
    _saveSessionOrder(ids);
    redrawLiveList();
}
