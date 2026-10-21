/* Rendering functions for session lists, chat history, and status updates */

// Load order only (#4996): the imports below with no names are loaded for what the module registers at load, in the order this module imported them before the split (the startup-sequence golden records it).
import './model_labels.js';
import './import_once.js';
import { state } from './state.js';
import './settings_lists.js';
import './hub_access.js';
import { sessionServer, noteBoardLabels, sessionTeamKey } from './server_base.js';
import './sleeping_view.js';
import './knowledge_distill.js';
import './utils.js';
import { registerActions, idOf, tokenOf, textOf } from './actions.js';
import './live_chat.js';
import './tags.js';
import './folder_tags.js';
import { updateSectionVisibility } from './sidebar.js';
import { isStaleSession, serversShown, serverGroupHeaderHtml } from './server_status.js';
import { syncMobileAgentList } from './mobile.js';
import { refreshTeamAvailabilityIfChanged } from './team_availability.js';
import { liveListRendered } from './ready.js';
import { morphHtml } from './dom_morph.js';
import { pinnedFolders } from './pinned_folders.js';
import { LOCAL_SERVER } from './server_base.js';
import { machineState } from './launch_server.js';
import { syncHubFirstRun, hubEmptyListHtml } from './hub_first_run.js';
import './format.js';
import { registerLiveListRenderer, redrawLiveList, liveListEl } from './list_hooks.js';
import { _boardChatSelectAll, _boardChatSelectNone, _boardMentionInput, _boardMentionKey, _cancelBoardChatSelect, _exportBoardChatSelected, _insertBoardMention, _toggleBoardChatMsgSelect, _toggleBoardChatPause } from './board_chat.js';
import { buildSessionTooltip } from './session_state.js';
import './board_accents.js';
import { followServerOrder } from './list_order.js';
import { _isServerCollapsed, _openServerDetails } from './server_rows.js';
import { _attachDragListeners, _renderArchivedSection, _within, applySessionSummaryPref, isGroupedByServer } from './session_rows.js';
import './confirm_dialog.js';
import { getAgentFilter, setAgentFilter, getListFilters } from './agent_filter.js';
import { isFiltered as _listFiltered } from './agent_list_filters.js';
import { syncAgentListBars } from './agent_list_bar.js';
import { syncGroupToggle } from './group_collapse.js';
import { browserLocal } from './safe_storage.js';
import { getTeamTokenUsage } from './token_usage_ui.js';
import { _renderSidebarRail } from './sidebar_rail.js';
import { _passesFilters, _pinMatchesFilter } from './list_filter_match.js';
import { _renderGroups } from './group_cards.js';
import { UPDOWN, W, agentArgs, dirOf, menu, withAgent, withBoard } from './action_helpers.js';
export { KILL_LOG_NOTE, KILL_LOG_NOTE_MANY, attachDirect, killSessionDirect, restartDirect, showInfoDirect, toggleAgentArchive, toggleAgentSleep } from './agent_actions.js';
export { copyFolderPath, hideAlertModal, hidePromptModal, showAlertModal, showPromptModal } from './prompt_modals.js';
export { killBoard, killGroup, saveTeamFromSidebar, shareAgentTeam, toggleTeamSleep } from './team_actions.js';
export { expandSleepingGroup, toggleGroupByServer, toggleGroupByTeam, toggleSessionSummary, toggleShowServer, toggleShowBranches, toggleShowProvider, toggleShowModel, toggleShowThinking } from './list_display_settings.js';
export { renderHistoryChat, renderHistoryNodes, renderHistorySessions } from './history_list.js';
export { updateSessionBranch, updateSessionFolder, updateSessionStatus, updateSessionSummary, updateWaitingIndicator } from './session_header_updates.js';
export { distillTeamKnowledge, showTeamTokenUsage, updateHistoryTokenUsage, updateTokenUsage } from './token_usage_ui.js';
export { groupHeaderKey, toggleGroupCollapse } from './group_collapse.js';
export { hideConfirmModal, showConfirmModal } from './confirm_dialog.js';
export { applySessionSummaryPref, isSessionSummaryShown, toggleArchivedSection } from './session_rows.js';
export { moveGroupDown, moveGroupUp, moveSessionDown, moveSessionUp } from './list_order.js';
export { setBoardAccentColor } from './board_accents.js';
export { buildSessionTooltip, commandPlaceholder, deriveSessionState, resolveSessionIdentity, sessionStateInfo, terminalDotClass, applyTerminalDot } from './session_state.js';
export { getActiveBoardChat, hideBoardChatTab, showBoardChatTab } from './board_chat.js';

/* ── Helpers ────────────────────────────────────────────────────────── */

/* ── Agent Avatars ──────────────────────────────────────────────────── */





   // bumped whenever the board chat panel is rebuilt, to drop stale loads








// ── Confirm Modal ────────────────────────────────────────────────────










// Redraw the team menus once the Coral Hub's access list is in (#3443).
document.addEventListener('coral:server-access-loaded', () => { if (state.liveSessions) redrawLiveList(); });











// ── Server info in the agent list (#3085) ────────────────────────────────















// ── Agent search (sidebar) ────────────────────────────────────────────

let _filteredMatches = [];




export function filterLiveAgents(value) {
    setAgentFilter((value || '').trim().toLowerCase());
    redrawLiveList();
}

export function focusAgentSearch() {
    if (document.querySelector('.layout')?.classList.contains('sidebar-collapsed')) window.toggleSidebarRail?.();
    const input = document.getElementById('agent-search-input');
    if (input) { input.focus(); input.select(); }
}

/** Enter opens the first match; Escape clears the search. */
export function agentSearchKey(e) {
    if (e.key === 'Escape') {
        e.target.value = '';
        filterLiveAgents('');
    } else if (e.key === 'Enter') {
        const usable = _filteredMatches.filter(s => !isStaleSession(s));
        const first = usable.find(s => !s.sleeping) || usable[0];
        if (first) window.selectLiveSession?.(first.name, first.agent_type, first.session_id || '', sessionServer(first));
    }
}

/** Render the agent list now (user actions, which may read the DOM right after).
 *  A pending scheduled render is dropped: this one shows the latest list. */
export function renderLiveSessions(sessions) {
    if (_scheduledRender) { cancelAnimationFrame(_scheduledRender); _scheduledRender = 0; }
    followServerOrder(sessions); // a replaced agent's place was moved to its new id on the server (#4943)
    noteBoardLabels(sessions); // board_label beside board_project (#3536): what keyLabel shows
    syncHubFirstRun(); // Coral Hub with no machine connected: the first-run screens (#4586)
    const result = _renderLiveSessions(sessions);
    liveListRendered(Array.isArray(sessions) ? sessions.length : 0); // window.__coralReady (#3118)
    return result;
}

// High-frequency callers (the live feed, the team-availability refresh after a
// render, a tab coming back) schedule instead: a burst becomes one render of
// state.liveSessions in the next animation frame (#3121). A hidden tab renders
// nothing until it is shown again (visibilitychange renders then).
let _scheduledRender = 0;

let _lastListHtml = '';
/** The markup of the last agent-list render (for the suite that checks the
 *  incremental DOM update equals a fresh parse of it). */
export function lastLiveListHtml() { return _lastListHtml; }
export function scheduleLiveSessionsRender() {
    if (_scheduledRender) return;
    _scheduledRender = requestAnimationFrame(() => { _scheduledRender = 0; renderLiveSessions(state.liveSessions || []); });
}

/** Run a scheduled render now (for code that needs the list in the DOM at once). */
export function flushLiveSessionsRender() {
    if (_scheduledRender) renderLiveSessions(state.liveSessions || []);
}

registerLiveListRenderer(renderLiveSessions); // the hook other modules redraw through (list_hooks.js)

/** The session a list row shows (by id and server, by name when the row has no id). */
function _rowSession(item) {
    const id = item.dataset.sessionId, server = item.dataset.server, name = item.dataset.name;
    const same = x => (id ? x.session_id === id : x.name === name) && sessionServer(x) === server;
    return (state.liveSessions || []).find(same) || null;
}







// Puts the markup in the list and wires what is new: tooltips, drag and drop, team token usage, the keyboard focus the redraw took, and the phone's page.
function _applyListHtml(list, html, focusedSid, focusedInMenu) {
    // Only the parts that changed are re-parsed; unchanged rows keep their nodes,
    // listeners and late-filled text (#3121). `added` is what is new.
    _lastListHtml = html;
    const added = morphHtml(list, html);

    // Attach hover listeners for fixed-position tooltips (new rows only)
    for (const item of _within(added, ".session-group-item")) {
        const tip = item.querySelector(".session-tooltip");
        if (!tip) continue;
        item.addEventListener("mouseenter", () => {
            // Built when the pointer comes, from the session as it is now: a row carries an empty tooltip until then (#4497; the table was built for every row on every render).
            const sess = _rowSession(item);
            if (sess) tip.innerHTML = buildSessionTooltip(sess);
            const rect = item.getBoundingClientRect();
            tip.style.left = rect.right + 8 + "px";
            tip.style.top = rect.top + "px";
            tip.style.display = "block";
        });
        item.addEventListener("mouseleave", () => {
            tip.style.display = "none";
        });
    }

    // Attach drag-and-drop listeners for reordering
    _attachDragListeners(list, added);

    // Populate team token usage (non-blocking; kept cards keep their text)
    _within(added, '.team-token-usage[data-board]').forEach(el => {
        getTeamTokenUsage(el.dataset.board).then(text => {
            if (text) el.textContent = ' · ' + text;
        });
    });

    // Restore keyboard focus to the same agent after the rebuild
    if (focusedSid) {
        const again = list.querySelector(`.session-group-item[data-session-id="${CSS.escape(focusedSid)}"]`);
        // A control inside the row that morphHtml already put focus back on (the team's button, a row's own button) keeps it; the row takes it only where it had none.
        if (again && (focusedInMenu || !again.contains(document.activeElement))) (focusedInMenu && again.querySelector('.sidebar-kebab-btn') || again).focus({ preventScroll: true });
    }

    // The phone's page takes the list itself (it is not copied: #4497)
    syncMobileAgentList();

}

// Keep sidebar status aligned with the team availability API (see below).
function _refreshTeamBadges(sessions) {
    // Keep sidebar status aligned with the team availability API. The first
    // render uses the transcript state; the cached API snapshot replaces it
    // asynchronously when an assigned task is idle.
    // Re-render from the current list (not this call's, which may be stale by
    // the time the snapshot lands) and only when a badge actually changed.
    const teams = [...new Set(sessions.map(s => sessionTeamKey(s)).filter(Boolean))];
    for (const team of teams) {
        refreshTeamAvailabilityIfChanged(team).then(changed => {
            if (changed) scheduleLiveSessionsRender(); // several teams changing: one render
        });
    }
}

function _renderLiveSessions(sessions) {
    // Single-agent popout: the sidebar is chrome. Never render rows there —
    // they would carry session ids and destructive kebab actions into the DOM.
    if (document.body.classList.contains('popout-mode')) {
        const list = liveListEl();
        if (list) list.innerHTML = '';
        return;
    }

    applySessionSummaryPref();
    const unfilteredCount = sessions.length;
    const everyAgent = sessions;
    if (getAgentFilter() || _listFiltered(getListFilters())) sessions = sessions.filter(_passesFilters);
    _filteredMatches = sessions;
    syncAgentListBars(everyAgent, sessions.length); // the list bar's options, filters and count (agent_list_bar.js)
    // Archived agents leave the main list, its groups and counts; they are
    // listed in their own section at the bottom.
    const archived = sessions.filter(s => s.archived);
    sessions = sessions.filter(s => !s.archived);

    _renderSidebarRail(sessions);

    const list = liveListEl();
    // Re-render replaces every row: remember which row had keyboard focus.
    const focusedRow = document.activeElement && document.activeElement.closest
        ? document.activeElement.closest('.session-group-item[data-session-id]') : null;
    const focusedSid = focusedRow ? focusedRow.dataset.sessionId : null;
    // Focus inside the row's action menu (or on its button) goes back to the button when the row is redrawn, not to the row (#4621).
    const focusedInMenu = !!(focusedRow && document.activeElement.closest('.sidebar-kebab-wrapper'));
    _wireListKeyboard(list);
    _wireListKeyboard(document.getElementById('mobile-agent-list'));

    // Pinned folders count too: the list shows them with no agents (#3630).
    const pinsShown = pinnedFolders().filter(p => _pinMatchesFilter(p)).length;
    updateSectionVisibility('live-sessions', unfilteredCount + pinnedFolders().length);

    if (!sessions.length && !archived.length && !pinsShown) {
        // Coral Hub with no machine connected: say what to do (the same list is the phone's Agents page), not just that there is nothing
        list.innerHTML = (!unfilteredCount && hubEmptyListHtml(machineState())) || `<li class="empty-state">${unfilteredCount ? 'No matching agents' : 'No live sessions'}</li>`;
        syncGroupToggle(); // no groups drawn: the header toggle goes
        return;
    }

    // Sync the Group by Team checkmark
    const groupByTeam = browserLocal.get('coral-group-by-team') !== 'false';
    const check = document.getElementById('group-by-team-check-top');
    if (check) check.style.opacity = groupByTeam ? '1' : '0.2';


    // The servers UI (#3085): with two or more servers the groups below are
    // wrapped in collapsible server headers (the search-row pill was removed, #4932).

    let html = '';
    const shownServers = serversShown();
    if (isGroupedByServer()) {
        for (const e of shownServers) {
            const own = sessions.filter(x => sessionServer(x) === e.id);
            const collapsed = _isServerCollapsed(e.id);
            html += serverGroupHeaderHtml(e, own.length, collapsed, _openServerDetails.has(e.id));
            if (!collapsed) html += _renderGroups(own, groupByTeam, e.id === LOCAL_SERVER);
        }
    } else {
        html += _renderGroups(sessions, groupByTeam);
    }

    html += _renderArchivedSection(archived);
    _applyListHtml(list, html, focusedSid, focusedInMenu);
    syncGroupToggle(); // the header's Collapse / Expand toggle reads the headers just drawn (#4947)
    _refreshTeamBadges(sessions);
}

// Hidden tabs skip availability polls; catch up as soon as the tab is shown.
document.addEventListener('visibilitychange', () => {
    if (!document.hidden && state.liveSessions?.length) redrawLiveList(); // also drops a frame scheduled while hidden
});


 




/** Delegated keyboard selection for a list container (survives the mobile
 *  cloneNode, which drops element listeners). Enter/Space on a focused row
 *  selects it; events from the kebab, the open-tab anchor or the sparkle are
 *  left to those controls; Escape closes any open kebab. */
function _wireListKeyboard(container) {
    if (!container || container.dataset.kbWired) return;
    container.dataset.kbWired = '1';
    container.addEventListener('keydown', (e) => {
        if (e.key === 'Escape') {
            if (window.closeSidebarKebabs) window.closeSidebarKebabs({ restoreFocus: true });
            return;
        }
        const row = e.target.closest ? e.target.closest('.session-group-item') : null;
        if (!row || e.target !== row) return; // only the row itself, never its inner controls
        if (e.key === 'Enter' || e.key === ' ' || e.key === 'Spacebar') {
            e.preventDefault(); // Space must not scroll the pane or start a drag
            row.click();
        }
    });
}



 








registerActions({
    'kebab-close': (el, event) => { event.stopPropagation(); window.closeSidebarKebabs(); },
    'kebab-toggle': (el, event) => { event.stopPropagation(); window.toggleSidebarKebab(el, event); },
    'agent-info': withAgent('showInfoDirect'),
    'agent-kill': withAgent('killSessionDirect'),
    'agent-restart': withAgent('restartDirect'),
    'agent-rename': withAgent('renameAgent'),
    'agent-icon': withAgent('setAgentIcon'),
    'agent-attach': withAgent('attachDirect'),
    'agent-sleep': withAgent('toggleAgentSleep', ['sleep', 'wake']),
    'agent-archive': withAgent('toggleAgentArchive', ['archive', 'unarchive']),
    'agent-move': menu(el => { const sid = textOf(el.dataset.sessionId); if (sid && UPDOWN[el.dataset.dir]) W(el.dataset.dir === 'up' ? 'moveSessionUp' : 'moveSessionDown')(sid); }),
    'team-sleep': menu(el => { const b = textOf(el.dataset.board); if (b && ['sleep', 'wake'].includes(el.dataset.mode)) W('toggleTeamSleep')(b, el.dataset.mode); }),
    'team-working-mode': withBoard('showTeamWorkingModeWorkspace'),
    'team-tokens': withBoard('showTeamTokenUsage'),
    'team-availability': withBoard('showTeamAvailabilityWorkspace'),
    'team-share': withBoard('shareAgentTeam'),
    'team-save': withBoard('saveTeamFromSidebar'),
    'team-distill': withBoard('distillTeamKnowledge'),
    'team-reset': withBoard('resetTeam'),
    'team-kill': withBoard('killBoard'),
    'team-add-agent': menu(el => { const b = textOf(el.dataset.board); if (b) W('showAddAgentToBoard')(b, dirOf(el) || ''); }),
    'team-add-terminal': menu(el => { const b = textOf(el.dataset.board); if (b) W('launchTerminalToBoard')(b, dirOf(el) || ''); }),
    'team-accent': (el, event) => { event.stopPropagation(); const b = textOf(el.dataset.board); if (b) window.setBoardAccentColor(b); window.closeSidebarKebabs(); },
    'team-health': (el, event) => { event.stopPropagation(); const b = textOf(el.dataset.board); if (b) window.showTeamHealthSettings(b); },
    'team-enter': (el, event) => { event.stopPropagation(); const b = textOf(el.dataset.board); if (b) window.enterTeamContext(b); },
    'group-move': menu(el => { const g = textOf(el.dataset.group); if (g && UPDOWN[el.dataset.dir]) W(el.dataset.dir === 'up' ? 'moveGroupUp' : 'moveGroupDown')(g); }),
    'group-kill': menu(el => { const g = textOf(el.dataset.group); if (g) W('killGroup')(g); }),
    'group-collapse': el => { const g = textOf(el.dataset.group); if (g) window.toggleGroupCollapse(g); },
    'group-header-key': (el, event) => window.groupHeaderKey(event, el),
    'group-sleeping-expand': el => { const g = textOf(el.dataset.group); if (g) window.expandSleepingGroup(g); },
    'archived-toggle': () => window.toggleArchivedSection(),
    'folder-tag': menu(el => { const g = textOf(el.dataset.group); if (g) window.showFolderTagDropdown(g, el.closest('.session-group-header')); }),
    'folder-add-agent': menu(el => { const d = dirOf(el); if (d) W('showAddStandaloneAgent')(d, textOf(el.dataset.server) || ''); }),
    'folder-add-terminal': menu(el => { const d = dirOf(el); if (d) W('launchStandaloneTerminal')(d, textOf(el.dataset.server) || ''); }),
    'folder-launch-default': menu(el => { const d = dirOf(el); if (d) W('launchDefaultAgent')(d, textOf(el.dataset.server) || ''); }),
    'folder-copy': (el, event) => { event.stopPropagation(); const d = dirOf(el); if (d) window.copyFolderPath(d); },
    'folder-pin': menu(el => { const d = dirOf(el); if (d && (el.dataset.mode === 'pin' || el.dataset.mode === 'unpin')) W(el.dataset.mode === 'pin' ? 'pinFolder' : 'unpinFolder')(d); }),
    'wf-run-open': menu(el => { const id = idOf(el.dataset.runId); if (id) window.selectWorkflowRun(id); }),
    'goal-request': (el, event) => { event.stopPropagation(); const a = agentArgs(el); if (a) window.requestGoal(...a); },
    // A row (or a rail tile) opens its agent.
    'session-open': el => {
        const sid = textOf(el.dataset.sessionId) || '';
        const name = textOf(el.dataset.name), type = tokenOf(el.dataset.agentType);
        if (name && type) window.selectLiveSession(name, type, sid, textOf(el.dataset.server) || '');
    },
    'history-open': el => { const sid = textOf(el.dataset.sessionId); if (sid) window.selectHistorySession(sid); },
    'history-page': el => { const n = idOf(el.dataset.page); if (n) window.loadHistoryPage(n); },
    'board-open': el => { const b = textOf(el.dataset.board); if (b) window.selectBoardProject(b); },
    'msg-edit-resubmit': el => window.editAndResubmit(el),
    // The board chat panel beside an agent.
    'board-chat-open': (el, event) => { event.preventDefault(); const b = textOf(el.dataset.board); if (b) window.selectBoardProject(b); },
    'board-chat-pause': el => { const b = textOf(el.dataset.board); if (b) _toggleBoardChatPause(b); },
    'board-chat-select': () => window.__coral._toggleBoardChatSelect(),
    'board-chat-all': () => { const i = document.getElementById('board-panel-input'); if (i) { i.value = '@all ' + i.value; i.focus(); } },
    'board-chat-send': el => { const b = textOf(el.dataset.board); if (b) window.__coral._sendBoardChat(b); },
    // Ctrl+Enter (or Cmd+Enter) sends; while the @mention list is open the arrow keys, Enter, Tab and Escape drive it.
    'board-chat-key': (el, event) => { if (_boardMentionKey(event)) return; if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) { event.preventDefault(); const b = textOf(el.dataset.board); if (b) window.__coral._sendBoardChat(b); } },
    'board-chat-mention': (el, event) => { const b = textOf(el.dataset.board); if (b) _boardMentionInput(event, b); },
    'board-mention-pick': (el, event) => { event.preventDefault(); const n = textOf(el.dataset.name); if (n) _insertBoardMention(n); },
    'board-chat-earlier': () => window.__coral._loadEarlierBoardChat(),
    'board-chat-msg-select': el => { const id = idOf(el.dataset.msgId); if (id) _toggleBoardChatMsgSelect(id, el.checked); },
    'board-chat-select-all': () => _boardChatSelectAll(),
    'board-chat-select-none': () => _boardChatSelectNone(),
    'board-chat-export': () => _exportBoardChatSelected(),
    'board-chat-select-cancel': () => _cancelBoardChatSelect(),
});
