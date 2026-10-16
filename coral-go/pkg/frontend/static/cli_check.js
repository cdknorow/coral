/* Moved out of modals.js as it was (#3844): no behaviour change. */
import { _acfServer } from './launch_models.js';
import { checkAgentCLI } from './prerequisites.js';

// ── CLI Availability Check ────────────────────────────────────────────────
export const CLI_INSTALL_INSTRUCTIONS = {
    claude: { name: 'claude', cmd: 'npm install -g @anthropic-ai/claude-code' },
    agy: { name: 'agy', cmd: 'curl -fsSL https://antigravity.google/install.sh | bash' },
    antigravity: { name: 'agy', cmd: 'curl -fsSL https://antigravity.google/install.sh | bash' },
    gemini: { name: 'agy', cmd: 'curl -fsSL https://antigravity.google/install.sh | bash' },
    codex: { name: 'codex', cmd: 'npm install -g @openai/codex' },
    pi: { name: 'pi', cmd: 'npm install -g @mariozechner/pi-coding-agent' },
};

export function _checkAgentCLI(agentType, warning = document.getElementById('cli-check-warning'), force = false) {
    // The CLI must exist on the server the agent will run on.
    const server = _acfServer(warning && warning.closest ? warning.closest('[data-acf-uid]') : null);
    return checkAgentCLI(agentType, warning, {force,command:CLI_INSTALL_INSTRUCTIONS[agentType]?.cmd || '',server});
}
(window.__coral ||= {})._checkAgentCLI = _checkAgentCLI;
