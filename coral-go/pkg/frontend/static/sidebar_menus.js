/* The sidebar kebab menus and the top bar settings menu (moved out of app.js, #5082; no behaviour change). */
import { markBlockedControls } from './hub_blocked_controls.js';
import { openRowMenu } from './session_rows.js';
import { browserLocal } from './safe_storage.js';
import { ensureBuilt } from './lazy_dom.js';

// ── Sidebar kebab menu helpers ───────────────────────────────────────────
// With restoreFocus (Escape) the focus goes back to the button of the menu that was open, when it was inside it (#4621).
export function closeSidebarKebabs({ restoreFocus = false } = {}) {
    for (const m of document.querySelectorAll('.sidebar-kebab-menu')) {
        const open = m.style.display !== 'none';
        const btn = m.previousElementSibling;
        m.style.display = 'none';
        if (btn && btn.classList.contains('sidebar-kebab-btn')) {
            btn.setAttribute('aria-expanded', 'false');
            if (open && restoreFocus && m.contains(document.activeElement)) btn.focus({ preventScroll: true });
        }
    }
}

export function toggleSidebarKebab(btn, event) {
    const menu = btn.nextElementSibling;
    const wasOpen = menu.style.display !== 'none';
    closeSidebarKebabs();
    if (!wasOpen) {
        // An agent row's menu is drawn when it opens (#4611); the hub's tier marks follow at once rather than at the next sweep.
        if (menu.dataset.lazyMenu) {
            if (!openRowMenu(btn, menu)) return;
            markBlockedControls(menu);
        }
        btn.setAttribute('aria-expanded', 'true');
        // Show off-screen first to measure, then position
        menu.style.visibility = 'hidden';
        menu.style.display = 'block';
        const rect = btn.getBoundingClientRect();
        const menuHeight = menu.offsetHeight || 150;
        const menuWidth = menu.offsetWidth || 160;
        const viewportHeight = window.innerHeight;
        const viewportWidth = window.innerWidth;
        // If menu would overflow the bottom, position above the button
        if (rect.bottom + menuHeight + 4 > viewportHeight) {
            menu.style.top = Math.max(4, rect.top - menuHeight - 2) + 'px';
        } else {
            menu.style.top = rect.bottom + 2 + 'px';
        }
        // Clamp left so menu doesn't overflow right edge
        const left = Math.min(rect.left, viewportWidth - menuWidth - 8);
        menu.style.left = Math.max(4, left) + 'px';
        menu.style.visibility = '';
        // Every item is a menu item for a screen reader (the row menus are marked as they are drawn).
        for (const it of menu.querySelectorAll('.overflow-menu-item')) if (!it.hasAttribute('role')) it.setAttribute('role', 'menuitem');
        // Opened from the keyboard (Enter or Space is a click with no pointer): the focus goes to the first item.
        if (event && event.detail === 0) menu.querySelector('.overflow-menu-item')?.focus({ preventScroll: true });
        // Hide any visible tooltips so they don't cover the menu
        document.querySelectorAll('.session-tooltip').forEach(t => t.style.display = 'none');
    }
}

// Top-bar settings dropdown
export function toggleTopBarSettings(event) {
    const menu = ensureBuilt('top-bar-settings-menu'); // built on first open (#4500)
    if (!menu) return;
    const isOpen = menu.style.display !== 'none';
    menu.style.display = isOpen ? 'none' : '';
    if (!isOpen && event && event.detail === 0) [...menu.querySelectorAll('.overflow-menu-item')].find(i => i.getClientRects().length)?.focus({ preventScroll: true }); // opened from the keyboard (a click with no pointer), the focus goes into the menu (#6038)
    // Sync group-by-team checkmark
    const check = document.getElementById('group-by-team-check-top');
    if (check) {
        const groupByTeam = browserLocal.get('coral-group-by-team') === 'true';
        check.style.opacity = groupByTeam ? '1' : '0.2';
    }
}

// With restoreFocus (Escape) the focus goes back to the menu's button when it was inside the menu (#6038).
export function closeTopBarSettings({ restoreFocus = false } = {}) {
    const menu = document.getElementById('top-bar-settings-menu');
    if (!menu) return;
    const hadFocus = menu.contains(document.activeElement);
    menu.style.display = 'none';
    if (restoreFocus && hadFocus) document.querySelector('[aria-controls="top-bar-settings-menu"]')?.focus({ preventScroll: true });
}
