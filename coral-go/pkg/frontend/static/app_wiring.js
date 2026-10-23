/* The page's one-time wiring: the local store, platform, composer, pane scroll, panels and window events (moved out of app.js, #5082; no behaviour change). */
import { state, loadSessionDrafts, sessionKey, saveSessionDraft } from './state.js';
import { accountTag, setStoreOwner, ageOutStored, DAY_MS } from './local_store.js';
import { applyTabPrefs } from './tab_customize.js';
import { loadPendingFromStorage, initCommandInputAutosize, refreshLiveHistory } from './live_chat.js';
import { platform } from './platform/detect.js';
import { initNative } from './platform/native.js';
import { initMacOS } from './platform/macos.js';
import { initWindows } from './platform/windows.js';
import { initBrowser } from './platform/browser.js';
import { isCloud } from './server_base.js';
import { handleUnauthorized, isSignedOut401 } from './auth_redirect.js';
import { sendCommand, addMacro, initImageDrop } from './controls.js';
import { initFileSearch } from './changed_files.js';
import { initFileMention } from './file_mention.js';
import { initCommandMention } from './command_mention.js';
import { initNotesMd } from './agent_notes.js';
import { initChatFind } from './chat_find.js';
import { initSidebarResize, initTaskBarResize, initSidebarCollapse, initAgenticPanelCollapse, initAgenticBlockResize, initAgenticBlockCollapse, initSidebarRail } from './sidebar.js';
import { restoreAgenticTabs } from './agentic_state.js';
import { initPreviewPane } from './preview_pane.js';
import { syncPaneWidth, refreshCapture } from './capture.js';
import { fitTerminal } from './xterm_renderer.js';

// What was typed and where people worked, kept in this browser (local_store.js, #3681): owned by the
// signed-in Coral Hub account (none locally), aged out at start and daily in a tab left open.
export async function initLocalStore() {
    let tag = '';
    try { tag = state.account ? await accountTag(state.account.email) : ''; } catch { tag = ''; }
    setStoreOwner(tag);
    applyTabPrefs(); // the tab choice is kept per account, so read it now that the account is known (#4103)
    loadSessionDrafts(ageOutStored());
    loadPendingFromStorage();
    setInterval(() => loadSessionDrafts(ageOutStored()), DAY_MS); // interval-ok: daily, browser storage only, no request
}

/** Platform detection and what is particular to the platform the page runs in (the Coral app on macOS or Windows, or a browser). */
export function initPlatform() {
    // Initialize platform detection and platform-specific behavior
    platform.init();
    if (platform.isNative) {
        initNative();
        if (platform.isMacOS)   initMacOS();
        if (platform.isWindows) initWindows();
    } else {
        initBrowser();
    }
}

/** Every fetch of the page goes through a wrapper that sends a signed-out 401 of Coral's own /api to the sign-in page. */
export function installUnauthorizedRedirect() {
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
}

/** The command box and the inputs around it: image drop, the file search, the @file and /command mentions, the notes, the macro dialog's Enter and the box's own keys. */
export function wireComposer() {
    // Image drag-and-drop on command pane
    initImageDrop();

    // File search in files panel + top bar
    initFileSearch();

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
}

/** Scroll tracking of the capture pane and the live history, and pausing capture updates while text in the terminal pane is selected. */
export function wirePaneScroll() {
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
}

/** The sidebars and the side panel: collapsible sections, the overflow menu, the resize handles, the preview pane and the saved tabs. */
export function initPanels() {
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
    initPreviewPane();
    restoreAgenticTabs();
}

/** The window's own events: a hidden page pauses its polling, a resize refits the terminal and the pane width. */
export function wireWindowEvents() {
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
}

/** Lets the browser run what is waiting (a paint, an input, a response) before the next step: scheduler.yield where there is one, a zero timer where there is not. */
export const yieldToMain = () => (globalThis.scheduler && typeof scheduler.yield === 'function') ? scheduler.yield() : new Promise(r => setTimeout(r, 0));

// Whether a fetch target is this origin's /api (a 401 from elsewhere is not Coral's session).
function _sameOriginApi(input) {
    try {
        const u = new URL(typeof input === 'string' || input instanceof URL ? String(input) : input.url, location.href);
        return u.origin === location.origin && u.pathname.startsWith('/api/');
    } catch { return false; }
}
