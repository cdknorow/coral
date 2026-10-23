/* Moved out of render.js as it was (#3795): no behaviour change. */
import { modelDisplayName } from './model_names.js';
import { sessionLevel, typeName } from './model_labels.js';
import { escapeHtml, escapeAttr } from './utils.js';
import { formatTokens, formatCost } from './format.js';
import { getCachedAgentAvailability } from './team_availability.js';
import { isGoneAsleep } from './terminal_gone.js';
import { keyLabel, sessionTeamKey } from './server_base.js';
import { setClassName, setTitle, setAttr } from './dom_write.js';

/* ── Session identity ───────────────────────────────────────────────── */
const GOAL_LINE_MAX = 140;

/** Collapse whitespace/newlines and truncate to one line. */
 function oneLine(text, max = GOAL_LINE_MAX) {
    if (!text) return '';
    const flat = String(text).replace(/\s+/g, ' ').trim();
    if (flat.length <= max) return flat;
    return flat.slice(0, max - 1).trimEnd() + '\u2026';
}

/** Goal text for a session row: summary, else the first user prompt. */
 export function sessionGoalText(s) {
    if (!s) return '';
    return oneLine(s.summary || s.first_prompt || '');
}

/**
 * Stable identity source for a session, or '' when there is none.
 * Chain: display_name -> auto_name (D8, transcript-derived, write-once) ->
 * board_job_title. Neither summary nor first_prompt is identity: the summary
 * changes as the agent works, and a prompt is a sentence or a path, not a
 * name (task #85). Both are rendered as goal text only.
 */
 function sessionIdentitySource(s) {
    if (!s) return '';
    return (s.display_name || '').trim()
        || (s.auto_name || '').trim()
        || (s.board_job_title || '').trim();
}

// Placeholder of the command box: who it goes to, and that an image can be pasted. The agent's type is in the header and the board's name in the sidebar, so neither is repeated here
// (the board's generated id used to be, #5991); a phone gets the target only, as the full text is cut off mid-word in its box.
export function commandPlaceholder(identity, agentType) {
    if (window.innerWidth <= 767) return `Message ${identity}\u2026`;
    if (agentType === 'terminal') return `Type a command in ${identity}`;
    return `Message ${identity}, or paste an image`;
}

/** The box's words while a new agent's CLI is not up yet (#6382): it says so, and that a message typed now is held for it. */
export function startingPlaceholder(identity) {
    return window.innerWidth <= 767 ? `Starting ${identity}\u2026` : `Starting ${identity}\u2026 you can type now, it is sent when ready`;
}

/** Shared identity resolver used by row label, avatar, header, terminal label and placeholder. */
export function resolveSessionIdentity(s) {
    if (!s) return 'Agent';
    const src = sessionIdentitySource(s);
    if (src) return oneLine(src, 60);
    if (s.agent_type === 'terminal') return 'Terminal';
    // An agent nobody named is called by the folder it works in, else by its CLI (the home folder is no name): "my-app", "Claude", not "Agent" (#5990).
    return defaultAgentName(s.working_directory, s.agent_type);
}

/** The name an unnamed agent shows: its folder's name (not the home folder, the root or a drive), else the CLI's name, else "Agent". */
export function defaultAgentName(dir, agentType) {
    const trim = p => String(p || '').trim().replace(/[\\/]+$/, '');
    const d = trim(dir), root = trim(typeof document !== 'undefined' ? document.body?.dataset?.coralRoot : '');
    const folder = d && d !== '~' && d !== root && !/^[A-Za-z]:$/.test(d) ? d.split(/[\\/]/).pop() : '';
    return oneLine(folder, 60) || typeName(agentType) || 'Agent';
}

export function formatShortTime(isoStr) {
    const d = new Date(isoStr);
    if (isNaN(d)) return "";
    const dd = String(d.getDate()).padStart(2, "0");
    const mm = String(d.getMonth() + 1).padStart(2, "0");
    const yy = String(d.getFullYear()).slice(-2);
    return `${dd}/${mm}/${yy}`;
}

export function formatStaleness(seconds) {
    if (seconds === null || seconds === undefined) return "Unknown";
    if (seconds < 5) return "Just now";
    if (seconds < 60) return `${Math.floor(seconds)}s ago`;
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
    return `${Math.floor(seconds / 3600)}h ago`;
}

/* ── Agent state: one resolver, one vocabulary ─────────────────────────
 * Priority (AGENT_LIST_COMPACT state table):
 *   Sleeping > Stuck > Needs input > Check terminal
 *   > Ready for input (turn ended, awaiting instruction) > Working > Idle
 * Context, unread and selection are overlays, never winners. Every surface
 * (row dot/pill/aria, tooltip, mobile chip, workspace header, popout pill,
 * nav and group counts) reads this table so the words never diverge. */
 export const SESSION_STATES = {
    sleeping:       { label: 'Sleeping',       dot: 'sleeping',  pill: null,             pillClass: '',          chip: 'idle',        attention: false },
    stuck:          { label: 'Stuck',          dot: 'stuck',     pill: 'Stuck',          pillClass: 'stuck',     chip: 'error',       attention: true },
    needs_input:    { label: 'Needs input',    dot: 'waiting',   pill: 'Needs input',    pillClass: '',          chip: 'needs-input', attention: true },
    check_terminal: { label: 'Check terminal', dot: 'waiting',   pill: 'Check terminal', pillClass: '',          chip: 'needs-input', attention: true },
    your_turn:      { label: 'Ready for input', dot: 'your-turn', pill: null,            pillClass: '',          chip: '',            attention: false },
    working:        { label: 'Working',        dot: 'working',   pill: null,             pillClass: '',          chip: 'running',     attention: false },
    // Not urgent: a muted ring + neutral pill, never the yellow of Needs input (#3227).
    task_idle:      { label: 'Task assigned · idle', dot: 'task-idle', pill: 'Idle · task', pillClass: 'neutral', chip: 'idle', attention: false },
    // Waiting on a teammate's reply, a task or a commit (coral-board wait): calm, like
    // task_idle; nobody's input is needed (#3471). The label names what it waits on.
    waiting_on:     { label: 'Waiting on the board', dot: 'task-idle', pill: 'Waiting', pillClass: 'neutral', chip: 'idle', attention: false },
    idle:           { label: 'Idle',           dot: 'stale',     pill: null,             pillClass: '',          chip: 'idle',        attention: false },
};

/** Turn ended and the agent awaits its next instruction. Backend contract:
 *  `awaiting_user` (boolean, explicit). Servers without the field still send
 *  the stop-derived `done`, which means exactly this (never "completed"). */
function _isAwaitingUser(s) {
    if (s.awaiting_user !== undefined && s.awaiting_user !== null) return !!s.awaiting_user;
    return !!s.done;
}

/** Resolve the single winning state key for a session. */
export function deriveSessionState(s, opts = {}) {
    if (!s) return 'idle';
    if (s.sleeping) return 'sleeping';
    if (s.stuck) return 'stuck';
    if (s.waiting_for_input) return 'needs_input';
    if (s.not_started) return 'check_terminal';
    const availability = getCachedAgentAvailability(s)?.availability;
    if (availability === 'waiting' && !s.working) return 'waiting_on';
    if (availability === 'task_idle') return 'task_idle';
    if (_isAwaitingUser(s)) return 'your_turn';
    if (s.working) return 'working';
    return 'idle';
}

/** "Waiting on Lead Developer", "Waiting on task #12", from a registered board wait. */
function waitingOnLabel(w) {
    if (!w) return '';
    const id = String(w.target_id || '').trim();
    switch (w.wait_type) {
        case 'message': return id ? `Waiting on ${id}` : 'Waiting on a board message';
        case 'task': return id ? `Waiting on task #${id}` : 'Waiting on a task update';
        case 'commit': return id ? `Waiting on commit ${id.slice(0, 12)}` : 'Waiting on a commit';
        default: return id ? `Waiting on ${id}` : '';
    }
}

export function sessionStateInfo(s, opts) {
    const key = deriveSessionState(s, opts);
    const info = { key, ...SESSION_STATES[key] };
    // Asleep because its terminal session is gone, not because anyone asked (#3344): the
    // same state, said plainly. The row's "Terminal gone" chip restarts it.
    if (key === 'sleeping' && isGoneAsleep(s)) info.label = 'Terminal gone';
    // Asleep because its CLI exited (#6058): the word is "Ended", in the row and the header, not a plain "Sleeping".
    if (key === 'sleeping' && !s.archived && s.sleep_reason === 'agent_exited') info.label = info.pill = 'Ended';
    if (key === 'waiting_on') info.label = waitingOnLabel(getCachedAgentAvailability(s)?.waiting_on) || info.label;
    // What the agent itself says it waits on (#3481): its permission, question, plan or
    // input request. A prompt Coral only saw on the screen has no kind: plain Needs input.
    // waiting_kind comes from the agent's own event: only an own key of the table counts
    // (never 'constructor' or 'toString' from the prototype; QA's review).
    if (key === 'needs_input' && Object.hasOwn(WAITING_KINDS, s.waiting_kind || '')) {
        info.label = info.pill = `Needs input · ${WAITING_KINDS[s.waiting_kind]}`;
    }
    return info;
}

/** waiting_kind (from the agent's own request) → the word shown after Needs input. */
const WAITING_KINDS = { permission: 'Permission', question: 'Question', plan: 'Plan', elicitation: 'Input', login: 'Sign in' };

function getStateLabel(s) {
    return sessionStateInfo(s).label;
}

/** Class list for the workspace header status dot (same table as the rows). */
export function terminalDotClass(s) {
    return `terminal-status-dot ${sessionStateInfo(s || {}).dot}`;
}

/** The agent header's status dot (#4327, from the #4276 audit): its class, its tip and its accessible name all say the state in the sidebar's
 *  words, so it is not colour only. The word beside it (drawn on touch screens, which have no hover for the tip) is aria-hidden: read once. */
export function applyTerminalDot(dot, s) {
    if (!dot) return;
    const info = sessionStateInfo(s || {});
    setClassName(dot, terminalDotClass(s));
    setTitle(dot, info.label);
    setAttr(dot, 'role', 'img');
    setAttr(dot, 'aria-label', info.label);
    setAttr(document.getElementById('terminal-state-word'), 'data-word', info.label);
}

export function getMobileStatusChip(s) {
    const info = sessionStateInfo(s);
    return { label: info.label, className: info.chip };
}

export function buildSessionTooltip(s) {
    const stateLabel = getStateLabel(s);
    const lastAction = formatStaleness(s.staleness_seconds);
    const goal = s.summary || "No goal set";
    const status = s.status || "No status";
    const branch = (_repoBranch(s.repo_name, s.branch) || "—") + (s.git_state?.is_worktree ? " (worktree)" : "");
    const agent = s.agent_type || "claude";

    let rows = [
        `<tr><td class="tt-label">State</td><td class="tt-value tt-state-${stateLabel.toLowerCase().replace(/ /g, '-')}">${escapeHtml(stateLabel)}</td></tr>`,
        `<tr><td class="tt-label">Last action</td><td class="tt-value">${escapeHtml(lastAction)}</td></tr>`,
        `<tr><td class="tt-label">Goal</td><td class="tt-value">${escapeHtml(goal)}</td></tr>`,
        `<tr><td class="tt-label">Status</td><td class="tt-value">${escapeHtml(status)}</td></tr>`,
        `<tr><td class="tt-label">Branch</td><td class="tt-value">${escapeHtml(branch)}</td></tr>`,
        `<tr><td class="tt-label">Agent</td><td class="tt-value">${escapeHtml(agent)}</td></tr>`,
    ];
    // The model the agent is using now, from the CLI's own signals; no row while it is unknown (#4589).
    const modelName = modelDisplayName(s.model, s.model_display);
    if (modelName) rows.push(`<tr><td class="tt-label">Model</td><td class="tt-value" title="${escapeAttr(typeof s.model === 'string' ? s.model.trim() : '')}">${escapeHtml(modelName)}</td></tr>`);
    // The thinking (effort) level, as the chat bar's chip says it; "requested" while only the level it was started with is known; no row when none is on record.
    const level = sessionLevel(s);
    if (level) rows.push(`<tr><td class="tt-label">Thinking</td><td class="tt-value">${escapeHtml(level.name)}${level.requested ? ' (as started)' : ''}</td></tr>`);
    // Context window usage moved out of the row (AGENT_LIST_COMPACT): explicit
    // null (unknown model) must read "unknown", never 0% or full.
    const ctxText = (s.context_pct === null || s.context_pct === undefined)
        ? 'unknown'
        : (s.context_window ? `${s.context_pct}% of ${formatTokens(s.context_window)}` : `${s.context_pct}%`);
    rows.push(`<tr><td class="tt-label">Context</td><td class="tt-value">${escapeHtml(ctxText)}</td></tr>`);
    if (s.working_directory) {
        rows.push(`<tr><td class="tt-label">Directory</td><td class="tt-value" title="${escapeAttr(s.working_directory)}">${escapeHtml(_shortPath(s.working_directory, 3))}</td></tr>`);
    }
    if (s.changed_file_count > 0) {
        rows.push(`<tr><td class="tt-label">Files changed</td><td class="tt-value">${s.changed_file_count}</td></tr>`);
    }
    if (s.token_cost_usd > 0 || s.token_input > 0) {
        const tokIn = formatTokens(s.token_input || 0);
        const tokOut = formatTokens(s.token_output || 0);
        const tokCache = (s.token_cache_read || 0);
        let tokenText = `${tokIn} in / ${tokOut} out`;
        if (tokCache > 0) tokenText += ` / ${formatTokens(tokCache)} cache`;
        if (s.token_cost_usd > 0) tokenText += ` · ${_formatCost(s.token_cost_usd)}`;
        rows.push(`<tr><td class="tt-label">Tokens</td><td class="tt-value">${tokenText}</td></tr>`);
    }
    if (s.board_project) {
        const ttUnread = sessionUnreadCount(s);
        const unreadBadge = ttUnread > 0 ? ` <span class="tt-unread">(${ttUnread} unread)</span>` : '';
        rows.push(`<tr><td class="tt-label">Board</td><td class="tt-value">${escapeHtml(keyLabel(sessionTeamKey(s)))}${unreadBadge}</td></tr>`);
        rows.push(`<tr><td class="tt-label">Role</td><td class="tt-value">${escapeHtml(s.board_job_title)}</td></tr>`);
    }
    // Automated messages Coral holds for this agent: board nudges and messages wait for its
    // turn to end, and any send waits while a prompt or menu is open (#3578).
    const held = sessionHeldCount(s);
    if (held > 0) {
        rows.push(`<tr><td class="tt-label">Waiting</td><td class="tt-value tt-held">${held} automated message${held === 1 ? '' : 's'} held until the agent is free</td></tr>`);
    }
    return `<table class="session-tooltip-table">${rows.join("")}</table>`;
}

export function getDotClass(s) {
    return sessionStateInfo(s).dot;
}

export const _WORKTREE_ICON = '<svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M6 3v5a3 3 0 0 0 3 3h1"/><circle cx="6" cy="3" r="1.5"/><circle cx="11" cy="11" r="1.5"/></svg>';

/** Compact "↑2 ↓1 ●3" divergence summary for a session's git_state, or ''.
 *  Ahead/behind are measured against the repo's default branch. */
export function _gitStateSummary(gs) {
    if (!gs) return '';
    const base = gs.base_branch || 'main';
    const parts = [];
    if (gs.ahead) parts.push(`<span class="gs-ahead" title="${escapeAttr(gs.ahead)} commit${gs.ahead !== 1 ? 's' : ''} ahead of ${escapeAttr(base)}">↑${gs.ahead}</span>`);
    if (gs.behind) parts.push(`<span class="gs-behind" title="${escapeAttr(gs.behind)} commit${gs.behind !== 1 ? 's' : ''} behind ${escapeAttr(base)}">↓${gs.behind}</span>`);
    if (gs.dirty) parts.push(`<span class="gs-dirty" title="${escapeAttr(gs.dirty)} uncommitted file${gs.dirty !== 1 ? 's' : ''}">●${gs.dirty}</span>`);
    return parts.join(' ');
}

export function _repoBranch(repoName, branch) {
    if (repoName && branch) return `${repoName} : ${branch}`;
    return branch || repoName || '';
}

export function _shortPath(fullPath, segments = 2) {
    if (!fullPath) return '';
    let p = fullPath.replace(/\/+$/, '');
    // Try to replace common home dir with tilde
    p = p.replace(/^\/Users\/[^\/]+/, '~').replace(/^\/home\/[^\/]+/, '~');
    const parts = p.split('/');
    if (parts.length <= segments) return p;
    return '…/' + parts.slice(-segments).join('/');
}

/** held_inputs (automated sends the gate holds, #3578) as a safe non-negative integer. */
 function sessionHeldCount(s) {
    const n = Number(s && s.held_inputs);
    return Number.isFinite(n) && n > 0 ? Math.min(Math.floor(n), 999) : 0;
}

/** board_unread as a safe, finite, non-negative integer (payloads are untrusted input to markup). */
 export function sessionUnreadCount(s) {
    const n = Number(s && s.board_unread);
    return Number.isFinite(n) && n > 0 ? Math.floor(n) : 0;
}

export function _formatCost(c) {
    return formatCost(c);
}
