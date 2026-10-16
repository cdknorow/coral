/* The selected agent's team and server, under its name in the agent header (#3653: on a phone
 * the header was the only place that names the agent, and it said neither). The team shows when
 * the agent is on a board; the server only when this Coral is a hub with remotes, named as the
 * sidebar names it (the local one is "Local", the operator's choice). Both come from server data
 * and are set as textContent only. */

import { keyLabel, sessionServer, sessionTeamKey } from './server_base.js';
import { serverLabel, showServerLabels } from './server_status.js';

/** headerContext returns { team, server } for a session ("" where nothing is shown). */
export function headerContext(s) {
    const team = s && s.board_project ? keyLabel(sessionTeamKey(s)) : '';
    const server = s && showServerLabels() ? serverLabel(sessionServer(s)) : '';
    return { team, server };
}

/** updateHeaderContext shows session s's team and server in the agent header. */
export function updateHeaderContext(s) {
    const box = document.getElementById('terminal-header-context');
    if (!box) return;
    const { team, server } = headerContext(s);
    const teamEl = document.getElementById('terminal-header-team');
    const serverEl = document.getElementById('terminal-header-server');
    if (teamEl) teamEl.textContent = team;
    if (serverEl) serverEl.textContent = server;
    box.hidden = !team && !server;
    // The full text when it is cut short, and for a screen reader.
    box.title = [team && `Team: ${team}`, server && `Server: ${server}`].filter(Boolean).join(' · ');
}
