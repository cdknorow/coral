/* Quick actions, command sending, mode toggling, and session controls */

import { state, sessionKey } from './state.js';
import { escapeHtml, escapeAttr, showToast, showView } from './utils.js';
import { stopCaptureRefresh } from './capture.js';
import { renderLiveSessions } from './render.js';
import { loadAgentEvents, renderEventTimeline } from './agentic_state.js';

// Lazy imports to avoid circular dependency (xterm_renderer imports controls)
let _xtermModule = null;
async function _getXtermModule() {
    if (!_xtermModule) _xtermModule = await import('./xterm_renderer.js');
    return _xtermModule;
}

// Attached image paths (cleared on send)
const pendingAttachments = [];

export async function sendCommand() {
    if (!state.currentSession || state.currentSession.type !== "live") {
        showToast("No live session selected", true);
        return;
    }

    const input = document.getElementById("command-input");
    const textPart = input.value.trim();

    // Build the full command: image paths + text, space-separated
    const parts = [];
    for (const att of pendingAttachments) {
        parts.push(att.path);
    }
    if (textPart) parts.push(textPart);

    const command = parts.join(" ");
    if (!command) return;

    // Try WebSocket path first (sends text, then Enter separately)
    if (pendingAttachments.length === 0) {
        const xterm = await _getXtermModule();
        if (xterm.sendTerminalInputWs(command)) {
            // Send Enter after delay so bracket paste + tmux processing completes
            setTimeout(() => xterm.sendTerminalInputWs("\r"), 300);
            input.value = "";
            const key = sessionKey(state.currentSession);
            if (key) delete state.sessionInputText[key];
            showToast(`Sent: ${command}`);
            xterm.focusTerminal();
            return;
        }
    }

    // Fall back to POST endpoint
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
            clearAttachments();
            const key = sessionKey(state.currentSession);
            if (key) delete state.sessionInputText[key];
            showToast(`Sent: ${command}`);
            _getXtermModule().then(m => m.focusTerminal());
        }
    } catch (e) {
        showToast("Failed to send command", true);
        console.error("Send exception:", e);
    }
}

const DEFAULT_TEAM_REMINDER_ORCHESTRATOR = "Remember to coordinate with your team and check the message board for updates";
const DEFAULT_TEAM_REMINDER_WORKER = "Remember to work with your team";

function _isOrchestratorSession(session) {
    if (!session) return false;
    const name = (session.display_name || "").toLowerCase();
    const title = (session.board_job_title || "").toLowerCase();
    return name.includes("orchestrator") || title.includes("orchestrator");
}

export async function sendCommandWithTeam() {
    if (!state.currentSession || state.currentSession.type !== "live") {
        showToast("No live session selected", true);
        return;
    }

    const input = document.getElementById("command-input");
    const s = state.settings || {};
    const isOrch = _isOrchestratorSession(state.currentSession);
    const reminder = isOrch
        ? (s.team_reminder_orchestrator || DEFAULT_TEAM_REMINDER_ORCHESTRATOR)
        : (s.team_reminder_worker || DEFAULT_TEAM_REMINDER_WORKER);

    const current = input.value.trim();
    input.value = current ? `${current}\n\n${reminder}` : reminder;
    await sendCommand();
}

const BOARD_PROTOCOL = `You have access to a shared message board for team communication. Use these commands:

- coral-board post "<message>" [--to "agent1,agent2"] — Post a message (--to auto-prepends @mentions)
- coral-board read — Read recent messages
- coral-board read --all — Read all messages

Guidelines:
- Post status updates so the team knows what you're working on
- When you have a new message, Coral will notify you
- Keep messages concise and actionable
- When done with a task, post your results to the board`;

export async function sendBoardProtocol() {
    if (!state.currentSession || state.currentSession.type !== "live") {
        showToast("No live session selected", true);
        return;
    }
    const input = document.getElementById("command-input");
    const current = input.value.trim();
    input.value = current ? `${current}\n\n${BOARD_PROTOCOL}` : BOARD_PROTOCOL;
    await sendCommand();
}

export async function resendInputPrompt() {
    if (!state.currentSession || state.currentSession.type !== "live") {
        showToast("No live session selected", true);
        return;
    }
    const s = state.currentSession;
    const boardName = s.board_project || "";
    if (!boardName) {
        showToast("No board found for this session", true);
        return;
    }
    const isOrch = _isOrchestratorSession(s);
    let defaults = {};
    try {
        const res = await fetch("/api/settings/default-prompts");
        defaults = await res.json();
    } catch { /* use fallbacks below */ }
    const settings = state.settings || {};
    const promptTemplate = isOrch
        ? (settings.default_prompt_orchestrator || defaults.default_prompt_orchestrator || "")
        : (settings.default_prompt_worker || defaults.default_prompt_worker || "");
    if (!promptTemplate) {
        showToast("No default prompt configured", true);
        return;
    }
    const prompt = promptTemplate.replace(/\{board_name\}/g, boardName);
    const input = document.getElementById("command-input");
    input.value = prompt;
    await sendCommand();
}

const DEFAULT_MACROS = [
    { label: "🎨 Icon", command: "Pick an emoji that represents your role and set it with: coral-agent-icon set <emoji>" },
];

export function getMacros() {
    const raw = state.settings.macros;
    if (raw) {
        try { return JSON.parse(raw); } catch (e) { /* fall through */ }
    }
    return [...DEFAULT_MACROS];
}

export async function saveMacros(macros) {
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
        <button class="btn-nav btn-mode" onclick="cycleModeToggle()" data-tooltip="Cycles through Default → Plan → Accept Edits modes (Shift+Tab). Each click advances one step."><svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M4 2h8a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1z"/><line x1="6" y1="5" x2="10" y2="5"/><line x1="6" y1="8" x2="10" y2="8"/><line x1="6" y1="11" x2="8" y2="11"/></svg><span class="btn-label">Mode</span></button>
        <button class="btn-nav btn-mode" onclick="sendQuickCommand('!')" data-tooltip="Prefixes your input with ! so Claude runs it as a shell command instead of a prompt."><svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 3h12a1 1 0 0 1 1 1v8a1 1 0 0 1-1 1H2a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z"/><polyline points="4 7 6 9 4 11"/><line x1="8" y1="11" x2="12" y2="11"/></svg><span class="btn-label">Bash</span></button>
        <button class="btn-nav btn-mode" onclick="sendRawKeys(['Escape','Escape'])" data-tooltip="Sends two Escape keys to Claude. Interrupts the current response, rejects a pending tool call, or backs out of a permission prompt."><svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3.5 2v4h4"/><path d="M3.5 6A5.5 5.5 0 1 1 2.5 8"/></svg><span class="btn-label">Undo</span></button>
    `;

    const macroButtons = macros.map((m, i) => {
        const cls = m.danger ? "btn-nav btn-danger" : "btn-nav";
        return `<span class="macro-btn-wrap">
            <button class="${cls}" onclick="executeMacro('${escapeAttr(m.command)}')" data-tooltip="${escapeAttr(m.command)}">${escapeHtml(m.label)}</button>
            <button class="macro-delete-btn" onclick="deleteMacro(${i})" title="Remove macro">&times;</button>
        </span>`;
    }).join("");

    const navButtons = `
        <button class="btn-nav" onclick="sendRawKeys(['Escape'])" title="Escape" aria-label="Escape">Esc</button>
        <button class="btn-nav" onclick="sendRawKeys(['Up'])" title="Arrow Up" aria-label="Arrow Up">&uarr;</button>
        <button class="btn-nav" onclick="sendRawKeys(['Down'])" title="Arrow Down" aria-label="Arrow Down">&darr;</button>
        <button class="btn-nav btn-enter" onclick="sendRawKeys(['Enter'])" data-tooltip="Sends an Enter keypress to the terminal" aria-label="Enter">&#9166;</button>
    `;

    toolbar.innerHTML = `
        <div class="toolbar-group toolbar-group-modes">
            ${modeButtons}
        </div>
        <span class="toolbar-divider"></span>
        <div class="toolbar-group toolbar-group-macros">
            ${macroButtons}
            <button class="btn-nav btn-add-macro" onclick="showMacroModal()" title="Add macro" aria-label="Add macro">+</button>
        </div>
        <span class="toolbar-spacer"></span>
        <div class="toolbar-group toolbar-group-nav">
            ${navButtons}
            <div class="toolbar-send-split">
                <button class="btn-nav btn-send" onclick="sendCommand()" data-tooltip="Sends the text from the input box below to the terminal" aria-label="Send command"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="22" y1="2" x2="11" y2="13"/><polygon points="22 2 15 22 11 13 2 9 22 2"/></svg></button>
                <button class="btn-nav btn-send-dropdown" onclick="toggleSendMenu(this)" aria-label="Send options" title="Send options">
                    <svg width="8" height="8" viewBox="0 0 16 16" fill="currentColor"><path d="M4 6l4 4 4-4z"/></svg>
                </button>
                <div class="send-btn-menu" style="display:none">
                    <button class="send-menu-item" onclick="sendCommand(); closeSendMenu()">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="22" y1="2" x2="11" y2="13"/><polygon points="22 2 15 22 11 13 2 9 22 2"/></svg>
                        Send
                        <span class="send-menu-hint"><kbd>Ctrl</kbd>+<kbd>Enter</kbd></span>
                    </button>
                    <button class="send-menu-item" onclick="sendCommandWithTeam(); closeSendMenu()">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="9" cy="7" r="3"/><circle cx="17" cy="7" r="3"/><path d="M3 21v-2a4 4 0 0 1 4-4h4a4 4 0 0 1 4 4v2"/><path d="M17 11a4 4 0 0 1 4 4v2"/></svg>
                        Send + Team Reminder
                    </button>
                    <button class="send-menu-item" onclick="resendInputPrompt(); closeSendMenu()">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="1 4 1 10 7 10"/><path d="M3.51 15a9 9 0 1 0 2.13-9.36L1 10"/></svg>
                        Resend Prompt
                    </button>
                    <button class="send-menu-item" onclick="sendBoardProtocol(); closeSendMenu()">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="18" height="18" rx="2"/><line x1="8" y1="8" x2="16" y2="8"/><line x1="8" y1="12" x2="16" y2="12"/><line x1="8" y1="16" x2="12" y2="16"/></svg>
                        Send Board Protocol
                    </button>
                </div>
            </div>
        </div>
    `;
}

// Map tmux key names to raw terminal sequences for WebSocket input
const _KEY_TO_SEQ = {
    "Enter": "\r",
    "Escape": "\x1b",
    "Tab": "\t",
    "BTab": "\x1b[Z",
    "BSpace": "\x7f",
    "Up": "\x1b[A",
    "Down": "\x1b[B",
    "Right": "\x1b[C",
    "Left": "\x1b[D",
    "Home": "\x1b[H",
    "End": "\x1b[F",
    "DC": "\x1b[3~",      // Delete
    "PageUp": "\x1b[5~",
    "PageDown": "\x1b[6~",
};

export async function sendRawKeys(keys, { silent = false } = {}) {
    if (!state.currentSession || state.currentSession.type !== "live") {
        showToast("No live session selected", true);
        return;
    }

    // Try WebSocket path first — much faster than POST
    const xterm = await _getXtermModule();
    const allMapped = keys.every(k => k in _KEY_TO_SEQ);
    if (allMapped && xterm.sendTerminalInputWs) {
        const seq = keys.map(k => _KEY_TO_SEQ[k]).join("");
        if (xterm.sendTerminalInputWs(seq)) {
            if (!silent) showToast(`Sent: ${keys.join(" + ")}`);
            return;
        }
    }

    // Fall back to POST endpoint for unmapped keys or when WebSocket is unavailable
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
            if (!silent) showToast(`Sent: ${keys.join(" + ")}`);
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

    window.showConfirmModal('Kill Session', `Kill session "${state.currentSession.name}"? This will terminate the agent.`, async () => {
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
                if (result.changes_artifact_error) {
                    showToast(`Killed, but changes.diff could not be saved: ${result.changes_artifact_error}`, true);
                } else {
                    showToast(`Killed and saved changes.diff: ${state.currentSession.name}`);
                }
                // Preserve killed session for history link
                const killed = state.liveSessions.find(s => s.session_id === killedSid);
                if (killed) {
                    killed.done = true;
                    killed.working = false;
                    killed.waiting_for_input = false;
                    killed.stuck = false;
                    killed.sleeping = false;
                    state.killedSessions[killedSid] = killed;
                }
                stopCaptureRefresh();
                state.currentSession = null;
                showView("welcome-screen");
                state.liveSessions = state.liveSessions.filter(s => s.session_id !== killedSid);
                renderLiveSessions(state.liveSessions);
            }
        } catch (e) {
            showToast("Failed to kill session", true);
            console.error("killSession exception:", e);
        }
    });
}

export function restartSession() {
    if (!state.currentSession || state.currentSession.type !== "live") {
        showToast("No live session selected", true);
        return;
    }
    showRestartModal();
}

function showRestartModal() {
    const s = state.currentSession;
    document.getElementById("restart-modal-name").textContent =
        `Restarting: ${s.display_name || s.name}`;

    // Render unified config form pre-filled from session
    if (window.renderAgentConfigForm) {
        window.renderAgentConfigForm('restart-acf', {
            showPreset: false,
            showName: false,
            value: {
                agentType: s.agent_type || 'claude',
                model: s.model || '',
                prompt: s.prompt || '',
                capabilities: s.capabilities || null,
            },
        });
    }

    document.getElementById("restart-modal").style.display = "flex";
}

export function hideRestartModal() {
    document.getElementById("restart-modal").style.display = "none";
}

export async function confirmRestart() {
    const config = window.getAgentConfig ? window.getAgentConfig('restart-acf') : {};

    hideRestartModal();

    try {
        // Show restarting overlay instead of session-ended
        const xtermMod = await _getXtermModule();
        if (xtermMod.setRestarting) xtermMod.setRestarting(true);
        showToast(`Restarting ${state.currentSession.name}...`);
        const payload = { agent_type: config.agentType || state.currentSession.agent_type, session_id: state.currentSession.session_id };
        if (config.flags) {
            payload.extra_flags = config.flags;
        }
        if (config.prompt) payload.prompt = config.prompt;
        if (config.model) payload.model = config.model;
        if (config.capabilities) payload.capabilities = config.capabilities;
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
            // Clear activity data immediately so the UI shows zero for the new session
            state.currentAgentEvents = [];
            renderEventTimeline();
            // Reload events for the new session_id
            loadAgentEvents(state.currentSession.name, state.currentSession.session_id);
            showToast(`Restarted: ${state.currentSession.name}`);

            // Reconnect terminal WebSocket to the new session.
            // Poll until the new session is live, then connect.
            const newName = state.currentSession.name;
            const newSessionId = state.currentSession.session_id;
            const newAgentType = state.currentSession.agent_type;
            let pollCount = 0;
            const pollInterval = setInterval(async () => {
                pollCount++;
                try {
                    const liveResp = await fetch(`/api/sessions/live/${encodeURIComponent(newName)}?session_id=${encodeURIComponent(newSessionId)}`);
                    if (liveResp.ok) {
                        clearInterval(pollInterval);
                        xtermMod.connectTerminalWs(newName, newAgentType, newSessionId);
                    }
                } catch (_) { /* ignore fetch errors during poll */ }
                if (pollCount >= 15) {
                    // Timeout after ~30s — clear overlay and show error
                    clearInterval(pollInterval);
                    xtermMod.setRestarting(false);
                    showToast("Restart timed out — session may not have started", true);
                }
            }, 2000);
        }
    } catch (e) {
        showToast("Failed to restart session", true);
        console.error("confirmRestart exception:", e);
    }
}

// ── Goal editing ───────────────────────────────────────────────────────────

export function editGoal() {
    const el = document.getElementById("session-summary");
    if (!el) return;
    const textEl = el.querySelector(".summary-text");
    const actionBtns = el.querySelectorAll(".goal-action-btn");
    const currentText = textEl.textContent || "";

    // Replace text span and buttons with an input
    const input = document.createElement("input");
    input.type = "text";
    input.className = "goal-edit-input";
    input.value = currentText;

    textEl.style.display = "none";
    actionBtns.forEach(b => b.style.display = "none");
    el.appendChild(input);
    input.focus();
    input.select();

    const commit = async () => {
        const newGoal = input.value.trim();
        input.remove();
        textEl.style.display = "";
        actionBtns.forEach(b => b.style.display = "");

        if (!newGoal || newGoal === currentText) return;

        textEl.textContent = newGoal;

        // Post a goal event to persist the change
        if (state.currentSession && state.currentSession.type === "live") {
            try {
                await fetch(`/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/events`, {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({
                        event_type: "goal",
                        summary: newGoal,
                        session_id: state.currentSession.session_id,
                    }),
                });
            } catch (e) {
                console.error("Failed to save goal:", e);
            }
        }
    };

    input.addEventListener("blur", commit);
    input.addEventListener("keydown", (e) => {
        if (e.key === "Enter") { e.preventDefault(); input.blur(); }
        if (e.key === "Escape") { input.value = currentText; input.blur(); }
    });
}

export function refreshGoal() {
    if (!state.currentSession || state.currentSession.type !== "live") return;
    const name = state.currentSession.name;
    const msg = 'Emit a ||PULSE:SUMMARY <your current goal>|| line now to update the dashboard with your current goal.';
    fetch(`/api/sessions/live/${encodeURIComponent(name)}/send`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
            command: msg,
            agent_type: state.currentSession.agent_type,
            session_id: state.currentSession.session_id,
        }),
    }).then(() => {
        showToast("Asked agent to update goal");
    }).catch(() => {
        showToast("Failed to send refresh request", true);
    });
}

export function requestGoal(name, agentType, sessionId) {
    const msg = 'Emit a ||PULSE:SUMMARY <your current goal>|| line now to update the dashboard with your current goal.';
    fetch(`/api/sessions/live/${encodeURIComponent(name)}/send`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
            command: msg,
            agent_type: agentType,
            session_id: sessionId,
        }),
    }).then(() => {
        showToast("Asked agent to update goal");
    }).catch(() => {
        showToast("Failed to send refresh request", true);
    });
}

// Claude Code modes cycle via Shift+Tab (BTab in tmux).
// Order: default -> plan -> auto-accept -> default
const MODE_CYCLE = ["default", "auto", "plan"];

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

const MODE_LABELS = { default: "Default", plan: "Plan", auto: "Accept Edits" };

export function cycleModeToggle() {
    const current = detectCurrentMode();
    const currentIdx = MODE_CYCLE.indexOf(current);
    const nextMode = MODE_CYCLE[(currentIdx + 1) % MODE_CYCLE.length];
    showToast(`Switching to ${MODE_LABELS[nextMode]} mode`);
    sendRawKeys(["BTab"], { silent: true });
}

export function sendQuickCommand(command) {
    document.getElementById("command-input").value = command;
    sendCommand();
}

// ── Image Drag & Drop ──────────────────────────────────────────────────────

export function initImageDrop() {
    const commandPane = document.getElementById("command-pane");
    if (!commandPane) return;

    // Create drop overlay
    const overlay = document.createElement("div");
    overlay.id = "drop-overlay";
    overlay.className = "drop-overlay";
    overlay.innerHTML = `<div class="drop-overlay-content">
        <svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
            <rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="8.5" cy="8.5" r="1.5"/><path d="m21 15-5-5L5 21"/>
        </svg>
        <span>Drop image to attach</span>
    </div>`;
    overlay.style.display = "none";
    commandPane.style.position = "relative";
    commandPane.appendChild(overlay);

    let dragCounter = 0;

    commandPane.addEventListener("dragenter", (e) => {
        e.preventDefault();
        if (!hasImageFiles(e)) return;
        dragCounter++;
        overlay.style.display = "flex";
    });

    commandPane.addEventListener("dragover", (e) => {
        e.preventDefault();
        if (hasImageFiles(e)) e.dataTransfer.dropEffect = "copy";
    });

    commandPane.addEventListener("dragleave", (e) => {
        e.preventDefault();
        dragCounter--;
        if (dragCounter <= 0) {
            dragCounter = 0;
            overlay.style.display = "none";
        }
    });

    commandPane.addEventListener("drop", async (e) => {
        e.preventDefault();
        dragCounter = 0;
        overlay.style.display = "none";

        const files = [...(e.dataTransfer?.files || [])].filter(f =>
            f.type.startsWith("image/")
        );
        if (files.length === 0) {
            showToast("No image files found in drop", true);
            return;
        }

        for (const file of files) {
            await uploadAndInsertImage(file);
        }
    });

    // Also support paste — listen on document so it works even when textarea isn't focused
    const input = document.getElementById("command-input");
    const pasteHandler = async (e) => {
        // Only handle when a live session is selected
        if (!state.currentSession || state.currentSession.type !== "live") return;
        // Skip if pasting inside other inputs (not the command input)
        const tag = e.target.tagName;
        if ((tag === "INPUT" || tag === "TEXTAREA") && e.target.id !== "command-input") return;
        if (e.target.isContentEditable) return;

        const items = [...(e.clipboardData?.items || [])];
        const imageItems = items.filter(item => item.type.startsWith("image/"));
        if (imageItems.length === 0) return;

        // Extract all files synchronously before any async work,
        // because clipboardData items become invalid after the event.
        const files = imageItems.map(item => item.getAsFile()).filter(Boolean);
        if (files.length === 0) return;

        e.preventDefault();
        for (const file of files) {
            await uploadAndInsertImage(file);
        }
    };
    document.addEventListener("paste", pasteHandler);
}

function hasImageFiles(e) {
    if (e.dataTransfer?.types?.includes("Files")) {
        // Check items if available
        const items = e.dataTransfer.items;
        if (items) {
            for (const item of items) {
                if (item.kind === "file" && item.type.startsWith("image/")) return true;
            }
        }
        return true; // Can't check types on dragenter in some browsers, assume images
    }
    return false;
}

async function uploadAndInsertImage(file) {
    const formData = new FormData();
    formData.append("file", file);

    try {
        showToast(`Uploading ${file.name}...`);
        const resp = await fetch("/api/upload", {
            method: "POST",
            body: formData,
        });
        const result = await resp.json();
        if (result.error) {
            showToast(result.error, true);
            return;
        }

        // Create a local object URL for the thumbnail preview
        const previewUrl = URL.createObjectURL(file);
        pendingAttachments.push({
            path: result.path,
            filename: result.filename,
            previewUrl,
        });
        renderAttachments();

        document.getElementById("command-input").focus();
        showToast(`Attached: ${result.filename}`);
    } catch (e) {
        showToast("Failed to upload image", true);
        console.error("Upload error:", e);
    }
}

function renderAttachments() {
    const container = document.getElementById("image-attachments");
    if (!container) return;

    if (pendingAttachments.length === 0) {
        container.innerHTML = "";
        container.style.display = "none";
        return;
    }

    container.style.display = "flex";
    container.innerHTML = pendingAttachments.map((att, i) => `
        <div class="image-attachment" title="${escapeAttr(att.path)}">
            <img src="${att.previewUrl}" alt="${escapeAttr(att.filename)}" />
            <span class="image-attachment-name">${escapeHtml(att.filename)}</span>
            <button class="image-attachment-remove" onclick="removeAttachment(${i})" title="Remove">&times;</button>
        </div>
    `).join("");
}

export function removeAttachment(index) {
    const removed = pendingAttachments.splice(index, 1);
    if (removed[0]?.previewUrl) URL.revokeObjectURL(removed[0].previewUrl);
    renderAttachments();
}

function clearAttachments() {
    for (const att of pendingAttachments) {
        if (att.previewUrl) URL.revokeObjectURL(att.previewUrl);
    }
    pendingAttachments.length = 0;
    renderAttachments();
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
