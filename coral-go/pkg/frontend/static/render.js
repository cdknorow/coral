/* Rendering functions for session lists, chat history, and status updates */

import { modelLabel } from './model_labels.js';
import { importOnce } from './import_once.js';
import { state } from './state.js';
import { updateSettingList, upsertByName, parseList } from './settings_lists.js';
import { launchBlockedReason } from './hub_access.js';
import { isCloud, serverFetch, serverForSession, sessionServer, identityKey, splitKey, keyLabel, noteBoardLabels, sessionTeamKey, inTeam, sessionFolderKey, boardFetch, teamFetch, serverUrl } from './server_base.js';
import { applySleepingView } from './sleeping_view.js';
import { distillChip } from './knowledge_distill.js';
import { formatDurationSec as formatDuration, escapeHtml, showToast, escapeAttr, dbg, showView, renderMarkdown, hexToRgba, AGENT_NAME_PALETTE, agentNameColor, boardMessageDisplayText } from './utils.js';
import { registerActions, idOf, tokenOf, textOf } from './actions.js';
import { renderTranscript } from './live_chat.js';
import { renderSidebarTagDots } from './tags.js';
import { getFolderTags, renderFolderTagPills } from './folder_tags.js';
import { updateSectionVisibility } from './sidebar.js';
import { localFirst, isStaleSession, staleReason, showServerLabels, serverLabel, serversShown, serverGroupHeaderHtml, statusMeta } from './server_status.js';
import { syncMobileAgentList } from './mobile.js';
import { getCachedAgentAvailability, refreshTeamAvailabilityIfChanged } from './team_availability.js';
import { liveListRendered } from './ready.js';
import { morphHtml } from './dom_morph.js';
import { isPinnedDir, emptyPins, notePinKeys, pinPathForKey, pinnedFolders } from './pinned_folders.js';
import { LOCAL_SERVER } from './server_base.js';
import { formatTokens } from './format.js';
import { registerLiveListRenderer, redrawLiveList, liveListEl } from './list_hooks.js';
import { _boardChatSelectAll, _boardChatSelectNone, _boardMentionInput, _boardMentionKey, _cancelBoardChatSelect, _exportBoardChatSelected, _insertBoardMention, _toggleBoardChatMsgSelect, _toggleBoardChatPause } from './board_chat.js';
import { _formatCost, _gitStateSummary, _repoBranch, _shortPath, buildSessionTooltip, deriveSessionState, formatShortTime, formatStaleness, getDotClass, resolveSessionIdentity, sessionStateInfo, sessionUnreadCount } from './session_state.js';
import { _getBoardColor } from './board_accents.js';
import { _getGroupOrder, _sortByOrder, _sortGroups } from './list_order.js';
import { _isServerCollapsed, _openServerDetails, renderServerSwitcher } from './server_rows.js';
import { _attachDragListeners, _renderAgentListWithSubgroups, _renderArchivedSection, _renderSessionItem, _within, applySessionSummaryPref, isGroupedByServer, isServerShownOnRows, isSessionSummaryShown } from './session_rows.js';
import { showConfirmModal } from './confirm_dialog.js';
import { getAgentFilter, setAgentFilter } from './agent_filter.js';
import { _collapseAttrs, _isGroupCollapsed } from './group_collapse.js';
import { browserLocal } from './safe_storage.js';
export { groupHeaderKey, toggleGroupCollapse } from './group_collapse.js';
export { hideConfirmModal, showConfirmModal } from './confirm_dialog.js';
export { applySessionSummaryPref, isSessionSummaryShown, toggleArchivedSection } from './session_rows.js';
export { renderServerSwitcher } from './server_rows.js';
export { moveGroupDown, moveGroupUp, moveSessionDown, moveSessionUp } from './list_order.js';
export { setBoardAccentColor } from './board_accents.js';
export { buildSessionTooltip, commandPlaceholder, deriveSessionState, resolveSessionIdentity, sessionStateInfo, terminalDotClass, applyTerminalDot } from './session_state.js';
export { getActiveBoardChat, hideBoardChatTab, showBoardChatTab } from './board_chat.js';

/* ── Helpers ────────────────────────────────────────────────────────── */

/* ── Agent Avatars ──────────────────────────────────────────────────── */

const _roleAvatars = {
    'orchestrator': '👑', 'lead': '🔨', 'lead dev': '🔨', 'architect': '📐',
    'frontend': '🎨', 'backend': '⚙️', 'qa': '🔍', 'quality': '🔍', 'test': '🧪',
    'security': '🛡️', 'devops': '🚀', 'data': '📊', 'llm': '🧠', 'ai': '🤖',
    'design': '✏️', 'writer': '📝', 'content': '📝', 'research': '🔬',
    'marketing': '📣', 'seo': '📈', 'product': '💡', 'manager': '📋',
    'terminal': '💻', 'ops': '🔧', 'infra': '☁️',
};

function _getInitials(name) {
    if (!name) return '?';
    const words = name.trim().split(/\s+/);
    if (words.length >= 2) return (words[0][0] + words[1][0]).toUpperCase();
    return name.slice(0, 2).toUpperCase();
}



   // bumped whenever the board chat panel is rebuilt, to drop stale loads


// What Kill does with the agent's terminal log, said in every Kill confirmation (#4399): it is kept, on both terminal backends
// (#4437: tmux's KillSession no longer removes it), in <data dir>/logs.
export const KILL_LOG_NOTE = "Its terminal log is kept, in the logs folder of Coral's data folder.";
// The same for a confirmation about several agents (Kill All Agents, Kill Team, Reset Team: Reset kills each agent and
// starts it again under a new session id, so the old logs stay as they are).
export const KILL_LOG_NOTE_MANY = "Their terminal logs are kept, in the logs folder of Coral's data folder.";

// killCount is "the 1 agent" or "all 3 agents", and its log note to match.
function killCount(n) {
    return n === 1 ? { agents: 'the 1 agent', note: KILL_LOG_NOTE } : { agents: `all ${n} agents`, note: KILL_LOG_NOTE_MANY };
}

export function killSessionDirect(name, agentType, sessionId) {
    const server = serverForSession(name, sessionId);
    showConfirmModal('Kill Session', `Kill session "${name}"? This will terminate the agent. ${KILL_LOG_NOTE}`, async () => { try {
        const resp = await serverFetch(server, `/api/sessions/live/${encodeURIComponent(name)}/kill`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ agent_type: agentType, session_id: sessionId }),
        });
        const result = await resp.json();
        if (result.error) { showToast(result.error, true); return; }
        if (result.changes_artifact_error) {
            showToast(`Killed, but changes.diff could not be saved: ${result.changes_artifact_error}`, true);
        } else {
            showToast(`Killed and saved changes.diff: ${name}`);
        }
        // Mark as done and preserve for history link instead of removing
        const killed = state.liveSessions.find(s => s.session_id === sessionId && sessionServer(s) === server);
        if (killed) {
            killed.done = true;
            killed.working = false;
            killed.waiting_for_input = false;
            killed.stuck = false;
            killed.sleeping = false;
            state.killedSessions[sessionId] = killed;
        }
        state.liveSessions = state.liveSessions.filter(s => s.session_id !== sessionId);
        if (state.currentSession && state.currentSession.session_id === sessionId) {
            state.currentSession = null;
            showView("welcome-screen");
        }
        redrawLiveList();
    } catch (e) {
        showToast("Failed to kill session", true);
    } });
}

export async function showInfoDirect(name, agentType, sessionId) {
    const { selectLiveSession } = await importOnce('./sessions.js');
    await selectLiveSession(name, agentType, sessionId, serverForSession(name, sessionId));
    const { showInfoModal } = await importOnce('./modals.js');
    showInfoModal();
}

export async function attachDirect(name, agentType, sessionId) {
    try {
        const resp = await serverFetch(serverForSession(name, sessionId), `/api/sessions/live/${encodeURIComponent(name)}/attach`, {
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
    const { selectLiveSession } = await importOnce('./sessions.js');
    await selectLiveSession(name, agentType, sessionId, serverForSession(name, sessionId));
    const { restartSession } = await importOnce('./controls.js');
    restartSession();
}

// ── Confirm Modal ────────────────────────────────────────────────────

// opts.color = { value, auto }: show a colour row (palette swatches plus an
// "Auto" swatch showing `auto`); onConfirm then receives (text, color) where
// color is "#rrggbb" or "" for Auto.
export function showPromptModal(title, label, defaultValue, onConfirm, opts = {}) {
    document.getElementById("prompt-modal-title").textContent = title;
    const colorRow = document.getElementById("prompt-modal-colors");
    let pickedColor = (opts.color && opts.color.value) || "";
    if (colorRow) {
        colorRow.style.display = opts.color ? "" : "none";
        if (opts.color) {
            const sw = (value, bg, label) => `<button type="button" class="color-swatch${value === pickedColor ? " selected" : ""}" data-color="${escapeAttr(value)}" style="--swatch:${bg}" title="${escapeAttr(label)}" aria-label="${escapeAttr(label)}" aria-pressed="${value === pickedColor}">${value ? "" : "A"}</button>`;
            colorRow.querySelector(".color-swatches").innerHTML =
                sw("", opts.color.auto, "Auto") + AGENT_NAME_PALETTE.map(c => sw(c, c, c)).join("");
            colorRow.querySelectorAll(".color-swatch").forEach(btn => {
                btn.onclick = () => {
                    pickedColor = btn.dataset.color;
                    colorRow.querySelectorAll(".color-swatch").forEach(b => {
                        b.classList.toggle("selected", b === btn);
                        b.setAttribute("aria-pressed", String(b === btn));
                    });
                };
            });
        }
    }
    const labelEl = document.getElementById("prompt-modal-label");
    if (labelEl) labelEl.textContent = label || '';
    const input = document.getElementById("prompt-modal-input");
    input.value = defaultValue || '';
    const okBtn = document.getElementById("prompt-modal-ok");
    const newBtn = okBtn.cloneNode(true);
    okBtn.parentNode.replaceChild(newBtn, okBtn);
    const modal = document.getElementById("prompt-modal");
    const submit = () => {
        const val = input.value.trim();
        if (!val) { input.focus(); return; }
        hidePromptModal();
        onConfirm(val, pickedColor);
    };
    newBtn.addEventListener("click", submit);
    input.onkeydown = (e) => { if (e.key === 'Enter') submit(); if (e.key === 'Escape') hidePromptModal(); };
    modal.onclick = (e) => { if (e.target === modal) hidePromptModal(); };
    modal.style.display = "flex";
    setTimeout(() => input.focus(), 50);
}

export function hidePromptModal() {
    document.getElementById("prompt-modal").style.display = "none";
}

export function showAlertModal(title, message, onClose) {
    document.getElementById("alert-modal-title").textContent = title;
    document.getElementById("alert-modal-message").textContent = message;
    const okBtn = document.getElementById("alert-modal-ok");
    const newBtn = okBtn.cloneNode(true);
    okBtn.parentNode.replaceChild(newBtn, okBtn);
    const modal = document.getElementById("alert-modal");
    newBtn.addEventListener("click", () => {
        hideAlertModal();
        onClose?.();
    });
    modal.onclick = (e) => { if (e.target === modal) hideAlertModal(); };
    modal.style.display = "flex";
}

export function hideAlertModal() {
    const modal = document.getElementById("alert-modal");
    if (!modal) return;
    modal.style.display = "none";
}

export function dismissKilledSession(sessionId) {
    delete state.killedSessions[sessionId];
    state.liveSessions = state.liveSessions.filter(s => s.session_id !== sessionId);
    redrawLiveList();
}

export function dismissBoardKilled(boardName) {
    const killedIds = Object.keys(state.killedSessions).filter(sid => {
        const s = state.killedSessions[sid];
        return s && inTeam(s, boardName);
    });
    for (const sid of killedIds) {
        delete state.killedSessions[sid];
    }
    state.liveSessions = state.liveSessions.filter(s => !killedIds.includes(s.session_id));
    redrawLiveList();
}

export function copyFolderPath(path) {
    if (!path) return;
    navigator.clipboard.writeText(path).then(() => {
        showToast("Copied path to clipboard");
    });
}

// The agents a folder or workflow group shows, and only those (#3053): team
// members are listed (and killed) under their team, archived agents in the
// Archived section, and with Group by Team on, workflow-run agents in their
// workflow group. A search narrows the group as it does in the list.
function _groupMembers(groupName) {
    const byTeam = browserLocal.get('coral-group-by-team') !== 'false';
    const shown = state.liveSessions.filter(s => !s.archived && !s.board_project && (!getAgentFilter() || _agentMatches(s, getAgentFilter())));
    if (groupName.startsWith('wf:')) {
        const wf = groupName.slice(3);
        return byTeam ? shown.filter(s => s.workflow_name === wf) : [];
    }
    return shown.filter(s => sessionFolderKey(s) === groupName && !(byTeam && s.workflow_name));
}

export async function killGroup(groupName) {
    const groupSessions = _groupMembers(groupName);
    if (!groupSessions.length) return;

    showConfirmModal(
        "Kill All Agents",
        `Kill ${killCount(groupSessions.length).agents} in "${groupName.startsWith('wf:') ? groupName.slice(3) : keyLabel(groupName)}"? This will terminate ${groupSessions.length === 1 ? 'it' : 'them'}. ${killCount(groupSessions.length).note}`,
        async () => {
            for (const s of groupSessions) {
                try {
                    await serverFetch(sessionServer(s), `/api/sessions/live/${encodeURIComponent(s.name)}/kill`, {
                        method: "POST",
                        headers: { "Content-Type": "application/json" },
                        body: JSON.stringify({ agent_type: s.agent_type, session_id: s.session_id }),
                    });
                    // Preserve killed session for history link
                    s.done = true;
                    s.working = false;
                    s.waiting_for_input = false;
                    s.stuck = false;
                    s.sleeping = false;
                    state.killedSessions[s.session_id] = s;
                } catch (e) {
                    console.error(`Failed to kill ${s.name}:`, e);
                }
            }
            const killedIds = new Set(groupSessions.map(s => s.session_id));
            state.liveSessions = state.liveSessions.filter(s => !killedIds.has(s.session_id));
            redrawLiveList();
        }
    );
}

// The team menu's "Kill All". Through the Coral Hub, killing a whole team needs Launch on its
// machine (#3433): below that it is shown disabled with the reason (#3443).
function killAllItem(boardName) {
    const icon = '<svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="4" y1="4" x2="12" y2="12"/><line x1="12" y1="4" x2="4" y2="12"/></svg>';
    const blocked = launchBlockedReason(splitKey(boardName).server);
    if (blocked) {
        return `<button class="overflow-menu-item overflow-menu-danger overflow-menu-blocked" disabled aria-disabled="true" title="${escapeAttr(blocked)}">
                    ${icon}
                    Kill All <span class="overflow-menu-note">needs Launch</span>
                </button>`;
    }
    return `<button class="overflow-menu-item overflow-menu-danger" data-on-click="team-kill" data-board="${escapeAttr(boardName)}">
                    ${icon}
                    Kill All
                </button>`;
}

// Redraw the team menus once the Coral Hub's access list is in (#3443).
document.addEventListener('coral:server-access-loaded', () => { if (state.liveSessions) redrawLiveList(); });

export async function killBoard(boardName) {
    const boardSessions = state.liveSessions.filter(s => inTeam(s, boardName));
    if (!boardSessions.length) return;
    // Through the Coral Hub a whole team needs Launch (#3433, #3443): say why (a toast, as Delete
    // does) instead of a refusal per agent.
    const blocked = launchBlockedReason(splitKey(boardName).server);
    if (blocked) { showToast(blocked, true); return; }

    showConfirmModal(
        "Kill Team",
        `Kill ${killCount(boardSessions.length).agents} on board "${keyLabel(boardName)}"? This will terminate ${boardSessions.length === 1 ? 'it' : 'them'}. ${killCount(boardSessions.length).note}`,
        async () => {
            for (const s of boardSessions) {
                try {
                    await serverFetch(sessionServer(s), `/api/sessions/live/${encodeURIComponent(s.name)}/kill`, {
                        method: "POST",
                        headers: { "Content-Type": "application/json" },
                        body: JSON.stringify({ agent_type: s.agent_type, session_id: s.session_id }),
                    });
                    s.done = true;
                    s.working = false;
                    s.waiting_for_input = false;
                    s.stuck = false;
                    s.sleeping = false;
                    state.killedSessions[s.session_id] = s;
                } catch (e) {
                    console.error(`Failed to kill ${s.name}:`, e);
                }
            }
            const killedIds = new Set(boardSessions.map(s => s.session_id));
            state.liveSessions = state.liveSessions.filter(s => !killedIds.has(s.session_id));
            redrawLiveList();
        }
    );
}

function _buildTeamTemplateFromBoard(boardName) {
    const boardSessions = (state.liveSessions || []).filter(s => inTeam(s, boardName));
    if (!boardSessions.length) return null;

    const agents = [];
    for (const s of boardSessions) {
        const agent = {
            name: s.display_name || s.board_job_title || s.name,
            prompt: s.prompt || '',
        };
        if (s.agent_type) agent.agent_type = s.agent_type;
        if (s.model) agent.model = s.model;
        if (s.capabilities) agent.capabilities = s.capabilities;
        agents.push(agent);
    }
    return { name: keyLabel(boardName), agents, flags: '' };
}

async function _attachKnowledgeToTemplate(boardName, tmpl) {
    try {
        const resp = await teamFetch(boardName, n => `/api/teams/detail/${encodeURIComponent(n)}/knowledge`);
        if (!resp.ok) return tmpl;
        const data = await resp.json();
        if (!data.exists || !data.agents) return tmpl;
        const knowledgeMap = {};
        for (const a of data.agents) {
            knowledgeMap[a.name.toLowerCase()] = a.content;
        }
        for (const agent of tmpl.agents) {
            const slug = agent.name.toLowerCase().replace(/\s+/g, '-').replace(/[^a-z0-9_-]/g, '');
            if (knowledgeMap[slug]) {
                agent.knowledge = knowledgeMap[slug];
            }
        }
        if (data.index) tmpl.team_knowledge_index = data.index;
    } catch { /* knowledge is optional */ }
    return tmpl;
}

async function _attachWorkingModeToTemplate(boardName, tmpl) {
    try {
        const [modeResp, presetsResp] = await Promise.all([
            boardFetch(boardName, `/working-mode`),
            boardFetch(boardName, `/working-mode/presets`),
        ]);
        if (modeResp.ok) {
            const mode = await modeResp.json();
            if (mode.mode && mode.mode !== 'none') {
                tmpl.working_mode = {
                    mode: mode.mode,
                    dependency_guidance: mode.dependency_guidance || false,
                    custom_instructions: mode.custom_instructions || '',
                };
            }
        }
        if (presetsResp.ok) {
            const data = await presetsResp.json();
            const customPresets = (data.presets || []).filter(p => !p.builtin || p.overridden);
            if (customPresets.length > 0) {
                tmpl.workflow_presets = customPresets.map(p => ({
                    id: p.id,
                    name: p.name,
                    instructions: p.instructions || '',
                    builtin: p.builtin || false,
                }));
            }
        }
    } catch { /* working mode is optional */ }
    return tmpl;
}

export async function shareAgentTeam(boardName) {
    let tmpl = _buildTeamTemplateFromBoard(boardName);
    if (!tmpl) {
        showToast("No agents found on this board", "error");
        return;
    }

    tmpl = await _attachKnowledgeToTemplate(boardName, tmpl);
    tmpl = await _attachWorkingModeToTemplate(boardName, tmpl);

    const template = {
        version: 1,
        type: "coral-team-templates",
        templates: [tmpl],
    };

    const jsonStr = JSON.stringify(template, null, 2);
    const filename = `coral-team-${keyLabel(boardName).replace(/[^a-zA-Z0-9-_]/g, "_")}.json`;

    // Show export modal with JSON content
    const modal = document.createElement('div');
    modal.className = 'modal';
    modal.style.display = 'flex';
    modal.innerHTML = `
        <div class="modal-content" style="width:600px">
            <div class="modal-header">
                <h3>Export Team: ${escapeHtml(keyLabel(boardName))}</h3>
                <p style="color:var(--text-secondary);font-size:0.8125rem;margin:6px 0 0">${tmpl.agents.length} agents</p>
            </div>
            <div class="modal-body">
                <textarea readonly style="width:100%;height:260px;font-family:monospace;font-size:0.75rem;resize:vertical;background:var(--bg-tertiary);color:var(--text-primary);border:1px solid var(--border);border-radius:8px;padding:12px">${escapeHtml(jsonStr)}</textarea>
            </div>
            <div class="modal-actions modal-footer">
                <button class="btn" data-action="close">Close</button>
                <button class="btn" data-action="download">Download</button>
                <button class="btn btn-primary" data-action="copy">Copy to Clipboard</button>
            </div>
        </div>
    `;
    document.body.appendChild(modal);
    modal.addEventListener('click', (e) => {
        const action = e.target.dataset?.action;
        if (action === 'close' || e.target === modal) { modal.remove(); return; }
        if (action === 'copy') {
            navigator.clipboard.writeText(jsonStr).then(() => showToast('Copied to clipboard'));
            return;
        }
        if (action === 'download') {
            const blob = new Blob([jsonStr], { type: 'application/json' });
            const url = URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = url; a.download = filename; a.click();
            URL.revokeObjectURL(url);
            showToast(`Downloaded ${filename}`);
        }
    });
}

export function saveTeamFromSidebar(boardName) {
    showPromptModal('Save Team Template', 'Template name', keyLabel(boardName), async (templateName) => {
        let tmpl = _buildTeamTemplateFromBoard(boardName);
        if (!tmpl) {
            showToast("No agents found on this board", "error");
            return;
        }
        tmpl = await _attachKnowledgeToTemplate(boardName, tmpl);
        tmpl = await _attachWorkingModeToTemplate(boardName, tmpl);
        tmpl.name = templateName;

        // Save to user_settings via the settings API: into the list the server has now (#4423), so a template another
        // tab saved meanwhile is kept.
        try {
            const { raw } = await updateSettingList("saved_team_templates", list => upsertByName(list, [tmpl]),
                { local: parseList(state.settings.saved_team_templates, []) });
            state.settings.saved_team_templates = raw;
        } catch {
            showToast("Could not save the template", "error");
            return;
        }
        showToast(`Saved template "${templateName}" (${tmpl.agents.length} agents)`);
    });
}

export function toggleTeamSleep(boardName, action) {
    const doIt = async () => {
        try {
            const resp = await teamFetch(boardName, n => `/api/sessions/live/team/${encodeURIComponent(n)}/${action}`, { method: 'POST' });
            const data = await resp.json();
            const sleeping = !!data.sleeping;
            for (const s of state.liveSessions) {
                // Team wake leaves archived agents asleep (#3030).
                if (inTeam(s, boardName) && !s.archived) s.sleeping = sleeping;
            }
            redrawLiveList();
            showToast(sleeping ? `Team "${keyLabel(boardName)}" is now sleeping` : `Team "${keyLabel(boardName)}" is awake`);
        } catch (e) {
            showToast('Failed to toggle team sleep', true);
        }
    };
    if (action === 'sleep') {
        showConfirmModal('Sleep Team', `Put all agents on "${keyLabel(boardName)}" to sleep?`, doIt);
    } else {
        doIt();
    }
}

export function toggleAgentSleep(name, agentType, sessionId, action) {
    const server = serverForSession(name, sessionId);
    // Resolves true when the server applied the action.
    const doIt = async () => {
        try {
            const resp = await serverFetch(server, `/api/sessions/live/${encodeURIComponent(sessionId)}/${action}`, { method: 'POST' });
            const data = await resp.json();
            if (!data.ok) { showToast(data.error || 'Failed to toggle sleep', true); return false; }
            const sleeping = !!data.sleeping;
            const s = state.liveSessions.find(s => s.session_id === sessionId && sessionServer(s) === server);
            if (s) {
                s.sleeping = sleeping;
                if (!sleeping) s.archived = false; // waking unarchives (#3030)
            }
            redrawLiveList();
            showToast(sleeping ? `"${name}" is now sleeping` : `"${name}" is awake`);
            return true;
        } catch (e) {
            showToast('Failed to toggle agent sleep', true);
            return false;
        }
    };
    if (action === 'sleep') {
        showConfirmModal('Sleep Agent', `Put "${name}" to sleep?`, doIt);
        return Promise.resolve(false);
    }
    return doIt();
}

/**
 * Archive or unarchive one agent (#3030). Archiving stops a running agent and
 * keeps it out of team wake, Wake All and the main list; unarchiving leaves it
 * sleeping. Resolves true when the server applied it, false on failure or
 * when the archive confirmation is cancelled.
 */
export function toggleAgentArchive(name, agentType, sessionId, action) {
    const server = serverForSession(name, sessionId);
    const doIt = async () => {
        try {
            const resp = await serverFetch(server, `/api/sessions/live/${encodeURIComponent(sessionId)}/${action}`, { method: 'POST' });
            const data = await resp.json();
            if (!resp.ok || !data.ok) { showToast(data.error || `Failed to ${action} agent`, true); return false; }
            const s = state.liveSessions.find(s => s.session_id === sessionId && sessionServer(s) === server);
            if (s) { s.archived = !!data.archived; s.sleeping = !!data.sleeping; }
            redrawLiveList();
            // The agent on screen follows now, not on the next feed tick.
            const cur = state.currentSession;
            if (cur && cur.type === 'live' && cur.session_id === sessionId && sessionServer(cur) === server) {
                if (!!cur.sleeping !== !!data.sleeping) {
                    window.selectLiveSession(cur.name, cur.agent_type, cur.session_id, server);
                } else if (data.sleeping) {
                    applySleepingView(true, { archived: !!data.archived, manageInput: !document.body.classList.contains('popout-mode') });
                }
            }
            showToast(data.archived ? `"${name}" is archived` : `"${name}" is unarchived (sleeping)`);
            return true;
        } catch (e) {
            showToast(`Failed to ${action} agent`, true);
            return false;
        }
    };
    if (action === 'archive') {
        const s = state.liveSessions.find(s => s.session_id === sessionId && sessionServer(s) === server);
        const stops = s && !s.sleeping ? ' It will be stopped.' : '';
        return new Promise(resolve => showConfirmModal('Archive Agent',
            `Archive "${name}"?${stops} Archived agents are not woken with their team or by Wake All.`,
            async () => resolve(await doIt()), () => resolve(false)));
    }
    return doIt();
}


// ── Server info in the agent list (#3085) ────────────────────────────────

export async function toggleShowServer() {
    const previous = state.settings?.show_session_server;
    const next = !isServerShownOnRows();
    state.settings = { ...state.settings, show_session_server: next ? 'True' : 'False' };
    redrawLiveList();
    try {
        const resp = await fetch('/api/settings', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ show_session_server: next }),
        });
        if (!resp.ok) throw new Error(`settings ${resp.status}`);
    } catch (e) {
        console.error('Failed to save server display setting:', e);
        state.settings = { ...state.settings, show_session_server: previous };
        redrawLiveList();
    }
}

export function toggleGroupByServer() {
    try { browserLocal.set('coral-group-by-server', isGroupedByServer() ? 'false' : 'true'); } catch { /* per-viewer only */ }
    redrawLiveList();
}

// A folder icon on folder groups, as teams have the people icon (#3507 v4, the operator's
// request): same size and spacing as the team icon, and named for screen readers.
// A folder entry's Pin / Unpin item (#3630): this Coral's folders only.
const PIN_ICON = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M9 4h6l-1 6 3 3v2H7v-2l3-3z"/><path d="M12 15v6"/></svg>';
function _pinMenuItem(groupName, dir) {
    if (!dir || splitKey(groupName).server !== LOCAL_SERVER) return '';
    const pinned = isPinnedDir(dir);
    return `<button class="overflow-menu-item" data-pin-action="${escapeAttr(pinned ? 'unpin' : 'pin')}" data-on-click="folder-pin" data-mode="${escapeAttr(pinned ? 'unpin' : 'pin')}" data-dir="${escapeAttr(dir)}">${PIN_ICON}${pinned ? 'Unpin folder' : 'Pin folder'}</button>`;
}

const FOLDER_GROUP_ICON = '<svg class="team-group-icon folder-group-icon" role="img" aria-label="Folder" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor"'
    + ' stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/></svg>';

/** The server before a team/folder name ("pro-coral-hub", the server part in italics), when
 *  'Show server' is on and not grouped (#3507: replaces the green-dot chip after the name). An
 *  offline or stale server is dimmed, and its state is in the tooltip and for screen readers. */
function _rowServerPrefix(key) {
    if (!isServerShownOnRows() || isGroupedByServer() || !serversShown().length) return '';
    const id = splitKey(key).server;
    const e = serversShown().find(x => x.id === id) || { id, label: serverLabel(id), status: 'unknown' };
    const name = serverLabel(id);
    const st = statusMeta(e.status);
    // Not checked yet ('unknown') isn't a failure: shown like an online server (QA's review).
    const down = e.status !== 'online' && e.status !== 'unknown';
    return `<span class="row-server-prefix server-${st.cls}${down ? ' is-down' : ''}" title="${escapeAttr(`${e.label || e.id}: ${st.label}`)}">`
        + `<span class="row-server-name">${escapeHtml(name)}</span><span class="row-server-sep" aria-hidden="true">-</span>`
        + `<span class="sr-only"> (server${down ? ', ' + escapeHtml(st.label.toLowerCase()) : ''}) </span></span>`;
}

/** A team/folder header's name, with its server before it when shown: one inline item, so
 *  the name line's flex gap doesn't split "pro-" from "coral-hub". */
function _rowNameWithServer(key) {
    const prefix = _rowServerPrefix(key);
    const name = escapeHtml(keyLabel(key));
    return prefix ? `<span class="row-name-with-server">${prefix}${name}</span>` : name;
}

export async function toggleSessionSummary() {
    const previous = state.settings?.show_session_summary;
    const next = !isSessionSummaryShown();
    state.settings = { ...state.settings, show_session_summary: next ? 'True' : 'False' };
    applySessionSummaryPref();
    try {
        const resp = await fetch('/api/settings', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ show_session_summary: next }),
        });
        if (!resp.ok) throw new Error(`settings ${resp.status}`);
    } catch (e) {
        console.error('Failed to save summary setting:', e);
        state.settings = { ...state.settings, show_session_summary: previous };
        applySessionSummaryPref();
    }
}

export function toggleGroupByTeam() {
    const current = browserLocal.get('coral-group-by-team') !== 'false';
    browserLocal.set('coral-group-by-team', !current ? 'true' : 'false');
    const check = document.getElementById('group-by-team-check-top');
    if (check) check.style.opacity = !current ? '1' : '0.2';
    redrawLiveList();
}

export function expandSleepingGroup(groupName) {
    const key = `coral-sleeping-expanded:${groupName}`;
    const current = browserLocal.get(key) === 'true';
    browserLocal.set(key, current ? 'false' : 'true');
    redrawLiveList();
}

const _RAIL_ICONS = {
    team: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="9" cy="7" r="3"/><path d="M3 21v-2a6 6 0 0 1 12 0v2M16 4a3 3 0 0 1 0 6M21 21v-2a6 6 0 0 0-4-5.65"/></svg>',
    folder: '<svg width="13" height="13" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2 4v8a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V6a1 1 0 0 0-1-1H8L6.5 3H3a1 1 0 0 0-1 1z"/></svg>',
    workflow: '<span class="material-icons" style="font-size:0.8125rem" aria-hidden="true">account_tree</span>',
};

// Role keyword -> Material icon for the rail tiles (first match wins).
const _RAIL_ROLE_ICONS = [
    ['orchestrator', 'hub'], ['architect', 'architecture'], ['design', 'palette'], ['artist', 'palette'],
    ['devops', 'settings'], ['backend', 'dns'], ['frontend', 'code'], ['qa', 'bug_report'], ['test', 'bug_report'],
    ['review', 'rate_review'], ['lead', 'construction'], ['dev', 'code'], ['research', 'travel_explore'],
];

function _railRoleIcon(s) {
    if (s.agent_type === 'terminal') return 'terminal';
    const text = `${s.display_name || ''} ${s.board_job_title || ''}`.toLowerCase();
    const hit = _RAIL_ROLE_ICONS.find(([k]) => text.includes(k));
    return hit ? hit[1] : '';
}

function _railTile(s, active) {
    const label = resolveSessionIdentity(s) + (s.board_project ? ` · ${keyLabel(sessionTeamKey(s))}` : '');
    const dot = getDotClass(s);
    const unread = sessionUnreadCount(s);
    const icon = s.icon && !s.sleeping
        ? `<span class="rail-tile-emoji">${escapeHtml(s.icon)}</span>`
        : (_railRoleIcon(s) ? `<span class="material-icons rail-tile-icon" aria-hidden="true">${_railRoleIcon(s)}</span>` : '');
    let badge = '';
    if (unread > 0) badge = `<span class="rail-tile-badge rail-tile-unread" aria-hidden="true">${unread > 9 ? '9+' : unread}</span>`; // the tile's name says the count (below)
    else if (dot === 'done') badge = '<span class="rail-tile-badge rail-tile-done material-icons" aria-hidden="true">check</span>';
    // The dot is colour only: the tile's own name says the state, in the words the sidebar uses, so it is read once and the dot is skipped (#4276).
    else badge = `<span class="rail-tile-dot avatar-status-dot ${dot}" aria-hidden="true"></span>`;
    const railStale = isStaleSession(s);
    const railLabel = railStale ? `${label}: ${staleReason(s)}` : `${showServerLabels() ? `${label} · ${serverLabel(sessionServer(s))}` : label}, ${sessionStateInfo(s).label}${unread > 0 ? `, ${unread} unread` : ''}`;
    return `<button type="button" class="sidebar-rail-item${active ? ' active' : ''}${railStale ? ' session-stale' : ''}" title="${escapeAttr(railLabel)}" aria-label="${escapeAttr(railLabel)}"${railStale ? ' disabled' : ''}
        ${railStale ? '' : `data-on-click="session-open" data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type)}" data-session-id="${escapeAttr(s.session_id || '')}" data-server="${escapeAttr(sessionServer(s))}"`}><span class="rail-tile"${agentNameColor(s) ? ` style="--agent-name-color:${escapeAttr(agentNameColor(s))}"` : ''}>${icon}<span class="rail-tile-text">${escapeHtml(_getInitials(resolveSessionIdentity(s)))}</span></span>${badge}</button>`;
}

/** Icon rail shown when the sidebar is collapsed: a team/folder header per
 *  group (click to expand or collapse, same state as the full list) with the
 *  awake agents beneath it. Sleeping agents are left out. */
function _renderSidebarRail(sessions) {
    const rail = document.getElementById('sidebar-rail-list');
    if (!rail) return;
    const groupByTeam = browserLocal.get('coral-group-by-team') !== 'false';

    // Same group and agent order as the full list, so both views line up.
    const isOrchSession = s => (s.display_name || s.board_job_title || '').toLowerCase().includes('orchestrator');
    const groups = []; // { kind, label, key, agents } in rendered order
    const bucket = (pick) => {
        const map = {};
        for (const s of sessions) {
            const k = pick(s);
            if (k !== null) (map[k] = map[k] || []).push(s);
        }
        return Object.entries(map);
    };
    // Awake agents in the order their rows render.
    const awakeInRowOrder = (agents) => agents.filter(a => !a.sleeping);

    if (groupByTeam) {
        const teams = _sortGroups(bucket(s => sessionTeamKey(s) || null));
        if (!_getGroupOrder().length) {
            teams.sort((a, b) => (a[1].every(x => x.sleeping) ? 1 : 0) - (b[1].every(x => x.sleeping) ? 1 : 0));
        }
        for (const [name, list] of teams) {
            const ordered = _sortByOrder(list).sort((a, b) => isOrchSession(b) - isOrchSession(a));
            groups.push({ kind: 'team', key: name, label: keyLabel(name), agents: awakeInRowOrder(ordered, list[0]?.working_directory || '') });
        }
        for (const [name, list] of bucket(s => (!s.board_project && s.workflow_name) ? s.workflow_name : null)) {
            groups.push({ kind: 'workflow', key: 'wf:' + name, label: name, agents: list.filter(a => !a.sleeping) });
        }
    }
    const folders = _sortGroups(bucket(s => (groupByTeam && (s.board_project || s.workflow_name)) ? null : sessionFolderKey(s)));
    for (const [name, list] of folders) {
        const ordered = _sortByOrder(list);
        groups.push({ kind: 'folder', key: name, label: keyLabel(name), agents: awakeInRowOrder(ordered, ordered[0]?.working_directory || '') });
    }

    let html = '';
    for (const g of groups) {
        if (!g.agents.length) continue;
        const collapsed = _isGroupCollapsed(g.key);
        const letter = (g.label.trim()[0] || '?').toUpperCase();
        // The same colours as the full list (#3507): a team shows the colour the user picked for it,
        // and every other team, folder and workflow the same neutral chip.
        const accent = g.kind === 'team' ? _getBoardColor(g.key) : null;
        const style = accent ? ` style="--team-accent:${escapeAttr(accent)}"` : '';
        const title = `${g.label} (${g.agents.length}) — click to ${collapsed ? 'expand' : 'collapse'}`;
        html += `<button type="button" class="sidebar-rail-group sidebar-rail-group-${g.kind}${collapsed ? ' collapsed' : ''}"${style} title="${escapeAttr(title)}" aria-expanded="${!collapsed}" aria-label="${escapeAttr(title)}"
            data-on-click="group-collapse" data-group="${escapeAttr(g.key)}">${_RAIL_ICONS[g.kind]}<span class="sidebar-rail-letter">${escapeHtml(letter)}</span></button>`;
        if (collapsed) continue;
        for (const s of g.agents) {
            const active = state.currentSession?.type === 'live' && state.currentSession.session_id === s.session_id;
            html += _railTile(s, active);
        }
    }
    morphHtml(rail, html); // unchanged rail entries keep their nodes (#3121)
}

// ── Agent search (sidebar) ────────────────────────────────────────────

let _filteredMatches = [];

// Matches the agent's name only (what the row label shows), not its team, folder or goal.
// While the list is filtered, an empty pinned folder shows only when its name matches.
function _pinMatchesFilter(p) {
    return !getAgentFilter() || getAgentFilter().split(/\s+/).every(term => String(p.name || '').toLowerCase().includes(term));
}

function _agentMatches(s, q) {
    const names = [resolveSessionIdentity(s), s.display_name, s.auto_name, s.board_job_title]
        .filter(Boolean).join(' ').toLowerCase();
    return q.split(/\s+/).every(term => names.includes(term));
}

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
    noteBoardLabels(sessions); // board_label beside board_project (#3536): what keyLabel shows
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

/** The session a list row shows: live, or a killed one still listed (by id and server, by name when the row has no id). */
function _rowSession(item) {
    const id = item.dataset.sessionId, server = item.dataset.server, name = item.dataset.name;
    const same = x => (id ? x.session_id === id : x.name === name) && sessionServer(x) === server;
    return (state.liveSessions || []).find(same) || Object.values(state.killedSessions || {}).find(same) || null;
}

function _renderLiveSessions(sessions) {
    // Single-agent popout: the sidebar is chrome. Never render rows there —
    // they would carry session ids and destructive kebab actions into the DOM.
    if (document.body.classList.contains('popout-mode')) {
        const list = liveListEl();
        if (list) list.innerHTML = '';
        return;
    }

    // Merge killed sessions back so they appear as "done" with history links.
    // Use a copy to avoid mutating state.liveSessions.
    const liveIds = new Set(sessions.map(s => s.session_id));
    sessions = [...sessions];
    for (const [sid, ks] of Object.entries(state.killedSessions)) {
        if (!liveIds.has(sid)) {
            sessions.push(ks);
        }
    }

    applySessionSummaryPref();
    const unfilteredCount = sessions.length;
    if (getAgentFilter()) sessions = sessions.filter(s => _agentMatches(s, getAgentFilter()));
    _filteredMatches = sessions;
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
    _wireListKeyboard(list);
    _wireListKeyboard(document.getElementById('mobile-agent-list'));

    // Pinned folders count too: the list shows them with no agents (#3630).
    const pinsShown = pinnedFolders().filter(p => _pinMatchesFilter(p)).length;
    updateSectionVisibility('live-sessions', unfilteredCount + pinnedFolders().length);

    if (!sessions.length && !archived.length && !pinsShown) {
        renderServerSwitcher();
        list.innerHTML = `<li class="empty-state">${unfilteredCount ? 'No matching agents' : 'No live sessions'}</li>`;
        return;
    }

    // Sync the Group by Team checkmark
    const groupByTeam = browserLocal.get('coral-group-by-team') !== 'false';
    const check = document.getElementById('group-by-team-check-top');
    if (check) check.style.opacity = groupByTeam ? '1' : '0.2';

    // A team's color: the one the user picked for it (the team's color menu, kept per viewer),
    // else none, so every team shows in the same neutral color (#3507, the operator's request;
    // teams used to get a color hashed from their name).
    function _boardAccentStyle(name) {
        const custom = _getBoardColor(name);
        return custom ? ` style="--team-accent: ${escapeAttr(custom)}"` : '';
    }

    // The servers UI (#3085): a compact button in the search row; with two or
    // more servers the groups below are wrapped in collapsible server headers.
    renderServerSwitcher();
    const renderGroups = (sessions, withPins = true) => {
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
        const accentStyle = _boardAccentStyle(boardName);
        const boardCollapsed = _isGroupCollapsed(boardName);
        const bChevron = boardCollapsed ? '&#x25B8;' : '&#x25BE;';
        const boardLink = '';
        const boardWorkDir = boardSessions[0]?.working_directory || '';
        const boardBranchSession = boardSessions.find(s => s.branch);
        const boardBranch = _repoBranch(boardBranchSession?.repo_name, boardBranchSession?.branch);
        const boardIsSleeping = boardSessions.every(s => s.sleeping);
        const boardSleepIcon = boardIsSleeping ? ' <span class="agent-icon" title="Team is sleeping">🌙</span>' : '';
        const sleepLabel = boardIsSleeping ? 'Wake Team' : 'Sleep Team';
        const sleepAction = boardIsSleeping ? 'wake' : 'sleep';
        const bKebab = `<div class="sidebar-kebab-wrapper group-kebab">
            <button class="sidebar-kebab-btn group-kebab-btn" data-on-click="kebab-toggle" title="Group actions">&#x22EE;</button>
            <div class="sidebar-kebab-menu" style="display:none">
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
                <button class="overflow-menu-item" data-on-click="team-dismiss" data-board="${escapeAttr(boardName)}" title="Remove all killed agents from the sidebar">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h10M5 6V4a3 3 0 0 1 6 0v2M6 10v2M10 10v2M4 6l1 8h6l1-8"/></svg>
                    Dismiss All
                </button>
            </div>
        </div>`;
        const branchLine = boardBranch ? `<div class="board-card-branch-line"><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="5" cy="4" r="2"/><circle cx="5" cy="12" r="2"/><circle cx="12" cy="6" r="2"/><path d="M5 6v4M10.2 5.2 7 8"/></svg> <span class="branch-name">${escapeHtml(boardBranch)}</span><button class="branch-copy-btn" data-on-click="team-branch-copy" data-branch="${escapeAttr(boardBranch)}" title="Copy branch name"><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><rect x="4" y="4" width="9" height="9" rx="1"/><path d="M4 8H3a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v1"/></svg></button></div>` : '';
        const teamDirLine = boardWorkDir ? `<div class="board-card-dir" title="${escapeAttr(boardWorkDir)}"><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 4v8a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V6a1 1 0 0 0-1-1H8L6.5 3H3a1 1 0 0 0-1 1z"/></svg> ${escapeHtml(_shortPath(boardWorkDir, 3))}</div>${branchLine}` : '';
        const teamSubline = `<div class="board-card-subline"><svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="9" cy="7" r="3"/><circle cx="17" cy="7" r="3"/><path d="M3 21v-2a4 4 0 0 1 4-4h4a4 4 0 0 1 4 4v2"/><path d="M17 11a4 4 0 0 1 4 4v2"/></svg> ${boardSessions.length} agents<span class="team-token-usage" data-board="${escapeAttr(boardName)}"></span></div>`;
        const sleepingClass = boardIsSleeping ? ' team-sleeping' : '';
        html += `<li class="session-board-card session-team-group session-board-card-toplevel${sleepingClass}"${accentStyle}>
            <div class="session-group-header board-card-header" data-group-name="${escapeAttr(boardName)}" data-on-click="group-collapse" data-group="${escapeAttr(boardName)}"${_collapseAttrs(boardCollapsed)}>
                <div class="group-header-text"><div class="group-name-line"><svg class="team-group-icon" aria-hidden="true" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><circle cx="9" cy="7" r="3"/><path d="M3 21v-2a6 6 0 0 1 12 0v2M16 4a3 3 0 0 1 0 6M21 21v-2a6 6 0 0 0-4-5.65"/></svg>${_rowNameWithServer(boardName)}${boardSleepIcon}${distillChip(boardName)} <span class="session-group-count">${boardSessions.length}</span></div></div><span class="session-name-spacer"></span>${boardLink}<button type="button" class="folder-copy-btn team-open-btn${escapeAttr(state.selectedTeam === boardName ? ' is-selected' : '')}" data-on-click="team-enter" data-board="${escapeAttr(boardName)}" title="Team view: members, availability and knowledge" aria-label="Open team view for ${escapeAttr(keyLabel(boardName))}"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 11v5M12 8h.01"/></svg></button>${bKebab}
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
    }

    // Step 2b: Render workflow agent groups
    for (const [wfName, wfSessions] of Object.entries(workflowGroups)) {
        const wfCollapsed = _isGroupCollapsed('wf:' + wfName);
        const wfRunId = wfSessions[0]?.workflow_run_id || '';
        const wfSubline = `<div class="board-card-subline"><svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 3H5a2 2 0 0 0-2 2v4m6-6h10a2 2 0 0 1 2 2v4M9 3v18m0 0h10a2 2 0 0 0 2-2V9M9 21H5a2 2 0 0 1-2-2V9m0 0h18"/></svg> Workflow · ${wfSessions.length} agent${wfSessions.length !== 1 ? 's' : ''}${wfRunId ? ' · Run #' + wfRunId : ''}</div>`;
        const wfKebab = `<div class="sidebar-kebab-wrapper group-kebab">
            <button class="sidebar-kebab-btn group-kebab-btn" data-on-click="kebab-toggle" title="Workflow actions">&#x22EE;</button>
            <div class="sidebar-kebab-menu" style="display:none">
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
            <div class="session-group-header board-card-header" data-group-name="wf:${escapeAttr(wfName)}" data-on-click="group-collapse" data-group="wf:${escapeAttr(wfName)}"${_collapseAttrs(wfCollapsed)}>
                <div class="group-header-text"><div class="group-name-line"><span class="material-icons" style="font-size:0.875rem;vertical-align:-2px;margin-right:3px;color:var(--color-tool-agent)" aria-hidden="true">account_tree</span>${escapeHtml(wfName)}</div>${wfSubline}</div><span class="session-name-spacer"></span>${wfKebab}
            </div>
            <ul class="board-card-agents${wfCollapsed ? ' board-card-collapsed' : ''}">`;

        for (const s of wfSessions) {
            html += _renderSessionItem(s, 'wf:' + wfName, true, wfCollapsed, '');
        }
        html += `</ul></li>`;
    }

    // Step 3: Render standalone agents grouped by folder
    const sortedFolders = localFirst(_sortGroups(Object.entries(standaloneByFolder)));
    for (const [groupName, groupSessions] of sortedFolders) {
        const sorted = _sortByOrder(groupSessions);
        const isMulti = sorted.length > 1;
        const countBadge = ` <span class="session-group-count">${sorted.length}</span>`; void isMulti;
        const collapsed = _isGroupCollapsed(groupName);
        const groupDirRaw = sorted[0]?.working_directory || pinPathForKey(groupName);
        const groupKebab = `<div class="sidebar-kebab-wrapper group-kebab">
            <button class="sidebar-kebab-btn group-kebab-btn" data-on-click="kebab-toggle" title="Group actions">&#x22EE;</button>
            <div class="sidebar-kebab-menu" style="display:none">
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
        const groupBranchSession = sorted.find(s => s.branch);
        const groupBranch = _repoBranch(groupBranchSession?.repo_name, groupBranchSession?.branch);
        const groupBranchLine = groupBranch ? `<div class="group-branch-line"><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M6 3v5a3 3 0 0 0 3 3h1"/><circle cx="6" cy="3" r="1.5"/><circle cx="11" cy="11" r="1.5"/></svg> ${escapeHtml(groupBranch)}</div>` : "";
        const fTags = getFolderTags(groupName);
        const tagDots = renderFolderTagPills(fTags);
        const groupWorkDir = groupDirRaw;
        const groupDirLine = groupWorkDir ? `<div class="board-card-dir" title="${escapeAttr(groupWorkDir)}"><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 4v8a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V6a1 1 0 0 0-1-1H8L6.5 3H3a1 1 0 0 0-1 1z"/></svg> ${escapeHtml(_shortPath(groupWorkDir, 3))}</div>` : '';
        const copyBtn = `<button class="folder-copy-btn" data-on-click="folder-copy" data-dir="${escapeAttr(groupWorkDir)}" title="Copy path"><svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="5.5" y="5.5" width="8" height="8" rx="1.5"/><path d="M5.5 10.5h-1a1.5 1.5 0 0 1-1.5-1.5v-5a1.5 1.5 0 0 1 1.5-1.5h5a1.5 1.5 0 0 1 1.5 1.5v1"/></svg></button>`;
        html += `<li class="session-group-header" data-group-name="${escapeAttr(groupName)}" data-on-click="group-collapse" data-group="${escapeAttr(groupName)}"${_collapseAttrs(collapsed)}>
            <div class="group-header-text"><div class="group-name-line">${FOLDER_GROUP_ICON}${_rowNameWithServer(groupName)}${countBadge}</div></div>${tagDots}<span class="session-name-spacer"></span>${copyBtn}${groupKebab}</li>`;

        if (!collapsed && sorted.length) {
            html += _renderAgentListWithSubgroups(sorted, groupWorkDir, false, groupName);
        }
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
        const sorted = _sortByOrder(groupSessions);
        const isMulti = sorted.length > 1;
        const countBadge = ` <span class="session-group-count">${sorted.length}</span>`; void isMulti;
        const collapsed = _isGroupCollapsed(groupName);
        const groupDirRaw = sorted[0]?.working_directory || pinPathForKey(groupName);
        const groupKebab = `<div class="sidebar-kebab-wrapper group-kebab">
            <button class="sidebar-kebab-btn group-kebab-btn" data-on-click="kebab-toggle" title="Group actions">&#x22EE;</button>
            <div class="sidebar-kebab-menu" style="display:none">
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
        const groupBranchSession = sorted.find(s => s.branch);
        const groupBranch = _repoBranch(groupBranchSession?.repo_name, groupBranchSession?.branch);
        const groupBranchLine = groupBranch ? `<div class="group-branch-line"><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M6 3v5a3 3 0 0 0 3 3h1"/><circle cx="6" cy="3" r="1.5"/><circle cx="11" cy="11" r="1.5"/></svg> ${escapeHtml(groupBranch)}</div>` : "";
        const fTags = getFolderTags(groupName);
        const tagDots = renderFolderTagPills(fTags);
        const groupWorkDir = groupDirRaw;
        const groupDirLine = groupWorkDir ? `<div class="board-card-dir" title="${escapeAttr(groupWorkDir)}"><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 4v8a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V6a1 1 0 0 0-1-1H8L6.5 3H3a1 1 0 0 0-1 1z"/></svg> ${escapeHtml(_shortPath(groupWorkDir, 3))}</div>` : '';
        const copyBtn = `<button class="folder-copy-btn" data-on-click="folder-copy" data-dir="${escapeAttr(groupWorkDir)}" title="Copy path"><svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="5.5" y="5.5" width="8" height="8" rx="1.5"/><path d="M5.5 10.5h-1a1.5 1.5 0 0 1-1.5-1.5v-5a1.5 1.5 0 0 1 1.5-1.5h5a1.5 1.5 0 0 1 1.5 1.5v1"/></svg></button>`;
        html += `<li class="session-group-header" data-group-name="${escapeAttr(groupName)}" data-on-click="group-collapse" data-group="${escapeAttr(groupName)}"${_collapseAttrs(collapsed)}>
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
                const bChevron = boardCollapsed ? '&#x25B8;' : '&#x25BE;';
                const boardLink = '';
                const boardWorkDir = boardSessions[0]?.working_directory || '';
                const boardBranchSession = boardSessions.find(s => s.branch);
                const boardBranch = _repoBranch(boardBranchSession?.repo_name, boardBranchSession?.branch);
                const boardIsSleeping = boardSessions.some(s => s.sleeping);
                const boardSleepIcon = boardIsSleeping ? ' <span class="agent-icon" title="Team is sleeping">🌙</span>' : '';
                const sleepLabel = boardIsSleeping ? 'Wake Team' : 'Sleep Team';
                const sleepAction = boardIsSleeping ? 'wake' : 'sleep';
                const bKebab = `<div class="sidebar-kebab-wrapper group-kebab">
                    <button class="sidebar-kebab-btn group-kebab-btn" data-on-click="kebab-toggle" title="Group actions">&#x22EE;</button>
                    <div class="sidebar-kebab-menu" style="display:none">
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
                        <button class="overflow-menu-item" data-on-click="team-dismiss" data-board="${escapeAttr(boardName)}" title="Remove all killed agents from the sidebar">
                            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h10M5 6V4a3 3 0 0 1 6 0v2M6 10v2M10 10v2M4 6l1 8h6l1-8"/></svg>
                            Dismiss All
                        </button>
                    </div>
                </div>`;
                const branchLine = boardBranch ? `<div class="board-card-branch-line"><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="5" cy="4" r="2"/><circle cx="5" cy="12" r="2"/><circle cx="12" cy="6" r="2"/><path d="M5 6v4M10.2 5.2 7 8"/></svg> <span class="branch-name">${escapeHtml(boardBranch)}</span><button class="branch-copy-btn" data-on-click="team-branch-copy" data-branch="${escapeAttr(boardBranch)}" title="Copy branch name"><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><rect x="4" y="4" width="9" height="9" rx="1"/><path d="M4 8H3a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v1"/></svg></button></div>` : '';
                const teamDirLine = boardWorkDir ? `<div class="board-card-dir" title="${escapeAttr(boardWorkDir)}"><svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 4v8a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V6a1 1 0 0 0-1-1H8L6.5 3H3a1 1 0 0 0-1 1z"/></svg> ${escapeHtml(_shortPath(boardWorkDir, 3))}</div>${branchLine}` : '';
                const teamSubline = `<div class="board-card-subline"><svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="9" cy="7" r="3"/><circle cx="17" cy="7" r="3"/><path d="M3 21v-2a4 4 0 0 1 4-4h4a4 4 0 0 1 4 4v2"/><path d="M17 11a4 4 0 0 1 4 4v2"/></svg> ${boardSessions.length} agents</div>`;
                html += `<li class="session-board-card session-team-group"${accentStyle}>
                    <div class="session-group-header board-card-header" data-group-name="${escapeAttr(boardName)}" data-on-click="group-collapse" data-group="${escapeAttr(boardName)}"${_collapseAttrs(boardCollapsed)}>
                        <div class="group-header-text"><div class="group-name-line"><svg class="team-group-icon" aria-hidden="true" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><circle cx="9" cy="7" r="3"/><path d="M3 21v-2a6 6 0 0 1 12 0v2M16 4a3 3 0 0 1 0 6M21 21v-2a6 6 0 0 0-4-5.65"/></svg>${_rowNameWithServer(boardName)}${boardSleepIcon}${distillChip(boardName)} <span class="session-group-count">${boardSessions.length}</span></div></div><span class="session-name-spacer"></span>${boardLink}<button type="button" class="folder-copy-btn team-open-btn${escapeAttr(state.selectedTeam === boardName ? ' is-selected' : '')}" data-on-click="team-enter" data-board="${escapeAttr(boardName)}" title="Team view: members, availability and knowledge" aria-label="Open team view for ${escapeAttr(keyLabel(boardName))}"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 11v5M12 8h.01"/></svg></button>${bKebab}
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
    }

    } // end if/else groupByTeam
    return html;
    };

    let html = '';
    const shownServers = serversShown();
    if (isGroupedByServer()) {
        for (const e of shownServers) {
            const own = sessions.filter(x => sessionServer(x) === e.id);
            const collapsed = _isServerCollapsed(e.id);
            html += serverGroupHeaderHtml(e, own.length, collapsed, _openServerDetails.has(e.id));
            if (!collapsed) html += renderGroups(own, e.id === LOCAL_SERVER);
        }
    } else {
        html += renderGroups(sessions);
    }

    html += _renderArchivedSection(archived);
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
        if (again) again.focus({ preventScroll: true });
    }

    // The phone's page takes the list itself (it is not copied: #4497)
    syncMobileAgentList();

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

// Hidden tabs skip availability polls; catch up as soon as the tab is shown.
document.addEventListener('visibilitychange', () => {
    if (!document.hidden && state.liveSessions?.length) redrawLiveList(); // also drops a frame scheduled while hidden
});

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
        const isGroup = s.type === 'group' || (s.session_id && s.session_id.startsWith('board:'));
        if (isGroup) {
            const projectName = s.title || s.session_id.replace(/^board:/, '');
            const label = projectName;
            const truncated = label.length > 40 ? label.substring(0, 40) + "..." : label;
            const isActive = state.currentSession && state.currentSession.type === "board" && state.currentSession.name === projectName;
            const timeStr = s.updated_at ? formatShortTime(s.updated_at) : "";
            const timeTag = timeStr ? `<span class="session-time">${escapeHtml(timeStr)}</span>` : "";
            const countInfo = s.message_count != null ? `${s.message_count} msgs` : "";
            const subsInfo = s.subscriber_count != null ? `${s.subscriber_count} members` : "";
            const meta = [countInfo, subsInfo].filter(Boolean).join(' · ');
            return `<li class="${escapeAttr(isActive ? 'active' : '')}" data-on-click="board-open" data-board="${escapeAttr(projectName)}">
                <div class="session-row-top">${timeTag}<span class="badge board-badge">Group</span></div>
                <div class="session-row-mid"><span class="session-label" title="${escapeHtml(label)}">${escapeHtml(truncated)}</span></div>
                ${meta ? `<div class="session-row-bottom"><span class="session-meta">${escapeHtml(meta)}</span></div>` : ""}
            </li>`;
        }
        const label = s.summary_title || s.summary || s.session_id;
        const truncated = label.length > 40 ? label.substring(0, 40) + "..." : label;
        const isActive = state.currentSession && state.currentSession.type === "history" && state.currentSession.name === s.session_id;
        const typeTag = (s.source_type === "gemini" || s.source_type === "agy" || s.source_type === "antigravity")
            ? `<span class="badge agy">agy</span>`
            : "";
        const agentName = s.display_name || s.agent_name || "";
        const agentTag = agentName ? `<span class="sidebar-agent-name">${escapeHtml(agentName)}</span>` : "";
        const reminderTag = getCachedAgentAvailability(s)?.reminder
            ? `<span class="agent-reminder-icon" title="Periodic reminder active" aria-label="Periodic reminder active">⏰</span>`
            : "";
        const sourceTypeTag = s.source_type ? `<span class="sidebar-source-type">${escapeHtml(s.source_type)}</span>` : "";
        const branchDisplay = _repoBranch(s.repo_name, s.branch);
        const branchTag = branchDisplay ? `<span class="sidebar-branch">${escapeHtml(branchDisplay)}</span>` : "";
        const tagDots = s.tags ? renderSidebarTagDots(s.tags) : "";
        const timeStr = s.last_timestamp ? formatShortTime(s.last_timestamp) : "";
        const timeTag = timeStr ? `<span class="session-time">${escapeHtml(timeStr)}</span>` : "";
        const durStr = s.duration_sec != null ? formatDuration(s.duration_sec) : '';
        const durTag = durStr ? `<span class="session-dur">${escapeHtml(durStr)}</span>` : '';
        const bottomMeta = [agentTag, reminderTag, sourceTypeTag, branchTag].filter(Boolean).join('');
        return `<li class="${escapeAttr(isActive ? 'active' : '')}" data-on-click="history-open" data-session-id="${escapeAttr(s.session_id)}">
            <div class="session-row-top">${timeTag}${durTag}${typeTag}${tagDots}</div>
            <div class="session-row-mid"><span class="session-label" title="${escapeHtml(label)}">${escapeHtml(truncated)}</span></div>
            ${bottomMeta ? `<div class="session-row-bottom">${bottomMeta}</div>` : ""}
        </li>`;
    }).join("");

    if (total !== undefined) {
        renderPaginationControls(total, page || 1, pageSize || 50);
    }
}

 function renderPaginationControls(total, page, pageSize) {
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
        <button class="btn btn-small" ${page <= 1 ? 'disabled' : ''} data-on-click="history-page" data-page="${Number(page - 1)}">Prev</button>
        <span class="pagination-info">${page} / ${totalPages} (${total})</span>
        <button class="btn btn-small" ${page >= totalPages ? 'disabled' : ''} data-on-click="history-page" data-page="${Number(page + 1)}">Next</button>
    `;
}

export function renderHistoryChat(messages, agentType = "claude", baseIndex = 0, focusIndex = null) {
    const container = document.getElementById("history-messages");
    container.innerHTML = "";
    container.append(...renderHistoryNodes(messages, agentType, baseIndex));
    container.scrollTop = container.scrollHeight;
    if (focusIndex != null) {
        requestAnimationFrame(() => {
            const target = container.querySelector(`[data-message-index="${CSS.escape(String(focusIndex))}"]`);
            if (target) {
                target.classList.add('chat-search-target');
                target.scrollIntoView({ block: 'center', behavior: 'smooth' });
                target.setAttribute('tabindex', '-1');
                target.focus({ preventScroll: true });
            } else {
                const notice = document.createElement('div');
                notice.className = 'chat-search-status';
                notice.textContent = 'The matching message is no longer available.';
                container.prepend(notice);
            }
        });
    }
}

/** A page of the history transcript as nodes, not yet in the page (history_pages.js inserts
 *  pages above or below what is shown, #3718). Same renderer as the live Chat view: replies as
 *  prose, tool calls and their output folded into collapsed "N steps" groups. baseIndex is the
 *  first message's index in the whole transcript. */
export function renderHistoryNodes(messages, agentType = "claude", baseIndex = 0) {
    const holder = document.createElement("div");
    const normalized = messages.map((entry, index) => {
        const value = normalizeHistoryEntry(entry);
        if (value) value.__coralMessageIndex = baseIndex + index;
        return value;
    }).filter(Boolean);
    renderTranscript(normalized, holder, agentType);
    for (const bubble of holder.querySelectorAll(".chat-bubble.human")) {
        bubble.insertAdjacentHTML("beforeend",
            `<button class="edit-btn" data-on-click="msg-edit-resubmit">Edit & Resubmit</button>`);
    }
    return [...holder.childNodes];
}

// The reader returns normalized entries; older raw JSONL entries (content
// nested under entry.message) are reduced to plain user/assistant text.
function normalizeHistoryEntry(entry) {
    if (entry.message) {
        const msg = entry.message;
        let content = "";
        if (typeof msg.content === "string") content = msg.content;
        else if (typeof msg.text === "string") content = msg.text;
        else if (Array.isArray(msg.content)) {
            content = msg.content.filter(b => b.type === "text").map(b => b.text).join("\n");
        }
        if (!content.trim()) return null;
        entry = (entry.type === "human" || entry.type === "user")
            ? { type: "user", content }
            : { type: "assistant", text: content };
    }
    if (entry.type === "human") return { ...entry, type: "user" };
    if (entry.type === "assistant") {
        const text = typeof entry.text === "string" ? entry.text : (typeof entry.content === "string" ? entry.content : "");
        return { ...entry, text };
    }
    return entry;
}

export function updateSessionStatus(status) {
    const el = document.getElementById("session-status");
    if (status) {
        el.querySelector(".status-text").textContent = status;
    }
}

/** Delegated keyboard selection for a list container (survives the mobile
 *  cloneNode, which drops element listeners). Enter/Space on a focused row
 *  selects it; events from the kebab, the open-tab anchor or the sparkle are
 *  left to those controls; Escape closes any open kebab. */
function _wireListKeyboard(container) {
    if (!container || container.dataset.kbWired) return;
    container.dataset.kbWired = '1';
    container.addEventListener('keydown', (e) => {
        if (e.key === 'Escape') {
            if (window.closeSidebarKebabs) window.closeSidebarKebabs();
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

/** Team details: anchored disclosure with dialog semantics for the metadata
 *  that left the team header (directory, branch, agents, tokens, activity). */
let _teamDetailsReturnFocus = null;
export function showTeamDetails(boardName) {
    hideTeamDetails();
    const sessions = (state.liveSessions || []).filter(s => inTeam(s, boardName));
    const header = document.querySelector(`.session-group-header[data-group-name="${CSS.escape(boardName)}"]`);
    _teamDetailsReturnFocus = (document.activeElement && document.activeElement !== document.body)
        ? document.activeElement
        : (header ? header.querySelector('.sidebar-kebab-btn') : null);
    const dir = sessions.find(s => s.working_directory)?.working_directory || '';
    const branchSession = sessions.find(s => s.branch);
    const branch = _repoBranch(branchSession?.repo_name, branchSession?.branch) || '—';
    let minStale = null;
    for (const s of sessions) {
        if (typeof s.staleness_seconds === 'number' && (minStale === null || s.staleness_seconds < minStale)) minStale = s.staleness_seconds;
    }
    const names = sessions.map(s => resolveSessionIdentity(s));
    const titleId = 'team-details-title';
    const pop = document.createElement('div');
    pop.id = 'team-details-popover';
    pop.className = 'team-details-popover';
    pop.setAttribute('role', 'dialog');
    pop.setAttribute('aria-labelledby', titleId);
    pop.setAttribute('tabindex', '-1');
    pop.innerHTML = `
        <div class="team-details-head">
            <span id="${escapeAttr(titleId)}" class="team-details-title">${escapeHtml(keyLabel(boardName))}</span>
            <button type="button" class="team-details-close" aria-label="Close team details" data-on-click="team-details-close">&times;</button>
        </div>
        <dl class="team-details-list">
            <dt>Directory</dt><dd title="${escapeAttr(dir)}">${dir ? escapeHtml(_shortPath(dir, 3)) : '—'}</dd>
            <dt>Branch</dt><dd>${escapeHtml(branch)}</dd>
            <dt>Agents</dt><dd>${sessions.length}${names.length ? ' · ' + escapeHtml(names.join(', ')) : ''}</dd>
            <dt>Tokens</dt><dd class="team-token-usage team-details-tokens" data-board="${escapeAttr(boardName)}">…</dd>
            <dt>Last activity</dt><dd>${escapeHtml(formatStaleness(minStale))}</dd>
        </dl>`;
    document.body.appendChild(pop);
    const rect = header ? header.getBoundingClientRect() : { left: 12, bottom: 60 };
    pop.style.left = Math.max(8, Math.min(rect.left, window.innerWidth - pop.offsetWidth - 8)) + 'px';
    pop.style.top = Math.min(rect.bottom + 4, window.innerHeight - pop.offsetHeight - 8) + 'px';
    getTeamTokenUsage(boardName).then(text => {
        const el = pop.querySelector('.team-details-tokens');
        if (el) el.textContent = text || '—';
    }).catch(() => {});
    pop.addEventListener('keydown', (e) => { if (e.key === 'Escape') { e.stopPropagation(); hideTeamDetails(); } });
    setTimeout(() => { document.addEventListener('mousedown', _teamDetailsOutside, true); }, 0);
    pop.focus();
}

function _teamDetailsOutside(e) {
    const pop = document.getElementById('team-details-popover');
    if (pop && !pop.contains(e.target)) hideTeamDetails();
}

export function hideTeamDetails() {
    const pop = document.getElementById('team-details-popover');
    document.removeEventListener('mousedown', _teamDetailsOutside, true);
    if (!pop) return;
    pop.remove();
    const back = _teamDetailsReturnFocus;
    _teamDetailsReturnFocus = null;
    if (back && document.contains(back) && typeof back.focus === 'function') back.focus();
}

export async function updateTokenUsage(sessionId) {
    const el = document.getElementById('session-token-usage');
    if (!el) return;
    if (!sessionId) { el.style.display = 'none'; return; }

    try {
        const resp = await serverFetch(serverForSession(null, sessionId), `/api/token-usage?session_id=${encodeURIComponent(sessionId)}`);
        if (!resp.ok) { el.style.display = 'none'; return; }
        const data = await resp.json();
        if (!data.totals.total_tokens || data.totals.total_tokens === 0) { el.style.display = 'none'; return; }

        const parts = [formatTokens(data.totals.total_tokens) + ' tokens'];
        if (data.totals.cost_usd > 0) parts.push(_formatCost(data.totals.cost_usd));
        el.textContent = parts.join(' · ');
        el.style.display = '';
    } catch {
        el.style.display = 'none';
    }
}

export async function updateHistoryTokenUsage(sessionId) {
    const container = document.getElementById('history-token-usage');
    if (!container) return;
    if (!sessionId) { container.style.display = 'none'; return; }

    try {
        const resp = await fetch(`/api/token-usage?session_id=${encodeURIComponent(sessionId)}`);
        if (!resp.ok) { container.style.display = 'none'; return; }
        const data = await resp.json();
        const t = data.totals;
        if (!t || !t.total_tokens || t.total_tokens === 0) { container.style.display = 'none'; return; }

        document.getElementById('htu-input').textContent = formatTokens(t.input_tokens || 0);
        document.getElementById('htu-output').textContent = formatTokens(t.output_tokens || 0);
        const cache = (t.cache_read_tokens || 0) + (t.cache_write_tokens || 0);
        document.getElementById('htu-cache').textContent = cache > 0 ? formatTokens(cache) : '—';
        document.getElementById('htu-requests').textContent = String(t.num_sessions || '—');
        document.getElementById('htu-cost').textContent = t.cost_usd > 0 ? _formatCost(t.cost_usd) : '—';
        container.style.display = '';
    } catch {
        container.style.display = 'none';
    }
}

 async function getTeamTokenUsage(boardName) {
    try {
        const resp = await teamFetch(boardName, n => `/api/token-usage?board_name=${encodeURIComponent(n)}`);
        if (!resp.ok) return null;
        const data = await resp.json();
        if (!data.totals.total_tokens || data.totals.total_tokens === 0) return null;
        const parts = [formatTokens(data.totals.total_tokens) + ' tokens'];
        if (data.totals.cost_usd > 0) parts.push(_formatCost(data.totals.cost_usd));
        if (data.totals.execution_time_sec > 0) parts.push(formatDuration(data.totals.execution_time_sec));
        return parts.join(' · ');
    } catch {
        return null;
    }
}

export async function showTeamTokenUsage(boardName) {
    const modal = document.getElementById('task-detail-modal');
    const titleEl = document.getElementById('task-detail-modal-title');
    const content = document.getElementById('task-detail-content');
    if (!modal || !content) return;

    titleEl.textContent = `Token Usage — ${keyLabel(boardName)}`;
    content.innerHTML = '<div style="text-align:center;padding:16px;color:var(--text-muted)">Loading...</div>';
    modal.style.display = '';
    modal.onclick = (e) => { if (e.target === modal) { modal.style.display = 'none'; } };

    // Token usage is read from Coral's unified usage API.
    const usageData = await teamFetch(boardName, n => `/api/token-usage?board_name=${encodeURIComponent(n)}`)
        .then(resp => resp.ok ? resp.json() : null)
        .catch(() => null);
    const agents = (usageData?.by_agent || []).map(a => ({
        name: a.agent_name || a.session_id || 'Unknown agent',
        total_input_tokens: a.input_tokens || 0,
        total_output_tokens: a.output_tokens || 0,
        total_cache_read_tokens: a.cache_read_tokens || 0,
        total_cache_write_tokens: a.cache_write_tokens || 0,
        total_cost_usd: a.cost_usd || 0,
        total_requests: a.num_sessions || a.requests || 0,
    })).filter(a => a.total_requests > 0 || a.total_cost_usd > 0);

    // Compute totals
    let totalIn = 0, totalOut = 0, totalCacheR = 0, totalCacheW = 0, totalCost = 0, totalReqs = 0;
    for (const a of agents) {
        totalIn += a.total_input_tokens || 0;
        totalOut += a.total_output_tokens || 0;
        totalCacheR += a.total_cache_read_tokens || 0;
        totalCacheW += a.total_cache_write_tokens || 0;
        totalCost += a.total_cost_usd || 0;
        totalReqs += a.total_requests || 0;
    }

    if (agents.length === 0) {
        content.innerHTML = '<div style="text-align:center;padding:16px;color:var(--text-muted)">No token usage recorded for this team</div>';
        return;
    }

    // Render summary + agent table
    const executionTimeRow = usageData?.totals?.execution_time_sec > 0
        ? `<span class="info-token-label">Execution Time</span><span class="info-token-value">${formatDuration(usageData.totals.execution_time_sec)}</span>`
        : '';
    let html = `<div class="info-token-grid" style="margin-bottom:16px">
        <span class="info-token-label">Input</span><span class="info-token-value">${formatTokens(totalIn)}</span>
        <span class="info-token-label">Output</span><span class="info-token-value">${formatTokens(totalOut)}</span>
        <span class="info-token-label">Cache Read</span><span class="info-token-value">${formatTokens(totalCacheR)}</span>
        <span class="info-token-label">Cache Write</span><span class="info-token-value">${formatTokens(totalCacheW)}</span>
        <span class="info-token-label">Requests</span><span class="info-token-value">${totalReqs}</span>
        <span class="info-token-label">Cost</span><span class="info-token-value">${_formatCost(totalCost)}</span>
        ${executionTimeRow}
    </div>`;

    html += `<table class="cost-table" style="font-size:0.75rem">
        <thead><tr>
            <th>Agent</th>
            <th style="text-align:right">Input</th>
            <th style="text-align:right">Output</th>
            <th style="text-align:right">Cache R</th>
            <th style="text-align:right">Cache W</th>
            <th style="text-align:right">Cost</th>
        </tr></thead><tbody>`;

    for (const a of agents.sort((x, y) => (y.total_cost_usd || 0) - (x.total_cost_usd || 0))) {
        html += `<tr>
            <td style="font-weight:500">${escapeHtml(a.name)}</td>
            <td style="text-align:right">${formatTokens(a.total_input_tokens || 0)}</td>
            <td style="text-align:right">${formatTokens(a.total_output_tokens || 0)}</td>
            <td style="text-align:right">${formatTokens(a.total_cache_read_tokens || 0)}</td>
            <td style="text-align:right">${formatTokens(a.total_cache_write_tokens || 0)}</td>
            <td style="text-align:right;font-weight:600;color:var(--accent)">${_formatCost(a.total_cost_usd || 0)}</td>
        </tr>`;
    }
    html += '</tbody></table>';
    content.innerHTML = html;
}

export async function distillTeamKnowledge(boardName) {
    if (window.enterTeamContext) await window.enterTeamContext(boardName, { tab: 'knowledge' });
    const { distillKnowledgeInTab } = await importOnce('./team_artifacts.js');
    distillKnowledgeInTab(boardName);
}

export async function showTeamKnowledge(boardName) {
    const { reloadKnowledgeTab } = await importOnce('./team_artifacts.js');
    if (window.enterTeamContext) await window.enterTeamContext(boardName, { tab: 'knowledge' });
    reloadKnowledgeTab();
}

// The terminal header shows the git branch; only an agent with no branch
// shows its folder instead (full path on hover).
let _headerDir = "";
let _headerHasBranch = false;
function _syncHeaderFolder() {
    const el = document.getElementById("terminal-header-folder");
    if (!el) return;
    const show = !!_headerDir && !_headerHasBranch;
    el.hidden = !show;
    if (!show) return;
    el.querySelector(".terminal-folder-text").textContent = _shortPath(_headerDir, 2);
    el.title = _headerDir;
}

export function updateSessionFolder(dir) {
    _headerDir = dir || "";
    _syncHeaderFolder();
}

/** The terminal header's model chip: "Claude · Opus 4.1", hidden when the agent has no model on record (#4066). */
export function updateSessionModel(agentType, model, server) {
    const el = document.getElementById("terminal-header-model");
    if (!el) return;
    const text = modelLabel(agentType, model, server);
    el.hidden = !text;
    el.textContent = text;
    el.title = text ? model : "";
}

export function updateSessionBranch(branch, repoName, gitState) {
    const display = _repoBranch(repoName, branch);
    const el = document.getElementById("session-branch");
    if (el) {
        if (display) {
            el.querySelector(".branch-text").textContent = display;
            const gsEl = el.querySelector(".branch-git-state");
            if (gsEl) {
                const wt = gitState?.is_worktree ? '<span class="gs-worktree" title="This agent is working in a linked git worktree">worktree</span> ' : '';
                gsEl.innerHTML = wt + _gitStateSummary(gitState);
            }
            el.style.display = "";
        } else {
            el.style.display = "none";
        }
    }
    _headerHasBranch = !!display;
    _syncHeaderFolder();
    // Same info in the terminal header, which has room beside the agent name.
    const th = document.getElementById("terminal-header-branch");
    if (th) {
        th.hidden = !display;
        if (display) {
            th.querySelector(".terminal-branch-text").textContent = display;
            th.title = display;
            const gs = th.querySelector(".branch-git-state");
            if (gs) {
                const wt = gitState?.is_worktree ? '<span class="gs-worktree" title="This agent is working in a linked git worktree">worktree</span> ' : '';
                gs.innerHTML = wt + _gitStateSummary(gitState);
            }
        }
    }
}

export function updateWaitingIndicator(s) {
    const dot = document.getElementById("session-status-dot");
    const banner = document.getElementById("waiting-banner");
    if (dot) {
        const key = deriveSessionState(s);
        dot.classList.toggle("waiting", key === 'needs_input' || key === 'check_terminal');
        dot.classList.toggle("stuck", key === 'stuck');
        dot.classList.toggle("working", key === 'working');
        dot.classList.toggle("your-turn", key === 'your_turn');
        dot.classList.remove("done"); // green no longer means anything
    }
    if (banner) {
        // Two states share this banner. waiting_for_input is a running agent
        // asking a question. not_started is an agent that has never done
        // anything — blocked on a first-run prompt, or simply never given a
        // task; those are indistinguishable from outside the CLI, so the copy
        // claims only what is true of both. We never answer a prompt on the
        // user's behalf: the banner's job is to point at the terminal, where
        // the question is already on screen.
        // Same resolver as the rows: the banner follows the WINNING state, so a
        // sleeping or stuck agent with a stale waiting flag never shows it.
        const bannerKey = deriveSessionState(s);
        banner.style.display = (bannerKey === 'needs_input' || bannerKey === 'check_terminal') ? "" : "none";
        if (bannerKey === 'needs_input') {
            banner.className = "waiting-banner";
            banner.textContent = s.waiting_kind === 'login' ? "⏳ Agent needs you to sign in (in its terminal)" : "⏳ Agent is waiting for input";
        } else if (bannerKey === 'check_terminal') {
            banner.className = "waiting-banner waiting-banner-not-started";
            banner.textContent =
                "⏳ This agent hasn't done anything yet — check the terminal below. Some agents ask permission to trust a folder the first time they run in it.";
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
    // clear a previously-known goal.
}

// ── The sidebar, its menus, the history list and the board chat panel (#3645) ──
// Markup names these actions in data-on-*; their values are data attributes, checked here (a bad
// one does nothing). Each calls the same global the inline handler did. A menu item stops the
// click and closes the menus first, as before. The destructive ones (agent-kill, agent-restart,
// agent-rename, group-kill, team-kill, team-reset) are on actions.js's DESTRUCTIVE_ACTIONS.
const W = name => (...a) => window[name](...a);
const agentArgs = el => {
    const name = textOf(el.dataset.name), type = tokenOf(el.dataset.agentType), sid = textOf(el.dataset.sessionId);
    return name && type ? [name, type, sid || ''] : null;
};
const menu = f => (el, event) => { event.stopPropagation(); window.closeSidebarKebabs(); f(el, event); };
const withAgent = (fn, modes) => menu(el => {
    const a = agentArgs(el);
    if (!a) return;
    if (modes) { if (modes.includes(el.dataset.mode)) W(fn)(...a, el.dataset.mode); } else W(fn)(...a);
});
const withBoard = fn => menu(el => { const b = textOf(el.dataset.board); if (b) W(fn)(b); });
const withSid = fn => menu(el => { const sid = textOf(el.dataset.sessionId); if (sid) W(fn)(sid); });
const dirOf = el => textOf(el.dataset.dir, 4096);
const UPDOWN = { up: true, down: true };
registerActions({
    'kebab-close': (el, event) => { event.stopPropagation(); window.closeSidebarKebabs(); },
    'kebab-toggle': (el, event) => { event.stopPropagation(); window.toggleSidebarKebab(el); },
    'agent-info': withAgent('showInfoDirect'),
    'agent-kill': withAgent('killSessionDirect'),
    'agent-restart': withAgent('restartDirect'),
    'agent-rename': withAgent('renameAgent'),
    'agent-icon': withAgent('setAgentIcon'),
    'agent-attach': withAgent('attachDirect'),
    'agent-sleep': withAgent('toggleAgentSleep', ['sleep', 'wake']),
    'agent-archive': withAgent('toggleAgentArchive', ['archive', 'unarchive']),
    'agent-move': menu(el => { const sid = textOf(el.dataset.sessionId); if (sid && UPDOWN[el.dataset.dir]) W(el.dataset.dir === 'up' ? 'moveSessionUp' : 'moveSessionDown')(sid); }),
    'agent-history': withSid('selectHistorySession'),
    'agent-dismiss': withSid('dismissKilledSession'),
    'team-sleep': menu(el => { const b = textOf(el.dataset.board); if (b && ['sleep', 'wake'].includes(el.dataset.mode)) W('toggleTeamSleep')(b, el.dataset.mode); }),
    'team-working-mode': withBoard('showTeamWorkingModeWorkspace'),
    'team-tokens': withBoard('showTeamTokenUsage'),
    'team-availability': withBoard('showTeamAvailabilityWorkspace'),
    'team-share': withBoard('shareAgentTeam'),
    'team-save': withBoard('saveTeamFromSidebar'),
    'team-distill': withBoard('distillTeamKnowledge'),
    'team-dismiss': withBoard('dismissBoardKilled'),
    'team-reset': withBoard('resetTeam'),
    'team-kill': withBoard('killBoard'),
    'team-add-agent': menu(el => { const b = textOf(el.dataset.board); if (b) W('showAddAgentToBoard')(b, dirOf(el) || ''); }),
    'team-add-terminal': menu(el => { const b = textOf(el.dataset.board); if (b) W('launchTerminalToBoard')(b, dirOf(el) || ''); }),
    'team-accent': (el, event) => { event.stopPropagation(); const b = textOf(el.dataset.board); if (b) window.setBoardAccentColor(b); window.closeSidebarKebabs(); },
    'team-enter': (el, event) => { event.stopPropagation(); const b = textOf(el.dataset.board); if (b) window.enterTeamContext(b); },
    'team-branch-copy': (el, event) => {
        event.stopPropagation();
        const branch = textOf(el.dataset.branch, 500);
        if (!branch) return;
        navigator.clipboard.writeText(branch);
        const icon = el.innerHTML;
        el.textContent = '✓';
        setTimeout(() => { el.innerHTML = icon; }, 1000);
    },
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
    // A row (or a rail tile) opens its agent, or a done one's history.
    'session-open': el => {
        const sid = textOf(el.dataset.sessionId) || '';
        if (el.dataset.done === '1') { if (sid) window.selectHistorySession(sid); return; }
        const name = textOf(el.dataset.name), type = tokenOf(el.dataset.agentType);
        if (name && type) window.selectLiveSession(name, type, sid, textOf(el.dataset.server) || '');
    },
    'history-open': el => { const sid = textOf(el.dataset.sessionId); if (sid) window.selectHistorySession(sid); },
    'history-page': el => { const n = idOf(el.dataset.page); if (n) window.loadHistoryPage(n); },
    'board-open': el => { const b = textOf(el.dataset.board); if (b) window.selectBoardProject(b); },
    'msg-edit-resubmit': el => window.editAndResubmit(el),
    'team-details-close': () => window.hideTeamDetails(),
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
