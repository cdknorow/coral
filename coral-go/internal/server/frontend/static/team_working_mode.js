import { escapeHtml } from './utils.js';

const PRESET_NAMES = { none: 'None', shared_checkout: 'Shared checkout', worktrees: 'Worktrees' };

function settingsFormSnapshot(form) {
    if (!form) return '';
    return JSON.stringify(Array.from(form.elements).filter(el => el.name).map(el => ({
        name: el.name, type: el.type, value: el.type === 'checkbox' ? el.checked : el.value
    })));
}

export function hasUnsavedTeamSettings() {
    const form = document.querySelector('#team-settings-workspace form');
    return Boolean(form?.dataset.initialState && settingsFormSnapshot(form) !== form.dataset.initialState);
}

/** True when leaving the team settings panel should be confirmed first. */
export function confirmTeamWorkspaceChange() {
    if (!hasUnsavedTeamSettings()) return true;
    return window.confirm('Team settings have unsaved changes. Leave without saving?');
}

/** Open the team: team view in the center, team tabs (settings first) in the sidebar. */
export function showTeamWorkingModeWorkspace(team, options = {}) {
    return import('./team_context.js').then(m => m.enterTeamContext(team, { tab: 'team-settings', restore: options.restore }));
}

function jsonError(response, fallback) {
    return response.json().catch(() => ({})).then(data => data.error || fallback);
}

function presetLabel(preset) {
    return `${preset.name || PRESET_NAMES[preset.id] || preset.id}${preset.builtin ? ' · built in' : ''}`;
}

function renderInspection(data, workingMode) {
    const roles = [['orchestrator', 'Orchestrator'], ['worker', 'Agent / worker'], ['task', 'Task defaults']];
    return roles.map(([key, label]) => {
        const role = data?.[key] || {};
        const fragment = (name, title) => `<details class="prompt-inspection-fragment"><summary>${title}</summary><pre>${escapeHtml(role[name] || 'Not configured')}</pre></details>`;
        if (key === 'task') {
            const defaultInst = role.default_instructions || '';
            const teamInst = workingMode?.instructions?.trim();
            const composed = defaultInst + (teamInst ? `\n\nTeam working instructions:\n${teamInst}` : '') + '\n\n[+ per-task extra when supplied at creation]';
            return `<article class="prompt-inspection-card"><h4>${label}</h4><p class="prompt-inspection-meta"><span>Scope: ${escapeHtml(role.scope || 'global')}</span><span>Applies: ${escapeHtml(role.applicability || '—')}</span></p>${fragment('default_instructions', 'Default task instructions')}<details class="prompt-inspection-fragment"><summary>Effective composed task preview (on first claim)</summary><pre>${escapeHtml(composed)}</pre></details></article>`;
        }
        return `<article class="prompt-inspection-card"><h4>${label}</h4><p class="prompt-inspection-meta"><span>Scope: ${escapeHtml(role.scope || 'global')}</span><span>Applies: ${escapeHtml(role.applicability || '—')}</span></p>${fragment('system_default', 'System default')}${fragment('action_default', 'Action default')}${fragment('override', 'Override')}${fragment('effective_system', 'Effective system')}${fragment('effective_action', 'Effective action')}</article>`;
    }).join('');
}

export async function showTeamWorkingMode(team, options = {}) {
    const workspaceMode = Boolean(options.workspace);
    document.getElementById(workspaceMode ? 'team-settings-workspace' : 'team-working-mode-dialog')?.remove();
    const dialog = document.createElement(workspaceMode ? 'section' : 'dialog');
    dialog.id = 'team-working-mode-dialog';
    if (workspaceMode) {
        dialog.id = 'team-settings-workspace';
        dialog.className = 'team-settings-workspace';
    } else dialog.className = 'team-availability-dialog';
    dialog.setAttribute('aria-labelledby', 'working-mode-title');
    const workspaceActions = workspaceMode
        ? ''  // sidebar panel: no close/back, the team context owns navigation
        : `<button type="button" class="modal-close-btn" aria-label="Close"><span class="material-icons" aria-hidden="true">close</span></button>`;
    dialog.innerHTML = `<header><div><h2 id="working-mode-title">Team settings</h2><p class="working-mode-team"></p></div>${workspaceActions}</header>
      <div class="working-mode-status" role="status" aria-live="polite"></div>
      <nav class="team-settings-tabs" aria-label="Team settings sections" role="tablist">
        <button type="button" role="tab" aria-selected="true" aria-controls="team-settings-workflow" data-settings-tab="workflow">Workflow</button>
        <button type="button" role="tab" aria-selected="false" aria-controls="team-settings-prompts" data-settings-tab="prompts">Prompt inspection</button>
        <button type="button" role="tab" aria-selected="false" aria-controls="team-settings-global" data-settings-tab="global">Global settings</button>
      </nav>
      <form class="working-mode-form">
      <section class="team-settings-panel active" id="team-settings-workflow" role="tabpanel" data-settings-panel="workflow" tabindex="0">
      <fieldset class="team-settings-section workflow-presets-section">
        <legend>Workflow instructions</legend>
        <p class="working-mode-hint">Choose the instructions captured on tasks when they are first claimed. Preset edits are local to this team.</p>
        <label>Preset<select name="preset"></select></label>
        <div class="workflow-preset-actions"><button type="button" class="btn preset-new">New preset</button><button type="button" class="btn preset-reset" hidden>Reset built-in</button></div>
        <label class="preset-name-field">Preset name<input name="preset_name" maxlength="80" autocomplete="off"></label>
        <label>Preset instructions<textarea name="preset_instructions" rows="5" maxlength="4096" placeholder="Instructions agents should follow"></textarea></label>
        <p class="working-mode-hint preset-state"></p>
        <button type="button" class="btn preset-save">Save preset</button>
      </fieldset>
      <fieldset class="team-settings-section">
        <legend>Team working mode</legend>
        <label>Working mode<select name="mode"><option value="none">None</option><option value="shared_checkout">Shared checkout</option><option value="worktrees">Worktrees</option></select></label>
        <label><input type="checkbox" name="dependency_guidance"> Include dependent-queue guidance</label>
        <label>Additional instructions<textarea name="custom_instructions" rows="4" placeholder="Team-specific conventions"></textarea></label>
        <p class="working-mode-hint">Working mode instructions are saved with each task on its first claim. Existing claimed tasks keep their instructions. This setting provides guidance; it does not create worktrees automatically. <a href="#" class="working-mode-docs-link" data-doc="teams">Docs: Team working modes</a></p>
      </fieldset>
      </section>
      <section class="team-settings-panel" id="team-settings-prompts" role="tabpanel" data-settings-panel="prompts" tabindex="0" hidden>
      <fieldset class="team-settings-section prompt-inspection-section">
        <legend>Prompt inspection</legend>
        <p class="working-mode-hint">Read-only view of the defaults, overrides, effective role prompts, and task defaults used by Coral. Per-task additions are only known when a task is created.</p>
        <div class="prompt-inspection-content" aria-live="polite">Loading prompt inspection…</div>
      </fieldset>
      </section>
      <section class="team-settings-panel" id="team-settings-global" role="tabpanel" data-settings-panel="global" tabindex="0" hidden>
      <fieldset class="team-settings-section global-settings-section">
        <legend>Global settings</legend>
        <label><input type="checkbox" name="board_health_monitor"> Run global board health monitor</label>
        <p class="working-mode-hint">Applies to all teams. Takes effect on next Coral server restart.</p>
      </fieldset>
      </section>
      <div class="team-settings-actions"><button type="submit" class="btn" disabled>Save settings</button><span>Saves selected working mode, additional guidance, and global health. Save preset instructions separately.</span></div>
      </form>`;
    dialog.querySelector('.working-mode-team').textContent = team;
    if (workspaceMode) {
        // Rendered into the sidebar's Team Settings tab (see team_context.js).
        document.getElementById('agentic-panel-team-settings')?.replaceChildren(dialog);
    } else {
        document.body.append(dialog);
        dialog.querySelector('header button').onclick = () => dialog.close();
        dialog.addEventListener('close', () => dialog.remove());
        dialog.addEventListener('click', event => { if (event.target === dialog) dialog.close(); });
        dialog.showModal();
    }

    dialog.addEventListener('click', event => {
        const docLink = event.target.closest('.working-mode-docs-link');
        if (docLink) {
            event.preventDefault();
            const docName = docLink.dataset.doc;
            if (window.switchNavTab) window.switchNavTab('docs');
            import('./docs.js').then(m => m.selectDoc(docName));
        }
    });

    const form = dialog.querySelector('form');
    const status = dialog.querySelector('[role=status]');
    const save = form.querySelector('[type=submit]');
    const presetSelect = form.elements.preset;
    const presetName = form.elements.preset_name;
    const presetInstructions = form.elements.preset_instructions;
    const presetState = dialog.querySelector('.preset-state');
    const presetReset = dialog.querySelector('.preset-reset');
    const presetSave = dialog.querySelector('.preset-save');
    const inspection = dialog.querySelector('.prompt-inspection-content');
    const tabs = [...dialog.querySelectorAll('[data-settings-tab]')];
    const panels = [...dialog.querySelectorAll('[data-settings-panel]')];
    const workingModeURL = `/api/board/${encodeURIComponent(team)}/working-mode`;
    const presetsURL = `/api/board/${encodeURIComponent(team)}/working-mode/presets`;
    const inspectionURL = `/api/settings/prompt-inspection?board=${encodeURIComponent(team)}`;
    let presets = [];
    let workingMode = null;
    let settingsLoaded = false;

    function activatePanel(name, moveFocus = false) {
        tabs.forEach(tab => {
            const active = tab.dataset.settingsTab === name;
            tab.setAttribute('aria-selected', String(active));
            tab.tabIndex = active ? 0 : -1;
        });
        panels.forEach(panel => {
            const active = panel.dataset.settingsPanel === name;
            panel.hidden = !active;
            panel.classList.toggle('active', active);
        });
        if (moveFocus) tabs.find(tab => tab.dataset.settingsTab === name)?.focus();
    }
    tabs.forEach((tab, index) => {
        tab.onclick = () => activatePanel(tab.dataset.settingsTab);
        tab.onkeydown = event => {
            if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
            event.preventDefault();
            const next = event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length;
            activatePanel(tabs[next].dataset.settingsTab, true);
        };
    });

    const selectedPreset = () => presets.find(p => p.id === presetSelect.value);
    function syncPresetForm() {
        const preset = selectedPreset();
        if (!preset) return;
        presetName.value = preset.name || PRESET_NAMES[preset.id] || preset.id;
        presetInstructions.value = preset.overridden
            ? (preset.instructions ?? '')
            : (preset.instructions ?? preset.default_instructions ?? '');
        presetName.readOnly = Boolean(preset.builtin);
        presetReset.hidden = !preset.builtin || !preset.overridden;
        presetSave.textContent = preset.builtin ? 'Save built-in override' : 'Save preset';
        presetState.textContent = preset.builtin
            ? (preset.overridden ? 'This built-in preset has a team-local override.' : 'This is the shipped built-in preset.')
            : 'This is a team-local custom preset.';
    }
    function renderPresets() {
        presetSelect.innerHTML = presets.map(p => `<option value="${escapeHtml(p.id)}">${escapeHtml(presetLabel(p))}</option>`).join('');
        const modeSelect = form.elements.mode;
        presets.filter(p => ![...modeSelect.options].some(option => option.value === p.id)).forEach(p => modeSelect.add(new Option(presetLabel(p), p.id)));
        if (workingMode?.mode && presets.some(p => p.id === workingMode.mode)) presetSelect.value = workingMode.mode;
        syncPresetForm();
    }
    function setStatus(message, error = false) {
        status.textContent = message;
        status.classList.toggle('is-error', error);
    }
    presetSelect.onchange = () => {
        syncPresetForm();
        if (presetSelect.value) form.elements.mode.value = presetSelect.value;
    };
    dialog.querySelector('.preset-new').onclick = () => {
        const id = `custom-${Date.now().toString(36)}`;
        presets.push({ id, name: 'New preset', instructions: '', builtin: false, isNew: true });
        renderPresets();
        presetSelect.value = id;
        syncPresetForm();
        form.elements.mode.value = id;
        presetName.focus();
    };
    presetSave.onclick = async () => {
        const preset = selectedPreset();
        if (!preset) return;
        const name = presetName.value.trim();
        const instructions = presetInstructions.value;
        if (!name) { setStatus('Preset name is required.', true); return; }
        if (!preset.builtin && !instructions.trim()) { setStatus('Preset instructions are required.', true); return; }
        presetSave.disabled = true;
        try {
            const isNew = Boolean(preset.isNew);
            const method = isNew ? 'POST' : 'PUT';
            const url = isNew ? presetsURL : `${presetsURL}/${encodeURIComponent(preset.id)}`;
            const body = preset.builtin ? { name, instructions } : { id: preset.id, name, instructions };
            const response = await fetch(url, { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
            if (!response.ok) throw new Error(await jsonError(response, 'Could not save preset.'));
            const result = await response.json();
            const saved = result.preset || result;
            preset.isNew = false;
            if (preset.builtin) preset.overridden = true;
            Object.assign(preset, saved, { name, instructions, builtin: Boolean(saved.builtin ?? preset.builtin) });
            renderPresets(); presetSelect.value = preset.id; syncPresetForm();
            setStatus('Workflow preset saved.');
        } catch (error) { setStatus(error.message, true); }
        finally { presetSave.disabled = false; }
    };
    presetReset.onclick = async () => {
        const preset = selectedPreset();
        if (!preset?.builtin) return;
        presetReset.disabled = true;
        try {
            const response = await fetch(`${presetsURL}/${encodeURIComponent(preset.id)}/reset`, { method: 'POST' });
            if (!response.ok) throw new Error(await jsonError(response, 'Could not reset preset.'));
            const result = await response.json();
            Object.assign(preset, result.preset || result, { overridden: false });
            syncPresetForm(); setStatus('Built-in preset reset to the shipped default.');
        } catch (error) { setStatus(error.message, true); }
        finally { presetReset.disabled = false; }
    };

    async function load() {
        setStatus('Loading…');
        try {
            const [modeResponse, presetsResponse, settingsResponse, inspectionResponse] = await Promise.all([
                fetch(workingModeURL), fetch(presetsURL), fetch('/api/settings'), fetch(inspectionURL),
            ]);
            if (!modeResponse.ok) throw new Error('Could not load working mode. Close and reopen to retry.');
            if (!presetsResponse.ok) throw new Error(await jsonError(presetsResponse, 'Could not load workflow presets.'));
            workingMode = await modeResponse.json();
            const presetData = await presetsResponse.json();
            presets = presetData.presets || [];
            renderPresets();
            form.elements.mode.value = workingMode.mode || 'none';
            form.elements.dependency_guidance.checked = Boolean(workingMode.dependency_guidance);
            form.elements.custom_instructions.value = workingMode.custom_instructions || '';
            if (settingsResponse.ok) {
                const settings = await settingsResponse.json();
                form.elements.board_health_monitor.checked = settings.settings?.board_health_monitor !== 'false';
                settingsLoaded = true;
            } else {
                form.elements.board_health_monitor.disabled = true;
                const hint = dialog.querySelector('.global-settings-section .working-mode-hint');
                if (hint) hint.textContent = 'Could not load global settings; changes to global settings are disabled.';
            }
            inspection.innerHTML = inspectionResponse.ok ? renderInspection(await inspectionResponse.json(), workingMode) : '<p class="working-mode-hint">Prompt inspection is unavailable.</p>';
            setStatus('');
            form.dataset.initialState = settingsFormSnapshot(form);
            save.disabled = false;
        } catch (error) { setStatus(error.message, true); inspection.textContent = 'Prompt inspection could not be loaded.'; }
    }
    form.onsubmit = async event => {
        event.preventDefault(); save.disabled = true; setStatus('Saving…');
        let modeError = null; let healthError = null;
        try {
            const response = await fetch(workingModeURL, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ mode: form.elements.mode.value, dependency_guidance: form.elements.dependency_guidance.checked, custom_instructions: form.elements.custom_instructions.value }) });
            if (!response.ok) modeError = await jsonError(response, 'Could not save working mode.');
            else workingMode = await response.json();
        } catch (error) { modeError = error.message; }
        if (settingsLoaded) {
            try {
                const response = await fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ board_health_monitor: form.elements.board_health_monitor.checked ? 'true' : 'false' }) });
                if (!response.ok) healthError = 'Could not save board health setting.';
            } catch (error) { healthError = error.message; }
        }
        if (!modeError && !healthError) setStatus('Saved. Working mode applies on first claim; global health monitor applies on server restart.');
        else if (modeError && healthError) setStatus(`Save failed: ${modeError}; ${healthError}`, true);
        else if (modeError) setStatus(`Working mode save failed: ${modeError}. Global health monitor setting was saved (applies on restart).`, true);
        else setStatus(`Working mode saved (applies on first claim). Could not save global health monitor setting: ${healthError}`, true);
        if (!modeError && !healthError) form.dataset.initialState = settingsFormSnapshot(form);
        save.disabled = false;
    };
    load();
}
