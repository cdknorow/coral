/* The dashboard's prompt and alert dialogs, and copying a folder path (moved out of render.js, #4996; no behaviour change). */
import { escapeAttr, AGENT_NAME_PALETTE, showToast } from './utils.js';

// opts.color = { value, auto }: show a colour row (palette swatches plus an
// "Auto" swatch showing `auto`); onConfirm then receives (text, color) where
// color is "#rrggbb" or "" for Auto.
export function showPromptModal(title, label, defaultValue, onConfirm, opts = {}) {
    document.getElementById("prompt-modal-title").textContent = title;
    const colorRow = document.getElementById("prompt-modal-colors");
    let pickedColor = (opts.color && opts.color.value) || "";
    if (colorRow) {
        colorRow.style.display = opts.color ? "" : "none";
        if (opts.color) {
            const sw = (value, bg, label) => `<button type="button" class="color-swatch${value === pickedColor ? " selected" : ""}" data-color="${escapeAttr(value)}" style="--swatch:${bg}" title="${escapeAttr(label)}" aria-label="${escapeAttr(label)}" aria-pressed="${value === pickedColor}">${value ? "" : "A"}</button>`;
            colorRow.querySelector(".color-swatches").innerHTML =
                sw("", opts.color.auto, "Auto") + AGENT_NAME_PALETTE.map(c => sw(c, c, c)).join("");
            colorRow.querySelectorAll(".color-swatch").forEach(btn => {
                btn.onclick = () => {
                    pickedColor = btn.dataset.color;
                    colorRow.querySelectorAll(".color-swatch").forEach(b => {
                        b.classList.toggle("selected", b === btn);
                        b.setAttribute("aria-pressed", String(b === btn));
                    });
                };
            });
        }
    }
    const labelEl = document.getElementById("prompt-modal-label");
    if (labelEl) labelEl.textContent = label || '';
    const input = document.getElementById("prompt-modal-input");
    input.value = defaultValue || '';
    const okBtn = document.getElementById("prompt-modal-ok");
    const newBtn = okBtn.cloneNode(true);
    okBtn.parentNode.replaceChild(newBtn, okBtn);
    const modal = document.getElementById("prompt-modal");
    const submit = () => {
        const val = input.value.trim();
        if (!val) { input.focus(); return; }
        hidePromptModal();
        onConfirm(val, pickedColor);
    };
    newBtn.addEventListener("click", submit);
    input.onkeydown = (e) => { if (e.key === 'Enter') submit(); if (e.key === 'Escape') hidePromptModal(); };
    modal.onclick = (e) => { if (e.target === modal) hidePromptModal(); };
    modal.style.display = "flex";
    setTimeout(() => input.focus(), 50);
}

export function hidePromptModal() {
    document.getElementById("prompt-modal").style.display = "none";
}

export function showAlertModal(title, message, onClose) {
    document.getElementById("alert-modal-title").textContent = title;
    document.getElementById("alert-modal-message").textContent = message;
    const okBtn = document.getElementById("alert-modal-ok");
    const newBtn = okBtn.cloneNode(true);
    okBtn.parentNode.replaceChild(newBtn, okBtn);
    const modal = document.getElementById("alert-modal");
    newBtn.addEventListener("click", () => {
        hideAlertModal();
        onClose?.();
    });
    modal.onclick = (e) => { if (e.target === modal) hideAlertModal(); };
    modal.style.display = "flex";
}

export function hideAlertModal() {
    const modal = document.getElementById("alert-modal");
    if (!modal) return;
    modal.style.display = "none";
}

export function copyFolderPath(path) {
    if (!path) return;
    navigator.clipboard.writeText(path).then(() => {
        showToast("Copied path to clipboard");
    });
}
