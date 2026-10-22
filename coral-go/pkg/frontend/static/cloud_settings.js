/* Per-user settings on the Cloud Hub.
 *
 * The hub keeps each account's UI preferences (theme, defaults, ...) and nothing that belongs to a machine. It accepts
 * an allowlist of keys on PUT /api/settings and refuses the WHOLE request if one other key is present, so the UI sends
 * only these keys, and the settings dialog shows only the controls that map to them. Machine settings (working
 * directory, remote access, API key, CLI paths, telemetry, ...) are set on the machine in Coral. */
import { refreshSettingsGroups } from './settings_nav.js';

/** The keys the hub stores per user (keep in step with internal/usersettings allowedSettings). */
export const HUB_SETTING_KEYS = new Set([
    'theme', 'custom_theme', 'default_agent_type', 'default_permission_mode', 'default_renderer', 'git_diff_mode',
    'show_session_summary', 'show_session_server', 'fit_pane_width', 'refresh_files_on_switch', 'notify_needs_input', 'file_search_limit',
    'session_order', 'group_order', 'saved_personas', 'saved_team_templates', 'macros',
    'default_prompt_worker', 'default_prompt_orchestrator',
]);

/** Split a settings object into the keys the hub accepts and the ones it does not. */
export function filterSettingsPayload(obj) {
    const kept = {}, dropped = [];
    for (const [k, v] of Object.entries(obj || {})) {
        if (HUB_SETTING_KEYS.has(k)) kept[k] = v; else dropped.push(k);
    }
    return { kept, dropped };
}

/** Plain-language text for a hub refusal of a settings or theme write, or '' when it is not one we explain. */
export function settingsErrorMessage(status, body) {
    const err = body && typeof body === 'object' ? body.error : '';
    if (status === 413) return 'Your saved settings are too large. Remove some saved personas, templates or macros.';
    if (status !== 400) return '';
    if (err === 'unknown_setting') return 'That setting is kept on the machine, not on the hub. Change it in Coral on that machine.';
    if (err === 'invalid_value') return 'That value is not allowed' + (body.key ? ` for ${String(body.key).slice(0, 60)}` : '') + '.';
    return '';
}

// Dialog controls that map to hub settings, or are kept in this browser: everything else in the dialog is hidden.
const SHOWN_CONTROLS = new Set([
    'settings-theme', 'settings-renderer-select', 'settings-agent-type', 'settings-permission-mode',
    'settings-show-summary', 'settings-group-by-team', 'settings-fit-pane-width', 'settings-notify-needs-input', 'settings-desktop-notify',
    'settings-file-search-limit',
]);

/** Hide every row of the Settings dialog that is not a per-user hub setting, and say where the rest lives. A group left with no row leaves the group list. */
export function applyCloudSettingsDialog() {
    const modal = document.getElementById('settings-modal');
    const shell = modal && modal.querySelector('.settings-shell');
    if (!shell || shell.dataset.cloud === '1') return;
    shell.dataset.cloud = '1';
    for (const row of modal.querySelectorAll('.settings-row')) {
        const ids = Array.from(row.querySelectorAll('[id]')).map(c => c.id);
        if (!ids.some(id => SHOWN_CONTROLS.has(id))) row.hidden = true;
    }
    const about = document.getElementById('about-section');
    if (about) about.hidden = true;
    const note = document.createElement('p');
    note.className = 'settings-hint settings-cloud-note';
    note.textContent = ('Settings for this account are saved on the hub. Machine settings (working directory, remote access, API key, CLI paths, telemetry) are set on the machine in Coral.');
    // Inside the content column, not beside the shell: .settings-modal-body is a row, and a sibling there takes the shell's width (the dialog collapsed to a few pixels in hub mode).
    (shell.querySelector('.settings-panes') || shell).prepend(note);
    refreshSettingsGroups();
}
