/* Moved out of render.js as it was (#3795): no behaviour change. */
import { redrawLiveList } from './list_hooks.js';
import { serverSwitcherHtml } from './server_status.js';
import { browserLocal } from './safe_storage.js';

// ── Servers UI in the agent list (#3085) ─────────────────────────────────
// Which disclosures are open survives the frequent list re-renders.
let _switcherOpen = false;
export const _openServerDetails = new Set();

export function _isServerCollapsed(id) {
    try { return browserLocal.get(`coral-server-collapsed:${id}`) === 'true'; } catch { return false; }
}

/** Render the search-row server button (and its phone copy). */
export function renderServerSwitcher() {
    const html = serverSwitcherHtml(_switcherOpen);
    for (const el of document.querySelectorAll('[data-server-switcher-host]')) {
        if (el.dataset.html === html) continue; // unchanged: keep focus and scroll
        el.dataset.html = html;
        el.innerHTML = html;
        el.hidden = !html;
    }
}

if (typeof document !== 'undefined') {
    document.addEventListener('click', (e) => {
        const t = e.target.closest && e.target.closest('[data-server-toggle]');
        if (!t) return;
        e.preventDefault();
        const id = t.dataset.serverToggle;
        try { browserLocal.set(`coral-server-collapsed:${id}`, _isServerCollapsed(id) ? 'false' : 'true'); } catch { /* per-viewer only */ }
        _openServerDetails.delete(id);
        redrawLiveList();
        const again = document.querySelector(`#live-sessions-list .server-group-toggle[data-server-toggle="${CSS.escape(id)}"]`);
        if (again) again.focus();
    });
    // <details> toggle does not bubble: listen in the capture phase.
    document.addEventListener('toggle', (e) => {
        const d = e.target;
        if (!(d instanceof HTMLElement)) return;
        if (d.matches('[data-server-switcher]')) _switcherOpen = d.open;
        else if (d.matches('[data-server-details]')) {
            if (d.open) _openServerDetails.add(d.dataset.serverDetails); else _openServerDetails.delete(d.dataset.serverDetails);
        }
    }, true);
    // A click or tap outside an open server list, server details or display menu closes it; a click inside leaves it open (#4062).
    // Capture phase, before any action redraws the list, so a click inside a row that gets replaced is still seen as inside.
    document.addEventListener('click', (e) => {
        for (const d of document.querySelectorAll('details[data-server-switcher][open], details[data-server-details][open], details[data-list-display-menu][open]')) {
            if (!d.contains(e.target)) d.open = false;
        }
    }, true);
    // Arrow keys in the server list: Down on the button opens it, then Up and Down, Home and End move through the rows and the footer.
    document.addEventListener('keydown', (e) => {
        if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp' && e.key !== 'Home' && e.key !== 'End') return;
        const d = e.target.closest && e.target.closest('details[data-server-switcher]');
        if (!d) return;
        const items = () => [...d.querySelectorAll('.server-switcher-item, .server-open-btn')];
        if (e.target.matches('summary')) {
            if (e.key !== 'ArrowDown') return;
            e.preventDefault();
            d.open = true;
            requestAnimationFrame(() => items()[0] && items()[0].focus());
            return;
        }
        const list = items(), at = list.indexOf(document.activeElement);
        if (!list.length || at < 0) return;
        e.preventDefault();
        const to = e.key === 'Home' ? 0 : e.key === 'End' ? list.length - 1 : e.key === 'ArrowDown' ? Math.min(at + 1, list.length - 1) : Math.max(at - 1, 0);
        list[to].focus();
    });
    // Escape closes an open server disclosure and returns focus to its button.
    document.addEventListener('keydown', (e) => {
        if (e.key !== 'Escape') return;
        const d = e.target.closest && e.target.closest('details[data-server-switcher][open], details[data-server-details][open]');
        if (!d) return;
        d.open = false;
        const sum = d.querySelector('summary');
        if (sum) sum.focus();
    });
}
