/* Rendering functions for session lists, chat history, and status updates */

import { state } from './state.js';
import { escapeHtml, showToast, escapeAttr } from './utils.js';
import { renderSidebarTagDots } from './tags.js';
import { getFolderTags, renderFolderTagPills } from './folder_tags.js';
import { updateSectionVisibility } from './sidebar.js';
import { syncMobileAgentList } from './mobile.js';

function formatShortTime(isoStr) {
    const d = new Date(isoStr);
    if (isNaN(d)) return "";
    const dd = String(d.getDate()).padStart(2, "0");
    const mm = String(d.getMonth() + 1).padStart(2, "0");
    const yy = String(d.getFullYear()).slice(-2);
    return `${dd}/${mm}/${yy}`;
}

function formatDuration(sec) {
    if (sec == null || sec <= 0) return '';
    if (sec < 60) return `${sec}s`;
    if (sec < 3600) return `${Math.round(sec / 60)}m`;
    const h = Math.floor(sec / 3600);
    const m = Math.round((sec % 3600) / 60);
    return m > 0 ? `${h}h ${m}m` : `${h}h`;
}

function formatStaleness(seconds) {
    if (seconds === null || seconds === undefined) return "Unknown";
    if (seconds < 5) return "Just now";
    if (seconds < 60) return `${Math.floor(seconds)}s ago`;
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
    return `${Math.floor(seconds / 3600)}h ago`;
}

function getStateLabel(s) {
    if (s.waiting_for_input) return "Needs input";
    if (s.stuck) return "Stuck";
    if (s.working) return "Working";
    if (s.done) return "Done";
    return "Idle";
}

function buildSessionTooltip(s) {
    const stateLabel = getStateLabel(s);
    const lastAction = formatStaleness(s.staleness_seconds);
    const goal = s.summary || "No goal set";
    const status = s.status || "No status";
    const branch = s.branch || "—";
    const agent = s.agent_type || "claude";

    let rows = [
        `<tr><td class="tt-label">State</td><td class="tt-value tt-state-${stateLabel.toLowerCase().replace(/ /g, '-')}">${escapeHtml(stateLabel)}</td></tr>`,
        `<tr><td class="tt-label">Last action</td><td class="tt-value">${escapeHtml(lastAction)}</td></tr>`,
        `<tr><td class="tt-label">Goal</td><td class="tt-value">${escapeHtml(goal)}</td></tr>`,
        `<tr><td class="tt-label">Status</td><td class="tt-value">${escapeHtml(status)}</td></tr>`,
        `<tr><td class="tt-label">Branch</td><td class="tt-value">${escapeHtml(branch)}</td></tr>`,
        `<tr><td class="tt-label">Agent</td><td class="tt-value">${escapeHtml(agent)}</td></tr>`,
    ];
    if (s.board_project) {
        const unreadBadge = s.board_unread > 0 ? ` <span class="tt-unread">(${s.board_unread} unread)</span>` : '';
        rows.push(`<tr><td class="tt-label">Board</td><td class="tt-value">${escapeHtml(s.board_project)}${unreadBadge}</td></tr>`);
        rows.push(`<tr><td class="tt-label">Role</td><td class="tt-value">${escapeHtml(s.board_job_title)}</td></tr>`);
    }
    return `<table class="session-tooltip-table">${rows.join("")}</table>`;
}

function getDotClass(s) {
    if (s.waiting_for_input) return "waiting";
    if (s.stuck) return "stuck";
    if (s.working) return "working";
    if (s.done) return "done";
    return "stale";
}

// ── Session ordering ──────────────────────────────────────────────────

function _getSessionOrder() {
    try {
        return JSON.parse(state.settings.session_order || "[]");
    } catch { return []; }
}

function _sortByOrder(sessions) {
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

async function _saveSessionOrder(orderedIds) {
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

// ── Group collapse state ─────────────────────────────────────────────
function _getCollapsedGroups() {
    try { return JSON.parse(localStorage.getItem('coral_collapsed_groups') || '[]'); }
    catch { return []; }
}

function _isGroupCollapsed(groupName) {
    return _getCollapsedGroups().includes(groupName);
}

export function toggleGroupCollapse(groupName) {
    const groups = _getCollapsedGroups();
    const idx = groups.indexOf(groupName);
    if (idx >= 0) groups.splice(idx, 1);
    else groups.push(groupName);
    localStorage.setItem('coral_collapsed_groups', JSON.stringify(groups));
    renderLiveSessions(state.liveSessions);
}

export async function killSessionDirect(name, agentType, sessionId) {
    if (!confirm(`Kill session "${name}"? This will terminate the agent.`)) return;
    try {
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(name)}/kill`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ agent_type: agentType, session_id: sessionId }),
        });
        const result = await resp.json();
        if (result.error) { showToast(result.error, true); return; }
        showToast(`Killed: ${name}`);
        state.liveSessions = state.liveSessions.filter(s => s.session_id !== sessionId);
        if (state.currentSession && state.currentSession.session_id === sessionId) {
            state.currentSession = null;
            document.getElementById("live-session-view").style.display = "none";
            document.getElementById("welcome-screen").style.display = "flex";
        }
        renderLiveSessions(state.liveSessions);
    } catch (e) {
        showToast("Failed to kill session", true);
    }
}

export async function showInfoDirect(name, agentType, sessionId) {
    const { selectLiveSession } = await import('./sessions.js');
    await selectLiveSession(name, agentType, sessionId);
    const { showInfoModal } = await import('./modals.js');
    showInfoModal();
}

export async function attachDirect(name, agentType, sessionId) {
    try {
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(name)}/attach`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ agent_type: agentType, session_id: sessionId }),
        });
        const result = await resp.json();
        if (result.error) showToast(result.error, true);
        else showToast("Terminal opened");
    } catch (e) {
        showToast("Failed to open terminal", true);
    }
}

export async function restartDirect(name, agentType, sessionId) {
    const { selectLiveSession } = await import('./sessions.js');
    await selectLiveSession(name, agentType, sessionId);
    const { restartSession } = await import('./controls.js');
    restartSession();
}

// ── Confirm Modal ────────────────────────────────────────────────────

export function showConfirmModal(title, message, onConfirm) {
    document.getElementById("confirm-modal-title").textContent = title;
    document.getElementById("confirm-modal-message").textContent = message;
    const yesBtn = document.getElementById("confirm-modal-yes");
    const newBtn = yesBtn.cloneNode(true);
    yesBtn.parentNode.replaceChild(newBtn, yesBtn);
    newBtn.addEventListener("click", () => {
        hideConfirmModal();
        onConfirm();
    });
    document.getElementById("confirm-modal").style.display = "flex";
}

export function hideConfirmModal() {
    document.getElementById("confirm-modal").style.display = "none";
}

export async function killGroup(groupName) {
    const groupSessions = state.liveSessions.filter(s => (s.name || 'unknown') === groupName);
    if (!groupSessions.length) return;

    showConfirmModal(
        "Kill All Agents",
        `Kill all ${groupSessions.length} agent(s) in "${groupName}"? This will terminate them.`,
        async () => {
            for (const s of groupSessions) {
                try {
                    await fetch(`/api/sessions/live/${encodeURIComponent(s.name)}/kill`, {
                        method: "POST",
                        headers: { "Content-Type": "application/json" },
                        body: JSON.stringify({ agent_type: s.agent_type, session_id: s.session_id }),
                    });
                } catch (e) {
                    console.error(`Failed to kill ${s.name}:`, e);
                }
            }
            const killedIds = new Set(groupSessions.map(s => s.session_id));
            state.liveSessions = state.liveSessions.filter(s => !killedIds.has(s.session_id));
            renderLiveSessions(state.liveSessions);
        }
    );
}

export async function shareAgentTeam(boardName) {
    // Find all sessions on this board
    const boardSessions = (state.liveSessions || []).filter(s => s.board_project === boardName);
    if (!boardSessions.length) {
        showToast("No agents found on this board", "error");
        return;
    }

    // Fetch each agent's prompt from session info
    const agents = [];
    for (const s of boardSessions) {
        let agentPrompt = "";
        try {
            const resp = await fetch(`/api/sessions/live/${encodeURIComponent(s.name)}/info?session_id=${encodeURIComponent(s.session_id || "")}`);
            const info = await resp.json();
            agentPrompt = info.prompt || "";
        } catch { /* use empty prompt */ }
        agents.push({
            name: s.display_name || s.board_job_title || s.name,
            prompt: agentPrompt,
        });
    }

    const template = {
        version: 1,
        type: "coral-team-templates",
        templates: [{
            name: boardName,
            agents,
            flags: "",
        }],
    };

    const blob = new Blob([JSON.stringify(template, null, 2)], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `coral-team-${boardName.replace(/[^a-zA-Z0-9-_]/g, "_")}.json`;
    a.click();
    URL.revokeObjectURL(url);
    showToast(`Exported team "${boardName}" (${agents.length} agents)`);
}

export async function saveTeamFromSidebar(boardName) {
    const boardSessions = (state.liveSessions || []).filter(s => s.board_project === boardName);
    if (!boardSessions.length) {
        showToast("No agents found on this board", "error");
        return;
    }

    const templateName = window.prompt("Template name:", boardName);
    if (!templateName) return;

    // Fetch each agent's prompt from session info
    const agents = [];
    for (const s of boardSessions) {
        let agentPrompt = "";
        try {
            const resp = await fetch(`/api/sessions/live/${encodeURIComponent(s.name)}/info?session_id=${encodeURIComponent(s.session_id || "")}`);
            const info = await resp.json();
            agentPrompt = info.prompt || "";
        } catch { /* use empty prompt */ }
        agents.push({
            name: s.display_name || s.board_job_title || s.name,
            prompt: agentPrompt,
        });
    }

    // Save to user_settings via the settings API
    let existing = [];
    try {
        existing = JSON.parse(state.settings.saved_team_templates || "[]");
    } catch { /* ignore */ }
    const idx = existing.findIndex(t => t.name === templateName);
    const entry = { name: templateName, agents, flags: "" };
    if (idx >= 0) existing[idx] = entry; else existing.push(entry);
    state.settings.saved_team_templates = JSON.stringify(existing);
    await fetch("/api/settings", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ saved_team_templates: state.settings.saved_team_templates }),
    });
    showToast(`Saved template "${templateName}" (${agents.length} agents)`);
}

// Drag-and-drop state
let _draggedSid = null;

function _renderSessionItem(s, groupName, isCompact, collapsed) {
    const dotClass = getDotClass(s);
    const isActive = state.currentSession && state.currentSession.type === "live" && state.currentSession.session_id === s.session_id;
    const typeTag = s.agent_type && s.agent_type !== "claude" ? ` <span class="badge ${escapeHtml(s.agent_type)}">${escapeHtml(s.agent_type)}</span>` : "";
    // Branch is shown at folder level, not per agent
    const branchTag = "";
    const waitingBadge = s.waiting_for_input
        ? ' <span class="badge waiting-badge">Needs input</span>'
        : s.done
        ? ' <span class="badge done-badge">Done</span>'
        : '';
    const isTerminal = s.agent_type === "terminal";
    const sid = s.session_id ? escapeHtml(s.session_id) : "";
    const goalText = s.summary ? escapeHtml(s.summary) : null;
    const goal = goalText || (isTerminal ? "" : `<a href="#" class="generate-goal-link" onclick="event.preventDefault(); event.stopPropagation(); requestGoal('${escapeHtml(s.name)}', '${escapeHtml(s.agent_type)}', '${sid}')" title="Ask agent to set a goal">Generate Goal</a>`);
    const displayLabel = s.display_name || (isCompact && s.board_job_title) || (isTerminal ? "Terminal" : "Agent");
    const kebabMenu = `<div class="sidebar-kebab-wrapper">
        <button class="sidebar-kebab-btn" onclick="event.stopPropagation(); toggleSidebarKebab(this)" title="More actions">&#x22EE;</button>
        <div class="sidebar-kebab-menu" style="display:none">
            <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); attachDirect('${escapeHtml(s.name)}', '${escapeHtml(s.agent_type)}', '${sid}')">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="4,4 8,8 4,12"/><line x1="9" y1="12" x2="13" y2="12"/></svg>
                Attach
            </button>
            <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); restartDirect('${escapeHtml(s.name)}', '${escapeHtml(s.agent_type)}', '${sid}')">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3.5 2v4h4"/><path d="M3.5 6A5.5 5.5 0 1 1 2.5 8"/></svg>
                Restart
            </button>
            <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); renameAgent('${escapeHtml(s.name)}', '${escapeHtml(s.agent_type)}', '${sid}')">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><path d="M11.5 1.5l3 3L5 14H2v-3z"/></svg>
                Rename
            </button>
            <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); showInfoDirect('${escapeHtml(s.name)}', '${escapeHtml(s.agent_type)}', '${sid}')">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><circle cx="8" cy="8" r="6.5"/><line x1="8" y1="7" x2="8" y2="11"/><circle cx="8" cy="5" r="0.5" fill="currentColor" stroke="none"/></svg>
                Session Info
            </button>
            <hr class="overflow-menu-divider">
            <button class="overflow-menu-item overflow-menu-danger" onclick="event.stopPropagation(); closeSidebarKebabs(); killSessionDirect('${escapeHtml(s.name)}', '${escapeHtml(s.agent_type)}', '${sid}')">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="4" y1="4" x2="12" y2="12"/><line x1="12" y1="4" x2="4" y2="12"/></svg>
                Kill Session
            </button>
        </div>
    </div>`;
    const tooltip = buildSessionTooltip(s);
    const compactClass = isCompact ? ' session-compact' : '';
    const collapsedClass = collapsed ? ' group-collapsed' : '';
    return `<li class="session-group-item${isActive ? ' active' : ''}${compactClass}${collapsedClass}"
        draggable="true"
        data-session-id="${sid}"
        data-group="${escapeHtml(groupName)}"
        onclick="selectLiveSession('${escapeHtml(s.name)}', '${escapeHtml(s.agent_type)}', '${sid}')">
        <span class="session-dot ${dotClass}"></span>
        <div class="session-info">
            <div class="session-name-row">
                <span class="session-label">${isTerminal ? '<svg class="terminal-icon" width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="4,4 8,8 4,12"/><line x1="9" y1="12" x2="13" y2="12"/></svg> ' : ''}${escapeHtml(displayLabel)}${typeTag}</span>
                <span class="session-name-spacer"></span>
                ${waitingBadge}
                ${kebabMenu}
            </div>
            <span class="session-goal${isCompact ? ' session-goal-compact' : ''}">${goal}</span>
            ${branchTag}
        </div>
        <div class="session-tooltip">${tooltip}</div>
    </li>`;
}

export function renderLiveSessions(sessions) {
    const list = document.getElementById("live-sessions-list");

    updateSectionVisibility('live-sessions', sessions.length);

    if (!sessions.length) {
        list.innerHTML = '<li class="empty-state">No live sessions</li>';
        return;
    }

    // Group sessions by folder name (primary grouping)
    const groups = {};
    for (const s of sessions) {
        const key = s.name || "unknown";
        if (!groups[key]) groups[key] = [];
        groups[key].push(s);
    }

    // Helper to generate a deterministic accent color from a string
    function _boardAccentColor(name) {
        let hash = 0;
        for (let i = 0; i < name.length; i++) hash = ((hash << 5) - hash + name.charCodeAt(i)) | 0;
        const hue = ((hash % 360) + 360) % 360;
        return `hsl(${hue}, 60%, 55%)`;
    }

    let html = "";
    for (const [groupName, groupSessions] of Object.entries(groups)) {
        const sorted = _sortByOrder(groupSessions);
        const isMulti = sorted.length > 1;
        const countBadge = isMulti ? ` <span class="session-group-count">${sorted.length}</span>` : "";
        const collapsed = _isGroupCollapsed(groupName);
        const chevron = collapsed ? '&#x25B8;' : '&#x25BE;';
        const groupKebab = `<div class="sidebar-kebab-wrapper group-kebab">
            <button class="sidebar-kebab-btn group-kebab-btn" onclick="event.stopPropagation(); toggleSidebarKebab(this)" title="Group actions">&#x22EE;</button>
            <div class="sidebar-kebab-menu" style="display:none">
                <button class="overflow-menu-item overflow-menu-danger" onclick="event.stopPropagation(); closeSidebarKebabs(); killGroup('${escapeHtml(groupName)}')">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="4" y1="4" x2="12" y2="12"/><line x1="12" y1="4" x2="4" y2="12"/></svg>
                    Kill All
                </button>
            </div>
        </div>`;
        const groupBranch = sorted.find(s => s.branch)?.branch || "";
        const groupBranchLine = groupBranch ? `<div class="group-branch-line"><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M6 3v5a3 3 0 0 0 3 3h1"/><circle cx="6" cy="3" r="1.5"/><circle cx="11" cy="11" r="1.5"/></svg> ${escapeHtml(groupBranch)}</div>` : "";
        const fTags = getFolderTags(groupName);
        const tagDots = renderFolderTagPills(fTags);
        const tagBtn = `<button class="folder-tag-btn" onclick="event.stopPropagation(); showFolderTagDropdown('${escapeAttr(groupName)}', this.closest('.session-group-header'))" title="Manage tags">+</button>`;
        html += `<li class="session-group-header" data-group-name="${escapeHtml(groupName)}" onclick="toggleGroupCollapse('${escapeHtml(groupName)}')">
            <span class="group-chevron">${chevron}</span><div class="group-header-text"><div class="group-name-line">${escapeHtml(groupName)}${countBadge}</div>${groupBranchLine}</div>${tagDots}<span class="session-name-spacer"></span>${tagBtn}${groupKebab}</li>`;

        if (collapsed) {
            // Skip rendering items when collapsed
        } else {
            // Sub-group by board within this folder
            const boardSubs = {};
            const unboardedItems = [];
            for (const s of sorted) {
                if (s.board_project) {
                    if (!boardSubs[s.board_project]) boardSubs[s.board_project] = [];
                    boardSubs[s.board_project].push(s);
                } else {
                    unboardedItems.push(s);
                }
            }

            // Render board sub-groups as cards within the folder
            for (const [boardName, boardSessions] of Object.entries(boardSubs)) {
                const accentColor = _boardAccentColor(boardName);
                const boardCollapsed = _isGroupCollapsed(boardName);
                const bChevron = boardCollapsed ? '&#x25B8;' : '&#x25BE;';
                const boardLink = `<button class="group-board-link" onclick="event.stopPropagation(); selectBoardProject('${escapeHtml(boardName)}')" title="View board: ${escapeHtml(boardName)}"><svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 3h12v8H5l-3 3V3z"/></svg></button>`;
                const boardWorkDir = boardSessions[0]?.working_directory || '';
                const bKebab = `<div class="sidebar-kebab-wrapper group-kebab">
                    <button class="sidebar-kebab-btn group-kebab-btn" onclick="event.stopPropagation(); toggleSidebarKebab(this)" title="Group actions">&#x22EE;</button>
                    <div class="sidebar-kebab-menu" style="display:none">
                        <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); showAddAgentToBoard('${escapeAttr(boardName)}', '${escapeAttr(boardWorkDir)}')">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="8" y1="3" x2="8" y2="13"/><line x1="3" y1="8" x2="13" y2="8"/></svg>
                            Add Agent
                        </button>
                        <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); saveTeamFromSidebar('${escapeAttr(boardName)}')">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3h8l2 2v8a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z"/><path d="M6 3v3h4V3"/><rect x="5" y="9" width="6" height="3"/></svg>
                            Save as Template
                        </button>
                        <button class="overflow-menu-item" onclick="event.stopPropagation(); closeSidebarKebabs(); shareAgentTeam('${escapeAttr(boardName)}')">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12v1a2 2 0 0 0 2 2h4a2 2 0 0 0 2-2v-1"/><polyline points="8 3 8 10"/><polyline points="5 6 8 3 11 6"/></svg>
                            Share Team
                        </button>
                        <hr class="overflow-menu-divider">
                        <button class="overflow-menu-item overflow-menu-danger" onclick="event.stopPropagation(); closeSidebarKebabs(); killGroup('${escapeHtml(groupName)}')">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="4" y1="4" x2="12" y2="12"/><line x1="12" y1="4" x2="4" y2="12"/></svg>
                            Kill All
                        </button>
                    </div>
                </div>`;
                const teamSubline = `<div class="board-card-subline"><svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="9" cy="7" r="3"/><circle cx="17" cy="7" r="3"/><path d="M3 21v-2a4 4 0 0 1 4-4h4a4 4 0 0 1 4 4v2"/><path d="M17 11a4 4 0 0 1 4 4v2"/></svg> Agent Team</div>`;
                html += `<li class="session-board-card" style="border-left-color: ${accentColor}">
                    <div class="session-group-header board-card-header" onclick="toggleGroupCollapse('${escapeHtml(boardName)}')">
                        <span class="group-chevron">${bChevron}</span><div class="group-header-text"><div class="group-name-line">${escapeHtml(boardName)} <span class="session-group-count">${boardSessions.length}</span></div>${teamSubline}</div><span class="session-name-spacer"></span>${boardLink}${bKebab}
                    </div>
                    <ul class="board-card-agents${boardCollapsed ? ' board-card-collapsed' : ''}">`;
                for (const s of boardSessions) {
                    html += _renderSessionItem(s, groupName, true);
                }
                html += `</ul></li>`;
            }

            // Render unboarded items as flat list
            for (const s of unboardedItems) {
                html += _renderSessionItem(s, groupName, false);
            }
        }
    }

    list.innerHTML = html;

    // Attach hover listeners for fixed-position tooltips
    for (const item of list.querySelectorAll(".session-group-item")) {
        const tip = item.querySelector(".session-tooltip");
        if (!tip) continue;
        item.addEventListener("mouseenter", () => {
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
    _attachDragListeners(list);

    // Sync mobile agent list
    syncMobileAgentList();
}

function _attachDragListeners(list) {
    const items = list.querySelectorAll(".session-group-item[draggable]");

    for (const item of items) {
        item.addEventListener("dragstart", (e) => {
            _draggedSid = item.dataset.sessionId;
            item.classList.add("dragging");
            e.dataTransfer.effectAllowed = "move";
            // Use a minimal drag image to avoid tooltip flash
            e.dataTransfer.setData("text/plain", _draggedSid);
        });

        item.addEventListener("dragend", () => {
            item.classList.remove("dragging");
            _draggedSid = null;
            // Clear all drop indicators
            for (const el of list.querySelectorAll(".drag-over")) {
                el.classList.remove("drag-over");
            }
        });

        item.addEventListener("dragover", (e) => {
            e.preventDefault();
            if (!_draggedSid || _draggedSid === item.dataset.sessionId) return;
            // Only allow reorder within the same group
            const draggedEl = list.querySelector(`[data-session-id="${_draggedSid}"]`);
            if (!draggedEl || draggedEl.dataset.group !== item.dataset.group) return;
            e.dataTransfer.dropEffect = "move";
            item.classList.add("drag-over");
        });

        item.addEventListener("dragleave", () => {
            item.classList.remove("drag-over");
        });

        item.addEventListener("drop", (e) => {
            e.preventDefault();
            item.classList.remove("drag-over");
            if (!_draggedSid || _draggedSid === item.dataset.sessionId) return;

            const group = item.dataset.group;
            const draggedEl = list.querySelector(`[data-session-id="${_draggedSid}"]`);
            if (!draggedEl || draggedEl.dataset.group !== group) return;

            // Gather current order of session_ids in this group
            const groupItems = [...list.querySelectorAll(".session-group-item")].filter(el => el.dataset.group === group);
            const ids = groupItems.map(el => el.dataset.sessionId);

            // Remove dragged from its position and insert before drop target
            const fromIdx = ids.indexOf(_draggedSid);
            const toIdx = ids.indexOf(item.dataset.sessionId);
            if (fromIdx === -1 || toIdx === -1) return;

            ids.splice(fromIdx, 1);
            ids.splice(toIdx, 0, _draggedSid);

            // Save and re-render
            _saveSessionOrder(ids);
            renderLiveSessions(state.liveSessions);
        });
    }
}

export function renderHistorySessions(sessions, total, page, pageSize) {
    const list = document.getElementById("history-sessions-list");
    state.historySessionsList = sessions || [];

    updateSectionVisibility('history', total || (sessions ? sessions.length : 0));

    if (!sessions || !sessions.length) {
        list.innerHTML = '<li class="empty-state">No history found</li>';
        renderPaginationControls(0, 1, 50);
        return;
    }

    list.innerHTML = sessions.map(s => {
        const label = s.summary_title || s.summary || s.session_id;
        const truncated = label.length > 40 ? label.substring(0, 40) + "..." : label;
        const isActive = state.currentSession && state.currentSession.type === "history" && state.currentSession.name === s.session_id;
        const typeTag = s.source_type === "gemini" ? '<span class="badge gemini">gemini</span>' : "";
        const branchTag = s.branch ? `<span class="sidebar-branch">${escapeHtml(s.branch)}</span>` : "";
        const tagDots = s.tags ? renderSidebarTagDots(s.tags) : "";
        const timeStr = s.last_timestamp ? formatShortTime(s.last_timestamp) : "";
        const timeTag = timeStr ? `<span class="session-time">${escapeHtml(timeStr)}</span>` : "";
        const durStr = s.duration_sec != null ? formatDuration(s.duration_sec) : '';
        const durTag = durStr ? `<span class="session-dur">${escapeHtml(durStr)}</span>` : '';
        return `<li class="${isActive ? 'active' : ''}" onclick="selectHistorySession('${escapeHtml(s.session_id)}')">
            <div class="session-row-top">${timeTag}${durTag}${typeTag}${tagDots}</div>
            <div class="session-row-mid"><span class="session-label" title="${escapeHtml(label)}">${escapeHtml(truncated)}</span></div>
            ${branchTag ? `<div class="session-row-bottom">${branchTag}</div>` : ""}
        </li>`;
    }).join("");

    if (total !== undefined) {
        renderPaginationControls(total, page || 1, pageSize || 50);
    }
}

export function renderPaginationControls(total, page, pageSize) {
    let container = document.getElementById("history-pagination");
    if (!container) {
        // Create pagination container after the history list
        const list = document.getElementById("history-sessions-list");
        container = document.createElement("div");
        container.id = "history-pagination";
        container.className = "pagination-controls";
        list.parentNode.appendChild(container);
    }

    const totalPages = Math.max(1, Math.ceil(total / pageSize));
    if (totalPages <= 1) {
        container.innerHTML = `<span class="pagination-info">${total} session${total !== 1 ? 's' : ''}</span>`;
        return;
    }

    container.innerHTML = `
        <button class="btn btn-small" ${page <= 1 ? 'disabled' : ''} onclick="loadHistoryPage(${page - 1})">Prev</button>
        <span class="pagination-info">${page} / ${totalPages} (${total})</span>
        <button class="btn btn-small" ${page >= totalPages ? 'disabled' : ''} onclick="loadHistoryPage(${page + 1})">Next</button>
    `;
}

function stripPulseLines(text) {
    return text.replace(/^\|\|PULSE:(STATUS|SUMMARY|CONFIDENCE)\s[^\|]*\|\|$/gm, '').replace(/\n{3,}/g, '\n\n');
}

export function renderHistoryChat(messages) {
    const container = document.getElementById("history-messages");
    container.innerHTML = "";

    for (const entry of messages) {
        const type = entry.type || "unknown";
        const msg = entry.message || {};
        let content = "";

        if (typeof msg.content === "string") {
            content = msg.content;
        } else if (Array.isArray(msg.content)) {
            content = msg.content
                .filter(b => b.type === "text")
                .map(b => b.text)
                .join("\n");
        }

        if (!content.trim()) continue;

        const isHuman = type === "human" || type === "user";
        const bubbleClass = isHuman ? "human" : "assistant";
        const roleLabel = isHuman ? "You" : "Assistant";

        const bubble = document.createElement("div");
        bubble.className = `chat-bubble ${bubbleClass}`;

        let messageHtml;
        if (isHuman) {
            messageHtml = escapeHtml(content);
        } else {
            const cleaned = stripPulseLines(content);
            messageHtml = marked.parse(cleaned);
        }

        bubble.innerHTML = `
            <div class="role-label">${roleLabel}</div>
            <div class="message-text${!isHuman ? " markdown-body" : ""}">${messageHtml}</div>
            ${isHuman ? `<button class="edit-btn" onclick="editAndResubmit(this)">Edit & Resubmit</button>` : ""}
        `;
        container.appendChild(bubble);
    }

    container.scrollTop = container.scrollHeight;
}

export function updateSessionStatus(status) {
    const el = document.getElementById("session-status");
    if (status) {
        el.querySelector(".status-text").textContent = status;
    }
}

export function updateSessionBranch(branch) {
    const el = document.getElementById("session-branch");
    if (branch) {
        el.querySelector(".branch-text").textContent = branch;
        el.style.display = "";
    } else {
        el.style.display = "none";
    }
}

export function updateWaitingIndicator(s) {
    const dot = document.getElementById("session-status-dot");
    const banner = document.getElementById("waiting-banner");
    if (dot) {
        dot.classList.toggle("waiting", !!s.waiting_for_input);
        dot.classList.toggle("stuck", !!s.stuck);
        dot.classList.toggle("working", !!s.working);
        dot.classList.toggle("done", !!s.done);
    }
    if (banner) {
        // Only show banner for needs-input state
        banner.style.display = s.waiting_for_input ? "" : "none";
        if (s.waiting_for_input) {
            banner.className = "waiting-banner";
            banner.textContent = "⏳ Agent is waiting for input";
        }
    }
}

export function updateSessionSummary(summary) {
    const el = document.getElementById("session-summary");
    if (!el) return;
    if (summary) {
        el.querySelector(".summary-text").textContent = summary;
        el.style.display = "";
    }
    // Don't hide — a null summary from a WebSocket tick shouldn't
    // clear a previously-known goal. The log parser may simply not
    // have found the PULSE:SUMMARY line in the current chunk.
}
