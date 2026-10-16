/* Moved out of modals.js as it was (#3836): no behaviour change. */
import { serverFetch, currentServer, isHub, isCloud, normServer, LOCAL_SERVER } from './server_base.js';
import { escapeAttr } from './utils.js';
import { launch } from './launch_state.js';
import { state } from './state.js';

export function _effectiveServer(server) {
    return isHub() ? normServer(server) : LOCAL_SERVER;
}

// Per-server option sources. Everything keyed by server so switching the
// dropdown never shows another machine's models, defaults or CLI state.
//   settings    GET /api/settings        defaults: working dir, agent type, permission mode, models (per server)
//   models      GET /api/agent-models    model catalog (per server: CLI versions differ)
export const _serverSettings = new Map();      // remote id -> settings object (local uses state.settings)
const _agentModelsByServer = new Map(); // server -> Promise<{type:[models]}>
const _defaultModelsByServer = new Map();

/** Settings that apply to a launch on `server` (the hub's own for local). */
export function _settingsFor(server) {
    const id = normServer(server);
    if (id === LOCAL_SERVER) return state.settings || {};
    return _serverSettings.get(id) || {};
}

/** Fetch (and cache) a remote's settings. Throws when the remote cannot be reached. */
export async function _loadServerSettings(server, { fresh = false } = {}) {
    const id = normServer(server);
    if (id === LOCAL_SERVER) return state.settings || {};
    if (!fresh && _serverSettings.has(id)) return _serverSettings.get(id);
    const resp = await serverFetch(id, '/api/settings');
    // Through the hub /api/settings is not exposed at all (a machine's settings are never readable
    // remotely): use the defaults quietly instead of reporting an error on every launch.
    if (resp.status === 404 && isCloud()) { _serverSettings.set(id, {}); return {}; }
    const data = await resp.json().catch(() => null);
    if (!resp.ok || !data) throw new Error((data && data.error) || `HTTP ${resp.status}`);
    const s = data.settings || {};
    _serverSettings.set(id, s);
    _defaultModelsByServer.delete(id);
    return s;
}

/** Server an Agent Config Form container launches on. */
export function _acfServer(container) {
    const id = container && container.id;
    if (id === 'add-agent-board-acf') return _effectiveServer(launch.addAgentServer);
    if (id === 'restart-acf') return _effectiveServer(currentServer());
    return _effectiveServer(launch.server);
}

// ── Canonical per-agent-type model list ────────────────────────────────
// Fetched once per server from GET /api/agent-models and cached for the tab lifetime.
// Shape: { claude: [...], codex: [], gemini: [], terminal: [] }
export function _getAgentModels(server = LOCAL_SERVER) {
    const id = normServer(server);
    if (!_agentModelsByServer.has(id)) {
        _agentModelsByServer.set(id, serverFetch(id, '/api/agent-models')
            .then(r => r.ok ? r.json() : {})
            .catch(() => ({})));
    }
    return _agentModelsByServer.get(id);
}
(window.__coral ||= {})._getAgentModels = _getAgentModels;

// _fillModelDatalist replaces <option>s in the given <datalist> element with
// the canonical list for the agent type. Unknown types or an empty list
// result in an empty datalist (the input still accepts free-form text).
export function _fillModelDatalist(datalistEl, agentType, modelsByType) {
    if (!datalistEl) return;
    const list = (modelsByType && modelsByType[agentType]) || [];
    datalistEl.innerHTML = list
        .map(m => `<option value="${escapeAttr(m)}"></option>`)
        .join('');
}

// ── User's per-type default model (for ACF pre-fill) ───────────────────
// Pulled from the same /api/settings payload the Settings modal uses (the
// target server's own settings for a remote), cached per server with its own
// invalidation (triggered after a Settings save so the next form open sees
// the new default).
// Shape: { claude: "<id>", codex: "<id>", gemini: "<id>" }
function _modelDefaultsFrom(s) {
    const agyModel = (s.default_model_agy || s.default_model_gemini || '').trim();
    return {
        claude: (s.default_model_claude || '').trim(),
        codex: (s.default_model_codex || '').trim(),
        agy: agyModel,
        antigravity: agyModel,
        gemini: (s.default_model_gemini || s.default_model_agy || '').trim(),
        pi: (s.default_model_pi || '').trim(),
    };
}
const _NO_MODEL_DEFAULTS = { claude: '', codex: '', agy: '', antigravity: '', gemini: '', pi: '' };
export function _getDefaultModels(server = LOCAL_SERVER) {
    const id = normServer(server);
    if (!_defaultModelsByServer.has(id)) {
        const p = id === LOCAL_SERVER
            ? fetch('/api/settings')
                .then(r => r.ok ? r.json() : { settings: {} })
                .then(data => _modelDefaultsFrom(data.settings || {}))
            : _loadServerSettings(id).then(_modelDefaultsFrom);
        _defaultModelsByServer.set(id, p.catch(() => {
            _defaultModelsByServer.delete(id);
            return { ..._NO_MODEL_DEFAULTS };
        }));
    }
    return _defaultModelsByServer.get(id);
}
export function _invalidateDefaultModels() {
    _defaultModelsByServer.clear();
    _serverSettings.clear();
}
(window.__coral ||= {})._invalidateDefaultModels = _invalidateDefaultModels;
