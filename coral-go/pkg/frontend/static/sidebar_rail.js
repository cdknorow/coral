/* The collapsed sidebar's rail of agent tiles (moved out of render.js, #4996; no behaviour change). */
import { sessionServer, keyLabel, sessionTeamKey, sessionFolderKey } from './server_base.js';
import { escapeHtml, escapeAttr, agentNameColor } from './utils.js';
import { isStaleSession, staleReason, showServerLabels, serverLabel } from './server_status.js';
import { getDotClass, resolveSessionIdentity, sessionStateInfo, sessionUnreadCount } from './session_state.js';
import { state } from './state.js';
import { morphHtml } from './dom_morph.js';
import { _getBoardColor } from './board_accents.js';
import { _getGroupOrder, _sortByOrder, _sortGroups } from './list_order.js';
import { _isGroupCollapsed } from './group_collapse.js';
import { browserLocal } from './safe_storage.js';

function _getInitials(name) {
    if (!name) return '?';
    const words = name.trim().split(/\s+/);
    if (words.length >= 2) return (words[0][0] + words[1][0]).toUpperCase();
    return name.slice(0, 2).toUpperCase();
}

const _RAIL_ICONS = {
    team: '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="9" cy="7" r="3"/><path d="M3 21v-2a6 6 0 0 1 12 0v2M16 4a3 3 0 0 1 0 6M21 21v-2a6 6 0 0 0-4-5.65"/></svg>',
    folder: '<svg width="13" height="13" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2 4v8a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1V6a1 1 0 0 0-1-1H8L6.5 3H3a1 1 0 0 0-1 1z"/></svg>',
    workflow: '<span class="material-icons" style="font-size:0.8125rem" aria-hidden="true">account_tree</span>',
};

// Role keyword -> Material icon for the rail tiles (first match wins).
const _RAIL_ROLE_ICONS = [
    ['orchestrator', 'hub'], ['architect', 'architecture'], ['design', 'palette'], ['artist', 'palette'],
    ['devops', 'settings'], ['backend', 'dns'], ['frontend', 'code'], ['qa', 'bug_report'], ['test', 'bug_report'],
    ['review', 'rate_review'], ['lead', 'construction'], ['dev', 'code'], ['research', 'travel_explore'],
];

function _railRoleIcon(s) {
    if (s.agent_type === 'terminal') return 'terminal';
    const text = `${s.display_name || ''} ${s.board_job_title || ''}`.toLowerCase();
    const hit = _RAIL_ROLE_ICONS.find(([k]) => text.includes(k));
    return hit ? hit[1] : '';
}

function _railTile(s, active) {
    const label = resolveSessionIdentity(s) + (s.board_project ? ` · ${keyLabel(sessionTeamKey(s))}` : '');
    const dot = getDotClass(s);
    const unread = sessionUnreadCount(s);
    const icon = s.icon && !s.sleeping
        ? `<span class="rail-tile-emoji">${escapeHtml(s.icon)}</span>`
        : (_railRoleIcon(s) ? `<span class="material-icons rail-tile-icon" aria-hidden="true">${_railRoleIcon(s)}</span>` : '');
    let badge = '';
    if (unread > 0) badge = `<span class="rail-tile-badge rail-tile-unread" aria-hidden="true">${unread > 9 ? '9+' : unread}</span>`; // the tile's name says the count (below)
    else if (dot === 'done') badge = '<span class="rail-tile-badge rail-tile-done material-icons" aria-hidden="true">check</span>';
    // The dot is colour only: the tile's own name says the state, in the words the sidebar uses, so it is read once and the dot is skipped (#4276).
    else badge = `<span class="rail-tile-dot avatar-status-dot ${dot}" aria-hidden="true"></span>`;
    const railStale = isStaleSession(s);
    const railLabel = railStale ? `${label}: ${staleReason(s)}` : `${showServerLabels() ? `${label} · ${serverLabel(sessionServer(s))}` : label}, ${sessionStateInfo(s).label}${unread > 0 ? `, ${unread} unread` : ''}`;
    return `<button type="button" class="sidebar-rail-item${active ? ' active' : ''}${railStale ? ' session-stale' : ''}" title="${escapeAttr(railLabel)}" aria-label="${escapeAttr(railLabel)}"${railStale ? ' disabled' : ''}
        ${railStale ? '' : `data-on-click="session-open" data-name="${escapeAttr(s.name)}" data-agent-type="${escapeAttr(s.agent_type)}" data-session-id="${escapeAttr(s.session_id || '')}" data-server="${escapeAttr(sessionServer(s))}"`}><span class="rail-tile"${agentNameColor(s) ? ` style="--agent-name-color:${escapeAttr(agentNameColor(s))}"` : ''}>${icon}<span class="rail-tile-text">${escapeHtml(_getInitials(resolveSessionIdentity(s)))}</span></span>${badge}</button>`;
}

/** Icon rail shown when the sidebar is collapsed: a team/folder header per
 *  group (click to expand or collapse, same state as the full list) with the
 *  awake agents beneath it. Sleeping agents are left out. */
export function _renderSidebarRail(sessions) {
    const rail = document.getElementById('sidebar-rail-list');
    if (!rail) return;
    const groupByTeam = browserLocal.get('coral-group-by-team') !== 'false';

    // Same group and agent order as the full list, so both views line up.
    const isOrchSession = s => (s.display_name || s.board_job_title || '').toLowerCase().includes('orchestrator');
    const groups = []; // { kind, label, key, agents } in rendered order
    const bucket = (pick) => {
        const map = {};
        for (const s of sessions) {
            const k = pick(s);
            if (k !== null) (map[k] = map[k] || []).push(s);
        }
        return Object.entries(map);
    };
    // Awake agents in the order their rows render.
    const awakeInRowOrder = (agents) => agents.filter(a => !a.sleeping);

    if (groupByTeam) {
        const teams = _sortGroups(bucket(s => sessionTeamKey(s) || null));
        if (!_getGroupOrder().length) {
            teams.sort((a, b) => (a[1].every(x => x.sleeping) ? 1 : 0) - (b[1].every(x => x.sleeping) ? 1 : 0));
        }
        for (const [name, list] of teams) {
            const ordered = _sortByOrder(list).sort((a, b) => isOrchSession(b) - isOrchSession(a));
            groups.push({ kind: 'team', key: name, label: keyLabel(name), agents: awakeInRowOrder(ordered, list[0]?.working_directory || '') });
        }
        for (const [name, list] of bucket(s => (!s.board_project && s.workflow_name) ? s.workflow_name : null)) {
            groups.push({ kind: 'workflow', key: 'wf:' + name, label: name, agents: list.filter(a => !a.sleeping) });
        }
    }
    const folders = _sortGroups(bucket(s => (groupByTeam && (s.board_project || s.workflow_name)) ? null : sessionFolderKey(s)));
    for (const [name, list] of folders) {
        const ordered = _sortByOrder(list);
        groups.push({ kind: 'folder', key: name, label: keyLabel(name), agents: awakeInRowOrder(ordered, ordered[0]?.working_directory || '') });
    }

    let html = '';
    for (const g of groups) {
        if (!g.agents.length) continue;
        const collapsed = _isGroupCollapsed(g.key);
        const letter = (g.label.trim()[0] || '?').toUpperCase();
        // The same colours as the full list (#3507): a team shows the colour the user picked for it,
        // and every other team, folder and workflow the same neutral chip.
        const accent = g.kind === 'team' ? _getBoardColor(g.key) : null;
        const style = accent ? ` style="--team-accent:${escapeAttr(accent)}"` : '';
        const title = `${g.label} (${g.agents.length}) — click to ${collapsed ? 'expand' : 'collapse'}`;
        html += `<button type="button" class="sidebar-rail-group sidebar-rail-group-${g.kind}${collapsed ? ' collapsed' : ''}"${style} title="${escapeAttr(title)}" aria-expanded="${!collapsed}" aria-label="${escapeAttr(title)}"
            data-on-click="group-collapse" data-group="${escapeAttr(g.key)}">${_RAIL_ICONS[g.kind]}<span class="sidebar-rail-letter">${escapeHtml(letter)}</span></button>`;
        if (collapsed) continue;
        for (const s of g.agents) {
            const active = state.currentSession?.type === 'live' && state.currentSession.session_id === s.session_id;
            html += _railTile(s, active);
        }
    }
    morphHtml(rail, html); // unchanged rail entries keep their nodes (#3121)
}
