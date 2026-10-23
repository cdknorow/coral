import { importOnce } from './import_once.js';
/* Modal management: launch and info dialogs */

import { registerActions, tokenOf, textOf } from './actions.js';
import { ensureBuilt } from './lazy_dom.js';

// The Launch, Info and Settings dialogs are built from their templates when this module loads, which is their first use
// (#4500): everything below finds them in the page. Settings' load-time writers (settings_core.js and others) find nothing
// until then, and showSettingsModal fills every field as it opens.
ensureBuilt('launch-modal');
ensureBuilt('info-modal');
ensureBuilt('settings-modal');
const { _togglePermChip, _togglePermissions } = await importOnce('./agent_config_form.js');
const settingsDialog = await importOnce('./settings_dialog.js');
const infoDialog = await importOnce('./info_dialog.js');
const flagShortcuts = await importOnce('./flag_shortcuts.js');
const launchCore = await importOnce('./launch_core.js');
const teamDialogs = await importOnce('./team_dialogs.js');
const personaIo = await importOnce('./persona_io.js');
const boardLaunches = await importOnce('./board_launches.js');
const launchDialog = await importOnce('./launch_dialog.js');
const { _applyACFPresetFor, _moveTeamAgent } = teamDialogs;
const { _renderTeamTemplateSelector, teamTemplateKey } = await importOnce('./team_templates_ui.js');
const { showTeamStep, teamTabKey } = await importOnce('./team_steps.js');
const { _reviewGeneratedTeam } = await importOnce('./team_wizard.js'); // loads the wizard and its window hooks (#5174)
const { _selectAgentPreset } = personaIo;
export const { showLaunchModal, launchSession } = launchDialog;
export const { showAddAgentToBoard, hideAddAgentBoardModal, launchAgentToBoard, launchTerminalToBoard, launchDefaultAgent, showAddStandaloneAgent, launchStandaloneTerminal } = boardLaunches;
export const { exportPersonas, importPersonas, exportTeamTemplates, importTeamTemplates } = personaIo;
export const { _launchOn, hideLaunchModal } = launchCore; // _launchOn is exported for board_ids_browser
export const { toggleFlag } = flagShortcuts;
export const { showInfoModal, hideInfoModal, copyInfoCommand } = infoDialog;
export const { showSettingsModal, hideSettingsModal, applySettings } = settingsDialog;
// Kept on modals.js's surface (app.js and others import them from here).
export { loadSettings, applyTheme } from './settings_core.js';


// ── Mobile Connect Modal ─────────────────────────────────────────────────

// The mobile QR dialog loads on first use (#3140).
window._showMobileConnectModal = (...args) => importOnce('./mobile_connect.js', 'the mobile connect dialog').then(m => m.showMobileConnectModal(...args), () => {});
window._hideMobileConnectModal = (...args) => importOnce('./mobile_connect.js').then(m => m.hideMobileConnectModal(...args), () => {});

// The dialogs' generated markup (#3645): data-on-* names, never code. Values are checked; a bad
// one does nothing. (The job dialog's own flag buttons and agent select register in app.js: they
// exist before this module loads.)
const teamRow = el => el.closest('.team-agent-row');
registerActions({
    'team-generated-review': () => _reviewGeneratedTeam(),
    'info-copy': el => {
        const cmd = textOf(el.dataset.cmd, 8192);
        if (!cmd) return;
        navigator.clipboard.writeText(cmd);
        el.textContent = 'Copied!';
        setTimeout(() => { el.textContent = 'Copy'; }, 1500);
    },
    'preset-pick': el => {
        const v = textOf(el.value);
        if (v && el.dataset.kind === 'agent') _selectAgentPreset(v);
        else if (v && el.dataset.kind === 'acf') { const t = tokenOf(el.dataset.target); if (t) _applyACFPresetFor(t, v); }
        el.value = '';
    },
    'perm-chip-toggle': el => _togglePermChip(el),
    'perm-mode-describe': el => window._updatePermModeDescription(el),
    'perms-toggle': el => { const t = tokenOf(el.dataset.target); if (t) _togglePermissions(t); },
    'team-agent-edit': (el, event) => { event.stopPropagation(); const r = teamRow(el); if (r) window.showTeamAgentModal(r); },
    'team-agent-move': (el, event) => { event.stopPropagation(); if (el.dataset.dir === 'up' || el.dataset.dir === 'down') _moveTeamAgent(el, el.dataset.dir === 'up' ? -1 : 1); },
    'team-agent-remove': (el, event) => { event.stopPropagation(); teamDialogs._removeTeamAgent(teamRow(el)); },
    'team-agent-undo': () => teamDialogs._undoTeamAgentRemoval(),
    'team-agent-prompt': (el, event) => { event.stopPropagation(); teamDialogs._toggleTeamAgentPrompt(teamRow(el)); },
    'team-step': el => { if (['team', 'members', 'start'].includes(el.dataset.step)) { if (el.dataset.step === 'members') teamDialogs._refreshTeamSummaries(); showTeamStep(el.dataset.step); } },
    'team-step-next': () => teamDialogs.teamStepMove(1),
    'team-step-back': () => teamDialogs.teamStepMove(-1),
    'team-tab-key': (el, event) => { const to = teamTabKey(event); if (to) { event.preventDefault(); if (to === 'members') teamDialogs._refreshTeamSummaries(); showTeamStep(to, { focus: true }); } },
    'team-template-pick': el => { const n = textOf(el.dataset.name, 200); if (n) window._loadTeamTemplateFromSelect(n); },
    'team-template-filter': el => _renderTeamTemplateSelector(textOf(el.value, 200)),
    'team-template-key': (el, event) => teamTemplateKey(event),
});
