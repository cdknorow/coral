/* Modal management: launch and info dialogs */

import { state } from './state.js';
import { showToast, escapeHtml, escapeAttr } from './utils.js';
import { loadLiveSessions } from './api.js';
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
    // Widen the modal for two-column layouts (agent and team forms)
    const content = document.querySelector("#launch-modal .modal-content");
    if (content) {
        const wide = step === "team" || step === "agent";
        content.classList.toggle("modal-content-extra-wide", wide);
        content.classList.toggle("modal-content-wide", !wide);
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
        document.getElementById("launch-agent-name").focus();
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
    document.getElementById("launch-flags").value = "";
    document.getElementById("launch-agent-prompt").value = "";
    document.getElementById("launch-board-name").value = "";
    document.getElementById("launch-board-server").value = "";
    const agentBoardSelect = document.getElementById("launch-board-select");
    if (agentBoardSelect) agentBoardSelect.value = "";
    document.getElementById("launch-new-board-row").style.display = "none";
    document.getElementById("launch-board-server-row").style.display = "none";
    _syncFlagButtons("launch-flags");

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

// ── Agent Team ────────────────────────────────────────────────────────────

let _teamAgentCounter = 0;

// Predefined agent presets
const AGENT_PRESETS = [
    {
        name: "Lead Developer",
        prompt: "You are the lead developer. Implement features, write code, and coordinate with the team via the message board. IMPORTANT: Do not start any actions until you receive instructions from the Orchestrator on the message board. Introduce yourself, then wait.",
    },
    {
        name: "QA Engineer",
        prompt: "You are an expert QA engineer. Review the work of other agents, create test plans, write tests, and ask probing questions about complex areas. IMPORTANT: Do not start any actions until you receive instructions from the Orchestrator on the message board. Introduce yourself, then wait.",
    },
    {
        name: "Orchestrator",
        prompt: "You are the orchestrator. Coordinate the team, break down tasks, assign work via the message board, and track progress. Do not write code yourself — delegate to the other agents. IMPORTANT: Do not send any task assignments or plans to the message board until you have discussed the approach with the operator (the human user) first. Introduce yourself to the board, then discuss your proposed plan with the operator before posting assignments.",
    },
    {
        name: "Frontend Dev",
        prompt: "You are a frontend developer. Build UI components, style pages, and ensure a great user experience. Coordinate with the team via the message board. IMPORTANT: Do not start any actions until you receive instructions from the Orchestrator on the message board. Introduce yourself, then wait.",
    },
    {
        name: "Backend Dev",
        prompt: "You are a backend developer. Build APIs, services, and data models. Coordinate with the team via the message board. IMPORTANT: Do not start any actions until you receive instructions from the Orchestrator on the message board. Introduce yourself, then wait.",
    },
    {
        name: "DevOps Engineer",
        prompt: "You are a DevOps engineer. Handle CI/CD, infrastructure, deployment, and monitoring. Coordinate with the team via the message board. IMPORTANT: Do not start any actions until you receive instructions from the Orchestrator on the message board. Introduce yourself, then wait.",
    },
    {
        name: "Security Reviewer",
        prompt: "You are a security reviewer. Audit code for vulnerabilities, review auth flows, and ensure OWASP best practices. Report findings via the message board. IMPORTANT: Do not start any actions until you receive instructions from the Orchestrator on the message board. Introduce yourself, then wait.",
    },
    {
        name: "Technical Writer",
        prompt: "You are a technical writer. Write documentation, API guides, and READMEs. Coordinate with the team via the message board. IMPORTANT: Do not start any actions until you receive instructions from the Orchestrator on the message board. Introduce yourself, then wait.",
    },
];

// Default team: first 3 presets
const DEFAULT_TEAM_PRESETS = AGENT_PRESETS.slice(0, 3);

async function _initTeamForm() {
    // Reset form
    document.getElementById("team-board-name").value = "";
    document.getElementById("team-board-server").value = "";
    document.getElementById("team-flags").value = "";
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

    // Add three default agents
    for (const preset of DEFAULT_TEAM_PRESETS) {
        _addTeamAgent(preset.name, preset.prompt);
    }

    document.getElementById("team-board-name").focus();
}

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
                <label>Name / Role:
                    <input type="text" class="team-agent-name" placeholder="e.g. QA Engineer, Frontend Dev" value="${escapeAttr(defaultName || '')}"
                        oninput="const card=this.closest('.team-agent-row'); card.querySelector('.team-agent-role-name').textContent=this.value||'New Agent'">
                </label>
                <label>Behavior Prompt:
                    <textarea class="team-agent-prompt" rows="3" placeholder="Describe this agent's role and behavior..."
                        oninput="const card=this.closest('.team-agent-row'); card.querySelector('.team-agent-prompt-preview').textContent=this.value.substring(0,200)+(this.value.length>200?'\u2026':'')">${escapeHtml(defaultPrompt || '')}</textarea>
                </label>
                <button class="btn btn-small team-agent-done-btn" onclick="this.closest('.team-agent-card').classList.remove('editing')">Done</button>
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
    const picker = document.getElementById("team-agent-picker");
    if (!picker) return;

    // Get names of agents already added
    const existingNames = new Set();
    document.querySelectorAll("#team-agents-list .team-agent-name").forEach(input => {
        const n = input.value.trim().toLowerCase();
        if (n) existingNames.add(n);
    });

    // Build picker items: presets not already in the list
    const available = AGENT_PRESETS.filter(p => !existingNames.has(p.name.toLowerCase()));

    let html = '';
    for (const preset of available) {
        html += `<button class="agent-picker-item" onclick="window._addPresetAgent('${escapeAttr(preset.name)}')">${escapeHtml(preset.name)}</button>`;
    }
    if (available.length > 0) {
        html += '<div class="agent-picker-divider"></div>';
    }
    html += `<button class="agent-picker-item agent-picker-custom" onclick="window._addTeamAgent()">+ Create Custom</button>`;

    picker.innerHTML = html;
    picker.style.display = "";

    // Close picker on outside click
    const closeHandler = (e) => {
        if (!picker.contains(e.target) && !e.target.closest('.team-add-agent-btn')) {
            picker.style.display = "none";
            document.removeEventListener("click", closeHandler);
        }
    };
    setTimeout(() => document.addEventListener("click", closeHandler), 0);
}
window._showAddAgentPicker = _showAddAgentPicker;

function _addPresetAgent(name) {
    const preset = AGENT_PRESETS.find(p => p.name === name);
    if (preset) {
        _addTeamAgent(preset.name, preset.prompt);
    }
    const picker = document.getElementById("team-agent-picker");
    if (picker) picker.style.display = "none";
}
window._addPresetAgent = _addPresetAgent;
window._addTeamAgent = () => {
    _addTeamAgent("", "");
    const picker = document.getElementById("team-agent-picker");
    if (picker) picker.style.display = "none";
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
    const flagsStr = document.getElementById("team-flags").value.trim();
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
        if (!name) {
            showToast("Each agent needs a name", true);
            return;
        }
        agents.push({ name, prompt });
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
        state.settings = s;

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

        showToast("Settings saved");
    } catch (e) {
        showToast("Failed to save settings", true);
    }

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
});

// Close modals on Escape
document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") {
        hideLaunchModal();
        hideInfoModal();
        hideResumeModal();
        hideSettingsModal();
        hideRestartModal();
        const mm = document.getElementById("macro-modal");
        if (mm) mm.style.display = "none";
        window.hideWebhookModal?.();
    }
});
