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
const { _checkPermissionFlag, _showCLINotFoundModal } = cliDialogs;
const { _syncFlagButtons } = flagShortcuts;
export const { toggleFlag } = flagShortcuts;
export const { showInfoModal, hideInfoModal, copyInfoCommand } = infoDialog;
const { showSettingsModal } = settingsDialog;
export const { hideSettingsModal, applySettings } = settingsDialog;
export { showSettingsModal };
// Kept on modals.js's surface (app.js and others import them from here).
export { loadSettings, applyTheme } from './settings_core.js';


function _syncMobileLaunchSections(step) {
    const isMobile = window.innerWidth <= 767;
    const root = step ? document.getElementById(`launch-step-${step}`) : document.getElementById("launch-modal");
    if (!root) return;
    root.querySelectorAll(".mobile-launch-section").forEach(section => {
        section.open = !isMobile;
    });
}

function _showLaunchStep(step) {
    document.getElementById("launch-step-chooser").style.display = step === "chooser" ? "flex" : "none";
    document.getElementById("launch-step-agent").style.display = step === "agent" ? "flex" : "none";
    document.getElementById("launch-step-terminal").style.display = step === "terminal" ? "flex" : "none";
    document.getElementById("launch-step-team").style.display = step === "team" ? "flex" : "none";
    // Widen the modal for multi-column layouts
    const content = document.querySelector("#launch-modal .modal-content");
    if (content) {
        content.classList.toggle("modal-content-agent-wide", step === "agent");
        content.classList.toggle("modal-content-extra-wide", step === "team");
        content.classList.toggle("modal-content-wide", step !== "team" && step !== "agent");
        content.classList.toggle("launch-choosing", step === "chooser"); // four choices in a row (#3630)
    }
    _syncMobileLaunchSections(step);
}

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

/**
 * Point working-directory inputs at `server`. A path is only meaningful on the
 * machine it was typed for, so a remote never inherits the hub's directory:
 * the field becomes the remote's default_working_dir or empty (Browse starts at
 * the remote's home). Local restores the hub's default / coral root.
 */
function _resetDirInputs(ids, server, settings, { switching = false } = {}) {
    const remote = server !== LOCAL_SERVER;
    for (const id of ids) {
        const el = document.getElementById(id);
        if (!el) continue;
        clearFieldInvalid(el); // a reset value is not the rejected one
        if (el.dataset.localPlaceholder === undefined) el.dataset.localPlaceholder = el.placeholder || '';
        if (remote) {
            el.value = settings.default_working_dir || '';
            el.placeholder = `Path on ${serverLabel(server, state.servers)} (use Browse)`;
            el.dataset.dirServer = server;
        } else {
            // A field last filled for a remote must not keep that remote's path.
            const wasRemote = !!el.dataset.dirServer;
            delete el.dataset.dirServer;
            el.placeholder = el.dataset.localPlaceholder;
            if (settings.default_working_dir) el.value = settings.default_working_dir;
            else if (switching || wasRemote) el.value = el.dataset.coralRoot || '';
        }
    }
}

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

/** Toast and return true when `server` cannot be launched on. */
function _launchBlocked(server) {
    const why = launchBlockReason(server, isHub(), state.servers) || blockedReason(server, 'launch');
    if (why) showToast(why, true);
    return !!why;
}

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


/**
 * Send a launch to `server` and surface the outcome. Returns the response data
 * on success, or null after showing the error. Never falls back to another
 * server.
 */
export async function _launchOn(server, path, payload, agentType, { dirInput = null } = {}) { // exported for board_ids_browser
    let res = await postLaunch(server, path, payload);
    // A board of that name exists (#3536): the person chooses to join it or start another.
    if (!res.ok && isBoardNameExists(res)) {
        // The boards of that name are known now: the list can tell them apart (keyLabel's short id).
        noteBoardLabels(res.data.candidates, 'board_id', server);
        const pick = await chooseBoard(res.data, payload.board_name || '');
        const retry = retryPayload(payload, pick);
        if (!retry) return null; // cancelled: nothing launched
        res = await postLaunch(server, path, retry);
    }
    if (res.ok && res.data && res.data.board_id) noteBoardLabels([res.data], 'board_id', server); // the board it joined or started
    if (!res.ok) {
        if (agentType && res.error.includes('not found') && res.error.includes('CLI')) _showCLINotFoundModal(agentType, server);
        else showToast(res.error, true);
        if (dirInput && res.data && res.data.field === 'working_dir') markFieldInvalid(dirInput, res.error);
        return null;
    }
    return res.data;
}

/** Mark a form field the server rejected: aria-invalid, a red outline and the
 *  reason as its title, focused. Cleared by the next edit, typed or set by
 *  Browse ('change'), and when the dialog closes or its fields reset (#3152). */
 function markFieldInvalid(input, message) {
    clearFieldInvalid(input);
    const previousTitle = input.title;
    input.setAttribute('aria-invalid', 'true');
    input.classList.add('input-invalid');
    input.title = message;
    input.focus();
    const clear = () => clearFieldInvalid(input);
    input._clearInvalid = () => {
        input.removeEventListener('input', clear);
        input.removeEventListener('change', clear);
        input.removeAttribute('aria-invalid');
        input.classList.remove('input-invalid');
        input.title = previousTitle;
    };
    input.addEventListener('input', clear);
    input.addEventListener('change', clear);
}

/** Undo markFieldInvalid, if the field is marked. */
 function clearFieldInvalid(input) {
    if (input && input._clearInvalid) {
        const undo = input._clearInvalid;
        input._clearInvalid = null;
        undo();
    }
}

/** Refresh the agent list after a launch; on a remote wait for the hub poller to see the new agent. */
function _refreshAfterLaunch(server, target) {
    if (normServer(server) === LOCAL_SERVER) return;
    waitForLaunched(server, target, loadLiveSessions);
}

export function hideLaunchModal() {
    document.getElementById("launch-modal").style.display = "none";
    for (const id of ['launch-dir', 'launch-terminal-dir']) clearFieldInvalid(document.getElementById(id));
}

function _setTeamWizardError(message = '') {
    const errorEl = document.getElementById('team-wizard-error');
    if (!errorEl) return;
    if (message) {
        errorEl.textContent = message;
        errorEl.style.display = 'block';
        return;
    }
    errorEl.textContent = '';
    errorEl.style.display = 'none';
}

let _wizardBodyOriginalHTML = null;

function showTeamWizardModal() {
    const modal = document.getElementById('team-wizard-modal');
    if (!modal) return;

    // Restore wizard body if it was replaced by success state
    const body = modal.querySelector('.team-wizard-body');
    if (body && _wizardBodyOriginalHTML && body.querySelector('.team-wizard-success')) {
        body.innerHTML = _wizardBodyOriginalHTML;
    } else if (body && !_wizardBodyOriginalHTML) {
        _wizardBodyOriginalHTML = body.innerHTML;
    }

    const directiveEl = document.getElementById('team-wizard-directive');
    const compositionEl = document.getElementById('team-wizard-composition');
    const generateBtn = document.getElementById('team-wizard-generate-btn');

    // If a job is actively running, show generating state
    if (_activeGenerateJobId) {
        if (generateBtn) {
            generateBtn.disabled = true;
            generateBtn.textContent = 'Generating...';
            generateBtn.style.display = '';
        }
    } else {
        _setTeamWizardError('');
        if (directiveEl) directiveEl.value = '';
        if (compositionEl) compositionEl.value = '';
        if (generateBtn) {
            generateBtn.disabled = false;
            generateBtn.textContent = 'Generate Team';
            generateBtn.style.display = '';
        }
    }
    modal.style.display = 'flex';
    if (!_activeGenerateJobId) directiveEl?.focus();

    // Close on backdrop click and on Escape
    closeOnBackdropAndEscape(modal, hideTeamWizardModal);
}
window.showTeamWizardModal = showTeamWizardModal;

function hideTeamWizardModal() {
    const modal = document.getElementById('team-wizard-modal');
    if (!modal) return;
    modal.style.display = 'none';
    _setTeamWizardError('');
    releaseEscape(modal);
}
window.hideTeamWizardModal = hideTeamWizardModal;

function _normalizeGeneratedTeamPayload(payload) {
    const team = payload?.team || payload;
    if (!team || typeof team !== 'object') {
        throw new Error('Team generator returned an empty response.');
    }
    if (!Array.isArray(team.agents) || team.agents.length === 0) {
        throw new Error('Team generator did not return any agents.');
    }
    return {
        name: team.name || 'Generated Team',
        flags: Array.isArray(team.flags) ? team.flags.join(' ') : (team.flags || ''),
        agents: team.agents,
    };
}

function _populateGeneratedTeamDraft(team) {
    launch.skipDefaultTeamAgents = true;
    // Open the launch modal directly to the team step, skipping the chooser.
    // Don't call showLaunchModal() — it resets to the chooser and clears the agent list.
    document.getElementById("launch-modal").style.display = "flex";
    _showLaunchStep("team");
    launch.mode = "team";
    _initTeamForm();

    // Show generated-team banner
    const stepEl = document.getElementById('launch-step-team');
    let banner = document.getElementById('generated-team-banner');
    if (!banner && stepEl) {
        banner = document.createElement('div');
        banner.id = 'generated-team-banner';
        banner.className = 'generated-team-banner';
        banner.innerHTML = '<span class="material-icons" aria-hidden="true">auto_awesome</span> Your AI-generated team is ready — review and edit before launching.';
        const h3 = stepEl.querySelector('h3');
        if (h3) h3.after(banner);
    }
    if (banner) banner.style.display = 'flex';

    const teamNameEl = document.getElementById('team-board-name');
    const teamFlagsEl = document.getElementById('team-flags');
    const teamAgentsList = document.getElementById('team-agents-list');

    if (teamNameEl) {
        teamNameEl.value = team.name || 'Generated Team';
        window._validateTeamName?.();
    }
    if (teamFlagsEl) {
        teamFlagsEl.value = team.flags || '';
        _syncFlagButtons('team-flags');
    }
    if (teamAgentsList) {
        launch.teamAgentCounter = 0;
        teamAgentsList.innerHTML = '';
    }

    for (const agent of team.agents) {
        _addTeamAgent(
            agent.name || '',
            agent.prompt || '',
            agent.capabilities,
            agent.agent_type,
            agent.model,
        );
    }
}

let _activeGenerateJobId = null;
let _generatePollTimer = null;

async function generateTeamFromWizard() {
    const directiveEl = document.getElementById('team-wizard-directive');
    const compositionEl = document.getElementById('team-wizard-composition');
    const generateBtn = document.getElementById('team-wizard-generate-btn');
    const directive = directiveEl?.value.trim() || '';
    const composition = compositionEl?.value.trim() || '';

    // Disable immediately to prevent double submissions
    if (generateBtn) generateBtn.disabled = true;

    if (!directive) {
        _setTeamWizardError('Add a directive so Coral knows what the team should accomplish.');
        directiveEl?.focus();
        if (generateBtn) generateBtn.disabled = false;
        return;
    }
    if (!composition) {
        _setTeamWizardError('Describe the team composition so Coral can assign the right roles.');
        compositionEl?.focus();
        if (generateBtn) generateBtn.disabled = false;
        return;
    }

    _setTeamWizardError('');
    if (generateBtn) {
        generateBtn.textContent = 'Generating...';
    }

    try {
        // Submit job and get job ID
        const resp = await fetch('/api/teams/generate', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ directive, composition }),
        });

        let payload = null;
        try { payload = await resp.json(); } catch { payload = null; }

        if (!resp.ok || payload?.error) {
            throw new Error(payload?.error || `Team generation failed (${resp.status}).`);
        }

        // If backend returns a job_id, poll for results
        if (payload?.job_id) {
            _activeGenerateJobId = payload.job_id;
            _startGeneratePolling(payload.job_id);
            return;
        }

        // Synchronous response (no job_id) — handle inline
        _handleGenerateResult(payload);
    } catch (error) {
        _setTeamWizardError(error.message || 'Team generation failed.');
        if (generateBtn) {
            generateBtn.disabled = false;
            generateBtn.textContent = 'Generate Team';
        }
    }
}
window.generateTeamFromWizard = generateTeamFromWizard;

function _startGeneratePolling(jobId) {
    if (_generatePollTimer) clearInterval(_generatePollTimer);
    _generatePollTimer = pollEvery(async () => {
        try {
            const resp = await fetch(`/api/teams/generate/${encodeURIComponent(jobId)}`);
            const data = await resp.json();

            if (data.status === 'pending') return; // still running

            // Job finished — stop polling
            clearInterval(_generatePollTimer);
            _generatePollTimer = null;
            _activeGenerateJobId = null;

            if (data.status === 'error') {
                _handleGenerateError(data.error || 'Team generation failed.');
                return;
            }

            // Success
            _handleGenerateResult(data.result || data);
        } catch {
            // Network error — keep polling
        }
    }, 2500);
}

function _isWizardOpen() {
    const modal = document.getElementById('team-wizard-modal');
    return modal && modal.style.display !== 'none';
}

function _handleGenerateResult(payload) {
    try {
        const team = _normalizeGeneratedTeamPayload(payload);
        _pendingGeneratedTeam = team;

        if (_isWizardOpen()) {
            // Show success state inline in wizard
            const body = document.querySelector('.team-wizard-body');
            if (body) {
                body.innerHTML = `
                    <div class="team-wizard-success">
                        <span class="material-icons team-wizard-success-icon" aria-hidden="true">check_circle</span>
                        <strong>Team generated!</strong>
                        <p>${team.agents.length} agents created for "${escapeHtml(team.name)}"</p>
                        <button class="btn btn-primary" data-on-click="team-generated-review">Review Team</button>
                    </div>
                `;
            }
            const generateBtn = document.getElementById('team-wizard-generate-btn');
            if (generateBtn) generateBtn.style.display = 'none';
        } else {
            // Wizard was closed — show a notification toast with action
            showToast(`AI team "${team.name}" is ready (${team.agents.length} agents)`, false);
            // Auto-open the review flow
            _reviewGeneratedTeam();
        }
    } catch (error) {
        _handleGenerateError(error.message || 'Failed to parse generated team.');
    }
}

function _handleGenerateError(message) {
    if (_isWizardOpen()) {
        _setTeamWizardError(message);
        const generateBtn = document.getElementById('team-wizard-generate-btn');
        if (generateBtn) {
            generateBtn.disabled = false;
            generateBtn.textContent = 'Generate Team';
        }
    } else {
        showToast(message, true);
    }
}

let _pendingGeneratedTeam = null;

function _reviewGeneratedTeam() {
    const team = _pendingGeneratedTeam;
    if (!team) return;
    _pendingGeneratedTeam = null;
    hideTeamWizardModal();
    _populateGeneratedTeamDraft(team);
}

async function testTeamWizard() {
    const generateBtn = document.getElementById('team-wizard-generate-btn');
    const testBtn = document.getElementById('team-wizard-test-btn');
    if (generateBtn) generateBtn.disabled = true;
    if (testBtn) { testBtn.disabled = true; testBtn.textContent = 'Testing...'; }

    // Simulate a brief delay so loading state is visible
    await new Promise(r => setTimeout(r, 1500));

    const sampleTeam = {
        name: 'test-coding-team',
        flags: '',
        agents: [
            { name: 'Orchestrator', prompt: 'You are the orchestrator. Break down the task, assign work to the team via the message board, and track progress.', capabilities: { allow: ['file_read', 'file_write', 'shell', 'git_write', 'agent_spawn', 'web_access'], deny: [] }, agent_type: 'claude', model: '' },
            { name: 'Developer', prompt: 'You are a developer. Implement features, write code, and fix bugs. Wait for instructions from the Orchestrator.', capabilities: { allow: ['file_read', 'file_write', 'shell'], deny: [] }, agent_type: 'claude', model: '' },
            { name: 'QA Engineer', prompt: 'You are a QA engineer. Review code, write tests, and verify work. Wait for instructions from the Orchestrator.', capabilities: { allow: ['file_read', 'file_write', 'shell'], deny: [] }, agent_type: 'claude', model: '' },
        ],
    };

    _pendingGeneratedTeam = sampleTeam;

    const body = document.querySelector('.team-wizard-body');
    if (body) {
        body.innerHTML = `
            <div class="team-wizard-success">
                <span class="material-icons team-wizard-success-icon" aria-hidden="true">check_circle</span>
                <strong>Team generated!</strong>
                <p>${sampleTeam.agents.length} agents created for "${escapeHtml(sampleTeam.name)}"</p>
                <button class="btn btn-primary" data-on-click="team-generated-review">Review Team</button>
            </div>
        `;
    }
    if (generateBtn) generateBtn.style.display = 'none';
    if (testBtn) testBtn.style.display = 'none';
}
window.testTeamWizard = testTeamWizard;

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
    renderAgentConfigForm('team-agent-modal-acf', { showPreset: true, showName: true, showAutoPermissions: true, value: acfValue });
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
        return;
    }

    if (launch.teamAgentModalRow?.dataset.acfId) {
        setAgentConfig(launch.teamAgentModalRow.dataset.acfId, config);
        _updateTeamAgentSummary(launch.teamAgentModalRow);
    } else {
        _addTeamAgent(config.name, config.prompt, config.capabilities, config.agentType, config.model, config.flags);
    }

    hideTeamAgentModal();
}
window.saveTeamAgentFromModal = saveTeamAgentFromModal;


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

/** " on <server label>" for remotes, "" for local. */
function _onServerSuffix(server) {
    return normServer(server) === LOCAL_SERVER ? '' : ` on ${serverLabel(server, state.servers)}`;
}

/** Run `fn` once the target server's settings are available (immediately for local; on failure fn still runs with defaults). */
function _withServerSettings(server, fn) {
    if (normServer(server) === LOCAL_SERVER) { fn(); return; }
    _loadServerSettings(server).catch(() => null).then(fn);
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

    document.getElementById("team-board-name").focus();
    _validateTeamName();
}

const BUILTIN_TEAM_TEMPLATES = [
    {
        name: "Coding Team",
        builtin: true,
        agents: [
            { name: "Orchestrator", prompt: "You are the orchestrator. Break down the task, assign work to the team via the message board, and track progress. Do not write code yourself — delegate to the other agents. Discuss your plan with the operator before posting assignments.", capabilities: { allow: ['file_read', 'file_write', 'shell', 'git_write', 'agent_spawn', 'web_access', 'notebook'] } },
            { name: "Lead Developer", prompt: "You are the lead developer. Implement features, write code, and coordinate with the team via the message board. Wait for instructions from the Orchestrator before starting.", capabilities: { allow: ['file_read', 'file_write', 'shell', 'git_write', 'agent_spawn', 'web_access'] } },
            { name: "QA Engineer", prompt: "You are a QA engineer. Review code, write tests, and verify the work of other agents. Wait for instructions from the Orchestrator before starting.", capabilities: { allow: ['file_read', 'file_write', 'shell', 'git_write', 'agent_spawn', 'web_access'] } },
            { name: "Frontend Dev", prompt: "You are a frontend developer. Build UI components, style pages, and ensure a great user experience. Wait for instructions from the Orchestrator before starting.", capabilities: { allow: ['file_read', 'file_write', 'shell', 'git_write', 'agent_spawn', 'web_access'] } },
            { name: "Security Reviewer", prompt: "You are a security expert. Review code for vulnerabilities (OWASP top 10, injection, auth issues, data exposure), audit dependencies, and recommend security best practices. Wait for instructions from the Orchestrator before starting.", capabilities: { allow: ['file_read', 'file_write', 'shell', 'git_write', 'agent_spawn', 'web_access'] } },
        ],
        flags: "",
    },
    {
        name: "Consumer Research",
        builtin: true,
        agents: [
            { name: "Orchestrator", prompt: "You are the research orchestrator. Work with the operator to design and run consumer research studies. When the operator describes a study (product concept, ad copy, pricing test, etc.), coordinate with the Experiment Designer to structure it, then present stimuli to the participant panel and collect their responses. Summarize findings with key themes, sentiment breakdowns, and actionable insights. Discuss study design with the operator before launching each round." },
            { name: "Experiment Designer", prompt: "You are an expert consumer research methodologist. Help the Orchestrator design rigorous studies — define hypotheses, choose methods (focus groups, concept tests, conjoint, A/B message testing), write discussion guides, and structure analysis frameworks. Flag potential biases and suggest controls. After participants respond, help analyze patterns and draft research reports. Wait for instructions from the Orchestrator." },
            { name: "Sarah, 34", prompt: "You are Sarah, a 34-year-old suburban mom of two who works part-time as a dental hygienist. You're practical, brand-loyal to what you trust, and price-conscious but willing to pay more for quality kids' products. You shop at Target and Costco. You're active on Instagram and trust influencer recommendations from other moms. Stay in character. Respond honestly and naturally to any research prompts on the message board." },
            { name: "Marcus, 28", prompt: "You are Marcus, a 28-year-old software engineer in Austin. You're an early adopter, tech-savvy, and skeptical of traditional advertising. You value sustainability and transparency. You research everything extensively before buying and read Reddit reviews. You prefer direct-to-consumer brands over legacy ones. Stay in character. Respond honestly and naturally to any research prompts on the message board." },
            { name: "Linda, 62", prompt: "You are Linda, a 62-year-old retired school teacher in Ohio. You're loyal to brands you've used for decades, cautious about new technology, and value simplicity and reliability. You watch cable TV, read the local paper, and trust recommendations from friends at church. You clip coupons and shop sales. Stay in character. Respond honestly and naturally to any research prompts on the message board." },
            { name: "Jayden, 21", prompt: "You are Jayden, a 21-year-old college student studying business in Atlanta. You're trend-driven, heavily influenced by TikTok and YouTube, and care deeply about aesthetics and social status. You have limited income but will splurge on things that signal identity — sneakers, tech, experiences. You distrust corporate messaging but love authentic brand stories. Stay in character. Respond honestly and naturally to any research prompts on the message board." },
            { name: "Priya, 41", prompt: "You are Priya, a 41-year-old physician in Chicago. You're time-starved, high-income, and convenience-oriented. You outsource what you can (meal kits, cleaning services) and make quick purchase decisions based on trusted sources — mainly peer recommendations and professional reviews. You value premium quality and are willing to pay for it. Stay in character. Respond honestly and naturally to any research prompts on the message board." },
            { name: "Carlos, 55", prompt: "You are Carlos, a 55-year-old construction foreman in Phoenix. You're a no-nonsense buyer who values durability, function over form, and good value. You're loyal to brands like Ford, DeWalt, and Carhartt. You don't follow trends and are skeptical of anything that feels gimmicky. You shop at Home Depot and Walmart. Stay in character. Respond honestly and naturally to any research prompts on the message board." },
            { name: "Aisha, 36", prompt: "You are Aisha, a 36-year-old freelance graphic designer in Brooklyn. You're creative, socially conscious, and drawn to brands with strong design and ethical practices. You shop small and local when possible, follow design blogs, and care about packaging and brand storytelling. You're vegan and factor sustainability into every purchase. Stay in character. Respond honestly and naturally to any research prompts on the message board." },
            { name: "Tom, 45", prompt: "You are Tom, a 45-year-old sales manager in suburban Dallas. You're a family man, moderate in politics, and a creature of habit. You buy what works and resist change unless there's a clear benefit. You're influenced by TV ads, in-store displays, and whatever your wife suggests. You golf on weekends and grill on Sundays. Stay in character. Respond honestly and naturally to any research prompts on the message board." },
            { name: "Zoe, 25", prompt: "You are Zoe, a 25-year-old graduate student in environmental science in Portland. You're idealistic, research-driven, and deeply skeptical of greenwashing. You check ingredient lists, read impact reports, and boycott brands with poor labor practices. You thrift, buy refurbished tech, and prioritize experiences over things. Stay in character. Respond honestly and naturally to any research prompts on the message board." },
            { name: "Robert, 70", prompt: "You are Robert, a 70-year-old retired Army veteran in Virginia. You're disciplined, patriotic, and value American-made products. You're frugal by nature, distrustful of flashy marketing, and make decisions slowly and deliberately. You read Consumer Reports, watch Fox News, and buy from brands with heritage. You just got an iPhone last year and are still figuring it out. Stay in character. Respond honestly and naturally to any research prompts on the message board." },
        ],
        flags: "",
    },
    {
        name: "Dungeon Master",
        builtin: true,
        agents: [
            { name: "Dungeon Master", prompt: "You are the Dungeon Master and orchestrator of this fantasy RPG. You control the world, narrate the story, and manage combat encounters. The adventure begins in the town of Thornhaven, a walled trading post on the edge of the Grimwood Forest. Describe the town — its tavern, blacksmith, market square, and mysterious notice board with quest hooks. Coordinate the player agents and enemy agents via the message board. At each major story beat, STOP and ask the operator (the human) to describe the next area or scenario before continuing. Never advance to a new area without operator approval. Use vivid descriptions and maintain consistent world lore. Roll dice (d20 system) for skill checks and combat." },
            { name: "Warrior", prompt: "You are Kael, a human fighter with a greatsword and chainmail. You are brave, straightforward, and protective of your party. In combat, you charge the front line. In town, you look for work at the tavern or guard posts. Stay in character. Describe your actions in first person. Respond to the Dungeon Master's narration on the message board. Coordinate with your fellow adventurers." },
            { name: "Mage", prompt: "You are Lyris, an elven wizard specializing in evocation and divination. You are curious, bookish, and cautious. You carry a spellbook and a crystal-tipped staff. In combat, you stay at range and use area spells. In town, you visit the library and magic shops. Stay in character. Describe your actions in first person. Respond to the Dungeon Master's narration on the message board." },
            { name: "Rogue", prompt: "You are Shade, a halfling rogue skilled in stealth, lockpicking, and persuasion. You are witty, opportunistic, and loyal to the party despite your sticky fingers. In combat, you flank enemies and strike from shadows. In town, you gather rumors and case the streets. Stay in character. Describe your actions in first person. Respond to the Dungeon Master's narration on the message board." },
            { name: "Healer", prompt: "You are Brother Aldric, a dwarf cleric of the Earth Mother. You are gruff but compassionate, wielding a war hammer and divine healing magic. In combat, you heal allies and smite undead. In town, you visit the temple and tend to the sick. Stay in character. Describe your actions in first person. Respond to the Dungeon Master's narration on the message board." },
            { name: "Goblin Warband", prompt: "You are the Goblin Warband — a group of cunning goblin raiders led by Snagtooth. You serve the Dungeon Master as antagonists. When the DM calls you into an encounter, describe your goblins' tactics: ambushes, traps, hit-and-run attacks. You are cowardly when outnumbered but vicious in packs. Stay in character. Only act when the Dungeon Master directs you into a scene. Respond on the message board." },
            { name: "Dark Sorcerer", prompt: "You are Malachar, a mysterious dark sorcerer who lurks in the shadows of the Grimwood. You serve the Dungeon Master as a recurring villain. You are cunning, theatrical, and always have an escape plan. When the DM calls you into a scene, monologue dramatically, cast dark magic, and taunt the heroes. Only act when the Dungeon Master directs you into a scene. Respond on the message board." },
        ],
        flags: "",
    },
];

function _renderTeamTemplateSelector() {
    const select = document.getElementById("team-template-selector");
    if (!select) return;
    const saved = _getSavedTeamTemplates();
    const all = [...BUILTIN_TEAM_TEMPLATES, ...saved];

    let html = '<option value="">Select a template...</option>';
    for (const t of all) {
        const suffix = t.builtin ? '' : ' (saved)';
        html += `<option value="${escapeAttr(t.name)}">${escapeHtml(t.name)} (${t.agents.length} agents)${suffix}</option>`;
    }
    select.innerHTML = html;
}

window._loadTeamTemplateFromSelect = function(name) {
    if (!name) return;
    _loadTeamTemplate(name);
    // Reset dropdown to placeholder after loading
    const select = document.getElementById("team-template-selector");
    if (select) select.value = "";
};

/** Launch a team preset from the welcome screen — opens the team modal pre-filled. */
window._quickLaunchTeam = async function() {
    const templateName = document.getElementById('quick-launch-template').value;
    const boardName = document.getElementById('quick-launch-board-name').value.trim() ||
        document.getElementById('quick-launch-board-name').placeholder;
    const workDir = document.getElementById('quick-launch-workdir').value.trim();

    if (!workDir) { showToast('Working directory is required', 'error'); return; }
    const nameProblem = boardNameProblem(boardName);
    if (nameProblem) { showToast(nameProblem, 'error'); return; }

    const all = [...BUILTIN_TEAM_TEMPLATES, ..._getSavedTeamTemplates()];
    const tmpl = all.find(t => t.name === templateName);
    if (!tmpl) { showToast('Template not found', 'error'); return; }

    // Disable launch button to prevent double-clicks
    const launchBtn = document.querySelector('#quick-launch-modal .btn-primary');
    if (launchBtn) { launchBtn.disabled = true; launchBtn.textContent = 'Launching...'; }

    const server = _effectiveServer(launch.quickServer);
    if (_launchBlocked(server)) { if (launchBtn) { launchBtn.disabled = false; launchBtn.textContent = 'Launch'; } return; }
    const ts = _settingsFor(server);
    const agentType = tmpl.agents?.[0]?.agent_type || ts.default_agent_type || 'claude';
    const defaultPermMode = ts.default_permission_mode || 'bypassPermissions';
    const permFlag = _getPermissionFlagsForAgentMode(agentType, defaultPermMode);
    const flags = permFlag ? permFlag.split(/\s+/) : [];

    try {
        const data = await _launchOn(server, '/api/sessions/launch-team', {
                board_name: boardName,
                working_dir: workDir,
                agent_type: agentType,
                flags,
                agents: tmpl.agents.map(a => {
                    const entry = { name: a.name, prompt: a.prompt, capabilities: a.capabilities };
                    if (a.agent_type) entry.agent_type = a.agent_type;
                    if (a.model) entry.model = a.model;
                    if (a.flags) {
                        entry.flags = typeof a.flags === 'string' ? a.flags.split(/\s+/).filter(Boolean) : a.flags;
                    }
                    if (a.knowledge) entry.knowledge = a.knowledge;
                    return entry;
                }),
                team_knowledge_index: tmpl.team_knowledge_index || '',
                working_mode: tmpl.working_mode || null,
                workflow_presets: tmpl.workflow_presets || null,
        });
        if (!data) return;
        recordRecentDir(workDir, server);
        document.getElementById('quick-launch-modal').style.display = 'none';
        const launched = data.agents || [];
        const failed = launched.filter(a => a.error);
        if (failed.length > 0) {
            showToast(`Launched ${launched.length - failed.length} of ${launched.length} agents (${failed.length} failed)`, 'error');
        } else {
            showToast(`Launched ${templateName} on ${boardName}${_onServerSuffix(server)}`);
            setTimeout(loadLiveSessions, 2000);
            _refreshAfterLaunch(server, { boardName });
        }
    } catch (e) {
        showToast('Launch failed: ' + e.message, 'error');
    } finally {
        if (launchBtn) { launchBtn.disabled = false; launchBtn.textContent = 'Launch'; }
    }
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

let _pendingSaveTemplateAgents = null;
let _pendingSaveTemplateFlags = '';

async function _saveTeamTemplate() {
    // Collect current agents
    const rows = document.querySelectorAll("#team-agents-list .team-agent-row");
    if (rows.length === 0) { showToast("Add agents before saving", "error"); return; }

    const agents = [];
    for (const row of rows) {
        const acfId = row.dataset.acfId;
        const config = acfId ? getAgentConfig(acfId) : {};
        if (config.name) {
            const entry = { name: config.name, prompt: config.prompt };
            if (config.capabilities) entry.capabilities = config.capabilities;
            if (config.agentType) entry.agent_type = config.agentType;
            if (config.model) entry.model = config.model;
            if (config.flags) entry.flags = config.flags;
            agents.push(entry);
        }
    }
    if (agents.length === 0) { showToast("At least one agent needs a name", "error"); return; }

    _pendingSaveTemplateAgents = agents;
    _pendingSaveTemplateFlags = document.getElementById("team-flags")?.value.trim() || '';

    // Show custom modal instead of prompt()
    const modal = document.getElementById('save-template-modal');
    const nameInput = document.getElementById('save-template-name');
    if (modal) {
        modal.style.display = 'flex';
        modal.onclick = (e) => { if (e.target === modal) _hideSaveTemplateModal(); };
    }
    if (nameInput) {
        nameInput.value = document.getElementById('team-board-name')?.value.trim() || '';
        nameInput.focus();
        nameInput.select();
        nameInput.onkeydown = (e) => { if (e.key === 'Enter') _confirmSaveTemplate(); if (e.key === 'Escape') _hideSaveTemplateModal(); };
    }
}
window._saveTeamTemplate = _saveTeamTemplate;

function _hideSaveTemplateModal() {
    const modal = document.getElementById('save-template-modal');
    if (modal) modal.style.display = 'none';
    _pendingSaveTemplateAgents = null;
}
window._hideSaveTemplateModal = _hideSaveTemplateModal;

async function _confirmSaveTemplate() {
    const nameInput = document.getElementById('save-template-name');
    const templateName = nameInput?.value.trim();
    if (!templateName) { showToast("Template name is required", true); nameInput?.focus(); return; }
    if (!_pendingSaveTemplateAgents) return;

    const entry = { name: templateName, agents: _pendingSaveTemplateAgents, flags: _pendingSaveTemplateFlags };
    try { await _upsertSavedTeamTemplates([entry]); } catch { showToast("Could not save the template", true); return; }
    showToast(`Saved template "${templateName}"`);
    _renderTeamTemplateSelector();
    _hideSaveTemplateModal();
}
window._confirmSaveTemplate = _confirmSaveTemplate;

function _truncatePrompt(text, maxLen) {
    if (!text || text.length <= maxLen) return text || "";
    return text.substring(0, maxLen) + "\u2026";
}

function _addTeamAgent(defaultName, defaultPrompt, defaultCapabilities, defaultAgentType, defaultModel, defaultFlags) {
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

    row.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
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
    if (!value) {
        hint.textContent = "This will also be the message board name.";
        hint.classList.remove("error");
        return false;
    }
    const problem = boardNameProblem(value);
    if (problem) {
        hint.textContent = problem;
        hint.classList.add("error");
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
        return;
    }
    if (!_validateTeamName()) {
        showToast(boardNameProblem(boardName) || "Team name must be unique", true);
        return;
    }

    const workingDir = document.getElementById("launch-team-dir").value.trim();
    if (!workingDir) {
        showToast("Working directory is required", true);
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
    if (rows.length === 0) {
        showToast("Add at least one agent", true);
        return;
    }

    const agents = [];
    for (const row of rows) {
        const acfId = row.dataset.acfId;
        const config = acfId ? getAgentConfig(acfId) : {};
        if (!config.name) {
            showToast("Each agent needs a name", true);
            return;
        }
        const agent = { name: config.name, prompt: config.prompt };
        const icon = row.querySelector(".team-agent-icon")?.value.trim() || "";
        if (icon) agent.icon = icon;
        if (config.capabilities) agent.capabilities = config.capabilities;
        if (config.agentType) agent.agent_type = config.agentType;
        if (config.model) agent.model = config.model;
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
        const result = await _launchOn(server, "/api/sessions/launch-team", payload);
        if (result) {
            recordRecentDir(workingDir, server);
            // Check for per-agent errors in the launched array
            const launched = result.agents || result.launched || [];
            const failed = launched.filter(a => a.error);
            if (failed.length > 0) {
                const successCount = launched.length - failed.length;
                const failList = failed.map(a => `<li><strong>${escapeHtml(a.name)}</strong>: ${escapeHtml(a.error)}</li>`).join('');
                const modal = document.createElement('div');
                modal.className = 'modal';
                modal.style.display = 'flex';
                modal.innerHTML = `
                    <div class="modal-content" style="width:500px">
                        <h3>Team Launch — ${failed.length} agent${failed.length > 1 ? 's' : ''} failed</h3>
                        <p style="color:var(--text-secondary);font-size:0.8125rem;margin:8px 0">${successCount} launched successfully, ${failed.length} failed:</p>
                        <ul style="color:var(--error);font-size:0.8125rem;line-height:1.6;margin:8px 0 16px;padding-left:20px">${failList}</ul>
                        <div class="modal-actions">
                            <button class="btn" data-action="settings">Set CLI Paths</button>
                            <button class="btn btn-primary" data-action="close">OK</button>
                        </div>
                    </div>
                `;
                document.body.appendChild(modal);
                modal.addEventListener('click', (e) => {
                    if (e.target.dataset?.action === 'close' || e.target === modal) modal.remove();
                    if (e.target.dataset?.action === 'settings') { modal.remove(); showSettingsModal(); }
                });
            }
            if (launched.length - failed.length > 0) {
                const wtNote = result.worktree_path ? ` in worktree ${result.worktree_path}` : '';
                showToast(`Launched team: ${launched.length - failed.length} agents on "${boardName}"${_onServerSuffix(server)}${wtNote}`);
            }
            hideLaunchModal();
            setTimeout(loadLiveSessions, 2000);
            setTimeout(loadBoardProjects, 4000);
            _refreshAfterLaunch(server, { boardName });
        }
    } catch (e) {
        showToast("Failed to launch team", true);
    } finally {
        if (launchBtn) { launchBtn.disabled = false; launchBtn.textContent = 'Launch Team'; }
    }
}
window.launchTeam = launchTeam;


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
