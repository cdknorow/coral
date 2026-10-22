/* Moved out of modals.js as it was (#3844): no behaviour change. */
import { importOnce } from './import_once.js';
const { _acfServer } = await importOnce('./launch_models.js');
import { checkAgentCLI } from './prerequisites.js';

// ── CLI Availability Check ────────────────────────────────────────────────
// Mirrors agentCLIs in internal/agent/agent.go (a Go test keeps them equal): the install command, the vendor's own install page and,
// for agy, what to do about ~/.local/bin (#4569). A server's answer (install_url, install_note) wins where it has one.
const AGY_NOTE = 'The installer puts agy in ~/.local/bin. Coral looks there too, so it finds agy either way. To run agy in your own terminal, put that folder on your PATH: add export PATH="$HOME/.local/bin:$PATH" to ~/.zshrc or ~/.bashrc, then open a new terminal.';
// Windows (#5885): the vendor's PowerShell line, and a note with no bash, export or ~/.local in it (what GetCLIInfo gives agy on a Windows server).
const AGY_WIN_NOTE = 'Run this in PowerShell. The installer puts agy.exe in %LOCALAPPDATA%\\agy\\bin. Coral looks there too, so it finds agy either way. To run agy in a terminal of your own, open a new terminal after the install so it picks up the PATH change.';
const AGY = { name: 'agy', cmd: 'curl -fsSL https://antigravity.google/cli/install.sh | bash', url: 'https://antigravity.google', note: AGY_NOTE,
    winCmd: 'irm https://antigravity.google/cli/install.ps1 | iex', winNote: AGY_WIN_NOTE };
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
    // This table is only the fallback for a server that did not answer with its own command and note (they win, and they are right for the server's OS): the browser's OS is the server's
    // only when the server is this computer.
    const win = server === 'local' && /^Win/i.test((typeof navigator !== 'undefined' && (navigator.userAgentData?.platform || navigator.platform)) || '');
    return checkAgentCLI(agentType, warning, {force,command:(win && info?.winCmd) || info?.cmd || '',url:info?.url || '',note:(win && info?.winNote) || info?.note || '',server});
}
(window.__coral ||= {})._checkAgentCLI = _checkAgentCLI;
