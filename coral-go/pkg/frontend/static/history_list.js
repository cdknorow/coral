/* The Chats list and a history transcript (moved out of render.js, #4996; no behaviour change). */
import { historyServer } from './history_source.js';
import { identityKey } from './server_base.js';
import { state } from './state.js';
import { scrollBehavior } from './motion.js';
import { escapeHtml, escapeAttr } from './utils.js';
import { renderSidebarTagDots } from './tags.js';
import { updateSectionVisibility } from './sidebar.js';
import { getCachedAgentAvailability } from './team_availability.js';
import { whenText } from './when_text.js';
import { renderTranscript } from './live_chat.js';
import { historyLabel } from './history_label.js';


/** The usual branches say nothing: only another one is worth a word in a row. */
const unusualBranch = b => !!b && !/^(main|master|trunk|develop)$/.test(b);
/** The provider as a text badge (never colour alone), only where it is not the usual Claude. */
function providerBadge(src) {
    const t = String(src || '').toLowerCase();
    if (!t || t === 'claude') return '';
    const word = t === 'gemini' || t === 'antigravity' ? 'agy' : t;
    return `<span class="badge agy">${escapeHtml(word)}</span>`;
}
/** How long a chat went on, in words that cannot be taken for an age: "ran 21 min", "ran 2 h 5 min". */
function ranFor(sec) {
    if (sec == null || !(sec >= 0)) return '';
    const m = Math.round(sec / 60);
    if (m < 1) return 'ran under a minute';
    return m < 60 ? `ran ${m} min` : `ran ${Math.floor(m / 60)} h${m % 60 ? ` ${m % 60} min` : ''}`;
}
/** When a chat last spoke, in words ("today 14:32", "yesterday 17:20", "Oct 17", with the year when it is not this one: when_text.js, the same words as every other list), the full value for a tooltip and the time
 *  element, and "ran 21 min" for how long it went on. */
export function chatWhen(iso, durationSec, now = new Date()) {
    const d = new Date(iso);
    if (!iso || isNaN(d)) return null;
    return { day: whenText(iso, now), iso: d.toISOString(), full: d.toLocaleString(), ran: ranFor(durationSec) };
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
        const isGroup = s.type === 'group' || (s.session_id && s.session_id.startsWith('board:'));
        if (isGroup) {
            const projectName = s.title || s.session_id.replace(/^board:/, '');
            const label = projectName;
            const isActive = state.currentSession && state.currentSession.type === "board" && state.currentSession.name === projectName;
            const gwhen = chatWhen(s.updated_at, null);
            const gWhenTag = gwhen ? `<span class="session-time"><time datetime="${escapeAttr(gwhen.iso)}" title="${escapeAttr(gwhen.full)}">${escapeHtml(gwhen.day)}</time></span>` : '';
            const countInfo = s.message_count != null ? `<span class="session-meta">${escapeHtml(`${s.message_count} msgs`)}</span>` : "";
            const subsInfo = s.subscriber_count != null ? `<span class="session-meta">${escapeHtml(`${s.subscriber_count} members`)}</span>` : "";
            // laid out as a chat row (#6961): the title and its Group badge, then one line of facts: how many messages and members, and when
            const gfacts = [countInfo, subsInfo, gWhenTag].filter(Boolean).join('<span class="chat-sep" aria-hidden="true"> · </span>');
            return `<li class="${escapeAttr(isActive ? 'active' : '')}" data-on-click="board-open" data-board="${escapeAttr(identityKey(historyServer(), projectName))}"><button type="button" class="row-btn"${isActive ? ' aria-current="true"' : ''}>
                <div class="session-row-mid chat-row-title"><span class="session-label" title="${escapeHtml(label)}">${escapeHtml(label)}</span><span class="badge board-badge">Group</span></div>
                ${gfacts ? `<div class="session-row-bottom chat-row-facts">${gfacts}</div>` : ""}
            </button></li>`;
        }
        const label = historyLabel(s);
        const isActive = state.currentSession && state.currentSession.type === "history" && state.currentSession.name === s.session_id;
        // A chat is its title, then one line of facts: who, which branch (not the usual one), when, and how long it ran (#6580). The provider is a badge only where it is not Claude.
        const typeTag = providerBadge(s.source_type);
        const agentName = s.display_name || s.agent_name || "";
        const agentTag = agentName ? `<span class="sidebar-agent-name">${escapeHtml(agentName)}</span>` : "";
        const reminderTag = getCachedAgentAvailability(s)?.reminder
            ? `<span class="agent-reminder-icon" title="Periodic reminder active" aria-label="Periodic reminder active">⏰</span>`
            : "";
        const branchTag = unusualBranch(s.branch) ? `<span class="sidebar-branch">${escapeHtml(s.branch)}</span>` : "";
        const tagDots = s.tags ? renderSidebarTagDots(s.tags) : "";
        const when = chatWhen(s.last_timestamp, s.duration_sec);
        const whenTag = when ? `<span class="session-time"><time datetime="${escapeAttr(when.iso)}" title="${escapeAttr(when.full)}">${escapeHtml(when.day)}</time>${when.ran ? `, ${escapeHtml(when.ran)}` : ''}</span>` : '';
        const noWhen = !when ? '<span class="session-time">date unknown</span>' : '';
        const facts = [agentTag, branchTag, whenTag || noWhen].filter(Boolean).join('<span class="chat-sep" aria-hidden="true"> · </span>');
        return `<li class="${escapeAttr(isActive ? 'active' : '')}" data-on-click="history-open" data-session-id="${escapeAttr(s.session_id)}"><button type="button" class="row-btn"${isActive ? ' aria-current="true"' : ''}>
            <div class="session-row-mid chat-row-title"><span class="session-label" title="${escapeHtml(label)}">${escapeHtml(label)}</span>${typeTag}${tagDots}${reminderTag}</div>
            ${facts ? `<div class="session-row-bottom chat-row-facts">${facts}</div>` : ""}
        </button></li>`;
    }).join("");

    if (total !== undefined) {
        renderPaginationControls(total, page || 1, pageSize || 50);
    }
}

function renderPaginationControls(total, page, pageSize) {
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
        container.innerHTML = ''; // the section's heading says the count ("Chats 9"): not a second time here (#6961)
        return;
    }

    container.innerHTML = `
        <button class="btn btn-small" ${page <= 1 ? 'disabled' : ''} data-on-click="history-page" data-page="${Number(page - 1)}">Prev</button>
        <span class="pagination-info">${page} / ${totalPages}</span>
        <button class="btn btn-small" ${page >= totalPages ? 'disabled' : ''} data-on-click="history-page" data-page="${Number(page + 1)}">Next</button>
    `;
}

export function renderHistoryChat(messages, agentType = "claude", baseIndex = 0, focusIndex = null) {
    const container = document.getElementById("history-messages");
    container.innerHTML = "";
    container.append(...renderHistoryNodes(messages, agentType, baseIndex));
    container.scrollTop = container.scrollHeight;
    if (focusIndex != null) {
        requestAnimationFrame(() => {
            const target = container.querySelector(`[data-message-index="${CSS.escape(String(focusIndex))}"]`);
            if (target) {
                target.classList.add('chat-search-target');
                target.scrollIntoView({ block: 'center', behavior: scrollBehavior() });
                target.setAttribute('tabindex', '-1');
                target.focus({ preventScroll: true });
            } else {
                const notice = document.createElement('div');
                notice.className = 'chat-search-status';
                notice.textContent = 'The matching message is no longer available.';
                container.prepend(notice);
            }
        });
    }
}

/** A page of the history transcript as nodes, not yet in the page (history_pages.js inserts
 *  pages above or below what is shown, #3718). Same renderer as the live Chat view: replies as
 *  prose, tool calls and their output folded into collapsed "N steps" groups. baseIndex is the
 *  first message's index in the whole transcript. */
export function renderHistoryNodes(messages, agentType = "claude", baseIndex = 0) {
    const holder = document.createElement("div");
    const normalized = messages.map((entry, index) => {
        const value = normalizeHistoryEntry(entry);
        if (value) value.__coralMessageIndex = baseIndex + index;
        return value;
    }).filter(Boolean);
    renderTranscript(normalized, holder, agentType);
    for (const bubble of holder.querySelectorAll(".chat-bubble.human")) {
        bubble.insertAdjacentHTML("beforeend",
            `<button class="edit-btn" data-history-local-only data-on-click="msg-edit-resubmit">Edit & Resubmit</button>`);
    }
    return [...holder.childNodes];
}

// The reader returns normalized entries; older raw JSONL entries (content
// nested under entry.message) are reduced to plain user/assistant text.
function normalizeHistoryEntry(entry) {
    if (entry.message) {
        const msg = entry.message;
        let content = "";
        if (typeof msg.content === "string") content = msg.content;
        else if (typeof msg.text === "string") content = msg.text;
        else if (Array.isArray(msg.content)) {
            content = msg.content.filter(b => b.type === "text").map(b => b.text).join("\n");
        }
        if (!content.trim()) return null;
        entry = (entry.type === "human" || entry.type === "user")
            ? { type: "user", content }
            : { type: "assistant", text: content };
    }
    if (entry.type === "human") return { ...entry, type: "user" };
    if (entry.type === "assistant") {
        const text = typeof entry.text === "string" ? entry.text : (typeof entry.content === "string" ? entry.content : "");
        return { ...entry, text };
    }
    return entry;
}
