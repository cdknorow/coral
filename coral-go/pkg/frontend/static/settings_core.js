/* Settings and theme, loaded with the page (#3140). loadSettings() runs at
 * startup and applies the theme; the theme follows the OS in "system" mode.
 * Everything else of
 * the settings, launch and team dialogs (modals.js) loads the first time one
 * opens. */

import { state } from './state.js';
import { updateTerminalTheme } from './xterm_renderer.js';

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
        el.textContent = `Saved: ${saved ? 'enabled' : 'disabled'}. Running now: ${effective ? 'remote access enabled' : 'local-only'}; restart required to apply the saved value.`;
    } else {
        el.textContent = `Saved and running: ${effective ? 'remote access enabled' : 'local-only'}.`;
    }
    el.dataset.state = restart ? 'restart-required' : 'current';
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
        try {
            const privacyResp = await fetch('/api/system/privacy');
            if (!privacyResp.ok) throw new Error(`privacy status ${privacyResp.status}`);
            state.privacyStatus = await privacyResp.json();
            const remote = document.getElementById('settings-remote-access-enabled');
            if (remote) remote.checked = settingBool(state.privacyStatus.remote_access_enabled, false);
            renderRemoteAccessStatus(state.privacyStatus);
        } catch (error) {
            state.privacyStatus = null;
            const remote = document.getElementById('settings-remote-access-enabled');
            if (remote) remote.checked = settingBool(s.remote_access_enabled, false);
            renderRemoteAccessStatus(null, true);
        }

        // Apply scrollbar visibility
        document.body.classList.toggle('no-scrollbars', !s.show_scrollbars);

        // Apply theme from settings (default to Dark if no theme configured)
        const themeName = (!s.custom_theme || s.custom_theme === "GhostV3") ? "Dark" : s.custom_theme;
        await applyCustomThemeByName(themeName);
        if (!s.custom_theme || s.custom_theme === "GhostV3") {
            state.settings.custom_theme = "Dark";
        }
        // A partner build names itself in the settings menu
        _loadTierBadge();
    } catch (e) {
        console.error("Failed to load settings:", e);
    }
}

/** A partner build's name in the settings menu (Coral for Dropbox); nothing otherwise.
 *  Coral has no paid licence (#3221). */
export async function _loadTierBadge() {
    const badge = document.getElementById('tier-badge');
    if (!badge) return;
    try {
        const resp = await fetch('/api/system/status');
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

/** Resolve "system" to the actual dark/light value */
export function resolveTheme(theme) {
    if (theme === "system") {
        return window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
    }
    return theme;
}

/** Apply a theme to the document */
export function applyTheme(theme) {
    const resolved = resolveTheme(theme);
    document.documentElement.setAttribute("data-theme", resolved);

    // Store the *preference* (dark/light/system) so other pages can read it
    try { localStorage.setItem("coral-theme", theme); } catch (_) {}

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
export async function applyCustomThemeByName(name) {
    try {
        const resp = await fetch(`/api/themes/${encodeURIComponent(name)}`);
        const data = await resp.json();
        if (data.error || !data.theme?.variables) return;

        // Apply the base theme (dark/light)
        const base = data.theme.base || "dark";
        applyTheme(base);

        // Overlay custom variables
        for (const [cssVar, value] of Object.entries(data.theme.variables)) {
            if (value) document.documentElement.style.setProperty(cssVar, value);
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
