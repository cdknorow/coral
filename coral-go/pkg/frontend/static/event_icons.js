/* The icons, titles and filter groups for an agent's events, shared by the live Activity view (agentic_state.js) and the History
 * tab (history_tabs.js) so both show the same ones (#3832; History had an older copy without the auto-accept events). */

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

// What Coral did for an agent with Auto-accept on (#3182): approved a prompt, paused itself, left one for the user, or
// turned it off because the agent restarted, resumed or woke; and every time it was turned on or off.
const AUTO_ACCEPT_EVENTS = ['auto_accept', 'auto_accept_paused', 'auto_accept_skipped', 'auto_accept_cleared', 'auto_accept_enabled', 'auto_accept_disabled'];

// Filter groups — maps display label to the set of tool_names/event_types it covers
export const FILTER_GROUPS = [
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
    { key: 'autoaccept', label: '&#xe877;', title: 'Auto-accept',     cls: 'tool-auto-accept', match: ev => AUTO_ACCEPT_EVENTS.includes(ev.event_type) },
    { key: 'pulse',      label: '&#xe87e;', title: 'Pulse (other)',   cls: 'tool-pulse',      match: ev => ev.event_type && !ev.tool_name && !['thinking','status','goal','confidence','stop','notification', ...AUTO_ACCEPT_EVENTS].includes(ev.event_type) },
];

// Icons for agent event types
const EVENT_ICONS = {
    thinking:     { char: '&#xea4a;', cls: 'tool-thinking',   title: 'Thinking' },
    status:       { char: '&#xe8b8;', cls: 'tool-status',     title: 'Status' },
    goal:         { char: '&#xe153;', cls: 'tool-goal',       title: 'Goal' },
    confidence:   { char: '&#xe8e8;', cls: 'tool-confidence', title: 'Confidence' },
    stop:         { char: '&#xe002;', cls: 'tool-stop',       title: 'Stop' },
    notification: { char: '&#xe7f4;', cls: 'tool-notification', title: 'Notification' },
    auto_accept:         { char: '&#xe877;', cls: 'tool-auto-accept', title: 'Auto-approved' },
    auto_accept_paused:  { char: '&#xe034;', cls: 'tool-auto-accept-paused', title: 'Auto-accept paused' },
    auto_accept_skipped: { char: '&#xe8fd;', cls: 'tool-auto-accept-skipped', title: 'Left for you to answer' },
    auto_accept_cleared: { char: '&#xe034;', cls: 'tool-auto-accept-skipped', title: 'Auto-accept turned off' },
    auto_accept_enabled: { char: '&#xe877;', cls: 'tool-auto-accept-paused', title: 'Auto-accept turned on' },
    auto_accept_disabled: { char: '&#xe034;', cls: 'tool-auto-accept-skipped', title: 'Auto-accept turned off' },
};

export function getToolIcon(toolName, eventType) {
    // Check agent event types first
    const eventIcon = EVENT_ICONS[eventType];
    if (eventIcon) return { ...eventIcon };
    // Tool-based events
    const icon = TOOL_ICONS[toolName];
    if (icon) return { ...icon, title: toolName };
    // Generic fallback for unknown event types
    if (eventType && !toolName) {
        const label = eventType.charAt(0).toUpperCase();
        const title = eventType.charAt(0).toUpperCase() + eventType.slice(1);
        return { char: '&#xe87e;', cls: 'tool-pulse', title };
    }
    return { char: '&#xe061;', cls: 'tool-default', title: toolName || eventType || 'Event' };
}

/** Whether an event shows with these filter keys hidden (a Set); no hidden filter shows everything. */
export function eventVisible(ev, hidden) {
    if (hidden.size === 0) return true;
    for (const group of FILTER_GROUPS) {
        if (hidden.has(group.key) && group.match(ev)) return false;
    }
    return true;
}

/** The History tab's events and its hidden filters, shared with the filter dropdown in agentic_state.js (one dropdown serves both
 *  views). Mutated in place, never reassigned, so a holder always sees the current ones. */
export const historyEventsView = { hidden: new Set(), events: [] };
if (typeof window !== 'undefined') (window.__coral ||= {}).historyFilterHidden = historyEventsView.hidden; // the CDP suites read it
