/* Moved out of modals.js as it was (#4684): The AI team wizard, the team-agent dialog, and the Launch dialog's team form: its agents, templates, quick launch and Launch Team. No behaviour change. */
import { importOnce } from './import_once.js';
import { registerDialog } from './modal_chrome.js';
import { scrollBehavior } from './motion.js';
import { showToast, escapeHtml, escapeAttr, boardNameProblem } from './utils.js';
import { loadLiveSessions } from './api.js';
import { loadBoardProjects } from './message_board.js';
import { recordRecentDir } from './browser.js';
import { serverFetch, LOCAL_SERVER } from './server_base.js';
const { launch } = await importOnce('./launch_state.js');
const { _acfServer, _effectiveServer, _settingsFor } = await importOnce('./launch_models.js');
const { AGENT_PRESETS } = await importOnce('./agent_presets.js');
const { _getPermissionFlagsForAgentMode, _normalizePermFlagsForAgent, _updatePermModeDescription } = await importOnce('./permission_flags.js');
const { _findPersona, _getSavedTeamTemplates, _upsertSavedTeamTemplates } = await importOnce('./persona_store.js');
const { getAgentConfig, renderAgentConfigForm, setAgentConfig } = await importOnce('./agent_config_form.js');
const { markFieldInvalid, _launchBlocked, _launchOn, _onServerSuffix, _refreshAfterLaunch, _resetDirInputs, _showLaunchStep, hideLaunchModal, showTeamLaunchFailures } = await importOnce('./launch_core.js');
const { showSection } = await importOnce('./section_dialog.js');
const { _syncFlagButtons } = await importOnce('./flag_shortcuts.js');
const { showSettingsModal } = await importOnce('./settings_dialog.js');
const { BUILTIN_TEAM_TEMPLATES, _renderTeamTemplateSelector } = await importOnce('./team_templates_ui.js');

function _updateTeamAgentSummary(row) {
    if (!row) return;
    const acfId = row.dataset.acfId;
    const config = acfId ? getAgentConfig(acfId) : {};
    const name = config.name || 'New Agent';
    const promptPreview = _truncatePrompt(config.prompt || '', 200);
    const nameEl = row.querySelector('.team-agent-role-name');
    const previewEl = row.querySelector('.team-agent-prompt-preview');
    if (nameEl) nameEl.textContent = name;
    if (previewEl) previewEl.textContent = promptPreview || 'No behavior prompt yet.';
}

function _defaultTeamAgentModalConfig() {
    return {
        name: '',
        prompt: '',
        agentType: document.getElementById('team-agent-type')?.value || '',
        model: '',
        capabilities: null,
    };
}

function showTeamAgentModal(row = null) {
    launch.teamAgentModalRow = row;
    const title = document.getElementById('team-agent-modal-title');
    const subtitle = document.getElementById('team-agent-modal-subtitle');
    const modal = document.getElementById('team-agent-modal');
    if (!title || !subtitle || !modal) return;

    title.textContent = row ? 'Edit Team Agent' : 'Add Team Agent';
    subtitle.textContent = row
        ? 'Update this team member and save the changes back into the draft.'
        : 'Configure a team member, then add it to the team draft.';

    const acfValue = row?.dataset.acfId ? getAgentConfig(row.dataset.acfId) : _defaultTeamAgentModalConfig();
    renderAgentConfigForm('team-agent-modal-acf', { showPreset: true, showName: true, showAutoPermissions: true, sections: true, value: acfValue });
    modal.style.display = 'flex';
}
window.showTeamAgentModal = showTeamAgentModal;

function hideTeamAgentModal() {
    const modal = document.getElementById('team-agent-modal');
    if (modal) modal.style.display = 'none';
    launch.teamAgentModalRow = null;
}
window.hideTeamAgentModal = hideTeamAgentModal;

function saveTeamAgentFromModal() {
    const config = getAgentConfig('team-agent-modal-acf');
    if (!config.name) {
        showToast('Agent name is required', true);
        markFieldInvalid(document.querySelector('#team-agent-modal-acf .acf-name'), 'Agent name is required');
        return;
    }

    if (launch.teamAgentModalRow?.dataset.acfId) {
        setAgentConfig(launch.teamAgentModalRow.dataset.acfId, config);
        _updateTeamAgentSummary(launch.teamAgentModalRow);
    } else {
        _addTeamAgent(config.name, config.prompt, config.capabilities, config.agentType, config.model, config.flags, { reveal: true });
    }

    hideTeamAgentModal();
}
window.saveTeamAgentFromModal = saveTeamAgentFromModal;

// ── Agent Team ────────────────────────────────────────────────────────────


/** Apply a preset to a specific AgentConfigForm by container ID */
function _applyACFPresetFor(containerId, name) {
    const persona = name ? _findPersona(name) : null;
    const current = getAgentConfig(containerId);
    if (persona) {
        setAgentConfig(containerId, {
            name: persona.name,
            prompt: persona.prompt,
            flags: persona.flags || current.flags,
            agentType: persona.agentType || persona.agent_type || current.agentType,
            model: persona.model || current.model,
            capabilities: persona.capabilities,
        });
    } else {
        setAgentConfig(containerId, {
            agentType: _settingsFor(_acfServer(document.getElementById(containerId))).default_agent_type || current.agentType,
        });
    }
}

// Default team: first 3 presets
const DEFAULT_TEAM_PRESETS = AGENT_PRESETS.slice(0, 4);

/** Worktree option is offered when the working directory is a git repo ON THE TARGET SERVER. */
async function _checkTeamGitDir() {
    const server = _effectiveServer(launch.server);
    const dirInput = document.getElementById("launch-team-dir");
    const dir = dirInput ? dirInput.value.trim() : '';
    const worktreeOpt = document.getElementById('team-worktree-option');
    if (!dir || !worktreeOpt) return;
    try {
        const resp = await serverFetch(server, `/api/filesystem/is-git?path=${encodeURIComponent(dir)}`);
        const data = await resp.json();
        if (server !== _effectiveServer(launch.server)) return;
        worktreeOpt.style.display = data.is_git ? '' : 'none';
    } catch { worktreeOpt.style.display = 'none'; }
}

/** The target server's board names and labels, to say before launching that one exists (#3536). */
async function _loadExistingTeamBoards() {
    const server = _effectiveServer(launch.server);
    launch.existingTeamBoardNames = new Set();
    try {
        const resp = await serverFetch(server, "/api/board/projects");
        const projects = await resp.json();
        if (server !== _effectiveServer(launch.server) || !Array.isArray(projects)) return;
        for (const p of projects) {
            if (p.project) launch.existingTeamBoardNames.add(String(p.project).trim().toLowerCase());
            if (p.board_label) launch.existingTeamBoardNames.add(String(p.board_label).trim().toLowerCase());
        }
    } catch (_) {}
    _validateTeamName();
}

async function _initTeamForm() {
    // Hide generated-team banner on normal opens
    const banner = document.getElementById('generated-team-banner');
    if (banner) banner.style.display = 'none';

    // Reset form
    document.getElementById("team-board-name").value = "";
    document.getElementById("team-board-server").value = "";
    launch.existingTeamBoardNames = new Set();
    _validateTeamName();
    const tapEl = document.getElementById("team-permission-mode");
    if (tapEl) {
        tapEl.value = _settingsFor(_effectiveServer(launch.server)).default_permission_mode || 'bypassPermissions';
        _updatePermModeDescription(tapEl);
    }
    launch.teamAgentCounter = 0;
    const list = document.getElementById("team-agents-list");
    list.innerHTML = "";

    // Pre-fill working dir from the target server's settings
    const s = _settingsFor(_effectiveServer(launch.server));
    const dirInput = document.getElementById("launch-team-dir");
    if (launch.server !== LOCAL_SERVER) _resetDirInputs(['launch-team-dir'], launch.server, s, { switching: true });
    else if (s.default_working_dir && dirInput) dirInput.value = s.default_working_dir;

    // Show worktree option when directory is a git repo (checked on the target server)
    if (dirInput && !dirInput.dataset.gitWired) {
        dirInput.dataset.gitWired = '1';
        let _gitCheckTimer;
        dirInput.addEventListener('input', () => { clearTimeout(_gitCheckTimer); _gitCheckTimer = setTimeout(_checkTeamGitDir, 500); });
        dirInput.addEventListener('change', _checkTeamGitDir);
    }
    _checkTeamGitDir(); // initial check
    const typeSelect = document.getElementById("team-agent-type");
    if (s.default_agent_type && typeSelect) typeSelect.value = s.default_agent_type;
    if (typeSelect) {
        typeSelect.onchange = () => {
            const tfEl = document.getElementById("team-flags");
            if (tfEl) {
                const mode = document.getElementById("team-permission-mode")?.value || 'default';
                tfEl.value = _normalizePermFlagsForAgent(tfEl.value, typeSelect.value, mode);
            }
        };
    }

    // Fetch existing boards for uniqueness validation
    await _loadExistingTeamBoards();

    // Render team template selector
    _renderTeamTemplateSelector();

    // Add three default agents (skip if wizard is populating)
    if (!launch.skipDefaultTeamAgents) {
        for (const preset of DEFAULT_TEAM_PRESETS) {
            _addTeamAgent(preset.name, preset.prompt, preset.capabilities, preset.agent_type, preset.model);
        }
    }
    launch.skipDefaultTeamAgents = false;

    // (no focus into the team name here: it sits in Board & workspace, which is not the section the dialog opens on, and focusing it would scroll that pane; section_dialog.js puts the keyboard in the list)
    _validateTeamName();
}

window._loadTeamTemplateFromSelect = function(name) {
    if (!name) return;
    _loadTeamTemplate(name);
    // Reset dropdown to placeholder after loading
    const select = document.getElementById("team-template-selector");
    if (select) select.value = "";
};

function _loadTeamTemplate(name) {
    const all = [...BUILTIN_TEAM_TEMPLATES, ..._getSavedTeamTemplates()];
    const tmpl = all.find(t => t.name === name);
    if (!tmpl) { console.warn('[coral] Template not found:', name); return; }

    // Clear current agents and load from template
    launch.teamAgentCounter = 0;
    document.getElementById("team-agents-list").innerHTML = "";

    for (const agent of tmpl.agents) {
        const agentName = agent.name || agent.role || '';
        const agentPrompt = agent.prompt || agent.description || '';
        _addTeamAgent(agentName, agentPrompt, agent.capabilities, agent.agent_type, agent.model, agent.flags);
    }
    if (tmpl.flags) {
        const tfEl = document.getElementById("team-flags");
        if (tfEl) { tfEl.value = tmpl.flags; _syncFlagButtons("team-flags"); }
    }
    showToast(`Loaded template "${name}"`);
}

function _truncatePrompt(text, maxLen) {
    if (!text || text.length <= maxLen) return text || "";
    return text.substring(0, maxLen) + "\u2026";
}

// reveal: scroll the new card into view. Only for an agent the person added; the default agents and a template's are added while the dialog opens or fills, and scrolling to the last of them
// left the Members section opened past its heading and the template picker (#4950).
function _addTeamAgent(defaultName, defaultPrompt, defaultCapabilities, defaultAgentType, defaultModel, defaultFlags, { reveal = false } = {}) {
    launch.teamAgentCounter++;
    const idx = launch.teamAgentCounter;
    const acfId = `team-agent-acf-${idx}`;
    const list = document.getElementById("team-agents-list");
    const row = document.createElement("div");
    row.className = "team-agent-row";
    row.draggable = true;
    row.dataset.idx = idx;
    row.dataset.acfId = acfId;

    row.innerHTML = `
        <div class="team-agent-card">
            <div class="team-agent-summary" data-on-click="team-agent-open">
                <div class="team-agent-summary-left">
                    <span class="team-agent-role-name">${escapeHtml(defaultName || 'New Agent')}</span>
                    <span class="team-agent-prompt-preview">${escapeHtml(_truncatePrompt(defaultPrompt, 200) || 'No behavior prompt yet.')}</span>
                </div>
                <div class="team-agent-summary-actions">
                    <button class="team-agent-reorder-btn" title="Move up" data-on-click="team-agent-move" data-dir="up">
                        <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M8 3v10M4 7l4-4 4 4"/></svg>
                    </button>
                    <button class="team-agent-reorder-btn" title="Move down" data-on-click="team-agent-move" data-dir="down">
                        <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M8 13V3M4 9l4 4 4-4"/></svg>
                    </button>
                    <button class="team-agent-edit-btn" title="Edit" data-on-click="team-agent-edit">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>
                    </button>
                    <button class="team-agent-remove" data-on-click="team-agent-remove" title="Remove">&times;</button>
                </div>
            </div>
            <div class="team-agent-form" hidden>
                <div id="${escapeAttr(acfId)}"></div>
            </div>
        </div>
    `;
    list.appendChild(row);
    // Cards come and go (add, remove, drag, move): keep the ends' buttons in step with the order.
    if (!list.dataset.moveSync) {
        list.dataset.moveSync = '1';
        new MutationObserver(() => _syncTeamMoveButtons(list)).observe(list, { childList: true });
    }
    _syncTeamMoveButtons(list);

    // Render ACF inside the card
    const caps = defaultCapabilities || _findPersona(defaultName)?.capabilities || null;
    const acfValue = {
        name: defaultName || '',
        prompt: defaultPrompt || '',
        agentType: defaultAgentType || '',
        model: defaultModel || '',
        capabilities: caps,
    };
    if (defaultFlags) acfValue.flags = defaultFlags;
    renderAgentConfigForm(acfId, {
        showPreset: false,
        showName: true,
        showAutoPermissions: true,
        value: acfValue,
    });

    _updateTeamAgentSummary(row);

    // Drag-and-drop reorder handlers
    row.addEventListener('dragstart', (e) => {
        e.dataTransfer.effectAllowed = 'move';
        e.dataTransfer.setData('text/plain', idx.toString());
        row.classList.add('dragging');
    });
    row.addEventListener('dragend', () => {
        row.classList.remove('dragging');
        list.querySelectorAll('.team-agent-row.drag-over').forEach(el => el.classList.remove('drag-over'));
    });
    row.addEventListener('dragover', (e) => {
        e.preventDefault();
        e.dataTransfer.dropEffect = 'move';
        row.classList.add('drag-over');
    });
    row.addEventListener('dragleave', () => {
        row.classList.remove('drag-over');
    });
    row.addEventListener('drop', (e) => {
        e.preventDefault();
        row.classList.remove('drag-over');
        const draggedIdx = e.dataTransfer.getData('text/plain');
        const draggedRow = list.querySelector(`.team-agent-row[data-idx="${draggedIdx}"]`);
        if (draggedRow && draggedRow !== row) {
            const rows = [...list.children];
            const fromPos = rows.indexOf(draggedRow);
            const toPos = rows.indexOf(row);
            if (fromPos < toPos) {
                list.insertBefore(draggedRow, row.nextSibling);
            } else {
                list.insertBefore(draggedRow, row);
            }
        }
    });

    if (reveal) row.scrollIntoView({ block: 'nearest', behavior: scrollBehavior() });
}

// The first card cannot move up and the last cannot move down: those buttons are disabled (#3770).
function _syncTeamMoveButtons(list) {
    const rows = [...list.querySelectorAll(':scope > .team-agent-row')];
    rows.forEach((row, i) => {
        const up = row.querySelector('.team-agent-reorder-btn[data-dir="up"]');
        const down = row.querySelector('.team-agent-reorder-btn[data-dir="down"]');
        if (up) up.disabled = i === 0;
        if (down) down.disabled = i === rows.length - 1;
    });
}

// Move a team agent card up or down
function _moveTeamAgent(btn, direction) {
    const row = btn.closest('.team-agent-row');
    if (!row) return;
    const list = row.parentElement;
    if (direction === -1 && row.previousElementSibling) {
        list.insertBefore(row, row.previousElementSibling);
    } else if (direction === 1 && row.nextElementSibling) {
        list.insertBefore(row.nextElementSibling, row);
    }
}

window._addTeamAgent = (name, prompt) => {
    _addTeamAgent(name || "", prompt || "");
};

function _validateTeamName() {
    const input = document.getElementById("team-board-name");
    const hint = document.getElementById("team-board-name-hint");
    if (!input || !hint) return true;
    const value = input.value.trim();
    input.removeAttribute("aria-invalid");
    if (!value) {
        hint.textContent = "This will also be the message board name.";
        hint.classList.remove("error");
        return false;
    }
    const problem = boardNameProblem(value); // a bad name is flagged (aria-invalid) below, which marks its section in the list while the person is elsewhere
    if (problem) {
        hint.textContent = problem;
        hint.classList.add("error");
        input.setAttribute("aria-invalid", "true");
        return false;
    }
    // A name in use is allowed (#3536): launching asks whether to join that board or start another.
    if (launch.existingTeamBoardNames.has(value.toLowerCase())) {
        hint.textContent = "A board with this name exists. Launching asks whether to join it or start another.";
        hint.classList.remove("error");
        return true;
    }
    hint.textContent = "Looks good. This will also be the message board name.";
    hint.classList.remove("error");
    return true;
}
window._validateTeamName = _validateTeamName;

async function launchTeam() {
    const boardName = document.getElementById("team-board-name").value.trim();
    if (!boardName) {
        showToast("Team name is required", true);
        markFieldInvalid(document.getElementById("team-board-name"), "Team name is required");
        return;
    }
    if (!_validateTeamName()) {
        const problem = boardNameProblem(boardName) || "Team name must be unique";
        showToast(problem, true);
        markFieldInvalid(document.getElementById("team-board-name"), problem);
        return;
    }

    const workingDir = document.getElementById("launch-team-dir").value.trim();
    if (!workingDir) {
        showToast("Working directory is required", true);
        markFieldInvalid(document.getElementById("launch-team-dir"), "Working directory is required");
        return;
    }

    const server = _effectiveServer(launch.server);
    if (_launchBlocked(server)) return;
    const agentType = document.getElementById("team-agent-type").value;
    const teamPermMode = document.getElementById("team-permission-mode")?.value || 'default';

    // Build agent-native flags from the permission mode dropdown.
    const flags = _getPermissionFlagsForAgentMode(agentType, teamPermMode).split(/\s+/).filter(Boolean);

    // Collect agent definitions
    const rows = document.querySelectorAll("#team-agents-list .team-agent-row");
    const members = () => { const shell = document.querySelector('#launch-step-team .settings-shell'); if (shell) showSection(shell, 'members'); }; // the problem is in Members: open it
    if (rows.length === 0) {
        showToast("Add at least one agent", true);
        members();
        return;
    }

    const agents = [];
    for (const row of rows) {
        const acfId = row.dataset.acfId;
        const config = acfId ? getAgentConfig(acfId) : {};
        if (!config.name) {
            showToast("Each agent needs a name", true);
            members();
            return;
        }
        const agent = { name: config.name, prompt: config.prompt };
        const icon = row.querySelector(".team-agent-icon")?.value.trim() || "";
        if (icon) agent.icon = icon;
        if (config.capabilities) agent.capabilities = config.capabilities;
        if (config.agentType) agent.agent_type = config.agentType;
        if (config.model) agent.model = config.model;
        if (config.thinking) agent.thinking = config.thinking; // #4969
        if (config.flags) agent.flags = config.flags.split(/\s+/).filter(Boolean);
        agents.push(agent);
    }

    const boardServer = document.getElementById("team-board-server").value.trim();
    const useWorktree = document.getElementById("launch-team-worktree")?.checked;
    const baseBranch = document.getElementById("launch-team-base-branch")?.value.trim();

    const payload = {
        board_name: boardName,
        working_dir: workingDir,
        agent_type: agentType,
        flags,
        agents,
    };
    if (boardServer) payload.board_server = boardServer;
    if (useWorktree) {
        payload.worktree = true;
        if (baseBranch) payload.base_branch = baseBranch;
    }

    // Disable launch button to prevent double-clicks
    const launchBtn = document.querySelector('#launch-step-team .btn-primary');
    if (launchBtn) { launchBtn.disabled = true; launchBtn.textContent = 'Launching...'; }

    try {
        const result = await _launchOn(server, "/api/sessions/launch-team", payload, undefined, { teamFailures: true });
        if (result) {
            if (result.ok !== false) recordRecentDir(workingDir, server); // not for a team in which nobody started (#122)
            // Check for per-agent errors in the launched array
            const launched = result.agents || result.launched || [];
            const failed = launched.filter(a => a.error);
            if (failed.length > 0) showTeamLaunchFailures(failed, launched.length - failed.length, showSettingsModal);
            if (launched.length - failed.length > 0) {
                const wtNote = result.worktree_path ? ` in worktree ${result.worktree_path}` : '';
                showToast(`Launched team: ${launched.length - failed.length} agents on "${boardName}"${_onServerSuffix(server)}${wtNote}`);
            }
            // A team in which nobody started (the server answered an error, #122): the dialog stays open, to fix the setup and launch again.
            if (result.ok !== false) {
                hideLaunchModal();
                setTimeout(loadLiveSessions, 2000);
                setTimeout(loadBoardProjects, 4000);
                _refreshAfterLaunch(server, { boardName });
            }
        }
    } catch (e) {
        showToast("Failed to launch team", true);
    } finally {
        if (launchBtn) { launchBtn.disabled = false; launchBtn.textContent = 'Launch Team'; }
    }
}
window.launchTeam = launchTeam;

export { _addTeamAgent, _applyACFPresetFor, _checkTeamGitDir, _initTeamForm, _loadExistingTeamBoards, _moveTeamAgent, _renderTeamTemplateSelector };
