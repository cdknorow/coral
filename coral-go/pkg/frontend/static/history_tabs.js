import { historyFetch } from './history_source.js';
/* History tabs — read-only rendering of Activity, Tasks, and Agent Notes for historical sessions */

import { escapeHtml, sanitizeMarkdown } from './utils.js';
import { eventRowHtml, renderActivityChart } from './event_row.js';
import { setHistoryEventsSession, syncHistoryEventSearch } from './history_events_search.js';
import { FILTER_GROUPS, eventVisible, historyEventsView } from './event_icons.js';

// The icon and filter definitions are shared with the live Activity view (event_icons.js)
// Per-session filter state for history
const historyFilterHidden = historyEventsView.hidden;
let historyEvents = [];
let historyTasks = [];
let historyAgentNotes = [];

function isEventVisible(ev) {
    return eventVisible(ev, historyFilterHidden);
}

// ── Activity ──────────────────────────────────────────────────────────────

export async function loadHistoryEvents(sessionId) {
    setHistoryEventsSession(sessionId);
    historyFilterHidden.clear();
    historyEvents = [];
    historyEventsView.events = historyEvents;
    try {
        const resp = await historyFetch(`/api/sessions/history/${encodeURIComponent(sessionId)}/events?limit=200`);
        historyEvents = await resp.json();
    } catch (e) {
        if (e.name === 'AbortError') return;
        historyEvents = [];
    }
    historyEventsView.events = historyEvents;

    const countEl = document.getElementById('history-activity-count');
    if (countEl) countEl.textContent = historyEvents.length > 0 ? historyEvents.length : '';

    renderHistoryActivityChart();
    renderHistoryEventTimeline();
}

function renderHistoryActivityChart() {
    renderActivityChart(document.getElementById('history-activity-chart'), historyEvents, FILTER_GROUPS);
}

function renderHistoryEventTimeline() {
    const container = document.getElementById('history-events-list');
    if (!container) return;

    // A search of the session's events (the bar above the list) shows its results in place of the list
    if (syncHistoryEventSearch(renderHistoryEventTimeline)) return;

    if (historyEvents.length === 0) {
        container.innerHTML = '<div class="event-empty">No activity recorded</div>';
        return;
    }

    const visible = historyEvents.filter(isEventVisible);
    if (visible.length === 0) {
        container.innerHTML = '<div class="event-empty">All events filtered out</div>';
        return;
    }

    container.innerHTML = visible.map(eventRowHtml).join('');
}

// ── Tasks ─────────────────────────────────────────────────────────────────

export async function loadHistoryTasks(sessionId) {
    historyTasks = [];
    try {
        const resp = await historyFetch(`/api/sessions/history/${encodeURIComponent(sessionId)}/tasks`);
        historyTasks = await resp.json();
    } catch (e) {
        if (e.name === 'AbortError') return;
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
        const resp = await historyFetch(`/api/sessions/history/${encodeURIComponent(sessionId)}/agent-notes`);
        historyAgentNotes = await resp.json();
    } catch (e) {
        if (e.name === 'AbortError') return;
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
