/* Same-browser interactive ownership for one live session.
 *
 * Several tabs (dashboard + popouts) may stream the same terminal. Only ONE
 * of them — the interactive owner — sends keyboard input, xterm resizes and
 * the capture pane-width sync, so two windows with different widths never
 * fight over the shared tmux pane. Ownership is coordinated over a
 * BroadcastChannel keyed by session id, which only spans one browser profile;
 * a different browser or the native app is a separate world (documented v1
 * limitation — exact server-side targeting still prevents cross-session
 * effects there).
 *
 * Rules: a tab that joins and hears no owner within OWNER_PING_MS becomes the
 * owner; otherwise it is a viewer. Any keystroke/click in a viewer claims
 * ownership. Ownership is released on pagehide. */

const TAB_ID = (typeof crypto !== 'undefined' && crypto.randomUUID)
    ? crypto.randomUUID()
    : String(Date.now()) + Math.random().toString(16).slice(2);
const OWNER_PING_MS = 200;

let _channel = null;
let _sessionId = null;
let _ownerId = null;        // TAB_ID | other tab id | null (nobody has claimed)
let _pingTimer = null;
const _listeners = new Set();

function _emit() {
    const owner = isInteractiveOwner();
    for (const cb of _listeners) {
        try { cb(owner); } catch (e) { console.error('ownership listener failed', e); }
    }
    try {
        document.dispatchEvent(new CustomEvent('coral:ownership-changed', { detail: { owner } }));
    } catch {}
}

function _post(msg) {
    if (!_channel) return;
    try { _channel.postMessage({ ...msg, from: TAB_ID, sessionId: _sessionId }); } catch {}
}

function _setOwner(id) {
    if (_ownerId === id) return;
    _ownerId = id;
    _emit();
}

/** Join the ownership group for a session (leaving any previous one). */
export function joinSessionOwnership(sessionId) {
    if (!sessionId) { leaveSessionOwnership(); return; }
    if (_sessionId === sessionId && _channel) return;
    leaveSessionOwnership();
    _sessionId = sessionId;
    if (typeof BroadcastChannel === 'undefined') {
        _ownerId = TAB_ID;
        _emit();
        return;
    }
    try {
        _channel = new BroadcastChannel(`coral-agent-${sessionId}`);
    } catch {
        _ownerId = TAB_ID;
        _emit();
        return;
    }
    _channel.onmessage = (ev) => {
        const m = ev.data || {};
        if (m.sessionId !== _sessionId || m.from === TAB_ID) return;
        switch (m.type) {
            case 'ping':
                if (_ownerId === TAB_ID) _post({ type: 'owner' });
                break;
            case 'owner':
            case 'claim':
                if (_pingTimer) { clearTimeout(_pingTimer); _pingTimer = null; }
                _setOwner(m.from);
                break;
            case 'release':
                // No automatic reclaim: the remaining window stays a viewer
                // until the user clicks or types in it (explicit claim).
                if (_ownerId === m.from) _setOwner(null);
                break;
        }
    };
    _ownerId = null;
    _post({ type: 'ping' });
    _pingTimer = setTimeout(() => {
        _pingTimer = null;
        if (_ownerId === null) {
            _setOwner(TAB_ID);
            _post({ type: 'claim' });
        }
    }, OWNER_PING_MS);
}

export function leaveSessionOwnership() {
    if (_pingTimer) { clearTimeout(_pingTimer); _pingTimer = null; }
    if (_ownerId === TAB_ID) _post({ type: 'release' });
    if (_channel) { try { _channel.close(); } catch {} }
    _channel = null;
    _sessionId = null;
    _ownerId = null;
    _emit();
}

/** True when this tab may send input/resize for the joined session
 *  (also true when no session is joined, so non-session code paths behave as before). */
export function isInteractiveOwner() {
    if (!_sessionId) return true;
    return _ownerId === TAB_ID;
}

export function ownershipSessionId() { return _sessionId; }

/** Take control of the joined session (idempotent). Returns true when owner. */
export function claimOwnership() {
    if (!_sessionId) return true;
    if (_ownerId !== TAB_ID) {
        if (_pingTimer) { clearTimeout(_pingTimer); _pingTimer = null; }
        _setOwner(TAB_ID);
        _post({ type: 'claim' });
    }
    return true;
}

export function releaseOwnership() {
    if (_sessionId && _ownerId === TAB_ID) {
        _setOwner(null);
        _post({ type: 'release' });
    }
}

export function onOwnershipChange(cb) {
    _listeners.add(cb);
    return () => _listeners.delete(cb);
}

window.addEventListener('pagehide', () => {
    if (_sessionId && _ownerId === TAB_ID) _post({ type: 'release' });
});
