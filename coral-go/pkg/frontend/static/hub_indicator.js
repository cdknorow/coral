/* The Coral Hub connection indicator in the dashboard header (#3544): one icon beside Home,
 * the panel toggle and Settings that says whether this Coral's tunnel to its hub is up.
 *
 *   connected     the hub icon in the success colour (green: a status, not a selected button)
 *   connecting    muted, with a slow pulse (none with prefers-reduced-motion): dialing, or
 *                 reconnecting after the hub was unreachable or busy
 *   disconnected  a warning tint: enrolled, but the hub refused the token, the stored token
 *                 can't be read, this Coral must be upgraded, or it is enrolled with the hub's
 *                 old host (hub_moved, #3546: it connects nowhere until enrolled again)
 *
 * Hidden when this Coral isn't enrolled (no hub configured, not enrolled, enrolling, or still
 * loading), on Coral Hub itself, in the pop-out, and whenever the status can't be read (only
 * the dashboard on this computer may read it). The tooltip and accessible name give the state,
 * the hub's host and when it connected; never a token, a code or an id. A click opens Settings
 * at its Coral Hub section. State: GET /api/cloud/status, polled (the feed doesn't carry it). */

import { isCloud } from './server_base.js';
import { pollEvery } from './overload.js';

const STATUS_URL = '/api/cloud/status';
const POLL_MS = 15000;

/** The indicator's state for a cloud status: 'connected', 'connecting', 'disconnected', or
 *  null (hidden). Pure, for the unit test. */
export function indicatorState(st) {
    if (!st || typeof st !== 'object' || st.loading) return null;
    // Enrolled with the hub's old host (#3546): hub_moved stays set through a failed attempt to
    // enroll again (that failure has its own error), so the signal stays until one succeeds.
    if (st.state === 'idle' && (st.hub_moved || st.last_error_code === 'hub_moved')) return 'disconnected';
    switch (st.state) {
        case 'connected': return 'connected';
        case 'connecting': case 'retrying': return 'connecting';
        case 'revoked': case 'key_unreadable': case 'upgrade_required': return 'disconnected';
        default: return null; // off, idle, enrolling, or unknown
    }
}

/** The hub's host for display ("app.coralhub.dev"), or '' for anything that isn't an http(s) URL. */
export function hubHost(url) {
    try {
        const u = new URL(String(url || ''));
        return u.protocol === 'https:' || u.protocol === 'http:' ? u.host : '';
    } catch { return ''; }
}

/** The tooltip and accessible name for a status (state from indicatorState). */
export function indicatorLabel(st, state, now = new Date(), locale) {
    const host = hubHost(st && st.hub_url);
    const at = host ? ` to ${host}` : '';
    if (state === 'connected') {
        const since = st && st.connected_at ? new Date(st.connected_at) : null;
        let when = '';
        if (since && !isNaN(since)) {
            const sameDay = since.toDateString() === now.toDateString();
            when = ` since ${since.toLocaleString(locale, sameDay ? { hour: 'numeric', minute: '2-digit' } : { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' })}`;
        }
        return `Coral Hub: connected${at}${when}`;
    }
    if (state === 'connecting') {
        return st && st.state === 'retrying' ? `Coral Hub: reconnecting${at}` : `Coral Hub: connecting${at}`;
    }
    if (st && (st.hub_moved || st.last_error_code === 'hub_moved')) {
        // A fixed message with the new host only (Security): what to run, nothing else.
        return `Coral Hub moved to ${host || 'a new host'}: run coral cloud connect --force`;
    }
    const why = { revoked: 'the hub no longer accepts this Coral', key_unreadable: "the stored connection can't be read", upgrade_required: 'this Coral needs an update' }[st && st.state] || 'not connected';
    return `Coral Hub: disconnected${host ? ` from ${host}` : ''} (${why})`;
}

let _btn = null;

/** Hide the indicator, leaving no state, title or name behind (QA). */
function hide(b) {
    b.hidden = true;
    b.removeAttribute('data-state');
    b.removeAttribute('title');
    b.removeAttribute('aria-label');
}
let _timer = null;
let _stopped = false;

function button() {
    if (_btn && _btn.isConnected) return _btn;
    const bar = document.querySelector('.top-bar-actions');
    if (!bar) return null;
    const b = document.createElement('button');
    b.type = 'button';
    b.id = 'hub-indicator';
    b.className = 'top-bar-btn hub-indicator';
    b.hidden = true;
    // A hub: one node joined to three.
    b.innerHTML = '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" aria-hidden="true"><circle cx="8" cy="8" r="2"/><circle cx="8" cy="2.5" r="1.3"/><circle cx="2.8" cy="12" r="1.3"/><circle cx="13.2" cy="12" r="1.3"/><path d="M8 6V3.8M6.3 9.1 3.9 11.2M9.7 9.1l2.4 2.1"/></svg>';
    b.addEventListener('click', openHubSettings);
    bar.insertBefore(b, bar.firstChild);
    _btn = b;
    return b;
}

/** Show a status on the indicator (exported for the browser test). */
export function renderIndicator(st, now = new Date()) {
    // Other views follow the same status (the home section's Coral Hub item, #3546).
    try { document.dispatchEvent(new CustomEvent('coral:cloud-status', { detail: st })); } catch { /* no DOM events */ }
    const b = button();
    if (!b) return;
    const state = indicatorState(st);
    if (!state) { hide(b); return; }
    const label = indicatorLabel(st, state, now);
    b.dataset.state = state;
    b.title = label;
    b.setAttribute('aria-label', label);
    b.hidden = false;
}

/** Read the status and show it (the poll's step; exported for the browser test). */
export async function refresh() {
    if (_stopped) return;
    try {
        const resp = await fetch(STATUS_URL, { credentials: 'same-origin' });
        if (resp.status === 403 || resp.status === 404) { stopHubIndicator(); return; } // not this computer's dashboard
        if (!resp.ok) return; // a blip: keep what is shown
        renderIndicator(await resp.json());
    } catch { /* offline: keep what is shown */ }
}

/** Open Settings at its Coral Hub section. */
 async function openHubSettings() {
    try { await window.showSettingsModal?.(); } catch { /* the dialog shows its own errors */ }
    const section = document.getElementById('settings-cloud');
    const heading = section && section.previousElementSibling;
    (heading || section)?.scrollIntoView({ block: 'start' });
    const status = document.getElementById('settings-cloud-status');
    if (status) { status.setAttribute('tabindex', '-1'); status.focus({ preventScroll: true }); }
}

/** Start the indicator (the dashboard on this computer; not on Coral Hub or in the pop-out). */
export function initHubIndicator() {
    if (isCloud() || document.body.classList.contains('popout-mode')) return;
    _stopped = false;
    if (!button()) return;
    refresh();
    _timer = pollEvery(refresh, POLL_MS);
    document.addEventListener('visibilitychange', () => { if (!document.hidden) refresh(); });
}

/** Stop polling and hide (the status can't be read here). */
 function stopHubIndicator() {
    _stopped = true;
    if (_timer) { clearInterval(_timer); _timer = null; }
    if (_btn) hide(_btn);
}
