/* Moved out of modals.js as it was (#3844): no behaviour change. */
import { importOnce } from './import_once.js';
const { ALL_CAPABILITIES } = await importOnce('./agent_presets.js');
const { PERMISSION_MODE_DESCRIPTIONS, defaultPermissionMode, syncDefaultPermissionMode, trackPermissionPick, _hasPermFlag, _normalizePermFlagsForAgent, _permissionModeFromFlags, _stripPermFlags, _syncACFAutoPermissions } = await importOnce('./permission_flags.js');
const { _acfServer, _fillModelDatalist, _getAgentModels, _getDefaultModels, _settingsFor } = await importOnce('./launch_models.js');
const { _checkAgentCLI } = await importOnce('./cli_check.js');
const { _classifyLoadedACFModel } = await importOnce('./acf_models.js');
const { modelPickerHtml, loadModelPicker, wireModelPicker, syncModelPicker, thinkingPickerHtml, thinkingValue, setThinking, setThinkingActual, wireThinking } = await importOnce('./model_picker.js');
const { _renderPresetButtons } = await importOnce('./persona_store.js');
const { presetEnforcement } = await importOnce('./preset_enforcement.js');
const { initSectionDialog } = await importOnce('./section_dialog.js');
import { escapeHtml, escapeAttr } from './utils.js';

// ── Permissions Editor ─────────────────────────────────────────────────
const CAPABILITY_LABELS = {
    file_read: 'Read Files',
    file_write: 'Write Files',
    shell: 'Shell',
    web_access: 'Web Access',
    git_write: 'Git Write',
    agent_spawn: 'Spawn Agents',
    notebook: 'Notebooks',
};
// What a capability does beyond its name, per CLI (#4545, Security's D: for Codex, Git Write is what turns
// off its sandbox).
const CAPABILITY_TITLES = {
    git_write: 'Claude: pre-approves git push, commit, branch, merge and rebase. Codex: with Shell, Read Files and Write Files, it runs without a sandbox.',
};

export function _togglePermissions(containerId) {
    const el = document.getElementById(containerId);
    if (!el) return;
    const isHidden = el.style.display === 'none';
    el.style.display = isHidden ? '' : 'none';
    const chevron = el.previousElementSibling?.querySelector('.permissions-chevron');
    if (chevron) chevron.style.transform = isHidden ? 'rotate(90deg)' : '';
}

function _renderPermChips(containerId, type, capabilities) {
    const container = document.getElementById(containerId);
    if (!container) return;
    const caps = capabilities?.[type] || [];
    // Separate base capabilities from shell patterns
    const baseCaps = caps.filter(c => !c.startsWith('shell:'));
    const shellPatterns = caps.filter(c => c.startsWith('shell:')).map(c => c.slice(6));

    let html = '';
    for (const cap of ALL_CAPABILITIES) {
        const isActive = baseCaps.includes(cap);
        const cls = `perm-chip${isActive ? ' active' : ''}`;
        const title = CAPABILITY_TITLES[cap] ? ` title="${escapeAttr(CAPABILITY_TITLES[cap])}"` : '';
        html += `<button type="button" class="${escapeAttr(cls)}" aria-pressed="${escapeAttr(isActive ? 'true' : 'false')}" data-cap="${escapeAttr(cap)}" data-type="${escapeAttr(type)}" data-on-click="perm-chip-toggle"${title}>${CAPABILITY_LABELS[cap] || cap}</button>`;
    }
    container.innerHTML = html;

    // Show shell patterns input if shell is in allow
    const shellSection = container.closest('.permissions-editor')?.querySelector('.perms-shell-patterns');
    const shellInput = container.closest('.permissions-editor')?.querySelector('.perms-shell-input');
    if (shellSection && shellInput && type === 'allow') {
        const hasShell = baseCaps.includes('shell');
        const hasShellPatterns = shellPatterns.length > 0;
        shellSection.style.display = (hasShell || hasShellPatterns) ? '' : 'none';
        shellInput.value = shellPatterns.join(', ');
    }
}

export function _togglePermChip(btn) {
    if (btn.disabled) return;
    btn.classList.toggle('active');
    btn.setAttribute('aria-pressed', String(btn.classList.contains('active')));
    // Toggle shell patterns visibility
    const cap = btn.dataset.cap;
    const type = btn.dataset.type;
    if (cap === 'shell' && type === 'allow') {
        const shellSection = btn.closest('.permissions-editor')?.querySelector('.perms-shell-patterns');
        if (shellSection) {
            shellSection.style.display = btn.classList.contains('active') ? '' : 'none';
        }
    }
    _syncEnforcement(btn.closest('[data-acf-uid]'));
}

// _syncEnforcement says under Permissions what the chosen agent CLI enforces for these permissions
// (#4545, Security's #4550): Codex keeps only a sandbox and an approval mode and Antigravity only a mode, so
// for them a new Deny entry can't be added (an existing one can be removed: it still changes the mode, shown
// in the line) and shell patterns are marked as not enforced. Coral adds no permission layer of its own.
function _syncEnforcement(container) {
    if (!container) return;
    const uid = container.dataset.acfUid;
    const agentType = container.querySelector('.acf-agent-type')?.value || 'claude';
    const permMode = container.querySelector('.acf-permission-mode')?.value
        || defaultPermissionMode(_settingsFor(_acfServer(container)), agentType);
    const e = presetEnforcement(agentType, _getPermissions(`${uid}-perms`) || {}, permMode);
    // The form's own line, and any line elsewhere that mirrors it (the Launch dialog shows it under the
    // persona, while this form is folded away).
    const lines = [container.querySelector('.acf-perms-enforced'),
        ...(container.id ? document.querySelectorAll(`[data-acf-enforced-for="${CSS.escape(container.id)}"]`) : [])];
    for (const line of lines) {
        if (!line) continue;
        line.textContent = e.text;
        line.hidden = !e.text || (e.quiet && line !== container.querySelector('.acf-perms-enforced')); // the plain Bypass line is said once, on Permissions (#5595)
    }
    // Claude in Bypass Permissions mode never asks, so Allow pre-approves nothing: the row is not shown (its
    // chips keep their state, so a saved preset is not changed by looking at it) and the line above says why.
    const allowGroup = container.querySelector(`#${CSS.escape(uid)}-perms-allow`)?.closest('.perms-group');
    const noAllow = _agentFamilyIsClaude(agentType) && permMode === 'bypassPermissions';
    if (allowGroup) allowGroup.hidden = noAllow;
    const patterns = container.querySelector('.perms-shell-patterns'); // the shell patterns are Allow entries: no Allow row, no patterns field (its value is kept)
    if (patterns) patterns.hidden = noAllow;
    for (const chip of container.querySelectorAll(`#${CSS.escape(uid)}-perms-deny .perm-chip`)) {
        chip.disabled = !e.denyEnforced && !chip.classList.contains('active');
    }
    const denyNote = container.querySelector('.perms-deny-note');
    if (denyNote) denyNote.textContent = e.denyEnforced ? '' : 'Not enforced by this agent';
    const patternsNote = container.querySelector('.perms-patterns-note');
    if (patternsNote) patternsNote.textContent = e.patternsEnforced ? '' : 'Not enforced by this agent';
}

function _agentFamilyIsClaude(agentType) {
    return !agentType || agentType === 'claude';
}

function _getPermissions(editorId) {
    const editor = document.getElementById(editorId);
    if (!editor) return null;

    const result = {};
    for (const type of ['allow', 'deny']) {
        const chips = editor.querySelectorAll(`[data-type="${type}"].active`);
        if (chips.length > 0) {
            result[type] = [...chips].map(c => c.dataset.cap);
        }
    }

    // Add shell patterns to allow list
    const shellInput = editor.querySelector('.perms-shell-input');
    if (shellInput && shellInput.value.trim()) {
        if (!result.allow) result.allow = [];
        const patterns = shellInput.value.split(',').map(p => p.trim()).filter(Boolean);
        for (const p of patterns) {
            result.allow.push(`shell:${p}`);
        }
    }

    return Object.keys(result).length > 0 ? result : null;
}

function _setPermissions(editorId, capabilities) {
    const editor = document.getElementById(editorId);
    if (!editor) return;
    _renderPermChips(editorId + '-allow', 'allow', capabilities || {});
    _renderPermChips(editorId + '-deny', 'deny', capabilities || {});
}

// ── Unified Agent Config Form ─────────────────────────────────────────

let _acfCounter = 0;

/**
 * Render a unified agent configuration form into a container.
 * @param {string} containerId - DOM element ID to render into
 * @param {object} opts - { mode: 'full'|'compact-card', showPreset: bool, showName: bool, value: {...} }
 */
/** The form's missing-CLI line: in the form, or where the dialog asked for it (opts.cliSlot). */
export function cliWarningOf(container) { return container._acfCliWarning || container.querySelector('.acf-cli-warning'); }

export function renderAgentConfigForm(containerId, opts = {}) {
    const container = document.getElementById(containerId);
    if (!container) return;
    _acfCounter++;
    const uid = `acf-${_acfCounter}`;
    container.dataset.acfUid = uid;
    delete container._acfCliWarning;

    const showPreset = opts.showPreset !== false;
    const showName = opts.showName !== false;
    const showAutoPermissions = opts.showAutoPermissions !== false;
    const v = opts.value || {};
    // A form that starts from the Settings defaults says so under those two fields: Settings is where the default lives
    // (UI audit #5589, finding 22), the field here is this launch's own choice.

    const agentTypeVal = v.agentType || v.agent_type || _settingsFor(_acfServer(container)).default_agent_type || '';
    const modelVal = v.model || '';
    const nameVal = v.name || '';
    const promptVal = v.prompt || '';
    const flagsVal = v.flags || '';
    const autoPermsVal = Object.prototype.hasOwnProperty.call(v, 'autoPermissions')
        ? !!v.autoPermissions
        : (Object.prototype.hasOwnProperty.call(v, 'flags') ? _hasPermFlag(flagsVal) : true);

    // Build permissions summary text
    const caps = v.capabilities || null;
    let permsSummary = 'default';
    if (caps) {
        const parts = [];
        if (caps.allow?.length) parts.push(`${caps.allow.length} allow`);
        if (caps.deny?.length) parts.push(`${caps.deny.length} deny`);
        permsSummary = parts.length ? parts.join(', ') : 'default';
    }

    let presetHTML = '';
    if (showPreset) {
        presetHTML = `<div id="${escapeAttr(uid)}-presets" class="agent-preset-selector" style="margin-bottom:8px"></div>`;
    }

    let nameHTML = '';
    if (showName) {
        nameHTML = `
            <label>Name / Role:
                <input type="text" class="acf-name" placeholder="e.g. QA Engineer, Frontend Dev" value="${escapeAttr(nameVal)}">
            </label>`;
    }

    let autoPermissionsHTML = '';
    if (showAutoPermissions) {
        const defaultMode = defaultPermissionMode(_settingsFor(_acfServer(container)), agentTypeVal || 'claude');
        const permModeVal = autoPermsVal
            ? (typeof autoPermsVal === 'string' ? autoPermsVal : (_permissionModeFromFlags(flagsVal) || defaultMode))
            : 'default';
        const permModeDesc = PERMISSION_MODE_DESCRIPTIONS[permModeVal] || '';
        autoPermissionsHTML = `
            <label>Permission Mode:
                <select class="acf-permission-mode" data-on-change="perm-mode-describe">
                    <option value="default"${permModeVal === 'default' ? ' selected' : ''}>Default</option>
                    <option value="plan"${permModeVal === 'plan' ? ' selected' : ''}>Plan</option>
                    <option value="acceptEdits"${permModeVal === 'acceptEdits' ? ' selected' : ''}>Accept Edits</option>
                    <option value="auto"${permModeVal === 'auto' ? ' selected' : ''}>Auto</option>
                    <option value="bypassPermissions"${permModeVal === 'bypassPermissions' ? ' selected' : ''}>Bypass Permissions</option>
                    <option value="dontAsk"${permModeVal === 'dontAsk' ? ' selected' : ''}>Don't Ask</option>
                </select>
                <span class="settings-hint perm-mode-desc">${escapeHtml(permModeDesc)}</span>
            </label>`;
    }

    const topHTML = `
        <div class="acf-top-row">
            <label>Agent Type:
                <select class="acf-agent-type">
                    <option value="claude"${agentTypeVal === 'claude' || !agentTypeVal ? ' selected' : ''}>Claude</option>
                    <option value="agy"${agentTypeVal === 'agy' || agentTypeVal === 'antigravity' || agentTypeVal === 'gemini' ? ' selected' : ''}>Antigravity</option>
                    <option value="codex"${agentTypeVal === 'codex' ? ' selected' : ''}>Codex</option>
                    <option value="pi"${agentTypeVal === 'pi' ? ' selected' : ''}>Pi</option>
                    <option value="terminal"${agentTypeVal === 'terminal' ? ' selected' : ''}>Terminal</option>
                </select>
            </label>
            <label class="acf-model-wrap">Model <span style="color:var(--text-muted);font-weight:normal">(optional)</span>:
                ${modelPickerHtml()}
                <input type="text" class="acf-model" list="acf-models-${uid}" placeholder="Leave blank to use default" autocomplete="off" value="${escapeAttr(modelVal)}">
                <datalist id="acf-models-${escapeAttr(uid)}"></datalist>
            </label>
            ${thinkingPickerHtml()}
        </div>
        <div class="acf-cli-warning" style="display:none;margin:8px 0" role="status"></div>`;
    const permEditorHTML = (open) => `
            <p class="settings-hint acf-perms-enforced" aria-live="polite" hidden></p>
            <div id="${escapeAttr(uid)}-perms" class="permissions-editor"${open ? '' : ' style="display:none"'}>
                <div class="perms-group">
                    <span class="perms-label" id="${escapeAttr(uid)}-perms-allow-label">Allow:</span>
                    <div id="${escapeAttr(uid)}-perms-allow" class="perms-chips" role="group" aria-labelledby="${escapeAttr(uid)}-perms-allow-label"></div>
                </div>
                <div class="perms-group">
                    <span class="perms-label" id="${escapeAttr(uid)}-perms-deny-label">Deny: <span class="perms-deny-note settings-hint"></span></span>
                    <div id="${escapeAttr(uid)}-perms-deny" class="perms-chips" role="group" aria-labelledby="${escapeAttr(uid)}-perms-deny-label"></div>
                </div>
                <div class="perms-shell-patterns" style="display:none">
                    <label class="perms-label">Shell patterns <span style="color:var(--text-muted);font-weight:normal">(comma-separated)</span>: <span class="perms-patterns-note settings-hint"></span></label>
                    <input type="text" class="perms-shell-input" placeholder="e.g. git *, npm *, npx *">
                </div>
            </div>`;
    const flagsInput = `<input type="text" class="acf-flags" placeholder="e.g. --verbose" value="${escapeAttr(_stripPermFlags(flagsVal))}">`;
    if (opts.sections) {
        // The sectioned dialogs (#4950, section_dialog.js): one pane per group of fields, every field the same element with the same class as in the one-form layout.
        const pane = (id, title, icon, desc, body) => `<section class="settings-pane" data-section="${escapeAttr(id)}" data-title="${escapeAttr(title)}" data-icon="${escapeAttr(icon)}" data-desc="${escapeAttr(desc)}" hidden>${body}</section>`;
        // Whatever the dialog asked to sit in a pane (opts.adopt: { paneId: element id }) is parked beside the container while the panes are rebuilt.
        const adopted = Object.entries(opts.adopt || {}).map(([paneId, id]) => [paneId, document.getElementById(id)]).filter(([, el]) => el);
        for (const [, el] of adopted) container.after(el);
        container.innerHTML = [
            (showPreset || showName) ? pane('role', 'Role', 'badge', '', `${presetHTML}${nameHTML}`) : '',
            pane('agent', 'Agent', 'smart_toy', '', topHTML),
            pane('prompt', 'Prompt', 'description', '', `<textarea class="acf-prompt" rows="14" aria-label="Behavior prompt" placeholder="Describe the agent's role and behavior...">${escapeHtml(promptVal)}</textarea>`),
            pane('permissions', 'Permissions', 'lock', '', `${autoPermissionsHTML}${permEditorHTML(true)}`),
            pane('advanced', 'Advanced', 'tune', '', `<label>Flags <span style="color:var(--text-muted);font-weight:normal">(optional)</span>:${flagsInput}</label>`),
        ].join('');
        for (const [paneId, el] of adopted) container.querySelector(`[data-section="${CSS.escape(paneId)}"]`)?.prepend(el);
    } else container.innerHTML = `
        ${presetHTML}
        ${nameHTML}
        ${topHTML}
        <label>Behavior Prompt:
            <textarea class="acf-prompt" rows="4" placeholder="Describe the agent's role and behavior...">${escapeHtml(promptVal)}</textarea>
        </label>
        ${autoPermissionsHTML}
        <div class="permissions-section">
            <button type="button" class="permissions-toggle-btn" data-on-click="perms-toggle" data-target="${escapeAttr(uid + '-perms')}">
                <svg class="permissions-chevron" width="10" height="10" viewBox="0 0 16 16" fill="currentColor"><path d="M6 4l4 4-4 4z"/></svg>
                Permissions <span class="acf-perms-summary" style="color:var(--text-muted);font-weight:normal;font-size:0.6875rem">(${escapeHtml(permsSummary)})</span>
            </button>
            ${permEditorHTML(false)}
        </div>
        <details class="team-advanced acf-flags-details">
            <summary>Flags</summary>
            <div class="team-advanced-body">
                <label>Flags <span style="color:var(--text-muted);font-weight:normal">(optional)</span>:
                    ${flagsInput}
                </label>
            </div>
        </details>
    `;

    // Initialize permissions chips
    _setPermissions(`${uid}-perms`, caps);
    setThinking(container, v.thinking); // a form opened for a running agent shows the level it started with (#4969)
    setThinkingActual(container, v.thinking, v.thinkingActual); // and, under it, what the agent last ran at when that differs (#5105)
    _syncEnforcement(container);
    trackPermissionPick(container.querySelector('.acf-permission-mode')); // a mode picked here is the person's, whatever the agent type does next (#5836)
    container.querySelector('.acf-permission-mode')?.addEventListener('change', () => _syncEnforcement(container));
    container.querySelector('.perms-shell-input')?.addEventListener('input', () => _syncEnforcement(container));

    // Render presets if enabled — pass containerId so the preset handler knows which ACF to update
    if (showPreset) {
        _renderPresetButtons(`${uid}-presets`, 'acf', { target: containerId });
    }

    // Loaded values are classified against the provider's known defaults and
    // model catalog. Known values may be replaced on provider switch; custom
    // values remain user-owned. Hold pre-fill until classification completes.
    container.dataset.acfModelDirty = 'false';
    container.dataset.acfModelClassifying = modelVal ? 'true' : 'false';
    if (modelVal) _classifyLoadedACFModel(container, agentTypeVal, modelVal);

    // Any user keystroke / paste / clear marks the field dirty so subsequent
    // agent-type changes won't clobber it. Guard with e.isTrusted so our
    // programmatic assignments (pre-fill, setAgentConfig) don't trip it.
    const modelInput = container.querySelector('.acf-model');
    if (modelInput) {
        modelInput.addEventListener('input', (e) => {
            if (e.isTrusted) {
                container.dataset.acfModelClassifying = 'false';
                container.dataset.acfModelDirty = 'true';
            }
        });
    }

    // A dialog that keeps the form's sections behind "More options" can ask for the missing-CLI line to sit outside them (opts.cliSlot, an element id): the first run's
    // most common stop is a CLI that is not installed, and it was found only after pressing Launch (#5835).
    const cliSlot = opts.cliSlot && document.getElementById(opts.cliSlot);
    const cliLine = container.querySelector('.acf-cli-warning');
    if (cliSlot && cliLine) { cliSlot.replaceChildren(cliLine); container._acfCliWarning = cliLine; }
    // Populate datalist + apply visibility + pre-fill for the initial agent type.
    _syncACFModelField(container);
    _checkAgentCLI(container.querySelector('.acf-agent-type')?.value || 'claude', cliWarningOf(container));
    container.querySelector('.acf-agent-type')?.addEventListener('change', () => {
        const agentType = container.querySelector('.acf-agent-type')?.value || 'claude';
        _checkAgentCLI(agentType, cliWarningOf(container));
        // Still showing the default (nobody picked a mode here): it follows the agent type, Auto for Claude and the other CLIs' own default (#5836)
        syncDefaultPermissionMode(container.querySelector('.acf-permission-mode'), _settingsFor(_acfServer(container)), agentType);
        const permMode = container.querySelector('.acf-permission-mode')?.value || 'default';
        const flagsEl = container.querySelector('.acf-flags');
        if (flagsEl) flagsEl.value = _stripPermFlags(_normalizePermFlagsForAgent(flagsEl.value, agentType, permMode));
        _syncACFAutoPermissions(container);
        _syncACFModelField(container);
        _syncEnforcement(container);
    });
    if (opts.sections) { const shell = container.closest('.settings-shell'); if (shell) initSectionDialog(shell); } // the panes are in: build the section list
}

// _syncACFModelField reconciles the Model input with the currently selected
// agent type:
//   • fills the <datalist> with the canonical options for that type,
//   • hides the field entirely for the terminal agent,
//   • pre-fills the input with the user's default for that type, but only
//     when the user hasn't touched the field (dirty flag off).
export function _syncACFModelField(container) {
    if (!container) return;
    const typeSel = container.querySelector('.acf-agent-type');
    const modelWrap = container.querySelector('.acf-model-wrap');
    const modelInput = container.querySelector('.acf-model');
    const datalist = container.querySelector('datalist[id^="acf-models-"]');
    const agentType = typeSel?.value || 'claude';
    if (modelWrap) {
        modelWrap.style.display = (agentType === 'terminal') ? 'none' : '';
    }
    if (datalist) {
        _getAgentModels(_acfServer(container)).then(models => _fillModelDatalist(datalist, agentType, models));
    }
    wireModelPicker(container);
    wireThinking(container);
    loadModelPicker(container, agentType);   // the cached list at once, then the server's (#4066)
    if (!modelInput) return;
    if (container.dataset.acfModelClassifying === 'true' || container.dataset.acfModelDirty === 'true') return; // user owns or we are classifying the value
    if (agentType === 'terminal') {
        modelInput.value = '';
        syncModelPicker(container);
        return;
    }
    _getDefaultModels(_acfServer(container)).then(defaults => {
        // If the user started typing during the fetch, don't clobber their input.
        if (container.dataset.acfModelDirty === 'true' || container.dataset.acfModelClassifying === 'true') return;
        // Re-check the agent type — the user may have switched again mid-fetch.
        const currentType = typeSel?.value || 'claude';
        if (currentType === 'terminal') {
            modelInput.value = '';
            syncModelPicker(container);
            return;
        }
        modelInput.value = defaults[currentType] || '';
        syncModelPicker(container);
    });
}
window.renderAgentConfigForm = renderAgentConfigForm;

/**
 * Get current values from an AgentConfigForm.
 * @param {string} containerId - The container ID
 * @returns {object} { name, agentType, model, prompt, flags, capabilities }
 */
export function getAgentConfig(containerId) {
    const container = document.getElementById(containerId);
    if (!container) return {};
    const uid = container.dataset.acfUid;
    const agentType = container.querySelector('.acf-agent-type')?.value || 'claude';
    const permMode = container.querySelector('.acf-permission-mode')?.value || 'default';
    let flags = _stripPermFlags(container.querySelector('.acf-flags')?.value.trim() || '');
    if (permMode && permMode !== 'default') {
        flags = _normalizePermFlagsForAgent(flags, agentType, permMode);
    }

    // Terminal agents never carry a model — the field is hidden in the UI.
    const rawModel = container.querySelector('.acf-model')?.value.trim() || '';
    const model = agentType === 'terminal' ? '' : rawModel;

    return {
        name: container.querySelector('.acf-name')?.value.trim() || '',
        agentType,
        model,
        thinking: agentType === 'terminal' ? undefined : thinkingValue(container), // undefined: not chosen, so a launch uses the default and a restart keeps the agent's level (#4969)
        prompt: container.querySelector('.acf-prompt')?.value.trim() || '',
        flags,
        capabilities: uid ? _getPermissions(`${uid}-perms`) : null,
    };
}
window.getAgentConfig = getAgentConfig;

/**
 * Set/pre-fill values in an AgentConfigForm.
 * @param {string} containerId - The container ID
 * @param {object} values - { name, agentType, model, prompt, flags, capabilities }
 */
export function setAgentConfig(containerId, values) {
    const container = document.getElementById(containerId);
    if (!container) return;
    const uid = container.dataset.acfUid;
    const v = values || {};

    const nameEl = container.querySelector('.acf-name');
    if (nameEl) nameEl.value = v.name || '';
    const typeEl = container.querySelector('.acf-agent-type');
    if (typeEl) typeEl.value = v.agentType || v.agent_type || _settingsFor(_acfServer(container)).default_agent_type || 'claude';
    const modelEl = container.querySelector('.acf-model');
    if (modelEl) {
        modelEl.value = v.model || '';
        syncModelPicker(container);
        container.dataset.acfModelClassifying = v.model ? 'true' : 'false';
        container.dataset.acfModelDirty = 'false';
        if (v.model) _classifyLoadedACFModel(container, typeEl?.value || 'claude', v.model);
    }
    setThinking(container, v.thinking);
    setThinkingActual(container, v.thinking, v.thinkingActual);
    const promptEl = container.querySelector('.acf-prompt');
    if (promptEl) promptEl.value = v.prompt || '';
    const flagsEl = container.querySelector('.acf-flags');
    if (flagsEl) flagsEl.value = _stripPermFlags(v.flags || '');
    const permModeEl = container.querySelector('.acf-permission-mode');
    if (permModeEl) {
        // Extract permission mode from flags if present, otherwise use default from settings
        const flagMode = _permissionModeFromFlags(v.flags || '');
        const defaultMode = defaultPermissionMode(_settingsFor(_acfServer(container)), typeEl?.value || 'claude');
        if (flagMode) {
            permModeEl.value = flagMode;
            permModeEl.dataset.pickedByPerson = '1'; // a saved configuration's mode is a choice
        } else if (_hasPermFlag(v.flags || '')) {
            // Legacy --dangerously-skip-permissions flag → map to bypassPermissions
            permModeEl.value = 'bypassPermissions';
            permModeEl.dataset.pickedByPerson = '1';
        } else if (Object.prototype.hasOwnProperty.call(v, 'flags')) {
            permModeEl.value = 'default';
            permModeEl.dataset.pickedByPerson = '1';
        } else {
            permModeEl.value = defaultMode;
        }
    }

    if (uid) _setPermissions(`${uid}-perms`, v.capabilities || null);
    _syncEnforcement(container);

    // Re-sync Model combo and prerequisite warning after a preset/type change.
    _syncACFModelField(container);
    _checkAgentCLI(typeEl?.value || 'claude', cliWarningOf(container));
}
(window.__coral ||= {}).setAgentConfig = setAgentConfig;
