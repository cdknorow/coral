/* Mobile navigation and view management */

import { state } from './state.js';
import { syncDisplayMenu } from './session_rows.js';
import { showView } from './utils.js';
import { note, backIs } from './nav.js';

const MOBILE_BREAKPOINT = 767;

let _currentMobileTab = 'agents';

function isMobile() {
    return window.innerWidth <= MOBILE_BREAKPOINT;
}

// ── Tablet Sidebar Toggle ─────────────────────────────────────────────────

function toggleTabletSidebar() {
    const sidebar = document.querySelector('.sidebar');
    const backdrop = document.querySelector('.tablet-sidebar-backdrop');
    if (!sidebar) return;

    const isOpen = sidebar.classList.toggle('tablet-open');
    if (backdrop) backdrop.classList.toggle('active', isOpen);
}
window.toggleTabletSidebar = toggleTabletSidebar;

// ── Bottom Tab Bar Navigation ─────────────────────────────────────────────

function switchMobileTab(tab) {
    _currentMobileTab = tab;

    // Tab bar is hidden on mobile via CSS — don't override with inline styles

    // Update tab active states
    document.querySelectorAll('.mobile-tab').forEach(t => {
        t.classList.toggle('active', t.dataset.tab === tab);
    });

    const view = tab === 'board' ? 'messageboard-view'
        : tab === 'chat' && state.currentSession?.type === 'live' ? 'live-session-view'
        : 'mobile-agent-list';
    showView(view);
}
window.switchMobileTab = switchMobileTab;

// ── Mobile Back Navigation ────────────────────────────────────────────────

function mobileBack() {
    if (!isMobile()) return;

    // If panel overlay is currently open, close it first and return to chat
    const agenticState = document.getElementById('agentic-state');
    if (agenticState && agenticState.classList.contains('mobile-panel-overlay')) {
        if (window.toggleAgenticPanel) {
            window.toggleAgenticPanel(false);
        } else {
            agenticState.classList.remove('mobile-panel-overlay');
        }
        return;
    }

    showView('mobile-agent-list');

    // Hide panel overlay and panel tab
    if (agenticState) agenticState.classList.remove('mobile-panel-overlay');
    const panelTab = document.getElementById('mobile-tab-panel');
    if (panelTab) panelTab.style.display = 'none';

    _currentMobileTab = 'agents';
    document.querySelectorAll('.mobile-tab').forEach(t => {
        t.classList.toggle('active', t.dataset.tab === 'agents');
    });

    // The agent list is the Agents page (#3532): Back when that is the entry before, so the phone's
    // back button and the browser's agree; otherwise a new entry.
    if (backIs({ view: 'agents' })) history.back();
    else note({ view: 'agents' });
}
window.mobileBack = mobileBack;

// ── Sync Agent List to Mobile View ────────────────────────────────────────

// On a phone the agent list is built once, in the Agents page itself: the sidebar's own list element moves into #mobile-agent-list (and takes the id
// #mobile-session-list that the phone's styles use) instead of being cloned, so the page holds one copy of every row and no id twice (#4497). On a wide
// screen it moves back, as #live-sessions-list, to where the sidebar has it.
let _sidebarHome = null; // { parent, next }: where the list sits in the sidebar

function _listHeader(agentList) {
    let header = agentList.querySelector(':scope > .mobile-list-header');
    if (header) return header;
    header = document.createElement('div');
    header.className = 'mobile-list-header';
    header.innerHTML = `
            <h2 class="mobile-list-title">Live Sessions</h2>
            <div class="server-switcher-host" data-server-switcher-host hidden></div>
            <details class="list-display-menu" data-list-display-menu>
                <summary class="sidebar-collapse-btn" data-display-button title="Display options" aria-label="Display options">
                    <span class="material-icons" style="font-size:1.125rem" aria-hidden="true">notes</span>
                </summary>
                <div class="list-display-panel" role="group" aria-label="Display options">
                    <label class="list-display-option"><input type="checkbox" data-display-input="summary" data-on-change="toggle-session-summary"> Show summaries</label>
                    <label class="list-display-option" data-display-row="server" hidden><input type="checkbox" data-display-input="server" data-on-change="toggle-show-server"> Show server</label>
                    <label class="list-display-option" data-display-row="group" hidden><input type="checkbox" data-display-input="group" data-on-change="toggle-group-by-server"> Group by server</label>
                    <div class="list-display-actions"><button type="button" class="btn btn-small" data-on-click="groups-collapse-all">Collapse all</button><button type="button" class="btn btn-small" data-on-click="groups-expand-all">Expand all</button></div>
                </div>
            </details>
            <button class="btn btn-small btn-primary" data-on-click="launch-show">+ New</button>
            <div class="mobile-search-row" id="mobile-search-row">
                <input type="search" class="sidebar-search-input mobile-search-input" placeholder="Search agents" autocomplete="off" aria-label="Search agents" data-on-input="agent-search-filter" data-on-keydown="agent-search-key">
            </div>
        `;
    agentList.prepend(header);
    // The list's filters and sort (agent_list_bar.js, #4744), under the header's own controls.
    const bar = document.createElement('div'); bar.className = 'agent-list-bar'; bar.id = 'mobile-agent-list-bar'; bar.hidden = true;
    header.after(bar);
    return header;
}

// The phone's Agents page has its own Search agents box under the list header's controls (#4624): the sidebar's search is not on the phone. It is the same search (filterLiveAgents,
// Enter and Escape as there); a wide screen's box takes over its text when the list goes back to the sidebar (restoreSidebarList).

export function syncMobileAgentList() {
    if (document.body.classList.contains('popout-mode')) return;
    if (!isMobile()) { restoreSidebarList(); return; }

    const agentList = document.getElementById('mobile-agent-list');
    if (!agentList || agentList.dataset.mode === 'history') return;

    _listHeader(agentList);
    const phoneBox = agentList.querySelector('#mobile-search-row input'), sideBox = document.getElementById('agent-search-input');
    if (phoneBox && sideBox && !phoneBox.value) phoneBox.value = sideBox.value; // a search typed on a wide screen is still in the box
    syncDisplayMenu();   // the phone's copy of the ≡ menu (#3991)
    // The servers button (#3085) lives in the sidebar's search row on wider screens.
    if (window.renderServerSwitcher) window.renderServerSwitcher();

    const list = document.getElementById('live-sessions-list');
    if (list && list.parentElement !== agentList) {
        _sidebarHome = { parent: list.parentElement, next: list.nextSibling };
        list.id = 'mobile-session-list';
        agentList.appendChild(list); // the cloned items kept their data-on-* actions; the moved ones keep their listeners too
    }
}

/** Wide screen: the list goes back into the sidebar as #live-sessions-list, where it was. */
function restoreSidebarList() {
    const list = document.getElementById('mobile-session-list');
    if (!list || !_sidebarHome || !_sidebarHome.parent.isConnected) return;
    list.id = 'live-sessions-list';
    // The search the phone's box held carries over to the sidebar's own box.
    const phoneSearch = document.querySelector('#mobile-search-row input'), sideSearch = document.getElementById('agent-search-input');
    if (phoneSearch && sideSearch) sideSearch.value = phoneSearch.value;
    _sidebarHome.parent.insertBefore(list, _sidebarHome.next && _sidebarHome.next.parentNode === _sidebarHome.parent ? _sidebarHome.next : null);
    _sidebarHome = null;
}

// ── Intercept Session Selection on Mobile ─────────────────────────────────

const _origSelectLiveSession = window.selectLiveSession;
 function wrapSelectLiveSession() {
    const orig = window.selectLiveSession;
    if (!orig) return;

    window.selectLiveSession = function(name, agentType, sessionId, server) {
        // Call original
        orig(name, agentType, sessionId, server);

        // On mobile, hide agent list to show full session view and reset panel overlay
        if (isMobile()) {
            const agentList = document.getElementById('mobile-agent-list');
            if (agentList) agentList.style.display = 'none';
            const agenticState = document.getElementById('agentic-state');
            if (agenticState) {
                agenticState.classList.remove('mobile-panel-overlay');
                if (window.toggleAgenticPanel) window.toggleAgenticPanel(false);
            }
        }

        // On tablet, close the sidebar overlay
        const sidebar = document.querySelector('.sidebar');
        const backdrop = document.querySelector('.tablet-sidebar-backdrop');
        if (sidebar) sidebar.classList.remove('tablet-open');
        if (backdrop) backdrop.classList.remove('active');
    };
}

// ── Pull-to-Refresh ───────────────────────────────────────────────────────

function _initPullToRefresh() {
    const agentList = document.getElementById('mobile-agent-list');
    if (!agentList) return;

    let startY = 0;
    let pulling = false;
    let refreshIndicator = null;

    agentList.addEventListener('touchstart', (e) => {
        if (agentList.scrollTop === 0) {
            startY = e.touches[0].clientY;
            pulling = true;
        }
    }, { passive: true });

    agentList.addEventListener('touchmove', (e) => {
        if (!pulling) return;
        const dy = e.touches[0].clientY - startY;
        if (dy > 10 && agentList.scrollTop === 0) {
            if (!refreshIndicator) {
                refreshIndicator = document.createElement('div');
                refreshIndicator.className = 'pull-refresh-indicator';
                refreshIndicator.textContent = 'Pull to refresh...';
                agentList.insertBefore(refreshIndicator, agentList.firstChild);
            }
            const progress = Math.min(dy / 80, 1);
            refreshIndicator.style.height = Math.min(dy * 0.5, 50) + 'px';
            refreshIndicator.style.opacity = progress;
            if (dy > 80) {
                refreshIndicator.textContent = 'Release to refresh';
            } else {
                refreshIndicator.textContent = 'Pull to refresh...';
            }
        }
    }, { passive: true });

    agentList.addEventListener('touchend', (e) => {
        if (!pulling) return;
        pulling = false;
        const dy = (e.changedTouches[0]?.clientY || 0) - startY;
        if (dy > 80 && refreshIndicator) {
            refreshIndicator.textContent = 'Refreshing...';
            // Trigger session reload
            if (window._coralLoadLiveSessions) {
                window._coralLoadLiveSessions();
            }
            setTimeout(() => {
                if (refreshIndicator && refreshIndicator.parentNode) {
                    refreshIndicator.remove();
                }
                refreshIndicator = null;
            }, 1000);
        } else {
            if (refreshIndicator && refreshIndicator.parentNode) {
                refreshIndicator.remove();
            }
            refreshIndicator = null;
        }
    }, { passive: true });
}

// ── Swipe Between Agents ──────────────────────────────────────────────────

function _initSwipeNavigation() {
    let touchStartX = 0;
    let touchStartY = 0;

    document.addEventListener('touchstart', (e) => {
        if (!isMobile()) return;
        touchStartX = e.touches[0].clientX;
        touchStartY = e.touches[0].clientY;
    }, { passive: true });

    document.addEventListener('touchend', (e) => {
        if (!isMobile()) return;
        if (document.body.classList.contains('popout-mode')) return;

        const dx = e.changedTouches[0].clientX - touchStartX;
        const dy = e.changedTouches[0].clientY - touchStartY;

        // Only detect horizontal swipes (dx much larger than dy)
        if (Math.abs(dx) < 60 || Math.abs(dy) > Math.abs(dx) * 0.7) return;

        // Only swipe when viewing a live session and side panel is closed
        const liveView = document.getElementById('live-session-view');
        if (!liveView || liveView.style.display === 'none') return;
        const panel = document.getElementById('agentic-state');
        if (panel && panel.classList.contains('mobile-panel-overlay')) return;

        const sessions = state.liveSessions || [];
        if (sessions.length < 2) return;

        const currentId = state.currentSession?.session_id;
        const currentIdx = sessions.findIndex(s => s.session_id === currentId);
        if (currentIdx < 0) return;

        let nextIdx;
        if (dx > 0) {
            // Swipe right → previous agent
            nextIdx = currentIdx > 0 ? currentIdx - 1 : sessions.length - 1;
        } else {
            // Swipe left → next agent
            nextIdx = currentIdx < sessions.length - 1 ? currentIdx + 1 : 0;
        }

        const next = sessions[nextIdx];
        if (next && window.selectLiveSession) {
            window.selectLiveSession(next.name, next.agent_type, next.session_id, next.server);
        }
    }, { passive: true });
}

// ── Virtual Keyboard Detection & Viewport Height Tracking ─────────────────

function _initKeyboardDetection() {
    if (!window.visualViewport) return;

    const updateViewportHeight = () => {
        if (!isMobile()) {
            document.documentElement.style.removeProperty('--visual-viewport-height');
            return;
        }
        const height = window.visualViewport.height;
        document.documentElement.style.setProperty('--visual-viewport-height', `${height}px`);
    };

    // Track the initial viewport height to detect keyboard open/close
    let initialHeight = window.visualViewport.height;
    updateViewportHeight();

    window.visualViewport.addEventListener('resize', () => {
        if (!isMobile()) return;
        updateViewportHeight();

        // Keyboard is open when viewport shrinks significantly (>100px)
        const heightDiff = initialHeight - window.visualViewport.height;
        const keyboardOpen = heightDiff > 100;

        document.body.classList.toggle('keyboard-open', keyboardOpen);
    });

    window.visualViewport.addEventListener('scroll', () => {
        // Prevent unwanted visualViewport scroll offset on mobile
        if (window.visualViewport.pageTop > 0) {
            window.scrollTo(0, 0);
        }
    });

    // Update initial height on orientation change
    window.addEventListener('orientationchange', () => {
        setTimeout(() => {
            initialHeight = window.visualViewport.height;
            updateViewportHeight();
        }, 300);
    });

    window.addEventListener('resize', () => {
        if (!document.body.classList.contains('keyboard-open')) {
            initialHeight = window.visualViewport.height;
        }
        updateViewportHeight();
    });
}

// ── Top Bar Height ────────────────────────────────────────────────────────

// Fixed overlays below the top bar (side panel, sidebar, backdrop) start at
// --top-bar-height. The bar's height is not a constant (44px touch targets on a
// coarse pointer, safe-area padding, wrapping), so it is measured.
function _initTopBarHeight() {
    const bar = document.querySelector('header.top-bar');
    if (!bar) return;
    const root = document.documentElement;
    const update = () => {
        const bottom = Math.ceil(bar.getBoundingClientRect().bottom);
        if (bottom > 0) root.style.setProperty('--top-bar-height', bottom + 'px');
    };
    update();
    if (typeof ResizeObserver !== 'undefined') new ResizeObserver(update).observe(bar);
    window.addEventListener('resize', update);
}

// ── Initialize Mobile ─────────────────────────────────────────────────────

export function initMobile() {
    _initTopBarHeight();
    // Popout mode has no agent list, tab bar or swipe navigation.
    if (document.body.classList.contains('popout-mode')) {
        _initKeyboardDetection();
        return;
    }
    // Set default tab
    if (isMobile()) {
        switchMobileTab('agents');
    }

    // Wrap selectLiveSession for mobile navigation
    wrapSelectLiveSession();

    // Initialize touch gestures
    _initPullToRefresh();
    _initSwipeNavigation();

    // Detect virtual keyboard for hiding tab bar
    _initKeyboardDetection();

    // Listen for resize to toggle mobile/desktop
    let wasMobile = isMobile();
    window.addEventListener('resize', () => {
        const mobile = isMobile();
        if (mobile !== wasMobile) {
            const list = document.getElementById('mobile-agent-list');
            const welcome = document.getElementById('welcome-screen');
            if (mobile) {
                syncMobileAgentList();
                if (welcome && welcome.style.display !== 'none') showView('mobile-agent-list');
            } else {
                restoreSidebarList();
                if (list && list.style.display !== 'none') showView('welcome-screen');
            }
            wasMobile = mobile;
        }
        const tabBar = document.querySelector('.mobile-tab-bar');
        if (!tabBar) return;

        if (!isMobile()) {
            // Restore side panel if resized to desktop
            const agenticState = document.getElementById('agentic-state');
            if (agenticState && agenticState.classList.contains('mobile-panel-overlay')) {
                agenticState.classList.remove('mobile-panel-overlay');
            }
            // Ensure tab bar stays hidden on desktop (CSS handles mobile)
            tabBar.style.display = '';
            // Restore sidebar visibility
            const sidebar = document.querySelector('.sidebar');
            const handle = document.querySelector('.sidebar-resize-handle');
            if (sidebar) sidebar.style.display = '';
            if (handle) handle.style.display = '';
        }
    });
}
