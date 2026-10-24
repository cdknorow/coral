/* The agent list's groups: a team's card, a workflow's, a folder's, and the order they go in (moved out of render.js, #4996; no behaviour change). */
import { state } from './state.js';
import { keyLabel, splitKey, sessionTeamKey, sessionFolderKey } from './server_base.js';
import { distillChip } from './knowledge_distill.js';
import { escapeAttr, escapeHtml } from './utils.js';
import { _sortByOrder, _getGroupOrder, _sortGroups } from './list_order.js';
import { kebabId, _renderAgentListWithSubgroups, _renderSessionItem } from './session_rows.js';
import { CHEVRON_DECO, _chevron, _collapseAttrs, _isGroupCollapsed, _teamHeaderAttrs } from './group_collapse.js';
import { getFolderTags, renderFolderTagPills } from './folder_tags.js';
import { pinPathForKey, emptyPins, notePinKeys } from './pinned_folders.js';
import { localFirst } from './server_status.js';
import { killAllItem, teamHealthBtn, _rowNameWithServer, _boardAccentStyle, _pinMenuItem, FOLDER_GROUP_ICON } from './list_card_parts.js';
import { _pinMatchesFilter } from './list_filter_match.js';

// Team-first mode: one team as a card: its header, its menu and its agents.
function _renderTeamCard(boardName, boardSessions) {
    let html = '';
        const accentStyle = _boardAccentStyle(boardName);
        const boardCollapsed = _isGroupCollapsed(boardName);
        const boardLink = '';
        const boardWorkDir = boardSessions[0]?.working_directory || '';
        const boardIsSleeping = boardSessions.every(s => s.sleeping);
        const boardSleepIcon = boardIsSleeping ? ' <span class="agent-icon" title="Team is sleeping">🌙</span>' : '';
        const sleepLabel = boardIsSleeping ? 'Wake Team' : 'Sleep Team';
        const sleepAction = boardIsSleeping ? 'wake' : 'sleep';
        const bKebab = `<div class="sidebar-kebab-wrapper group-kebab">
            <button class="sidebar-kebab-btn group-kebab-btn" data-on-click="kebab-toggle" title="Group actions" aria-label="More actions for ${escapeAttr(keyLabel(boardName))}" aria-haspopup="menu" aria-expanded="false" aria-controls="${escapeAttr(kebabId('team', boardName))}">&#x22EE;</button>
            <div class="sidebar-kebab-menu" id="${escapeAttr(kebabId('team', boardName))}" style="display:none" role="menu" aria-label="Actions for ${escapeAttr(keyLabel(boardName))}">
                <button class="overflow-menu-item" data-on-click="team-availability" data-board="${escapeAttr(boardName)}"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="9" cy="7" r="3"/><path d="M3 21v-2a6 6 0 0 1 12 0v2M16 4a3 3 0 0 1 0 6M21 21v-2a6 6 0 0 0-4-5.65"/></svg>Team view</button>
                <button class="overflow-menu-item" data-on-click="team-working-mode" data-board="${escapeAttr(boardName)}"><span class="material-icons" style="font-size:0.875rem;vertical-align:-2px" aria-hidden="true">settings</span> Team settings</button>
                <button class="overflow-menu-item" data-on-click="team-add-agent" data-board="${escapeAttr(boardName)}" data-dir="${escapeAttr(boardWorkDir)}">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="8" y1="3" x2="8" y2="13"/><line x1="3" y1="8" x2="13" y2="8"/></svg>
                    Add Agent
                </button>
                <button class="overflow-menu-item" data-on-click="team-add-terminal" data-board="${escapeAttr(boardName)}" data-dir="${escapeAttr(boardWorkDir)}">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="12" height="10" rx="1.5"/><polyline points="5 7 7 9 5 11"/><line x1="9" y1="11" x2="11" y2="11"/></svg>
                    Add Terminal
                </button>
                <hr class="overflow-menu-divider">
                <button class="overflow-menu-item" data-on-click="team-save" data-board="${escapeAttr(boardName)}">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3h8l2 2v8a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z"/><path d="M6 3v3h4V3"/><rect x="5" y="9" width="6" height="3"/></svg>
                    Save as Template
                </button>
                <button class="overflow-menu-item" data-on-click="team-distill" data-board="${escapeAttr(boardName)}">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M8 2L3 7v7h10V7L8 2z"/><path d="M6 14v-4h4v4"/><line x1="8" y1="7" x2="8" y2="10"/><line x1="6.5" y1="8.5" x2="9.5" y2="8.5"/></svg>
                    Distill Knowledge
                </button>
                <button class="overflow-menu-item" data-on-click="team-share" data-board="${escapeAttr(boardName)}">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12v1a2 2 0 0 0 2 2h4a2 2 0 0 0 2-2v-1"/><polyline points="8 3 8 10"/><polyline points="5 6 8 3 11 6"/></svg>
                    Share Team
                </button>
                <button class="overflow-menu-item" data-on-click="team-sleep" data-board="${escapeAttr(boardName)}" data-mode="${escapeAttr(sleepAction)}">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3a6 6 0 1 0 0 10 5 5 0 0 1 0-10z"/></svg>
                    ${sleepLabel}
                </button>
                <hr class="overflow-menu-divider">
                <button class="overflow-menu-item" data-on-click="team-accent" data-board="${escapeAttr(boardName)}">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="8" r="6.5"/><path d="M8 1.5v13M1.5 8h13"/></svg>
                    Set Color
                </button>
                <button class="overflow-menu-item" data-on-click="team-tokens" data-board="${escapeAttr(boardName)}">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 13V5h3v8H2zM7 13V3h3v10H7zM12 13V7h3v6h-3z"/></svg>
                    Token Usage
                </button>
                <button class="overflow-menu-item" data-on-click="group-move" data-group="${escapeAttr(boardName)}" data-dir="up">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M8 3v10M4 7l4-4 4 4"/></svg>
                    Move Up
                </button>
                <button class="overflow-menu-item" data-on-click="group-move" data-group="${escapeAttr(boardName)}" data-dir="down">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M8 13V3M4 9l4 4 4-4"/></svg>
                    Move Down
                </button>
                <hr class="overflow-menu-divider">
                <button class="overflow-menu-item overflow-menu-danger" data-on-click="team-reset" data-board="${escapeAttr(boardName)}">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M1 8a7 7 0 0 1 12.9-3.8"/><path d="M15 8a7 7 0 0 1-12.9 3.8"/><polyline points="13 1 14 4.2 10.5 4.2"/><polyline points="3 15 2 11.8 5.5 11.8"/></svg>
                    Reset Team
                </button>
                ${killAllItem(boardName)}
            </div>
        </div>`;
        const sleepingClass = boardIsSleeping ? ' team-sleeping' : '';
        html += `<li class="session-board-card session-team-group session-board-card-toplevel${sleepingClass}"${accentStyle}>
            <div class="session-group-header board-card-header${escapeAttr(state.selectedTeam === boardName ? ' team-selected' : '')}"${state.selectedTeam === boardName ? ' aria-current="true"' : ''} data-group-name="${escapeAttr(boardName)}" data-on-click="team-name-open" data-board="${escapeAttr(boardName)}" data-group="${escapeAttr(boardName)}"${_teamHeaderAttrs(boardName)}>${_chevron(boardName, boardCollapsed)}
                <div class="group-header-text"><div class="group-name-line"><svg class="team-group-icon" aria-hidden="true" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><circle cx="9" cy="7" r="3"/><path d="M3 21v-2a6 6 0 0 1 12 0v2M16 4a3 3 0 0 1 0 6M21 21v-2a6 6 0 0 0-4-5.65"/></svg><span class="group-name-text" title="${escapeAttr(keyLabel(boardName))}">${_rowNameWithServer(boardName)}</span>${boardSleepIcon}${distillChip(boardName)} <span class="session-group-count">${boardSessions.length}</span></div></div><span class="session-name-spacer"></span>${boardLink}${teamHealthBtn(boardName, boardSessions)}${bKebab}
            </div>
            <ul class="board-card-agents${boardCollapsed ? ' board-card-collapsed' : ''}">`;

        // Apply saved order, then always pin orchestrator to top
        const orderedBoard = _sortByOrder(boardSessions);
        orderedBoard.sort((a, b) => {
            const aOrch = (a.display_name || a.board_job_title || '').toLowerCase().includes('orchestrator');
            const bOrch = (b.display_name || b.board_job_title || '').toLowerCase().includes('orchestrator');
            if (aOrch && !bOrch) return -1;
            if (!aOrch && bOrch) return 1;
            return 0;
        });
        html += _renderAgentListWithSubgroups(orderedBoard, boardWorkDir, true, boardName);
        html += `</ul></li>`;
    return html;
}

// Team-first mode: one workflow's agents, as a card.
function _renderWorkflowCard(wfName, wfSessions) {
    let html = '';
        const wfCollapsed = _isGroupCollapsed('wf:' + wfName);
        const wfRunId = wfSessions[0]?.workflow_run_id || '';
        const wfSubline = `<div class="board-card-subline"><svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 3H5a2 2 0 0 0-2 2v4m6-6h10a2 2 0 0 1 2 2v4M9 3v18m0 0h10a2 2 0 0 0 2-2V9M9 21H5a2 2 0 0 1-2-2V9m0 0h18"/></svg> Workflow · ${wfSessions.length} agent${wfSessions.length !== 1 ? 's' : ''}${wfRunId ? ' · Run #' + wfRunId : ''}</div>`;
        const wfKebab = `<div class="sidebar-kebab-wrapper group-kebab">
            <button class="sidebar-kebab-btn group-kebab-btn" data-on-click="kebab-toggle" title="Workflow actions" aria-label="More actions for ${escapeAttr(wfName)}" aria-haspopup="menu" aria-expanded="false" aria-controls="${escapeAttr(kebabId('workflow', wfName))}">&#x22EE;</button>
            <div class="sidebar-kebab-menu" id="${escapeAttr(kebabId('workflow', wfName))}" style="display:none" role="menu" aria-label="Actions for ${escapeAttr(wfName)}">
                ${wfRunId ? `<button class="overflow-menu-item" data-on-click="wf-run-open" data-run-id="${Number(wfRunId)}">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><path d="M2 8h12M10 4l4 4-4 4"/></svg>
                    View Run
                </button>` : ''}
                <button class="overflow-menu-item overflow-menu-danger" data-on-click="group-kill" data-group="wf:${escapeAttr(wfName)}">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="4" y1="4" x2="12" y2="12"/><line x1="12" y1="4" x2="4" y2="12"/></svg>
                    Kill All
                </button>
            </div>
        </div>`;

        html += `<li class="session-board-card session-board-card-toplevel session-wf-card" style="border-left-color: var(--color-tool-agent)">
            <div class="session-group-header board-card-header" data-group-name="wf:${escapeAttr(wfName)}" data-on-click="group-collapse" data-group="wf:${escapeAttr(wfName)}"${_collapseAttrs(wfCollapsed)}>${CHEVRON_DECO}
                <div class="group-header-text"><div class="group-name-line"><span class="material-icons" style="font-size:0.875rem;vertical-align:-2px;margin-right:3px;color:var(--color-tool-agent)" aria-hidden="true">account_tree</span>${escapeHtml(wfName)}</div>${wfSubline}</div><span class="session-name-spacer"></span>${wfKebab}
            </div>
            <ul class="board-card-agents${wfCollapsed ? ' board-card-collapsed' : ''}">`;

        for (const s of wfSessions) {
            html += _renderSessionItem(s, 'wf:' + wfName, true, wfCollapsed, '');
        }
        html += `</ul></li>`;
    return html;
}

// Team-first mode: one folder group of the agents that are on no team or workflow.
function _renderFolderGroup(groupName, groupSessions) {
    let html = '';
        const sorted = _sortByOrder(groupSessions);
        const isMulti = sorted.length > 1;
        const countBadge = ` <span class="session-group-count">${sorted.length}</span>`; void isMulti;
        const collapsed = _isGroupCollapsed(groupName);
        const groupDirRaw = sorted[0]?.working_directory || pinPathForKey(groupName);
        const groupKebab = `<div class="sidebar-kebab-wrapper group-kebab">
            <button class="sidebar-kebab-btn group-kebab-btn" data-on-click="kebab-toggle" title="Group actions" aria-label="More actions for ${escapeAttr(groupName)}" aria-haspopup="menu" aria-expanded="false" aria-controls="${escapeAttr(kebabId('folder', groupName))}">&#x22EE;</button>
            <div class="sidebar-kebab-menu" id="${escapeAttr(kebabId('folder', groupName))}" style="display:none" role="menu" aria-label="Actions for ${escapeAttr(groupName)}">
                <button class="overflow-menu-item" data-on-click="folder-launch-default" data-dir="${escapeAttr(groupDirRaw)}" data-server="${escapeAttr(splitKey(groupName).server)}">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="8" y1="3" x2="8" y2="13"/><line x1="3" y1="8" x2="13" y2="8"/></svg>
                    Add Agent
                </button>
                <button class="overflow-menu-item" data-on-click="folder-add-agent" data-dir="${escapeAttr(groupDirRaw)}" data-server="${escapeAttr(splitKey(groupName).server)}">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="5" r="2.5"/><path d="M3 14c0-2.8 2.2-5 5-5s5 2.2 5 5"/><line x1="12" y1="3" x2="12" y2="7"/><line x1="10" y1="5" x2="14" y2="5"/></svg>
                    Add Custom Agent
                </button>
                <button class="overflow-menu-item" data-on-click="folder-add-terminal" data-dir="${escapeAttr(groupDirRaw)}" data-server="${escapeAttr(splitKey(groupName).server)}">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="12" height="10" rx="1.5"/><polyline points="5 7 7 9 5 11"/><line x1="9" y1="11" x2="11" y2="11"/></svg>
                    Add Terminal
                </button>
                <hr class="overflow-menu-divider">
                ${_pinMenuItem(groupName, groupDirRaw)}
                <button class="overflow-menu-item" data-on-click="folder-tag" data-group="${escapeAttr(groupName)}">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 8.5V3a1 1 0 0 1 1-1h5.5l5.5 5.5-5.5 5.5L2 8.5z"/><circle cx="5.5" cy="5.5" r="1" fill="currentColor" stroke="none"/></svg>
                    Manage Tags
                </button>
                <button class="overflow-menu-item" data-on-click="group-move" data-group="${escapeAttr(groupName)}" data-dir="up">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M8 3v10"/><path d="M4 7l4-4 4 4"/></svg>
                    Move Up
                </button>
                <button class="overflow-menu-item" data-on-click="group-move" data-group="${escapeAttr(groupName)}" data-dir="down">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M8 13V3"/><path d="M4 9l4 4 4-4"/></svg>
                    Move Down
                </button>
                ${sorted.length ? `<button class="overflow-menu-item overflow-menu-danger" data-on-click="group-kill" data-group="${escapeAttr(groupName)}">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="4" y1="4" x2="12" y2="12"/><line x1="12" y1="4" x2="4" y2="12"/></svg>
                    Kill All
                </button>` : ''}
            </div>
        </div>`;
        const fTags = getFolderTags(groupName);
        const tagDots = renderFolderTagPills(fTags);
        const groupWorkDir = groupDirRaw;
        const copyBtn = `<button class="folder-copy-btn" data-on-click="folder-copy" data-dir="${escapeAttr(groupWorkDir)}" title="Copy path"><svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="5.5" y="5.5" width="8" height="8" rx="1.5"/><path d="M5.5 10.5h-1a1.5 1.5 0 0 1-1.5-1.5v-5a1.5 1.5 0 0 1 1.5-1.5h5a1.5 1.5 0 0 1 1.5 1.5v1"/></svg></button>`;
        html += `<li class="session-group-header" data-group-name="${escapeAttr(groupName)}" data-on-click="group-collapse" data-group="${escapeAttr(groupName)}"${_collapseAttrs(collapsed)}>${CHEVRON_DECO}
            <div class="group-header-text"><div class="group-name-line">${FOLDER_GROUP_ICON}${_rowNameWithServer(groupName)}${countBadge}</div></div>${tagDots}<span class="session-name-spacer"></span>${copyBtn}${groupKebab}</li>`;

        if (!collapsed && sorted.length) {
            html += _renderAgentListWithSubgroups(sorted, groupWorkDir, false, groupName);
        }
    return html;
}

// Folder-first mode: one folder group, with its boards nested inside as cards.
function _renderFolderFirstGroup(groupName, groupSessions) {
    let html = '';
        const sorted = _sortByOrder(groupSessions);
        const isMulti = sorted.length > 1;
        const countBadge = ` <span class="session-group-count">${sorted.length}</span>`; void isMulti;
        const collapsed = _isGroupCollapsed(groupName);
        const groupDirRaw = sorted[0]?.working_directory || pinPathForKey(groupName);
        const groupKebab = `<div class="sidebar-kebab-wrapper group-kebab">
            <button class="sidebar-kebab-btn group-kebab-btn" data-on-click="kebab-toggle" title="Group actions" aria-label="More actions for ${escapeAttr(groupName)}" aria-haspopup="menu" aria-expanded="false" aria-controls="${escapeAttr(kebabId('folder', groupName))}">&#x22EE;</button>
            <div class="sidebar-kebab-menu" id="${escapeAttr(kebabId('folder', groupName))}" style="display:none" role="menu" aria-label="Actions for ${escapeAttr(groupName)}">
                <button class="overflow-menu-item" data-on-click="folder-launch-default" data-dir="${escapeAttr(groupDirRaw)}" data-server="${escapeAttr(splitKey(groupName).server)}">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="8" y1="3" x2="8" y2="13"/><line x1="3" y1="8" x2="13" y2="8"/></svg>
                    Add Agent
                </button>
                <button class="overflow-menu-item" data-on-click="folder-add-agent" data-dir="${escapeAttr(groupDirRaw)}" data-server="${escapeAttr(splitKey(groupName).server)}">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="5" r="2.5"/><path d="M3 14c0-2.8 2.2-5 5-5s5 2.2 5 5"/><line x1="12" y1="3" x2="12" y2="7"/><line x1="10" y1="5" x2="14" y2="5"/></svg>
                    Add Custom Agent
                </button>
                <button class="overflow-menu-item" data-on-click="folder-add-terminal" data-dir="${escapeAttr(groupDirRaw)}" data-server="${escapeAttr(splitKey(groupName).server)}">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="12" height="10" rx="1.5"/><polyline points="5 7 7 9 5 11"/><line x1="9" y1="11" x2="11" y2="11"/></svg>
                    Add Terminal
                </button>
                <hr class="overflow-menu-divider">
                ${_pinMenuItem(groupName, groupDirRaw)}
                <button class="overflow-menu-item" data-on-click="folder-tag" data-group="${escapeAttr(groupName)}">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 8.5V3a1 1 0 0 1 1-1h5.5l5.5 5.5-5.5 5.5L2 8.5z"/><circle cx="5.5" cy="5.5" r="1" fill="currentColor" stroke="none"/></svg>
                    Manage Tags
                </button>
                <button class="overflow-menu-item" data-on-click="group-move" data-group="${escapeAttr(groupName)}" data-dir="up">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M8 3v10"/><path d="M4 7l4-4 4 4"/></svg>
                    Move Up
                </button>
                <button class="overflow-menu-item" data-on-click="group-move" data-group="${escapeAttr(groupName)}" data-dir="down">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M8 13V3"/><path d="M4 9l4 4 4-4"/></svg>
                    Move Down
                </button>
                ${sorted.length ? `<button class="overflow-menu-item overflow-menu-danger" data-on-click="group-kill" data-group="${escapeAttr(groupName)}">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="4" y1="4" x2="12" y2="12"/><line x1="12" y1="4" x2="4" y2="12"/></svg>
                    Kill All
                </button>` : ''}
            </div>
        </div>`;
        const fTags = getFolderTags(groupName);
        const tagDots = renderFolderTagPills(fTags);
        const groupWorkDir = groupDirRaw;
        const copyBtn = `<button class="folder-copy-btn" data-on-click="folder-copy" data-dir="${escapeAttr(groupWorkDir)}" title="Copy path"><svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="5.5" y="5.5" width="8" height="8" rx="1.5"/><path d="M5.5 10.5h-1a1.5 1.5 0 0 1-1.5-1.5v-5a1.5 1.5 0 0 1 1.5-1.5h5a1.5 1.5 0 0 1 1.5 1.5v1"/></svg></button>`;
        html += `<li class="session-group-header" data-group-name="${escapeAttr(groupName)}" data-on-click="group-collapse" data-group="${escapeAttr(groupName)}"${_collapseAttrs(collapsed)}>${CHEVRON_DECO}
            <div class="group-header-text"><div class="group-name-line">${FOLDER_GROUP_ICON}${_rowNameWithServer(groupName)}${countBadge}</div></div>${tagDots}<span class="session-name-spacer"></span>${copyBtn}${groupKebab}</li>`;

        if (collapsed || !sorted.length) {
            // Skip rendering items when collapsed (or a pinned folder with no agents, #3630)
        } else {
            // Sub-group by board within this folder
            const boardSubs = {};
            const unboardedItems = [];
            for (const s of sorted) {
                if (s.board_project) {
                    const tk = sessionTeamKey(s);
                    if (!boardSubs[tk]) boardSubs[tk] = [];
                    boardSubs[tk].push(s);
                } else {
                    unboardedItems.push(s);
                }
            }

            // Render board sub-groups as cards within the folder
            for (const [boardName, boardSessions] of Object.entries(boardSubs)) {
                const accentStyle = _boardAccentStyle(boardName);
                const boardCollapsed = _isGroupCollapsed(boardName);
                const boardLink = '';
                const boardWorkDir = boardSessions[0]?.working_directory || '';
                const boardIsSleeping = boardSessions.some(s => s.sleeping);
                const boardSleepIcon = boardIsSleeping ? ' <span class="agent-icon" title="Team is sleeping">🌙</span>' : '';
                const sleepLabel = boardIsSleeping ? 'Wake Team' : 'Sleep Team';
                const sleepAction = boardIsSleeping ? 'wake' : 'sleep';
                const bKebab = `<div class="sidebar-kebab-wrapper group-kebab">
                    <button class="sidebar-kebab-btn group-kebab-btn" data-on-click="kebab-toggle" title="Group actions" aria-label="More actions for ${escapeAttr(keyLabel(boardName))}" aria-haspopup="menu" aria-expanded="false" aria-controls="${escapeAttr(kebabId('team', boardName))}">&#x22EE;</button>
                    <div class="sidebar-kebab-menu" id="${escapeAttr(kebabId('team', boardName))}" style="display:none" role="menu" aria-label="Actions for ${escapeAttr(keyLabel(boardName))}">
                <button class="overflow-menu-item" data-on-click="team-availability" data-board="${escapeAttr(boardName)}"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="9" cy="7" r="3"/><path d="M3 21v-2a6 6 0 0 1 12 0v2M16 4a3 3 0 0 1 0 6M21 21v-2a6 6 0 0 0-4-5.65"/></svg>Team view</button>
                <button class="overflow-menu-item" data-on-click="team-working-mode" data-board="${escapeAttr(boardName)}"><span class="material-icons" style="font-size:0.875rem;vertical-align:-2px" aria-hidden="true">settings</span> Team settings</button>
                        <button class="overflow-menu-item" data-on-click="team-add-agent" data-board="${escapeAttr(boardName)}" data-dir="${escapeAttr(boardWorkDir)}">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="8" y1="3" x2="8" y2="13"/><line x1="3" y1="8" x2="13" y2="8"/></svg>
                            Add Agent
                        </button>
                        <button class="overflow-menu-item" data-on-click="team-add-terminal" data-board="${escapeAttr(boardName)}" data-dir="${escapeAttr(boardWorkDir)}">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="12" height="10" rx="1.5"/><polyline points="5 7 7 9 5 11"/><line x1="9" y1="11" x2="11" y2="11"/></svg>
                            Add Terminal
                        </button>
                        <hr class="overflow-menu-divider">
                        <button class="overflow-menu-item" data-on-click="team-save" data-board="${escapeAttr(boardName)}">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3h8l2 2v8a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z"/><path d="M6 3v3h4V3"/><rect x="5" y="9" width="6" height="3"/></svg>
                            Save as Template
                        </button>
                        <button class="overflow-menu-item" data-on-click="team-distill" data-board="${escapeAttr(boardName)}">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M8 2L3 7v7h10V7L8 2z"/><path d="M6 14v-4h4v4"/><line x1="8" y1="7" x2="8" y2="10"/><line x1="6.5" y1="8.5" x2="9.5" y2="8.5"/></svg>
                            Distill Knowledge
                        </button>
                        <button class="overflow-menu-item" data-on-click="team-share" data-board="${escapeAttr(boardName)}">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12v1a2 2 0 0 0 2 2h4a2 2 0 0 0 2-2v-1"/><polyline points="8 3 8 10"/><polyline points="5 6 8 3 11 6"/></svg>
                            Share Team
                        </button>
                        <button class="overflow-menu-item" data-on-click="team-sleep" data-board="${escapeAttr(boardName)}" data-mode="${escapeAttr(sleepAction)}">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3a6 6 0 1 0 0 10 5 5 0 0 1 0-10z"/></svg>
                            ${sleepLabel}
                        </button>
                        <hr class="overflow-menu-divider">
                        <button class="overflow-menu-item" data-on-click="team-accent" data-board="${escapeAttr(boardName)}">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="8" r="6.5"/><path d="M8 1.5v13M1.5 8h13"/></svg>
                            Set Color
                        </button>
                        <button class="overflow-menu-item" data-on-click="team-tokens" data-board="${escapeAttr(boardName)}">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 13V5h3v8H2zM7 13V3h3v10H7zM12 13V7h3v6h-3z"/></svg>
                            Token Usage
                        </button>
                        <hr class="overflow-menu-divider">
                        ${killAllItem(boardName)}
                    </div>
                </div>`;
                html += `<li class="session-board-card session-team-group"${accentStyle}>
                    <div class="session-group-header board-card-header${escapeAttr(state.selectedTeam === boardName ? ' team-selected' : '')}"${state.selectedTeam === boardName ? ' aria-current="true"' : ''} data-group-name="${escapeAttr(boardName)}" data-on-click="team-name-open" data-board="${escapeAttr(boardName)}" data-group="${escapeAttr(boardName)}"${_teamHeaderAttrs(boardName)}>${_chevron(boardName, boardCollapsed)}
                        <div class="group-header-text"><div class="group-name-line"><svg class="team-group-icon" aria-hidden="true" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><circle cx="9" cy="7" r="3"/><path d="M3 21v-2a6 6 0 0 1 12 0v2M16 4a3 3 0 0 1 0 6M21 21v-2a6 6 0 0 0-4-5.65"/></svg><span class="group-name-text" title="${escapeAttr(keyLabel(boardName))}">${_rowNameWithServer(boardName)}</span>${boardSleepIcon}${distillChip(boardName)} <span class="session-group-count">${boardSessions.length}</span></div></div><span class="session-name-spacer"></span>${boardLink}${teamHealthBtn(boardName, boardSessions)}${bKebab}
                    </div>
                    <ul class="board-card-agents${boardCollapsed ? ' board-card-collapsed' : ''}">`;
                const orderedBoardNested = _sortByOrder(boardSessions);
                orderedBoardNested.sort((a, b) => {
                    const aOrch = (a.display_name || a.board_job_title || '').toLowerCase().includes('orchestrator');
                    const bOrch = (b.display_name || b.board_job_title || '').toLowerCase().includes('orchestrator');
                    if (aOrch && !bOrch) return -1;
                    if (!aOrch && bOrch) return 1;
                    return 0;
                });
                html += _renderAgentListWithSubgroups(orderedBoardNested, boardWorkDir, true, groupName);
                html += `</ul></li>`;
            }

            // Render unboarded items as flat list with subgroups
            html += _renderAgentListWithSubgroups(unboardedItems, groupWorkDir, false, groupName);
        }
    return html;
}

// The groups of a list of sessions as markup: teams, workflows and folders (team first), or folders with their boards nested (folder first); with pins, pinned folders that have no agent here are listed too.
export function _renderGroups(sessions, groupByTeam, withPins = true) {
    let html = '';

    if (groupByTeam) {
    // ── Team-first mode: teams at top level, standalone by folder below ──

    // Step 1: Separate into team (has board_project) vs workflow vs standalone
    const teamGroups = {};
    const workflowGroups = {};
    const standaloneByFolder = {};
    for (const s of sessions) {
        if (s.board_project) {
            const tk = sessionTeamKey(s);
            if (!teamGroups[tk]) teamGroups[tk] = [];
            teamGroups[tk].push(s);
        } else if (s.workflow_name) {
            const wfKey = s.workflow_name;
            if (!workflowGroups[wfKey]) workflowGroups[wfKey] = [];
            workflowGroups[wfKey].push(s);
        } else {
            const key = sessionFolderKey(s);
            if (!standaloneByFolder[key]) standaloneByFolder[key] = [];
            standaloneByFolder[key].push(s);
        }
    }
    // Pinned folders with no agent here are listed as empty folder entries (#3630).
    if (withPins) {
        const empty = emptyPins(sessions.filter(s => !s.board_project && !s.workflow_name), new Set(Object.keys(standaloneByFolder)));
        notePinKeys(empty);
        for (const p of empty) if (_pinMatchesFilter(p)) standaloneByFolder[p.key] = [];
    }

    // Step 2: Render team groups at top level
    // Apply saved group order first, then sort active before sleeping
    const teamEntries = localFirst(_sortGroups(Object.entries(teamGroups)));
    // Secondary sort: if no saved order, put sleeping teams after active
    const hasOrder = _getGroupOrder().length > 0;
    if (!hasOrder) {
        teamEntries.sort((a, b) => {
            const aAllSleeping = a[1].every(s => s.sleeping);
            const bAllSleeping = b[1].every(s => s.sleeping);
            if (aAllSleeping !== bAllSleeping) return aAllSleeping ? 1 : -1;
            return 0;
        });
    }

    for (const [boardName, boardSessions] of teamEntries) {
        html += _renderTeamCard(boardName, boardSessions);
    }

    // Step 2b: Render workflow agent groups
    for (const [wfName, wfSessions] of Object.entries(workflowGroups)) {
        html += _renderWorkflowCard(wfName, wfSessions);
    }

    // Step 3: Render standalone agents grouped by folder
    const sortedFolders = localFirst(_sortGroups(Object.entries(standaloneByFolder)));
    for (const [groupName, groupSessions] of sortedFolders) {
        html += _renderFolderGroup(groupName, groupSessions);
    }

    } else {
    // ── Folder-first mode: all sessions grouped by folder, boards nested inside ──

    // Group sessions by folder name (primary grouping)
    const groups = {};
    for (const s of sessions) {
        const key = sessionFolderKey(s);
        if (!groups[key]) groups[key] = [];
        groups[key].push(s);
    }
    if (withPins) {
        const empty = emptyPins(sessions, new Set(Object.keys(groups)));
        notePinKeys(empty);
        for (const p of empty) if (_pinMatchesFilter(p)) groups[p.key] = [];
    }

    const sortedGroups = localFirst(_sortGroups(Object.entries(groups)));
    for (const [groupName, groupSessions] of sortedGroups) {
        html += _renderFolderFirstGroup(groupName, groupSessions);
    }

    } // end if/else groupByTeam
    return html;
}
