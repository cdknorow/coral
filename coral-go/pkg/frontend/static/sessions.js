import { showTerminalStatusbar } from './terminal_statusbar.js';
import { setCommandPlaceholder } from './composer_placeholder.js';
import { updateModelChip } from './model_chip.js';
import { importOnce } from './import_once.js';
import { refreshAgentUI } from './agent_ui.js';
/* Session selection and management */

import { state, sessionKey, saveSessionDraft } from './state.js';
import { showToast, escapeHtml, escapeAttr, dbg, showView } from './utils.js';
import { loadHistoryMessages } from './api.js';
import { HISTORY_PAGE, setHistoryWindow } from './history_pages.js';
import { normServer, serverForSession, serverFetch, sessionServer, agentPath, identityKey, keyLabel, isCloud } from './server_base.js';
import { updateHeaderContext } from './header_context.js';
import { stopCaptureRefresh, startCaptureRefresh } from './capture.js';
import { updateSessionStatus, updateSessionSummary, updateSessionBranch, updateSessionFolder, updateWaitingIndicator, updateTokenUsage, updateHistoryTokenUsage, renderHistoryChat, showBoardChatTab, hideBoardChatTab, resolveSessionIdentity, commandPlaceholder, applyTerminalDot } from './render.js';
import { renderQuickActions, updateSidebarActive, renderAttachments } from './controls.js';
import { loadSessionNotes, switchHistoryTab } from './notes.js';
import { loadSessionTags } from './tags.js';
import { loadSessionCommits } from './commits.js';
import { loadBoardTasks, loadSubagents, renderTaskList } from './tasks.js';
import { loadChangedFiles, refreshChangedFiles, syncFilesViewerSession } from './changed_files.js';
import { loadAgentNotes } from './agent_notes.js';
import { renderEventTimeline, switchAgenticTab } from './agentic_state.js';
import { loadHistoryEvents, loadHistoryTasks, loadHistoryAgentNotes } from './history_tabs.js';
import { loadHistoryChanges } from './history_changes.js';
import { note, currentRoute } from './nav.js';
import { liveRoute, normalize } from './routes.js';
import { stopLiveHistoryPoll, resetLiveHistory, applyLiveViewMode, effectiveLiveViewMode, clearViewOverride } from './live_chat.js';
import { agentViewVisible, onAgentView } from './agent_view.js';
import { syncPaneWidth, resetSyncedCols } from './capture.js';
import { disposeTerminal, createTerminal, connectTerminalWs, disconnectTerminalWs, fitTerminal } from './xterm_renderer.js';
import { syncSessionOverlay } from './session_overlay.js';
import { loadXterm, xtermReady, showVendorLoading, showVendorLoadError } from './vendor_loader.js';
import { agentSwitchStarted, agentViewReady } from './ready.js';
import { getRendererMode } from './renderers.js';
import { invalidateFileCache } from './file_mention.js';
import { isPopout, popoutResolved, popoutUpdateFromSession, formatTerminalLabel } from './popout.js';
import { joinSessionOwnership, leaveSessionOwnership } from './ownership.js';
import { applySleepingView } from './sleeping_view.js';
import { refreshHubAccess, syncUnreachableBanner } from './hub_access.js';
import { syncAutoAcceptHeader } from './auto_accept.js';
import { clearComposerError, syncTerminalGoneHeader } from './terminal_gone.js';
import { browserLocal } from './safe_storage.js';

// Bumped on every switch, so deferred work started for one agent can tell it
// is no longer the selected one.
let switchGeneration = 0;

// The selected agent's terminal (#3719). It is only attached while the Terminal view is the one showing and the
// agent's page is on screen: in Chat view (the default) there is no xterm.js download, no WebSocket and no tmux
// attach, and Back to the phone's list or any other view closes the socket. _attachTerminal is set by
// selectLiveSession for an agent that has a terminal; _terminalStarted says its attach has begun and the socket
// was not closed since.
let _attachTerminal = null;
let _terminalStarted = false;
const closeTerminalSocket = () => { _terminalStarted = false; disconnectTerminalWs(); };
// A popout is a window for one agent's terminal: it always attaches. The dashboard attaches in Terminal view only.
const terminalWanted = () => isPopout() || effectiveLiveViewMode(state.currentSession?.sleeping ? 'chat' : undefined) === 'terminal';
const attachTerminalIfWanted = () => {
    if (_attachTerminal && !_terminalStarted && agentViewVisible() && terminalWanted()) _attachTerminal();
};
onAgentView('coral:terminal-wanted', attachTerminalIfWanted);
// The view is shown just before an agent tap's selectLiveSession runs: wait a tick, so the terminal that attaches
// is the one for the agent being selected, not the one left behind.
onAgentView('coral:agent-view-shown', () => setTimeout(attachTerminalIfWanted, 0));
onAgentView('coral:terminal-unwanted', () => {
    if (_terminalStarted) closeTerminalSocket();
    if (!isPopout()) syncSessionOverlay(); // Chat view: no "Session ended" over the transcript; a restart in progress still shows
});
onAgentView('coral:agent-view-hidden', () => { if (_terminalStarted) closeTerminalSocket(); });

// The agent being left: its refreshers, terminal socket and file cache stop, and what was typed in its box is kept. Returns the box.
function _leaveCurrentAgent() {
    stopCaptureRefresh();
    stopLiveHistoryPoll();
    _attachTerminal = null;
    closeTerminalSocket();
    invalidateFileCache();

    // Save current input text for the old session
    const input = document.getElementById("command-input");
    const oldKey = sessionKey(state.currentSession);
    if (oldKey) {
        saveSessionDraft(oldKey, input.value);
    }
    return input;
}

// The session being chosen becomes the current one: found in the live list, with what the list knows of it. Returns that record and its display name.
function _enterAgent(name, agentType, sessionId, srv) {
    // Look up display_name and working_directory from live sessions data
    const agentData = state.liveSessions.find(s => s.session_id === sessionId && sessionServer(s) === srv);
    const displayName = agentData ? agentData.display_name : null;
    const workingDirectory = agentData ? agentData.working_directory : "";

    state.currentSession = {
        type: "live", name, server: srv, agent_type: agentType || null, session_id: sessionId || null,
        display_name: displayName || null, working_directory: workingDirectory || "",
        prompt: agentData?.prompt || "", model: agentData?.model || "",
        thinking: agentData?.thinking || "", // the level it is set to: the Restart form shows it and keeps it (#5070)
        thinking_actual: agentData?.thinking_actual || "", // what its transcript shows it running at: the Restart form says so when that is not the level above (#5105)
        capabilities: agentData?.capabilities || null,
        board_project: agentData?.board_project || null,
        board_job_title: agentData?.board_job_title || null,
        // The terminal socket's target: the tmux session, which is not the agent's list name for
        // every agent (a terminal agent is listed under its folder's name). Reconnects use it (#3469).
        tmux_session: agentData?.tmux_session || null,
    };
    return { agentData, displayName };
}

// The agent's header, composer placeholder and ownership group, by the shared identity chain (display name, auto name, board title, "Agent"/"Terminal").
function _drawAgentHeader(name, agentType, sessionId, srv, agentData, displayName) {
    // Update header via the shared identity chain (display_name -> auto_name -> board_job_title -> "Agent"/"Terminal")
    // Popout: the resolver record is authoritative at selection time (the live
    // list may only enrich later ticks), so identity comes from it first.
    const identitySource = (isPopout() && popoutResolved())
        ? Object.assign({}, agentData || {}, popoutResolved())
        : (agentData || { display_name: displayName, agent_type: agentType });
    const identity = resolveSessionIdentity(identitySource);
    const nameEl = document.getElementById("session-name");
    if (nameEl) nameEl.textContent = identity;
    const termLabel = document.getElementById("terminal-header-label");
    if (termLabel) termLabel.textContent = formatTerminalLabel(identity, sessionId);
    const termDot = document.getElementById("terminal-status-dot");
    if (termDot && agentData) {
        applyTerminalDot(termDot, agentData);
    }
    const badge = document.getElementById("session-type-badge");
    if (badge) {
        badge.textContent = agentType || "claude";
        badge.className = `badge ${(agentType || "claude").toLowerCase()}`;
    }
    // Dashboard: "Open Agent Tab" link for this exact session. Popout mode
    // hides this control and never carries the id in it.
    const openWin = document.getElementById("terminal-open-window-link");
    if (openWin && !isPopout()) {
        if (sessionId) { openWin.href = agentPath(srv, sessionId); openWin.hidden = false; }
        else { openWin.removeAttribute("href"); openWin.hidden = true; }
    }
    // Multi-window ownership group for this session (dashboard and popout alike)
    joinSessionOwnership(sessionId || null);
    popoutUpdateFromSession(isPopout() && popoutResolved()
        ? Object.assign({}, agentData || {}, popoutResolved())
        : (agentData || { agent_type: agentType, session_id: sessionId }));

    // Update command input placeholder with session target
    const cmdInput = document.getElementById("command-input");
    if (cmdInput) {
        setCommandPlaceholder(cmdInput, commandPlaceholder(identity, agentType)); // view only and asleep keep their words (#5856)
    }
    // Keep the sleeping placeholder over the awake one just set.
    if (state.currentSession.sleeping && !isPopout()) applySleepingView(true);

    // One overlay serves every agent: the new one's own state (restarting or not) replaces the previous agent's (#4481).
    if (!isPopout()) syncSessionOverlay();
    // Reset summary/status before loading new session detail
    const summaryEl = document.getElementById("session-summary");
    if (summaryEl) summaryEl.style.display = "none";
}

// The Board chat tab shows for an agent on a board (and the sidebar opens it unless another tab is saved), and hides for one that is not.
function _syncBoardTab(agentData, srv) {
    // Show/hide Board chat tab based on whether agent is on a board
    const boardTab = document.getElementById('agentic-tab-board');
    if (agentData && agentData.board_project) {
        if (boardTab) boardTab.style.display = '';
        showBoardChatTab(identityKey(srv, agentData.board_project));
        // Auto-switch to Board tab if no persisted preference
        const savedTab = browserLocal.get('coral-agentic-tab-top');
        if (!savedTab || savedTab === 'board') {
            switchAgenticTab('board', 'top');
        }
    } else {
        if (boardTab) boardTab.style.display = 'none';
        hideBoardChatTab();
        // If persisted tab was 'board' but agent has no board, fall back to files
        const savedTab = browserLocal.get('coral-agentic-tab-top');
        if (savedTab === 'board') {
            switchAgenticTab('files', 'top');
        }
    }
}

// The center pane's source for the agent: the xterm terminal and its socket, or the capture pane. Returns what settles when the terminal is attached (at once when there is none).
function _startTerminalOrCapture(name, agentType, sessionId, srv, agentData, generation) {
    const mode = getRendererMode(agentType, sessionId);
    dbg('renderer mode:', mode, 'xterm loaded:', xtermReady());
    // Always start capture refresh — it polls tasks and events for any mode
    startCaptureRefresh();
    // Popout: routing comes from the resolver only; a sleeping target has no
    // attachable pane (tmux_session null) so the terminal is not connected.
    const popoutTarget = isPopout() ? popoutResolved() : null;
    const canAttach = !isPopout() || !!(popoutTarget && popoutTarget.tmux_session);
    // Settles when the terminal is attached (or when there is no terminal to attach).
    let terminalAttached = Promise.resolve();
    if (mode === "xterm" && canAttach) {
        dbg('switching to xterm mode, creating terminal + WS');
        document.getElementById("pane-capture").style.display = "none";
        const container = document.getElementById("xterm-container");
        container.style.display = "flex";
        showTerminalStatusbar(true);
        container.querySelector(':scope > .vendor-load-notice')?.remove(); // a previous agent's failed load
        const tmuxName = popoutTarget ? popoutTarget.tmux_session : (agentData ? (agentData.tmux_session || name) : name);
        if (generation === switchGeneration && state.currentSession) state.currentSession.tmux_session = tmuxName;
        const attach = () => {
            // Don't clear innerHTML — createTerminal reuses the existing xterm
            // instance to avoid canvas recreation issues in WebKit webview
            createTerminal(container);
            dbg('terminal WS using tmux_session:', tmuxName, '(agent name:', name, ')');
            connectTerminalWs(tmuxName, agentType, sessionId, srv);
            // Fit terminal after session switch — the container may already be
            // visible at the right size so ResizeObserver won't fire.
            // Double-fit: first at 50ms for quick response, second at 250ms
            // after layout has fully settled (WebKit webview can be slow).
            setTimeout(fitTerminal, 50);
            setTimeout(fitTerminal, 250);
        };
        // xterm.js loads on the first terminal view (#3083). A failed load shows
        // Retry in the pane; a switch meanwhile drops the result.
        const failed = (err) => {
            console.error('[coral]', err);
            if (generation === switchGeneration) showVendorLoadError(container, 'The terminal', () => { if (generation === switchGeneration) { _terminalStarted = true; loadThenAttach(); } });
        };
        let attached = () => {};
        const tryAttach = () => {
            if (generation !== switchGeneration) return;
            // The terminal stopped being wanted while xterm.js loaded (Chat view, Back to the list closed the socket and
            // cleared _terminalStarted): no socket is opened for a view that is not showing (#4518). A later switch to
            // Terminal attaches then. A view that still wants it (a popout always does) attaches whatever cleared the flag.
            if (!_terminalStarted && !(agentViewVisible() && terminalWanted())) { attached(); return; }
            try { attach(); attached(); } catch (err) { _terminalStarted = false; failed(err); }
        };
        const loadThenAttach = () => {
            if (xtermReady()) { tryAttach(); return; }
            const notice = showVendorLoading(container, 'the terminal');
            loadXterm().then(() => { notice.remove(); tryAttach(); }, (err) => { notice.remove(); _terminalStarted = false; failed(err); });
        };
        _attachTerminal = () => { if (generation !== switchGeneration) return; _terminalStarted = true; loadThenAttach(); };
        // Chat view shows no terminal: nothing is loaded or opened until the Terminal view is shown (applyLiveViewMode).
        if (terminalWanted()) {
            terminalAttached = new Promise(r => { attached = r; });
            _attachTerminal();
        }
    } else {
        dbg('switching to capture mode, disposing terminal');
        document.getElementById("xterm-container").style.display = "none";
        showTerminalStatusbar(false);
        document.getElementById("pane-capture").style.display = "";
        disposeTerminal();
        resetSyncedCols();
    }
    return terminalAttached;
}

// What loads in the background once an agent is chosen: its subagents, its board's tasks, its notes, and (after the transcript) its changed files.
function _loadAgentSecondary(name, sessionId, agentData, srv, generation, transcriptReady) {
    // Load secondary data in background (non-blocking, after terminal is connected)
    // The agent's tasks and events come with the first capture poll (startCaptureRefresh, above): GET /poll carries both,
    // so they are not fetched again here (#3720).
    loadSubagents(name, sessionId);
    const boardProject = agentData && agentData.board_project;
    loadBoardTasks(boardProject ? identityKey(srv, boardProject) : null);
    loadAgentNotes(name, sessionId);
    // The git queries wait for the transcript: on a large repo they take
    // seconds, and the browser's per-origin connection budget is small enough
    // that they would hold up the chat request behind them (task: chat stuck
    // behind the git diff poller). A failed or absent transcript never blocks
    // them: they run on the next tick either way.
    const loadFiles = () => {
        if (generation !== switchGeneration) return; // switched agents meanwhile
        if (state.settings.refresh_files_on_switch) {
            refreshChangedFiles();
        } else {
            loadChangedFiles(name, sessionId);
        }
        // The repository's file list (search-files) is no longer fetched on every switch
        // (#3721: the slowest request of a switch): the @-mention, the file search and the Files
        // tab fetch it when used, and the server caches it.
    };
    Promise.resolve(transcriptReady).catch(() => {}).then(loadFiles);
}

// Every way of opening an agent (a toast, a desktop notification, a board link, a Workflows step, a Needs you row, a route) comes
// through selectLiveSession, so this is the one place that puts the Agents tab up first: the nav, the agent list in the sidebar and
// the agent then agree. From Inbox, Chats, Servers, Workflows or Jobs the agent used to draw over the tab that was still active,
// with the agent list hidden (#4953). The page's own address is written by selectLiveSession, so the tab switch writes none.
function showAgentsTab() {
    if (isPopout()) return;
    const active = document.querySelector('.top-nav-tab.active');
    if (active && active.id !== 'nav-tab-agents') window.switchNavTab?.('agents', { fromHistory: true });
}

export async function selectLiveSession(name, agentType, sessionId, server) {
    // Identity is (server, name). Callers that only know the session id (hash
    // restore, toasts) omit `server` and it is resolved from the live list.
    const srv = normServer(server || serverForSession(name, sessionId));
    dbg('selectLiveSession', { name, agentType, sessionId, server: srv });
    // Selecting an agent leaves the team view (declining to discard unsaved team settings cancels).
    if (state.selectedTeam && window.exitTeamContext && await window.exitTeamContext({ goBack: false }) === false) return;
    showAgentsTab();
    const generation = ++switchGeneration;
    agentSwitchStarted(name, sessionId || null); // window.__coralReady.agentSelected (#3118)
    const input = _leaveCurrentAgent();

    const { agentData, displayName } = _enterAgent(name, agentType, sessionId, srv);

    syncFilesViewerSession();
    refreshAgentUI();
    // A sleeping agent keeps its transcript readable; the composer offers Wake
    // instead of sending. The popout corrects this from its resolver below.
    applySleepingView(!!(agentData && agentData.sleeping), { manageInput: !isPopout(), archived: !!(agentData && agentData.archived) });
    // Cloud Hub: show the read-only chip at once for a View-only server (#3068).
    syncUnreachableBanner(); // an agent on an unreachable server shows why (#3151)
    syncAutoAcceptHeader(); // AUTO chip in the header while auto-accept is on (#3182)
    clearComposerError(); // the last agent's send error is not this one's (#3218)
    syncTerminalGoneHeader(); // "Terminal gone · Restart" for an agent whose terminal is gone
    refreshHubAccess(state.currentSession);

    // Restore input text for the new session
    const newKey = sessionKey(state.currentSession);
    input.value = state.sessionInputText[newKey] || "";
    renderAttachments();
    input.focus();

    // Show live view, hide others
    showView("live-session-view");

    // The agent's page (#3532): a new history entry, or the same one when it is already this
    // agent's (a link, Back, a re-select), keeping its sub-tab. The popout's URL is its identity.
    if (!isPopout()) {
        const route = liveRoute(srv, sessionId);
        const cur = currentRoute();
        if (cur && cur.view === 'agents' && cur.id === String(sessionId || '').toLowerCase() && cur.server === route.server) route.sub = cur.sub;
        note(route);
    }

    // Show loading skeleton
    const captureWrapper = document.getElementById("capture-wrapper");
    captureWrapper.classList.add("loading-skeleton");

    _drawAgentHeader(name, agentType, sessionId, srv, agentData, displayName);

    // Use cached data from WS for immediate display (no blocking fetch)
    const agent = state.liveSessions.find(s => s.session_id === sessionId && sessionServer(s) === srv);
    if (agent) {
        updateSessionStatus(agent.status);
        updateSessionSummary(agent.summary);
    }
    updateHeaderContext(agent || agentData || null);
    captureWrapper.classList.remove("loading-skeleton");
    updateSessionBranch(agent?.branch || null, agent?.repo_name || null, agent?.git_state || null);
    updateSessionFolder(agent?.working_directory || "");
    updateModelChip(agent || null, srv);
    updateWaitingIndicator(agent || {});
    updateTokenUsage(sessionId);

    // Set up quick action buttons
    state.currentCommands = (agent && agent.commands) || [
        { name: "compact", command: "/compact", description: "Compress conversation history" },
        { name: "clear", command: "/clear", description: "Clear conversation and start fresh" },
    ];
    renderQuickActions();

    // Highlight in sidebar
    updateSidebarActive();

    _syncBoardTab(agentData, srv);

    // Reset live history and start capture/terminal FIRST (fastest path to visible output)
    resetLiveHistory();
    clearViewOverride(); // an "Open terminal" was for the agent before (#4711)
    // Clear stale task state from previous session before starting refresh
    state.currentAgentTasks = [];
    state.currentBoardTasks = [];
    renderTaskList();
    state.currentAgentEvents = []; // the previous agent's activity goes now; the first poll fills the new one
    renderEventTimeline();
    const terminalAttached = _startTerminalOrCapture(name, agentType, sessionId, srv, agentData, generation);

    // Sync tmux pane width to match browser display after layout settles
    setTimeout(syncPaneWidth, 100);

    // Show terminal or chat in the center pane (starts the transcript poll in chat mode)
    // A sleeping agent has no terminal to show: its history is the view.
    const transcriptReady = applyLiveViewMode(state.currentSession.sleeping ? "chat" : undefined);
    const chatView = !!document.getElementById("capture-wrapper")?.classList.contains("chat-mode");
    (chatView ? Promise.resolve(transcriptReady).catch(() => {}) : terminalAttached).then(() => {
        if (generation === switchGeneration) agentViewReady(name, chatView ? 'chat' : 'terminal', sessionId || null);
    });

    _loadAgentSecondary(name, sessionId, agentData, srv, generation, transcriptReady);
}

import { historyServer, historyRoute } from './history_source.js';

export async function selectHistorySession(sessionId, locator = null) {
    const source = historyServer();
    const route = normalize(historyRoute(sessionId));
    if (!route || (isCloud() && !source)) return false;
    const generation = ++switchGeneration;
    stopCaptureRefresh();
    leaveSessionOwnership();
    disposeTerminal();

    // Save current input text for the old session
    const input = document.getElementById("command-input");
    const oldKey = sessionKey(state.currentSession);
    if (oldKey) {
        saveSessionDraft(oldKey, input.value);
    }

    state.currentSession = { type: "history", name: sessionId, server: source || 'local' };
    refreshAgentUI();

    // The chat's page (#3532).
    note(route);

    // Restore input text for the new session
    const newKey = sessionKey(state.currentSession);
    input.value = state.sessionInputText[newKey] || "";

    showView("history-session-view");

    document.getElementById("history-session-title").textContent = `Session: ${sessionId}`;
    document.getElementById("history-session-id").textContent = sessionId;
    const computer = (state.servers || []).find(s => s.id === source);
    const sourceLabel = document.getElementById('history-session-source');
    if (sourceLabel) {
        sourceLabel.hidden = !isCloud();
        sourceLabel.textContent = computer?.label || source || '';
    }

    // Update branch from history sessions data
    const historyEntry = state.historySessionsList.find(s => s.session_id === sessionId);
    const branchEl = document.getElementById("history-session-branch");
    if (historyEntry && historyEntry.branch) {
        const bt = branchEl.querySelector(".branch-text");
        bt.textContent = historyEntry.branch;
        branchEl.dataset.full = historyEntry.branch; // drawn by the chip's ::after on hover, focus or tap (branch_expand.js)
        bt.setAttribute("aria-label", `Branch ${historyEntry.branch}`);
        branchEl.style.display = "";
    } else {
        branchEl.style.display = "none";
    }

    // Show/hide Resume button based on source type
    const resumeBtn = document.getElementById("btn-resume-session");
    if (resumeBtn) {
        const resumable = historyEntry && (historyEntry.source_type === "claude" || historyEntry.source_type === "codex");
        resumeBtn.style.display = resumable ? "" : "none";
    }

    // Show board link if session was part of a message board
    const boardLink = document.getElementById("history-session-board-link");
    if (boardLink) {
        const boardName = historyEntry?.board_project || historyEntry?.board_name;
        if (boardName) {
            boardLink.textContent = keyLabel(boardName); // its label (#3536)
            boardLink.onclick = () => { if (window.selectBoardProject) window.selectBoardProject(identityKey(source, boardName)); };
            boardLink.style.display = "";
        } else {
            boardLink.style.display = "none";
        }
    }

    updateSidebarActive();

    // Open the transcript by default; summaries may not exist yet.
    switchHistoryTab('chat');

    // The newest page (#3718: a whole transcript can be too big for one reply through Coral Hub).
    let data = await loadHistoryMessages(sessionId, { limit: HISTORY_PAGE });
    let messageBaseIndex = 0;
    if (generation !== switchGeneration || source !== historyServer()) return;
    if (data && data.messages) {
        messageBaseIndex = data.start;
        // Search locators identify the absolute message index. Fetch a page
        // around that index so a hit in an older conversation is never opened
        // at the newest page or an ambiguous text match.
        if (locator && Number.isInteger(locator.message_index) && data.total > 0) {
            const limit = 80;
            const end = Math.min(data.total, locator.message_index + 40);
            const offset = Math.max(0, data.total - end);
            const focused = await loadHistoryMessages(sessionId, { limit, offset });
            if (generation !== switchGeneration) return;
            // The page's first message is at start (the reply says so), not at its offset from the end.
            if (focused?.messages?.length) { data = focused; messageBaseIndex = focused.start; }
        }
        // Resume reads the type from here when the history list has no entry.
        if (data.agent_type) state.currentSession.agent_type = data.agent_type;
        const historyType = data.agent_type || historyEntry?.source_type || "claude";
        renderHistoryChat(data.messages, historyType, messageBaseIndex, locator?.message_index);
        setHistoryWindow(sessionId, historyType, messageBaseIndex, messageBaseIndex + data.messages.length, data.total);
        // A session opened before the history list has it (a /chats/<id> link,
        // or one beyond the loaded page) still gets Resume from its own record.
        if (!historyEntry && (data.agent_type === "claude" || data.agent_type === "codex")) {
            const resumeBtn = document.getElementById("btn-resume-session");
            if (resumeBtn) resumeBtn.style.display = "";
        }
    }

    // Load notes, tags, commits, token usage, and history tabs in parallel
    loadSessionNotes(sessionId);
    loadSessionTags(sessionId);
    loadSessionCommits(sessionId);
    loadHistoryEvents(sessionId);
    loadHistoryTasks(sessionId);
    loadHistoryAgentNotes(sessionId);
    loadHistoryChanges(sessionId);
    updateHistoryTokenUsage(sessionId);
    return data !== null; // false: Coral has no such chat (404) or it could not be loaded
}

export function renameAgent(name, agentType, sessionId) {
    const server = serverForSession(name, sessionId);
    const current = state.liveSessions.find(s => s.session_id === sessionId && sessionServer(s) === server);
    const currentName = (current && current.display_name) || name;
    const currentColor = (current && current.name_color) || "";
    // "Auto" previews the default colour for this type (theme tokens).
    const autoColor = (current && current.agent_type === 'terminal') ? 'var(--terminal-name-color)' : 'var(--accent-secondary)';
    window.showPromptModal('Rename Agent', 'Display name', currentName, async (newName, newColor) => {
        const nameChanged = newName !== currentName;
        const colorChanged = newColor !== currentColor;
        if (!nameChanged && !colorChanged) return;
        const put = async (path, body) => {
            const resp = await serverFetch(server, `/api/sessions/live/${encodeURIComponent(name)}/${path}`, {
                method: "PUT",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ ...body, session_id: sessionId }),
            });
            const result = await resp.json();
            if (result.error) throw new Error(result.error);
        };
        try {
            if (nameChanged) await put("display-name", { display_name: newName });
            if (colorChanged) await put("name-color", { color: newColor });
            // Update in-memory state
            if (current) {
                current.display_name = newName;
                current.name_color = newColor || null;
            }
            // Update header if this is the current session
            if (state.currentSession && state.currentSession.session_id === sessionId) {
                state.currentSession.display_name = newName;
                document.getElementById("session-name").textContent = newName;
            }
            // Re-render sidebar
            const { renderLiveSessions } = await importOnce('./render.js');
            renderLiveSessions(state.liveSessions);
            showToast(nameChanged ? "Agent renamed" : "Agent color updated");
        } catch (e) {
            showToast(e.message || "Failed to rename agent", true);
        }
    }, { color: { value: currentColor, auto: autoColor } });
}

export async function setAgentIcon(name, agentType, sessionId) {
    const server = serverForSession(name, sessionId);
    showEmojiPicker(async (emoji) => {
        try {
            const resp = await serverFetch(server, `/api/sessions/live/${encodeURIComponent(name)}/icon`, {
                method: "PUT",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ icon: emoji, session_id: sessionId }),
            });
            const result = await resp.json();
            if (result.error) {
                showToast(result.error, true);
                return;
            }
            const current = state.liveSessions.find(s => s.session_id === sessionId && sessionServer(s) === server);
            if (current) current.icon = emoji;
            const { renderLiveSessions } = await importOnce('./render.js');
            renderLiveSessions(state.liveSessions);
            showToast(emoji ? "Icon set" : "Icon removed");
        } catch (e) {
            showToast("Failed to set icon", true);
        }
    });
}

// ── Emoji Picker ─────────────────────────────────────────────────────

const EMOJI_CATEGORIES = [
    { name: "People", emojis: ["👨\u200d💻", "👩\u200d💻", "🧑\u200d🔬", "🕵️", "👷", "🧑\u200d🎨", "🧑\u200d💼", "🤖", "🧙", "🦸", "🥷", "👨\u200d🏫", "👩\u200d⚕️", "🧑\u200d🚀", "🧑\u200d🍳"] },
    { name: "Animals", emojis: ["🐙", "🦊", "🐺", "🦅", "🐝", "🐬", "🦁", "🐻", "🐲", "🦄", "🐍", "🦉", "🐧", "🦈", "🐢"] },
    { name: "Objects", emojis: ["⚡", "🔥", "💎", "🚀", "⭐", "🎯", "🛡️", "⚙️", "🔧", "💡", "🎨", "📡", "🔬", "💻", "🗡️"] },
    { name: "Symbols", emojis: ["✨", "💫", "🌟", "❤️", "🏆", "👑", "🎪", "🎵", "🌈", "☀️", "🌙", "❄️", "🔮", "🎲", "♟️"] },
];

function showEmojiPicker(onSelect) {
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
        selectLiveSession(s.name, s.agent_type, s.session_id, sessionServer(s));
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
