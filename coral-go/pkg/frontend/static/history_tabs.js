/* History tabs — read-only rendering of Activity, Tasks, and Agent Notes for historical sessions */

import { state } from './state.js';
import { escapeHtml, escapeAttr, sanitizeMarkdown } from './utils.js';
import { durationChip, eventDetailText, eventFailed, renderActivityChart } from './event_row.js';
import { formatClockTime } from './format.js';
import { FILTER_GROUPS, getToolIcon, eventVisible } from './event_icons.js';

// The icon and filter definitions are shared with the live Activity view (event_icons.js)
// Per-session filter state for history
let historyFilterHidden = new Set();
let historyEvents = [];
let historyTasks = [];
let historyAgentNotes = [];

function isEventVisible(ev) {
    return eventVisible(ev, historyFilterHidden);
}

// ── Activity ──────────────────────────────────────────────────────────────

export async function loadHistoryEvents(sessionId) {
    historyFilterHidden = new Set();
    historyEvents = [];
    try {
        const resp = await fetch(`/api/sessions/history/${encodeURIComponent(sessionId)}/events?limit=200`);
        historyEvents = await resp.json();
    } catch (e) {
        historyEvents = [];
    }

    const countEl = document.getElementById('history-activity-count');
    if (countEl) countEl.textContent = historyEvents.length > 0 ? historyEvents.length : '';

    renderHistoryActivityChart();
    renderHistoryEventFilters();
    renderHistoryEventTimeline();
}

function renderHistoryActivityChart() {
    renderActivityChart(document.getElementById('history-activity-chart'), historyEvents, FILTER_GROUPS);
}

function renderHistoryEventFilters() {
    const container = document.getElementById('history-event-filters');
    if (!container) return;

    const activeCount = FILTER_GROUPS.length - historyFilterHidden.size;
    const badge = historyFilterHidden.size > 0 ? `<span class="filter-btn-badge">${activeCount}/${FILTER_GROUPS.length}</span>` : '';

    container.innerHTML = `<button class="filter-dropdown-btn" data-on-click="filter-dropdown" data-mode="history">
        <span class="event-filter-char">&#xe152;</span>
        <span class="filter-btn-label">Filter</span>
        ${badge}
    </button>`;
}

// Expose historyFilterHidden for the shared dropdown in agentic_state.js
window.historyFilterHiddenRef = historyFilterHidden;

export function toggleHistoryEventFilter(key) {
    if (!FILTER_GROUPS.some(g => g.key === key)) return; // only a filter this list has
    if (historyFilterHidden.has(key)) {
        historyFilterHidden.delete(key);
    } else {
        historyFilterHidden.add(key);
    }
    _refreshHistoryFilterDropdown();
    renderHistoryEventFilters();
    renderHistoryEventTimeline();
}

export function toggleAllHistoryEventFilters() {
    if (historyFilterHidden.size === 0) {
        for (const group of FILTER_GROUPS) historyFilterHidden.add(group.key);
    } else {
        historyFilterHidden.clear();
    }
    _refreshHistoryFilterDropdown();
    renderHistoryEventFilters();
    renderHistoryEventTimeline();
}

function _refreshHistoryFilterDropdown() {
    const dropdown = document.getElementById('filter-dropdown');
    if (!dropdown) return;
    // Re-render dropdown items in place
    const items = FILTER_GROUPS.map(group => {
        const isHidden = historyFilterHidden.has(group.key);
        const checked = !isHidden;
        const count = historyEvents.filter(group.match).length;
        const countHtml = `<span class="filter-item-count">${count}</span>`;
        return `<div class="filter-dropdown-item ${escapeAttr(group.cls)}" data-on-click="filter-toggle" data-mode="history" data-key="${escapeAttr(group.key)}">
            <span class="filter-item-icon event-filter-char">${group.label}</span>
            <span class="filter-item-label">${group.title}</span>
            ${countHtml}
            <span class="filter-item-check">${checked ? '&#xe834;' : '&#xe835;'}</span>
        </div>`;
    }).join('');

    const list = dropdown.querySelector('.filter-dropdown-list');
    if (list) list.innerHTML = items;
}

function renderHistoryEventTimeline() {
    const container = document.getElementById('history-events-list');
    if (!container) return;

    if (historyEvents.length === 0) {
        container.innerHTML = '<div class="event-empty">No activity recorded</div>';
        return;
    }

    const visible = historyEvents.filter(isEventVisible);
    if (visible.length === 0) {
        container.innerHTML = '<div class="event-empty">All events filtered out</div>';
        return;
    }

    container.innerHTML = visible.map(ev => {
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
    }).join('');
}

// ── Tasks ─────────────────────────────────────────────────────────────────

export async function loadHistoryTasks(sessionId) {
    historyTasks = [];
    try {
        const resp = await fetch(`/api/sessions/history/${encodeURIComponent(sessionId)}/tasks`);
        historyTasks = await resp.json();
    } catch (e) {
        historyTasks = [];
    }

    const countEl = document.getElementById('history-tasks-count');
    if (countEl) {
        const doneCount = historyTasks.filter(t => t.completed === 1).length;
        countEl.textContent = historyTasks.length > 0 ? `${doneCount}/${historyTasks.length}` : '';
    }

    renderHistoryTaskList();
}

function renderHistoryTaskList() {
    const list = document.getElementById('history-task-list');
    if (!list) return;

    if (historyTasks.length === 0) {
        list.innerHTML = '<div class="task-empty">No tasks recorded</div>';
        return;
    }

    list.innerHTML = historyTasks.map(t => {
        const statusClass = t.completed === 1 ? 'completed' : t.completed === 2 ? 'in-progress' : '';
        const icon = t.completed === 2
            ? '<span class="task-spinner" title="In progress"></span>'
            : `<input type="checkbox" class="task-checkbox" ${t.completed === 1 ? 'checked' : ''} disabled>`;
        return `
        <div class="task-item ${statusClass}">
            ${icon}
            <span class="task-title">${escapeHtml(t.title)}</span>
        </div>`;
    }).join('');
}

// ── Agent Notes ───────────────────────────────────────────────────────────

export async function loadHistoryAgentNotes(sessionId) {
    historyAgentNotes = [];
    try {
        const resp = await fetch(`/api/sessions/history/${encodeURIComponent(sessionId)}/agent-notes`);
        historyAgentNotes = await resp.json();
    } catch (e) {
        historyAgentNotes = [];
    }

    const countEl = document.getElementById('history-agent-notes-count');
    if (countEl) countEl.textContent = historyAgentNotes.length > 0 ? historyAgentNotes.length : '';

    renderHistoryAgentNotes();
}

function renderHistoryAgentNotes() {
    const container = document.getElementById('history-note-list');
    if (!container) return;

    if (historyAgentNotes.length === 0) {
        container.innerHTML = '<div class="empty-notes">No agent notes recorded</div>';
        return;
    }

    const md = historyAgentNotes.map(n => n.content).join('\n\n');
    if (typeof marked !== 'undefined') {
        container.innerHTML = sanitizeMarkdown(md, undefined, 'board');
    } else {
        container.textContent = md;
    }
}
