/* The agent list's Display expander (#4947): the ☰ button opens a panel in place, under the search (VS Code style, not a popover), holding the list's filters and sort and the display
 * options. Closed, a filter that is on is told by a dot on the button. The same markup is in the sidebar and on the phone's Agents page, so
 * everything here finds its parts by data attributes:
 *   [data-display-button]   the ☰ toggle (aria-expanded, aria-controls the panel)
 *   [data-display-panel]    the panel
 * The open or shut state is the person's, kept in this browser. */
import { browserLocal } from './safe_storage.js';
import { setHidden, setAttr } from './dom_write.js';

/** Said when the panel opens: the list's filter bar, which is not drawn while the panel stays shut (agent_list_bar.js, #4995), draws itself then. */
export const DISPLAY_PANEL_OPEN = 'coral:display-panel-open';

const KEY = 'coral_display_panel_open';

/** The elements the Display expander is made of, found inside the two headers that hold it (the sidebar's and the phone's), not by an attribute selector over the whole
 *  page: the list's render asks for them every time, and the page (with a long chat in it) has thousands of nodes (#4990). */
export function inListHeaders(selector) {
    const out = [];
    for (const h of [...document.getElementsByClassName('sidebar-header'), ...document.getElementsByClassName('mobile-list-header')]) out.push(...h.querySelectorAll(selector));
    return out;
}

export function isDisplayPanelOpen() { return browserLocal.get(KEY) === '1'; }

/** Applies the stored state to every copy of the expander (called after the phone's header is built and after every list render). Each copy is
 *  looked for inside its own header (the sidebar's and the phone's), never over the whole page: this runs on every list render (#4985's budget). */
export function syncDisplayPanels() {
    const open = isDisplayPanelOpen();
    for (const b of inListHeaders('[data-display-button]')) setAttr(b, 'aria-expanded', open);
    for (const p of inListHeaders('[data-display-panel]')) setHidden(p, !open);
}

export function toggleDisplayPanel(button) {
    const open = !isDisplayPanelOpen();
    browserLocal.set(KEY, open ? '1' : '0');
    syncDisplayPanels();
    if (open) document.dispatchEvent(new Event(DISPLAY_PANEL_OPEN));
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
