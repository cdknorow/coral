/* Inbox tab (#4718): the Coral Hub's /inbox page (what needs the Operator, what was saved) inside the dashboard. It exists only where the
 * hub serves the dashboard: the tab is hidden everywhere else, like the Servers tab's hub-only case. The tab carries a badge with the number
 * of agents waiting for input, counted from the list the dashboard already holds (no request of its own).
 * The frame's address is the constant below: never built from a URL, a hash or a message, and nothing is posted to or read from the frame. */

import { state } from './state.js';
import { isCloud } from './server_base.js';

const INBOX_URL = '/inbox';

/** How many agents are waiting for input right now, from the feed's list. */
export function waitingCount() {
    return (state.liveSessions || []).filter(s => s && s.waiting_for_input).length;
}

/** Show or hide the tab with where the hub serves the dashboard, and keep its badge in step. Safe to call often (after each list update). */
export function updateInboxTab() {
    const tab = document.getElementById('nav-tab-hub-inbox');
    if (!tab) return;
    const show = isCloud();
    if (tab.hidden === show) {
        tab.hidden = !show;
        // Leaving a tab that no longer applies.
        if (!show && tab.classList.contains('active') && window.switchNavTab) window.switchNavTab('agents');
    }
    const badge = document.getElementById('nav-tab-hub-inbox-badge');
    if (!badge) return;
    const n = show ? waitingCount() : 0;
    const text = n > 99 ? '99+' : n > 0 ? String(n) : '';
    // Only what changed is written: nav_tabs.js redraws the row on any change to it, and this runs for every message of the feed (setting hidden to
    // the value it has is still a change to a MutationObserver).
    if (badge.textContent !== text) badge.textContent = text;
    if (badge.hidden !== (n === 0)) badge.hidden = n === 0;
    const label = n > 0 ? `Inbox, ${n} waiting for input` : null;
    if (tab.getAttribute('aria-label') !== label) { if (label) tab.setAttribute('aria-label', label); else tab.removeAttribute('aria-label'); }
}

/** Called by switchNavTab('hub-inbox'): the page is loaded the first time the tab is shown, and kept after. */
export function showInboxTabContent() {
    const view = document.getElementById('inbox-view');
    if (!view || document.getElementById('inbox-frame')) return;
    // Made here, not in the page: a page that never shows the tab (a local Coral, a hub visit that stays on Agents) carries no empty frame.
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
