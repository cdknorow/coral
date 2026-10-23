/* Settings and theme, loaded with the page (#3140). loadSettings() runs at
 * startup and applies the theme; the theme follows the OS in "system" mode.
 * Everything else of
 * the settings, launch and team dialogs (modals.js) loads the first time one
 * opens. */

import { state } from './state.js';
import { fetchSystemStatus } from './system_status.js';
import { updateTerminalTheme } from './xterm_renderer.js';
import { browserLocal } from './safe_storage.js';
import { onBuilt } from './lazy_dom.js';
import { isCloud } from './server_base.js';
// What Save sends: only the fields the person changed (here so the module is loaded at startup, import_hygiene).
export { changedSettings } from './settings_diff.js';

export function settingBool(value, fallback = true) {
    if (typeof value === 'boolean') return value;
    if (typeof value === 'string') return !['false', '0', 'off', 'disabled'].includes(value.trim().toLowerCase());
    return fallback;
}

export function renderRemoteAccessStatus(status, loadError = false) {
    const el = document.getElementById('settings-remote-access-status');
    if (!el) return;
    if (loadError) {
        el.textContent = 'Could not read the running access state. The saved value was not changed.';
        el.dataset.state = 'error';
        return;
    }
    const saved = settingBool(status?.remote_access_enabled, false);
    const effective = settingBool(status?.remote_access_effective, false);
    const restart = status?.remote_access_restart_required === true || saved !== effective;
    if (restart) {
        el.textContent = `Saved: ${saved ? 'on' : 'off'}. Running: ${effective ? 'on' : 'local only'}. Restart to apply.`;
    } else {
        el.textContent = ''; // the switch already says it; only a mismatch or a risk is worth a line
    }
    // Remote access without TLS is plain HTTP (#3355): say so wherever it is, or will be, on.
    if ((effective || saved) && status?.remote_access_tls !== true) {
        el.textContent = `${el.textContent} Not encrypted: the API key, login and terminal traffic cross the network as plain HTTP. Use Coral Hub, a TLS proxy, or --tls-cert and --tls-key.`.trim();
        el.dataset.unencrypted = 'true';
    } else {
        delete el.dataset.unencrypted;
        if (effective && status?.remote_access_tls === true) el.textContent = `${el.textContent} Encrypted (HTTPS).`.trim();
    }
    el.dataset.state = restart ? 'restart-required' : 'current';
}

// The terminal input debug log (#3256): the toggle shows the saved setting, and a
// visible note says when the log is on (it records typed text, which may include
// secrets). CORAL_DEBUG_INPUT / --debug-input force it on; the toggle can't
// turn that off, so it is shown checked and disabled.
// The agent tools folder (H5): shown only when an account other than the system and administrators
// can change it (a portable Coral in the user's profile), since every agent runs the hooks in it.
export function renderToolsFolderStatus(status) {
    const row = document.getElementById('settings-tools-folder-row');
    const el = document.getElementById('settings-tools-folder-status');
    if (!row || !el) return;
    const tools = status?.tools_folder || {};
    const who = Array.isArray(tools.writable_by) ? tools.writable_by : [];
    if (!tools.path || who.length === 0) {
        row.hidden = true;
        el.textContent = '';
        return;
    }
    // One line up front (what is wrong, and a way to the fix), the whole explanation behind "How to fix it" (#6162's follow-up: the Terminal group was 100 words on a machine where this shows).
    const line = document.createElement('span');
    line.textContent = 'Your agent tools folder can be changed without administrator rights. ';
    const more = document.createElement('button');
    more.type = 'button';
    more.className = 'tb-learn';
    more.id = 'settings-tools-folder-more';
    more.textContent = 'How to fix it';
    more.setAttribute('aria-expanded', 'false');
    more.setAttribute('aria-controls', 'settings-tools-folder-detail');
    const detail = document.createElement('div');
    detail.id = 'settings-tools-folder-detail';
    detail.hidden = true;
    detail.textContent = `${tools.path} can be changed by ${who.join(', ')}, not only by an administrator. A program running as one of those accounts could replace the tools every agent runs. To fix it, install Coral with the installer: it goes in Program Files, where only an administrator can change it.`;
    more.addEventListener('click', () => {
        detail.hidden = !detail.hidden;
        more.setAttribute('aria-expanded', String(!detail.hidden));
        more.textContent = detail.hidden ? 'How to fix it' : 'Show less';
    });
    el.replaceChildren(line, more, detail);
    row.hidden = false;
}

export function renderDebugInputLogStatus(status) {
    const box = document.getElementById('settings-debug-input-log');
    const el = document.getElementById('settings-debug-input-log-status');
    if (!box || !el) return;
    const log = status?.debug_input_log || {};
    box.disabled = log.forced === true;
    if (log.forced === true) box.checked = true;
    const del = document.getElementById('settings-debug-input-log-delete');
    const file = log.path || 'input-debug.log';
    if (log.enabled === true) {
        const via = log.forced ? ' (turned on by CORAL_DEBUG_INPUT or --debug-input)' : '';
        // #60: redacted, and a person's own typing kept as its length only, but not empty of
        // sensitive text: say so.
        el.textContent = `On${via}: what Coral sends to agent terminals is written to ${file}, with the secrets Coral recognises removed and your own typing kept as its length only. It can still hold board messages and other text sent to agents, and secrets the redaction misses. Turn it off when you are done; it is deleted after 7 days without a write.`;
        el.dataset.state = 'on';
        el.hidden = false;
    } else if (log.file_exists === true) {
        // Turning it off keeps the file, which may hold secrets: say so, and offer to delete it.
        el.textContent = `Off. ${file} is kept (its older copy was removed): it can hold text sent to agents, so delete it when you no longer need it. It is deleted after 7 days without a write.`;
        el.dataset.state = 'kept';
        el.hidden = false;
    } else {
        el.textContent = '';
        el.dataset.state = 'off';
        el.hidden = true;
    }
    if (del) {
        del.hidden = !(log.enabled !== true && log.file_exists === true);
        del.onclick = deleteDebugInputLog;
    }
}

// deleteDebugInputLog deletes the kept log (only from this machine; the server refuses otherwise).
export async function deleteDebugInputLog() {
    const el = document.getElementById('settings-debug-input-log-status');
    try {
        const resp = await fetch('/api/system/debug-input-log', { method: 'DELETE' });
        const body = await resp.json().catch(() => ({}));
        if (!resp.ok) throw new Error(body.error || `delete failed (${resp.status})`);
        if (state.privacyStatus) state.privacyStatus.debug_input_log = body.debug_input_log;
        renderDebugInputLogStatus({ debug_input_log: body.debug_input_log });
    } catch (error) {
        if (el) { el.textContent = error.message; el.dataset.state = 'error'; el.hidden = false; }
    }
}

export async function loadSettings() {
    try {
        const resp = await fetch("/api/settings");
        const data = await resp.json();
        const s = data.settings || {};
        // Boolean settings are stored as strings in SQLite — coerce them
        if (typeof s.fit_pane_width === "string") {
            s.fit_pane_width = s.fit_pane_width === "True";
        }
        // Default fit_pane_width to true if not yet set
        if (s.fit_pane_width === undefined) {
            s.fit_pane_width = true;
        }
        if (typeof s.notify_needs_input === "string") {
            s.notify_needs_input = s.notify_needs_input === "True";
        }
        if (s.notify_needs_input === undefined) {
            s.notify_needs_input = true;
        }
        // Coerce show_scrollbars
        if (typeof s.show_scrollbars === "string") {
            s.show_scrollbars = s.show_scrollbars === "True";
        }
        if (s.show_scrollbars === undefined) {
            s.show_scrollbars = true;
        }
        // Coerce refresh_files_on_switch (default: disabled)
        if (typeof s.refresh_files_on_switch === "string") {
            s.refresh_files_on_switch = s.refresh_files_on_switch === "True" || s.refresh_files_on_switch === "true";
        }
        if (s.refresh_files_on_switch === undefined) {
            s.refresh_files_on_switch = false;
        }
        // Default file_search_mode to 'directory'
        if (!s.file_search_mode) {
            s.file_search_mode = 'directory';
        }
        // Default git_diff_mode to 'previous_commit' (HEAD~1)
        if (!s.git_diff_mode) {
            s.git_diff_mode = 'previous_commit';
        }
        state.settings = s;
        window.applySessionSummaryPref?.();

        const telemetry = document.getElementById('settings-telemetry-enabled');
        if (telemetry) telemetry.checked = settingBool(s.telemetry_enabled, true);
        const autoAttach = document.getElementById('settings-board-auto-attach');
        // Off unless the Operator turned it on (#3563); an empty value is unset.
        if (autoAttach) autoAttach.checked = settingBool(s.board_auto_attach || undefined, false);
        const healthMonitor = document.getElementById('settings-board-health-monitor');
        // On unless the Operator turned it off (#5128); an empty value is unset.
        if (healthMonitor) healthMonitor.checked = settingBool(s.board_health_monitor || undefined, true);
        const autoResume = document.getElementById('settings-auto-resume');
        // Off unless the Operator turned it on (#3613).
        if (autoResume) autoResume.checked = settingBool(s.auto_resume_after_api_error || undefined, false);
        const autoGoals = document.getElementById('settings-auto-goals');
        // On unless the Operator turned it off (#4204).
        if (autoGoals) autoGoals.checked = settingBool(s.auto_goals || undefined, true);
        const debugInput = document.getElementById('settings-debug-input-log');
        if (debugInput) debugInput.checked = settingBool(s.debug_input_log, false);
        try {
            const privacyResp = await fetch('/api/system/privacy');
            if (!privacyResp.ok) throw new Error(`privacy status ${privacyResp.status}`);
            state.privacyStatus = await privacyResp.json();
            const remote = document.getElementById('settings-remote-access-enabled');
            if (remote) remote.checked = settingBool(state.privacyStatus.remote_access_enabled, false);
            renderRemoteAccessStatus(state.privacyStatus);
            renderDebugInputLogStatus(state.privacyStatus);
            renderToolsFolderStatus(state.privacyStatus);
        } catch (error) {
            state.privacyStatus = null;
            const remote = document.getElementById('settings-remote-access-enabled');
            if (remote) remote.checked = settingBool(s.remote_access_enabled, false);
            renderRemoteAccessStatus(null, true);
        }

        // Apply scrollbar visibility
        document.body.classList.toggle('no-scrollbars', !s.show_scrollbars);

        // Apply theme from settings (default to Dark if no theme configured)
        // No custom theme (or the retired default) is the built-in one the page already has (theme_init.js): nothing is fetched or overlaid, so a hub that stores no theme leaves the stylesheet's own tokens (#6087).
        // Nothing is written into the settings here (#6114): a stored theme the person never picked would be saved back by the next Save.
        if (s.custom_theme && s.custom_theme !== "GhostV3") await applyCustomThemeByName(s.custom_theme, { remember: false }); // the page load applies the person's stored theme; it does not rewrite the browser's saved choice
        // A partner build names itself in the settings menu
        _loadTierBadge();
    } catch (e) {
        console.error("Failed to load settings:", e);
    }
}

/** A partner build's name in the settings menu (Coral for Dropbox); nothing otherwise. The menu is built on first open
 *  (#4500); until then this has nothing to fill. Coral has no paid licence (#3221). */
 async function _loadTierBadge() {
    const badge = document.getElementById('tier-badge');
    if (!badge) return;
    try {
        const resp = await fetchSystemStatus();
        if (!resp.ok) return;
        const data = await resp.json();
        if (data.tier_name === 'dropboxers') {
            badge.innerHTML = '<span class="tier-label tier-pro">Coral For Dropbox</span>';
            badge.style.display = '';
        } else {
            badge.style.display = 'none';
        }
    } catch { /* silent — non-critical */ }
}
onBuilt('top-bar-settings-menu', () => { _loadTierBadge(); });

/** Resolve "system" to the actual dark/light value */
 function resolveTheme(theme) {
    if (theme === "system") {
        return window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
    }
    return theme;
}

/** What the theme control shows when no theme is stored: this computer starts dark, the Cloud Hub follows the system (theme_init.js). */
export function defaultThemeChoice() { return isCloud() ? "system" : "dark"; }

/** Apply a theme to the document */
export function applyTheme(theme, { remember = true } = {}) {
    const resolved = resolveTheme(theme);
    document.documentElement.setAttribute("data-theme", resolved);

    // Store the *preference* (dark/light/system) so other pages can read it: only when the person chose it (the settings dialog), never when a page load applies what a hub says (#6087)
    if (remember) { try { browserLocal.set("coral-theme", theme); } catch (_) {} }

    // Update xterm.js terminal colors to match
    updateTerminalTheme();
}

/** Remove all custom CSS variables from inline styles (reset before switching themes) */
export function clearCustomThemeVars() {
    const style = document.documentElement.style;
    // Remove any --* properties that were set inline
    const toRemove = [];
    for (let i = 0; i < style.length; i++) {
        const prop = style[i];
        if (prop.startsWith("--")) toRemove.push(prop);
    }
    for (const prop of toRemove) style.removeProperty(prop);
}

/** Load a custom theme by name: apply its base theme + overlay its CSS variables */
export async function applyCustomThemeByName(name, { remember = true } = {}) {
    try {
        const resp = await fetch(`/api/themes/${encodeURIComponent(name)}`);
        if (!resp.ok) return; // a theme the server does not have: the stylesheet's tokens stay
        const data = await resp.json();
        if (data.error || !data.theme?.variables || typeof data.theme.variables !== "object") return;

        // Apply the base theme (dark/light); anything else is not a base, and the page keeps its own
        const base = data.theme.base;
        if (base === "dark" || base === "light") applyTheme(base, { remember });

        // Overlay custom variables: custom properties with a real value only (an empty or blank one would empty the token the stylesheet defines, #6087)
        for (const [cssVar, value] of Object.entries(data.theme.variables)) {
            if (typeof cssVar === "string" && cssVar.startsWith("--") && typeof value === "string" && value.trim()) document.documentElement.style.setProperty(cssVar, value.trim());
        }

        // Refresh terminal colors
        updateTerminalTheme();
    } catch {
        // Fall back to default
    }
}

// Listen for OS theme changes when in "system" mode
window.matchMedia("(prefers-color-scheme: light)").addEventListener("change", () => {
    const pref = (state.settings && state.settings.theme) || "dark";
    if (pref === "system") applyTheme("system");
});
