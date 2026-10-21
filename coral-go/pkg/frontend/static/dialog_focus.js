/* Focus in and out of a modal dialog (accessibility pass). The dialog's own code calls
 * attachDialogFocus(dialog) as it opens it and closes it however it likes (a button, Escape, a
 * save); this follows the dialog's display:
 *   - on open: moves focus to the first control inside (modal_chrome.js keeps Tab inside);
 *   - on close: focus goes back to what opened it. An opener inside a menu that has closed since
 *     (the top bar's menu) hands focus to the button that controls that menu (aria-controls). */

import { lastFocusOutsideDialogs } from './modal_chrome.js';
import { onAnyBuilt } from './lazy_dom.js';

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

/** Keep Tab inside `dialog` for a dialog that handles its own keys (the artifact viewer, the Coral Hub setup screen): from
 *  the last control it wraps to the first, from the first with Shift to the last. */
export function trapTab(dialog, e) {
    const f = focusables(dialog);
    if (!f.length) return;
    const first = f[0], last = f[f.length - 1];
    if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
    else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
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
            const pref = dialog.querySelector('[data-initial-focus]'); // a dialog may name where focus starts (Settings: its open group's tab)
            const first = visible(pref) ? pref : focusables(dialog)[0];
            if (first) first.focus();
        });
    };
    const onClose = () => {
        const back = returnTarget(dialog._focusOpener);
        dialog._focusOpener = null;
        // Only when focus was left in the closed dialog or dropped to the page: never take it
        // from something the close itself moved it to, or put it behind another dialog opened from
        // this one (Settings' Themes and Default Prompts buttons). Back into a dialog that is still
        // open is right: a confirmation asked from Settings, cancelled or escaped, gives focus back to
        // the button in Settings that asked (it dropped to the page: UX on #4500).
        const now = document.activeElement;
        const behind = [...document.querySelectorAll('.modal')].some(m => m !== dialog && isShown(m) && m.getClientRects().length > 0 && !m.contains(back));
        if (back && !behind && (!now || now === document.body || dialog.contains(now))) back.focus();
    };
    dialog._focusRestore = onClose;
    new MutationObserver(() => {
        const now = isShown(dialog);
        if (now === open) return;
        open = now;
        if (now) { noteOpener(dialog); onOpen(); } else onClose();
    }).observe(dialog, { attributes: true, attributeFilter: ['style', 'hidden', 'class'] });
    if (isShown(dialog)) { open = true; onOpen(); }
}

/** For a dialog that is removed from the page when it closes (the observer above sees a display change, not a removal): call it after removing the
 *  dialog, and focus goes back to the opener as it does for a dialog that is only hidden. */
export function restoreDialogFocus(dialog) {
    if (dialog && dialog._focusRestore) dialog._focusRestore();
}

// A dialog whose code never called attachDialogFocus is opened by showing it: the control that has focus at that moment is the opener.
function noteOpener(dialog) {
    const a = document.activeElement;
    if (a && a !== document.body && !dialog.contains(a)) dialog._focusOpener = a;
    // Focus is already inside (the dialog's own show function focused a field before this ran: the macro and team wizard dialogs do) or on the page:
    // the opener is the control that last had focus outside every dialog. This branch also required focus to be outside the dialog, so it never ran
    // for exactly those dialogs and Escape or Cancel dropped focus to the page (#5705).
    else if (!dialog._focusOpener) dialog._focusOpener = lastFocusOutsideDialogs();
}

/** Every dialog in the page shell (#4026): focus moves in when it opens and back to the opener when it closes, however its own code
 *  opens it. Only a handful of dialogs called attachDialogFocus themselves; this covers the rest the same way. */
export function attachAllDialogFocus(root = document) {
    for (const d of root.querySelectorAll('.modal[role="dialog"]')) attachDialogFocus(d);
}
// A dialog built from its template on first open (#4500) gets the same, as it is built.
onAnyBuilt(el => { if (el.matches('.modal[role="dialog"]')) attachDialogFocus(el); });
