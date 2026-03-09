/* Agentic State — event loading, timeline rendering, tab switching, filtering */

import { state } from './state.js';
import { startLiveHistoryPoll, stopLiveHistoryPoll } from './live_chat.js';

const TOOL_ICONS = {
    Read:       { char: '&#xe8f4;', cls: 'tool-read' },
    Write:      { char: '&#xe3c9;', cls: 'tool-write' },
    Edit:       { char: '&#xe3c9;', cls: 'tool-edit' },
    Bash:       { char: '&#xeb8e;', cls: 'tool-bash' },
    Grep:       { char: '&#xe8b6;', cls: 'tool-grep' },
    Glob:       { char: '&#xe2c8;', cls: 'tool-glob' },
    WebFetch:   { char: '&#xe894;', cls: 'tool-web' },
    WebSearch:  { char: '&#xf02e;', cls: 'tool-web' },
    TaskCreate: { char: '&#xe145;', cls: 'tool-task' },
    TaskUpdate: { char: '&#xe8d5;', cls: 'tool-task' },
    TaskList:   { char: '&#xe8ef;', cls: 'tool-task' },
    TaskGet:    { char: '&#xe8ef;', cls: 'tool-task' },
    Task:       { char: '&#xea21;', cls: 'tool-agent' },
};

// Filter groups — maps display label to the set of tool_names/event_types it covers
const FILTER_GROUPS = [
    { key: 'read',   label: '&#xe8f4;', title: 'Read',          cls: 'tool-read',   match: ev => ev.tool_name === 'Read' },
    { key: 'write',  label: '&#xe3c9;', title: 'Write',         cls: 'tool-write',  match: ev => ev.tool_name === 'Write' },
    { key: 'edit',   label: '&#xe3c9;', title: 'Edit',          cls: 'tool-edit',   match: ev => ev.tool_name === 'Edit' },
    { key: 'bash',   label: '&#xeb8e;', title: 'Bash',          cls: 'tool-bash',   match: ev => ev.tool_name === 'Bash' },
    { key: 'search', label: '&#xe8b6;', title: 'Grep / Glob',   cls: 'tool-grep',   match: ev => ev.tool_name === 'Grep' || ev.tool_name === 'Glob' },
    { key: 'web',    label: '&#xe894;', title: 'Web',           cls: 'tool-web',    match: ev => ev.tool_name === 'WebFetch' || ev.tool_name === 'WebSearch' },
    { key: 'task',   label: '&#xe8ef;', title: 'Tasks',         cls: 'tool-task',   match: ev => ['TaskCreate','TaskUpdate','TaskList','TaskGet'].includes(ev.tool_name) },
    { key: 'agent',  label: '&#xea21;', title: 'Subagents',     cls: 'tool-agent',  match: ev => ev.tool_name === 'Task' },
    { key: 'status',     label: '&#xe8b8;', title: 'Status',          cls: 'tool-status',     match: ev => ev.event_type === 'status' },
    { key: 'goal',       label: '&#xe153;', title: 'Goal',            cls: 'tool-goal',       match: ev => ev.event_type === 'goal' },
    { key: 'confidence', label: '&#xe8e8;', title: 'Confidence',      cls: 'tool-confidence', match: ev => ev.event_type === 'confidence' },
    { key: 'system',     label: '&#xe002;', title: 'Stop / Notify',   cls: 'tool-stop',       match: ev => ev.event_type === 'stop' || ev.event_type === 'notification' },
    { key: 'pulse',      label: '&#xe87e;', title: 'Pulse (other)',   cls: 'tool-pulse',      match: ev => ev.event_type && !ev.tool_name && !['status','goal','confidence','stop','notification'].includes(ev.event_type) },
];

// Hidden filters are persisted per-session in state
// state.eventFiltersHidden = Set of filter keys that are hidden

function getHiddenFilters() {
    if (!state.eventFiltersHidden) {
        state.eventFiltersHidden = new Set();
    }
    return state.eventFiltersHidden;
}

// Known pulse event types — add new PULSE:<TYPE> entries here
const PULSE_ICONS = {
    status:       { char: '&#xe8b8;', cls: 'tool-status',     title: 'Status' },
    goal:         { char: '&#xe153;', cls: 'tool-goal',       title: 'Goal' },
    confidence:   { char: '&#xe8e8;', cls: 'tool-confidence', title: 'Confidence' },
    stop:         { char: '&#xe002;', cls: 'tool-stop',       title: 'Stop' },
    notification: { char: '&#xe7f4;', cls: 'tool-notification', title: 'Notification' },
};

function getToolIcon(toolName, eventType) {
    // Check pulse event types first
    const pulse = PULSE_ICONS[eventType];
    if (pulse) return { ...pulse };
    // Tool-based events
    const icon = TOOL_ICONS[toolName];
    if (icon) return { ...icon, title: toolName };
    // Generic fallback for unknown pulse event types
    if (eventType && !toolName) {
        const label = eventType.charAt(0).toUpperCase();
        const title = eventType.charAt(0).toUpperCase() + eventType.slice(1);
        return { char: '&#xe87e;', cls: 'tool-pulse', title };
    }
    return { char: '&#xe061;', cls: 'tool-default', title: eventType || 'Event' };
}

function formatTime(isoStr) {
    if (!isoStr) return '';
    try {
        const d = new Date(isoStr);
        return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    } catch {
        return '';
    }
}

function escapeAttr(str) {
    return (str || '').replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

function escapeHtml(str) {
    const div = document.createElement('div');
    div.textContent = str;
    return div.innerHTML;
}

function isEventVisible(ev) {
    const hidden = getHiddenFilters();
    if (hidden.size === 0) return true;
    for (const group of FILTER_GROUPS) {
        if (hidden.has(group.key) && group.match(ev)) return false;
    }
    return true;
}

export async function loadAgentEvents(agentName, sessionId) {
    if (!agentName) return;
    try {
        const params = new URLSearchParams({ limit: 50 });
        const sid = sessionId || (state.currentSession && state.currentSession.session_id);
        if (sid) params.set("session_id", sid);
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(agentName)}/events?${params}`);
        state.currentAgentEvents = await resp.json();
    } catch (e) {
        state.currentAgentEvents = [];
    }
    renderEventTimeline();
}

export function renderEventFilters() {
    const container = document.getElementById('event-filters');
    if (!container) return;

    const hidden = getHiddenFilters();
    const events = state.currentAgentEvents || [];
    const activeCount = FILTER_GROUPS.length - hidden.size;
    const badge = hidden.size > 0 ? `<span class="filter-btn-badge">${activeCount}/${FILTER_GROUPS.length}</span>` : '';

    container.innerHTML = `<button class="filter-dropdown-btn" onclick="toggleFilterDropdown('live')">
        <span class="event-filter-char">&#xe152;</span>
        <span class="filter-btn-label">Filter</span>
        ${badge}
    </button>`;
}

// Track which mode the dropdown is open for
let _filterDropdownMode = null;

export function toggleFilterDropdown(mode) {
    const existing = document.getElementById('filter-dropdown');
    if (existing) {
        existing.remove();
        _filterDropdownMode = null;
        return;
    }
    _openFilterDropdown(mode);
}

function _openFilterDropdown(mode) {
    _filterDropdownMode = mode;
    const isLive = mode === 'live';
    const hidden = isLive ? getHiddenFilters() : window.historyFilterHiddenRef;
    const events = isLive ? (state.currentAgentEvents || []) : null;
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
        const toggleFn = isLive ? 'toggleEventFilter' : 'toggleHistoryEventFilter';
        return `<div class="filter-dropdown-item ${group.cls}" onclick="event.stopPropagation(); ${toggleFn}('${group.key}')">
            <span class="filter-item-icon event-filter-char">${group.label}</span>
            <span class="filter-item-label">${group.title}</span>
            ${countHtml}
            <span class="filter-item-check">${checked ? '&#xe834;' : '&#xe835;'}</span>
        </div>`;
    }).join('');

    const toggleAllFn = isLive ? 'toggleAllEventFilters' : 'toggleAllHistoryEventFilters';
    dropdown.innerHTML = `
        <div class="filter-dropdown-header">
            <span class="filter-dropdown-title">Filter Events</span>
            <button class="filter-dropdown-toggle" onclick="event.stopPropagation(); ${toggleAllFn}()">Toggle All</button>
        </div>
        <div class="filter-dropdown-list">${items}</div>
    `;

    const rect = btn.getBoundingClientRect();
    dropdown.style.position = 'fixed';
    dropdown.style.top = (rect.bottom + 4) + 'px';
    dropdown.style.left = rect.left + 'px';

    document.body.appendChild(dropdown);

    // Close on outside click
    setTimeout(() => {
        const closeHandler = (e) => {
            const dd = document.getElementById('filter-dropdown');
            if (!dd || (!dd.contains(e.target) && !btn.contains(e.target))) {
                if (dd) dd.remove();
                _filterDropdownMode = null;
                document.removeEventListener('click', closeHandler);
            }
        };
        document.addEventListener('click', closeHandler);
    }, 0);
}

export function toggleEventFilter(key) {
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
    const hidden = isLive ? getHiddenFilters() : window.historyFilterHiddenRef;
    const events = isLive ? (state.currentAgentEvents || []) : null;

    const items = FILTER_GROUPS.map(group => {
        const isHidden = hidden ? hidden.has(group.key) : false;
        const checked = !isHidden;
        const count = events ? events.filter(group.match).length : '';
        const countHtml = count !== '' ? `<span class="filter-item-count">${count}</span>` : '';
        const toggleFn = isLive ? 'toggleEventFilter' : 'toggleHistoryEventFilter';
        return `<div class="filter-dropdown-item ${group.cls}" onclick="event.stopPropagation(); ${toggleFn}('${group.key}')">
            <span class="filter-item-icon event-filter-char">${group.label}</span>
            <span class="filter-item-label">${group.title}</span>
            ${countHtml}
            <span class="filter-item-check">${checked ? '&#xe834;' : '&#xe835;'}</span>
        </div>`;
    }).join('');

    const list = dropdown.querySelector('.filter-dropdown-list');
    if (list) list.innerHTML = items;
}

export function renderEventTimeline() {
    const container = document.getElementById('agentic-state-events');
    if (!container) return;

    const events = state.currentAgentEvents || [];

    // Also update filter chips (counts may have changed)
    renderEventFilters();

    if (events.length === 0) {
        container.innerHTML = '<div class="event-empty">No activity yet</div>';
        return;
    }

    const visible = events.filter(isEventVisible);
    if (visible.length === 0) {
        container.innerHTML = '<div class="event-empty">All events filtered out</div>';
        return;
    }

    // Events come newest-first from API, display newest at top
    container.innerHTML = visible.map(ev => {
        const icon = getToolIcon(ev.tool_name, ev.event_type);
        const typeCls = ev.event_type ? `event-type-${ev.event_type}` : '';
        return `<div class="event-item ${typeCls}" data-tooltip="${icon.title}: ${escapeAttr(ev.summary)}">
            <span class="event-icon ${icon.cls}">${icon.char}</span>
            <span class="event-body">
                <span class="event-summary">${escapeHtml(ev.summary)}</span>
            </span>
            <span class="event-time">${formatTime(ev.created_at)}</span>
        </div>`;
    }).join('');

    renderLiveActivityChart();
}

function renderLiveActivityChart() {
    const container = document.getElementById('live-activity-chart');
    if (!container) return;

    const events = state.currentAgentEvents || [];
    if (events.length === 0) {
        container.innerHTML = '';
        return;
    }

    const counts = [];
    const matched = new Set();

    for (const group of FILTER_GROUPS) {
        let groupCount = 0;
        events.forEach((ev, idx) => {
            if (group.match(ev)) {
                groupCount++;
                matched.add(idx);
            }
        });
        if (groupCount > 0) {
            counts.push({ key: group.key, label: group.title, cls: group.cls, count: groupCount });
        }
    }

    const unmatchedCount = events.length - matched.size;
    if (unmatchedCount > 0) {
        counts.push({ key: 'other', label: 'Other', cls: 'tool-default', count: unmatchedCount });
    }

    if (counts.length === 0) {
        container.innerHTML = '';
        return;
    }

    counts.sort((a, b) => b.count - a.count);
    const maxCount = counts[0].count;

    container.innerHTML = counts.map(item => {
        const pct = Math.max(2, (item.count / maxCount) * 100);
        return `<div class="activity-chart-row">
            <span class="activity-chart-label">${escapeHtml(item.label)}</span>
            <div class="activity-chart-bar-track">
                <div class="activity-chart-bar ${item.cls}" style="width: ${pct}%"></div>
            </div>
            <span class="activity-chart-count">${item.count}</span>
        </div>`;
    }).join('');
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
                <span class="filter-popup-time">${formatTime(ev.created_at)}</span>
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

export function switchAgenticTab(tabName) {
    // Update tab buttons
    document.querySelectorAll('.agentic-tab').forEach(btn => btn.classList.remove('active'));
    const activeBtn = document.getElementById(`agentic-tab-${tabName}`);
    if (activeBtn) activeBtn.classList.add('active');

    // Update panels
    document.querySelectorAll('.agentic-panel').forEach(panel => panel.classList.remove('active'));
    const activePanel = document.getElementById(`agentic-panel-${tabName}`);
    if (activePanel) activePanel.classList.add('active');

    // Start/stop history polling based on tab
    if (tabName === 'history') {
        startLiveHistoryPoll();
    } else {
        stopLiveHistoryPoll();
    }
}
