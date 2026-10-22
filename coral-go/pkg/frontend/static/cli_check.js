/* Moved out of modals.js as it was (#3844): no behaviour change. */
import { importOnce } from './import_once.js';
const { _acfServer } = await importOnce('./launch_models.js');
import { checkAgentCLI } from './prerequisites.js';

// ── CLI Availability Check ────────────────────────────────────────────────
// Mirrors agentCLIs in internal/agent/agent.go (a Go test keeps them equal): the install command, the vendor's own install page and,
// for agy, what to do about ~/.local/bin (#4569). A server's answer (install_url, install_note) wins where it has one.
const AGY_NOTE = 'The installer puts agy in ~/.local/bin. Coral looks there too, so it finds agy either way. To run agy in your own terminal, put that folder on your PATH: add export PATH="$HOME/.local/bin:$PATH" to ~/.zshrc or ~/.bashrc, then open a new terminal.';
const AGY = { name: 'agy', cmd: 'curl -fsSL https://antigravity.google/cli/install.sh | bash', url: 'https://antigravity.google', note: AGY_NOTE };
export const CLI_INSTALL_INSTRUCTIONS = {
    claude: { name: 'claude', cmd: 'npm install -g @anthropic-ai/claude-code', url: 'https://code.claude.com/docs/en/overview' },
    agy: AGY,
    antigravity: AGY,
    gemini: AGY,
    codex: { name: 'codex', cmd: 'npm install -g @openai/codex', url: 'https://github.com/openai/codex' },
    pi: { name: 'pi', cmd: 'npm install -g --ignore-scripts @earendil-works/pi-coding-agent', url: 'https://pi.dev' },
};

export function _checkAgentCLI(agentType, warning = document.getElementById('cli-check-warning'), force = false) {
    // The CLI must exist on the server the agent will run on.
    const server = _acfServer(warning && warning.closest ? warning.closest('[data-acf-uid]') : null);
    const info = CLI_INSTALL_INSTRUCTIONS[agentType];
    return checkAgentCLI(agentType, warning, {force,command:info?.cmd || '',url:info?.url || '',note:info?.note || '',server});
}
(window.__coral ||= {})._checkAgentCLI = _checkAgentCLI;
