/* Team context: selecting a team swaps the workspace to team-level views.
 *
 *   center:  the team's group chat (board chat) over the chat/terminal
 *   sidebar: Team View, Team Settings, Team Knowledge, Team Artifacts, Tasks, Notes
 *            (agent-only tabs such as Files, Activity and Agent UI are hidden)
 *
 * Selecting an agent leaves the team context. The agent's chat stays mounted
 * underneath, so closing the team view returns to it unchanged.
 */
import { state } from './state.js';
import { sessionTeamKey, splitKey, identityKey } from './server_base.js';
import { showView } from './utils.js';
import { showTeamAvailability } from './team_availability.js';
import { showTeamWorkingMode, hasUnsavedTeamSettings, confirmTeamWorkspaceChange } from './team_working_mode.js';
import { syncFilesSourceTeam } from './team_artifacts.js';
import { setPreviewSession } from './preview_pane.js';
import { renderLiveSessions, showBoardChatTab, hideBoardChatTab, getActiveBoardChat } from './render.js';

let _historyBound = false;
let _boardTeam = null;   // team whose group chat is mounted in the center

export function getSelectedTeam() {
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
    _boardTeam = team;
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

/** Put the board chat panel back in the sidebar and restore the selected agent's board. */
function unmountGroupChat() {
    const host = document.getElementById('team-center-view');
    const panel = document.getElementById('agentic-panel-board');
    host?.querySelector('.team-center-close')?.remove();
    if (panel && host && panel.parentElement === host) document.getElementById('agentic-block-top')?.appendChild(panel);
    _boardTeam = null;
    const board = state.currentSession?.type === 'live' ? sessionTeamKey(state.currentSession) : null;
    if (board && getActiveBoardChat() === board) return; // same board: keep what is already loaded
    if (board) showBoardChatTab(board);
    else hideBoardChatTab();
}

function bindHistory() {
    if (_historyBound) return;
    _historyBound = true;
    window.addEventListener('popstate', () => {
        const h = history.state;
        if (h?.coralTeamContext) enterTeamContext(h.team, { tab: h.tab, restore: true, silent: true });
        else if (getSelectedTeam()) exitTeamContext({ goBack: false });
    });
}

/**
 * Select a team. opts.tab picks the sidebar tab (default: Board chat);
 * opts.restore replaces the history entry instead of pushing one.
 * Returns false if the user declined to discard unsaved team settings.
 */
export async function enterTeamContext(team, opts = {}) {
    if (!team) return false;
    const current = getSelectedTeam();
    if (current && current !== team && !confirmTeamWorkspaceChange()) return false;
    bindHistory();

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
    const hash = tab === 'team-settings' ? `#team-settings=${encodeURIComponent(team)}` : `#team-view=${encodeURIComponent(team)}`;
    if (!opts.silent) {
        const entry = { coralTeamContext: true, team, tab };
        if (opts.restore || (history.state?.coralTeamContext && current === team)) history.replaceState(entry, '', hash);
        else history.pushState(entry, '', hash);
    }

    showTeamAvailability(team, { workspace: true });
    mountGroupChat(team);
    document.getElementById('team-center-view')?.removeAttribute('hidden');
    syncFilesSourceTeam();
    window.switchAgenticTab?.(tab, 'top');
    refreshSessionList();
    return true;
}

/** Leave the team context and return to the selected agent. */
export function exitTeamContext({ goBack = true, force = false } = {}) {
    if (!getSelectedTeam()) return true;
    if (!force && hasUnsavedTeamSettings() && !confirmTeamWorkspaceChange()) return false;

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
    if (goBack && history.state?.coralTeamContext) history.back();
    return true;
}

window.exitTeamContext = exitTeamContext;

/** Called when an agent is selected. */
export function leaveTeamForAgent() {
    return exitTeamContext({ goBack: false });
}

/** Render the sidebar's Team Settings tab when it is opened. */
export function renderTeamSettingsTab() {
    const team = getSelectedTeam();
    const panel = document.getElementById('agentic-panel-team-settings');
    if (!team || !panel) return;
    if (panel.querySelector('#team-settings-workspace .working-mode-team')?.textContent?.trim() === splitKey(team).name) return;
    showTeamWorkingMode(team, { workspace: true });
}
