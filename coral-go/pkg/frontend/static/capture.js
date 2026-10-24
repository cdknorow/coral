/* Capture text rendering and auto-refresh */

import { importOnce } from './import_once.js';
import { state, CAPTURE_REFRESH_MS, CAPTURE_REFRESH_STREAMING_MS } from './state.js';
import { getRenderer } from './renderers.js';
import { renderTaskList } from './tasks.js';
import { renderEventTimeline } from './agentic_state.js';
import { getTerminalCols, isTerminalWsOpen, mayResizePane, isFollowingPane } from './xterm_renderer.js';
import { platform } from './platform/detect.js';
import { serverFetch, currentServer, isCloud, dropBody } from './server_base.js';
import { sessionUnreachable, liveViewShown, backoffBlocked, backoffRecord } from './remote_health.js';
import { pollEvery } from './overload.js';

let _refreshCaptureHasRun = false;

/* ── Tmux pane width sync ─────────────────────────────────────────────── */

function measureTerminalColumns() {
    const el = document.getElementById("pane-capture");
    if (!el) return null;

    const span = document.createElement("span");
    span.style.visibility = "hidden";
    span.style.position = "absolute";
    span.style.fontFamily = getComputedStyle(el).fontFamily;
    span.style.fontSize = getComputedStyle(el).fontSize;
    span.style.whiteSpace = "pre";
    span.textContent = "M";
    document.body.appendChild(span);
    const charWidth = span.getBoundingClientRect().width;
    document.body.removeChild(span);

    if (charWidth === 0) return null;

    const style = getComputedStyle(el);
    const availableWidth = el.clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight);
    return Math.floor(availableWidth / charWidth);
}

let _lastSyncedCols = null;
// Servers that refused a pane resize (a View-mode viewer may not change the shared pane): stop asking for this page load.
const _resizeRefused = new Set();

export async function syncPaneWidth() {
    if (!state.settings?.fit_pane_width) return;
    if (!state.currentSession || state.currentSession.type !== "live") return;
    // Only the interactive owner window may resize the shared pane (one that has joined the agent's
    // ownership group: a booting popout has not), never a phone or a View-only viewer, and not while
    // the terminal shows the pane at its own size (#3422).
    if (!mayResizePane() || isFollowingPane()) return;
    // In xterm mode the semantic pane is hidden, so use xterm's own column count
    const cols = getTerminalCols() || measureTerminalColumns();
    if (!cols || cols < 10) return;
    if (cols === _lastSyncedCols) return;
    _lastSyncedCols = cols;

    const server = currentServer();
    if (_resizeRefused.has(server)) return;
    // Through the hub, a viewer (View mode, or a machine limited to View) must not write at all, even in the background:
    // looking at a session never sends a request the hub would refuse.
    if (isCloud() && server !== 'local') {
        const { serverMayInteract } = await importOnce('./cloud_servers.js');
        if (!(await serverMayInteract(server))) { _lastSyncedCols = null; return; }
    }
    try {
        // Background request: no toast if the hub or the machine says no (View mode); remember it and do not ask again.
        const resp = await serverFetch(server, `/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/resize`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
                columns: cols,
                agent_type: state.currentSession.agent_type,
                session_id: state.currentSession.session_id,
            }),
            quiet: true,
        });
        if (resp && resp.status === 403) _resizeRefused.add(server);
    } catch (e) {
        console.error("Failed to sync pane width:", e);
    }
}

export function resetSyncedCols() {
    _lastSyncedCols = null;
}

export function renderCaptureText(el, text) {
    const agentType = state.currentSession?.agent_type || "claude";
    const sessionId = state.currentSession?.session_id || null;
    const renderer = getRenderer(agentType, sessionId);
    renderer.render(el, text);
}

export async function refreshCapture() {
    if (!state.currentSession || state.currentSession.type !== "live") return;

    // Skip polling when the tab is not visible (saves CPU in background tabs).
    // Skip this check in native apps — WKWebView can report document.hidden=true
    // permanently due to NSWindowStyleMaskFullSizeContentView, which blocks all
    // subsequent polls and leaves the agentic state panel blank.
    if (!platform.isNative && document.hidden && _refreshCaptureHasRun) return;
    _refreshCaptureHasRun = true;
    // Nothing to poll while the agent's server is unreachable or the agent view is not
    // on screen; after gateway errors, back off (#3151). The feed says when it is back.
    if (sessionUnreachable(state.currentSession) || !liveViewShown()) return;
    const backoffKey = 'poll:' + currentServer();
    if (backoffBlocked(backoffKey)) return;

    try {
        const params = new URLSearchParams();
        if (state.currentSession.agent_type) params.set("agent_type", state.currentSession.agent_type);
        if (state.currentSession.session_id) params.set("session_id", state.currentSession.session_id);
        const qs = params.toString() ? `?${params}` : "";

        // Single batch endpoint replaces separate capture + tasks + events calls. The last answer's
        // ETag goes back with the next poll of the same agent: the server says 304, with no body,
        // while nothing changed, and the last answer is used again (#3439).
        const server = currentServer();
        const path = `/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/poll${qs}`;
        const key = server + ' ' + path;
        const last = _lastPoll && _lastPoll.key === key ? _lastPoll : null;
        const resp = await serverFetch(server, path, last ? { headers: { 'If-None-Match': last.etag } } : undefined);
        const unchanged = resp.status === 304 && !!last;
        const gateway = backoffRecord(backoffKey, resp.ok || unchanged ? 200 : resp.status);
        if (!resp.ok && !unchanged) {
            dropBody(resp);
            if (gateway) { if (!_gatewayLogged) { _gatewayLogged = true; console.warn(`capture poll: server not reachable (${resp.status}); backing off`); } return; }
            // A launch's first poll can arrive before Coral knows the session (400): the next poll finds it, so say it quietly. Three in a row are a problem (#290).
            if (resp.status === 400 && ++_quietPoll400 <= 3) { console.debug('capture poll: the session is not known yet (400); trying again'); return; }
            throw new Error(`poll failed: ${resp.status}`);
        }
        _gatewayLogged = false;
        _quietPoll400 = 0;
        let data;
        if (unchanged) {
            data = last.data;
            _pollStats.notModified++;
        } else {
            data = await resp.json();
            // Through Coral Hub, ETag is dropped (no-store on a shared origin); the same validator
            // comes in X-Coral-Validator.
            const etag = resp.headers.get('ETag') || resp.headers.get('X-Coral-Validator');
            _lastPoll = etag ? { key, etag, data } : null;
            _pollStats.full++;
        }

        // ── Capture ──
        const captureData = data.capture || {};
        const el = document.getElementById("pane-capture");
        const text = captureData.capture || captureData.error || "No capture available";
        // Mark whether this is a real terminal buffer (vs an error placeholder)
        // so consumers such as the Mode label can tell "default mode" from
        // "nothing usable captured".
        const captureState = captureData.capture ? "ok" : "error";
        if (el.dataset.captureState !== captureState) el.dataset.captureState = captureState; // an unchanged value is still a mutation

        if (el._lastCapture !== text) {
            if (state.isSelecting) return;

            const savedScroll = state.autoScroll ? null : el.scrollTop;

            el._lastCapture = text;
            renderCaptureText(el, text);
            // Let listeners (e.g. the Mode label) re-read the rescanned buffer.
            document.dispatchEvent(new CustomEvent('coral:terminal-updated'));

            if (state.autoScroll) {
                el.scrollTop = el.scrollHeight;
            } else if (savedScroll !== null) {
                el.scrollTop = savedScroll;
            }
        }

        // ── Tasks ──
        state.currentAgentTasks = data.tasks || [];
        renderTaskList();

        // ── Events ──
        state.currentAgentEvents = data.events || [];
        renderEventTimeline();

    } catch (e) {
        console.error("Failed to refresh capture:", e);
    }
}

let _lastCaptureAt = 0;
let _gatewayLogged = false;
let _quietPoll400 = 0; // 400s in a row from the first polls of a new session (#290)
// The last poll answer with its ETag, for the same agent's next poll (#3439).
let _lastPoll = null; // { key, etag, data }
// How many polls came back in full or as 304 (tests and diagnostics).
const _pollStats = { full: 0, notModified: 0 };
export function capturePollStats() { return { ..._pollStats }; }

// One tick of the capture timer. The timer keeps its 500 ms beat so the fast pace is back within one beat of the
// terminal socket dropping; while the socket is up most beats do nothing.
 function captureTick(now = Date.now()) {
    // The fast beat is for a terminal that shows the pane (no socket yet). In Chat view the terminal is not
    // shown and has no socket (#3719), so the capture only feeds the side panels and keeps the slow pace.
    const chatView = !!document.getElementById("capture-wrapper")?.classList.contains("chat-mode");
    if ((isTerminalWsOpen() || chatView) && now - _lastCaptureAt < CAPTURE_REFRESH_STREAMING_MS) return;
    _lastCaptureAt = now;
    refreshCapture();
}

export function startCaptureRefresh() {
    stopCaptureRefresh();
    _lastCaptureAt = Date.now();
    refreshCapture();
    state.captureInterval = pollEvery(captureTick, CAPTURE_REFRESH_MS, { machine: () => currentServer(), agentView: true });
}

export function stopCaptureRefresh() {
    if (state.captureInterval) {
        clearInterval(state.captureInterval);
        state.captureInterval = null;
    }
}

// Re-sync the pane width once this window takes interactive control.
document.addEventListener('coral:ownership-changed', (e) => {
    if (e.detail && e.detail.owner) { resetSyncedCols(); syncPaneWidth(); }
});
