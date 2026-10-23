/* Team context: selecting a team swaps the workspace to team-level views.
 *
 *   center:  the team's group chat (board chat) over the chat/terminal
 *   sidebar: Team View, Team Settings, Team Knowledge, Team Artifacts, Tasks, Notes
 *            (agent-only tabs such as Files, Activity and Agent UI are hidden)
 *
 * Selecting an agent leaves the team context. The agent's chat stays mounted
 * underneath, so closing the team view returns to it unchanged.
 */
import { isPhoneLayout } from './phone_layout.js';
import { importOnce } from './import_once.js';
import { state } from './state.js';
import { sessionTeamKey, splitKey } from './server_base.js';
import { showView } from './utils.js';
import { showTeamAvailability } from './team_availability.js';
const { showTeamWorkingMode, hasUnsavedTeamSettings, confirmTeamWorkspaceChange } = await importOnce('./team_working_mode.js'); // lazy modules load the others they need through importOnce, so a failed load is retried
import { syncFilesSourceTeam } from './team_artifacts.js';
import { setPreviewSession } from './preview_pane.js';
import { renderLiveSessions, showBoardChatTab, hideBoardChatTab, getActiveBoardChat } from './render.js';
import { note, currentRoute } from './nav.js';
import { keyRoute, liveRoute } from './routes.js';

/** The team on screen changed (entered or left, with or without a route): the Saved list and its count are of the agent or team shown (#4792). */
function contextChanged() { const w = document.defaultView || window; document.dispatchEvent(new w.CustomEvent('coral:route-changed')); }

 function getSelectedTeam() {
    return state.selectedTeam || null;
}

function sidebar() {
    return document.getElementById('agentic-state');
}

function refreshSessionList() {
    if (state.liveSessions?.length) renderLiveSessions(state.liveSessions);
}

function activeTabName() {
    const el = document.querySelector('#agentic-state .agentic-tab.active');
    return el ? el.id.replace('agentic-tab-', '') : null;
}

/** Mount the team's group chat (the board chat panel) in the center column. */
function mountGroupChat(team) {
    const host = document.getElementById('team-center-view');
    const panel = document.getElementById('agentic-panel-board');
    if (!host || !panel) return;
    // The panel keeps its content when moved, so only rebuild it for a different board.
    if (getActiveBoardChat() !== team) showBoardChatTab(team);
    if (panel.parentElement !== host) host.appendChild(panel);
    if (!host.querySelector('.team-center-close')) {
        const close = document.createElement('button');
        close.type = 'button';
        close.className = 'team-center-close';
        close.title = 'Close team view';
        close.setAttribute('aria-label', 'Close team view');
        close.textContent = '×';
        close.onclick = () => exitTeamContext();
        host.appendChild(close);
    }
}

/** Show the newest message: moving the panel resets its scroll, and a chat with nothing new does not re-render. */
function scrollGroupChatToNewest() {
    requestAnimationFrame(() => {
        const msgs = document.getElementById('board-panel-msgs');
        if (msgs) msgs.scrollTop = msgs.scrollHeight;
    });
}

/** Put the board chat panel back in the sidebar and restore the selected agent's board. */
function unmountGroupChat() {
    const host = document.getElementById('team-center-view');
    const panel = document.getElementById('agentic-panel-board');
    host?.querySelector('.team-center-close')?.remove();
    if (panel && host && panel.parentElement === host) document.getElementById('agentic-block-top')?.appendChild(panel);
    // In the center the panel is shown whatever its class, and the sidebar's tab switches never
    // reach it, so it can come back marked active beside another tab (Tasks): match the sidebar.
    if (panel) panel.classList.toggle('active', activeTabName() === 'board');
    const board = state.currentSession?.type === 'live' ? sessionTeamKey(state.currentSession) : null;
    if (board && getActiveBoardChat() === board) return; // same board: keep what is already loaded
    if (board) showBoardChatTab(board);
    else hideBoardChatTab();
}

/**
 * Select a team. opts.tab picks the sidebar tab (default: Board chat);
 * opts.restore replaces the history entry instead of pushing one; opts.silent writes none (the
 * router is showing the team's page). Back and Forward are nav.js's (#3532).
 * Returns false if the user declined to discard unsaved team settings.
 */
export async function enterTeamContext(team, opts = {}) {
    if (!team) return false;
    const current = getSelectedTeam();
    if (current && current !== team && hasUnsavedTeamSettings() && !(await confirmTeamWorkspaceChange())) return false;

    state.selectedTeam = team;
    state.currentBoardServer = splitKey(team).server;
    // Previews opened from the team keep their own tabs, separate from any agent's.
    setPreviewSession('team:' + team);
    // The pane needs the live view mounted even when no agent is selected yet.
    const view = document.getElementById('live-session-view');
    if (view && view.style.display === 'none') showView('live-session-view');

    sidebar()?.classList.add('team-context');
    sidebar()?.classList.remove('collapsed');
    document.getElementById('agentic-collapse-btn')?.classList.remove('collapsed');

    const tab = opts.tab || (activeTabName() && document.querySelector(`#agentic-tab-${activeTabName()}[data-scope="team"]`) ? activeTabName() : 'team-view');
    if (!opts.silent) note(keyRoute('team', team, tab === 'team-settings' ? 'settings' : null), { replace: !!opts.restore || current === team });

    showTeamAvailability(team, { workspace: true });
    mountGroupChat(team);
    document.getElementById('team-center-view')?.removeAttribute('hidden');
    scrollGroupChatToNewest();
    syncFilesSourceTeam();
    window.switchAgenticTab?.(tab, 'top');
    // On a phone the side panel is an overlay that starts shut: a team page that asks for one of its panel's tabs (Team settings, Knowledge) opens it, or the tab is drawn at no size behind the chat (#5131).
    if (tab !== 'team-view' && isPhoneLayout()) window.toggleAgenticPanel?.(true);
    refreshSessionList();
    contextChanged();
    return true;
}

/** Leave the team context and return to the selected agent. Resolves false if the user declined to discard unsaved team settings. */
export async function exitTeamContext({ goBack = true, force = false } = {}) {
    if (!getSelectedTeam()) return true;
    // The dialog is only awaited when there is something unsaved, so a plain exit still happens at once.
    if (!force && hasUnsavedTeamSettings() && !(await confirmTeamWorkspaceChange())) return false;

    state.selectedTeam = null;
    setPreviewSession(state.currentSession?.type === 'live' ? state.currentSession.session_id : null);
    document.getElementById('team-center-view')?.setAttribute('hidden', '');
    unmountGroupChat();
    document.getElementById('agentic-panel-team-view')?.replaceChildren();
    document.getElementById('agentic-panel-team-settings')?.replaceChildren();
    sidebar()?.classList.remove('team-context');

    // A team-only tab cannot stay active without a team.
    const active = activeTabName();
    if (active && document.querySelector(`#agentic-tab-${active}[data-scope="team"]`)) {
        const hasBoard = !!sessionTeamKey(state.currentSession);
        window.switchAgenticTab?.(hasBoard ? 'board' : 'files', 'top');
    }
    syncFilesSourceTeam();
    refreshSessionList();
    // Closing the team (×) goes to the agent underneath: its page, or the Agents page.
    if (goBack && currentRoute()?.view === 'team') {
        const s = state.currentSession;
        note(s?.type === 'live' && s.session_id ? liveRoute(s.server, s.session_id) : { view: 'agents' });
    }
    contextChanged();
    return true;
}

window.exitTeamContext = exitTeamContext;

/** Render the sidebar's Team Settings tab when it is opened. */
export function renderTeamSettingsTab() {
    const team = getSelectedTeam();
    const panel = document.getElementById('agentic-panel-team-settings');
    if (!team || !panel) return;
    if (panel.querySelector('#team-settings-workspace .working-mode-team')?.dataset.team === team) return;
    showTeamWorkingMode(team, { workspace: true });
}
