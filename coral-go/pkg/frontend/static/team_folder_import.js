/* Importing a team from a folder (moved out of app.js, #5082; no behaviour change). */
import { importOnce } from './import_once.js';

// ── Import team from folder ───────────────────────────────────────────
export function _importTeamFromFolder() {
    const input = document.getElementById('team-folder-input');
    if (input) { input.value = ''; input.click(); }
}

export async function _handleTeamFolderImport(input) {
    const files = Array.from(input.files || []);
    if (!files.length) return;

    // Parse YAML frontmatter from markdown content
    function parseFrontmatter(text) {
        const match = text.match(/^---\s*\n([\s\S]*?)\n---\s*\n([\s\S]*)$/);
        if (!match) return { meta: {}, body: text.trim() };
        const meta = {};
        match[1].split('\n').forEach(line => {
            const m = line.match(/^(\w[\w-]*):\s*(.+)$/);
            if (m) meta[m[1].trim()] = m[2].trim().replace(/^["']|["']$/g, '');
        });
        return { meta, body: match[2].trim() };
    }

    // Find SKILL.md (orchestrator) and agents/*.md
    let orchestrator = null;
    const agents = [];

    let folderName = '';
    for (const file of files) {
        if (!file.name.endsWith('.md')) continue;
        const path = file.webkitRelativePath || file.name;
        const text = await file.text();
        const { meta, body } = parseFrontmatter(text);

        // Extract top-level folder name for team board name
        if (!folderName && path.includes('/')) {
            folderName = path.split('/')[0];
        }

        if (file.name.match(/^SKILL\.md$/i)) {
            orchestrator = { name: 'Orchestrator', description: meta.description || '', prompt: body };
        } else if (path.match(/\/agents\//i) || file.name !== 'SKILL.md') {
            const name = meta.name || file.name.replace(/\.md$/, '').replace(/-/g, ' ');
            agents.push({ name, prompt: body });
        }
    }

    if (!orchestrator && agents.length === 0) {
        const { showToast } = await importOnce('./utils.js');
        showToast('No valid .md files found (expected SKILL.md and/or agents/*.md)', true);
        return;
    }

    // Set team board name from folder name if empty
    const boardNameInput = document.getElementById('team-board-name');
    if (boardNameInput && !boardNameInput.value.trim() && folderName) {
        boardNameInput.value = folderName.replace(/-/g, ' ');
        if (window._validateTeamName) window._validateTeamName();
    }

    // Clear existing agents and populate
    const list = document.getElementById('team-agents-list');
    if (list) list.innerHTML = '';

    // Fetch default prompts from settings for orchestrator/worker board instructions
    let defaults = {};
    try {
        const res = await fetch('/api/settings/default-prompts');
        defaults = await res.json();
    } catch { /* use empty defaults */ }
    const orchSuffix = (defaults.default_prompt_orchestrator || 'Coordinate with the team via the message board.').replace('{board_name}', folderName || 'team');
    const workerSuffix = (defaults.default_prompt_worker || 'Coordinate with the team via the message board.').replace('{board_name}', folderName || 'team');

    if (orchestrator) {
        // Strip the name/title heading from the prompt body
        let body = orchestrator.prompt.replace(/^#\s+.*\n*/m, '').trim();
        const prompt = `You are the Orchestrator. For this session here is the skill you will use:\n\n${body}\n\n${orchSuffix}`;
        if (window._addTeamAgent) window._addTeamAgent(orchestrator.name, prompt);
    }
    for (const agent of agents) {
        let body = agent.prompt.replace(/^#\s+.*\n*/m, '').trim();
        const prompt = `For this session here is the skill you will use:\n\n${body}\n\n${workerSuffix}`;
        if (window._addTeamAgent) window._addTeamAgent(agent.name, prompt);
    }

    const { showToast } = await importOnce('./utils.js');
    const count = (orchestrator ? 1 : 0) + agents.length;
    showToast(`Imported ${count} agent${count !== 1 ? 's' : ''} from folder`);
}
