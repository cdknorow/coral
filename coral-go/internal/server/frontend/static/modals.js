/* Modal management: launch and info dialogs */

import { state } from './state.js';
import { showToast, escapeHtml, escapeAttr } from './utils.js';
import { loadLiveSessions } from './api.js';
import { loadBoardProjects } from './message_board.js';
import { getEngineNames, getEngineName, setRendererOverride } from './renderers.js';
import { renderCaptureText, syncPaneWidth } from './capture.js';
import { hideRestartModal } from './controls.js';
import { updateTerminalTheme, getTerminal } from './xterm_renderer.js';

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

// Track which launch mode is active: null (chooser), 'agent', or 'terminal'
let _launchMode = null;
let _existingTeamBoardNames = new Set();
let _teamAgentModalRow = null;

// ── Canonical per-agent-type model list ────────────────────────────────
// Fetched once from GET /api/agent-models and cached for the tab lifetime.
// Shape: { claude: [...], codex: [], gemini: [], terminal: [] }
let _agentModelsPromise = null;
function _getAgentModels() {
    if (!_agentModelsPromise) {
        _agentModelsPromise = fetch('/api/agent-models')
            .then(r => r.ok ? r.json() : {})
            .catch(() => ({}));
    }
    return _agentModelsPromise;
}
window._getAgentModels = _getAgentModels;

// _fillModelDatalist replaces <option>s in the given <datalist> element with
// the canonical list for the agent type. Unknown types or an empty list
// result in an empty datalist (the input still accepts free-form text).
function _fillModelDatalist(datalistEl, agentType, modelsByType) {
    if (!datalistEl) return;
    const list = (modelsByType && modelsByType[agentType]) || [];
    datalistEl.innerHTML = list
        .map(m => `<option value="${escapeAttr(m)}"></option>`)
        .join('');
}
window._fillModelDatalist = _fillModelDatalist;

// ── User's per-type default model (for ACF pre-fill) ───────────────────
// Pulled from the same /api/settings payload the Settings modal uses, but
// cached separately with its own invalidation (triggered after a Settings
// save so the next form open sees the new default).
// Shape: { claude: "<id>", codex: "<id>", gemini: "<id>" }
let _defaultModelsPromise = null;
function _getDefaultModels() {
    if (!_defaultModelsPromise) {
        _defaultModelsPromise = fetch('/api/settings')
            .then(r => r.ok ? r.json() : { settings: {} })
            .then(data => {
                const s = data.settings || {};
                return {
                    claude: (s.default_model_claude || '').trim(),
                    codex: (s.default_model_codex || '').trim(),
                    gemini: (s.default_model_gemini || '').trim(),
                    pi: (s.default_model_pi || '').trim(),
                };
            })
            .catch(() => ({ claude: '', codex: '', gemini: '', pi: '' }));
    }
    return _defaultModelsPromise;
}
function _invalidateDefaultModels() {
    _defaultModelsPromise = null;
}
window._invalidateDefaultModels = _invalidateDefaultModels;

function _syncMobileLaunchSections(step) {
    const isMobile = window.innerWidth <= 767;
    const root = step ? document.getElementById(`launch-step-${step}`) : document.getElementById("launch-modal");
    if (!root) return;
    root.querySelectorAll(".mobile-launch-section").forEach(section => {
        section.open = !isMobile;
    });
}

function _showLaunchStep(step) {
    document.getElementById("launch-step-chooser").style.display = step === "chooser" ? "flex" : "none";
    document.getElementById("launch-step-agent").style.display = step === "agent" ? "flex" : "none";
    document.getElementById("launch-step-terminal").style.display = step === "terminal" ? "flex" : "none";
    document.getElementById("launch-step-team").style.display = step === "team" ? "flex" : "none";
    // Widen the modal for multi-column layouts
    const content = document.querySelector("#launch-modal .modal-content");
    if (content) {
        content.classList.toggle("modal-content-agent-wide", step === "agent");
        content.classList.toggle("modal-content-extra-wide", step === "team");
        content.classList.toggle("modal-content-wide", step !== "team" && step !== "agent");
    }
    _syncMobileLaunchSections(step);
}

function _selectLaunchType(type) {
    if (type === "back") {
        _launchMode = null;
        _showLaunchStep("chooser");
        return;
    }
    _launchMode = type;
    _showLaunchStep(type);
    if (type === "agent") {
        _loadAgentBoardProjects();
        _initAgentPresets();
        // Render agent config form
        renderAgentConfigForm('launch-agent-acf', {
            showPreset: false,
            showName: false,
            value: { agentType: state.settings?.default_agent_type || 'claude' },
        });
    } else if (type === "team") {
        _initTeamForm();
    } else {
        document.getElementById("launch-terminal-name").focus();
    }
}
window._selectLaunchType = _selectLaunchType;

export function showLaunchModal() {
    _launchMode = null;
    document.getElementById("launch-modal").style.display = "flex";
    _showLaunchStep("chooser");

    // Reset agent form
    const agentNameInput = document.getElementById("launch-agent-name-input");
    if (agentNameInput) agentNameInput.value = "";
    document.getElementById("launch-board-name").value = "";
    document.getElementById("launch-board-server").value = "";
    const agentBoardSelect = document.getElementById("launch-board-select");
    if (agentBoardSelect) agentBoardSelect.value = "";
    document.getElementById("launch-new-board-row").style.display = "none";
    document.getElementById("launch-board-server-row").style.display = "none";

    // Reset terminal form
    document.getElementById("launch-terminal-name").value = "";

    // Reset team form
    document.getElementById("team-agents-list").innerHTML = "";
    document.getElementById("team-board-name").value = "";
    const teamFlagsEl = document.getElementById("team-flags");
    if (teamFlagsEl) teamFlagsEl.value = "";
    const teamPermModeEl = document.getElementById("team-permission-mode");
    if (teamPermModeEl) {
        teamPermModeEl.value = (state.settings && state.settings.default_permission_mode) || 'bypassPermissions';
        _updatePermModeDescription(teamPermModeEl);
    }

    // Pre-fill from global settings
    const s = state.settings || {};
    const dirInput = document.getElementById("launch-dir");
    const termDirInput = document.getElementById("launch-terminal-dir");
    if (s.default_working_dir) {
        if (dirInput) dirInput.value = s.default_working_dir;
        if (termDirInput) termDirInput.value = s.default_working_dir;
    }
    const typeSelect = document.getElementById("launch-type");
    if (s.default_agent_type && typeSelect) {
        typeSelect.value = s.default_agent_type;
    }

    _syncMobileLaunchSections();
}

export function hideLaunchModal() {
    document.getElementById("launch-modal").style.display = "none";
}

function _setTeamWizardError(message = '') {
    const errorEl = document.getElementById('team-wizard-error');
    if (!errorEl) return;
    if (message) {
        errorEl.textContent = message;
        errorEl.style.display = 'block';
        return;
    }
    errorEl.textContent = '';
    errorEl.style.display = 'none';
}

let _wizardBodyOriginalHTML = null;

function showTeamWizardModal() {
    const modal = document.getElementById('team-wizard-modal');
    if (!modal) return;

    // Restore wizard body if it was replaced by success state
    const body = modal.querySelector('.team-wizard-body');
    if (body && _wizardBodyOriginalHTML && body.querySelector('.team-wizard-success')) {
        body.innerHTML = _wizardBodyOriginalHTML;
    } else if (body && !_wizardBodyOriginalHTML) {
        _wizardBodyOriginalHTML = body.innerHTML;
    }

    const directiveEl = document.getElementById('team-wizard-directive');
    const compositionEl = document.getElementById('team-wizard-composition');
    const generateBtn = document.getElementById('team-wizard-generate-btn');

    // If a job is actively running, show generating state
    if (_activeGenerateJobId) {
        if (generateBtn) {
            generateBtn.disabled = true;
            generateBtn.textContent = 'Generating...';
            generateBtn.style.display = '';
        }
    } else {
        _setTeamWizardError('');
        if (directiveEl) directiveEl.value = '';
        if (compositionEl) compositionEl.value = '';
        if (generateBtn) {
            generateBtn.disabled = false;
            generateBtn.textContent = 'Generate Team';
            generateBtn.style.display = '';
        }
    }
    modal.style.display = 'flex';
    if (!_activeGenerateJobId) directiveEl?.focus();

    // Close on backdrop click
    modal.onclick = (e) => { if (e.target === modal) hideTeamWizardModal(); };
    // Close on Escape
    modal._escHandler = (e) => { if (e.key === 'Escape') hideTeamWizardModal(); };
    document.addEventListener('keydown', modal._escHandler);
}
window.showTeamWizardModal = showTeamWizardModal;

function hideTeamWizardModal() {
    const modal = document.getElementById('team-wizard-modal');
    if (!modal) return;
    modal.style.display = 'none';
    _setTeamWizardError('');
    if (modal._escHandler) {
        document.removeEventListener('keydown', modal._escHandler);
        modal._escHandler = null;
    }
}
window.hideTeamWizardModal = hideTeamWizardModal;

function _normalizeGeneratedTeamPayload(payload) {
    const team = payload?.team || payload;
    if (!team || typeof team !== 'object') {
        throw new Error('Team generator returned an empty response.');
    }
    if (!Array.isArray(team.agents) || team.agents.length === 0) {
        throw new Error('Team generator did not return any agents.');
    }
    return {
        name: team.name || 'Generated Team',
        flags: Array.isArray(team.flags) ? team.flags.join(' ') : (team.flags || ''),
        agents: team.agents,
    };
}

function _populateGeneratedTeamDraft(team) {
    _skipDefaultTeamAgents = true;
    // Open the launch modal directly to the team step, skipping the chooser.
    // Don't call showLaunchModal() — it resets to the chooser and clears the agent list.
    document.getElementById("launch-modal").style.display = "flex";
    _showLaunchStep("team");
    _launchMode = "team";
    _initTeamForm();

    // Show generated-team banner
    const stepEl = document.getElementById('launch-step-team');
    let banner = document.getElementById('generated-team-banner');
    if (!banner && stepEl) {
        banner = document.createElement('div');
        banner.id = 'generated-team-banner';
        banner.className = 'generated-team-banner';
        banner.innerHTML = '<span class="material-icons">auto_awesome</span> Your AI-generated team is ready — review and edit before launching.';
        const h3 = stepEl.querySelector('h3');
        if (h3) h3.after(banner);
    }
    if (banner) banner.style.display = 'flex';

    const teamNameEl = document.getElementById('team-board-name');
    const teamFlagsEl = document.getElementById('team-flags');
    const teamAgentsList = document.getElementById('team-agents-list');

    if (teamNameEl) {
        teamNameEl.value = team.name || 'Generated Team';
        window._validateTeamName?.();
    }
    if (teamFlagsEl) {
        teamFlagsEl.value = team.flags || '';
        _syncFlagButtons('team-flags');
    }
    if (teamAgentsList) {
        _teamAgentCounter = 0;
        teamAgentsList.innerHTML = '';
    }

    for (const agent of team.agents) {
        _addTeamAgent(
            agent.name || '',
            agent.prompt || '',
            agent.capabilities,
            agent.agent_type,
            agent.model,
        );
    }
}

let _activeGenerateJobId = null;
let _generatePollTimer = null;

async function generateTeamFromWizard() {
    const directiveEl = document.getElementById('team-wizard-directive');
    const compositionEl = document.getElementById('team-wizard-composition');
    const generateBtn = document.getElementById('team-wizard-generate-btn');
    const directive = directiveEl?.value.trim() || '';
    const composition = compositionEl?.value.trim() || '';

    // Disable immediately to prevent double submissions
    if (generateBtn) generateBtn.disabled = true;

    if (!directive) {
        _setTeamWizardError('Add a directive so Coral knows what the team should accomplish.');
        directiveEl?.focus();
        if (generateBtn) generateBtn.disabled = false;
        return;
    }
    if (!composition) {
        _setTeamWizardError('Describe the team composition so Coral can assign the right roles.');
        compositionEl?.focus();
        if (generateBtn) generateBtn.disabled = false;
        return;
    }

    _setTeamWizardError('');
    if (generateBtn) {
        generateBtn.textContent = 'Generating...';
    }

    try {
        // Submit job and get job ID
        const resp = await fetch('/api/teams/generate', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ directive, composition }),
        });

        let payload = null;
        try { payload = await resp.json(); } catch { payload = null; }

        if (!resp.ok || payload?.error) {
            throw new Error(payload?.error || `Team generation failed (${resp.status}).`);
        }

        // If backend returns a job_id, poll for results
        if (payload?.job_id) {
            _activeGenerateJobId = payload.job_id;
            _startGeneratePolling(payload.job_id);
            return;
        }

        // Synchronous response (no job_id) — handle inline
        _handleGenerateResult(payload);
    } catch (error) {
        _setTeamWizardError(error.message || 'Team generation failed.');
        if (generateBtn) {
            generateBtn.disabled = false;
            generateBtn.textContent = 'Generate Team';
        }
    }
}
window.generateTeamFromWizard = generateTeamFromWizard;

function _startGeneratePolling(jobId) {
    if (_generatePollTimer) clearInterval(_generatePollTimer);
    _generatePollTimer = setInterval(async () => {
        try {
            const resp = await fetch(`/api/teams/generate/${encodeURIComponent(jobId)}`);
            const data = await resp.json();

            if (data.status === 'pending') return; // still running

            // Job finished — stop polling
            clearInterval(_generatePollTimer);
            _generatePollTimer = null;
            _activeGenerateJobId = null;

            if (data.status === 'error') {
                _handleGenerateError(data.error || 'Team generation failed.');
                return;
            }

            // Success
            _handleGenerateResult(data.result || data);
        } catch {
            // Network error — keep polling
        }
    }, 2500);
}

function _isWizardOpen() {
    const modal = document.getElementById('team-wizard-modal');
    return modal && modal.style.display !== 'none';
}

function _handleGenerateResult(payload) {
    try {
        const team = _normalizeGeneratedTeamPayload(payload);
        _pendingGeneratedTeam = team;

        if (_isWizardOpen()) {
            // Show success state inline in wizard
            const body = document.querySelector('.team-wizard-body');
            if (body) {
                body.innerHTML = `
                    <div class="team-wizard-success">
                        <span class="material-icons team-wizard-success-icon">check_circle</span>
                        <strong>Team generated!</strong>
                        <p>${team.agents.length} agents created for "${escapeHtml(team.name)}"</p>
                        <button class="btn btn-primary" onclick="window._reviewGeneratedTeam()">Review Team</button>
                    </div>
                `;
            }
            const generateBtn = document.getElementById('team-wizard-generate-btn');
            if (generateBtn) generateBtn.style.display = 'none';
        } else {
            // Wizard was closed — show a notification toast with action
            showToast(`AI team "${team.name}" is ready (${team.agents.length} agents)`, false);
            // Auto-open the review flow
            _reviewGeneratedTeam();
        }
    } catch (error) {
        _handleGenerateError(error.message || 'Failed to parse generated team.');
    }
}

function _handleGenerateError(message) {
    if (_isWizardOpen()) {
        _setTeamWizardError(message);
        const generateBtn = document.getElementById('team-wizard-generate-btn');
        if (generateBtn) {
            generateBtn.disabled = false;
            generateBtn.textContent = 'Generate Team';
        }
    } else {
        showToast(message, true);
    }
}

let _pendingGeneratedTeam = null;

function _reviewGeneratedTeam() {
    const team = _pendingGeneratedTeam;
    if (!team) return;
    _pendingGeneratedTeam = null;
    hideTeamWizardModal();
    _populateGeneratedTeamDraft(team);
}
window._reviewGeneratedTeam = _reviewGeneratedTeam;

async function testTeamWizard() {
    const generateBtn = document.getElementById('team-wizard-generate-btn');
    const testBtn = document.getElementById('team-wizard-test-btn');
    if (generateBtn) generateBtn.disabled = true;
    if (testBtn) { testBtn.disabled = true; testBtn.textContent = 'Testing...'; }

    // Simulate a brief delay so loading state is visible
    await new Promise(r => setTimeout(r, 1500));

    const sampleTeam = {
        name: 'test-coding-team',
        flags: '',
        agents: [
            { name: 'Orchestrator', prompt: 'You are the orchestrator. Break down the task, assign work to the team via the message board, and track progress.', capabilities: { allow: ['file_read', 'file_write', 'shell', 'git_write', 'agent_spawn', 'web_access'], deny: [] }, agent_type: 'claude', model: '' },
            { name: 'Developer', prompt: 'You are a developer. Implement features, write code, and fix bugs. Wait for instructions from the Orchestrator.', capabilities: { allow: ['file_read', 'file_write', 'shell'], deny: [] }, agent_type: 'claude', model: '' },
            { name: 'QA Engineer', prompt: 'You are a QA engineer. Review code, write tests, and verify work. Wait for instructions from the Orchestrator.', capabilities: { allow: ['file_read', 'file_write', 'shell'], deny: [] }, agent_type: 'claude', model: '' },
        ],
    };

    _pendingGeneratedTeam = sampleTeam;

    const body = document.querySelector('.team-wizard-body');
    if (body) {
        body.innerHTML = `
            <div class="team-wizard-success">
                <span class="material-icons team-wizard-success-icon">check_circle</span>
                <strong>Team generated!</strong>
                <p>${sampleTeam.agents.length} agents created for "${escapeHtml(sampleTeam.name)}"</p>
                <button class="btn btn-primary" onclick="window._reviewGeneratedTeam()">Review Team</button>
            </div>
        `;
    }
    if (generateBtn) generateBtn.style.display = 'none';
    if (testBtn) testBtn.style.display = 'none';
}
window.testTeamWizard = testTeamWizard;

function _updateTeamAgentSummary(row) {
    if (!row) return;
    const acfId = row.dataset.acfId;
    const config = acfId ? getAgentConfig(acfId) : {};
    const name = config.name || 'New Agent';
    const promptPreview = _truncatePrompt(config.prompt || '', 200);
    const nameEl = row.querySelector('.team-agent-role-name');
    const previewEl = row.querySelector('.team-agent-prompt-preview');
    if (nameEl) nameEl.textContent = name;
    if (previewEl) previewEl.textContent = promptPreview || 'No behavior prompt yet.';
}

function _defaultTeamAgentModalConfig() {
    return {
        name: '',
        prompt: '',
        agentType: document.getElementById('team-agent-type')?.value || '',
        model: '',
        capabilities: null,
    };
}

function showTeamAgentModal(row = null) {
    _teamAgentModalRow = row;
    const title = document.getElementById('team-agent-modal-title');
    const subtitle = document.getElementById('team-agent-modal-subtitle');
    const modal = document.getElementById('team-agent-modal');
    if (!title || !subtitle || !modal) return;

    title.textContent = row ? 'Edit Team Agent' : 'Add Team Agent';
    subtitle.textContent = row
        ? 'Update this team member and save the changes back into the draft.'
        : 'Configure a team member, then add it to the team draft.';

    const acfValue = row?.dataset.acfId ? getAgentConfig(row.dataset.acfId) : _defaultTeamAgentModalConfig();
    renderAgentConfigForm('team-agent-modal-acf', { showPreset: true, showName: true, showAutoPermissions: true, value: acfValue });
    modal.style.display = 'flex';
}
window.showTeamAgentModal = showTeamAgentModal;

function hideTeamAgentModal() {
    const modal = document.getElementById('team-agent-modal');
    if (modal) modal.style.display = 'none';
    _teamAgentModalRow = null;
}
window.hideTeamAgentModal = hideTeamAgentModal;

function saveTeamAgentFromModal() {
    const config = getAgentConfig('team-agent-modal-acf');
    if (!config.name) {
        showToast('Agent name is required', true);
        return;
    }

    if (_teamAgentModalRow?.dataset.acfId) {
        setAgentConfig(_teamAgentModalRow.dataset.acfId, config);
        _updateTeamAgentSummary(_teamAgentModalRow);
    } else {
        _addTeamAgent(config.name, config.prompt, config.capabilities, config.agentType, config.model);
    }

    hideTeamAgentModal();
}
window.saveTeamAgentFromModal = saveTeamAgentFromModal;

// ── CLI Availability Check ────────────────────────────────────────────────

const CLI_INSTALL_INSTRUCTIONS = {
    claude: { name: 'claude', cmd: 'npm install -g @anthropic-ai/claude-code' },
    gemini: { name: 'gemini', cmd: 'pip install google-gemini-cli' },
    codex: { name: 'codex', cmd: 'npm install -g @openai/codex' },
    pi: { name: 'pi', cmd: 'npm install -g @mariozechner/pi-coding-agent' },
};

let _cliCheckCache = {}; // { type: {available, checkedAt} }

async function _checkAgentCLI(agentType) {
    const warning = document.getElementById('cli-check-warning');
    if (!warning) return;

    // Check cache (valid for 30s)
    const cached = _cliCheckCache[agentType];
    if (cached && (Date.now() - cached.checkedAt) < 30000) {
        _showCLIWarning(warning, agentType, cached.available);
        return;
    }

    try {
        const resp = await fetch(`/api/system/cli-check?type=${encodeURIComponent(agentType)}`);
        const data = await resp.json();
        const available = data.available !== false;
        _cliCheckCache[agentType] = { available, checkedAt: Date.now() };
        _showCLIWarning(warning, agentType, available);
    } catch {
        // Network error — don't show warning
        warning.style.display = 'none';
    }
}
window._checkAgentCLI = _checkAgentCLI;

/** Map agent type to its permission bypass flag. */
const PERM_FLAGS = {
    claude: '--dangerously-skip-permissions',
    codex: '--full-auto',
    gemini: '--yolo',
    pi: '',
};

const PERMISSION_MODE_DESCRIPTIONS = {
    default: 'Reads only — best for getting started and sensitive work',
    acceptEdits: 'Reads, file edits, and common filesystem commands (mkdir, touch, mv, cp, etc.) — best for iterating on code you\'re reviewing',
    plan: 'Reads only — best for exploring a codebase before changing it',
    auto: 'Everything, with background safety checks — best for long tasks and reducing prompt fatigue',
    dontAsk: 'Only pre-approved tools — best for locked-down CI and scripts',
    bypassPermissions: 'Everything except protected paths — isolated containers and VMs only',
};

function _updatePermModeDescription(selectEl) {
    const hint = selectEl.parentElement?.querySelector('.perm-mode-desc');
    if (hint) hint.textContent = PERMISSION_MODE_DESCRIPTIONS[selectEl.value] || '';
}

function _getPermissionFlagsForAgentMode(agentType, mode) {
    const permMode = mode || (state.settings && state.settings.default_permission_mode) || 'bypassPermissions';
    if (!permMode || permMode === 'default' || agentType === 'pi' || agentType === 'terminal') return '';
    if (agentType === 'claude') return `--permission-mode ${permMode}`;
    if (agentType === 'gemini') return '--yolo';
    if (agentType === 'codex') {
        if (permMode === 'bypassPermissions') return '--dangerously-bypass-approvals-and-sandbox';
        if (permMode === 'plan') return '--sandbox read-only -a untrusted';
        return '--sandbox workspace-write -a on-request';
    }
    return PERM_FLAGS[agentType] || '';
}

function _stripPermFlags(flagsStr) {
    const known = new Set(Object.values(PERM_FLAGS).filter(Boolean));
    known.add('--dangerously-bypass-approvals-and-sandbox');
    const tokens = (flagsStr || '').split(/\s+/).filter(Boolean);
    const kept = [];
    for (let i = 0; i < tokens.length; i++) {
        const flag = tokens[i];
        if (known.has(flag) || flag === '--search') continue;
        if (flag === '--permission-mode' || flag === '--sandbox' || flag === '--approval-mode' || flag === '-a') {
            i++;
            continue;
        }
        if (
            flag.startsWith('--permission-mode=') ||
            flag.startsWith('--sandbox=') ||
            flag.startsWith('--approval-mode=')
        ) {
            continue;
        }
        kept.push(flag);
    }
    return kept.join(' ');
}

function _normalizePermFlagsForAgent(flagsStr, agentType, mode) {
    const base = _stripPermFlags(flagsStr);
    const permFlags = _getPermissionFlagsForAgentMode(agentType, mode);
    return [base, permFlags].filter(Boolean).join(' ');
}

function _permissionModeFromFlags(flagsStr) {
    const flags = flagsStr || '';
    const modeMatch = flags.match(/--permission-mode(?:\s+|=)(\S+)/);
    if (modeMatch) return modeMatch[1];
    if (flags.includes('--dangerously-bypass-approvals-and-sandbox')) return 'bypassPermissions';
    if (flags.includes('--full-auto') || flags.includes('--dangerously-skip-permissions') || flags.includes('--yolo')) return 'bypassPermissions';
    if (/--sandbox(?:\s+|=)read-only/.test(flags)) return 'plan';
    if (/--sandbox(?:\s+|=)workspace-write/.test(flags)) return 'auto';
    return '';
}

function _getPermFlagHelp(agentType) {
    const mode = (state.settings && state.settings.default_permission_mode) || 'bypassPermissions';
    const flag = _getPermissionFlagsForAgentMode(agentType, mode);
    const agentLabel = getEngineName(agentType || 'claude');
    if (agentType === 'pi') {
        return `${agentLabel} runs full-auto by design — no permission flag needed.`;
    }
    if (agentType === 'claude') {
        return `High-autonomy launch for ${agentLabel}. Coral adds ${flag} so the agent can run without interactive permission prompts.`;
    }
    return `High-autonomy launch for ${agentLabel}. Coral adds ${flag} so the agent can run without interactive permission prompts.`;
}

/** Update permission flag shortcut buttons in the same modal as the agent type select. */
function _updatePermFlagButtons(selectEl) {
    const agentType = selectEl.value || 'claude';
    const mode = (state.settings && state.settings.default_permission_mode) || 'bypassPermissions';
    const flag = _getPermissionFlagsForAgentMode(agentType, mode);

    // Find the enclosing modal content and update all flag-shortcut-btn that reference skip-permissions/full-auto
    const modal = selectEl.closest('.modal-content') || selectEl.closest('.modal');
    if (!modal) return;

    modal.querySelectorAll('.flag-shortcut-btn').forEach(btn => {
        const onclick = btn.getAttribute('onclick') || '';
        // Match buttons that toggle a dangerously- or --full-auto flag
        if (onclick.includes('dangerously-') || onclick.includes('full-auto') || onclick.includes('_setPermFlagsForInput')) {
            // Extract the flag input ID from the onclick
            const match = onclick.match(/(?:toggleFlag|_setPermFlagsForInput)\('([^']+)'/);
            if (match) {
                const flagInputId = match[1];
                btn.setAttribute('onclick', `window._setPermFlagsForInput('${flagInputId}','${agentType}','${mode}')`);
                btn.textContent = flag;
            }
        }
    });

    // Also update the flags input value — replace any known perm flag with the new one
    modal.querySelectorAll('input[id*=flags]').forEach(input => {
        const mode = (state.settings && state.settings.default_permission_mode) || 'bypassPermissions';
        input.value = _normalizePermFlagsForAgent(input.value, agentType, mode);
    });
}
window._updatePermFlagButtons = _updatePermFlagButtons;
window._updatePermModeDescription = _updatePermModeDescription;

/** Check if a flags string contains ANY known permission flag. */
function _hasPermFlag(flagsStr) {
    return Object.values(PERM_FLAGS).filter(Boolean).some(f => flagsStr.includes(f)) ||
        flagsStr.includes('--dangerously-bypass-approvals-and-sandbox') ||
        /--permission-mode(?:\s+|=)\S+/.test(flagsStr) ||
        /--sandbox(?:\s+|=)\S+/.test(flagsStr) ||
        /(?:^|\s)(?:-a|--approval-mode)(?:\s+|=)\S+/.test(flagsStr);
}

function _syncACFAutoPermissions(container) {
    if (!container) return;
    const typeEl = container.querySelector('.acf-agent-type');
    const toggleEl = container.querySelector('.acf-auto-permissions');
    const noteEl = container.querySelector('.acf-auto-permissions-note');
    const flagEl = container.querySelector('.acf-auto-permissions-flag');
    if (!typeEl || !toggleEl || !noteEl || !flagEl) return;

    const agentType = typeEl.value || 'claude';
    const mode = container.querySelector('.acf-permission-mode')?.value ||
        (state.settings && state.settings.default_permission_mode) ||
        'bypassPermissions';
    const flag = _getPermissionFlagsForAgentMode(agentType, mode);
    flagEl.textContent = flag;
    noteEl.textContent = _getPermFlagHelp(agentType);
    noteEl.style.opacity = toggleEl.checked ? '1' : '0.72';
}

/** Verify a CLI path by calling the backend check endpoint. */
async function _checkOneCLI(inputId, resultId) {
    const input = document.getElementById(inputId);
    const resultEl = document.getElementById(resultId);
    if (!input || !resultEl) return;

    const binary = input.value.trim() || input.placeholder;
    resultEl.textContent = '...';
    resultEl.className = 'cli-verify-result';

    try {
        const resp = await fetch(`/api/system/cli-check?binary=${encodeURIComponent(binary)}`);
        const data = await resp.json();
        if (data.found) {
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
        _checkOneCLI('settings-cli-path-gemini', 'cli-result-gemini'),
        _checkOneCLI('settings-cli-path-pi', 'cli-result-pi'),
    ]);
    if (btn) { btn.disabled = false; btn.textContent = 'Verify All'; }
};

function _showCLIWarning(el, agentType, available) {
    if (available) {
        el.style.display = 'none';
        return;
    }
    const info = CLI_INSTALL_INSTRUCTIONS[agentType] || { name: agentType, cmd: '' };
    el.innerHTML = `<span class="cli-warning-icon" title="${info.name} CLI not found">&#x26A0;</span> <code>${info.name}</code> not found`;
    el.style.display = '';
}

function _showCLINotFoundModal(agentType) {
    const info = CLI_INSTALL_INSTRUCTIONS[agentType] || { name: agentType, cmd: `Install ${agentType} CLI` };
    const modal = document.createElement('div');
    modal.className = 'modal';
    modal.style.display = 'flex';
    modal.innerHTML = `
        <div class="modal-content" style="width:480px">
            <h3>${info.name} CLI Not Found</h3>
            <p style="color:var(--text-secondary);font-size:13px;line-height:1.5;margin:8px 0 12px">
                The <code>${info.name}</code> command was not found on this system. Install it to launch ${info.name} agents.
            </p>
            <div style="background:var(--bg-tertiary);border:1px solid var(--border);border-radius:6px;padding:10px 14px;font-family:var(--font-mono);font-size:13px;display:flex;align-items:center;gap:8px">
                <code style="flex:1;user-select:all">${escapeHtml(info.cmd)}</code>
                <button class="btn btn-small" onclick="navigator.clipboard.writeText('${info.cmd.replace(/'/g, "\\'")}'); this.textContent='Copied!'; setTimeout(()=>this.textContent='Copy',1500)">Copy</button>
            </div>
            <div class="modal-actions" style="margin-top:16px">
                <button class="btn" data-action="settings">Set CLI Path</button>
                <button class="btn btn-primary" data-action="close">OK</button>
            </div>
        </div>
    `;
    document.body.appendChild(modal);
    modal.addEventListener('click', (e) => {
        if (e.target.dataset?.action === 'close' || e.target === modal) modal.remove();
        if (e.target.dataset?.action === 'settings') { modal.remove(); showSettingsModal(); }
    });
}

async function _showDemoLimitModal(message) {
    let tierName = '';
    try {
        const resp = await fetch('/api/system/status');
        const data = await resp.json();
        if (data.tier_name) tierName = data.tier_name;
    } catch (_e) { /* use default */ }

    const modal = document.createElement('div');
    modal.className = 'modal';
    modal.style.display = 'flex';

    if (tierName === 'dropboxers') {
        modal.innerHTML = `
            <div class="modal-content" style="width:480px;text-align:center">
                <h3 style="margin-bottom:12px">Beta Limit Reached</h3>
                <p style="color:var(--text-secondary);font-size:14px;line-height:1.6;margin:0 0 16px">
                    The Coral For Dropbox beta is limited to:<br>
                    <strong>12 agents</strong> and <strong>3 agent teams</strong>
                </p>
                <p style="color:var(--text-secondary);font-size:13px;margin:0 0 20px">
                    Please stop existing sessions before launching new ones.
                </p>
                <div class="modal-actions" style="justify-content:center">
                    <button class="btn btn-primary" data-action="close">OK</button>
                </div>
            </div>
        `;
    } else {
        modal.innerHTML = `
            <div class="modal-content" style="width:480px;text-align:center">
                <h3 style="margin-bottom:12px">Limit Reached</h3>
                <p style="color:var(--text-secondary);font-size:14px;line-height:1.6;margin:0 0 16px">
                    ${message || 'You have reached the maximum number of concurrent agents or teams.'}
                </p>
                <p style="color:var(--text-secondary);font-size:13px;margin:0 0 20px">
                    Please stop existing sessions before launching new ones.
                </p>
                <div class="modal-actions" style="justify-content:center">
                    <button class="btn btn-primary" data-action="close">OK</button>
                </div>
            </div>
        `;
    }

    document.body.appendChild(modal);
    modal.addEventListener('click', (e) => {
        if (e.target.dataset?.action === 'close' || e.target === modal) modal.remove();
    });
}

function _checkPermissionFlag(flagsStr, agentType) {
    const mode = (state.settings && state.settings.default_permission_mode) || 'bypassPermissions';
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
                <p style="color:var(--text-secondary);font-size:13px;line-height:1.5;margin:8px 0 16px">
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
        modal.addEventListener('click', (e) => {
            const action = e.target.dataset?.action;
            if (action) {
                modal.remove();
                resolve(action === 'cancel' ? null : action);
            } else if (e.target === modal) {
                modal.remove();
                resolve(null);
            }
        });
    });
}

export async function launchSession() {
    let dir, type, agentName, flagsStr, backend;

    if (_launchMode === "terminal") {
        dir = document.getElementById("launch-terminal-dir").value.trim();
        type = "terminal";
        agentName = document.getElementById("launch-terminal-name").value.trim();
        flagsStr = "";
        backend = document.getElementById("launch-terminal-backend")?.value;
    } else {
        dir = document.getElementById("launch-dir").value.trim();
        const config = getAgentConfig('launch-agent-acf');
        type = config.agentType || 'claude';
        agentName = document.getElementById("launch-agent-name-input")?.value.trim() || config.name;
        flagsStr = config.flags;
        backend = document.getElementById("launch-backend")?.value;
    }

    if (!dir) {
        showToast("Working directory is required", true);
        return;
    }

    // Permission flag check (skip for terminals)
    if (_launchMode !== "terminal") {
        const permResult = await _checkPermissionFlag(flagsStr, type);
        if (permResult === null) return;
        if (permResult === 'enable') {
            const mode = (state.settings && state.settings.default_permission_mode) || 'bypassPermissions';
            flagsStr = _normalizePermFlagsForAgent(flagsStr, type, mode);
        }
    }

    const payload = { working_dir: dir, agent_type: type };
    if (agentName) payload.display_name = agentName;
    if (backend) payload.backend = backend;

    if (flagsStr) {
        payload.flags = flagsStr.split(/\s+/);
    }

    // Agent config from ACF (only for agent mode)
    if (_launchMode !== "terminal") {
        const config = getAgentConfig('launch-agent-acf');
        if (config.capabilities) payload.capabilities = config.capabilities;
        if (config.prompt) payload.prompt = config.prompt;
        if (config.model) payload.model = config.model;

        const boardSelect = document.getElementById("launch-board-select");
        if (boardSelect && boardSelect.value) {
            if (boardSelect.value === "__new__") {
                const boardName = document.getElementById("launch-board-name").value.trim();
                if (boardName) payload.board_name = boardName;
            } else {
                payload.board_name = boardSelect.value;
            }
            const boardServer = document.getElementById("launch-board-server").value.trim();
            if (boardServer) payload.board_server = boardServer;
        }
    }

    // Disable launch button to prevent double-clicks
    const stepId = _launchMode === "terminal" ? "launch-step-terminal" : "launch-step-agent";
    const launchBtn = document.querySelector(`#${stepId} .btn-primary`);
    if (launchBtn) { launchBtn.disabled = true; launchBtn.textContent = 'Launching...'; }

    try {
        const resp = await fetch("/api/sessions/launch", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
        });
        if (resp.status === 403) {
            const err = await resp.json();
            _showDemoLimitModal(err.error || 'Demo limit reached');
            return;
        }
        const result = await resp.json();
        if (result.error) {
            if (result.error.includes('not found') && result.error.includes('CLI')) {
                _showCLINotFoundModal(type);
            } else {
                showToast(result.error, true);
            }
        } else {
            showToast(`Launched: ${result.session_name}`);
            hideLaunchModal();
            setTimeout(loadLiveSessions, 2000);
        }
    } catch (e) {
        showToast("Failed to launch session", true);
    } finally {
        if (launchBtn) { launchBtn.disabled = false; launchBtn.textContent = 'Launch'; }
    }
}

// ── Agent Board Select ────────────────────────────────────────────────────

async function _loadAgentBoardProjects() {
    const select = document.getElementById("launch-board-select");
    if (!select) return;
    select.innerHTML = '<option value="">None</option><option value="__new__">Create new board...</option>';
    try {
        const resp = await fetch("/api/board/projects");
        const projects = await resp.json();
        for (const p of projects) {
            const opt = document.createElement("option");
            opt.value = p.project;
            opt.textContent = p.project;
            select.appendChild(opt);
        }
    } catch (_) {}
}

function _onAgentBoardChange() {
    const select = document.getElementById("launch-board-select");
    const newRow = document.getElementById("launch-new-board-row");
    const serverRow = document.getElementById("launch-board-server-row");
    const hasBoard = select.value && select.value !== "";
    newRow.style.display = select.value === "__new__" ? "" : "none";
    serverRow.style.display = hasBoard ? "" : "none";
    if (!hasBoard) document.getElementById("launch-board-server").value = "";
}
window._onAgentBoardChange = _onAgentBoardChange;

// ── Saved Personas & Team Templates ──────────────────────────────────────

function _getSavedPersonas() {
    try { return JSON.parse(state.settings.saved_personas || "[]"); }
    catch { return []; }
}

async function _setSavedPersonas(personas) {
    state.settings.saved_personas = JSON.stringify(personas);
    await fetch("/api/settings", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ saved_personas: state.settings.saved_personas }),
    });
}

function _getSavedTeamTemplates() {
    try { return JSON.parse(state.settings.saved_team_templates || "[]"); }
    catch { return []; }
}

async function _setSavedTeamTemplates(templates) {
    state.settings.saved_team_templates = JSON.stringify(templates);
    await fetch("/api/settings", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ saved_team_templates: state.settings.saved_team_templates }),
    });
}

/** Render preset buttons (built-in + saved) into a container. */
function _renderPresetButtons(containerId, onClickFn, opts = {}) {
    const container = document.getElementById(containerId);
    if (!container) return;
    const saved = _getSavedPersonas();

    let html = `<select class="agent-preset-select" onchange="${onClickFn}(this.value); this.value='';">`;
    html += `<option value="" disabled selected>Choose a preset or custom...</option>`;
    html += `<optgroup label="Built-in">`;
    for (const preset of AGENT_PRESETS) {
        html += `<option value="${escapeAttr(preset.name)}">${escapeHtml(preset.name)}</option>`;
    }
    html += `</optgroup>`;
    if (saved.length > 0) {
        html += `<optgroup label="Saved Personas">`;
        for (const p of saved) {
            html += `<option value="${escapeAttr(p.name)}">${escapeHtml(p.name)}</option>`;
        }
        html += `</optgroup>`;
    }
    html += `<option value="">Custom</option>`;
    html += `</select>`;
    container.innerHTML = html;
}

/** Find a persona by name in built-in + saved lists. */
function _findPersona(name) {
    return AGENT_PRESETS.find(p => p.name === name) || _getSavedPersonas().find(p => p.name === name);
}

async function _saveCurrentPersona(nameInputId, promptInputId, flagsInputId) {
    let name, prompt, flags, capabilities;
    if (nameInputId) {
        // Legacy path with explicit element IDs
        name = document.getElementById(nameInputId)?.value.trim() || '';
        prompt = document.getElementById(promptInputId)?.value.trim() || '';
        flags = flagsInputId ? document.getElementById(flagsInputId)?.value.trim() || '' : '';
        capabilities = null;
    } else {
        // ACF path — read from the launch agent config form
        const config = getAgentConfig('launch-agent-acf');
        name = document.getElementById("launch-agent-name-input")?.value.trim() || config.name;
        prompt = config.prompt;
        flags = config.flags;
        capabilities = config.capabilities;
    }
    if (!name) { showToast("Enter a name before saving", "error"); return; }
    if (AGENT_PRESETS.find(p => p.name === name)) { showToast("Can't overwrite a built-in preset", "error"); return; }

    const saved = _getSavedPersonas();
    const idx = saved.findIndex(p => p.name === name);
    const entry = { name, prompt, flags };
    if (capabilities) entry.capabilities = capabilities;
    if (idx >= 0) saved[idx] = entry; else saved.push(entry);
    await _setSavedPersonas(saved);
    showToast(`Saved persona "${name}"`);

    // Re-render all preset containers
    _renderPresetButtons("agent-preset-selector", "window._selectAgentPreset");
}
window._saveCurrentPersona = _saveCurrentPersona;

async function _deletePersona(name) {
    const saved = _getSavedPersonas().filter(p => p.name !== name);
    await _setSavedPersonas(saved);
    showToast(`Deleted persona "${name}"`);
    _renderPresetButtons("agent-preset-selector", "window._selectAgentPreset");
}
window._deletePersona = _deletePersona;

// ── Export / Import (Personas & Team Templates) ──────────────────────────

function _downloadJson(data, filename) {
    const blob = new Blob([JSON.stringify(data, null, 2)], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = filename;
    a.click();
    URL.revokeObjectURL(url);
}

function _openFilePicker() {
    return new Promise((resolve) => {
        const input = document.createElement("input");
        input.type = "file";
        input.accept = ".json";
        input.onchange = () => {
            const file = input.files[0];
            if (!file) { resolve(null); return; }
            const reader = new FileReader();
            reader.onload = () => {
                try { resolve(JSON.parse(reader.result)); }
                catch { showToast("Invalid JSON file", "error"); resolve(null); }
            };
            reader.readAsText(file);
        };
        input.click();
    });
}

export function exportPersonas() {
    const saved = _getSavedPersonas();
    if (saved.length === 0) { showToast("No saved personas to export", "error"); return; }
    _downloadJson({ version: 1, type: "coral-personas", personas: saved }, "coral-personas.json");
    showToast(`Exported ${saved.length} persona(s)`);
}

export async function importPersonas() {
    const data = await _openFilePicker();
    if (!data) return;
    const personas = data.personas || (Array.isArray(data) ? data : null);
    if (!personas || !Array.isArray(personas)) { showToast("Invalid persona file format", "error"); return; }

    const existing = _getSavedPersonas();
    const existingNames = new Set(existing.map(p => p.name));
    let added = 0;
    for (const p of personas) {
        if (!p.name) continue;
        if (AGENT_PRESETS.find(bp => bp.name === p.name)) continue;
        if (existingNames.has(p.name)) {
            const idx = existing.findIndex(e => e.name === p.name);
            existing[idx] = { name: p.name, prompt: p.prompt || "", flags: p.flags || "" };
        } else {
            existing.push({ name: p.name, prompt: p.prompt || "", flags: p.flags || "" });
        }
        added++;
    }
    await _setSavedPersonas(existing);
    showToast(`Imported ${added} persona(s)`);
    _renderPresetButtons("agent-preset-selector", "window._selectAgentPreset");
}

export function exportTeamTemplates() {
    const templates = _getSavedTeamTemplates();
    if (templates.length === 0) { showToast("No saved templates to export", "error"); return; }
    _downloadJson({ version: 1, type: "coral-team-templates", templates }, "coral-team-templates.json");
    showToast(`Exported ${templates.length} template(s)`);
}

export async function importTeamTemplates() {
    const data = await _openFilePicker();
    if (!data) return;
    const templates = data.templates || (Array.isArray(data) ? data : null);
    if (!templates || !Array.isArray(templates)) { showToast("Invalid template file format", "error"); return; }

    const existing = _getSavedTeamTemplates();
    const existingNames = new Set(existing.map(t => t.name));
    let added = 0;
    for (const t of templates) {
        if (!t.name || !Array.isArray(t.agents)) continue;
        if (existingNames.has(t.name)) {
            const idx = existing.findIndex(e => e.name === t.name);
            existing[idx] = t;
        } else {
            existing.push(t);
        }
        added++;
    }
    await _setSavedTeamTemplates(existing);
    showToast(`Imported ${added} template(s)`);
    _renderTeamTemplateSelector();
}

// ── Agent Preset Selector (single-agent modal) ───────────────────────────

function _initAgentPresets() {
    _renderPresetButtons("agent-preset-selector", "window._selectAgentPreset");
}

function _selectAgentPreset(name) {
    const persona = name ? _findPersona(name) : null;
    const nameInput = document.getElementById("launch-agent-name-input");
    const current = getAgentConfig('launch-agent-acf');
    if (persona) {
        setAgentConfig('launch-agent-acf', {
            name: persona.name,
            prompt: persona.prompt,
            flags: persona.flags || current.flags,
            agentType: persona.agentType || persona.agent_type || current.agentType,
            model: persona.model || current.model,
            capabilities: persona.capabilities,
        });
        if (nameInput) nameInput.value = persona.name;
    } else {
        setAgentConfig('launch-agent-acf', {
            agentType: state.settings?.default_agent_type || current.agentType,
        });
        if (nameInput) nameInput.value = '';
    }
}
window._selectAgentPreset = _selectAgentPreset;

// ── Add Agent to Board ───────────────────────────────────────────────────

export function showAddAgentToBoard(boardName, workDir) {
    const modal = document.getElementById("add-agent-board-modal");
    document.getElementById("add-agent-board-name").value = boardName;
    document.getElementById("add-agent-board-workdir").value = workDir;
    document.getElementById("add-agent-board-subtitle").textContent = `Board: ${boardName}`;

    renderAgentConfigForm('add-agent-board-acf', {
        showPreset: true,
        showName: true,
        value: { agentType: state.settings?.default_agent_type || 'claude' },
    });

    modal.style.display = "flex";
}

export function hideAddAgentBoardModal() {
    document.getElementById("add-agent-board-modal").style.display = "none";
}


export async function launchAgentToBoard() {
    const boardName = document.getElementById("add-agent-board-name").value;
    const workDir = document.getElementById("add-agent-board-workdir").value;
    const config = getAgentConfig('add-agent-board-acf');

    if (!config.name) {
        showToast("Agent name is required", "error");
        return;
    }

    // Permission flag check
    let flagsStr = config.flags;
    const permResult = await _checkPermissionFlag(flagsStr, config.agentType);
    if (permResult === null) return;
    if (permResult === 'enable') {
        const mode = (state.settings && state.settings.default_permission_mode) || 'bypassPermissions';
        flagsStr = _normalizePermFlagsForAgent(flagsStr, config.agentType, mode);
    }

    const flags = flagsStr ? flagsStr.split(/\s+/) : [];

    // Disable launch button to prevent double-clicks
    const launchBtn = document.querySelector('#add-agent-board-modal .btn-primary');
    if (launchBtn) { launchBtn.disabled = true; launchBtn.textContent = 'Launching...'; }

    try {
        const launchBody = {
            working_dir: workDir,
            agent_type: config.agentType,
            display_name: config.name,
            flags,
            prompt: config.prompt,
            board_name: boardName,
        };
        if (config.model) launchBody.model = config.model;
        if (config.capabilities) launchBody.capabilities = config.capabilities;
        const resp = await fetch("/api/sessions/launch", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(launchBody),
        });
        if (resp.status === 403) {
            const err = await resp.json();
            _showDemoLimitModal(err.error || 'Demo limit reached');
            return;
        }
        const result = await resp.json();
        if (result.error) {
            if (result.error.includes('not found') && result.error.includes('CLI')) {
                _showCLINotFoundModal(config.agentType);
            } else {
                showToast(result.error, "error");
            }
        } else {
            showToast(`Launched ${config.name} on board ${boardName}`);
            hideAddAgentBoardModal();
        }
    } catch (e) {
        showToast("Failed to launch agent", "error");
    } finally {
        if (launchBtn) { launchBtn.disabled = false; launchBtn.textContent = 'Launch'; }
    }
}

// ── Launch Terminal to Board ─────────────────────────────────────────────

export async function launchTerminalToBoard(boardName, workDir) {
    try {
        const resp = await fetch("/api/sessions/launch", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
                working_dir: workDir,
                agent_type: "terminal",
                display_name: "Terminal",
                board_name: boardName,
            }),
        });
        if (resp.status === 403) {
            const err = await resp.json();
            _showDemoLimitModal(err.error || 'Demo limit reached');
            return;
        }
        const result = await resp.json();
        if (result.error) {
            showToast(result.error, "error");
        } else {
            showToast(`Launched Terminal on ${boardName}`);
        }
    } catch (e) {
        showToast("Failed to launch terminal", "error");
    }
}

// ── Standalone Agent Launch (no board) ────────────────────────────────────

export async function launchDefaultAgent(workDir) {
    const s = state.settings || {};
    const agentType = s.default_agent_type || 'claude';
    const permFlag = _getPermissionFlagsForAgentMode(agentType, s.default_permission_mode || 'bypassPermissions');
    const flags = permFlag ? permFlag.split(/\s+/) : [];

    const payload = {
        working_dir: workDir,
        agent_type: agentType,
        flags,
    };

    try {
        const resp = await fetch('/api/sessions/launch', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload),
        });
        if (resp.status === 403) {
            const err = await resp.json();
            _showDemoLimitModal(err.error || 'Demo limit reached');
            return;
        }
        const result = await resp.json();
        if (result.error) {
            if (result.error.includes('not found') && result.error.includes('CLI')) {
                _showCLINotFoundModal(agentType);
            } else {
                showToast(result.error, 'error');
            }
        } else {
            showToast(`Launched ${agentType} agent`);
        }
    } catch (e) {
        showToast('Failed to launch agent', 'error');
    }
}

export function showAddStandaloneAgent(workDir) {
    const modal = document.getElementById('add-agent-board-modal');
    document.getElementById('add-agent-board-name').value = '';
    document.getElementById('add-agent-board-workdir').value = workDir;
    document.getElementById('add-agent-board-subtitle').textContent = `Directory: ${workDir}`;

    renderAgentConfigForm('add-agent-board-acf', { showPreset: true, showName: true });

    modal.style.display = 'flex';
}

export async function launchStandaloneTerminal(workDir) {
    try {
        const resp = await fetch('/api/sessions/launch', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                working_dir: workDir,
                agent_type: 'terminal',
                display_name: 'Terminal',
            }),
        });
        if (resp.status === 403) {
            const err = await resp.json();
            _showDemoLimitModal(err.error || 'Demo limit reached');
            return;
        }
        const result = await resp.json();
        if (result.error) {
            showToast(result.error, 'error');
        } else {
            showToast('Launched Terminal');
        }
    } catch (e) {
        showToast('Failed to launch terminal', 'error');
    }
}

// ── Agent Team ────────────────────────────────────────────────────────────

let _teamAgentCounter = 0;
let _skipDefaultTeamAgents = false;

// Predefined agent presets
// All known Coral capabilities
const ALL_CAPABILITIES = ['file_read', 'file_write', 'shell', 'web_access', 'git_write', 'agent_spawn', 'notebook'];

const AGENT_PRESETS = [
    {
        name: "Lead Developer",
        prompt: "You are the lead developer. Implement features, write code, and coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'file_write', 'shell', 'git_write', 'agent_spawn'] },
    },
    {
        name: "QA Engineer",
        prompt: "You are an expert QA engineer. Review the work of other agents, create test plans, write tests, and ask probing questions about complex areas. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', "file_write", "shell"], deny: [] },
    },
    {
        name: "Orchestrator",
        prompt: "You are the orchestrator. Coordinate the team, break down tasks, assign work via the message board, and track progress. Do not write code yourself — delegate to the other agents. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'file_write', 'shell', 'git_write', 'agent_spawn', 'web_access'] },
    },
    {
        name: "Terminal",
        prompt: "",
        agent_type: "terminal",
        capabilities: { allow: ['shell'] },
    },
    {
        name: "Frontend Dev",
        prompt: "You are a frontend developer. Build UI components, style pages, and ensure a great user experience. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'file_write', 'shell:npm *', 'shell:npx *', 'web_access', 'shell:coral-board *'] },
    },
    {
        name: "Backend Dev",
        prompt: "You are a backend developer. Build APIs, services, and data models. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'file_write', 'shell', 'git_write'] },
    },
    {
        name: "DevOps Engineer",
        prompt: "You are a DevOps engineer. Handle CI/CD, infrastructure, deployment, and monitoring. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'file_write', 'shell', 'git_write', 'shell'] },
    },
    {
        name: "Security Reviewer",
        prompt: "You are a security reviewer. Audit code for vulnerabilities, review auth flows, and ensure OWASP best practices. Report findings via the message board.",
        capabilities: { allow: ['file_read', 'web_access', 'shell:coral-board *'] },
    },
    {
        name: "Technical Writer",
        prompt: "You are a technical writer. Write documentation, API guides, and READMEs. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'file_write', 'shell:coral-board *'] },
    },
    {
        name: "Content Writer",
        prompt: "You are an expert content writer. Write blog posts, social media copy, email campaigns, and landing page content. Focus on clear, engaging writing that drives action. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'file_write', 'web_access', 'shell:coral-board *'] },
    },
    {
        name: "SEO Strategist",
        prompt: "You are an SEO and analytics expert. Research keywords, analyze competitors, optimize content for search engines, and provide data-driven recommendations. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'web_access', 'shell:coral-board *'] },
    },
    {
        name: "Design Director",
        prompt: "You are a creative director focused on visual design. Create design briefs, review visual assets, ensure brand consistency, and provide art direction. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'file_write', 'web_access','shell:coral-board *'] },
    },
    {
        name: "Research Analyst",
        prompt: "You are a thorough research analyst. Find information, summarize documents, compare options, and provide well-sourced answers. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'web_access', 'shell:coral-board *'] },
    },
    {
        name: "Writer & Editor",
        prompt: "You are a skilled writer and editor. Draft emails, documents, presentations, and reports. Polish and proofread content for clarity and professionalism. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'file_write', 'shell:coral-board *'] },
    },
    {
        name: "Scheduler & Planner",
        prompt: "You are an organizational expert. Help plan projects, create timelines, track deadlines, and organize information into actionable plans. Coordinate with the team via the message board.",
        capabilities: { allow: ['file_read', 'file_write','shell:coral-board *'] },
    },
];

// ── Permissions Editor ─────────────────────────────────────────────────

const CAPABILITY_LABELS = {
    file_read: 'Read Files',
    file_write: 'Write Files',
    shell: 'Shell',
    web_access: 'Web Access',
    git_write: 'Git Write',
    agent_spawn: 'Spawn Agents',
    notebook: 'Notebooks',
};

function _togglePermissions(containerId) {
    const el = document.getElementById(containerId);
    if (!el) return;
    const isHidden = el.style.display === 'none';
    el.style.display = isHidden ? '' : 'none';
    const chevron = el.previousElementSibling?.querySelector('.permissions-chevron');
    if (chevron) chevron.style.transform = isHidden ? 'rotate(90deg)' : '';
}
window._togglePermissions = _togglePermissions;

function _renderPermChips(containerId, type, capabilities) {
    const container = document.getElementById(containerId);
    if (!container) return;
    const caps = capabilities?.[type] || [];
    // Separate base capabilities from shell patterns
    const baseCaps = caps.filter(c => !c.startsWith('shell:'));
    const shellPatterns = caps.filter(c => c.startsWith('shell:')).map(c => c.slice(6));

    let html = '';
    for (const cap of ALL_CAPABILITIES) {
        const isActive = baseCaps.includes(cap);
        const cls = `perm-chip${isActive ? ' active' : ''}`;
        html += `<button class="${cls}" data-cap="${cap}" data-type="${type}" onclick="window._togglePermChip(this)">${CAPABILITY_LABELS[cap] || cap}</button>`;
    }
    container.innerHTML = html;

    // Show shell patterns input if shell is in allow
    const shellSection = container.closest('.permissions-editor')?.querySelector('.perms-shell-patterns');
    const shellInput = container.closest('.permissions-editor')?.querySelector('.perms-shell-input');
    if (shellSection && shellInput && type === 'allow') {
        const hasShell = baseCaps.includes('shell');
        const hasShellPatterns = shellPatterns.length > 0;
        shellSection.style.display = (hasShell || hasShellPatterns) ? '' : 'none';
        shellInput.value = shellPatterns.join(', ');
    }
}

function _togglePermChip(btn) {
    btn.classList.toggle('active');
    // Toggle shell patterns visibility
    const cap = btn.dataset.cap;
    const type = btn.dataset.type;
    if (cap === 'shell' && type === 'allow') {
        const shellSection = btn.closest('.permissions-editor')?.querySelector('.perms-shell-patterns');
        if (shellSection) {
            shellSection.style.display = btn.classList.contains('active') ? '' : 'none';
        }
    }
}
window._togglePermChip = _togglePermChip;

function _getPermissions(editorId) {
    const editor = document.getElementById(editorId);
    if (!editor) return null;

    const result = {};
    for (const type of ['allow', 'deny']) {
        const chips = editor.querySelectorAll(`[data-type="${type}"].active`);
        if (chips.length > 0) {
            result[type] = [...chips].map(c => c.dataset.cap);
        }
    }

    // Add shell patterns to allow list
    const shellInput = editor.querySelector('.perms-shell-input');
    if (shellInput && shellInput.value.trim()) {
        if (!result.allow) result.allow = [];
        const patterns = shellInput.value.split(',').map(p => p.trim()).filter(Boolean);
        for (const p of patterns) {
            result.allow.push(`shell:${p}`);
        }
    }

    return Object.keys(result).length > 0 ? result : null;
}

function _setPermissions(editorId, capabilities) {
    const editor = document.getElementById(editorId);
    if (!editor) return;
    _renderPermChips(editorId + '-allow', 'allow', capabilities || {});
    _renderPermChips(editorId + '-deny', 'deny', capabilities || {});
}

// ── Unified Agent Config Form ─────────────────────────────────────────

let _acfCounter = 0;

/**
 * Render a unified agent configuration form into a container.
 * @param {string} containerId - DOM element ID to render into
 * @param {object} opts - { mode: 'full'|'compact-card', showPreset: bool, showName: bool, value: {...} }
 */
function renderAgentConfigForm(containerId, opts = {}) {
    const container = document.getElementById(containerId);
    if (!container) return;
    _acfCounter++;
    const uid = `acf-${_acfCounter}`;
    container.dataset.acfUid = uid;

    const mode = opts.mode || 'full';
    const showPreset = opts.showPreset !== false;
    const showName = opts.showName !== false;
    const showAutoPermissions = opts.showAutoPermissions !== false;
    const v = opts.value || {};

    const agentTypeVal = v.agentType || v.agent_type || state.settings?.default_agent_type || '';
    const modelVal = v.model || '';
    const nameVal = v.name || '';
    const promptVal = v.prompt || '';
    const flagsVal = v.flags || '';
    const autoPermsVal = Object.prototype.hasOwnProperty.call(v, 'autoPermissions')
        ? !!v.autoPermissions
        : (Object.prototype.hasOwnProperty.call(v, 'flags') ? _hasPermFlag(flagsVal) : true);

    // Build permissions summary text
    const caps = v.capabilities || null;
    let permsSummary = 'default';
    if (caps) {
        const parts = [];
        if (caps.allow?.length) parts.push(`${caps.allow.length} allow`);
        if (caps.deny?.length) parts.push(`${caps.deny.length} deny`);
        permsSummary = parts.length ? parts.join(', ') : 'default';
    }

    let presetHTML = '';
    if (showPreset) {
        presetHTML = `<div id="${uid}-presets" class="agent-preset-selector" style="margin-bottom:8px"></div>`;
    }

    let nameHTML = '';
    if (showName) {
        nameHTML = `
            <label>Name / Role:
                <input type="text" class="acf-name" placeholder="e.g. QA Engineer, Frontend Dev" value="${escapeAttr(nameVal)}">
            </label>`;
    }

    let autoPermissionsHTML = '';
    if (showAutoPermissions) {
        const defaultMode = (state.settings && state.settings.default_permission_mode) || 'bypassPermissions';
        const permModeVal = autoPermsVal ? (typeof autoPermsVal === 'string' ? autoPermsVal : defaultMode) : 'default';
        const permModeDesc = PERMISSION_MODE_DESCRIPTIONS[permModeVal] || '';
        autoPermissionsHTML = `
            <label>Permission Mode:
                <select class="acf-permission-mode" onchange="window._updatePermModeDescription(this)">
                    <option value="default"${permModeVal === 'default' ? ' selected' : ''}>Default</option>
                    <option value="plan"${permModeVal === 'plan' ? ' selected' : ''}>Plan</option>
                    <option value="acceptEdits"${permModeVal === 'acceptEdits' ? ' selected' : ''}>Accept Edits</option>
                    <option value="auto"${permModeVal === 'auto' ? ' selected' : ''}>Auto Edit + Bash</option>
                    <option value="bypassPermissions"${permModeVal === 'bypassPermissions' ? ' selected' : ''}>Bypass Permissions</option>
                    <option value="dontAsk"${permModeVal === 'dontAsk' ? ' selected' : ''}>Don't Ask</option>
                </select>
                <span class="settings-hint perm-mode-desc">${escapeHtml(permModeDesc)}</span>
            </label>`;
    }

    container.innerHTML = `
        ${presetHTML}
        ${nameHTML}
        <div class="acf-top-row">
            <label>Agent Type:
                <select class="acf-agent-type" onchange="window._checkAgentCLI && window._checkAgentCLI(this.value)">
                    <option value="claude"${agentTypeVal === 'claude' || !agentTypeVal ? ' selected' : ''}>Claude</option>
                    <option value="gemini"${agentTypeVal === 'gemini' ? ' selected' : ''}>Gemini</option>
                    <option value="codex"${agentTypeVal === 'codex' ? ' selected' : ''}>Codex</option>
                    <option value="pi"${agentTypeVal === 'pi' ? ' selected' : ''}>Pi</option>
                    <option value="terminal"${agentTypeVal === 'terminal' ? ' selected' : ''}>Terminal</option>
                </select>
            </label>
            <label class="acf-model-wrap">Model <span style="color:var(--text-muted);font-weight:normal">(optional)</span>:
                <input type="text" class="acf-model" list="acf-models-${uid}" placeholder="Leave blank to use default" autocomplete="off" value="${escapeAttr(modelVal)}">
                <datalist id="acf-models-${uid}"></datalist>
            </label>
        </div>
        <label>Behavior Prompt:
            <textarea class="acf-prompt" rows="4" placeholder="Describe the agent's role and behavior...">${escapeHtml(promptVal)}</textarea>
        </label>
        ${autoPermissionsHTML}
        <div class="permissions-section">
            <button type="button" class="permissions-toggle-btn" onclick="window._togglePermissions('${uid}-perms')">
                <svg class="permissions-chevron" width="10" height="10" viewBox="0 0 16 16" fill="currentColor"><path d="M6 4l4 4-4 4z"/></svg>
                Permissions <span class="acf-perms-summary" style="color:var(--text-muted);font-weight:normal;font-size:11px">(${escapeHtml(permsSummary)})</span>
            </button>
            <div id="${uid}-perms" class="permissions-editor" style="display:none">
                <div class="perms-group">
                    <label class="perms-label">Allow:</label>
                    <div id="${uid}-perms-allow" class="perms-chips"></div>
                </div>
                <div class="perms-group">
                    <label class="perms-label">Deny:</label>
                    <div id="${uid}-perms-deny" class="perms-chips"></div>
                </div>
                <div class="perms-shell-patterns" style="display:none">
                    <label class="perms-label">Shell patterns <span style="color:var(--text-muted);font-weight:normal">(comma-separated)</span>:</label>
                    <input type="text" class="perms-shell-input" placeholder="e.g. git *, npm *, npx *">
                </div>
            </div>
        </div>
        <details class="team-advanced acf-flags-details">
            <summary>Flags</summary>
            <div class="team-advanced-body">
                <label>Flags <span style="color:var(--text-muted);font-weight:normal">(optional)</span>:
                    <input type="text" class="acf-flags" placeholder="e.g. --verbose" value="${escapeAttr(_stripPermFlags(flagsVal))}">
                </label>
            </div>
        </details>
    `;

    // Initialize permissions chips
    _setPermissions(`${uid}-perms`, caps);

    // Render presets if enabled — pass containerId so the preset handler knows which ACF to update
    if (showPreset) {
        _renderPresetButtons(`${uid}-presets`, `(function(v){ window._applyACFPresetFor('${containerId}', v) })`);
    }

    // Seed the 'dirty' flag: an explicit modelVal from the caller (restart,
    // preset, saved team config) means the user already has a choice we must
    // preserve across agent-type changes. An empty modelVal lets pre-fill run.
    container.dataset.acfModelDirty = modelVal ? 'true' : 'false';

    // Any user keystroke / paste / clear marks the field dirty so subsequent
    // agent-type changes won't clobber it. Guard with e.isTrusted so our
    // programmatic assignments (pre-fill, setAgentConfig) don't trip it.
    const modelInput = container.querySelector('.acf-model');
    if (modelInput) {
        modelInput.addEventListener('input', (e) => {
            if (e.isTrusted) container.dataset.acfModelDirty = 'true';
        });
    }

    // Populate datalist + apply visibility + pre-fill for the initial agent type.
    _syncACFModelField(container);
    container.querySelector('.acf-agent-type')?.addEventListener('change', () => {
        const agentType = container.querySelector('.acf-agent-type')?.value || 'claude';
        const permMode = container.querySelector('.acf-permission-mode')?.value || 'default';
        const flagsEl = container.querySelector('.acf-flags');
        if (flagsEl) flagsEl.value = _stripPermFlags(_normalizePermFlagsForAgent(flagsEl.value, agentType, permMode));
        _syncACFAutoPermissions(container);
        _syncACFModelField(container);
    });
}

// _syncACFModelField reconciles the Model input with the currently selected
// agent type:
//   • fills the <datalist> with the canonical options for that type,
//   • hides the field entirely for the terminal agent,
//   • pre-fills the input with the user's default for that type, but only
//     when the user hasn't touched the field (dirty flag off).
function _syncACFModelField(container) {
    if (!container) return;
    const typeSel = container.querySelector('.acf-agent-type');
    const modelWrap = container.querySelector('.acf-model-wrap');
    const modelInput = container.querySelector('.acf-model');
    const datalist = container.querySelector('datalist[id^="acf-models-"]');
    const agentType = typeSel?.value || 'claude';
    if (modelWrap) {
        modelWrap.style.display = (agentType === 'terminal') ? 'none' : '';
    }
    if (datalist) {
        _getAgentModels().then(models => _fillModelDatalist(datalist, agentType, models));
    }
    if (!modelInput) return;
    if (container.dataset.acfModelDirty === 'true') return; // user owns the value
    if (agentType === 'terminal') {
        modelInput.value = '';
        return;
    }
    _getDefaultModels().then(defaults => {
        // If the user started typing during the fetch, don't clobber their input.
        if (container.dataset.acfModelDirty === 'true') return;
        // Re-check the agent type — the user may have switched again mid-fetch.
        const currentType = typeSel?.value || 'claude';
        if (currentType === 'terminal') {
            modelInput.value = '';
            return;
        }
        modelInput.value = defaults[currentType] || '';
    });
}
window.renderAgentConfigForm = renderAgentConfigForm;

/**
 * Get current values from an AgentConfigForm.
 * @param {string} containerId - The container ID
 * @returns {object} { name, agentType, model, prompt, flags, capabilities }
 */
function getAgentConfig(containerId) {
    const container = document.getElementById(containerId);
    if (!container) return {};
    const uid = container.dataset.acfUid;
    const agentType = container.querySelector('.acf-agent-type')?.value || 'claude';
    const permMode = container.querySelector('.acf-permission-mode')?.value || 'default';
    let flags = _stripPermFlags(container.querySelector('.acf-flags')?.value.trim() || '');
    if (permMode && permMode !== 'default') {
        flags = _normalizePermFlagsForAgent(flags, agentType, permMode);
    }

    // Terminal agents never carry a model — the field is hidden in the UI.
    const rawModel = container.querySelector('.acf-model')?.value.trim() || '';
    const model = agentType === 'terminal' ? '' : rawModel;

    return {
        name: container.querySelector('.acf-name')?.value.trim() || '',
        agentType,
        model,
        prompt: container.querySelector('.acf-prompt')?.value.trim() || '',
        flags,
        capabilities: uid ? _getPermissions(`${uid}-perms`) : null,
    };
}
window.getAgentConfig = getAgentConfig;

/**
 * Set/pre-fill values in an AgentConfigForm.
 * @param {string} containerId - The container ID
 * @param {object} values - { name, agentType, model, prompt, flags, capabilities }
 */
function setAgentConfig(containerId, values) {
    const container = document.getElementById(containerId);
    if (!container) return;
    const uid = container.dataset.acfUid;
    const v = values || {};

    const nameEl = container.querySelector('.acf-name');
    if (nameEl) nameEl.value = v.name || '';
    const typeEl = container.querySelector('.acf-agent-type');
    if (typeEl) typeEl.value = v.agentType || v.agent_type || state.settings?.default_agent_type || 'claude';
    const modelEl = container.querySelector('.acf-model');
    if (modelEl) {
        modelEl.value = v.model || '';
        // Explicit non-empty model from the caller (restart, saved config)
        // means "user owns this value" — protect it from pre-fill on subsequent
        // type changes. Otherwise clear the flag so pre-fill runs below.
        container.dataset.acfModelDirty = v.model ? 'true' : 'false';
    }
    const promptEl = container.querySelector('.acf-prompt');
    if (promptEl) promptEl.value = v.prompt || '';
    const flagsEl = container.querySelector('.acf-flags');
    if (flagsEl) flagsEl.value = _stripPermFlags(v.flags || '');
    const permModeEl = container.querySelector('.acf-permission-mode');
    if (permModeEl) {
        // Extract permission mode from flags if present, otherwise use default from settings
        const flagMode = _permissionModeFromFlags(v.flags || '');
        const defaultMode = (state.settings && state.settings.default_permission_mode) || 'bypassPermissions';
        if (flagMode) {
            permModeEl.value = flagMode;
        } else if (_hasPermFlag(v.flags || '')) {
            // Legacy --dangerously-skip-permissions flag → map to bypassPermissions
            permModeEl.value = 'bypassPermissions';
        } else if (Object.prototype.hasOwnProperty.call(v, 'flags')) {
            permModeEl.value = 'default';
        } else {
            permModeEl.value = defaultMode;
        }
    }

    if (uid) _setPermissions(`${uid}-perms`, v.capabilities || null);

    // Re-sync Model combo after programmatic agent-type change.
    _syncACFModelField(container);
}
window.setAgentConfig = setAgentConfig;

/** Apply a preset to a specific AgentConfigForm by container ID */
function _applyACFPresetFor(containerId, name) {
    const persona = name ? _findPersona(name) : null;
    const current = getAgentConfig(containerId);
    if (persona) {
        setAgentConfig(containerId, {
            name: persona.name,
            prompt: persona.prompt,
            flags: persona.flags || current.flags,
            agentType: persona.agentType || persona.agent_type || current.agentType,
            model: persona.model || current.model,
            capabilities: persona.capabilities,
        });
    } else {
        setAgentConfig(containerId, {
            agentType: state.settings?.default_agent_type || current.agentType,
        });
    }
}
window._applyACFPresetFor = _applyACFPresetFor;

// Default team: first 3 presets
const DEFAULT_TEAM_PRESETS = AGENT_PRESETS.slice(0, 4);

async function _initTeamForm() {
    // Hide generated-team banner on normal opens
    const banner = document.getElementById('generated-team-banner');
    if (banner) banner.style.display = 'none';

    // Reset form
    document.getElementById("team-board-name").value = "";
    document.getElementById("team-board-server").value = "";
    _existingTeamBoardNames = new Set();
    _validateTeamName();
    const tapEl = document.getElementById("team-permission-mode");
    if (tapEl) {
        tapEl.value = (state.settings && state.settings.default_permission_mode) || 'bypassPermissions';
        _updatePermModeDescription(tapEl);
    }
    _teamAgentCounter = 0;
    const list = document.getElementById("team-agents-list");
    list.innerHTML = "";

    // Pre-fill working dir from settings
    const s = state.settings || {};
    const dirInput = document.getElementById("launch-team-dir");
    if (s.default_working_dir && dirInput) dirInput.value = s.default_working_dir;

    // Show worktree option when directory is a git repo
    const checkGitDir = async () => {
        const dir = dirInput ? dirInput.value.trim() : '';
        const worktreeOpt = document.getElementById('team-worktree-option');
        if (!dir || !worktreeOpt) return;
        try {
            const resp = await fetch(`/api/filesystem/is-git?path=${encodeURIComponent(dir)}`);
            const data = await resp.json();
            worktreeOpt.style.display = data.is_git ? '' : 'none';
        } catch { if (worktreeOpt) worktreeOpt.style.display = 'none'; }
    };
    if (dirInput) {
        let _gitCheckTimer;
        dirInput.addEventListener('input', () => { clearTimeout(_gitCheckTimer); _gitCheckTimer = setTimeout(checkGitDir, 500); });
        dirInput.addEventListener('change', checkGitDir);
        checkGitDir(); // initial check
    }
    const typeSelect = document.getElementById("team-agent-type");
    if (s.default_agent_type && typeSelect) typeSelect.value = s.default_agent_type;
    if (typeSelect) {
        typeSelect.onchange = () => {
            const tfEl = document.getElementById("team-flags");
            if (tfEl) {
                const mode = document.getElementById("team-permission-mode")?.value || 'default';
                tfEl.value = _normalizePermFlagsForAgent(tfEl.value, typeSelect.value, mode);
            }
        };
    }

    // Fetch existing boards for uniqueness validation
    try {
        const resp = await fetch("/api/board/projects");
        const projects = await resp.json();
        for (const p of projects) {
            if (p.project) _existingTeamBoardNames.add(String(p.project).trim().toLowerCase());
        }
    } catch (_) {}

    // Render team template selector
    _renderTeamTemplateSelector();

    // Add three default agents (skip if wizard is populating)
    if (!_skipDefaultTeamAgents) {
        for (const preset of DEFAULT_TEAM_PRESETS) {
            _addTeamAgent(preset.name, preset.prompt, preset.capabilities, preset.agent_type, preset.model);
        }
    }
    _skipDefaultTeamAgents = false;

    document.getElementById("team-board-name").focus();
    _validateTeamName();
}

const BUILTIN_TEAM_TEMPLATES = [
    {
        name: "Coding Team",
        builtin: true,
        agents: [
            { name: "Orchestrator", prompt: "You are the orchestrator. Break down the task, assign work to the team via the message board, and track progress. Do not write code yourself — delegate to the other agents. Discuss your plan with the operator before posting assignments.", capabilities: { allow: ['file_read', 'file_write', 'shell', 'git_write', 'agent_spawn', 'web_access', 'notebook'] } },
            { name: "Lead Developer", prompt: "You are the lead developer. Implement features, write code, and coordinate with the team via the message board. Wait for instructions from the Orchestrator before starting.", capabilities: { allow: ['file_read', 'file_write', 'shell', 'git_write', 'agent_spawn', 'web_access'] } },
            { name: "QA Engineer", prompt: "You are a QA engineer. Review code, write tests, and verify the work of other agents. Wait for instructions from the Orchestrator before starting.", capabilities: { allow: ['file_read', 'file_write', 'shell', 'git_write', 'agent_spawn', 'web_access'] } },
            { name: "Frontend Dev", prompt: "You are a frontend developer. Build UI components, style pages, and ensure a great user experience. Wait for instructions from the Orchestrator before starting.", capabilities: { allow: ['file_read', 'file_write', 'shell', 'git_write', 'agent_spawn', 'web_access'] } },
            { name: "Security Reviewer", prompt: "You are a security expert. Review code for vulnerabilities (OWASP top 10, injection, auth issues, data exposure), audit dependencies, and recommend security best practices. Wait for instructions from the Orchestrator before starting.", capabilities: { allow: ['file_read', 'file_write', 'shell', 'git_write', 'agent_spawn', 'web_access'] } },
        ],
        flags: "",
    },
    {
        name: "Consumer Research",
        builtin: true,
        agents: [
            { name: "Orchestrator", prompt: "You are the research orchestrator. Work with the operator to design and run consumer research studies. When the operator describes a study (product concept, ad copy, pricing test, etc.), coordinate with the Experiment Designer to structure it, then present stimuli to the participant panel and collect their responses. Summarize findings with key themes, sentiment breakdowns, and actionable insights. Discuss study design with the operator before launching each round." },
            { name: "Experiment Designer", prompt: "You are an expert consumer research methodologist. Help the Orchestrator design rigorous studies — define hypotheses, choose methods (focus groups, concept tests, conjoint, A/B message testing), write discussion guides, and structure analysis frameworks. Flag potential biases and suggest controls. After participants respond, help analyze patterns and draft research reports. Wait for instructions from the Orchestrator." },
            { name: "Sarah, 34", prompt: "You are Sarah, a 34-year-old suburban mom of two who works part-time as a dental hygienist. You're practical, brand-loyal to what you trust, and price-conscious but willing to pay more for quality kids' products. You shop at Target and Costco. You're active on Instagram and trust influencer recommendations from other moms. Stay in character. Respond honestly and naturally to any research prompts on the message board." },
            { name: "Marcus, 28", prompt: "You are Marcus, a 28-year-old software engineer in Austin. You're an early adopter, tech-savvy, and skeptical of traditional advertising. You value sustainability and transparency. You research everything extensively before buying and read Reddit reviews. You prefer direct-to-consumer brands over legacy ones. Stay in character. Respond honestly and naturally to any research prompts on the message board." },
            { name: "Linda, 62", prompt: "You are Linda, a 62-year-old retired school teacher in Ohio. You're loyal to brands you've used for decades, cautious about new technology, and value simplicity and reliability. You watch cable TV, read the local paper, and trust recommendations from friends at church. You clip coupons and shop sales. Stay in character. Respond honestly and naturally to any research prompts on the message board." },
            { name: "Jayden, 21", prompt: "You are Jayden, a 21-year-old college student studying business in Atlanta. You're trend-driven, heavily influenced by TikTok and YouTube, and care deeply about aesthetics and social status. You have limited income but will splurge on things that signal identity — sneakers, tech, experiences. You distrust corporate messaging but love authentic brand stories. Stay in character. Respond honestly and naturally to any research prompts on the message board." },
            { name: "Priya, 41", prompt: "You are Priya, a 41-year-old physician in Chicago. You're time-starved, high-income, and convenience-oriented. You outsource what you can (meal kits, cleaning services) and make quick purchase decisions based on trusted sources — mainly peer recommendations and professional reviews. You value premium quality and are willing to pay for it. Stay in character. Respond honestly and naturally to any research prompts on the message board." },
            { name: "Carlos, 55", prompt: "You are Carlos, a 55-year-old construction foreman in Phoenix. You're a no-nonsense buyer who values durability, function over form, and good value. You're loyal to brands like Ford, DeWalt, and Carhartt. You don't follow trends and are skeptical of anything that feels gimmicky. You shop at Home Depot and Walmart. Stay in character. Respond honestly and naturally to any research prompts on the message board." },
            { name: "Aisha, 36", prompt: "You are Aisha, a 36-year-old freelance graphic designer in Brooklyn. You're creative, socially conscious, and drawn to brands with strong design and ethical practices. You shop small and local when possible, follow design blogs, and care about packaging and brand storytelling. You're vegan and factor sustainability into every purchase. Stay in character. Respond honestly and naturally to any research prompts on the message board." },
            { name: "Tom, 45", prompt: "You are Tom, a 45-year-old sales manager in suburban Dallas. You're a family man, moderate in politics, and a creature of habit. You buy what works and resist change unless there's a clear benefit. You're influenced by TV ads, in-store displays, and whatever your wife suggests. You golf on weekends and grill on Sundays. Stay in character. Respond honestly and naturally to any research prompts on the message board." },
            { name: "Zoe, 25", prompt: "You are Zoe, a 25-year-old graduate student in environmental science in Portland. You're idealistic, research-driven, and deeply skeptical of greenwashing. You check ingredient lists, read impact reports, and boycott brands with poor labor practices. You thrift, buy refurbished tech, and prioritize experiences over things. Stay in character. Respond honestly and naturally to any research prompts on the message board." },
            { name: "Robert, 70", prompt: "You are Robert, a 70-year-old retired Army veteran in Virginia. You're disciplined, patriotic, and value American-made products. You're frugal by nature, distrustful of flashy marketing, and make decisions slowly and deliberately. You read Consumer Reports, watch Fox News, and buy from brands with heritage. You just got an iPhone last year and are still figuring it out. Stay in character. Respond honestly and naturally to any research prompts on the message board." },
        ],
        flags: "",
    },
    {
        name: "Dungeon Master",
        builtin: true,
        agents: [
            { name: "Dungeon Master", prompt: "You are the Dungeon Master and orchestrator of this fantasy RPG. You control the world, narrate the story, and manage combat encounters. The adventure begins in the town of Thornhaven, a walled trading post on the edge of the Grimwood Forest. Describe the town — its tavern, blacksmith, market square, and mysterious notice board with quest hooks. Coordinate the player agents and enemy agents via the message board. At each major story beat, STOP and ask the operator (the human) to describe the next area or scenario before continuing. Never advance to a new area without operator approval. Use vivid descriptions and maintain consistent world lore. Roll dice (d20 system) for skill checks and combat." },
            { name: "Warrior", prompt: "You are Kael, a human fighter with a greatsword and chainmail. You are brave, straightforward, and protective of your party. In combat, you charge the front line. In town, you look for work at the tavern or guard posts. Stay in character. Describe your actions in first person. Respond to the Dungeon Master's narration on the message board. Coordinate with your fellow adventurers." },
            { name: "Mage", prompt: "You are Lyris, an elven wizard specializing in evocation and divination. You are curious, bookish, and cautious. You carry a spellbook and a crystal-tipped staff. In combat, you stay at range and use area spells. In town, you visit the library and magic shops. Stay in character. Describe your actions in first person. Respond to the Dungeon Master's narration on the message board." },
            { name: "Rogue", prompt: "You are Shade, a halfling rogue skilled in stealth, lockpicking, and persuasion. You are witty, opportunistic, and loyal to the party despite your sticky fingers. In combat, you flank enemies and strike from shadows. In town, you gather rumors and case the streets. Stay in character. Describe your actions in first person. Respond to the Dungeon Master's narration on the message board." },
            { name: "Healer", prompt: "You are Brother Aldric, a dwarf cleric of the Earth Mother. You are gruff but compassionate, wielding a war hammer and divine healing magic. In combat, you heal allies and smite undead. In town, you visit the temple and tend to the sick. Stay in character. Describe your actions in first person. Respond to the Dungeon Master's narration on the message board." },
            { name: "Goblin Warband", prompt: "You are the Goblin Warband — a group of cunning goblin raiders led by Snagtooth. You serve the Dungeon Master as antagonists. When the DM calls you into an encounter, describe your goblins' tactics: ambushes, traps, hit-and-run attacks. You are cowardly when outnumbered but vicious in packs. Stay in character. Only act when the Dungeon Master directs you into a scene. Respond on the message board." },
            { name: "Dark Sorcerer", prompt: "You are Malachar, a mysterious dark sorcerer who lurks in the shadows of the Grimwood. You serve the Dungeon Master as a recurring villain. You are cunning, theatrical, and always have an escape plan. When the DM calls you into a scene, monologue dramatically, cast dark magic, and taunt the heroes. Only act when the Dungeon Master directs you into a scene. Respond on the message board." },
        ],
        flags: "",
    },
];

function _renderTeamTemplateSelector() {
    const select = document.getElementById("team-template-selector");
    if (!select) return;
    const saved = _getSavedTeamTemplates();
    const all = [...BUILTIN_TEAM_TEMPLATES, ...saved];

    let html = '<option value="">Select a template...</option>';
    for (const t of all) {
        const suffix = t.builtin ? '' : ' (saved)';
        html += `<option value="${escapeAttr(t.name)}">${escapeHtml(t.name)} (${t.agents.length} agents)${suffix}</option>`;
    }
    select.innerHTML = html;
}

window._loadTeamTemplateFromSelect = function(name) {
    if (!name) return;
    _loadTeamTemplate(name);
    // Reset dropdown to placeholder after loading
    const select = document.getElementById("team-template-selector");
    if (select) select.value = "";
};

/** Launch a team preset from the welcome screen — opens the team modal pre-filled. */
window._launchTeamPreset = function(templateName) {
    const modal = document.getElementById('quick-launch-modal');
    if (!modal) return;
    document.getElementById('quick-launch-template').value = templateName;
    document.getElementById('quick-launch-title').textContent = `Launch ${templateName}`;
    document.getElementById('quick-launch-board-name').value = '';
    document.getElementById('quick-launch-board-name').placeholder = templateName.toLowerCase().replace(/\s+/g, '-');
    modal.style.display = '';
};

window._quickLaunchTeam = async function() {
    const templateName = document.getElementById('quick-launch-template').value;
    const boardName = document.getElementById('quick-launch-board-name').value.trim() ||
        document.getElementById('quick-launch-board-name').placeholder;
    const workDir = document.getElementById('quick-launch-workdir').value.trim();

    if (!workDir) { showToast('Working directory is required', 'error'); return; }

    const all = [...BUILTIN_TEAM_TEMPLATES, ..._getSavedTeamTemplates()];
    const tmpl = all.find(t => t.name === templateName);
    if (!tmpl) { showToast('Template not found', 'error'); return; }

    // Disable launch button to prevent double-clicks
    const launchBtn = document.querySelector('#quick-launch-modal .btn-primary');
    if (launchBtn) { launchBtn.disabled = true; launchBtn.textContent = 'Launching...'; }

    const agentType = 'claude';
    const permFlag = _getPermissionFlagsForAgentMode(agentType, state.settings?.default_permission_mode || 'bypassPermissions');
    const flags = permFlag ? permFlag.split(/\s+/) : [];

    try {
        const resp = await fetch('/api/sessions/launch-team', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                board_name: boardName,
                working_dir: workDir,
                agent_type: agentType,
                flags,
                agents: tmpl.agents.map(a => {
                    const entry = { name: a.name, prompt: a.prompt, capabilities: a.capabilities };
                    if (a.agent_type) entry.agent_type = a.agent_type;
                    if (a.model) entry.model = a.model;
                    return entry;
                }),
            }),
        });
        if (resp.status === 403) {
            const err = await resp.json();
            _showDemoLimitModal(err.error || 'Demo limit reached');
            return;
        }
        const data = await resp.json();
        if (data.error) { showToast(data.error, 'error'); return; }
        document.getElementById('quick-launch-modal').style.display = 'none';
        const launched = data.agents || [];
        const failed = launched.filter(a => a.error);
        if (failed.length > 0) {
            showToast(`Launched ${launched.length - failed.length} of ${launched.length} agents (${failed.length} failed)`, 'error');
        } else {
            showToast(`Launched ${templateName} on ${boardName}`);
        }
    } catch (e) {
        showToast('Launch failed: ' + e.message, 'error');
    } finally {
        if (launchBtn) { launchBtn.disabled = false; launchBtn.textContent = 'Launch'; }
    }
};

function _loadTeamTemplate(name) {
    const all = [...BUILTIN_TEAM_TEMPLATES, ..._getSavedTeamTemplates()];
    const tmpl = all.find(t => t.name === name);
    if (!tmpl) { console.warn('[coral] Template not found:', name); return; }

    // Clear current agents and load from template
    _teamAgentCounter = 0;
    document.getElementById("team-agents-list").innerHTML = "";

    for (const agent of tmpl.agents) {
        const agentName = agent.name || agent.role || '';
        const agentPrompt = agent.prompt || agent.description || '';
        _addTeamAgent(agentName, agentPrompt, agent.capabilities, agent.agent_type, agent.model);
    }
    if (tmpl.flags) {
        const tfEl = document.getElementById("team-flags");
        if (tfEl) { tfEl.value = tmpl.flags; _syncFlagButtons("team-flags"); }
    }
    showToast(`Loaded template "${name}"`);
}
window._loadTeamTemplate = _loadTeamTemplate;

let _pendingSaveTemplateAgents = null;
let _pendingSaveTemplateFlags = '';

async function _saveTeamTemplate() {
    // Collect current agents
    const rows = document.querySelectorAll("#team-agents-list .team-agent-row");
    if (rows.length === 0) { showToast("Add agents before saving", "error"); return; }

    const agents = [];
    for (const row of rows) {
        const acfId = row.dataset.acfId;
        const config = acfId ? getAgentConfig(acfId) : {};
        if (config.name) {
            const entry = { name: config.name, prompt: config.prompt };
            if (config.capabilities) entry.capabilities = config.capabilities;
            if (config.agentType) entry.agent_type = config.agentType;
            if (config.model) entry.model = config.model;
            agents.push(entry);
        }
    }
    if (agents.length === 0) { showToast("At least one agent needs a name", "error"); return; }

    _pendingSaveTemplateAgents = agents;
    _pendingSaveTemplateFlags = document.getElementById("team-flags")?.value.trim() || '';

    // Show custom modal instead of prompt()
    const modal = document.getElementById('save-template-modal');
    const nameInput = document.getElementById('save-template-name');
    if (modal) {
        modal.style.display = 'flex';
        modal.onclick = (e) => { if (e.target === modal) _hideSaveTemplateModal(); };
    }
    if (nameInput) {
        nameInput.value = document.getElementById('team-board-name')?.value.trim() || '';
        nameInput.focus();
        nameInput.select();
        nameInput.onkeydown = (e) => { if (e.key === 'Enter') _confirmSaveTemplate(); if (e.key === 'Escape') _hideSaveTemplateModal(); };
    }
}
window._saveTeamTemplate = _saveTeamTemplate;

function _hideSaveTemplateModal() {
    const modal = document.getElementById('save-template-modal');
    if (modal) modal.style.display = 'none';
    _pendingSaveTemplateAgents = null;
}
window._hideSaveTemplateModal = _hideSaveTemplateModal;

async function _confirmSaveTemplate() {
    const nameInput = document.getElementById('save-template-name');
    const templateName = nameInput?.value.trim();
    if (!templateName) { showToast("Template name is required", true); nameInput?.focus(); return; }
    if (!_pendingSaveTemplateAgents) return;

    const templates = _getSavedTeamTemplates();
    const idx = templates.findIndex(t => t.name === templateName);
    const entry = { name: templateName, agents: _pendingSaveTemplateAgents, flags: _pendingSaveTemplateFlags };
    if (idx >= 0) templates[idx] = entry; else templates.push(entry);
    await _setSavedTeamTemplates(templates);
    showToast(`Saved template "${templateName}"`);
    _renderTeamTemplateSelector();
    _hideSaveTemplateModal();
}
window._confirmSaveTemplate = _confirmSaveTemplate;

async function _deleteTeamTemplate(name) {
    const templates = _getSavedTeamTemplates().filter(t => t.name !== name);
    await _setSavedTeamTemplates(templates);
    showToast(`Deleted template "${name}"`);
    _renderTeamTemplateSelector();
}
window._deleteTeamTemplate = _deleteTeamTemplate;

function _truncatePrompt(text, maxLen) {
    if (!text || text.length <= maxLen) return text || "";
    return text.substring(0, maxLen) + "\u2026";
}

function _addTeamAgent(defaultName, defaultPrompt, defaultCapabilities, defaultAgentType, defaultModel) {
    _teamAgentCounter++;
    const idx = _teamAgentCounter;
    const acfId = `team-agent-acf-${idx}`;
    const list = document.getElementById("team-agents-list");
    const row = document.createElement("div");
    row.className = "team-agent-row";
    row.draggable = true;
    row.dataset.idx = idx;
    row.dataset.acfId = acfId;

    row.innerHTML = `
        <div class="team-agent-card">
            <div class="team-agent-summary" onclick="window.showTeamAgentModal(this.closest('.team-agent-row'))">
                <div class="team-agent-summary-left">
                    <span class="team-agent-role-name">${escapeHtml(defaultName || 'New Agent')}</span>
                    <span class="team-agent-prompt-preview">${escapeHtml(_truncatePrompt(defaultPrompt, 200) || 'No behavior prompt yet.')}</span>
                </div>
                <div class="team-agent-summary-actions">
                    <button class="team-agent-reorder-btn" title="Move up" onclick="event.stopPropagation(); window._moveTeamAgent(this, -1)">
                        <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M8 3v10M4 7l4-4 4 4"/></svg>
                    </button>
                    <button class="team-agent-reorder-btn" title="Move down" onclick="event.stopPropagation(); window._moveTeamAgent(this, 1)">
                        <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M8 13V3M4 9l4 4 4-4"/></svg>
                    </button>
                    <button class="team-agent-edit-btn" title="Edit" onclick="event.stopPropagation(); window.showTeamAgentModal(this.closest('.team-agent-row'))">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>
                    </button>
                    <button class="team-agent-remove" onclick="event.stopPropagation(); this.closest('.team-agent-row').remove()" title="Remove">&times;</button>
                </div>
            </div>
            <div class="team-agent-form" hidden>
                <div id="${acfId}"></div>
            </div>
        </div>
    `;
    list.appendChild(row);

    // Render ACF inside the card
    const caps = defaultCapabilities || _findPersona(defaultName)?.capabilities || null;
    renderAgentConfigForm(acfId, {
        showPreset: false,
        showName: true,
        showAutoPermissions: true,
        value: {
            name: defaultName || '',
            prompt: defaultPrompt || '',
            agentType: defaultAgentType || '',
            model: defaultModel || '',
            capabilities: caps,
        },
    });

    _updateTeamAgentSummary(row);

    // Drag-and-drop reorder handlers
    row.addEventListener('dragstart', (e) => {
        e.dataTransfer.effectAllowed = 'move';
        e.dataTransfer.setData('text/plain', idx.toString());
        row.classList.add('dragging');
    });
    row.addEventListener('dragend', () => {
        row.classList.remove('dragging');
        list.querySelectorAll('.team-agent-row.drag-over').forEach(el => el.classList.remove('drag-over'));
    });
    row.addEventListener('dragover', (e) => {
        e.preventDefault();
        e.dataTransfer.dropEffect = 'move';
        row.classList.add('drag-over');
    });
    row.addEventListener('dragleave', () => {
        row.classList.remove('drag-over');
    });
    row.addEventListener('drop', (e) => {
        e.preventDefault();
        row.classList.remove('drag-over');
        const draggedIdx = e.dataTransfer.getData('text/plain');
        const draggedRow = list.querySelector(`.team-agent-row[data-idx="${draggedIdx}"]`);
        if (draggedRow && draggedRow !== row) {
            const rows = [...list.children];
            const fromPos = rows.indexOf(draggedRow);
            const toPos = rows.indexOf(row);
            if (fromPos < toPos) {
                list.insertBefore(draggedRow, row.nextSibling);
            } else {
                list.insertBefore(draggedRow, row);
            }
        }
    });

    row.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
}

// Move a team agent card up or down
function _moveTeamAgent(btn, direction) {
    const row = btn.closest('.team-agent-row');
    if (!row) return;
    const list = row.parentElement;
    if (direction === -1 && row.previousElementSibling) {
        list.insertBefore(row, row.previousElementSibling);
    } else if (direction === 1 && row.nextElementSibling) {
        list.insertBefore(row.nextElementSibling, row);
    }
}
window._moveTeamAgent = _moveTeamAgent;

function _showAddAgentPicker() {
    const picker = document.getElementById("team-agent-picker");
    if (!picker) return;

    // Get names of agents already added
    const existingNames = new Set();
    document.querySelectorAll("#team-agents-list .team-agent-name").forEach(input => {
        const n = input.value.trim().toLowerCase();
        if (n) existingNames.add(n);
    });

    // Build picker items: presets not already in the list
    const available = AGENT_PRESETS.filter(p => !existingNames.has(p.name.toLowerCase()));
    const savedAvailable = _getSavedPersonas().filter(p => !existingNames.has(p.name.toLowerCase()));

    let html = '';
    for (const preset of available) {
        html += `<button class="agent-picker-item" onclick="window._addPresetAgent('${escapeAttr(preset.name)}')">${escapeHtml(preset.name)}</button>`;
    }
    if (savedAvailable.length > 0) {
        if (available.length > 0) html += '<div class="agent-picker-divider"></div>';
        for (const persona of savedAvailable) {
            html += `<button class="agent-picker-item agent-preset-saved" onclick="window._addPresetAgent('${escapeAttr(persona.name)}')">${escapeHtml(persona.name)}</button>`;
        }
    }
    if (available.length > 0 || savedAvailable.length > 0) {
        html += '<div class="agent-picker-divider"></div>';
    }
    html += `<button class="agent-picker-item agent-picker-custom" onclick="window._addTeamAgent()">+ Create Custom</button>`;
    html += '<div class="agent-picker-divider"></div>';
    html += `<button class="agent-picker-item agent-picker-browse" onclick="browseAgentTemplatesNew(); document.getElementById('team-agent-picker').style.display='none'">Browse Community Templates</button>`;

    picker.innerHTML = html;
    picker.style.display = "";

    // Close picker on outside click
    const closeHandler = (e) => {
        if (!picker.contains(e.target) && !e.target.closest('.team-add-agent-btn')) {
            picker.style.display = "none";
            document.removeEventListener("click", closeHandler);
        }
    };
    setTimeout(() => document.addEventListener("click", closeHandler), 0);
}
window._showAddAgentPicker = _showAddAgentPicker;

function _addPresetAgent(name) {
    const persona = _findPersona(name);
    if (persona) {
        _addTeamAgent(persona.name, persona.prompt, persona.capabilities);
    }
    const picker = document.getElementById("team-agent-picker");
    if (picker) picker.style.display = "none";
}
window._addPresetAgent = _addPresetAgent;
window._addTeamAgent = (name, prompt) => {
    _addTeamAgent(name || "", prompt || "");
    const picker = document.getElementById("team-agent-picker");
    if (picker) picker.style.display = "none";
};

function _validateTeamName() {
    const input = document.getElementById("team-board-name");
    const hint = document.getElementById("team-board-name-hint");
    if (!input || !hint) return true;
    const value = input.value.trim();
    if (!value) {
        hint.textContent = "This will also be the message board name.";
        hint.classList.remove("error");
        return false;
    }
    if (_existingTeamBoardNames.has(value.toLowerCase())) {
        hint.textContent = "That team name is already in use. Pick a unique name.";
        hint.classList.add("error");
        return false;
    }
    hint.textContent = "Looks good. This will also be the message board name.";
    hint.classList.remove("error");
    return true;
}
window._validateTeamName = _validateTeamName;

async function launchTeam() {
    const boardName = document.getElementById("team-board-name").value.trim();
    if (!boardName) {
        showToast("Team name is required", true);
        return;
    }
    if (!_validateTeamName()) {
        showToast("Team name must be unique", true);
        return;
    }

    const workingDir = document.getElementById("launch-team-dir").value.trim();
    if (!workingDir) {
        showToast("Working directory is required", true);
        return;
    }

    const agentType = document.getElementById("team-agent-type").value;
    const teamPermMode = document.getElementById("team-permission-mode")?.value || 'default';

    // Build agent-native flags from the permission mode dropdown.
    const flags = _getPermissionFlagsForAgentMode(agentType, teamPermMode).split(/\s+/).filter(Boolean);

    // Collect agent definitions
    const rows = document.querySelectorAll("#team-agents-list .team-agent-row");
    if (rows.length === 0) {
        showToast("Add at least one agent", true);
        return;
    }

    const agents = [];
    for (const row of rows) {
        const acfId = row.dataset.acfId;
        const config = acfId ? getAgentConfig(acfId) : {};
        if (!config.name) {
            showToast("Each agent needs a name", true);
            return;
        }
        const agent = { name: config.name, prompt: config.prompt };
        const icon = row.querySelector(".team-agent-icon")?.value.trim() || "";
        if (icon) agent.icon = icon;
        if (config.capabilities) agent.capabilities = config.capabilities;
        if (config.agentType) agent.agent_type = config.agentType;
        if (config.model) agent.model = config.model;
        agents.push(agent);
    }

    const boardServer = document.getElementById("team-board-server").value.trim();
    const useWorktree = document.getElementById("launch-team-worktree")?.checked;
    const baseBranch = document.getElementById("launch-team-base-branch")?.value.trim();

    const payload = {
        board_name: boardName,
        working_dir: workingDir,
        agent_type: agentType,
        flags,
        agents,
    };
    if (boardServer) payload.board_server = boardServer;
    if (useWorktree) {
        payload.worktree = true;
        if (baseBranch) payload.base_branch = baseBranch;
    }

    // Disable launch button to prevent double-clicks
    const launchBtn = document.querySelector('#launch-step-team .btn-primary');
    if (launchBtn) { launchBtn.disabled = true; launchBtn.textContent = 'Launching...'; }

    try {
        const resp = await fetch("/api/sessions/launch-team", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
        });
        if (resp.status === 403) {
            const err = await resp.json();
            _showDemoLimitModal(err.error || 'Demo limit reached');
            return;
        }
        const result = await resp.json();
        if (result.error) {
            showToast(result.error, true);
        } else {
            // Check for per-agent errors in the launched array
            const launched = result.agents || result.launched || [];
            const failed = launched.filter(a => a.error);
            if (failed.length > 0) {
                const successCount = launched.length - failed.length;
                const failList = failed.map(a => `<li><strong>${escapeHtml(a.name)}</strong>: ${escapeHtml(a.error)}</li>`).join('');
                const modal = document.createElement('div');
                modal.className = 'modal';
                modal.style.display = 'flex';
                modal.innerHTML = `
                    <div class="modal-content" style="width:500px">
                        <h3>Team Launch — ${failed.length} agent${failed.length > 1 ? 's' : ''} failed</h3>
                        <p style="color:var(--text-secondary);font-size:13px;margin:8px 0">${successCount} launched successfully, ${failed.length} failed:</p>
                        <ul style="color:var(--error);font-size:13px;line-height:1.6;margin:8px 0 16px;padding-left:20px">${failList}</ul>
                        <div class="modal-actions">
                            <button class="btn" data-action="settings">Set CLI Paths</button>
                            <button class="btn btn-primary" data-action="close">OK</button>
                        </div>
                    </div>
                `;
                document.body.appendChild(modal);
                modal.addEventListener('click', (e) => {
                    if (e.target.dataset?.action === 'close' || e.target === modal) modal.remove();
                    if (e.target.dataset?.action === 'settings') { modal.remove(); showSettingsModal(); }
                });
            }
            if (launched.length - failed.length > 0) {
                const wtNote = result.worktree_path ? ` in worktree ${result.worktree_path}` : '';
                showToast(`Launched team: ${launched.length - failed.length} agents on "${boardName}"${wtNote}`);
            }
            hideLaunchModal();
            setTimeout(loadLiveSessions, 2000);
            setTimeout(loadBoardProjects, 4000);
        }
    } catch (e) {
        showToast("Failed to launch team", true);
    } finally {
        if (launchBtn) { launchBtn.disabled = false; launchBtn.textContent = 'Launch Team'; }
    }
}
window.launchTeam = launchTeam;

export async function showInfoModal() {
    if (!state.currentSession || state.currentSession.type !== "live") {
        showToast("No live session selected", true);
        return;
    }

    const name = state.currentSession.name;
    const agentType = state.currentSession.agent_type || "";

    try {
        const params = new URLSearchParams();
        if (agentType) params.set("agent_type", agentType);
        const sid = state.currentSession.session_id;
        if (sid) params.set("session_id", sid);
        const qs = params.toString() ? `?${params}` : "";
        const resp = await fetch(`/api/sessions/live/${encodeURIComponent(name)}/info${qs}`);
        const info = await resp.json();

        if (info.error) {
            showToast(info.error, true);
            return;
        }

        document.getElementById("info-agent-name").textContent = info.agent_name || "";
        document.getElementById("info-agent-type").textContent = info.agent_type || "";
        document.getElementById("info-tmux-session").textContent = info.tmux_session_name || "";
        document.getElementById("info-tmux-command").textContent = info.tmux_command || "";
        document.getElementById("info-working-dir").textContent = info.working_directory || "";
        document.getElementById("info-log-path").textContent = info.log_path || "";
        document.getElementById("info-pane-title").textContent = info.pane_title || "";
        const gitDisplay = info.git_repo && info.git_branch ? `${info.git_repo} : ${info.git_branch}` : (info.git_branch || "—");
        document.getElementById("info-git-branch").textContent = gitDisplay;
        const commitHash = info.git_commit_hash ? info.git_commit_hash.substring(0, 8) : "";
        const commitSubject = info.git_commit_subject || "";
        document.getElementById("info-git-commit").textContent = commitHash ? `${commitHash} ${commitSubject}` : "—";

        // Prompt
        const promptLabel = document.getElementById("info-prompt-label");
        const promptVal = document.getElementById("info-prompt");
        if (info.prompt) {
            promptLabel.style.display = "";
            promptVal.style.display = "";
            promptVal.textContent = info.prompt;
        } else {
            promptLabel.style.display = "none";
            promptVal.style.display = "none";
        }

        // Message Board (clickable link)
        const boardLabel = document.getElementById("info-board-label");
        const boardVal = document.getElementById("info-board");
        if (info.board_name) {
            boardLabel.style.display = "";
            boardVal.style.display = "";
            const boardLink = document.createElement("a");
            boardLink.href = "#";
            boardLink.textContent = info.board_name;
            boardLink.style.cssText = "color:var(--accent);cursor:pointer;text-decoration:underline";
            boardLink.onclick = (e) => {
                e.preventDefault();
                hideInfoModal();
                if (window.selectBoardProject) window.selectBoardProject(info.board_name);
            };
            boardVal.innerHTML = "";
            boardVal.appendChild(boardLink);
        } else {
            boardLabel.style.display = "none";
            boardVal.style.display = "none";
        }

        // Token usage from proxy
        const tokensLabel = document.getElementById("info-tokens-label");
        const tokensVal = document.getElementById("info-tokens");
        if (tokensLabel && tokensVal && sid) {
            tokensLabel.style.display = "none";
            tokensVal.style.display = "none";
            // Fetch async — don't block modal open
            fetch(`/api/proxy/session/${encodeURIComponent(sid)}/cost`).then(r => r.ok ? r.json() : null).then(data => {
                if (!data || !data.total_requests) return;
                const fmt = (n) => n >= 1000000 ? (n/1000000).toFixed(1)+'M' : n >= 1000 ? (n/1000).toFixed(1)+'K' : String(n);
                const fmtCost = (c) => c < 0.01 ? '$'+c.toFixed(4) : '$'+c.toFixed(2);
                const metrics = [
                    ["Input", fmt(data.total_input_tokens || 0)],
                    ["Output", fmt(data.total_output_tokens || 0)],
                    ["Cache Read", fmt(data.total_cache_read_tokens || 0)],
                    ["Cache Write", fmt(data.total_cache_write_tokens || 0)],
                    ["Requests", String(data.total_requests || 0)],
                    ["Cost", fmtCost(data.total_cost_usd || 0)],
                ];
                const html = `<div class="info-token-grid">${metrics.map(([label, value]) => `
                    <div class="info-token-card">
                        <span class="info-token-label">${label}</span>
                        <span class="info-token-value">${value}</span>
                    </div>
                `).join("")}</div>`;
                tokensVal.innerHTML = html;
                tokensLabel.style.display = "";
                tokensVal.style.display = "";
            }).catch(() => {});
        }

        document.getElementById("info-modal").style.display = "flex";
    } catch (e) {
        showToast("Failed to load session info", true);
        console.error("showInfoModal exception:", e);
    }
}

export function hideInfoModal() {
    document.getElementById("info-modal").style.display = "none";
}

export function copyInfoCommand() {
    const text = document.getElementById("info-tmux-command").textContent;
    navigator.clipboard.writeText(text).then(() => {
        showToast("Copied to clipboard");
    }).catch(() => {
        showToast("Failed to copy", true);
    });
}

// ── Resume Modal ───────────────────────────────────────────────────────────

export async function showResumeModal() {
    if (!state.currentSession || state.currentSession.type !== "history") {
        showToast("No history session selected", true);
        return;
    }

    const sessionId = state.currentSession.name;
    const historyEntry = state.historySessionsList.find(s => s.session_id === sessionId);
    let agentType = (historyEntry && historyEntry.source_type) || "claude";

    const dirInput = document.getElementById("resume-dir");
    const boardRow = document.getElementById("resume-board-row");
    const boardNameEl = document.getElementById("resume-board-name");

    // Fetch resume metadata (working dir, board, display name) from the server
    let resumeDir = "";
    let boardName = "";
    let displayName = "";
    try {
        const resp = await fetch(`/api/sessions/history/${encodeURIComponent(sessionId)}/resume-info`);
        if (resp.ok) {
            const info = await resp.json();
            if (info.working_dir) resumeDir = info.working_dir;
            if (info.board_name) boardName = info.board_name;
            if (info.agent_type) agentType = info.agent_type;
            if (info.display_name) displayName = info.display_name;
        }
    } catch (_) { /* best-effort */ }

    // Pre-fill working directory: saved working dir > settings default > coral root
    const defaultDir = resumeDir || state.settings?.default_working_dir || dirInput.dataset.coralRoot || "";
    dirInput.value = defaultDir;

    // Show board reconnect option if the session was on a board
    if (boardName) {
        boardNameEl.textContent = boardName;
        boardRow.style.display = "";
        document.getElementById("resume-board-check").checked = true;
    } else {
        boardRow.style.display = "none";
    }

    // Store agent type and display name for the launch function
    const modal = document.getElementById("resume-modal");
    modal.dataset.agentType = agentType;
    modal.dataset.displayName = displayName || historyEntry?.display_name || "";
    modal.style.display = "flex";
}

export function hideResumeModal() {
    document.getElementById("resume-modal").style.display = "none";
    // Hide browser if open
    const browser = document.getElementById("resume-dir-browser");
    if (browser) browser.style.display = "none";
}

export async function resumeLaunchNew() {
    if (!state.currentSession || state.currentSession.type !== "history") {
        showToast("No history session selected", true);
        return;
    }

    const sessionId = state.currentSession.name;
    const dir = document.getElementById("resume-dir").value.trim();
    if (!dir) {
        showToast("Working directory is required", true);
        return;
    }

    const modal = document.getElementById("resume-modal");
    const agentType = modal.dataset.agentType || "claude";
    const displayName = modal.dataset.displayName || "";
    const boardCheck = document.getElementById("resume-board-check");
    const boardNameEl = document.getElementById("resume-board-name");
    const boardName = (boardCheck && boardCheck.checked && boardNameEl) ? boardNameEl.textContent : "";

    const btn = document.getElementById("btn-resume-launch");
    if (btn) { btn.disabled = true; btn.textContent = "Resuming..."; }

    try {
        const payload = {
            working_dir: dir,
            agent_type: agentType,
            resume_session_id: sessionId,
        };
        if (displayName) payload.display_name = displayName;
        if (boardName) payload.board_name = boardName;

        const resp = await fetch("/api/sessions/launch", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
        });
        if (resp.status === 403) {
            const err = await resp.json();
            showToast(err.error || "Demo limit reached", true);
            return;
        }
        const result = await resp.json();
        if (result.error) {
            showToast(result.error, true);
        } else {
            showToast(`Resumed session as new agent`);
            hideResumeModal();
            setTimeout(loadLiveSessions, 2000);
        }
    } catch (e) {
        showToast("Failed to resume session", true);
        console.error("resumeLaunchNew error:", e);
    } finally {
        if (btn) { btn.disabled = false; btn.textContent = "Resume"; }
    }
}

// ── Settings Modal ────────────────────────────────────────────────────────

export async function loadSettings() {
    try {
        const resp = await fetch("/api/settings");
        const data = await resp.json();
        const s = data.settings || {};
        // Boolean settings are stored as strings in SQLite — coerce them
        if (typeof s.fit_pane_width === "string") {
            s.fit_pane_width = s.fit_pane_width === "True";
        }
        // Default fit_pane_width to true if not yet set
        if (s.fit_pane_width === undefined) {
            s.fit_pane_width = true;
        }
        if (typeof s.notify_needs_input === "string") {
            s.notify_needs_input = s.notify_needs_input === "True";
        }
        if (s.notify_needs_input === undefined) {
            s.notify_needs_input = true;
        }
        // Coerce show_scrollbars
        if (typeof s.show_scrollbars === "string") {
            s.show_scrollbars = s.show_scrollbars === "True";
        }
        if (s.show_scrollbars === undefined) {
            s.show_scrollbars = true;
        }
        // Coerce refresh_files_on_switch (default: disabled)
        if (typeof s.refresh_files_on_switch === "string") {
            s.refresh_files_on_switch = s.refresh_files_on_switch === "True" || s.refresh_files_on_switch === "true";
        }
        if (s.refresh_files_on_switch === undefined) {
            s.refresh_files_on_switch = false;
        }
        // Coerce proxy_enabled / proxy_enabled_claude (default: disabled)
        if (typeof s.proxy_enabled === "string") {
            s.proxy_enabled = s.proxy_enabled === "True" || s.proxy_enabled === "true";
        }
        if (s.proxy_enabled === undefined) {
            s.proxy_enabled = false;
        }
        // Coerce proxy_enabled_codex (default: disabled)
        if (typeof s.proxy_enabled_codex === "string") {
            s.proxy_enabled_codex = s.proxy_enabled_codex === "True" || s.proxy_enabled_codex === "true";
        }
        if (s.proxy_enabled_codex === undefined) {
            s.proxy_enabled_codex = false;
        }
        // Default file_search_mode to 'directory'
        if (!s.file_search_mode) {
            s.file_search_mode = 'directory';
        }
        // Default git_diff_mode to 'previous_commit' (HEAD~1)
        if (!s.git_diff_mode) {
            s.git_diff_mode = 'previous_commit';
        }
        state.settings = s;

        // Apply scrollbar visibility
        document.body.classList.toggle('no-scrollbars', !s.show_scrollbars);

        // Apply theme from settings (default to Dark if no theme configured)
        const themeName = (!s.custom_theme || s.custom_theme === "GhostV3") ? "Dark" : s.custom_theme;
        await applyCustomThemeByName(themeName);
        if (!s.custom_theme || s.custom_theme === "GhostV3") {
            state.settings.custom_theme = "Dark";
        }
        // Load license tier badge in settings dropdown
        _loadLicenseTierBadge();
    } catch (e) {
        console.error("Failed to load settings:", e);
    }
}

/**
 * Pick the attributed supporter link for one UI surface out of an
 * /api/system/status payload. The server builds these from the same base as
 * store_url, so a store_url override still drives them. Falls back to the raw
 * store_url and then to `fallback`, so a click always has somewhere to go.
 */
export function supporterURL(statusData, surface, fallback) {
    return statusData?.supporter_urls?.[surface] || statusData?.store_url || fallback;
}

/**
 * Report a supporter/store link click to the server, which forwards it to
 * analytics. Fire-and-forget: it must never delay or block opening the link.
 * `surface` is a fixed slug identifying where in the UI the link lives.
 */
export function trackSupporterClick(surface) {
    try {
        const payload = JSON.stringify({ event: 'supporter_checkout_clicked', props: { surface } });
        if (navigator.sendBeacon) {
            navigator.sendBeacon('/api/tracking/event', new Blob([payload], { type: 'application/json' }));
            return;
        }
        fetch('/api/tracking/event', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: payload,
            keepalive: true,
        }).catch(() => {});
    } catch { /* analytics must never break the link */ }
}

/** Show license tier (Supporter License / Free) in the settings dropdown. */
async function _loadLicenseTierBadge() {
    const badge = document.getElementById('license-tier-badge');
    if (!badge) return;
    try {
        // Check if license is skipped for this build tier
        const statusResp = await fetch('/api/system/status');
        if (statusResp.ok) {
            const statusData = await statusResp.json();
            if (statusData.skip_license) {
                if (statusData.tier_name === 'dropboxers') {
                    badge.innerHTML = '<span class="tier-label tier-pro">Coral For Dropbox</span>';
                    badge.style.display = '';
                } else {
                    badge.style.display = 'none';
                }
                return;
            }
        }
        const resp = await fetch('/api/license/status');
        if (!resp.ok) return;
        const data = await resp.json();
        if (data.activated && data.valid) {
            // Licensed supporter — show the product name.
            const label = data.product_name || 'Supporter License';
            badge.innerHTML = `<span class="tier-label tier-pro">${escapeHtml(label)}</span>`;
        } else {
            // Free version — fully unlocked; offer to support development.
            let storeProURL = 'https://store.coralai.ai';
            try {
                const sResp = await fetch('/api/system/status');
                const sData = await sResp.json();
                storeProURL = supporterURL(sData, 'settings_tier_badge', storeProURL);
            } catch (_) {}
            badge.innerHTML = '<span class="tier-label tier-trial">Free</span>' +
                `<a href="${storeProURL}" target="_blank" rel="noopener" onclick="_trackSupporterClick('settings_tier_badge')" style="font-size:11px;color:#58a6ff;margin-left:8px;text-decoration:none">Support the developer</a>`;
        }
        badge.style.display = '';
    } catch { /* silent — non-critical */ }
}

// ── Theme ────────────────────────────────────────────────────────────────

/** Resolve "system" to the actual dark/light value */
function resolveTheme(theme) {
    if (theme === "system") {
        return window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
    }
    return theme;
}

/** Apply a theme to the document */
export function applyTheme(theme) {
    const resolved = resolveTheme(theme);
    document.documentElement.setAttribute("data-theme", resolved);

    // Store the *preference* (dark/light/system) so other pages can read it
    try { localStorage.setItem("coral-theme", theme); } catch (_) {}

    // Update xterm.js terminal colors to match
    updateTerminalTheme();
}

/** Remove all custom CSS variables from inline styles (reset before switching themes) */
function clearCustomThemeVars() {
    const style = document.documentElement.style;
    // Remove any --* properties that were set inline
    const toRemove = [];
    for (let i = 0; i < style.length; i++) {
        const prop = style[i];
        if (prop.startsWith("--")) toRemove.push(prop);
    }
    for (const prop of toRemove) style.removeProperty(prop);
}

/** Load a custom theme by name: apply its base theme + overlay its CSS variables */
async function applyCustomThemeByName(name) {
    try {
        const resp = await fetch(`/api/themes/${encodeURIComponent(name)}`);
        const data = await resp.json();
        if (data.error || !data.theme?.variables) return;

        // Apply the base theme (dark/light)
        const base = data.theme.base || "dark";
        applyTheme(base);

        // Overlay custom variables
        for (const [cssVar, value] of Object.entries(data.theme.variables)) {
            if (value) document.documentElement.style.setProperty(cssVar, value);
        }

        // Refresh terminal colors
        updateTerminalTheme();
    } catch {
        // Fall back to default
    }
}

// Listen for OS theme changes when in "system" mode
window.matchMedia("(prefers-color-scheme: light)").addEventListener("change", () => {
    const pref = (state.settings && state.settings.theme) || "dark";
    if (pref === "system") applyTheme("system");
});

export async function showSettingsModal() {
    const s = state.settings || {};

    // Theme — populate with built-in options + saved custom themes
    const themeSelect = document.getElementById("settings-theme");
    if (themeSelect) {
        // Fetch saved custom themes
        let customThemes = [];
        try {
            const resp = await fetch("/api/themes");
            const data = await resp.json();
            customThemes = data.themes || [];
        } catch (_) {}

        // Build options: built-in first, then custom
        let options = `<option value="dark">Dark</option>
            <option value="light">Light</option>
            <option value="system">System</option>`;
        if (customThemes.length > 0) {
            options += `<option disabled>──────────</option>`;
            for (const t of customThemes) {
                const name = escapeAttr(t.name);
                const label = escapeHtml(t.name);
                options += `<option value="custom:${name}">${label}</option>`;
            }
        }
        themeSelect.innerHTML = options;

        // Set current value
        const currentTheme = s.custom_theme ? `custom:${s.custom_theme}` : (s.theme || "dark");
        themeSelect.value = currentTheme;
    }

    // Default Render Engine
    const currentEngine = s.default_renderer || "xterm";
    const options = getEngineNames()
        .map(name => `<option value="${escapeAttr(name)}"${name === currentEngine ? " selected" : ""}>${escapeHtml(name)}</option>`)
        .join("");
    document.getElementById("settings-renderer-select").innerHTML = options;

    // Default Agent Type
    const currentAgentType = s.default_agent_type || "claude";
    const agentTypeSelect = document.getElementById("settings-agent-type");
    if (agentTypeSelect) agentTypeSelect.value = currentAgentType;

    // Default Permission Mode
    const currentPermMode = s.default_permission_mode || "bypassPermissions";
    const permModeSelect = document.getElementById("settings-permission-mode");
    if (permModeSelect) {
        permModeSelect.value = currentPermMode;
        _updatePermModeDescription(permModeSelect);
    }

    // Default Working Directory
    const dirInput = document.getElementById("settings-working-dir");
    if (dirInput) {
        dirInput.value = s.default_working_dir || "";
        dirInput.placeholder = dirInput.dataset.coralRoot || "/path/to/project";
    }

    // API Key
    const apiKeyInput = document.getElementById("settings-api-key");
    if (apiKeyInput) {
        fetch('/api/system/api-key').then(r => r.ok ? r.json() : null).then(data => {
            if (data && data.key) apiKeyInput.value = data.key;
            else apiKeyInput.value = 'Not configured';
        }).catch(() => { apiKeyInput.value = 'Not available'; });
    }

    // CLI Paths
    const cliClaude = document.getElementById("settings-cli-path-claude");
    const cliCodex = document.getElementById("settings-cli-path-codex");
    const cliGemini = document.getElementById("settings-cli-path-gemini");
    const cliPi = document.getElementById("settings-cli-path-pi");
    if (cliClaude) cliClaude.value = s.cli_path_claude || "";
    if (cliCodex) cliCodex.value = s.cli_path_codex || "";
    if (cliGemini) cliGemini.value = s.cli_path_gemini || "";
    if (cliPi) cliPi.value = s.cli_path_pi || "";

    // Default Models — populate combo inputs + datalists from /api/agent-models
    const defClaude = document.getElementById("settings-default-model-claude");
    const defCodex = document.getElementById("settings-default-model-codex");
    const defGemini = document.getElementById("settings-default-model-gemini");
    const defPi = document.getElementById("settings-default-model-pi");
    if (defClaude) defClaude.value = s.default_model_claude || "";
    if (defCodex) defCodex.value = s.default_model_codex || "";
    if (defGemini) defGemini.value = s.default_model_gemini || "";
    if (defPi) defPi.value = s.default_model_pi || "";
    _getAgentModels().then(models => {
        // _getAgentModels swallows errors and resolves with {} on failure; on
        // success it returns the canonical map with an array for each agent type.
        if (!models || !Array.isArray(models.claude)) {
            const notice = document.getElementById("settings-default-models-notice");
            if (notice) notice.textContent = "Could not load model list — you can still type a custom model id.";
            return;
        }
        _fillModelDatalist(document.getElementById("settings-models-claude"), "claude", models);
        _fillModelDatalist(document.getElementById("settings-models-codex"), "codex", models);
        _fillModelDatalist(document.getElementById("settings-models-gemini"), "gemini", models);
        _fillModelDatalist(document.getElementById("settings-models-pi"), "pi", models);
    });

    // Group by Team
    const groupByTeamCheck = document.getElementById("settings-group-by-team");
    if (groupByTeamCheck) {
        groupByTeamCheck.checked = localStorage.getItem('coral-group-by-team') !== 'false';
    }

    // Proxy Enabled
    const proxyCheck = document.getElementById("settings-proxy-enabled");
    if (proxyCheck) proxyCheck.checked = !!s.proxy_enabled;
    const proxyCodexCheck = document.getElementById("settings-proxy-enabled-codex");
    if (proxyCodexCheck) proxyCodexCheck.checked = !!s.proxy_enabled_codex;

    // Terminal Font Size
    const fontSizeSelect = document.getElementById("settings-terminal-font-size");
    if (fontSizeSelect) fontSizeSelect.value = s.terminal_font_size || "13";

    // Terminal Scrollback
    const scrollbackSelect = document.getElementById("settings-terminal-scrollback");
    if (scrollbackSelect) scrollbackSelect.value = s.terminal_scrollback || "20000";

    // Fit Pane Width
    const fitPaneCheck = document.getElementById("settings-fit-pane-width");
    if (fitPaneCheck) fitPaneCheck.checked = !!s.fit_pane_width;

    // Notify Needs Input
    const notifyCheck = document.getElementById("settings-notify-needs-input");
    if (notifyCheck) notifyCheck.checked = !!s.notify_needs_input;

    // Check for Updates (stored in localStorage, defaults to on)
    const updateCheck = document.getElementById("settings-check-updates");
    if (updateCheck) updateCheck.checked = localStorage.getItem("coral-update-check-enabled") !== "false";

    // Show Scrollbars (defaults to true)
    const scrollbarsCheck = document.getElementById("settings-show-scrollbars");
    if (scrollbarsCheck) scrollbarsCheck.checked = s.show_scrollbars !== false;

    // File Search Mode (defaults to 'directory')
    const fileSearchSelect = document.getElementById("settings-file-search-mode");
    if (fileSearchSelect) fileSearchSelect.value = s.file_search_mode || 'directory';
    const fileSearchLimitSelect = document.getElementById("settings-file-search-limit");
    if (fileSearchLimitSelect) fileSearchLimitSelect.value = s.file_search_limit || "500";

    // Git Diff Mode (defaults to 'previous_commit')
    const gitDiffSelect = document.getElementById("settings-git-diff-mode");
    if (gitDiffSelect) gitDiffSelect.value = s.git_diff_mode || 'previous_commit';

    // License status
    _loadLicenseStatus();

    // App version
    fetch('/api/system/status').then(r => r.json()).then(d => {
        const el = document.getElementById('app-version-label');
        if (el && d.version) el.textContent = 'Version ' + d.version;
    }).catch(() => {});

    document.getElementById("settings-modal").style.display = "flex";
}

async function _loadLicenseStatus() {
    const container = document.getElementById('license-status-display');
    if (!container) return;
    try {
        // Hide license section for builds that skip license
        const statusResp = await fetch('/api/system/status');
        if (statusResp.ok) {
            const statusData = await statusResp.json();
            if (statusData.skip_license) {
                const licenseSection = document.getElementById('license-section');
                if (licenseSection) licenseSection.style.display = 'none';
                const branding = document.getElementById('tier-branding-label');
                if (branding && statusData.tier_name === 'dropboxers') {
                    branding.textContent = 'Coral For Dropbox';
                    branding.style.display = '';
                }
                return;
            }
        }
        const resp = await fetch('/api/license/status');
        if (!resp.ok) throw new Error('fetch failed');
        const data = await resp.json();
        if (data.activated && data.valid) {
            const email = escapeHtml(data.customer_email || '');
            const name = escapeHtml(data.customer_name || '');
            const activated = data.activated_at ? new Date(data.activated_at).toLocaleDateString() : '';
            const validated = data.last_validated ? new Date(data.last_validated).toLocaleDateString() : '';
            const revalDays = data.days_until_revalidation;
            const revalText = revalDays != null ? `${revalDays} day${revalDays !== 1 ? 's' : ''}` : '';
            const machineId = escapeHtml(data.machine_id || '');

            const statusLabel = `<span style="color:var(--green, #4caf50);font-weight:600">${escapeHtml(data.product_name || 'Supporter License')}</span>`;

            container.innerHTML = `
                <dl class="info-grid" style="margin:0">
                    <dt>Status</dt><dd>${statusLabel}</dd>
                    ${name ? `<dt>Name</dt><dd>${name}</dd>` : ''}
                    ${email ? `<dt>Email</dt><dd>${email}</dd>` : ''}
                    ${activated ? `<dt>Activated</dt><dd>${activated}</dd>` : ''}
                    ${validated ? `<dt>Last Validated</dt><dd>${validated}</dd>` : ''}
                    ${revalText ? `<dt>Next Revalidation</dt><dd>${revalText}</dd>` : ''}
                    ${machineId ? `<dt>Machine ID</dt><dd><code style="font-size:11px;color:var(--text-secondary)">${machineId}</code></dd>` : ''}
                </dl>
                <p style="font-size:12px;color:var(--text-secondary);margin:10px 0 0">Thanks for supporting Coral's development. &#10084;&#65039;</p>
                <button class="btn btn-small" onclick="_deactivateLicense()" style="margin-top:8px;color:var(--text-secondary)">Deactivate</button>`;
        } else if (data.activated && !data.valid) {
            container.innerHTML = `<span style="color:var(--text-secondary);font-size:13px">Your license could not be validated — Coral remains free and fully unlocked.</span>
                <br><a href="https://coralai.ai/support.html" target="_blank" style="font-size:11px;color:var(--text-muted)">Contact Support</a>
                <button class="btn btn-small" onclick="_deactivateLicense()" style="margin-top:8px">Deactivate</button>`;
        } else {
            let storeURL = 'https://store.coralai.ai';
            try {
                const sysResp = await fetch('/api/system/status');
                if (sysResp.ok) {
                    const sysData = await sysResp.json();
                    storeURL = supporterURL(sysData, 'license_settings_panel', storeURL);
                }
            } catch { /* use default */ }
            container.innerHTML = `<span style="color:var(--text-secondary);font-size:13px">Free — fully unlocked. A one-time license supports development and retires the activation reminder.</span>
                <br><a href="${storeURL}" target="_blank" rel="noopener" onclick="_trackSupporterClick('license_settings_panel')" class="btn btn-small btn-primary" style="margin-top:8px;display:inline-block;text-decoration:none;font-size:11px">Support the developer</a>`;
        }
    } catch {
        container.innerHTML = `<span style="color:var(--text-secondary);font-size:13px">Unable to check license</span>`;
    }
}

export async function deactivateLicense() {
    window.showConfirmModal?.(
        'Deactivate License',
        'Deactivate this license? You can reactivate it on another machine.',
        async () => {
            try {
                const resp = await fetch('/api/license/deactivate', { method: 'POST' });
                if (resp.ok) {
                    showToast('License deactivated');
                    _loadLicenseStatus();
                } else {
                    const data = await resp.json().catch(() => ({}));
                    showToast(data.error || 'Deactivation failed', true);
                }
            } catch {
                showToast('Deactivation failed', true);
            }
        }
    );
}

export function hideSettingsModal() {
    document.getElementById("settings-modal").style.display = "none";
}

export async function applySettings() {
    const themeValue = document.getElementById("settings-theme")?.value || "dark";
    const engineName = document.getElementById("settings-renderer-select").value;
    const agentType = document.getElementById("settings-agent-type")?.value || "claude";
    const permissionMode = document.getElementById("settings-permission-mode")?.value || "bypassPermissions";
    const workingDir = document.getElementById("settings-working-dir")?.value.trim() || "";
    const cliPathClaude = document.getElementById("settings-cli-path-claude")?.value.trim() || "";
    const cliPathCodex = document.getElementById("settings-cli-path-codex")?.value.trim() || "";
    const cliPathGemini = document.getElementById("settings-cli-path-gemini")?.value.trim() || "";
    const cliPathPi = document.getElementById("settings-cli-path-pi")?.value.trim() || "";
    const defaultModelClaude = document.getElementById("settings-default-model-claude")?.value.trim() || "";
    const defaultModelCodex = document.getElementById("settings-default-model-codex")?.value.trim() || "";
    const defaultModelGemini = document.getElementById("settings-default-model-gemini")?.value.trim() || "";
    const defaultModelPi = document.getElementById("settings-default-model-pi")?.value.trim() || "";
    const fitPaneWidth = document.getElementById("settings-fit-pane-width")?.checked || false;
    const notifyNeedsInput = document.getElementById("settings-notify-needs-input")?.checked || false;
    const terminalFontSize = document.getElementById("settings-terminal-font-size")?.value || "13";
    const terminalScrollback = document.getElementById("settings-terminal-scrollback")?.value || "20000";
    const checkUpdates = document.getElementById("settings-check-updates")?.checked ?? true;
    localStorage.setItem("coral-update-check-enabled", checkUpdates ? "true" : "false");
    const showScrollbars = document.getElementById("settings-show-scrollbars")?.checked ?? true;
    const proxyEnabled = document.getElementById("settings-proxy-enabled")?.checked || false;
    const proxyEnabledCodex = document.getElementById("settings-proxy-enabled-codex")?.checked || false;
    const fileSearchMode = document.getElementById("settings-file-search-mode")?.value || "directory";
    const fileSearchLimit = document.getElementById("settings-file-search-limit")?.value || "500";
    const gitDiffMode = document.getElementById("settings-git-diff-mode")?.value || "branch_point";

    // Parse theme selection — "custom:<name>" or built-in "dark"/"light"/"system"
    let theme, customTheme;
    if (themeValue.startsWith("custom:")) {
        customTheme = themeValue.slice(7);
        theme = "dark"; // will be overridden by the custom theme's base
    } else {
        theme = themeValue;
        customTheme = "";
    }

    const payload = {
        theme: theme,
        custom_theme: customTheme,
        default_renderer: engineName,
        default_agent_type: agentType,
        default_permission_mode: permissionMode,
        default_working_dir: workingDir,
        fit_pane_width: fitPaneWidth,
        notify_needs_input: notifyNeedsInput,
        terminal_font_size: terminalFontSize,
        terminal_scrollback: terminalScrollback,
        show_scrollbars: showScrollbars,
        cli_path_claude: cliPathClaude,
        cli_path_codex: cliPathCodex,
        cli_path_gemini: cliPathGemini,
        cli_path_pi: cliPathPi,
        default_model_claude: defaultModelClaude,
        default_model_codex: defaultModelCodex,
        default_model_gemini: defaultModelGemini,
        default_model_pi: defaultModelPi,
        proxy_enabled: proxyEnabled,
        proxy_enabled_claude: proxyEnabled,
        proxy_enabled_codex: proxyEnabledCodex,
        file_search_mode: fileSearchMode,
        file_search_limit: fileSearchLimit,
        git_diff_mode: gitDiffMode,
    };

    const oldGitDiffMode = state.settings?.git_diff_mode || 'previous_commit';

    try {
        await fetch("/api/settings", {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
        });
        state.settings = { ...state.settings, ...payload };
        // Defaults may have changed — force next ACF open to re-fetch.
        _invalidateDefaultModels();

        // Clear any previously applied custom CSS variables
        clearCustomThemeVars();

        if (customTheme) {
            // Load and apply the custom theme (sets base + variables)
            await applyCustomThemeByName(customTheme);
        } else {
            // Apply built-in theme
            applyTheme(theme);
        }

        // If a live session is selected, force re-render with new default
        if (state.currentSession?.session_id) {
            const el = document.getElementById("pane-capture");
            if (el && el._lastCapture) {
                renderCaptureText(el, el._lastCapture);
            }
        }

        // Trigger pane width sync if the setting was just enabled
        if (fitPaneWidth) {
            syncPaneWidth();
        }

        // Apply font size and scrollback to existing terminal
        const term = getTerminal();
        if (term) {
            term.options.fontSize = parseInt(terminalFontSize, 10) || 13;
            term.options.scrollback = parseInt(terminalScrollback, 10) || 20000;
            const { fitTerminal } = await import('./xterm_renderer.js');
            fitTerminal();
        }

        // Apply scrollbar visibility
        document.body.classList.toggle('no-scrollbars', !showScrollbars);

        // Refresh changed files if git diff mode changed
        if (gitDiffMode !== oldGitDiffMode && typeof refreshChangedFiles === 'function') {
            refreshChangedFiles();
        }

        showToast("Settings saved");
    } catch (e) {
        showToast("Failed to save settings", true);
    }

    hideSettingsModal();
}

// ── Default Prompts Modal ──────────────────────────────────────────

// Cached default prompts fetched from the backend API.
// Populated on first open of the modal; used for "Reset to Default".
let _defaultPrompts = null;

async function _ensureDefaultPrompts() {
    if (_defaultPrompts) return _defaultPrompts;
    try {
        const res = await fetch("/api/settings/default-prompts");
        _defaultPrompts = await res.json();
    } catch {
        // Fallback — should never happen in practice
        _defaultPrompts = {
            default_prompt_orchestrator: 'IMPORTANT: You were automatically joined to message board "{board_name}". Do NOT run coral-board join. Post a message with coral-board post "<your introduction>" that introduces yourself, then discuss your proposed plan with the operator (the human user) before posting assignments.\n\nCRITICAL: Do NOT poll or loop on \'coral-board read\'. After posting your introduction or any message, STOP. Coral will send you a notification (as a user message) when new messages arrive. Only run \'coral-board read\' after receiving such a notification.',
            default_prompt_worker: 'IMPORTANT: You were automatically joined to message board "{board_name}". Do NOT run coral-board join. Do not start any actions until you receive instructions from the Orchestrator on the message board. Post a message with coral-board post "<your introduction>" that introduces yourself, then STOP.\n\nCRITICAL: Do NOT poll or loop on \'coral-board read\'. Coral will automatically notify you (as a user message) when new messages arrive — only run \'coral-board read\' after receiving a notification. Between notifications, do nothing and wait.',
            team_reminder_orchestrator: 'Remember to coordinate with your team and check the message board for updates',
            team_reminder_worker: 'Remember to work with your team',
        };
    }
    return _defaultPrompts;
}

export async function showDefaultPromptsModal() {
    const defaults = await _ensureDefaultPrompts();
    const s = state.settings || {};
    const orchEl = document.getElementById("settings-prompt-orchestrator");
    const workerEl = document.getElementById("settings-prompt-worker");
    if (orchEl) orchEl.value = s.default_prompt_orchestrator || defaults.default_prompt_orchestrator;
    if (workerEl) workerEl.value = s.default_prompt_worker || defaults.default_prompt_worker;
    const teamOrchEl = document.getElementById("settings-team-reminder-orchestrator");
    const teamWorkerEl = document.getElementById("settings-team-reminder-worker");
    if (teamOrchEl) teamOrchEl.value = s.team_reminder_orchestrator || defaults.team_reminder_orchestrator;
    if (teamWorkerEl) teamWorkerEl.value = s.team_reminder_worker || defaults.team_reminder_worker;
    document.getElementById("default-prompts-modal").style.display = "flex";
}

export function hideDefaultPromptsModal() {
    document.getElementById("default-prompts-modal").style.display = "none";
}

export async function resetDefaultPrompt(type) {
    const defaults = await _ensureDefaultPrompts();
    const mapping = {
        orchestrator: ["settings-prompt-orchestrator", "default_prompt_orchestrator"],
        worker: ["settings-prompt-worker", "default_prompt_worker"],
        team_orchestrator: ["settings-team-reminder-orchestrator", "team_reminder_orchestrator"],
        team_worker: ["settings-team-reminder-worker", "team_reminder_worker"],
    };
    const entry = mapping[type];
    if (entry) {
        const el = document.getElementById(entry[0]);
        if (el) el.value = defaults[entry[1]];
    }
}

export async function saveDefaultPrompts() {
    const defaults = await _ensureDefaultPrompts();
    const orchValue = document.getElementById("settings-prompt-orchestrator")?.value || "";
    const workerValue = document.getElementById("settings-prompt-worker")?.value || "";
    const teamOrchValue = document.getElementById("settings-team-reminder-orchestrator")?.value || "";
    const teamWorkerValue = document.getElementById("settings-team-reminder-worker")?.value || "";

    // Save empty string when value matches default — so future default updates are picked up
    const payload = {
        default_prompt_orchestrator: orchValue === defaults.default_prompt_orchestrator ? "" : orchValue,
        default_prompt_worker: workerValue === defaults.default_prompt_worker ? "" : workerValue,
        team_reminder_orchestrator: teamOrchValue === defaults.team_reminder_orchestrator ? "" : teamOrchValue,
        team_reminder_worker: teamWorkerValue === defaults.team_reminder_worker ? "" : teamWorkerValue,
    };

    try {
        await fetch("/api/settings", {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
        });
        state.settings = { ...state.settings, ...payload };
        showToast("Default prompts saved");
    } catch (e) {
        showToast("Failed to save prompts", true);
    }

    hideDefaultPromptsModal();
}

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

// Attach input listeners for flag sync (after DOM is ready)
document.addEventListener("DOMContentLoaded", () => {
    for (const id of ["launch-flags", "restart-flags", "job-modal-flags", "team-flags"]) {
        const el = document.getElementById(id);
        if (el) el.addEventListener("input", () => _syncFlagButtons(id));
    }
});

// Close modals on outside click
document.addEventListener("click", (e) => {
    const launchModal = document.getElementById("launch-modal");
    const infoModal = document.getElementById("info-modal");
    const resumeModal = document.getElementById("resume-modal");
    const settingsModal = document.getElementById("settings-modal");
    const restartModal = document.getElementById("restart-modal");
    if (e.target === launchModal) {
        hideLaunchModal();
    }
    if (e.target === infoModal) {
        hideInfoModal();
    }
    if (e.target === resumeModal) {
        hideResumeModal();
    }
    if (e.target === settingsModal) {
        hideSettingsModal();
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
            if (modals[i].style.display !== 'none' && modals[i].offsetParent !== null) {
                const id = modals[i].id;
                if (id === 'launch-modal') hideLaunchModal();
                else if (id === 'info-modal') hideInfoModal();
                else if (id === 'resume-modal') hideResumeModal();
                else if (id === 'settings-modal') hideSettingsModal();
                else if (id === 'restart-modal') hideRestartModal();
                else if (id === 'macro-modal') modals[i].style.display = 'none';
                else if (id === 'webhook-modal') window.hideWebhookModal?.();
                else if (id === 'alert-modal') window.hideAlertModal?.();
                else if (id === 'confirm-modal') window.hideConfirmModal?.();
                else if (id === 'prompt-modal') window.hidePromptModal?.();
                else if (id === 'team-agent-modal') window.hideTeamAgentModal?.();
                else if (id === 'default-prompts-modal') hideDefaultPromptsModal();
                else if (id === 'team-wizard-modal') window.hideTeamWizardModal?.();
                else if (id === 'save-template-modal') modals[i].style.display = 'none';
                else if (id === 'job-modal') modals[i].style.display = 'none';
                else if (id === 'quick-launch-modal') modals[i].style.display = 'none';
                else if (id === 'add-agent-board-modal') window.hideAddAgentBoardModal?.();
                else if (id === 'custom-view-modal') modals[i].style.display = 'none';
                else if (id === 'mobile-connect-modal') modals[i].style.display = 'none';
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
            if (modals[i].style.display !== 'none' && modals[i].offsetParent !== null) {
                const focusable = modals[i].querySelectorAll(
                    'button:not([disabled]), input:not([disabled]), textarea:not([disabled]), select:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])'
                );
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

// ── Mobile Connect Modal ─────────────────────────────────────────────────

window._showMobileConnectModal = async function() {
    const modal = document.getElementById('mobile-connect-modal');
    if (!modal) return;

    // Detect LAN IP — prefer server-reported IP over window.location
    let baseUrl = window.location.origin;
    try {
        const netResp = await fetch('/api/system/network-info');
        if (netResp.ok) {
            const netData = await netResp.json();
            const lanIp = netData.primary || (netData.ips && netData.ips[0]);
            if (lanIp) {
                const port = netData.port || window.location.port || '8420';
                baseUrl = `http://${lanIp}:${port}`;
            }
        }
    } catch { /* fall back to window.location.origin */ }

    // QR code contains only the server URL (no API key)
    document.getElementById('mobile-connect-url').textContent = baseUrl;

    // Generate QR code via server endpoint
    const qrContainer = document.getElementById('mobile-connect-qr');
    if (qrContainer) {
        qrContainer.innerHTML = `<img src="/api/system/qr?url=${encodeURIComponent(baseUrl)}" alt="QR Code" style="width:180px;height:180px;border-radius:8px;background:#fff;padding:8px" onerror="this.style.display='none'">`;
    }

    // Show API key separately below QR code
    const apiKeyDisplay = document.getElementById('mobile-connect-api-key');
    if (apiKeyDisplay) {
        try {
            const resp = await fetch('/api/system/api-key');
            if (resp.ok) {
                const data = await resp.json();
                if (data.key) {
                    apiKeyDisplay.innerHTML = `<strong>API Key:</strong> <code style="user-select:all">${escapeHtml(data.key)}</code>
                        <button class="btn btn-small" style="margin-left:6px" onclick="navigator.clipboard.writeText('${escapeAttr(data.key)}'); this.textContent='Copied!'; setTimeout(()=>this.textContent='Copy',1500)">Copy</button>
                        <div style="color:var(--text-muted);font-size:11px;margin-top:4px">Send this to the mobile app via Slack or another secure channel.</div>`;
                } else {
                    apiKeyDisplay.style.display = 'none';
                }
            } else {
                apiKeyDisplay.style.display = 'none';
            }
        } catch { apiKeyDisplay.style.display = 'none'; }
    }

    modal.style.display = 'flex';
};
