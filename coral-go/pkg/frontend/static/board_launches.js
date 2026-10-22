/* Moved out of modals.js as it was (#4684): The launches that start from a board or a folder group: Add agent (to a board or standalone), the Terminal, and the default agent. No behaviour change. */
import { importOnce } from './import_once.js';
import { state } from './state.js';
import { showToast } from './utils.js';
import { splitKey, isHub, normServer, LOCAL_SERVER, keyLabel } from './server_base.js';
import { serverLabel, bareBoardName } from './launch_server.js';
const { launch } = await importOnce('./launch_state.js');
const { _effectiveServer, _loadServerSettings, _serverSettings, _settingsFor } = await importOnce('./launch_models.js');
const { defaultPermissionMode, _getPermissionFlagsForAgentMode, _normalizePermFlagsForAgent } = await importOnce('./permission_flags.js');
const { getAgentConfig, renderAgentConfigForm } = await importOnce('./agent_config_form.js');
const { _checkPermissionFlag } = await importOnce('./cli_dialogs.js');
const { _launchBlocked, _launchOn, _onServerSuffix, _refreshAfterLaunch, _withServerSettings } = await importOnce('./launch_core.js');

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
        const mode = defaultPermissionMode(_settingsFor(server), config.agentType);
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
        if (config.thinking !== undefined) launchBody.thinking = config.thinking; // #4969
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
    const permFlag = _getPermissionFlagsForAgentMode(agentType, defaultPermissionMode(s, agentType));
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
