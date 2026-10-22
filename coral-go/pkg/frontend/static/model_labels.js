/* The agent's model as people read it (#4066): "Claude · Opus 4.1" from the model id the server reports, using the display name from
 * the cached model list (model_picker.js keeps it) when there is one, else a short name worked out from the id. Pure, and small enough
 * for the agent list to import at startup. */
import { browserLocal } from './safe_storage.js';

const LOCAL_SERVER = 'local'; // server_base.js's LOCAL_SERVER; not imported so this file stays pure

const TYPE_NAMES = { claude: 'Claude', codex: 'Codex', gemini: 'Gemini', agy: 'Antigravity', antigravity: 'Antigravity', pi: 'Pi' };

const MODELS_CACHE_PREFIX = 'coral-models:v1:';

export function typeName(agentType) {
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

/** The model's name as the list shows it ("Opus 4.1"), or '' when the agent has no model on record or is a terminal. */
export function modelName(agentType, modelId, server) {
    if (!modelId || agentType === 'terminal') return '';
    const cached = readModelsCache(server, agentType);
    const hit = cached && cached.models.find(m => m && m.value === modelId);
    return (hit && hit.display_name) || shortModelName(modelId);
}

/** "Claude · Opus 4.1", or '' when the agent has no model on record. */
export function modelLabel(agentType, modelId, server) {
    const name = modelName(agentType, modelId, server);
    return name ? `${typeName(agentType)} · ${name}` : '';
}

/** The provider's name for an agent type ("Claude"), '' for a terminal (it has none). */
export function providerName(agentType) { return agentType === 'terminal' ? '' : typeName(agentType); }

export const LEVEL_NAMES = { low: 'Low', medium: 'Medium', high: 'High', xhigh: 'Extra high', max: 'Max' };
export const levelName = l => LEVEL_NAMES[l] || l;

/** The thinking (effort) level a row has on record: { name, requested } or null. The level the newest turn ran at (thinking_actual, from the transcript) when there is one, else the level the
 *  agent was started with (thinking), marked requested: the same two fields and the same order as the chat bar's thinking chip. Nothing recorded is null, never a word for it. */
export function sessionLevel(s) {
    if (s && s.thinking_actual) return { name: levelName(s.thinking_actual), requested: false };
    if (s && s.thinking) return { name: levelName(s.thinking), requested: true };
    return null;
}
