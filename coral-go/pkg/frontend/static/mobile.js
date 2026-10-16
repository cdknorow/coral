/* Mobile navigation and view management */

import { state } from './state.js';
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

function _showMobileHistory(agentList) {
    if (!agentList) return;
    agentList.style.display = 'flex';
    agentList.dataset.mode = 'history';
    agentList.innerHTML = '';

    // Header
    const header = document.createElement('div');
    header.style.cssText = 'padding:12px 16px;';
    header.innerHTML = '<h2 class="mobile-list-title">Session History</h2>';
    agentList.appendChild(header);

    // Move the sidebar's history section body content into mobile view
    // We reference the actual sidebar elements so search/filters work
    const historySection = document.querySelector('[data-section="history"]');
    if (historySection) {
        const body = historySection.querySelector('.sidebar-section-body');
        if (body) {
            // Clone to avoid removing from sidebar
            const clone = body.cloneNode(true);
            clone.style.cssText = 'padding:0 12px;overflow-y:auto;flex:1;';

            // cloneNode keeps the inline onclick attributes, so the clones work as they are: natively on a
            // local Coral, through csp_compat's delegation on the hub (which forbids eval).

            // Re-wire search input
            const searchInput = clone.querySelector('input[type="search"]');
            if (searchInput) {
                const origInput = document.getElementById('history-search');
                if (origInput) {
                    searchInput.addEventListener('input', () => {
                        origInput.value = searchInput.value;
                        origInput.dispatchEvent(new Event('input'));
                    });
                }
            }

            agentList.appendChild(clone);
        }
    }
}

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

export function syncMobileAgentList() {
    if (!isMobile()) return;
    if (document.body.classList.contains('popout-mode')) return;

    const agentList = document.getElementById('mobile-agent-list');
    if (!agentList || agentList.dataset.mode === 'history') return;

    // Copy the live sessions list from sidebar
    const sidebarList = document.getElementById('live-sessions-list');
    if (sidebarList && agentList) {
        // Clear and clone
        agentList.innerHTML = '';

        // Add header with New button
        const header = document.createElement('div');
        header.style.cssText = 'display:flex;justify-content:space-between;align-items:center;padding:12px 16px;';
        header.innerHTML = `
            <h2 class="mobile-list-title">Live Sessions</h2>
            <div class="server-switcher-host" data-server-switcher-host hidden></div>
            <button class="btn btn-small btn-primary" data-on-click="launch-show">+ New</button>
        `;
        agentList.appendChild(header);
        // The servers button (#3085) lives in the sidebar's search row on wider screens.
        if (window.renderServerSwitcher) window.renderServerSwitcher();

        const clone = sidebarList.cloneNode(true);
        clone.id = 'mobile-session-list';

        // The cloned items keep their inline onclick attributes: they run natively on a local Coral and through
        // csp_compat's delegation on the hub. Moving them into eval() would be blocked by the hub's CSP.
        agentList.appendChild(clone);
    }
}

// ── Intercept Session Selection on Mobile ─────────────────────────────────

const _origSelectLiveSession = window.selectLiveSession;
export function wrapSelectLiveSession() {
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

// ── Nav Strip Edge Fade ───────────────────────────────────────────────────

// On a phone the top nav tabs scroll sideways with no scrollbar; the edge that
// hides more tabs fades out so they are discoverable (#3152).
function _initNavFade() {
    const nav = document.querySelector('.top-nav-tabs');
    if (!nav) return;
    const update = () => {
        const max = nav.scrollWidth - nav.clientWidth;
        nav.classList.toggle('fade-left', nav.scrollLeft > 2);
        nav.classList.toggle('fade-right', max > 2 && nav.scrollLeft < max - 2);
    };
    nav.addEventListener('scroll', update, { passive: true });
    // A mouse wheel in a window this narrow turns only vertically, and the strip has no scrollbar
    // to drag: a vertical turn over a clipped strip scrolls it sideways (#3608).
    nav.addEventListener('wheel', e => {
        const max = nav.scrollWidth - nav.clientWidth;
        if (max <= 2 || e.ctrlKey || Math.abs(e.deltaY) <= Math.abs(e.deltaX)) return;
        const step = e.deltaMode === 1 ? e.deltaY * 16 : e.deltaMode === 2 ? e.deltaY * nav.clientWidth : e.deltaY;
        const next = Math.max(0, Math.min(max, nav.scrollLeft + step));
        if (next === nav.scrollLeft) return;
        e.preventDefault();
        nav.scrollLeft = next;
    }, { passive: false });
    if (typeof ResizeObserver !== 'undefined') new ResizeObserver(update).observe(nav);
    else window.addEventListener('resize', update);
    update();
}

// ── Initialize Mobile ─────────────────────────────────────────────────────

export function initMobile() {
    _initTopBarHeight();
    _initNavFade();
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
            } else if (list && list.style.display !== 'none') {
                showView('welcome-screen');
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
