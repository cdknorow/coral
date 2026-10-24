/* Moved out of render.js as it was (#3795): no behaviour change. */
import { SESSION_STATES, _WORKTREE_ICON, _gitStateSummary, deriveSessionState, getDotClass, getMobileStatusChip, resolveSessionIdentity, sessionGoalText, sessionStateInfo, sessionUnreadCount } from './session_state.js';
import { _saveSessionOrder } from './list_order.js';
import { escapeHtml, escapeAttr, agentNameColor } from './utils.js';
import { autoAcceptChip, autoAcceptMenuItem } from './auto_accept.js';
import { formatTokens } from './format.js';
import { getCachedAgentAvailability } from './team_availability.js';
import { isGoneAsleep, isExitedAsleep } from './terminal_gone.js';
import { isStaleSession, staleReason, serversShown } from './server_status.js';
import { sessionServer, keyLabel, sessionTeamKey } from './server_base.js';
import { redrawLiveList } from './list_hooks.js';
import { state } from './state.js';
import { heldNotice } from './held_inputs.js';
import { modelName, providerName, sessionLevel, typeName } from './model_labels.js';
import { terminalGoneChip } from './terminal_gone.js';
import { hooksMissingChip } from './hooks_missing.js';
import { browserLocal } from './safe_storage.js';
import { onBuilt } from './lazy_dom.js';
import { syncDisplayPanels } from './display_panel.js';
import { setChecked, setHidden } from './dom_write.js';

// ── Archived section (#3030) ─────────────────────────────────────────────
// One global collapsed section at the bottom of the list. The grouping is all
// in here so it can become per-team later.
function _archivedTeamLabel(s) {
    return s.board_project ? keyLabel(sessionTeamKey(s)) : 'No team';
}

export function _renderArchivedSection(archived) {
    if (!archived.length) return '';
    const expanded = _isArchivedExpanded();
    let html = `<li class="sleeping-summary-row archived-summary-row" role="button" tabindex="0" aria-expanded="${escapeAttr(expanded)}" data-on-click="archived-toggle">
        <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="12" height="3" rx="0.5"/><path d="M3 6v7h10V6"/><path d="M6.5 9h3"/></svg>
        <span class="sleeping-summary-label">Archived (${archived.length})</span>
    </li>`;
    if (!expanded) return html;
    const byTeam = new Map();
    for (const s of archived) {
        const label = _archivedTeamLabel(s);
        if (!byTeam.has(label)) byTeam.set(label, []);
        byTeam.get(label).push(s);
    }
    const labels = [...byTeam.keys()].sort((a, b) => (a === 'No team') - (b === 'No team') || a.localeCompare(b));
    for (const label of labels) {
        html += `<li class="archived-team-caption">${escapeHtml(label)}</li>`;
        for (const s of byTeam.get(label)) html += _renderSessionItem(s, '__archived__', true, false, '');
    }
    return html;
}

export function toggleArchivedSection() {
    try { browserLocal.set('coral-archived-expanded', _isArchivedExpanded() ? 'false' : 'true'); } catch { /* per-viewer only */ }
    redrawLiveList();
}

function _isArchivedExpanded() {
    try { return browserLocal.get('coral-archived-expanded') === 'true'; }
    catch { return false; }
}

// Drag-and-drop state
let _draggedSid = null;

// The text of each row's action menu, by the row's identity: drawn into the row when its menu opens (openRowMenu) and not before, so a list of 100 agents holds one menu's elements instead
// of a hundred. Every render sets each shown row's again (an agent whose state changed has a new row, which closes its menu as before); the oldest go past a thousand.
const rowMenus = new Map();
const rowMenuKey = (sid, server, name) => `${sid || ''}|${server || ''}|${name || ''}`;
function rememberRowMenu(s, html) {
    const key = rowMenuKey(s.session_id, sessionServer(s), s.name);
    rowMenus.delete(key);
    rowMenus.set(key, html);
    if (rowMenus.size > 1000) rowMenus.delete(rowMenus.keys().next().value);
}

/** Draw the menu of the row `btn` (its kebab button) belongs to into `menu`, and take every other row's drawn menu out. Returns false when the row's menu text is not known. */
export function openRowMenu(btn, menu) {
    for (const other of document.querySelectorAll('.sidebar-kebab-menu[data-lazy-menu][data-drawn]')) {
        if (other !== menu) { other.replaceChildren(); delete other.dataset.drawn; }
    }
    const row = btn.closest('.session-group-item');
    const html = row && rowMenus.get(rowMenuKey(row.dataset.sessionId, row.dataset.server, row.dataset.name));
    if (html === undefined) return false;
    if (!menu.dataset.drawn) { menu.innerHTML = html; menu.dataset.drawn = '1'; for (const it of menu.querySelectorAll('.overflow-menu-item')) if (!it.hasAttribute('role')) it.setAttribute('role', 'menuitem'); } // the auto-accept item is a menuitemcheckbox already
    return true;
}

/** A DOM id for a row's or group's menu, so its button can say aria-controls (#4621): the same on every render and different for every key (a short hash with the key's letters). */
export function kebabId(kind, key) {
    const k = String(key);
    let h = 5381;
    for (const c of k) h = ((h * 33) ^ c.codePointAt(0)) >>> 0;
    return `kebab-${kind}-${k.toLowerCase().replace(/[^a-z0-9]+/g, '-').slice(0, 24)}-${h.toString(36)}`;
}

const LABEL_TAIL = 7, LABEL_SPLIT_OVER = 16;
/** [head, tail] for a name longer than 16 characters (the tail is its last 7, never in the middle of a character), else [name, '']. */
function splitLabel(name) {
    const chars = Array.from(String(name));
    return chars.length > LABEL_SPLIT_OVER ? [chars.slice(0, -LABEL_TAIL).join(''), chars.slice(-LABEL_TAIL).join('')] : [String(name), ''];
}

/** What the phone's row shows besides the name (the sidebar hides these): the state chip, the unread count, and the banner for an agent that needs the person. */
function _mobileRowParts(s, stateInfo) {
    const mobileStatus = getMobileStatusChip(s);
    const mobileStatusChip = mobileStatus.className
        ? `<span class="session-status-chip ${escapeAttr(mobileStatus.className)}">${escapeHtml(mobileStatus.label)}</span>`
        : '';
    const mobileUnread = sessionUnreadCount(s);
    const unreadBoardBadge = mobileUnread > 0
        ? `<span class="session-mobile-meta-pill">${mobileUnread} unread</span>`
        : '';
    const mobileAttentionBanner = stateInfo.key === 'needs_input'
        ? '<div class="session-mobile-banner">Waiting for your input</div>'
        : (stateInfo.key === 'check_terminal'
            ? '<div class="session-mobile-banner">Nothing yet — open the terminal</div>'
            : (stateInfo.key === 'stuck' ? '<div class="session-mobile-banner error">Session needs attention</div>' : ''));
    return { mobileStatusChip, unreadBoardBadge, mobileAttentionBanner };
}

/** The items of an agent row's action menu (drawn when it opens): the archived, sleeping or awake set. */
function _rowMenuContent(s, sid) {
    const _archiveItem = `
            <button class="overflow-menu-item" data-on-click="agent-archive" data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type)}" data-session-id="${escapeAttr(sid)}" data-mode="archive">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="12" height="3" rx="0.5"/><path d="M3 6v7h10V6"/><path d="M6.5 9h3"/></svg>
                Archive
            </button>
`;
    const _sleepingMenu = `
            <a class="overflow-menu-item overflow-menu-open-window" href="/agent/${escapeAttr(sid)}" target="_blank" rel="noopener noreferrer" title="Open Agent Tab" aria-label="Open Agent Tab" data-on-click="kebab-close">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M9 2h5v5"/><path d="M14 2L7 9"/><path d="M12 9v4a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1h4"/></svg>
                Open Agent Tab
            </a>
            <button class="overflow-menu-item" data-on-click="agent-sleep" data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type)}" data-session-id="${escapeAttr(sid)}" data-mode="wake">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3a6 6 0 1 0 0 10 5 5 0 0 1 0-10z"/></svg>
                Wake
            </button>
${_archiveItem}            <button class="overflow-menu-item" data-on-click="agent-restart" data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type)}" data-session-id="${escapeAttr(sid)}">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3.5 2v4h4"/><path d="M3.5 6A5.5 5.5 0 1 1 2.5 8"/></svg>
                Restart
            </button>
            <button class="overflow-menu-item" data-on-click="agent-rename" data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type)}" data-session-id="${escapeAttr(sid)}">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><path d="M11.5 1.5l3 3L5 14H2v-3z"/></svg>
                Rename
            </button>
            <button class="overflow-menu-item" data-on-click="agent-icon" data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type)}" data-session-id="${escapeAttr(sid)}">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="8" r="6.5"/><path d="M5.5 9.5c0 0 1 1.5 2.5 1.5s2.5-1.5 2.5-1.5"/><circle cx="6" cy="6.5" r="0.5" fill="currentColor" stroke="none"/><circle cx="10" cy="6.5" r="0.5" fill="currentColor" stroke="none"/></svg>
                Set Icon
            </button>
            <button class="overflow-menu-item" data-on-click="agent-info" data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type)}" data-session-id="${escapeAttr(sid)}">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><circle cx="8" cy="8" r="6.5"/><line x1="8" y1="7" x2="8" y2="11"/><circle cx="8" cy="5" r="0.5" fill="currentColor" stroke="none"/></svg>
                Session Info
            </button>
            <hr class="overflow-menu-divider">
            <button class="overflow-menu-item" data-on-click="agent-move" data-session-id="${escapeAttr(sid)}" data-dir="up">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M8 3v10M4 7l4-4 4 4"/></svg>
                Move Up
            </button>
            <button class="overflow-menu-item" data-on-click="agent-move" data-session-id="${escapeAttr(sid)}" data-dir="down">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M8 13V3M4 9l4 4 4-4"/></svg>
                Move Down
            </button>
            <hr class="overflow-menu-divider">
            <button class="overflow-menu-item overflow-menu-danger" data-on-click="agent-kill" data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type)}" data-session-id="${escapeAttr(sid)}">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="4" y1="4" x2="12" y2="12"/><line x1="12" y1="4" x2="4" y2="12"/></svg>
                Remove Agent
            </button>`;
    const _awakeMenu = `
            <a class="overflow-menu-item overflow-menu-open-window" href="/agent/${escapeAttr(sid)}" target="_blank" rel="noopener noreferrer" title="Open Agent Tab" aria-label="Open Agent Tab" data-on-click="kebab-close">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M9 2h5v5"/><path d="M14 2L7 9"/><path d="M12 9v4a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1h4"/></svg>
                Open Agent Tab
            </a>
            <button class="overflow-menu-item" data-local-only data-on-click="agent-attach" data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type)}" data-session-id="${escapeAttr(sid)}">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="4,4 8,8 4,12"/><line x1="9" y1="12" x2="13" y2="12"/></svg>
                Attach
            </button>
            <button class="overflow-menu-item" data-on-click="agent-restart" data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type)}" data-session-id="${escapeAttr(sid)}">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3.5 2v4h4"/><path d="M3.5 6A5.5 5.5 0 1 1 2.5 8"/></svg>
                Restart
            </button>
            ${autoAcceptMenuItem(s)}
            <button class="overflow-menu-item" data-on-click="agent-rename" data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type)}" data-session-id="${escapeAttr(sid)}">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><path d="M11.5 1.5l3 3L5 14H2v-3z"/></svg>
                Rename
            </button>
            <button class="overflow-menu-item" data-on-click="agent-icon" data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type)}" data-session-id="${escapeAttr(sid)}">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="8" r="6.5"/><path d="M5.5 9.5c0 0 1 1.5 2.5 1.5s2.5-1.5 2.5-1.5"/><circle cx="6" cy="6.5" r="0.5" fill="currentColor" stroke="none"/><circle cx="10" cy="6.5" r="0.5" fill="currentColor" stroke="none"/></svg>
                Set Icon
            </button>
            <button class="overflow-menu-item" data-on-click="agent-info" data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type)}" data-session-id="${escapeAttr(sid)}">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><circle cx="8" cy="8" r="6.5"/><line x1="8" y1="7" x2="8" y2="11"/><circle cx="8" cy="5" r="0.5" fill="currentColor" stroke="none"/></svg>
                Session Info
            </button>
            <button class="overflow-menu-item" data-on-click="agent-sleep" data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type)}" data-session-id="${escapeAttr(sid)}" data-mode="sleep">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3a6 6 0 1 0 0 10 5 5 0 0 1 0-10z"/></svg>
                Sleep
            </button>
${_archiveItem}            <hr class="overflow-menu-divider">
            <button class="overflow-menu-item" data-on-click="agent-move" data-session-id="${escapeAttr(sid)}" data-dir="up">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M8 3v10M4 7l4-4 4 4"/></svg>
                Move Up
            </button>
            <button class="overflow-menu-item" data-on-click="agent-move" data-session-id="${escapeAttr(sid)}" data-dir="down">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M8 13V3M4 9l4 4 4-4"/></svg>
                Move Down
            </button>
            <hr class="overflow-menu-divider">
            <button class="overflow-menu-item overflow-menu-danger" data-on-click="agent-kill" data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type)}" data-session-id="${escapeAttr(sid)}">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="4" y1="4" x2="12" y2="12"/><line x1="12" y1="4" x2="4" y2="12"/></svg>
                Kill Session
            </button>`;
    // Archived: no Restart, Rename or reordering (#3030).
    const _archivedMenu = `
            <a class="overflow-menu-item overflow-menu-open-window" href="/agent/${escapeAttr(sid)}" target="_blank" rel="noopener noreferrer" title="Open Agent Tab" aria-label="Open Agent Tab" data-on-click="kebab-close">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M9 2h5v5"/><path d="M14 2L7 9"/><path d="M12 9v4a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1h4"/></svg>
                Open Agent Tab
            </a>
            <button class="overflow-menu-item" data-on-click="agent-archive" data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type)}" data-session-id="${escapeAttr(sid)}" data-mode="unarchive">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="3" width="12" height="3" rx="0.5"/><path d="M3 6v7h10V6"/><path d="M8 12V8M6 10l2-2 2 2"/></svg>
                Unarchive
            </button>
            <button class="overflow-menu-item" data-on-click="agent-sleep" data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type)}" data-session-id="${escapeAttr(sid)}" data-mode="wake">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3a6 6 0 1 0 0 10 5 5 0 0 1 0-10z"/></svg>
                Wake
            </button>
            <button class="overflow-menu-item" data-on-click="agent-info" data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type)}" data-session-id="${escapeAttr(sid)}">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><circle cx="8" cy="8" r="6.5"/><line x1="8" y1="7" x2="8" y2="11"/><circle cx="8" cy="5" r="0.5" fill="currentColor" stroke="none"/></svg>
                Session Info
            </button>
            <hr class="overflow-menu-divider">
            <button class="overflow-menu-item overflow-menu-danger" data-on-click="agent-kill" data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type)}" data-session-id="${escapeAttr(sid)}">
                <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="4" y1="4" x2="12" y2="12"/><line x1="12" y1="4" x2="4" y2="12"/></svg>
                Remove Agent
            </button>`;
    return s.archived ? _archivedMenu : (s.sleeping ? _sleepingMenu : _awakeMenu);
}

export function _renderSessionItem(s, groupName, isCompact, collapsed, teamDefaultDir) {
    const dotClass = getDotClass(s);
    const isActive = !state.selectedTeam && state.currentSession && state.currentSession.type === "live" && state.currentSession.session_id === s.session_id;

    // Spec: remove agent type from the default row layout
    const typeTag = "";

    // Directory override chip
    // Directory override now lives in the tooltip (Directory row), not the row.
    const dirChip = "";

    // 'Show branches' (#5117, #5237): the branch of every row that has one. An agent on its own worktree also gets the worktree icon and its ahead/behind summary; one on a shared
    // checkout gets the branch alone (it used to get no line at all, so the option did nothing for a team that works in shared checkouts).
    const gsWt = s.git_state?.is_worktree ? s.git_state : null;
    const branchTag = (isBranchShown() && s.branch)
        ? (gsWt
            ? `<div class="session-worktree-line" title="Worktree branch ${escapeAttr(s.branch)}${escapeAttr(gsWt.base_branch ? ' (vs ' + escapeAttr(gsWt.base_branch) + ')' : '')}">${_WORKTREE_ICON}<span class="wt-branch">${escapeHtml(s.branch)}</span>${_gitStateSummary(gsWt)}</div>`
            : `<div class="session-worktree-line" title="Branch ${escapeAttr(s.branch)}"><span class="wt-branch">${escapeHtml(s.branch)}</span></div>`)
        : "";
    // One pill at most, from the shared state table. The attention states carry a
    // warning pill; "Task assigned · idle" a neutral one (#3227). "Ready for input"
    // has none: the tooltip and the aria-label carry that state (no dot) (task #175).
    const stateInfo = sessionStateInfo(s);
    const pillKind = stateInfo.attention ? 'waiting-badge session-state-pill session-attention-pill' : 'session-state-pill';
    const waitingBadge = stateInfo.pill
        ? ` <span class="badge ${pillKind}${stateInfo.pillClass ? ' ' + stateInfo.pillClass : ''}">${stateInfo.pill}</span>`
        : '';
    // Context is secondary: any state pill owns line 1 and the ctx pill
    // collapses to the dot's ring, which survives on every glyph.
    const ctxPill = waitingBadge ? '' : _renderCtxPill(s);
    const ctxHigh = s.context_pct !== null && s.context_pct !== undefined && s.context_pct >= 80;
    const isTerminal = s.agent_type === "terminal";
    const sid = s.session_id ? escapeAttr(s.session_id) : "";
    // Goal line is rendered on every row (not just the active one):
    // summary -> first user prompt -> inline "No goal yet" + generate button.
    const goalText = sessionGoalText(s);
    const goalBtn = "";
    // Row label follows the shared identity chain (display_name -> auto_name
    // -> board_job_title -> Agent/Terminal). The prompt is never a name; it
    // shows on the goal line below when there is no summary.
    const identity = resolveSessionIdentity(s);
    // An agent called by its folder under that folder's own group would read the same twice: its CLI tells it from the group ("my-app · Claude", #6029).
    const sameAsGroup = !isTerminal && groupName && String(identity).toLowerCase() === String(keyLabel(groupName)).toLowerCase() && typeName(s.agent_type);
    const displayLabel = sameAsGroup ? `${identity} \u00b7 ${typeName(s.agent_type)}` : identity;
    // A long name is cut with an ellipsis, and agents of one team share a prefix: its last characters never shrink, so the end that tells them apart stays ("release-engineering-coo...-4-9").
    const [nameHead, nameTail] = splitLabel(displayLabel);
    const reminder = getCachedAgentAvailability(s);
    const reminderTag = reminder?.reminder
        ? `<span class="agent-reminder-icon" title="Periodic reminder every ${escapeAttr(Math.max(1, Math.round((reminder.reminder_interval_seconds || 300) / 60)))} min" aria-label="Periodic reminder active">⏰</span>`
        : '';
    let goalLine = "";
    if (goalText) {
        goalLine = `<span class="session-goal${isCompact ? ' session-goal-compact' : ''}" title="${escapeAttr(goalText)}">${escapeHtml(goalText)}</span>`;
    } else if (!isTerminal) {
        // The "No goal yet" label is PASSIVE text: it sits where users click to
        // select a row, so it must bubble to the row's click handler. Only the
        // sparkle button requests a goal.
        goalLine = `<span class="session-goal session-goal-empty${isCompact ? ' session-goal-compact' : ''}">
                <button class="sidebar-goal-btn sidebar-goal-btn-inline" data-on-click="goal-request" data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type)}" data-session-id="${escapeAttr(sid)}" title="Generate goal" aria-label="Generate goal for ${escapeAttr(identity)}"><span class="material-icons" style="font-size:0.875rem" aria-hidden="true">auto_awesome</span></button>
                <span class="session-goal-empty-label">No goal yet</span>
            </span>`;
    }
    // A state without a chip class ("Ready for input") renders no mobile chip. The meta
    // row is emitted without whitespace so :empty can collapse it on the phone.
    const { mobileStatusChip, unreadBoardBadge, mobileAttentionBanner } = _mobileRowParts(s, stateInfo);
    const needsAttention = sessionNeedsAttention(s);
    // Unread board messages: a neutral count on line 2 (never an attention colour),
    // shown on selected rows too.
    const unreadCount = sessionUnreadCount(s);
    // The unread count on agent rows was not useful to users, so no chip is rendered.
    void unreadCount;
    const unreadChip = '';
    const held = heldNotice(s); // automated messages held for over a minute (#6473)
    const heldLine = held ? `<div class="session-held-line" title="${escapeAttr(held.text)}">${escapeHtml(held.text)}</div>` : '';
    const kebabMenuContent = _rowMenuContent(s, sid);
    // The menu's items (about 50 elements a row) are drawn when it opens (#4611): the row holds an empty menu and this module the text for it.
    rememberRowMenu(s, kebabMenuContent);
    const rowMenuId = kebabId('agent', rowMenuKey(s.session_id, sessionServer(s), s.name));
    const kebabMenu = `<div class="sidebar-kebab-wrapper">
        <button class="sidebar-kebab-btn" data-on-click="kebab-toggle" title="More actions" aria-label="More actions for ${escapeAttr(identity)}" aria-haspopup="menu" aria-expanded="false" aria-controls="${escapeAttr(rowMenuId)}">&#x22EE;</button>
        <div class="sidebar-kebab-menu" id="${escapeAttr(rowMenuId)}" style="display:none" data-lazy-menu="1" role="menu" aria-label="Actions for ${escapeAttr(identity)}"></div>
    </div>`;
    const compactClass = isCompact ? ' session-compact' : '';
    const collapsedClass = collapsed ? ' group-collapsed' : '';
    const sleepingClass = s.sleeping ? ' sleeping' : '';
    const attentionClass = needsAttention ? ' needs-attention' : '';
    // Hub: an agent on an unreachable server is last-known data only.
    const stale = isStaleSession(s);
    const staleClass = stale ? ' session-stale' : '';
    // Its click opens the agent: data the session-open action reads.
    const clickHandler = stale ? '' : ` data-on-click="session-open" data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type)}"`;
    // Focusable list item (not a button: it contains the kebab, the open-tab
    // anchor and the sparkle). Selection is delegated (see _wireListKeyboard).
    const ariaLabel = `${sameAsGroup ? `${identity}, ${typeName(s.agent_type)}` : identity}, ${stateInfo.label}`
        + (ctxHigh ? `, context ${s.context_pct >= 100 ? 'full' : Math.round(s.context_pct) + '%'}` : '')
        + (unreadCount > 0 ? `, ${unreadCount} unread` : '');
    const stuckClass = stateInfo.key === 'stuck' ? ' is-stuck' : '';
    return `<li class="session-group-item${isActive ? ' active' : ''}${compactClass}${collapsedClass}${sleepingClass}${attentionClass}${stuckClass}${staleClass}"
        draggable="${stale ? 'false' : 'true'}"${stale ? ` aria-disabled="true" title="${escapeAttr(staleReason(s))}"` : ''}
        data-state="${escapeAttr(stateInfo.key)}"
        tabindex="0"
        aria-label="${escapeAttr(ariaLabel)}"${isActive ? ' aria-current="true"' : ''}
        data-session-id="${escapeAttr(sid)}"
        data-server="${escapeAttr(sessionServer(s))}"
        data-group="${escapeAttr(groupName)}"
        ${clickHandler}>
        <span class="drag-grip" title="Drag to reorder">&#x2630;</span>
        <div class="session-info">
            <div class="session-name-row">
                <span class="session-dot ${dotClass}${ctxHigh ? ' ctx-high' : ''}" title="${escapeAttr(stateInfo.label)}" aria-hidden="true"></span>
                <span class="session-label${nameTail ? ' session-label-split' : ''}" title="${escapeAttr(displayLabel)}"><span class="session-label-name${s.agent_type === 'terminal' ? ' is-terminal' : ''}"${agentNameColor(s) ? ` style="--agent-name-color:${agentNameColor(s)}"` : ''}>${nameTail ? `<span class="sln-head">${escapeHtml(nameHead)}</span><span class="sln-tail">${escapeHtml(nameTail)}</span>` : escapeHtml(displayLabel)}</span>${nameTail ? `<span class="session-label-tags">${reminderTag}${typeTag}${dirChip}</span>` : `${reminderTag}${typeTag}${dirChip}`}</span>${autoAcceptChip(s)}${terminalGoneChip(s)}${hooksMissingChip(s)}
                <span class="session-name-spacer"></span>
                ${stale ? '' : waitingBadge + ctxPill}
                ${goalBtn}
                ${stale ? '' : kebabMenu}
            </div>
            <div class="session-mobile-banner-row">
                ${mobileAttentionBanner}
            </div>
            <div class="session-mobile-meta">${mobileStatusChip}${unreadBoardBadge}</div>
            ${(goalLine || unreadChip) ? `<div class="session-line2">${goalLine}${unreadChip}</div>` : ''}
            ${agentLineHtml(s)}
            ${branchTag}${heldLine}
        </div>
        <div class="session-tooltip"></div>
    </li>`;
}

function _renderSleepingSummary(sleepingAgents, groupName, isCompact, teamDefaultDir) {
    const count = sleepingAgents.length;
    const expanded = _isSleepingExpanded(groupName);
    const summaryName = `${count} sleeping agent${count !== 1 ? 's' : ''} in ${keyLabel(groupName)}`; // each folder's summary has its own name
    let html = `<li class="sleeping-summary-row" role="button" tabindex="0" data-key-click aria-expanded="${escapeAttr(String(expanded))}" aria-label="${escapeAttr(summaryName)}" data-on-click="group-sleeping-expand" data-group="${escapeAttr(groupName)}">
        <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3a6 6 0 1 0 0 10 5 5 0 0 1 0-10z"/></svg>
        <span class="sleeping-summary-label">${count} sleeping agent${count !== 1 ? 's' : ''}</span>
    </li>`;
    if (expanded) {
        for (const s of sleepingAgents) {
            html += _renderSessionItem(s, groupName, isCompact, false, teamDefaultDir);
        }
    }
    return html;
}

export function _renderAgentListWithSubgroups(agents, teamDefaultDir, isCompact, groupName) {
    if (!agents.length) return "";

    // An agent asleep because its terminal is gone stays in view with its "Terminal gone" chip
    // (#3344), not folded into the "N sleeping agents" row: it needs a Restart, not a nap.
    // One whose CLI exited stays in view too, with an "Ended" chip (#6058).
    const awake = agents.filter(s => !s.sleeping || isGoneAsleep(s) || isExitedAsleep(s));
    const sleeping = agents.filter(s => s.sleeping && !isGoneAsleep(s) && !isExitedAsleep(s));

    // One flat list: an agent's folder is shown in the header bar, not as
    // sidebar sub-groups.
    let html = awake.map(s => _renderSessionItem(s, groupName, isCompact, false, teamDefaultDir)).join('');

    if (sleeping.length > 0) {
        html += _renderSleepingSummary(sleeping, groupName, isCompact, teamDefaultDir);
    }

    return html;
}

// ── Agent summary line (the goal text under each agent name) ──────────

/** The line under an agent's name that says what it runs (#5133, #5742): its provider, its model and its thinking level ("Claude · Opus 5.5 · High"). Three options, each kept per viewer and on
 *  unless turned off: Show provider, Show model, Show thinking level. A viewer who set the old single "Show agent type" switch keeps that choice for all three until they change one. */
const AGENT_LINE_KEY = { provider: 'coral-show-provider', model: 'coral-show-model', thinking: 'coral-show-thinking' };
export function isAgentLineShown(part) {
    try {
        const own = browserLocal.get(AGENT_LINE_KEY[part]);
        if (own === 'true' || own === 'false') return own === 'true';
        const legacy = browserLocal.get('coral-show-agent-type');
        return legacy === 'true' || legacy === 'false' ? legacy === 'true' : true;
    } catch { return true; }
}
export function setAgentLineShown(part, on) {
    try { browserLocal.set(AGENT_LINE_KEY[part], on ? 'true' : 'false'); } catch { /* per-viewer only */ }
}
/** The words of the line for a row, by the options that are on: only what the agent has on record (a terminal has none of the three, an agent with no level shows no level). The provider is the
 *  start of the line, not a line of its own: a row with no model and no level on record has no line (as before the options, so the list does not grow a line per agent), except that with
 *  Show model and Show thinking level both off the provider is all that was asked for and is shown. */
export function agentLineText(s) {
    const model = isAgentLineShown('model') ? modelName(s.agent_type, s.model, sessionServer(s)) : '';
    const level = isAgentLineShown('thinking') ? sessionLevel(s) : null;
    const provider = isAgentLineShown('provider') ? providerName(s.agent_type) : '';
    const providerOnly = !isAgentLineShown('model') && !isAgentLineShown('thinking');
    if (!model && !level && !(provider && providerOnly)) return '';
    return [provider, model, level && level.name].filter(Boolean).join(' \u00b7 ');
}
function agentLineHtml(s) {
    const text = agentLineText(s);
    if (!text) return '';
    const lvl = sessionLevel(s);
    const tip = [s.model, lvl ? `thinking: ${lvl.name}${lvl.requested ? ' (as started)' : ''}` : ''].filter(Boolean).join(', ');
    return `<div class="session-model"${tip ? ` title="${escapeAttr(tip)}"` : ''}>${escapeHtml(text)}</div>`;
}

/** 'Show branches' (#5117): the worktree and branch line under an agent's name. Kept per viewer, like Group by server; off unless turned on. The branch stays in the row's tooltip and the agent's header. */
export function isBranchShown() {
    try { return browserLocal.get('coral-show-branches') === 'true'; }
    catch { return false; }
}

/** Stored server-side in user settings (`show_session_summary`); off unless turned on. */
export function isSessionSummaryShown() {
    return String(state.settings?.show_session_summary).toLowerCase() === 'true';
}

export function applySessionSummaryPref() {
    const shown = isSessionSummaryShown();
    document.querySelector('.sidebar')?.classList.toggle('summary-hidden', !shown);
    const check = document.getElementById('settings-show-summary');
    if (check) check.checked = shown;
    syncDisplayMenu();
}
onBuilt('settings-modal', () => applySessionSummaryPref()); // its Show summaries box, once Settings is built (#4500)

/** 'Show server' (setting show_session_server): unset = on with 2+ servers, off with one. */
 export function isServerShownOnRows() {
    const v = String(state.settings?.show_session_server ?? '').toLowerCase();
    if (v === 'true') return true;
    if (v === 'false') return false;
    return serversShown().length >= 2;
}

/** 'Group by server' (per viewer, off unless chosen); only with 2+ servers. */
 export function isGroupedByServer() {
    if (serversShown().length < 2) return false;
    try { return browserLocal.get('coral-group-by-server') === 'true'; } catch { return false; }
}

/** What the Display panel says under its options when 'Show branches' or 'Show agent type' is on and some agents have nothing to show for it (#5237): an option that
 *  seems to do nothing for a row is explained, not silent. "" when neither option is on or every agent has its data. A terminal has no model, so it is not counted for that. */
export function displayHintText() {
    const model = isAgentLineShown('model'), thinking = isAgentLineShown('thinking'), branch = isBranchShown();
    if (!model && !thinking && !branch) return '';
    const live = (state.liveSessions || []).filter(s => !s.archived);
    const parts = [];
    const count = (n, what) => `${n} ${n === 1 ? 'agent has' : 'agents have'} no ${what} on record`;
    if (model) { const n = live.filter(s => s.agent_type !== 'terminal' && !modelName(s.agent_type, s.model, sessionServer(s))).length; if (n) parts.push(count(n, 'model')); }
    if (thinking) { const n = live.filter(s => s.agent_type !== 'terminal' && !sessionLevel(s)).length; if (n) parts.push(count(n, 'thinking level')); }
    if (branch) { const n = live.filter(s => !s.branch).length; if (n) parts.push(count(n, 'branch')); }
    return parts.length ? parts.join('; ') + '.' : '';
}

/** Sync the ≡ display menu's checkboxes and which options it offers: the sidebar's, and the phone's copy in the agent list
 *  header (#3991, the sidebar is hidden below 768 px). Both are found by data attributes, so each can appear once. It runs on every render of the
 *  list, so it looks only inside the two headers that hold the menu: an attribute selector over the whole document walked every node of the
 *  page (and of a long chat) each time (#4985). */
 export function syncDisplayMenu() {
    const n = serversShown().length;
    const checked = { summary: isSessionSummaryShown(), server: isServerShownOnRows(), group: isGroupedByServer(), branches: isBranchShown(), provider: isAgentLineShown('provider'), model: isAgentLineShown('model'), thinking: isAgentLineShown('thinking') };
    const offered = { summary: true, server: n >= 1, group: n >= 2, branches: true, provider: true, model: true, thinking: true };
    const hintText = displayHintText();
    for (const header of [...document.getElementsByClassName('sidebar-header'), ...document.getElementsByClassName('mobile-list-header')]) {
        for (const input of header.querySelectorAll('[data-display-input]')) setChecked(input, checked[input.dataset.displayInput] === true);
        for (const row of header.querySelectorAll('[data-display-row]')) setHidden(row, !offered[row.dataset.displayRow]);
        for (const btn of header.querySelectorAll('[data-display-button]')) btn.classList.toggle('active', checked.summary || (n >= 1 && checked.server));
        for (const hint of header.querySelectorAll('[data-display-hint]')) { hint.textContent = hintText; setHidden(hint, !hintText); }
    }
    syncDisplayPanels(); // the phone's header is built later than the sidebar's: it takes the stored open or shut state here
}

function _isSleepingExpanded(groupName) {
    try { return browserLocal.get(`coral-sleeping-expanded:${groupName}`) === 'true'; }
    catch { return false; }
}

/** Row-level attention (amber/red tint + attention pill): the three states
 *  that need the operator at the terminal. */
 function sessionNeedsAttention(s) {
    return !!(s && SESSION_STATES[deriveSessionState(s)].attention);
}

/** Elements matching `sel` among `roots` and their descendants. */
export function _within(roots, sel) {
    const out = [];
    for (const r of roots) {
        if (r.matches(sel)) out.push(r);
        out.push(...r.querySelectorAll(sel));
    }
    return out;
}

/** Wire drag-and-drop on the rows and containers under `roots` (the nodes a
 *  render added; kept nodes already have their listeners). */
export function _attachDragListeners(list, roots = [list]) {
    const items = _within(roots, ".session-group-item[draggable]");

    // Allow drops inside nested board-card-agents containers
    for (const container of _within(roots, ".board-card-agents")) {
        container.addEventListener("dragover", (e) => {
            e.preventDefault();
            e.dataTransfer.dropEffect = "move";
        });
    }

    for (const item of items) {
        item.addEventListener("dragstart", (e) => {
            e.stopPropagation(); // Prevent board card from intercepting
            _draggedSid = item.dataset.sessionId;
            item.classList.add("dragging");
            e.dataTransfer.effectAllowed = "move";
            // Use a minimal drag image to avoid tooltip flash
            e.dataTransfer.setData("text/plain", _draggedSid);
        });

        item.addEventListener("dragend", () => {
            item.classList.remove("dragging");
            _draggedSid = null;
            // Clear all drop indicators
            for (const el of list.querySelectorAll(".drag-over")) {
                el.classList.remove("drag-over");
            }
        });

        item.addEventListener("dragover", (e) => {
            e.preventDefault();
            if (!_draggedSid || _draggedSid === item.dataset.sessionId) return;
            // Only allow reorder within the same group
            const draggedEl = list.querySelector(`[data-session-id="${_draggedSid}"]`);
            if (!draggedEl || draggedEl.dataset.group !== item.dataset.group) return;
            e.dataTransfer.dropEffect = "move";
            item.classList.add("drag-over");
        });

        item.addEventListener("dragleave", () => {
            item.classList.remove("drag-over");
        });

        item.addEventListener("drop", (e) => {
            e.preventDefault();
            e.stopPropagation();
            item.classList.remove("drag-over");
            if (!_draggedSid || _draggedSid === item.dataset.sessionId) return;

            const group = item.dataset.group;
            const draggedEl = list.querySelector(`[data-session-id="${_draggedSid}"]`);
            if (!draggedEl || draggedEl.dataset.group !== group) return;

            // Gather current order of session_ids in this group
            const groupItems = [...list.querySelectorAll(".session-group-item")].filter(el => el.dataset.group === group);
            const ids = groupItems.map(el => el.dataset.sessionId);

            // Remove dragged from its position and insert before drop target
            const fromIdx = ids.indexOf(_draggedSid);
            const toIdx = ids.indexOf(item.dataset.sessionId);
            if (fromIdx === -1 || toIdx === -1) return;

            ids.splice(fromIdx, 1);
            ids.splice(toIdx, 0, _draggedSid);

            // Save all session IDs in current DOM order (preserves other groups)
            const allItems = [...list.querySelectorAll(".session-group-item")];
            const allIds = allItems.map(el => el.dataset.sessionId);
            // Replace the current group's IDs with the reordered ones
            const groupSet = new Set(ids);
            const merged = [];
            let groupInserted = false;
            for (const sid of allIds) {
                if (groupSet.has(sid)) {
                    if (!groupInserted) {
                        merged.push(...ids);
                        groupInserted = true;
                    }
                } else {
                    merged.push(sid);
                }
            }
            if (!groupInserted) merged.push(...ids);

            // Save and re-render
            _saveSessionOrder(merged);
            redrawLiveList();
        });
    }
}

/** Line-1 context pill, only at >=80% (D3 thresholds; explicit null = unknown = nothing). */
function _renderCtxPill(s) {
    const pct = s.context_pct;
    if (pct === null || pct === undefined || pct < 80) return '';
    const label = pct >= 100 ? 'ctx full' : `ctx ${Math.round(pct)}%`;
    const win = s.context_window || 0;
    const title = win > 0
        ? `Context: ${formatTokens(Math.round(win * pct / 100))} / ${formatTokens(win)} tokens (${pct}%)`
        : `Context: ${pct}%`;
    return ` <span class="badge session-ctx-pill${pct >= 100 ? ' ctx-full' : ''}" title="${escapeAttr(title)}">${label}</span>`;
}
