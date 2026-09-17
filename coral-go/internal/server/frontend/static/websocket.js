/* WebSocket connection for real-time coral updates */

import { state } from './state.js';
import { renderLiveSessions, updateSessionStatus, updateSessionSummary, updateSessionBranch, updateWaitingIndicator, resolveSessionIdentity } from './render.js';
import { renderLiveJobs } from './live_jobs.js';
import { updateChangedFileCount } from './changed_files.js';
import { updateSectionVisibility } from './sidebar.js';
import { showNotificationToast, showWorkflowNotification, showAlertNotification, showToast, escapeHtml, dbg } from './utils.js';
import { isPopout, popoutAllowsToastFor, popoutUpdateFromSession, popoutHandleSessionsTick, formatTerminalLabel } from './popout.js';

export function connectCoralWs() {
    dbg('connectCoralWs: establishing connection');
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const url = `${proto}//${location.host}/ws/coral`;

    state.coralWs = new WebSocket(url);

    state.coralWs.onmessage = (event) => {
        handleCoralMessage(JSON.parse(event.data));
    };

    state.coralWs.onclose = (ev) => {
        dbg('coralWs CLOSE', { code: ev.code, reason: ev.reason });
        setTimeout(connectCoralWs, 5000);
    };

    state.coralWs.onerror = (ev) => {
        dbg('coralWs ERROR', ev);
        // Will trigger onclose
    };
}

/**
 * Generic merge of a session payload onto the previous snapshot.
 * Fields the server omits keep their old value; anything present in the
 * payload (including explicit null / "" / 0) overwrites. Returns a new
 * object so the previous snapshot is never aliased.
 */
function mergeSession(prev, next) {
    return { ...(prev || {}), ...next };
}

/** Apply one message from /ws/coral. Exported for tests (window._coralHandleWsMessage). */
export function handleCoralMessage(data) {
    {
        // Handle diff updates: merge changed/removed into existing session list
        if (data.type === "coral_diff") {
            let sessions = [...(state.liveSessions || [])];

            // Apply changed sessions (update existing or add new)
            if (data.changed) {
                for (const changed of data.changed) {
                    const key = changed.session_id || changed.name;
                    const idx = sessions.findIndex(s => (s.session_id || s.name) === key);
                    if (idx >= 0) {
                        // Omitted fields keep their previous value; explicit
                        // values (including null) overwrite.
                        sessions[idx] = mergeSession(sessions[idx], changed);
                    } else {
                        // New session: taken as-is.
                        sessions.push(changed);
                    }
                }
            }

            // Remove sessions that no longer exist
            if (data.removed) {
                const removedSet = new Set(data.removed);
                sessions = sessions.filter(s => !removedSet.has(s.session_id || s.name));
            }

            // Treat merged list as a full update for the rest of the handler
            data.type = "coral_update";
            data.sessions = sessions;
        }

        if (data.type === "coral_update") {
            // Detect sessions that just transitioned to "needs input"
            for (const s of data.sessions) {
                const id = s.session_id || s.name;
                const wasWaiting = state.prevWaitingState[id];
                const notifyEnabled = state.settings.notify_needs_input !== false;
                if (notifyEnabled && s.waiting_for_input && !wasWaiting && popoutAllowsToastFor(s.session_id)) {
                    const label = escapeHtml(s.display_name || s.name);
                    const detail = s.waiting_summary ? escapeHtml(s.waiting_summary) : null;
                    const sessionName = s.name;
                    const agentType = s.agent_type;
                    const sessionId = s.session_id;
                    showNotificationToast(label, detail, () => {
                        import('./sessions.js').then(m => m.selectLiveSession(sessionName, agentType, sessionId));
                    });
                }
                state.prevWaitingState[id] = !!s.waiting_for_input;

                // Detect goal (summary) changes — disabled for now, needs design polish
                // const prevSummary = state.prevSummaryState && state.prevSummaryState[id];
                // if (s.summary && s.summary !== prevSummary) {
                //     const goalLabel = s.display_name || s.board_job_title || s.name;
                //     showToast(`${goalLabel}: ${s.summary}`);
                // }
                // if (!state.prevSummaryState) state.prevSummaryState = {};
                // state.prevSummaryState[id] = s.summary || null;
            }

            // Fields a full update omits (commands, branch, repo_name, ...)
            // keep their previous value via the same generic merge.
            if (state.liveSessions && state.liveSessions.length) {
                const prevMap = {};
                for (const s of state.liveSessions) {
                    const key = s.session_id || s.name;
                    prevMap[key] = s;
                }
                data.sessions = data.sessions.map(s => mergeSession(prevMap[s.session_id || s.name], s));
            }
            state.liveSessions = data.sessions;
            renderLiveSessions(data.sessions);
            if (isPopout()) popoutHandleSessionsTick(data.sessions);

            // Show notifications pushed via POST /api/notifications
            if (data.notifications) {
                for (const n of data.notifications) {
                    if (n.type === 'alert') {
                        showAlertNotification(n.title, n.message, n.level, n.link || null);
                    } else {
                        showWorkflowNotification(n.title, n.message, n.level);
                    }
                }
            }

            // Update Jobs sidebar
            if (data.active_runs) {
                renderLiveJobs(data.active_runs);
            }

            // Update status/summary/branch if we're viewing a live session
            if (state.currentSession && state.currentSession.type === "live") {
                const sid = state.currentSession.session_id;
                // Try matching by session_id first, then fall back to name
                let s = sid
                    ? data.sessions.find(s => s.session_id === sid)
                    : null;
                if (!s && !isPopout()) {
                    // Dashboard only: a restarted agent may briefly be matchable by name.
                    // Popout mode is exact-id only and never retargets.
                    s = data.sessions.find(s => s.name === state.currentSession.name);
                }
                if (s) {
                    // Keep state in sync with backend (handles restarts
                    // where session_id or name may change).
                    // Only update session_id if we matched by session_id (not
                    // by name fallback), to avoid switching to the wrong
                    // session when multiple sessions share the same directory.
                    const matchedById = sid && s.session_id === sid;
                    if (matchedById && s.name !== state.currentSession.name) {
                        state.currentSession.name = s.name;
                    }
                    if (!matchedById && !isPopout() && s.session_id && s.session_id !== state.currentSession.session_id) {
                        // Matched by name only — adopt new session_id
                        // (only safe when there's a single session with this name)
                        const sameNameCount = data.sessions.filter(x => x.name === state.currentSession.name).length;
                        if (sameNameCount === 1) {
                            state.currentSession.session_id = s.session_id;
                        }
                    }
                    // Keep all identity inputs in sync so the header and command
                    // placeholder resolve the same name as the sidebar. `s` is
                    // already merged onto the previous snapshot, so omitted
                    // fields are preserved and explicit values win.
                    state.currentSession.display_name = s.display_name || null;
                    state.currentSession.auto_name = s.auto_name || null;
                    state.currentSession.summary = s.summary || null;
                    state.currentSession.first_prompt = s.first_prompt || '';
                    const headerName = resolveSessionIdentity(s);
                    const nameEl = document.getElementById("session-name");
                    if (nameEl) nameEl.textContent = headerName;
                    const termLabel = document.getElementById("terminal-header-label");
                    if (termLabel) termLabel.textContent = formatTerminalLabel(headerName, s.session_id);
                    const cmdInput = document.getElementById("command-input");
                    if (cmdInput) {
                        const typeInfo = s.agent_type ? ` (${s.agent_type})` : '';
                        const boardInfo = s.board_project ? ` on ${s.board_project}` : '';
                        cmdInput.placeholder = `Sending to: ${headerName}${typeInfo}${boardInfo} — type a command or paste an image...`;
                    }
                    updateSessionStatus(s.status);
                    updateSessionSummary(s.summary);
                    updateSessionBranch(s.branch);
                    updateWaitingIndicator(s);
                    updateChangedFileCount(s.changed_file_count || 0);
                    // Update terminal header status dot
                    const termDot = document.getElementById('terminal-status-dot');
                    if (termDot) termDot.className = `terminal-status-dot ${s.working ? 'working' : s.waiting_for_input ? 'waiting' : s.sleeping ? 'sleeping' : s.done ? 'done' : 'stale'}`;
                    popoutUpdateFromSession(s);
                }
            }
        }
    }
}
