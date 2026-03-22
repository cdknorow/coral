/* Coral Dashboard — Entry Point */

import { state } from './state.js';
import { loadLiveSessions, loadHistorySessions, loadHistorySessionsPaged } from './api.js';
import { filterState, deserializeFromUrl, serializeToUrl,
         hasActiveFilters, countActiveFilters, resetFilters }
    from './search_filters.js';
import { connectCoralWs } from './websocket.js';
import { sendCommand, sendCommandWithTeam, resendInputPrompt, sendRawKeys, sendModeToggle, cycleModeToggle, sendQuickCommand, executeMacro, addMacro, deleteMacro, showMacroModal, hideMacroModal, attachTerminal, killSession, restartSession, hideRestartModal, confirmRestart, initImageDrop, removeAttachment, editGoal, refreshGoal, requestGoal } from './controls.js';
import { selectLiveSession, selectHistorySession, editAndResubmit, renameAgent, setAgentIcon, showEmojiPicker } from './sessions.js';
import { toggleGroupCollapse, killGroup, killBoard, toggleTeamSleep, toggleAgentSleep, sleepAllAgents, wakeAllAgents, shareAgentTeam, saveTeamFromSidebar, killSessionDirect, showInfoDirect, attachDirect, restartDirect, showConfirmModal, hideConfirmModal, copyFolderPath, moveGroupUp, moveGroupDown, toggleGroupByTeam } from './render.js';
import { syncPaneWidth, refreshCapture } from './capture.js';
import { showLaunchModal, hideLaunchModal, launchSession, showInfoModal, hideInfoModal, copyInfoCommand, showResumeModal, hideResumeModal, resumeIntoSession, showSettingsModal, hideSettingsModal, applySettings, loadSettings, toggleFlag, showAddAgentToBoard, hideAddAgentBoardModal, launchAgentToBoard, exportPersonas, importPersonas, exportTeamTemplates, importTeamTemplates, showDefaultPromptsModal, hideDefaultPromptsModal, resetDefaultPrompt, saveDefaultPrompts, showSubgentSettingsModal, hideSubgentSettingsModal } from './modals.js';
import { toggleBrowser, browserNavigateTo, browserNavigateUp } from './browser.js';
import { initSidebarResize, initCommandPaneResize, initTaskBarResize, initSidebarCollapse, switchJobsSubtab, initAgenticPanelCollapse, toggleAgenticPanel, initAgenticBlockResize, initAgenticBlockCollapse } from './sidebar.js';
import { fitTerminal } from './xterm_renderer.js';
import { loadSessionNotes, saveNotes, resummarize, toggleNotesEdit, cancelNotesEdit, switchHistoryTab } from './notes.js';
import { loadSessionTags, addTagToSession, removeTagFromSession, showTagDropdown, hideTagDropdown, createTag, loadAllTags } from './tags.js';
import { loadSessionCommits } from './commits.js';
import { showTemplateBrowser } from './template_browser.js';
import { loadAgentTasks, addAgentTask, toggleAgentTask, deleteAgentTask, editAgentTaskTitle } from './tasks.js';
import { loadChangedFiles, openFileDiff, openFilePreview, refreshChangedFiles } from './changed_files.js';
import { initFileMention } from './file_mention.js';
import { initCommandMention } from './command_mention.js';
import { loadAgentNotes, initNotesMd } from './agent_notes.js';
import { switchAgenticTab, loadAgentEvents, toggleEventFilter, toggleAllEventFilters, toggleFilterDropdown, showFilterPopup, hideFilterPopup } from './agentic_state.js';
import { toggleHistoryEventFilter, toggleAllHistoryEventFilters } from './history_tabs.js';
import { copyBranchName, escapeHtml } from './utils.js';
import { initScheduler, selectScheduledJob, toggleScheduledJob, deleteScheduledJob, editScheduledJob, showJobModal, hideJobModal, validateCronPreview, saveScheduledJob } from './scheduler.js';
import {
    showWebhookModal, hideWebhookModal, showWebhookCreate,
    showWebhookList, showWebhookEdit, saveWebhook, deleteWebhook,
    testWebhook, showWebhookHistory,
} from './webhooks.js';
import { initLiveJobs, renderLiveJobs, selectLiveJobRun } from './live_jobs.js';
import { showThemeConfigurator, hideThemeConfigurator } from './theme_config.js';
import { initMessageBoard, selectBoardProject, showMessageBoardProjects, postBoardMessage, deleteMessageBoardProject, toggleBoardPause, toggleBoardSleep, deleteBoardMessage } from './message_board.js';
import { loadAllFolderTags, showFolderTagDropdown, hideFolderTagDropdown, addFolderTag, removeFolderTag, createAndAddFolderTag } from './folder_tags.js';
import { initMobile, syncMobileAgentList } from './mobile.js';

import { checkForUpdates, dismissUpdateToast } from './update_check.js';

// ── Expose functions to HTML onclick handlers ─────────────────────────────
window._coralLoadLiveSessions = loadLiveSessions;
window.sendCommand = sendCommand;
window.sendCommandWithTeam = sendCommandWithTeam;
window.resendInputPrompt = resendInputPrompt;
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
window.sendRawKeys = sendRawKeys;
window.sendModeToggle = sendModeToggle;
window.cycleModeToggle = cycleModeToggle;
window.sendQuickCommand = sendQuickCommand;
window.executeMacro = executeMacro;
window.addMacro = addMacro;
window.deleteMacro = deleteMacro;
window.showMacroModal = showMacroModal;
window.hideMacroModal = hideMacroModal;
window.attachTerminal = attachTerminal;
window.killSession = killSession;
window.restartSession = restartSession;
window.editGoal = editGoal;
window.refreshGoal = refreshGoal;
window.requestGoal = requestGoal;
window.hideRestartModal = hideRestartModal;
window.confirmRestart = confirmRestart;
window.removeAttachment = removeAttachment;
window.showSettingsModal = showSettingsModal;
window.hideSettingsModal = hideSettingsModal;
window.showSubgentSettingsModal = showSubgentSettingsModal;
window.hideSubgentSettingsModal = hideSubgentSettingsModal;
window.applySettings = applySettings;
window.showDefaultPromptsModal = showDefaultPromptsModal;
window.hideDefaultPromptsModal = hideDefaultPromptsModal;
window.resetDefaultPrompt = resetDefaultPrompt;
window.saveDefaultPrompts = saveDefaultPrompts;
window.selectLiveSession = selectLiveSession;
window.selectHistorySession = selectHistorySession;
window.editAndResubmit = editAndResubmit;
window.renameAgent = renameAgent;
window.setAgentIcon = setAgentIcon;
window.openTeamIconPicker = function(btn) {
    showEmojiPicker((emoji) => {
        btn.textContent = emoji || '🤖';
        btn.dataset.icon = emoji;
        const hiddenInput = btn.parentElement.querySelector('.team-agent-icon');
        if (hiddenInput) hiddenInput.value = emoji;
    });
};
window.browseAgentTemplatesNew = function() {
    showTemplateBrowser('agents', (template) => {
        const name = (template.name || '').replace(/-/g, ' ');
        const prompt = (template.body || '') + '\n\nCoordinate with the team via the message board.';
        if (window._addTeamAgent) {
            window._addTeamAgent(name, prompt);
        }
    });
};
window.browseAgentTemplatesForModal = function() {
    showTemplateBrowser('agents', (template) => {
        const name = (template.name || '').replace(/-/g, ' ');
        const prompt = (template.body || '') + '\n\nCoordinate with the team via the message board.';
        const nameEl = document.getElementById('add-agent-board-agent-name');
        const promptEl = document.getElementById('add-agent-board-prompt');
        if (nameEl) nameEl.value = name;
        if (promptEl) promptEl.value = prompt;
    });
};
window.browseAgentTemplates = function(btn) {
    showTemplateBrowser('agents', (template) => {
        const row = btn.closest('.team-agent-row');
        if (!row) return;
        const promptEl = row.querySelector('.team-agent-prompt');
        if (promptEl) {
            promptEl.value = (template.body || '') + '\n\nCoordinate with the team via the message board.';
            promptEl.dispatchEvent(new Event('input'));
        }
        const nameEl = row.querySelector('.team-agent-name');
        if (nameEl && template.name && !nameEl.value.trim()) {
            nameEl.value = (template.name || '').replace(/-/g, ' ');
            nameEl.dispatchEvent(new Event('input'));
        }
    });
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
window.showLaunchModal = showLaunchModal;
window.hideLaunchModal = hideLaunchModal;
window.launchSession = launchSession;
window.toggleFlag = toggleFlag;
window.showInfoModal = showInfoModal;
window.hideInfoModal = hideInfoModal;
window.copyInfoCommand = copyInfoCommand;
window.copyBranchName = copyBranchName;
window.showResumeModal = showResumeModal;
window.hideResumeModal = hideResumeModal;
window.resumeIntoSession = resumeIntoSession;
window.toggleBrowser = toggleBrowser;
window.browserNavigateTo = browserNavigateTo;
window.browserNavigateUp = browserNavigateUp;
window.loadSessionNotes = loadSessionNotes;
window.saveNotes = saveNotes;
window.resummarize = resummarize;
window.toggleNotesEdit = toggleNotesEdit;
window.cancelNotesEdit = cancelNotesEdit;
window.switchHistoryTab = switchHistoryTab;
window.loadSessionTags = loadSessionTags;
window.addTagToSession = addTagToSession;
window.removeTagFromSession = removeTagFromSession;
window.showTagDropdown = showTagDropdown;
window.hideTagDropdown = hideTagDropdown;
window.createTag = createTag;
window.loadHistoryPage = loadHistoryPage;
window.loadChangedFiles = loadChangedFiles;
window.openFileDiff = openFileDiff;
window.openFilePreview = openFilePreview;
window.refreshChangedFiles = refreshChangedFiles;
window.loadAgentTasks = loadAgentTasks;
window.addAgentTask = addAgentTask;
window.toggleAgentTask = toggleAgentTask;
window.deleteAgentTask = deleteAgentTask;
window.editAgentTaskTitle = editAgentTaskTitle;
window.loadAgentNotes = loadAgentNotes;
window.switchAgenticTab = switchAgenticTab;
window.loadAgentEvents = loadAgentEvents;
window.toggleEventFilter = toggleEventFilter;
window.toggleAllEventFilters = toggleAllEventFilters;
window.toggleFilterDropdown = toggleFilterDropdown;
window.showFilterPopup = showFilterPopup;
window.hideFilterPopup = hideFilterPopup;
window.toggleHistoryEventFilter = toggleHistoryEventFilter;
window.toggleAllHistoryEventFilters = toggleAllHistoryEventFilters;
window.selectScheduledJob = selectScheduledJob;
window.toggleScheduledJob = toggleScheduledJob;
window.deleteScheduledJob = deleteScheduledJob;
window.editScheduledJob = editScheduledJob;
window.showJobModal = showJobModal;
window.hideJobModal = hideJobModal;
window.validateCronPreview = validateCronPreview;
window.saveScheduledJob = saveScheduledJob;
window.showWebhookModal  = showWebhookModal;
window.hideWebhookModal  = hideWebhookModal;
window.showWebhookCreate = showWebhookCreate;
window.showWebhookList   = showWebhookList;
window.showWebhookEdit   = showWebhookEdit;
window.saveWebhook       = saveWebhook;
window.deleteWebhook     = deleteWebhook;
window.testWebhook       = testWebhook;
window.showWebhookHistory = showWebhookHistory;
window.selectLiveJobRun = selectLiveJobRun;
window.switchJobsSubtab = switchJobsSubtab;
window.toggleAgenticPanel = toggleAgenticPanel;
window.showThemeConfigurator = showThemeConfigurator;
window.hideThemeConfigurator = hideThemeConfigurator;
window.dismissUpdateToast = dismissUpdateToast;
window.selectBoardProject = selectBoardProject;
window.showMessageBoardProjects = showMessageBoardProjects;
window.postBoardMessage = postBoardMessage;
window.deleteMessageBoardProject = deleteMessageBoardProject;
window.toggleBoardPause = toggleBoardPause;
window.toggleBoardSleep = toggleBoardSleep;
window.deleteBoardMessage = deleteBoardMessage;
window.showFolderTagDropdown = showFolderTagDropdown;
window.hideFolderTagDropdown = hideFolderTagDropdown;
window.addFolderTag = addFolderTag;
window.removeFolderTag = removeFolderTag;
window.createAndAddFolderTag = createAndAddFolderTag;

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

window.toggleSidebarKebab = toggleSidebarKebab;
window.closeSidebarKebabs = closeSidebarKebabs;
window.toggleGroupCollapse = toggleGroupCollapse;
window.killGroup = killGroup;
window.moveGroupUp = moveGroupUp;
window.moveGroupDown = moveGroupDown;
window.copyFolderPath = copyFolderPath;
window.killBoard = killBoard;
window.toggleTeamSleep = toggleTeamSleep;
window.toggleAgentSleep = toggleAgentSleep;
window.sleepAllAgents = sleepAllAgents;
window.wakeAllAgents = wakeAllAgents;
window.shareAgentTeam = shareAgentTeam;
window.saveTeamFromSidebar = saveTeamFromSidebar;
window.toggleGroupByTeam = toggleGroupByTeam;
window.showConfirmModal = showConfirmModal;
window.hideConfirmModal = hideConfirmModal;
window.showAddAgentToBoard = showAddAgentToBoard;
window.hideAddAgentBoardModal = hideAddAgentBoardModal;
window.launchAgentToBoard = launchAgentToBoard;
window.exportPersonas = exportPersonas;
window.importPersonas = importPersonas;
window.exportTeamTemplates = exportTeamTemplates;
window.importTeamTemplates = importTeamTemplates;
window.showTemplateBrowser = showTemplateBrowser;
window.killSessionDirect = killSessionDirect;
window.showInfoDirect = showInfoDirect;
window.attachDirect = attachDirect;
window.restartDirect = restartDirect;

// Close kebab menus when clicking outside
document.addEventListener('click', (e) => {
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

// ── Initialization ────────────────────────────────────────────────────────
document.addEventListener("DOMContentLoaded", () => {
    loadSettings();

    // Restore filter state from URL query params before first load
    const restored = deserializeFromUrl();
    historyPage = restored.page;

    loadLiveSessions();
    loadAllFolderTags();
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

    // Pause polling when tab is hidden; refresh immediately when visible again
    document.addEventListener("visibilitychange", () => {
        if (!document.hidden && state.currentSession && state.currentSession.type === "live") {
            refreshCapture();
        }
    });

    // Sync tmux pane width on window resize and panel drag
    let resizeDebounce;
    window.addEventListener("resize", () => {
        clearTimeout(resizeDebounce);
        resizeDebounce = setTimeout(syncPaneWidth, 300);
        fitTerminal();
    });
    // Re-sync after sidebar/task-bar drag ends (the panels change available width)
    document.addEventListener("mouseup", () => {
        setTimeout(syncPaneWidth, 50);
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
