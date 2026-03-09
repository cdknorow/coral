/* Modal management: launch and info dialogs */

import { state } from './state.js';
import { showToast, escapeHtml, escapeAttr } from './utils.js';
import { loadLiveSessions } from './api.js';
import { getEngineNames, getEngineName, setRendererOverride } from './renderers.js';
import { renderCaptureText } from './capture.js';
import { hideRestartModal } from './controls.js';

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

export function showLaunchModal() {
    document.getElementById("launch-modal").style.display = "flex";
    document.getElementById("launch-agent-name").value = "";
    document.getElementById("launch-flags").value = "";
    _syncFlagButtons("launch-flags");

    // Pre-fill from global settings
    const s = state.settings || {};
    const dirInput = document.getElementById("launch-dir");
    if (s.default_working_dir && dirInput) {
        dirInput.value = s.default_working_dir;
    }
    const typeSelect = document.getElementById("launch-type");
    if (s.default_agent_type && typeSelect) {
        typeSelect.value = s.default_agent_type;
    }

    document.getElementById("launch-agent-name").focus();
}

export function hideLaunchModal() {
    document.getElementById("launch-modal").style.display = "none";
}

export async function launchSession() {
    const dir = document.getElementById("launch-dir").value.trim();
    const type = document.getElementById("launch-type").value;
    const agentName = document.getElementById("launch-agent-name").value.trim();

    if (!dir) {
        showToast("Working directory is required", true);
        return;
    }

    const payload = { working_dir: dir, agent_type: type };
    if (agentName) payload.display_name = agentName;

    const flagsStr = document.getElementById("launch-flags").value.trim();
    if (flagsStr) {
        payload.flags = flagsStr.split(/\s+/);
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
        state.settings = data.settings || {};
    } catch (e) {
        console.error("Failed to load settings:", e);
    }
}

export function showSettingsModal() {
    const s = state.settings || {};

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
        dirInput.placeholder = dirInput.dataset.corralRoot || "/path/to/project";
    }

    document.getElementById("settings-modal").style.display = "flex";
}

export function hideSettingsModal() {
    document.getElementById("settings-modal").style.display = "none";
}

export async function applySettings() {
    const engineName = document.getElementById("settings-renderer-select").value;
    const agentType = document.getElementById("settings-agent-type")?.value || "claude";
    const workingDir = document.getElementById("settings-working-dir")?.value.trim() || "";

    const payload = {
        default_renderer: engineName,
        default_agent_type: agentType,
        default_working_dir: workingDir,
    };

    try {
        await fetch("/api/settings", {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
        });
        state.settings = { ...state.settings, ...payload };

        // If a live session is selected, force re-render with new default
        if (state.currentSession?.session_id) {
            const el = document.getElementById("pane-capture");
            if (el && el._lastCapture) {
                renderCaptureText(el, el._lastCapture);
            }
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
    for (const id of ["launch-flags", "restart-flags"]) {
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
    }
});
