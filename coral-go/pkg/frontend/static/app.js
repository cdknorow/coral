import './update_banner.js'; // first: it wraps fetch before the other modules ask for anything (#3751)
import { importOnce } from './import_once.js';
import './hub_blocked_controls.js'; // dims controls the Hub tier does not allow (#3769)
import './nav_tabs.js'; // the tab row, its More menu and the phone's view dropdown (#3816)
import { initPrerequisiteChecks } from './prerequisites.js';
import { fetchSystemStatus } from './system_status.js';
import { beginDashboardAnalytics, dashboardInitialized, dashboardSessionsLoaded, dashboardStartupComplete, dashboardFailed } from './dashboard_analytics.js';
import { initAgentUI } from './agent_ui.js';
import { showTeamWorkingMode, showTeamWorkingModeWorkspace } from './team_working_mode.js';
import { showTeamAvailability, showTeamAvailabilityWorkspace } from './team_availability.js';
import { enterTeamContext, exitTeamContext, renderTeamSettingsTab } from './team_context.js';
/* Coral Dashboard — Entry Point */

import { state, sessionKey, saveSessionDraft, loadSessionDrafts } from './state.js';
import { accountTag, setStoreOwner, ageOutStored, DAY_MS } from './local_store.js';
import { loadLiveSessions, loadHistorySessions, loadHistorySessionsPaged } from './api.js';
import { filterState, deserializeFromUrl, serializeToUrl, urlSearchFor,
         hasActiveFilters, countActiveFilters, resetFilters }
    from './search_filters.js';
import { connectCoralWs, handleCoralMessage, _resetWaitingSeedForTests } from './websocket.js';
import { sendCommand, sendCommandWithTeam, sendBoardProtocol, resendInputPrompt, sendRawKeys, sendModeToggle, cycleModeToggle, sendQuickCommand, refreshModeLabel, executeMacro, addMacro, deleteMacro, showMacroModal, hideMacroModal, attachTerminal, killSession, restartSession, hideRestartModal, confirmRestart, initImageDrop, removeAttachment, editGoal, refreshGoal, requestGoal } from './controls.js';
import { selectLiveSession, selectHistorySession, editAndResubmit, renameAgent, setAgentIcon, showEmojiPicker } from './sessions.js';
import { buildSessionTooltip, showTeamDetails, hideTeamDetails, toggleGroupCollapse, killGroup, killBoard, toggleTeamSleep, toggleAgentSleep, toggleAgentArchive, toggleArchivedSection, sleepAllAgents, wakeAllAgents, shareAgentTeam, saveTeamFromSidebar, killSessionDirect, dismissKilledSession, dismissBoardKilled, showInfoDirect, attachDirect, restartDirect, showConfirmModal, hideConfirmModal, showPromptModal, hidePromptModal, showAlertModal, hideAlertModal, copyFolderPath, moveGroupUp, moveGroupDown, toggleGroupByTeam, expandSleepingGroup, setBoardAccentColor, moveSessionUp, moveSessionDown, showTeamTokenUsage, distillTeamKnowledge, showTeamKnowledge, renderLiveSessions, toggleSessionSummary, applySessionSummaryPref, filterLiveAgents, focusAgentSearch, agentSearchKey, toggleShowServer, toggleGroupByServer, renderServerSwitcher, groupHeaderKey } from './render.js';
import { syncPaneWidth, refreshCapture } from './capture.js';
import { loadSettings } from './settings_core.js';
import { initEditions } from './editions.js';
import './modal_chrome.js'; // modal backdrop/Escape/Tab handling for every dialog (#3140)
import { attachAllDialogFocus } from './dialog_focus.js';
import './board_choice.js'; // the duplicate-name choice used by the lazy modals.js (#3538): loaded at startup so a launch never fails on it
import './dialog_focus.js'; // focus into a dialog and back to its opener (accessibility pass); used by lazy modals.js
import { toggleBrowser, browserNavigateTo, browserNavigateUp, browserSelectRecent } from './browser.js';
import { initSidebarResize, initTaskBarResize, initBoardChatResize, initSidebarCollapse, switchJobsSubtab, initAgenticPanelCollapse, toggleAgenticPanel, initAgenticBlockResize, initAgenticBlockCollapse, toggleSidebarRail, initSidebarRail } from './sidebar.js';
import { fitTerminal, getTerminal, connectTerminalWs, disconnectTerminalWs } from './xterm_renderer.js';
import { loadSessionNotes, saveNotes, generateSummary, resummarize, toggleNotesEdit, cancelNotesEdit, switchHistoryTab } from './notes.js';
import { loadSessionTags, addTagToSession, removeTagFromSession, showTagDropdown, hideTagDropdown, createTag, loadAllTags } from './tags.js';
import { loadSessionCommits } from './commits.js';
import { loadAgentTasks, addAgentTask, toggleAgentTask, deleteAgentTask, editAgentTaskTitle, loadBoardTasks, loadMoreBoardTasks, renderBoardTaskList, showTaskDetailModal, hideTaskDetailModal, showCreateTaskModal, hideCreateTaskModal, submitCreateTask, enableTaskEditMode, saveTaskEdit, cancelTaskEdit, completeBoardTask, cancelBoardTask, _doCompleteTask, _doCancelTask, _restoreTaskFooter, publishBoardTask, nudgeBoardTask, remindBoardTask, stopBoardTaskReminder } from './tasks.js';
import { loadChangedFiles, openFilePreview, openFileEdit, refreshChangedFiles, toggleGitDiffMode, setGitDiffMode, toggleStarFile, copyFilePath, searchRepoFiles, renderStarredFiles, initFileSearch, initTopBarSearch, showTopBarSearch, hideTopBarSearch } from './changed_files.js';
import { initFileMention } from './file_mention.js';
import { initCommandMention } from './command_mention.js';
import { loadAgentNotes, initNotesMd } from './agent_notes.js';
import { loadCustomViews, activateCustomView } from './custom_views.js';
import { initTabCustomize } from './tab_customize.js';
import { startNav, go, note, onRoute, currentRoute } from './nav.js';
import { liveRoute, keyRoute, routeKey, AGENT_SUBTABS, parse as parseRoute } from './routes.js';
import { initPopout, applyPopoutBodyClass, popoutRetry, togglePopoutPanel, popoutWake, popoutApi } from './popout.js';
import { isInteractiveOwner, claimOwnership } from './ownership.js';
import { setLiveViewMode, showTerminalView, refreshLiveHistory, initCommandInputAutosize, loadPendingFromStorage } from './live_chat.js';
import { switchAgenticTab, restoreAgenticTabs, loadAgentEvents, toggleEventFilter, toggleAllEventFilters, toggleFilterDropdown, showFilterPopup, hideFilterPopup } from './agentic_state.js';
import { toggleHistoryEventFilter, toggleAllHistoryEventFilters } from './history_tabs.js';
import { copyBranchName, escapeHtml, showView, showToast, viewChanges } from './utils.js';
import { initPreviewPane, openPreviewTab, closeTab as closePreviewTab, closeAllTabs as closeAllPreviewTabs, resetPreviewPane } from './preview_pane.js';
import { initScheduler, selectScheduledJob, toggleScheduledJob, deleteScheduledJob, editScheduledJob, showJobModal, hideJobModal, validateCronPreview, saveScheduledJob, switchJobType, pickSchedulePreset } from './scheduler.js';
import { initLiveJobs, renderLiveJobs, selectLiveJobRun } from './live_jobs.js';
import { initMessageBoard, selectBoardProject, showMessageBoardProjects, postBoardMessage, deleteMessageBoardProject, toggleBoardPause, toggleBoardSleep, deleteBoardMessage, showExportBoardModal, doExportBoard, toggleSelectMode, toggleMessageSelect, selectAllMessages, selectNoneMessages, cancelSelectMode, exportSelectedAsMarkdown, openBoardSubscribers, hideBoardSubscribers } from './message_board.js';
import { loadAllFolderTags, showFolderTagDropdown, hideFolderTagDropdown, addFolderTag, removeFolderTag, createAndAddFolderTag } from './folder_tags.js';
import { loadPinnedFolders, pinFolder, unpinFolder, showOpenFolderModal, hideOpenFolderModal, submitOpenFolder } from './pinned_folders.js';
import { initMobile, syncMobileAgentList } from './mobile.js';
import { platform } from './platform/detect.js';
import { initNative } from './platform/native.js';
import { initMacOS } from './platform/macos.js';
import { initWindows } from './platform/windows.js';
import { initBrowser } from './platform/browser.js';
import { initChatFind } from './chat_find.js';

import { checkForUpdates, dismissUpdateToast, showSecurityWarning } from './update_check.js';
import { teamFetch, sessionServer, keyLabel } from './server_base.js';
import { showServersModal, hideServersModal, initServersUi } from './servers.js';
import { initHubIndicator } from './hub_indicator.js';
import './servers_tab.js'; // Servers tab: chip clicks and deep links (#3070)
import { registerActions, tokenOf } from './actions.js'; // delegated data-on-* handlers, replacing inline ones (#3645)
import { markReady } from './ready.js';
import { lazyFns, lazyWindowFns, loadWorkflows, loadCostDashboard, loadDocs, loadThemeConfig, loadTemplateBrowser, loadChatSearch, loadModals, loadResumeDialog, loadDefaultPromptsDialog } from './lazy_panels.js';

// Panels off the landing view load on first use (#3083 phase 2, lazy_panels.js).
const {
    showWorkflowsTab, selectWorkflow, selectWorkflowRun, triggerWorkflow, killWorkflowRun, deleteWorkflow,
    showWorkflowCreateModal, hideWorkflowCreateModal, editWorkflow, editWorkflowWithAgent, workflowAddStep,
    workflowStepTypeChanged, saveWorkflow, workflowsBackToList, launchWorkflowAgent,
} = lazyFns(loadWorkflows, ['showWorkflowsTab', 'selectWorkflow', 'selectWorkflowRun', 'triggerWorkflow', 'killWorkflowRun', 'deleteWorkflow',
    'showWorkflowCreateModal', 'hideWorkflowCreateModal', 'editWorkflow', 'editWorkflowWithAgent', 'workflowAddStep',
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
    'm-on-quick-launch-server-change': el => { window._onQuickLaunchServerChange(el.value); },
    'm-open-folder-backdrop': (el, event) => { if (event.target === el) window.hideOpenFolderModal(); },
    'm-open-folder-key': (el, event) => { if (event.key === 'Enter') { event.preventDefault(); window.submitOpenFolder(); } },
    'm-perm-mode-describe': el => { if (window._updatePermModeDescription) window._updatePermModeDescription(el); },
    'm-pick-schedule-preset': el => { window.pickSchedulePreset(el); },
    'm-quick-launch-close': () => { const m = document.getElementById('quick-launch-modal'); if (m) m.style.display = 'none'; },
    'm-quick-launch-team': el => { window._quickLaunchTeam(); },
    'm-reset-default-prompt': el => { if (["orchestrator", "team_orchestrator", "team_worker", "worker"].includes(el.dataset.arg)) { window.resetDefaultPrompt(el.dataset.arg); } },
    'm-resume-launch-new': el => { window.resumeLaunchNew(); },
    'm-save-current-persona': el => { window._saveCurrentPersona(); },
    'm-save-default-prompts': el => { window.saveDefaultPrompts(); },
    'm-save-scheduled-job': el => { window.saveScheduledJob(); },
    'm-save-team-agent-from-modal': el => { window.saveTeamAgentFromModal(); },
    'm-save-team-template': el => { window._saveTeamTemplate(); },
    'm-save-view': el => { window._saveView(); },
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
    'm-close-top-bar-settings-then-check-for-updates': () => { window.__coral.closeTopBarSettings(); window.checkForUpdates(true); },
    'm-close-top-bar-settings-then-show-servers-modal': () => { window.__coral.closeTopBarSettings(); window.showServersModal(); },
    'm-close-top-bar-settings-then-show-settings-modal': () => { window.__coral.closeTopBarSettings(); window.showSettingsModal(); },
    'm-close-top-bar-settings-then-show-theme-configurator': () => { window.__coral.closeTopBarSettings(); window.showThemeConfigurator(); },
    'm-close-top-bar-settings-then-sleep-all-agents': () => { window.__coral.closeTopBarSettings(); window.sleepAllAgents(); },
    'm-close-top-bar-settings-then-stop-all-auto-accept': () => { window.__coral.closeTopBarSettings(); window.stopAllAutoAccept(); },
    'm-close-top-bar-settings-then-wake-all-agents': () => { window.__coral.closeTopBarSettings(); window.wakeAllAgents(); },
    'm-dismiss-auto-wake': () => { window.dismissAutoWake(); },
    'm-end-leftover-tmux': () => { window.endLeftoverTmux(); },
    'm-go-home': () => { window.__coral._goHome(); },
    'm-mobile-connect-open': (el, event) => { event.preventDefault(); window._showMobileConnectModal(); },
    'm-show-launch-modal-then-select-launch-type': el => { if (["agent", "team"].includes(el.dataset.arg)) { window.showLaunchModal(); window._selectLaunchType(el.dataset.arg); } },
    'm-switch-mobile-tab': el => { if (["agents", "board", "chat"].includes(el.dataset.arg)) { window.switchMobileTab(el.dataset.arg); } },
    'm-switch-nav-tab': el => { if (["agents", "board", "docs", "history", "jobs", "servers", "tokens", "workflows"].includes(el.dataset.arg)) { window.switchNavTab(el.dataset.arg); } },
    'm-tmux-banner-close': () => { const b = document.getElementById('tmux-missing-banner'); if (b) b.style.display = 'none'; },
    'm-toggle-agentic-panel': () => { window.toggleAgenticPanel(); },
    'm-toggle-tablet-sidebar': () => { window.toggleTabletSidebar(); },
    'm-toggle-top-bar-settings': () => { window.__coral.toggleTopBarSettings(); },
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
    'toggle-show-server': () => window.toggleShowServer(),
    'toggle-group-by-server': () => window.toggleGroupByServer(),
    'jobs-subtab': el => { if (el.dataset.tab === 'active' || el.dataset.tab === 'scheduled') window.switchJobsSubtab(el.dataset.tab); },
});
// The live agent view's header, view toggle, side panel tabs and command row (#3645). Restart is
// destructive (actions.js DESTRUCTIVE_ACTIONS: a popout hides it) and still asks first
// (restartSession's own confirmation). A side panel tab must be one the panel has.
const AGENTIC_TABS = new Set(['board', 'files', 'browse', 'artifacts', 'team-view', 'team-settings', 'team-artifacts', 'knowledge', 'tasks', 'notes', 'activity', 'agent-ui']);
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
const stopCostDashboard = () => { loadCostDashboard.module?.stopCostDashboard(); };
const { showDocsTab, selectDoc } = lazyFns(loadDocs, ['showDocsTab', 'selectDoc']);
const { showThemeConfigurator, hideThemeConfigurator } = lazyFns(loadThemeConfig, ['showThemeConfigurator', 'hideThemeConfigurator']);
const { showTemplateBrowser } = lazyFns(loadTemplateBrowser, ['showTemplateBrowser']);
const { searchChats } = lazyFns(loadChatSearch, ['searchChats']);
// The dialogs (#3140): their window functions are stubs until one opens.
const {
    showLaunchModal, hideLaunchModal, launchSession, showInfoModal, hideInfoModal, copyInfoCommand,
    showSettingsModal, hideSettingsModal, applySettings,
    toggleFlag, showAddAgentToBoard, hideAddAgentBoardModal, launchAgentToBoard, launchTerminalToBoard, launchDefaultAgent,
    showAddStandaloneAgent, launchStandaloneTerminal, exportPersonas, importPersonas, exportTeamTemplates, importTeamTemplates,
} = lazyFns(loadModals, [
    'showLaunchModal', 'hideLaunchModal', 'launchSession', 'showInfoModal', 'hideInfoModal', 'copyInfoCommand', 'showSettingsModal', 'hideSettingsModal', 'applySettings', 'toggleFlag', 'showAddAgentToBoard', 'hideAddAgentBoardModal', 'launchAgentToBoard', 'launchTerminalToBoard', 'launchDefaultAgent', 'showAddStandaloneAgent', 'launchStandaloneTerminal', 'exportPersonas', 'importPersonas', 'exportTeamTemplates', 'importTeamTemplates']);
const { showResumeModal, hideResumeModal, resumeLaunchNew } = lazyFns(loadResumeDialog, ['showResumeModal', 'hideResumeModal', 'resumeLaunchNew']);
const { showDefaultPromptsModal, hideDefaultPromptsModal, resetDefaultPrompt, saveDefaultPrompts } = lazyFns(loadDefaultPromptsDialog, ['showDefaultPromptsModal', 'hideDefaultPromptsModal', 'resetDefaultPrompt', 'saveDefaultPrompts']);
// Called from the home view's buttons before any dialog has loaded; modals.js sets them on window itself.
Object.assign(window, lazyWindowFns(loadModals, ['_selectLaunchType', '_showMobileConnectModal']));
import { initCloud } from './cloud.js';
import { isCloud } from './server_base.js';
import { installOverloadWatch } from './overload.js';
import { handleUnauthorized, isSignedOut401 } from './auth_redirect.js';
import { browserLocal } from './safe_storage.js';

// ── Expose functions to HTML onclick handlers ─────────────────────────────
// Grouped by source module for maintainability. Add new entries to the
// appropriate section rather than appending to the bottom.
Object.assign(window, {
    // api
    _coralLoadLiveSessions: loadLiveSessions,
    // test hooks (tests/frontend): inject a live-sessions payload and render it;
    // feed a /ws/coral message through the real handler without a socket.
    _coralSetLiveSessions: (sessions) => { state.liveSessions = sessions || []; renderLiveSessions(state.liveSessions); },
    _coralHandleWsMessage: handleCoralMessage,
    _coralResetWaitingSeed: _resetWaitingSeedForTests,
    _coralGetLiveSessions: () => state.liveSessions,
    // popout (/agent/{uuid}) + multi-window ownership
    popoutRetry, togglePopoutPanel, popoutWake,
    _coralPopout: popoutApi,
    _coralPopoutIsOwner: isInteractiveOwner,
    _coralPopoutClaim: claimOwnership,
    // controls
    sendCommand, sendCommandWithTeam, sendBoardProtocol, resendInputPrompt, sendRawKeys,
    sendModeToggle, cycleModeToggle, sendQuickCommand, refreshModeLabel,
    executeMacro, addMacro, deleteMacro, showMacroModal, hideMacroModal,
    attachTerminal, killSession, restartSession,
    editGoal, refreshGoal, requestGoal,
    hideRestartModal, confirmRestart, removeAttachment,
    // modals
    showSettingsModal, hideSettingsModal, applySettings,
    showDefaultPromptsModal, hideDefaultPromptsModal, resetDefaultPrompt, saveDefaultPrompts,
    showLaunchModal, hideLaunchModal, launchSession, toggleFlag,
    showInfoModal, hideInfoModal, copyInfoCommand,
    showResumeModal, hideResumeModal, resumeLaunchNew,
    showAddAgentToBoard, hideAddAgentBoardModal, launchAgentToBoard, launchTerminalToBoard,
    launchDefaultAgent, showAddStandaloneAgent, launchStandaloneTerminal,
    exportPersonas, importPersonas, exportTeamTemplates, importTeamTemplates,
    // sessions
    selectLiveSession, selectHistorySession, editAndResubmit, renameAgent, setAgentIcon,
    searchChats,
    // xterm
    getTerminal, connectTerminalWs, disconnectTerminalWs,
    // notes
    loadSessionNotes, saveNotes, generateSummary, resummarize, toggleNotesEdit, cancelNotesEdit, switchHistoryTab,
    // tags
    loadSessionTags, addTagToSession, removeTagFromSession, showTagDropdown, hideTagDropdown, createTag,
    // changed_files
    loadChangedFiles, openFilePreview, openFileEdit, refreshChangedFiles,
    toggleGitDiffMode, setGitDiffMode, toggleStarFile, copyFilePath, searchRepoFiles, renderStarredFiles,
    // tasks
    loadAgentTasks, addAgentTask, toggleAgentTask, deleteAgentTask, editAgentTaskTitle, loadBoardTasks, renderBoardTaskList,
    showTaskDetailModal, hideTaskDetailModal, loadMoreBoardTasks,
    showCreateTaskModal, hideCreateTaskModal, submitCreateTask,
    enableTaskEditMode, saveTaskEdit, cancelTaskEdit,
    completeBoardTask, cancelBoardTask, _doCompleteTask, _doCancelTask, _restoreTaskFooter, publishBoardTask, nudgeBoardTask, remindBoardTask, stopBoardTaskReminder,
    // agent_notes
    loadAgentNotes,
    // agentic_state
    switchAgenticTab, loadAgentEvents,
    // live_chat
    setLiveViewMode, showTerminalView,
    toggleEventFilter, toggleAllEventFilters, toggleFilterDropdown, showFilterPopup, hideFilterPopup,
    // history_tabs
    toggleHistoryEventFilter, toggleAllHistoryEventFilters,
    // scheduler
    selectScheduledJob, toggleScheduledJob, deleteScheduledJob, editScheduledJob,
    showJobModal, hideJobModal, validateCronPreview, saveScheduledJob, switchJobType, pickSchedulePreset,
    // live_jobs
    selectLiveJobRun,
    // preview_pane
    openPreviewTab, closePreviewTab, closeAllPreviewTabs, resetPreviewPane,
    // sidebar
    switchJobsSubtab, toggleAgenticPanel, toggleSidebarRail,
    // browser
    toggleBrowser, browserNavigateTo, browserNavigateUp, browserSelectRecent,
    // theme
    showThemeConfigurator, hideThemeConfigurator,
    // servers (hub mode)
    showServersModal, hideServersModal,
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
    selectBoardProject, showMessageBoardProjects, postBoardMessage,
    deleteMessageBoardProject, confirmDeleteBoard: deleteMessageBoardProject,
    toggleBoardPause, toggleBoardSleep, deleteBoardMessage,
    showExportBoardModal, doExportBoard,
    toggleSelectMode, toggleMessageSelect, selectAllMessages, selectNoneMessages,
    cancelSelectMode, exportSelectedAsMarkdown, openBoardSubscribers, hideBoardSubscribers,
    // workflows
    showWorkflowsTab,
    selectWorkflow, selectWorkflowRun, triggerWorkflow, killWorkflowRun, deleteWorkflow,
    showWorkflowCreateModal, hideWorkflowCreateModal, editWorkflow,
    workflowAddStep, workflowStepTypeChanged, saveWorkflow, workflowsBackToList, launchWorkflowAgent, editWorkflowWithAgent,
    // cost_dashboard
    showCostDashboard, _refreshCostDashboard, _costTimeRangeChanged,
    // docs
    showDocsTab, selectDoc,
    // folder_tags
    showFolderTagDropdown, hideFolderTagDropdown, addFolderTag, removeFolderTag, createAndAddFolderTag,
    // utils
    copyBranchName,
    // router
    _activateCustomView: activateCustomView, _noteRoute: note,
    // render (pagination)
    loadHistoryPage,
    // top nav
    switchNavTab,
});

// ── Reset Team ───────────────────────────────────────────────────────
function resetTeam(boardName) {
    showConfirmModal('Reset Team', `Reset all agents in "${keyLabel(boardName)}"? Their context will be cleared and they'll restart with their original prompts.`, async () => {
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
    // The URL follows the tab (#3532): a tab the user chose is a history entry. Servers writes its
    // own (it can carry a server); the board tab names no board, so it keeps the URL.
    const TAB_ROUTE = { agents: 'agents', history: 'chats', workflows: 'workflows', jobs: 'jobs', tokens: 'analytics', docs: 'docs' };
    if (!opts.fromHistory && TAB_ROUTE[tab]) {
        const cur = currentRoute();
        const route = tabRoute(tab, TAB_ROUTE[tab]);
        if (!cur || cur.view !== route.view || cur.id !== route.id) note(route);
    }

    // Toggle sidebar visibility — full-width views hide the sidebar
    const fullWidthTabs = new Set(['tokens', 'workflows', 'servers']);
    const layout = document.querySelector('.layout');
    if (layout) layout.classList.toggle('sidebar-hidden', fullWidthTabs.has(tab));

    // Update the nav buttons: page navigation, so the current one is aria-current="page".
    document.querySelectorAll('.top-nav-tab').forEach(btn => { btn.classList.remove('active'); btn.removeAttribute('aria-current'); });
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
        showView('messageboard-view');
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
    if (tab === 'history') return cur && cur.type === 'history' ? { view: 'chats', id: cur.name } : { view: 'chats', query: currentChatsQuery() };
    return { view };
}

// ── Import team from folder ───────────────────────────────────────────
function _importTeamFromFolder() {
    const input = document.getElementById('team-folder-input');
    if (input) { input.value = ''; input.click(); }
};

async function _handleTeamFolderImport(input) {
    const files = Array.from(input.files || []);
    if (!files.length) return;

    // Parse YAML frontmatter from markdown content
    function parseFrontmatter(text) {
        const match = text.match(/^---\s*\n([\s\S]*?)\n---\s*\n([\s\S]*)$/);
        if (!match) return { meta: {}, body: text.trim() };
        const meta = {};
        match[1].split('\n').forEach(line => {
            const m = line.match(/^(\w[\w-]*):\s*(.+)$/);
            if (m) meta[m[1].trim()] = m[2].trim().replace(/^["']|["']$/g, '');
        });
        return { meta, body: match[2].trim() };
    }

    // Find SKILL.md (orchestrator) and agents/*.md
    let orchestrator = null;
    const agents = [];

    let folderName = '';
    for (const file of files) {
        if (!file.name.endsWith('.md')) continue;
        const path = file.webkitRelativePath || file.name;
        const text = await file.text();
        const { meta, body } = parseFrontmatter(text);

        // Extract top-level folder name for team board name
        if (!folderName && path.includes('/')) {
            folderName = path.split('/')[0];
        }

        if (file.name.match(/^SKILL\.md$/i)) {
            orchestrator = { name: 'Orchestrator', description: meta.description || '', prompt: body };
        } else if (path.match(/\/agents\//i) || file.name !== 'SKILL.md') {
            const name = meta.name || file.name.replace(/\.md$/, '').replace(/-/g, ' ');
            agents.push({ name, prompt: body });
        }
    }

    if (!orchestrator && agents.length === 0) {
        const { showToast } = await importOnce('./utils.js');
        showToast('No valid .md files found (expected SKILL.md and/or agents/*.md)', true);
        return;
    }

    // Set team board name from folder name if empty
    const boardNameInput = document.getElementById('team-board-name');
    if (boardNameInput && !boardNameInput.value.trim() && folderName) {
        boardNameInput.value = folderName.replace(/-/g, ' ');
        if (window._validateTeamName) window._validateTeamName();
    }

    // Clear existing agents and populate
    const list = document.getElementById('team-agents-list');
    if (list) list.innerHTML = '';

    // Fetch default prompts from settings for orchestrator/worker board instructions
    let defaults = {};
    try {
        const res = await fetch('/api/settings/default-prompts');
        defaults = await res.json();
    } catch { /* use empty defaults */ }
    const orchSuffix = (defaults.default_prompt_orchestrator || 'Coordinate with the team via the message board.').replace('{board_name}', folderName || 'team');
    const workerSuffix = (defaults.default_prompt_worker || 'Coordinate with the team via the message board.').replace('{board_name}', folderName || 'team');

    if (orchestrator) {
        // Strip the name/title heading from the prompt body
        let body = orchestrator.prompt.replace(/^#\s+.*\n*/m, '').trim();
        const prompt = `You are the Orchestrator. For this session here is the skill you will use:\n\n${body}\n\n${orchSuffix}`;
        if (window._addTeamAgent) window._addTeamAgent(orchestrator.name, prompt);
    }
    for (const agent of agents) {
        let body = agent.prompt.replace(/^#\s+.*\n*/m, '').trim();
        const prompt = `For this session here is the skill you will use:\n\n${body}\n\n${workerSuffix}`;
        if (window._addTeamAgent) window._addTeamAgent(agent.name, prompt);
    }

    const { showToast } = await importOnce('./utils.js');
    const count = (orchestrator ? 1 : 0) + agents.length;
    showToast(`Imported ${count} agent${count !== 1 ? 's' : ''} from folder`);
};

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
// ── Sidebar kebab menu helpers ───────────────────────────────────────────
function closeSidebarKebabs() {
    document.querySelectorAll('.sidebar-kebab-menu').forEach(m => m.style.display = 'none');
}

function toggleSidebarKebab(btn) {
    const menu = btn.nextElementSibling;
    const wasOpen = menu.style.display !== 'none';
    closeSidebarKebabs();
    if (!wasOpen) {
        // Show off-screen first to measure, then position
        menu.style.visibility = 'hidden';
        menu.style.display = 'block';
        const rect = btn.getBoundingClientRect();
        const menuHeight = menu.offsetHeight || 150;
        const menuWidth = menu.offsetWidth || 160;
        const viewportHeight = window.innerHeight;
        const viewportWidth = window.innerWidth;
        // If menu would overflow the bottom, position above the button
        if (rect.bottom + menuHeight + 4 > viewportHeight) {
            menu.style.top = Math.max(4, rect.top - menuHeight - 2) + 'px';
        } else {
            menu.style.top = rect.bottom + 2 + 'px';
        }
        // Clamp left so menu doesn't overflow right edge
        const left = Math.min(rect.left, viewportWidth - menuWidth - 8);
        menu.style.left = Math.max(4, left) + 'px';
        menu.style.visibility = '';
        // Hide any visible tooltips so they don't cover the menu
        document.querySelectorAll('.session-tooltip').forEach(t => t.style.display = 'none');
    }
}

Object.assign(window, {
    toggleSidebarKebab, closeSidebarKebabs,
    // render (sidebar actions)
    filterLiveAgents, focusAgentSearch, agentSearchKey, toggleSessionSummary, applySessionSummaryPref,
    toggleShowServer, toggleGroupByServer, renderServerSwitcher,
    toggleGroupCollapse, groupHeaderKey, killGroup, moveGroupUp, moveGroupDown,
    copyFolderPath, killBoard, dismissBoardKilled, setBoardAccentColor, resetTeam,
    moveSessionUp, moveSessionDown,
    toggleTeamSleep, toggleAgentSleep, toggleAgentArchive, toggleArchivedSection, sleepAllAgents, wakeAllAgents,
    enterTeamContext, exitTeamContext, renderTeamSettingsTab,
    showTeamDetails, hideTeamDetails, buildSessionTooltip, showTeamAvailability, showTeamAvailabilityWorkspace, showTeamWorkingMode, showTeamWorkingModeWorkspace,
    shareAgentTeam, saveTeamFromSidebar, showTeamTokenUsage, distillTeamKnowledge, showTeamKnowledge,
    showConfirmModal, hideConfirmModal, showPromptModal, hidePromptModal, showAlertModal, hideAlertModal,
    killSessionDirect, dismissKilledSession, showInfoDirect, attachDirect, restartDirect,
    toggleGroupByTeam, expandSleepingGroup,
    // template_browser
    showTemplateBrowser,
});

// Top-bar settings dropdown
function toggleTopBarSettings() {
    const menu = document.getElementById('top-bar-settings-menu');
    if (!menu) return;
    const isOpen = menu.style.display !== 'none';
    menu.style.display = isOpen ? 'none' : '';
    // Sync group-by-team checkmark
    const check = document.getElementById('group-by-team-check-top');
    if (check) {
        const groupByTeam = browserLocal.get('coral-group-by-team') === 'true';
        check.style.opacity = groupByTeam ? '1' : '0.2';
    }
}
function closeTopBarSettings() {
    const menu = document.getElementById('top-bar-settings-menu');
    if (menu) menu.style.display = 'none';
}
(window.__coral ||= {}).toggleTopBarSettings = toggleTopBarSettings;
(window.__coral ||= {}).closeTopBarSettings = closeTopBarSettings;


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
let historyPage = 1;  // page number only; all other filter state lives in filterState

function loadHistoryPage(page) {
    historyPage = page;
    loadHistoryFiltered();
}

function loadHistoryFiltered() {
    serializeToUrl(historyPage);
    const loaded = filterState.q ? searchChats(filterState.q, historyPage) : loadHistorySessionsPaged(historyPage, 50);
    updateFilterBadge();
    return loaded;
}

async function populateHfTagSelect() {
    const tags = await loadAllTags();
    const sel = document.getElementById('hf-tag-add');
    if (!sel || !tags) return;
    sel.innerHTML = '<option value="">+ tag</option>';
    for (const tag of tags) {
        const opt = document.createElement('option');
        opt.value = tag.id;
        opt.textContent = tag.name;
        sel.appendChild(opt);
    }
}

function renderFilterTagPills() {
    const container = document.getElementById('hf-tag-pills');
    if (!container) return;
    const sel = document.getElementById('hf-tag-add');
    const tagMap = {};
    if (sel) {
        for (const opt of sel.options) {
            if (opt.value) tagMap[parseInt(opt.value)] = opt.textContent;
        }
    }
    container.innerHTML = filterState.tagIds.map(id => {
        const name = tagMap[id] || `Tag ${id}`;
        return `<span class="hf-tag-pill" data-tag-id="${Number(id)}">
            ${escapeHtml(name)}
            <span class="hf-tag-remove">&times;</span>
        </span>`;
    }).join('');

    container.querySelectorAll('.hf-tag-remove').forEach(btn => {
        btn.addEventListener('click', (e) => {
            const tagId = parseInt(e.target.closest('[data-tag-id]').dataset.tagId);
            filterState.tagIds = filterState.tagIds.filter(id => id !== tagId);
            renderFilterTagPills();
            const logicRow = document.getElementById('hf-tag-logic-row');
            if (logicRow) logicRow.style.display = filterState.tagIds.length > 1 ? '' : 'none';
            historyPage = 1;
            loadHistoryFiltered();
        });
    });
}

function updateFilterBadge() {
    const badge = document.getElementById('hf-active-count');
    if (!badge) return;
    const n = countActiveFilters();
    badge.textContent = String(n);
    badge.style.display = n > 0 ? '' : 'none';
}

function syncFilterDomToState() {
    const searchInput = document.getElementById('history-search');
    if (searchInput) searchInput.value = filterState.q;

    const dateFrom = document.getElementById('hf-date-from');
    if (dateFrom) dateFrom.value = filterState.dateFrom;
    const dateTo = document.getElementById('hf-date-to');
    if (dateTo) dateTo.value = filterState.dateTo;

    const durMin = document.getElementById('hf-dur-min');
    if (durMin && filterState.minDurationSec != null)
        durMin.value = String(Math.round(filterState.minDurationSec / 60));
    const durMax = document.getElementById('hf-dur-max');
    if (durMax && filterState.maxDurationSec != null)
        durMax.value = String(Math.round(filterState.maxDurationSec / 60));

    document.querySelectorAll('[data-chat-type]').forEach(b => {
        b.classList.toggle('active', b.dataset.chatType === (filterState.chatType || 'all'));
    });
    document.querySelectorAll('[data-source]').forEach(b => {
        if (b.dataset.source === 'all')
            b.classList.toggle('active', filterState.sourceTypes.length === 0);
        else
            b.classList.toggle('active', filterState.sourceTypes.includes(b.dataset.source));
    });
    document.querySelectorAll('[data-logic]').forEach(b =>
        b.classList.toggle('active', b.dataset.logic === filterState.tagLogic));
    document.querySelectorAll('[data-mode]').forEach(b =>
        b.classList.toggle('active', b.dataset.mode === filterState.ftsMode));

    if (hasActiveFilters()) {
        const panel = document.getElementById('hf-advanced');
        if (panel) panel.style.display = '';
    }

    const ftsModeRow = document.getElementById('hf-fts-mode-row');
    if (ftsModeRow) ftsModeRow.style.display = filterState.q ? '' : 'none';

    renderFilterTagPills();
    updateFilterBadge();
}

// window.__coralReady 'liveListLoaded': the page start loads the live list twice, once at
// once and once when the startup check finds the server ready (the two join when they
// overlap). The part is marked when both have settled, loaded or failed, so a test that
// injects state after it can't have a late list answer land on top. A startup check that
// fails, or a page without one, counts as settled for this, so the signal never hangs.
let _startListLoads = 2;
const _startListLoadSettled = () => { if (--_startListLoads === 0) markReady('liveListLoaded'); };
let _pollListLoadSettled = false;
const _settlePollListLoad = () => { if (!_pollListLoadSettled) { _pollListLoadSettled = true; _startListLoadSettled(); } };

function pollStartupStatus() {
    const el = document.getElementById('startup-loading');
    if (!el) { _settlePollListLoad(); return; }
    let firstCheck = true;   // the first answer may be the one the page's other startup callers share; a retry must be fresh
    const check = async () => {
        // The hub has no local startup phase: each machine is its own server, and the merged list
        // fills in as the feed reports them, so there is nothing to wait for here.
        if (isCloud()) {
            el.classList.add('hidden');
            dashboardStartupComplete();
            loadLiveSessions(dashboardFailed).then(dashboardSessionsLoaded).finally(_settlePollListLoad);
            return;
        }
        try {
            const resp = await fetchSystemStatus({ fresh: !firstCheck });
            firstCheck = false;
            if (!resp.ok) { dashboardFailed('status_fetch_http'); _settlePollListLoad(); setTimeout(check, 2000); return; }
            const data = await resp.json();
            if (!data || typeof data.startup_complete !== 'boolean') throw new SyntaxError('Invalid startup status');
            if (data.startup_complete) {
                el.classList.add('hidden');
                if (data.security_warning) showSecurityWarning(data.security_warning); // #3637
                dashboardStartupComplete();
                // One question at a time: set a password (#3948, once, while none is set), then Coral Hub sign-in (#3966, once, for a
                // computer already linked to Coral Hub). The second waits if the first is still open.
                importOnce('./password_ui.js').then(m => m.maybePromptSetPassword()).catch(() => {})
                    .then(() => importOnce('./hub_notice.js')).then(m => m.maybeOfferHubSignin()).catch(() => {});
                loadLiveSessions(dashboardFailed).then(dashboardSessionsLoaded).finally(_settlePollListLoad);
                return;
            }
        } catch (error) { dashboardFailed(error instanceof SyntaxError ? 'status_fetch_invalid' : 'status_fetch_network'); _settlePollListLoad(); }
        setTimeout(check, 2000);
    };
    check();
}

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

// Settles once the first live list has loaded (or failed): an agent link needs the list.
let _liveListLoaded;
const liveListReady = new Promise(r => { _liveListLoaded = r; });

/** The Chats list's query for its current filters and page (no "?"). */
function currentChatsQuery() {
    return urlSearchFor(historyPage, '').replace(/^\?/, '');
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
        if (r.id) {
            switchNavTab('history', { fromHistory: true });
            const cur = state.currentSession;
            if (!(cur && cur.type === 'history' && cur.name === r.id) && (await selectHistorySession(r.id)) === false) {
                state.currentSession = null;
                notFound('That chat was not found', { view: 'chats', query: currentChatsQuery() });
            }
            return;
        }
        // The list: its filters are the URL's (startup already read them).
        if (!opts.startup) {
            historyPage = deserializeFromUrl().page;
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
// What was typed and where people worked, kept in this browser (local_store.js, #3681): owned by the
// signed-in Coral Hub account (none locally), aged out at start and daily in a tab left open.
async function initLocalStore() {
    let tag = '';
    try { tag = state.account ? await accountTag(state.account.email) : ''; } catch { tag = ''; }
    setStoreOwner(tag);
    loadSessionDrafts(ageOutStored());
    loadPendingFromStorage();
    setInterval(() => loadSessionDrafts(ageOutStored()), DAY_MS); // interval-ok: daily, browser storage only, no request
}

document.addEventListener("DOMContentLoaded", async () => {
    beginDashboardAnalytics();
    try {
    // Initialize platform detection and platform-specific behavior
    platform.init();
    if (platform.isNative) {
        initNative();
        if (platform.isMacOS)   initMacOS();
        if (platform.isWindows) initWindows();
    } else {
        initBrowser();
    }

    // A 401 from Coral's /api (session ended; strict mode #3418): to /auth?return_to=this page,
    // once (auth_redirect.js). The Coral Hub has its own redirect (cloud.js).
    const _origFetch = window.fetch;
    window.fetch = async function(...args) {
        const resp = await _origFetch.apply(this, args);
        // Only a 401 that means signed out goes to /auth (#3528); the caller gets the response either way.
        if (resp.status === 401 && !isCloud() && _sameOriginApi(args[0])) {
            resp.clone().text().then(t => { if (isSignedOut401(t)) handleUnauthorized(); }).catch(() => handleUnauthorized());
        }
        return resp;
    };

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

    // Restore filter state from URL query params before first load
    const restored = deserializeFromUrl();
    historyPage = restored.page;

    // The hash the page was opened with: a deep link to restore. A hash the user
    // sets while the list loads (e.g. opening Servers) is not restored again,
    // which would re-open that view a second time (#3152).
    loadLiveSessions(dashboardFailed).then(ok => {
        markReady('liveList'); // already marked by the render; this covers a failed first load
        dashboardSessionsLoaded(ok);
    }).finally(() => { _startListLoadSettled(); _liveListLoaded(); });
    loadAllFolderTags();
    if (!popout) loadPinnedFolders();
    if (!popout) importOnce('./terminal_backend.js').then(m => { m.checkLeftoverTmux(); m.watchAutoWake(); m.watchHooksStatus(); }).catch(() => {}); // #3634, #3636, #3665
    loadCustomViews();
    initTabCustomize();
    initAgentUI();
    connectCoralWs();
    if (!popout) checkForUpdates();
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

    initPrerequisiteChecks();
    attachAllDialogFocus();   // focus in and back for every dialog in the shell (#4026)

    // ── Filter event wiring ─────────────────────────────────────────────

    // Search bar with debounce
    const searchInput = document.getElementById('history-search');
    if (searchInput) {
        let debounceTimer;
        searchInput.addEventListener('input', () => {
            clearTimeout(debounceTimer);
            debounceTimer = setTimeout(() => {
                filterState.q = searchInput.value.trim();
                historyPage = 1;
                const ftsModeRow = document.getElementById('hf-fts-mode-row');
                if (ftsModeRow)
                    ftsModeRow.style.display = filterState.q ? '' : 'none';
                loadHistoryFiltered();
            }, 300);
        });
    }

    // Advanced panel toggle
    const advToggle = document.getElementById('hf-adv-toggle');
    if (advToggle) {
        advToggle.addEventListener('click', () => {
            const panel = document.getElementById('hf-advanced');
            if (panel) panel.style.display = panel.style.display === 'none' ? '' : 'none';
        });
    }

    // FTS mode buttons
    document.querySelectorAll('[data-mode]').forEach(btn => {
        btn.addEventListener('click', () => {
            filterState.ftsMode = btn.dataset.mode;
            document.querySelectorAll('[data-mode]')
                .forEach(b => b.classList.toggle('active', b.dataset.mode === filterState.ftsMode));
            historyPage = 1;
            loadHistoryFiltered();
        });
    });

    // Source toggle buttons
    document.querySelectorAll('[data-source]').forEach(btn => {
        btn.addEventListener('click', () => {
            const source = btn.dataset.source;
            if (source === 'all') {
                filterState.sourceTypes = [];
            } else {
                const idx = filterState.sourceTypes.indexOf(source);
                if (idx >= 0) filterState.sourceTypes.splice(idx, 1);
                else filterState.sourceTypes.push(source);
            }
            document.querySelectorAll('[data-source]').forEach(b => {
                if (b.dataset.source === 'all')
                    b.classList.toggle('active', filterState.sourceTypes.length === 0);
                else
                    b.classList.toggle('active', filterState.sourceTypes.includes(b.dataset.source));
            });
            historyPage = 1;
            loadHistoryFiltered();
        });
    });

    // Chat type toggle buttons
    document.querySelectorAll('[data-chat-type]').forEach(btn => {
        btn.addEventListener('click', () => {
            filterState.chatType = btn.dataset.chatType;
            document.querySelectorAll('[data-chat-type]').forEach(b => {
                b.classList.toggle('active', b.dataset.chatType === filterState.chatType);
            });
            historyPage = 1;
            loadHistoryFiltered();
        });
    });

    // Tag add select
    const tagAddSel = document.getElementById('hf-tag-add');
    if (tagAddSel) {
        tagAddSel.addEventListener('change', () => {
            const id = parseInt(tagAddSel.value);
            if (!id || filterState.tagIds.includes(id)) return;
            filterState.tagIds.push(id);
            tagAddSel.value = '';
            renderFilterTagPills();
            const logicRow = document.getElementById('hf-tag-logic-row');
            if (logicRow) logicRow.style.display = filterState.tagIds.length > 1 ? '' : 'none';
            historyPage = 1;
            loadHistoryFiltered();
        });
    }

    // Tag logic buttons
    document.querySelectorAll('[data-logic]').forEach(btn => {
        btn.addEventListener('click', () => {
            filterState.tagLogic = btn.dataset.logic;
            document.querySelectorAll('[data-logic]')
                .forEach(b => b.classList.toggle('active', b.dataset.logic === filterState.tagLogic));
            historyPage = 1;
            loadHistoryFiltered();
        });
    });

    // Date filters
    const dateFrom = document.getElementById('hf-date-from');
    const dateTo = document.getElementById('hf-date-to');
    if (dateFrom) dateFrom.addEventListener('change', () => {
        filterState.dateFrom = dateFrom.value || '';
        historyPage = 1;
        loadHistoryFiltered();
    });
    if (dateTo) dateTo.addEventListener('change', () => {
        filterState.dateTo = dateTo.value || '';
        historyPage = 1;
        loadHistoryFiltered();
    });

    // Duration filters
    const durMin = document.getElementById('hf-dur-min');
    const durMax = document.getElementById('hf-dur-max');
    if (durMin) durMin.addEventListener('change', () => {
        const val = parseFloat(durMin.value);
        filterState.minDurationSec = isNaN(val) ? null : Math.round(val * 60);
        historyPage = 1;
        loadHistoryFiltered();
    });
    if (durMax) durMax.addEventListener('change', () => {
        const val = parseFloat(durMax.value);
        filterState.maxDurationSec = isNaN(val) ? null : Math.round(val * 60);
        historyPage = 1;
        loadHistoryFiltered();
    });

    // Clear all filters button
    const clearBtn = document.querySelector('.hf-clear-btn');
    if (clearBtn) {
        clearBtn.addEventListener('click', () => {
            resetFilters();
            if (searchInput) searchInput.value = '';
            if (dateFrom) dateFrom.value = '';
            if (dateTo) dateTo.value = '';
            if (durMin) durMin.value = '';
            if (durMax) durMax.value = '';
            document.querySelectorAll('[data-chat-type]')
                .forEach(b => b.classList.toggle('active', b.dataset.chatType === 'all'));
            document.querySelectorAll('[data-source]')
                .forEach(b => b.classList.toggle('active', b.dataset.source === 'all'));
            document.querySelectorAll('[data-logic]')
                .forEach(b => b.classList.toggle('active', b.dataset.logic === 'AND'));
            document.querySelectorAll('[data-mode]')
                .forEach(b => b.classList.toggle('active', b.dataset.mode === 'or'));
            renderFilterTagPills();
            historyPage = 1;
            loadHistoryFiltered();
        });
    }

    // Image drag-and-drop on command pane
    initImageDrop();

    // File search in files panel + top bar
    initFileSearch();
    initTopBarSearch();

    // @file mention autocomplete
    initFileMention();

    // /command mention autocomplete
    initCommandMention();

    // Markdown notes panel: click-to-edit, blur-to-save
    initNotesMd();

    // Enter in macro modal submits
    document.getElementById("macro-command-input").addEventListener("keydown", (e) => {
        if (e.key === "Enter") { e.preventDefault(); addMacro(); }
    });

    // Chat mode: the command box grows with its text
    initCommandInputAutosize();
    initChatFind();

    // Enter sends command, Shift+Enter inserts newline
    const cmdInput = document.getElementById("command-input");
    cmdInput.addEventListener("keydown", (e) => {
        if (e.key === "Enter" && !e.shiftKey) {
            e.preventDefault();
            sendCommand();
        }
    });
    cmdInput.addEventListener("input", () => {
        const key = sessionKey(state.currentSession);
        if (key) saveSessionDraft(key, cmdInput.value);
    });

    // Global keyboard shortcuts removed — terminal input now handled by
    // xterm.js onData when the terminal is focused.

    // Auto-scroll detection for capture pane and live history
    const capture = document.getElementById("pane-capture");
    capture.addEventListener("scroll", () => {
        const { scrollTop, scrollHeight, clientHeight } = capture;
        state.autoScroll = (scrollHeight - scrollTop - clientHeight) < 50;
    });
    const liveHistory = document.getElementById("live-history-messages");
    if (liveHistory) {
        liveHistory.addEventListener("scroll", () => {
            const { scrollTop, scrollHeight, clientHeight } = liveHistory;
            state.autoScroll = (scrollHeight - scrollTop - clientHeight) < 50;
        });
    }

    // Pause capture updates while user is selecting text inside the terminal pane
    const pauseBadge = document.getElementById("selection-pause-badge");
    const captureWrapper = document.getElementById("capture-wrapper");
    document.addEventListener("selectionchange", () => {
        const sel = window.getSelection();
        const hasSelection = sel && sel.toString().length > 0;
        const inCapture = hasSelection && captureWrapper && sel.anchorNode && captureWrapper.contains(sel.anchorNode);
        state.isSelecting = !!inCapture;
        if (pauseBadge) pauseBadge.style.display = state.isSelecting ? "" : "none";
    });

    // Sidebar collapsible sections
    initSidebarCollapse();
    initAgenticPanelCollapse();
    initAgenticBlockResize();
    initAgenticBlockCollapse();

    // Overflow menu close on outside click
    document.addEventListener('click', (e) => {
        const openMenus = document.querySelectorAll('.overflow-menu[style*="block"]');
        for (const menu of openMenus) {
            if (!menu.parentElement.contains(e.target)) {
                menu.style.display = 'none';
            }
        }
    });
    // Overflow menu toggle
    document.addEventListener('click', (e) => {
        const trigger = e.target.closest('.overflow-menu-trigger');
        if (!trigger) return;
        e.stopPropagation();
        const menu = trigger.nextElementSibling;
        if (menu && menu.classList.contains('overflow-menu')) {
            menu.style.display = menu.style.display === 'none' ? 'block' : 'none';
        }
    });

    // Resize handles
    initSidebarResize();
    initSidebarRail();
    initTaskBarResize();
    initBoardChatResize();
    initPreviewPane();
    restoreAgenticTabs();

    // Initialize nav tabs — hide history section on load (Agents tab is default)
    if (!popout) {
        switchNavTab('agents');
    } else {
        initPopout();
    }

    // Pause polling when tab is hidden; refresh immediately when visible again.
    // Skip in native apps — WKWebView may report document.hidden=true permanently.
    if (!platform.isNative) {
        document.addEventListener("visibilitychange", () => {
            if (!document.hidden && state.currentSession && state.currentSession.type === "live") {
                refreshCapture();
                // The transcript poll skips hidden windows, so a window that
                // was in the background has no chat yet (or a stale one).
                // Waiting for the next tick to notice shows an empty pane.
                refreshLiveHistory();
            }
        });
    }

    // Sync tmux pane width on window resize and panel drag
    let resizeDebounce;
    window.addEventListener("resize", () => {
        clearTimeout(resizeDebounce);
        resizeDebounce = setTimeout(syncPaneWidth, 300);
        fitTerminal();
    });
    // Re-sync after sidebar/task-bar drag ends (the panels change available width)
    document.addEventListener("mouseup", () => {
        setTimeout(() => { fitTerminal(); syncPaneWidth(); }, 50);
    });

    // The page's route (#3532): its path, or an old hash link, shown once the handlers are in place.
    if (!popout) {
        registerRouteHandlers();
        startNav();
    }
    dashboardInitialized();
    } catch (error) {
        dashboardFailed('init_failed');
        throw error;
    }
});


// Whether a fetch target is this origin's /api (a 401 from elsewhere is not Coral's session).
function _sameOriginApi(input) {
    try {
        const u = new URL(typeof input === 'string' || input instanceof URL ? String(input) : input.url, location.href);
        return u.origin === location.origin && u.pathname.startsWith('/api/');
    } catch { return false; }
}
