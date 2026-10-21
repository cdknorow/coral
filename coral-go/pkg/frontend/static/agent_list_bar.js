/* The agent list's filters and sort on the shared list bar (#4744 stage 3, Search everywhere): State, Board and Machine, a Sort, the count and the chips, under the list's own search box in the
 * sidebar and on the phone's Agents page (one bar each, in step). The search text stays the list's own (agent_filter.js); everything here works on the feed the dashboard already holds. */
import { buildListBar } from './list_bar.js';
import { STATES, STATE_LABEL, SORTS, SORT_LABEL, EMPTY, makeModel, comparator } from './agent_list_filters.js';
import { getAgentFilter, getListFilters, setListFilters, setListComparator } from './agent_filter.js';
import { deriveSessionState, resolveSessionIdentity } from './session_state.js';
import { sessionServer, sessionTeamKey, keyLabel, LOCAL_SERVER } from './server_base.js';
import { serverLabel } from './launch_server.js';
import { state } from './state.js';
import { redrawLiveList } from './list_hooks.js';
import { isBranchShown, isAgentLineShown, AGENT_LINE_PARTS, AGENT_LINE_LABEL } from './session_rows.js';
import { toggleShowBranches, toggleAgentLine } from './list_display_settings.js';
import { syncDisplayPanels, inListHeaders, isDisplayPanelOpen, DISPLAY_PANEL_OPEN } from './display_panel.js';
import { setData, setHidden, setAttr } from './dom_write.js';

const HOSTS = [['agent-list-bar', 'al'], ['mobile-agent-list-bar', 'alm']]; // the sidebar's and the phone's
const bars = new Map(); // host element -> its bar
const sigs = new Map(); // host element -> what it last showed

const boardLabel = v => keyLabel(v);
const machineLabel = v => serverLabel(v, state.servers);
const MODEL = makeModel({ boardLabel, machineLabel });

function spec(idp) {
    return {
        idp, noun: 'agent', noSearch: true, inline: true, label: 'Filter and sort agents', searchLabel: 'Search agents', placeholder: 'Search agents',
        sorts: { values: SORTS, labels: SORT_LABEL, aria: 'Sort agents' },
        fields: [
            { type: 'select', key: 'state', id: `${idp}-state`, label: 'State', values: STATES, labels: STATE_LABEL, any: 'Any state' },
            { type: 'select', key: 'board', id: `${idp}-board`, label: 'Board', values: [], labels: {}, any: 'Any board' },
            { type: 'select', key: 'machine', id: `${idp}-machine`, label: 'Machine', values: [], labels: {}, any: 'Any machine' },
        ],
        model: MODEL,
    };
}

function onChange(f) {
    setListFilters(f);
    setListComparator(comparator(getListFilters().sort, resolveSessionIdentity));
    redrawLiveList();
}

/** What the filters read off an agent. */
export const FILTER_CTX = {
    stateKey: s => deriveSessionState(s),
    boardOf: s => sessionTeamKey(s) || '',
    machineOf: s => sessionServer(s),
};

/** Called by every render of the list: the bars exist where their host does, offer the boards and machines there are now, and show the filters and the count.
 *  sessions: every agent the list has before any search or filter; matched: how many pass them. */
// The bars are built when the Display panel is open (or was left open), not at load: they are two selects and a row of fields in a panel nobody has opened yet, and drawing
// them cost 17 ms of the first render at a 4x slowdown (#4995). The summary line and the count dot do not need them.
let lastArgs = null;
document.addEventListener(DISPLAY_PANEL_OPEN, () => { if (lastArgs) { sigs.clear(); syncAgentListBars(...lastArgs); } });

export function syncAgentListBars(sessions, matched) {
    lastArgs = [sessions, matched];
    const boards = [...new Set(sessions.map(s => sessionTeamKey(s)).filter(Boolean))].sort((a, b) => keyLabel(a).localeCompare(keyLabel(b)));
    const machines = [...new Set(sessions.map(s => sessionServer(s)))];
    const f = getListFilters();
    // A filter whose board or machine has no agents now is still shown (and can be removed).
    if (f.board && !boards.includes(f.board)) boards.push(f.board);
    if (f.machine && !machines.includes(f.machine)) machines.push(f.machine);
    const narrowed = !!(f.state || f.board || f.machine || getAgentFilter());
    const n = sessions.length;
    const text = narrowed ? `${matched} of ${n} agent${n === 1 ? '' : 's'}` : `${n} agent${n === 1 ? '' : 's'}`;
    // The summary line and the Display button's count tell, with the panel shut, which filters and which sort are on (the sort counts when it is not the list's own order).
    const sortOn = f.sort && f.sort !== EMPTY.sort;
    const branchesOn = isBranchShown(); // a display option that differs from its default is told here too (#5117, #5133)
    const hiddenParts = AGENT_LINE_PARTS.filter(p => !isAgentLineShown(p)); // the agent line's options are on unless turned off: an option that is off is told here
    const parts = [...MODEL.chips(f).map(c => c.label), ...(sortOn ? [`Sort: ${SORT_LABEL[f.sort]}`] : []), ...(branchesOn ? ['Branches shown'] : []), ...hiddenParts.map(p => `${AGENT_LINE_LABEL[p]} hidden`)];
    const summary = parts.length ? `${parts.join(' · ')}${narrowed ? ` (${text})` : ''}` : '';
    for (const el of inListHeaders('[data-agent-list-summary]')) {
        if (el.dataset.text !== summary) {
            el.dataset.text = summary;
            el.replaceChildren();
            if (summary) {
                const t = document.createElement('span'); t.className = 'agent-list-summary-text'; t.textContent = summary;
                const clear = document.createElement('button'); clear.type = 'button'; clear.className = 'agent-list-summary-clear'; clear.textContent = 'Clear';
                clear.setAttribute('aria-label', branchesOn || hiddenParts.length ? 'Clear filters, sort and display options' : 'Clear filters and sort');
                clear.addEventListener('click', () => { if (branchesOn) toggleShowBranches(); for (const p of hiddenParts) toggleAgentLine(p); onChange({ ...EMPTY }); });
                el.append(t, clear);
            }
        }
    }
    for (const b of inListHeaders('[data-display-badge]')) {
        setData(b, 'count', parts.length ? String(parts.length) : ''); setHidden(b, !parts.length); // a dot: the count is data (and in the button's name), not text a contrast check would measure
        const btn = b.closest('[data-display-button]'); if (btn) setAttr(btn, 'aria-label', parts.length ? `Display options, ${parts.length} on` : 'Display options');
    }
    syncDisplayPanels();
    for (const [id, idp] of HOSTS) {
        const host = document.getElementById(id);
        if (!host) continue;
        let bar = bars.get(host);
        if (!bar && !isDisplayPanelOpen()) continue; // not drawn until the panel opens
        if (!bar) {
            bar = buildListBar(spec(idp), onChange);
            host.append(bar.node);
            bars.set(host, bar);
        }
        // Every render comes here (the feed redraws the list every few seconds): only what changed is written, so an open menu or a chip with the focus is left alone.
        const sig = JSON.stringify([boards, machines, f, text, state.servers]);

        if (sigs.get(host) === sig) continue;
        sigs.set(host, sig);
        bar.setOptions('board', boards, Object.fromEntries(boards.map(b => [b, boardLabel(b)])));
        bar.setOptions('machine', machines, Object.fromEntries(machines.map(m => [m, machineLabel(m)])));
        bar.showField('board', boards.length > 0);
        bar.showField('machine', machines.length > 1 || (machines.length === 1 && machines[0] !== LOCAL_SERVER));
        bar.set({ ...EMPTY, ...f }, { text });
        host.hidden = n === 0;
    }
}
