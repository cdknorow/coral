/* Readiness signals for tests and tools (#3118). The app reports milestones;
 * nothing here polls or changes behaviour.
 *
 *   window.__coralReady                    a Promise: resolves once the first live list
 *                                          has rendered, the page start's live-list answers
 *                                          have been applied ('liveListLoaded'), the feed
 *                                          connected, and the first history list and
 *                                          settings loaded (each loaded or failed). In a
 *                                          popout, history counts as done.
 *   window.__coralReady.liveListRendered() the next live-list render after the call
 *                                          (resolves with the row count).
 *   window.__coralReady.startup(...parts)  just those parts ('liveList', 'liveListLoaded',
 *                                          'feed', 'history', 'settings'), for a suite that
 *                                          stubs one out.
 *
 * 'liveList' is the first render of the agent list, which the feed's first update can also
 * cause; 'liveListLoaded' is the page start's HTTP list answers applied (or failed). A test
 * that injects state (a hub, its servers, a fake agent) waits for 'liveListLoaded', so no
 * late list answer lands on top of what it set up.
 *   window.__coralReady.parts              read-only snapshot of which parts are done.
 *   window.__coralReady.agentSelected(id)  the current agent is `id` (its name or its
 *                                          session_id) and its view has settled: the
 *                                          chat's first page rendered (or failed), or the
 *                                          terminal attached. Resolves at once when that
 *                                          is already true. Names need not be unique:
 *                                          pass the session_id to be exact.
 *
 * Matching window events: 'coral:ready', 'coral:live-list-rendered' {count},
 * 'coral:agent-view-ready' {name, sessionId, mode}. */

const _parts = { liveList: false, liveListLoaded: false, feed: false, history: false, settings: false };
let _resolveReady;
const ready = new Promise(r => { _resolveReady = r; });
let _isReady = false;

const _partWaiters = []; // { parts, resolve }

const _liveWaiters = [];
let _view = { name: null, sessionId: null, mode: null, settled: false };
const _isView = id => id != null && (id === _view.name || id === _view.sessionId);
const _agentWaiters = [];

function emit(type, detail) {
    try { window.dispatchEvent(new CustomEvent(type, { detail })); } catch { /* never break the app */ }
}

/** Mark one startup part done ('liveList' | 'liveListLoaded' | 'feed' | 'history' | 'settings'). */
export function markReady(part) {
    if (!(part in _parts) || _parts[part]) return;
    _parts[part] = true;
    for (let i = _partWaiters.length - 1; i >= 0; i--) {
        if (_partWaiters[i].parts.every(p => _parts[p])) _partWaiters.splice(i, 1)[0].resolve();
    }
    if (!_isReady && Object.values(_parts).every(Boolean)) {
        _isReady = true;
        _resolveReady();
        emit('coral:ready', {});
    }
}

/** The agent list was rendered with `count` rows. */
export function liveListRendered(count) {
    markReady('liveList');
    _liveWaiters.splice(0).forEach(r => r(count));
    emit('coral:live-list-rendered', { count });
}

/** A live agent is being selected: its view is not settled yet. */
export function agentSwitchStarted(name, sessionId = null) {
    _view = { name, sessionId, mode: null, settled: false };
}

/** The selected agent's view settled ('chat' or 'terminal'). Ignored if another agent was selected meanwhile. */
export function agentViewReady(name, mode, sessionId = null) {
    if (_view.name !== name || _view.sessionId !== sessionId || _view.settled) return;
    _view = { name, sessionId, mode, settled: true };
    const detail = { name, sessionId, mode };
    for (let i = _agentWaiters.length - 1; i >= 0; i--) {
        if (_isView(_agentWaiters[i].id)) _agentWaiters.splice(i, 1)[0].resolve(detail);
    }
    emit('coral:agent-view-ready', detail);
}

ready.liveListRendered = () => new Promise(r => _liveWaiters.push(r));
ready.agentSelected = (id) => (_view.settled && _isView(id))
    ? Promise.resolve({ name: _view.name, sessionId: _view.sessionId, mode: _view.mode })
    : new Promise(resolve => _agentWaiters.push({ id, resolve }));
ready.startup = (...parts) => {
    const want = parts.length ? parts : Object.keys(_parts);
    const unknown = want.filter(p => !(p in _parts));
    if (unknown.length) return Promise.reject(new Error(`unknown startup part: ${unknown.join(', ')}`));
    return want.every(p => _parts[p]) ? Promise.resolve() : new Promise(resolve => _partWaiters.push({ parts: want, resolve }));
};
Object.defineProperty(ready, 'parts', { get: () => ({ ..._parts }), enumerable: true });

window.__coralReady = ready;
