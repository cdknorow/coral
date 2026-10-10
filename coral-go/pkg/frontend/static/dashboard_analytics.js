/* Bounded browser observations only. Never send session IDs, text, or errors. */
let enabled = false;
let initialized = false;
let sessionsReady = false;
let startupReady = false;
let ready = false;
const failures = new Set();
let activeDay = '';
const pageID = (() => {
    if (typeof crypto.randomUUID === 'function') return crypto.randomUUID();
    const bytes = crypto.getRandomValues(new Uint8Array(16));
    bytes[6] = (bytes[6] & 15) | 64; bytes[8] = (bytes[8] & 63) | 128;
    const hex = Array.from(bytes, b => b.toString(16).padStart(2,'0')).join('');
    return `${hex.slice(0,8)}-${hex.slice(8,12)}-${hex.slice(12,16)}-${hex.slice(16,20)}-${hex.slice(20)}`;
})();

function observeActiveDay(force = false) {
    if (!ready || document.hidden) return;
    const day = new Date().toISOString().slice(0,10);
    if (!force && activeDay === day) return;
    activeDay = day;
    emit('dashboard_active_day');
}

function emit(event, props = {}) {
    try {
        fetch('/api/tracking/event', {
            method:'POST', headers:{'Content-Type':'application/json'},
            body:JSON.stringify({event, props}), keepalive:true,
        }).catch(() => {});
    } catch { /* Observability must never affect the application. */ }
}

export function beginDashboardAnalytics() {
    enabled = document.body.dataset.entryMode !== 'agent' && !location.pathname.startsWith('/agent/');
}

function maybeReady() {
    if (!enabled || ready || !initialized || !sessionsReady || !startupReady) return;
    ready = true;
    emit('dashboard_ready', {page_id:pageID});
    // Installation/UTC-day deduplication belongs to the server, not browser storage.
    observeActiveDay();
    document.addEventListener('visibilitychange', () => observeActiveDay(true));
    setInterval(observeActiveDay, 60000);
}

export function dashboardInitialized() { initialized = true; maybeReady(); }
export function dashboardSessionsLoaded(ok) {
    if (ok) { sessionsReady = true; maybeReady(); }
}
export function dashboardStartupComplete() { startupReady = true; maybeReady(); }

export function dashboardFailed(code) {
    const codes = ['sessions_fetch_http','sessions_fetch_network','sessions_fetch_invalid','status_fetch_http','status_fetch_network','status_fetch_invalid','init_failed'];
    if (!enabled || ready || !codes.includes(code) || failures.has(code)) return;
    failures.add(code);
    emit('dashboard_failed', {code});
}

// Composer intent only: not proof of socket delivery, agent readiness or response.
export function dashboardPromptRequested() {
    // Let the server own the install milestone so an opted-out or failed
    // observation does not consume a later, explicit opted-in submission.
    if (!enabled) return;
    emit('prompt_submit_requested', {source:'dashboard_composer'});
}
