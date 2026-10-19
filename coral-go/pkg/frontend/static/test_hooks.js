/* The page's test surface (#5015): what the browser suites reach into that no product code uses. It was a layer of window functions in app.js and exports
 * kept only because a test mentioned them (js_unused exempted both). They live here now and nowhere else, behind one namespace, window.__coralTest,
 * installed only when the page's boot data says testHooks (a dev build started with CORAL_TEST_HOOKS=1, which the suites' runner sets; a release build
 * never asks for it). The window functions the suites used to call (window.sendCommand ...) are also set on window from here, for the suites that have
 * not moved to the namespace yet. Taking the hooks out of a build is deleting this file and the one line in app.js that imports it. */
import { state } from './state.js';
import { testHooksOn } from './server_base.js';
import { handleCoralMessage, _resetWaitingSeedForTests } from './websocket.js';
import { popoutApi } from './popout.js';
import { renderLiveSessions, buildSessionTooltip, lastLiveListHtml, flushLiveSessionsRender } from './render.js';
import { sendCommand, sendCommandWithTeam, sendBoardProtocol, resendInputPrompt, sendRawKeys, sendModeToggle, cycleModeToggle, sendQuickCommand, executeMacro, deleteMacro, killSession } from './controls.js';
import { getTerminal, terminalInputAcks } from './xterm_renderer.js';
import { switchHistoryTab } from './notes.js';
import { loadAgentTasks, loadBoardTasks, renderBoardTaskList } from './tasks.js';
import { loadChangedFiles } from './changed_files.js';
import { loadAgentEvents } from './agentic_state.js';
import { selectScheduledJob, editScheduledJob } from './scheduler.js';
import { closeAllTabs as closeAllPreviewTabs, openPreviewTab } from './preview_pane.js';
import { hideServersModal } from './servers.js';
import { cancelSelectMode, deleteMessageBoardProject, showExportBoardModal, showMessageBoardProjects, postBoardMessage, toggleSelectMode } from './message_board.js';
import { hideFolderTagDropdown } from './folder_tags.js';
import { showTeamAvailability } from './team_availability.js';
import { showTeamWorkingMode } from './team_working_mode.js';
import { lazyFns, loadWorkflows, loadTemplateBrowser, loadChatSearch, loadModals, loadResumeDialog } from './lazy_panels.js';
import { hasAction } from './actions.js';
import { boardSearchState } from './board_search.js';
import { capturePollStats } from './capture.js';
import { dropChatPrefetch } from './chat_page.js';
import { _resetRateForTests } from './desktop_notify.js';
import { resetMorph } from './dom_morph.js';
import { _resetLivePrune } from './local_store.js';
import { countsAsOverload, _resetOverloadForTests } from './overload.js';
import { PANEL_HOST_CSP } from './panel_sandbox.js';
import { serverStatus, serverBadgeHtml, serverStripHtml } from './server_status.js';
import { _boardBodyCacheStatsForTests } from './utils.js';
import { HOST_CSP } from './view_sandbox.js';

// The panels and dialogs that load on first use: the same stubs the page's own buttons would have called.
const { triggerWorkflow, showWorkflowCreateModal, editWorkflow, saveWorkflow, workflowsBackToList } =
    lazyFns(loadWorkflows, ['triggerWorkflow', 'showWorkflowCreateModal', 'editWorkflow', 'saveWorkflow', 'workflowsBackToList']);
const { showTemplateBrowser } = lazyFns(loadTemplateBrowser, ['showTemplateBrowser']);
const { searchChats } = lazyFns(loadChatSearch, ['searchChats']);
const { showInfoModal, exportTeamTemplates } = lazyFns(loadModals, ['showInfoModal', 'exportTeamTemplates']);
const { showResumeModal } = lazyFns(loadResumeDialog, ['showResumeModal']);

// What the suites called as window functions.
const windowHooks = {
    // feed a live-sessions payload, or a /ws/coral message, through the real handlers without a socket
    _coralSetLiveSessions: (sessions) => { state.liveSessions = sessions || []; renderLiveSessions(state.liveSessions); },
    _coralHandleWsMessage: handleCoralMessage,
    _coralGetLiveSessions: () => state.liveSessions,
    _coralPopout: popoutApi,
    sendCommand, sendCommandWithTeam, sendBoardProtocol, resendInputPrompt, sendRawKeys, sendModeToggle, cycleModeToggle, sendQuickCommand,
    executeMacro, deleteMacro, killSession, getTerminal, switchHistoryTab,
    loadAgentTasks, loadBoardTasks, renderBoardTaskList, loadChangedFiles, loadAgentEvents,
    selectScheduledJob, editScheduledJob, closeAllPreviewTabs, openPreviewTab, hideServersModal,
    cancelSelectMode, deleteMessageBoardProject, showExportBoardModal, showMessageBoardProjects, postBoardMessage, toggleSelectMode,
    hideFolderTagDropdown, buildSessionTooltip, showTeamAvailability, showTeamWorkingMode,
    triggerWorkflow, showWorkflowCreateModal, editWorkflow, saveWorkflow, workflowsBackToList,
    showTemplateBrowser, searchChats, showInfoModal, exportTeamTemplates, showResumeModal,
};

// What the suites read or reset inside the page's modules.
const probes = {
    hasAction, boardSearchState, capturePollStats, dropChatPrefetch, resetMorph, countsAsOverload, lastLiveListHtml, flushLiveSessionsRender,
    serverStatus, serverBadgeHtml, serverStripHtml, terminalInputAcks, PANEL_HOST_CSP, HOST_CSP,
    _resetRateForTests, _resetLivePrune, _resetOverloadForTests, _resetWaitingSeedForTests, _boardBodyCacheStatsForTests,
};

/** Called once by app.js: installs window.__coralTest, and the window functions, when the page asks for them. */
export function installTestHooks() {
    if (!testHooksOn()) return;
    window.__coralTest = { ...windowHooks, ...probes };
    Object.assign(window, windowHooks);
}
