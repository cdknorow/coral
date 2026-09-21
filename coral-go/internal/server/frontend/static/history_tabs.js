/* History tabs — read-only rendering of Activity, Tasks, and Agent Notes for historical sessions */

import { state } from './state.js';
import { escapeHtml, escapeAttr } from './utils.js';
import { durationChip, eventDetailText, eventFailed, renderActivityChart } from './event_row.js';

// Re-use tool icon/filter definitions from agentic_state.js
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

const FILTER_GROUPS = [
    { key: 'read',   label: '&#xe8f4;', title: 'Read',          cls: 'tool-read',   match: ev => ev.tool_name === 'Read' },
    { key: 'write',  label: '&#xe3c9;', title: 'Write',         cls: 'tool-write',  match: ev => ev.tool_name === 'Write' },
    { key: 'edit',   label: '&#xe3c9;', title: 'Edit',          cls: 'tool-edit',   match: ev => ev.tool_name === 'Edit' },
    { key: 'bash',   label: '&#xeb8e;', title: 'Bash',          cls: 'tool-bash',   match: ev => ev.tool_name === 'Bash' },
    { key: 'search', label: '&#xe8b6;', title: 'Grep / Glob',   cls: 'tool-grep',   match: ev => ev.tool_name === 'Grep' || ev.tool_name === 'Glob' },
    { key: 'web',    label: '&#xe894;', title: 'Web',           cls: 'tool-web',    match: ev => ev.tool_name === 'WebFetch' || ev.tool_name === 'WebSearch' },
    { key: 'task',   label: '&#xe8ef;', title: 'Tasks',         cls: 'tool-task',   match: ev => ['TaskCreate','TaskUpdate','TaskList','TaskGet'].includes(ev.tool_name) },
    { key: 'agent',  label: '&#xea21;', title: 'Subagents',     cls: 'tool-agent',  match: ev => ev.tool_name === 'Task' },
    { key: 'thinking',   label: '&#xea4a;', title: 'Thinking',        cls: 'tool-thinking',   match: ev => ev.event_type === 'thinking' },
    { key: 'status',     label: '&#xe8b8;', title: 'Status',          cls: 'tool-status',     match: ev => ev.event_type === 'status' },
    { key: 'goal',       label: '&#xe153;', title: 'Goal',            cls: 'tool-goal',       match: ev => ev.event_type === 'goal' },
    { key: 'confidence', label: '&#xe8e8;', title: 'Confidence',      cls: 'tool-confidence', match: ev => ev.event_type === 'confidence' },
    { key: 'system',     label: '&#xe002;', title: 'Stop / Notify',   cls: 'tool-stop',       match: ev => ev.event_type === 'stop' || ev.event_type === 'notification' },
    { key: 'pulse',      label: '&#xe87e;', title: 'Pulse (other)',   cls: 'tool-pulse',      match: ev => ev.event_type && !ev.tool_name && !['thinking','status','goal','confidence','stop','notification'].includes(ev.event_type) },
];

// Per-session filter state for history
let historyFilterHidden = new Set();
let historyEvents = [];
let historyTasks = [];
let historyAgentNotes = [];

function formatTime(isoStr) {
    if (!isoStr) return '';
    try {
        const d = new Date(isoStr);
        return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    } catch {
        return '';
    }
}

// Known pulse event types — add new PULSE:<TYPE> entries here
const PULSE_ICONS = {
    thinking:     { char: '&#xea4a;', cls: 'tool-thinking',   title: 'Thinking' },
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
    return { char: '&#xe061;', cls: 'tool-default', title: toolName || eventType || 'Event' };
}

function isEventVisible(ev) {
    if (historyFilterHidden.size === 0) return true;
    for (const group of FILTER_GROUPS) {
        if (historyFilterHidden.has(group.key) && group.match(ev)) return false;
    }
    return true;
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

    container.innerHTML = `<button class="filter-dropdown-btn" onclick="toggleFilterDropdown('history')">
        <span class="event-filter-char">&#xe152;</span>
        <span class="filter-btn-label">Filter</span>
        ${badge}
    </button>`;
}

// Expose historyFilterHidden for the shared dropdown in agentic_state.js
window.historyFilterHiddenRef = historyFilterHidden;

export function toggleHistoryEventFilter(key) {
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
        return `<div class="filter-dropdown-item ${group.cls}" onclick="event.stopPropagation(); toggleHistoryEventFilter('${group.key}')">
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
        return `<div class="event-item ${typeCls}" data-tooltip="${icon.title === ev.summary ? '' : escapeAttr(icon.title) + ': '}${escapeAttr(ev.summary)}${extra ? escapeAttr(' (' + extra + ')') : ''}">
            <span class="event-icon ${icon.cls}">${icon.char}</span>
            <span class="event-body">
                <span class="event-summary">${escapeHtml(ev.summary)}</span>
            </span>
            ${durationChip(ev)}
            <span class="event-time">${formatTime(ev.created_at)}</span>
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
        container.innerHTML = typeof DOMPurify !== 'undefined' ? DOMPurify.sanitize(marked.parse(md)) : marked.parse(md);
    } else {
        container.textContent = md;
    }
}
