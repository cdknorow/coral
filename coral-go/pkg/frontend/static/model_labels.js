/* The agent's model as people read it (#4066): "Claude · Opus 4.1" from the model id the server reports, using the display name from
 * the cached model list (model_picker.js keeps it) when there is one, else a short name worked out from the id. Pure, and small enough
 * for the agent list to import at startup. */
import { browserLocal } from './safe_storage.js';

const LOCAL_SERVER = 'local'; // server_base.js's LOCAL_SERVER; not imported so this file stays pure

const TYPE_NAMES = { claude: 'Claude', codex: 'Codex', gemini: 'Gemini', agy: 'Antigravity', antigravity: 'Antigravity', pi: 'Pi' };

const MODELS_CACHE_PREFIX = 'coral-models:v1:';

function typeName(agentType) {
    return TYPE_NAMES[agentType] || (agentType ? agentType.charAt(0).toUpperCase() + agentType.slice(1) : '');
}

/** The cached model list for a server and agent type ({models, fetched_at, source}), or null. */
export function readModelsCache(server, agentType) {
    try {
        const raw = browserLocal.get(`${MODELS_CACHE_PREFIX}${server || LOCAL_SERVER}:${agentType}`);
        const v = raw ? JSON.parse(raw) : null;
        return v && Array.isArray(v.models) ? v : null;
    } catch { return null; }
}

export function writeModelsCache(server, agentType, data) {
    try { browserLocal.set(`${MODELS_CACHE_PREFIX}${server || LOCAL_SERVER}:${agentType}`, JSON.stringify({ models: data.models, fetched_at: data.fetched_at || null, source: data.source || '' })); } catch { /* per-viewer only */ }
}

/** Forgets every cached model list (a save of the custom models changes what every picker shows, #5480). */
export function clearModelsCache() {
    try { for (const k of browserLocal.keys()) if (k.startsWith(MODELS_CACHE_PREFIX)) browserLocal.remove(k); } catch { /* storage unavailable: nothing was cached */ }
}

/** A short name from a model id when the list has no display name: claude-opus-4-1-20250805 gives "Opus 4.1". */
function shortModelName(id) {
    let s = String(id || '').trim();
    if (!s) return '';
    s = s.replace(/^claude-/, '').replace(/-\d{8}$/, '').replace(/\[[^\]]*\]$/, '');
    const m = /^(opus|sonnet|haiku)-(\d+)(?:-(\d+))?(?:-.*)?$/i.exec(s) || /^(\d+)(?:-(\d+))?-(opus|sonnet|haiku)$/i.exec(s);
    if (m) {
        const named = /^\d/.test(m[1]);
        const family = named ? m[3] : m[1], major = named ? m[1] : m[2], minor = named ? m[2] : m[3];
        return `${family.charAt(0).toUpperCase()}${family.slice(1).toLowerCase()} ${major}${minor ? '.' + minor : ''}`;
    }
    return s;
}

/** "Claude · Opus 4.1", or '' when the agent has no model on record. */
export function modelLabel(agentType, modelId, server) {
    if (!modelId || agentType === 'terminal') return '';
    const cached = readModelsCache(server, agentType);
    const hit = cached && cached.models.find(m => m && m.value === modelId);
    const name = (hit && hit.display_name) || shortModelName(modelId);
    return `${typeName(agentType)} · ${name}`;
}
