import { importOnce } from './import_once.js';
import { bindNativeDialog } from './modal_chrome.js';
import { switchRow } from './switch_row.js';
import { escapeHtml, escapeAttr, copyText, showToast } from './utils.js';
import { splitKey, keyLabel, serverUrl, isCloud } from './server_base.js';
const { boardRenameControl } = await importOnce('./board_rename.js'); // lazy modules load the others they need through importOnce, so a failed load is retried
const { refusalOf, answeredLocally } = await importOnce('./owner_refusal.js');
import { changedFields, rebaseChanges, NOTHING_TO_SAVE } from './settings_diff.js';

const PRESET_NAMES = { none: 'None', shared_checkout: 'Shared checkout', worktrees: 'Worktrees', merge_queue: 'Merge queue' };

function settingsFormSnapshot(form) {
    if (!form) return '';
    return JSON.stringify(Array.from(form.elements).filter(el => el.name).map(el => ({
        name: el.name, type: el.type, value: el.type === 'checkbox' ? el.checked : el.value
    })));
}

export function hasUnsavedTeamSettings() {
    // The main form, and the Health check tab's own (#4230): either with changes asks before leaving.
    return [...document.querySelectorAll('#team-settings-workspace form, #team-working-mode-dialog form')].some(form => Boolean(form.dataset.initialState && settingsFormSnapshot(form) !== form.dataset.initialState));
}

/** Resolves true when leaving the team settings panel may go ahead: nothing unsaved, or the user
 *  chose to leave in the confirm dialog (Cancel, Escape and a replaced dialog all resolve false). */
export function confirmTeamWorkspaceChange() {
    if (!hasUnsavedTeamSettings()) return Promise.resolve(true);
    return new Promise(resolve => window.showConfirmModal('Unsaved Team Settings', 'Team settings have unsaved changes. Leave without saving?',
        () => resolve(true), () => resolve(false)));
}

// The team's settings opened on its Health check tab (#4962): the icon beside the team's info button. The tab is added once the
// settings arrive, so the wish waits for it and is dropped if the team has none (Coral Hub leaves the tab out).
let wantHealthTab = false;
export function showTeamHealthSettings(team) {
    wantHealthTab = true;
    return showTeamWorkingModeWorkspace(team);
}

/** Open the team: team view in the center, team tabs (settings first) in the sidebar. */
export function showTeamWorkingModeWorkspace(team, options = {}) {
    return importOnce('./team_context.js').then(m => m.enterTeamContext(team, { tab: 'team-settings', restore: options.restore }));
}

function jsonError(response, fallback) {
    return response.json().catch(() => ({})).then(data => data.error || fallback);
}

// What the preset form shows for a preset's name and instructions.
const presetNameText = preset => preset.name || PRESET_NAMES[preset.id] || preset.id;
const presetInstructionsText = preset => preset.overridden
    ? (preset.instructions ?? '')
    : (preset.instructions ?? preset.default_instructions ?? '');

function presetLabel(preset) {
    return `${preset.name || PRESET_NAMES[preset.id] || preset.id}${preset.builtin ? ' · built in' : ''}`;
}

function renderInspection(data, workingMode) {
    const roles = [['orchestrator', 'Orchestrator'], ['worker', 'Agent / worker'], ['task', 'Task defaults']];
    // One role at a time: a page that scrolls once (UI audit #5589, finding 10). Each text is a labelled block, not a small scroll box; the texts behind the effective ones are folded.
    const block = (role, name, title, text) => `<h5>${title}</h5><pre>${escapeHtml(text ?? (role[name] || 'Not configured'))}</pre>`;
    // What the agent actually gets (the effective texts) is shown; the defaults and the override it came from are one click away, so the page starts short.
    const shown = (role, name, title, text) => `<section class="prompt-inspection-fragment">${block(role, name, title, text)}</section>`;
    const folded = (role, name, title, text) => `<details class="disclosure prompt-inspection-fragment prompt-inspection-folded"><summary>${title}</summary><pre>${escapeHtml(text ?? (role[name] || 'Not configured'))}</pre></details>`;
    const cards = roles.map(([key, label], index) => {
        const role = data?.[key] || {};
        const head = `<h4>${label}</h4><p class="prompt-inspection-meta"><span>Scope: ${escapeHtml(role.scope || 'global')}</span><span>Applies: ${escapeHtml(role.applicability || '—')}</span></p>`;
        let body;
        if (key === 'task') {
            const defaultInst = role.default_instructions || '';
            const teamInst = workingMode?.instructions?.trim();
            const composed = defaultInst + (teamInst ? `\n\nTeam working instructions:\n${teamInst}` : '') + '\n\n[+ per-task extra when supplied at creation]';
            body = `${shown(role, null, 'Effective composed task preview (on first claim)', composed)}${folded(role, 'default_instructions', 'Default task instructions')}`;
        } else {
            body = shown(role, 'effective_system', 'Effective system') + shown(role, 'effective_action', 'Effective action') + folded(role, 'system_default', 'System default') + folded(role, 'action_default', 'Action default') + folded(role, 'override', 'Override');
        }
        return `<article class="prompt-inspection-card" data-role="${escapeAttr(key)}"${index ? ' hidden' : ''}>${head}${body}</article>`;
    }).join('');
    const options = roles.map(([key, label]) => `<option value="${escapeAttr(key)}">${label}</option>`).join('');
    return `<div class="prompt-inspection-bar"><label>Show the prompts for <select class="prompt-inspection-role">${options}</select></label><button type="button" class="btn btn-small prompt-inspection-copy">Copy</button></div>${cards}`;
}

// The empty team settings: a section for the sidebar or a dialog, with its three tabs and the form (filled by the load).
function _teamSettingsShell(workspaceMode) {
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
        : `<button type="button" class="modal-close-btn" data-dialog-close aria-label="Close"><span class="material-icons" aria-hidden="true">close</span></button>`;
    dialog.innerHTML = `<header><div><h2 id="working-mode-title">Team settings</h2><p class="working-mode-team"></p></div>${workspaceActions}</header>
      <div class="working-mode-status" role="status" aria-live="polite"></div>
      <nav class="team-settings-tabs" aria-label="Team settings sections" role="tablist">
        <button type="button" role="tab" id="team-settings-tab-workflow" aria-selected="true" aria-controls="team-settings-workflow" tabindex="0" data-settings-tab="workflow">Workflow</button>
        <button type="button" role="tab" id="team-settings-tab-prompts" aria-selected="false" aria-controls="team-settings-prompts" tabindex="-1" data-settings-tab="prompts">Prompt inspection</button>
        ${'<button type="button" role="tab" id="team-settings-tab-messaging" aria-selected="false" aria-controls="team-settings-messaging" tabindex="-1" data-settings-tab="messaging">Messaging</button>'}
      </nav>
      <form class="working-mode-form">
      <section class="team-settings-panel active" id="team-settings-workflow" role="tabpanel" aria-labelledby="team-settings-tab-workflow" data-settings-panel="workflow" tabindex="0">
      <fieldset class="team-settings-section">
        <legend>Team working mode</legend>
        <label>Working mode<select name="mode" aria-describedby="working-mode-desc"><option value="none">None</option><option value="shared_checkout">Shared checkout</option><option value="worktrees">Worktrees</option></select></label>
        <p class="working-mode-hint working-mode-desc" id="working-mode-desc"></p>
        ${switchRow({ label: 'Include dependent-queue guidance', name: 'dependency_guidance' })}
        <label>Additional instructions<textarea name="custom_instructions" rows="4" placeholder="Team-specific conventions"></textarea></label>
        <p class="working-mode-hint">Working mode instructions are saved with each task on its first claim. Existing claimed tasks keep their instructions. This setting provides guidance; it does not create worktrees automatically. <a href="#" class="working-mode-docs-link" data-doc="teams">Docs: Team working modes</a></p>
      </fieldset>
      <details class="disclosure team-settings-section workflow-presets-section preset-editor">
        <summary>Edit presets</summary>
        <p class="working-mode-hint">Choose the instructions captured on tasks when they are first claimed. Preset edits are local to this team.</p>
        <label>Preset<select name="preset"></select></label>
        <div class="workflow-preset-actions"><button type="button" class="btn preset-new">New preset</button><button type="button" class="btn preset-reset" hidden>Reset built-in</button></div>
        <label class="preset-name-field">Preset name<input name="preset_name" maxlength="80" autocomplete="off"></label>
        <label>Preset instructions<textarea name="preset_instructions" rows="5" maxlength="4096" placeholder="Instructions agents should follow"></textarea></label>
        <p class="working-mode-hint preset-state"></p>
        <button type="button" class="btn preset-save">Save preset</button>
      </details>
      </section>
      <section class="team-settings-panel" id="team-settings-prompts" role="tabpanel" aria-labelledby="team-settings-tab-prompts" data-settings-panel="prompts" tabindex="0" hidden>
      <fieldset class="team-settings-section prompt-inspection-section">
        <legend>Prompt inspection</legend>
        <p class="working-mode-hint">Read-only view of the defaults, overrides, effective role prompts, and task defaults used by Coral. Per-task additions are only known when a task is created.</p>
        <div class="prompt-inspection-content" aria-live="polite">Loading prompt inspection…</div>
      </fieldset>
      </section>
      <div class="team-settings-actions"><button type="submit" class="btn" disabled>Save settings</button><span title="Saves the working mode and additional guidance. Save preset instructions separately."><span class="ts-caption-more">Saves the working mode and additional guidance. </span>Save preset instructions separately.</span></div>
      </form>
      ${'<section class="team-settings-panel" id="team-settings-messaging" role="tabpanel" aria-labelledby="team-settings-tab-messaging" data-settings-panel="messaging" tabindex="0" hidden><p class="working-mode-hint ct-loading" role="status">Loading…</p></section>'}`;
    return dialog;
}

// Puts the dialog on the page: the team's name and its rename control, the sidebar panel or a modal, and the link to the docs.
function _mountTeamSettings(dialog, team, workspaceMode) {
    dialog.querySelector('.working-mode-team').textContent = keyLabel(team);
    dialog.querySelector('.working-mode-team').dataset.team = team; // which team, as data (the text is its label)
    // Rename the board (#3536): its label only; the heading and every list follow.
    dialog.querySelector('header > div').append(boardRenameControl(team, {
        onRenamed: () => {
            dialog.querySelector('.working-mode-team').textContent = keyLabel(team);
            importOnce('./render.js').then(m => m.scheduleLiveSessionsRender()); // the board list follows on its next poll
        },
    }));
    if (workspaceMode) {
        // Rendered into the sidebar's Team Settings tab (see team_context.js).
        document.getElementById('agentic-panel-team-settings')?.replaceChildren(dialog);
    } else {
        document.body.append(dialog);
        bindNativeDialog(dialog); // Close (data-dialog-close), the backdrop, and removal on close
        dialog.showModal();
    }

    dialog.addEventListener('click', event => {
        const docLink = event.target.closest('.working-mode-docs-link');
        if (docLink) {
            event.preventDefault();
            const docName = docLink.dataset.doc;
            if (window.switchNavTab) window.switchNavTab('docs');
            importOnce('./lazy_panels.js').then(l => l.loadDocs()).then(m => m.selectDoc(docName)).catch(() => {});
        }
    });
}

// The tab strip: which panel shows, the click and the Arrow, Home and End keys. Returns the tabs and panels (the Health check tab adds one) and bindTab.
function _wireSettingsTabs(dialog, form) {
    const tabs = [...dialog.querySelectorAll('[data-settings-tab]')];
    const panels = [...dialog.querySelectorAll('[data-settings-panel]')];
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
        form.hidden = name === 'health' || name === 'messaging'; // these tabs have their own form and Save; the main Save bar is not for them (#4230, #5510)
        // On a phone the tab strip scrolls sideways: the selected tab is brought into view (the Health check tab is last).
        tabs.find(tab => tab.dataset.settingsTab === name)?.scrollIntoView({ inline: 'nearest', block: 'nearest' });
        if (moveFocus) tabs.find(tab => tab.dataset.settingsTab === name)?.focus();
    }
    function bindTab(tab) {
        tab.onclick = () => activatePanel(tab.dataset.settingsTab);
        tab.onkeydown = event => {
            if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
            event.preventDefault();
            const index = tabs.indexOf(tab);
            const next = event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length;
            activatePanel(tabs[next].dataset.settingsTab, true);
        };
    }
    tabs.forEach(bindTab);
    return { tabs, panels, bindTab };
}

export async function showTeamWorkingMode(team, options = {}) {
    const openHealthTab = wantHealthTab; // asked for by the health icon (#4962); taken now so a load that fails does not leave it for the next open
    wantHealthTab = false;
    const workspaceMode = Boolean(options.workspace);
    const dialog = _teamSettingsShell(workspaceMode);
    _mountTeamSettings(dialog, team, workspaceMode);

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
    // One role's prompts at a time; Copy takes the text of the one shown (UI audit #5589, finding 10).
    inspection.addEventListener('change', event => {
        if (!event.target.matches('.prompt-inspection-role')) return;
        for (const card of inspection.querySelectorAll('.prompt-inspection-card')) card.hidden = card.dataset.role !== event.target.value;
    });
    inspection.addEventListener('click', async event => {
        if (!event.target.closest('.prompt-inspection-copy')) return;
        const card = inspection.querySelector('.prompt-inspection-card:not([hidden])');
        if (!card) return;
        // Every text of the role, the folded ones too (innerText leaves out what a closed details hides).
        const parts = [card.querySelector('h4')?.textContent.trim() || ''];
        for (const fragment of card.querySelectorAll('.prompt-inspection-fragment')) parts.push(`${fragment.querySelector('h5, summary')?.textContent.trim() || ''}\n${fragment.querySelector('pre')?.textContent || ''}`);
        if (await copyText(parts.join('\n\n'))) showToast('Copied'); else showToast('Could not copy: select the text and copy it', true);
    });
    const { server: teamServer, name: teamName } = splitKey(team);
    const workingModeURL = serverUrl(`/api/board/${encodeURIComponent(teamName)}/working-mode`, teamServer);
    const presetsURL = serverUrl(`/api/board/${encodeURIComponent(teamName)}/working-mode/presets`, teamServer);
    const boardSettingsURL = serverUrl(`/api/board/${encodeURIComponent(teamName)}/settings`, teamServer);
    let healthMonitorOn = true; // Settings > Agents: "Allow team health checks (all teams)", on unless turned off
    const inspectionURL = serverUrl(`/api/settings/prompt-inspection?board=${encodeURIComponent(teamName)}`, teamServer);
    let presets = [];
    let workingMode = null;
    // What each part of the form showed when it loaded (#4423): Save sends only what the person changed since, so a
    // setting another client changed while the dialog was open is not put back.
    let opened = null;
    const modeValues = () => ({ mode: form.elements.mode.value, dependency_guidance: form.elements.dependency_guidance.checked, custom_instructions: form.elements.custom_instructions.value });

    const { tabs, panels, bindTab } = _wireSettingsTabs(dialog, form);
    // The Health check tab (#4230) exists only when the settings GET returned a "health" object: not through Coral Hub, which leaves it out.
    async function addHealthTab(health) {
        const m = await importOnce('./team_health_settings.js');
        const tab = document.createElement('button');
        tab.type = 'button'; tab.setAttribute('role', 'tab'); tab.setAttribute('aria-selected', 'false'); tab.setAttribute('aria-controls', 'team-settings-health');
        tab.dataset.settingsTab = 'health'; tab.id = 'team-settings-tab-health'; tab.tabIndex = -1; tab.textContent = 'Health check';
        const panel = document.createElement('section');
        panel.className = 'team-settings-panel'; panel.id = 'team-settings-health'; panel.setAttribute('role', 'tabpanel'); panel.setAttribute('aria-labelledby', 'team-settings-tab-health');
        panel.dataset.settingsPanel = 'health'; panel.tabIndex = 0; panel.hidden = true;
        // before the Messaging tab, which is drawn with the strip (#5518): the strip never changes after it appears, and its order is the tab order
        const strip = dialog.querySelector('.team-settings-tabs'), messaging = strip.querySelector('#team-settings-tab-messaging');
        strip.insertBefore(tab, messaging);
        form.after(panel);
        tabs.splice(messaging ? tabs.indexOf(messaging) : tabs.length, 0, tab); panels.push(panel); bindTab(tab);
        // The tab's form is drawn again after a save, so the note about the all-Coral switch (#5128) sits beside it, not in it.
        const host = document.createElement('div');
        panel.append(host);
        if (!healthMonitorOn) {
            const note = document.createElement('p');
            note.className = 'working-mode-hint health-global-off'; note.setAttribute('role', 'status');
            note.append('Team health checks are turned off for all of Coral, so this team\'s check will not run. Turn on "Allow team health checks (all teams)" in ');
            const link = document.createElement('button');
            link.type = 'button'; link.className = 'working-mode-settings-link'; link.textContent = 'Settings';
            link.addEventListener('click', () => {
                if (typeof dialog.close === 'function' && dialog.matches(':modal')) dialog.close(); // the Settings dialog opens over the page, not under this one
                window.showSettingsModal?.('agents');
            });
            note.append(link, '. It takes effect after Coral restarts.');
            panel.prepend(note);
        }
        await m.mountHealthSettings(host, team, health);
    }

    // The Messaging tab (#5510, #5516, #5518, #5572): cross-team messaging for this team. The tab and its panel are drawn with the strip (never through Coral Hub), so the strip does not change after it appears; the panel
    // is filled once the state is read, whether or not the list is enforced yet (the list is editable before it is in force), and says so when the state is not available.
    async function fillMessagingPanel(info) {
        const panel = dialog.querySelector('#team-settings-messaging');
        if (!panel) return;
        const m = await importOnce('./team_cross_team_settings.js');
        await m.mountCrossTeamSettings(panel, team, info || {});
    }

    const selectedPreset = () => presets.find(p => p.id === presetSelect.value);
    function syncPresetForm() {
        const preset = selectedPreset();
        if (!preset) return;
        presetName.value = presetNameText(preset);
        presetInstructions.value = presetInstructionsText(preset);
        // What the form opened with: Save applies only what the person changed to the preset as the server has it then.
        preset.openedForm = { name: presetName.value, instructions: presetInstructions.value };
        // Through Coral Hub a preset can be created but not changed or reset (the Hub refuses the PUT and the reset, #4274): those
        // buttons are not drawn, and an existing preset reads as read-only with the reason in its status line.
        const hubLocked = isCloud() && !preset.isNew;
        presetName.readOnly = Boolean(preset.builtin) || hubLocked;
        presetInstructions.readOnly = hubLocked;
        presetSave.hidden = hubLocked;
        presetReset.hidden = !preset.builtin || !preset.overridden || isCloud();
        presetSave.textContent = preset.builtin ? 'Save built-in override' : 'Save preset';
        presetState.textContent = preset.builtin
            ? (preset.overridden ? 'This built-in preset has a team-local override.' : 'This is the shipped built-in preset.')
            : 'This is a team-local custom preset.';
        if (hubLocked) presetState.textContent += ' Through Coral Hub a preset can be added but not changed: edit it in Coral on that computer.';
    }
    // One line under the Working mode list: what the chosen mode means, from the preset (the server's description; a team's own preset has none, and the line is empty).
    function syncModeDescription() {
        const preset = presets.find(p => p.id === form.elements.mode.value);
        const line = dialog.querySelector('#working-mode-desc');
        line.textContent = (preset && preset.description) || '';
        line.hidden = !line.textContent;
    }
    form.elements.mode.addEventListener('change', syncModeDescription);
    function renderPresets() {
        presetSelect.innerHTML = presets.map(p => `<option value="${escapeHtml(p.id)}">${escapeHtml(presetLabel(p))}</option>`).join('');
        const modeSelect = form.elements.mode;
        presets.filter(p => ![...modeSelect.options].some(option => option.value === p.id)).forEach(p => modeSelect.add(new Option(presetLabel(p), p.id)));
        if (workingMode?.mode && presets.some(p => p.id === workingMode.mode)) presetSelect.value = workingMode.mode;
        syncPresetForm();
        syncModeDescription();
    }
    function setStatus(message, error = false) {
        status.textContent = message;
        status.classList.toggle('is-error', error);
    }
    presetSelect.onchange = () => {
        syncPresetForm();
        if (presetSelect.value) form.elements.mode.value = presetSelect.value;
        syncModeDescription();
    };
    dialog.querySelector('.preset-new').onclick = () => {
        const id = `custom-${Date.now().toString(36)}`;
        presets.push({ id, name: 'New preset', instructions: '', builtin: false, isNew: true });
        renderPresets();
        presetSelect.value = id;
        syncPresetForm();
        form.elements.mode.value = id;
        syncModeDescription();
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
            // A PUT replaces name and instructions together, so a field the person left alone would put back the value this
            // form opened with over a change another client made meanwhile (a rename, an instructions edit): read the preset
            // again now and apply only what was changed here (#4423).
            let toSave = { name, instructions };
            if (!isNew && preset.openedForm) {
                try {
                    const again = await fetch(presetsURL);
                    const fresh = again.ok ? ((await again.json()).presets || []).find(p => p.id === preset.id) : null;
                    if (fresh) toSave = rebaseChanges(preset.openedForm, { name, instructions }, { name: presetNameText(fresh), instructions: presetInstructionsText(fresh) });
                } catch { /* save what the form holds */ }
            }
            const body = preset.builtin ? { name: toSave.name, instructions: toSave.instructions } : { id: preset.id, name: toSave.name, instructions: toSave.instructions };
            const response = await fetch(url, { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
            if (!response.ok) throw new Error(await jsonError(response, 'Could not save preset.'));
            const result = await response.json();
            const saved = result.preset || result;
            preset.isNew = false;
            if (preset.builtin) preset.overridden = true;
            Object.assign(preset, saved, { name: toSave.name, instructions: toSave.instructions, builtin: Boolean(saved.builtin ?? preset.builtin) });
            renderPresets(); presetSelect.value = preset.id; syncPresetForm();
            setStatus('Workflow preset saved.');
        } catch (error) { setStatus(error.message, true); }
        finally { presetSave.disabled = false; }
    };
    // Enter in the preset name box is "Save preset", not an implicit submit of the whole team settings form (the form's own Save button).
    presetName.addEventListener('keydown', event => {
        if (event.key !== 'Enter' || event.isComposing) return;
        event.preventDefault();
        if (!presetSave.hidden) presetSave.click();
    });
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
            const [modeResponse, presetsResponse, settingsResponse, inspectionResponse, boardSettingsResponse] = await Promise.all([
                fetch(workingModeURL), fetch(presetsURL), fetch('/api/settings'), fetch(inspectionURL),
                fetch(boardSettingsURL).catch(() => null),
            ]);
            if (!modeResponse.ok) throw new Error('Could not load working mode. Close and reopen to retry.');
            if (!presetsResponse.ok) throw new Error(await jsonError(presetsResponse, 'Could not load workflow presets.'));
            workingMode = await modeResponse.json();
            const presetData = await presetsResponse.json();
            presets = presetData.presets || [];
            renderPresets();
            form.elements.mode.value = workingMode.mode || 'none';
            syncModeDescription();
            form.elements.dependency_guidance.checked = Boolean(workingMode.dependency_guidance);
            form.elements.custom_instructions.value = workingMode.custom_instructions || '';
            // The all-Coral health switch lives in Settings (#5128); this dialog only reads it, for the Health check tab's note.
            if (settingsResponse.ok) healthMonitorOn = String((await settingsResponse.json()).settings?.board_health_monitor ?? '').trim().toLowerCase() !== 'false'; // the dialog stores a boolean as "False"
            if (boardSettingsResponse?.ok) {
                const boardSettings = await boardSettingsResponse.json();
                if (boardSettings.health && typeof boardSettings.health === 'object') await addHealthTab(boardSettings.health).catch(() => {});
                let crossTeam = null;
                { // the machine's own answer; through Coral Hub the route may not be served (then null and the switches stay as they were)
                    const stateResponse = await fetch(serverUrl(`/api/board/${encodeURIComponent(teamName)}/cross-team-state`, teamServer)).catch(() => null);
                    crossTeam = stateResponse?.ok ? await stateResponse.json().catch(() => null) : null;
                }
                await fillMessagingPanel({ state: crossTeam, settings: boardSettings }).catch(() => fillMessagingPanel({}));
                if (openHealthTab) document.getElementById('team-settings-tab-health')?.click();
            } else {
                // The board settings could not be loaded: through Coral Hub the owner-only settings can be refused (#5323): the Messaging tab says why, in our words (and the Health check tab is not drawn).
                await fillMessagingPanel({ refusal: boardSettingsResponse ? await refusalOf(boardSettingsResponse, answeredLocally(team)) : '' });
            }
            inspection.innerHTML = inspectionResponse.ok ? renderInspection(await inspectionResponse.json(), workingMode) : `<p class="working-mode-hint">Prompt inspection: ${escapeHtml(await refusalOf(inspectionResponse, answeredLocally(team)))}</p>`;
            setStatus('');
            form.dataset.initialState = settingsFormSnapshot(form);
            opened = { mode: modeValues() };
            save.disabled = false;
        } catch (error) { setStatus(error.message, true); inspection.textContent = 'Prompt inspection could not be loaded.'; fillMessagingPanel({}).catch(() => {}); }
    }
    form.onsubmit = async event => {
        event.preventDefault(); save.disabled = true; setStatus('Saving…');
        let modeError = null;
        const base = opened || { mode: {} };
        const mode = modeValues();
        const modeChanged = Object.keys(changedFields(base.mode, mode)).length > 0;
        if (!modeChanged) {
            setStatus(NOTHING_TO_SAVE);
            save.disabled = false;
            return;
        }
        if (modeChanged) {
            try {
                // The working mode is saved whole: what the server has now, with only the fields changed here.
                const current = await fetch(workingModeURL);
                if (!current.ok) throw new Error(await jsonError(current, 'Could not read the working mode to save it.'));
                const fresh = await current.json();
                const body = rebaseChanges(base.mode, mode, { mode: fresh.mode || 'none', dependency_guidance: Boolean(fresh.dependency_guidance), custom_instructions: fresh.custom_instructions || '' });
                const response = await fetch(workingModeURL, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
                if (!response.ok) modeError = await jsonError(response, 'Could not save working mode.');
                else { workingMode = await response.json(); base.mode = mode; }
            } catch (error) { modeError = error.message; }
        }
        opened = base;
        if (!modeError) setStatus('Saved. Working mode applies on first claim.');
        else setStatus(`Save failed: ${modeError}`, true);
        if (!modeError) form.dataset.initialState = settingsFormSnapshot(form);
        save.disabled = false;
    };
    load();
}
