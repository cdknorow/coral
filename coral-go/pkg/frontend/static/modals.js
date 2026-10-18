import { importOnce } from './import_once.js';
import { closeOnBackdropAndEscape, releaseEscape } from './dialog_close.js';
/* Modal management: launch and info dialogs */

import { state } from './state.js';
import { showToast, escapeHtml, escapeAttr, boardNameProblem } from './utils.js';
import { blockedReason } from './hub_access.js';
import { registerActions, tokenOf, textOf } from './actions.js';
import { loadLiveSessions } from './api.js';
import { loadBoardProjects } from './message_board.js';
import { recordRecentDir, setDirServerResolver } from './browser.js';
import { serverFetch, splitKey, identityKey, isHub, normServer, LOCAL_SERVER, keyLabel, noteBoardLabels } from './server_base.js';
import { chooseBoard, retryPayload, isBoardNameExists } from './board_choice.js';
import {
    pickerVisible, pickerOptions, serverLabel, launchBlockReason, coerceSelection,
    bareBoardName, postLaunch, waitForLaunched,
} from './launch_server.js';
import { pollEvery } from './overload.js';
import { ensureBuilt } from './lazy_dom.js';

// The Launch, Info and Settings dialogs are built from their templates when this module loads, which is their first use
// (#4500): everything below finds them in the page. Settings' load-time writers (settings_core.js and others) find nothing
// until then, and showSettingsModal fills every field as it opens.
ensureBuilt('launch-modal');
ensureBuilt('info-modal');
ensureBuilt('settings-modal');
const { launch } = await importOnce('./launch_state.js');
const { _acfServer, _effectiveServer, _fillModelDatalist, _getAgentModels, _getDefaultModels, _invalidateDefaultModels, _loadServerSettings, _serverSettings, _settingsFor } = await importOnce('./launch_models.js');
const { AGENT_PRESETS, ALL_CAPABILITIES } = await importOnce('./agent_presets.js');
const { PERMISSION_MODE_DESCRIPTIONS, _getPermissionFlagsForAgentMode, _hasPermFlag, _normalizePermFlagsForAgent, _permissionModeFromFlags, _stripPermFlags, _syncACFAutoPermissions, _updatePermModeDescription } = await importOnce('./permission_flags.js');
const { CLI_INSTALL_INSTRUCTIONS, _checkAgentCLI } = await importOnce('./cli_check.js');
const { _classifyLoadedACFModel } = await importOnce('./acf_models.js');
const { _findPersona, _getSavedPersonas, _getSavedTeamTemplates, _renderPresetButtons, _upsertSavedPersonas, _upsertSavedTeamTemplates } = await importOnce('./persona_store.js');
const { _syncACFModelField, _togglePermChip, _togglePermissions, getAgentConfig, renderAgentConfigForm, setAgentConfig } = await importOnce('./agent_config_form.js');
const settingsDialog = await importOnce('./settings_dialog.js');
const infoDialog = await importOnce('./info_dialog.js');
const flagShortcuts = await importOnce('./flag_shortcuts.js');
const cliDialogs = await importOnce('./cli_dialogs.js');
const launchCore = await importOnce('./launch_core.js');
const teamDialogs = await importOnce('./team_dialogs.js');
const { _applyACFPresetFor, _checkTeamGitDir, _initTeamForm, _loadExistingTeamBoards, _moveTeamAgent, _renderTeamTemplateSelector, _reviewGeneratedTeam } = teamDialogs;
const { _launchBlocked, _launchOn, _onServerSuffix, _refreshAfterLaunch, _resetDirInputs, _showLaunchStep, _syncMobileLaunchSections, _withServerSettings, hideLaunchModal } = launchCore;
export { _launchOn, hideLaunchModal };
const { _checkPermissionFlag, _showCLINotFoundModal } = cliDialogs;
const { _syncFlagButtons } = flagShortcuts;
export const { toggleFlag } = flagShortcuts;
export const { showInfoModal, hideInfoModal, copyInfoCommand } = infoDialog;
const { showSettingsModal } = settingsDialog;
export const { hideSettingsModal, applySettings } = settingsDialog;
export { showSettingsModal };
// Kept on modals.js's surface (app.js and others import them from here).
export { loadSettings, applyTheme } from './settings_core.js';


function _selectLaunchType(type) {
    if (type === "back") {
        launch.mode = null;
        _showLaunchStep("chooser");
        return;
    }
    launch.mode = type;
    _showLaunchStep(type);
    if (type === "agent") {
        _loadAgentBoardProjects();
        _initAgentPresets();
        // Render agent config form
        renderAgentConfigForm('launch-agent-acf', {
            showPreset: false,
            showName: false,
            value: { agentType: _settingsFor(_effectiveServer(launch.server)).default_agent_type || 'claude' },
        });
    } else if (type === "team") {
        _initTeamForm();
    } else {
        document.getElementById("launch-terminal-name").focus();
    }
}
window._selectLaunchType = _selectLaunchType;

/**
 * Open the launch modal. `server` (hub mode) pre-selects a launch target; with
 * `lock` the dropdown is fixed to it (launches started from inside a remote
 * context). Default is the hub itself with a free choice.
 */
export function showLaunchModal(server, lock = false) {
    launch.mode = null;
    _serverSettings.clear();
    launch.serverLocked = !!lock && isHub();
    launch.server = _effectiveServer(coerceSelection(server, isHub(), state.servers));
    if (launch.serverLocked && normServer(server) !== LOCAL_SERVER) launch.server = normServer(server);
    _renderServerPicker('launch');
    document.getElementById("launch-modal").style.display = "flex";
    _showLaunchStep("chooser");

    // Reset agent form
    const agentNameInput = document.getElementById("launch-agent-name-input");
    if (agentNameInput) agentNameInput.value = "";
    document.getElementById("launch-board-name").value = "";
    document.getElementById("launch-board-server").value = "";
    const agentBoardSelect = document.getElementById("launch-board-select");
    if (agentBoardSelect) agentBoardSelect.value = "";
    document.getElementById("launch-new-board-row").style.display = "none";
    document.getElementById("launch-board-server-row").style.display = "none";

    // Reset terminal form
    document.getElementById("launch-terminal-name").value = "";

    // Reset team form
    document.getElementById("team-agents-list").innerHTML = "";
    document.getElementById("team-board-name").value = "";
    const teamFlagsEl = document.getElementById("team-flags");
    if (teamFlagsEl) teamFlagsEl.value = "";
    const teamPermModeEl = document.getElementById("team-permission-mode");
    if (teamPermModeEl) {
        teamPermModeEl.value = _settingsFor(_effectiveServer(launch.server)).default_permission_mode || 'bypassPermissions';
        _updatePermModeDescription(teamPermModeEl);
    }

    // Pre-fill from the target server's settings (the hub's own when local)
    _resetLaunchDirs(launch.server);
    const s = _settingsFor(launch.server);
    const typeSelect = document.getElementById("launch-type");
    if (s.default_agent_type && typeSelect) {
        typeSelect.value = s.default_agent_type;
    }

    _syncMobileLaunchSections();
    _syncLaunchAccess();
    if (launch.server !== LOCAL_SERVER) _onLaunchServerChange(launch.server, { force: true });
}

// ── Server picker (hub mode) ─────────────────────────────────────────────

const _PICKERS = {
    launch: { row: 'launch-server-row', select: 'launch-server' },
    quick: { row: 'quick-launch-server-row', select: 'quick-launch-server' },
};

/** Fill and show/hide a Server dropdown. Hidden unless hub mode has a usable remote. */
function _renderServerPicker(which) {
    const ids = _PICKERS[which];
    const row = document.getElementById(ids.row);
    const sel = document.getElementById(ids.select);
    if (!row || !sel) return;
    const current = which === 'launch' ? launch.server : launch.quickServer;
    const locked = which === 'launch' && launch.serverLocked;
    const show = isHub() && (locked ? current !== LOCAL_SERVER : pickerVisible(true, state.servers));
    if (!show) { row.style.display = 'none'; return; }
    let opts = pickerOptions(state.servers);
    if (!opts.some(o => o.id === current)) opts = [...opts, { id: current, label: current, disabled: false }];
    sel.innerHTML = opts.map(o =>
        `<option value="${escapeAttr(o.id)}"${o.disabled ? ' disabled' : ''}>${escapeHtml(o.label)}</option>`).join('');
    sel.value = current;
    sel.disabled = locked;
    row.style.display = 'flex';
}

const _LAUNCH_DIR_IDS = ['launch-dir', 'launch-terminal-dir', 'launch-team-dir'];
const _LAUNCH_BROWSER_IDS = ['dir-browser', 'dir-browser-terminal', 'dir-browser-team'];


function _resetLaunchDirs(server, opts) {
    // The team dir is also filled by _initTeamForm; only touch it on a switch
    // or for a remote so local behaviour with hub off is unchanged.
    const ids = (opts && opts.switching) || server !== LOCAL_SERVER ? _LAUNCH_DIR_IDS : _LAUNCH_DIR_IDS.slice(0, 2);
    _resetDirInputs(ids, server, _settingsFor(server), opts);
    for (const id of _LAUNCH_BROWSER_IDS) {
        const b = document.getElementById(id);
        if (b) b.style.display = 'none';
    }
}

/** Reload everything that depends on the launch server after the dropdown changes. */
async function _onLaunchServerChange(value, { force = false } = {}) {
    const next = _effectiveServer(coerceSelection(value, isHub(), state.servers));
    if (next === launch.server && !force) return;
    const prev = launch.server;
    launch.server = next;
    _syncLaunchAccess();
    const note = document.getElementById('launch-server-note');
    if (note) note.textContent = next === LOCAL_SERVER ? '' : 'Loading…';
    // Nothing from the previous server may survive: directory, board server, board choice.
    _resetLaunchDirs(next, { switching: next !== prev || force });
    for (const id of ['launch-board-server', 'team-board-server']) {
        const el = document.getElementById(id);
        if (el) el.value = '';
    }
    let loadError = '';
    if (next !== LOCAL_SERVER) {
        try { await _loadServerSettings(next); } catch (e) { loadError = e.message || 'unreachable'; }
        if (launch.server !== next) return; // changed again while loading
    }
    if (note) note.textContent = loadError ? `Could not read ${serverLabel(next, state.servers)}: ${loadError}` : '';
    // Re-apply defaults from the (now loaded) settings, then the dependent option sources.
    _resetLaunchDirs(next, { switching: true });
    const s = _settingsFor(next);
    const typeSelect = document.getElementById('launch-type');
    if (s.default_agent_type && typeSelect) typeSelect.value = s.default_agent_type;
    if (launch.mode === 'agent') _loadAgentBoardProjects();
    if (launch.mode === 'team') { _loadExistingTeamBoards(); _checkTeamGitDir(); }
    document.querySelectorAll('#launch-modal [data-acf-uid], #team-agent-modal [data-acf-uid]').forEach(c => {
        if (c.querySelector('.acf-agent-type')) {
            _syncACFModelField(c);
            _checkAgentCLI(c.querySelector('.acf-agent-type').value || 'claude', c.querySelector('.acf-cli-warning'));
        }
    });
}
window._onLaunchServerChange = _onLaunchServerChange;

async function _onQuickLaunchServerChange(value) {
    const next = _effectiveServer(coerceSelection(value, isHub(), state.servers));
    if (next === launch.quickServer) return;
    launch.quickServer = next;
    const b = document.getElementById('quick-launch-browser');
    if (b) b.style.display = 'none';
    _resetDirInputs(['quick-launch-workdir'], next, {}, { switching: true });
    if (next !== LOCAL_SERVER) {
        try { await _loadServerSettings(next); } catch (_) { /* launch will surface the error */ }
        if (launch.quickServer !== next) return;
        _resetDirInputs(['quick-launch-workdir'], next, _settingsFor(next), { switching: true });
    } else {
        _resetDirInputs(['quick-launch-workdir'], next, _settingsFor(next), { switching: true });
    }
}
window._onQuickLaunchServerChange = _onQuickLaunchServerChange;

// Directory inputs belong to whichever server their form launches on.
setDirServerResolver(inputId => {
    if (!isHub()) return LOCAL_SERVER;
    if (inputId === 'quick-launch-workdir') return launch.quickServer;
    if (inputId === 'resume-dir') return LOCAL_SERVER; // resume uses the hub's own session history
    return launch.server;
});


// Through the Coral Hub a machine at View (or Interact) refuses a launch: say so when the form opens, with the launch buttons dimmed, instead of after it is filled in (#4586).
// The buttons stay clickable; a click says the same reason in one toast (_launchBlocked).
function _syncLaunchAccess() {
    const modal = document.getElementById('launch-modal');
    if (!modal) return;
    const why = isHub() ? blockedReason(_effectiveServer(launch.server), 'launch') : '';
    const note = document.getElementById('launch-access-note');
    if (note) { note.hidden = !why; note.textContent = why; }
    for (const b of modal.querySelectorAll('[data-on-click="m-launch-session"], [data-on-click="m-launch-team"]')) {
        b.classList.toggle('hub-blocked', !!why);
        if (why) { b.setAttribute('aria-disabled', 'true'); b.title = why; } else { b.removeAttribute('aria-disabled'); b.removeAttribute('title'); }
    }
}
document.addEventListener('coral:server-access-loaded', () => { if (document.getElementById('launch-modal')?.style.display === 'flex') _syncLaunchAccess(); });


export async function launchSession() {
    let dir, type, agentName, flagsStr, backend;

    if (launch.mode === "terminal") {
        dir = document.getElementById("launch-terminal-dir").value.trim();
        type = "terminal";
        agentName = document.getElementById("launch-terminal-name").value.trim();
        flagsStr = "";
        backend = document.getElementById("launch-terminal-backend")?.value;
    } else {
        dir = document.getElementById("launch-dir").value.trim();
        const config = getAgentConfig('launch-agent-acf');
        type = config.agentType || 'claude';
        agentName = document.getElementById("launch-agent-name-input")?.value.trim() || config.name;
        flagsStr = config.flags;
        backend = document.getElementById("launch-backend")?.value;
    }

    if (!dir) {
        showToast("Working directory is required", true);
        return;
    }

    // Everything below (directory, CLI, defaults) is interpreted on this server.
    const server = _effectiveServer(launch.server);
    if (_launchBlocked(server)) return;

    // Permission flag check (skip for terminals)
    if (launch.mode !== "terminal") {
        const permResult = await _checkPermissionFlag(flagsStr, type, server);
        if (permResult === null) return;
        if (permResult === 'enable') {
            const mode = _settingsFor(server).default_permission_mode || 'bypassPermissions';
            flagsStr = _normalizePermFlagsForAgent(flagsStr, type, mode);
        }
    }

    const payload = { working_dir: dir, agent_type: type };
    if (agentName) payload.display_name = agentName;
    if (backend) payload.backend = backend;

    if (flagsStr) {
        payload.flags = flagsStr.split(/\s+/);
    }

    // Agent config from ACF (only for agent mode)
    if (launch.mode !== "terminal") {
        const config = getAgentConfig('launch-agent-acf');
        if (config.capabilities) payload.capabilities = config.capabilities;
        if (config.prompt) payload.prompt = config.prompt;
        if (config.model) payload.model = config.model;

        const boardSelect = document.getElementById("launch-board-select");
        if (boardSelect && boardSelect.value) {
            if (boardSelect.value === "__new__") {
                const boardName = document.getElementById("launch-board-name").value.trim();
                if (boardName) {
                    const problem = boardNameProblem(boardName);
                    if (problem) { showToast(problem, true); return; }
                    payload.board_name = boardName;
                }
            } else {
                // An existing board joins by its id (#3536); board_name is the same key, for a Coral
                // from before board ids.
                payload.board_id = payload.board_name = bareBoardName(boardSelect.value);
            }
            const boardServer = document.getElementById("launch-board-server").value.trim();
            if (boardServer) payload.board_server = boardServer;
        }
    }
    // The board select lists the target server's own boards (bare names).
    if (payload.board_name) payload.board_name = bareBoardName(payload.board_name);

    // Disable launch button to prevent double-clicks
    const stepId = launch.mode === "terminal" ? "launch-step-terminal" : "launch-step-agent";
    const launchBtn = document.querySelector(`#${stepId} .btn-primary`);
    if (launchBtn) { launchBtn.disabled = true; launchBtn.textContent = 'Launching...'; }

    try {
        const dirInput = document.getElementById(launch.mode === "terminal" ? "launch-terminal-dir" : "launch-dir");
        const result = await _launchOn(server, "/api/sessions/launch", payload, type, { dirInput });
        if (result) {
            recordRecentDir(dir, server);
            showToast(server === LOCAL_SERVER ? `Launched: ${result.session_name}` : `Launched on ${serverLabel(server, state.servers)}: ${result.session_name}`);
            hideLaunchModal();
            setTimeout(loadLiveSessions, 2000);
            _refreshAfterLaunch(server, { sessionName: result.session_name, sessionId: result.session_id });
        }
    } catch (e) {
        showToast("Failed to launch session", true);
    } finally {
        if (launchBtn) { launchBtn.disabled = false; launchBtn.textContent = 'Launch'; }
    }
}

// ── Agent Board Select ────────────────────────────────────────────────────

async function _loadAgentBoardProjects() {
    const select = document.getElementById("launch-board-select");
    if (!select) return;
    select.innerHTML = '<option value="">None</option><option value="__new__">Create new board...</option>';
    try {
        // Boards that exist on the server the agent will launch on.
        const server = _effectiveServer(launch.server);
        const resp = await serverFetch(server, "/api/board/projects");
        const projects = await resp.json();
        if (!Array.isArray(projects) || server !== _effectiveServer(launch.server)) return;
        noteBoardLabels(projects, 'project', server);
        for (const p of projects) {
            const opt = document.createElement("option");
            opt.value = p.project;
            opt.textContent = keyLabel(identityKey(server, p.project)); // its label, with a short id when two share it
            select.appendChild(opt);
        }
    } catch (_) {}
}

function _onAgentBoardChange() {
    const select = document.getElementById("launch-board-select");
    const newRow = document.getElementById("launch-new-board-row");
    const serverRow = document.getElementById("launch-board-server-row");
    const hasBoard = select.value && select.value !== "";
    newRow.style.display = select.value === "__new__" ? "" : "none";
    serverRow.style.display = hasBoard ? "" : "none";
    if (!hasBoard) document.getElementById("launch-board-server").value = "";
}
window._onAgentBoardChange = _onAgentBoardChange;


async function _saveCurrentPersona(nameInputId, promptInputId, flagsInputId) {
    let name, prompt, flags, capabilities;
    if (nameInputId) {
        // Legacy path with explicit element IDs
        name = document.getElementById(nameInputId)?.value.trim() || '';
        prompt = document.getElementById(promptInputId)?.value.trim() || '';
        flags = flagsInputId ? document.getElementById(flagsInputId)?.value.trim() || '' : '';
        capabilities = null;
    } else {
        // ACF path — read from the launch agent config form
        const config = getAgentConfig('launch-agent-acf');
        name = document.getElementById("launch-agent-name-input")?.value.trim() || config.name;
        prompt = config.prompt;
        flags = config.flags;
        capabilities = config.capabilities;
    }
    if (!name) { showToast("Enter a name before saving", "error"); return; }
    if (AGENT_PRESETS.find(p => p.name === name)) { showToast("Can't overwrite a built-in preset", "error"); return; }

    const entry = { name, prompt, flags };
    if (capabilities) entry.capabilities = capabilities;
    try { await _upsertSavedPersonas([entry]); } catch { showToast("Could not save the persona", "error"); return; }
    showToast(`Saved persona "${name}"`);

    // Re-render all preset containers
    _renderPresetButtons("agent-preset-selector", "agent");
}
window._saveCurrentPersona = _saveCurrentPersona;

// ── Export / Import (Personas & Team Templates) ──────────────────────────

function _downloadJson(data, filename) {
    const blob = new Blob([JSON.stringify(data, null, 2)], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = filename;
    a.click();
    URL.revokeObjectURL(url);
}

function _openFilePicker() {
    return new Promise((resolve) => {
        const input = document.createElement("input");
        input.type = "file";
        input.accept = ".json";
        input.onchange = () => {
            const file = input.files[0];
            if (!file) { resolve(null); return; }
            const reader = new FileReader();
            reader.onload = () => {
                try { resolve(JSON.parse(reader.result)); }
                catch { showToast("Invalid JSON file", "error"); resolve(null); }
            };
            reader.readAsText(file);
        };
        input.click();
    });
}

export function exportPersonas() {
    const saved = _getSavedPersonas();
    if (saved.length === 0) { showToast("No saved personas to export", "error"); return; }
    _downloadJson({ version: 1, type: "coral-personas", personas: saved }, "coral-personas.json");
    showToast(`Exported ${saved.length} persona(s)`);
}

export async function importPersonas() {
    const data = await _openFilePicker();
    if (!data) return;
    const personas = data.personas || (Array.isArray(data) ? data : null);
    if (!personas || !Array.isArray(personas)) { showToast("Invalid persona file format", "error"); return; }

    const entries = [];
    for (const p of personas) {
        if (!p.name) continue;
        if (AGENT_PRESETS.find(bp => bp.name === p.name)) continue;
        entries.push({ name: p.name, prompt: p.prompt || "", flags: p.flags || "" });
    }
    const added = entries.length;
    try { await _upsertSavedPersonas(entries); } catch { showToast("Could not save the imported personas", "error"); return; }
    showToast(`Imported ${added} persona(s)`);
    _renderPresetButtons("agent-preset-selector", "agent");
}

export function exportTeamTemplates() {
    const templates = _getSavedTeamTemplates();
    if (templates.length === 0) { showToast("No saved templates to export", "error"); return; }
    _downloadJson({ version: 1, type: "coral-team-templates", templates }, "coral-team-templates.json");
    showToast(`Exported ${templates.length} template(s)`);
}

export async function importTeamTemplates() {
    const data = await _openFilePicker();
    if (!data) return;
    const templates = data.templates || (Array.isArray(data) ? data : null);
    if (!templates || !Array.isArray(templates)) { showToast("Invalid template file format", "error"); return; }

    const entries = templates.filter(t => t.name && Array.isArray(t.agents));
    const added = entries.length;
    try { await _upsertSavedTeamTemplates(entries); } catch { showToast("Could not save the imported templates", "error"); return; }
    showToast(`Imported ${added} template(s)`);
    _renderTeamTemplateSelector();
}

// ── Agent Preset Selector (single-agent modal) ───────────────────────────

function _initAgentPresets() {
    _renderPresetButtons("agent-preset-selector", "agent");
}

function _selectAgentPreset(name) {
    const persona = name ? _findPersona(name) : null;
    const nameInput = document.getElementById("launch-agent-name-input");
    const current = getAgentConfig('launch-agent-acf');
    if (persona) {
        setAgentConfig('launch-agent-acf', {
            name: persona.name,
            prompt: persona.prompt,
            flags: persona.flags || current.flags,
            agentType: persona.agentType || persona.agent_type || current.agentType,
            model: persona.model || current.model,
            capabilities: persona.capabilities,
        });
        if (nameInput) nameInput.value = persona.name;
    } else {
        setAgentConfig('launch-agent-acf', {
            agentType: _settingsFor(_effectiveServer(launch.server)).default_agent_type || current.agentType,
        });
        if (nameInput) nameInput.value = '';
    }
}

// ── Add Agent to Board ───────────────────────────────────────────────────
// Launches started from inside a board / folder group inherit that group's
// server (the key is "@server/name" for remotes) and cannot be redirected: the
// working directory shown belongs to that server.

export function showAddAgentToBoard(boardName, workDir) {
    const server = _effectiveServer(splitKey(boardName).server);
    if (_launchBlocked(server)) return;
    launch.addAgentServer = server;
    _serverSettings.delete(server);
    const modal = document.getElementById("add-agent-board-modal");
    document.getElementById("add-agent-board-name").value = boardName;
    document.getElementById("add-agent-board-workdir").value = workDir;
    document.getElementById("add-agent-board-subtitle").textContent =
        `Board: ${keyLabel(boardName)}${_onServerSuffix(server)}`;

    const open = () => {
        renderAgentConfigForm('add-agent-board-acf', {
            showPreset: true,
            showName: true,
            value: { agentType: _settingsFor(server).default_agent_type || 'claude' },
        });
        modal.style.display = "flex";
    };
    _withServerSettings(server, open);
}


export function hideAddAgentBoardModal() {
    document.getElementById("add-agent-board-modal").style.display = "none";
}


export async function launchAgentToBoard() {
    const boardKey = document.getElementById("add-agent-board-name").value;
    const workDir = document.getElementById("add-agent-board-workdir").value;
    const server = _effectiveServer(launch.addAgentServer);
    if (_launchBlocked(server)) return;
    const config = getAgentConfig('add-agent-board-acf');

    if (!config.name) {
        showToast("Agent name is required", "error");
        return;
    }

    // Permission flag check
    let flagsStr = config.flags;
    const permResult = await _checkPermissionFlag(flagsStr, config.agentType, server);
    if (permResult === null) return;
    if (permResult === 'enable') {
        const mode = _settingsFor(server).default_permission_mode || 'bypassPermissions';
        flagsStr = _normalizePermFlagsForAgent(flagsStr, config.agentType, mode);
    }

    const flags = flagsStr ? flagsStr.split(/\s+/) : [];

    // Disable launch button to prevent double-clicks
    const launchBtn = document.querySelector('#add-agent-board-modal .btn-primary');
    if (launchBtn) { launchBtn.disabled = true; launchBtn.textContent = 'Launching...'; }

    try {
        const boardName = bareBoardName(boardKey);
        const launchBody = {
            working_dir: workDir,
            agent_type: config.agentType,
            display_name: config.name,
            flags,
            prompt: config.prompt,
        };
        // Standalone (no board) when opened from a folder group; a board joins by its id (#3536).
        if (boardName) launchBody.board_id = launchBody.board_name = boardName;
        if (config.model) launchBody.model = config.model;
        if (config.capabilities) launchBody.capabilities = config.capabilities;
        const result = await _launchOn(server, "/api/sessions/launch", launchBody, config.agentType);
        if (result) {
            showToast(boardName ? `Launched ${config.name} on board ${keyLabel(boardKey)}${_onServerSuffix(server)}` : `Launched ${config.name}${_onServerSuffix(server)}`);
            hideAddAgentBoardModal();
            _refreshAfterLaunch(server, { sessionName: result.session_name, sessionId: result.session_id });
        }
    } catch (e) {
        showToast("Failed to launch agent", "error");
    } finally {
        if (launchBtn) { launchBtn.disabled = false; launchBtn.textContent = 'Launch'; }
    }
}

// ── Launch Terminal to Board ─────────────────────────────────────────────

export async function launchTerminalToBoard(boardName, workDir) {
    const server = _effectiveServer(splitKey(boardName).server);
    if (_launchBlocked(server)) return;
    const board = bareBoardName(boardName);
    try {
        const result = await _launchOn(server, "/api/sessions/launch", {
            working_dir: workDir,
            agent_type: "terminal",
            display_name: "Terminal",
            board_id: board, // joins the board (#3536)
            board_name: board,
        });
        if (result) {
            showToast(`Launched Terminal on ${keyLabel(boardName)}${_onServerSuffix(server)}`);
            _refreshAfterLaunch(server, { sessionName: result.session_name, sessionId: result.session_id });
        }
    } catch (e) {
        showToast("Failed to launch terminal", "error");
    }
}

// ── Standalone Agent Launch (no board) ────────────────────────────────────

export async function launchDefaultAgent(workDir, server) {
    server = _effectiveServer(server);
    if (_launchBlocked(server)) return;
    let s = _settingsFor(server);
    if (server !== LOCAL_SERVER) {
        // Defaults (agent type, permission mode) are the remote's own; if they
        // cannot be read the remote is not reachable, so launch nothing.
        try { s = await _loadServerSettings(server, { fresh: true }); } catch (e) {
            showToast(`${serverLabel(server, state.servers)}: ${e.message || 'unreachable'}`, true);
            return;
        }
    }
    const agentType = s.default_agent_type || 'claude';
    const permFlag = _getPermissionFlagsForAgentMode(agentType, s.default_permission_mode || 'bypassPermissions');
    const flags = permFlag ? permFlag.split(/\s+/) : [];

    const payload = {
        working_dir: workDir,
        agent_type: agentType,
        flags,
    };

    try {
        const result = await _launchOn(server, '/api/sessions/launch', payload, agentType);
        if (result) {
            showToast(`Launched ${agentType} agent${_onServerSuffix(server)}`);
            _refreshAfterLaunch(server, { sessionName: result.session_name, sessionId: result.session_id });
        }
    } catch (e) {
        showToast('Failed to launch agent', 'error');
    }
}

export function showAddStandaloneAgent(workDir, server) {
    server = _effectiveServer(server);
    if (_launchBlocked(server)) return;
    launch.addAgentServer = server;
    _serverSettings.delete(server);
    const modal = document.getElementById('add-agent-board-modal');
    document.getElementById('add-agent-board-name').value = '';
    document.getElementById('add-agent-board-workdir').value = workDir;
    document.getElementById('add-agent-board-subtitle').textContent = `Directory: ${workDir}${_onServerSuffix(server)}`;

    _withServerSettings(server, () => {
        renderAgentConfigForm('add-agent-board-acf', { showPreset: true, showName: true });
        modal.style.display = 'flex';
    });
}

/**
 * Server of the folder group whose directory is `workDir`, for callers that
 * only know the path. Falls back to local when no live session uses it; refuses
 * (returns null) when the same path is used on several servers, because
 * guessing could launch on the wrong machine.
 */
function _serverForWorkDir(workDir) {
    if (!isHub()) return LOCAL_SERVER;
    const servers = new Set((state.liveSessions || [])
        .filter(s => s.working_directory === workDir)
        .map(s => normServer(s.server)));
    if (servers.size === 0) return LOCAL_SERVER;
    return servers.size === 1 ? [...servers][0] : null;
}

export async function launchStandaloneTerminal(workDir, server) {
    if (server === undefined) {
        server = _serverForWorkDir(workDir);
        if (server === null) {
            showToast('This directory is used on more than one server; launch the terminal from that server\'s group.', true);
            return;
        }
    }
    server = _effectiveServer(server);
    if (_launchBlocked(server)) return;
    try {
        const result = await _launchOn(server, '/api/sessions/launch', {
            working_dir: workDir,
            agent_type: 'terminal',
            display_name: 'Terminal',
        });
        if (result) {
            showToast(`Launched Terminal${_onServerSuffix(server)}`);
            _refreshAfterLaunch(server, { sessionName: result.session_name, sessionId: result.session_id });
        }
    } catch (e) {
        showToast('Failed to launch terminal', 'error');
    }
}


// ── Mobile Connect Modal ─────────────────────────────────────────────────

// The mobile QR dialog loads on first use (#3140).
window._showMobileConnectModal = (...args) => importOnce('./mobile_connect.js', 'the mobile connect dialog').then(m => m.showMobileConnectModal(...args), () => {});
window._hideMobileConnectModal = (...args) => importOnce('./mobile_connect.js').then(m => m.hideMobileConnectModal(...args), () => {});

// The dialogs' generated markup (#3645): data-on-* names, never code. Values are checked; a bad
// one does nothing. (The job dialog's own flag buttons and agent select register in app.js: they
// exist before this module loads.)
const teamRow = el => el.closest('.team-agent-row');
registerActions({
    'team-generated-review': () => _reviewGeneratedTeam(),
    'info-copy': el => {
        const cmd = textOf(el.dataset.cmd, 8192);
        if (!cmd) return;
        navigator.clipboard.writeText(cmd);
        el.textContent = 'Copied!';
        setTimeout(() => { el.textContent = 'Copy'; }, 1500);
    },
    'preset-pick': el => {
        const v = textOf(el.value);
        if (v && el.dataset.kind === 'agent') _selectAgentPreset(v);
        else if (v && el.dataset.kind === 'acf') { const t = tokenOf(el.dataset.target); if (t) _applyACFPresetFor(t, v); }
        el.value = '';
    },
    'perm-chip-toggle': el => _togglePermChip(el),
    'perm-mode-describe': el => window._updatePermModeDescription(el),
    'perms-toggle': el => { const t = tokenOf(el.dataset.target); if (t) _togglePermissions(t); },
    'team-agent-open': el => { const r = teamRow(el); if (r) window.showTeamAgentModal(r); },
    'team-agent-edit': (el, event) => { event.stopPropagation(); const r = teamRow(el); if (r) window.showTeamAgentModal(r); },
    'team-agent-move': (el, event) => { event.stopPropagation(); if (el.dataset.dir === 'up' || el.dataset.dir === 'down') _moveTeamAgent(el, el.dataset.dir === 'up' ? -1 : 1); },
    'team-agent-remove': (el, event) => { event.stopPropagation(); teamRow(el)?.remove(); },
});
