/* Moved out of modals.js as it was (#4684): The flag shortcut buttons (toggleFlag, the permission-mode flags of an input, keeping the buttons in step with the text box). No behaviour change. */
import { importOnce } from './import_once.js';
import { onBuilt } from './lazy_dom.js';
const { _normalizePermFlagsForAgent } = await importOnce('./permission_flags.js');

export function toggleFlag(inputId, flag) {
    const input = document.getElementById(inputId);
    const current = input.value.trim();
    const flags = current ? current.split(/\s+/) : [];
    const idx = flags.indexOf(flag);
    if (idx >= 0) {
        flags.splice(idx, 1);
    } else {
        flags.push(flag);
    }
    input.value = flags.join(" ");
    // Update button active states
    input.dispatchEvent(new Event("input"));
}

function _setPermFlagsForInput(inputId, agentType, mode) {
    const input = document.getElementById(inputId);
    if (!input) return;
    input.value = _normalizePermFlagsForAgent(input.value, agentType, mode);
    input.dispatchEvent(new Event("input"));
}
window._setPermFlagsForInput = _setPermFlagsForInput;

// Sync flag shortcut button active states with the text input
function _syncFlagButtons(inputId) {
    const input = document.getElementById(inputId);
    if (!input) return;
    const flags = input.value.trim() ? input.value.trim().split(/\s+/) : [];
    const container = input.closest("label")?.nextElementSibling;
    if (!container) return;
    container.querySelectorAll(".flag-shortcut-btn").forEach(btn => {
        const flag = btn.textContent.trim();
        btn.classList.toggle("active", flags.includes(flag));
    });
}

// Attach input listeners for flag sync. This module loads on first use (#3140), usually long after DOMContentLoaded, so
// attach at once; the Job dialog's input once that dialog is built (#4500), which can be later still.
function _attachFlagSync(id) {
    const el = document.getElementById(id);
    if (!el || el._flagSync) return;
    el._flagSync = true;
    el.addEventListener("input", () => _syncFlagButtons(id));
}
for (const id of ["launch-flags", "restart-flags", "team-flags"]) _attachFlagSync(id);
onBuilt("job-modal", () => _attachFlagSync("job-modal-flags"));

export { _syncFlagButtons };
