/* Moved out of modals.js as it was (#4684): The AI team wizard, the team-agent dialog, and the Launch dialog's team form: its agents, templates and Launch Team. No behaviour change. */
import { importOnce } from './import_once.js';
import { scrollBehavior } from './motion.js';
import { showToast, escapeHtml, escapeAttr, boardNameProblem, OPERATOR_ID } from './utils.js';
import { loadLiveSessions } from './api.js';
import { loadBoardProjects } from './message_board.js';
import { recordRecentDir } from './browser.js';
import { serverFetch, boardFetch, identityKey, LOCAL_SERVER } from './server_base.js';
const { launch } = await importOnce('./launch_state.js');
const { _acfServer, _effectiveServer, _settingsFor } = await importOnce('./launch_models.js');
const { trackPermissionPick, _getPermissionFlagsForAgentMode, _normalizePermFlagsForAgent, _updatePermModeDescription } = await importOnce('./permission_flags.js');
const { _findPersona, _getSavedTeamTemplates, _upsertSavedTeamTemplates } = await importOnce('./persona_store.js');
const { getAgentConfig, renderAgentConfigForm, setAgentConfig } = await importOnce('./agent_config_form.js');
const { markFieldInvalid, _launchBlocked, _launchOn, _onServerSuffix, _refreshAfterLaunch, _resetDirInputs, _showLaunchStep, hideLaunchModal, showTeamLaunchFailures } = await importOnce('./launch_core.js');
const { _syncFlagButtons } = await importOnce('./flag_shortcuts.js');
const { showSettingsModal } = await importOnce('./settings_dialog.js');
const { modelName, levelName } = await importOnce('./model_labels.js');
const { launchedMode } = await importOnce('./preset_enforcement.js');
const { BUILTIN_TEAM_TEMPLATES, _renderTeamTemplateSelector, markTeamTemplate, resetTeamTemplatePicker } = await importOnce('./team_templates_ui.js');
const { showTeamStep, currentTeamStep, TEAM_STEPS } = await importOnce('./team_steps.js');


const _modeLabel = v => [...(document.getElementById('team-permission-mode')?.options || [])].find(o => o.value === v)?.textContent || v;

// A member follows the team's provider unless it was given one (a template's own, or one picked in the dialog): the form's select is set from the team's, so the launch sends it.
function _followTeamType() {
    const type = document.getElementById('team-agent-type')?.value;
    if (!type) return;
    for (const row of document.querySelectorAll('#team-agents-list .team-agent-row[data-follows-team-type="1"]')) {
        const sel = document.getElementById(row.dataset.acfId)?.querySelector('.acf-agent-type');
        if (sel && sel.value !== type) { sel.value = type; sel.dispatchEvent(new Event('change', { bubbles: true })); }
    }
}

function _updateTeamAgentSummary(row) {
    if (!row) return;
    const acfId = row.dataset.acfId;
    const config = acfId ? getAgentConfig(acfId) : {};
    const name = config.name || 'New Agent';
    const typeSelect = document.getElementById('team-agent-type');
    const typeLabel = v => [...(typeSelect?.options || [])].find(o => o.value === v)?.textContent.replace(/ \(.*\)$/, '') || v;
    // A cell shows the value that will be used; a small mark says where it comes from when the member did not choose it.
    const set = (sel, text, mark = '') => {
        const el = row.querySelector(sel); if (!el) return;
        el.textContent = text;
        if (mark) { const m = document.createElement('span'); m.className = 'team-default-mark'; m.textContent = mark; el.append(' ', m); }
    };
    const type = config.agentType || typeSelect?.value || 'claude';
    set('.team-agent-role-name', name);
    set('.team-agent-provider', typeLabel(type));
    const modelId = config.model || '';
    set('.team-agent-model', modelId ? modelName(type, modelId, _effectiveServer(launch.server)) : 'CLI default', modelId && modelId === _settingsFor(_effectiveServer(launch.server))['default_model_' + type] ? 'default' : '');
    set('.team-agent-thinking', config.thinking ? levelName(config.thinking) : 'CLI default');
    // What launches (#6425): the member's own Mode, else the team's, is applied; for Codex and Antigravity the capabilities may only narrow it, and with no Mode they decide.
    const acf = acfId ? document.getElementById(acfId) : null;
    const ownMode = acf?.querySelector('.acf-permission-mode')?.value || 'default';
    const teamMode = document.getElementById('team-permission-mode')?.value || 'default';
    const mode = ownMode !== 'default' ? ownMode : teamMode;
    const lm = launchedMode(type, mode, config.capabilities);
    if (lm.narrowed) set('.team-agent-mode', lm.text);
    else set('.team-agent-mode', _modeLabel(mode), ownMode === 'default' ? 'team' : '');
    row.querySelector('.team-agent-mode')?.classList.toggle('team-mode-warn', !!lm.warn);
    const prompt = row.querySelector('.team-agent-prompt');
    if (prompt) prompt.textContent = config.prompt || 'No behavior prompt yet.';
    const nameBtn = row.querySelector('.team-agent-role-name');
    nameBtn?.setAttribute('aria-label', `${name}: show or hide its prompt`);
    row.querySelector('.team-agent-edit-btn')?.setAttribute('aria-label', `Edit ${name}`);
    row.querySelector('.team-agent-remove')?.setAttribute('aria-label', `Remove ${name}`);
    for (const dir of ['up', 'down']) {
        const b = row.querySelector(`[data-dir="${dir}"]`);
        b?.setAttribute('aria-label', `Move ${name} ${dir}`);
        b?.setAttribute('title', `Move ${dir}`);
    }
}

function _refreshTeamSummaries() {
    document.querySelectorAll('#team-agents-list .team-agent-row').forEach(_updateTeamAgentSummary);
}

// Remove keeps the row aside for Undo (one removal back); a new removal, a template or a closed dialog makes the last one final.
let _removedTeamAgent = null;
function _clearTeamUndo() {
    _removedTeamAgent = null;
    const line = document.getElementById('team-undo');
    if (line) line.textContent = ''; // always in the page, only its text changes
}
function _removeTeamAgent(row) {
    if (!row) return;
    const name = row.querySelector('.team-agent-role-name')?.textContent || 'Member';
    _removedTeamAgent = { row, next: row.nextElementSibling, list: row.parentElement };
    row.remove();
    const line = document.getElementById('team-undo');
    if (!line) return;
    line.textContent = '';
    line.append(`Removed ${name}. `);
    const undo = document.createElement('button');
    undo.type = 'button'; undo.className = 'btn-link'; undo.setAttribute('data-on-click', 'team-agent-undo'); undo.textContent = 'Undo';
    line.append(undo);
}
function _undoTeamAgentRemoval() {
    const r = _removedTeamAgent;
    if (!r) return;
    r.list.insertBefore(r.row, r.next && r.next.parentElement === r.list ? r.next : null);
    _clearTeamUndo();
    r.row.querySelector('.team-agent-edit-btn')?.focus();
}
function _toggleTeamAgentPrompt(row) {
    const btn = row?.querySelector('.team-agent-role-name');
    const box = row?.querySelector('.team-agent-prompt');
    if (!btn || !box) return;
    const open = box.hidden;
    box.hidden = !open;
    btn.setAttribute('aria-expanded', open ? 'true' : 'false');
}

// Next on the Team step needs what every launch needs: a name that is fine, and a folder.
function teamStepMove(dir) {
    const step = currentTeamStep();
    const i = TEAM_STEPS.indexOf(step);
    if (dir > 0 && step === 'team') {
        const name = document.getElementById('team-board-name');
        const dirInput = document.getElementById('launch-team-dir');
        if (!name.value.trim()) { markFieldInvalid(name, 'Team name is required'); return; }
        if (!_validateTeamName()) { markFieldInvalid(name, boardNameProblem(name.value.trim()) || 'Team name must be unique'); return; }
        if (!dirInput.value.trim()) { markFieldInvalid(dirInput, 'Choose a folder for the team'); return; }
    }
    const to = TEAM_STEPS[Math.min(TEAM_STEPS.length - 1, Math.max(0, i + dir))];
    if (to === 'members') _refreshTeamSummaries();
    showTeamStep(to, { focus: true });
}

const _teamModeFlags = agentType => _getPermissionFlagsForAgentMode(agentType || document.getElementById('team-agent-type')?.value || 'claude', document.getElementById('team-permission-mode')?.value || 'default');

function _defaultTeamAgentModalConfig() {
    const agentType = document.getElementById('team-agent-type')?.value || '';
    return {
        name: '',
        prompt: '',
        agentType,
        model: '',
        capabilities: null,
        flags: _teamModeFlags(agentType), // a new member starts at the team's Mode, not Auto (#6412)
    };
}

function showTeamAgentModal(row = null) {
    launch.teamAgentModalRow = row;
    const title = document.getElementById('team-agent-modal-title');
    const subtitle = document.getElementById('team-agent-modal-subtitle');
    const modal = document.getElementById('team-agent-modal');
    if (!title || !subtitle || !modal) return;

    title.textContent = row ? 'Edit Team Agent' : 'Add Team Agent';
    subtitle.textContent = ''; // no sub-title (#6012): the title and the form say it

    const acfValue = row?.dataset.acfId ? getAgentConfig(row.dataset.acfId) : _defaultTeamAgentModalConfig();
    // A member with no Mode of its own launches with the team's: the dialog shows that Mode (saving it unchanged keeps the member following the team, see saveTeamAgentFromModal).
    if (row && !/--permission-mode|--mode|--sandbox|--full-auto|--dangerously/.test(acfValue.flags || '')) acfValue.flags = [acfValue.flags, _teamModeFlags(acfValue.agentType)].filter(Boolean).join(' ');
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

    // The Mode left at the team's is not the member's own: it keeps following the team, which the launch needs (an own Mode beats the team's, #6440).
    const modalMode = document.querySelector('#team-agent-modal-acf .acf-permission-mode')?.value;
    if (modalMode && modalMode === (document.getElementById('team-permission-mode')?.value || 'default')) config.flags = _normalizePermFlagsForAgent(config.flags, config.agentType, 'default');
    const followsType = config.agentType === document.getElementById('team-agent-type')?.value; // a provider left at the team's keeps following it
    if (launch.teamAgentModalRow?.dataset.acfId) {
        launch.teamAgentModalRow.dataset.followsTeamType = followsType ? '1' : '';
        setAgentConfig(launch.teamAgentModalRow.dataset.acfId, config);
        _updateTeamAgentSummary(launch.teamAgentModalRow);
    } else {
        _addTeamAgent(config.name, config.prompt, config.capabilities, followsType ? '' : config.agentType, config.model, config.flags, { reveal: true });
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
        // Base Branch starts as the repo's own current branch (the answer's `branch`), not a guess ("main" in a repo that has master, #6707), until the person types one.
        const base = document.getElementById('launch-team-base-branch');
        if (base && data.is_git && typeof data.branch === 'string' && data.branch && base.dataset.edited !== '1') base.value = data.branch;
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

// The team form starts at the CLI's own confirm mode (Default), not Auto: the person's saved default wins, and a mode they pick stays (#6397).
function _applyTeamDefaultMode() {
    const el = document.getElementById('team-permission-mode');
    if (!el) return;
    trackPermissionPick(el);
    if (!el.dataset.pickedByPerson) el.value = _settingsFor(_effectiveServer(launch.server)).default_permission_mode || 'default';
    _updatePermModeDescription(el);
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
        delete tapEl.dataset.pickedByPerson; // a new dialog: the default again, for the agent type the team starts with (#5836)
        const teamSettings = _settingsFor(_effectiveServer(launch.server));
        _applyTeamDefaultMode();
    }
    launch.teamAgentCounter = 0;
    const list = document.getElementById("team-agents-list");
    list.innerHTML = "";
    _clearTeamUndo();
    showTeamStep('team');
    _applyTeamDefaultMode();

    // Pre-fill working dir from the target server's settings
    const s = _settingsFor(_effectiveServer(launch.server));
    const dirInput = document.getElementById("launch-team-dir");
    // No folder is chosen for the person (#5989): the Settings default if they set one, else empty and required, never the home folder.
    if (dirInput) dirInput.value = '';
    const goalEl = document.getElementById("team-goal");
    if (goalEl) goalEl.value = "";
    if (launch.server !== LOCAL_SERVER) _resetDirInputs(['launch-team-dir'], launch.server, s, { switching: true });
    else if (s.default_working_dir && dirInput) dirInput.value = s.default_working_dir;

    // Show worktree option when directory is a git repo (checked on the target server)
    if (dirInput && !dirInput.dataset.gitWired) {
        dirInput.dataset.gitWired = '1';
        let _gitCheckTimer;
        dirInput.addEventListener('input', () => { clearTimeout(_gitCheckTimer); _gitCheckTimer = setTimeout(_checkTeamGitDir, 500); });
        dirInput.addEventListener('change', _checkTeamGitDir);
    }
    document.getElementById('launch-team-base-branch')?.addEventListener('input', e => { e.target.dataset.edited = '1'; }); // a branch the person typed is kept when the folder changes
    _checkTeamGitDir(); // initial check
    const typeSelect = document.getElementById("team-agent-type");
    if (s.default_agent_type && typeSelect) typeSelect.value = s.default_agent_type;
    if (typeSelect) {
        typeSelect.onchange = () => {
            _followTeamType(); // the members with no provider of their own take the team's (#6440)
            _applyTeamDefaultMode(); // still the default: it follows the saved setting (#5836)
            _refreshTeamSummaries();
            const tfEl = document.getElementById("team-flags");
            if (tfEl) {
                const modeEl = document.getElementById("team-permission-mode");
                const mode = modeEl?.value || 'default';
                tfEl.value = _normalizePermFlagsForAgent(tfEl.value, typeSelect.value, mode);
            }
        };
    }

    // Fetch existing boards for uniqueness validation
    await _loadExistingTeamBoards();

    // The template picker starts with nothing chosen
    resetTeamTemplatePicker();

    // The dialog opens on the template choice with no members (#5989): four agents nobody picked were launched by a first-time Launch Team.
    launch.skipDefaultTeamAgents = false;

    // (no focus into the team name here: it sits in Board & workspace, which is not the section the dialog opens on, and focusing it would scroll that pane; section_dialog.js puts the keyboard in the list)
    _validateTeamName();
}

window._loadTeamTemplateFromSelect = function(name) {
    if (!name) return;
    _loadTeamTemplate(name);
};

function _loadTeamTemplate(name) {
    const all = [...BUILTIN_TEAM_TEMPLATES, ..._getSavedTeamTemplates()];
    const tmpl = all.find(t => t.name === name);
    if (!tmpl) { console.warn('[coral] Template not found:', name); return; }

    // Clear current agents and load from template
    launch.teamAgentCounter = 0;
    document.getElementById("team-agents-list").innerHTML = "";
    _clearTeamUndo();

    for (const agent of tmpl.agents) {
        const agentName = agent.name || agent.role || '';
        const agentPrompt = agent.prompt || agent.description || '';
        _addTeamAgent(agentName, agentPrompt, agent.capabilities, agent.agent_type, agent.model, agent.flags);
    }
    if (tmpl.flags) {
        const tfEl = document.getElementById("team-flags");
        if (tfEl) { tfEl.value = tmpl.flags; _syncFlagButtons("team-flags"); }
    }
    markTeamTemplate(name);
}

// reveal: scroll the new card into view. Only for an agent the person added; the default agents and a template's are added while the dialog opens or fills, and scrolling to the last of them
// left the Members section opened past its heading and the template picker (#4950).
function _addTeamAgent(defaultName, defaultPrompt, defaultCapabilities, defaultAgentType, defaultModel, defaultFlags, { reveal = false } = {}) {
    launch.teamAgentCounter++;
    const idx = launch.teamAgentCounter;
    const acfId = `team-agent-acf-${idx}`;
    const list = document.getElementById("team-agents-list");
    const row = document.createElement("tr");
    row.className = "team-agent-row";
    row.draggable = true;
    row.dataset.idx = idx;
    row.dataset.acfId = acfId;
    if (!defaultAgentType) row.dataset.followsTeamType = '1';

    row.innerHTML = `
        <td class="team-agent-role" data-label="Role">
            <button type="button" class="team-agent-role-name" aria-expanded="false" data-on-click="team-agent-prompt">${escapeHtml(defaultName || 'New Agent')}</button>
            <div class="team-agent-prompt" hidden></div>
            <div class="team-agent-form" hidden><div id="${escapeAttr(acfId)}"></div></div>
        </td>
        <td class="team-agent-provider" data-label="Provider"></td>
        <td class="team-agent-model" data-label="Model"></td>
        <td class="team-agent-thinking" data-label="Thinking"></td>
        <td class="team-agent-mode" data-label="Mode"></td>
        <td class="team-agent-actions-cell"><div class="team-agent-summary-actions">
            <button type="button" class="team-agent-reorder-btn" data-on-click="team-agent-move" data-dir="up">
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M8 3v10M4 7l4-4 4 4"/></svg>
            </button>
            <button type="button" class="team-agent-reorder-btn" data-on-click="team-agent-move" data-dir="down">
                <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M8 13V3M4 9l4 4 4-4"/></svg>
            </button>
            <button type="button" class="btn btn-small btn-subtle team-agent-edit-btn" data-on-click="team-agent-edit">Edit</button>
            <button type="button" class="btn btn-small btn-subtle team-agent-remove" data-on-click="team-agent-remove">Remove</button>
        </div></td>
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
        agentType: defaultAgentType || document.getElementById('team-agent-type')?.value || '',
        model: defaultModel || '',
        capabilities: caps,
    };
    acfValue.flags = defaultFlags || ''; // a member with no mode of its own starts at Default and follows the team's Mode on the Start step (#6397)
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
        hint.textContent = "";
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
    hint.textContent = "";
    hint.classList.remove("error");
    return true;
}
window._validateTeamName = _validateTeamName;

async function launchTeam() {
    const boardName = document.getElementById("team-board-name").value.trim();
    if (!boardName) {
        showToast("Team name is required", true);
        showTeamStep('team'); markFieldInvalid(document.getElementById("team-board-name"), "Team name is required");
        return;
    }
    if (!_validateTeamName()) {
        const problem = boardNameProblem(boardName) || "Team name must be unique";
        showToast(problem, true);
        showTeamStep('team');
        // Said once, under the field (#297): the hint goes back to its plain text while the error line shows the problem.
        const hint = document.getElementById("team-board-name-hint");
        if (hint) { hint.textContent = "This will also be the message board name."; hint.classList.remove("error"); }
        markFieldInvalid(document.getElementById("team-board-name"), problem);
        return;
    }

    const workingDir = document.getElementById("launch-team-dir").value.trim();
    if (!workingDir) {
        showTeamStep('team'); markFieldInvalid(document.getElementById("launch-team-dir"), "Choose a folder for the team");
        return;
    }

    if (document.querySelector('#launch-step-team [data-blocks-launch]')) return; // tmux is missing: the line above Launch Team says so (#6046)
    const server = _effectiveServer(launch.server);
    if (_launchBlocked(server)) return;
    const agentType = document.getElementById("team-agent-type").value;
    const teamPermMode = document.getElementById("team-permission-mode")?.value || 'default';

    // Build agent-native flags from the permission mode dropdown.
    const flags = _getPermissionFlagsForAgentMode(agentType, teamPermMode).split(/\s+/).filter(Boolean);

    // Collect agent definitions
    const rows = document.querySelectorAll("#team-agents-list .team-agent-row");
    const members = () => showTeamStep('members'); // the problem is in Members: open it
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

    const goal = document.getElementById("team-goal")?.value.trim() || "";
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
        const result = await _launchOn(server, "/api/sessions/launch-team", payload, undefined, { teamFailures: true, fields: { base_branch: document.getElementById('launch-team-base-branch') } });
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
                const boardId = result.board_id || boardName; // the id the server made (a repeated name gets its own, "Create another"), not the label that was typed
                const seen = _refreshAfterLaunch(server, { boardName: boardId });
                // The team opens the way a single agent does (#5989): its board with the Team view beside it, and the goal, if one was typed, as the Operator's first message to the lead. On a remote this waits for the hub to see the team.
                if (launched.length - failed.length > 0) _openLaunchedTeam(server, boardId, goal, agents, seen);
            }
        }
    } catch (e) {
        showToast("Failed to launch team", true);
    } finally {
        if (launchBtn) { launchBtn.disabled = false; launchBtn.textContent = 'Launch Team'; }
    }
}
/** Open the launched team, and post the goal to its lead: the agent named Orchestrator, else the first member. The goal that could not be sent is left in the board's message box with a toast, to send again without launching the team twice. */
async function _openLaunchedTeam(server, boardName, goal, agents, seen) {
    const key = identityKey(server, boardName);
    const lead = (agents.find(a => a.name === 'Orchestrator') || agents[0]).name;
    const content = goal ? `@${lead} ${goal}` : '';
    let sent = !goal;
    try {
        await seen; // on a remote, until the hub lists the team (or the wait runs out: the post and the open are still tried)
        if (goal) {
            const r = await boardFetch(key, '/messages', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ subscriber_id: OPERATOR_ID, as: 'Operator', content }),
            });
            sent = r.ok;
        }
    } catch { sent = false; }
    try {
        await loadLiveSessions();
        const { enterTeamContext } = await importOnce('./team_context.js');
        await enterTeamContext(key);
    } catch { /* the toast and the row are still there */ }
    const input = document.getElementById('board-panel-input');
    if (!sent) {
        if (input && !input.value) input.value = content;
        showToast('The team started, but the first task was not sent. It is in the message box: press Send to try again.', true);
    }
    if (!goal || !sent) input?.focus();
}
window.launchTeam = launchTeam;

export { _openLaunchedTeam, _addTeamAgent, _applyACFPresetFor, _checkTeamGitDir, _initTeamForm, _loadExistingTeamBoards, _moveTeamAgent, _renderTeamTemplateSelector, _removeTeamAgent, _undoTeamAgentRemoval, _toggleTeamAgentPrompt, _refreshTeamSummaries, teamStepMove };
