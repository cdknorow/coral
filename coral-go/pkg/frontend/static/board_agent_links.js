/* Opening an agent's session from a board message (moved out of message_board.js, #5065; no behaviour change). */
import { state } from './state.js';
import { sessionServer } from './server_base.js';
import { showToast } from './utils.js';

export function navigateToAgentSession(sessionName, displayName) {
    if (sessionName) {
        // session_name format is "<agentType>-<uuid>" — extract the UUID
        const dashIdx = sessionName.indexOf('-');
        const sessionId = dashIdx >= 0 ? sessionName.substring(dashIdx + 1) : sessionName;
        // Try live session first, fall back to history
        const live = (state.liveSessions || []).find(s => s.session_id === sessionId);
        if (live) {
            window.selectLiveSession(live.name, live.agent_type, live.session_id, sessionServer(live));
        } else {
            window.selectHistorySession(sessionId);
        }
        return;
    }
    // Fallback: search by display name
    navigateToAgentHistoryByName(displayName);
}

async function navigateToAgentHistoryByName(displayName) {
    const resp = await fetch(`/api/sessions/history?page_size=50`);
    const data = await resp.json();
    const sessions = data.sessions || [];
    const match = sessions.find(s => s.display_name === displayName || s.agent_name === displayName);
    if (match) {
        window.selectHistorySession(match.session_id);
    } else {
        showToast(`No history found for ${displayName}`);
    }
}
