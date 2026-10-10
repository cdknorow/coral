/* Sidebar drag-to-resize functionality */

import { fitTerminal } from './xterm_renderer.js';
import { makeDraggable, makeDelegatedDraggable } from './draggable.js';

/* Layout persistence is scoped per entry mode: a narrow popout window must
   never overwrite the dashboard's saved panel sizes. */
function layoutKey(base) {
    return document.body.classList.contains('popout-mode') ? `${base}:popout` : base;
}

export function initSidebarResize() {
    const handle = document.getElementById("sidebar-resize-handle");
    const sidebar = document.querySelector(".sidebar");

    const saved = localStorage.getItem('coral-sidebar-width');
    if (saved) {
        const w = parseInt(saved, 10);
        if (w >= 200 && w <= window.innerWidth * 0.5) sidebar.style.width = w + "px";
    }

    makeDraggable(handle, {
        cursor: 'col-resize',
        onMove: (e) => Math.min(Math.max(e.clientX, 200), window.innerWidth * 0.5),
        onFlush: (v) => { sidebar.style.width = v + "px"; },
        onEnd: () => { localStorage.setItem('coral-sidebar-width', sidebar.offsetWidth); },
    });
}

/* Task bar drag-to-resize functionality */

const AGENTIC_MIN_WIDTH = 280;
const LEFT_COLUMN_MIN_WIDTH = 240;

function maxAgenticWidth(liveBody) {
    const measured = liveBody ? liveBody.getBoundingClientRect().width : 0;
    const available = measured > 0 ? measured : window.innerWidth;
    return Math.max(AGENTIC_MIN_WIDTH, available - LEFT_COLUMN_MIN_WIDTH);
}

export function initTaskBarResize() {
    const handle = document.getElementById("task-bar-resize-handle");
    const taskBar = document.getElementById("agentic-state");
    const liveBody = document.querySelector(".live-body");

    if (!handle || !taskBar || !liveBody) return;

    const saved = localStorage.getItem(layoutKey('coral-taskbar-width'));
    if (saved) {
        const w = parseInt(saved, 10);
        if (w >= AGENTIC_MIN_WIDTH) {
            taskBar.style.width = Math.min(w, maxAgenticWidth(liveBody)) + "px";
        }
    }

    makeDraggable(handle, {
        cursor: 'col-resize',
        onMove: (e) => {
            const rect = liveBody.getBoundingClientRect();
            const newWidth = rect.right - e.clientX;
            return Math.min(Math.max(newWidth, AGENTIC_MIN_WIDTH), maxAgenticWidth(liveBody));
        },
        onFlush: (v) => { taskBar.style.width = v + "px"; },
        onEnd: (v) => {
            if (v != null) localStorage.setItem(layoutKey('coral-taskbar-width'), Math.round(v));
        },
    });
}

/* Board chat input pane resize */

export function initBoardChatResize() {
    makeDelegatedDraggable(document, '.board-chat-resize-handle', {
        cursor: 'row-resize',
        resolve: (handle) => {
            const pane = handle.closest('.board-chat-input-pane');
            return pane ? { pane } : null;
        },
        onStart: (handle) => { handle.style.background = 'var(--accent)'; },
        onMove: (e, { pane }) => {
            const rect = pane.parentElement.getBoundingClientRect();
            const newHeight = rect.bottom - e.clientY;
            return Math.min(Math.max(newHeight, 80), rect.height * 0.6);
        },
        onFlush: (v, { pane }) => { pane.style.height = v + "px"; },
        onEnd: (handle, _v, { pane }) => {
            handle.style.background = '';
            localStorage.setItem(layoutKey('coral-boardchat-height'), pane.offsetHeight);
        },
    });
}

/* Agentic block resize (top/bottom split) */

export function initAgenticBlockResize() {
    const handle = document.getElementById('agentic-block-resize-handle');
    const topBlock = document.getElementById('agentic-block-top');
    const bottomBlock = document.getElementById('agentic-block-bottom');
    const container = document.getElementById('agentic-state');

    if (!handle || !topBlock || !bottomBlock || !container) return;

    // Restore saved ratio
    const saved = localStorage.getItem('coral-block-ratio');
    if (saved) {
        const ratio = parseFloat(saved);
        if (ratio > 0 && ratio < 1) {
            topBlock.style.flex = ratio.toString();
            bottomBlock.style.flex = (1 - ratio).toString();
        }
    }

    makeDraggable(handle, {
        cursor: 'row-resize',
        canDrag: () => !topBlock.classList.contains('collapsed') && !bottomBlock.classList.contains('collapsed'),
        onMove: (e) => {
            const rect = container.getBoundingClientRect();
            const topTabs = topBlock.querySelector('.agentic-state-tabs');
            const bottomTabs = bottomBlock.querySelector('.agentic-state-tabs');
            const tabsHeight = (topTabs ? topTabs.offsetHeight : 0) + (bottomTabs ? bottomTabs.offsetHeight : 0) + handle.offsetHeight;
            const available = rect.height - tabsHeight;
            const topTabsH = topTabs ? topTabs.offsetHeight : 0;
            const topPanelHeight = e.clientY - rect.top - topTabsH;
            const minPanel = 40;
            return Math.max(minPanel, Math.min(topPanelHeight, available - minPanel)) / available;
        },
        onFlush: (ratio) => {
            topBlock.style.flex = ratio.toString();
            bottomBlock.style.flex = (1 - ratio).toString();
        },
        onEnd: (ratio) => {
            if (ratio != null) localStorage.setItem('coral-block-ratio', ratio.toFixed(3));
        },
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
    const stored = localStorage.getItem(layoutKey('coral-agentic-collapsed'));
    const collapsed = stored === null ? false : stored === 'true';
    if (collapsed) panel.classList.add('collapsed');

    // Sync toggle button state
    _syncPanelToggleBtn(!collapsed);

    // Keep the old collapse button working too
    const btn = document.getElementById('agentic-collapse-btn');
    if (btn) {
        btn.addEventListener('click', () => {
            const isCollapsed = panel.classList.toggle('collapsed');
            localStorage.setItem(layoutKey('coral-agentic-collapsed'), isCollapsed);
            _syncPanelToggleBtn(!isCollapsed);
            fitTerminal();
        });
    }
}

export function toggleAgenticPanel(forceState) {
    const panel = document.getElementById('agentic-state');
    if (!panel) return;
    if (window.innerWidth <= 767) {
        let isOverlay;
        if (typeof forceState === 'boolean') {
            isOverlay = forceState;
            panel.classList.toggle('mobile-panel-overlay', isOverlay);
        } else {
            isOverlay = panel.classList.toggle('mobile-panel-overlay');
        }
        panel.classList.remove('collapsed');
        _syncPanelToggleBtn(isOverlay);
        if (isOverlay) {
            // Ensure an active panel is selected if none active
            const activePanel = panel.querySelector('.agentic-panel.active');
            if (!activePanel) {
                const filesTab = document.getElementById('agentic-tab-files');
                if (filesTab) filesTab.click();
            }
        }
        return;
    }
    const isCollapsed = typeof forceState === 'boolean' ? !forceState : panel.classList.toggle('collapsed');
    if (typeof forceState === 'boolean') {
        panel.classList.toggle('collapsed', isCollapsed);
    }
    localStorage.setItem(layoutKey('coral-agentic-collapsed'), isCollapsed);
    _syncPanelToggleBtn(!isCollapsed);
    // Fit terminal immediately and again after CSS transition completes
    fitTerminal();
    setTimeout(() => fitTerminal(), 50);
    setTimeout(() => fitTerminal(), 300);
}

export function _syncPanelToggleBtn(isOpen) {
    const btn = document.getElementById('panel-toggle-btn');
    if (btn) btn.classList.toggle('active', isOpen);
    const popoutBtn = document.getElementById('popout-panel-toggle-btn');
    if (popoutBtn) {
        popoutBtn.classList.toggle('active', isOpen);
        popoutBtn.setAttribute('aria-pressed', isOpen ? 'true' : 'false');
        popoutBtn.setAttribute('aria-label', isOpen ? 'Hide side panel' : 'Show side panel');
        popoutBtn.title = popoutBtn.getAttribute('aria-label');
    }
}


/* Collapse the agent sidebar to an icon rail (avatars rendered by render.js). */
const RAIL_KEY = 'coral-sidebar-collapsed-rail';

function applyRail(collapsed) {
    document.querySelector('.layout')?.classList.toggle('sidebar-collapsed', collapsed);
    // The terminal column changes width with the sidebar.
    requestAnimationFrame(() => fitTerminal?.());
}

export function toggleSidebarRail() {
    const collapsed = !document.querySelector('.layout')?.classList.contains('sidebar-collapsed');
    try { localStorage.setItem(RAIL_KEY, collapsed ? '1' : '0'); } catch { /* storage unavailable */ }
    applyRail(collapsed);
}

export function initSidebarRail() {
    let collapsed = false;
    try { collapsed = localStorage.getItem(RAIL_KEY) === '1'; } catch { /* storage unavailable */ }
    if (collapsed) applyRail(true);
}
