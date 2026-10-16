// Whether the selected agent's page is on screen (#3719). Everything the dashboard polls for the selected
// agent (its chat, pending tool, prompt options, pane capture, Agent UI panels) and its terminal socket
// are only worth having while that page is showing: on a phone, Back to the agent list used to leave all of
// it running with nothing to show (3 requests a second and an open WebSocket), and so did any other view
// (Chats, Jobs, Workflows) and a hidden tab.
//
// showView (utils.js) and the tab's visibility call checkAgentView; when the answer changes it announces
// 'coral:agent-view-hidden' or 'coral:agent-view-shown' on document. A poller asks agentViewVisible() each
// tick (pollEvery's agentView option); the terminal closes its socket on hidden and reopens on shown.

/** True when the agent page is the view on screen: the live-session view is showing, the tab is visible,
 *  or this is an agent's own popout window (which has nothing else to show). */
export function agentViewVisible() {
    if (typeof document === 'undefined' || typeof document.getElementById !== 'function') return true;
    if (document.hidden) return false;
    if (document.body?.classList?.contains('popout-mode')) return true;
    const view = document.getElementById('live-session-view');
    return !view || view.style.display !== 'none'; // no such element: nothing hides the agent page
}

/** Listen for one of this module's events (and the terminal's). A no-op without a real document (the unit tests' stand-in). */
export function onAgentView(type, fn) {
    if (typeof document !== 'undefined' && typeof document.addEventListener === 'function') document.addEventListener(type, fn);
}

/** Announce an event (the same guard as above). */
export function fireAgentView(type) {
    if (typeof document !== 'undefined' && typeof document.dispatchEvent === 'function' && typeof CustomEvent === 'function') document.dispatchEvent(new CustomEvent(type));
}

let last = null;

/** Recompute and announce a change. The first call only records the starting state. */
export function checkAgentView() {
    const now = agentViewVisible();
    const before = last;
    last = now;
    if (before === null || before === now) return;
    fireAgentView(now ? 'coral:agent-view-shown' : 'coral:agent-view-hidden');
}

onAgentView('visibilitychange', checkAgentView);
