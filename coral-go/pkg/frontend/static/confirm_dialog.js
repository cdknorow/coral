/* Moved out of render.js as it was (#3795): no behaviour change. */

// Called when the open confirm modal is dismissed without confirming.
let _confirmCancel = null;

export function showConfirmModal(title, message, onConfirm, onCancel) {
    _dismissConfirm(); // a modal replaced before an answer counts as cancelled
    document.getElementById("confirm-modal-title").textContent = title;
    document.getElementById("confirm-modal-message").textContent = message;
    const yesBtn = document.getElementById("confirm-modal-yes");
    const newBtn = yesBtn.cloneNode(true);
    yesBtn.parentNode.replaceChild(newBtn, yesBtn);
    newBtn.addEventListener("click", () => {
        _confirmCancel = null;
        hideConfirmModal();
        onConfirm();
    });
    _confirmCancel = onCancel || null;
    document.getElementById("confirm-modal").style.display = "flex";
}

function _dismissConfirm() {
    const cancel = _confirmCancel;
    _confirmCancel = null;
    if (cancel) cancel();
}

export function hideConfirmModal() {
    _dismissConfirm();
    const modal = document.getElementById("confirm-modal");
    modal.style.display = "none";
    // Remove any extra width classes added by export modal
    const content = modal.querySelector('.modal-content');
    if (content) content.classList.remove('modal-content-wide', 'modal-content-extra-wide');
}
