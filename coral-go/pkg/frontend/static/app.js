import './update_banner.js'; // first: it wraps fetch before the other modules ask for anything (#3751)
import { importOnce } from './import_once.js';
import { loadUpdatesFlag } from './update_flag.js';
import { startChatPrefetch } from './chat_page.js';
import './hub_blocked_controls.js'; // dims controls the Hub tier does not allow (#3769)
// Load order only (#4996): the imports below with no names are loaded for what the module registers at load, in the order this module imported them before the split (the startup-sequence golden records it).
// Load order only (#4996): the imports below with no names are loaded for what the module registers at load, in the order this module imported them before the split (the startup-sequence golden records it).
import './session_rows.js';
import './branch_expand.js'; // the capped branch name in the agent header opens in place (#5142)
import './nav_tabs.js'; // the tab row, its More menu and the phone's view dropdown (#3816)
import { initPrerequisiteChecks } from './prerequisites.js';
import './system_status.js';
import { beginDashboardAnalytics, dashboardInitialized, dashboardSessionsLoaded, dashboardFailed } from './dashboard_analytics.js';
import { initAgentUI } from './agent_ui.js';
import { showTeamAvailabilityWorkspace } from './team_availability.js';
/* Coral Dashboard — Entry Point */

import { state } from './state.js';
import './local_store.js';
import { loadLiveSessions } from './api.js';
import { deserializeFromUrl, urlSearchFor } from './search_filters.js';
import { connectCoralWs } from './websocket.js';
import { addMacro, hideMacroModal, restartSession, hideRestartModal, confirmRestart, editGoal, refreshGoal, requestGoal } from './controls.js';
import { selectLiveSession, selectHistorySession, editAndResubmit, renameAgent, setAgentIcon } from './sessions.js';
import { KILL_LOG_NOTE_MANY, toggleGroupCollapse, killGroup, killBoard, toggleTeamSleep, toggleAgentSleep, toggleAgentArchive, toggleArchivedSection, shareAgentTeam, saveTeamFromSidebar, killSessionDirect, showInfoDirect, attachDirect, restartDirect, showConfirmModal, hideConfirmModal, showPromptModal, hidePromptModal, showAlertModal, hideAlertModal, copyFolderPath, moveGroupUp, moveGroupDown, toggleGroupByTeam, expandSleepingGroup, setBoardAccentColor, moveSessionUp, moveSessionDown, showTeamTokenUsage, distillTeamKnowledge, toggleSessionSummary, applySessionSummaryPref, filterLiveAgents, focusAgentSearch, agentSearchKey, toggleShowServer, toggleShowBranches, toggleShowProvider, toggleShowModel, toggleShowThinking, toggleGroupByServer, groupHeaderKey, teamHeaderKey } from './render.js';
import './capture.js';
import { loadSettings } from './settings_core.js';
import { initEditions } from './editions.js';
import './version_line.js';
import './modal_chrome.js'; // modal backdrop/Escape/Tab handling for every dialog (#3140)
import { attachAllDialogFocus } from './dialog_focus.js';
import './board_choice.js'; // the duplicate-name choice used by the lazy modals.js (#3538): loaded at startup so a launch never fails on it
import './dialog_focus.js'; // focus into a dialog and back to its opener (accessibility pass); used by lazy modals.js
import { toggleBrowser, browserNavigateTo, browserNavigateUp, browserSelectRecent, browserUse } from './browser.js';
import { switchJobsSubtab, toggleAgenticPanel, toggleSidebarRail } from './sidebar.js';
import { connectTerminalWs, disconnectTerminalWs } from './xterm_renderer.js';
import { saveNotes, generateSummary, toggleNotesEdit, cancelNotesEdit, switchHistoryTab } from './notes.js';
import { addTagToSession, removeTagFromSession, showTagDropdown, hideTagDropdown, createTag } from './tags.js';
import { loadMoreBoardTasks, showTaskDetailModal, hideTaskDetailModal, showCreateTaskModal, hideCreateTaskModal, submitCreateTask, enableTaskEditMode, saveTaskEdit, cancelTaskEdit, completeBoardTask, cancelBoardTask, _doCompleteTask, _doCancelTask, _restoreTaskFooter, publishBoardTask, nudgeBoardTask, remindBoardTask, stopBoardTaskReminder } from './tasks.js';
import { openFilePreview, openFileEdit, refreshChangedFiles, copyFilePath } from './changed_files.js';
import './file_mention.js';
import './command_mention.js';
import './agent_notes.js';
import { loadCustomViews, activateCustomView } from './custom_views.js';
import { initTabCustomize } from './tab_customize.js';
import { startNav, go, note, onRoute, currentRoute } from './nav.js';
import { liveRoute, keyRoute, routeKey, AGENT_SUBTABS, parse as parseRoute } from './routes.js';
import { initPopout, applyPopoutBodyClass, popoutRetry } from './popout.js';
import { setLiveViewMode, showTerminalView } from './live_chat.js';
import { switchAgenticTab } from './agentic_state.js';
import { copyBranchName, showView, showToast, viewChanges, buildWelcome } from './utils.js';
import './preview_pane.js';
import { initScheduler, showJobModal, hideJobModal, validateCronPreview, saveScheduledJob, switchJobType, pickSchedulePreset } from './scheduler.js';
import { initLiveJobs, selectLiveJobRun } from './live_jobs.js';
import { initMessageBoard, showMessageBoardProjects, selectBoardProject, postBoardMessage, deleteMessageBoardProject, toggleBoardPause, showExportBoardModal, toggleSelectMode, openBoardSubscribers, hideBoardSubscribers } from './message_board.js';
import { loadAllFolderTags, showFolderTagDropdown, addFolderTag, removeFolderTag, createAndAddFolderTag } from './folder_tags.js';
import { loadPinnedFolders, pinFolder, unpinFolder, showOpenFolderModal, hideOpenFolderModal, submitOpenFolder } from './pinned_folders.js';
import { initMobile, syncMobileAgentList } from './mobile.js';
import './platform/detect.js';
import './platform/native.js';
import './platform/macos.js';
import './platform/windows.js';
import './platform/browser.js';
import './chat_find.js';

import { checkForUpdates, dismissUpdateToast } from './update_check.js';
import { teamFetch, sessionServer, keyLabel } from './server_base.js';
import { showServersModal, initServersUi } from './servers.js';
import { initHubIndicator } from './hub_indicator.js';
import './servers_tab.js'; // Servers tab: chip clicks and deep links (#3070)
import './inbox_tab.js'; // Inbox tab (hub only): its visibility and badge (#4718)
import { toggleAllGroups } from './group_collapse.js'; // the list header's one Collapse / Expand toggle
import { toggleDisplayPanel, displayPanelKey } from './display_panel.js'; // the Display expander
import { registerActions, tokenOf } from './actions.js'; // delegated data-on-* handlers, replacing inline ones (#3645)
import { markReady } from './ready.js';
import { lazyFns, lazyWindowFns, loadTeamContext, loadTeamSettings, loadWorkflows, loadCostDashboard, loadDocs, loadThemeConfig, loadModals, loadResumeDialog, loadDefaultPromptsDialog } from './lazy_panels.js';

// Panels off the landing view load on first use (#3083 phase 2, lazy_panels.js).
const {
    showWorkflowsTab, selectWorkflow, selectWorkflowRun, killWorkflowRun, deleteWorkflow,
    showWorkflowCreateModal, hideWorkflowCreateModal, editWorkflowWithAgent, workflowAddStep,
    workflowStepTypeChanged, saveWorkflow, workflowsBackToList, launchWorkflowAgent,
} = lazyFns(loadWorkflows, ['showWorkflowsTab', 'selectWorkflow', 'selectWorkflowRun', 'killWorkflowRun', 'deleteWorkflow',
    'showWorkflowCreateModal', 'hideWorkflowCreateModal', 'editWorkflowWithAgent', 'workflowAddStep',
    'workflowStepTypeChanged', 'saveWorkflow', 'workflowsBackToList', 'launchWorkflowAgent']);
const { showCostDashboard, _refreshCostDashboard, _costTimeRangeChanged } =
    lazyFns(loadCostDashboard, ['showCostDashboard', '_refreshCostDashboard', '_costTimeRangeChanged']);
// The board view's header, composer and subscribers sheet (#3645). Pause and deleting the board
// behave as before: the board's delete still asks first (deleteMessageBoardProject).
registerActions({
    'mb-subscribers': () => openBoardSubscribers(),
    'mb-subscribers-close': () => hideBoardSubscribers(),
    'mb-subscribers-backdrop': (el, event) => { if (event.target === el) hideBoardSubscribers(); },
    'mb-select-mode': () => toggleSelectMode(),
    'mb-export': () => showExportBoardModal(),
    'mb-pause': () => toggleBoardPause(),
    'mb-delete-board': () => deleteMessageBoardProject(),
    'mb-post': () => postBoardMessage(),
    // Enter sends, Shift+Enter is a new line.
    'mb-post-key': (el, event) => { if (event.key === 'Enter' && !event.shiftKey) { event.preventDefault(); postBoardMessage(); } },
});
// The job dialog's flag shortcuts and agent select (#3645): a shortcut toggles its flag in its
// flags input; the permission shortcut (flag-perm, set by _updatePermFlagButtons when the agent
// changes) sets that agent's permission flags for the saved mode. Values are checked.
registerActions({
    'flag-toggle': el => {
        const input = tokenOf(el.dataset.flagInput), flag = el.dataset.flag || '';
        if (input && /^--[a-z0-9-]+$/.test(flag)) window.toggleFlag(input, flag);
    },
    'flag-perm': el => {
        const input = tokenOf(el.dataset.flagInput), type = tokenOf(el.dataset.agentType), mode = tokenOf(el.dataset.mode);
        if (input && type && mode) window._setPermFlagsForInput(input, type, mode);
    },
    'flag-agent-change': el => { if (window._updatePermFlagButtons) window._updatePermFlagButtons(el); },
});
// The dialogs in modals.html (#3645 slice 7b): their template is there before the dialogs
// module loads, so their actions register here and call the same globals the inline handlers did.
// A literal argument comes from data-arg and must be one the markup uses.
registerActions({
    'm-add-macro': el => { window.addMacro(); },
    'm-apply-settings': el => { window.applySettings(); },
    'm-browser-navigate-up': el => { window.browserNavigateUp(); },
    'm-browser-use': el => { window.browserUse(); },
    'm-confirm-restart': el => { window.confirmRestart(); },
    'm-confirm-save-template': el => { window._confirmSaveTemplate(); },
    'm-copy-info-command': el => { window.copyInfoCommand(); },
    'm-export-personas': el => { window.exportPersonas(); },
    'm-generate-team-from-wizard': el => { window.generateTeamFromWizard(); },
    'm-handle-team-folder-import': el => { _handleTeamFolderImport(el); },
    'm-hide-add-agent-board-modal': el => { window.hideAddAgentBoardModal(); },
    'm-hide-confirm-modal': el => { window.hideConfirmModal(); },
    'm-hide-create-task-modal': el => { window.hideCreateTaskModal(); },
    'm-hide-default-prompts-modal': el => { window.hideDefaultPromptsModal(); },
    'm-hide-info-modal': el => { window.hideInfoModal(); },
    'm-hide-job-modal': el => { window.hideJobModal(); },
    'm-hide-launch-modal': el => { window.hideLaunchModal(); },
    'm-hide-launch-modal-then-show-job-modal': el => { window.hideLaunchModal(); window.showJobModal(); },
    'm-hide-macro-modal': el => { window.hideMacroModal(); },
    'm-hide-mobile-connect-modal': el => { window._hideMobileConnectModal(); },
    'm-hide-open-folder-modal': el => { window.hideOpenFolderModal(); },
    'm-hide-prompt-modal': el => { window.hidePromptModal(); },
    'm-hide-restart-modal': el => { window.hideRestartModal(); },
    'm-hide-resume-modal': el => { window.hideResumeModal(); },
    'm-hide-save-template-modal': el => { window._hideSaveTemplateModal(); },
    'm-hide-settings-modal': el => { window.hideSettingsModal(); },
    'm-hide-settings-modal-then-show-default-prompts-modal': el => { window.hideSettingsModal(); window.showDefaultPromptsModal(); },
    'm-hide-settings-modal-then-show-theme-configurator': el => { window.hideSettingsModal(); window.showThemeConfigurator(); },
    'm-hide-task-detail-modal': el => { window.hideTaskDetailModal(); },
    'm-hide-team-agent-modal': el => { window.hideTeamAgentModal(); },
    'm-hide-team-wizard-modal': el => { window.hideTeamWizardModal(); },
    'm-hide-view-modal': el => { window._hideViewModal(); },
    'm-import-personas': el => { window.importPersonas(); },
    'm-import-team-from-folder': el => { _importTeamFromFolder(); },
    'm-import-team-templates': el => { window.importTeamTemplates(); },
    'm-launch-agent-to-board': el => { window.launchAgentToBoard(); },
    'm-launch-session': el => { window.launchSession(); },
    'm-launch-team': el => { window.launchTeam(); },
    'm-load-team-template-from-select': el => { window._loadTeamTemplateFromSelect(el.value); },
    'm-mobile-connect-backdrop': (el, event) => { if (event.target === el) window._hideMobileConnectModal(); },
    'm-mobile-connect-copy': el => { const u = document.getElementById('mobile-connect-url'); if (!u) return; navigator.clipboard.writeText(u.textContent); el.textContent = 'Copied!'; setTimeout(() => { el.textContent = 'Copy'; }, 1500); },
    'm-on-agent-board-change': el => { window._onAgentBoardChange(); },
    'm-on-launch-server-change': el => { window._onLaunchServerChange(el.value); },
    'm-open-folder-backdrop': (el, event) => { if (event.target === el) window.hideOpenFolderModal(); },
    'm-open-folder-key': (el, event) => { if (event.key === 'Enter') { event.preventDefault(); window.submitOpenFolder(); } },
    'm-perm-mode-describe': el => { if (window._updatePermModeDescription) window._updatePermModeDescription(el); },
    'm-pick-schedule-preset': el => { window.pickSchedulePreset(el); },
    'm-reset-default-prompt': el => { if (["orchestrator", "team_orchestrator", "team_worker", "worker"].includes(el.dataset.arg)) { window.resetDefaultPrompt(el.dataset.arg); } },
    'm-resume-launch-new': el => { window.resumeLaunchNew(); },
    'm-save-current-persona': el => { window._saveCurrentPersona(); },
    'm-save-default-prompts': el => { window.saveDefaultPrompts(); },
    'm-save-scheduled-job': el => { window.saveScheduledJob(); },
    'm-save-team-agent-from-modal': el => { window.saveTeamAgentFromModal(); },
    'm-save-team-template': el => { window._saveTeamTemplate(); },
    'm-save-view': el => { window._saveView(); },
    'm-agent-step': el => { if (['folder', 'details'].includes(el.dataset.arg)) window._launchStage(el.dataset.arg); },
    'm-select-launch-type': el => { if (["agent", "back", "team", "terminal"].includes(el.dataset.arg)) { window._selectLaunchType(el.dataset.arg); } },
    'm-show-mobile-connect-modal': el => { window._showMobileConnectModal(); },
    'm-show-open-folder-modal': el => { window.showOpenFolderModal(); },
    'm-show-team-agent-modal': el => { window.showTeamAgentModal(); },
    'm-show-team-wizard-modal': el => { window.showTeamWizardModal(); },
    'm-submit-create-task': el => { window.submitCreateTask(); },
    'm-submit-open-folder': el => { window.submitOpenFolder(); },
    'm-switch-job-type': el => { if (["prompt", "workflow"].includes(el.dataset.arg)) { window.switchJobType(el.dataset.arg); } },
    'm-team-worktree': el => { const r = document.getElementById('team-worktree-branch-row'); if (r) r.style.display = el.checked ? '' : 'none'; },
    'm-test-team-wizard': el => { window.testTeamWizard(); },
    'm-toggle-browser': el => { const ids = String(el.dataset.browser || '').split(' '); if (ids.length === 4 && ids.every(i => /^[a-z0-9-]+$/.test(i))) window.toggleBrowser(...ids); },
    'm-toggle-group-by-team': el => { window.toggleGroupByTeam(); },
    'm-toggle-session-summary': el => { window.toggleSessionSummary(); },
    'm-validate-cron-preview': el => { window.validateCronPreview(); },
    'm-validate-team-name': () => { if (window._validateTeamName) window._validateTeamName(); },
    'm-verify-all-c-l-is': el => { window._verifyAllCLIs(); },
});
// The page shell in index.html (#3645 slice 8): top bar, nav tabs, settings menu, banners,
// the empty state and the mobile tabs. A literal argument comes from data-arg and must be one
// the markup uses.
registerActions({
    'docs-copy-agent-line': () => { importOnce('./docs.js').then(m => m.copyDocsLine()).catch(() => {}); }, // the Docs page's button, in the page from the start; docs.js loads with the Docs tab
    'm-close-top-bar-settings-then-check-for-updates': () => { window.__coral.closeTopBarSettings(); window.checkForUpdates(true); },
    'm-close-top-bar-settings-then-show-saved': () => { window.__coral.closeTopBarSettings(); importOnce('./saved.js').then(m => m.openSavedAll()).catch(() => {}); },
    'm-close-top-bar-settings-then-show-servers-modal': () => { window.__coral.closeTopBarSettings(); window.showServersModal(); },
    'm-close-top-bar-settings-then-show-settings-modal': () => { window.__coral.closeTopBarSettings(); window.showSettingsModal(); },
    'm-close-top-bar-settings-then-show-theme-configurator': () => { window.__coral.closeTopBarSettings(); window.showThemeConfigurator(); },
    'm-close-top-bar-settings-then-stop-all-auto-accept': () => { window.__coral.closeTopBarSettings(); window.stopAllAutoAccept(); },
    'm-dismiss-auto-wake': () => { window.dismissAutoWake(); },
    'm-end-leftover-tmux': () => { window.endLeftoverTmux(); },
    'm-dismiss-default-notice': () => { importOnce('./terminal_backend.js').then(m => m.dismissDefaultNotice()); },
    'm-open-terminal-settings': () => { window.showSettingsModal('terminal'); },
    'm-go-home': () => { window.__coral._goHome(); },
    'saved-close': () => { importOnce('./saved.js').then(m => m.closeSaved()).catch(() => {}); }, // the X in the Saved sidebar (#4742)
    'm-show-saved': () => { importOnce('./saved.js').then(m => m.openSavedAll({ toggle: true })).catch(() => {}); }, // the Saved list loads the first time it is opened (#4510)
    'm-mobile-connect-open': (el, event) => { event.preventDefault(); window._showMobileConnectModal(); },
    'm-show-launch-modal-then-select-launch-type': el => { if (["agent", "team"].includes(el.dataset.arg)) { window.showLaunchModal(); window._selectLaunchType(el.dataset.arg); } },
    'm-switch-mobile-tab': el => { if (["agents", "board", "chat"].includes(el.dataset.arg)) { window.switchMobileTab(el.dataset.arg); } },
    'm-switch-nav-tab': el => { if (["agents", "artifacts", "board", "docs", "history", "hub-inbox", "jobs", "servers", "tokens", "workflows"].includes(el.dataset.arg)) { window.switchNavTab(el.dataset.arg); } },
    'm-tmux-banner-close': () => { const b = document.getElementById('tmux-missing-banner'); if (b) b.style.display = 'none'; },
    'm-toggle-agentic-panel': () => { window.toggleAgenticPanel(); },
    'm-toggle-tablet-sidebar': () => { window.toggleTabletSidebar(); },
    'm-toggle-top-bar-settings': (el, event) => { window.__coral.toggleTopBarSettings(event); },
    'm-wake-pending-agents': () => { window.wakePendingAgents(); },
});
// The sidebar's own controls (#3645): rail, search, launch, display toggles, the Jobs subtabs.
registerActions({
    'sidebar-rail-toggle': () => window.toggleSidebarRail(),
    'agent-search-focus': () => window.focusAgentSearch(),
    'launch-show': () => window.showLaunchModal(),
    'agent-search-filter': el => window.filterLiveAgents(el.value),
    'agent-search-key': (el, event) => window.agentSearchKey(event),
    'toggle-session-summary': () => window.toggleSessionSummary(),
    'groups-toggle-all': () => toggleAllGroups(),
    'display-panel-toggle': el => toggleDisplayPanel(el),
    'display-panel-key': (el, event) => displayPanelKey(event),
    'toggle-show-server': () => window.toggleShowServer(),
    'toggle-show-branches': () => window.toggleShowBranches(),
    'toggle-show-provider': () => window.toggleShowProvider(),
    'toggle-show-model': () => window.toggleShowModel(),
    'toggle-show-thinking': () => window.toggleShowThinking(),
    'toggle-group-by-server': () => window.toggleGroupByServer(),
    'jobs-subtab': el => { if (el.dataset.tab === 'active' || el.dataset.tab === 'scheduled') window.switchJobsSubtab(el.dataset.tab); },
});
// The live agent view's header, view toggle, side panel tabs and command row (#3645). Restart is
// destructive (actions.js DESTRUCTIVE_ACTIONS: a popout hides it) and still asks first
// (restartSession's own confirmation). A side panel tab must be one the panel has.
const AGENTIC_TABS = new Set(['board', 'files', 'browse', 'artifacts', 'team-view', 'team-settings', 'team-artifacts', 'knowledge', 'tasks', 'notes', 'activity', 'agent-ui', 'saved', 'subscribers']);
registerActions({
    'mobile-back': () => window.mobileBack(),
    'goal-refresh': () => window.refreshGoal(),
    'goal-edit': () => window.editGoal(),
    'live-view-mode': el => { if (el.dataset.mode === 'chat' || el.dataset.mode === 'terminal') window.setLiveViewMode(el.dataset.mode); },
    'chat-find-toggle': () => window.toggleChatFind(),
    'agentic-panel-toggle': el => (el.dataset.open === '0' ? window.toggleAgenticPanel(false) : window.toggleAgenticPanel()),
    'agentic-tab': el => { if (AGENTIC_TABS.has(el.dataset.tab)) window.switchAgenticTab(el.dataset.tab, 'top'); },
    'session-restart': () => window.restartSession(),
    'popout-retry': () => window.popoutRetry(),
    'files-refresh': () => window.refreshChangedFiles(),
    'task-create-show': () => window.showCreateTaskModal(),
});
// The history view's header, tabs and summary editor (#3645). A tab must be one the view has.
const HISTORY_TABS = new Set(['notes', 'chat', 'activity', 'tasks', 'agent-notes', 'commits', 'changes']);
registerActions({
    'copy-branch': el => copyBranchName(el),
    'tag-dropdown-show': () => showTagDropdown(),
    'tag-dropdown-hide': () => hideTagDropdown(),
    'tag-create': () => createTag(),
    'summary-generate': () => generateSummary(),
    'resume-show': () => showResumeModal(),
    'history-details': el => { const id = document.getElementById('history-session-id'); if (!id) return; id.hidden = !id.hidden; el.setAttribute('aria-expanded', String(!id.hidden)); el.textContent = id.hidden ? 'Details' : 'Hide details'; },
    'history-list': () => {
        state.currentSession = null;
        switchNavTab('history');
        document.getElementById('history-search')?.focus();
    },
    'history-tab': el => { if (HISTORY_TABS.has(el.dataset.tab)) switchHistoryTab(el.dataset.tab); },
    'notes-edit': () => toggleNotesEdit(),
    'notes-cancel': () => cancelNotesEdit(),
    'notes-save': () => saveNotes(),
});
// The Workflows view's own buttons and create dialog (#3645); its generated markup's actions
// register with its module.
registerActions({
    'wf-back': () => workflowsBackToList(),
    'wf-build-agent': () => launchWorkflowAgent(),
    'wf-create': () => showWorkflowCreateModal(),
    'wf-modal-backdrop': (el, event) => { if (event.target === el) hideWorkflowCreateModal(); },
    'wf-modal-close': () => hideWorkflowCreateModal(),
    'wf-add-step': () => workflowAddStep(),
    'wf-save': () => saveWorkflow(),
});
// The Cost view's own controls (#3645); the table's actions register with its module.
registerActions({
    'cost-time-range': () => _costTimeRangeChanged(),
    'cost-refresh': () => _refreshCostDashboard(),
});
// Runs on every tab switch: there is nothing to stop until the dashboard has loaded.
// The team view and its settings (with the Health check tab) load the first time a team is opened (#5364): about 40 KB of source the page does not need to start.
const { enterTeamContext, exitTeamContext, renderTeamSettingsTab } = lazyFns(loadTeamContext, ['enterTeamContext', 'exitTeamContext', 'renderTeamSettingsTab']);
const { showTeamWorkingModeWorkspace, showTeamHealthSettings } = lazyFns(loadTeamSettings, ['showTeamWorkingModeWorkspace', 'showTeamHealthSettings']);
const stopCostDashboard = () => { loadCostDashboard.module?.stopCostDashboard(); };
const { showDocsTab, selectDoc } = lazyFns(loadDocs, ['showDocsTab', 'selectDoc']);
const { showThemeConfigurator, hideThemeConfigurator } = lazyFns(loadThemeConfig, ['showThemeConfigurator', 'hideThemeConfigurator']);
// The dialogs (#3140): their window functions are stubs until one opens.
const {
    showLaunchModal, hideLaunchModal, launchSession, hideInfoModal, copyInfoCommand,
    showSettingsModal, hideSettingsModal, applySettings,
    toggleFlag, showAddAgentToBoard, hideAddAgentBoardModal, launchAgentToBoard, launchTerminalToBoard, launchDefaultAgent,
    showAddStandaloneAgent, launchStandaloneTerminal, exportPersonas, importPersonas, importTeamTemplates,
} = lazyFns(loadModals, [
    'showLaunchModal', 'hideLaunchModal', 'launchSession', 'hideInfoModal', 'copyInfoCommand', 'showSettingsModal', 'hideSettingsModal', 'applySettings', 'toggleFlag', 'showAddAgentToBoard', 'hideAddAgentBoardModal', 'launchAgentToBoard', 'launchTerminalToBoard', 'launchDefaultAgent', 'showAddStandaloneAgent', 'launchStandaloneTerminal', 'exportPersonas', 'importPersonas', 'importTeamTemplates']);
const { showResumeModal, hideResumeModal, resumeLaunchNew } = lazyFns(loadResumeDialog, ['showResumeModal', 'hideResumeModal', 'resumeLaunchNew']);
const { showDefaultPromptsModal, hideDefaultPromptsModal, resetDefaultPrompt, saveDefaultPrompts } = lazyFns(loadDefaultPromptsDialog, ['showDefaultPromptsModal', 'hideDefaultPromptsModal', 'resetDefaultPrompt', 'saveDefaultPrompts']);
// Called from the home view's buttons before any dialog has loaded; modals.js sets them on window itself.
Object.assign(window, lazyWindowFns(loadModals, ['_selectLaunchType', '_showMobileConnectModal']));
import { initCloud } from './cloud.js';
import { isCloud } from './server_base.js';
import { installOverloadWatch } from './overload.js';
import './auth_redirect.js';
import './safe_storage.js';
import './lazy_dom.js';
import { installTestHooks } from './test_hooks.js';
import { _handleTeamFolderImport, _importTeamFromFolder } from './team_folder_import.js';
import { closeSidebarKebabs, closeTopBarSettings, toggleSidebarKebab, toggleTopBarSettings } from './sidebar_menus.js';
import { _startListLoadSettled, pollStartupStatus } from './startup_status.js';
import { getHistoryPage, setHistoryPage } from './history_state.js';
import { initLocalStore, initPanels, initPlatform, installUnauthorizedRedirect, wireComposer, wirePaneScroll, wireWindowEvents, yieldToMain } from './app_wiring.js';
import { loadHistoryFiltered, loadHistoryPage, populateHfTagSelect, syncFilterDomToState, wireHistoryFilters } from './history_filters.js'; // the suites' surface, installed only when the page's boot data asks for it (#5015)

installTestHooks();

// ── Expose functions to HTML onclick handlers ─────────────────────────────
// Grouped by source module for maintainability. Add new entries to the
// appropriate section rather than appending to the bottom.
Object.assign(window, {
    // api
    _coralLoadLiveSessions: loadLiveSessions,
    // test hooks (tests/frontend): inject a live-sessions payload and render it;
    // feed a /ws/coral message through the real handler without a socket.
    // popout (/agent/{uuid}) + multi-window ownership
    popoutRetry, 
    
    // controls
    addMacro, hideMacroModal,
    restartSession,
    editGoal, refreshGoal, requestGoal,
    hideRestartModal, confirmRestart, 
    // modals
    showSettingsModal, hideSettingsModal, applySettings,
    showDefaultPromptsModal, hideDefaultPromptsModal, resetDefaultPrompt, saveDefaultPrompts,
    showLaunchModal, hideLaunchModal, launchSession, toggleFlag,
    hideInfoModal, copyInfoCommand,
    hideResumeModal, resumeLaunchNew,
    showAddAgentToBoard, hideAddAgentBoardModal, launchAgentToBoard, launchTerminalToBoard,
    launchDefaultAgent, showAddStandaloneAgent, launchStandaloneTerminal,
    exportPersonas, importPersonas, importTeamTemplates,
    // sessions
    selectLiveSession, selectHistorySession, editAndResubmit, renameAgent, setAgentIcon,
    // xterm
    connectTerminalWs, disconnectTerminalWs,
    // notes
    // tags
    addTagToSession, removeTagFromSession, 
    // changed_files
    openFilePreview, openFileEdit, refreshChangedFiles,
    copyFilePath, 
    // tasks
    showTaskDetailModal, hideTaskDetailModal, loadMoreBoardTasks,
    showCreateTaskModal, hideCreateTaskModal, submitCreateTask,
    enableTaskEditMode, saveTaskEdit, cancelTaskEdit,
    completeBoardTask, cancelBoardTask, _doCompleteTask, _doCancelTask, _restoreTaskFooter, publishBoardTask, nudgeBoardTask, remindBoardTask, stopBoardTaskReminder,
    // agent_notes
    // agentic_state
    switchAgenticTab,
    // live_chat
    setLiveViewMode, showTerminalView,
    // scheduler
    showJobModal, hideJobModal, validateCronPreview, saveScheduledJob, switchJobType, pickSchedulePreset,
    // live_jobs
    selectLiveJobRun,
    // preview_pane
    // sidebar
    switchJobsSubtab, toggleAgenticPanel, toggleSidebarRail,
    // browser
    toggleBrowser, browserNavigateTo, browserNavigateUp, browserSelectRecent, browserUse,
    // theme
    showThemeConfigurator, hideThemeConfigurator,
    // servers (hub mode)
    showServersModal,
    // update_check
    checkForUpdates, dismissUpdateToast,
    // pinned folders (#3630)
    pinFolder, unpinFolder, showOpenFolderModal, hideOpenFolderModal, submitOpenFolder,
    // terminal backend (#3634): the banner's End them, loaded on first use
    endLeftoverTmux: () => importOnce('./terminal_backend.js').then(m => m.endLeftoverTmux()),
    // auto-wake (#3636): the over-the-cap banner's two buttons
    wakePendingAgents: () => importOnce('./terminal_backend.js').then(m => m.wakePendingAgents()),
    dismissAutoWake: () => importOnce('./terminal_backend.js').then(m => m.dismissAutoWake()),
    // message_board
    selectBoardProject,
    // workflows
    showWorkflowsTab,
    selectWorkflow, selectWorkflowRun,
    // cost_dashboard
    showCostDashboard, 
    // docs
    showDocsTab, selectDoc,
    // folder_tags
    showFolderTagDropdown, addFolderTag, removeFolderTag, createAndAddFolderTag,
    // utils
    // router
    _activateCustomView: activateCustomView, 
    // render (pagination)
    loadHistoryPage,
    // top nav
    switchNavTab,
});

// ── Reset Team ───────────────────────────────────────────────────────
function resetTeam(boardName) {
    showConfirmModal('Reset Team', `Reset all agents in "${keyLabel(boardName)}"? Their context will be cleared and they'll restart with their original prompts. ${KILL_LOG_NOTE_MANY}`, async () => {
        try {
            const resp = await teamFetch(boardName, n => `/api/sessions/live/team/${encodeURIComponent(n)}/reset`, { method: 'POST' });
            if (resp.ok) {
                showToast(`Team "${keyLabel(boardName)}" is resetting...`);
            } else {
                const data = await resp.json().catch(() => ({}));
                showToast(data.error || 'Reset failed', true);
            }
        } catch {
            showToast('Reset failed', true);
        }
    });
}

// ── Top Nav Tab Switching ─────────────────────────────────────────────
let _navGeneration = 0;

import { historyRoute, historyServer, setHistorySource } from './history_source.js';

function switchNavTab(tab, opts = {}) {
    // A panel that loads on first use (lazy_panels.js) shows its view only if
    // nothing else was selected or shown while it loaded. Once loaded it shows at once.
    const generation = ++_navGeneration;
    const showWhenLoaded = (load, fn) => {
        if (load.module) { load.module[fn](); return; }
        const views = viewChanges();
        load().then(m => { if (generation === _navGeneration && views === viewChanges()) m[fn](); }, () => {});
    };
    // Clean up any active cost dashboard timer when leaving tokens tab
    stopCostDashboard();
    if (tab !== 'servers') importOnce('./servers_tab.js').then(m => m.leaveServersTab());
    // Leaving the Inbox: the one Saved list goes back to the right sidebar (#4811). Loaded only if the list is in the page, not on every tab switch.
    if (tab !== 'hub-inbox' && document.getElementById('saved-view')?.contains(document.getElementById('saved-ui'))) importOnce('./saved.js').then(m => m.leaveSavedView());
    // The URL follows the tab (#3532): a tab the user chose is a history entry. Servers writes its
    // own (it can carry a server); the board tab names no board, so it keeps the URL.
    const TAB_ROUTE = { agents: 'agents', history: 'chats', workflows: 'workflows', jobs: 'jobs', tokens: 'analytics', docs: 'docs' };
    if (!opts.fromHistory && TAB_ROUTE[tab]) {
        const cur = currentRoute();
        const route = tabRoute(tab, TAB_ROUTE[tab]);
        if (!cur || cur.view !== route.view || cur.id !== route.id || cur.server !== route.server) note(route);
    }

    // Toggle sidebar visibility — full-width views hide the sidebar
    const fullWidthTabs = new Set(['tokens', 'workflows', 'servers', 'hub-inbox', 'artifacts']);
    const layout = document.querySelector('.layout');
    if (layout) layout.classList.toggle('sidebar-hidden', fullWidthTabs.has(tab));

    // Update the nav buttons: page navigation, so the current one is aria-current="page".
    document.querySelectorAll('.top-nav-tab').forEach(btn => { btn.classList.remove('active'); btn.removeAttribute('aria-current'); });
    if (document.body.getAttribute('data-nav-tab') !== tab) document.body.setAttribute('data-nav-tab', tab); // what a page-scoped note (the Claude sign-in line) follows
    const activeTab = document.getElementById(`nav-tab-${tab}`);
    if (activeTab) { activeTab.classList.add('active'); activeTab.setAttribute('aria-current', 'page'); }

    // Show/hide sidebar sections based on tab
    const liveSection = document.querySelector('[data-section="live-sessions"]');
    const historySection = document.querySelector('[data-section="history"]');
    const jobsSection = document.querySelector('[data-section="jobs"]');
    const docsSection = document.querySelector('[data-section="docs"]');
    const sidebarFooter = document.querySelector('.sidebar-footer');
    const sidebarHeader = document.querySelector('.sidebar-header');

    if (liveSection) liveSection.style.display = tab === 'agents' ? '' : 'none';
    if (jobsSection) jobsSection.style.display = tab === 'jobs' ? '' : 'none';
    if (historySection) historySection.style.display = tab === 'history' ? '' : 'none';
    if (docsSection) docsSection.style.display = tab === 'docs' ? '' : 'none';
    if (sidebarFooter) sidebarFooter.style.display = tab === 'agents' ? '' : 'none';
    if (sidebarHeader) sidebarHeader.style.display = tab === 'agents' ? '' : 'none';

    // Switch main view based on tab
    if (tab === 'board') {
        showMessageBoardProjects();
    } else if (tab === 'jobs') {
        showView('scheduler-view');
    } else if (tab === 'workflows') {
        showWhenLoaded(loadWorkflows, 'showWorkflowsTab');
    } else if (tab === 'docs') {
        showWhenLoaded(loadDocs, 'showDocsTab');
    } else if (tab === 'servers') {
        showView('servers-view');
        if (!opts.fromHistory) note(opts.focusId ? { view: 'servers', id: opts.focusId } : { view: 'servers' });
        importOnce('./servers_tab.js').then(m => m.showServersTabContent(opts.focusId));
    } else if (tab === 'hub-inbox') {
        // The hub's /inbox page in a frame where the hub serves the dashboard, the dashboard's own Inbox (Needs you, Saved for later) on every other Coral (#4811); the tab writes no address of its own.
        showView('inbox-view');
        importOnce('./inbox_tab.js').then(m => m.showInboxTabContent());
    } else if (tab === 'artifacts') {
        // The hub's /artifacts page in a frame (artifacts_tab.js), the Inbox's way; the tab writes no address of its own.
        showView('artifacts-view');
        importOnce('./artifacts_tab.js').then(m => m.showArtifactsTabContent());
    } else if (tab === 'tokens') {
        showWhenLoaded(loadCostDashboard, 'showCostDashboard');
    } else if (tab === 'history') {
        // History is populated by the background indexer, so the cached list may
        // have changed while another tab was active. Refresh it whenever the
        // user returns while preserving the current page and filters.
        loadHistoryFiltered();
        if (state.currentSession && state.currentSession.type === 'history') {
            showView('history-session-view');
        } else {
            showView('chats-empty-view');
        }
    } else if (tab === 'agents' && window.innerWidth <= 767) {
        syncMobileAgentList();
        showView('mobile-agent-list');
    } else if (tab === 'agents') {
        // Restore previously selected session or show welcome
        if (state.currentSession && state.currentSession.type === 'live') {
            showView('live-session-view');
        } else {
            showView('welcome-screen');
        }
    }
}

/** The route a top tab shows: the Agents tab its open team or selected agent (on a desktop, where
 *  the tab shows it), the Chats tab its open chat or its filtered list. */
function tabRoute(tab, view) {
    const cur = state.currentSession;
    if (tab === 'agents' && state.selectedTeam) return keyRoute('team', state.selectedTeam);
    if (tab === 'agents' && cur && cur.type === 'live' && window.innerWidth > 767) return liveRoute(sessionServer(cur), cur.session_id);
    if (tab === 'history') return historyRoute(cur?.type === 'history' ? cur.name : null, currentChatsQuery());
    return { view };
}

;

;

// Inline helpers that need custom logic (can't be simple re-exports)
window.toggleSendMenu = function(btn) {
    const menu = btn.parentElement.querySelector('.send-btn-menu');
    if (!menu) return;
    const show = menu.style.display === 'none';
    menu.style.display = show ? '' : 'none';
    if (show) {
        const close = (e) => {
            if (!menu.contains(e.target) && !btn.contains(e.target)) {
                menu.style.display = 'none';
                document.removeEventListener('click', close);
            }
        };
        setTimeout(() => document.addEventListener('click', close), 0);
    }
};
window.closeSendMenu = function() {
    document.querySelectorAll('.send-btn-menu').forEach(m => m.style.display = 'none');
};

// Up, Down, Home and End move between a menu's items (it says aria-haspopup="menu"); Escape closes it from inside.
document.addEventListener('keydown', e => {
    const menu = e.target.closest?.('.sidebar-kebab-menu');
    if (!menu) return;
    const items = [...menu.querySelectorAll('.overflow-menu-item')];
    const at = items.indexOf(document.activeElement);
    const to = e.key === 'ArrowDown' ? (at + 1) % items.length : e.key === 'ArrowUp' ? (at <= 0 ? items.length - 1 : at - 1) : e.key === 'Home' ? 0 : e.key === 'End' ? items.length - 1 : -1;
    if (to >= 0 && items.length) { e.preventDefault(); items[to].focus(); }
}, true);

// Tab (or Shift+Tab) out of an open menu closes it: the focus has left the button and its menu, and nothing is left open behind it.
document.addEventListener('focusout', e => {
    const wrap = e.target.closest?.('.sidebar-kebab-wrapper');
    if (!wrap || !document.hasFocus()) return;
    const to = e.relatedTarget;
    if (to && wrap.contains(to)) return;
    const menu = wrap.querySelector('.sidebar-kebab-menu');
    if (menu && menu.style.display !== 'none') closeSidebarKebabs();
});


Object.assign(window, {
    toggleSidebarKebab, closeSidebarKebabs,
    // render (sidebar actions)
    filterLiveAgents, focusAgentSearch, agentSearchKey, toggleSessionSummary, applySessionSummaryPref,
    toggleShowServer, toggleGroupByServer, toggleShowBranches, toggleShowProvider, toggleShowModel, toggleShowThinking,
    toggleGroupCollapse, groupHeaderKey, teamHeaderKey, killGroup, moveGroupUp, moveGroupDown,
    copyFolderPath, killBoard, setBoardAccentColor, resetTeam,
    moveSessionUp, moveSessionDown,
    toggleTeamSleep, toggleAgentSleep, toggleAgentArchive, toggleArchivedSection,
    enterTeamContext, exitTeamContext, renderTeamSettingsTab,
    showTeamAvailabilityWorkspace, showTeamWorkingModeWorkspace, showTeamHealthSettings,
    shareAgentTeam, saveTeamFromSidebar, showTeamTokenUsage, distillTeamKnowledge,
    showConfirmModal, hideConfirmModal, showPromptModal, hidePromptModal, showAlertModal, hideAlertModal,
    killSessionDirect, showInfoDirect, attachDirect, restartDirect,
    toggleGroupByTeam, expandSleepingGroup,
    // template_browser
});

(window.__coral ||= {}).toggleTopBarSettings = toggleTopBarSettings;
(window.__coral ||= {}).closeTopBarSettings = closeTopBarSettings;


// A top-bar menu item chosen with a click that focused nothing (a clicked button gets no focus in Safari on a Mac) left focus on
// the page once the menu closed, so Escape in what it opened dropped the keyboard user at the top: focus goes to the menu's
// button first (before the item's action, which closes the menu), and what the item opens hands focus back there (UX on #4500).
document.addEventListener('click', (e) => {
    const item = e.target.closest?.('#top-bar-settings-menu .overflow-menu-item');
    if (!item) return;
    const a = document.activeElement;
    if (a && a !== document.body && !item.parentElement.contains(a)) return;
    document.querySelector('[aria-controls="top-bar-settings-menu"]')?.focus({ preventScroll: true });
}, true);

// Close kebab menus when clicking outside
document.addEventListener('click', (e) => {
    if (!e.target.closest('.top-bar-settings-wrapper')) {
        closeTopBarSettings();
    }
    if (!e.target.closest('.sidebar-kebab-wrapper')) {
        closeSidebarKebabs();
    }
});

// ── History search/filter/pagination state ───────────────────────────────










// ── Routes (#3532) ───────────────────────────────────────────────────────
// nav.js owns history; these show a route the page was opened at or the user went Back or
// Forward to. Each view writes its own route when the user opens something (note()), so a handler
// calling the same function writes nothing new. A route's ids were checked by routes.js before it
// gets here.

/** A page whose object doesn't exist (design 4.1): say so, and show its list instead (a replace,
 *  so Back doesn't return to the missing page). */
function notFound(message, listRoute) {
    showToast(message, true);
    return go(listRoute, { replace: true });
}

// The query the page was opened with, read before anything rewrites the URL (a link to one saved message, #4596, is in it).
const START_SEARCH = location.search;

/** The tray's "Update to vX available" opens /?settings=updates: Settings on its Updates tab. That one value and no other (anything else is ignored), only when the server says the updater is on
 *  (the caller is that answer), and the parameter is removed from the address so a reload does not open it again (#5280 step 3 D). */
function openUpdatesFromLink() {
    const p = new URLSearchParams(START_SEARCH);
    const v = p.getAll('settings');
    if (v.length !== 1 || v[0] !== 'updates') return;
    p.delete('settings');
    const rest = p.toString();
    try { history.replaceState(history.state, '', location.pathname + (rest ? '?' + rest : '') + location.hash); } catch { /* an address that cannot be rewritten stays */ }
    window.showSettingsModal('updates');
}

// Settles once the first live list has loaded (or failed): an agent link needs the list.
let _liveListLoaded;
const liveListReady = new Promise(r => { _liveListLoaded = r; });

/** The Chats list's query for its current filters and page (no "?"). */
function currentChatsQuery() {
    return urlSearchFor(getHistoryPage(), '').replace(/^\?/, '');
}

function registerRouteHandlers() {
    // Any view but a team leaves the team workspace (Back from a team to where the user was).
    const route = (view, fn) => onRoute(view, async (r, opts) => {
        if (state.selectedTeam && await exitTeamContext({ goBack: false }) === false) {
            note(keyRoute('team', state.selectedTeam), { replace: true }); // kept: unsaved team settings
            return;
        }
        await fn(r, opts);
    });

    route('agents', async (r, opts) => {
        if (!r.id) {
            switchNavTab('agents', { fromHistory: true });
            if (!opts.startup) showHome();
            return;
        }
        await liveListReady;
        const s = (state.liveSessions || []).find(x => String(x.session_id || '').toLowerCase() === r.id
            && liveRoute(sessionServer(x), x.session_id).server === r.server);
        if (!s) return notFound('That agent is not running here', { view: 'agents' });
        switchNavTab('agents', { fromHistory: true });
        const cur = state.currentSession;
        const shown = cur && cur.type === 'live' && cur.session_id === s.session_id && sessionServer(cur) === sessionServer(s);
        if (!shown) await window.selectLiveSession(s.name, s.agent_type, s.session_id, sessionServer(s));
        else if (window.innerWidth <= 767) showView('live-session-view'); // the phone's Agents tab showed the list
        if (r.sub) switchAgenticTab(r.sub, 'top');
    });

    route('chats', async (r, opts) => {
        await liveListReady;
        if (r.server && !setHistorySource(r.server)) return notFound('That computer was not found', { view: 'chats' });
        if (r.id) {
            switchNavTab('history', { fromHistory: true });
            const cur = state.currentSession;
            if (!(cur && cur.type === 'history' && cur.name === r.id && sessionServer(cur) === historyServer()) && (await selectHistorySession(r.id)) === false) {
                state.currentSession = null;
                notFound('That chat was not found', { view: 'chats', query: currentChatsQuery() });
            }
            return;
        }
        // The list: its filters are the URL's (startup already read them).
        if (!opts.startup) {
            setHistoryPage(deserializeFromUrl().page);
            syncFilterDomToState();
        }
        if (state.currentSession && state.currentSession.type === 'history') state.currentSession = null; // Back closed the chat
        switchNavTab('history', { fromHistory: true });
    });

    onRoute('team', async r => {
        await liveListReady;
        switchNavTab('agents', { fromHistory: true });
        const tab = r.sub === 'settings' ? 'team-settings' : 'team-view';
        const ok = await enterTeamContext(routeKey(r), { tab, restore: true, silent: true });
        if (ok === false && state.selectedTeam) note(keyRoute('team', state.selectedTeam), { replace: true });
    });

    route('board', r => {
        switchNavTab('board', { fromHistory: true });
        selectBoardProject(routeKey(r));
    });

    route('workflows', async r => {
        switchNavTab('workflows', { fromHistory: true });
        const m = await loadWorkflows();
        const found = r.run ? await m.selectWorkflowRun(r.run, { workflowId: r.id }) : r.id ? await m.selectWorkflow(r.id) : m.workflowsBackToList();
        if (found === false) {
            m.workflowsBackToList();
            notFound(r.run ? 'That workflow run was not found' : 'That workflow was not found', { view: 'workflows' });
        }
    });

    route('jobs', () => switchNavTab('jobs', { fromHistory: true }));
    route('analytics', () => switchNavTab('tokens', { fromHistory: true }));

    route('docs', async r => {
        switchNavTab('docs', { fromHistory: true });
        (await loadDocs()).openDoc(r.id || null);
    });

    // Servers is the hub's tab (known once the list has loaded); a Coral without one goes home.
    route('servers', async r => {
        await liveListReady;
        const m = await importOnce('./servers_tab.js');
        if (!(await m.openServersTab(r.id || undefined, { fromHistory: true }))) go({ view: 'agents' }, { replace: true });
    });

    // A dashboard page link in agent-written text (#3542 keeps it as a[data-route], rebuilt from
    // the parsed route): a plain click goes through the router instead of reloading the dashboard.
    // A modified or middle click, another origin, or a path the router refuses keeps the browser's.
    document.addEventListener('click', e => {
        if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
        const a = e.target.closest && e.target.closest('a[data-route][href]');
        if (!a || (a.target && a.target !== '_self')) return;
        let url;
        try { url = new URL(a.getAttribute('href'), location.origin); } catch { return; }
        if (url.origin !== location.origin) return;
        const r = parseRoute(url.pathname, url.search);
        if (!r) return;
        e.preventDefault();
        go(r);
    });

    // A sidebar tab the user picks is part of the page (a replace): an agent's tab, or a team's
    // view or settings. Tabs switched by code (restoring the last one) leave the URL alone.
    document.addEventListener('click', e => {
        const btn = e.target.closest && e.target.closest('#agentic-block-top .agentic-tab');
        if (!btn) return;
        const tab = btn.id.replace('agentic-tab-', '');
        const cur = currentRoute();
        if (cur && cur.view === 'team' && state.selectedTeam) {
            if (tab === 'team-view' || tab === 'team-settings') note(keyRoute('team', state.selectedTeam, tab === 'team-settings' ? 'settings' : null), { replace: true });
            return;
        }
        const s = state.currentSession;
        if (cur && cur.view === 'agents' && cur.id && s && s.type === 'live' && AGENT_SUBTABS.includes(tab)) {
            note(liveRoute(sessionServer(s), s.session_id, tab), { replace: true });
        }
    });
}

// ── Home Navigation ──────────────────────────────────────────────────────
(window.__coral ||= {})._goHome = function() {
    note({ view: 'agents' });
    showHome();
};

/** The Agents home: the welcome screen (the agent list on a phone), no agent highlighted. */
function showHome() {
    showView(window.innerWidth <= 767 ? "mobile-agent-list" : "welcome-screen");
    // Deselect sidebar items
    document.querySelectorAll('.sidebar .session-item.active, .sidebar .session-item.selected').forEach(el => {
        el.classList.remove('active', 'selected');
    });
}

// ── Initialization ────────────────────────────────────────────────────────
// A page opened at /agents/<session id> asks for that chat's first page now, not after the live list names the agent (chat_page.js, #4326).
// The Coral Hub, the pop-out and a remote server's agent are not /agents/<id> of a local session: nothing starts there.
startChatPrefetch({ cloud: isCloud() });




/** The first loads (the agent list, tags, pinned folders, views, the feed's socket, history, the scheduler, the board) and the filter state the URL asked for. */
function loadInitialData(popout) {
    // Restore filter state from URL query params before first load
    const restored = deserializeFromUrl();
    setHistoryPage(restored.page);

    // The hash the page was opened with: a deep link to restore. A hash the user
    // sets while the list loads (e.g. opening Servers) is not restored again,
    // which would re-open that view a second time (#3152).
    loadLiveSessions(dashboardFailed).then(ok => {
        markReady('liveList'); // already marked by the render; this covers a failed first load
        dashboardSessionsLoaded(ok);
    }).finally(() => { _startListLoadSettled(); _liveListLoaded(); });
    loadAllFolderTags();
    if (!popout) loadPinnedFolders();
    if (!popout) importOnce('./received_cap_notice.js').then(m => m.checkReceivedCap()).catch(() => {}); // #4441: told once when received files reach 80% of their limit
    if (!popout) importOnce('./terminal_backend.js').then(m => { m.checkTerminalNotices(); m.watchAutoWake(); m.watchHooksStatus(); }).catch(() => {}); // #3634, #3636, #3665
    for (const [n, f] of [['loadCustomViews', loadCustomViews], ['initTabCustomize', initTabCustomize], ['initAgentUI', initAgentUI]]) { // a failing step must not stop the feed
        const fail = e => console.error(`init step ${n} failed`, e);
        try { Promise.resolve(f()).catch(fail); } catch (e) { fail(e); }
    }
    connectCoralWs();
    if (!popout) checkForUpdates();
    if (!popout) loadUpdatesFlag().then(on => on && importOnce('./update_ui.js').then(m => { m.startUpdateUi(); openUpdatesFromLink(); })).catch(() => {}); // the auto-update control, shown when the server says the updater is on (#5275, #5280)
    pollStartupStatus();
    if (!popout) {
        populateHfTagSelect().then(() => {
            syncFilterDomToState();
            return loadHistoryFiltered();
        }).finally(() => markReady('history'));
        initScheduler();
        initLiveJobs();
        initMessageBoard();
    }
    if (popout) markReady('history'); // a popout has no history list
    initMobile();
}






/** The page's route: its path, or an old hash link, shown once the handlers are in place. */
function startRoute(popout) {
    // The page's route (#3532): its path, or an old hash link, shown once the handlers are in place.
    if (!popout) {
        registerRouteHandlers();
        // A link to one saved message (#4596): read before the route is written, run once the live list is in.
        const savedLink = START_SEARCH.includes('saved=') ? importOnce('./saved_link.js').then(m => m.parseSavedLink(START_SEARCH, isCloud())).catch(() => null) : null;
        startNav();
        if (savedLink) Promise.all([savedLink, liveListReady]).then(([item]) => item && importOnce('./saved.js').then(m => m.openSavedLink(item))).catch(() => {});
    }
}


document.addEventListener("DOMContentLoaded", async () => {
    beginDashboardAnalytics();
    if (window.innerWidth > 767) buildWelcome(); // a phone starts on the agent list and builds the welcome screen only if it is shown (#4497)
    try {
    initPlatform();
    installUnauthorizedRedirect();

    // Back off on 429/502/503 (#3403): every poller pauses while Coral (or the hub) is overloaded.
    installOverloadWatch();

    await initCloud();   // no-op unless the page's boot data says mode "cloud"
    await initLocalStore();
    loadSettings().then(initEditions).catch(e => console.error('ways to run Coral:', e)).finally(() => markReady('settings'));
    initServersUi();   // reveals hub-only entry points when /api/health reports hub:true

    // Single-agent popout mode (/agent/{uuid}): chrome-free workspace only.
    // Marked before any renderer runs so CSS and storage scoping apply.
    const popout = applyPopoutBodyClass();
    if (!popout) initHubIndicator(); // the Coral Hub connection in the header (#3544)

    loadInitialData(popout);

    initPrerequisiteChecks();
    attachAllDialogFocus();   // focus in and back for every dialog in the shell (#4026)

    wireHistoryFilters();
    wireComposer();
    wirePaneScroll();
    initPanels();

    // The page's startup was one task of about 60 ms at a 4x slowdown (the bundle's evaluation and every step of this handler, ahead of the first paint). Everything is wired
    // and the requests are out; the browser may paint and answer before the navigation and the route are set up (#4995).
    await yieldToMain();

    // Initialize nav tabs — hide history section on load (Agents tab is default)
    if (!popout) {
        switchNavTab('agents');
    } else {
        initPopout();
    }

    wireWindowEvents();
    startRoute(popout);
    dashboardInitialized();
    } catch (error) {
        dashboardFailed('init_failed');
        throw error;
    }
});


