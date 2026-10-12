/* WebSocket connection for real-time coral updates */

import { state } from './state.js';
import { signInUrl } from './cloud.js';
import { feedRetryDelay, isStalenessDrift, DRIFT_RENDER_MAX_AGE_MS } from './feed_pacing.js';
import { renderLiveSessions, updateSessionStatus, updateSessionSummary, updateSessionBranch, updateSessionFolder, updateWaitingIndicator, resolveSessionIdentity, commandPlaceholder, terminalDotClass } from './render.js';
import { renderLiveJobs } from './live_jobs.js';
import { updateChangedFileCount } from './changed_files.js';
import { updateSectionVisibility } from './sidebar.js';
import { showNotificationToast, showWorkflowNotification, showAlertNotification, showToast, escapeHtml, dbg } from './utils.js';
import { serverWsBase, identityKey, sessionServer } from './server_base.js';
import { isPopout, popoutAllowsToastFor, popoutUpdateFromSession, popoutHandleSessionsTick, formatTerminalLabel } from './popout.js';

// Reconnect pacing for the live feed. A fixed 5 s retry made every open dashboard reconnect in the same second after
// a hub restart or deploy, and kept retrying forever after the sign-in ended. Retries now back off exponentially
// with jitter (feed_pacing.js). A "session ended" close (4401, sent by the hub) is checked against /auth/session
// first and stops the retries only when that says the browser is signed out (see recheckSignIn).
const FEED_CLOSE_SESSION_ENDED = 4401;
let _feedFailures = 0;
let _lastSidebarRenderAt = 0;

function scheduleFeedReconnect() {
    _feedFailures++;
    setTimeout(connectCoralWs, feedRetryDelay(_feedFailures));
}

// What the feed does once the browser is confirmed signed out. A seam for tests; the default is the same redirect the
// fetch wrapper in cloud.js performs on a 401.
export const feedHooks = {
    signedOut() {
        if (!/^\/(signin|auth|enroll)/.test(location.pathname)) location.replace(signInUrl());
    },
};

// 4401 says the hub found the session over, but the browser asks before giving up. GET /auth/session answers 200
// with {authenticated:false} for a browser that is really signed out (it never answers 401): only that stops the
// retries and sends the browser to sign in. {authenticated:true}, any other status (the hub answers 503 when it
// cannot check), an unreadable body or a network error mean the close was a blip: reconnect with the backoff.
async function recheckSignIn() {
    try {
        const r = await fetch('/auth/session', { cache: 'no-store', credentials: 'same-origin' });
        if (r.status === 200) {
            const body = await r.json();
            if (body && body.authenticated === false) { feedHooks.signedOut(); return; }
        }
    } catch (e) { /* network trouble or a bad body is not a sign-out */ }
    scheduleFeedReconnect();
}

export function connectCoralWs() {
    dbg('connectCoralWs: establishing connection');
    // The hub's own feed carries every server's sessions, tagged with `server`
    // (remote feeds are bridged by the hub), so one socket is enough.
    const url = `${serverWsBase('local')}/ws/coral`;

    state.coralWs = new WebSocket(url);

    state.coralWs.onmessage = (event) => {
        _feedFailures = 0;   // a socket that delivers is healthy; the next drop starts the backoff over
        handleCoralMessage(JSON.parse(event.data));
    };

    state.coralWs.onclose = (ev) => {
        dbg('coralWs CLOSE', { code: ev.code, reason: ev.reason });
        if (ev.code === FEED_CLOSE_SESSION_ENDED) { recheckSignIn(); return; }
        scheduleFeedReconnect();
    };

    state.coralWs.onerror = (ev) => {
        dbg('coralWs ERROR', ev);
        // Will trigger onclose
    };
}

/** Identity key of a feed row: (server, session_id || name). */
function _feedKey(s) {
    return identityKey(s.server, s.session_id || s.name);
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

let _waitingSeeded = false; // first coral_update only records who is already waiting

/** True when the operator is focused in this session's command input or terminal. */
function _operatorIsTypingIn(s) {
    const cur = state.currentSession;
    if (!cur || cur.type !== 'live' || !s.session_id || cur.session_id !== s.session_id) return false;
    const a = document.activeElement;
    return !!(a && (a.id === 'command-input' || (a.closest && a.closest('#xterm-container'))));
}

/** Test hook: forget the seed so a harness can simulate a fresh page load. */
export function _resetWaitingSeedForTests() { _waitingSeeded = false; state.prevWaitingState = {}; }

/** Apply one message from /ws/coral. Exported for tests (window._coralHandleWsMessage). */
export function handleCoralMessage(data) {
    {
        // Handle diff updates: merge changed/removed into existing session list
        if (data.type === "coral_diff") {
            let sessions = [...(state.liveSessions || [])];

            // A diff that only ages agents (staleness_seconds) changes nothing the sidebar shows beyond the "Ns ago"
            // text, so it may skip the full sidebar render, but never for longer than DRIFT_RENDER_MAX_AGE_MS.
            data._skipSidebarRender = !data.removed?.length && !data.removed_remote?.length && !data.servers
                && isStalenessDrift(sessions, data.changed, _feedKey)
                && Date.now() - _lastSidebarRenderAt < DRIFT_RENDER_MAX_AGE_MS;

            // Apply changed sessions (update existing or add new)
            if (data.changed) {
                for (const changed of data.changed) {
                    const key = _feedKey(changed);
                    const idx = sessions.findIndex(s => _feedKey(s) === key);
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
                sessions = sessions.filter(s => sessionServer(s) !== 'local' || !removedSet.has(s.session_id || s.name));
            }
            // Rows gone from a remote server arrive as {server, key}.
            if (data.removed_remote) {
                const removedRemote = new Set(data.removed_remote.map(r => identityKey(r.server, r.key)));
                sessions = sessions.filter(s => !removedRemote.has(_feedKey(s)));
            }

            // Treat merged list as a full update for the rest of the handler
            data.type = "coral_update";
            if (!data.servers && state.servers && state.servers.length) data.servers = state.servers;
            data.sessions = sessions;
        }

        if (data.type === "coral_update") {
            // Per-server status ([{id,label,status}]) is only sent when remotes exist.
            state.servers = (state.hub && Array.isArray(data.servers)) ? data.servers : [];
            // Detect sessions that just transitioned to "needs input"
            for (const s of data.sessions || []) {
                const id = _feedKey(s);
                const wasWaiting = state.prevWaitingState[id];
                const notifyEnabled = state.settings.notify_needs_input !== false;
                // Rising edge only. The first snapshot after load seeds the map
                // silently (no storm for agents that were already waiting), and the
                // agent the operator is typing into never toasts itself.
                if (notifyEnabled && _waitingSeeded && s.waiting_for_input && !wasWaiting
                    && popoutAllowsToastFor(s.session_id) && !_operatorIsTypingIn(s)) {
                    const label = escapeHtml(s.display_name || s.name);
                    const detail = s.waiting_summary ? escapeHtml(s.waiting_summary) : null;
                    const sessionName = s.name;
                    const agentType = s.agent_type;
                    const sessionId = s.session_id;
                    const sessionSrv = sessionServer(s);
                    showNotificationToast(label, detail, () => {
                        import('./sessions.js').then(m => m.selectLiveSession(sessionName, agentType, sessionId, sessionSrv));
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

            _waitingSeeded = true;

            // Fields a full update omits (commands, branch, repo_name, ...)
            // keep their previous value via the same generic merge.
            if (state.liveSessions && state.liveSessions.length) {
                const prevMap = {};
                for (const s of state.liveSessions) {
                    prevMap[_feedKey(s)] = s;
                }
                data.sessions = data.sessions.map(s => mergeSession(prevMap[_feedKey(s)], s));
            }
            state.liveSessions = data.sessions;
            if (!data._skipSidebarRender) {
                _lastSidebarRenderAt = Date.now();
                renderLiveSessions(data.sessions);
            }
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
                    ? data.sessions.find(s => s.session_id === sid && sessionServer(s) === sessionServer(state.currentSession))
                    : null;
                if (!s && !isPopout()) {
                    // Dashboard only: a restarted agent may briefly be matchable by name.
                    // Popout mode is exact-id only and never retargets.
                    const curSrv = sessionServer(state.currentSession);
                    s = data.sessions.find(s => s.name === state.currentSession.name && sessionServer(s) === curSrv);
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
                        const sameNameCount = data.sessions.filter(x => x.name === state.currentSession.name && sessionServer(x) === sessionServer(state.currentSession)).length;
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
                        cmdInput.placeholder = commandPlaceholder(headerName, s.agent_type, s.board_project);
                    }
                    updateSessionStatus(s.status);
                    updateSessionSummary(s.summary);
                    updateSessionBranch(s.branch, s.repo_name, s.git_state);
                    updateSessionFolder(s.working_directory || "");
                    updateWaitingIndicator(s);
                    updateChangedFileCount(s.changed_file_count || 0);
                    // Update terminal header status dot
                    const termDot = document.getElementById('terminal-status-dot');
                    if (termDot) termDot.className = terminalDotClass(s);
                    popoutUpdateFromSession(s);
                }
            }
        }
    }
}
