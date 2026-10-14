/* Focus in and out of a modal dialog (accessibility pass). The dialog's own code calls
 * attachDialogFocus(dialog) as it opens it and closes it however it likes (a button, Escape, a
 * save); this follows the dialog's display:
 *   - on open: moves focus to the first control inside (modal_chrome.js keeps Tab inside);
 *   - on close: focus goes back to what opened it. An opener inside a menu that has closed since
 *     (the top bar's menu) hands focus to the button that controls that menu (aria-controls). */

import { lastFocusOutsideDialogs } from './modal_chrome.js';

const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), summary, [tabindex]:not([tabindex="-1"])';

const isShown = el => !!el && !el.hidden && getComputedStyle(el).display !== 'none';
const visible = el => !!el && el.isConnected && el.getClientRects().length > 0;

/** The dialog's focusable controls that can be reached now (visible ones only). */
function focusables(dialog) {
    return [...dialog.querySelectorAll(FOCUSABLE)].filter(visible);
}

// Where focus goes back to: the opener, or the control of the hidden menu it sits in.
function returnTarget(opener) {
    if (!opener || !opener.isConnected) return null;
    if (visible(opener)) return opener;
    for (let el = opener.parentElement; el; el = el.parentElement) {
        if (!el.id) continue;
        const ctl = document.querySelector(`[aria-controls="${CSS.escape(el.id)}"]`);
        if (visible(ctl)) return ctl;
    }
    return null;
}

/** Call as the dialog opens (before it is shown is fine): records the opener now, while the
 *  clicked control still has focus, and wires the dialog up once. */
export function attachDialogFocus(dialog) {
    if (!dialog) return;
    const a = document.activeElement;
    if (a && a !== document.body && !dialog.contains(a)) dialog._focusOpener = a;
    else if (!dialog.contains(a)) dialog._focusOpener = lastFocusOutsideDialogs(); // its menu closed first
    if (dialog._focusAttached) return;
    dialog._focusAttached = true;
    let open = false;
    const onOpen = () => {
        // The dialog fills itself in as it opens: focus once its controls are laid out.
        requestAnimationFrame(() => {
            if (!isShown(dialog) || dialog.contains(document.activeElement)) return;
            const first = focusables(dialog)[0];
            if (first) first.focus();
        });
    };
    const onClose = () => {
        const back = returnTarget(dialog._focusOpener);
        dialog._focusOpener = null;
        // Only when focus was left in the closed dialog or dropped to the page: never take it
        // from something the close itself moved it to, or put it behind another dialog opened from
        // this one (Settings' Themes and Default Prompts buttons).
        const now = document.activeElement;
        const otherOpen = [...document.querySelectorAll('.modal')].some(m => m !== dialog && isShown(m) && m.getClientRects().length > 0);
        if (back && !otherOpen && (!now || now === document.body || dialog.contains(now))) back.focus();
    };
    new MutationObserver(() => {
        const now = isShown(dialog);
        if (now === open) return;
        open = now;
        if (now) onOpen(); else onClose();
    }).observe(dialog, { attributes: true, attributeFilter: ['style', 'hidden', 'class'] });
    if (isShown(dialog)) { open = true; onOpen(); }
}
