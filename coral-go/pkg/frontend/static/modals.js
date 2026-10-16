import { importOnce } from './import_once.js';
import { checkAgentCLI } from './prerequisites.js';
/* Modal management: launch and info dialogs */

import { state } from './state.js';
import { showToast, escapeHtml, escapeAttr, copyText, boardNameProblem, boardNameSlug } from './utils.js';
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
import { settingBool, renderRemoteAccessStatus, renderDebugInputLogStatus, applyTheme, clearCustomThemeVars, applyCustomThemeByName } from './settings_core.js';
import { pollEvery } from './overload.js';
import { attachDialogFocus } from './dialog_focus.js';
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

// Track which launch mode is active: null (chooser), 'agent', or 'terminal'
let _launchMode = null;
let _existingTeamBoardNames = new Set();
let _teamAgentModalRow = null;

// ── Launch target server (multi-server hub, spec section 5) ─────────────
// A launch always runs on exactly one server. _launchServer is the Server
// dropdown of the launch modal, _quickServer the quick-launch (team preset)
// modal and _addAgentServer the server inherited by "add agent to board" /
// folder-group launches. With hub mode off all three stay "local" and nothing
// below ever touches /api/remote/*.
let _launchServer = LOCAL_SERVER;
let _launchServerLocked = false;
let _quickServer = LOCAL_SERVER;
let _addAgentServer = LOCAL_SERVER;

function _effectiveServer(server) {
    return isHub() ? normServer(server) : LOCAL_SERVER;
}

// Per-server option sources. Everything keyed by server so switching the
// dropdown never shows another machine's models, defaults or CLI state.
//   settings    GET /api/settings        defaults: working dir, agent type, permission mode, models (per server)
//   models      GET /api/agent-models    model catalog (per server: CLI versions differ)
const _serverSettings = new Map();      // remote id -> settings object (local uses state.settings)
const _agentModelsByServer = new Map(); // server -> Promise<{type:[models]}>
const _defaultModelsByServer = new Map();

/** Settings that apply to a launch on `server` (the hub's own for local). */
function _settingsFor(server) {
    const id = normServer(server);
    if (id === LOCAL_SERVER) return state.settings || {};
    return _serverSettings.get(id) || {};
}

/** Fetch (and cache) a remote's settings. Throws when the remote cannot be reached. */
async function _loadServerSettings(server, { fresh = false } = {}) {
    const id = normServer(server);
    if (id === LOCAL_SERVER) return state.settings || {};
    if (!fresh && _serverSettings.has(id)) return _serverSettings.get(id);
    const resp = await serverFetch(id, '/api/settings');
    // Through the hub /api/settings is not exposed at all (a machine's settings are never readable
    // remotely): use the defaults quietly instead of reporting an error on every launch.
    if (resp.status === 404 && isCloud()) { _serverSettings.set(id, {}); return {}; }
    const data = await resp.json().catch(() => null);
    if (!resp.ok || !data) throw new Error((data && data.error) || `HTTP ${resp.status}`);
    const s = data.settings || {};
    _serverSettings.set(id, s);
    _defaultModelsByServer.delete(id);
    return s;
}

/** Server an Agent Config Form container launches on. */
function _acfServer(container) {
    const id = container && container.id;
    if (id === 'add-agent-board-acf') return _effectiveServer(_addAgentServer);
    if (id === 'restart-acf') return _effectiveServer(currentServer());
    return _effectiveServer(_launchServer);
}

// ── Canonical per-agent-type model list ────────────────────────────────
// Fetched once per server from GET /api/agent-models and cached for the tab lifetime.
// Shape: { claude: [...], codex: [], gemini: [], terminal: [] }
function _getAgentModels(server = LOCAL_SERVER) {
    const id = normServer(server);
    if (!_agentModelsByServer.has(id)) {
        _agentModelsByServer.set(id, serverFetch(id, '/api/agent-models')
            .then(r => r.ok ? r.json() : {})
            .catch(() => ({})));
    }
    return _agentModelsByServer.get(id);
}
window._getAgentModels = _getAgentModels;

// _fillModelDatalist replaces <option>s in the given <datalist> element with
// the canonical list for the agent type. Unknown types or an empty list
// result in an empty datalist (the input still accepts free-form text).
function _fillModelDatalist(datalistEl, agentType, modelsByType) {
    if (!datalistEl) return;
    const list = (modelsByType && modelsByType[agentType]) || [];
    datalistEl.innerHTML = list
        .map(m => `<option value="${escapeAttr(m)}"></option>`)
        .join('');
}
window._fillModelDatalist = _fillModelDatalist;

// ── User's per-type default model (for ACF pre-fill) ───────────────────
// Pulled from the same /api/settings payload the Settings modal uses (the
// target server's own settings for a remote), cached per server with its own
// invalidation (triggered after a Settings save so the next form open sees
// the new default).
// Shape: { claude: "<id>", codex: "<id>", gemini: "<id>" }
function _modelDefaultsFrom(s) {
    const agyModel = (s.default_model_agy || s.default_model_gemini || '').trim();
    return {
        claude: (s.default_model_claude || '').trim(),
        codex: (s.default_model_codex || '').trim(),
        agy: agyModel,
        antigravity: agyModel,
        gemini: (s.default_model_gemini || s.default_model_agy || '').trim(),
        pi: (s.default_model_pi || '').trim(),
    };
}
const _NO_MODEL_DEFAULTS = { claude: '', codex: '', agy: '', antigravity: '', gemini: '', pi: '' };
function _getDefaultModels(server = LOCAL_SERVER) {
    const id = normServer(server);
    if (!_defaultModelsByServer.has(id)) {
        const p = id === LOCAL_SERVER
            ? fetch('/api/settings')
                .then(r => r.ok ? r.json() : { settings: {} })
                .then(data => _modelDefaultsFrom(data.settings || {}))
            : _loadServerSettings(id).then(_modelDefaultsFrom);
        _defaultModelsByServer.set(id, p.catch(() => {
            _defaultModelsByServer.delete(id);
            return { ..._NO_MODEL_DEFAULTS };
        }));
    }
    return _defaultModelsByServer.get(id);
}
function _invalidateDefaultModels() {
    _defaultModelsByServer.clear();
    _serverSettings.clear();
}
window._invalidateDefaultModels = _invalidateDefaultModels;

// A persisted model is not automatically a user edit. Treat known provider
// defaults/models as replaceable when the provider changes, while preserving
// an explicitly chosen custom model across the switch.
function _classifyLoadedACFModel(container, agentType, model) {
    if (!container || !model) return;
    const input = container.querySelector('.acf-model');
    const initialModel = model;
    const acfServer = _acfServer(container);
    Promise.all([_getDefaultModels(acfServer), _getAgentModels(acfServer)]).then(([defaults, models]) => {
        if (container.dataset.acfModelDirty === 'true' || input?.value !== initialModel) return;
        const knownModels = Array.isArray(models?.[agentType]) ? models[agentType] : [];
        const isKnown = defaults?.[agentType] === initialModel || knownModels.includes(initialModel);
        container.dataset.acfModelDirty = isKnown ? 'false' : 'true';
        container.dataset.acfModelClassifying = 'false';
    }).catch(() => {
        // If the catalog cannot be read, preserve the explicit value rather
        // than risking data loss.
        container.dataset.acfModelClassifying = 'false';
        container.dataset.acfModelDirty = 'true';
    });
}

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
        _launchMode = null;
        _showLaunchStep("chooser");
        return;
    }
    _launchMode = type;
    _showLaunchStep(type);
    if (type === "agent") {
        _loadAgentBoardProjects();
        _initAgentPresets();
        // Render agent config form
        renderAgentConfigForm('launch-agent-acf', {
            showPreset: false,
            showName: false,
            value: { agentType: _settingsFor(_effectiveServer(_launchServer)).default_agent_type || 'claude' },
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
    _launchMode = null;
    _serverSettings.clear();
    _launchServerLocked = !!lock && isHub();
    _launchServer = _effectiveServer(coerceSelection(server, isHub(), state.servers));
    if (_launchServerLocked && normServer(server) !== LOCAL_SERVER) _launchServer = normServer(server);
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
        teamPermModeEl.value = _settingsFor(_effectiveServer(_launchServer)).default_permission_mode || 'bypassPermissions';
        _updatePermModeDescription(teamPermModeEl);
    }

    // Pre-fill from the target server's settings (the hub's own when local)
    _resetLaunchDirs(_launchServer);
    const s = _settingsFor(_launchServer);
    const typeSelect = document.getElementById("launch-type");
    if (s.default_agent_type && typeSelect) {
        typeSelect.value = s.default_agent_type;
    }

    _syncMobileLaunchSections();
    if (_launchServer !== LOCAL_SERVER) _onLaunchServerChange(_launchServer, { force: true });
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
    const current = which === 'launch' ? _launchServer : _quickServer;
    const locked = which === 'launch' && _launchServerLocked;
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
    if (next === _launchServer && !force) return;
    const prev = _launchServer;
    _launchServer = next;
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
        if (_launchServer !== next) return; // changed again while loading
    }
    if (note) note.textContent = loadError ? `Could not read ${serverLabel(next, state.servers)}: ${loadError}` : '';
    // Re-apply defaults from the (now loaded) settings, then the dependent option sources.
    _resetLaunchDirs(next, { switching: true });
    const s = _settingsFor(next);
    const typeSelect = document.getElementById('launch-type');
    if (s.default_agent_type && typeSelect) typeSelect.value = s.default_agent_type;
    if (_launchMode === 'agent') _loadAgentBoardProjects();
    if (_launchMode === 'team') { _loadExistingTeamBoards(); _checkTeamGitDir(); }
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
    if (next === _quickServer) return;
    _quickServer = next;
    const b = document.getElementById('quick-launch-browser');
    if (b) b.style.display = 'none';
    _resetDirInputs(['quick-launch-workdir'], next, {}, { switching: true });
    if (next !== LOCAL_SERVER) {
        try { await _loadServerSettings(next); } catch (_) { /* launch will surface the error */ }
        if (_quickServer !== next) return;
        _resetDirInputs(['quick-launch-workdir'], next, _settingsFor(next), { switching: true });
    } else {
        _resetDirInputs(['quick-launch-workdir'], next, _settingsFor(next), { switching: true });
    }
}
window._onQuickLaunchServerChange = _onQuickLaunchServerChange;

// Directory inputs belong to whichever server their form launches on.
setDirServerResolver(inputId => {
    if (!isHub()) return LOCAL_SERVER;
    if (inputId === 'quick-launch-workdir') return _quickServer;
    if (inputId === 'resume-dir') return LOCAL_SERVER; // resume uses the hub's own session history
    return _launchServer;
});

/** Toast and return true when `server` cannot be launched on. */
function _launchBlocked(server) {
    const why = launchBlockReason(server, isHub(), state.servers);
    if (why) showToast(why, true);
    return !!why;
}

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
export function markFieldInvalid(input, message) {
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
export function clearFieldInvalid(input) {
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

    // Close on backdrop click
    modal.onclick = (e) => { if (e.target === modal) hideTeamWizardModal(); };
    // Close on Escape
    modal._escHandler = (e) => { if (e.key === 'Escape') hideTeamWizardModal(); };
    document.addEventListener('keydown', modal._escHandler);
}
window.showTeamWizardModal = showTeamWizardModal;

function hideTeamWizardModal() {
    const modal = document.getElementById('team-wizard-modal');
    if (!modal) return;
    modal.style.display = 'none';
    _setTeamWizardError('');
    if (modal._escHandler) {
        document.removeEventListener('keydown', modal._escHandler);
        modal._escHandler = null;
    }
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
    _skipDefaultTeamAgents = true;
    // Open the launch modal directly to the team step, skipping the chooser.
    // Don't call showLaunchModal() — it resets to the chooser and clears the agent list.
    document.getElementById("launch-modal").style.display = "flex";
    _showLaunchStep("team");
    _launchMode = "team";
    _initTeamForm();

    // Show generated-team banner
    const stepEl = document.getElementById('launch-step-team');
    let banner = document.getElementById('generated-team-banner');
    if (!banner && stepEl) {
        banner = document.createElement('div');
        banner.id = 'generated-team-banner';
        banner.className = 'generated-team-banner';
        banner.innerHTML = '<span class="material-icons">auto_awesome</span> Your AI-generated team is ready — review and edit before launching.';
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
        _teamAgentCounter = 0;
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
                        <span class="material-icons team-wizard-success-icon">check_circle</span>
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
window._reviewGeneratedTeam = _reviewGeneratedTeam;

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
                <span class="material-icons team-wizard-success-icon">check_circle</span>
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
    _teamAgentModalRow = row;
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
    _teamAgentModalRow = null;
}
window.hideTeamAgentModal = hideTeamAgentModal;

function saveTeamAgentFromModal() {
    const config = getAgentConfig('team-agent-modal-acf');
    if (!config.name) {
        showToast('Agent name is required', true);
        return;
    }

    if (_teamAgentModalRow?.dataset.acfId) {
        setAgentConfig(_teamAgentModalRow.dataset.acfId, config);
        _updateTeamAgentSummary(_teamAgentModalRow);
    } else {
        _addTeamAgent(config.name, config.prompt, config.capabilities, config.agentType, config.model, config.flags);
    }

    hideTeamAgentModal();
}
window.saveTeamAgentFromModal = saveTeamAgentFromModal;

// ── CLI Availability Check ────────────────────────────────────────────────

const CLI_INSTALL_INSTRUCTIONS = {
    claude: { name: 'claude', cmd: 'npm install -g @anthropic-ai/claude-code' },
    agy: { name: 'agy', cmd: 'curl -fsSL https://antigravity.google/install.sh | bash' },
    antigravity: { name: 'agy', cmd: 'curl -fsSL https://antigravity.google/install.sh | bash' },
    gemini: { name: 'agy', cmd: 'curl -fsSL https://antigravity.google/install.sh | bash' },
    codex: { name: 'codex', cmd: 'npm install -g @openai/codex' },
    pi: { name: 'pi', cmd: 'npm install -g @mariozechner/pi-coding-agent' },
};

function _checkAgentCLI(agentType, warning = document.getElementById('cli-check-warning'), force = false) {
    // The CLI must exist on the server the agent will run on.
    const server = _acfServer(warning && warning.closest ? warning.closest('[data-acf-uid]') : null);
    return checkAgentCLI(agentType, warning, {force,command:CLI_INSTALL_INSTRUCTIONS[agentType]?.cmd || '',server});
}
window._checkAgentCLI = _checkAgentCLI;

/** Map agent type to its permission bypass flag. */
const PERM_FLAGS = {
    claude: '--dangerously-skip-permissions',
    agy: '--dangerously-skip-permissions',
    antigravity: '--dangerously-skip-permissions',
    gemini: '--dangerously-skip-permissions',
    codex: '--full-auto',
    pi: '',
};

const PERMISSION_MODE_DESCRIPTIONS = {
    default: 'Reads only — best for getting started and sensitive work',
    acceptEdits: 'Reads, file edits, and common filesystem commands (mkdir, touch, mv, cp, etc.) — best for iterating on code you\'re reviewing',
    plan: 'Reads only — best for exploring a codebase before changing it',
    auto: 'Everything, with background safety checks — best for long tasks and reducing prompt fatigue',
    dontAsk: 'Only pre-approved tools — best for locked-down CI and scripts',
    bypassPermissions: 'Everything except protected paths — isolated containers and VMs only',
};

function _updatePermModeDescription(selectEl) {
    const hint = selectEl.parentElement?.querySelector('.perm-mode-desc');
    if (hint) hint.textContent = PERMISSION_MODE_DESCRIPTIONS[selectEl.value] || '';
}

function _getPermissionFlagsForAgentMode(agentType, mode) {
    const permMode = mode || (state.settings && state.settings.default_permission_mode) || 'bypassPermissions';
    if (!permMode || permMode === 'default' || agentType === 'pi' || agentType === 'terminal') return '';
    if (agentType === 'claude') return `--permission-mode ${permMode}`;
    if (agentType === 'agy' || agentType === 'antigravity' || agentType === 'gemini') {
        if (permMode === 'plan') return '--mode plan';
        if (permMode === 'acceptEdits') return '--mode accept-edits';
        return '--dangerously-skip-permissions';
    }
    if (agentType === 'codex') {
        if (permMode === 'bypassPermissions') return '--dangerously-bypass-approvals-and-sandbox';
        if (permMode === 'plan') return '--sandbox read-only -a on-request';
        if (permMode === 'acceptEdits') return '--sandbox workspace-write -a on-request';
        if (permMode === 'auto' || permMode === 'dontAsk') return '--sandbox workspace-write -a never';
        return '--sandbox workspace-write -a on-request';
    }
    return PERM_FLAGS[agentType] || '';
}

function _stripPermFlags(flagsStr) {
    const known = new Set(Object.values(PERM_FLAGS).filter(Boolean));
    known.add('--dangerously-bypass-approvals-and-sandbox');
    known.add('--dangerously-skip-permissions');
    known.add('--yolo');
    const tokens = (flagsStr || '').split(/\s+/).filter(Boolean);
    const kept = [];
    for (let i = 0; i < tokens.length; i++) {
        const flag = tokens[i];
        if (known.has(flag) || flag === '--search') continue;
        if (flag === '--permission-mode' || flag === '--sandbox' || flag === '--approval-mode' || flag === '-a' || flag === '--mode') {
            i++;
            continue;
        }
        if (
            flag.startsWith('--permission-mode=') ||
            flag.startsWith('--sandbox=') ||
            flag.startsWith('--approval-mode=') ||
            flag.startsWith('--mode=')
        ) {
            continue;
        }
        kept.push(flag);
    }
    return kept.join(' ');
}

function _normalizePermFlagsForAgent(flagsStr, agentType, mode) {
    const base = _stripPermFlags(flagsStr);
    const permFlags = _getPermissionFlagsForAgentMode(agentType, mode);
    return [base, permFlags].filter(Boolean).join(' ');
}

function _permissionModeFromFlags(flagsStr) {
    const flags = flagsStr || '';
    const modeMatch = flags.match(/--permission-mode(?:\s+|=)(\S+)/);
    if (modeMatch) return modeMatch[1];
    if (flags.includes('--dangerously-bypass-approvals-and-sandbox')) return 'bypassPermissions';
    if (flags.includes('--full-auto') || flags.includes('--dangerously-skip-permissions') || flags.includes('--yolo')) return 'bypassPermissions';
    if (/--sandbox(?:\s+|=)read-only/.test(flags)) return 'plan';
    if (/--sandbox(?:\s+|=)workspace-write/.test(flags)) return 'auto';
    return '';
}

function _getPermFlagHelp(agentType) {
    const mode = (state.settings && state.settings.default_permission_mode) || 'bypassPermissions';
    const flag = _getPermissionFlagsForAgentMode(agentType, mode);
    const agentLabel = getEngineName(agentType || 'claude');
    if (agentType === 'pi') {
        return `${agentLabel} runs full-auto by design — no permission flag needed.`;
    }
    if (agentType === 'claude') {
        return `High-autonomy launch for ${agentLabel}. Coral adds ${flag} so the agent can run without interactive permission prompts.`;
    }
    return `High-autonomy launch for ${agentLabel}. Coral adds ${flag} so the agent can run without interactive permission prompts.`;
}

/** Update permission flag shortcut buttons in the same modal as the agent type select. */
function _updatePermFlagButtons(selectEl) {
    const agentType = selectEl.value || 'claude';
    const mode = (state.settings && state.settings.default_permission_mode) || 'bypassPermissions';
    const flag = _getPermissionFlagsForAgentMode(agentType, mode);

    // Find the enclosing modal content and update all flag-shortcut-btn that reference skip-permissions/full-auto
    const modal = selectEl.closest('.modal-content') || selectEl.closest('.modal');
    if (!modal) return;

    // The permission button: the shortcut whose flag is a skip-permissions or full-auto one, or that
    // this function already switched (data-on-click="flag-perm"). Found by its data attributes, never
    // by handler text (#3645): it now sets the agent type's permission flags for the saved mode.
    modal.querySelectorAll('.flag-shortcut-btn').forEach(btn => {
        const isPerm = btn.getAttribute('data-on-click') === 'flag-perm' || /dangerously-|full-auto/.test(btn.dataset.flag || '');
        if (!isPerm || !btn.dataset.flagInput) return;
        btn.setAttribute('data-on-click', 'flag-perm');
        btn.dataset.agentType = agentType;
        btn.dataset.mode = mode;
        btn.dataset.flag = flag;
        btn.textContent = flag;
    });

    // Also update the flags input value — replace any known perm flag with the new one
    modal.querySelectorAll('input[id*=flags]').forEach(input => {
        const mode = (state.settings && state.settings.default_permission_mode) || 'bypassPermissions';
        input.value = _normalizePermFlagsForAgent(input.value, agentType, mode);
    });
}
window._updatePermFlagButtons = _updatePermFlagButtons;
window._updatePermModeDescription = _updatePermModeDescription;

/** Check if a flags string contains ANY known permission flag. */
function _hasPermFlag(flagsStr) {
    return Object.values(PERM_FLAGS).filter(Boolean).some(f => flagsStr.includes(f)) ||
        flagsStr.includes('--dangerously-bypass-approvals-and-sandbox') ||
        flagsStr.includes('--dangerously-skip-permissions') ||
        flagsStr.includes('--yolo') ||
        /--permission-mode(?:\s+|=)\S+/.test(flagsStr) ||
        /--sandbox(?:\s+|=)\S+/.test(flagsStr) ||
        /--mode(?:\s+|=)\S+/.test(flagsStr) ||
        /(?:^|\s)(?:-a|--approval-mode)(?:\s+|=)\S+/.test(flagsStr);
}

function _syncACFAutoPermissions(container) {
    if (!container) return;
    const typeEl = container.querySelector('.acf-agent-type');
    const toggleEl = container.querySelector('.acf-auto-permissions');
    const noteEl = container.querySelector('.acf-auto-permissions-note');
    const flagEl = container.querySelector('.acf-auto-permissions-flag');
    if (!typeEl || !toggleEl || !noteEl || !flagEl) return;

    const agentType = typeEl.value || 'claude';
    const mode = container.querySelector('.acf-permission-mode')?.value ||
        _settingsFor(_acfServer(container)).default_permission_mode ||
        'bypassPermissions';
    const flag = _getPermissionFlagsForAgentMode(agentType, mode);
    flagEl.textContent = flag;
    noteEl.textContent = _getPermFlagHelp(agentType);
    noteEl.style.opacity = toggleEl.checked ? '1' : '0.72';
}

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
            <p style="color:var(--text-secondary);font-size:13px;line-height:1.5;margin:8px 0 12px">
                The <code>${info.name}</code> command was not found ${where}. Install it there to launch ${info.name} agents.
            </p>
            <div style="background:var(--bg-tertiary);border:1px solid var(--border);border-radius:6px;padding:10px 14px;font-family:var(--font-mono);font-size:13px;display:flex;align-items:center;gap:8px">
                <code style="flex:1;user-select:all">${escapeHtml(info.cmd)}</code>
                <button class="btn btn-small" data-on-click="info-copy" data-cmd="${escapeAttr(info.cmd)}">Copy</button>
            </div>
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
                <p style="color:var(--text-secondary);font-size:13px;line-height:1.5;margin:8px 0 16px">
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

    if (_launchMode === "terminal") {
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
    const server = _effectiveServer(_launchServer);
    if (_launchBlocked(server)) return;

    // Permission flag check (skip for terminals)
    if (_launchMode !== "terminal") {
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
    if (_launchMode !== "terminal") {
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
    const stepId = _launchMode === "terminal" ? "launch-step-terminal" : "launch-step-agent";
    const launchBtn = document.querySelector(`#${stepId} .btn-primary`);
    if (launchBtn) { launchBtn.disabled = true; launchBtn.textContent = 'Launching...'; }

    try {
        const dirInput = document.getElementById(_launchMode === "terminal" ? "launch-terminal-dir" : "launch-dir");
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
        const server = _effectiveServer(_launchServer);
        const resp = await serverFetch(server, "/api/board/projects");
        const projects = await resp.json();
        if (!Array.isArray(projects) || server !== _effectiveServer(_launchServer)) return;
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

// ── Saved Personas & Team Templates ──────────────────────────────────────

function _getSavedPersonas() {
    try { return JSON.parse(state.settings.saved_personas || "[]"); }
    catch { return []; }
}

async function _setSavedPersonas(personas) {
    state.settings.saved_personas = JSON.stringify(personas);
    await fetch("/api/settings", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ saved_personas: state.settings.saved_personas }),
    });
}

function _getSavedTeamTemplates() {
    try { return JSON.parse(state.settings.saved_team_templates || "[]"); }
    catch { return []; }
}

async function _setSavedTeamTemplates(templates) {
    state.settings.saved_team_templates = JSON.stringify(templates);
    await fetch("/api/settings", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ saved_team_templates: state.settings.saved_team_templates }),
    });
}

// What a preset select's choice does, by the handler a caller names (#3645).
const PRESET_KINDS = { 'window._selectAgentPreset': 'agent', 'window._applyACFPresetValue': 'acf' };

/** Render preset buttons (built-in + saved) into a container. */
function _renderPresetButtons(containerId, onClickFn, opts = {}) {
    const container = document.getElementById(containerId);
    if (!container) return;
    const saved = _getSavedPersonas();

    const target = opts.target;
    // The select names what a choice does (preset-pick, by kind): the agent dialog's preset, or an
    // agent config form's (with its target), never a function name in the markup.
    const kind = PRESET_KINDS[onClickFn];
    if (!kind) throw new Error('unknown preset handler ' + onClickFn);
    let html = `<select class="agent-preset-select" data-on-change="preset-pick" data-kind="${escapeAttr(kind)}" data-target="${escapeAttr(target || '')}">`;
    html += `<option value="" disabled selected>Choose a preset or custom...</option>`;
    html += `<optgroup label="Built-in">`;
    for (const preset of AGENT_PRESETS) {
        html += `<option value="${escapeAttr(preset.name)}">${escapeHtml(preset.name)}</option>`;
    }
    html += `</optgroup>`;
    if (saved.length > 0) {
        html += `<optgroup label="Saved Personas">`;
        for (const p of saved) {
            html += `<option value="${escapeAttr(p.name)}">${escapeHtml(p.name)}</option>`;
        }
        html += `</optgroup>`;
    }
    html += `<option value="">Custom</option>`;
    html += `</select>`;
    container.innerHTML = html;
}

/** Find a persona by name in built-in + saved lists. */
function _findPersona(name) {
    return AGENT_PRESETS.find(p => p.name === name) || _getSavedPersonas().find(p => p.name === name);
}

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

    const saved = _getSavedPersonas();
    const idx = saved.findIndex(p => p.name === name);
    const entry = { name, prompt, flags };
    if (capabilities) entry.capabilities = capabilities;
    if (idx >= 0) saved[idx] = entry; else saved.push(entry);
    await _setSavedPersonas(saved);
    showToast(`Saved persona "${name}"`);

    // Re-render all preset containers
    _renderPresetButtons("agent-preset-selector", "window._selectAgentPreset");
}
window._saveCurrentPersona = _saveCurrentPersona;

async function _deletePersona(name) {
    const saved = _getSavedPersonas().filter(p => p.name !== name);
    await _setSavedPersonas(saved);
    showToast(`Deleted persona "${name}"`);
    _renderPresetButtons("agent-preset-selector", "window._selectAgentPreset");
}
window._deletePersona = _deletePersona;

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

    const existing = _getSavedPersonas();
    const existingNames = new Set(existing.map(p => p.name));
    let added = 0;
    for (const p of personas) {
        if (!p.name) continue;
        if (AGENT_PRESETS.find(bp => bp.name === p.name)) continue;
        if (existingNames.has(p.name)) {
            const idx = existing.findIndex(e => e.name === p.name);
            existing[idx] = { name: p.name, prompt: p.prompt || "", flags: p.flags || "" };
        } else {
            existing.push({ name: p.name, prompt: p.prompt || "", flags: p.flags || "" });
        }
        added++;
    }
    await _setSavedPersonas(existing);
    showToast(`Imported ${added} persona(s)`);
    _renderPresetButtons("agent-preset-selector", "window._selectAgentPreset");
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

    const existing = _getSavedTeamTemplates();
    const existingNames = new Set(existing.map(t => t.name));
    let added = 0;
    for (const t of templates) {
        if (!t.name || !Array.isArray(t.agents)) continue;
        if (existingNames.has(t.name)) {
            const idx = existing.findIndex(e => e.name === t.name);
            existing[idx] = t;
        } else {
            existing.push(t);
        }
        added++;
    }
    await _setSavedTeamTemplates(existing);
    showToast(`Imported ${added} template(s)`);
    _renderTeamTemplateSelector();
}

// ── Agent Preset Selector (single-agent modal) ───────────────────────────

function _initAgentPresets() {
    _renderPresetButtons("agent-preset-selector", "window._selectAgentPreset");
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
            agentType: _settingsFor(_effectiveServer(_launchServer)).default_agent_type || current.agentType,
        });
        if (nameInput) nameInput.value = '';
    }
}
window._selectAgentPreset = _selectAgentPreset;

// ── Add Agent to Board ───────────────────────────────────────────────────
// Launches started from inside a board / folder group inherit that group's
// server (the key is "@server/name" for remotes) and cannot be redirected: the
// working directory shown belongs to that server.

export function showAddAgentToBoard(boardName, workDir) {
    const server = _effectiveServer(splitKey(boardName).server);
    if (_launchBlocked(server)) return;
    _addAgentServer = server;
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
    const server = _effectiveServer(_addAgentServer);
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
    _addAgentServer = server;
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

let _teamAgentCounter = 0;
let _skipDefaultTeamAgents = false;

// Predefined agent presets
// All known Coral capabilities
const ALL_CAPABILITIES = ['file_read', 'file_write', 'shell', 'web_access', 'git_write', 'agent_spawn', 'notebook'];

const AGENT_PRESETS = [
    {
        name: "Lead Developer",
        prompt: "You are the lead developer. Implement features, write code, and coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'file_write', 'shell', 'git_write', 'agent_spawn'] },
    },
    {
        name: "QA Engineer",
        prompt: "You are an expert QA engineer. Review the work of other agents, create test plans, write tests, and ask probing questions about complex areas. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', "file_write", "shell"], deny: [] },
    },
    {
        name: "Orchestrator",
        prompt: "You are the orchestrator. Coordinate the team, break down tasks, assign work via the message board, and track progress. Do not write code yourself — delegate to the other agents. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'file_write', 'shell', 'git_write', 'agent_spawn', 'web_access'] },
    },
    {
        name: "Terminal",
        prompt: "",
        agent_type: "terminal",
        capabilities: { allow: ['shell'] },
    },
    {
        name: "Frontend Dev",
        prompt: "You are a frontend developer. Build UI components, style pages, and ensure a great user experience. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'file_write', 'shell:npm *', 'shell:npx *', 'web_access', 'shell:coral-board *'] },
    },
    {
        name: "Backend Dev",
        prompt: "You are a backend developer. Build APIs, services, and data models. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'file_write', 'shell', 'git_write'] },
    },
    {
        name: "DevOps Engineer",
        prompt: "You are a DevOps engineer. Handle CI/CD, infrastructure, deployment, and monitoring. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'file_write', 'shell', 'git_write', 'shell'] },
    },
    {
        name: "Security Reviewer",
        prompt: "You are a security reviewer. Audit code for vulnerabilities, review auth flows, and ensure OWASP best practices. Report findings via the message board.",
        capabilities: { allow: ['file_read', 'web_access', 'shell:coral-board *'] },
    },
    {
        name: "Technical Writer",
        prompt: "You are a technical writer. Write documentation, API guides, and READMEs. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'file_write', 'shell:coral-board *'] },
    },
    {
        name: "Content Writer",
        prompt: "You are an expert content writer. Write blog posts, social media copy, email campaigns, and landing page content. Focus on clear, engaging writing that drives action. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'file_write', 'web_access', 'shell:coral-board *'] },
    },
    {
        name: "SEO Strategist",
        prompt: "You are an SEO and analytics expert. Research keywords, analyze competitors, optimize content for search engines, and provide data-driven recommendations. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'web_access', 'shell:coral-board *'] },
    },
    {
        name: "Design Director",
        prompt: "You are a creative director focused on visual design. Create design briefs, review visual assets, ensure brand consistency, and provide art direction. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'file_write', 'web_access','shell:coral-board *'] },
    },
    {
        name: "Research Analyst",
        prompt: "You are a thorough research analyst. Find information, summarize documents, compare options, and provide well-sourced answers. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'web_access', 'shell:coral-board *'] },
    },
    {
        name: "Writer & Editor",
        prompt: "You are a skilled writer and editor. Draft emails, documents, presentations, and reports. Polish and proofread content for clarity and professionalism. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'file_write', 'shell:coral-board *'] },
    },
    {
        name: "Scheduler & Planner",
        prompt: "You are an organizational expert. Help plan projects, create timelines, track deadlines, and organize information into actionable plans. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'file_write','shell:coral-board *'] },
    },
];

// ── Permissions Editor ─────────────────────────────────────────────────

const CAPABILITY_LABELS = {
    file_read: 'Read Files',
    file_write: 'Write Files',
    shell: 'Shell',
    web_access: 'Web Access',
    git_write: 'Git Write',
    agent_spawn: 'Spawn Agents',
    notebook: 'Notebooks',
};

function _togglePermissions(containerId) {
    const el = document.getElementById(containerId);
    if (!el) return;
    const isHidden = el.style.display === 'none';
    el.style.display = isHidden ? '' : 'none';
    const chevron = el.previousElementSibling?.querySelector('.permissions-chevron');
    if (chevron) chevron.style.transform = isHidden ? 'rotate(90deg)' : '';
}
window._togglePermissions = _togglePermissions;

function _renderPermChips(containerId, type, capabilities) {
    const container = document.getElementById(containerId);
    if (!container) return;
    const caps = capabilities?.[type] || [];
    // Separate base capabilities from shell patterns
    const baseCaps = caps.filter(c => !c.startsWith('shell:'));
    const shellPatterns = caps.filter(c => c.startsWith('shell:')).map(c => c.slice(6));

    let html = '';
    for (const cap of ALL_CAPABILITIES) {
        const isActive = baseCaps.includes(cap);
        const cls = `perm-chip${isActive ? ' active' : ''}`;
        html += `<button class="${escapeAttr(cls)}" data-cap="${escapeAttr(cap)}" data-type="${escapeAttr(type)}" data-on-click="perm-chip-toggle">${CAPABILITY_LABELS[cap] || cap}</button>`;
    }
    container.innerHTML = html;

    // Show shell patterns input if shell is in allow
    const shellSection = container.closest('.permissions-editor')?.querySelector('.perms-shell-patterns');
    const shellInput = container.closest('.permissions-editor')?.querySelector('.perms-shell-input');
    if (shellSection && shellInput && type === 'allow') {
        const hasShell = baseCaps.includes('shell');
        const hasShellPatterns = shellPatterns.length > 0;
        shellSection.style.display = (hasShell || hasShellPatterns) ? '' : 'none';
        shellInput.value = shellPatterns.join(', ');
    }
}

function _togglePermChip(btn) {
    btn.classList.toggle('active');
    // Toggle shell patterns visibility
    const cap = btn.dataset.cap;
    const type = btn.dataset.type;
    if (cap === 'shell' && type === 'allow') {
        const shellSection = btn.closest('.permissions-editor')?.querySelector('.perms-shell-patterns');
        if (shellSection) {
            shellSection.style.display = btn.classList.contains('active') ? '' : 'none';
        }
    }
}
window._togglePermChip = _togglePermChip;

function _getPermissions(editorId) {
    const editor = document.getElementById(editorId);
    if (!editor) return null;

    const result = {};
    for (const type of ['allow', 'deny']) {
        const chips = editor.querySelectorAll(`[data-type="${type}"].active`);
        if (chips.length > 0) {
            result[type] = [...chips].map(c => c.dataset.cap);
        }
    }

    // Add shell patterns to allow list
    const shellInput = editor.querySelector('.perms-shell-input');
    if (shellInput && shellInput.value.trim()) {
        if (!result.allow) result.allow = [];
        const patterns = shellInput.value.split(',').map(p => p.trim()).filter(Boolean);
        for (const p of patterns) {
            result.allow.push(`shell:${p}`);
        }
    }

    return Object.keys(result).length > 0 ? result : null;
}

function _setPermissions(editorId, capabilities) {
    const editor = document.getElementById(editorId);
    if (!editor) return;
    _renderPermChips(editorId + '-allow', 'allow', capabilities || {});
    _renderPermChips(editorId + '-deny', 'deny', capabilities || {});
}

// ── Unified Agent Config Form ─────────────────────────────────────────

let _acfCounter = 0;

/**
 * Render a unified agent configuration form into a container.
 * @param {string} containerId - DOM element ID to render into
 * @param {object} opts - { mode: 'full'|'compact-card', showPreset: bool, showName: bool, value: {...} }
 */
function renderAgentConfigForm(containerId, opts = {}) {
    const container = document.getElementById(containerId);
    if (!container) return;
    _acfCounter++;
    const uid = `acf-${_acfCounter}`;
    container.dataset.acfUid = uid;

    const mode = opts.mode || 'full';
    const showPreset = opts.showPreset !== false;
    const showName = opts.showName !== false;
    const showAutoPermissions = opts.showAutoPermissions !== false;
    const v = opts.value || {};

    const agentTypeVal = v.agentType || v.agent_type || _settingsFor(_acfServer(container)).default_agent_type || '';
    const modelVal = v.model || '';
    const nameVal = v.name || '';
    const promptVal = v.prompt || '';
    const flagsVal = v.flags || '';
    const autoPermsVal = Object.prototype.hasOwnProperty.call(v, 'autoPermissions')
        ? !!v.autoPermissions
        : (Object.prototype.hasOwnProperty.call(v, 'flags') ? _hasPermFlag(flagsVal) : true);

    // Build permissions summary text
    const caps = v.capabilities || null;
    let permsSummary = 'default';
    if (caps) {
        const parts = [];
        if (caps.allow?.length) parts.push(`${caps.allow.length} allow`);
        if (caps.deny?.length) parts.push(`${caps.deny.length} deny`);
        permsSummary = parts.length ? parts.join(', ') : 'default';
    }

    let presetHTML = '';
    if (showPreset) {
        presetHTML = `<div id="${escapeAttr(uid)}-presets" class="agent-preset-selector" style="margin-bottom:8px"></div>`;
    }

    let nameHTML = '';
    if (showName) {
        nameHTML = `
            <label>Name / Role:
                <input type="text" class="acf-name" placeholder="e.g. QA Engineer, Frontend Dev" value="${escapeAttr(nameVal)}">
            </label>`;
    }

    let autoPermissionsHTML = '';
    if (showAutoPermissions) {
        const defaultMode = _settingsFor(_acfServer(container)).default_permission_mode || 'bypassPermissions';
        const permModeVal = autoPermsVal
            ? (typeof autoPermsVal === 'string' ? autoPermsVal : (_permissionModeFromFlags(flagsVal) || defaultMode))
            : 'default';
        const permModeDesc = PERMISSION_MODE_DESCRIPTIONS[permModeVal] || '';
        autoPermissionsHTML = `
            <label>Permission Mode:
                <select class="acf-permission-mode" data-on-change="perm-mode-describe">
                    <option value="default"${permModeVal === 'default' ? ' selected' : ''}>Default</option>
                    <option value="plan"${permModeVal === 'plan' ? ' selected' : ''}>Plan</option>
                    <option value="acceptEdits"${permModeVal === 'acceptEdits' ? ' selected' : ''}>Accept Edits</option>
                    <option value="auto"${permModeVal === 'auto' ? ' selected' : ''}>Auto Edit + Bash</option>
                    <option value="bypassPermissions"${permModeVal === 'bypassPermissions' ? ' selected' : ''}>Bypass Permissions</option>
                    <option value="dontAsk"${permModeVal === 'dontAsk' ? ' selected' : ''}>Don't Ask</option>
                </select>
                <span class="settings-hint perm-mode-desc">${escapeHtml(permModeDesc)}</span>
            </label>`;
    }

    container.innerHTML = `
        ${presetHTML}
        ${nameHTML}
        <div class="acf-top-row">
            <label>Agent Type:
                <select class="acf-agent-type">
                    <option value="claude"${agentTypeVal === 'claude' || !agentTypeVal ? ' selected' : ''}>Claude</option>
                    <option value="agy"${agentTypeVal === 'agy' || agentTypeVal === 'antigravity' || agentTypeVal === 'gemini' ? ' selected' : ''}>Antigravity</option>
                    <option value="codex"${agentTypeVal === 'codex' ? ' selected' : ''}>Codex</option>
                    <option value="pi"${agentTypeVal === 'pi' ? ' selected' : ''}>Pi</option>
                    <option value="terminal"${agentTypeVal === 'terminal' ? ' selected' : ''}>Terminal</option>
                </select>
            </label>
            <label class="acf-model-wrap">Model <span style="color:var(--text-muted);font-weight:normal">(optional)</span>:
                <input type="text" class="acf-model" list="acf-models-${uid}" placeholder="Leave blank to use default" autocomplete="off" value="${escapeAttr(modelVal)}">
                <datalist id="acf-models-${escapeAttr(uid)}"></datalist>
            </label>
        </div>
        <div class="acf-cli-warning" style="display:none;margin:8px 0" role="status"></div>
        <label>Behavior Prompt:
            <textarea class="acf-prompt" rows="4" placeholder="Describe the agent's role and behavior...">${escapeHtml(promptVal)}</textarea>
        </label>
        ${autoPermissionsHTML}
        <div class="permissions-section">
            <button type="button" class="permissions-toggle-btn" data-on-click="perms-toggle" data-target="${escapeAttr(uid + '-perms')}">
                <svg class="permissions-chevron" width="10" height="10" viewBox="0 0 16 16" fill="currentColor"><path d="M6 4l4 4-4 4z"/></svg>
                Permissions <span class="acf-perms-summary" style="color:var(--text-muted);font-weight:normal;font-size:11px">(${escapeHtml(permsSummary)})</span>
            </button>
            <div id="${escapeAttr(uid)}-perms" class="permissions-editor" style="display:none">
                <div class="perms-group">
                    <label class="perms-label">Allow:</label>
                    <div id="${escapeAttr(uid)}-perms-allow" class="perms-chips"></div>
                </div>
                <div class="perms-group">
                    <label class="perms-label">Deny:</label>
                    <div id="${escapeAttr(uid)}-perms-deny" class="perms-chips"></div>
                </div>
                <div class="perms-shell-patterns" style="display:none">
                    <label class="perms-label">Shell patterns <span style="color:var(--text-muted);font-weight:normal">(comma-separated)</span>:</label>
                    <input type="text" class="perms-shell-input" placeholder="e.g. git *, npm *, npx *">
                </div>
            </div>
        </div>
        <details class="team-advanced acf-flags-details">
            <summary>Flags</summary>
            <div class="team-advanced-body">
                <label>Flags <span style="color:var(--text-muted);font-weight:normal">(optional)</span>:
                    <input type="text" class="acf-flags" placeholder="e.g. --verbose" value="${escapeAttr(_stripPermFlags(flagsVal))}">
                </label>
            </div>
        </details>
    `;

    // Initialize permissions chips
    _setPermissions(`${uid}-perms`, caps);

    // Render presets if enabled — pass containerId so the preset handler knows which ACF to update
    if (showPreset) {
        _renderPresetButtons(`${uid}-presets`, 'window._applyACFPresetValue', { target: containerId });
    }

    // Loaded values are classified against the provider's known defaults and
    // model catalog. Known values may be replaced on provider switch; custom
    // values remain user-owned. Hold pre-fill until classification completes.
    container.dataset.acfModelDirty = 'false';
    container.dataset.acfModelClassifying = modelVal ? 'true' : 'false';
    if (modelVal) _classifyLoadedACFModel(container, agentTypeVal, modelVal);

    // Any user keystroke / paste / clear marks the field dirty so subsequent
    // agent-type changes won't clobber it. Guard with e.isTrusted so our
    // programmatic assignments (pre-fill, setAgentConfig) don't trip it.
    const modelInput = container.querySelector('.acf-model');
    if (modelInput) {
        modelInput.addEventListener('input', (e) => {
            if (e.isTrusted) {
                container.dataset.acfModelClassifying = 'false';
                container.dataset.acfModelDirty = 'true';
            }
        });
    }

    // Populate datalist + apply visibility + pre-fill for the initial agent type.
    _syncACFModelField(container);
    _checkAgentCLI(container.querySelector('.acf-agent-type')?.value || 'claude', container.querySelector('.acf-cli-warning'));
    container.querySelector('.acf-agent-type')?.addEventListener('change', () => {
        const agentType = container.querySelector('.acf-agent-type')?.value || 'claude';
        _checkAgentCLI(agentType, container.querySelector('.acf-cli-warning'));
        const permMode = container.querySelector('.acf-permission-mode')?.value || 'default';
        const flagsEl = container.querySelector('.acf-flags');
        if (flagsEl) flagsEl.value = _stripPermFlags(_normalizePermFlagsForAgent(flagsEl.value, agentType, permMode));
        _syncACFAutoPermissions(container);
        _syncACFModelField(container);
    });
}

// _syncACFModelField reconciles the Model input with the currently selected
// agent type:
//   • fills the <datalist> with the canonical options for that type,
//   • hides the field entirely for the terminal agent,
//   • pre-fills the input with the user's default for that type, but only
//     when the user hasn't touched the field (dirty flag off).
function _syncACFModelField(container) {
    if (!container) return;
    const typeSel = container.querySelector('.acf-agent-type');
    const modelWrap = container.querySelector('.acf-model-wrap');
    const modelInput = container.querySelector('.acf-model');
    const datalist = container.querySelector('datalist[id^="acf-models-"]');
    const agentType = typeSel?.value || 'claude';
    if (modelWrap) {
        modelWrap.style.display = (agentType === 'terminal') ? 'none' : '';
    }
    if (datalist) {
        _getAgentModels(_acfServer(container)).then(models => _fillModelDatalist(datalist, agentType, models));
    }
    if (!modelInput) return;
    if (container.dataset.acfModelClassifying === 'true' || container.dataset.acfModelDirty === 'true') return; // user owns or we are classifying the value
    if (agentType === 'terminal') {
        modelInput.value = '';
        return;
    }
    _getDefaultModels(_acfServer(container)).then(defaults => {
        // If the user started typing during the fetch, don't clobber their input.
        if (container.dataset.acfModelDirty === 'true' || container.dataset.acfModelClassifying === 'true') return;
        // Re-check the agent type — the user may have switched again mid-fetch.
        const currentType = typeSel?.value || 'claude';
        if (currentType === 'terminal') {
            modelInput.value = '';
            return;
        }
        modelInput.value = defaults[currentType] || '';
    });
}
window.renderAgentConfigForm = renderAgentConfigForm;

/**
 * Get current values from an AgentConfigForm.
 * @param {string} containerId - The container ID
 * @returns {object} { name, agentType, model, prompt, flags, capabilities }
 */
function getAgentConfig(containerId) {
    const container = document.getElementById(containerId);
    if (!container) return {};
    const uid = container.dataset.acfUid;
    const agentType = container.querySelector('.acf-agent-type')?.value || 'claude';
    const permMode = container.querySelector('.acf-permission-mode')?.value || 'default';
    let flags = _stripPermFlags(container.querySelector('.acf-flags')?.value.trim() || '');
    if (permMode && permMode !== 'default') {
        flags = _normalizePermFlagsForAgent(flags, agentType, permMode);
    }

    // Terminal agents never carry a model — the field is hidden in the UI.
    const rawModel = container.querySelector('.acf-model')?.value.trim() || '';
    const model = agentType === 'terminal' ? '' : rawModel;

    return {
        name: container.querySelector('.acf-name')?.value.trim() || '',
        agentType,
        model,
        prompt: container.querySelector('.acf-prompt')?.value.trim() || '',
        flags,
        capabilities: uid ? _getPermissions(`${uid}-perms`) : null,
    };
}
window.getAgentConfig = getAgentConfig;

/**
 * Set/pre-fill values in an AgentConfigForm.
 * @param {string} containerId - The container ID
 * @param {object} values - { name, agentType, model, prompt, flags, capabilities }
 */
function setAgentConfig(containerId, values) {
    const container = document.getElementById(containerId);
    if (!container) return;
    const uid = container.dataset.acfUid;
    const v = values || {};

    const nameEl = container.querySelector('.acf-name');
    if (nameEl) nameEl.value = v.name || '';
    const typeEl = container.querySelector('.acf-agent-type');
    if (typeEl) typeEl.value = v.agentType || v.agent_type || _settingsFor(_acfServer(container)).default_agent_type || 'claude';
    const modelEl = container.querySelector('.acf-model');
    if (modelEl) {
        modelEl.value = v.model || '';
        container.dataset.acfModelClassifying = v.model ? 'true' : 'false';
        container.dataset.acfModelDirty = 'false';
        if (v.model) _classifyLoadedACFModel(container, typeEl?.value || 'claude', v.model);
    }
    const promptEl = container.querySelector('.acf-prompt');
    if (promptEl) promptEl.value = v.prompt || '';
    const flagsEl = container.querySelector('.acf-flags');
    if (flagsEl) flagsEl.value = _stripPermFlags(v.flags || '');
    const permModeEl = container.querySelector('.acf-permission-mode');
    if (permModeEl) {
        // Extract permission mode from flags if present, otherwise use default from settings
        const flagMode = _permissionModeFromFlags(v.flags || '');
        const defaultMode = _settingsFor(_acfServer(container)).default_permission_mode || 'bypassPermissions';
        if (flagMode) {
            permModeEl.value = flagMode;
        } else if (_hasPermFlag(v.flags || '')) {
            // Legacy --dangerously-skip-permissions flag → map to bypassPermissions
            permModeEl.value = 'bypassPermissions';
        } else if (Object.prototype.hasOwnProperty.call(v, 'flags')) {
            permModeEl.value = 'default';
        } else {
            permModeEl.value = defaultMode;
        }
    }

    if (uid) _setPermissions(`${uid}-perms`, v.capabilities || null);

    // Re-sync Model combo and prerequisite warning after a preset/type change.
    _syncACFModelField(container);
    _checkAgentCLI(typeEl?.value || 'claude', container.querySelector('.acf-cli-warning'));
}
window.setAgentConfig = setAgentConfig;

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
window._applyACFPresetFor = _applyACFPresetFor;
window._applyACFPresetValue = (value, containerId) => _applyACFPresetFor(containerId, value);

// Default team: first 3 presets
const DEFAULT_TEAM_PRESETS = AGENT_PRESETS.slice(0, 4);

/** Worktree option is offered when the working directory is a git repo ON THE TARGET SERVER. */
async function _checkTeamGitDir() {
    const server = _effectiveServer(_launchServer);
    const dirInput = document.getElementById("launch-team-dir");
    const dir = dirInput ? dirInput.value.trim() : '';
    const worktreeOpt = document.getElementById('team-worktree-option');
    if (!dir || !worktreeOpt) return;
    try {
        const resp = await serverFetch(server, `/api/filesystem/is-git?path=${encodeURIComponent(dir)}`);
        const data = await resp.json();
        if (server !== _effectiveServer(_launchServer)) return;
        worktreeOpt.style.display = data.is_git ? '' : 'none';
    } catch { worktreeOpt.style.display = 'none'; }
}

/** The target server's board names and labels, to say before launching that one exists (#3536). */
async function _loadExistingTeamBoards() {
    const server = _effectiveServer(_launchServer);
    _existingTeamBoardNames = new Set();
    try {
        const resp = await serverFetch(server, "/api/board/projects");
        const projects = await resp.json();
        if (server !== _effectiveServer(_launchServer) || !Array.isArray(projects)) return;
        for (const p of projects) {
            if (p.project) _existingTeamBoardNames.add(String(p.project).trim().toLowerCase());
            if (p.board_label) _existingTeamBoardNames.add(String(p.board_label).trim().toLowerCase());
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
    _existingTeamBoardNames = new Set();
    _validateTeamName();
    const tapEl = document.getElementById("team-permission-mode");
    if (tapEl) {
        tapEl.value = _settingsFor(_effectiveServer(_launchServer)).default_permission_mode || 'bypassPermissions';
        _updatePermModeDescription(tapEl);
    }
    _teamAgentCounter = 0;
    const list = document.getElementById("team-agents-list");
    list.innerHTML = "";

    // Pre-fill working dir from the target server's settings
    const s = _settingsFor(_effectiveServer(_launchServer));
    const dirInput = document.getElementById("launch-team-dir");
    if (_launchServer !== LOCAL_SERVER) _resetDirInputs(['launch-team-dir'], _launchServer, s, { switching: true });
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
    if (!_skipDefaultTeamAgents) {
        for (const preset of DEFAULT_TEAM_PRESETS) {
            _addTeamAgent(preset.name, preset.prompt, preset.capabilities, preset.agent_type, preset.model);
        }
    }
    _skipDefaultTeamAgents = false;

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
window._launchTeamPreset = function(templateName) {
    const modal = document.getElementById('quick-launch-modal');
    if (!modal) return;
    document.getElementById('quick-launch-template').value = templateName;
    document.getElementById('quick-launch-title').textContent = `Launch ${templateName}`;
    document.getElementById('quick-launch-board-name').value = '';
    document.getElementById('quick-launch-board-name').placeholder = boardNameSlug(templateName);
    _serverSettings.clear();
    _quickServer = LOCAL_SERVER;
    _resetDirInputs(['quick-launch-workdir'], LOCAL_SERVER, {}, { switching: false });
    _renderServerPicker('quick');
    const quickBrowser = document.getElementById('quick-launch-browser');
    if (quickBrowser) quickBrowser.style.display = 'none';
    modal.style.display = '';
};

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

    const server = _effectiveServer(_quickServer);
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
    _teamAgentCounter = 0;
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
window._loadTeamTemplate = _loadTeamTemplate;

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

    const templates = _getSavedTeamTemplates();
    const idx = templates.findIndex(t => t.name === templateName);
    const entry = { name: templateName, agents: _pendingSaveTemplateAgents, flags: _pendingSaveTemplateFlags };
    if (idx >= 0) templates[idx] = entry; else templates.push(entry);
    await _setSavedTeamTemplates(templates);
    showToast(`Saved template "${templateName}"`);
    _renderTeamTemplateSelector();
    _hideSaveTemplateModal();
}
window._confirmSaveTemplate = _confirmSaveTemplate;

async function _deleteTeamTemplate(name) {
    const templates = _getSavedTeamTemplates().filter(t => t.name !== name);
    await _setSavedTeamTemplates(templates);
    showToast(`Deleted template "${name}"`);
    _renderTeamTemplateSelector();
}
window._deleteTeamTemplate = _deleteTeamTemplate;

function _truncatePrompt(text, maxLen) {
    if (!text || text.length <= maxLen) return text || "";
    return text.substring(0, maxLen) + "\u2026";
}

function _addTeamAgent(defaultName, defaultPrompt, defaultCapabilities, defaultAgentType, defaultModel, defaultFlags) {
    _teamAgentCounter++;
    const idx = _teamAgentCounter;
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
window._moveTeamAgent = _moveTeamAgent;

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
    if (_existingTeamBoardNames.has(value.toLowerCase())) {
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

    const server = _effectiveServer(_launchServer);
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
                        <p style="color:var(--text-secondary);font-size:13px;margin:8px 0">${successCount} launched successfully, ${failed.length} failed:</p>
                        <ul style="color:var(--error);font-size:13px;line-height:1.6;margin:8px 0 16px;padding-left:20px">${failList}</ul>
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

// ── Resume Modal ───────────────────────────────────────────────────────────

export async function showResumeModal() {
    if (!state.currentSession || state.currentSession.type !== "history") {
        showToast("No history session selected", true);
        return;
    }

    const sessionId = state.currentSession.name;
    const historyEntry = state.historySessionsList.find(s => s.session_id === sessionId);
    // The list entry, else the loaded transcript (a /chats/<id> link that is not
    // in the list); /resume-info below overrides both. Never guess a type.
    let agentType = historyEntry?.source_type || state.currentSession.agent_type || "";

    const dirInput = document.getElementById("resume-dir");
    const boardRow = document.getElementById("resume-board-row");
    const boardNameEl = document.getElementById("resume-board-name");

    // Fetch resume metadata (working dir, board, display name) from the server
    let resumeDir = "";
    let boardName = "";
    let displayName = "";
    let resumeSessionId = sessionId;
    try {
        const resp = await fetch(`/api/sessions/history/${encodeURIComponent(sessionId)}/resume-info`);
        if (resp.ok) {
            const info = await resp.json();
            if (info.working_dir) resumeDir = info.working_dir;
            if (info.board_name) boardName = info.board_name;
            if (info.agent_type) agentType = info.agent_type;
            if (info.resume_session_id) resumeSessionId = info.resume_session_id;
            if (info.display_name) displayName = info.display_name;
        }
    } catch (_) { /* best-effort */ }
    if (!agentType) {
        showToast("Can't resume: this session's agent type is unknown", true);
        return;
    }

    // Pre-fill working directory: saved working dir > settings default > coral root
    const defaultDir = resumeDir || state.settings?.default_working_dir || dirInput.dataset.coralRoot || "";
    dirInput.value = defaultDir;

    // Show board reconnect option if the session was on a board
    if (boardName) {
        boardNameEl.textContent = keyLabel(boardName); // its label; the key rejoins it (#3536)
        boardNameEl.dataset.boardKey = boardName;
        boardRow.style.display = "";
        document.getElementById("resume-board-check").checked = true;
    } else {
        boardRow.style.display = "none";
        delete boardNameEl.dataset.boardKey; // never rejoin the previous chat's board
    }

    // Store agent type and display name for the launch function
    const modal = document.getElementById("resume-modal");
    modal.dataset.agentType = agentType;
    modal.dataset.displayName = displayName || historyEntry?.display_name || "";
    modal.dataset.resumeSessionId = resumeSessionId;
    modal.style.display = "flex";
}

export function hideResumeModal() {
    document.getElementById("resume-modal").style.display = "none";
    // Hide browser if open
    const browser = document.getElementById("resume-dir-browser");
    if (browser) browser.style.display = "none";
}

export async function resumeLaunchNew() {
    if (!state.currentSession || state.currentSession.type !== "history") {
        showToast("No history session selected", true);
        return;
    }

    const sessionId = state.currentSession.name;
    const dir = document.getElementById("resume-dir").value.trim();
    if (!dir) {
        showToast("Working directory is required", true);
        return;
    }

    const modal = document.getElementById("resume-modal");
    const agentType = modal.dataset.agentType;
    if (!agentType) {
        showToast("Can't resume: this session's agent type is unknown", true);
        return;
    }
    const resumeSessionId = modal.dataset.resumeSessionId || sessionId;
    const displayName = modal.dataset.displayName || "";
    const boardCheck = document.getElementById("resume-board-check");
    const boardNameEl = document.getElementById("resume-board-name");
    const boardName = (boardCheck && boardCheck.checked && boardNameEl) ? (boardNameEl.dataset.boardKey || "") : "";

    const btn = document.getElementById("btn-resume-launch");
    if (btn) { btn.disabled = true; btn.textContent = "Resuming..."; }

    try {
        const payload = {
            working_dir: dir,
            agent_type: agentType,
            resume_session_id: resumeSessionId,
        };
        if (displayName) payload.display_name = displayName;
        if (boardName) payload.board_id = payload.board_name = boardName; // rejoins that board (#3536)

        const resp = await fetch("/api/sessions/launch", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
        });
        const result = await resp.json();
        if (result.error) {
            showToast(result.error, true);
        } else {
            recordRecentDir(dir);
            showToast(`Resumed session as new agent`);
            hideResumeModal();
            setTimeout(loadLiveSessions, 2000);
        }
    } catch (e) {
        showToast("Failed to resume session", true);
        console.error("resumeLaunchNew error:", e);
    } finally {
        if (btn) { btn.disabled = false; btn.textContent = "Resume"; }
    }
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
        window._databaseEncryptionStatus = status;
        renderDatabaseEncryptionStatus(status);
        return status;
    } catch (_) {
        window._databaseEncryptionStatus = null;
        renderDatabaseEncryptionStatus(null, true);
        return null;
    }
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
    window._databaseEncryptionStatus = status;
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
async function loadArtifactStoreSize() {
    const out = document.getElementById('settings-artifact-store-size');
    const input = document.getElementById('settings-artifact-store-max');
    if (!out || !input) return;
    try {
        const r = await fetch('/api/system/artifact-store');
        if (!r.ok) throw new Error(String(r.status));
        const d = await r.json();
        input.value = d.max_bytes > 0 ? String(Math.round(d.max_bytes / GIB)) : '0';
        input.dataset.loaded = '1'; // only a limit read from Coral is saved back
        const lines = [
            `Artifacts: ${_gib(d.bytes)} in ${Number(d.objects).toLocaleString()} file${d.objects === 1 ? '' : 's'}${d.max_bytes > 0 ? ` (limit ${_gib(d.max_bytes)})` : ''}`,
            `Saved task diffs: ${_gib(d.task_diffs?.bytes || 0)} in ${Number(d.task_diffs?.files || 0).toLocaleString()} files (not limited)`,
            `Saved session diffs: ${_gib(d.session_diffs?.bytes || 0)} in ${Number(d.session_diffs?.files || 0).toLocaleString()} files (not limited)`,
        ];
        out.replaceChildren(...lines.flatMap((l, i) => (i ? [document.createElement('br'), l] : [l])));
    } catch {
        out.textContent = "Couldn't read the store's size.";
    }
    loadStorageDetail();
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
            `By age: artifacts ${age('objects')}`,
            `task diffs ${age('task_diffs')}`,
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

function pruneSummary(rep) {
    const n = Object.values(rep.kinds || {}).reduce((a, k) => a + (k.count || 0), 0);
    return { n, text: Object.entries(rep.kinds || {}).map(([k, v]) => `${v.count} ${k} (${_gib(v.bytes)})`).join(', ') };
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
    window.showConfirmModal?.(`${verb} saved data`, `${verb} ${s.text}, ${what}`, async () => {
        result.textContent = `${verb === 'Shrink' ? 'Shrinking' : 'Pruning'}…`;
        try {
            const done = await pruneRequest({ ...body, dry_run: false });
            result.textContent = `${verb === 'Shrink' ? 'Saved' : 'Freed'} ${_gib(done.freed)}: ${done.removed} done${done.skipped ? `, ${done.skipped} left alone because they changed` : ''}${done.errors ? `, ${done.errors} errors` : ''}.`;
            loadArtifactStoreSize();
        } catch (e) {
            result.dataset.state = 'error';
            result.textContent = `Couldn't finish: ${e.message}`;
        }
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

/** The limit to save, in bytes ({} when the field isn't on this page). */
function artifactStoreMaxSetting() {
    const input = document.getElementById('settings-artifact-store-max');
    // Not on this page, or never read from Coral (a failed load must not reset a limit to 0).
    if (!input || isCloud() || input.dataset.loaded !== '1') return {};
    const gib = Math.max(0, Math.floor(Number(input.value) || 0));
    return { artifact_store_max_bytes: String(gib * GIB) };
}

export async function showSettingsModal() {
    // Focus moves in on open, Tab stays inside, and focus comes back on close (accessibility pass).
    attachDialogFocus(document.getElementById('settings-modal'));
    importOnce('./terminal_backend.js').then(m => m.loadTerminalBackendRow()).catch(() => {}); // #3634
    const s = state.settings || {};
    // The machine-only parts of this dialog (privacy, remote access, database encryption, connecting to a hub) do not
    // exist on the hub: the cloud dialog shows only the per-user settings (applyCloudSettingsDialog below).
    if (!isCloud()) {
        const telemetry = document.getElementById('settings-telemetry-enabled');
        if (telemetry) telemetry.checked = settingBool(s.telemetry_enabled, true);
        loadArtifactStoreSize(); // the store's size next to its limit (#3553)
        wireStorageActions(); // free space and automatic cleanup (#3554)
        const autoAttach = document.getElementById('settings-board-auto-attach');
        // Off unless the Operator turned it on (#3563); an empty value is unset.
        if (autoAttach) autoAttach.checked = settingBool(s.board_auto_attach || undefined, false);
        const autoResume = document.getElementById('settings-auto-resume');
        // Off unless the Operator turned it on (#3613).
        if (autoResume) autoResume.checked = settingBool(s.auto_resume_after_api_error || undefined, false);
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
        fetch('/api/system/api-key').then(r => r.ok ? r.json() : null).then(data => {
            _setApiKey(apiKeyInput, data && data.key, 'Not configured');
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
        groupByTeamCheck.checked = localStorage.getItem('coral-group-by-team') !== 'false';
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
    if (updateCheck) updateCheck.checked = localStorage.getItem("coral-update-check-enabled") !== "false";

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
    localStorage.setItem("coral-update-check-enabled", checkUpdates ? "true" : "false");
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
        ...artifactStoreMaxSetting(),
        ...retentionSettings(),
        debug_input_log: debugInputLog,
        remote_access_enabled: remoteAccessEnabled,
    };

    // The hub accepts only the per-user keys (anything else refuses the whole request); the machine ones are hidden.
    if (isCloud()) payload = filterSettingsPayload(payload).kept;

    const oldGitDiffMode = state.settings?.git_diff_mode || 'previous_commit';

    try {
        if (!isCloud()) await saveDatabaseEncryptionSettings();
        const response = await fetch("/api/settings", {
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

        showToast("Settings saved");
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

// ── Default Prompts Modal ──────────────────────────────────────────

// Cached default prompts fetched from the backend API.
// Populated on first open of the modal; used for "Reset to Default".
let _defaultPrompts = null;

async function _ensureDefaultPrompts() {
    if (_defaultPrompts) return _defaultPrompts;
    try {
        const res = await fetch("/api/settings/default-prompts");
        _defaultPrompts = await res.json();
    } catch {
        // Do not keep a second, abbreviated prompt contract here. The runtime
        // defaults remain authoritative; an unavailable endpoint is explicit.
        _defaultPrompts = {
            unavailable: true,
            default_prompt_orchestrator: '',
            default_prompt_worker: '',
            team_reminder_orchestrator: '',
            team_reminder_worker: '',
        };
    }
    return _defaultPrompts;
}

export async function showDefaultPromptsModal() {
    const defaults = await _ensureDefaultPrompts();
    if (defaults.unavailable) {
        showToast("Default prompts are unavailable; reload and try again.", true);
        return;
    }
    const s = state.settings || {};
    const orchEl = document.getElementById("settings-prompt-orchestrator");
    const workerEl = document.getElementById("settings-prompt-worker");
    if (orchEl) orchEl.value = s.default_prompt_orchestrator || defaults.default_prompt_orchestrator;
    if (workerEl) workerEl.value = s.default_prompt_worker || defaults.default_prompt_worker;
    const teamOrchEl = document.getElementById("settings-team-reminder-orchestrator");
    const teamWorkerEl = document.getElementById("settings-team-reminder-worker");
    if (teamOrchEl) teamOrchEl.value = s.team_reminder_orchestrator || defaults.team_reminder_orchestrator;
    if (teamWorkerEl) teamWorkerEl.value = s.team_reminder_worker || defaults.team_reminder_worker;
    document.getElementById("default-prompts-modal").style.display = "flex";
}

export function hideDefaultPromptsModal() {
    document.getElementById("default-prompts-modal").style.display = "none";
}

export async function resetDefaultPrompt(type) {
    const defaults = await _ensureDefaultPrompts();
    const mapping = {
        orchestrator: ["settings-prompt-orchestrator", "default_prompt_orchestrator"],
        worker: ["settings-prompt-worker", "default_prompt_worker"],
        team_orchestrator: ["settings-team-reminder-orchestrator", "team_reminder_orchestrator"],
        team_worker: ["settings-team-reminder-worker", "team_reminder_worker"],
    };
    const entry = mapping[type];
    if (entry) {
        const el = document.getElementById(entry[0]);
        if (el) el.value = defaults[entry[1]];
    }
}

export async function saveDefaultPrompts() {
    const defaults = await _ensureDefaultPrompts();
    const orchValue = document.getElementById("settings-prompt-orchestrator")?.value || "";
    const workerValue = document.getElementById("settings-prompt-worker")?.value || "";
    const teamOrchValue = document.getElementById("settings-team-reminder-orchestrator")?.value || "";
    const teamWorkerValue = document.getElementById("settings-team-reminder-worker")?.value || "";

    // Save empty string when value matches default — so future default updates are picked up
    const payload = {
        default_prompt_orchestrator: orchValue === defaults.default_prompt_orchestrator ? "" : orchValue,
        default_prompt_worker: workerValue === defaults.default_prompt_worker ? "" : workerValue,
        team_reminder_orchestrator: teamOrchValue === defaults.team_reminder_orchestrator ? "" : teamOrchValue,
        team_reminder_worker: teamWorkerValue === defaults.team_reminder_worker ? "" : teamWorkerValue,
    };

    try {
        await fetch("/api/settings", {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
        });
        state.settings = { ...state.settings, ...payload };
        showToast("Default prompts saved");
    } catch (e) {
        showToast("Failed to save prompts", true);
    }

    hideDefaultPromptsModal();
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

// Attach input listeners for flag sync. This module loads on first use (#3140),
// usually long after DOMContentLoaded, so attach at once when the page is parsed.
function _attachFlagSync() {
    for (const id of ["launch-flags", "restart-flags", "job-modal-flags", "team-flags"]) {
        const el = document.getElementById(id);
        if (el) el.addEventListener("input", () => _syncFlagButtons(id));
    }
}
if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", _attachFlagSync);
else _attachFlagSync();


// ── Mobile Connect Modal ─────────────────────────────────────────────────

// The mobile QR dialog loads on first use (#3140).
window._showMobileConnectModal = (...args) => importOnce('./mobile_connect.js', 'the mobile connect dialog').then(m => m.showMobileConnectModal(...args), () => {});
window._hideMobileConnectModal = (...args) => importOnce('./mobile_connect.js').then(m => m.hideMobileConnectModal(...args), () => {});

// The dialogs' generated markup (#3645): data-on-* names, never code. Values are checked; a bad
// one does nothing. (The job dialog's own flag buttons and agent select register in app.js: they
// exist before this module loads.)
const teamRow = el => el.closest('.team-agent-row');
registerActions({
    'team-generated-review': () => window._reviewGeneratedTeam(),
    'info-copy': el => {
        const cmd = textOf(el.dataset.cmd, 8192);
        if (!cmd) return;
        navigator.clipboard.writeText(cmd);
        el.textContent = 'Copied!';
        setTimeout(() => { el.textContent = 'Copy'; }, 1500);
    },
    'preset-pick': el => {
        const v = textOf(el.value);
        if (v && el.dataset.kind === 'agent') window._selectAgentPreset(v);
        else if (v && el.dataset.kind === 'acf') { const t = tokenOf(el.dataset.target); if (t) window._applyACFPresetValue(v, t); }
        el.value = '';
    },
    'perm-chip-toggle': el => window._togglePermChip(el),
    'perm-mode-describe': el => window._updatePermModeDescription(el),
    'perms-toggle': el => { const t = tokenOf(el.dataset.target); if (t) window._togglePermissions(t); },
    'team-agent-open': el => { const r = teamRow(el); if (r) window.showTeamAgentModal(r); },
    'team-agent-edit': (el, event) => { event.stopPropagation(); const r = teamRow(el); if (r) window.showTeamAgentModal(r); },
    'team-agent-move': (el, event) => { event.stopPropagation(); if (el.dataset.dir === 'up' || el.dataset.dir === 'down') window._moveTeamAgent(el, el.dataset.dir === 'up' ? -1 : 1); },
    'team-agent-remove': (el, event) => { event.stopPropagation(); teamRow(el)?.remove(); },
});
