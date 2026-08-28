/* Coral Dashboard — Entry Point */

import { state } from './state.js';
import { loadLiveSessions, loadHistorySessions, loadHistorySessionsPaged } from './api.js';
import { filterState, deserializeFromUrl, serializeToUrl,
         hasActiveFilters, countActiveFilters, resetFilters }
    from './search_filters.js';
import { connectCoralWs } from './websocket.js';
import { sendCommand, sendCommandWithTeam, sendBoardProtocol, resendInputPrompt, sendRawKeys, sendModeToggle, cycleModeToggle, sendQuickCommand, executeMacro, addMacro, deleteMacro, showMacroModal, hideMacroModal, attachTerminal, killSession, restartSession, hideRestartModal, confirmRestart, initImageDrop, removeAttachment, editGoal, refreshGoal, requestGoal } from './controls.js';
import { selectLiveSession, selectHistorySession, editAndResubmit, renameAgent, setAgentIcon, showEmojiPicker } from './sessions.js';
import { toggleGroupCollapse, killGroup, killBoard, toggleTeamSleep, toggleAgentSleep, sleepAllAgents, wakeAllAgents, shareAgentTeam, saveTeamFromSidebar, killSessionDirect, dismissKilledSession, dismissBoardKilled, showInfoDirect, attachDirect, restartDirect, showConfirmModal, hideConfirmModal, showPromptModal, hidePromptModal, showAlertModal, hideAlertModal, copyFolderPath, moveGroupUp, moveGroupDown, toggleGroupByTeam, setBoardAccentColor, moveSessionUp, moveSessionDown, showTeamTokenUsage } from './render.js';
import { syncPaneWidth, refreshCapture } from './capture.js';
import { showLaunchModal, hideLaunchModal, launchSession, showInfoModal, hideInfoModal, copyInfoCommand, showResumeModal, hideResumeModal, resumeLaunchNew, showSettingsModal, hideSettingsModal, applySettings, loadSettings, toggleFlag, showAddAgentToBoard, hideAddAgentBoardModal, launchAgentToBoard, launchTerminalToBoard, launchDefaultAgent, showAddStandaloneAgent, launchStandaloneTerminal, exportPersonas, importPersonas, exportTeamTemplates, importTeamTemplates, showDefaultPromptsModal, hideDefaultPromptsModal, resetDefaultPrompt, saveDefaultPrompts, deactivateLicense, trackSupporterClick } from './modals.js';
import { toggleBrowser, browserNavigateTo, browserNavigateUp } from './browser.js';
import { initSidebarResize, initCommandPaneResize, initTaskBarResize, initBoardChatResize, initSidebarCollapse, switchJobsSubtab, initAgenticPanelCollapse, toggleAgenticPanel, initAgenticBlockResize, initAgenticBlockCollapse } from './sidebar.js';
import { fitTerminal, getTerminal, connectTerminalWs, disconnectTerminalWs } from './xterm_renderer.js';
import { loadSessionNotes, saveNotes, generateSummary, resummarize, toggleNotesEdit, cancelNotesEdit, switchHistoryTab } from './notes.js';
import { loadSessionTags, addTagToSession, removeTagFromSession, showTagDropdown, hideTagDropdown, createTag, loadAllTags } from './tags.js';
import { loadSessionCommits } from './commits.js';
import { showTemplateBrowser } from './template_browser.js';
import { loadAgentTasks, addAgentTask, toggleAgentTask, deleteAgentTask, editAgentTaskTitle, loadBoardTasks, renderBoardTaskList, showTaskDetailModal, hideTaskDetailModal, showCreateTaskModal, hideCreateTaskModal, submitCreateTask, enableTaskEditMode, saveTaskEdit, cancelTaskEdit, completeBoardTask, cancelBoardTask, _doCompleteTask, _doCancelTask, _restoreTaskFooter, publishBoardTask } from './tasks.js';
import { loadChangedFiles, openFileDiff, openFilePreview, openFileEdit, refreshChangedFiles, toggleGitDiffMode, setGitDiffMode, toggleStarFile, copyFilePath, searchRepoFiles, renderStarredFiles, initFileSearch, initTopBarSearch, showTopBarSearch, hideTopBarSearch, toggleFileSearchMode } from './changed_files.js';
import { initFileMention } from './file_mention.js';
import { initCommandMention } from './command_mention.js';
import { loadAgentNotes, initNotesMd } from './agent_notes.js';
import { loadCustomViews, activateCustomView } from './custom_views.js';
import { initRouter, pushView } from './router.js';
import { switchAgenticTab, restoreAgenticTabs, loadAgentEvents, toggleEventFilter, toggleAllEventFilters, toggleFilterDropdown, showFilterPopup, hideFilterPopup } from './agentic_state.js';
import { toggleHistoryEventFilter, toggleAllHistoryEventFilters } from './history_tabs.js';
import { copyBranchName, escapeHtml, showView } from './utils.js';
import { initScheduler, selectScheduledJob, toggleScheduledJob, deleteScheduledJob, editScheduledJob, showJobModal, hideJobModal, validateCronPreview, saveScheduledJob, switchJobType, pickSchedulePreset } from './scheduler.js';
import {
    showWebhookModal, hideWebhookModal, showWebhookCreate,
    showWebhookList, showWebhookEdit, saveWebhook, deleteWebhook,
    testWebhook, showWebhookHistory,
} from './webhooks.js';
import { initLiveJobs, renderLiveJobs, selectLiveJobRun } from './live_jobs.js';
import { showThemeConfigurator, hideThemeConfigurator } from './theme_config.js';
import { initMessageBoard, selectBoardProject, showMessageBoardProjects, postBoardMessage, deleteMessageBoardProject, toggleBoardPause, toggleBoardSleep, deleteBoardMessage, showExportBoardModal, doExportBoard, toggleSelectMode, toggleMessageSelect, selectAllMessages, selectNoneMessages, cancelSelectMode, exportSelectedAsMarkdown } from './message_board.js';
import { loadAllFolderTags, showFolderTagDropdown, hideFolderTagDropdown, addFolderTag, removeFolderTag, createAndAddFolderTag } from './folder_tags.js';
import { initWorkflows, showWorkflowsTab, selectWorkflow, selectWorkflowRun, triggerWorkflow, killWorkflowRun, deleteWorkflow, showWorkflowCreateModal, hideWorkflowCreateModal, editWorkflow, editWorkflowWithAgent, workflowAddStep, workflowStepTypeChanged, saveWorkflow, workflowsBackToList, launchWorkflowAgent } from './workflows.js';
import { showConnectedApps, showConnectAppModal, hideConnectAppModal, startOAuthFlow, testConnectedApp, disconnectApp } from './connected_apps.js';
import { showCostDashboard, stopCostDashboard, _refreshCostDashboard, _costTimeRangeChanged } from './cost_dashboard.js';
import { showDocsTab, selectDoc } from './docs.js';
import { maybeShowTelemetryDisclosure, acknowledgeTelemetryDisclosure, openTelemetryDoc } from './telemetry.js';
import { initMobile, syncMobileAgentList } from './mobile.js';
import { platform } from './platform/detect.js';
import { initNative } from './platform/native.js';
import { initMacOS } from './platform/macos.js';
import { initWindows } from './platform/windows.js';
import { initBrowser } from './platform/browser.js';

import { checkForUpdates, dismissUpdateToast } from './update_check.js';

// ── Expose functions to HTML onclick handlers ─────────────────────────────
// Grouped by source module for maintainability. Add new entries to the
// appropriate section rather than appending to the bottom.
Object.assign(window, {
    // api
    _coralLoadLiveSessions: loadLiveSessions,
    // controls
    sendCommand, sendCommandWithTeam, sendBoardProtocol, resendInputPrompt, sendRawKeys,
    sendModeToggle, cycleModeToggle, sendQuickCommand,
    executeMacro, addMacro, deleteMacro, showMacroModal, hideMacroModal,
    attachTerminal, killSession, restartSession,
    editGoal, refreshGoal, requestGoal,
    hideRestartModal, confirmRestart, removeAttachment,
    // modals
    showSettingsModal, hideSettingsModal, applySettings,
    showDefaultPromptsModal, hideDefaultPromptsModal, resetDefaultPrompt, saveDefaultPrompts,
    _deactivateLicense: deactivateLicense,
    _trackSupporterClick: trackSupporterClick,
    showLaunchModal, hideLaunchModal, launchSession, toggleFlag,
    showInfoModal, hideInfoModal, copyInfoCommand,
    showResumeModal, hideResumeModal, resumeLaunchNew,
    showAddAgentToBoard, hideAddAgentBoardModal, launchAgentToBoard, launchTerminalToBoard,
    launchDefaultAgent, showAddStandaloneAgent, launchStandaloneTerminal,
    exportPersonas, importPersonas, exportTeamTemplates, importTeamTemplates,
    // sessions
    selectLiveSession, selectHistorySession, editAndResubmit, renameAgent, setAgentIcon,
    // xterm
    getTerminal, connectTerminalWs, disconnectTerminalWs,
    // notes
    loadSessionNotes, saveNotes, generateSummary, resummarize, toggleNotesEdit, cancelNotesEdit, switchHistoryTab,
    // tags
    loadSessionTags, addTagToSession, removeTagFromSession, showTagDropdown, hideTagDropdown, createTag,
    // changed_files
    loadChangedFiles, openFileDiff, openFilePreview, openFileEdit, refreshChangedFiles,
    toggleGitDiffMode, setGitDiffMode, toggleStarFile, copyFilePath, searchRepoFiles, renderStarredFiles, toggleFileSearchMode,
    // tasks
    loadAgentTasks, addAgentTask, toggleAgentTask, deleteAgentTask, editAgentTaskTitle, loadBoardTasks, renderBoardTaskList,
    showTaskDetailModal, hideTaskDetailModal,
    showCreateTaskModal, hideCreateTaskModal, submitCreateTask,
    enableTaskEditMode, saveTaskEdit, cancelTaskEdit,
    completeBoardTask, cancelBoardTask, _doCompleteTask, _doCancelTask, _restoreTaskFooter, publishBoardTask,
    // agent_notes
    loadAgentNotes,
    // agentic_state
    switchAgenticTab, loadAgentEvents,
    toggleEventFilter, toggleAllEventFilters, toggleFilterDropdown, showFilterPopup, hideFilterPopup,
    // history_tabs
    toggleHistoryEventFilter, toggleAllHistoryEventFilters,
    // scheduler
    selectScheduledJob, toggleScheduledJob, deleteScheduledJob, editScheduledJob,
    showJobModal, hideJobModal, validateCronPreview, saveScheduledJob, switchJobType, pickSchedulePreset,
    // webhooks
    showWebhookModal, hideWebhookModal, showWebhookCreate, showWebhookList,
    showWebhookEdit, saveWebhook, deleteWebhook, testWebhook, showWebhookHistory,
    // live_jobs
    selectLiveJobRun,
    // sidebar
    switchJobsSubtab, toggleAgenticPanel,
    // browser
    toggleBrowser, browserNavigateTo, browserNavigateUp,
    // theme
    showThemeConfigurator, hideThemeConfigurator,
    // update_check
    checkForUpdates, dismissUpdateToast,
    // message_board
    selectBoardProject, showMessageBoardProjects, postBoardMessage,
    deleteMessageBoardProject, confirmDeleteBoard: deleteMessageBoardProject,
    toggleBoardPause, toggleBoardSleep, deleteBoardMessage,
    showExportBoardModal, doExportBoard,
    toggleSelectMode, toggleMessageSelect, selectAllMessages, selectNoneMessages,
    cancelSelectMode, exportSelectedAsMarkdown,
    // workflows
    showWorkflowsTab,
    selectWorkflow, selectWorkflowRun, triggerWorkflow, killWorkflowRun, deleteWorkflow,
    showWorkflowCreateModal, hideWorkflowCreateModal, editWorkflow,
    workflowAddStep, workflowStepTypeChanged, saveWorkflow, workflowsBackToList, launchWorkflowAgent, editWorkflowWithAgent,
    // connected_apps
    showConnectedApps, showConnectAppModal, hideConnectAppModal, startOAuthFlow,
    testConnectedApp, disconnectApp,
    // cost_dashboard
    showCostDashboard, _refreshCostDashboard, _costTimeRangeChanged,
    // docs
    showDocsTab, selectDoc,
    // telemetry
    acknowledgeTelemetryDisclosure, openTelemetryDoc,
    // folder_tags
    showFolderTagDropdown, hideFolderTagDropdown, addFolderTag, removeFolderTag, createAndAddFolderTag,
    // utils
    copyBranchName,
    // router
    _activateCustomView: activateCustomView, _pushView: pushView,
    // render (pagination)
    loadHistoryPage,
    // top nav
    switchNavTab,
});

// ── Reset Team ───────────────────────────────────────────────────────
function resetTeam(boardName) {
    showConfirmModal('Reset Team', `Reset all agents in "${boardName}"? Their context will be cleared and they'll restart with their original prompts.`, async () => {
        try {
            const resp = await fetch(`/api/sessions/live/team/${encodeURIComponent(boardName)}/reset`, { method: 'POST' });
            if (resp.ok) {
                showToast(`Team "${boardName}" is resetting...`);
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
function switchNavTab(tab) {
    // Clean up any active cost dashboard timer when leaving tokens tab
    stopCostDashboard();

    // Toggle sidebar visibility — full-width views hide the sidebar
    const fullWidthTabs = new Set(['tokens', 'workflows', 'connected-apps']);
    const layout = document.querySelector('.layout');
    if (layout) layout.classList.toggle('sidebar-hidden', fullWidthTabs.has(tab));

    // Update tab button states
    document.querySelectorAll('.top-nav-tab').forEach(btn => btn.classList.remove('active'));
    const activeTab = document.getElementById(`nav-tab-${tab}`);
    if (activeTab) activeTab.classList.add('active');

    // Show/hide sidebar sections based on tab
    const liveSection = document.querySelector('[data-section="live-sessions"]');
    const historySection = document.querySelector('[data-section="history"]');
    const jobsSection = document.querySelector('[data-section="jobs"]');
    const docsSection = document.querySelector('[data-section="docs"]');
    const sidebarFooter = document.querySelector('.sidebar-footer');

    if (liveSection) liveSection.style.display = tab === 'agents' ? '' : 'none';
    if (jobsSection) jobsSection.style.display = tab === 'jobs' ? '' : 'none';
    if (historySection) historySection.style.display = tab === 'history' ? '' : 'none';
    if (docsSection) docsSection.style.display = tab === 'docs' ? '' : 'none';
    if (sidebarFooter) sidebarFooter.style.display = tab === 'agents' ? '' : 'none';

    // Switch main view based on tab
    if (tab === 'board') {
        showView('messageboard-view');
    } else if (tab === 'jobs') {
        showView('scheduler-view');
    } else if (tab === 'workflows') {
        showWorkflowsTab();
    } else if (tab === 'docs') {
        showDocsTab();
    } else if (tab === 'tokens') {
        showCostDashboard();
    } else if (tab === 'history') {
        // History is populated by the background indexer, so the cached list may
        // have changed while another tab was active. Refresh it whenever the
        // user returns while preserving the current page and filters.
        loadHistoryFiltered();
        if (state.currentSession && state.currentSession.type === 'history') {
            showView('history-session-view');
        } else {
            showView('welcome-screen');
        }
    } else if (tab === 'agents') {
        // Restore previously selected session or show welcome
        if (state.currentSession && state.currentSession.type === 'live') {
            showView('live-session-view');
        } else {
            showView('welcome-screen');
        }
    }
}

// ── Import team from folder ───────────────────────────────────────────
window._importTeamFromFolder = function() {
    const input = document.getElementById('team-folder-input');
    if (input) { input.value = ''; input.click(); }
};

window._handleTeamFolderImport = async function(input) {
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
        const { showToast } = await import('./utils.js');
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

    const { showToast } = await import('./utils.js');
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
window.openTeamIconPicker = function(btn) {
    showEmojiPicker((emoji) => {
        btn.textContent = emoji || '🤖';
        btn.dataset.icon = emoji;
        const hiddenInput = btn.parentElement.querySelector('.team-agent-icon');
        if (hiddenInput) hiddenInput.value = emoji;
    });
};
function applyAgentTemplate(template, target, btn) {
    const name = (template.name || '').replace(/-/g, ' ');
    const prompt = (template.body || '') + '\n\nCoordinate with the team via the message board.';
    if (target === 'team-new') {
        if (window._addTeamAgent) window._addTeamAgent(name, prompt);
    } else if (target === 'modal') {
        if (window.setAgentConfig) {
            window.setAgentConfig('add-agent-board-acf', { name, prompt });
        }
    } else if (target === 'row' && btn) {
        const row = btn.closest('.team-agent-row');
        if (!row) return;
        const acfId = row.dataset.acfId;
        if (acfId && window.setAgentConfig) {
            window.setAgentConfig(acfId, { name, prompt });
            // Update summary text
            const nameEl = row.querySelector('.team-agent-role-name');
            if (nameEl) nameEl.textContent = name || 'New Agent';
            const previewEl = row.querySelector('.team-agent-prompt-preview');
            if (previewEl) previewEl.textContent = prompt.substring(0, 200) + (prompt.length > 200 ? '\u2026' : '');
        }
    }
}
window.browseAgentTemplatesNew = function() {
    showTemplateBrowser('agents', (t) => applyAgentTemplate(t, 'team-new'));
};
window.browseAgentTemplatesForModal = function() {
    showTemplateBrowser('agents', (t) => applyAgentTemplate(t, 'modal'));
};
window.browseAgentTemplates = function(btn) {
    showTemplateBrowser('agents', (t) => applyAgentTemplate(t, 'row', btn));
};
window.showMacroAddMenu = function(btn) {
    // Remove existing menu
    const existing = document.getElementById('macro-add-menu');
    if (existing) { existing.remove(); return; }

    const rect = btn.getBoundingClientRect();
    const menu = document.createElement('div');
    menu.id = 'macro-add-menu';
    menu.className = 'sidebar-kebab-menu';
    menu.style.cssText = `display:block;position:fixed;bottom:${window.innerHeight - rect.top + 4}px;left:${rect.left}px;min-width:180px;z-index:10000`;
    menu.innerHTML = `
        <button class="overflow-menu-item" onclick="this.closest('#macro-add-menu').remove(); showMacroModal()">
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="8" y1="3" x2="8" y2="13"/><line x1="3" y1="8" x2="13" y2="8"/></svg>
            Create Custom
        </button>
        <button class="overflow-menu-item" onclick="this.closest('#macro-add-menu').remove(); browseCommandTemplates()">
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="10" height="10" rx="1.5"/><line x1="6" y1="6" x2="10" y2="6"/><line x1="6" y1="8.5" x2="10" y2="8.5"/><line x1="6" y1="11" x2="8" y2="11"/></svg>
            Browse Commands <span style="font-size:9px;color:var(--text-muted)">(aitmpl.com)</span>
        </button>
    `;
    document.body.appendChild(menu);

    // Close on outside click
    setTimeout(() => {
        const close = (e) => { if (!menu.contains(e.target) && e.target !== btn) { menu.remove(); document.removeEventListener('click', close, true); } };
        document.addEventListener('click', close, true);
    }, 50);
};
window.browseCommandTemplates = async function() {
    const { getMacros, saveMacros } = await import('./controls.js');
    showTemplateBrowser('commands', async (template) => {
        const label = template.description || template.name || 'Template';
        const truncLabel = label.length > 20 ? label.substring(0, 20) + '...' : label;
        const command = template.body || '';
        const macros = getMacros();
        macros.push({ label: truncLabel, command });
        await saveMacros(macros);
        const { renderQuickActions } = await import('./controls.js');
        renderQuickActions();
    });
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
    toggleGroupCollapse, killGroup, moveGroupUp, moveGroupDown,
    copyFolderPath, killBoard, dismissBoardKilled, setBoardAccentColor, resetTeam,
    moveSessionUp, moveSessionDown,
    toggleTeamSleep, toggleAgentSleep, sleepAllAgents, wakeAllAgents,
    shareAgentTeam, saveTeamFromSidebar, showTeamTokenUsage,
    showConfirmModal, hideConfirmModal, showPromptModal, hidePromptModal, showAlertModal, hideAlertModal,
    killSessionDirect, dismissKilledSession, showInfoDirect, attachDirect, restartDirect,
    toggleGroupByTeam,
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
        const groupByTeam = localStorage.getItem('coral-group-by-team') === 'true';
        check.style.opacity = groupByTeam ? '1' : '0.2';
    }
}
function closeTopBarSettings() {
    const menu = document.getElementById('top-bar-settings-menu');
    if (menu) menu.style.display = 'none';
}
window.toggleTopBarSettings = toggleTopBarSettings;
window.closeTopBarSettings = closeTopBarSettings;


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
    loadHistorySessionsPaged(historyPage, 50);
    updateFilterBadge();
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
        return `<span class="hf-tag-pill" data-tag-id="${id}">
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

function pollStartupStatus() {
    const el = document.getElementById('startup-loading');
    if (!el) return;
    const check = async () => {
        try {
            const resp = await fetch('/api/system/status');
            const data = await resp.json();
            if (data.startup_complete) {
                el.classList.add('hidden');
                loadLiveSessions();
                return;
            }
        } catch {}
        setTimeout(check, 2000);
    };
    check();
}

// ── Home Navigation ──────────────────────────────────────────────────────
window._goHome = function() {
    window.location.hash = '';
    showView("welcome-screen");
    // Deselect sidebar items
    document.querySelectorAll('.sidebar .session-item.active, .sidebar .session-item.selected').forEach(el => {
        el.classList.remove('active', 'selected');
    });
    // On mobile, show agent list
    const agentList = document.querySelector('.mobile-agent-list');
    if (agentList) agentList.style.display = '';
};

// ── Initialization ────────────────────────────────────────────────────────
document.addEventListener("DOMContentLoaded", () => {
    // Initialize platform detection and platform-specific behavior
    platform.init();
    if (platform.isNative) {
        initNative();
        if (platform.isMacOS)   initMacOS();
        if (platform.isWindows) initWindows();
    } else {
        initBrowser();
    }

    // Intercept 401 responses — redirect to auth page
    const _origFetch = window.fetch;
    window.fetch = async function(...args) {
        const resp = await _origFetch.apply(this, args);
        if (resp.status === 401 && !window.location.pathname.startsWith('/auth')) {
            window.location.href = '/auth';
        }
        return resp;
    };

    loadSettings();

    // Restore filter state from URL query params before first load
    const restored = deserializeFromUrl();
    historyPage = restored.page;

    loadLiveSessions();
    loadAllFolderTags();
    loadCustomViews();
    initRouter();
    connectCoralWs();
    checkForUpdates();
    pollStartupStatus();
    populateHfTagSelect().then(() => {
        syncFilterDomToState();
        loadHistoryFiltered();
    });
    initScheduler();
    initLiveJobs();
    initMessageBoard();
    initMobile();

    // First-run telemetry disclosure. Shown once, only on builds that can
    // actually send anything, and never blocking: the dashboard is already
    // usable behind it.
    maybeShowTelemetryDisclosure();

    // Hide connected apps in prod builds (feature is dev/beta only),
    // and surface a banner if tmux is missing so the user knows agents
    // can't be launched yet.
    fetch('/api/system/status').then(r => r.json()).then(data => {
        if (data.tier_name === 'prod' || data.tier_name === 'staging') {
            const btn = document.getElementById('connected-apps-menu-btn');
            if (btn) btn.style.display = 'none';
        }
        if (data.tmux_available === false) {
            const banner = document.getElementById('tmux-missing-banner');
            const cmd = document.getElementById('tmux-missing-banner-cmd-text');
            if (cmd && data.tmux_install_command) cmd.textContent = data.tmux_install_command;
            if (banner) banner.style.display = '';
        }
    }).catch(() => {});

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
                .forEach(b => b.classList.toggle('active', b.dataset.mode === 'and'));
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

    // Enter sends command, Shift+Enter inserts newline
    document.getElementById("command-input").addEventListener("keydown", (e) => {
        if (e.key === "Enter" && !e.shiftKey) {
            e.preventDefault();
            sendCommand();
        }
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
    initCommandPaneResize();
    initTaskBarResize();
    initBoardChatResize();
    restoreAgenticTabs();

    // Initialize nav tabs — hide history section on load (Agents tab is default)
    switchNavTab('agents');

    // Pause polling when tab is hidden; refresh immediately when visible again.
    // Skip in native apps — WKWebView may report document.hidden=true permanently.
    if (!platform.isNative) {
        document.addEventListener("visibilitychange", () => {
            if (!document.hidden && state.currentSession && state.currentSession.type === "live") {
                refreshCapture();
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

    // Restore session from URL hash
    const hash = window.location.hash;
    if (hash.startsWith('#session/')) {
        const sessionId = hash.substring('#session/'.length);
        if (sessionId) {
            // Delay slightly to allow history list to populate first
            setTimeout(() => selectHistorySession(sessionId), 500);
        }
    }
});
