/* Moved out of modals.js as it was (#3836): the Default Prompts dialog loads on its own the first time it opens. */
import { state } from './state.js';
import { showToast } from './utils.js';

// ── Default Prompts Modal ──────────────────────────────────────────

// Cached default prompts fetched from the backend API.
// Populated on first open of the modal; used for "Reset to Default".
let _defaultPrompts = null;

async function _ensureDefaultPrompts() {
    if (_defaultPrompts) return _defaultPrompts;
    try {
        const res = await fetch("/api/settings/default-prompts");
        _defaultPrompts = await res.json();
    } catch {
        // Do not keep a second, abbreviated prompt contract here. The runtime
        // defaults remain authoritative; an unavailable endpoint is explicit.
        _defaultPrompts = {
            unavailable: true,
            default_prompt_orchestrator: '',
            default_prompt_worker: '',
            team_reminder_orchestrator: '',
            team_reminder_worker: '',
        };
    }
    return _defaultPrompts;
}

// The four prompts, by setting key, and the field each one is edited in.
const PROMPT_FIELDS = {
    default_prompt_orchestrator: "settings-prompt-orchestrator",
    default_prompt_worker: "settings-prompt-worker",
    team_reminder_orchestrator: "settings-team-reminder-orchestrator",
    team_reminder_worker: "settings-team-reminder-worker",
};

// What each field held when the dialog opened: Save writes only the prompts the person changed (#4423), so saving
// one never puts back the other three as this page last saw them.
let _openedPrompts = null;

export async function showDefaultPromptsModal() {
    const defaults = await _ensureDefaultPrompts();
    if (defaults.unavailable) {
        showToast("Default prompts are unavailable; reload and try again.", true);
        return;
    }
    // The prompts as the server has them now, not as this page loaded them (another tab may have saved one).
    try {
        const res = await fetch("/api/settings");
        if (res.ok) {
            const fresh = (await res.json()).settings || {};
            const update = {};
            for (const key of Object.keys(PROMPT_FIELDS)) update[key] = fresh[key] || "";
            state.settings = { ...state.settings, ...update };
        }
    } catch { /* the dialog opens with the prompts this page has */ }
    const s = state.settings || {};
    const orchEl = document.getElementById("settings-prompt-orchestrator");
    const workerEl = document.getElementById("settings-prompt-worker");
    if (orchEl) orchEl.value = s.default_prompt_orchestrator || defaults.default_prompt_orchestrator;
    if (workerEl) workerEl.value = s.default_prompt_worker || defaults.default_prompt_worker;
    const teamOrchEl = document.getElementById("settings-team-reminder-orchestrator");
    const teamWorkerEl = document.getElementById("settings-team-reminder-worker");
    if (teamOrchEl) teamOrchEl.value = s.team_reminder_orchestrator || defaults.team_reminder_orchestrator;
    if (teamWorkerEl) teamWorkerEl.value = s.team_reminder_worker || defaults.team_reminder_worker;
    _openedPrompts = {};
    for (const [key, id] of Object.entries(PROMPT_FIELDS)) _openedPrompts[key] = document.getElementById(id)?.value ?? "";
    document.getElementById("default-prompts-modal").style.display = "flex";
}

export function hideDefaultPromptsModal() {
    document.getElementById("default-prompts-modal").style.display = "none";
}

export async function resetDefaultPrompt(type) {
    const defaults = await _ensureDefaultPrompts();
    const mapping = {
        orchestrator: ["settings-prompt-orchestrator", "default_prompt_orchestrator"],
        worker: ["settings-prompt-worker", "default_prompt_worker"],
        team_orchestrator: ["settings-team-reminder-orchestrator", "team_reminder_orchestrator"],
        team_worker: ["settings-team-reminder-worker", "team_reminder_worker"],
    };
    const entry = mapping[type];
    if (entry) {
        const el = document.getElementById(entry[0]);
        if (el) el.value = defaults[entry[1]];
    }
}

export async function saveDefaultPrompts() {
    const defaults = await _ensureDefaultPrompts();
    // Only the prompts the person changed since the dialog opened. Save an empty string when the value matches the
    // default, so future default updates are picked up.
    const payload = {};
    for (const [key, id] of Object.entries(PROMPT_FIELDS)) {
        const value = document.getElementById(id)?.value || "";
        if (_openedPrompts && value === _openedPrompts[key]) continue;
        payload[key] = value === defaults[key] ? "" : value;
    }

    try {
        if (Object.keys(payload).length > 0) {
            const res = await fetch("/api/settings", {
                method: "PUT",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify(payload),
            });
            if (!res.ok) throw new Error(`save failed (${res.status})`);
            state.settings = { ...state.settings, ...payload };
            if (_openedPrompts) for (const key of Object.keys(payload)) _openedPrompts[key] = document.getElementById(PROMPT_FIELDS[key])?.value ?? "";
        }
        showToast("Default prompts saved");
    } catch (e) {
        showToast("Failed to save prompts", true);
        return; // the dialog stays open with what was typed
    }

    hideDefaultPromptsModal();
}
