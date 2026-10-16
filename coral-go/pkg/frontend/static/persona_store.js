/* Moved out of modals.js as it was (#3844): no behaviour change. */
import { AGENT_PRESETS } from './agent_presets.js';
import { escapeHtml, escapeAttr } from './utils.js';
import { state } from './state.js';

// ── Saved Personas & Team Templates ──────────────────────────────────────
export function _getSavedPersonas() {
    try { return JSON.parse(state.settings.saved_personas || "[]"); }
    catch { return []; }
}

export async function _setSavedPersonas(personas) {
    state.settings.saved_personas = JSON.stringify(personas);
    await fetch("/api/settings", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ saved_personas: state.settings.saved_personas }),
    });
}

export function _getSavedTeamTemplates() {
    try { return JSON.parse(state.settings.saved_team_templates || "[]"); }
    catch { return []; }
}

export async function _setSavedTeamTemplates(templates) {
    state.settings.saved_team_templates = JSON.stringify(templates);
    await fetch("/api/settings", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ saved_team_templates: state.settings.saved_team_templates }),
    });
}

// What a preset select's choice does, by the handler a caller names (#3645).
const PRESET_KINDS = new Set(['agent', 'acf']);

/** Render preset buttons (built-in + saved) into a container. */
export function _renderPresetButtons(containerId, kind, opts = {}) {
    const container = document.getElementById(containerId);
    if (!container) return;
    const saved = _getSavedPersonas();

    const target = opts.target;
    // The select names what a choice does (preset-pick, by kind): the agent dialog's preset, or an
    // agent config form's (with its target), never a function name in the markup.
    if (!PRESET_KINDS.has(kind)) throw new Error('unknown preset kind ' + kind);
    let html = `<select class="agent-preset-select" data-on-change="preset-pick" data-kind="${escapeAttr(kind)}" data-target="${escapeAttr(target || '')}">`;
    html += `<option value="" disabled selected>Choose a preset or custom...</option>`;
    html += `<optgroup label="Built-in">`;
    for (const preset of AGENT_PRESETS) {
        html += `<option value="${escapeAttr(preset.name)}">${escapeHtml(preset.name)}</option>`;
    }
    html += `</optgroup>`;
    if (saved.length > 0) {
        html += `<optgroup label="Saved Personas">`;
        for (const p of saved) {
            html += `<option value="${escapeAttr(p.name)}">${escapeHtml(p.name)}</option>`;
        }
        html += `</optgroup>`;
    }
    html += `<option value="">Custom</option>`;
    html += `</select>`;
    container.innerHTML = html;
}

/** Find a persona by name in built-in + saved lists. */
export function _findPersona(name) {
    return AGENT_PRESETS.find(p => p.name === name) || _getSavedPersonas().find(p => p.name === name);
}
