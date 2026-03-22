/* Modal management: launch and info dialogs */

import { state } from './state.js';
import { showToast, escapeHtml, escapeAttr } from './utils.js';
import { loadLiveSessions } from './api.js';
import { loadBoardProjects } from './message_board.js';
import { getEngineNames, getEngineName, setRendererOverride } from './renderers.js';
import { renderCaptureText, syncPaneWidth } from './capture.js';
import { hideRestartModal } from './controls.js';
import { updateTerminalTheme, getTerminal } from './xterm_renderer.js';

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

// Track which launch mode is active: null (chooser), 'agent', or 'terminal'
let _launchMode = null;

function _showLaunchStep(step) {
    document.getElementById("launch-step-chooser").style.display = step === "chooser" ? "" : "none";
    document.getElementById("launch-step-agent").style.display = step === "agent" ? "" : "none";
    document.getElementById("launch-step-terminal").style.display = step === "terminal" ? "" : "none";
    document.getElementById("launch-step-team").style.display = step === "team" ? "" : "none";
    // Widen the modal for multi-column layouts
    const content = document.querySelector("#launch-modal .modal-content");
    if (content) {
        content.classList.toggle("modal-content-agent-wide", step === "agent");
        content.classList.toggle("modal-content-extra-wide", step === "team");
        content.classList.toggle("modal-content-wide", step !== "team" && step !== "agent");
    }
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
    } else if (type === "team") {
        _initTeamForm();
    } else {
        document.getElementById("launch-terminal-name").focus();
    }
}
window._selectLaunchType = _selectLaunchType;

export function showLaunchModal() {
    _launchMode = null;
    document.getElementById("launch-modal").style.display = "flex";
    _showLaunchStep("chooser");

    // Reset agent form
    document.getElementById("launch-agent-name").value = "";
    document.getElementById("launch-flags").value = "--dangerously-skip-permissions";
    document.getElementById("launch-agent-prompt").value = "";
    document.getElementById("launch-board-name").value = "";
    document.getElementById("launch-board-server").value = "";
    const agentBoardSelect = document.getElementById("launch-board-select");
    if (agentBoardSelect) agentBoardSelect.value = "";
    document.getElementById("launch-new-board-row").style.display = "none";
    document.getElementById("launch-board-server-row").style.display = "none";
    _syncFlagButtons("launch-flags");
    // Clear preset selection and unlock fields
    document.querySelectorAll("#agent-preset-selector .agent-preset-btn").forEach(btn => btn.classList.remove("active"));
    const agentNameInput = document.getElementById("launch-agent-name");
    const agentPromptInput = document.getElementById("launch-agent-prompt");
    agentNameInput.readOnly = false;
    agentPromptInput.readOnly = false;
    agentNameInput.classList.remove("preset-locked");
    agentPromptInput.classList.remove("preset-locked");
    const savePersonaBtn = document.getElementById("agent-save-persona-btn");
    if (savePersonaBtn) savePersonaBtn.style.display = "";

    // Reset terminal form
    document.getElementById("launch-terminal-name").value = "";

    // Reset team form
    document.getElementById("team-agents-list").innerHTML = "";
    document.getElementById("team-board-name").value = "";
    document.getElementById("team-flags").value = "";
    _syncFlagButtons("team-flags");

    // Pre-fill from global settings
    const s = state.settings || {};
    const dirInput = document.getElementById("launch-dir");
    const termDirInput = document.getElementById("launch-terminal-dir");
    if (s.default_working_dir) {
        if (dirInput) dirInput.value = s.default_working_dir;
        if (termDirInput) termDirInput.value = s.default_working_dir;
    }
    const typeSelect = document.getElementById("launch-type");
    if (s.default_agent_type && typeSelect) {
        typeSelect.value = s.default_agent_type;
    }
}

export function hideLaunchModal() {
    document.getElementById("launch-modal").style.display = "none";
}

/**
 * Check if --dangerously-skip-permissions is in flags.
 * If not, show a confirmation dialog. Returns a promise resolving to:
 *   'enable' — user wants to add the flag
 *   'skip' — user wants to launch without it
 *   null — user cancelled
 */
function _checkPermissionFlag(flagsStr) {
    return new Promise((resolve) => {
        if (flagsStr && flagsStr.includes('--dangerously-skip-permissions')) {
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
                    Agents may get stuck on permission prompts without <code>--dangerously-skip-permissions</code>. Would you like to enable it?
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
    let dir, type, agentName, flagsStr;

    if (_launchMode === "terminal") {
        dir = document.getElementById("launch-terminal-dir").value.trim();
        type = "terminal";
        agentName = document.getElementById("launch-terminal-name").value.trim();
        flagsStr = "";
    } else {
        dir = document.getElementById("launch-dir").value.trim();
        type = document.getElementById("launch-type").value;
        agentName = document.getElementById("launch-agent-name").value.trim();
        flagsStr = document.getElementById("launch-flags").value.trim();
    }

    if (!dir) {
        showToast("Working directory is required", true);
        return;
    }

    // Permission flag check (skip for terminals)
    if (_launchMode !== "terminal") {
        const permResult = await _checkPermissionFlag(flagsStr);
        if (permResult === null) return;
        if (permResult === 'enable') {
            flagsStr = (flagsStr ? flagsStr + ' ' : '') + '--dangerously-skip-permissions';
            const flagsInput = document.getElementById("launch-flags");
            if (flagsInput) flagsInput.value = flagsStr;
        }
    }

    const payload = { working_dir: dir, agent_type: type };
    if (agentName) payload.display_name = agentName;

    if (flagsStr) {
        payload.flags = flagsStr.split(/\s+/);
    }

    // Agent prompt and message board (only for agent mode)
    if (_launchMode !== "terminal") {
        const prompt = document.getElementById("launch-agent-prompt").value.trim();
        if (prompt) payload.prompt = prompt;

        const boardSelect = document.getElementById("launch-board-select");
        if (boardSelect && boardSelect.value) {
            if (boardSelect.value === "__new__") {
                const boardName = document.getElementById("launch-board-name").value.trim();
                if (boardName) payload.board_name = boardName;
            } else {
                payload.board_name = boardSelect.value;
            }
            const boardServer = document.getElementById("launch-board-server").value.trim();
            if (boardServer) payload.board_server = boardServer;
        }
    }

    try {
        const resp = await fetch("/api/sessions/launch", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
        });
        const result = await resp.json();
        if (result.error) {
            showToast(result.error, true);
        } else {
            showToast(`Launched: ${result.session_name}`);
            hideLaunchModal();
            setTimeout(loadLiveSessions, 2000);
        }
    } catch (e) {
        showToast("Failed to launch session", true);
    }
}

// ── Agent Board Select ────────────────────────────────────────────────────

async function _loadAgentBoardProjects() {
    const select = document.getElementById("launch-board-select");
    if (!select) return;
    select.innerHTML = '<option value="">None</option><option value="__new__">Create new board...</option>';
    try {
        const resp = await fetch("/api/board/projects");
        const projects = await resp.json();
        for (const p of projects) {
            const opt = document.createElement("option");
            opt.value = p.project;
            opt.textContent = p.project;
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

/** Render preset buttons (built-in + saved) into a container. */
function _renderPresetButtons(containerId, onClickFn, opts = {}) {
    const container = document.getElementById(containerId);
    if (!container) return;
    const saved = _getSavedPersonas();

    let html = '';
    for (const preset of AGENT_PRESETS) {
        html += `<button class="agent-preset-btn" data-preset="${escapeAttr(preset.name)}" onclick="${onClickFn}('${escapeAttr(preset.name)}')">${escapeHtml(preset.name)}</button>`;
    }
    if (saved.length > 0) {
        for (const p of saved) {
            html += `<button class="agent-preset-btn agent-preset-saved" data-preset="${escapeAttr(p.name)}" onclick="${onClickFn}('${escapeAttr(p.name)}')">${escapeHtml(p.name)}<span class="agent-preset-delete" onclick="event.stopPropagation(); window._deletePersona('${escapeAttr(p.name)}')">×</span></button>`;
        }
    }
    html += `<button class="agent-preset-btn agent-preset-custom" data-preset="" onclick="${onClickFn}('')">Custom</button>`;
    container.innerHTML = html;
}

/** Find a persona by name in built-in + saved lists. */
function _findPersona(name) {
    return AGENT_PRESETS.find(p => p.name === name) || _getSavedPersonas().find(p => p.name === name);
}

async function _saveCurrentPersona(nameInputId, promptInputId, flagsInputId) {
    const name = document.getElementById(nameInputId).value.trim();
    const prompt = document.getElementById(promptInputId).value.trim();
    const flags = flagsInputId ? document.getElementById(flagsInputId).value.trim() : "";
    if (!name) { showToast("Enter a name before saving", "error"); return; }
    if (AGENT_PRESETS.find(p => p.name === name)) { showToast("Can't overwrite a built-in preset", "error"); return; }

    const saved = _getSavedPersonas();
    const idx = saved.findIndex(p => p.name === name);
    const entry = { name, prompt, flags };
    if (idx >= 0) saved[idx] = entry; else saved.push(entry);
    await _setSavedPersonas(saved);
    showToast(`Saved persona "${name}"`);

    // Re-render all preset containers
    _renderPresetButtons("agent-preset-selector", "window._selectAgentPreset");
    _renderPresetButtons("add-agent-board-presets", "window._selectBoardAgentPreset");
}
window._saveCurrentPersona = _saveCurrentPersona;

async function _deletePersona(name) {
    const saved = _getSavedPersonas().filter(p => p.name !== name);
    await _setSavedPersonas(saved);
    showToast(`Deleted persona "${name}"`);
    _renderPresetButtons("agent-preset-selector", "window._selectAgentPreset");
    _renderPresetButtons("add-agent-board-presets", "window._selectBoardAgentPreset");
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
    _renderPresetButtons("add-agent-board-presets", "window._selectBoardAgentPreset");
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
    const nameInput = document.getElementById("launch-agent-name");
    const promptInput = document.getElementById("launch-agent-prompt");
    const saveBtn = document.getElementById("agent-save-persona-btn");

    if (name) {
        const persona = _findPersona(name);
        if (persona) {
            nameInput.value = persona.name;
            promptInput.value = persona.prompt;
        }
    } else {
        nameInput.value = "";
        promptInput.value = "";
    }

    // Lock fields for built-in presets (they can't be saved/overwritten)
    const isBuiltIn = !!AGENT_PRESETS.find(p => p.name === name);
    nameInput.readOnly = isBuiltIn;
    promptInput.readOnly = isBuiltIn;
    nameInput.classList.toggle("preset-locked", isBuiltIn);
    promptInput.classList.toggle("preset-locked", isBuiltIn);
    if (saveBtn) saveBtn.style.display = isBuiltIn ? "none" : "";

    document.querySelectorAll("#agent-preset-selector .agent-preset-btn").forEach(btn => {
        btn.classList.toggle("active", btn.dataset.preset === name);
    });

    if (!isBuiltIn) nameInput.focus();
}
window._selectAgentPreset = _selectAgentPreset;

// ── Add Agent to Board ───────────────────────────────────────────────────

export function showAddAgentToBoard(boardName, workDir) {
    _showAddAgentModal("board");
    document.getElementById("add-agent-board-name").value = boardName;
    document.getElementById("add-agent-board-workdir").value = workDir;
    document.getElementById("add-agent-board-subtitle").textContent = `Board: ${boardName}`;
    document.getElementById("add-agent-board-flags").value = "--dangerously-skip-permissions";
    _syncFlagButtons("add-agent-board-flags");
}

export function hideAddAgentBoardModal() {
    document.getElementById("add-agent-board-modal").style.display = "none";
}

function _selectBoardAgentPreset(name) {
    const nameInput = document.getElementById("add-agent-board-agent-name");
    const promptInput = document.getElementById("add-agent-board-prompt");

    if (name) {
        const persona = _findPersona(name);
        if (persona) {
            nameInput.value = persona.name;
            promptInput.value = persona.prompt;
        }
    } else {
        nameInput.value = "";
        promptInput.value = "";
    }

    document.querySelectorAll("#add-agent-board-presets .agent-preset-btn").forEach(btn => {
        btn.classList.toggle("active", btn.dataset.preset === name);
    });
    nameInput.focus();
}
window._selectBoardAgentPreset = _selectBoardAgentPreset;

export async function launchAgentToBoard() {
    const boardName = document.getElementById("add-agent-board-name").value;
    const workDir = document.getElementById("add-agent-board-workdir").value;
    const agentName = document.getElementById("add-agent-board-agent-name").value.trim();
    const prompt = document.getElementById("add-agent-board-prompt").value.trim();
    let flagsStr = document.getElementById("add-agent-board-flags").value.trim();

    if (!agentName) {
        showToast("Agent name is required", "error");
        return;
    }

    // Permission flag check
    const permResult = await _checkPermissionFlag(flagsStr);
    if (permResult === null) return;
    if (permResult === 'enable') {
        flagsStr = (flagsStr ? flagsStr + ' ' : '') + '--dangerously-skip-permissions';
        const flagsInput = document.getElementById("add-agent-board-flags");
        if (flagsInput) flagsInput.value = flagsStr;
    }

    const flags = flagsStr ? flagsStr.split(/\s+/) : [];

    try {
        const resp = await fetch("/api/sessions/launch", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
                working_dir: workDir,
                agent_type: "claude",
                display_name: agentName,
                flags,
                prompt,
                board_name: boardName,
            }),
        });
        const result = await resp.json();
        if (result.error) {
            showToast(result.error, "error");
        } else {
            showToast(`Launched ${agentName} on board ${boardName}`);
            hideAddAgentBoardModal();
        }
    } catch (e) {
        showToast("Failed to launch agent", "error");
    }
}

// ── Agent Team ────────────────────────────────────────────────────────────

let _teamAgentCounter = 0;

// Predefined agent presets
const AGENT_PRESETS = [
    {
        name: "Lead Developer",
        prompt: "You are the lead developer. Implement features, write code, and coordinate with the team via the message board.",
    },
    {
        name: "QA Engineer",
        prompt: "You are an expert QA engineer. Review the work of other agents, create test plans, write tests, and ask probing questions about complex areas. Coordinate with the team via the message board.",
    },
    {
        name: "Orchestrator",
        prompt: "You are the orchestrator. Coordinate the team, break down tasks, assign work via the message board, and track progress. Do not write code yourself — delegate to the other agents. Coordinate with the team via the message board.",
    },
    {
        name: "Frontend Dev",
        prompt: "You are a frontend developer. Build UI components, style pages, and ensure a great user experience. Coordinate with the team via the message board.",
    },
    {
        name: "Backend Dev",
        prompt: "You are a backend developer. Build APIs, services, and data models. Coordinate with the team via the message board.",
    },
    {
        name: "DevOps Engineer",
        prompt: "You are a DevOps engineer. Handle CI/CD, infrastructure, deployment, and monitoring. Coordinate with the team via the message board.",
    },
    {
        name: "Security Reviewer",
        prompt: "You are a security reviewer. Audit code for vulnerabilities, review auth flows, and ensure OWASP best practices. Report findings via the message board.",
    },
    {
        name: "Technical Writer",
        prompt: "You are a technical writer. Write documentation, API guides, and READMEs. Coordinate with the team via the message board.",
    },
];

// Default team: first 3 presets
const DEFAULT_TEAM_PRESETS = AGENT_PRESETS.slice(0, 3);

async function _initTeamForm() {
    // Reset form
    document.getElementById("team-board-name").value = "";
    document.getElementById("team-board-server").value = "";
    document.getElementById("team-flags").value = "--dangerously-skip-permissions";
    _syncFlagButtons("team-flags");
    _teamAgentCounter = 0;
    const list = document.getElementById("team-agents-list");
    list.innerHTML = "";

    // Pre-fill working dir from settings
    const s = state.settings || {};
    const dirInput = document.getElementById("launch-team-dir");
    if (s.default_working_dir && dirInput) dirInput.value = s.default_working_dir;
    const typeSelect = document.getElementById("team-agent-type");
    if (s.default_agent_type && typeSelect) typeSelect.value = s.default_agent_type;

    // Fetch existing message board projects for the dropdown
    const select = document.getElementById("team-board-select");
    select.innerHTML = '<option value="__new__">Create new board...</option>';
    try {
        const resp = await fetch("/api/board/projects");
        const projects = await resp.json();
        for (const p of projects) {
            const opt = document.createElement("option");
            opt.value = p.project;
            opt.textContent = p.project;
            select.appendChild(opt);
        }
    } catch (_) {}

    // Show new board name input
    document.getElementById("team-new-board-row").style.display = "";

    // Render team template selector
    _renderTeamTemplateSelector();

    // Show Subgent banner or Remote Server field based on saved settings
    const subgentBanner = document.getElementById("team-subgent-banner");
    const remoteServerRow = document.getElementById("team-remote-server-row");
    const configureHint = document.getElementById("subgent-configure-hint");
    document.getElementById("team-board-server").value = "";

    try {
        const subgentResp = await fetch("/api/settings/subgent");
        const subgentData = await subgentResp.json();
        if (subgentData.key_configured) {
            // Show banner, hide Remote Server field
            if (subgentBanner) {
                subgentBanner.style.display = "";
                const urlSpan = document.getElementById("team-subgent-banner-url");
                if (urlSpan) urlSpan.textContent = subgentData.admin_url || "";
            }
            if (remoteServerRow) remoteServerRow.style.display = "none";
        } else {
            // Hide banner, show Remote Server field with hint
            if (subgentBanner) subgentBanner.style.display = "none";
            if (remoteServerRow) remoteServerRow.style.display = "";
            if (configureHint) configureHint.style.display = "";
        }
    } catch (_) {
        if (subgentBanner) subgentBanner.style.display = "none";
        if (remoteServerRow) remoteServerRow.style.display = "";
    }

    // Add three default agents
    for (const preset of DEFAULT_TEAM_PRESETS) {
        _addTeamAgent(preset.name, preset.prompt);
    }

    document.getElementById("team-board-name").focus();
}

const BUILTIN_TEAM_TEMPLATES = [
    {
        name: "Demo Team",
        builtin: true,
        agents: [
            { name: "Orchestrator", prompt: "You are the orchestrator. Break down the task, assign work to the team via the message board, and track progress. Do not write code yourself — delegate to the other agents. Discuss your plan with the operator before posting assignments." },
            { name: "Lead Developer", prompt: "You are the lead developer. Implement features, write code, and coordinate with the team via the message board. Wait for instructions from the Orchestrator before starting." },
            { name: "QA Engineer", prompt: "You are a QA engineer. Review code, write tests, and verify the work of other agents. Wait for instructions from the Orchestrator before starting." },
        ],
        flags: "",
    },
];

function _renderTeamTemplateSelector() {
    const container = document.getElementById("team-template-selector");
    if (!container) return;
    const saved = _getSavedTeamTemplates();
    const all = [...BUILTIN_TEAM_TEMPLATES, ...saved];
    if (all.length === 0) {
        container.innerHTML = '';
        return;
    }

    let html = '<div class="team-template-row">';
    html += '<span class="team-template-label">Templates:</span>';
    for (const t of all) {
        const isBuiltin = t.builtin;
        const cls = isBuiltin ? "agent-preset-btn" : "agent-preset-btn agent-preset-saved";
        const deleteBtn = isBuiltin ? "" : `<span class="agent-preset-delete" onclick="event.stopPropagation(); window._deleteTeamTemplate('${escapeAttr(t.name)}')">×</span>`;
        html += `<button class="${cls}" onclick="window._loadTeamTemplate('${escapeAttr(t.name)}')">${escapeHtml(t.name)} <span class="team-template-count">${t.agents.length}</span>${deleteBtn}</button>`;
    }
    html += '</div>';
    container.innerHTML = html;
}

function _loadTeamTemplate(name) {
    const all = [...BUILTIN_TEAM_TEMPLATES, ..._getSavedTeamTemplates()];
    const tmpl = all.find(t => t.name === name);
    if (!tmpl) return;

    // Clear current agents and load from template
    _teamAgentCounter = 0;
    document.getElementById("team-agents-list").innerHTML = "";

    for (const agent of tmpl.agents) {
        _addTeamAgent(agent.name, agent.prompt);
    }
    if (tmpl.flags) {
        document.getElementById("team-flags").value = tmpl.flags;
        _syncFlagButtons("team-flags");
    }
    showToast(`Loaded template "${name}"`);
}
window._loadTeamTemplate = _loadTeamTemplate;

async function _saveTeamTemplate() {
    // Collect current agents
    const rows = document.querySelectorAll("#team-agents-list .team-agent-row");
    if (rows.length === 0) { showToast("Add agents before saving", "error"); return; }

    const agents = [];
    for (const row of rows) {
        const name = row.querySelector(".team-agent-name").value.trim();
        const prompt = row.querySelector(".team-agent-prompt").value.trim();
        if (name) agents.push({ name, prompt });
    }
    if (agents.length === 0) { showToast("At least one agent needs a name", "error"); return; }

    const templateName = prompt("Template name:");
    if (!templateName) return;

    const flags = document.getElementById("team-flags").value.trim();
    const templates = _getSavedTeamTemplates();
    const idx = templates.findIndex(t => t.name === templateName);
    const entry = { name: templateName, agents, flags };
    if (idx >= 0) templates[idx] = entry; else templates.push(entry);
    await _setSavedTeamTemplates(templates);
    showToast(`Saved template "${templateName}"`);
    _renderTeamTemplateSelector();
}
window._saveTeamTemplate = _saveTeamTemplate;

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

function _addTeamAgent(defaultName, defaultPrompt) {
    _teamAgentCounter++;
    const idx = _teamAgentCounter;
    const list = document.getElementById("team-agents-list");
    const row = document.createElement("div");
    row.className = "team-agent-row";
    row.dataset.idx = idx;

    const hasContent = !!(defaultName || defaultPrompt);
    const collapsed = hasContent;

    row.innerHTML = `
        <div class="team-agent-card ${collapsed ? '' : 'editing'}">
            <div class="team-agent-summary" onclick="this.closest('.team-agent-card').classList.toggle('editing')">
                <div class="team-agent-summary-left">
                    <span class="team-agent-role-name">${escapeHtml(defaultName || 'New Agent')}</span>
                    <span class="team-agent-prompt-preview">${escapeHtml(_truncatePrompt(defaultPrompt, 200))}</span>
                </div>
                <div class="team-agent-summary-actions">
                    <button class="team-agent-edit-btn" title="Edit" onclick="event.stopPropagation(); this.closest('.team-agent-card').classList.add('editing')">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>
                    </button>
                    <button class="team-agent-remove" onclick="event.stopPropagation(); this.closest('.team-agent-row').remove()" title="Remove">&times;</button>
                </div>
            </div>
            <div class="team-agent-form">
                <div style="display:flex;gap:8px;align-items:end">
                    <label style="flex:1">Name / Role:
                        <input type="text" class="team-agent-name" placeholder="e.g. QA Engineer, Frontend Dev" value="${escapeAttr(defaultName || '')}"
                            oninput="const card=this.closest('.team-agent-row'); card.querySelector('.team-agent-role-name').textContent=this.value||'New Agent'">
                    </label>
                    <div style="flex-shrink:0;text-align:center">
                        <div style="font-size:11px;color:var(--text-secondary);margin-bottom:2px">Icon</div>
                        <button type="button" class="team-agent-icon-btn" onclick="openTeamIconPicker(this)" style="width:36px;height:36px;font-size:18px;border-radius:6px;border:1px solid var(--border);background:var(--bg-tertiary);cursor:pointer" data-icon="">🤖</button>
                        <input type="hidden" class="team-agent-icon" value="">
                    </div>
                </div>
                <label>Behavior Prompt:
                    <textarea class="team-agent-prompt" rows="3" placeholder="Describe this agent's role and behavior..."
                        oninput="const card=this.closest('.team-agent-row'); card.querySelector('.team-agent-prompt-preview').textContent=this.value.substring(0,200)+(this.value.length>200?'\u2026':'')">${escapeHtml(defaultPrompt || '')}</textarea>
                </label>
                <div style="display:flex;gap:6px">
                    <button class="btn btn-small" onclick="browseAgentTemplates(this)" title="Browse community agent templates from aitmpl.com">Browse Templates</button>
                    <button class="btn btn-small team-agent-done-btn" onclick="this.closest('.team-agent-card').classList.remove('editing')">Done</button>
                </div>
            </div>
        </div>
    `;
    list.appendChild(row);

    // Focus the name input if it's a new empty agent
    if (!hasContent) {
        const nameInput = row.querySelector('.team-agent-name');
        if (nameInput) setTimeout(() => nameInput.focus(), 50);
    }
}

function _showAddAgentPicker() {
    _showAddAgentModal("team");
}
window._showAddAgentPicker = _showAddAgentPicker;

function _showAddAgentModal(mode) {
    const modal = document.getElementById("add-agent-board-modal");
    const modeInput = document.getElementById("add-agent-board-mode");
    const titleEl = document.getElementById("add-agent-board-title");
    const subtitleEl = document.getElementById("add-agent-board-subtitle");
    const submitBtn = document.getElementById("add-agent-board-submit");

    modeInput.value = mode;
    document.getElementById("add-agent-board-agent-name").value = "";
    document.getElementById("add-agent-board-prompt").value = "";
    document.getElementById("add-agent-board-flags").value = "";
    _syncFlagButtons("add-agent-board-flags");

    if (mode === "team") {
        titleEl.textContent = "Add Agent to Team";
        subtitleEl.textContent = "";
        submitBtn.textContent = "Add to Team";
        submitBtn.onclick = _addAgentFromModal;
    } else {
        titleEl.textContent = "Add Agent to Board";
        submitBtn.textContent = "Launch";
        submitBtn.onclick = () => launchAgentToBoard();
    }

    _renderPresetButtons("add-agent-board-presets", "window._selectBoardAgentPreset");
    modal.style.display = "flex";
    setTimeout(() => document.getElementById("add-agent-board-agent-name").focus(), 50);
}

function _addAgentFromModal() {
    const name = document.getElementById("add-agent-board-agent-name").value.trim();
    const prompt = document.getElementById("add-agent-board-prompt").value.trim();
    _addTeamAgent(name, prompt);
    hideAddAgentBoardModal();
}

window._addTeamAgent = (name, prompt) => {
    _addTeamAgent(name || "", prompt || "");
};

function _onTeamBoardChange() {
    const select = document.getElementById("team-board-select");
    const newRow = document.getElementById("team-new-board-row");
    newRow.style.display = select.value === "__new__" ? "" : "none";
}
window._onTeamBoardChange = _onTeamBoardChange;

async function launchTeam() {
    // Collect board name
    const boardSelect = document.getElementById("team-board-select");
    let boardName;
    if (boardSelect.value === "__new__") {
        boardName = document.getElementById("team-board-name").value.trim();
        if (!boardName) {
            showToast("Board name is required", true);
            return;
        }
    } else {
        boardName = boardSelect.value;
    }

    const workingDir = document.getElementById("launch-team-dir").value.trim();
    if (!workingDir) {
        showToast("Working directory is required", true);
        return;
    }

    const agentType = document.getElementById("team-agent-type").value;
    let flagsStr = document.getElementById("team-flags").value.trim();

    // Permission flag check
    const permResult = await _checkPermissionFlag(flagsStr);
    if (permResult === null) return;
    if (permResult === 'enable') {
        flagsStr = (flagsStr ? flagsStr + ' ' : '') + '--dangerously-skip-permissions';
        const flagsInput = document.getElementById("team-flags");
        if (flagsInput) flagsInput.value = flagsStr;
    }

    const flags = flagsStr ? flagsStr.split(/\s+/) : [];

    // Collect agent definitions
    const rows = document.querySelectorAll("#team-agents-list .team-agent-row");
    if (rows.length === 0) {
        showToast("Add at least one agent", true);
        return;
    }

    const agents = [];
    for (const row of rows) {
        const name = row.querySelector(".team-agent-name").value.trim();
        const prompt = row.querySelector(".team-agent-prompt").value.trim();
        const icon = row.querySelector(".team-agent-icon")?.value.trim() || "";
        if (!name) {
            showToast("Each agent needs a name", true);
            return;
        }
        const agent = { name, prompt };
        if (icon) agent.icon = icon;
        agents.push(agent);
    }

    const boardServer = document.getElementById("team-board-server").value.trim();
    const payload = {
        board_name: boardName,
        working_dir: workingDir,
        agent_type: agentType,
        flags,
        agents,
    };
    if (boardServer) payload.board_server = boardServer;

    // Subgent config is handled server-side via saved settings (no explicit fields needed)

    try {
        const resp = await fetch("/api/sessions/launch-team", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
        });
        const result = await resp.json();
        if (result.error) {
            showToast(result.error, true);
        } else {
            showToast(`Launched team: ${agents.length} agents on "${boardName}"`);
            hideLaunchModal();
            setTimeout(loadLiveSessions, 2000);
            setTimeout(loadBoardProjects, 4000);
        }
    } catch (e) {
        showToast("Failed to launch team", true);
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
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(name)}/info${qs}`);
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
        document.getElementById("info-git-branch").textContent = info.git_branch || "—";
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
            boardLink.textContent = info.board_name;
            boardLink.style.cssText = "color:var(--accent);cursor:pointer;text-decoration:underline";
            boardLink.onclick = (e) => {
                e.preventDefault();
                hideInfoModal();
                if (window.selectBoardProject) window.selectBoardProject(info.board_name);
            };
            boardVal.innerHTML = "";
            boardVal.appendChild(boardLink);
        } else {
            boardLabel.style.display = "none";
            boardVal.style.display = "none";
        }

        // Subgent status
        const subgentLabel = document.getElementById("info-subgent-label");
        const subgentVal = document.getElementById("info-subgent");
        if (info.subgent_admin_url) {
            subgentLabel.style.display = "";
            subgentVal.style.display = "";
            subgentVal.innerHTML = `<span class="info-subgent-badge connected">Connected</span> <span style="font-size:11px;color:var(--text-muted)">${escapeHtml(info.subgent_admin_url)}</span>`;
        } else {
            subgentLabel.style.display = "none";
            subgentVal.style.display = "none";
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

export function showResumeModal() {
    const list = document.getElementById("resume-session-list");
    list.innerHTML = "";

    if (!state.liveSessions || state.liveSessions.length === 0) {
        list.innerHTML = '<p style="color:var(--text-muted);font-size:13px;text-align:center;padding:16px 0">No live agents available</p>';
        document.getElementById("resume-modal").style.display = "flex";
        return;
    }

    for (const agent of state.liveSessions) {
        const item = document.createElement("div");
        item.className = "resume-session-item";
        const typeBadge = (agent.agent_type || "claude").toLowerCase();
        const goal = agent.summary ? escapeHtml(agent.summary) : '<span style="color:var(--text-muted)">No goal set</span>';
        item.innerHTML = `
            <div class="resume-item-header">
                <span class="resume-item-name">${escapeHtml(agent.name)}</span>
                <span class="badge ${typeBadge}">${typeBadge}</span>
            </div>
            <div class="resume-item-goal">${goal}</div>
        `;
        item.addEventListener("click", () => resumeIntoSession(agent.name, agent.agent_type, agent.session_id));
        list.appendChild(item);
    }

    document.getElementById("resume-modal").style.display = "flex";
}

export function hideResumeModal() {
    document.getElementById("resume-modal").style.display = "none";
}

export async function resumeIntoSession(agentName, agentType, currentSessionId) {
    if (!state.currentSession || state.currentSession.type !== "history") {
        showToast("No history session selected", true);
        return;
    }

    const sessionId = state.currentSession.name;
    const displayType = (agentType || "claude").toLowerCase();

    if (!confirm(`This will kill the current session in "${agentName}" and resume session ${sessionId.substring(0, 8)}... in its place. Continue?`)) {
        return;
    }

    hideResumeModal();

    try {
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(agentName)}/resume`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ session_id: sessionId, agent_type: agentType, current_session_id: currentSessionId }),
        });
        const result = await resp.json();
        if (result.error) {
            showToast(result.error, true);
        } else {
            showToast(`Resumed session in ${agentName}`);
            // Switch to the live session view — session_id will now be the resumed session
            if (window.selectLiveSession) {
                window.selectLiveSession(agentName, displayType, sessionId);
            }
        }
    } catch (e) {
        showToast("Failed to resume session", true);
        console.error("resumeIntoSession error:", e);
    }
}

// ── Settings Modal ────────────────────────────────────────────────────────

export async function loadSettings() {
    try {
        const resp = await fetch("/api/settings");
        const data = await resp.json();
        const s = data.settings || {};
        // Boolean settings are stored as strings in SQLite — coerce them
        if (typeof s.fit_pane_width === "string") {
            s.fit_pane_width = s.fit_pane_width === "True";
        }
        // Default fit_pane_width to true if not yet set
        if (s.fit_pane_width === undefined) {
            s.fit_pane_width = true;
        }
        if (typeof s.notify_needs_input === "string") {
            s.notify_needs_input = s.notify_needs_input === "True";
        }
        if (s.notify_needs_input === undefined) {
            s.notify_needs_input = true;
        }
        // Coerce show_scrollbars
        if (typeof s.show_scrollbars === "string") {
            s.show_scrollbars = s.show_scrollbars === "True";
        }
        if (s.show_scrollbars === undefined) {
            s.show_scrollbars = true;
        }
        state.settings = s;

        // Apply scrollbar visibility
        document.body.classList.toggle('no-scrollbars', !s.show_scrollbars);

        // Apply theme from settings (default to GhostV3 if no theme configured)
        const themeName = s.custom_theme || "GhostV3";
        await applyCustomThemeByName(themeName);
        if (!s.custom_theme) {
            state.settings.custom_theme = "GhostV3";
        }
    } catch (e) {
        console.error("Failed to load settings:", e);
    }
}

// ── Theme ────────────────────────────────────────────────────────────────

/** Resolve "system" to the actual dark/light value */
function resolveTheme(theme) {
    if (theme === "system") {
        return window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
    }
    return theme;
}

/** Apply a theme to the document */
export function applyTheme(theme) {
    const resolved = resolveTheme(theme);
    document.documentElement.setAttribute("data-theme", resolved);

    // Store the *preference* (dark/light/system) so other pages can read it
    try { localStorage.setItem("coral-theme", theme); } catch (_) {}

    // Update xterm.js terminal colors to match
    updateTerminalTheme();
}

/** Remove all custom CSS variables from inline styles (reset before switching themes) */
function clearCustomThemeVars() {
    const style = document.documentElement.style;
    // Remove any --* properties that were set inline
    const toRemove = [];
    for (let i = 0; i < style.length; i++) {
        const prop = style[i];
        if (prop.startsWith("--")) toRemove.push(prop);
    }
    for (const prop of toRemove) style.removeProperty(prop);
}

/** Load a custom theme by name: apply its base theme + overlay its CSS variables */
async function applyCustomThemeByName(name) {
    try {
        const resp = await fetch(`/api/themes/${encodeURIComponent(name)}`);
        const data = await resp.json();
        if (data.error || !data.theme?.variables) return;

        // Apply the base theme (dark/light)
        const base = data.theme.base || "dark";
        applyTheme(base);

        // Overlay custom variables
        for (const [cssVar, value] of Object.entries(data.theme.variables)) {
            if (value) document.documentElement.style.setProperty(cssVar, value);
        }

        // Refresh terminal colors
        updateTerminalTheme();
    } catch {
        // Fall back to default
    }
}

// Listen for OS theme changes when in "system" mode
window.matchMedia("(prefers-color-scheme: light)").addEventListener("change", () => {
    const pref = (state.settings && state.settings.theme) || "dark";
    if (pref === "system") applyTheme("system");
});

export async function showSettingsModal() {
    const s = state.settings || {};

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
                options += `<option value="custom:${name}">${label}</option>`;
            }
        }
        themeSelect.innerHTML = options;

        // Set current value
        const currentTheme = s.custom_theme ? `custom:${s.custom_theme}` : (s.theme || "dark");
        themeSelect.value = currentTheme;
    }

    // Default Render Engine
    const currentEngine = s.default_renderer || "block-group";
    const options = getEngineNames()
        .map(name => `<option value="${escapeAttr(name)}"${name === currentEngine ? " selected" : ""}>${escapeHtml(name)}</option>`)
        .join("");
    document.getElementById("settings-renderer-select").innerHTML = options;

    // Default Agent Type
    const currentAgentType = s.default_agent_type || "claude";
    const agentTypeSelect = document.getElementById("settings-agent-type");
    if (agentTypeSelect) agentTypeSelect.value = currentAgentType;

    // Default Working Directory
    const dirInput = document.getElementById("settings-working-dir");
    if (dirInput) {
        dirInput.value = s.default_working_dir || "";
        dirInput.placeholder = dirInput.dataset.coralRoot || "/path/to/project";
    }

    // Terminal Scrollback
    const scrollbackSelect = document.getElementById("settings-terminal-scrollback");
    if (scrollbackSelect) scrollbackSelect.value = s.terminal_scrollback || "1000";

    // Fit Pane Width
    const fitPaneCheck = document.getElementById("settings-fit-pane-width");
    if (fitPaneCheck) fitPaneCheck.checked = !!s.fit_pane_width;

    // Notify Needs Input
    const notifyCheck = document.getElementById("settings-notify-needs-input");
    if (notifyCheck) notifyCheck.checked = !!s.notify_needs_input;

    // Check for Updates (stored in localStorage, defaults to on)
    const updateCheck = document.getElementById("settings-check-updates");
    if (updateCheck) updateCheck.checked = localStorage.getItem("coral-update-check-enabled") !== "false";

    // Show Scrollbars (defaults to true)
    const scrollbarsCheck = document.getElementById("settings-show-scrollbars");
    if (scrollbarsCheck) scrollbarsCheck.checked = s.show_scrollbars !== false;

    document.getElementById("settings-modal").style.display = "flex";
}

export function hideSettingsModal() {
    document.getElementById("settings-modal").style.display = "none";
}

export async function applySettings() {
    const themeValue = document.getElementById("settings-theme")?.value || "dark";
    const engineName = document.getElementById("settings-renderer-select").value;
    const agentType = document.getElementById("settings-agent-type")?.value || "claude";
    const workingDir = document.getElementById("settings-working-dir")?.value.trim() || "";
    const fitPaneWidth = document.getElementById("settings-fit-pane-width")?.checked || false;
    const notifyNeedsInput = document.getElementById("settings-notify-needs-input")?.checked || false;
    const terminalScrollback = document.getElementById("settings-terminal-scrollback")?.value || "1000";
    const checkUpdates = document.getElementById("settings-check-updates")?.checked ?? true;
    localStorage.setItem("coral-update-check-enabled", checkUpdates ? "true" : "false");
    const showScrollbars = document.getElementById("settings-show-scrollbars")?.checked ?? true;

    // Parse theme selection — "custom:<name>" or built-in "dark"/"light"/"system"
    let theme, customTheme;
    if (themeValue.startsWith("custom:")) {
        customTheme = themeValue.slice(7);
        theme = "dark"; // will be overridden by the custom theme's base
    } else {
        theme = themeValue;
        customTheme = "";
    }

    const payload = {
        theme: theme,
        custom_theme: customTheme,
        default_renderer: engineName,
        default_agent_type: agentType,
        default_working_dir: workingDir,
        fit_pane_width: fitPaneWidth,
        notify_needs_input: notifyNeedsInput,
        terminal_scrollback: terminalScrollback,
        show_scrollbars: showScrollbars,
    };

    try {
        await fetch("/api/settings", {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
        });
        state.settings = { ...state.settings, ...payload };

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

        // Apply scrollback to existing terminal
        const term = getTerminal();
        if (term) {
            term.options.scrollback = parseInt(terminalScrollback, 10) || 1000;
        }

        // Apply scrollbar visibility
        document.body.classList.toggle('no-scrollbars', !showScrollbars);

        showToast("Settings saved");
    } catch (e) {
        showToast("Failed to save settings", true);
    }

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
        // Fallback — should never happen in practice
        _defaultPrompts = {
            default_prompt_orchestrator: 'IMPORTANT: You were automatically joined to message board "{board_name}". Do NOT run coral-board join. Post a message with coral-board post "<your introduction>" that introduces yourself, then discuss your proposed plan with the operator (the human user) before posting assignments. Periodically check for new messages.',
            default_prompt_worker: 'IMPORTANT: You were automatically joined to message board "{board_name}". Do NOT run coral-board join. Do not start any actions until you receive instructions from the Orchestrator on the message board. Post a message with coral-board post "<your introduction>" that introduces yourself, then periodically check for new messages.',
            team_reminder_orchestrator: 'Remember to coordinate with your team and check the message board for updates',
            team_reminder_worker: 'Remember to work with your team',
        };
    }
    return _defaultPrompts;
}

export async function showDefaultPromptsModal() {
    const defaults = await _ensureDefaultPrompts();
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

// Attach input listeners for flag sync (after DOM is ready)
document.addEventListener("DOMContentLoaded", () => {
    for (const id of ["launch-flags", "restart-flags", "job-modal-flags", "team-flags"]) {
        const el = document.getElementById(id);
        if (el) el.addEventListener("input", () => _syncFlagButtons(id));
    }
});

// Close modals on outside click
document.addEventListener("click", (e) => {
    const launchModal = document.getElementById("launch-modal");
    const infoModal = document.getElementById("info-modal");
    const resumeModal = document.getElementById("resume-modal");
    const settingsModal = document.getElementById("settings-modal");
    const restartModal = document.getElementById("restart-modal");
    if (e.target === launchModal) {
        hideLaunchModal();
    }
    if (e.target === infoModal) {
        hideInfoModal();
    }
    if (e.target === resumeModal) {
        hideResumeModal();
    }
    if (e.target === settingsModal) {
        hideSettingsModal();
    }
    if (e.target === restartModal) {
        hideRestartModal();
    }
    const macroModal = document.getElementById("macro-modal");
    if (e.target === macroModal) {
        macroModal.style.display = "none";
    }
    const webhookModal = document.getElementById("webhook-modal");
    if (e.target === webhookModal) window.hideWebhookModal?.();
    const confirmModal = document.getElementById("confirm-modal");
    if (e.target === confirmModal) window.hideConfirmModal?.();
    const subgentModal = document.getElementById("subgent-settings-modal");
    if (e.target === subgentModal) hideSubgentSettingsModal();
});

// ── Subgent Settings Modal ───────────────────────────────────────────────────

let _subgentConfigured = false;

export async function showSubgentSettingsModal() {
    const modal = document.getElementById("subgent-settings-modal");
    const statusEl = document.getElementById("subgent-settings-status");
    statusEl.style.display = "none";

    // Load saved settings
    try {
        const resp = await fetch("/api/settings/subgent");
        const data = await resp.json();
        document.getElementById("subgent-settings-url").value = data.admin_url || "";
        document.getElementById("subgent-settings-org").value = data.org_id || "default";
        const keyInput = document.getElementById("subgent-settings-key");
        if (data.key_configured) {
            keyInput.value = "";
            keyInput.placeholder = data.admin_key || "••••••••";
            _subgentConfigured = true;
        } else {
            keyInput.value = "";
            keyInput.placeholder = "cb_live_...";
            _subgentConfigured = false;
        }
        document.getElementById("subgent-clear-btn").style.display = _subgentConfigured ? "" : "none";
    } catch (_) {
        _subgentConfigured = false;
        document.getElementById("subgent-clear-btn").style.display = "none";
    }

    // Reset key visibility
    document.getElementById("subgent-settings-key").type = "password";

    modal.style.display = "flex";
}
window.showSubgentSettingsModal = showSubgentSettingsModal;

export function hideSubgentSettingsModal() {
    document.getElementById("subgent-settings-modal").style.display = "none";
}
window.hideSubgentSettingsModal = hideSubgentSettingsModal;

function _toggleSubgentKeyVisibility() {
    const input = document.getElementById("subgent-settings-key");
    input.type = input.type === "password" ? "text" : "password";
}
window._toggleSubgentKeyVisibility = _toggleSubgentKeyVisibility;

async function _saveSubgentSettings() {
    const url = document.getElementById("subgent-settings-url").value.trim();
    const key = document.getElementById("subgent-settings-key").value.trim();
    const org = document.getElementById("subgent-settings-org").value.trim() || "default";

    if (!url) {
        showToast("Admin URL is required", true);
        return;
    }

    const payload = { admin_url: url, org_id: org };
    // Only send key if user entered a new one (not empty placeholder)
    if (key) payload.admin_key = key;
    else if (!_subgentConfigured) {
        showToast("Admin Key is required", true);
        return;
    }

    try {
        const resp = await fetch("/api/settings/subgent", {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
        });
        if (resp.ok) {
            showToast("Subgent settings saved");
            _subgentConfigured = true;
            _updateSubgentTopBarIcon(true);
            hideSubgentSettingsModal();
        } else {
            const data = await resp.json();
            showToast(data.error || "Failed to save", true);
        }
    } catch (e) {
        showToast("Failed to save settings", true);
    }
}
window._saveSubgentSettings = _saveSubgentSettings;

async function _clearSubgentSettings() {
    try {
        const resp = await fetch("/api/settings/subgent", { method: "DELETE" });
        if (resp.ok) {
            showToast("Subgent settings cleared");
            _subgentConfigured = false;
            _updateSubgentTopBarIcon(false);
            hideSubgentSettingsModal();
        } else {
            showToast("Failed to clear settings", true);
        }
    } catch (e) {
        showToast("Failed to clear settings", true);
    }
}
window._clearSubgentSettings = _clearSubgentSettings;

async function _testSubgentConnection() {
    const url = document.getElementById("subgent-settings-url").value.trim();
    const statusEl = document.getElementById("subgent-settings-status");
    if (!url) {
        showToast("Enter an Admin URL first", true);
        return;
    }

    statusEl.style.display = "";
    statusEl.style.background = "rgba(139,148,158,0.1)";
    statusEl.style.color = "var(--text-muted)";
    statusEl.textContent = "Testing connection...";

    const key = document.getElementById("subgent-settings-key").value.trim();
    const payload = { admin_url: url };
    if (key) payload.admin_key = key;

    try {
        const resp = await fetch("/api/settings/subgent/test", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
        });
        const data = await resp.json();
        if (data.ok) {
            statusEl.style.background = "rgba(63,185,80,0.1)";
            statusEl.style.color = "var(--success, #3fb950)";
            const count = data.projects?.length ?? 0;
            let msg = `Connected — ${count} board${count !== 1 ? "s" : ""} found`;
            statusEl.textContent = msg;
            // Show CLI warning if subgent binary not found
            if (data.cli_available === false) {
                const warn = document.createElement("div");
                warn.style.cssText = "margin-top:6px;padding:6px 10px;border-radius:6px;font-size:12px;background:rgba(210,153,34,0.1);color:var(--warning, #d29922)";
                warn.textContent = data.cli_warning || "subgent CLI not found in PATH. Agents need it to communicate on the board.";
                statusEl.after(warn);
                // Clean up warning when modal closes
                const modal = document.getElementById("subgent-settings-modal");
                const observer = new MutationObserver(() => {
                    if (modal.style.display === "none") { warn.remove(); observer.disconnect(); }
                });
                observer.observe(modal, { attributes: true, attributeFilter: ["style"] });
            }
        } else {
            statusEl.style.background = "rgba(248,81,73,0.1)";
            statusEl.style.color = "var(--error, #f85149)";
            statusEl.textContent = data.error || "Connection failed";
        }
    } catch (e) {
        statusEl.style.background = "rgba(248,81,73,0.1)";
        statusEl.style.color = "var(--error, #f85149)";
        statusEl.textContent = "Connection failed";
    }
}
window._testSubgentConnection = _testSubgentConnection;

function _updateSubgentTopBarIcon(configured) {
    const btn = document.getElementById("subgent-top-btn");
    if (btn) btn.classList.toggle("configured", configured);
}

// Check subgent config status on page load for top bar icon
async function _checkSubgentConfigStatus() {
    try {
        const resp = await fetch("/api/settings/subgent");
        const data = await resp.json();
        _subgentConfigured = !!data.key_configured;
        _updateSubgentTopBarIcon(_subgentConfigured);
    } catch (_) {}
}
_checkSubgentConfigStatus();

// Close modals on Escape
document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") {
        hideLaunchModal();
        hideInfoModal();
        hideResumeModal();
        hideSettingsModal();
        hideSubgentSettingsModal();
        hideRestartModal();
        const mm = document.getElementById("macro-modal");
        if (mm) mm.style.display = "none";
        window.hideWebhookModal?.();
    }
});
