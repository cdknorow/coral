/* Moved out of render.js as it was (#3795): no behaviour change. */
import { redrawLiveList } from './list_hooks.js';
import { serverSwitcherHtml } from './server_status.js';

// ── Servers UI in the agent list (#3085) ─────────────────────────────────
// Which disclosures are open survives the frequent list re-renders.
let _switcherOpen = false;
export const _openServerDetails = new Set();

export function _isServerCollapsed(id) {
    try { return localStorage.getItem(`coral-server-collapsed:${id}`) === 'true'; } catch { return false; }
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
        try { localStorage.setItem(`coral-server-collapsed:${id}`, _isServerCollapsed(id) ? 'false' : 'true'); } catch { /* per-viewer only */ }
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
