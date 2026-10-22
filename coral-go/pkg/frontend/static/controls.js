import { importOnce } from './import_once.js';
import { moveRestarting } from './session_overlay.js';
import { dashboardPromptRequested } from './dashboard_analytics.js';
/* Quick actions, command sending, mode toggling, and session controls */

import { state, sessionKey, saveSessionDraft } from './state.js';
import { updateSettingList, removeFirst } from './settings_lists.js';
import { claimOwnership } from './ownership.js';
import { popoutTerminalBlocked } from './popout.js';
import { escapeHtml, escapeAttr, showToast, showView } from './utils.js';
import { registerActions, idOf } from './actions.js';
import { stopCaptureRefresh } from './capture.js';
import { renderLiveSessions, KILL_LOG_NOTE } from './render.js';
import { loadAgentEvents, renderEventTimeline } from './agentic_state.js';
import { addPendingMessage, confirmPendingMessage, removePendingMessage, resetLiveHistory, effectiveLiveViewMode } from './live_chat.js';
import { serverFetch, currentServer, serverForSession, sessionServer, isCloud } from './server_base.js';
import { applySleepingView } from './sleeping_view.js';
import { hubBlocked, blockedReason } from './hub_access.js';
import { prepareImage, artifactErrorText, PICKER_ACCEPT, parseUploadReply, uploadStatusFailure, UPLOAD_UNFINISHED } from './image_attach.js';
import { attachmentsLead, sentToastText } from './shared_images.js';
import { loadModals } from './lazy_panels.js';
import { responseErrorText, responseError } from './server_status.js';
import { reportSendError, showComposerError, clearComposerError, awaitSocketAccept, nextInputId, refuseSocketSends } from './terminal_gone.js';
import { pollEvery } from './overload.js';
import { MODES, modeFromFooterText, pressesFor } from './mode_cycle.js';

// Lazy imports to avoid circular dependency (xterm_renderer imports controls)
let _xtermModule = null;
async function _getXtermModule() {
    if (!_xtermModule) _xtermModule = await importOnce('./xterm_renderer.js');
    return _xtermModule;
}

// Active upload promises scoped by session_id
const activeUploadPromisesBySession = new Map(); // session_id -> Promise[]
let _isSending = false;

// Attached image paths scoped by session_id (cleared on send)
const pendingAttachmentsBySession = new Map(); // session_id -> [{ path, filename, previewUrl }]

export function getPendingAttachments(session = state.currentSession) {
    const sid = session?.session_id;
    if (!sid) return [];
    if (!pendingAttachmentsBySession.has(sid)) {
        pendingAttachmentsBySession.set(sid, []);
    }
    return pendingAttachmentsBySession.get(sid);
}

// A sleeping agent has no terminal to receive input. The composer hides the
// send controls; this is the barrier behind them for every send path.
function sleepingBlocked(silent = false) {
    if (!(state.currentSession && state.currentSession.sleeping)) return false;
    if (!silent) showToast("Agent is sleeping — wake it to send commands", true);
    return true;
}

/** Send was pressed with nothing to send: focus the box and flash it once (#3770). */
function nudgeEmptyInput() {
    const input = document.getElementById("command-input");
    if (!input || input.disabled) return;
    input.focus();
    input.classList.remove("empty-send-nudge");
    void input.offsetWidth; // restart the animation if it is still running
    input.classList.add("empty-send-nudge");
    input.addEventListener("animationend", () => input.classList.remove("empty-send-nudge"), { once: true });
}

// nudge: the Send button was pressed (Enter on an empty box stays quiet).
export async function sendCommand({ nudge = false } = {}) {
    if (_isSending) return;
    const targetSession = state.currentSession;
    if (!targetSession || targetSession.type !== "live") {
        showToast("No live session selected", true);
        return;
    }
    if (sleepingBlocked()) return;
    if (popoutTerminalBlocked()) {
        showToast("Agent is not ready to receive input", true);
        return;
    }
    claimOwnership(); // sending from a viewer window takes interactive control

    const targetSessionId = targetSession.session_id;
    const targetSessionName = targetSession.name;
    const targetAgentType = targetSession.agent_type;

    _isSending = true;
    try {
        // Through the Cloud Hub, a View-only server drops terminal input silently:
        // refuse here so nothing says "Sent" and the draft is kept (#3068). Inside
        // the _isSending guard, so a double Enter cannot get past it twice.
        if (await hubBlocked(targetSession)) return;
        const activeUploads = activeUploadPromisesBySession.get(targetSessionId);
        if (activeUploads && activeUploads.length > 0) {
            showToast("Waiting for image upload to complete...");
            const results = await Promise.all(activeUploads);
            if (results.some(ok => ok === false)) {
                showToast("Image upload failed, message not sent", true);
                return;
            }
        }

        // Verify session did not change while waiting for uploads
        if (!state.currentSession || state.currentSession.session_id !== targetSessionId) {
            showToast("Session changed while uploading, send cancelled", true);
            return;
        }

        const input = document.getElementById("command-input");
        const textPart = input.value.trim();

        // Build the full command: image paths + text, space-separated
        const sessionAttachments = getPendingAttachments(targetSession);
        const parts = attachmentsLead(sessionAttachments); // each image's artifact link and one hint to fetch them, never the local path (#5481); the path only for an image with no artifact
        if (textPart) parts.push(textPart);

        const command = parts.join(" ");
        if (!command) { if (nudge) nudgeEmptyInput(); return; }
        if (targetAgentType !== 'terminal') dashboardPromptRequested();
        const sentSessionId = targetSessionId;
        const sentAt = Date.now();
        const hasAttachments = sessionAttachments.length > 0;

        // A message to an agent: one composer_send frame, answered only once the agent's input box has taken and
        // submitted it (#4587: an Enter typed while it started was lost, the text sat in its box, and Chat said Sent).
        if (!hasAttachments && targetAgentType !== 'terminal') {
            const xterm = await _getXtermModule();
            const id = nextInputId();
            if (xterm.sendComposerWs(command, id)) {
                const accepted = awaitSocketAccept(targetSession, [id]);
                addPendingMessage(sentSessionId, command, sentAt, { sending: true });
                input.value = "";
                xterm.focusTerminal();
                accepted.then(ok => _settleSocketSend(targetSession, command, sentAt, ok));
                return;
            }
        }
        // A shell: the WebSocket path types it as keystrokes (text, then Enter separately)
        if (!hasAttachments && targetAgentType === 'terminal') {
            const xterm = await _getXtermModule();
            // The server acknowledges the text and the Enter by id (#3276).
            const textId = nextInputId();
            if (xterm.sendTerminalInputWs(command, textId)) {
                const enterId = nextInputId();
                // The box clears now, but the saved draft only once the send is accepted:
                // a gone terminal refuses it and the text comes back (#3218).
                const accepted = awaitSocketAccept(targetSession, [textId, enterId]);
                // Send Enter after delay so bracket paste + tmux processing completes
                setTimeout(() => {
                    if (!xterm.sendTerminalInputWs("\r", enterId)) refuseSocketSends(targetSession.session_id, enterId);
                }, 300);
                addPendingMessage(sentSessionId, command, sentAt, { sending: true });
                input.value = "";
                xterm.focusTerminal();
                accepted.then(ok => _settleSocketSend(targetSession, command, sentAt, ok));
                return;
            }
        }

        // Fall back to POST endpoint. The message shows as Sending until this request's answer: Sent only when it says
        // the message was submitted (#4587; the server waits for a starting agent and confirms the submit). The box
        // is emptied meanwhile, so a second click has nothing to send again, and a refusal puts the text back.
        addPendingMessage(sentSessionId, command, sentAt, { sending: true });
        input.value = "";
        const notSent = () => {
            removePendingMessage(sentSessionId, sentAt);
            // The typed text only: the attachments stay attached until a send of them succeeds.
            if (!input.value && state.currentSession && state.currentSession.session_id === sentSessionId) input.value = textPart;
        };
        try {
            const resp = await serverFetch(sessionServer(targetSession), `/api/sessions/live/${encodeURIComponent(targetSessionName)}/send`, {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ command, agent_type: targetAgentType, session_id: sentSessionId }),
            });
            if (!resp.ok) {
                notSent();
                // Plain text naming the server; never the raw response (#3151). The draft stays.
                // Said under the composer (#3218); a gone terminal also marks the agent and offers Restart.
                const { text, code, exited } = await responseError(resp, sessionServer(targetSession), `The message was not sent (HTTP ${resp.status}).`);
                reportSendError(targetSession, text, code, exited);
                console.error("Send failed:", resp.status);
                return;
            }
            const result = await resp.json();
            if (result.error) {
                notSent();
                showComposerError(result.error);
                console.error("Send error:", result.error);
            } else {
                clearComposerError();
                confirmPendingMessage(sentSessionId, sentAt);
                clearAttachments(targetSession);
                const key = sessionKey(targetSession);
                if (key) saveSessionDraft(key, "");
                showToast(sentToastText(command));
                _getXtermModule().then(m => m.focusTerminal());
            }
        } catch (e) {
            notSent();
            showComposerError("The message was not sent. Check the connection and try again.");
            console.error("Send exception:", e);
        }
    } finally {
        _isSending = false;
    }
}

/** A composer send over the terminal socket was accepted or refused (#3218). */
function _settleSocketSend(session, command, sentAt, ok) {
    const key = sessionKey(session);
    const saved = (key && state.sessionInputText[key]) || "";
    const input = document.getElementById("command-input");
    const onIt = !!(input && state.currentSession && sessionKey(state.currentSession) === key);
    if (ok) {
        confirmPendingMessage(session.session_id, sentAt);
        // Text typed since the send is a new draft and stays.
        if (key && saved.trim() === command) saveSessionDraft(key, "");
        showToast(sentToastText(command));
        return;
    }
    removePendingMessage(session.session_id, sentAt);
    if (key && !saved.trim()) saveSessionDraft(key, command);
    if (onIt && !input.value) input.value = command;
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

    if (sleepingBlocked() || await hubBlocked(state.currentSession)) return; // before the reminder is added to the draft
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
    if (sleepingBlocked() || await hubBlocked(state.currentSession)) return; // before the protocol is added to the draft
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
    if (sleepingBlocked() || await hubBlocked(state.currentSession)) return; // before the prompt replaces the draft
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

// Macros are one list in one setting: a change is applied to the list the server has NOW (#4423), so a macro another tab
// added is kept, and a delete removes the macro the person clicked, not whatever is at that position now.
async function changeMacros(change) {
    try {
        const { raw } = await updateSettingList("macros", change, { fallback: DEFAULT_MACROS, local: getMacros() });
        state.settings.macros = raw;
    } catch (e) {
        console.error("Failed to save macros:", e);
    }
}

export async function executeMacro(command) {
    // Support chained commands with "&&" separator (1s delay between each)
    if (sleepingBlocked() || await hubBlocked(state.currentSession)) return; // keep the draft
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
    await changeMacros(list => [...list, { label, command }]);
    hideMacroModal();
    renderQuickActions();
}

export async function deleteMacro(index) {
    const target = getMacros()[index];
    if (!target) return;
    await changeMacros(list => removeFirst(list, m => m && m.label === target.label && m.command === target.command));
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

    const modeInfo = _modeLabelInfo();
    const modeButtons = `
        <button class="btn-nav btn-mode" id="btn-mode-toggle" data-on-click="cmd-mode-toggle" data-tooltip="${escapeAttr(modeInfo.tooltip)}" aria-label="${escapeAttr(modeInfo.aria)}"><svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M4 2h8a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1z"/><line x1="6" y1="5" x2="10" y2="5"/><line x1="6" y1="8" x2="10" y2="8"/><line x1="6" y1="11" x2="8" y2="11"/></svg><span class="btn-label">${escapeHtml(modeInfo.label)}</span></button>
        <button class="btn-nav btn-mode" data-on-click="cmd-quick" data-cmd="!" aria-label="Bash command" data-tooltip="Prefixes your input with ! so Claude runs it as a shell command instead of a prompt."><svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 3h12a1 1 0 0 1 1 1v8a1 1 0 0 1-1 1H2a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z"/><polyline points="4 7 6 9 4 11"/><line x1="8" y1="11" x2="12" y2="11"/></svg><span class="btn-label">Bash</span></button>
        <button class="btn-nav btn-mode" data-on-click="cmd-raw-keys" data-keys="Escape,Escape" aria-label="Undo" data-tooltip="Sends two Escape keys to Claude. Interrupts the current response, rejects a pending tool call, or backs out of a permission prompt."><svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3.5 2v4h4"/><path d="M3.5 6A5.5 5.5 0 1 1 2.5 8"/></svg><span class="btn-label">Undo</span></button>
    `;

    const macroButtons = macros.map((m, i) => {
        const cls = m.danger ? "btn-nav btn-danger" : "btn-nav";
        return `<span class="macro-btn-wrap">
            <button class="${escapeAttr(cls)}" data-on-click="cmd-macro" data-index="${Number(i)}" data-tooltip="${escapeAttr(m.command)}">${escapeHtml(m.label)}</button>
            <button class="macro-delete-btn" data-on-click="macro-delete" data-index="${Number(i)}" title="Remove macro">&times;</button>
        </span>`;
    }).join("");

    const navButtons = `
        <button class="btn-nav btn-esc" data-on-click="cmd-raw-keys" data-keys="Escape" title="Stop — sends Escape to interrupt the agent or dismiss its prompt" aria-label="Stop (Escape)"><span class="esc-label-terminal" aria-hidden="true">Esc</span><span class="esc-label-chat" aria-hidden="true"><span class="material-icons" aria-hidden="true">stop</span></span></button>
        <button class="btn-nav btn-nav-key" data-on-click="cmd-raw-keys" data-keys="Up" title="Arrow Up" aria-label="Arrow Up">&uarr;</button>
        <button class="btn-nav btn-nav-key" data-on-click="cmd-raw-keys" data-keys="Down" title="Arrow Down" aria-label="Arrow Down">&darr;</button>
        <button class="btn-nav btn-enter btn-nav-key" data-on-click="cmd-raw-keys" data-keys="Enter" data-tooltip="Sends an Enter keypress to the terminal" aria-label="Enter">&#9166;</button>
    `;

    const chipBar = document.getElementById("model-chip-bar"); // the toolbar's first item (model_chip.js owns it): kept, with its listeners, across the rebuild
    toolbar.innerHTML = `
        <div class="toolbar-group toolbar-group-modes">
            ${modeButtons}
        </div>
        <span class="toolbar-divider"></span>
        <div class="toolbar-group toolbar-group-macros">
            ${macroButtons}
            <button class="btn-nav btn-add-macro" data-on-click="macro-show" title="Add macro" aria-label="Add macro">+</button>
        </div>
        <span class="toolbar-spacer"></span>
        <div class="toolbar-group toolbar-group-nav">
            ${navButtons}
            <div class="toolbar-send-split">
                <button class="btn-nav btn-send" data-on-click="cmd-send" data-tooltip="Sends the text from the input box below to the terminal" aria-label="Send command"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="22" y1="2" x2="11" y2="13"/><polygon points="22 2 15 22 11 13 2 9 22 2"/></svg></button>
                <button class="btn-nav btn-send-dropdown" data-on-click="cmd-send-menu" aria-label="Send options" title="Send options">
                    <svg width="8" height="8" viewBox="0 0 16 16" fill="currentColor"><path d="M4 6l4 4 4-4z"/></svg>
                </button>
                <div class="send-btn-menu" style="display:none">
                    <button class="send-menu-item" data-on-click="send-menu-send">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="22" y1="2" x2="11" y2="13"/><polygon points="22 2 15 22 11 13 2 9 22 2"/></svg>
                        Send
                        <span class="send-menu-hint"><kbd>Ctrl</kbd>+<kbd>Enter</kbd></span>
                    </button>
                    <button class="send-menu-item" data-on-click="send-menu-team">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="9" cy="7" r="3"/><circle cx="17" cy="7" r="3"/><path d="M3 21v-2a4 4 0 0 1 4-4h4a4 4 0 0 1 4 4v2"/><path d="M17 11a4 4 0 0 1 4 4v2"/></svg>
                        Send + Team Reminder
                    </button>
                    <button class="send-menu-item" data-on-click="send-menu-resend">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="1 4 1 10 7 10"/><path d="M3.51 15a9 9 0 1 0 2.13-9.36L1 10"/></svg>
                        Resend Prompt
                    </button>
                    <button class="send-menu-item" data-on-click="send-menu-protocol">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="18" height="18" rx="2"/><line x1="8" y1="8" x2="16" y2="8"/><line x1="8" y1="12" x2="16" y2="12"/><line x1="8" y1="16" x2="12" y2="16"/></svg>
                        Send Board Protocol
                    </button>
                </div>
            </div>
        </div>
    `;
    if (chipBar) toolbar.prepend(chipBar);
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
    if (sleepingBlocked(silent)) return;
    if (await hubBlocked(state.currentSession, { silent })) return;
    if (popoutTerminalBlocked()) {
        if (!silent) showToast("Agent is not ready to receive input", true);
        return;
    }
    claimOwnership();

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
        const resp = await serverFetch(currentServer(), `/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/keys`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ keys, agent_type: state.currentSession.agent_type, session_id: state.currentSession.session_id }),
        });
        if (!resp.ok) {
            const { text, code } = await responseError(resp, currentServer(), `The keys were not sent (HTTP ${resp.status}).`);
            reportSendError(state.currentSession, text, code);
            return;
        }
        const result = await resp.json();
        if (result.error) {
            showComposerError(result.error);
        } else {
            clearComposerError();
            if (!silent) showToast(`Sent: ${keys.join(" + ")}`);
        }
    } catch (e) {
        showComposerError("The keys were not sent. Check the connection and try again.");
        console.error("sendRawKeys exception:", e);
    }
}

export async function killSession() {
    if (!state.currentSession || state.currentSession.type !== "live") {
        showToast("No live session selected", true);
        return;
    }

    window.showConfirmModal('Kill Session', `Kill session "${state.currentSession.name}"? This will terminate the agent. ${KILL_LOG_NOTE}`, async () => {
        try {
            const resp = await serverFetch(currentServer(), `/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/kill`, {
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
                // The row goes at once; the agent's chat stays in History.
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

async function showRestartModal() {
    // The agent config form (renderAgentConfigForm / getAgentConfig) lives with the
    // dialogs, which load on first use (#3140): load it before showing, so a restart
    // never goes out with an empty config. A failed load already showed a toast.
    try { await loadModals(); } catch { return; }
    const s = state.currentSession;
    document.getElementById("restart-modal-name").textContent =
        `Restarting: ${s.display_name || s.name}`;

    // Render unified config form pre-filled from session
    if (window.renderAgentConfigForm) {
        window.renderAgentConfigForm('restart-acf', {
            showPreset: false,
            showName: false,
            sections: true,
            value: {
                agentType: s.agent_type || 'claude',
                model: s.model || '',
                thinking: s.thinking || undefined, // the level it was started with: shown, and kept by the restart (#4969)
                thinkingActual: s.thinking_actual || '', // what it last ran at: a note under Thinking when that differs (#5105)
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

// Polls until the replacement session is live, then ends its restart (the overlay goes if that agent is on screen) and runs onLive; ~30 s at most.
function watchRestarted(xtermMod, name, sessionId, agentType, onLive) {
    let pollCount = 0;
    const pollInterval = pollEvery(async () => {
        pollCount++;
        try {
            // The detail route needs the exact target, agent_type with session_id; without it the
            // answer is 400 and the terminal never reattached after a restart (#3469).
            const liveResp = await serverFetch(currentServer(), `/api/sessions/live/${encodeURIComponent(name)}?session_id=${encodeURIComponent(sessionId)}&agent_type=${encodeURIComponent(agentType || '')}`);
            if (liveResp.ok) {
                clearInterval(pollInterval);
                xtermMod.setRestarting(false, sessionId);
                onLive();
                return;
            }
        } catch (_) { /* ignore fetch errors during poll */ }
        if (pollCount >= 15) {
            // Timeout after ~30s: the overlay goes and the Operator is told
            clearInterval(pollInterval);
            xtermMod.setRestarting(false, sessionId);
            showToast("Restart timed out — session may not have started", true);
        }
    }, 2000, { whenHidden: true }); // follows a Restart the user just asked for
}

export async function confirmRestart() {
    const config = window.getAgentConfig ? window.getAgentConfig('restart-acf') : {};

    hideRestartModal();

    const restartId = state.currentSession && state.currentSession.session_id;
    try {
        // Show restarting overlay instead of session-ended
        const xtermMod = await _getXtermModule();
        if (xtermMod.setRestarting) xtermMod.setRestarting(true, restartId);
        showToast(`Restarting ${state.currentSession.name}...`);
        const payload = { agent_type: config.agentType || state.currentSession.agent_type, session_id: state.currentSession.session_id };
        if (config.flags) {
            payload.extra_flags = config.flags;
        }
        if (config.prompt) payload.prompt = config.prompt;
        if (config.model !== undefined) payload.model = config.model;
        if (config.thinking !== undefined) payload.thinking = config.thinking; // only when chosen: a restart keeps the agent's level otherwise (#4969)
        if (config.capabilities) payload.capabilities = config.capabilities;
        const resp = await serverFetch(currentServer(), `/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/restart`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
        });
        const result = await resp.json();
        if (result.error) {
            xtermMod.setRestarting(false, restartId);
            showToast(result.error, true);
        } else if (!state.currentSession || state.currentSession.session_id !== restartId) {
            // The Operator switched to another agent while this one restarted: the screen belongs to that agent now, so nothing of it is
            // touched; the restarting mark moves to the replacement session and ends when that one is live (#4481).
            const replacement = result.session_id || restartId;
            moveRestarting(restartId, replacement);
            const name = result.session_name || result.agent_name || '';
            watchRestarted(xtermMod, name, replacement, config.agentType || payload.agent_type, () => {});
        } else {
            // A restarted sleeping agent is running again
            state.currentSession.sleeping = false;
            applySleepingView(false);
            // Update state with the new session_id (tmux session was renamed)
            if (result.session_id) {
                state.currentSession.session_id = result.session_id;
                moveRestarting(restartId, result.session_id); // the restart is now the replacement session's
            }
            if (config.agentType) {
                state.currentSession.agent_type = config.agentType;
            }
            // Restart renames the tmux session and returns its canonical name.
            // Keep the browser target aligned with the replacement session.
            const restartedName = result.session_name || result.agent_name;
            // The terminal reconnects to the replacement tmux session, not the old one (#3469).
            state.currentSession.tmux_session = result.session_name || null;
            if (restartedName) {
                state.currentSession.name = restartedName;
                document.getElementById("session-name").textContent = restartedName;
            }
            // The replacement session has an independent transcript and count.
            // Drop the old reader generation before the next poll, otherwise an
            // empty/new transcript is queried with the old session's offset.
            resetLiveHistory({ resume: true });
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
            watchRestarted(xtermMod, newName, newSessionId, newAgentType, () => {
                // Chat view has no terminal socket to take over (#3719): the Terminal view attaches when it is shown.
                if (state.currentSession && state.currentSession.session_id === newSessionId && effectiveLiveViewMode() === 'terminal') xtermMod.connectTerminalWs(state.currentSession.tmux_session || newName, newAgentType, newSessionId, currentServer());
            });
        }
    } catch (e) {
        if (restartId) _getXtermModule().then((m) => m.setRestarting(false, restartId)).catch(() => {});
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
                await serverFetch(currentServer(), `/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/events`, {
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
    const s = state.currentSession;
    requestGoal(s.name, s.agent_type, s.session_id);
}

// Asks the server's goal generator for a fresh goal line. It reads the
// agent's transcript in the background; nothing is typed into the agent's
// terminal. The new goal arrives on the normal session updates.
export function requestGoal(name, agentType, sessionId) {
    serverFetch(serverForSession(name, sessionId), `/api/sessions/live/${encodeURIComponent(name)}/goal`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ agent_type: agentType, session_id: sessionId }),
    }).then(async (resp) => {
        if (resp.ok) {
            showToast("Updating goal…");
            return;
        }
        const data = await resp.json().catch(() => ({}));
        showToast(data.error || "Failed to update goal", true);
    }).catch(() => {
        showToast("Failed to update goal", true);
    });
}

/** The mode the CLI's own mode line shows now, or null when there is no mode line to read (nothing captured, a non-Claude session, a screen not at the prompt). This is the ONLY thing key presses are counted from. */
function detectFooterMode() {
    if (state.currentSession?.agent_type === "terminal") return null;
    const el = document.getElementById("pane-capture");
    if (el?.dataset.captureState === "error") return null;
    return modeFromFooterText(el?.textContent);
}

/** The hook's mode (live_permission_mode: Claude's permission_mode, kept by the server), as a key of MODES, or null. It is the AGENT's own report: it is shown as a label, never trusted to count key presses. */
function reportedMode() {
    if (state.currentSession?.agent_type === "terminal") return null;
    const sig = state.currentSession?.live_permission_mode;
    return typeof sig === "string" && Object.prototype.hasOwnProperty.call(MODES, sig) ? sig : null;
}

/**
 * The mode for the button's LABEL: the screen's mode line (the status line NOW; Shift+Tab at an idle prompt sends no hook event, so the hook's mode is as of the last event), else the mode the
 * agent reported (flagged `reported`). { mode: key|null, reported: boolean }. Never a guess: no mode line and no report is unknown.
 */
function detectLabelMode() {
    const footer = detectFooterMode();
    if (footer) return { mode: footer, reported: false };
    const rep = reportedMode();
    return rep ? { mode: rep, reported: true } : { mode: null, reported: false };
}

function _modeLabelInfo() {
    const { mode: current, reported } = detectLabelMode();
    if (!current) {
        return {
            label: "Mode",
            tooltip: "Cycles Claude's permission mode (Shift+Tab). Each click advances one step.",
            aria: "Mode: unknown. Click to cycle Claude's permission mode (Shift+Tab).",
        };
    }
    const m = MODES[current];
    // A mode the agent reported is a label only: the next step is not named from it (the screen's own mode line is what a click is counted from).
    const next = !reported && m.next ? MODES[m.next].label : "";
    const tail = next ? `Click to switch to ${next} (Shift+Tab).` : "Click for the next mode (Shift+Tab).";
    // Claude calls its starting mode Manual; Coral's Settings and launch form call the same mode Default: the tooltip and the name say so once
    const who = (reported ? " (reported by the agent)" : "") + (current === "default" ? " (Default in Settings)" : "");
    return { label: m.short, tooltip: `Current: ${m.label}${who}. ${tail}`, aria: `Mode: ${m.label}${who}. ${tail}` };
}

/** Sync the Mode quick-action label/tooltip with the terminal buffer. */
export function refreshModeLabel() {
    const btn = document.getElementById("btn-mode-toggle");
    if (!btn) return;
    const info = _modeLabelInfo();
    const labelEl = btn.querySelector(".btn-label");
    if (labelEl && labelEl.textContent !== info.label) labelEl.textContent = info.label;
    if (btn.getAttribute("data-tooltip") !== info.tooltip) btn.setAttribute("data-tooltip", info.tooltip);
    if (btn.getAttribute("aria-label") !== info.aria) btn.setAttribute("aria-label", info.aria);
}

// Re-read the mode every time the terminal buffer is rescanned (capture poll
// or initial pane detail), so Shift+Tab pressed directly in the terminal is
// reflected in the button, not only clicks on it.
if (typeof document !== "undefined") {
    document.addEventListener("coral:terminal-updated", refreshModeLabel);
}

export function sendModeToggle(targetMode) {
    // Presses are counted ONLY from the mode line on the screen: the agent's own report can be wrong (or forged), and a wrong start would land on a more permissive mode than the person chose.
    const current = detectFooterMode();
    if (!current) {
        // no mode line to count from: one step, then the footer says where it landed and the person chooses again
        showToast("Stepping one mode: check the mode line, then choose again");
        sendRawKeys(["BTab"], { silent: true });
        return;
    }
    if (current === targetMode) {
        showToast(`Already in ${MODES[targetMode] ? MODES[targetMode].label : "that"} mode`);
        return;
    }
    // Only the steps that hold in every session can be counted ahead (Default, Accept edits, Plan); from or to another mode the Mode button steps one at a time and the footer says where it is.
    const presses = pressesFor(current, targetMode);
    if (presses === null) { showToast("Use the Mode button to step to that mode"); return; }

    const keys = Array(presses).fill("BTab");
    sendRawKeys(keys);
}

export function cycleModeToggle() {
    // One press, whatever the label says; the next mode is named only from the screen's mode line, never from the agent's report.
    const m = MODES[detectFooterMode() || ""];
    showToast(m && m.next ? `Switching to ${MODES[m.next].label} mode` : "Switching to the next mode");
    sendRawKeys(["BTab"], { silent: true });
}

export async function sendQuickCommand(command) {
    if (sleepingBlocked() || await hubBlocked(state.currentSession)) return; // keep the draft
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

/** The Add image button (#6088): opens the file picker (a phone offers Photo Library, Take Photo and Browse). Launch tier through Coral Hub, as the upload route is: a refused account is told why. This runs
 *  straight from the tap, with nothing awaited before the click, or a phone would not open the picker. */
export function openImagePicker() {
    const session = state.currentSession;
    if (!session || session.type !== "live") return;
    if (sleepingBlocked()) return;
    const why = blockedReason(sessionServer(session), "launch");
    if (why) { showToast(why, true); return; }
    const input = document.getElementById("attach-image-input");
    if (!input) return;
    if (!input.dataset.bound) { // bound on the first press, not at startup: the startup sequence stays as it was
        input.dataset.bound = "1";
        // The files are taken before the input is cleared, so the same photo can be chosen again.
        input.addEventListener("change", () => {
            const files = [...input.files];
            input.value = "";
            if (files.length) uploadPickedImages(files);
        });
    }
    input.accept = PICKER_ACCEPT;
    input.click();
}

/** Pictures chosen in the picker go up one after another, not at once (a phone on a slow connection, and the hub's limit on parallel requests); each is made smaller first when it is a big photo. */
export async function uploadPickedImages(files, session = state.currentSession) {
    for (const file of files) {
        const prepared = await prepareImage(file, { viaHub: isCloud() });
        if (prepared.error) { showToast(prepared.error, true); continue; }
        await uploadAndInsertImage(prepared.file, session);
    }
}

export async function uploadAndInsertImage(file, session = state.currentSession) {
    const targetSession = session || state.currentSession;
    const sid = targetSession?.session_id;
    if (!sid) return false;

    const uploadPromise = (async () => {
        const formData = new FormData();
        formData.append("file", file);
        formData.append("session_id", sid); // the agent this image is for: its artifact copy goes on that agent's record (#5284)

        try {
            // The path is typed into the agent, so the file must land on the agent's own server.
            const resp = await serverFetch(sessionServer(targetSession), "/api/upload", {
                method: "POST",
                body: formData,
            });
            // A failed upload says so in a sentence, with Retry where trying again can work (#6088: through Coral Hub an upload can be cut short and answered with an EMPTY 200).
            const failed = (text, retry) => showToast(text, true, 10000, retry ? { action: { label: "Retry", onClick: () => uploadAndInsertImage(file, targetSession) } } : {});
            if (!resp.ok) {
                const known = uploadStatusFailure(resp.status);
                if (known) failed(known.text, known.retry);
                else showToast(`Image not uploaded: ${await responseErrorText(resp, sessionServer(targetSession), `HTTP ${resp.status}`)}`, true);
                return false;
            }
            const result = parseUploadReply(await resp.text());
            if (!result) { failed(UPLOAD_UNFINISHED, true); return false; }
            if (result.error) {
                showToast(result.error, true);
                return false;
            }

            // Create a local object URL for the thumbnail preview
            const previewUrl = URL.createObjectURL(file);
            const list = getPendingAttachments(targetSession);
            list.push({
                path: result.path,
                filename: result.filename,
                previewUrl,
                artifact: result.artifact || null, // the image as an artifact (#5284); null with artifact_error saying why
            });
            if (state.currentSession?.session_id === sid) {
                renderAttachments(targetSession);
                document.getElementById("command-input")?.focus();
            }
            // The chip says what was attached (a toast per file covered Send on a phone, #6160): a screen reader is told in a hidden status line; only a copy that could not be shared is a toast, as it is news the chip does not carry.
            const status = document.getElementById("image-attach-status");
            if (status) status.textContent = list.length === 1 ? `Attached ${result.filename}` : `${list.length} images attached`;
            if (result.artifact_error) showToast(`Attached ${result.filename}, not shared: ${artifactErrorText(result)}`, true);
            return true;
        } catch (e) {
            showToast(UPLOAD_UNFINISHED, true, 10000, { action: { label: "Retry", onClick: () => uploadAndInsertImage(file, targetSession) } });
            console.error("Upload error:", e);
            return false;
        }
    })();

    const activeList = activeUploadPromisesBySession.get(sid) || [];
    activeList.push(uploadPromise);
    activeUploadPromisesBySession.set(sid, activeList);
    if (state.currentSession?.session_id === sid) renderAttachments(targetSession); // "Uploading" shows in the chip row, not as a toast per file (#6160)

    try {
        return await uploadPromise;
    } finally {
        const currentList = activeUploadPromisesBySession.get(sid) || [];
        const nextList = currentList.filter(p => p !== uploadPromise);
        if (nextList.length > 0) {
            activeUploadPromisesBySession.set(sid, nextList);
        } else {
            activeUploadPromisesBySession.delete(sid);
        }
        if (state.currentSession?.session_id === sid) renderAttachments(targetSession);
    }
}

// An attachment is named by its place in the strip ("Image 2"), not by its file's name: the name is a generated id that says nothing; it stays in the tooltip.
const imageName = (i, n) => (n > 1 ? `Image ${i + 1}` : 'Image');

export function renderAttachments(session = state.currentSession) {
    const container = document.getElementById("image-attachments");
    if (!container) return;

    const list = getPendingAttachments(session);
    const uploading = (activeUploadPromisesBySession.get(session?.session_id) || []).length;
    if (list.length === 0 && uploading === 0) {
        container.innerHTML = "";
        container.style.display = "none";
        return;
    }

    container.style.display = "flex";
    const uploadingText = `Uploading ${uploading === 1 ? "image" : `${uploading} images`}…`;
    const pending = uploading ? `<div class="image-uploading">${escapeHtml(uploadingText)}</div>` : "";
    container.innerHTML = list.map((att, i) => `
        <div class="image-attachment" title="${escapeAttr(att.filename)}">
            <img src="${escapeAttr(att.previewUrl)}" alt="${escapeAttr(imageName(i, list.length))}" />
            <button type="button" class="image-attachment-remove" data-on-click="attachment-remove" data-index="${Number(i)}" aria-label="Remove ${escapeAttr(imageName(i, list.length).toLowerCase())}" title="Remove ${escapeAttr(att.filename)}">&times;</button>
        </div>
    `).join("") + pending;
}

export function removeAttachment(index, session = state.currentSession) {
    const targetSession = session || state.currentSession;
    const list = getPendingAttachments(targetSession);
    const removed = list.splice(index, 1);
    if (removed[0]?.previewUrl) URL.revokeObjectURL(removed[0].previewUrl);
    renderAttachments(targetSession);
}

function clearAttachments(session = state.currentSession) {
    const targetSession = session || state.currentSession;
    const sid = targetSession?.session_id;
    if (!sid) return;
    const list = pendingAttachmentsBySession.get(sid) || [];
    for (const att of list) {
        if (att.previewUrl) URL.revokeObjectURL(att.previewUrl);
    }
    pendingAttachmentsBySession.set(sid, []);
    if (state.currentSession?.session_id === sid) {
        renderAttachments(targetSession);
    }
}

/** Moves the highlight between live agents by touching the two rows that change (the one that was active, the one that is now), not by drawing every row again
 *  (#4990: a switch cost a full list render, about 10 ms at 4x and the browser's style and layout for every row). A row's markup depends on the selection in
 *  two ways only, its `active` class and aria-current, and the collapsed rail's tile has the same class, so those are what move. False when it cannot be
 *  done here (a team or a history entry is selected, or the new agent has no row yet): the list is then drawn as before. */
function moveActiveRow() {
    const cur = state.currentSession;
    if (state.selectedTeam || !cur || cur.type !== "live" || !cur.session_id) return false;
    const list = document.getElementById("live-sessions-list") || document.getElementById("mobile-session-list");
    const sid = cur.session_id;
    const next = list && list.querySelector(`li.session-group-item[data-session-id="${CSS.escape(sid)}"]`);
    if (!next) return false;
    for (const li of document.querySelectorAll(".session-list li.active")) {
        if (li === next) continue;
        li.classList.remove("active");
        li.removeAttribute("aria-current");
    }
    if (!next.classList.contains("active")) { next.classList.add("active"); next.setAttribute("aria-current", "true"); }
    for (const tile of document.querySelectorAll("#sidebar-rail .sidebar-rail-item")) tile.classList.toggle("active", tile.dataset.sessionId === sid);
    return true;
}

export function updateSidebarActive() {
    if (moveActiveRow()) {
        // a history entry that was highlighted is not any more
        document.querySelectorAll("#history-sessions-list li.active").forEach(li => li.classList.remove("active"));
        return;
    }
    document.querySelectorAll(".session-list li").forEach(li => li.classList.remove("active"));
    if (state.liveSessions.length) renderLiveSessions(state.liveSessions);

    // Highlight the active history session: its row carries the session id (data-session-id)
    if (state.currentSession && state.currentSession.type === "history") {
        const sid = state.currentSession.name;
        document.querySelectorAll("#history-sessions-list li").forEach(li => {
            if ((li.dataset.sessionId || "") === sid) li.classList.add("active");
        });
    }
}

// The command pane's toolbar, send menu and attachments (#3645): data-on-* names, never code. Keys
// and the quick command come from allowlists; a macro runs by its index in the saved list, so its
// command never sits in an attribute; an index is checked (a bad one does nothing).
const RAW_KEYS = new Set(['Escape', 'Up', 'Down', 'Enter']);
const indexOf = v => (v === '0' ? 0 : idOf(v));
registerActions({
    'cmd-mode-toggle': () => cycleModeToggle(),
    'cmd-quick': el => { if (el.dataset.cmd === '!') sendQuickCommand('!'); },
    'cmd-raw-keys': el => {
        const keys = String(el.dataset.keys || '').split(',');
        if (keys.length && keys.length <= 4 && keys.every(k => RAW_KEYS.has(k))) sendRawKeys(keys);
    },
    'cmd-macro': el => {
        const i = indexOf(el.dataset.index), m = i === null ? null : getMacros()[i];
        if (m) executeMacro(m.command);
    },
    'macro-delete': el => { const i = indexOf(el.dataset.index); if (i !== null) deleteMacro(i); },
    'macro-show': () => showMacroModal(),
    'cmd-send': () => sendCommand({ nudge: true }),
    'cmd-send-team': () => sendCommandWithTeam(),
    'attach-image': () => openImagePicker(),
    'cmd-send-menu': el => window.toggleSendMenu(el),
    'send-menu-send': () => { sendCommand({ nudge: true }); window.closeSendMenu(); },
    'send-menu-team': () => { sendCommandWithTeam(); window.closeSendMenu(); },
    'send-menu-resend': () => { resendInputPrompt(); window.closeSendMenu(); },
    'send-menu-protocol': () => { sendBoardProtocol(); window.closeSendMenu(); },
    'attachment-remove': el => { const i = indexOf(el.dataset.index); if (i !== null) removeAttachment(i); },
});
