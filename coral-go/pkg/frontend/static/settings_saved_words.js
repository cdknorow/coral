/* What the Save toast says (#7041): the rows that changed, by the name the dialog shows, instead of "Settings saved" for every change. A switch says its new state, a field only its name; three
 * rows are named and the rest counted. One row that is several settings (the theme pair, the CLI paths, the default models, the cleanup) is named once. A key this table does not know is left out:
 * with nothing known to name, the toast keeps its plain words. */

// payload key -> [the row's name, kind]; a 'switch' row says on or off.
const ROWS = {
    theme: ['Theme'], custom_theme: ['Theme'],
    default_renderer: ['Render engine'], default_agent_type: ['Default agent type'], default_permission_mode: ['Default permission mode'], default_working_dir: ['Default working directory'],
    default_thinking_claude: ['Default thinking'],
    default_model_claude: ['Default models'], default_model_codex: ['Default models'], default_model_agy: ['Default models'], default_model_gemini: ['Default models'], default_model_pi: ['Default models'],
    cli_path_claude: ['CLI paths'], cli_path_codex: ['CLI paths'], cli_path_agy: ['CLI paths'], cli_path_gemini: ['CLI paths'], cli_path_pi: ['CLI paths'],
    fit_pane_width: ['Fit pane width', 'switch'], notify_needs_input: ['Notifications', 'switch'], show_scrollbars: ['Show scrollbars', 'switch'],
    terminal_font_size: ['Terminal font size'], terminal_scrollback: ['Terminal scrollback'],
    file_search_mode: ['File search mode'], file_search_limit: ['File search limit'], git_diff_mode: ['Git diff mode'], git_poll_interval_s: ['Git scan interval'],
    agent_priority: ['Run agents at lower priority', 'low'], telemetry_enabled: ['Usage analytics', 'switch'], board_auto_attach: ['Attach files linked in posts', 'switch'],
    nudge_claude_artifacts: ['Coral artifact reminders', 'switch'], board_health_monitor: ['Team health checks', 'switch'], auto_resume_after_api_error: ['Resume after API errors', 'switch'],
    auto_goals: ["Name each agent's goal", 'switch'], debug_input_log: ['Log terminal input', 'switch'], remote_access_enabled: ['Remote access', 'switch'],
    artifact_store_max_bytes: ['Artifact store limit'], received_artifacts_max_bytes: ['Received files limit'],
    artifact_retention_days: ['Automatic cleanup'], artifact_retention_kinds: ['Automatic cleanup'],
};
const on = v => v === true || String(v).toLowerCase() === 'true';

/** The toast's text for a saved payload. opts.extra names rows that were saved apart (custom models, update choices); opts.restart says a saved row applies only when Coral restarts. */
export function savedWords(payload, { extra = [], restart = false } = {}) {
    const names = [];
    const add = n => { if (n && !names.includes(n)) names.push(n); };
    for (const [key, value] of Object.entries(payload || {})) {
        const row = ROWS[key];
        if (!row) continue;
        const [name, kind] = row;
        add(kind === 'switch' ? `${name} ${on(value) ? 'on' : 'off'}` : kind === 'low' ? `${name} ${value === 'low' ? 'on' : 'off'}` : name);
    }
    for (const e of extra) add(e);
    if (!names.length) return 'Settings saved';
    const shown = names.slice(0, 3).join(', ');
    const more = names.length - 3;
    const text = `Saved: ${shown}${more > 0 ? ` and ${more} more` : ''}`;
    return restart && 'remote_access_enabled' in (payload || {}) ? `${text}. Applies when Coral restarts.` : text;
}
