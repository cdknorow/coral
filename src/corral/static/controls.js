/* Quick actions, command sending, mode toggling, and session controls */

import { state, sessionKey } from './state.js';
import { escapeHtml, escapeAttr, showToast } from './utils.js';
import { stopCaptureRefresh } from './capture.js';
import { renderLiveSessions } from './render.js';

export async function sendCommand() {
    if (!state.currentSession || state.currentSession.type !== "live") {
        showToast("No live session selected", true);
        return;
    }

    const input = document.getElementById("command-input");
    const command = input.value.trim();
    if (!command) return;

    try {
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/send`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ command, agent_type: state.currentSession.agent_type, session_id: state.currentSession.session_id }),
        });
        if (!resp.ok) {
            const text = await resp.text();
            showToast(`Server error ${resp.status}: ${text}`, true);
            console.error("Send failed:", resp.status, text);
            return;
        }
        const result = await resp.json();
        if (result.error) {
            showToast(result.error, true);
            console.error("Send error:", result.error);
        } else {
            input.value = "";
            const key = sessionKey(state.currentSession);
            if (key) delete state.sessionInputText[key];
            showToast(`Sent: ${command}`);
        }
    } catch (e) {
        showToast("Failed to send command", true);
        console.error("Send exception:", e);
    }
}

const DEFAULT_MACROS = [
    { label: "/compact", command: "/compact" },
    { label: "/clear", command: "/clear" },
    { label: "Reset", command: "/compact && /clear", danger: true },
];

export function getMacros() {
    const raw = state.settings.macros;
    if (raw) {
        try { return JSON.parse(raw); } catch (e) { /* fall through */ }
    }
    return [...DEFAULT_MACROS];
}

async function saveMacros(macros) {
    state.settings.macros = JSON.stringify(macros);
    try {
        await fetch("/api/settings", {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ macros: state.settings.macros }),
        });
    } catch (e) {
        console.error("Failed to save macros:", e);
    }
}

export function executeMacro(command) {
    // Support chained commands with "&&" separator (1s delay between each)
    const parts = command.split("&&").map(c => c.trim()).filter(Boolean);
    if (parts.length === 0) return;
    document.getElementById("command-input").value = parts[0];
    sendCommand();
    for (let i = 1; i < parts.length; i++) {
        setTimeout(() => {
            document.getElementById("command-input").value = parts[i];
            sendCommand();
        }, i * 1000);
    }
}

export async function addMacro() {
    const label = document.getElementById("macro-label-input").value.trim();
    const command = document.getElementById("macro-command-input").value.trim();
    if (!label || !command) { showToast("Label and command are required", true); return; }
    const macros = getMacros();
    macros.push({ label, command });
    await saveMacros(macros);
    hideMacroModal();
    renderQuickActions();
}

export async function deleteMacro(index) {
    const macros = getMacros();
    macros.splice(index, 1);
    await saveMacros(macros);
    renderQuickActions();
}

export function showMacroModal() {
    document.getElementById("macro-label-input").value = "";
    document.getElementById("macro-command-input").value = "";
    document.getElementById("macro-modal").style.display = "flex";
    document.getElementById("macro-label-input").focus();
}

export function hideMacroModal() {
    document.getElementById("macro-modal").style.display = "none";
}

export function renderQuickActions() {
    const toolbar = document.getElementById("command-toolbar");
    const macros = getMacros();

    const modeButtons = `
        <button class="btn-nav btn-mode" onclick="sendModeToggle('plan')" title="Plan Mode"><svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M4 2h8a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1z"/><line x1="6" y1="5" x2="10" y2="5"/><line x1="6" y1="8" x2="10" y2="8"/><line x1="6" y1="11" x2="8" y2="11"/></svg><span class="btn-label">Plan Mode</span></button>
        <button class="btn-nav btn-mode" onclick="sendModeToggle('auto')" title="Accept Edits"><svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="3.5 8.5 6.5 11.5 12.5 4.5"/></svg><span class="btn-label">Accept Edits</span></button>
        <button class="btn-nav btn-mode" onclick="sendQuickCommand('!')" title="Bash Mode"><svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 3h12a1 1 0 0 1 1 1v8a1 1 0 0 1-1 1H2a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z"/><polyline points="4 7 6 9 4 11"/><line x1="8" y1="11" x2="12" y2="11"/></svg><span class="btn-label">Bash Mode</span></button>
    `;

    const macroButtons = macros.map((m, i) => {
        const cls = m.danger ? "btn-nav btn-danger" : "btn-nav";
        return `<span class="macro-btn-wrap">
            <button class="${cls}" onclick="executeMacro('${escapeAttr(m.command)}')" title="${escapeAttr(m.command)}">${escapeHtml(m.label)}</button>
            <button class="macro-delete-btn" onclick="deleteMacro(${i})" title="Remove macro">&times;</button>
        </span>`;
    }).join("");

    const navButtons = `
        <button class="btn-nav" onclick="sendRawKeys(['Escape'])" title="Escape">Esc</button>
        <button class="btn-nav" onclick="sendRawKeys(['Up'])" title="Arrow Up">&uarr;</button>
        <button class="btn-nav" onclick="sendRawKeys(['Down'])" title="Arrow Down">&darr;</button>
        <button class="btn-nav btn-enter" onclick="sendRawKeys(['Enter'])" title="Enter">&#9166;</button>
        <button class="btn-nav btn-primary btn-send" onclick="sendCommand()">Send</button>
    `;

    toolbar.innerHTML = `
        ${modeButtons}
        <span class="toolbar-divider"></span>
        ${macroButtons}
        <button class="btn-nav btn-add-macro" onclick="showMacroModal()" title="Add macro">+</button>
        <span class="toolbar-spacer"></span>
        ${navButtons}
    `;
}

export async function sendRawKeys(keys) {
    if (!state.currentSession || state.currentSession.type !== "live") {
        showToast("No live session selected", true);
        return;
    }

    try {
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/keys`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ keys, agent_type: state.currentSession.agent_type, session_id: state.currentSession.session_id }),
        });
        const result = await resp.json();
        if (result.error) {
            showToast(result.error, true);
        } else {
            showToast(`Sent: ${keys.join(" + ")}`);
        }
    } catch (e) {
        showToast("Failed to send keys", true);
        console.error("sendRawKeys exception:", e);
    }
}

export async function attachTerminal() {
    if (!state.currentSession || state.currentSession.type !== "live") {
        showToast("No live session selected", true);
        return;
    }

    try {
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/attach`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ agent_type: state.currentSession.agent_type, session_id: state.currentSession.session_id }),
        });
        const result = await resp.json();
        if (result.error) {
            showToast(result.error, true);
        } else {
            showToast("Terminal opened");
        }
    } catch (e) {
        showToast("Failed to open terminal", true);
        console.error("attachTerminal exception:", e);
    }
}

export async function killSession() {
    if (!state.currentSession || state.currentSession.type !== "live") {
        showToast("No live session selected", true);
        return;
    }

    if (!confirm(`Kill session "${state.currentSession.name}"? This will terminate the agent.`)) {
        return;
    }

    try {
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/kill`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ agent_type: state.currentSession.agent_type, session_id: state.currentSession.session_id }),
        });
        const result = await resp.json();
        if (result.error) {
            showToast(result.error, true);
        } else {
            const killedSid = state.currentSession.session_id;
            showToast(`Killed: ${state.currentSession.name}`);
            stopCaptureRefresh();
            state.currentSession = null;
            document.getElementById("live-session-view").style.display = "none";
            document.getElementById("welcome-screen").style.display = "flex";
            // Remove from cached list and re-render immediately
            state.liveSessions = state.liveSessions.filter(s => s.session_id !== killedSid);
            renderLiveSessions(state.liveSessions);
        }
    } catch (e) {
        showToast("Failed to kill session", true);
        console.error("killSession exception:", e);
    }
}

export function restartSession() {
    if (!state.currentSession || state.currentSession.type !== "live") {
        showToast("No live session selected", true);
        return;
    }
    showRestartModal();
}

function showRestartModal() {
    document.getElementById("restart-modal-name").textContent =
        `Session: ${state.currentSession.name}`;
    document.getElementById("restart-flags").value = "";
    document.getElementById("restart-modal").style.display = "flex";
}

export function hideRestartModal() {
    document.getElementById("restart-modal").style.display = "none";
}

export async function confirmRestart() {
    const flagsStr = document.getElementById("restart-flags").value.trim();

    hideRestartModal();

    try {
        showToast(`Restarting ${state.currentSession.name}...`);
        const payload = { agent_type: state.currentSession.agent_type, session_id: state.currentSession.session_id };
        if (flagsStr) {
            payload.extra_flags = flagsStr;
        }
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/restart`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
        });
        const result = await resp.json();
        if (result.error) {
            showToast(result.error, true);
        } else {
            // Update state with the new session_id (tmux session was renamed)
            if (result.session_id) {
                state.currentSession.session_id = result.session_id;
            }
            if (result.agent_name) {
                state.currentSession.name = result.agent_name;
                document.getElementById("session-name").textContent = result.agent_name;
            }
            showToast(`Restarted: ${state.currentSession.name}`);
        }
    } catch (e) {
        showToast("Failed to restart session", true);
        console.error("confirmRestart exception:", e);
    }
}

// Claude Code modes cycle via Shift+Tab (BTab in tmux).
// Order: default -> plan -> auto-accept -> default
const MODE_CYCLE = ["default", "plan", "auto"];

function detectCurrentMode() {
    const el = document.getElementById("pane-capture");
    const text = (el.textContent || "").toLowerCase();
    if (text.includes("plan mode")) return "plan";
    if (text.includes("auto-accept") || text.includes("accept edits")) return "auto";
    return "default";
}

export function sendModeToggle(targetMode) {
    const current = detectCurrentMode();
    if (current === targetMode) {
        showToast(`Already in ${targetMode === "plan" ? "Plan" : targetMode === "auto" ? "Accept Edits" : "Base"} mode`);
        return;
    }

    const currentIdx = MODE_CYCLE.indexOf(current);
    const targetIdx = MODE_CYCLE.indexOf(targetMode);
    let presses = (targetIdx - currentIdx + MODE_CYCLE.length) % MODE_CYCLE.length;
    if (presses === 0) presses = MODE_CYCLE.length;

    const keys = Array(presses).fill("BTab");
    sendRawKeys(keys);
}

export function sendQuickCommand(command) {
    document.getElementById("command-input").value = command;
    sendCommand();
}

export function updateSidebarActive() {
    document.querySelectorAll(".session-list li").forEach(li => li.classList.remove("active"));
    if (state.liveSessions.length) renderLiveSessions(state.liveSessions);

    // Highlight the active history session by matching onclick session_id
    if (state.currentSession && state.currentSession.type === "history") {
        const sid = state.currentSession.name;
        document.querySelectorAll("#history-sessions-list li").forEach(li => {
            const onclick = li.getAttribute("onclick") || "";
            if (onclick.includes(sid)) li.classList.add("active");
        });
    }
}
