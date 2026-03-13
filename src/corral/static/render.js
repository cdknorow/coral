/* Rendering functions for session lists, chat history, and status updates */

import { state } from './state.js';
import { escapeHtml } from './utils.js';
import { renderSidebarTagDots } from './tags.js';
import { updateSectionVisibility } from './sidebar.js';

function formatShortTime(isoStr) {
    const d = new Date(isoStr);
    if (isNaN(d)) return "";
    const dd = String(d.getDate()).padStart(2, "0");
    const mm = String(d.getMonth() + 1).padStart(2, "0");
    const yy = String(d.getFullYear()).slice(-2);
    return `${dd}/${mm}/${yy}`;
}

function formatDuration(sec) {
    if (sec == null || sec <= 0) return '';
    if (sec < 60) return `${sec}s`;
    if (sec < 3600) return `${Math.round(sec / 60)}m`;
    const h = Math.floor(sec / 3600);
    const m = Math.round((sec % 3600) / 60);
    return m > 0 ? `${h}h ${m}m` : `${h}h`;
}

function formatStaleness(seconds) {
    if (seconds === null || seconds === undefined) return "Unknown";
    if (seconds < 5) return "Just now";
    if (seconds < 60) return `${Math.floor(seconds)}s ago`;
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
    return `${Math.floor(seconds / 3600)}h ago`;
}

function getStateLabel(waitingForInput, working, staleness, waitingReason) {
    if (waitingForInput) {
        return waitingReason === "stop" ? "Done" : "Needs input";
    }
    if (working) return "Working";
    if (staleness !== null && staleness !== undefined && staleness < 60) return "Active";
    return "Idle";
}

function buildSessionTooltip(s) {
    const stateLabel = getStateLabel(s.waiting_for_input, s.working, s.staleness_seconds, s.waiting_reason);
    const lastAction = formatStaleness(s.staleness_seconds);
    const goal = s.summary || "No goal set";
    const status = s.status || "No status";
    const branch = s.branch || "—";
    const agent = s.agent_type || "claude";

    let rows = [
        `<tr><td class="tt-label">State</td><td class="tt-value tt-state-${stateLabel.toLowerCase().replace(/ /g, '-')}">${escapeHtml(stateLabel)}</td></tr>`,
        `<tr><td class="tt-label">Last action</td><td class="tt-value">${escapeHtml(lastAction)}</td></tr>`,
        `<tr><td class="tt-label">Goal</td><td class="tt-value">${escapeHtml(goal)}</td></tr>`,
        `<tr><td class="tt-label">Status</td><td class="tt-value">${escapeHtml(status)}</td></tr>`,
        `<tr><td class="tt-label">Branch</td><td class="tt-value">${escapeHtml(branch)}</td></tr>`,
        `<tr><td class="tt-label">Agent</td><td class="tt-value">${escapeHtml(agent)}</td></tr>`,
    ];
    return `<table class="session-tooltip-table">${rows.join("")}</table>`;
}

function getDotClass(staleness, waitingForInput, working, waitingReason) {
    if (waitingForInput) return waitingReason === "stop" ? "done" : "waiting";
    if (working) return "working";
    if (staleness === null || staleness === undefined) return "stale";
    if (staleness < 60) return "active";
    return "stale";
}

export function renderLiveSessions(sessions) {
    const list = document.getElementById("live-sessions-list");

    updateSectionVisibility('live-sessions', sessions.length);

    if (!sessions.length) {
        list.innerHTML = '<li class="empty-state">No live sessions</li>';
        return;
    }

    // Group sessions by agent_name (folder)
    const groups = {};
    for (const s of sessions) {
        const key = s.name || "unknown";
        if (!groups[key]) groups[key] = [];
        groups[key].push(s);
    }

    let html = "";
    for (const [groupName, groupSessions] of Object.entries(groups)) {
        const isMulti = groupSessions.length > 1;
        const countBadge = isMulti ? ` <span class="session-group-count">${groupSessions.length}</span>` : "";
        html += `<li class="session-group-header">${escapeHtml(groupName)}${countBadge}</li>`;

        for (const s of groupSessions) {
            const dotClass = getDotClass(s.staleness_seconds, s.waiting_for_input, s.working, s.waiting_reason);
            const isActive = state.currentSession && state.currentSession.type === "live" && state.currentSession.session_id === s.session_id;
            const typeTag = s.agent_type && s.agent_type !== "claude" ? ` <span class="badge ${escapeHtml(s.agent_type)}">${escapeHtml(s.agent_type)}</span>` : "";
            const branchTag = s.branch ? ` <span class="sidebar-branch">${escapeHtml(s.branch)}</span>` : "";
            const waitingBadge = s.waiting_for_input
                ? (s.waiting_reason === "stop"
                    ? ' <span class="badge done-badge">Done</span>'
                    : ' <span class="badge waiting-badge">Needs input</span>')
                : '';
            const goal = s.summary ? escapeHtml(s.summary) : "No goal set";
            const displayLabel = s.display_name || "Agent";
            const sid = s.session_id ? escapeHtml(s.session_id) : "";
            const editBtn = `<button class="sidebar-edit-btn" onclick="event.stopPropagation(); renameAgent('${escapeHtml(s.name)}', '${escapeHtml(s.agent_type)}', '${sid}')" title="Rename agent">&#x270E;</button>`;
            const tooltip = buildSessionTooltip(s);
            html += `<li class="session-group-item${isActive ? ' active' : ''}" onclick="selectLiveSession('${escapeHtml(s.name)}', '${escapeHtml(s.agent_type)}', '${sid}')">
                <span class="session-dot ${dotClass}"></span>
                <div class="session-info">
                    <div class="session-name-row">
                        <span class="session-label">${escapeHtml(displayLabel)}${typeTag}${waitingBadge}</span>
                        ${editBtn}
                    </div>
                    <span class="session-goal">${goal}</span>
                    ${branchTag ? `<span class="session-branch-row">${branchTag}</span>` : ''}
                </div>
                <div class="session-tooltip">${tooltip}</div>
            </li>`;
        }
    }

    list.innerHTML = html;

    // Attach hover listeners for fixed-position tooltips
    for (const item of list.querySelectorAll(".session-group-item")) {
        const tip = item.querySelector(".session-tooltip");
        if (!tip) continue;
        item.addEventListener("mouseenter", () => {
            const rect = item.getBoundingClientRect();
            tip.style.left = rect.right + 8 + "px";
            tip.style.top = rect.top + "px";
            tip.style.display = "block";
        });
        item.addEventListener("mouseleave", () => {
            tip.style.display = "none";
        });
    }
}

export function renderHistorySessions(sessions, total, page, pageSize) {
    const list = document.getElementById("history-sessions-list");
    state.historySessionsList = sessions || [];

    updateSectionVisibility('history', total || (sessions ? sessions.length : 0));

    if (!sessions || !sessions.length) {
        list.innerHTML = '<li class="empty-state">No history found</li>';
        renderPaginationControls(0, 1, 50);
        return;
    }

    list.innerHTML = sessions.map(s => {
        const label = s.summary_title || s.summary || s.session_id;
        const truncated = label.length > 40 ? label.substring(0, 40) + "..." : label;
        const isActive = state.currentSession && state.currentSession.type === "history" && state.currentSession.name === s.session_id;
        const typeTag = s.source_type === "gemini" ? '<span class="badge gemini">gemini</span>' : "";
        const branchTag = s.branch ? `<span class="sidebar-branch">${escapeHtml(s.branch)}</span>` : "";
        const tagDots = s.tags ? renderSidebarTagDots(s.tags) : "";
        const timeStr = s.last_timestamp ? formatShortTime(s.last_timestamp) : "";
        const timeTag = timeStr ? `<span class="session-time">${escapeHtml(timeStr)}</span>` : "";
        const durStr = s.duration_sec != null ? formatDuration(s.duration_sec) : '';
        const durTag = durStr ? `<span class="session-dur">${escapeHtml(durStr)}</span>` : '';
        return `<li class="${isActive ? 'active' : ''}" onclick="selectHistorySession('${escapeHtml(s.session_id)}')">
            <div class="session-row-top">${timeTag}${durTag}${typeTag}${tagDots}</div>
            <div class="session-row-mid"><span class="session-label" title="${escapeHtml(label)}">${escapeHtml(truncated)}</span></div>
            ${branchTag ? `<div class="session-row-bottom">${branchTag}</div>` : ""}
        </li>`;
    }).join("");

    if (total !== undefined) {
        renderPaginationControls(total, page || 1, pageSize || 50);
    }
}

export function renderPaginationControls(total, page, pageSize) {
    let container = document.getElementById("history-pagination");
    if (!container) {
        // Create pagination container after the history list
        const list = document.getElementById("history-sessions-list");
        container = document.createElement("div");
        container.id = "history-pagination";
        container.className = "pagination-controls";
        list.parentNode.appendChild(container);
    }

    const totalPages = Math.max(1, Math.ceil(total / pageSize));
    if (totalPages <= 1) {
        container.innerHTML = `<span class="pagination-info">${total} session${total !== 1 ? 's' : ''}</span>`;
        return;
    }

    container.innerHTML = `
        <button class="btn btn-small" ${page <= 1 ? 'disabled' : ''} onclick="loadHistoryPage(${page - 1})">Prev</button>
        <span class="pagination-info">${page} / ${totalPages} (${total})</span>
        <button class="btn btn-small" ${page >= totalPages ? 'disabled' : ''} onclick="loadHistoryPage(${page + 1})">Next</button>
    `;
}

function stripPulseLines(text) {
    return text.replace(/^\|\|PULSE:(STATUS|SUMMARY|CONFIDENCE)\s[^\|]*\|\|$/gm, '').replace(/\n{3,}/g, '\n\n');
}

export function renderHistoryChat(messages) {
    const container = document.getElementById("history-messages");
    container.innerHTML = "";

    for (const entry of messages) {
        const type = entry.type || "unknown";
        const msg = entry.message || {};
        let content = "";

        if (typeof msg.content === "string") {
            content = msg.content;
        } else if (Array.isArray(msg.content)) {
            content = msg.content
                .filter(b => b.type === "text")
                .map(b => b.text)
                .join("\n");
        }

        if (!content.trim()) continue;

        const isHuman = type === "human" || type === "user";
        const bubbleClass = isHuman ? "human" : "assistant";
        const roleLabel = isHuman ? "You" : "Assistant";

        const bubble = document.createElement("div");
        bubble.className = `chat-bubble ${bubbleClass}`;

        let messageHtml;
        if (isHuman) {
            messageHtml = escapeHtml(content);
        } else {
            const cleaned = stripPulseLines(content);
            messageHtml = marked.parse(cleaned);
        }

        bubble.innerHTML = `
            <div class="role-label">${roleLabel}</div>
            <div class="message-text${!isHuman ? " markdown-body" : ""}">${messageHtml}</div>
            ${isHuman ? `<button class="edit-btn" onclick="editAndResubmit(this)">Edit & Resubmit</button>` : ""}
        `;
        container.appendChild(bubble);
    }

    container.scrollTop = container.scrollHeight;
}

export function updateSessionStatus(status) {
    const el = document.getElementById("session-status");
    if (status) {
        el.querySelector(".status-text").textContent = status;
    }
}

export function updateSessionBranch(branch) {
    const el = document.getElementById("session-branch");
    if (branch) {
        el.querySelector(".branch-text").textContent = branch;
        el.style.display = "";
    } else {
        el.style.display = "none";
    }
}

export function updateWaitingIndicator(waiting, working, waitingReason) {
    const dot = document.getElementById("session-status-dot");
    const banner = document.getElementById("waiting-banner");
    const isDone = waiting && waitingReason === "stop";
    if (dot) {
        dot.classList.toggle("waiting", !!waiting && !isDone);
        dot.classList.toggle("done", isDone);
        dot.classList.toggle("working", !!working);
    }
    if (banner) {
        banner.style.display = waiting ? "" : "none";
        if (waiting) {
            banner.className = isDone ? "waiting-banner done-banner" : "waiting-banner";
            banner.textContent = isDone ? "✅ Agent is done" : "⏳ Agent is waiting for input";
        }
    }
}

export function updateSessionSummary(summary) {
    const el = document.getElementById("session-summary");
    if (!el) return;
    if (summary) {
        el.querySelector(".summary-text").textContent = summary;
        el.style.display = "";
    }
    // Don't hide — a null summary from a WebSocket tick shouldn't
    // clear a previously-known goal. The log parser may simply not
    // have found the PULSE:SUMMARY line in the current chunk.
}
