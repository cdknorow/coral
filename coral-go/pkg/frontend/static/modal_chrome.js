/* Page-wide modal behaviour, loaded with the page (#3140): a click on a modal's
 * backdrop closes it, Escape closes the topmost visible modal, and Tab stays
 * inside it. It covers every dialog (confirm, alert, restart, macro, ...), not
 * only the ones in modals.js, which loads the first time one of its dialogs
 * opens; those are closed through their window functions, which exist by the
 * time such a dialog can be visible. */

import { hideRestartModal } from './controls.js';

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
const DIALOGS = {
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
};

function hideDialog(modal) {
    const d = DIALOGS[modal.id];
    if (d && d.hide) d.hide();
    else modal.style.display = 'none';
}

// Close modals on outside click
document.addEventListener("click", (e) => {
    const t = e.target;
    if (t && DIALOGS[t.id] && DIALOGS[t.id].backdrop) hideDialog(t);
});

// Close topmost visible modal on Escape and trap focus inside it
document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") {
        // Find topmost visible modal and close only that one
        const modals = document.querySelectorAll('.modal');
        for (let i = modals.length - 1; i >= 0; i--) {
            if (modals[i].style.display !== 'none' && modals[i].getClientRects().length > 0) {
                hideDialog(modals[i]);
                e.preventDefault();
                return;
            }
        }
    }

    // Focus trapping: Tab/Shift+Tab stays inside the topmost visible modal
    if (e.key === "Tab") {
        const modals = document.querySelectorAll('.modal');
        for (let i = modals.length - 1; i >= 0; i--) {
            if (modals[i].style.display !== 'none' && modals[i].getClientRects().length > 0) {
                const focusable = Array.from(modals[i].querySelectorAll(
                    'button:not([disabled]), input:not([disabled]), textarea:not([disabled]), select:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])'
                )).filter(el => el.getClientRects().length > 0);
                if (focusable.length === 0) return;
                const first = focusable[0];
                const last = focusable[focusable.length - 1];
                // Focus outside the modal (left on the control that opened it) comes in too.
                const outside = !modals[i].contains(document.activeElement);
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
