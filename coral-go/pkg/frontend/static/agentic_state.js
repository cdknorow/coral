import { refreshAgentUI } from './agent_ui.js';
/* Agentic State — event loading, timeline rendering, tab switching, filtering */

import { importOnce } from './import_once.js';
import { state } from './state.js';
import { loadChangedFiles } from './changed_files.js';
import { selectFilesSource } from './team_artifacts.js';
import { startBoardTaskPoll, stopBoardTaskPoll } from './tasks.js';
import { escapeHtml, escapeAttr } from './utils.js';
import { morphHtml } from './dom_morph.js';
import { registerActions } from './actions.js';
import { durationChip, eventDetailText, eventFailed, renderActivityChart } from './event_row.js';
import { serverFetch, serverForSession } from './server_base.js';
import { formatClockTime } from './format.js';
import { FILTER_GROUPS, getToolIcon, eventVisible, historyEventsView } from './event_icons.js';
import { openableTab } from './tab_customize.js';
import { filterItemHtml, refillFilterList, syncFilterExpanded, wireFilterDropdown } from './filter_dropdown.js';
import { browserLocal } from './safe_storage.js';

// Hidden filters are persisted per-session in state
// state.eventFiltersHidden = Set of filter keys that are hidden

function getHiddenFilters() {
    if (!state.eventFiltersHidden) {
        state.eventFiltersHidden = new Set();
    }
    return state.eventFiltersHidden;
}

function isEventVisible(ev) {
    return eventVisible(ev, getHiddenFilters());
}

export async function loadAgentEvents(agentName, sessionId, options) {
    if (!agentName) return;
    const sid = sessionId || (state.currentSession && state.currentSession.session_id);
    try {
        const params = new URLSearchParams({ limit: 50 });
        if (sid) params.set("session_id", sid);
        const resp = await serverFetch(serverForSession(agentName, sid), `/api/sessions/live/${encodeURIComponent(agentName)}/events?${params}`, options);
        if (!resp.ok) throw new Error(`events fetch failed: ${resp.status}`);
        const events = await resp.json();
        if (sid && state.currentSession?.session_id !== sid) return;
        state.currentAgentEvents = events;
    } catch (e) {
        if (e?.name === 'AbortError') return;
        if (sid && state.currentSession?.session_id !== sid) return;
        state.currentAgentEvents = [];
    }
    renderEventTimeline();
}

 function renderEventFilters() {
    const container = document.getElementById('event-filters');
    if (!container) return;

    const hidden = getHiddenFilters();
    const events = state.currentAgentEvents || [];
    const activeCount = FILTER_GROUPS.length - hidden.size;
    const badge = hidden.size > 0 ? `<span class="filter-btn-badge">${activeCount}/${FILTER_GROUPS.length}</span>` : '';

    morphHtml(container, `<button class="filter-dropdown-btn" data-on-click="filter-dropdown" data-mode="live"
        aria-haspopup="menu" aria-expanded="${document.getElementById('filter-dropdown') ? 'true' : 'false'}">
        <span class="event-filter-char">&#xe152;</span>
        <span class="filter-btn-label">Filter</span>
        ${badge}
    </button>`);
}

// Track which mode the dropdown is open for
let _filterDropdownMode = null;

export function toggleFilterDropdown(mode) {
    const existing = document.getElementById('filter-dropdown');
    if (existing) {
        existing.remove();
        _filterDropdownMode = null;
        syncFilterExpanded();
        return;
    }
    _openFilterDropdown(mode);
}

function _openFilterDropdown(mode) {
    _filterDropdownMode = mode;
    const isLive = mode === 'live';
    const hidden = isLive ? getHiddenFilters() : historyEventsView.hidden;
    const events = isLive ? (state.currentAgentEvents || []) : historyEventsView.events;
    const btn = document.querySelector(`#${isLive ? 'event-filters' : 'history-event-filters'} .filter-dropdown-btn`);
    if (!btn) return;

    const dropdown = document.createElement('div');
    dropdown.className = 'filter-dropdown';
    dropdown.id = 'filter-dropdown';

    const items = FILTER_GROUPS.map(group => {
        const isHidden = hidden ? hidden.has(group.key) : false;
        const checked = !isHidden;
        const count = events ? events.filter(group.match).length : '';
        const countHtml = count !== '' ? `<span class="filter-item-count">${count}</span>` : '';
        return filterItemHtml(group, mode, checked, countHtml);
    }).join('');

    dropdown.innerHTML = `
        <div class="filter-dropdown-header">
            <span class="filter-dropdown-title">Filter Events</span>
            <button class="filter-dropdown-toggle" data-on-click="filter-toggle-all" data-mode="${escapeAttr(mode)}">Toggle All</button>
        </div>
        <div class="filter-dropdown-list">${items}</div>
    `;

    const rect = btn.getBoundingClientRect();
    dropdown.style.position = 'fixed';
    dropdown.style.top = (rect.bottom + 4) + 'px';
    dropdown.style.left = rect.left + 'px';

    document.body.appendChild(dropdown);
    syncFilterExpanded();
    wireFilterDropdown(dropdown, () => document.querySelector(`#${isLive ? 'event-filters' : 'history-event-filters'} .filter-dropdown-btn`), () => toggleFilterDropdown(mode));

    // Close on outside click
    setTimeout(() => {
        const closeHandler = (e) => {
            const dd = document.getElementById('filter-dropdown');
            if (!dd || (!dd.contains(e.target) && !btn.contains(e.target))) {
                if (dd) dd.remove();
                _filterDropdownMode = null;
                syncFilterExpanded();
                document.removeEventListener('click', closeHandler);
            }
        };
        document.addEventListener('click', closeHandler);
    }, 0);
}

export function toggleEventFilter(key) {
    if (!FILTER_GROUPS.some(g => g.key === key)) return; // only a filter this list has
    const hidden = getHiddenFilters();
    if (hidden.has(key)) {
        hidden.delete(key);
    } else {
        hidden.add(key);
    }
    _refreshFilterDropdown('live');
    renderEventFilters();
    renderEventTimeline();
}

export function toggleAllEventFilters() {
    const hidden = getHiddenFilters();
    if (hidden.size === 0) {
        for (const group of FILTER_GROUPS) hidden.add(group.key);
    } else {
        hidden.clear();
    }
    _refreshFilterDropdown('live');
    renderEventFilters();
    renderEventTimeline();
}

function _refreshFilterDropdown(mode) {
    const dropdown = document.getElementById('filter-dropdown');
    if (!dropdown) return;
    // Re-render dropdown items in place
    const isLive = mode === 'live';
    const hidden = isLive ? getHiddenFilters() : historyEventsView.hidden;
    const events = isLive ? (state.currentAgentEvents || []) : historyEventsView.events;

    const items = FILTER_GROUPS.map(group => {
        const isHidden = hidden ? hidden.has(group.key) : false;
        const checked = !isHidden;
        const count = events ? events.filter(group.match).length : '';
        const countHtml = count !== '' ? `<span class="filter-item-count">${count}</span>` : '';
        return filterItemHtml(group, mode, checked, countHtml);
    }).join('');

    refillFilterList(dropdown, items);
}

export function renderEventTimeline() {
    const container = document.getElementById('agentic-state-events');
    if (!container) return;

    const events = state.currentAgentEvents || [];

    // Also update filter chips (counts may have changed)
    renderEventFilters();

    if (events.length === 0) {
        morphHtml(container, '<div class="event-empty">No activity yet</div>');
        return;
    }

    const visible = events.filter(isEventVisible);
    if (visible.length === 0) {
        morphHtml(container, '<div class="event-empty">All events filtered out</div>');
        return;
    }

    // Events come newest-first from API, display newest at top
    morphHtml(container, visible.map(ev => {
        const icon = getToolIcon(ev.tool_name, ev.event_type);
        const typeCls = (ev.event_type ? `event-type-${ev.event_type}` : '') + (eventFailed(ev) ? ' event-failed' : '');
        const extra = eventDetailText(ev);
        return `<div class="event-item ${typeCls}" data-tooltip="${escapeAttr(icon.title === ev.summary ? '' : escapeAttr(icon.title) + ': ')}${escapeAttr(ev.summary)}${escapeAttr(extra ? escapeAttr(' (' + extra + ')') : '')}">
            <span class="event-icon ${icon.cls}">${icon.char}</span>
            <span class="event-body">
                <span class="event-summary">${escapeHtml(ev.summary)}</span>
            </span>
            ${durationChip(ev)}
            <span class="event-time">${formatClockTime(ev.created_at)}</span>
        </div>`;
    }).join(''));

    renderLiveActivityChart();
}

function renderLiveActivityChart() {
    renderActivityChart(document.getElementById('live-activity-chart'), state.currentAgentEvents || [], FILTER_GROUPS);
}

let _popupTimer = null;

export function showFilterPopup(mouseEvent, key) {
    hideFilterPopup();
    const group = FILTER_GROUPS.find(g => g.key === key);
    if (!group) return;

    const events = (state.currentAgentEvents || []).filter(group.match);
    const hidden = getHiddenFilters();
    const isHidden = hidden.has(key);

    // Build popup content
    const statusText = isHidden ? 'Hidden — click to show' : 'Showing — click to hide';
    const recentItems = events.slice(0, 5);
    const recentHtml = recentItems.length > 0
        ? recentItems.map(ev =>
            `<div class="filter-popup-event">
                <span class="filter-popup-summary">${escapeHtml(ev.summary)}</span>
                <span class="filter-popup-time">${formatClockTime(ev.created_at)}</span>
            </div>`
        ).join('')
        : '<div class="filter-popup-empty">No events</div>';

    const popup = document.createElement('div');
    popup.className = 'filter-popup';
    popup.id = 'filter-popup';
    popup.innerHTML = `
        <div class="filter-popup-header">
            <span class="filter-popup-title">${escapeHtml(group.title)}</span>
            <span class="filter-popup-count">${events.length} event${events.length !== 1 ? 's' : ''}</span>
        </div>
        <div class="filter-popup-status">${statusText}</div>
        <div class="filter-popup-list">${recentHtml}</div>
        ${events.length > 5 ? `<div class="filter-popup-more">+${events.length - 5} more</div>` : ''}
    `;

    document.body.appendChild(popup);

    // Position relative to the chip
    const chip = mouseEvent.currentTarget;
    const rect = chip.getBoundingClientRect();
    const popupRect = popup.getBoundingClientRect();

    let left = rect.left + rect.width / 2 - popupRect.width / 2;
    // Clamp to viewport
    left = Math.max(4, Math.min(left, window.innerWidth - popupRect.width - 4));
    popup.style.left = left + 'px';
    popup.style.top = (rect.bottom + 6) + 'px';
}

export function hideFilterPopup() {
    const existing = document.getElementById('filter-popup');
    if (existing) existing.remove();
    if (_popupTimer) {
        clearTimeout(_popupTimer);
        _popupTimer = null;
    }
}

export function switchAgenticTab(tabName, blockId) {
    tabName = openableTab(tabName); // a tab the user hid opens the always-shown one instead
    // Scope tab/panel switching to the containing block
    const block = blockId
        ? document.getElementById(`agentic-block-${blockId}`)
        : document.getElementById(`agentic-tab-${tabName}`)?.closest('.agentic-block');

    if (block) {
        // Clicking a tab means the user wants to see content — uncollapse
        block.classList.remove('collapsed');
        block.querySelectorAll('.agentic-tab').forEach(btn => btn.classList.remove('active'));
        block.querySelectorAll('.agentic-panel').forEach(panel => panel.classList.remove('active'));
    }

    const activeBtn = document.getElementById(`agentic-tab-${tabName}`);
    if (activeBtn) activeBtn.classList.add('active');

    const activePanel = document.getElementById(`agentic-panel-${tabName}`);
    if (activePanel) activePanel.classList.add('active');

    // Persist tab choice per block (team-only tabs only exist while a team is selected)
    if (blockId && activeBtn?.dataset.scope !== 'team') {
        browserLocal.set(`coral-agentic-tab-${blockId}`, tabName);
    }

    if (tabName === 'team-settings') window.renderTeamSettingsTab?.();

    if (tabName === 'agent-ui') refreshAgentUI();

    // Saved for later (#4510): the list is read when its tab is shown
    if (tabName === 'saved') importOnce('./saved.js').then(m => m.showSaved()).catch(() => {});

    // Start/stop board task polling based on tab
    if (tabName === 'tasks') {
        startBoardTaskPoll();
    } else if (blockId === 'top') {
        stopBoardTaskPoll();
    }

    // Activate custom view iframe when switching to a custom view tab
    if (tabName.startsWith('custom-view-') && window._activateCustomView) {
        const viewId = parseInt(tabName.replace('custom-view-', ''), 10);
        window._activateCustomView(viewId);
    }

    // Files-viewer sources (browse, artifacts, ...) are top-level tabs
    if (['files', 'browse', 'artifacts', 'team-artifacts', 'knowledge'].includes(tabName)) selectFilesSource(tabName);

    // Refresh changed files when switching to the files tab
    if (tabName === 'files' && state.currentSession && state.currentSession.type === 'live') {
        loadChangedFiles(state.currentSession.name, state.currentSession.session_id);
    }

    // Scroll board chat to bottom when switching to it
    if (tabName === 'board') {
        setTimeout(() => {
            const msgs = document.getElementById('board-panel-msgs');
            if (msgs) msgs.scrollTop = msgs.scrollHeight;
        }, 50);
    }
}

/** Restore persisted tab selection on page load. */
export function restoreAgenticTabs() {
    // Only restore for the single unified top block
    const saved = browserLocal.get('coral-agentic-tab-top');
    if (saved) {
        const tab = document.getElementById(`agentic-tab-${saved}`);
        if (tab && tab.style.display !== 'none') {
            switchAgenticTab(saved, 'top');
        }
    }
    // Clean up stale bottom block preference
    browserLocal.remove('coral-agentic-tab-bottom');
}

// The activity filter's dropdown, for the live view and history alike (#3645): data-on-* names,
// never code. mode is "live" or "history"; each toggle function checks the key itself.
registerActions({
    'filter-dropdown': el => { if (el.dataset.mode === 'live' || el.dataset.mode === 'history') toggleFilterDropdown(el.dataset.mode); },
    'filter-toggle': (el, event) => {
        event.stopPropagation();
        if (el.dataset.mode === 'live') toggleEventFilter(el.dataset.key);
        else if (el.dataset.mode === 'history') window.toggleHistoryEventFilter(el.dataset.key);
    },
    'filter-toggle-all': (el, event) => {
        event.stopPropagation();
        if (el.dataset.mode === 'live') toggleAllEventFilters();
        else if (el.dataset.mode === 'history') window.toggleAllHistoryEventFilters();
    },
});
