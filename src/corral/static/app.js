/* Corral Dashboard — Entry Point */

import { state } from './state.js';
import { loadLiveSessions, loadHistorySessions, loadHistorySessionsPaged } from './api.js';
import { connectCorralWs } from './websocket.js';
import { sendCommand, sendRawKeys, sendModeToggle, sendQuickCommand, executeMacro, addMacro, deleteMacro, showMacroModal, hideMacroModal, attachTerminal, killSession, restartSession, hideRestartModal, confirmRestart } from './controls.js';
import { selectLiveSession, selectHistorySession, editAndResubmit, renameAgent } from './sessions.js';
import { showLaunchModal, hideLaunchModal, launchSession, showInfoModal, hideInfoModal, copyInfoCommand, showResumeModal, hideResumeModal, resumeIntoSession, showSettingsModal, hideSettingsModal, applySettings, loadSettings, toggleFlag } from './modals.js';
import { toggleBrowser, browserNavigateTo, browserNavigateUp } from './browser.js';
import { initSidebarResize, initCommandPaneResize, initTaskBarResize } from './sidebar.js';
import { loadSessionNotes, saveNotes, resummarize, toggleNotesEdit, cancelNotesEdit, switchHistoryTab } from './notes.js';
import { loadSessionTags, addTagToSession, removeTagFromSession, showTagDropdown, hideTagDropdown, createTag, loadAllTags } from './tags.js';
import { loadSessionCommits } from './commits.js';
import { loadAgentTasks, addAgentTask, toggleAgentTask, deleteAgentTask, editAgentTaskTitle } from './tasks.js';
import { loadAgentNotes, initNotesMd } from './agent_notes.js';
import { switchAgenticTab, loadAgentEvents, toggleEventFilter, toggleAllEventFilters, showFilterPopup, hideFilterPopup } from './agentic_state.js';
import { toggleHistoryEventFilter, toggleAllHistoryEventFilters } from './history_tabs.js';
import { copyBranchName } from './utils.js';

// ── Expose functions to HTML onclick handlers ─────────────────────────────
window.sendCommand = sendCommand;
window.sendRawKeys = sendRawKeys;
window.sendModeToggle = sendModeToggle;
window.sendQuickCommand = sendQuickCommand;
window.executeMacro = executeMacro;
window.addMacro = addMacro;
window.deleteMacro = deleteMacro;
window.showMacroModal = showMacroModal;
window.hideMacroModal = hideMacroModal;
window.attachTerminal = attachTerminal;
window.killSession = killSession;
window.restartSession = restartSession;
window.hideRestartModal = hideRestartModal;
window.confirmRestart = confirmRestart;
window.showSettingsModal = showSettingsModal;
window.hideSettingsModal = hideSettingsModal;
window.applySettings = applySettings;
window.selectLiveSession = selectLiveSession;
window.selectHistorySession = selectHistorySession;
window.editAndResubmit = editAndResubmit;
window.renameAgent = renameAgent;
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
window.showFilterPopup = showFilterPopup;
window.hideFilterPopup = hideFilterPopup;
window.toggleHistoryEventFilter = toggleHistoryEventFilter;
window.toggleAllHistoryEventFilters = toggleAllHistoryEventFilters;

// ── History search/filter/pagination state ───────────────────────────────
let historyPage = 1;
let historySearch = '';
let historyTagId = null;
let historySourceType = null;

function loadHistoryPage(page) {
    historyPage = page;
    loadHistoryFiltered();
}

function loadHistoryFiltered() {
    loadHistorySessionsPaged(historyPage, 50, historySearch || null, historyTagId, historySourceType || null);
}

async function populateTagFilter() {
    await loadAllTags();
    try {
        const resp = await fetch('/api/tags');
        const tags = await resp.json();
        const select = document.getElementById('tag-filter');
        if (!select) return;
        select.innerHTML = '<option value="">All tags</option>';
        for (const tag of tags) {
            const opt = document.createElement('option');
            opt.value = tag.id;
            opt.textContent = tag.name;
            select.appendChild(opt);
        }
    } catch (e) {
        console.error('Failed to load tags for filter:', e);
    }
}

// ── Initialization ────────────────────────────────────────────────────────
document.addEventListener("DOMContentLoaded", () => {
    loadSettings();
    loadLiveSessions();
    loadHistorySessions();
    connectCorralWs();
    populateTagFilter();

    // Search bar with debounce
    const searchInput = document.getElementById('history-search');
    if (searchInput) {
        let debounceTimer;
        searchInput.addEventListener('input', () => {
            clearTimeout(debounceTimer);
            debounceTimer = setTimeout(() => {
                historySearch = searchInput.value.trim();
                historyPage = 1;
                loadHistoryFiltered();
            }, 300);
        });
    }

    // Tag filter
    const tagFilter = document.getElementById('tag-filter');
    if (tagFilter) {
        tagFilter.addEventListener('change', (e) => {
            historyTagId = e.target.value ? parseInt(e.target.value) : null;
            historyPage = 1;
            loadHistoryFiltered();
        });
    }

    // Source type filter
    const sourceFilter = document.getElementById('source-filter');
    if (sourceFilter) {
        sourceFilter.addEventListener('change', (e) => {
            historySourceType = e.target.value || null;
            historyPage = 1;
            loadHistoryFiltered();
        });
    }

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

    // Global keyboard shortcuts: arrow keys, Esc, Enter → send to live session
    document.addEventListener("keydown", (e) => {
        // Skip if typing in an input, textarea, or contenteditable
        const tag = e.target.tagName;
        if (tag === "INPUT" || tag === "TEXTAREA" || e.target.isContentEditable) return;
        // Skip if a modal is open
        if (document.querySelector(".modal[style*='display: flex']")) return;
        // Only act when a live session is selected
        if (!state.currentSession || state.currentSession.type !== "live") return;

        const keyMap = {
            "Escape": ["Escape"],
            "ArrowUp": ["Up"],
            "ArrowDown": ["Down"],
            "Enter": ["Enter"],
        };
        const keys = keyMap[e.key];
        if (keys) {
            e.preventDefault();
            sendRawKeys(keys);
        }
    });

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

    // Resize handles
    initSidebarResize();
    initCommandPaneResize();
    initTaskBarResize();

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
