/* Moved out of modals.js as it was (#4684): The pieces every launch dialog shares: the launch steps, the working-directory resets, the 'cannot launch here' check, the request to the server (with its CLI-not-found and invalid-field outcomes), and what a launch refreshes and says. No behaviour change. */
import { importOnce } from './import_once.js';
import { state } from './state.js';
import { showToast } from './utils.js';
import { blockedReason } from './hub_access.js';
import { loadLiveSessions } from './api.js';
import { isHub, normServer, LOCAL_SERVER, noteBoardLabels } from './server_base.js';
import { chooseBoard, retryPayload, isBoardNameExists } from './board_choice.js';
import { serverLabel, launchBlockReason, postLaunch, waitForLaunched } from './launch_server.js';
const { _loadServerSettings } = await importOnce('./launch_models.js');
const { _showCLINotFoundModal } = await importOnce('./cli_dialogs.js');
const { revealField } = await importOnce('./section_dialog.js');

function _syncMobileLaunchSections(step) {
    const isMobile = window.innerWidth <= 767;
    const root = step ? document.getElementById(`launch-step-${step}`) : document.getElementById("launch-modal");
    if (!root) return;
    root.querySelectorAll(".mobile-launch-section").forEach(section => {
        section.open = !isMobile;
    });
}

/** New Agent opens on the folder alone; the name, persona, agent, prompt, permissions and board sit behind "More options" (#5595). */
export function setLaunchMore(open) {
    const more = document.getElementById("launch-agent-more");
    if (!more) return;
    more.hidden = !open;
    document.getElementById("launch-agent-more-btn")?.setAttribute("aria-expanded", String(open));
    const content = document.querySelector("#launch-modal .modal-content");
    if (content) content.classList.toggle("launch-simple", !open && document.getElementById("launch-step-agent").style.display !== "none");
}
// A field that is about to take the focus (a rejected board name, say) opens "More options" when it sits inside (section_dialog.js revealField).
document.addEventListener("sd-more-open", e => { if (e.target.id === "launch-agent-more") setLaunchMore(true); });

function _showLaunchStep(step) {
    document.getElementById("launch-step-chooser").style.display = step === "chooser" ? "flex" : "none";
    document.getElementById("launch-step-agent").style.display = step === "agent" ? "flex" : "none";
    document.getElementById("launch-step-terminal").style.display = step === "terminal" ? "flex" : "none";
    document.getElementById("launch-step-team").style.display = step === "team" ? "flex" : "none";
    if (step === "team") { const health = document.getElementById("launch-team-health"); if (health) health.checked = false; } // the health monitor is off for each new team (#4962)
    // Widen the modal for multi-column layouts
    const content = document.querySelector("#launch-modal .modal-content");
    if (content) {
        // New Agent and Agent Team are sectioned dialogs (#4950, section_dialog.js): the Settings shell's size; the chooser and the terminal form keep the narrow one.
        const sectioned = step === "agent" || step === "team";
        content.classList.toggle("sd-modal-content", sectioned);
        content.classList.toggle("modal-content-wide", !sectioned);
        content.classList.toggle("launch-choosing", step === "chooser"); // four choices in a row (#3630)
        content.classList.toggle("launch-simple", step === "agent" && document.getElementById("launch-agent-more")?.hidden === true);
    }
    _syncMobileLaunchSections(step);
}

/**
 * Point working-directory inputs at `server`. A path is only meaningful on the
 * machine it was typed for, so a remote never inherits the hub's directory:
 * the field becomes the remote's default_working_dir or empty (Browse starts at
 * the remote's home). Local restores the hub's default / coral root.
 */
function _resetDirInputs(ids, server, settings, { switching = false } = {}) {
    const remote = server !== LOCAL_SERVER;
    for (const id of ids) {
        const el = document.getElementById(id);
        if (!el) continue;
        clearFieldInvalid(el); // a reset value is not the rejected one
        if (el.dataset.localPlaceholder === undefined) el.dataset.localPlaceholder = el.placeholder || '';
        if (remote) {
            el.value = settings.default_working_dir || '';
            el.placeholder = `Path on ${serverLabel(server, state.servers)} (use Browse)`;
            el.dataset.dirServer = server;
        } else {
            // A field last filled for a remote must not keep that remote's path.
            const wasRemote = !!el.dataset.dirServer;
            delete el.dataset.dirServer;
            el.placeholder = el.dataset.localPlaceholder;
            if (settings.default_working_dir) el.value = settings.default_working_dir;
            else if (switching || wasRemote) el.value = el.dataset.coralRoot || '';
        }
    }
}

/** Toast and return true when `server` cannot be launched on. */
function _launchBlocked(server) {
    const why = launchBlockReason(server, isHub(), state.servers) || blockedReason(server, 'launch');
    if (why) showToast(why, true);
    return !!why;
}

/**
 * Send a launch to `server` and surface the outcome. Returns the response data
 * on success, or null after showing the error. Never falls back to another
 * server.
 */
export async function _launchOn(server, path, payload, agentType, { dirInput = null } = {}) { // exported for board_ids_browser
    let res = await postLaunch(server, path, payload);
    // A board of that name exists (#3536): the person chooses to join it or start another.
    if (!res.ok && isBoardNameExists(res)) {
        // The boards of that name are known now: the list can tell them apart (keyLabel's short id).
        noteBoardLabels(res.data.candidates, 'board_id', server);
        const pick = await chooseBoard(res.data, payload.board_name || '');
        const retry = retryPayload(payload, pick);
        if (!retry) return null; // cancelled: nothing launched
        res = await postLaunch(server, path, retry);
    }
    if (res.ok && res.data && res.data.board_id) noteBoardLabels([res.data], 'board_id', server); // the board it joined or started
    if (!res.ok) {
        if (agentType && res.error.includes('not found') && res.error.includes('CLI')) _showCLINotFoundModal(agentType, server);
        else showToast(res.error, true);
        if (dirInput && res.data && res.data.field === 'working_dir') markFieldInvalid(dirInput, res.error);
        return null;
    }
    return res.data;
}

/** Mark a form field the server rejected: aria-invalid, a red outline and the
 *  reason as its title, focused. Cleared by the next edit, typed or set by
 *  Browse ('change'), and when the dialog closes or its fields reset (#3152). */
 function markFieldInvalid(input, message) {
    clearFieldInvalid(input);
    const previousTitle = input.title;
    input.setAttribute('aria-invalid', 'true');
    input.classList.add('input-invalid');
    input.title = message;
    revealField(input); // a field in a section that is not open: its section opens first, so the focus lands
    input.focus();
    const clear = () => clearFieldInvalid(input);
    input._clearInvalid = () => {
        input.removeEventListener('input', clear);
        input.removeEventListener('change', clear);
        input.removeAttribute('aria-invalid');
        input.classList.remove('input-invalid');
        input.title = previousTitle;
    };
    input.addEventListener('input', clear);
    input.addEventListener('change', clear);
}

/** Undo markFieldInvalid, if the field is marked. */
 function clearFieldInvalid(input) {
    if (input && input._clearInvalid) {
        const undo = input._clearInvalid;
        input._clearInvalid = null;
        undo();
    }
}

/** Refresh the agent list after a launch; on a remote wait for the hub poller to see the new agent. */
function _refreshAfterLaunch(server, target) {
    if (normServer(server) === LOCAL_SERVER) return;
    waitForLaunched(server, target, loadLiveSessions);
}

export function hideLaunchModal() {
    document.getElementById("launch-modal").style.display = "none";
    for (const id of ['launch-dir', 'launch-terminal-dir']) clearFieldInvalid(document.getElementById(id));
}

/** " on <server label>" for remotes, "" for local. */
function _onServerSuffix(server) {
    return normServer(server) === LOCAL_SERVER ? '' : ` on ${serverLabel(server, state.servers)}`;
}

/** Run `fn` once the target server's settings are available (immediately for local; on failure fn still runs with defaults). */
function _withServerSettings(server, fn) {
    if (normServer(server) === LOCAL_SERVER) { fn(); return; }
    _loadServerSettings(server).catch(() => null).then(fn);
}

export { markFieldInvalid, _launchBlocked, _onServerSuffix, _refreshAfterLaunch, _resetDirInputs, _showLaunchStep, _syncMobileLaunchSections, _withServerSettings };
