/* Single-agent popout mode.
 *
 * GET /agent/{uuid} renders the same index template with
 * <body data-entry-mode="agent" data-target-session-id="<uuid>">. In this mode
 * the page shows ONLY the selected-agent workspace (terminal/transcript pane,
 * right tools pane, quick actions, command input): no top nav, no sidebar.
 *
 * Identity contract: the exact UUID is the only routing key. Routing fields
 * (name, agent_type, tmux_session) come from GET /api/sessions/{uuid}/resolve
 * and are never derived or matched by name. The session id never appears in
 * visible text, tooltips or document.title. */

import { state } from './state.js';
import { resolveSessionIdentity } from './render.js';
import { toggleAgenticPanel } from './sidebar.js';
import { fitTerminal } from './xterm_renderer.js';
import { showView } from './utils.js';
import { isInteractiveOwner, claimOwnership, releaseOwnership, onOwnershipChange } from './ownership.js';

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const RESOLVE_RETRIES = 3;      // index lag: valid id not yet in the live list
const RESOLVE_RETRY_MS = 1000;
const RESOLVER_DOWN_BASE_MS = 750; // first retry while the resolver is unreachable
const RESOLVER_DOWN_MAX_MS = 5000; // backoff cap while the resolver is unreachable
let _resolverRetryTimer = null;

let _mode = null;               // 'dashboard' | 'agent' | 'invalid'
let _targetId = '';
let _state = 'loading';         // loading|working|waiting|idle|sleeping|ended|reconnecting|not-found
let _resolved = null;           // authoritative record from the resolver
let _lastSession = null;        // last merged live record for the target
let _prevIds = null;            // session ids seen on the previous WS tick
let _restartedId = '';
let _terminalDown = false;
let _viewerTimer = null;
let _attached = false;          // terminal attached for the current target
let _waking = false;
const DESTRUCTIVE_SELECTOR = '[onclick*="killSession"],[onclick*="restartSession"],[onclick*="renameAgent"],[onclick*="confirmRestart"],[onclick*="killSessionDirect"],[onclick*="restartDirect"]';

const PILL_TEXT = {
    loading: 'Connecting…',
    working: 'Working',
    waiting: 'Needs input',
    idle: 'Idle',
    sleeping: 'Sleeping',
    ended: 'Ended',
    reconnecting: 'Reconnecting…',
    'not-found': 'Not found',
};
const TITLE_GLYPH = {
    working: '●', waiting: '⏸', idle: '○', sleeping: '◌', ended: '✕',
    reconnecting: '…', loading: '…', 'not-found': '?',
};

function _detect() {
    if (_mode !== null) return;
    const body = document.body;
    let mode = (body && body.dataset.entryMode) || '';
    let id = (body && body.dataset.targetSessionId) || '';
    // QA injection fallback only; production canonical source is the body attributes.
    const boot = window.__CORAL_BOOT;
    if (!mode && boot && boot.entryMode) { mode = String(boot.entryMode); id = String(boot.sessionId || ''); }
    if (!mode) {
        const m = location.pathname.match(/^\/agent\/([0-9a-f-]{36})\/?$/i);
        if (m) { mode = 'agent'; id = m[1]; }
    }
    if (mode !== 'agent') { _mode = 'dashboard'; return; }
    _targetId = UUID_RE.test(id) ? id.toLowerCase() : '';
    _mode = _targetId ? 'agent' : 'invalid';
}

export function isPopout() { _detect(); return _mode === 'agent' || _mode === 'invalid'; }
export function popoutTargetId() { _detect(); return _targetId; }
export function popoutGetState() { return _state; }

/** True while terminal input/keys/commands must not be sent: no authoritative
 *  attachable state yet (loading/reconnecting), sleeping, ended or not-found. */
export function popoutTerminalBlocked() {
    if (!isPopout()) return false;
    return !(_state === 'working' || _state === 'waiting' || _state === 'idle');
}
export function popoutResolved() { return _resolved; }

/** Terminal header label: the resolved identity only, in the dashboard and
 *  the popout alike. The session id stays in state, routing, URLs and Session
 *  Info; it is never shown in the header text, title or aria (task #156). */
export function formatTerminalLabel(identity, _sessionId) {
    return identity || '';
}

/** Toasts for other sessions are noise in a popout. */
export function popoutAllowsToastFor(sessionId) {
    return !isPopout() || (!!sessionId && sessionId === _targetId);
}

// ── Boot ──────────────────────────────────────────────────────────────────

/** Mark the document early (before any renderer runs) so CSS/storage scoping applies. */
export function applyPopoutBodyClass() {
    if (!isPopout()) return false;
    document.body.classList.add('popout-mode');
    document.body.dataset.entryMode = 'agent';
    if (_targetId) document.body.dataset.targetSessionId = _targetId;
    return true;
}

export async function initPopout() {
    if (!isPopout()) return;
    applyPopoutBodyClass();
    // The workspace is the whole page: show it before anything resolves so
    // loading / not-found / ended states render inside the real shell.
    showView('live-session-view');
    // Kill/restart/rename are dashboard-only: their controls must not exist in
    // the popout DOM at all (not merely hidden).
    document.querySelectorAll(DESTRUCTIVE_SELECTOR).forEach(el => el.remove());
    _wireHeader();
    _wireViewerChip();
    document.addEventListener('coral:terminal-closed', () => _onTerminalClosed());
    // renderQuickActions() rebuilds the toolbar; keep freshly rendered controls gated.
    const toolbar = document.getElementById('command-toolbar');
    if (toolbar && typeof MutationObserver !== 'undefined') {
        new MutationObserver(() => _gateTerminalControls(popoutTerminalBlocked())).observe(toolbar, { childList: true });
    }
    document.addEventListener('coral:terminal-disconnected', () => { _terminalDown = true; if (!_isTerminal(_state)) _setState('reconnecting'); });
    document.addEventListener('coral:terminal-reconnected', () => { _terminalDown = false; if (_state === 'reconnecting' && _lastSession) _applyLiveState(_lastSession); });
    if (_mode === 'invalid') { _setState('not-found'); return; }
    await resolveAndSelect();
}

function _isTerminal(s) { return s === 'ended' || s === 'not-found'; }

/** The resolver is the ONLY source of routing/lifecycle. A non-200 or a
 *  network error yields null: the caller shows Reconnecting… and retries;
 *  it never derives a record from the live list or history. */
async function _fetchResolver(id) {
    try {
        const resp = await fetch(`/api/sessions/${encodeURIComponent(id)}/resolve`);
        if (resp.status !== 200) return null;
        const j = await resp.json();
        return (j && typeof j === 'object' && j.state) ? j : null;
    } catch { return null; }
}

export async function resolveAndSelect(attempt = 0, downAttempt = 0) {
    if (!_targetId) { _setState('not-found'); return; }
    if (_resolverRetryTimer) { clearTimeout(_resolverRetryTimer); _resolverRetryTimer = null; }
    if (downAttempt === 0) _setState('loading');
    const r = await _fetchResolver(_targetId);
    if (!r) {
        // Resolver unreachable/erroring: surface it, retry with backoff, never attach.
        _setState('reconnecting');
        // Steady 750ms retries while the outage is fresh, then back off to the cap.
        const delay = downAttempt < 6
            ? RESOLVER_DOWN_BASE_MS
            : Math.min(RESOLVER_DOWN_BASE_MS * Math.pow(2, downAttempt - 5), RESOLVER_DOWN_MAX_MS);
        _resolverRetryTimer = setTimeout(() => resolveAndSelect(attempt, downAttempt + 1), delay);
        return;
    }
    _resolved = r;
    state.popoutTarget = r;
    if (r.state === 'not_found') {
        if (attempt < RESOLVE_RETRIES) {
            setTimeout(() => resolveAndSelect(attempt + 1), RESOLVE_RETRY_MS);
            return;
        }
        _setState('not-found');
        return;
    }
    _renderIdentity(r);
    if (r.state === 'finished') {
        _setState('ended');
        return;
    }
    // active | sleeping: make sure the live list holds the record, then select by exact id.
    if (!(state.liveSessions || []).some(s => s.session_id === _targetId) && window._coralLoadLiveSessions) {
        await window._coralLoadLiveSessions();
    }
    if (window.selectLiveSession) {
        try {
            await window.selectLiveSession(r.name, r.agent_type, r.session_id);
        } catch (e) {
            console.error('popout: selectLiveSession failed', e);
        }
    }
    _attached = !!(r.tmux_session && r.state === 'active');
    // Resolver fields win over the list at selection time; ticks enrich later.
    const live = (state.liveSessions || []).find(s => s.session_id === _targetId);
    _applyLiveState(Object.assign({}, live || {}, r));
}

/** Explicit Wake from the sleeping overlay (non-destructive, user gesture). */
export async function popoutWake() {
    if (!isPopout() || !_resolved || _waking) return;
    _waking = true;
    try {
        if (window.toggleAgentSleep) {
            await window.toggleAgentSleep(_resolved.name, _resolved.agent_type, _resolved.session_id, 'wake');
        }
        // Poll the resolver until the pane is attachable, then (re)select to attach.
        for (let i = 0; i < 10; i++) {
            await new Promise(r => setTimeout(r, 500));
            const r = await _fetchResolver(_targetId);
            if (r && r.state === 'active' && r.tmux_session) { await resolveAndSelect(RESOLVE_RETRIES); break; }
        }
    } finally {
        _waking = false;
    }
}

// ── Header / state ────────────────────────────────────────────────────────

function _wireHeader() {
    const openWin = document.getElementById('terminal-open-window-link');
    if (openWin) { openWin.hidden = true; openWin.removeAttribute('href'); }
    const openMain = document.getElementById('popout-open-main-btn');
    if (openMain) {
        openMain.hidden = false;
        if (_targetId) openMain.href = `/#chat/${_targetId}`;
        else openMain.href = '/';
        openMain.target = '_blank';
        openMain.rel = 'noopener noreferrer';
    }
    const toggle = document.getElementById('popout-panel-toggle-btn');
    if (toggle) {
        toggle.hidden = false;
        _syncPanelToggle();
    }
    const badge = document.getElementById('terminal-type-badge');
    if (badge) badge.hidden = true;
    const pill = document.getElementById('terminal-state-pill');
    if (pill) pill.hidden = false;
}

function _isPhone() { return window.innerWidth <= 767; }

export function togglePopoutPanel() {
    const panel = document.getElementById('agentic-state');
    if (!panel) return;
    if (_isPhone()) {
        panel.classList.toggle('mobile-panel-overlay');
        panel.classList.remove('collapsed');
    } else {
        toggleAgenticPanel();
    }
    _syncPanelToggle();
    setTimeout(fitTerminal, 50);
}

function _syncPanelToggle() {
    const btn = document.getElementById('popout-panel-toggle-btn');
    const panel = document.getElementById('agentic-state');
    if (!btn || !panel) return;
    const open = _isPhone() ? panel.classList.contains('mobile-panel-overlay') : !panel.classList.contains('collapsed');
    btn.setAttribute('aria-pressed', open ? 'true' : 'false');
    btn.setAttribute('aria-label', open ? 'Hide side panel' : 'Show side panel');
    btn.title = open ? 'Hide side panel' : 'Show side panel';
}

function _renderIdentity(rec) {
    const identity = resolveSessionIdentity(rec || {});
    const label = document.getElementById('terminal-header-label');
    if (label) label.textContent = identity;
    const badge = document.getElementById('terminal-type-badge');
    if (badge) {
        const t = (rec && rec.agent_type) || '';
        badge.textContent = t;
        badge.className = `badge terminal-type-badge ${t.toLowerCase()}`;
        badge.hidden = !t;
    }
    _updateTitle(identity);
}

function _updateTitle(identity) {
    const id = identity || (_lastSession ? resolveSessionIdentity(_lastSession) : (_resolved ? resolveSessionIdentity(_resolved) : 'Agent'));
    const glyph = TITLE_GLYPH[_state] || '○';
    document.title = `${glyph} ${id} · Coral`;
}

function _setState(next) {
    _state = next;
    const pill = document.getElementById('terminal-state-pill');
    if (pill) {
        pill.dataset.state = next;
        pill.textContent = PILL_TEXT[next] || next;
        pill.hidden = false;
    }
    document.body.classList.toggle('popout-ended', next === 'ended' || next === 'not-found');
    // Terminal actions (quick-action strip, send buttons) follow the same gate as the input.
    const blocked = !(next === 'working' || next === 'waiting' || next === 'idle');
    document.body.classList.toggle('popout-terminal-blocked', blocked);
    _gateTerminalControls(blocked);
    const wrapper = document.getElementById('capture-wrapper');
    if (wrapper) wrapper.classList.toggle('loading-skeleton', next === 'loading');
    _renderCards(next);
    _setInputEnabled(!(next === 'ended' || next === 'not-found' || next === 'loading' || next === 'sleeping' || next === 'reconnecting'));
    _updateTitle();
}

/** Every control in the command pane that can send input/keys/commands
 *  (Send, +Team, send menu items, quick actions, macros, Mode, Enter/Esc/arrows,
 *  mobile send buttons) is truly disabled while blocked and re-enabled after. */
const SEND_CAPABLE = /sendCommand|sendCommandWithTeam|sendBoardProtocol|sendRawKeys|cycleModeToggle|sendModeToggle|sendQuickCommand|executeMacro|resendInputPrompt|toggleSendMenu/;
function _gateTerminalControls(blocked) {
    const pane = document.getElementById('command-pane');
    if (!pane) return;
    pane.querySelectorAll('button, [role="button"], .send-menu-item').forEach(b => {
        const handler = b.getAttribute('onclick') || '';
        const sends = SEND_CAPABLE.test(handler) || b.classList.contains('mobile-send-btn')
            || b.classList.contains('btn-send') || b.classList.contains('btn-send-dropdown')
            || b.classList.contains('send-menu-item') || b.closest('.command-pane-toolbar');
        if (!sends) return;
        b.disabled = blocked;
        b.setAttribute('aria-disabled', blocked ? 'true' : 'false');
        if (blocked) b.setAttribute('tabindex', '-1'); else b.removeAttribute('tabindex');
    });
}

function _renderCards(next) {
    const overlay = document.getElementById('session-ended-overlay');
    const ended = document.getElementById('popout-ended');
    const notFound = document.getElementById('popout-not-found');
    const def = document.getElementById('session-ended-default');
    const restarting = document.getElementById('session-restarting');
    const lost = document.getElementById('session-lost-connection');
    if (!overlay) return;
    if (next === 'ended' || next === 'not-found') {
        overlay.style.display = '';
        if (def) def.style.display = 'none';
        if (restarting) restarting.style.display = 'none';
        if (lost) lost.style.display = 'none';
        if (ended) ended.style.display = next === 'ended' ? '' : 'none';
        if (notFound) notFound.style.display = next === 'not-found' ? '' : 'none';
        const sleep = document.getElementById('session-sleeping-overlay');
        if (sleep) sleep.style.display = 'none';
    } else {
        if (ended) ended.style.display = 'none';
        if (notFound) notFound.style.display = 'none';
        if (next !== 'reconnecting') overlay.style.display = 'none';
    }
    const link = document.getElementById('popout-restarted-link');
    if (link) {
        if (_restartedId && next === 'ended') { link.href = `/agent/${_restartedId}`; link.hidden = false; }
        else { link.hidden = true; link.removeAttribute('href'); }
    }
    // History transcript link: only in the ended state, never for unknown ids.
    const hist = document.getElementById('popout-open-history');
    if (hist) {
        if (next === 'ended' && _targetId) { hist.href = `/#session/${_targetId}`; hist.hidden = false; }
        else { hist.hidden = true; hist.removeAttribute('href'); }
    }
}

function _setInputEnabled(enabled) {
    const input = document.getElementById('command-input');
    if (input) input.disabled = !enabled;
    if (input && !enabled) {
        input.placeholder = _state === 'not-found' ? 'No agent to send to'
            : _state === 'reconnecting' ? 'Reconnecting to Coral…'
            : _state === 'loading' ? 'Connecting…'
            : 'Session ended';
    }
}

function _applyLiveState(s) {
    if (!s || _isTerminal(_state)) return;
    _lastSession = s;
    let next;
    if (s.sleeping) next = 'sleeping';
    else if (s.waiting_for_input || s.not_started) next = 'waiting';
    else if (s.working) next = 'working';
    else next = 'idle';
    if (_terminalDown && next !== 'sleeping') next = 'reconnecting';
    _renderIdentity(s);
    _setState(next);
    _syncSleeping(next === 'sleeping');
    // A target that woke up since we selected it has no terminal yet: attach now.
    if (next !== 'sleeping' && !_attached && !_waking && _resolved && _state !== 'loading') {
        _attached = true; // guard against re-entry while the resolver round-trips
        resolveAndSelect(RESOLVE_RETRIES).catch(() => { _attached = false; });
    }
}

function _syncSleeping(sleeping) {
    const overlay = document.getElementById('session-sleeping-overlay');
    if (overlay) overlay.style.display = sleeping ? '' : 'none';
    const wake = document.getElementById('popout-wake-btn');
    if (wake) wake.hidden = !sleeping;
    if (sleeping) {
        _attached = false;
        const input = document.getElementById('command-input');
        if (input) { input.disabled = true; input.placeholder = 'Agent is sleeping — wake it to send commands'; }
    }
}

/** Called by sessions.js / websocket.js with the merged live record of the target. */
export function popoutUpdateFromSession(s) {
    if (!isPopout() || !s) return;
    if (s.session_id && s.session_id !== _targetId) return;
    _applyLiveState(s);
}

/** Called by websocket.js on every coral_update in popout mode. */
export function popoutHandleSessionsTick(sessions) {
    if (!isPopout() || !_targetId) return;
    const list = Array.isArray(sessions) ? sessions : [];
    const ids = new Set(list.map(s => s.session_id));
    const target = list.find(s => s.session_id === _targetId);
    if (target) {
        _applyLiveState(target);
    } else if (_resolved && !_isTerminal(_state) && _state !== 'loading') {
        // Target vanished from the live list: ended. Never adopt a same-name
        // session; only offer it as an explicit link when it is new.
        const candidate = list.find(s => s.session_id !== _targetId
            && s.name === _resolved.name && s.agent_type === _resolved.agent_type
            && (!_prevIds || !_prevIds.has(s.session_id)));
        if (candidate) _restartedId = candidate.session_id;
        _setState('ended');
    } else if (_state === 'ended' && !_restartedId && _resolved) {
        const candidate = list.find(s => s.session_id !== _targetId
            && s.name === _resolved.name && s.agent_type === _resolved.agent_type
            && (!_prevIds || !_prevIds.has(s.session_id)));
        if (candidate) { _restartedId = candidate.session_id; _renderCards('ended'); }
    }
    _prevIds = ids;
}

function _onTerminalClosed() {
    if (!isPopout()) return;
    if (!_isTerminal(_state)) _setState('ended');
}

/** Retry button on the not-found card. */
export function popoutRetry() {
    if (!isPopout()) return;
    _restartedId = '';
    resolveAndSelect(RESOLVE_RETRIES); // one immediate attempt, no retry loop
}

// ── Viewer chip (multi-window ownership) ──────────────────────────────────

function _wireViewerChip() {
    const chip = document.getElementById('popout-viewer-chip');
    if (!chip) return;
    chip.addEventListener('click', () => { claimOwnership(); _syncChip(true); });
    onOwnershipChange((owner) => _syncChip(owner));
    _syncChip(isInteractiveOwner());
}

function _syncChip(owner, lostControl = false) {
    const chip = document.getElementById('popout-viewer-chip');
    if (!chip) return;
    if (owner || !state.currentSession || state.currentSession.type !== 'live') {
        chip.hidden = true;
        return;
    }
    chip.hidden = false;
    if (_viewerTimer) { clearTimeout(_viewerTimer); _viewerTimer = null; }
    if (lostControl) {
        chip.textContent = 'Controlled in another window';
        _viewerTimer = setTimeout(() => { chip.textContent = 'Viewing — click to take control'; }, 2000);
    } else {
        chip.textContent = 'Viewing — click to take control';
    }
}

// Viewer chip is useful in the dashboard too (same element); keep it in sync there.
onOwnershipChange((owner) => { if (!isPopout()) _syncChip(owner, !owner); });

export const popoutApi = {
    isPopout,
    targetSessionId: popoutTargetId,
    getState: popoutGetState,
    resolved: popoutResolved,
    isOwner: isInteractiveOwner,
    claimOwnership,
    releaseOwnership,
    retry: popoutRetry,
    togglePanel: togglePopoutPanel,
    wake: popoutWake,
};
