/* Capture text rendering and auto-refresh */

import { state, CAPTURE_REFRESH_MS } from './state.js';
import { getRenderer } from './renderers.js';
import { loadAgentTasks } from './tasks.js';
import { loadAgentEvents } from './agentic_state.js';

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

export async function syncPaneWidth() {
    if (!state.settings?.fit_pane_width) return;
    if (!state.currentSession || state.currentSession.type !== "live") return;
    const cols = measureTerminalColumns();
    if (!cols || cols < 10) return;
    if (cols === _lastSyncedCols) return;
    _lastSyncedCols = cols;

    try {
        await fetch(`/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/resize`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
                columns: cols,
                agent_type: state.currentSession.agent_type,
                session_id: state.currentSession.session_id,
            }),
        });
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

    try {
        const params = new URLSearchParams();
        if (state.currentSession.agent_type) params.set("agent_type", state.currentSession.agent_type);
        if (state.currentSession.session_id) params.set("session_id", state.currentSession.session_id);
        const qs = params.toString() ? `?${params}` : "";
        let captureUrl = `/api/sessions/live/${encodeURIComponent(state.currentSession.name)}/capture${qs}`;
        const resp = await fetch(captureUrl);
        const data = await resp.json();
        const el = document.getElementById("pane-capture");
        const text = data.capture || data.error || "No capture available";

        // Only update if content changed to avoid scroll jank
        if (el._lastCapture !== text) {
            // Defer DOM update while user is selecting text
            if (state.isSelecting) return;
            el._lastCapture = text;
            renderCaptureText(el, text);
            if (state.autoScroll) {
                el.scrollTop = el.scrollHeight;
            }
        }
    } catch (e) {
        console.error("Failed to refresh capture:", e);
    }

    // Poll tasks and events unconditionally
    if (state.currentSession && state.currentSession.type === "live") {
        const sid = state.currentSession.session_id;
        loadAgentTasks(state.currentSession.name, sid);
        loadAgentEvents(state.currentSession.name, sid);
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
