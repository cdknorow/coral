/* The Activity filter's dropdown, live and history alike: one item's markup, the list redraw that keeps the focused item, and the keyboard
 * (the items are menu checkboxes: arrows move, Space or Enter toggles, Escape closes and gives the focus back to the Filter button). */
import { escapeAttr, escapeHtml } from './utils.js';

/** One filter row. group.label is an icon-font character written as an HTML entity (&#xe8f4;), so it goes in as markup; the title is text. checked: whether this kind of event shows; countHtml: the count chip, or ''. */
export function filterItemHtml(group, mode, checked, countHtml) {
    return `<div class="filter-dropdown-item ${escapeAttr(group.cls)}" data-on-click="filter-toggle" data-mode="${escapeAttr(mode)}" data-key="${escapeAttr(group.key)}"
            role="menuitemcheckbox" aria-checked="${checked ? 'true' : 'false'}" tabindex="0" data-key-click>
            <span class="filter-item-icon event-filter-char">${group.label}</span>
            <span class="filter-item-label">${escapeHtml(group.title)}</span>
            ${countHtml}
            <span class="filter-item-check" aria-hidden="true">${checked ? '&#xe834;' : '&#xe835;'}</span>
        </div>`;
}

/** Redraw the list in place; the focused item stays focused (a toggle redraws every row, which dropped the focus). */
export function refillFilterList(dropdown, itemsHtml) {
    const list = dropdown.querySelector('.filter-dropdown-list');
    if (!list) return;
    const focused = list.contains(document.activeElement) ? document.activeElement.dataset.key : null;
    list.innerHTML = itemsHtml;
    if (focused) list.querySelector(`.filter-dropdown-item[data-key="${CSS.escape(focused)}"]`)?.focus();
}

/** The Filter buttons say whether their list is open. */
export function syncFilterExpanded() {
    const open = !!document.getElementById('filter-dropdown');
    for (const b of document.querySelectorAll('.filter-dropdown-btn')) b.setAttribute('aria-expanded', open ? 'true' : 'false');
}

/** Keyboard for an opened dropdown: focus goes to the first item; getBtn(): the Filter button as it is now (a redraw can replace it); close(): remove the list. */
export function wireFilterDropdown(dropdown, getBtn, close) {
    dropdown.setAttribute('role', 'menu');
    dropdown.setAttribute('aria-label', 'Filter events');
    const items = () => [...dropdown.querySelectorAll('.filter-dropdown-item')];
    dropdown.addEventListener('keydown', event => {
        if (event.key === 'Escape') {
            event.preventDefault();
            event.stopPropagation();
            close();
            getBtn()?.focus();
            return;
        }
        if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
        const list = items();
        const at = list.indexOf(document.activeElement);
        if (!list.length) return;
        event.preventDefault();
        list[(at + (event.key === 'ArrowDown' ? 1 : list.length - 1) + (at < 0 ? 1 : 0)) % list.length].focus();
    });
    // Tabbing out of the list closes it (it is the last element in the page, so Tab would otherwise leave the dashboard).
    dropdown.addEventListener('focusout', event => {
        const to = event.relatedTarget;
        if (to && (dropdown.contains(to) || to === getBtn())) return;
        if (!to) return; // a click elsewhere is the outside-click handler's
        close();
    });
    items()[0]?.focus();
}
