/* Page-wide modal behaviour, loaded with the page (#3140): a click on a modal's
 * backdrop closes it, Escape closes the topmost visible modal, and Tab stays
 * inside it. It covers every dialog (confirm, alert, restart, macro, ...), not
 * only the ones in modals.js, which loads the first time one of its dialogs
 * opens; those are closed through their window functions, which exist by the
 * time such a dialog can be visible. */

import { hideRestartModal } from './controls.js';

// Close modals on outside click
document.addEventListener("click", (e) => {
    const launchModal = document.getElementById("launch-modal");
    const infoModal = document.getElementById("info-modal");
    const resumeModal = document.getElementById("resume-modal");
    const settingsModal = document.getElementById("settings-modal");
    const restartModal = document.getElementById("restart-modal");
    if (e.target === launchModal) {
        window.hideLaunchModal?.();
    }
    if (e.target === infoModal) {
        window.hideInfoModal?.();
    }
    if (e.target === resumeModal) {
        window.hideResumeModal?.();
    }
    if (e.target === settingsModal) {
        window.hideSettingsModal?.();
    }
    if (e.target === restartModal) {
        hideRestartModal();
    }
    const macroModal = document.getElementById("macro-modal");
    if (e.target === macroModal) {
        macroModal.style.display = "none";
    }
    const webhookModal = document.getElementById("webhook-modal");
    if (e.target === webhookModal) window.hideWebhookModal?.();
    const alertModal = document.getElementById("alert-modal");
    if (e.target === alertModal) window.hideAlertModal?.();
    const confirmModal = document.getElementById("confirm-modal");
    if (e.target === confirmModal) window.hideConfirmModal?.();
});

// Close topmost visible modal on Escape and trap focus inside it
document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") {
        // Find topmost visible modal and close only that one
        const modals = document.querySelectorAll('.modal');
        for (let i = modals.length - 1; i >= 0; i--) {
            if (modals[i].style.display !== 'none' && modals[i].getClientRects().length > 0) {
                const id = modals[i].id;
                if (id === 'launch-modal') window.hideLaunchModal?.();
                else if (id === 'info-modal') window.hideInfoModal?.();
                else if (id === 'resume-modal') window.hideResumeModal?.();
                else if (id === 'settings-modal') window.hideSettingsModal?.();
                else if (id === 'restart-modal') hideRestartModal();
                else if (id === 'macro-modal') modals[i].style.display = 'none';
                else if (id === 'webhook-modal') window.hideWebhookModal?.();
                else if (id === 'alert-modal') window.hideAlertModal?.();
                else if (id === 'confirm-modal') window.hideConfirmModal?.();
                else if (id === 'prompt-modal') window.hidePromptModal?.();
                else if (id === 'team-agent-modal') window.hideTeamAgentModal?.();
                else if (id === 'default-prompts-modal') window.hideDefaultPromptsModal?.();
                else if (id === 'team-wizard-modal') window.hideTeamWizardModal?.();
                else if (id === 'save-template-modal') modals[i].style.display = 'none';
                else if (id === 'job-modal') modals[i].style.display = 'none';
                else if (id === 'quick-launch-modal') modals[i].style.display = 'none';
                else if (id === 'add-agent-board-modal') window.hideAddAgentBoardModal?.();
                else if (id === 'custom-view-modal') modals[i].style.display = 'none';
                else if (id === 'mobile-connect-modal') window._hideMobileConnectModal();
                else if (id === 'theme-configurator-modal') modals[i].style.display = 'none';
                else modals[i].style.display = 'none';
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
                if (e.shiftKey && document.activeElement === first) {
                    e.preventDefault();
                    last.focus();
                } else if (!e.shiftKey && document.activeElement === last) {
                    e.preventDefault();
                    first.focus();
                }
                return;
            }
        }
    }
});
