import { importOnce } from './import_once.js';
import { closeOnBackdropAndEscape, releaseEscape } from './dialog_close.js';
import { checkAgentCLI } from './prerequisites.js';
/* Modal management: launch and info dialogs */

import { state } from './state.js';
import { showToast, escapeHtml, escapeAttr, copyText, boardNameProblem, boardNameSlug } from './utils.js';
import { blockedReason } from './hub_access.js';
import { registerActions, tokenOf, textOf } from './actions.js';
import { loadLiveSessions } from './api.js';
import { refreshChangedFiles } from './changed_files.js';
import { loadBoardProjects } from './message_board.js';
import { getEngineNames, getEngineName, setRendererOverride } from './renderers.js';
import { renderCaptureText, syncPaneWidth } from './capture.js';
import { hideRestartModal } from './controls.js';
import { recordRecentDir, setDirServerResolver } from './browser.js';
import { updateTerminalTheme, getTerminal } from './xterm_renderer.js';
import { desktopNotifySupported, desktopNotifyEnabled, enableDesktopNotify, disableDesktopNotify } from './desktop_notify.js';
import { applyCloudSettingsDialog, filterSettingsPayload } from './cloud_settings.js';
import { serverFetch, currentServer, splitKey, identityKey, isHub, isCloud, normServer, LOCAL_SERVER, keyLabel, noteBoardLabels } from './server_base.js';
import { chooseBoard, retryPayload, isBoardNameExists } from './board_choice.js';
import {
    pickerVisible, pickerOptions, serverLabel, launchBlockReason, coerceSelection,
    bareBoardName, postLaunch, waitForLaunched,
} from './launch_server.js';
import { NOTHING_TO_SAVE } from './settings_diff.js';
import { settingBool, changedSettings, renderRemoteAccessStatus, renderDebugInputLogStatus, applyTheme, clearCustomThemeVars, applyCustomThemeByName } from './settings_core.js';
import { pollEvery } from './overload.js';
import { attachDialogFocus } from './dialog_focus.js';
import { browserLocal } from './safe_storage.js';
import { ensureBuilt, onBuilt } from './lazy_dom.js';
import { openSettingsGroup } from './settings_nav.js';

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
// Kept on modals.js's surface (app.js and others import them from here).
export { loadSettings, applyTheme } from './settings_core.js';

export function toggleFlag(inputId, flag) {
    const input = document.getElementById(inputId);
    const current = input.value.trim();
    const flags = current ? current.split(/\s+/) : [];
    const idx = flags.indexOf(flag);
    if (idx >= 0) {
        flags.splice(idx, 1);
    } else {
        flags.push(flag);
    }
    input.value = flags.join(" ");
    // Update button active states
    input.dispatchEvent(new Event("input"));
}

function _setPermFlagsForInput(inputId, agentType, mode) {
    const input = document.getElementById(inputId);
    if (!input) return;
    input.value = _normalizePermFlagsForAgent(input.value, agentType, mode);
    input.dispatchEvent(new Event("input"));
}
window._setPermFlagsForInput = _setPermFlagsForInput;



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

// ── Settings: API key (#3152) ─────────────────────────────────────────────
// Masked at full width with Show and Copy; a status message (not configured,
// not available) is shown as plain text with the controls off.

function _setApiKey(input, key, message) {
    const show = document.getElementById('settings-api-key-show');
    const copy = document.getElementById('settings-api-key-copy');
    input.value = key || message;
    input.type = key ? 'password' : 'text';
    if (show) { show.hidden = !key; show.textContent = 'Show'; show.setAttribute('aria-pressed', 'false'); }
    if (copy) copy.disabled = !key;
}

function _wireApiKeyControls(input) {
    if (input.dataset.wired) return;
    input.dataset.wired = '1';
    const show = document.getElementById('settings-api-key-show');
    const copy = document.getElementById('settings-api-key-copy');
    show?.addEventListener('click', () => {
        const reveal = input.type === 'password';
        input.type = reveal ? 'text' : 'password';
        show.textContent = reveal ? 'Hide' : 'Show';
        show.setAttribute('aria-pressed', String(reveal));
    });
    copy?.addEventListener('click', async () => {
        const ok = await copyText(input.value);
        copy.textContent = ok ? 'Copied!' : 'Copy failed';
        setTimeout(() => { copy.textContent = 'Copy'; }, 1500);
    });
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


/** Verify a CLI path by calling the backend check endpoint. */
async function _checkOneCLI(inputId, resultId) {
    const input = document.getElementById(inputId);
    const resultEl = document.getElementById(resultId);
    if (!input || !resultEl) return;

    const binary = input.value.trim() || input.placeholder;
    resultEl.textContent = '...';
    resultEl.className = 'cli-verify-result';

    try {
        // The type lets the server accept a path to that CLI's binary (#3583).
        const type = inputId.replace('settings-cli-path-', '');
        const resp = await fetch(`/api/system/cli-check?binary=${encodeURIComponent(binary)}&type=${encodeURIComponent(type)}`);
        const data = await resp.json();
        if (!resp.ok) {
            resultEl.textContent = 'Not allowed';
            resultEl.title = data.error || '';
            resultEl.classList.add('cli-verify-fail');
        } else if (data.found) {
            resultEl.textContent = data.version || 'Found';
            resultEl.classList.add('cli-verify-ok');
        } else {
            resultEl.textContent = 'Not found';
            resultEl.classList.add('cli-verify-fail');
        }
    } catch {
        resultEl.textContent = 'Error';
        resultEl.classList.add('cli-verify-fail');
    }
}

window._verifyAllCLIs = async function() {
    const btn = document.getElementById('verify-all-btn');
    if (btn) { btn.disabled = true; btn.textContent = 'Checking...'; }
    await Promise.all([
        _checkOneCLI('settings-cli-path-claude', 'cli-result-claude'),
        _checkOneCLI('settings-cli-path-codex', 'cli-result-codex'),
        _checkOneCLI('settings-cli-path-agy', 'cli-result-agy'),
        _checkOneCLI('settings-cli-path-pi', 'cli-result-pi'),
    ]);
    if (btn) { btn.disabled = false; btn.textContent = 'Verify All'; }
};

function _showCLINotFoundModal(agentType, server = LOCAL_SERVER) {
    const info = CLI_INSTALL_INSTRUCTIONS[agentType] || { name: agentType, cmd: `Install ${agentType} CLI` };
    const remote = normServer(server) !== LOCAL_SERVER;
    const where = remote ? `on ${escapeHtml(serverLabel(server, state.servers))}` : 'on this system';
    const modal = document.createElement('div');
    modal.className = 'modal';
    modal.style.display = 'flex';
    modal.innerHTML = `
        <div class="modal-content" style="width:480px">
            <h3>${info.name} CLI Not Found</h3>
            <p style="color:var(--text-secondary);font-size:0.8125rem;line-height:1.5;margin:8px 0 12px">
                The <code>${info.name}</code> command was not found ${where}. Install it there to launch ${info.name} agents.
            </p>
            <div style="background:var(--bg-tertiary);border:1px solid var(--border);border-radius:6px;padding:10px 14px;font-family:var(--font-mono);font-size:0.8125rem;display:flex;align-items:center;gap:8px">
                <code style="flex:1;user-select:all">${escapeHtml(info.cmd)}</code>
                <button class="btn btn-small" data-on-click="info-copy" data-cmd="${escapeAttr(info.cmd)}">Copy</button>
            </div>
            ${/^https:\/\/[^\s"'<>]+$/.test(info.url || '') ? `<p style="font-size:0.8125rem;margin:10px 0 0">Or follow the official install page:<br><a href="${escapeAttr(info.url)}" target="_blank" rel="noopener noreferrer" style="color:var(--accent);overflow-wrap:anywhere">${escapeHtml(info.url.replace(/^https:\/\//, ''))}</a></p>` : ''}
            ${info.note ? `<p style="color:var(--text-secondary);font-size:0.75rem;line-height:1.5;margin:8px 0 0">${escapeHtml(info.note)}</p>` : ''}
            <div class="modal-actions" style="margin-top:16px">
                ${remote ? '' : '<button class="btn" data-action="settings">Set CLI Path</button>'}
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

function _checkPermissionFlag(flagsStr, agentType, server = LOCAL_SERVER) {
    const mode = _settingsFor(server).default_permission_mode || 'bypassPermissions';
    const permFlag = _getPermissionFlagsForAgentMode(agentType, mode);
    if (!permFlag) return Promise.resolve('skip');
    return new Promise((resolve) => {
        if (flagsStr && _hasPermFlag(flagsStr)) {
            resolve('skip');
            return;
        }
        const modal = document.createElement('div');
        modal.className = 'modal';
        modal.style.display = 'flex';
        modal.innerHTML = `
            <div class="modal-content" style="width:460px">
                <h3>Permission Prompt Warning</h3>
                <p style="color:var(--text-secondary);font-size:0.8125rem;line-height:1.5;margin:8px 0 16px">
                    Agents may get stuck on permission prompts without <code>${escapeHtml(permFlag)}</code>. Would you like to enable it?
                </p>
                <div class="modal-actions" style="gap:8px">
                    <button class="btn" data-action="cancel">Cancel</button>
                    <button class="btn" data-action="skip">Launch Without</button>
                    <button class="btn btn-primary" data-action="enable">Enable &amp; Launch</button>
                </div>
            </div>
        `;
        document.body.appendChild(modal);
        modal.addEventListener('click', (e) => {
            const action = e.target.dataset?.action;
            if (action) {
                modal.remove();
                resolve(action === 'cancel' ? null : action);
            } else if (e.target === modal) {
                modal.remove();
                resolve(null);
            }
        });
    });
}

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

export async function showInfoModal() {
    if (!state.currentSession || state.currentSession.type !== "live") {
        showToast("No live session selected", true);
        return;
    }

    const name = state.currentSession.name;
    const agentType = state.currentSession.agent_type || "";

    try {
        const params = new URLSearchParams();
        if (agentType) params.set("agent_type", agentType);
        const sid = state.currentSession.session_id;
        if (sid) params.set("session_id", sid);
        const qs = params.toString() ? `?${params}` : "";
        const resp = await serverFetch(currentServer(), `/api/sessions/live/${encodeURIComponent(name)}/info${qs}`);
        const info = await resp.json();

        if (info.error) {
            showToast(info.error, true);
            return;
        }

        document.getElementById("info-agent-name").textContent = info.agent_name || "";
        document.getElementById("info-agent-type").textContent = info.agent_type || "";
        document.getElementById("info-tmux-session").textContent = info.tmux_session_name || "";
        document.getElementById("info-tmux-command").textContent = info.tmux_command || "";
        document.getElementById("info-working-dir").textContent = info.working_directory || "";
        document.getElementById("info-log-path").textContent = info.log_path || "";
        document.getElementById("info-pane-title").textContent = info.pane_title || "";
        const gitDisplay = info.git_repo && info.git_branch ? `${info.git_repo} : ${info.git_branch}` : (info.git_branch || "—");
        document.getElementById("info-git-branch").textContent = gitDisplay;
        const commitHash = info.git_commit_hash ? info.git_commit_hash.substring(0, 8) : "";
        const commitSubject = info.git_commit_subject || "";
        document.getElementById("info-git-commit").textContent = commitHash ? `${commitHash} ${commitSubject}` : "—";

        // Prompt
        const promptLabel = document.getElementById("info-prompt-label");
        const promptVal = document.getElementById("info-prompt");
        if (info.prompt) {
            promptLabel.style.display = "";
            promptVal.style.display = "";
            promptVal.textContent = info.prompt;
        } else {
            promptLabel.style.display = "none";
            promptVal.style.display = "none";
        }

        // Message Board (clickable link)
        const boardLabel = document.getElementById("info-board-label");
        const boardVal = document.getElementById("info-board");
        if (info.board_name) {
            boardLabel.style.display = "";
            boardVal.style.display = "";
            const boardLink = document.createElement("a");
            boardLink.href = "#";
            boardLink.textContent = info.board_label || keyLabel(identityKey(currentServer(), info.board_name));
            boardLink.style.cssText = "color:var(--accent);cursor:pointer;text-decoration:underline";
            boardLink.onclick = (e) => {
                e.preventDefault();
                hideInfoModal();
                if (window.selectBoardProject) window.selectBoardProject(identityKey(currentServer(), info.board_name));
            };
            boardVal.innerHTML = "";
            boardVal.appendChild(boardLink);
        } else {
            boardLabel.style.display = "none";
            boardVal.style.display = "none";
        }

        document.getElementById("info-modal").style.display = "flex";
    } catch (e) {
        showToast("Failed to load session info", true);
        console.error("showInfoModal exception:", e);
    }
}

export function hideInfoModal() {
    document.getElementById("info-modal").style.display = "none";
}

export function copyInfoCommand() {
    const text = document.getElementById("info-tmux-command").textContent;
    navigator.clipboard.writeText(text).then(() => {
        showToast("Copied to clipboard");
    }).catch(() => {
        showToast("Failed to copy", true);
    });
}

// ── Settings Modal ────────────────────────────────────────────────────────



function renderDatabaseEncryptionStatus(status, loadError = false) {
    const select = document.getElementById('settings-database-encryption');
    const hint = document.getElementById('settings-database-encryption-status');
    if (!select || !hint) return;
    if (loadError || !status) {
        select.disabled = true;
        hint.textContent = 'Database encryption settings are unavailable in this server build.';
        return;
    }
    const mode = status.saved_mode || status.effective_mode || 'disabled';
    select.value = mode;
    select.dataset.shown = mode; // what Save compares with, to tell a changed choice from none
    select.disabled = status.feature_available === false;
    const effective = status.effective_mode || 'disabled';
    const restart = status.restart_required === true;
    const surface = status.unlock_surface || 'unknown';
    const capability = status.encryption_compiled === false
        ? ' Experimental database encryption is not included in this standard build.'
        : status.feature_available === false ? ' Encryption is unavailable in this build.' : ' Database encryption is experimental.';
    hint.textContent = `Saved: ${mode}; running: ${effective}${restart ? '; restart required' : ''}. Unlock: ${surface}.${capability}`;
}

async function loadDatabaseEncryptionStatus() {
    try {
        const resp = await fetch('/api/system/database-security');
        if (!resp.ok) throw new Error(`database security ${resp.status}`);
        const status = await resp.json();
        renderDatabaseEncryptionStatus(status);
        return status;
    } catch (_) {
        renderDatabaseEncryptionStatus(null, true);
        return null;
    }
}

// True when the database encryption choice differs from what the dialog showed.
function databaseEncryptionChanged() {
    const select = document.getElementById('settings-database-encryption');
    return Boolean(select) && !select.disabled && select.dataset.shown !== undefined && select.value !== select.dataset.shown;
}

async function saveDatabaseEncryptionSettings() {
    const select = document.getElementById('settings-database-encryption');
    if (!select || select.disabled) return true;
    const mode = select.value;
    const resp = await fetch('/api/system/database-security', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ mode }),
    });
    if (!resp.ok) throw new Error(`database security save failed (${resp.status})`);
    const status = await resp.json();
    renderDatabaseEncryptionStatus(status);
    return true;
}





// ── Theme ────────────────────────────────────────────────────────────────






// ── Artifact store (#3553) ─────────────────────────────────────────────
const GIB = 1024 ** 3;
function _gib(n) {
    n = Number(n) || 0;
    if (n >= GIB / 10) return `${(n / GIB).toFixed(1)} GiB`;
    if (n >= 1024 ** 2) return `${Math.round(n / (1024 ** 2))} MiB`;
    if (n >= 1024) return `${Math.round(n / 1024)} KiB`;
    return `${n} B`;
}

/** Settings: the artifact store's size and the saved diffs' sizes, as separate lines, next to the
 *  store limit (which counts the artifacts only). This computer's dashboard only. */
async function loadArtifactStoreSize({ fresh = false } = {}) {
    const out = document.getElementById('settings-artifact-store-size');
    const input = document.getElementById('settings-artifact-store-max');
    if (!out || !input) return;
    const rin = document.getElementById('settings-received-max');
    const rout = document.getElementById('settings-received-size');
    if (fresh) {
        // Each open starts from nothing: the numbers of the last open must not outlive it. A failed or slow load then leaves the fields empty and off (a limit that was not
        // read is never saved back: the flag is gone), and the lines say that it is loading, not what it showed last time (#4469, UX's #4456 SHOULD 9).
        for (const [i, o] of [[input, out], [rin, rout]]) {
            if (!i || !o) continue;
            i.value = ''; delete i.dataset.loaded; i.disabled = true;
            o.textContent = 'Loading…'; delete o.dataset.state;
        }
        const ub = document.getElementById('settings-received-unlisted'); if (ub) ub.hidden = true;
    }
    try {
        const r = await fetch('/api/system/artifact-store');
        if (!r.ok) throw new Error(String(r.status));
        const d = await r.json();
        input.value = d.max_bytes > 0 ? String(Math.round(d.max_bytes / GIB)) : '0';
        input.dataset.loaded = '1'; // only a limit read from Coral is saved back
        input.disabled = false;
        const plural = (n, one, many) => `${Number(n).toLocaleString()} ${Number(n) === 1 ? one : many}`;
        // The same three states as the received files' line: below 80%, from 80% with what to do, and at the limit that new uploads are refused, in the warning colour.
        let art = `Artifacts: ${_gib(d.bytes)} in ${plural(d.objects, 'file', 'files')}`;
        const atLimit = d.max_bytes > 0 && d.bytes >= d.max_bytes;
        if (d.max_bytes > 0) {
            const full = atLimit || d.bytes * 5 >= d.max_bytes * 4;
            art += atLimit ? ` (limit ${_gib(d.max_bytes)} reached: new uploads are refused)` : full ? ` (limit ${_gib(d.max_bytes)}, ${Math.floor((d.bytes / d.max_bytes) * 100)}% full)` : ` (limit ${_gib(d.max_bytes)})`;
            if (full) art += '. Delete artifacts in Team Artifacts to make room, or raise the limit.';
            // Coral raised an unset limit by itself when the 20 GiB default arrived (#4602): the Operator should know it, and can change it here.
            if (d.auto_raised > 0 && d.auto_raised === d.max_bytes) art += `${full ? ' ' : '. '}The limit was raised to ${_gib(d.auto_raised)} automatically when the 20 GiB default arrived, because the store already held more than 16 GiB. Change it here.`;
        }
        const artNode = document.createElement('span');
        artNode.textContent = art;
        if (atLimit) artNode.dataset.state = 'warn';
        const rest = [
            `Saved task diffs: ${_gib(d.task_diffs?.bytes || 0)} in ${plural(d.task_diffs?.files || 0, 'file', 'files')} (not limited)`,
            `Saved session diffs: ${_gib(d.session_diffs?.bytes || 0)} in ${plural(d.session_diffs?.files || 0, 'file', 'files')} (not limited)`,
        ];
        out.replaceChildren(artNode, ...rest.flatMap(l => [document.createElement('br'), l]));
        showReceivedCap(d.received);
    } catch {
        if (fresh) {
            for (const i of [input, rin]) if (i) { i.value = ''; delete i.dataset.loaded; } // nothing typed meanwhile is kept: the field is off and saves nothing
            out.textContent = "Couldn't read the store's size, so the limit is not shown.";
            if (rout) rout.textContent = "Couldn't read the received files' size, so their limit is not shown.";
        } else out.textContent = "Couldn't read the store's size.";
    }
    loadStorageDetail();
    importOnce('./received_cap_notice.js').then(m => m.checkReceivedCap({ inSettings: true })).catch(() => {}); // remembered once at 80%; the line says it, no toast over this dialog (#4441)
}

// ── Terminal logs of ended agents (#4447) ─────────────────────────────
let _endedLogs = null; // { count, bytes } as last shown: what a confirmation is for

/** Settings: how many terminal logs there are and how many belong to ended agents. This computer's dashboard only. */
async function loadTerminalLogs() {
    const out = document.getElementById('settings-terminal-logs-status');
    const button = document.getElementById('settings-terminal-logs-delete');
    if (!out || !button) return;
    try {
        const r = await fetch('/api/system/terminal-logs');
        if (!r.ok) throw new Error(String(r.status));
        const d = await r.json();
        const n = Number(d.ended?.count) || 0;
        _endedLogs = { count: n, bytes: Number(d.ended?.bytes) || 0 };
        const files = `${Number(d.files).toLocaleString()} terminal log${d.files === 1 ? '' : 's'}`;
        out.textContent = n
            ? `${files}, ${_gib(d.bytes)}. ${n.toLocaleString()} ${n === 1 ? 'belongs' : 'belong'} to ended agents (${_gib(_endedLogs.bytes)}).`
            : `${files}, ${_gib(d.bytes)}. None belong to ended agents.`;
        button.hidden = n === 0;
        if (n) button.textContent = `Delete ${n.toLocaleString()} terminal log${n === 1 ? '' : 's'} of ended agents (${_gib(_endedLogs.bytes)})`;
        if (!button.dataset.wired) { button.dataset.wired = '1'; button.addEventListener('click', confirmDeleteEndedLogs); }
    } catch {
        _endedLogs = null;
        button.hidden = true;
        out.textContent = "Couldn't read the terminal logs.";
    }
}

function confirmDeleteEndedLogs() {
    const shown = _endedLogs;
    if (!shown || !shown.count) return;
    const n = shown.count;
    window.showConfirmModal?.('Delete terminal logs',
        `Delete ${n.toLocaleString()} terminal log${n === 1 ? '' : 's'} of ended agents (${_gib(shown.bytes)})? What ${n === 1 ? 'that agent\'s terminal' : 'those agents\' terminals'} showed can't be recovered. The logs of live, sleeping and archived agents are kept.`,
        async () => {
            try {
                const r = await fetch('/api/system/terminal-logs/delete-ended', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(shown) });
                const d = await r.json().catch(() => ({}));
                if (r.status === 409) {
                    showToast('The logs of ended agents changed since you looked, so nothing was deleted. Look at the new numbers and try again.', true, 8000);
                } else if (!r.ok) {
                    throw new Error(d.error || `HTTP ${r.status}`);
                } else {
                    const left = (d.failed || []).length;
                    showToast(`Deleted ${Number(d.deleted || 0).toLocaleString()} terminal log${d.deleted === 1 ? '' : 's'}, ${_gib(d.bytes)} freed.${left ? ` ${left} could not be deleted or are not ended any more.` : ''}`, false, 8000);
                }
            } catch (e) {
                showToast(`Couldn't delete the terminal logs: ${e.message}`, true);
            }
            loadTerminalLogs();
        });
    requestAnimationFrame(() => document.querySelector('#confirm-modal .modal-actions .btn:not(#confirm-modal-yes)')?.focus());
}

// ── Free space and automatic cleanup (#3554) ──────────────────────────
const PRUNE_KINDS = { diffs: ['diffs'], objects: ['objects'], all: ['all'] };

function _lines(el, lines) {
    el.replaceChildren(...lines.flatMap((l, i) => (i ? [document.createElement('br'), l] : [l])));
}

/** The store's breakdown (by age, by board, kept) and automatic cleanup's state. */
async function loadStorageDetail() {
    const out = document.getElementById('settings-storage-detail');
    const days = document.getElementById('settings-retention-days');
    const kind = document.getElementById('settings-retention-kind');
    if (!out || !days) return;
    try {
        const r = await fetch('/api/system/artifact-store?detail=1');
        if (!r.ok) throw new Error(String(r.status));
        const d = (await r.json()).detail;
        if (!d) throw new Error('no detail');
        const age = (k) => ['<1d', '1-7d', '7-30d', '>30d'].map(b => `${b} ${_gib(d.by_age?.[k]?.[b]?.bytes || 0)}`).join(' · ');
        const lines = [
            `Artifacts by age: ${age('objects')}`,
            `Task diffs by age: ${age('task_diffs')}`,
        ];
        const boards = (d.by_board || []).slice(0, 5).map(b => `${b.label || b.board} ${_gib(b.bytes)}`);
        if (boards.length) lines.push(`Largest boards: ${boards.join(' · ')}`);
        if (d.kept?.count) lines.push(`Kept: ${d.kept.count} artifact${d.kept.count === 1 ? '' : 's'}, ${_gib(d.kept.bytes)}`);
        _lines(out, lines);
        const ret = d.retention || {};
        days.value = String(Number(ret.days) || 0);
        days.dataset.loaded = '1';
        kind.value = ['diffs', 'objects', 'all'].includes(ret.kinds) ? ret.kinds : 'diffs';
        showRetentionStatus(ret);
    } catch {
        out.textContent = '';
    }
}

function showRetentionStatus(ret) {
    const status = document.getElementById('settings-retention-status');
    const review = document.getElementById('settings-retention-review');
    if (!status || !review) return;
    const days = Number(ret.days) || 0;
    const enabled = ret.enabled_at ? new Date(ret.enabled_at) : null;
    const acked = ret.acknowledged_at ? new Date(ret.acknowledged_at) : null;
    const waiting = days > 0 && enabled && !(acked && acked >= enabled) && Date.now() - enabled < 24 * 3600e3;
    review.hidden = !waiting;
    const lines = [];
    if (!days) lines.push('Off.');
    else if (waiting) lines.push(`On (${days} days), waiting: review what it would do to start now, or it starts on ${new Date(enabled.getTime() + 24 * 3600e3).toLocaleString()}.`);
    else if (days && !enabled) lines.push(`On (${days} days): its first check is within the hour; it then waits for your review.`);
    else lines.push(`On (${days} days).`);
    if (ret.last?.at) lines.push(`Last run ${new Date(ret.last.at).toLocaleString()}: freed ${_gib(ret.last.freed)}${ret.last.capped ? ' (stopped at the 2 GiB cap)' : ''}.`);
    _lines(status, lines);
}

/** Settings' value for automatic cleanup ({} when not read from Coral). */
function retentionSettings() {
    const days = document.getElementById('settings-retention-days');
    const kind = document.getElementById('settings-retention-kind');
    if (!days || isCloud() || days.dataset.loaded !== '1') return {};
    const n = Math.max(0, Math.min(3650, Math.floor(Number(days.value) || 0)));
    return { artifact_retention_days: String(n), artifact_retention_kinds: kind?.value || 'diffs' };
}

async function pruneRequest(body) {
    const r = await fetch('/api/artifacts/prune', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`);
    return d;
}

// The page's own words for what a prune removes, never the server's kind names (#4469, UX's #4456 SHOULD 11).
const PRUNE_NAMES = { diffs: ['saved diff', 'saved diffs'], objects: ['unlinked artifact', 'unlinked artifacts'], 'diff-objects': ['stored diff copy', 'stored diff copies'] };
function pruneSummary(rep) {
    const n = Object.values(rep.kinds || {}).reduce((a, k) => a + (k.count || 0), 0);
    return { n, text: Object.entries(rep.kinds || {}).map(([k, v]) => `${Number(v.count).toLocaleString()} ${(PRUNE_NAMES[k] || [k, k])[v.count === 1 ? 0 : 1]} (${_gib(v.bytes)})`).join(', ') };
}

/** A dry run, a confirmation (focus on Cancel), then the real run. */
async function previewAndRun(body, verb) {
    const result = document.getElementById('settings-storage-result');
    result.removeAttribute('data-state');
    result.textContent = 'Checking…';
    let dry;
    try {
        dry = await pruneRequest({ ...body, dry_run: true });
    } catch (e) {
        result.dataset.state = 'error';
        result.textContent = `Couldn't check: ${e.message}`;
        return;
    }
    const s = pruneSummary(dry);
    if (!s.n) {
        result.textContent = 'Nothing to do.';
        return;
    }
    result.textContent = '';
    const what = verb === 'Shrink' ? `saving about ${_gib(dry.freed)}. The code changes in them stay.` : `freeing about ${_gib(dry.freed)}. This can't be undone.`;
    const asker = document.getElementById(verb === 'Shrink' ? 'settings-storage-shrink' : 'settings-storage-prune');
    window.showConfirmModal?.(`${verb} saved data`, `${verb} ${s.text}, ${what}`, async () => {
        result.textContent = `${verb === 'Shrink' ? 'Shrinking' : 'Pruning'}…`;
        try {
            const done = await pruneRequest({ ...body, dry_run: false });
            result.textContent = `${verb === 'Shrink' ? 'Saved' : 'Freed'} ${_gib(done.freed)} from ${Number(done.removed).toLocaleString()} item${done.removed === 1 ? '' : 's'}${done.skipped ? `; ${done.skipped} left alone because ${done.skipped === 1 ? 'it' : 'they'} changed` : ''}${done.errors ? `; ${done.errors} error${done.errors === 1 ? '' : 's'}` : ''}.`;
            loadArtifactStoreSize();
        } catch (e) {
            result.dataset.state = 'error';
            result.textContent = `Couldn't finish: ${e.message}`;
        }
        asker?.focus(); // Yes closed the confirmation: the focus goes back to the button that asked, not to the page (#4469, UX's #4456 SHOULD 10)
    });
    // No default: focus starts on Cancel.
    requestAnimationFrame(() => document.querySelector('#confirm-modal .modal-actions .btn:not(#confirm-modal-yes)')?.focus());
}

function wireStorageActions() {
    const shrink = document.getElementById('settings-storage-shrink');
    if (!shrink || shrink.dataset.wired) return;
    shrink.dataset.wired = '1';
    shrink.addEventListener('click', () => previewAndRun({ shrink: true }, 'Shrink'));
    document.getElementById('settings-storage-prune')?.addEventListener('click', () => {
        const days = Math.max(1, Math.floor(Number(document.getElementById('settings-prune-days').value) || 14));
        const kind = document.getElementById('settings-prune-kind').value;
        previewAndRun({ older_than_seconds: days * 86400, kinds: PRUNE_KINDS[kind] || ['diffs'] }, 'Prune');
    });
    document.getElementById('settings-retention-review')?.addEventListener('click', async () => {
        const status = document.getElementById('settings-retention-status');
        const days = Number(document.getElementById('settings-retention-days').value) || 0;
        const kind = document.getElementById('settings-retention-kind').value;
        if (!days) return;
        let dry;
        try {
            dry = await pruneRequest({ older_than_seconds: days * 86400, kinds: PRUNE_KINDS[kind] || ['diffs'], dry_run: true });
        } catch (e) {
            status.dataset.state = 'error';
            status.textContent = `Couldn't check: ${e.message}`;
            return;
        }
        const s = pruneSummary(dry);
        window.showConfirmModal?.('Start automatic cleanup',
            `Its first run would remove ${s.n ? `${s.text}, about ${_gib(dry.freed)}` : 'nothing right now'} (at most 2 GiB a run), then check every hour. Start it?`,
            async () => {
                const r = await fetch('/api/artifacts/retention/ack', { method: 'POST' });
                if (!r.ok) {
                    status.dataset.state = 'error';
                    status.textContent = "Couldn't start it: save the setting first.";
                    return;
                }
                loadStorageDetail();
            });
        requestAnimationFrame(() => document.querySelector('#confirm-modal .modal-actions .btn:not(#confirm-modal-yes)')?.focus());
    });
}

/** Settings: what other teams and Corals have sent this computer, against the limit on it (#4441). One status line says the total, how full it is (from
 *  80% on, with what to do), that new files are refused at the limit (in the warning colour), and how many received files no board lists. */
function receivedLine(r) {
    const files = `${Number(r.objects).toLocaleString()} file${r.objects === 1 ? '' : 's'}`;
    let line = `Received: ${_gib(r.bytes)} in ${files}`;
    let full = false, atLimit = false;
    if (r.max_bytes > 0) {
        const pct = Math.floor((r.bytes / r.max_bytes) * 100);
        atLimit = r.bytes >= r.max_bytes;
        full = atLimit || r.bytes * 5 >= r.max_bytes * 4;
        line += atLimit ? ` (limit ${_gib(r.max_bytes)} reached: new received files are refused)` : full ? ` (limit ${_gib(r.max_bytes)}, ${pct}% full)` : ` (limit ${_gib(r.max_bytes)})`;
    } else line += ' (no limit)';
    if (full) line += '. Delete received files in Team Artifacts to make room, or raise the limit.';
    const u = Number(r.unlisted?.count) || 0;
    if (u) line += `${full ? ' ' : '. '}Not listed on any board: ${u.toLocaleString()} file${u === 1 ? '' : 's'}, ${_gib(r.unlisted.bytes)}. Another Coral sent ${u === 1 ? 'it' : 'them'} but the message that names ${u === 1 ? 'it' : 'them'} never came. ${u === 1 ? 'It counts' : 'They count'} toward the limit.${u > (r.unlisted.items || []).length ? ` Showing the oldest ${(r.unlisted.items || []).length}; delete them, then the rest.` : ''}`;
    return { line, atLimit };
}

function showReceivedCap(r) {
    const out = document.getElementById('settings-received-size');
    const input = document.getElementById('settings-received-max');
    if (!out || !input || !r) return;
    input.value = r.max_bytes > 0 ? String(Math.round(r.max_bytes / GIB)) : '0';
    input.dataset.loaded = '1'; // only a limit read from Coral is saved back
    input.disabled = false;
    const { line, atLimit } = receivedLine(r);
    out.textContent = line;
    if (atLimit) out.dataset.state = 'warn'; else delete out.dataset.state; // at the limit new files are refused: the warning colour
    showUnlistedReceived(r.unlisted);
}

// Received files no board lists: their message never came. They count in the limit; the Operator can delete them, by exact digest, after a
// confirmation. Never automatic (#4441, Security's #4440 INFO 2). The count is in the status line above; the button names what it deletes.
let _unlistedItems = [];
let _unlistedBytes = 0;
function showUnlistedReceived(u) {
    const box = document.getElementById('settings-received-unlisted');
    const button = document.getElementById('settings-received-unlisted-delete');
    if (!box || !button) return;
    const count = Number(u?.count) || 0;
    _unlistedItems = Array.isArray(u?.items) ? u.items.map(i => i.id) : [];
    _unlistedBytes = (u?.items || []).reduce((sum, i) => sum + (Number(i.size) || 0), 0);
    box.hidden = _unlistedItems.length === 0;
    if (!_unlistedItems.length) return;
    const n = _unlistedItems.length;
    button.textContent = `Delete ${n.toLocaleString()} unlisted file${n === 1 ? '' : 's'} (${_gib(_unlistedBytes)})`;
    if (!button.dataset.wired) {
        button.dataset.wired = '1';
        button.addEventListener('click', confirmDeleteUnlisted);
    }
}

function confirmDeleteUnlisted() {
    const ids = _unlistedItems.slice();
    if (!ids.length) return;
    const n = ids.length;
    window.showConfirmModal?.('Delete received files',
        `Delete ${n.toLocaleString()} unlisted file${n === 1 ? '' : 's'} (${_gib(_unlistedBytes)})? Only ${n === 1 ? 'this file is' : 'these files are'} removed from Coral and can't be recovered; nothing else is touched, and a file something now lists or links is left alone.`,
        async () => {
            let d;
            try {
                const r = await fetch('/api/artifacts/received/unlisted/delete', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ids }) });
                d = await r.json().catch(() => ({}));
                if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`);
            } catch (e) {
                showToast(`Couldn't delete the received files: ${e.message}`, true);
                return;
            }
            const left = (d.skipped || []).length;
            showToast(`Deleted ${Number(d.deleted || 0).toLocaleString()} received file${d.deleted === 1 ? '' : 's'}, ${_gib(d.bytes)} freed.${left ? ` ${left} left alone: something lists or links ${left === 1 ? 'it' : 'them'}.` : ''}`, false, 8000);
            loadArtifactStoreSize();
        });
    requestAnimationFrame(() => document.querySelector('#confirm-modal .modal-actions .btn:not(#confirm-modal-yes)')?.focus());
}

/** The received-files limit to save, in bytes ({} when the field isn't on this page or was never read). */
function receivedMaxSetting() {
    const input = document.getElementById('settings-received-max');
    if (!input || isCloud() || input.dataset.loaded !== '1') return {};
    if (String(input.value).trim() === '') return {}; // an emptied field is unchanged, never 0 (no limit) saved by accident
    const gib = Math.max(0, Math.floor(Number(input.value) || 0));
    return { received_artifacts_max_bytes: String(gib * GIB) };
}

/** The limit to save, in bytes ({} when the field isn't on this page). */
function artifactStoreMaxSetting() {
    const input = document.getElementById('settings-artifact-store-max');
    // Not on this page, or never read from Coral (a failed load must not reset a limit to 0).
    if (!input || isCloud() || input.dataset.loaded !== '1') return {};
    if (String(input.value).trim() === '') return {}; // an emptied field is unchanged, never 0 (no limit) saved by accident
    const gib = Math.max(0, Math.floor(Number(input.value) || 0));
    return { artifact_store_max_bytes: String(gib * GIB) };
}

// The settings the dialog opened with: Save sends only the fields that differ from them (settings_diff.js).
let _settingsAtOpen = null;

// The CLI path fields, as the server has them now. The dialog opens at once from the settings this page loaded, which a
// CLI path saved from another tab or client has outdated: a moment later the fields the person has not touched show the
// server's value, and it becomes what Save compares with (so a path the person never edited is never written back).
const CLI_PATH_FIELDS = { cli_path_claude: 'settings-cli-path-claude', cli_path_codex: 'settings-cli-path-codex', cli_path_pi: 'settings-cli-path-pi', cli_path_agy: 'settings-cli-path-agy' };

// The CLI path settings whose field the person typed in since the dialog opened.
function editedCLIPathKeys() {
    return Object.entries(CLI_PATH_FIELDS).filter(([, id]) => document.getElementById(id)?.dataset.edited === '1').map(([key]) => key);
}

// dropUnfilledCLIPaths removes from payload the CLI path settings whose field the dialog has not filled and the person has not
// typed in: an empty field that was never filled is not a path cleared on purpose (it would clear the path, v1.4.80 gate).
function dropUnfilledCLIPaths(payload) {
    for (const [key, id] of Object.entries(CLI_PATH_FIELDS)) {
        const el = document.getElementById(id);
        if (!el || el.dataset.filled === '1' || el.dataset.edited === '1') continue;
        delete payload[key];
        if (key === 'cli_path_agy') delete payload.cli_path_gemini; // one field for both
    }
    return payload;
}

function refreshCLIPathFields() {
    if (isCloud()) return; // the CLI path fields are hidden on the hub (machine settings): nothing to refresh
    const { cli_path_agy: _agy, ...fields } = CLI_PATH_FIELDS;
    const opened = _settingsAtOpen;
    const ids = [...Object.values(fields), 'settings-cli-path-agy'];
    // Typing in a field marks it edited, even when what is typed equals what it showed.
    for (const id of ids) {
        const el = document.getElementById(id);
        if (!el) continue;
        el.dataset.edited = '';
        if (!el.dataset.watched) { el.dataset.watched = '1'; el.addEventListener('input', () => { el.dataset.edited = '1'; }); }
    }
    fetch('/api/settings').then(r => r.ok ? r.json() : null).then(data => {
        const fresh = data && data.settings;
        // A different open since (the dialog was closed and opened again) has its own refresh.
        if (!fresh || _settingsAtOpen !== opened) return;
        const update = {};
        for (const [key, id] of Object.entries(fields)) {
            const el = document.getElementById(id);
            if (!el || el.dataset.edited === '1') continue; // the person is editing it: leave it
            const now = fresh[key] || '';
            el.value = now;
            update[key] = now;
        }
        const agy = document.getElementById('settings-cli-path-agy');
        if (agy && agy.dataset.edited !== '1') {
            const now = fresh.cli_path_agy || fresh.cli_path_gemini || '';
            agy.value = now;
            update.cli_path_agy = fresh.cli_path_agy || '';
            update.cli_path_gemini = fresh.cli_path_gemini || '';
        }
        Object.assign(_settingsAtOpen, update);
        state.settings = { ...state.settings, ...update };
    }).catch(() => { /* the dialog keeps what it opened with */ });
}

export async function showSettingsModal(group) {
    // Focus moves in on open, Tab stays inside, and focus comes back on close (accessibility pass).
    attachDialogFocus(document.getElementById('settings-modal'));

    importOnce('./terminal_backend.js').then(m => m.loadTerminalBackendRow()).catch(() => {}); // #3634
    importOnce('./security_settings.js').then(m => m.loadSecurityRow()).catch(() => {}); // #3948
    const s = state.settings || {};
    // What this dialog opens with: Save sends only the fields that end up different from it.
    _settingsAtOpen = { ...s };
    // The CLI path fields are filled later in this function (after awaited requests): until then they are empty, and a Save
    // made in that time (a suite that does not wait for the dialog) must not read them as "cleared".
    for (const id of Object.values(CLI_PATH_FIELDS)) { const el = document.getElementById(id); if (el) el.dataset.filled = ''; }
    // The machine-only parts of this dialog (privacy, remote access, database encryption, connecting to a hub) do not
    // exist on the hub: the cloud dialog shows only the per-user settings (applyCloudSettingsDialog below).
    if (!isCloud()) {
        const telemetry = document.getElementById('settings-telemetry-enabled');
        if (telemetry) telemetry.checked = settingBool(s.telemetry_enabled, true);
        loadArtifactStoreSize({ fresh: true }); // the store's size next to its limit (#3553)
        loadTerminalLogs(); // the terminal logs of ended agents (#4447)
        wireStorageActions(); // free space and automatic cleanup (#3554)
        const autoAttach = document.getElementById('settings-board-auto-attach');
        // Off unless the Operator turned it on (#3563); an empty value is unset.
        if (autoAttach) autoAttach.checked = settingBool(s.board_auto_attach || undefined, false);
        const autoResume = document.getElementById('settings-auto-resume');
        // Off unless the Operator turned it on (#3613).
        if (autoResume) autoResume.checked = settingBool(s.auto_resume_after_api_error || undefined, false);
        const autoGoals = document.getElementById('settings-auto-goals');
        // On unless the Operator turned it off (#4204).
        if (autoGoals) autoGoals.checked = settingBool(s.auto_goals || undefined, true);
        const debugInput = document.getElementById('settings-debug-input-log');
        if (debugInput) debugInput.checked = settingBool(s.debug_input_log, false);
        const remote = document.getElementById('settings-remote-access-enabled');
        if (remote) remote.checked = settingBool(s.remote_access_enabled, false);
        renderRemoteAccessStatus(state.privacyStatus);
        try {
            const privacyResp = await fetch('/api/system/privacy');
            if (!privacyResp.ok) throw new Error(`privacy status ${privacyResp.status}`);
            state.privacyStatus = await privacyResp.json();
            if (remote) remote.checked = settingBool(state.privacyStatus.remote_access_enabled, false);
            renderRemoteAccessStatus(state.privacyStatus);
            renderDebugInputLogStatus(state.privacyStatus);
        } catch (_) {
            renderRemoteAccessStatus(state.privacyStatus, !state.privacyStatus);
        }
        // Bootstrap database-encryption metadata lives outside the locked DB. The
        // endpoint returns status only; passwords/keys are never sent to the UI.
        loadDatabaseEncryptionStatus();
        // Loaded with the dialog, through importOnce so a failed load is retried (#3140).
        importOnce('./cloud_connect.js').then(m => { m.initCloudConnect(); m.loadCloudStatus(); }).catch(() => {});
        importOnce('./hub_mode_setting.js').then(m => m.loadHubModeSetting()).catch(() => {});
    }

    // Theme — populate with built-in options + saved custom themes
    const themeSelect = document.getElementById("settings-theme");
    if (themeSelect) {
        // Fetch saved custom themes
        let customThemes = [];
        try {
            const resp = await fetch("/api/themes");
            const data = await resp.json();
            customThemes = data.themes || [];
        } catch (_) {}

        // Build options: built-in first, then custom
        let options = `<option value="dark">Dark</option>
            <option value="light">Light</option>
            <option value="system">System</option>`;
        if (customThemes.length > 0) {
            options += `<option disabled>──────────</option>`;
            for (const t of customThemes) {
                const name = escapeAttr(t.name);
                const label = escapeHtml(t.name);
                options += `<option value="custom:${escapeAttr(name)}">${label}</option>`;
            }
        }
        themeSelect.innerHTML = options;

        // Set current value
        const currentTheme = s.custom_theme ? `custom:${s.custom_theme}` : (s.theme || "dark");
        themeSelect.value = currentTheme;
    }

    // Default Render Engine
    const currentEngine = s.default_renderer || "xterm";
    const options = getEngineNames()
        .map(name => `<option value="${escapeAttr(name)}"${name === currentEngine ? " selected" : ""}>${escapeHtml(name)}</option>`)
        .join("");
    document.getElementById("settings-renderer-select").innerHTML = options;

    // Default Agent Type
    const currentAgentType = s.default_agent_type || "claude";
    const agentTypeSelect = document.getElementById("settings-agent-type");
    if (agentTypeSelect) agentTypeSelect.value = currentAgentType;

    // Default Permission Mode
    const currentPermMode = s.default_permission_mode || "bypassPermissions";
    const permModeSelect = document.getElementById("settings-permission-mode");
    if (permModeSelect) {
        permModeSelect.value = currentPermMode;
        _updatePermModeDescription(permModeSelect);
    }

    // Default Working Directory
    const dirInput = document.getElementById("settings-working-dir");
    if (dirInput) {
        dirInput.value = s.default_working_dir || "";
        dirInput.placeholder = dirInput.dataset.coralRoot || "/path/to/project";
    }

    // API Key
    const apiKeyInput = document.getElementById("settings-api-key");
    if (apiKeyInput && !isCloud()) {
        _wireApiKeyControls(apiKeyInput);
        fetch('/api/system/api-key').then(r => {
            // No sign-in (the dashboard opened without one, local sign-in off): the server refuses the key. Say how to see it, not "Not configured".
            if (r.status === 401 || r.status === 403) return { signIn: true };
            return r.ok ? r.json() : null;
        }).then(data => {
            _setApiKey(apiKeyInput, data && data.key, data && data.signIn ? 'Sign in to see the API key (run: coral open)' : 'Not configured');
        }).catch(() => _setApiKey(apiKeyInput, '', 'Not available'));
    }

    // CLI Paths
    const cliClaude = document.getElementById("settings-cli-path-claude");
    const cliCodex = document.getElementById("settings-cli-path-codex");
    const cliAgy = document.getElementById("settings-cli-path-agy");
    const cliPi = document.getElementById("settings-cli-path-pi");
    if (cliClaude) cliClaude.value = s.cli_path_claude || "";
    if (cliCodex) cliCodex.value = s.cli_path_codex || "";
    const agyCliPath = s.cli_path_agy || s.cli_path_gemini || "";
    if (cliAgy) cliAgy.value = agyCliPath;
    if (cliPi) cliPi.value = s.cli_path_pi || "";
    for (const id of Object.values(CLI_PATH_FIELDS)) { const el = document.getElementById(id); if (el) el.dataset.filled = '1'; }
    refreshCLIPathFields();

    // Default Models — populate combo inputs + datalists from /api/agent-models
    const defClaude = document.getElementById("settings-default-model-claude");
    const defCodex = document.getElementById("settings-default-model-codex");
    const defAgy = document.getElementById("settings-default-model-agy");
    const defPi = document.getElementById("settings-default-model-pi");
    if (defClaude) defClaude.value = s.default_model_claude || "";
    if (defCodex) defCodex.value = s.default_model_codex || "";
    const agyModel = s.default_model_agy || s.default_model_gemini || "";
    if (defAgy) defAgy.value = agyModel;
    if (defPi) defPi.value = s.default_model_pi || "";
    // On the hub the Default Models rows are hidden (machine settings), and the hub has no model catalog of its own to ask.
    if (!isCloud()) _getAgentModels().then(models => {
        // _getAgentModels swallows errors and resolves with {} on failure; on
        // success it returns the canonical map with an array for each agent type.
        if (!models || !Array.isArray(models.claude)) {
            const notice = document.getElementById("settings-default-models-notice");
            if (notice) notice.textContent = "Could not load model list — you can still type a custom model id.";
            return;
        }
        _fillModelDatalist(document.getElementById("settings-models-claude"), "claude", models);
        _fillModelDatalist(document.getElementById("settings-models-codex"), "codex", models);
        _fillModelDatalist(document.getElementById("settings-models-agy"), "agy", models);
        _fillModelDatalist(document.getElementById("settings-models-pi"), "pi", models);
    });

    // Group by Team
    const groupByTeamCheck = document.getElementById("settings-group-by-team");
    if (groupByTeamCheck) {
        groupByTeamCheck.checked = browserLocal.get('coral-group-by-team') !== 'false';
    }

    // Terminal Font Size
    const fontSizeSelect = document.getElementById("settings-terminal-font-size");
    if (fontSizeSelect) fontSizeSelect.value = s.terminal_font_size || "13";

    // Terminal Scrollback
    const scrollbackSelect = document.getElementById("settings-terminal-scrollback");
    if (scrollbackSelect) scrollbackSelect.value = s.terminal_scrollback || "20000";

    // Fit Pane Width
    const fitPaneCheck = document.getElementById("settings-fit-pane-width");
    if (fitPaneCheck) fitPaneCheck.checked = !!s.fit_pane_width;

    // Notify Needs Input
    const notifyCheck = document.getElementById("settings-notify-needs-input");
    if (notifyCheck) notifyCheck.checked = !!s.notify_needs_input;

    // Desktop alerts: a per-browser opt-in, never sent to the server
    const desktopCheck = document.getElementById("settings-desktop-notify");
    if (desktopCheck) {
        desktopCheck.checked = desktopNotifyEnabled();
        desktopCheck.disabled = !desktopNotifySupported();
        if (!desktopCheck.dataset.bound) {
            desktopCheck.dataset.bound = "1";
            desktopCheck.addEventListener("change", async () => {
                if (desktopCheck.checked) desktopCheck.checked = await enableDesktopNotify();
                else disableDesktopNotify();
            });
        }
    }

    // Check for Updates (stored in localStorage, defaults to on)
    const updateCheck = document.getElementById("settings-check-updates");
    if (updateCheck) updateCheck.checked = browserLocal.get("coral-update-check-enabled") !== "false";

    // Show Scrollbars (defaults to true)
    const scrollbarsCheck = document.getElementById("settings-show-scrollbars");
    if (scrollbarsCheck) scrollbarsCheck.checked = s.show_scrollbars !== false;

    // File Search Mode (defaults to 'directory')
    const fileSearchSelect = document.getElementById("settings-file-search-mode");
    if (fileSearchSelect) fileSearchSelect.value = s.file_search_mode || 'directory';
    const fileSearchLimitSelect = document.getElementById("settings-file-search-limit");
    if (fileSearchLimitSelect) fileSearchLimitSelect.value = s.file_search_limit || "500";

    // Git Diff Mode (defaults to 'previous_commit')
    const gitDiffSelect = document.getElementById("settings-git-diff-mode");
    if (gitDiffSelect) gitDiffSelect.value = s.git_diff_mode || 'previous_commit';

    // Git scan interval, in seconds; "0" means the user scans manually.
    const gitPollSelect = document.getElementById("settings-git-poll-interval");
    if (gitPollSelect) {
        const secs = String(s.git_poll_interval_s ?? '120');
        // An interval set outside the presets (by hand, or by an older build)
        // still needs to show as itself rather than silently snapping.
        if (!Array.from(gitPollSelect.options).some(o => o.value === secs)) {
            const opt = new Option(`Every ${secs} seconds`, secs);
            gitPollSelect.add(opt);
        }
        gitPollSelect.value = secs;
    }

    // Run agents at lower priority (#3437, a checkbox since #3712): on unless set to normal.
    const agentLowPriority = document.getElementById("settings-agent-low-priority");
    if (agentLowPriority) agentLowPriority.checked = s.agent_priority !== 'normal';

    // A partner build's name (Coral has no paid licence, #3221)
    if (!isCloud()) _loadTierBranding();

    // App version
    fetch('/api/system/status').then(r => r.json()).then(d => {
        const el = document.getElementById('app-version-label');
        if (el && d.version) el.textContent = 'Version ' + d.version;
    }).catch(() => {});

    if (isCloud()) applyCloudSettingsDialog();

    openSettingsGroup(typeof group === 'string' ? group : undefined); // the group last used here, or the one asked for
    document.getElementById("settings-modal").style.display = "flex";
}

async function _loadTierBranding() {
    try {
        const resp = await fetch('/api/system/status');
        if (!resp.ok) return;
        const data = await resp.json();
        const branding = document.getElementById('tier-branding-label');
        if (branding && data.tier_name === 'dropboxers') {
            branding.textContent = 'Coral For Dropbox';
            branding.style.display = '';
        }
    } catch { /* non-critical */ }
}

export function hideSettingsModal() {
    document.getElementById("settings-modal").style.display = "none";
}

export async function applySettings() {
    const themeValue = document.getElementById("settings-theme")?.value || "dark";
    const engineName = document.getElementById("settings-renderer-select").value;
    const agentType = document.getElementById("settings-agent-type")?.value || "claude";
    const permissionMode = document.getElementById("settings-permission-mode")?.value || "bypassPermissions";
    const workingDir = document.getElementById("settings-working-dir")?.value.trim() || "";
    const cliPathClaude = document.getElementById("settings-cli-path-claude")?.value.trim() || "";
    const cliPathCodex = document.getElementById("settings-cli-path-codex")?.value.trim() || "";
    const cliPathAgy = document.getElementById("settings-cli-path-agy")?.value.trim() || "";
    const cliPathPi = document.getElementById("settings-cli-path-pi")?.value.trim() || "";
    const defaultModelClaude = document.getElementById("settings-default-model-claude")?.value.trim() || "";
    const defaultModelCodex = document.getElementById("settings-default-model-codex")?.value.trim() || "";
    const defaultModelAgy = document.getElementById("settings-default-model-agy")?.value.trim() || "";
    const defaultModelPi = document.getElementById("settings-default-model-pi")?.value.trim() || "";
    const fitPaneWidth = document.getElementById("settings-fit-pane-width")?.checked || false;
    const notifyNeedsInput = document.getElementById("settings-notify-needs-input")?.checked || false;
    const terminalFontSize = document.getElementById("settings-terminal-font-size")?.value || "13";
    const terminalScrollback = document.getElementById("settings-terminal-scrollback")?.value || "20000";
    const checkUpdates = document.getElementById("settings-check-updates")?.checked ?? true;
    browserLocal.set("coral-update-check-enabled", checkUpdates ? "true" : "false");
    const showScrollbars = document.getElementById("settings-show-scrollbars")?.checked ?? true;
    const fileSearchMode = document.getElementById("settings-file-search-mode")?.value || "directory";
    const fileSearchLimit = document.getElementById("settings-file-search-limit")?.value || "500";
    const gitDiffMode = document.getElementById("settings-git-diff-mode")?.value || "branch_point";
    const gitPollIntervalS = document.getElementById("settings-git-poll-interval")?.value ?? "120";
    // The server takes only "low" or "normal" (#3470): a value stored before that check is sent as low.
    const lowBox = document.getElementById("settings-agent-low-priority");
    const agentPriority = lowBox ? (lowBox.checked ? "low" : "normal") : (state.settings?.agent_priority === "normal" ? "normal" : "low");
    const telemetryEnabled = document.getElementById("settings-telemetry-enabled")?.checked ?? settingBool(state.settings?.telemetry_enabled, true);
    const boardAutoAttach = document.getElementById("settings-board-auto-attach")?.checked ?? settingBool(state.settings?.board_auto_attach || undefined, false);
    const autoResume = document.getElementById("settings-auto-resume")?.checked ?? settingBool(state.settings?.auto_resume_after_api_error || undefined, false);
    const autoGoals = document.getElementById("settings-auto-goals")?.checked ?? settingBool(state.settings?.auto_goals || undefined, true);
    // When CORAL_DEBUG_INPUT/--debug-input force the log on, the checkbox is shown checked and
    // disabled: keep the saved setting rather than saving that forced state (#3256).
    const debugInputBox = document.getElementById("settings-debug-input-log");
    const debugInputLog = debugInputBox && !debugInputBox.disabled ? debugInputBox.checked : settingBool(state.settings?.debug_input_log, false);
    const remoteAccessEnabled = document.getElementById("settings-remote-access-enabled")?.checked ?? settingBool(state.settings?.remote_access_enabled, false);

    // Parse theme selection — "custom:<name>" or built-in "dark"/"light"/"system"
    let theme, customTheme;
    if (themeValue.startsWith("custom:")) {
        customTheme = themeValue.slice(7);
        theme = "dark"; // will be overridden by the custom theme's base
    } else {
        theme = themeValue;
        customTheme = "";
    }

    let payload = {
        theme: theme,
        custom_theme: customTheme,
        default_renderer: engineName,
        default_agent_type: agentType,
        default_permission_mode: permissionMode,
        default_working_dir: workingDir,
        fit_pane_width: fitPaneWidth,
        notify_needs_input: notifyNeedsInput,
        terminal_font_size: terminalFontSize,
        terminal_scrollback: terminalScrollback,
        show_scrollbars: showScrollbars,
        cli_path_claude: cliPathClaude,
        cli_path_codex: cliPathCodex,
        cli_path_agy: cliPathAgy,
        cli_path_gemini: cliPathAgy,
        cli_path_pi: cliPathPi,
        default_model_claude: defaultModelClaude,
        default_model_codex: defaultModelCodex,
        default_model_agy: defaultModelAgy,
        default_model_gemini: defaultModelAgy,
        default_model_pi: defaultModelPi,
        file_search_mode: fileSearchMode,
        file_search_limit: fileSearchLimit,
        git_diff_mode: gitDiffMode,
        git_poll_interval_s: gitPollIntervalS,
        agent_priority: agentPriority,
        telemetry_enabled: telemetryEnabled,
        board_auto_attach: boardAutoAttach,
        auto_resume_after_api_error: autoResume,
        auto_goals: autoGoals,
        ...artifactStoreMaxSetting(),
        ...receivedMaxSetting(),
        ...retentionSettings(),
        debug_input_log: debugInputLog,
        remote_access_enabled: remoteAccessEnabled,
    };

    // Only what the person changed since the dialog opened: a field this dialog never touched (a CLI path another
    // tab saved meanwhile) is not written back with the value this page opened with.
    payload = changedSettings(dropUnfilledCLIPaths(payload), _settingsAtOpen, themeValue, editedCLIPathKeys());
    // The hub accepts only the per-user keys (anything else refuses the whole request); the machine ones are hidden.
    if (isCloud()) payload = filterSettingsPayload(payload).kept;

    const oldGitDiffMode = state.settings?.git_diff_mode || 'previous_commit';

    try {
        // Nothing to save when no setting changed and neither the database encryption choice nor the backend and wake choices did (they are saved apart).
        const sideChange = databaseEncryptionChanged() || await importOnce('./terminal_backend.js').then(tb => tb.hasUnsavedBackendChange(), () => true);
        const nothingToSave = Object.keys(payload).length === 0 && !sideChange;
        if (!isCloud()) await saveDatabaseEncryptionSettings();
        // Nothing changed: nothing to write.
        const response = Object.keys(payload).length === 0 ? { ok: true } : await fetch("/api/settings", {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
        });
        if (!response.ok) {
            // A refused value (e.g. a CLI path that isn't an executable file, #3364) says why.
            const data = await response.json().catch(() => null);
            const err = new Error(`settings save failed (${response.status})`);
            if (data && typeof data.error === 'string') err.userMessage = data.error;
            throw err;
        }
        state.settings = { ...state.settings, ...payload };
        if (_settingsAtOpen) _settingsAtOpen = { ..._settingsAtOpen, ...payload }; // the dialog stays open on a cancelled backend change
        if (!isCloud() && state.privacyStatus) {
            state.privacyStatus.remote_access_enabled = remoteAccessEnabled;
            state.privacyStatus.remote_access_restart_required = state.privacyStatus.remote_access_effective !== remoteAccessEnabled;
        }
        if (!isCloud() && state.privacyStatus) {
            const log = state.privacyStatus.debug_input_log || {};
            state.privacyStatus.debug_input_log = { ...log, enabled: log.forced === true || debugInputLog };
            // The server knows whether a log file is now kept on disk; refresh from it.
            try {
                const fresh = await fetch('/api/system/privacy');
                if (fresh.ok) {
                    const body = await fresh.json();
                    if (body && body.debug_input_log) state.privacyStatus.debug_input_log = body.debug_input_log;
                }
            } catch { /* keep the local guess */ }
        }
        if (!isCloud()) renderRemoteAccessStatus(state.privacyStatus);
        if (!isCloud()) renderDebugInputLogStatus(state.privacyStatus);
        // Defaults may have changed — force next ACF open to re-fetch.
        _invalidateDefaultModels();

        // Clear any previously applied custom CSS variables
        clearCustomThemeVars();

        if (customTheme) {
            // Load and apply the custom theme (sets base + variables)
            await applyCustomThemeByName(customTheme);
        } else {
            // Apply built-in theme
            applyTheme(theme);
        }

        // If a live session is selected, force re-render with new default
        if (state.currentSession?.session_id) {
            const el = document.getElementById("pane-capture");
            if (el && el._lastCapture) {
                renderCaptureText(el, el._lastCapture);
            }
        }

        // Trigger pane width sync if the setting was just enabled
        if (fitPaneWidth) {
            syncPaneWidth();
        }

        // Apply font size and scrollback to existing terminal
        const term = getTerminal();
        if (term) {
            term.options.fontSize = parseInt(terminalFontSize, 10) || 13;
            term.options.scrollback = parseInt(terminalScrollback, 10) || 20000;
            const { fitTerminal } = await importOnce('./xterm_renderer.js');
            fitTerminal();
        }

        // Apply scrollbar visibility
        document.body.classList.toggle('no-scrollbars', !showScrollbars);

        // Refresh changed files if git diff mode changed
        if (gitDiffMode !== oldGitDiffMode) {
            refreshChangedFiles();
        }

        showToast(nothingToSave ? NOTHING_TO_SAVE : "Settings saved");
    } catch (e) {
        showToast(e && e.userMessage ? `Failed to save settings: ${e.userMessage}` : "Failed to save settings", true);
        return;
    }

    // A changed terminal backend asks first and applies at the next start (#3634).
    try {
        const tb = await importOnce('./terminal_backend.js');
        if (!(await tb.applyTerminalBackendChange())) return; // cancelled: the dialog stays open
    } catch { /* the rest is saved */ }
    hideSettingsModal();
}

// Sync flag shortcut button active states with the text input
function _syncFlagButtons(inputId) {
    const input = document.getElementById(inputId);
    if (!input) return;
    const flags = input.value.trim() ? input.value.trim().split(/\s+/) : [];
    const container = input.closest("label")?.nextElementSibling;
    if (!container) return;
    container.querySelectorAll(".flag-shortcut-btn").forEach(btn => {
        const flag = btn.textContent.trim();
        btn.classList.toggle("active", flags.includes(flag));
    });
}

// Attach input listeners for flag sync. This module loads on first use (#3140), usually long after DOMContentLoaded, so
// attach at once; the Job dialog's input once that dialog is built (#4500), which can be later still.
function _attachFlagSync(id) {
    const el = document.getElementById(id);
    if (!el || el._flagSync) return;
    el._flagSync = true;
    el.addEventListener("input", () => _syncFlagButtons(id));
}
for (const id of ["launch-flags", "restart-flags", "team-flags"]) _attachFlagSync(id);
onBuilt("job-modal", () => _attachFlagSync("job-modal-flags"));


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
