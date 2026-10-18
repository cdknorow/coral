/* Moved out of modals.js as it was (#4684): Saving the launch form as a persona, exporting and importing personas and team templates as JSON files, and the agent preset selector of the single-agent dialog. No behaviour change. */
import { importOnce } from './import_once.js';
import { showToast } from './utils.js';
const { launch } = await importOnce('./launch_state.js');
const { _effectiveServer, _settingsFor } = await importOnce('./launch_models.js');
const { AGENT_PRESETS } = await importOnce('./agent_presets.js');
const { _findPersona, _getSavedPersonas, _getSavedTeamTemplates, _renderPresetButtons, _upsertSavedPersonas, _upsertSavedTeamTemplates } = await importOnce('./persona_store.js');
const { getAgentConfig, setAgentConfig } = await importOnce('./agent_config_form.js');
const { _renderTeamTemplateSelector } = await importOnce('./team_dialogs.js');

async function _saveCurrentPersona(nameInputId, promptInputId, flagsInputId) {
    let name, prompt, flags, capabilities;
    if (nameInputId) {
        // Legacy path with explicit element IDs
        name = document.getElementById(nameInputId)?.value.trim() || '';
        prompt = document.getElementById(promptInputId)?.value.trim() || '';
        flags = flagsInputId ? document.getElementById(flagsInputId)?.value.trim() || '' : '';
        capabilities = null;
    } else {
        // ACF path — read from the launch agent config form
        const config = getAgentConfig('launch-agent-acf');
        name = document.getElementById("launch-agent-name-input")?.value.trim() || config.name;
        prompt = config.prompt;
        flags = config.flags;
        capabilities = config.capabilities;
    }
    if (!name) { showToast("Enter a name before saving", "error"); return; }
    if (AGENT_PRESETS.find(p => p.name === name)) { showToast("Can't overwrite a built-in preset", "error"); return; }

    const entry = { name, prompt, flags };
    if (capabilities) entry.capabilities = capabilities;
    try { await _upsertSavedPersonas([entry]); } catch { showToast("Could not save the persona", "error"); return; }
    showToast(`Saved persona "${name}"`);

    // Re-render all preset containers
    _renderPresetButtons("agent-preset-selector", "agent");
}
window._saveCurrentPersona = _saveCurrentPersona;

// ── Export / Import (Personas & Team Templates) ──────────────────────────

function _downloadJson(data, filename) {
    const blob = new Blob([JSON.stringify(data, null, 2)], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = filename;
    a.click();
    URL.revokeObjectURL(url);
}

function _openFilePicker() {
    return new Promise((resolve) => {
        const input = document.createElement("input");
        input.type = "file";
        input.accept = ".json";
        input.onchange = () => {
            const file = input.files[0];
            if (!file) { resolve(null); return; }
            const reader = new FileReader();
            reader.onload = () => {
                try { resolve(JSON.parse(reader.result)); }
                catch { showToast("Invalid JSON file", "error"); resolve(null); }
            };
            reader.readAsText(file);
        };
        input.click();
    });
}

export function exportPersonas() {
    const saved = _getSavedPersonas();
    if (saved.length === 0) { showToast("No saved personas to export", "error"); return; }
    _downloadJson({ version: 1, type: "coral-personas", personas: saved }, "coral-personas.json");
    showToast(`Exported ${saved.length} persona(s)`);
}

export async function importPersonas() {
    const data = await _openFilePicker();
    if (!data) return;
    const personas = data.personas || (Array.isArray(data) ? data : null);
    if (!personas || !Array.isArray(personas)) { showToast("Invalid persona file format", "error"); return; }

    const entries = [];
    for (const p of personas) {
        if (!p.name) continue;
        if (AGENT_PRESETS.find(bp => bp.name === p.name)) continue;
        entries.push({ name: p.name, prompt: p.prompt || "", flags: p.flags || "" });
    }
    const added = entries.length;
    try { await _upsertSavedPersonas(entries); } catch { showToast("Could not save the imported personas", "error"); return; }
    showToast(`Imported ${added} persona(s)`);
    _renderPresetButtons("agent-preset-selector", "agent");
}

export function exportTeamTemplates() {
    const templates = _getSavedTeamTemplates();
    if (templates.length === 0) { showToast("No saved templates to export", "error"); return; }
    _downloadJson({ version: 1, type: "coral-team-templates", templates }, "coral-team-templates.json");
    showToast(`Exported ${templates.length} template(s)`);
}

export async function importTeamTemplates() {
    const data = await _openFilePicker();
    if (!data) return;
    const templates = data.templates || (Array.isArray(data) ? data : null);
    if (!templates || !Array.isArray(templates)) { showToast("Invalid template file format", "error"); return; }

    const entries = templates.filter(t => t.name && Array.isArray(t.agents));
    const added = entries.length;
    try { await _upsertSavedTeamTemplates(entries); } catch { showToast("Could not save the imported templates", "error"); return; }
    showToast(`Imported ${added} template(s)`);
    _renderTeamTemplateSelector();
}

// ── Agent Preset Selector (single-agent modal) ───────────────────────────

function _initAgentPresets() {
    _renderPresetButtons("agent-preset-selector", "agent");
}

function _selectAgentPreset(name) {
    const persona = name ? _findPersona(name) : null;
    const nameInput = document.getElementById("launch-agent-name-input");
    const current = getAgentConfig('launch-agent-acf');
    if (persona) {
        setAgentConfig('launch-agent-acf', {
            name: persona.name,
            prompt: persona.prompt,
            flags: persona.flags || current.flags,
            agentType: persona.agentType || persona.agent_type || current.agentType,
            model: persona.model || current.model,
            capabilities: persona.capabilities,
        });
        if (nameInput) nameInput.value = persona.name;
    } else {
        setAgentConfig('launch-agent-acf', {
            agentType: _settingsFor(_effectiveServer(launch.server)).default_agent_type || current.agentType,
        });
        if (nameInput) nameInput.value = '';
    }
}

export { _initAgentPresets, _selectAgentPreset };
