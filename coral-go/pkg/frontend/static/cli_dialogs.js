/* Moved out of modals.js as it was (#4684): The CLI checks of the Settings paths (verify one or all), the 'CLI not found' dialog of a launch, and the permission-prompt warning. No behaviour change. */
import { importOnce } from './import_once.js';
import { registerDialog } from './modal_chrome.js';
import { state } from './state.js';
import { escapeHtml, escapeAttr } from './utils.js';
import { normServer, LOCAL_SERVER, serverFetch } from './server_base.js';
import { serverLabel } from './launch_server.js';
const { _settingsFor } = await importOnce('./launch_models.js');
const { _getPermissionFlagsForAgentMode, _hasPermFlag } = await importOnce('./permission_flags.js');
const { CLI_INSTALL_INSTRUCTIONS } = await importOnce('./cli_check.js');
const { showSettingsModal } = await importOnce('./settings_dialog.js');

/** Verify a CLI path by calling the backend check endpoint. */
async function _checkOneCLI(inputId, resultId) {
    const input = document.getElementById(inputId);
    const resultEl = document.getElementById(resultId);
    if (!input || !resultEl) return;

    const binary = input.value.trim() || input.placeholder;
    resultEl.textContent = '...';
    resultEl.className = 'cli-verify-result';

    try {
        // The type lets the server accept a path to that CLI's binary (#3583).
        const type = inputId.replace('settings-cli-path-', '');
        const resp = await fetch(`/api/system/cli-check?binary=${encodeURIComponent(binary)}&type=${encodeURIComponent(type)}`);
        const data = await resp.json();
        if (!resp.ok) {
            resultEl.textContent = 'Not allowed';
            resultEl.title = data.error || '';
            resultEl.classList.add('cli-verify-fail');
        } else if (data.found) {
            resultEl.textContent = data.version || 'Found';
            resultEl.classList.add('cli-verify-ok');
        } else {
            resultEl.textContent = 'Not found';
            resultEl.classList.add('cli-verify-fail');
        }
    } catch {
        resultEl.textContent = 'Error';
        resultEl.classList.add('cli-verify-fail');
    }
}

window._verifyAllCLIs = async function() {
    const btn = document.getElementById('verify-all-btn');
    if (btn) { btn.disabled = true; btn.textContent = 'Checking...'; }
    await Promise.all([
        _checkOneCLI('settings-cli-path-claude', 'cli-result-claude'),
        _checkOneCLI('settings-cli-path-codex', 'cli-result-codex'),
        _checkOneCLI('settings-cli-path-agy', 'cli-result-agy'),
        _checkOneCLI('settings-cli-path-pi', 'cli-result-pi'),
    ]);
    if (btn) { btn.disabled = false; btn.textContent = 'Verify All'; }
};

function _showCLINotFoundModal(agentType, server = LOCAL_SERVER, { retry = null } = {}) {
    const info = CLI_INSTALL_INSTRUCTIONS[agentType] || { name: agentType, cmd: `Install ${agentType} CLI` };
    const name = escapeHtml(info.name); // an agent type the table does not know is shown as it came (#4853)
    const remote = normServer(server) !== LOCAL_SERVER;
    const where = remote ? `on ${escapeHtml(serverLabel(server, state.servers))}` : 'on this computer';
    const modal = document.createElement('div');
    modal.className = 'modal';
    modal.style.display = 'flex';
    modal.innerHTML = `
        <div class="modal-content" style="width:480px">
            <h3>${name} CLI Not Found</h3>
            <p style="color:var(--text-secondary);font-size:0.8125rem;line-height:1.5;margin:8px 0 12px">
                Coral could not find the <code>${name}</code> command ${where}. Install it, then press Check again.
            </p>
            <div style="background:var(--bg-tertiary);border:1px solid var(--border);border-radius:6px;padding:10px 14px;font-family:var(--font-mono);font-size:0.8125rem;display:flex;align-items:center;gap:8px">
                <code style="flex:1;user-select:all">${escapeHtml(info.cmd)}</code>
                <button class="btn btn-small" data-on-click="info-copy" data-cmd="${escapeAttr(info.cmd)}">Copy</button>
            </div>
            ${/^https:\/\/[^\s"'<>]+$/.test(info.url || '') ? `<p style="font-size:0.8125rem;margin:10px 0 0">Or follow the official install page:<br><a href="${escapeAttr(info.url)}" target="_blank" rel="noopener noreferrer" style="color:var(--accent);overflow-wrap:anywhere">${escapeHtml(info.url.replace(/^https:\/\//, ''))}</a></p>` : ''}
            ${info.note ? `<p style="color:var(--text-secondary);font-size:0.75rem;line-height:1.5;margin:8px 0 0">${escapeHtml(info.note)}</p>` : ''}
            <p class="cli-recheck-status" role="status" aria-live="polite"></p>
            <div class="modal-actions" style="margin-top:16px">
                ${remote ? '' : '<button class="btn" data-action="settings">Set CLI path</button>'}
                <button class="btn" data-action="close">OK</button>
                <button class="btn btn-primary" data-action="recheck">Check again</button>
            </div>
        </div>
    `;
    document.body.appendChild(modal);
    const close = registerDialog(modal);
    modal.addEventListener('click', (e) => {
        if (e.target.dataset?.action === 'close') close();
        if (e.target.dataset?.action === 'settings') { close(); showSettingsModal(); }
        if (e.target.dataset?.action === 'recheck') _recheckCLI(e.target, modal, agentType, server, close, retry);
    });
}

/** "Check again" in the Not Found dialog (#5835): ask the server to look again. When the CLI is there: with a retry (the caller's own launch) close the dialog and run it, as the person meant to;
 *  without one say "Found it. Press Launch again." and close nothing (a launch from the board has no form to repeat). When it is not: say so (a Coral that was started before the install may not see
 *  the new PATH until it is restarted). The status is a live region that is always displayed and only has its text set, so a screen reader hears each answer. */
async function _recheckCLI(button, modal, agentType, server, close, retry) {
    const status = modal.querySelector('.cli-recheck-status');
    status.textContent = '';
    button.disabled = true; button.textContent = 'Checking…';
    try {
        const resp = await serverFetch(server, `/api/system/cli-check?type=${encodeURIComponent(agentType)}&source=cli_recheck`, { cache: 'no-store' });
        const data = await resp.json();
        if (resp.ok && data && data.found) {
            if (typeof retry === 'function') { close(); retry(); return; }
            status.textContent = 'Found it. Press Launch again.';
        } else status.textContent = resp.ok ? 'Still not found. If you just installed it, restart Coral so it sees the new install, or use Set CLI path.' : 'Could not check just now. Try again.';
    } catch { status.textContent = 'Could not check just now. Try again.'; }
    button.disabled = false; button.textContent = 'Check again';
}

function _checkPermissionFlag(flagsStr, agentType, server = LOCAL_SERVER) {
    const mode = _settingsFor(server).default_permission_mode || 'bypassPermissions';
    const permFlag = _getPermissionFlagsForAgentMode(agentType, mode);
    if (!permFlag) return Promise.resolve('skip');
    return new Promise((resolve) => {
        if (flagsStr && _hasPermFlag(flagsStr)) {
            resolve('skip');
            return;
        }
        const modal = document.createElement('div');
        modal.className = 'modal';
        modal.style.display = 'flex';
        modal.innerHTML = `
            <div class="modal-content" style="width:460px">
                <h3>Permission Prompt Warning</h3>
                <p style="color:var(--text-secondary);font-size:0.8125rem;line-height:1.5;margin:8px 0 16px">
                    Agents may get stuck on permission prompts without <code>${escapeHtml(permFlag)}</code>. Would you like to enable it?
                </p>
                <div class="modal-actions" style="gap:8px">
                    <button class="btn" data-action="cancel">Cancel</button>
                    <button class="btn" data-action="skip">Launch Without</button>
                    <button class="btn btn-primary" data-action="enable">Enable &amp; Launch</button>
                </div>
            </div>
        `;
        document.body.appendChild(modal);
        const close = registerDialog(modal, { onClose: () => resolve(null) }); // Escape and the backdrop cancel: the launch must not wait on a dialog that is gone
        modal.addEventListener('click', (e) => {
            const action = e.target.dataset?.action;
            if (action) {
                resolve(action === 'cancel' ? null : action);
                close();
            }
        });
    });
}

export { _checkPermissionFlag, _showCLINotFoundModal };
