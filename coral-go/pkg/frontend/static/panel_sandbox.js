/* Agent UI panels on the Cloud Hub (option B of docs/design-usercontent-origin.md): agent-authored HTML in an opaque-origin sandbox.
 *
 * A panel is HTML (and script) the AGENT wrote, so the hub page never runs it. The dashboard frames a small trusted HOST document
 * (static/panel_host.html, served by the hub with its own CSP) with `sandbox="allow-scripts"` (no allow-same-origin: no cookies, no
 * storage, no access to the hub) and posts the panel HTML into it once. The host runs the panel in a child frame whose CSP is
 * `default-src 'none'; connect-src 'none'; frame-src 'none'; form-action 'none'; base-uri 'none'` (it can draw and run its own script
 * and nothing else), and whose navigations are governed by the host's `frame-src about:` (so it cannot send data out by navigating).
 *
 * The bridge carries exactly ONE message type up (`coral-ui-event`: a panel interaction, validated and rebuilt by the host and again here)
 * and one down (`coral-ui-result`). There is no read bridge, no fetch shim and no way to reach any other endpoint. Writes go to the
 * Interact-tier events route only, through the dashboard's own fetch (CSRF and same-origin as for any hub call).
 *
 * Why a URL and not srcdoc: a srcdoc/blob/data document INHERITS the embedding page's CSP, and the hub's `script-src 'self'` would
 * silently stop every panel script. A document loaded by URL is governed by its own response headers. */

export const PANEL_HOST_URL = '/static/panel_host.html';
export const PANEL_CSP = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'";
/** The response header the hub sends with panel_host.html (also asserted in the hub's tests). */
export const PANEL_HOST_CSP = PANEL_CSP.replace("frame-src 'none'", 'frame-src about:') + "; frame-ancestors 'self'; sandbox allow-scripts";
export const MAX_PANEL_BYTES = 2 * 1024 * 1024 + 16 * 1024; // 2 MiB of HTML plus the machine's small bridge prelude
export const MAX_PANELS = 20;
export const MAX_EVENT_BYTES = 16384;
const ID_RE = /^[A-Za-z0-9_-]{1,64}$/;

/** Is `m` a well-formed panel event? Used by the dashboard on every message from the host frame (the host checks it too). */
export function isPanelEvent(m) {
    if (!m || typeof m !== 'object' || m.type !== 'coral-ui-event') return false;
    if (typeof m.action !== 'string' || !ID_RE.test(m.action)) return false;
    if (typeof m.requestId !== 'string' || m.requestId.length > 64 || !m.requestId) return false;
    try { if (new TextEncoder().encode(JSON.stringify(m.payload === undefined ? null : m.payload)).length > MAX_EVENT_BYTES) return false; } catch { return false; }
    return true;
}

/** A locked frame for the panel host: scripts only, an opaque origin, no referrer. */
export function createPanelFrame(title) {
    const frame = document.createElement('iframe');
    frame.title = title || 'Agent panel';
    frame.setAttribute('sandbox', 'allow-scripts');
    frame.setAttribute('referrerpolicy', 'no-referrer');
    frame.setAttribute('allow', ''); // no camera, microphone, geolocation, clipboard...
    frame.src = PANEL_HOST_URL;
    return frame;
}

/** Track the host frame: the HTML is handed over once, when both the host has said it is ready AND the HTML has arrived (in either
 *  order). `onNavigated` fires if the panel tried to leave. Returns { setHtml(html), detach() }. */
export function openPanel(frame, { onNavigated, onReady } = {}) {
    let ready = false, html = null, sent = false;
    const trySend = () => {
        if (!ready || html === null || sent || !frame.contentWindow) return;
        sent = true;
        frame.contentWindow.postMessage({ type: 'coral-panel-load', html }, '*');
        if (onReady) onReady();
    };
    const handler = e => {
        if (e.source !== frame.contentWindow) return; // only this host
        const m = e.data;
        if (!m || typeof m !== 'object') return;
        if (m.type === 'coral-panel-host-ready') {
            // A repeat ready means the host frame was reloaded (moving an iframe, e.g. Expand, reloads it): hand the HTML over again.
            if (ready) sent = false;
            ready = true; trySend();
        }
        else if (m.type === 'coral-panel-navigated' && onNavigated) onNavigated();
    };
    window.addEventListener('message', handler);
    return { setHtml(h) { html = String(h); trySend(); }, detach() { window.removeEventListener('message', handler); } };
}

/** Per-panel limit on panel events: one in flight, at most ~2 a second and ~100 a minute. `now` is injectable for tests.
 *  tryStart() returns '' when the event may go ahead (then call done() when its request finishes) or the reason it may not. */
export function createEventThrottle(now = () => Date.now(), { minGapMs = 500, perMinute = 100 } = {}) {
    let inflight = false, last = -Infinity;
    const stamps = [];
    return {
        tryStart() {
            const t = now();
            if (inflight) return 'Still sending the previous response.';
            if (t - last < minGapMs) return 'Too many responses; slow down.';
            while (stamps.length && t - stamps[0] >= 60000) stamps.shift();
            if (stamps.length >= perMinute) return 'Too many responses this minute.';
            inflight = true; last = t; stamps.push(t);
            return '';
        },
        done() { inflight = false; },
    };
}
