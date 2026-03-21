/* Session selection and management */

import { state, sessionKey } from './state.js';
import { showToast, escapeHtml, escapeAttr } from './utils.js';
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
import { invalidateFileCache, fetchFileList } from './file_mention.js';

export async function selectLiveSession(name, agentType, sessionId) {
    stopCaptureRefresh();
    stopLiveHistoryPoll();
    disconnectTerminalWs();
    invalidateFileCache();

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

    // Show sleeping overlay if agent is sleeping
    const sleepOverlay = document.getElementById('session-sleeping-overlay');
    if (sleepOverlay) {
        sleepOverlay.style.display = (agent && agent.sleeping) ? '' : 'none';
    }

    // Set up quick action buttons
    state.currentCommands = (agent && agent.commands) || [
        { name: "compact", command: "/compact", description: "Compress conversation history" },
        { name: "clear", command: "/clear", description: "Clear conversation and start fresh" },
    ];
    renderQuickActions();

    // Highlight in sidebar
    updateSidebarActive();

    // Load tasks, notes, events, and changed files for this agent (pass session_id)
    loadAgentTasks(name, sessionId);
    loadAgentNotes(name, sessionId);
    loadAgentEvents(name, sessionId);
    loadChangedFiles(name, sessionId);
    fetchFileList();  // prefetch for @file mention

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

    // Show board link if session was part of a message board
    const boardLink = document.getElementById("history-session-board-link");
    if (boardLink) {
        const boardName = historyEntry?.board_project || historyEntry?.board_name;
        if (boardName) {
            boardLink.textContent = boardName;
            boardLink.onclick = () => { if (window.selectBoardProject) window.selectBoardProject(boardName); };
            boardLink.style.display = "";
        } else {
            boardLink.style.display = "none";
        }
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

export async function setAgentIcon(name, agentType, sessionId) {
    showEmojiPicker(async (emoji) => {
        try {
            const resp = await fetch(`/api/sessions/live/${encodeURIComponent(name)}/icon`, {
                method: "PUT",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ icon: emoji, session_id: sessionId }),
            });
            const result = await resp.json();
            if (result.error) {
                showToast(result.error, true);
                return;
            }
            const current = state.liveSessions.find(s => s.session_id === sessionId);
            if (current) current.icon = emoji;
            const { renderLiveSessions } = await import('./render.js');
            renderLiveSessions(state.liveSessions);
            showToast(emoji ? "Icon set" : "Icon removed");
        } catch (e) {
            showToast("Failed to set icon", true);
        }
    });
}

// ── Emoji Picker ─────────────────────────────────────────────────────

const EMOJI_CATEGORIES = [
    { name: "People", emojis: ["👨‍💻", "👩‍💻", "🧑‍🔬", "🕵️", "👷", "🧑‍🎨", "🧑‍💼", "🤖", "🧙", "🦸", "🥷", "👨‍🏫", "👩‍⚕️", "🧑‍🚀", "🧑‍🍳"] },
    { name: "Animals", emojis: ["🐙", "🦊", "🐺", "🦅", "🐝", "🐬", "🦁", "🐻", "🐲", "🦄", "🐍", "🦉", "🐧", "🦈", "🐢"] },
    { name: "Objects", emojis: ["⚡", "🔥", "💎", "🚀", "⭐", "🎯", "🛡️", "⚙️", "🔧", "💡", "🎨", "📡", "🔬", "💻", "🗡️"] },
    { name: "Symbols", emojis: ["✨", "💫", "🌟", "❤️", "🏆", "👑", "🎪", "🎵", "🌈", "☀️", "🌙", "❄️", "🔮", "🎲", "♟️"] },
];

export function showEmojiPicker(onSelect) {
    // Remove existing picker
    const existing = document.getElementById("emoji-picker-popover");
    if (existing) existing.remove();

    const picker = document.createElement("div");
    picker.id = "emoji-picker-popover";
    picker.className = "emoji-picker";
    picker.innerHTML = `
        <div class="emoji-picker-header">
            <input type="text" class="emoji-picker-search" placeholder="Search or paste emoji..." autofocus>
            <button class="emoji-picker-clear" title="Clear icon">✕ Clear</button>
        </div>
        <div class="emoji-picker-grid">
            ${EMOJI_CATEGORIES.map(cat => `
                <div class="emoji-picker-category">${escapeHtml(cat.name)}</div>
                <div class="emoji-picker-row">${cat.emojis.map(e => `<button class="emoji-picker-btn" data-emoji="${escapeAttr(e)}">${e}</button>`).join("")}</div>
            `).join("")}
        </div>
    `;
    document.body.appendChild(picker);

    const searchInput = picker.querySelector(".emoji-picker-search");
    const grid = picker.querySelector(".emoji-picker-grid");
    const allEmojis = EMOJI_CATEGORIES.flatMap(c => c.emojis);

    // Search/filter
    searchInput.addEventListener("input", () => {
        const q = searchInput.value.trim().toLowerCase();
        if (!q) {
            grid.innerHTML = EMOJI_CATEGORIES.map(cat => `
                <div class="emoji-picker-category">${escapeHtml(cat.name)}</div>
                <div class="emoji-picker-row">${cat.emojis.map(e => `<button class="emoji-picker-btn" data-emoji="${escapeAttr(e)}">${e}</button>`).join("")}</div>
            `).join("");
        } else {
            // Filter emojis or allow direct paste
            const filtered = allEmojis.filter(e => e.includes(q));
            grid.innerHTML = filtered.length
                ? `<div class="emoji-picker-row">${filtered.map(e => `<button class="emoji-picker-btn" data-emoji="${escapeAttr(e)}">${e}</button>`).join("")}</div>`
                : `<div class="emoji-picker-row"><button class="emoji-picker-btn" data-emoji="${escapeAttr(q)}">${escapeHtml(q)}</button></div>`;
        }
        // Re-attach click handlers
        grid.querySelectorAll(".emoji-picker-btn").forEach(btn => {
            btn.addEventListener("click", () => { selectEmoji(btn.dataset.emoji); });
        });
    });

    // Enter to select typed emoji
    searchInput.addEventListener("keydown", (e) => {
        if (e.key === "Enter") {
            e.preventDefault();
            const v = searchInput.value.trim();
            if (v) selectEmoji(v);
        } else if (e.key === "Escape") {
            closePicker();
        }
    });

    function selectEmoji(emoji) {
        closePicker();
        onSelect(emoji);
    }

    function closePicker() {
        picker.remove();
        document.removeEventListener("click", outsideClick, true);
    }

    // Clear button
    picker.querySelector(".emoji-picker-clear").addEventListener("click", () => {
        closePicker();
        onSelect("");
    });

    // Click on emoji buttons
    picker.querySelectorAll(".emoji-picker-btn").forEach(btn => {
        btn.addEventListener("click", () => { selectEmoji(btn.dataset.emoji); });
    });

    // Close on outside click (delayed to avoid immediate close)
    function outsideClick(e) {
        if (!picker.contains(e.target)) closePicker();
    }
    setTimeout(() => document.addEventListener("click", outsideClick, true), 100);
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
