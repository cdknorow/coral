/* Close a dialog on a click on its backdrop or on Escape, and let go of the Escape listener when it closes (#3857). Four dialogs
 * (team wizard, create task, task detail, subagent) each wrote this out; modal_chrome.js's page-wide Escape also closes them but does
 * not run a dialog's own cleanup, so the dialog's own close function is what these call. */

/** Wire `modal` to call `close` on a backdrop click or Escape. Safe to call again while open: the earlier Escape listener is released first. */
export function closeOnBackdropAndEscape(modal, close) {
    modal.onclick = (e) => { if (e.target === modal) close(); };
    releaseEscape(modal);
    modal._escHandler = (e) => { if (e.key === 'Escape') close(); };
    document.addEventListener('keydown', modal._escHandler);
}

/** Release the Escape listener closeOnBackdropAndEscape added (nothing if there is none). */
export function releaseEscape(modal) {
    if (modal._escHandler) {
        document.removeEventListener('keydown', modal._escHandler);
        modal._escHandler = null;
    }
}
