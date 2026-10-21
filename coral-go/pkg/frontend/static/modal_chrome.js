/* Page-wide modal behaviour, loaded with the page (#3140): a click on a modal's
 * backdrop closes it, Escape closes the topmost visible modal, and Tab stays
 * inside it. It covers every dialog (confirm, alert, restart, macro, ...), not
 * only the ones in modals.js, which loads the first time one of its dialogs
 * opens; those are closed through their window functions, which exist by the
 * time such a dialog can be visible. */

import { hideRestartModal } from './controls.js';
import { attachDialogFocus, restoreDialogFocus } from './dialog_focus.js';

// The control that last had focus outside every dialog. A dialog opened from a menu item (the top
// bar's menu closes as the item is chosen, and most dialogs load their code first) finds focus
// already dropped to the page by the time it opens: focus goes back here instead (dialog_focus.js).
let _lastFocusOutside = null;
document.addEventListener('focusin', (e) => {
    const t = e.target;
    if (t && t !== document.body && t.closest && !t.closest('.modal')) _lastFocusOutside = t;
});
export function lastFocusOutsideDialogs() {
    return _lastFocusOutside && _lastFocusOutside.isConnected ? _lastFocusOutside : null;
}

// How each dialog closes. `backdrop` marks the ones a click on their backdrop closes; Escape closes any visible one. A dialog
// without `hide` is closed by hiding its element (display none), which is also what an unlisted dialog gets. The lazy dialogs' hide
// functions are looked up on window when used, since they exist by the time such a dialog can be visible.
const byWindow = name => () => window[name]?.();
const DIALOGS = Object.assign(Object.create(null), { // no prototype: an element id like "constructor" finds nothing
    'launch-modal': { hide: byWindow('hideLaunchModal'), backdrop: true },
    'info-modal': { hide: byWindow('hideInfoModal'), backdrop: true },
    'resume-modal': { hide: byWindow('hideResumeModal'), backdrop: true },
    'settings-modal': { hide: byWindow('hideSettingsModal'), backdrop: true },
    'restart-modal': { hide: () => hideRestartModal(), backdrop: true },
    'macro-modal': { backdrop: true },
    'alert-modal': { hide: byWindow('hideAlertModal'), backdrop: true },
    'confirm-modal': { hide: byWindow('hideConfirmModal'), backdrop: true },
    'mb-export-modal': { backdrop: true },
    'prompt-modal': { hide: byWindow('hidePromptModal') },
    'team-agent-modal': { hide: byWindow('hideTeamAgentModal') },
    'default-prompts-modal': { hide: byWindow('hideDefaultPromptsModal') },
    'team-wizard-modal': { hide: byWindow('hideTeamWizardModal') },
    'add-agent-board-modal': { hide: byWindow('hideAddAgentBoardModal') },
    'mobile-connect-modal': { hide: () => window._hideMobileConnectModal() },
});

// Modals top-first: the confirmation is painted above Settings (z-index) though it comes earlier in the page, so Escape and Tab must reach it first (#4469).
function modalsTopFirst() {
    const all = [...document.querySelectorAll('.modal')].reverse();
    const confirm = all.findIndex((m) => m.id === 'confirm-modal');
    if (confirm > 0) all.unshift(...all.splice(confirm, 1));
    return all;
}

// A dialog built by script registers itself here (registerDialog): the same Escape, backdrop and focus rules as the dialogs in the page shell.
const registered = new WeakMap();

function hideDialog(modal) {
    const d = registered.get(modal) || DIALOGS[modal.id];
    if (d && d.hide) d.hide();
    else modal.style.display = 'none';
}

/** The native <dialog> counterpart (#5705): the control marked data-dialog-close closes it, so does a click on its backdrop, and it removes itself from the
 *  page when it closes (focus returns to the opener by itself). The Close button is found by that attribute, never by its place in the header: Team settings
 *  bound its Close to the first header button, Rename board, and Team view still does the same by selector. Escape is the dialog's own. */
export function bindNativeDialog(dialog, { remove = true } = {}) {
    for (const b of dialog.querySelectorAll('[data-dialog-close]')) b.addEventListener('click', () => dialog.close());
    dialog.addEventListener('click', event => { if (event.target === dialog) dialog.close(); });
    if (remove) dialog.addEventListener('close', () => dialog.remove());
}

/** Close any dialog the way Escape does (its registered close, else its hide function, else display:none). For a close button wired by an action. */
export function closeDialog(modal) { if (modal) hideDialog(modal); }

/** The one way a dialog built by script closes (#5705). Call it on the `.modal` element once it is in the page: Escape (the topmost one only), a click on
 *  its backdrop, and the returned close() all remove it, return focus to the control that opened it and then call onClose (resolve the promise that is
 *  waiting for it, say). It also gives the element role="dialog" and names it by its first heading, and moves focus in. Before this each dialog hid itself
 *  with display:none on Escape (the page-wide rule), which left a waiting promise unresolved, or kept its own copy of the rules.
 *  Options: backdrop (default true) whether a click outside closes it; onClose(). */
export function registerDialog(modal, { onClose, backdrop = true } = {}) {
    let closed = false;
    const close = () => {
        if (closed) return;
        closed = true;
        registered.delete(modal);
        modal.remove();
        restoreDialogFocus(modal);
        if (onClose) onClose();
    };
    registered.set(modal, { hide: close, backdrop });
    modal.setAttribute('role', 'dialog');
    modal.setAttribute('aria-modal', 'true');
    const heading = modal.querySelector('h2, h3, h4');
    if (heading && !modal.hasAttribute('aria-labelledby') && !modal.hasAttribute('aria-label')) {
        if (!heading.id) heading.id = `dialog-title-${Math.random().toString(36).slice(2, 8)}`;
        modal.setAttribute('aria-labelledby', heading.id);
    }
    attachDialogFocus(modal);
    return close;
}

// Close modals on outside click
document.addEventListener("click", (e) => {
    const t = e.target;
    if (!t) return;
    const own = registered.get(t);
    if (own ? own.backdrop : (DIALOGS[t.id] && DIALOGS[t.id].backdrop)) hideDialog(t);
});

// Close topmost visible modal on Escape and trap focus inside it
document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") {
        // Find topmost visible modal and close only that one
        for (const modal of modalsTopFirst()) {
            if (modal.style.display !== 'none' && modal.getClientRects().length > 0) {
                hideDialog(modal);
                e.preventDefault();
                return;
            }
        }
    }

    // Focus trapping: Tab/Shift+Tab stays inside the topmost visible modal
    if (e.key === "Tab") {
        for (const modal of modalsTopFirst()) {
            if (modal.style.display !== 'none' && modal.getClientRects().length > 0) {
                const focusable = Array.from(modal.querySelectorAll(
                    'button:not([disabled]), input:not([disabled]), textarea:not([disabled]), select:not([disabled]), a[href], summary, [tabindex]:not([tabindex="-1"])' // summary: a disclosure that is the last control (the artifact viewer's "Who can read this") is not skipped by the wrap
                )).filter(el => el.getClientRects().length > 0);
                if (focusable.length === 0) return;
                const first = focusable[0];
                const last = focusable[focusable.length - 1];
                // Focus outside the modal (left on the control that opened it) comes in too.
                const outside = !modal.contains(document.activeElement);
                if (e.shiftKey && (outside || document.activeElement === first)) {
                    e.preventDefault();
                    last.focus();
                } else if (!e.shiftKey && (outside || document.activeElement === last)) {
                    e.preventDefault();
                    first.focus();
                }
                return;
            }
        }
    }
});
