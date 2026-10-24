/* What the sidebar's menus do to a team or a folder's agents: kill all, share, save as a template, sleep (moved out of render.js, #4996; no behaviour change). */
import { state } from './state.js';
import { registerDialog } from './modal_chrome.js';
import { sessionFolderKey, serverFetch, sessionServer, keyLabel, splitKey, inTeam, teamFetch, boardFetch } from './server_base.js';
import { browserLocal } from './safe_storage.js';
import { redrawLiveList } from './list_hooks.js';
import { showConfirmModal } from './confirm_dialog.js';
import { launchBlockedReason } from './hub_access.js';
import { showToast, escapeHtml } from './utils.js';
import { updateSettingList, upsertByName, parseList } from './settings_lists.js';
import { _passesFilters } from './list_filter_match.js';
import { killCount } from './agent_actions.js';
import { showPromptModal } from './prompt_modals.js';
import { worktreeConfirmWords } from './worktree_words.js';
import { showWorktreeNotice } from './worktree_notice.js';

// The agents a folder or workflow group shows, and only those (#3053): team
// members are listed (and killed) under their team, archived agents in the
// Archived section, and with Group by Team on, workflow-run agents in their
// workflow group. A search narrows the group as it does in the list.
function _groupMembers(groupName) {
    const byTeam = browserLocal.get('coral-group-by-team') !== 'false';
    const shown = state.liveSessions.filter(s => !s.archived && !s.board_project && _passesFilters(s));
    if (groupName.startsWith('wf:')) {
        const wf = groupName.slice(3);
        return byTeam ? shown.filter(s => s.workflow_name === wf) : [];
    }
    return shown.filter(s => sessionFolderKey(s) === groupName && !(byTeam && s.workflow_name));
}

export async function killGroup(groupName) {
    const groupSessions = _groupMembers(groupName);
    if (!groupSessions.length) return;

    showConfirmModal(
        "Kill All Agents",
        `Kill ${killCount(groupSessions.length).agents} in "${groupName.startsWith('wf:') ? groupName.slice(3) : keyLabel(groupName)}"? This will terminate ${groupSessions.length === 1 ? 'it' : 'them'}. ${killCount(groupSessions.length).note}`,
        async () => {
            for (const s of groupSessions) {
                try {
                    await serverFetch(sessionServer(s), `/api/sessions/live/${encodeURIComponent(s.name)}/kill`, {
                        method: "POST",
                        headers: { "Content-Type": "application/json" },
                        body: JSON.stringify({ agent_type: s.agent_type, session_id: s.session_id }),
                    });
                } catch (e) {
                    console.error(`Failed to kill ${s.name}:`, e);
                }
            }
            const killedIds = new Set(groupSessions.map(s => s.session_id));
            state.liveSessions = state.liveSessions.filter(s => !killedIds.has(s.session_id));
            redrawLiveList();
        }
    );
}

/** Kill Team's confirmation says what happens to the team's worktree, when it has one (an agent of the team is on a worktree). */
function _teamWorktreeSentence(sessions) {
    const wt = sessions.find(s => s.git_state && s.git_state.is_worktree);
    return wt ? `${worktreeConfirmWords(wt.branch)} ` : '';
}

export async function killBoard(boardName) {
    const boardSessions = state.liveSessions.filter(s => inTeam(s, boardName));
    if (!boardSessions.length) return;
    // Through the Coral Hub a whole team needs Launch (#3433, #3443): say why (a toast, as Delete
    // does) instead of a refusal per agent.
    const blocked = launchBlockedReason(splitKey(boardName).server);
    if (blocked) { showToast(blocked, true); return; }

    showConfirmModal(
        "Kill Team",
        `Kill ${killCount(boardSessions.length).agents} on board "${keyLabel(boardName)}"? This will terminate ${boardSessions.length === 1 ? 'it' : 'them'}. ${_teamWorktreeSentence(boardSessions)}${killCount(boardSessions.length).note}`,
        async () => {
            let worktree = null; // the stop's answer about the team's worktree: the kill that ends it carries it
            for (const s of boardSessions) {
                try {
                    const r = await serverFetch(sessionServer(s), `/api/sessions/live/${encodeURIComponent(s.name)}/kill`, {
                        method: "POST",
                        headers: { "Content-Type": "application/json" },
                        body: JSON.stringify({ agent_type: s.agent_type, session_id: s.session_id }),
                    });
                    const answer = await r.json().catch(() => null);
                    if (answer && answer.worktree && answer.worktree.state && answer.worktree.state !== 'none') worktree = answer.worktree;
                } catch (e) {
                    console.error(`Failed to kill ${s.name}:`, e);
                }
            }
            showWorktreeNotice(worktree); // an error is an alert, the rest a status; a kept one stays on screen with its path (#6818)
            const killedIds = new Set(boardSessions.map(s => s.session_id));
            state.liveSessions = state.liveSessions.filter(s => !killedIds.has(s.session_id));
            redrawLiveList();
        }
    );
}

function _buildTeamTemplateFromBoard(boardName) {
    const boardSessions = (state.liveSessions || []).filter(s => inTeam(s, boardName));
    if (!boardSessions.length) return null;

    const agents = [];
    for (const s of boardSessions) {
        const agent = {
            name: s.display_name || s.board_job_title || s.name,
            prompt: s.prompt || '',
        };
        if (s.agent_type) agent.agent_type = s.agent_type;
        if (s.model) agent.model = s.model;
        if (s.capabilities) agent.capabilities = s.capabilities;
        agents.push(agent);
    }
    return { name: keyLabel(boardName), agents, flags: '' };
}

async function _attachKnowledgeToTemplate(boardName, tmpl) {
    try {
        const resp = await teamFetch(boardName, n => `/api/teams/detail/${encodeURIComponent(n)}/knowledge`);
        if (!resp.ok) return tmpl;
        const data = await resp.json();
        if (!data.exists || !data.agents) return tmpl;
        const knowledgeMap = {};
        for (const a of data.agents) {
            knowledgeMap[a.name.toLowerCase()] = a.content;
        }
        for (const agent of tmpl.agents) {
            const slug = agent.name.toLowerCase().replace(/\s+/g, '-').replace(/[^a-z0-9_-]/g, '');
            if (knowledgeMap[slug]) {
                agent.knowledge = knowledgeMap[slug];
            }
        }
        if (data.index) tmpl.team_knowledge_index = data.index;
    } catch { /* knowledge is optional */ }
    return tmpl;
}

async function _attachWorkingModeToTemplate(boardName, tmpl) {
    try {
        const [modeResp, presetsResp] = await Promise.all([
            boardFetch(boardName, `/working-mode`),
            boardFetch(boardName, `/working-mode/presets`),
        ]);
        if (modeResp.ok) {
            const mode = await modeResp.json();
            if (mode.mode && mode.mode !== 'none') {
                tmpl.working_mode = {
                    mode: mode.mode,
                    dependency_guidance: mode.dependency_guidance || false,
                    custom_instructions: mode.custom_instructions || '',
                };
            }
        }
        if (presetsResp.ok) {
            const data = await presetsResp.json();
            const customPresets = (data.presets || []).filter(p => !p.builtin || p.overridden);
            if (customPresets.length > 0) {
                tmpl.workflow_presets = customPresets.map(p => ({
                    id: p.id,
                    name: p.name,
                    instructions: p.instructions || '',
                    builtin: p.builtin || false,
                }));
            }
        }
    } catch { /* working mode is optional */ }
    return tmpl;
}

export async function shareAgentTeam(boardName) {
    let tmpl = _buildTeamTemplateFromBoard(boardName);
    if (!tmpl) {
        showToast("No agents found on this board", "error");
        return;
    }

    tmpl = await _attachKnowledgeToTemplate(boardName, tmpl);
    tmpl = await _attachWorkingModeToTemplate(boardName, tmpl);

    const template = {
        version: 1,
        type: "coral-team-templates",
        templates: [tmpl],
    };

    const jsonStr = JSON.stringify(template, null, 2);
    const filename = `coral-team-${keyLabel(boardName).replace(/[^a-zA-Z0-9-_]/g, "_")}.json`;

    // Show export modal with JSON content
    const modal = document.createElement('div');
    modal.className = 'modal';
    modal.style.display = 'flex';
    modal.innerHTML = `
        <div class="modal-content" style="width:600px">
            <div class="modal-header">
                <h3>Export Team: ${escapeHtml(keyLabel(boardName))}</h3>
                <p style="color:var(--text-secondary);font-size:0.8125rem;margin:6px 0 0">${tmpl.agents.length} agents</p>
            </div>
            <div class="modal-body">
                <textarea readonly style="width:100%;height:260px;font-family:monospace;font-size:0.75rem;resize:vertical;background:var(--bg-tertiary);color:var(--text-primary);border:1px solid var(--border);border-radius:8px;padding:12px">${escapeHtml(jsonStr)}</textarea>
            </div>
            <div class="modal-actions modal-footer">
                <button class="btn" data-action="close">Close</button>
                <button class="btn" data-action="download">Download</button>
                <button class="btn btn-primary" data-action="copy">Copy to Clipboard</button>
            </div>
        </div>
    `;
    document.body.appendChild(modal);
    const close = registerDialog(modal);
    modal.addEventListener('click', (e) => {
        const action = e.target.dataset?.action;
        if (action === 'close') { close(); return; }
        if (action === 'copy') {
            navigator.clipboard.writeText(jsonStr).then(() => showToast('Copied to clipboard'));
            return;
        }
        if (action === 'download') {
            const blob = new Blob([jsonStr], { type: 'application/json' });
            const url = URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = url; a.download = filename; a.click();
            URL.revokeObjectURL(url);
            showToast(`Downloaded ${filename}`);
        }
    });
}

export function saveTeamFromSidebar(boardName) {
    showPromptModal('Save Team Template', 'Template name', keyLabel(boardName), async (templateName) => {
        let tmpl = _buildTeamTemplateFromBoard(boardName);
        if (!tmpl) {
            showToast("No agents found on this board", "error");
            return;
        }
        tmpl = await _attachKnowledgeToTemplate(boardName, tmpl);
        tmpl = await _attachWorkingModeToTemplate(boardName, tmpl);
        tmpl.name = templateName;

        // Save to user_settings via the settings API: into the list the server has now (#4423), so a template another
        // tab saved meanwhile is kept.
        try {
            const { raw } = await updateSettingList("saved_team_templates", list => upsertByName(list, [tmpl]),
                { local: parseList(state.settings.saved_team_templates, []) });
            state.settings.saved_team_templates = raw;
        } catch {
            showToast("Could not save the template", "error");
            return;
        }
        showToast(`Saved template "${templateName}" (${tmpl.agents.length} agents)`);
    });
}

export function toggleTeamSleep(boardName, action) {
    const doIt = async () => {
        try {
            const resp = await teamFetch(boardName, n => `/api/sessions/live/team/${encodeURIComponent(n)}/${action}`, { method: 'POST' });
            const data = await resp.json();
            const sleeping = !!data.sleeping;
            for (const s of state.liveSessions) {
                // Team wake leaves archived agents asleep (#3030).
                if (inTeam(s, boardName) && !s.archived) s.sleeping = sleeping;
            }
            redrawLiveList();
            showToast(sleeping ? `Team "${keyLabel(boardName)}" is now sleeping` : `Team "${keyLabel(boardName)}" is awake`);
        } catch (e) {
            showToast('Failed to toggle team sleep', true);
        }
    };
    if (action === 'sleep') {
        showConfirmModal('Sleep Team', `Put all agents on "${keyLabel(boardName)}" to sleep?`, doIt);
    } else {
        doIt();
    }
}
