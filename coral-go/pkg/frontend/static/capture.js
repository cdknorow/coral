/* Capture text rendering and auto-refresh */

import { state, CAPTURE_REFRESH_MS } from './state.js';
import { isInteractiveOwner } from './ownership.js';
import { getRenderer } from './renderers.js';
import { renderTaskList } from './tasks.js';
import { renderEventTimeline } from './agentic_state.js';
import { getTerminalCols } from './xterm_renderer.js';
import { platform } from './platform/detect.js';
import { serverFetch, currentServer, isCloud } from './server_base.js';

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
    // Only the interactive owner window may resize the shared pane.
    if (!isInteractiveOwner()) return;
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
        const { serverMayInteract } = await import('./cloud_servers.js');
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

    try {
        const params = new URLSearchParams();
        if (state.currentSession.agent_type) params.set("agent_type", state.currentSession.agent_type);
        if (state.currentSession.session_id) params.set("session_id", state.currentSession.session_id);
        const qs = params.toString() ? `?${params}` : "";

        // Single batch endpoint replaces separate capture + tasks + events calls
        const resp = await serverFetch(currentServer(), `/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/poll${qs}`);
        if (!resp.ok) throw new Error(`poll failed: ${resp.status}`);
        const data = await resp.json();

        // ── Capture ──
        const captureData = data.capture || {};
        const el = document.getElementById("pane-capture");
        const text = captureData.capture || captureData.error || "No capture available";
        // Mark whether this is a real terminal buffer (vs an error placeholder)
        // so consumers such as the Mode label can tell "default mode" from
        // "nothing usable captured".
        el.dataset.captureState = captureData.capture ? "ok" : "error";

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

export function startCaptureRefresh() {
    stopCaptureRefresh();
    refreshCapture();
    state.captureInterval = setInterval(refreshCapture, CAPTURE_REFRESH_MS);
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
