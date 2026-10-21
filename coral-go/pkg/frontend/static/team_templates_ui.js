/* The Agent Team form's templates (moved out of team_dialogs.js as it was, #5174; no behaviour change): the built-in templates, the template selector, the welcome screen's quick launch and Save as Template. */
import { importOnce } from './import_once.js';
import { showToast, escapeHtml, escapeAttr, boardNameProblem } from './utils.js';
import { loadLiveSessions } from './api.js';
import { recordRecentDir } from './browser.js';
const { launch } = await importOnce('./launch_state.js');
const { _effectiveServer, _settingsFor } = await importOnce('./launch_models.js');
const { _getPermissionFlagsForAgentMode } = await importOnce('./permission_flags.js');
const { _getSavedTeamTemplates, _upsertSavedTeamTemplates } = await importOnce('./persona_store.js');
const { _launchBlocked, _launchOn, _onServerSuffix, _refreshAfterLaunch, showTeamLaunchFailures } = await importOnce('./launch_core.js');
const { showSettingsModal } = await importOnce('./settings_dialog.js');
const { getAgentConfig } = await importOnce('./agent_config_form.js');

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

/** Launch a team preset from the welcome screen — opens the team modal pre-filled. */
window._quickLaunchTeam = async function() {
    const templateName = document.getElementById('quick-launch-template').value;
    const boardName = document.getElementById('quick-launch-board-name').value.trim() ||
        document.getElementById('quick-launch-board-name').placeholder;
    const workDir = document.getElementById('quick-launch-workdir').value.trim();

    if (!workDir) { showToast('Working directory is required', 'error'); return; }
    const nameProblem = boardNameProblem(boardName);
    if (nameProblem) { showToast(nameProblem, 'error'); return; }

    const all = [...BUILTIN_TEAM_TEMPLATES, ..._getSavedTeamTemplates()];
    const tmpl = all.find(t => t.name === templateName);
    if (!tmpl) { showToast('Template not found', 'error'); return; }

    // Disable launch button to prevent double-clicks
    const launchBtn = document.querySelector('#quick-launch-modal .btn-primary');
    if (launchBtn) { launchBtn.disabled = true; launchBtn.textContent = 'Launching...'; }

    const server = _effectiveServer(launch.quickServer);
    if (_launchBlocked(server)) { if (launchBtn) { launchBtn.disabled = false; launchBtn.textContent = 'Launch'; } return; }
    const ts = _settingsFor(server);
    const agentType = tmpl.agents?.[0]?.agent_type || ts.default_agent_type || 'claude';
    const defaultPermMode = ts.default_permission_mode || 'bypassPermissions';
    const permFlag = _getPermissionFlagsForAgentMode(agentType, defaultPermMode);
    const flags = permFlag ? permFlag.split(/\s+/) : [];

    try {
        const data = await _launchOn(server, '/api/sessions/launch-team', {
                board_name: boardName,
                working_dir: workDir,
                agent_type: agentType,
                flags,
                agents: tmpl.agents.map(a => {
                    const entry = { name: a.name, prompt: a.prompt, capabilities: a.capabilities };
                    if (a.agent_type) entry.agent_type = a.agent_type;
                    if (a.model) entry.model = a.model;
                    if (a.flags) {
                        entry.flags = typeof a.flags === 'string' ? a.flags.split(/\s+/).filter(Boolean) : a.flags;
                    }
                    if (a.knowledge) entry.knowledge = a.knowledge;
                    return entry;
                }),
                team_knowledge_index: tmpl.team_knowledge_index || '',
                working_mode: tmpl.working_mode || null,
                workflow_presets: tmpl.workflow_presets || null,
        }, undefined, { teamFailures: true });
        if (!data) return;
        const launched = data.agents || [];
        const failed = launched.filter(a => a.error);
        // Nobody started (the server answered an error, #122): each member's error, and the dialog stays open to try again.
        if (data.ok === false) {
            showTeamLaunchFailures(failed, 0, showSettingsModal);
            return;
        }
        recordRecentDir(workDir, server);
        document.getElementById('quick-launch-modal').style.display = 'none';
        if (failed.length > 0) {
            showToast(`Launched ${launched.length - failed.length} of ${launched.length} agents (${failed.length} failed)`, 'error');
        } else {
            showToast(`Launched ${templateName} on ${boardName}${_onServerSuffix(server)}`);
            setTimeout(loadLiveSessions, 2000);
            _refreshAfterLaunch(server, { boardName });
        }
    } catch (e) {
        showToast('Launch failed: ' + e.message, 'error');
    } finally {
        if (launchBtn) { launchBtn.disabled = false; launchBtn.textContent = 'Launch'; }
    }
};

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
            if (config.thinking) entry.thinking = config.thinking; // #4969
            if (config.flags) entry.flags = config.flags;
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

    const entry = { name: templateName, agents: _pendingSaveTemplateAgents, flags: _pendingSaveTemplateFlags };
    try { await _upsertSavedTeamTemplates([entry]); } catch { showToast("Could not save the template", true); return; }
    showToast(`Saved template "${templateName}"`);
    _renderTeamTemplateSelector();
    _hideSaveTemplateModal();
}
window._confirmSaveTemplate = _confirmSaveTemplate;

export { BUILTIN_TEAM_TEMPLATES, _renderTeamTemplateSelector };
