/* The selected agent's team and server, under its name in the agent header (#3653: on a phone
 * the header was the only place that names the agent, and it said neither). The team shows when
 * the agent is on a board; the server only when this Coral is a hub with remotes, named as the
 * sidebar names it (the local one is "Local", the operator's choice). Both come from server data
 * and are set as textContent only. */

import { keyLabel, noteBoardLabels, onBoardLabelsChanged, sessionServer, sessionTeamKey } from './server_base.js';
import { serverLabel, showServerLabels } from './server_status.js';

/** headerContext returns { team, server } for a session ("" where nothing is shown). */
export function headerContext(s) {
    // The session carries its board's label (board_label): note it first, so the line never shows
    // the board's id while the list that notes labels hasn't rendered this session yet (seen on a
    // slow runner right after a team launch).
    if (s && s.board_project && s.board_label) noteBoardLabels([s]);
    const team = s && s.board_project ? keyLabel(sessionTeamKey(s)) : '';
    const server = s && showServerLabels() ? serverLabel(sessionServer(s)) : '';
    return { team, server };
}

/** updateHeaderContext shows session s's team and server in the agent header. */
let _shown = null;

export function updateHeaderContext(s) {
    _shown = s || null;
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

// A board's label can arrive after the header was filled (the board list loads later): show it then.
onBoardLabelsChanged(() => { if (_shown) updateHeaderContext(_shown); });
