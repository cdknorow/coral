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

export async function showDefaultPromptsModal() {
    const defaults = await _ensureDefaultPrompts();
    if (defaults.unavailable) {
        showToast("Default prompts are unavailable; reload and try again.", true);
        return;
    }
    const s = state.settings || {};
    const orchEl = document.getElementById("settings-prompt-orchestrator");
    const workerEl = document.getElementById("settings-prompt-worker");
    if (orchEl) orchEl.value = s.default_prompt_orchestrator || defaults.default_prompt_orchestrator;
    if (workerEl) workerEl.value = s.default_prompt_worker || defaults.default_prompt_worker;
    const teamOrchEl = document.getElementById("settings-team-reminder-orchestrator");
    const teamWorkerEl = document.getElementById("settings-team-reminder-worker");
    if (teamOrchEl) teamOrchEl.value = s.team_reminder_orchestrator || defaults.team_reminder_orchestrator;
    if (teamWorkerEl) teamWorkerEl.value = s.team_reminder_worker || defaults.team_reminder_worker;
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
    const orchValue = document.getElementById("settings-prompt-orchestrator")?.value || "";
    const workerValue = document.getElementById("settings-prompt-worker")?.value || "";
    const teamOrchValue = document.getElementById("settings-team-reminder-orchestrator")?.value || "";
    const teamWorkerValue = document.getElementById("settings-team-reminder-worker")?.value || "";

    // Save empty string when value matches default — so future default updates are picked up
    const payload = {
        default_prompt_orchestrator: orchValue === defaults.default_prompt_orchestrator ? "" : orchValue,
        default_prompt_worker: workerValue === defaults.default_prompt_worker ? "" : workerValue,
        team_reminder_orchestrator: teamOrchValue === defaults.team_reminder_orchestrator ? "" : teamOrchValue,
        team_reminder_worker: teamWorkerValue === defaults.team_reminder_worker ? "" : teamWorkerValue,
    };

    try {
        await fetch("/api/settings", {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
        });
        state.settings = { ...state.settings, ...payload };
        showToast("Default prompts saved");
    } catch (e) {
        showToast("Failed to save prompts", true);
    }

    hideDefaultPromptsModal();
}
