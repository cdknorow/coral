/* The selected agent's name and the query string its file routes take, shared by the file preview pane and the changed-files
 * list (#3794; both had the same two functions). */
import { state } from './state.js';

/** The selected live agent's name, or undefined. */
export function agentName() {
    return state.currentSession && state.currentSession.name;
}

/** A query for the agent's file routes: the path, and the session id when there is one. */
export function apiQs(filepath) {
    const qs = new URLSearchParams({ filepath });
    const sid = state.currentSession && state.currentSession.session_id;
    if (sid) qs.set('session_id', sid);
    return qs;
}
