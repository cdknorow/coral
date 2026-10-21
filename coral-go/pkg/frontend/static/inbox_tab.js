/* Inbox tab (#4718, #4811): on every Coral. Where the hub serves the dashboard it is the Coral Hub's /inbox page (what needs the Operator, what was saved) inside the dashboard, in a
 * frame whose address is the constant below: never built from a URL, a hash or a message, and nothing is posted to or read from the frame. On every other Coral the dashboard builds its own Inbox,
 * in the hub's /inbox order: Needs you (the agents waiting for input, from the live list the dashboard already holds, each opening that agent) and Saved for later (saved.js moves the one
 * Saved list in; two tabs, one panel at a time; the top bar's bookmark opens the Inbox at it where the scope is all). The tab carries a badge with the number of agents waiting for input, counted from that list (no
 * request of its own). */

import { state } from './state.js';
import { isCloud, sessionServer, splitKey } from './server_base.js';
import { importOnce } from './import_once.js';
import { browserLocal } from './safe_storage.js';
import { resolveSessionIdentity, sessionStateInfo } from './session_state.js';
import './safe_markdown.js'; // the shared Inbox renderer's wrapper: the lazy Inbox modules import it, and a lazy module imports only what loads at startup (import_hygiene)

const INBOX_URL = '/inbox';

/** How many agents are waiting for input right now, from the feed's list. */
export function waitingCount() {
    return (state.liveSessions || []).filter(s => s && s.waiting_for_input).length;
}

/** Keep the tab's badge (and, where the dashboard builds its own Inbox, the Needs you list) in step with the live list. Safe to call often (after each list update). */
export function updateInboxTab() {
    const tab = document.getElementById('nav-tab-hub-inbox');
    if (!tab) return;
    if (tab.hidden) tab.hidden = false; // the Inbox is on every Coral
    const badge = document.getElementById('nav-tab-hub-inbox-badge');
    if (!badge) return;
    const n = waitingCount();
    const text = n > 99 ? '99+' : n > 0 ? String(n) : '';
    // Only what changed is written: nav_tabs.js redraws the row on any change to it, and this runs for every message of the feed (setting hidden to
    // the value it has is still a change to a MutationObserver).
    if (badge.textContent !== text) badge.textContent = text;
    if (badge.hidden !== (n === 0)) badge.hidden = n === 0;
    const label = n > 0 ? `Inbox, ${n} waiting for input` : null;
    if (tab.getAttribute('aria-label') !== label) { if (label) tab.setAttribute('aria-label', label); else tab.removeAttribute('aria-label'); }
    renderNeedsYou();
    announceNewlyWaiting(tab);
}

let _announced = null; // the agents that were waiting at the last look (null before the first)
const waitingKey = s => `${sessionServer(s)}|${s.session_id}`;
/** A screen reader hears who has just started waiting, once each (#4926): "<agent> needs you: <why>" in a polite live region of the Inbox. Only an agent that was not waiting at the last look is new, so a redraw of
 *  the list, a refresh of the same agents, or the page loading with agents already waiting says nothing. It speaks only while the Inbox is on screen: elsewhere the toast ("needs input") and the tab's badge
 *  carry it, and a second voice for the same agent would only repeat it. The look is kept up while the Inbox is not shown, so what waited meanwhile is in the list but not announced when it is opened. */
function announceNewlyWaiting(tab) {
    const rows = (state.liveSessions || []).filter(s => s && s.waiting_for_input);
    const now = new Set(rows.map(waitingKey));
    const before = _announced;
    _announced = now;
    if (!before) return;
    const live = document.getElementById('inbox-live');
    const shown = !!live && tab.classList.contains('active') && !document.getElementById('inbox-local')?.hidden;
    const fresh = rows.filter(s => !before.has(waitingKey(s)));
    if (!shown || !fresh.length) return;
    const span = document.createElement('span'); // a new node each time: the same words for the same agent waiting again are said again
    span.textContent = fresh.map(s => `${resolveSessionIdentity(s)} needs you: ${sessionStateInfo(s).label}`).join('. ');
    live.replaceChildren(span);
}

let _needsKey = null;
/** The Needs you list of the dashboard's own Inbox: the agents waiting for input, each a button that opens that agent. Drawn only while that Inbox is on screen, and only when the list changed. */
function renderNeedsYou(force = false) {
    const local = document.getElementById('inbox-local');
    const list = document.getElementById('inbox-needs-list');
    if (!local || local.hidden || !list) return;
    const rows = (state.liveSessions || []).filter(s => s && s.waiting_for_input);
    const key = JSON.stringify(rows.map(s => [sessionServer(s), s.session_id, s.name, s.display_name, s.waiting_kind, s.board_project]));
    if (!force && key === _needsKey) return;
    _needsKey = key;
    const empty = document.getElementById('inbox-needs-empty');
    if (empty) empty.hidden = rows.length > 0;
    // Built with the DOM: no text from an agent is ever read as markup.
    list.replaceChildren(...rows.map(s => {
        const li = document.createElement('li');
        const b = document.createElement('button');
        b.type = 'button';
        b.className = 'inbox-needs-item';
        b.dataset.sessionId = String(s.session_id || '');
        const name = document.createElement('span'); name.className = 'inbox-needs-name'; name.textContent = resolveSessionIdentity(s);
        const why = document.createElement('span'); why.className = 'inbox-needs-why'; why.textContent = sessionStateInfo(s).label;
        b.append(name, why);
        if (s.board_project) { const t = document.createElement('span'); t.className = 'inbox-needs-team'; t.textContent = splitKey(s.board_project).name; b.append(t); }
        b.addEventListener('click', () => {
            window.switchNavTab?.('agents');
            window.selectLiveSession?.(s.name, s.agent_type, s.session_id, sessionServer(s));
        });
        li.append(b);
        return li;
    }));
}

// The two tabs of the dashboard's own Inbox (as the hub's): one panel shows at a time. The chosen tab is kept for this browser (and across a reload) (browserLocal).
const TAB_KEY = 'coral.inbox.tab';
const TABS = ['needs', 'tasks', 'saved'];
const TAB_TITLE = { needs: 'Needs you', tasks: 'Tasks for you', saved: 'Saved for later' };
let _tab = null;
const storedTab = () => { const v = browserLocal.get(TAB_KEY); return TABS.includes(v) ? v : 'needs'; };
/** Which tab the dashboard's own Inbox is on. */
export const inboxTab = () => _tab || storedTab();

/** Show one tab's panel (and only that one), remember the choice, and load what the panel holds. */
export function selectInboxTab(name, { focus = false } = {}) {
    if (!TABS.includes(name)) name = 'needs';
    _tab = name;
    browserLocal.set(TAB_KEY, name);
    for (const t of TABS) {
        const b = document.getElementById(`inbox-tab-${t}`);
        const panel = document.getElementById(`inbox-panel-${t}`);
        if (b) { b.setAttribute('aria-selected', String(t === name)); b.tabIndex = t === name ? 0 : -1; }
        if (panel && panel.hidden !== (t !== name)) panel.hidden = t !== name;
    }
    const title = document.getElementById('inbox-title'); // the page's heading is the tab it is on
    if (title) title.textContent = TAB_TITLE[name];
    if (focus) document.getElementById(`inbox-tab-${name}`)?.focus();
    if (name === 'needs') renderNeedsYou(true);
    else if (name === 'tasks') importOnce('./inbox_coral.js').then(m => m.refreshTasks()).catch(() => {});
    else if (!document.getElementById('inbox-local')?.hidden) importOnce('./saved.js').then(m => m.showSavedView()).catch(() => {});
}

let _tabsWired = false;
function wireTabs() {
    if (_tabsWired) return;
    const list = document.getElementById('inbox-tablist');
    if (!list) return;
    _tabsWired = true;
    for (const t of TABS) document.getElementById(`inbox-tab-${t}`)?.addEventListener('click', () => selectInboxTab(t));
    list.addEventListener('keydown', e => {
        const i = TABS.indexOf(inboxTab());
        const to = e.key === 'ArrowRight' ? (i + 1) % TABS.length : e.key === 'ArrowLeft' ? (i + TABS.length - 1) % TABS.length : e.key === 'Home' ? 0 : e.key === 'End' ? TABS.length - 1 : -1;
        if (to < 0) return;
        e.preventDefault();
        selectInboxTab(TABS[to], { focus: true });
    });
}

/** Called by switchNavTab('hub-inbox'): where the hub serves the dashboard the page is loaded the first time the tab is shown, and kept after; elsewhere the dashboard's own Inbox is drawn. */
export function showInboxTabContent() {
    const view = document.getElementById('inbox-view');
    if (!view) return;
    if (!isCloud()) {
        const local = document.getElementById('inbox-local');
        if (local) local.hidden = false;
        wireTabs();
        selectInboxTab(inboxTab()); // the tab it was on (this browser's last choice at first): that panel only is loaded and shown
        return;
    }
    if (document.getElementById('inbox-frame')) return;
    // Made here, not in the page: a page that never shows the tab (a hub visit that stays on Agents) carries no empty frame.
    const frame = document.createElement('iframe');
    frame.id = 'inbox-frame';
    frame.className = 'inbox-frame';
    frame.title = 'Inbox';
    frame.setAttribute('referrerpolicy', 'no-referrer');
    frame.setAttribute('allow', ''); // no camera, microphone, geolocation or other powerful features
    frame.setAttribute('src', INBOX_URL);
    view.append(frame);
}

updateInboxTab();
