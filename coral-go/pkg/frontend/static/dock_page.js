/* The right sidebar is part of the app layout (#4539), so it is on every page; each kind of page picks its tabs and keeps its own open or closed state, width and last tab.
 * The kinds: the agent's page (today's tabs), a board (Subscribers, Team artifacts, Tasks, Saved), Chats (Saved) and every other page (Saved). The tab sets are in
 * the markup (data-pages on a tab) and the CSS (#agentic-state[data-page]); this module knows the kind of the page on screen and tells the modules that own the layout
 * and the tabs (sidebar.js, agentic_state.js) when it changes. showView (utils.js) is the one caller of setDockPage. */
import { browserLocal } from './safe_storage.js';
import { state } from './state.js';

const KIND_OF_VIEW = {
    'live-session-view': 'agent',
    'messageboard-view': 'board',
    'history-session-view': 'chats',
    'chats-empty-view': 'chats',
};
/** Open on first use? The agent page has always shown its side column (a board does when the window is wide enough, dockOpenByDefault); the others start closed. */
const OPEN_BY_DEFAULT = { agent: true, board: true, chats: false, other: false };
/** The tab a kind of page opens on when it has no remembered one. */
export const DEFAULT_TAB = { agent: 'files', board: 'subscribers', chats: 'saved', other: 'saved' };

let kind = 'agent';
const hooks = [];

export const dockKind = () => kind;
/** A board starts with its sidebar open only when the message column stays about 560 px wide beside it (the row is at least that and the sidebar's 420 px); a narrower window starts it closed. */
const BOARD_ROW_MIN = 980;
export function dockOpenByDefault(k = kind) {
    if (k === 'board') {
        const row = document.querySelector('.main-row');
        return !!row && row.getBoundingClientRect().width >= BOARD_ROW_MIN;
    }
    return OPEN_BY_DEFAULT[k];
}

/** A storage key for the page on screen: the agent page keeps its original keys, the others add their kind; a popout window has its own (it must not overwrite the dashboard's sizes). */
export function dockKey(base, k = kind) {
    return `${base}${k === 'agent' ? '' : ':' + k}${document.body.classList.contains('popout-mode') ? ':popout' : ''}`;
}

/** The board the board page shows (an identity key), or null on any other page: the Tasks and Team artifacts tabs read it there. */
export function boardOfPage() { return kind === 'board' ? state.boardPageProject || null : null; }

const boardHooks = [];
/** fn() runs when the board the board page shows changes (or there is none): the tabs that read it read it again. */
export function onBoardPage(fn) { boardHooks.push(fn); }
export function boardPageChanged() {
    for (const fn of boardHooks) {
        try { fn(); } catch (e) { console.error('board page hook failed', e); }
    }
}

/** fn(kind, previous) runs when the kind of the page on screen changes (and once for the first page). */
export function onDockPage(fn) { hooks.push(fn); }

/** Whether a tab button is on the page on screen: asks the style sheet, so a tab a page or the user hides is not counted. */
export function tabShown(btn) { return !!btn && btn.style.display !== 'none' && getComputedStyle(btn).display !== 'none'; }

export function setDockPage(viewId) {
    const next = KIND_OF_VIEW[viewId] || 'other';
    const dock = document.getElementById('agentic-state');
    if (dock) dock.dataset.page = next;
    document.body.setAttribute('data-dock-page', next);
    if (next === kind && dock?.dataset.pageApplied) return;
    const previous = kind;
    kind = next;
    if (dock) dock.dataset.pageApplied = '1';
    for (const fn of hooks) {
        try { fn(next, previous); } catch (e) { console.error('dock page hook failed', e); }
    }
}

/** The tab the person last used on this kind of page, if it is still there. */
export function rememberedTab(k = kind) {
    try { return browserLocal.get(dockKey('coral-agentic-tab-top', k)); } catch { return null; }
}
