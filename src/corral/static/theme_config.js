/* Theme configurator — custom color theme editor with save/load/import/export */

import { state } from './state.js';
import { escapeHtml, showToast } from './utils.js';
import { updateTerminalTheme } from './xterm_renderer.js';

let variableGroups = {};   // { groupName: { cssVar: label, ... }, ... }
let currentEdits = {};     // { cssVar: colorValue, ... }
let savedThemes = [];      // [{ name, description, base }, ...]

export async function showThemeConfigurator() {
    // Load variable definitions and saved themes in parallel
    const [varsResp, themesResp] = await Promise.all([
        fetch('/api/themes/variables').then(r => r.json()),
        fetch('/api/themes').then(r => r.json()),
    ]);
    variableGroups = varsResp.groups || {};
    savedThemes = themesResp.themes || [];

    // Seed currentEdits from the current computed CSS values
    currentEdits = {};
    const styles = getComputedStyle(document.documentElement);
    for (const vars of Object.values(variableGroups)) {
        for (const cssVar of Object.keys(vars)) {
            const val = styles.getPropertyValue(cssVar).trim();
            currentEdits[cssVar] = val;
        }
    }

    renderModal();
    document.getElementById('theme-configurator-modal').style.display = 'flex';
}

export function hideThemeConfigurator() {
    document.getElementById('theme-configurator-modal').style.display = 'none';
}

function renderModal() {
    const modal = document.getElementById('theme-configurator-modal');
    if (!modal) return;

    const themeOptions = savedThemes.map(t =>
        `<option value="${escapeHtml(t.name)}">${escapeHtml(t.name)}</option>`
    ).join('');

    const groups = Object.entries(variableGroups).map(([groupName, vars]) => {
        const rows = Object.entries(vars).map(([cssVar, label]) => {
            const value = currentEdits[cssVar] || '';
            const hexValue = toHex(value);
            return `<div class="theme-row">
                <label class="theme-label" title="${escapeHtml(cssVar)}">${escapeHtml(label)}</label>
                <div class="theme-color-wrap">
                    <input type="color" class="theme-color-picker" value="${hexValue}" data-var="${escapeHtml(cssVar)}"
                        oninput="updateThemeVar(this)">
                    <input type="text" class="theme-color-text" value="${escapeHtml(value)}" data-var="${escapeHtml(cssVar)}"
                        oninput="updateThemeVarText(this)">
                </div>
            </div>`;
        }).join('');
        return `<div class="theme-group">
            <div class="theme-group-title">${escapeHtml(groupName)}</div>
            ${rows}
        </div>`;
    }).join('');

    modal.innerHTML = `
        <div class="modal-content theme-configurator-content">
            <h3>Theme Configurator</h3>
            <div class="theme-toolbar">
                <select id="theme-load-select" class="settings-select">
                    <option value="">Load theme...</option>
                    ${themeOptions}
                </select>
                <button class="btn btn-sm" onclick="loadSelectedTheme()">Load</button>
                <button class="btn btn-sm btn-danger" onclick="deleteSelectedTheme()">Delete</button>
                <span class="toolbar-spacer"></span>
                <button class="btn btn-sm" onclick="importThemeFile()">Import</button>
                <button class="btn btn-sm" onclick="exportCurrentTheme()">Export</button>
            </div>
            <div class="theme-generate-bar">
                <input type="text" id="theme-ai-prompt" class="settings-input" placeholder="Describe your theme... (e.g. &quot;cyberpunk neon with pink and teal&quot;)">
                <button class="btn btn-sm btn-primary" id="theme-generate-btn" onclick="generateThemeFromDescription()">Generate</button>
            </div>
            <div class="theme-editor-scroll">
                ${groups}
            </div>
            <div class="theme-save-bar">
                <input type="text" id="theme-save-name" class="settings-input" placeholder="Theme name">
                <input type="text" id="theme-save-desc" class="settings-input" placeholder="Description (optional)">
                <select id="theme-save-base" class="settings-select" style="width:auto">
                    <option value="dark">Based on: Dark</option>
                    <option value="light">Based on: Light</option>
                </select>
            </div>
            <div class="modal-actions">
                <button class="btn" onclick="hideThemeConfigurator()">Cancel</button>
                <button class="btn" onclick="resetThemeToDefaults()">Reset</button>
                <button class="btn btn-primary" onclick="previewTheme()">Preview</button>
                <button class="btn btn-primary" onclick="saveAndApplyTheme()">Save &amp; Apply</button>
            </div>
        </div>
    `;
}

// ── Color picker handlers (exposed globally) ────────────────────────────

window.updateThemeVar = function(picker) {
    const cssVar = picker.dataset.var;
    const value = picker.value;
    currentEdits[cssVar] = value;
    // Sync the text input
    const textInput = picker.parentElement.querySelector('.theme-color-text');
    if (textInput) textInput.value = value;
};

window.updateThemeVarText = function(input) {
    const cssVar = input.dataset.var;
    const value = input.value.trim();
    currentEdits[cssVar] = value;
    // Sync the color picker if it's a valid hex
    const hex = toHex(value);
    if (hex !== '#000000' || value.toLowerCase().startsWith('#000')) {
        const picker = input.parentElement.querySelector('.theme-color-picker');
        if (picker) picker.value = hex;
    }
};

// ── Theme operations ─────────────────────────────────────────────────────

window.previewTheme = function() {
    applyVariablesToDom(currentEdits);
    showToast('Theme preview applied');
};

window.resetThemeToDefaults = function() {
    // Remove all custom properties and reload
    for (const cssVar of Object.keys(currentEdits)) {
        document.documentElement.style.removeProperty(cssVar);
    }
    // Re-read computed values
    const styles = getComputedStyle(document.documentElement);
    for (const cssVar of Object.keys(currentEdits)) {
        currentEdits[cssVar] = styles.getPropertyValue(cssVar).trim();
    }
    renderModal();
    showToast('Reset to defaults');
};

window.saveAndApplyTheme = async function() {
    const name = document.getElementById('theme-save-name').value.trim();
    if (!name) {
        showToast('Theme name is required', true);
        return;
    }
    const description = document.getElementById('theme-save-desc').value.trim();
    const base = document.getElementById('theme-save-base').value;

    try {
        const resp = await fetch(`/api/themes/${encodeURIComponent(name)}`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ description, base, variables: currentEdits }),
        });
        const data = await resp.json();
        if (data.error) {
            showToast(data.error, true);
            return;
        }

        // Save the active theme name in settings
        await fetch('/api/settings', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ custom_theme: name }),
        });
        state.settings.custom_theme = name;

        applyVariablesToDom(currentEdits);
        showToast(`Theme "${name}" saved and applied`);
        hideThemeConfigurator();
    } catch (e) {
        showToast('Failed to save theme', true);
    }
};

window.loadSelectedTheme = async function() {
    const select = document.getElementById('theme-load-select');
    const name = select.value;
    if (!name) return;

    try {
        const resp = await fetch(`/api/themes/${encodeURIComponent(name)}`);
        const data = await resp.json();
        if (data.error) {
            showToast(data.error, true);
            return;
        }
        const theme = data.theme;
        currentEdits = { ...currentEdits, ...theme.variables };
        document.getElementById('theme-save-name').value = name;
        document.getElementById('theme-save-desc').value = theme.description || '';
        document.getElementById('theme-save-base').value = theme.base || 'dark';
        renderModal();
        // Restore name/desc after re-render
        document.getElementById('theme-save-name').value = name;
        document.getElementById('theme-save-desc').value = theme.description || '';
        document.getElementById('theme-save-base').value = theme.base || 'dark';
        showToast(`Loaded: ${name}`);
    } catch (e) {
        showToast('Failed to load theme', true);
    }
};

window.deleteSelectedTheme = async function() {
    const select = document.getElementById('theme-load-select');
    const name = select.value;
    if (!name) return;
    if (!confirm(`Delete theme "${name}"?`)) return;

    try {
        await fetch(`/api/themes/${encodeURIComponent(name)}`, { method: 'DELETE' });
        // If active theme is being deleted, clear the setting
        if (state.settings.custom_theme === name) {
            await fetch('/api/settings', {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ custom_theme: '' }),
            });
            state.settings.custom_theme = '';
        }
        // Refresh theme list
        const resp = await fetch('/api/themes');
        savedThemes = (await resp.json()).themes || [];
        renderModal();
        showToast(`Deleted: ${name}`);
    } catch (e) {
        showToast('Failed to delete theme', true);
    }
};

window.exportCurrentTheme = function() {
    const name = document.getElementById('theme-save-name')?.value.trim() || 'corral-theme';
    const description = document.getElementById('theme-save-desc')?.value.trim() || '';
    const base = document.getElementById('theme-save-base')?.value || 'dark';

    const data = { name, description, base, variables: currentEdits };
    const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${name}.json`;
    a.click();
    URL.revokeObjectURL(url);
    showToast('Theme exported');
};

window.generateThemeFromDescription = async function() {
    const prompt = document.getElementById('theme-ai-prompt')?.value.trim();
    if (!prompt) {
        showToast('Enter a theme description first', true);
        return;
    }

    const base = document.getElementById('theme-save-base')?.value || 'dark';
    const btn = document.getElementById('theme-generate-btn');
    const input = document.getElementById('theme-ai-prompt');
    const origText = btn.textContent;
    btn.textContent = 'Generating...';
    btn.disabled = true;
    if (input) input.disabled = true;

    // Show a persistent progress banner in the editor area
    const scroll = document.querySelector('.theme-editor-scroll');
    let banner = null;
    if (scroll) {
        banner = document.createElement('div');
        banner.className = 'theme-generate-banner';
        banner.innerHTML = '<span class="theme-generate-spinner"></span> Generating theme with AI — this may take 10-20 seconds...';
        scroll.prepend(banner);
    }

    try {
        const resp = await fetch('/api/themes/generate', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ description: prompt, base }),
        });
        const data = await resp.json();
        if (data.error) {
            showToast(data.error, true);
            return;
        }

        // Merge generated variables into current edits
        const generated = data.variables || {};
        for (const [cssVar, value] of Object.entries(generated)) {
            if (cssVar in currentEdits) {
                currentEdits[cssVar] = value;
            }
        }

        // Capture generated name before re-render
        const generatedName = data.name || '';

        // Re-render the editor with new values
        renderModal();

        // Restore fields after re-render (renderModal rebuilds all inputs)
        const promptInput = document.getElementById('theme-ai-prompt');
        if (promptInput) promptInput.value = prompt;
        if (generatedName) {
            document.getElementById('theme-save-name').value = generatedName;
        }

        showToast('Theme generated — click Preview to see it live');
    } catch (e) {
        showToast('Failed to generate theme', true);
    } finally {
        if (banner) banner.remove();
        btn.textContent = origText;
        btn.disabled = false;
        if (input) input.disabled = false;
    }
};

window.importThemeFile = function() {
    const input = document.createElement('input');
    input.type = 'file';
    input.accept = '.json';
    input.onchange = async () => {
        const file = input.files[0];
        if (!file) return;
        try {
            const text = await file.text();
            const data = JSON.parse(text);
            if (!data.variables || typeof data.variables !== 'object') {
                showToast('Invalid theme file: missing variables', true);
                return;
            }

            // Upload to server
            const formData = new FormData();
            formData.append('file', file);
            const resp = await fetch('/api/themes/import', { method: 'POST', body: formData });
            const result = await resp.json();
            if (result.error) {
                showToast(result.error, true);
                return;
            }

            // Load into editor
            currentEdits = { ...currentEdits, ...data.variables };
            const themesResp = await fetch('/api/themes');
            savedThemes = (await themesResp.json()).themes || [];
            renderModal();
            document.getElementById('theme-save-name').value = result.name;
            document.getElementById('theme-save-desc').value = data.description || '';
            document.getElementById('theme-save-base').value = data.base || 'dark';
            showToast(`Imported: ${result.name}`);
        } catch (e) {
            showToast('Failed to import theme', true);
        }
    };
    input.click();
};

// ── Apply theme on page load ─────────────────────────────────────────────

export async function loadCustomTheme() {
    const themeName = state.settings.custom_theme;
    if (!themeName) return;

    try {
        const resp = await fetch(`/api/themes/${encodeURIComponent(themeName)}`);
        const data = await resp.json();
        if (data.error || !data.theme?.variables) return;
        applyVariablesToDom(data.theme.variables);
    } catch {
        // Silently fail — fall back to default theme
    }
}

// ── Utilities ────────────────────────────────────────────────────────────

function applyVariablesToDom(variables) {
    for (const [cssVar, value] of Object.entries(variables)) {
        if (value) {
            document.documentElement.style.setProperty(cssVar, value);
        }
    }
    // Refresh xterm terminal to pick up new --xterm-* variables
    updateTerminalTheme();
}

function toHex(color) {
    if (!color) return '#000000';
    color = color.trim();

    // Already hex
    if (/^#[0-9a-fA-F]{6}$/.test(color)) return color;
    if (/^#[0-9a-fA-F]{3}$/.test(color)) {
        return '#' + color[1] + color[1] + color[2] + color[2] + color[3] + color[3];
    }

    // rgb/rgba
    const rgbMatch = color.match(/rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)/);
    if (rgbMatch) {
        const r = parseInt(rgbMatch[1]).toString(16).padStart(2, '0');
        const g = parseInt(rgbMatch[2]).toString(16).padStart(2, '0');
        const b = parseInt(rgbMatch[3]).toString(16).padStart(2, '0');
        return `#${r}${g}${b}`;
    }

    return '#000000';
}
