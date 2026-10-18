import { refreshAgentUI } from './agent_ui.js';
/* Agentic State — event loading, timeline rendering, tab switching, filtering */

import { importOnce } from './import_once.js';
import { state } from './state.js';
import { loadChangedFiles } from './changed_files.js';
import { selectFilesSource, syncFilesSourceTeam } from './team_artifacts.js';
import { startBoardTaskPoll, stopBoardTaskPoll } from './tasks.js';
import { morphHtml } from './dom_morph.js';
import { eventRowHtml, renderActivityChart } from './event_row.js';
import { syncActivitySearch } from './activity_search.js';
import { serverFetch, serverForSession } from './server_base.js';
import { FILTER_GROUPS, eventVisible } from './event_icons.js';
import { openableTab } from './tab_customize.js';
import { browserLocal } from './safe_storage.js';
import { DEFAULT_TAB, dockKey, dockKind, onBoardPage, onDockPage, rememberedTab, tabShown } from './dock_page.js';

// Hidden filters are persisted per-session in state
// state.eventFiltersHidden = Set of filter keys that are hidden

function getHiddenFilters() {
    if (!state.eventFiltersHidden) {
        state.eventFiltersHidden = new Set();
    }
    return state.eventFiltersHidden;
}

function isEventVisible(ev) {
    return eventVisible(ev, getHiddenFilters());
}

export async function loadAgentEvents(agentName, sessionId, options) {
    if (!agentName) return;
    const sid = sessionId || (state.currentSession && state.currentSession.session_id);
    try {
        const params = new URLSearchParams({ limit: 50 });
        if (sid) params.set("session_id", sid);
        const resp = await serverFetch(serverForSession(agentName, sid), `/api/sessions/live/${encodeURIComponent(agentName)}/events?${params}`, options);
        if (!resp.ok) throw new Error(`events fetch failed: ${resp.status}`);
        const events = await resp.json();
        if (sid && state.currentSession?.session_id !== sid) return;
        state.currentAgentEvents = events;
    } catch (e) {
        if (e?.name === 'AbortError') return;
        if (sid && state.currentSession?.session_id !== sid) return;
        state.currentAgentEvents = [];
    }
    renderEventTimeline();
}

export function renderEventTimeline() {
    const container = document.getElementById('agentic-state-events');
    if (!container) return;

    // A search of the agent's events (the bar above the list) shows its results in place of the live feed
    if (syncActivitySearch(renderEventTimeline)) { renderLiveActivityChart(); return; }

    const events = state.currentAgentEvents || [];

    if (events.length === 0) {
        morphHtml(container, '<div class="event-empty">No activity yet</div>');
        return;
    }

    const visible = events.filter(isEventVisible);
    if (visible.length === 0) {
        morphHtml(container, '<div class="event-empty">All events filtered out</div>');
        return;
    }

    // Events come newest-first from API, display newest at top
    morphHtml(container, visible.map(eventRowHtml).join(''));

    renderLiveActivityChart();
}

function renderLiveActivityChart() {
    renderActivityChart(document.getElementById('live-activity-chart'), state.currentAgentEvents || [], FILTER_GROUPS);
}

export function switchAgenticTab(tabName, blockId) {
    if (dockKind() === 'agent') tabName = openableTab(tabName); // a tab the user hid opens the always-shown one instead (only the agent page's tabs can be hidden)
    // Scope tab/panel switching to the containing block
    const block = blockId
        ? document.getElementById(`agentic-block-${blockId}`)
        : document.getElementById(`agentic-tab-${tabName}`)?.closest('.agentic-block');

    if (block) {
        // Clicking a tab means the user wants to see content — uncollapse
        block.classList.remove('collapsed');
        block.querySelectorAll('.agentic-tab').forEach(btn => btn.classList.remove('active'));
        block.querySelectorAll('.agentic-panel').forEach(panel => panel.classList.remove('active'));
    }

    const activeBtn = document.getElementById(`agentic-tab-${tabName}`);
    if (activeBtn) activeBtn.classList.add('active');

    const activePanel = document.getElementById(`agentic-panel-${tabName}`);
    if (activePanel) activePanel.classList.add('active');

    // Persist tab choice per block (team-only tabs only exist while a team is selected)
    if (blockId && activeBtn?.dataset.scope !== 'team') {
        browserLocal.set(dockKey(`coral-agentic-tab-${blockId}`), tabName);
    }

    document.dispatchEvent(new CustomEvent('coral:dock-changed')); // the Saved button's pressed state follows (saved_marks.js, #4742)

    if (tabName === 'team-settings') window.renderTeamSettingsTab?.();

    if (tabName === 'agent-ui') refreshAgentUI();

    // Saved for later (#4510): the list is read when its tab is shown, not while the sidebar is shut behind it (the home page's tab is Saved, and startup must not load the list: #4326);
    // opening the sidebar on it reads the list (sidebar.js)
    if (tabName === 'saved' && !document.getElementById('agentic-state')?.classList.contains('collapsed')) importOnce('./saved.js').then(m => m.showSaved()).catch(() => {});

    // Start/stop board task polling based on tab
    if (tabName === 'tasks') {
        startBoardTaskPoll();
    } else if (blockId === 'top') {
        stopBoardTaskPoll();
    }

    // Activate custom view iframe when switching to a custom view tab
    if (tabName.startsWith('custom-view-') && window._activateCustomView) {
        const viewId = parseInt(tabName.replace('custom-view-', ''), 10);
        window._activateCustomView(viewId);
    }

    // Files-viewer sources (browse, artifacts, ...) are top-level tabs
    if (['files', 'browse', 'artifacts', 'team-artifacts', 'knowledge'].includes(tabName)) selectFilesSource(tabName);

    // Refresh changed files when switching to the files tab
    if (tabName === 'files' && state.currentSession && state.currentSession.type === 'live') {
        loadChangedFiles(state.currentSession.name, state.currentSession.session_id);
    }

    // Scroll board chat to bottom when switching to it
    if (tabName === 'board') {
        setTimeout(() => {
            const msgs = document.getElementById('board-panel-msgs');
            if (msgs) msgs.scrollTop = msgs.scrollHeight;
        }, 50);
    }
}

/** Restore persisted tab selection on page load. */
export function restoreAgenticTabs() {
    // Only restore for the single unified top block
    const saved = rememberedTab();
    if (saved) {
        const tab = document.getElementById(`agentic-tab-${saved}`);
        if (tab && tab.style.display !== 'none') {
            switchAgenticTab(saved, 'top');
        }
    }
    // Clean up stale bottom block preference
    browserLocal.remove('coral-agentic-tab-bottom');
}

// The right sidebar is on every page (#4539): a page change opens the tab that kind of page last had, or its first one. On the agent page of a selected team the tab
// that is open stays when it belongs there (a team's own tabs are not remembered).
function showPageTab(kind) {
    const active = document.querySelector('#agentic-block-top .agentic-tab.active');
    if (kind === 'agent' && state.selectedTeam && tabShown(active)) return;
    const remembered = rememberedTab(kind);
    const tab = [remembered, DEFAULT_TAB[kind]].find(t => t && tabShown(document.getElementById(`agentic-tab-${t}`))) || DEFAULT_TAB[kind];
    switchAgenticTab(tab, 'top');
}
onDockPage(showPageTab);
// A board page that moved to another board: the Tasks and Team artifacts tabs read the board again.
onBoardPage(() => {
    if (dockKind() !== 'board') return;
    syncFilesSourceTeam();
    const active = document.querySelector('#agentic-block-top .agentic-tab.active');
    if (active?.dataset.tab) switchAgenticTab(active.dataset.tab, 'top');
});

// The sidebar's tab strip is a tablist: Left and Right move the focus to the neighbouring tab, Home and End to the first and the last shown one (Enter or Space opens it, as
// on any button); Tab still moves through them (UX's NIT on #4539).
document.addEventListener('keydown', e => {
    const cur = e.target.closest?.('#agentic-block-top .agentic-state-tabs .agentic-tab');
    if (!cur || e.altKey || e.ctrlKey || e.metaKey || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(e.key)) return;
    const tabs = [...cur.parentElement.querySelectorAll('.agentic-tab')].filter(tabShown);
    const at = tabs.indexOf(cur);
    if (at < 0) return;
    const next = e.key === 'Home' ? tabs[0] : e.key === 'End' ? tabs[tabs.length - 1]
        : tabs[(at + (e.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length];
    e.preventDefault();
    next.focus();
});
