/* Moved out of modals.js as it was (#4684): The Launch dialog itself: choosing what to launch, opening it, the hub's server picker, the directory resets on a server change, Launch for an agent or a terminal, and the board select. No behaviour change. */
import { importOnce } from './import_once.js';
import { state } from './state.js';
import { showToast, escapeHtml, escapeAttr, boardNameProblem } from './utils.js';
import { blockedReason } from './hub_access.js';
import { loadLiveSessions } from './api.js';
import { recordRecentDir, setDirServerResolver } from './browser.js';
import { serverFetch, identityKey, isHub, normServer, LOCAL_SERVER, keyLabel, noteBoardLabels } from './server_base.js';
import { pickerVisible, pickerOptions, serverLabel, coerceSelection, bareBoardName } from './launch_server.js';
const { launch } = await importOnce('./launch_state.js');
const { _effectiveServer, _loadServerSettings, _serverSettings, _settingsFor } = await importOnce('./launch_models.js');
const { _normalizePermFlagsForAgent, _updatePermModeDescription } = await importOnce('./permission_flags.js');
const { _checkAgentCLI } = await importOnce('./cli_check.js');
const { _syncACFModelField, getAgentConfig, renderAgentConfigForm, cliWarningOf } = await importOnce('./agent_config_form.js');
const { _checkPermissionFlag } = await importOnce('./cli_dialogs.js');
const { _checkTeamGitDir, _initTeamForm, _loadExistingTeamBoards } = await importOnce('./team_dialogs.js');
const { _initAgentPresets } = await importOnce('./persona_io.js');
const { markFieldInvalid, _launchBlocked, _launchOn, _refreshAfterLaunch, _resetDirInputs, _showLaunchStep, _syncMobileLaunchSections, hideLaunchModal, setLaunchMore } = await importOnce('./launch_core.js');
const { initSectionDialog, showSection } = await importOnce('./section_dialog.js');

function _selectLaunchType(type) {
    if (type === "back") {
        launch.mode = null;
        _showLaunchStep("chooser");
        return;
    }
    launch.mode = type;
    if (type === "agent") setLaunchMore(false); // New Agent always opens on the folder; its sections are behind "More options" (#5595)
    _showLaunchStep(type);
    if (type === "agent") {
        _loadAgentBoardProjects();
        _initAgentPresets();
        // Render agent config form
        renderAgentConfigForm('launch-agent-acf', {
            defaultNote: true,
            showPreset: false,
            showName: false,
            cliSlot: 'launch-cli-slot', // a missing CLI is said under the folder, not only inside More options (#5835)
            sections: true, // New Agent is a sectioned dialog (#4950): Workspace, then the form's own sections, the board in Advanced
            adopt: { advanced: 'launch-advanced-extra' },
            value: { agentType: _settingsFor(_effectiveServer(launch.server)).default_agent_type || 'claude' },
        });
        document.getElementById('launch-dir')?.focus(); // the one thing most launches need: the folder
    } else if (type === "team") {
        _initTeamForm();
        const shell = document.querySelector('#launch-step-team .settings-shell');
        if (shell) { initSectionDialog(shell); showSection(shell, 'members'); } // always opens on Members; the name, which every launch needs, is above the sections (#5589 finding 6), and the keyboard starts in it
        document.getElementById('team-board-name')?.focus();
    } else {
        document.getElementById("launch-terminal-name").focus();
    }
}
window._selectLaunchType = _selectLaunchType;
window._toggleLaunchMore = () => setLaunchMore(document.getElementById('launch-agent-more')?.hidden === true);

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
            _checkAgentCLI(c.querySelector('.acf-agent-type').value || 'claude', cliWarningOf(c));
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
        markFieldInvalid(document.getElementById(launch.mode === "terminal" ? "launch-terminal-dir" : "launch-dir"), "Working directory is required");
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
        if (config.thinking !== undefined) payload.thinking = config.thinking; // chosen on the form (#4969)

        const boardSelect = document.getElementById("launch-board-select");
        if (boardSelect && boardSelect.value) {
            if (boardSelect.value === "__new__") {
                const boardName = document.getElementById("launch-board-name").value.trim();
                if (boardName) {
                    const problem = boardNameProblem(boardName);
                    if (problem) { showToast(problem, true); markFieldInvalid(document.getElementById("launch-board-name"), problem); return; }
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
            if (normServer(server) === LOCAL_SERVER) _openLaunched(result, type);
        }
    } catch (e) {
        showToast("Failed to launch session", true);
    } finally {
        if (launchBtn) { launchBtn.disabled = false; launchBtn.textContent = 'Launch'; }
    }
}

/** Open what was just launched (#5835): a person who pressed Launch expects to land in the new agent, not on the home screen with a row to find (the first run's biggest drop:
 *  launched, never prompted). The list is read first so the row exists; the composer is focused by selecting it. A failure leaves the toast and the row, as before. */
async function _openLaunched(result, type) {
    try {
        await loadLiveSessions();
        const { selectLiveSession } = await importOnce('./sessions.js');
        await selectLiveSession(result.session_name, type, result.session_id, LOCAL_SERVER);
    } catch { /* the toast and the list row are still there */ }
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
