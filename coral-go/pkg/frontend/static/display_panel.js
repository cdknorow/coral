/* The agent list's Display expander (#4947): the ☰ button opens a panel in place, under the search (VS Code style, not a popover), holding the list's filters and sort and the display
 * options. Closed, a filter or sort that is on is told by the summary line under the search and a count on the button. The same markup is in the sidebar and on the phone's Agents page, so
 * everything here finds its parts by data attributes:
 *   [data-display-button]   the ☰ toggle (aria-expanded, aria-controls the panel)
 *   [data-display-panel]    the panel
 *   [data-agent-list-summary]  the read-only line
 * The open or shut state is the person's, kept in this browser. */
import { browserLocal } from './safe_storage.js';

const KEY = 'coral_display_panel_open';

export function isDisplayPanelOpen() { return browserLocal.get(KEY) === '1'; }

/** Shows the summary line only while the panel is shut and there is something to say. */
export function syncDisplaySummary() {
    const open = isDisplayPanelOpen();
    for (const el of document.querySelectorAll('[data-agent-list-summary]')) el.hidden = open || !el.dataset.text;
}

/** Applies the stored state to every copy of the expander (called after the phone's header is built and after every list render). */
export function syncDisplayPanels() {
    const open = isDisplayPanelOpen();
    for (const b of document.querySelectorAll('[data-display-button]')) b.setAttribute('aria-expanded', String(open));
    for (const p of document.querySelectorAll('[data-display-panel]')) p.hidden = !open;
    syncDisplaySummary();
}

export function toggleDisplayPanel(button) {
    const open = !isDisplayPanelOpen();
    browserLocal.set(KEY, open ? '1' : '0');
    syncDisplayPanels();
    // Opening puts the keyboard in the panel's first control; closing leaves it on the button that was used.
    if (open) document.querySelector((button?.closest('.mobile-list-header') ? '.mobile-list-header' : '.sidebar-header') + ' [data-display-panel] :is(select, input):not([hidden])')?.focus();
}

/** Escape inside the panel closes it and returns to its button. */
export function displayPanelKey(event) {
    if (event.key !== 'Escape') return;
    const panel = event.target.closest?.('[data-display-panel]');
    if (!panel || panel.hidden) return;
    event.preventDefault();
    const header = panel.parentElement;
    browserLocal.set(KEY, '0');
    syncDisplayPanels();
    header.querySelector('[data-display-button]')?.focus();
}
