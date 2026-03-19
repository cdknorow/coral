/* Session selection and management */

import { state, sessionKey } from './state.js';
import { showToast } from './utils.js';
import { loadLiveSessionDetail, loadHistoryMessages } from './api.js';
import { stopCaptureRefresh, startCaptureRefresh } from './capture.js';
import { updateSessionStatus, updateSessionSummary, updateSessionBranch, updateWaitingIndicator, renderHistoryChat } from './render.js';
import { renderQuickActions, updateSidebarActive } from './controls.js';
import { loadSessionNotes, switchHistoryTab } from './notes.js';
import { loadSessionTags } from './tags.js';
import { loadSessionCommits } from './commits.js';
import { loadAgentTasks } from './tasks.js';
import { loadChangedFiles } from './changed_files.js';
import { loadAgentNotes } from './agent_notes.js';
import { loadAgentEvents } from './agentic_state.js';
import { loadHistoryEvents, loadHistoryTasks, loadHistoryAgentNotes } from './history_tabs.js';
import { startLiveHistoryPoll, stopLiveHistoryPoll, resetLiveHistory } from './live_chat.js';
import { syncPaneWidth, resetSyncedCols } from './capture.js';
import { disposeTerminal, createTerminal, connectTerminalWs, disconnectTerminalWs } from './xterm_renderer.js';
import { getRendererMode } from './renderers.js';

export async function selectLiveSession(name, agentType, sessionId) {
    stopCaptureRefresh();
    stopLiveHistoryPoll();
    disconnectTerminalWs();

    // Save current input text for the old session
    const input = document.getElementById("command-input");
    const oldKey = sessionKey(state.currentSession);
    if (oldKey) {
        state.sessionInputText[oldKey] = input.value;
    }

    // Look up display_name and working_directory from live sessions data
    const agentData = state.liveSessions.find(s => s.session_id === sessionId);
    const displayName = agentData ? agentData.display_name : null;
    const workingDirectory = agentData ? agentData.working_directory : "";

    state.currentSession = {
        type: "live", name, agent_type: agentType || null, session_id: sessionId || null,
        display_name: displayName || null, working_directory: workingDirectory || "",
    };

    // Restore input text for the new session
    const newKey = sessionKey(state.currentSession);
    input.value = state.sessionInputText[newKey] || "";
    input.focus();

    // Show live view, hide others
    document.getElementById("welcome-screen").style.display = "none";
    document.getElementById("history-session-view").style.display = "none";
    document.getElementById("scheduler-view").style.display = "none";
    document.getElementById("messageboard-view").style.display = "none";
    document.getElementById("live-session-view").style.display = "flex";

    // Show loading skeleton
    const captureWrapper = document.getElementById("capture-wrapper");
    captureWrapper.classList.add("loading-skeleton");

    // Update header
    document.getElementById("session-name").textContent = displayName || name;
    const badge = document.getElementById("session-type-badge");
    badge.textContent = agentType || "claude";
    badge.className = `badge ${(agentType || "claude").toLowerCase()}`;

    // Reset summary/status before loading new session detail
    const summaryEl = document.getElementById("session-summary");
    if (summaryEl) summaryEl.style.display = "none";

    // Load detail for status/summary
    const detail = await loadLiveSessionDetail(name, agentType, sessionId);
    captureWrapper.classList.remove("loading-skeleton");
    if (detail) {
        updateSessionStatus(detail.status);
        updateSessionSummary(detail.summary);

        // Show initial pane capture
        if (detail.pane_capture) {
            document.getElementById("pane-capture").textContent = detail.pane_capture;
        }
    }

    // Update branch and waiting indicator from live sessions data
    const agent = state.liveSessions.find(s => s.session_id === sessionId);
    updateSessionBranch(agent && agent.branch ? agent.branch : null);
    updateWaitingIndicator(agent || {});

    // Set up quick action buttons
    state.currentCommands = (agent && agent.commands) || { compress: "/compact", clear: "/clear" };
    renderQuickActions();

    // Highlight in sidebar
    updateSidebarActive();

    // Load tasks, notes, events, and changed files for this agent (pass session_id)
    loadAgentTasks(name, sessionId);
    loadAgentNotes(name, sessionId);
    loadAgentEvents(name, sessionId);
    loadChangedFiles(name, sessionId);

    // Reset live history and start capture/terminal
    resetLiveHistory();
    const mode = getRendererMode(agentType, sessionId);
    // Always start capture refresh — it polls tasks and events for any mode
    startCaptureRefresh();
    if (mode === "xterm" && typeof Terminal !== 'undefined') {
        document.getElementById("pane-capture").style.display = "none";
        const container = document.getElementById("xterm-container");
        container.style.display = "flex";
        container.innerHTML = "";
        createTerminal(container);
        connectTerminalWs(name, agentType, sessionId);
    } else {
        document.getElementById("xterm-container").style.display = "none";
        document.getElementById("pane-capture").style.display = "";
        disposeTerminal();
        resetSyncedCols();
    }

    // Sync tmux pane width to match browser display after layout settles
    setTimeout(syncPaneWidth, 100);

    // Start history poll if the history tab is currently active
    const historyTab = document.getElementById("agentic-tab-history");
    if (historyTab && historyTab.classList.contains("active")) {
        startLiveHistoryPoll();
    }
}

export async function selectHistorySession(sessionId) {
    stopCaptureRefresh();
    disposeTerminal();

    // Save current input text for the old session
    const input = document.getElementById("command-input");
    const oldKey = sessionKey(state.currentSession);
    if (oldKey) {
        state.sessionInputText[oldKey] = input.value;
    }

    state.currentSession = { type: "history", name: sessionId };

    // Update URL hash for bookmarking
    window.location.hash = '#session/' + sessionId;

    // Restore input text for the new session
    const newKey = sessionKey(state.currentSession);
    input.value = state.sessionInputText[newKey] || "";

    document.getElementById("welcome-screen").style.display = "none";
    document.getElementById("live-session-view").style.display = "none";
    document.getElementById("scheduler-view").style.display = "none";
    document.getElementById("messageboard-view").style.display = "none";
    document.getElementById("history-session-view").style.display = "flex";

    document.getElementById("history-session-title").textContent = `Session: ${sessionId}`;
    document.getElementById("history-session-id").textContent = sessionId;

    // Update branch from history sessions data
    const historyEntry = state.historySessionsList.find(s => s.session_id === sessionId);
    const branchEl = document.getElementById("history-session-branch");
    if (historyEntry && historyEntry.branch) {
        branchEl.querySelector(".branch-text").textContent = historyEntry.branch;
        branchEl.style.display = "";
    } else {
        branchEl.style.display = "none";
    }

    // Show/hide Resume button based on source type
    const resumeBtn = document.getElementById("btn-resume-session");
    if (resumeBtn) {
        resumeBtn.style.display = (historyEntry && historyEntry.source_type === "claude") ? "" : "none";
    }

    updateSidebarActive();

    // Reset to summary tab
    switchHistoryTab('notes');

    const data = await loadHistoryMessages(sessionId);
    if (data && data.messages) {
        renderHistoryChat(data.messages);
    }

    // Load notes, tags, commits, and history tabs in parallel
    loadSessionNotes(sessionId);
    loadSessionTags(sessionId);
    loadSessionCommits(sessionId);
    loadHistoryEvents(sessionId);
    loadHistoryTasks(sessionId);
    loadHistoryAgentNotes(sessionId);
}

export async function renameAgent(name, agentType, sessionId) {
    const current = state.liveSessions.find(s => s.session_id === sessionId);
    const currentName = (current && current.display_name) || name;
    const newName = prompt("Enter display name for this agent:", currentName);
    if (!newName || newName.trim() === "" || newName.trim() === currentName) return;

    try {
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(name)}/display-name`, {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ display_name: newName.trim(), session_id: sessionId }),
        });
        const result = await resp.json();
        if (result.error) {
            showToast(result.error, true);
            return;
        }
        // Update in-memory state
        if (current) current.display_name = newName.trim();
        // Update header if this is the current session
        if (state.currentSession && state.currentSession.session_id === sessionId) {
            state.currentSession.display_name = newName.trim();
            document.getElementById("session-name").textContent = newName.trim();
        }
        // Re-render sidebar
        const { renderLiveSessions } = await import('./render.js');
        renderLiveSessions(state.liveSessions);
        showToast("Agent renamed");
    } catch (e) {
        showToast("Failed to rename agent", true);
    }
}

export function editAndResubmit(btn) {
    const bubble = btn.closest(".chat-bubble");
    const text = bubble.querySelector(".message-text").textContent;

    // Switch to a live session if one exists
    if (state.liveSessions.length > 0 && (!state.currentSession || state.currentSession.type !== "live")) {
        const s = state.liveSessions[0];
        selectLiveSession(s.name, s.agent_type, s.session_id);
    }

    // If we're viewing a live session, just populate the input
    if (state.currentSession && state.currentSession.type === "live") {
        document.getElementById("command-input").value = text;
        document.getElementById("command-input").focus();
        showToast("Message copied to input — edit and send");
    } else {
        showToast("No live session available to send to", true);
    }
}
