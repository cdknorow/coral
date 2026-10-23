/* Which view the dashboard shows (moved out of utils.js, #5065; no behaviour change). */
import { checkAgentView } from './agent_view.js';
import { setDockPage } from './dock_page.js';
import { state } from './state.js';
import { isCloud, normServer, LOCAL_SERVER } from './server_base.js';

const VIEW_IDS = [
    "mobile-agent-list",
    "welcome-screen",
    "chats-empty-view",
    "live-session-view",
    "history-session-view",
    "scheduler-view",
    "messageboard-view",
    "workflows-view",
    "cost-dashboard-view",
    "timeline-view",
    "kanban-view",
    "docs-view",
    "servers-view",
    "inbox-view",
    "artifacts-view",
];

const VIEW_DISPLAY = {
    "mobile-agent-list": "flex",
    "welcome-screen": "flex",
    "chats-empty-view": "flex",
    "live-session-view": "flex",
    "history-session-view": "flex",
    "scheduler-view": "block",
    "messageboard-view": "flex",
    "workflows-view": "block",
    "cost-dashboard-view": "flex",
    "timeline-view": "flex",
    "kanban-view": "flex",
    "docs-view": "block",
    "servers-view": "block",
    "inbox-view": "flex",
    "artifacts-view": "flex",
};

const FULL_WIDTH_VIEWS = new Set([
    "cost-dashboard-view",
    "timeline-view",
    "kanban-view",
    "workflows-view",
    "servers-view",
    "inbox-view",
    "artifacts-view",
]);

let _viewChanges = 0;

/** How many times showView has run: a view that loads asynchronously compares it to tell whether another view was shown meanwhile. */
export function viewChanges() { return _viewChanges; }

/** Build the welcome screen from its template (once). A phone shows the agent list as its home and never builds it (#4497); a wide window builds it at start. */
export function buildWelcome() {
    const host = document.getElementById('welcome-screen');
    const tpl = document.getElementById('welcome-template');
    if (!host || !tpl) return false;
    host.appendChild(tpl.content.cloneNode(true));
    tpl.remove();
    document.dispatchEvent(new CustomEvent('coral:welcome-built'));
    return true;
}

export function showView(activeId) {
    _viewChanges++;
    if (activeId === 'welcome-screen') buildWelcome();
    for (const id of VIEW_IDS) {
        const el = document.getElementById(id);
        if (el) el.style.display = id === activeId ? VIEW_DISPLAY[id] : "none";
    }
    const layout = document.querySelector('.layout');
    if (layout) layout.classList.toggle('sidebar-hidden', FULL_WIDTH_VIEWS.has(activeId));
    document.body.classList.toggle('live-session-active', activeId === 'live-session-view');
    // Which CLI the agent page on screen is, for the lines that are about one CLI only (the "Claude is not signed in" line, #6070): its type, or "remote" for an agent on another computer or seen through
    // Coral Hub (this computer's CLI says nothing about it); empty off an agent's page. Plain CSS reads it, so no listener is needed at startup.
    const open = activeId === 'live-session-view' ? state.currentSession : null;
    const cli = !open || open.type !== 'live' || !open.agent_type ? '' : (isCloud() || normServer(open.server) !== LOCAL_SERVER) ? 'remote' : open.agent_type;
    if ((document.body.getAttribute('data-agent-cli') || '') !== cli) document.body.setAttribute('data-agent-cli', cli); // (written only when it changes: the startup sequence does not gain a write)
    setDockPage(activeId); // the right sidebar follows the kind of page: its tabs and its own open state (#4539)
    checkAgentView(); // the agent's pollers and terminal socket follow whether its page is on screen (#3719)
}
