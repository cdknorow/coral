/* Which board and project the task views act on (moved out of tasks.js, #4996; no behaviour change). */
import { state } from './state.js';
import { boardOfPage } from './dock_page.js';
import { sessionServer, identityKey } from './server_base.js';

// An agent that is not on a team board: tasks created for it are agent
// tasks (its own list in Coral), not board tasks.
export function _isSoloAgent() {
    if (state.selectedTeam || boardOfPage()) return false;
    return !!state.currentSession && state.currentSession.type === 'live' && !state.currentSession.board_project;
}

// Team key (server + board) of the selected agent; a solo agent uses its own name as the board.
export function _currentBoardKey() {
    const cs = state.currentSession;
    if (!cs) return null;
    const board = cs.board_project || cs.name;
    return board ? identityKey(sessionServer(cs), board) : null;
}

export function _getBoardProject() {
    const pageBoard = boardOfPage();
    if (pageBoard) return pageBoard;
    if (state.selectedTeam) return state.selectedTeam;
    if (!state.currentSession) return null;
    return _currentBoardKey();
}
