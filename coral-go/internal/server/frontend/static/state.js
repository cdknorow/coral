/* Shared application state */

export const state = {
    currentSession: null,       // { type: "live"|"history", name: string, agent_type?: string, session_id?: string }
    coralWs: null,             // WebSocket for coral updates
    captureInterval: null,      // interval ID for auto-refreshing capture
    autoScroll: true,
    isSelecting: false,         // true when user has text selected; pauses DOM updates
    liveSessions: [],           // cached live session list
    historySessionsList: [],    // cached history session list (from last paginated fetch)
    currentCommands: {},        // commands for current session's agent type
    sessionInputText: {},       // per-session draft text: { "sessionKey": "partial text" }
    currentAgentTasks: [],      // tasks for the currently selected live agent
    currentSubagents: [],       // subagents launched by the currently selected live agent
    currentAgentNotes: [],      // user notes for the currently selected live agent
    currentAgentEvents: [],     // events for the currently selected live agent
    eventFiltersHidden: null,   // Set of hidden filter keys (lazily initialized)
    settings: {},               // cached global user settings from /api/settings
    prevWaitingState: {},       // tracks previous waiting_for_input per session_id for toast notifications
    killedSessions: {},         // sessionId -> session data for killed agents (preserved for history links)
};

export function sessionKey(session) {
    if (!session) return null;
    // Use session_id as the key when available (unique per session)
    if (session.session_id) return `${session.type}:${session.session_id}`;
    return `${session.type}:${session.name}`;
}

export const CAPTURE_REFRESH_MS = 500;
