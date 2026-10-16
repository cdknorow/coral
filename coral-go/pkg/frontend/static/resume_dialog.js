/* Moved out of modals.js as it was (#3836): the Resume dialog loads on its own the first time it opens. */
import { state } from './state.js';
import { showToast } from './utils.js';
import { keyLabel } from './server_base.js';
import { recordRecentDir } from './browser.js';
import { loadLiveSessions } from './api.js';

// ── Resume Modal ───────────────────────────────────────────────────────────

export async function showResumeModal() {
    if (!state.currentSession || state.currentSession.type !== "history") {
        showToast("No history session selected", true);
        return;
    }

    const sessionId = state.currentSession.name;
    const historyEntry = state.historySessionsList.find(s => s.session_id === sessionId);
    // The list entry, else the loaded transcript (a /chats/<id> link that is not
    // in the list); /resume-info below overrides both. Never guess a type.
    let agentType = historyEntry?.source_type || state.currentSession.agent_type || "";

    const dirInput = document.getElementById("resume-dir");
    const boardRow = document.getElementById("resume-board-row");
    const boardNameEl = document.getElementById("resume-board-name");

    // Fetch resume metadata (working dir, board, display name) from the server
    let resumeDir = "";
    let boardName = "";
    let displayName = "";
    let resumeSessionId = sessionId;
    try {
        const resp = await fetch(`/api/sessions/history/${encodeURIComponent(sessionId)}/resume-info`);
        if (resp.ok) {
            const info = await resp.json();
            if (info.working_dir) resumeDir = info.working_dir;
            if (info.board_name) boardName = info.board_name;
            if (info.agent_type) agentType = info.agent_type;
            if (info.resume_session_id) resumeSessionId = info.resume_session_id;
            if (info.display_name) displayName = info.display_name;
        }
    } catch (_) { /* best-effort */ }
    if (!agentType) {
        showToast("Can't resume: this session's agent type is unknown", true);
        return;
    }

    // Pre-fill working directory: saved working dir > settings default > coral root
    const defaultDir = resumeDir || state.settings?.default_working_dir || dirInput.dataset.coralRoot || "";
    dirInput.value = defaultDir;

    // Show board reconnect option if the session was on a board
    if (boardName) {
        boardNameEl.textContent = keyLabel(boardName); // its label; the key rejoins it (#3536)
        boardNameEl.dataset.boardKey = boardName;
        boardRow.style.display = "";
        document.getElementById("resume-board-check").checked = true;
    } else {
        boardRow.style.display = "none";
        delete boardNameEl.dataset.boardKey; // never rejoin the previous chat's board
    }

    // Store agent type and display name for the launch function
    const modal = document.getElementById("resume-modal");
    modal.dataset.agentType = agentType;
    modal.dataset.displayName = displayName || historyEntry?.display_name || "";
    modal.dataset.resumeSessionId = resumeSessionId;
    modal.style.display = "flex";
}

export function hideResumeModal() {
    document.getElementById("resume-modal").style.display = "none";
    // Hide browser if open
    const browser = document.getElementById("resume-dir-browser");
    if (browser) browser.style.display = "none";
}

export async function resumeLaunchNew() {
    if (!state.currentSession || state.currentSession.type !== "history") {
        showToast("No history session selected", true);
        return;
    }

    const sessionId = state.currentSession.name;
    const dir = document.getElementById("resume-dir").value.trim();
    if (!dir) {
        showToast("Working directory is required", true);
        return;
    }

    const modal = document.getElementById("resume-modal");
    const agentType = modal.dataset.agentType;
    if (!agentType) {
        showToast("Can't resume: this session's agent type is unknown", true);
        return;
    }
    const resumeSessionId = modal.dataset.resumeSessionId || sessionId;
    const displayName = modal.dataset.displayName || "";
    const boardCheck = document.getElementById("resume-board-check");
    const boardNameEl = document.getElementById("resume-board-name");
    const boardName = (boardCheck && boardCheck.checked && boardNameEl) ? (boardNameEl.dataset.boardKey || "") : "";

    const btn = document.getElementById("btn-resume-launch");
    if (btn) { btn.disabled = true; btn.textContent = "Resuming..."; }

    try {
        const payload = {
            working_dir: dir,
            agent_type: agentType,
            resume_session_id: resumeSessionId,
        };
        if (displayName) payload.display_name = displayName;
        if (boardName) payload.board_id = payload.board_name = boardName; // rejoins that board (#3536)

        const resp = await fetch("/api/sessions/launch", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
        });
        const result = await resp.json();
        if (result.error) {
            showToast(result.error, true);
        } else {
            recordRecentDir(dir);
            showToast(`Resumed session as new agent`);
            hideResumeModal();
            setTimeout(loadLiveSessions, 2000);
        }
    } catch (e) {
        showToast("Failed to resume session", true);
        console.error("resumeLaunchNew error:", e);
    } finally {
        if (btn) { btn.disabled = false; btn.textContent = "Resume"; }
    }
}
