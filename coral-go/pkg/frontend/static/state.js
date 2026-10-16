import { readDrafts, writeDrafts } from './local_store.js';

// Drafts are kept with when each was last typed in (local_store.js ages them out, #3681); the
// page itself works with the text only (state.sessionInputText).
let _draftAt = {};
let _draftsLoaded = false;

/** Load the stored drafts (once the store knows whose they are, and after an age-out). */
export function loadSessionDrafts(drafts = readDrafts()) {
    state.sessionInputText = {};
    _draftAt = {};
    _draftsLoaded = true;
    for (const [k, d] of Object.entries(drafts)) {
        state.sessionInputText[k] = d.t;
        _draftAt[k] = d.at;
    }
}

export function saveSessionDraft(key, text) {
    if (!key) return;
    if (!_draftsLoaded) loadSessionDrafts(); // never write over drafts not read yet
    if (text) {
        state.sessionInputText[key] = text;
        _draftAt[key] = Date.now();
    } else {
        delete state.sessionInputText[key];
        delete _draftAt[key];
    }
    const out = {};
    for (const [k, t] of Object.entries(state.sessionInputText)) out[k] = { t, at: _draftAt[k] || Date.now() };
    writeDrafts(out);
}

export const state = {
    currentSession: null,       // { type: "live"|"history", name: string, agent_type?: string, session_id?: string }
    coralWs: null,             // WebSocket for coral updates
    captureInterval: null,      // interval ID for auto-refreshing capture
    autoScroll: true,
    isSelecting: false,         // true when user has text selected; pauses DOM updates
    liveSessions: [],           // cached live session list
    historySessionsList: [],    // cached history session list (from last paginated fetch)
    currentCommands: {},        // commands for current session's agent type
    sessionInputText: {},       // per-session draft text: { "sessionKey": "partial text" } (loadSessionDrafts)
    currentAgentTasks: [],      // tasks for the currently selected live agent
    currentSubagents: [],       // subagents launched by the currently selected live agent
    currentAgentNotes: [],      // user notes for the currently selected live agent
    currentAgentEvents: [],     // events for the currently selected live agent
    eventFiltersHidden: null,   // Set of hidden filter keys (lazily initialized)
    settings: {},               // cached global user settings from /api/settings
    prevWaitingState: {},       // tracks previous waiting_for_input per session_id for toast notifications
    killedSessions: {},         // sessionId -> session data for killed agents (preserved for history links)
    hub: false,                 // server runs in hub mode (from /api/health, read once at startup)
    currentBoardServer: 'local', // server id of the open team/board context ("local" = the hub)
    servers: [],                // [{id,label,status}] from the live list when remotes are registered
};

export function sessionKey(session) {
    if (!session) return null;
    // Use session_id as the key when available (unique per session)
    if (session.session_id) return `${session.type}:${session.session_id}`;
    // Name-only sessions: key on (server, name). Local keys stay `type:name` so
    // drafts saved before the hub existed still load.
    const server = session.server;
    if (server && server !== 'local') return `${session.type}:@${server}/${session.name}`;
    return `${session.type}:${session.name}`;
}

export const CAPTURE_REFRESH_MS = 500;
// While the terminal socket is up the output arrives on that socket; the poll then only keeps tasks, events and
// the Mode label fresh, so it runs at this slower pace. 500 ms returns the moment the socket is down.
export const CAPTURE_REFRESH_STREAMING_MS = 3000;
