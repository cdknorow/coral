/* Pieces the agent list's team and folder cards share: the pin item, the icons, a row's name with its server, the team's accent, Kill All and the health icon (moved out of render.js, #4996; no behaviour change). */
import { launchBlockedReason } from './hub_access.js';
import { splitKey, LOCAL_SERVER, keyLabel } from './server_base.js';
import { escapeAttr, escapeHtml } from './utils.js';
import { isPinnedDir } from './pinned_folders.js';
import { serverLabel, serversShown, statusMeta } from './server_status.js';
import { isGroupedByServer, isServerShownOnRows } from './session_rows.js';
import { _getBoardColor } from './board_accents.js';

// The team menu's "Kill All". Through the Coral Hub, killing a whole team needs Launch on its
// machine (#3433): below that it is shown disabled with the reason (#3443).
export function killAllItem(boardName) {
    const icon = '<svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><line x1="4" y1="4" x2="12" y2="12"/><line x1="12" y1="4" x2="4" y2="12"/></svg>';
    const blocked = launchBlockedReason(splitKey(boardName).server);
    if (blocked) {
        return `<button class="overflow-menu-item overflow-menu-danger overflow-menu-blocked" disabled aria-disabled="true" title="${escapeAttr(blocked)}">
                    ${icon}
                    Kill All <span class="overflow-menu-note">needs Launch</span>
                </button>`;
    }
    return `<button class="overflow-menu-item overflow-menu-danger" data-on-click="team-kill" data-board="${escapeAttr(boardName)}">
                    ${icon}
                    Kill All
                </button>`;
}

// The team's health check, on or off (#4962): an icon beside the team view button, dimmed when off, accent when on, that opens the setting.
// The state is "team_health_on" on the team's rows in the live list; Coral Hub gets none (the check's settings are the local dashboard's), so no icon there.
export function teamHealthBtn(boardName, boardSessions) {
    const row = boardSessions.find(s => typeof s.team_health_on === 'boolean');
    if (!row) return '';
    const label = row.team_health_on ? 'Team health monitor: on' : 'Team health monitor: off';
    return `<button type="button" class="folder-copy-btn team-health-btn${escapeAttr(row.team_health_on ? ' is-on' : '')}" data-on-click="team-health" data-board="${escapeAttr(boardName)}" title="${escapeAttr(label)}" aria-label="${escapeAttr(label)}"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3 12h4l2.5-6 4 12 2.5-6H21"/></svg></button>`;
}

// A folder icon on folder groups, as teams have the people icon (#3507 v4, the operator's
// request): same size and spacing as the team icon, and named for screen readers.
// A folder entry's Pin / Unpin item (#3630): this Coral's folders only.
const PIN_ICON = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M9 4h6l-1 6 3 3v2H7v-2l3-3z"/><path d="M12 15v6"/></svg>';

export function _pinMenuItem(groupName, dir) {
    if (!dir || splitKey(groupName).server !== LOCAL_SERVER) return '';
    const pinned = isPinnedDir(dir);
    return `<button class="overflow-menu-item" data-pin-action="${escapeAttr(pinned ? 'unpin' : 'pin')}" data-on-click="folder-pin" data-mode="${escapeAttr(pinned ? 'unpin' : 'pin')}" data-dir="${escapeAttr(dir)}">${PIN_ICON}${pinned ? 'Unpin folder' : 'Pin folder'}</button>`;
}

export const FOLDER_GROUP_ICON = '<svg class="team-group-icon folder-group-icon" role="img" aria-label="Folder" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor"'
    + ' stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/></svg>';

/** The server before a team/folder name ("pro-coral-hub", the server part in italics), when
 *  'Show server' is on and not grouped (#3507: replaces the green-dot chip after the name). An
 *  offline or stale server is dimmed, and its state is in the tooltip and for screen readers. */
function _rowServerPrefix(key) {
    if (!isServerShownOnRows() || isGroupedByServer() || !serversShown().length) return '';
    const id = splitKey(key).server;
    const e = serversShown().find(x => x.id === id) || { id, label: serverLabel(id), status: 'unknown' };
    const name = serverLabel(id);
    const st = statusMeta(e.status);
    // Not checked yet ('unknown') isn't a failure: shown like an online server (QA's review).
    const down = e.status !== 'online' && e.status !== 'unknown';
    return `<span class="row-server-prefix server-${st.cls}${down ? ' is-down' : ''}" title="${escapeAttr(`${e.label || e.id}: ${st.label}`)}">`
        + `<span class="row-server-name">${escapeHtml(name)}</span><span class="row-server-sep" aria-hidden="true">-</span>`
        + `<span class="sr-only"> (server${down ? ', ' + escapeHtml(st.label.toLowerCase()) : ''}) </span></span>`;
}

/** A team/folder header's name, with its server before it when shown: one inline item, so
 *  the name line's flex gap doesn't split "pro-" from "coral-hub". */
export function _rowNameWithServer(key) {
    const prefix = _rowServerPrefix(key);
    const name = escapeHtml(keyLabel(key));
    return prefix ? `<span class="row-name-with-server">${prefix}${name}</span>` : name;
}

// A team's color: the one the user picked for it (the team's color menu, kept per viewer),
// else none, so every team shows in the same neutral color (#3507, the operator's request;
// teams used to get a color hashed from their name).
export function _boardAccentStyle(name) {
    const custom = _getBoardColor(name);
    return custom ? ` style="--team-accent: ${escapeAttr(custom)}"` : '';
}
