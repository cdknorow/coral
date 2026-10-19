/* Moved out of render.js as it was (#3795): no behaviour change. */
import { redrawLiveList, liveListEl } from './list_hooks.js';
import { browserLocal } from './safe_storage.js';

// ── Servers UI in the agent list (#3085) ─────────────────────────────────
// Which disclosures are open survives the frequent list re-renders.
export const _openServerDetails = new Set();

export function _isServerCollapsed(id) {
    try { return browserLocal.get(`coral-server-collapsed:${id}`) === 'true'; } catch { return false; }
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
        const again = liveListEl()?.querySelector(`.server-group-toggle[data-server-toggle="${CSS.escape(id)}"]`);
        if (again) again.focus();
    });
    // The server menu's Launch team / Open folder / Launch agent: the launch flow, with that server chosen.
    document.addEventListener('click', (e) => {
        const b = e.target.closest && e.target.closest('[data-server-launch]');
        if (!b || b.disabled) return;
        e.preventDefault();
        const kind = b.dataset.serverLaunch;
        const id = b.dataset.serverId || '';
        const d = b.closest('details[data-server-details]');
        if (d) d.open = false;
        if (kind === 'folder') { window.showOpenFolderModal?.(); return; }
        if (kind !== 'team' && kind !== 'agent') return;
        window.showLaunchModal?.(id);
        window._selectLaunchType?.(kind);
    });
    // <details> toggle does not bubble: listen in the capture phase.
    document.addEventListener('toggle', (e) => {
        const d = e.target;
        if (!(d instanceof HTMLElement)) return;
        if (d.matches('[data-server-details]')) {
            if (d.open) _openServerDetails.add(d.dataset.serverDetails); else _openServerDetails.delete(d.dataset.serverDetails);
        }
    }, true);
    // A click or tap outside an open server details or display menu closes it; a click inside leaves it open (#4062).
    // Capture phase, before any action redraws the list, so a click inside a row that gets replaced is still seen as inside.
    document.addEventListener('click', (e) => {
        for (const d of document.querySelectorAll('details[data-server-details][open], details[data-list-display-menu][open]')) {
            if (!d.contains(e.target)) d.open = false;
        }
    }, true);
    // Arrow keys in the sidebar's menus (a server's details, the display options): Down on the button opens the menu,
    // then Up and Down, Home and End move through its controls.
    const MENUS = 'details[data-server-details], details[data-list-display-menu]';
    const menuItems = d => [...d.querySelectorAll('button:not(:disabled), input:not(:disabled), a[href]')].filter(x => !x.closest('[hidden]'));
    document.addEventListener('keydown', (e) => {
        if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp' && e.key !== 'Home' && e.key !== 'End') return;
        const d = e.target.closest && e.target.closest(MENUS);
        if (!d) return;
        if (e.target.matches('summary')) {
            if (e.key !== 'ArrowDown') return;
            e.preventDefault();
            d.open = true;
            requestAnimationFrame(() => menuItems(d)[0] && menuItems(d)[0].focus());
            return;
        }
        const list = menuItems(d), at = list.indexOf(document.activeElement);
        if (!list.length || at < 0) return;
        e.preventDefault();
        const to = e.key === 'Home' ? 0 : e.key === 'End' ? list.length - 1 : e.key === 'ArrowDown' ? Math.min(at + 1, list.length - 1) : Math.max(at - 1, 0);
        list[to].focus();
    });
    // Escape closes an open menu and returns focus to its button.
    document.addEventListener('keydown', (e) => {
        if (e.key !== 'Escape') return;
        const d = e.target.closest && e.target.closest('details[data-server-details][open], details[data-list-display-menu][open]');
        if (!d) return;
        d.open = false;
        const sum = d.querySelector('summary');
        if (sum) sum.focus();
    });
}
