/* Sidebar drag-to-resize functionality */

import { fitTerminal } from './xterm_renderer.js';

export function initSidebarResize() {
    const handle = document.getElementById("sidebar-resize-handle");
    const sidebar = document.querySelector(".sidebar");

    // Restore saved width from localStorage
    const saved = localStorage.getItem('coral-sidebar-width');
    if (saved) {
        const w = parseInt(saved, 10);
        if (w >= 200 && w <= window.innerWidth * 0.5) sidebar.style.width = w + "px";
    }

    let dragging = false;

    handle.addEventListener("mousedown", (e) => {
        e.preventDefault();
        dragging = true;
        handle.classList.add("dragging");
        document.body.style.cursor = "col-resize";
        document.body.style.userSelect = "none";
    });

    document.addEventListener("mousemove", (e) => {
        if (!dragging) return;
        const newWidth = Math.min(Math.max(e.clientX, 200), window.innerWidth * 0.5);
        sidebar.style.width = newWidth + "px";
        fitTerminal();
    });

    document.addEventListener("mouseup", () => {
        if (!dragging) return;
        dragging = false;
        handle.classList.remove("dragging");
        document.body.style.cursor = "";
        document.body.style.userSelect = "";
        // Persist width
        localStorage.setItem('coral-sidebar-width', sidebar.offsetWidth);
        fitTerminal();
    });
}

/* Task bar drag-to-resize functionality */

export function initTaskBarResize() {
    const handle = document.getElementById("task-bar-resize-handle");
    const taskBar = document.getElementById("agentic-state");
    const liveBody = document.querySelector(".live-body");

    if (!handle || !taskBar || !liveBody) return;

    // Restore saved width from localStorage
    const saved = localStorage.getItem('coral-taskbar-width');
    if (saved) {
        const w = parseInt(saved, 10);
        if (w >= 280 && w <= window.innerWidth * 0.5) taskBar.style.width = w + "px";
    }

    let dragging = false;

    handle.addEventListener("mousedown", (e) => {
        e.preventDefault();
        dragging = true;
        handle.classList.add("dragging");
        document.body.style.cursor = "col-resize";
        document.body.style.userSelect = "none";
    });

    document.addEventListener("mousemove", (e) => {
        if (!dragging) return;
        const rect = liveBody.getBoundingClientRect();
        const newWidth = rect.right - e.clientX;
        const clamped = Math.min(Math.max(newWidth, 280), window.innerWidth * 0.5);
        taskBar.style.width = clamped + "px";
        fitTerminal();
    });

    document.addEventListener("mouseup", () => {
        if (!dragging) return;
        dragging = false;
        handle.classList.remove("dragging");
        document.body.style.cursor = "";
        document.body.style.userSelect = "";
        // Persist width
        localStorage.setItem('coral-taskbar-width', taskBar.offsetWidth);
        fitTerminal();
    });
}

/* Command pane drag-to-resize functionality */

export function initCommandPaneResize() {
    const handle = document.getElementById("command-pane-resize-handle");
    const pane = document.getElementById("command-pane");
    const column = document.querySelector(".live-left-column");

    // Restore saved height from localStorage
    const saved = localStorage.getItem('coral-cmdpane-height');
    if (saved) {
        const h = parseInt(saved, 10);
        if (h >= 80 && h <= 600) pane.style.height = h + "px";
    }

    let dragging = false;

    handle.addEventListener("mousedown", (e) => {
        e.preventDefault();
        dragging = true;
        handle.classList.add("dragging");
        document.body.style.cursor = "row-resize";
        document.body.style.userSelect = "none";
    });

    document.addEventListener("mousemove", (e) => {
        if (!dragging) return;
        const container = column || document.body;
        const rect = container.getBoundingClientRect();
        const newHeight = rect.bottom - e.clientY;
        const clamped = Math.min(Math.max(newHeight, 80), rect.height * 0.6);
        pane.style.height = clamped + "px";
        fitTerminal();
    });

    document.addEventListener("mouseup", () => {
        if (!dragging) return;
        dragging = false;
        handle.classList.remove("dragging");
        document.body.style.cursor = "";
        document.body.style.userSelect = "";
        // Persist height
        localStorage.setItem('coral-cmdpane-height', pane.offsetHeight);
        fitTerminal();
    });
}

/* Board chat input pane resize */

export function initBoardChatResize() {
    // Use event delegation since the board chat is dynamically created
    document.addEventListener("mousedown", (e) => {
        if (!e.target.classList.contains("board-chat-resize-handle")) return;
        e.preventDefault();
        const pane = e.target.closest(".board-chat-input-pane");
        if (!pane) return;
        const handle = e.target;
        handle.style.background = "var(--accent)";
        document.body.style.cursor = "row-resize";
        document.body.style.userSelect = "none";

        const onMove = (ev) => {
            const rect = pane.parentElement.getBoundingClientRect();
            const newHeight = rect.bottom - ev.clientY;
            const clamped = Math.min(Math.max(newHeight, 80), rect.height * 0.6);
            pane.style.height = clamped + "px";
        };
        const onUp = () => {
            handle.style.background = "";
            document.body.style.cursor = "";
            document.body.style.userSelect = "";
            localStorage.setItem('coral-boardchat-height', pane.offsetHeight);
            document.removeEventListener("mousemove", onMove);
            document.removeEventListener("mouseup", onUp);
        };
        document.addEventListener("mousemove", onMove);
        document.addEventListener("mouseup", onUp);
    });
}

/* Collapsible sidebar sections */

const STORAGE_KEY = 'coral-sidebar-collapsed';

function getCollapsedState() {
    try {
        return JSON.parse(localStorage.getItem(STORAGE_KEY) || '{}');
    } catch {
        return {};
    }
}

function saveCollapsedState(state) {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(state));
}

export function initSidebarCollapse() {
    const sections = document.querySelectorAll('.sidebar-section[data-section]');
    const saved = getCollapsedState();

    for (const section of sections) {
        const sectionId = section.dataset.section;
        const header = section.querySelector('[data-collapse-toggle]');
        if (!header) continue;

        // Restore saved state
        if (saved[sectionId]) {
            section.classList.add('collapsed');
            section.dataset.manualCollapse = 'true';
        }

        header.addEventListener('click', (e) => {
            // Don't toggle if clicking a button inside the header
            if (e.target.closest('button')) return;

            const isCollapsed = section.classList.toggle('collapsed');
            section.dataset.manualCollapse = isCollapsed ? 'true' : '';
            if (!isCollapsed) {
                section.dataset.manualExpand = 'true';
            } else {
                delete section.dataset.manualExpand;
            }

            const state = getCollapsedState();
            state[sectionId] = isCollapsed;
            saveCollapsedState(state);
        });

        // Add aria-expanded
        header.setAttribute('role', 'button');
        header.setAttribute('aria-expanded', !section.classList.contains('collapsed'));
    }
}

export function updateSectionVisibility(sectionId, itemCount) {
    const section = document.querySelector(`[data-section="${sectionId}"]`);
    if (!section) return;

    const badge = section.querySelector('.section-count-badge');
    if (badge) badge.textContent = itemCount;

    // Update aria-expanded
    const header = section.querySelector('[data-collapse-toggle]');
    if (header) {
        header.setAttribute('aria-expanded', !section.classList.contains('collapsed'));
    }

    // Keep History available when a search/filter has no matches. Collapsing the
    // section would also hide the controls needed to change or clear the filter.
    const canAutoCollapseWhenEmpty = sectionId !== 'history';

    // Auto-collapse empty sections (unless user manually expanded)
    if (itemCount === 0 && canAutoCollapseWhenEmpty && !section.dataset.manualExpand) {
        section.classList.add('collapsed');
    } else if ((itemCount > 0 || !canAutoCollapseWhenEmpty) && !section.dataset.manualCollapse) {
        section.classList.remove('collapsed');
    }
}

/* Jobs subtab switching */

export function switchJobsSubtab(tab) {
    // Update subtab buttons
    document.querySelectorAll('.sidebar-subtab').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.subtab === tab);
    });

    // Show/hide content
    const activeContent = document.getElementById('jobs-subtab-active');
    const scheduledContent = document.getElementById('jobs-subtab-scheduled');
    if (activeContent) activeContent.style.display = tab === 'active' ? '' : 'none';
    if (scheduledContent) scheduledContent.style.display = tab === 'scheduled' ? '' : 'none';
}

/* Agentic block resize (top/bottom split) */

export function initAgenticBlockResize() {
    const handle = document.getElementById('agentic-block-resize-handle');
    const topBlock = document.getElementById('agentic-block-top');
    const bottomBlock = document.getElementById('agentic-block-bottom');
    const container = document.getElementById('agentic-state');

    if (!handle || !topBlock || !bottomBlock || !container) return;

    let dragging = false;

    handle.addEventListener('mousedown', (e) => {
        e.preventDefault();
        // Don't allow resize if either block is collapsed
        if (topBlock.classList.contains('collapsed') || bottomBlock.classList.contains('collapsed')) return;
        dragging = true;
        handle.classList.add('dragging');
        document.body.style.cursor = 'row-resize';
        document.body.style.userSelect = 'none';
    });

    document.addEventListener('mousemove', (e) => {
        if (!dragging) return;
        const rect = container.getBoundingClientRect();
        // Account for header/tabs heights
        const topTabs = topBlock.querySelector('.agentic-state-tabs');
        const bottomTabs = bottomBlock.querySelector('.agentic-state-tabs');
        const tabsHeight = (topTabs ? topTabs.offsetHeight : 0) + (bottomTabs ? bottomTabs.offsetHeight : 0) + handle.offsetHeight;
        const collapseBtn = container.querySelector('.agentic-collapse-btn');
        const available = rect.height - tabsHeight - (collapseBtn ? 0 : 0);
        const mouseY = e.clientY - rect.top;
        const topTabsH = topTabs ? topTabs.offsetHeight : 0;
        const topPanelHeight = mouseY - topTabsH;
        const minPanel = 40;
        const clampedTop = Math.max(minPanel, Math.min(topPanelHeight, available - minPanel));
        const ratio = clampedTop / available;
        // Store ratio as flex-grow values
        topBlock.style.flex = ratio.toString();
        bottomBlock.style.flex = (1 - ratio).toString();
        localStorage.setItem('coral-block-ratio', ratio.toFixed(3));
    });

    document.addEventListener('mouseup', () => {
        if (!dragging) return;
        dragging = false;
        handle.classList.remove('dragging');
        document.body.style.cursor = '';
        document.body.style.userSelect = '';
        fitTerminal();
    });

    // Restore saved ratio
    const saved = localStorage.getItem('coral-block-ratio');
    if (saved) {
        const ratio = parseFloat(saved);
        if (ratio > 0 && ratio < 1) {
            topBlock.style.flex = ratio.toString();
            bottomBlock.style.flex = (1 - ratio).toString();
        }
    }
}

/* Agentic block collapse (double-click tab bar) */

export function initAgenticBlockCollapse() {
    const blocks = document.querySelectorAll('.agentic-block');

    // Clear any stale collapsed state from localStorage — the hidden
    // double-click-to-collapse behavior confused users into permanently
    // hiding the panel with no obvious way to restore it.
    localStorage.removeItem('coral-block-collapsed');

    for (const block of blocks) {
        const tabs = block.querySelector('.agentic-state-tabs');
        if (!tabs) continue;

        tabs.addEventListener('dblclick', (e) => {
            // Don't toggle if clicking a specific tab button
            if (e.target.closest('.agentic-tab')) return;
            // Toggle collapsed state (session-only, not persisted)
            block.classList.toggle('collapsed');
        });
    }
}

/* Agentic state panel collapse */

export function initAgenticPanelCollapse() {
    const panel = document.getElementById('agentic-state');
    if (!panel) return;

    // Default to open unless user has explicitly closed it
    const stored = localStorage.getItem('coral-agentic-collapsed');
    const collapsed = stored === null ? false : stored === 'true';
    if (collapsed) panel.classList.add('collapsed');

    // Sync toggle button state
    _syncPanelToggleBtn(!collapsed);

    // Keep the old collapse button working too
    const btn = document.getElementById('agentic-collapse-btn');
    if (btn) {
        btn.addEventListener('click', () => {
            const isCollapsed = panel.classList.toggle('collapsed');
            localStorage.setItem('coral-agentic-collapsed', isCollapsed);
            _syncPanelToggleBtn(!isCollapsed);
            fitTerminal();
        });
    }
}

export function toggleAgenticPanel() {
    const panel = document.getElementById('agentic-state');
    if (!panel) return;
    const isCollapsed = panel.classList.toggle('collapsed');
    localStorage.setItem('coral-agentic-collapsed', isCollapsed);
    _syncPanelToggleBtn(!isCollapsed);
    // Fit terminal immediately and again after CSS transition completes
    fitTerminal();
    setTimeout(() => fitTerminal(), 50);
    setTimeout(() => fitTerminal(), 300);
}

function _syncPanelToggleBtn(isOpen) {
    const btn = document.getElementById('panel-toggle-btn');
    if (!btn) return;
    btn.classList.toggle('active', isOpen);
}
