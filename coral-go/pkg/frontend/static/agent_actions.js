/* What the sidebar's menus do to one agent: kill, info, attach, restart, sleep and archive (moved out of render.js, #4996; no behaviour change). */
import { state } from './state.js';
import { worktreeConfirmWords } from './worktree_words.js';
import { showWorktreeNotice } from './worktree_notice.js';
import { serverFetch, serverForSession, sessionServer } from './server_base.js';
import { killLiveSession, dropKilledRows } from './kill_live.js';
import { showToast, showView } from './utils.js';
import { redrawLiveList } from './list_hooks.js';
import { showConfirmModal } from './confirm_dialog.js';
import { importOnce } from './import_once.js';
import { applySleepingView } from './sleeping_view.js';

// What Kill does with the agent's terminal log, said in every Kill confirmation (#4399): it is kept, on both terminal backends
// (#4437: tmux's KillSession no longer removes it), in <data dir>/logs.
export const KILL_LOG_NOTE = "Its terminal log is kept, in the logs folder of Coral's data folder.";

// The same for a confirmation about several agents (Kill All Agents, Kill Team, Reset Team: Reset kills each agent and
// starts it again under a new session id, so the old logs stay as they are).
export const KILL_LOG_NOTE_MANY = "Their terminal logs are kept, in the logs folder of Coral's data folder.";

// killCount is "the 1 agent" or "all 3 agents", and its log note to match.
export function killCount(n) {
    return n === 1 ? { agents: 'the 1 agent', note: KILL_LOG_NOTE } : { agents: `all ${n} agents`, note: KILL_LOG_NOTE_MANY };
}

export function killSessionDirect(name, agentType, sessionId) {
    const server = serverForSession(name, sessionId);
    const row = (state.liveSessions || []).find(s => s.session_id === sessionId);
    showConfirmModal('Kill Session', `Kill session "${name}"? This will terminate the agent. ${row && row.git_state && row.git_state.is_worktree ? worktreeConfirmWords(row.branch) + ' ' : ''}${KILL_LOG_NOTE}`, async () => { try {
        const result = await killLiveSession({ name, agent_type: agentType, session_id: sessionId }, server);
        if (!result) throw new Error('the kill answer was not JSON');
        if (result.error) { showToast(result.error, true); return; }
        if (result.changes_artifact_error) {
            showToast(`Killed, but changes.diff could not be saved: ${result.changes_artifact_error}`, true);
        } else {
            showToast(`Killed and saved changes.diff: ${name}`);
        }
        showWorktreeNotice(result.worktree, 'agent'); // what became of its worktree, if it had one (#6707); a kept one stays on screen with its path (#6818)
        // The row goes at once: the agent's chat stays in History. A killed agent is not listed again.
        dropKilledRows([{ session_id: sessionId, server }]);
        if (state.currentSession && state.currentSession.session_id === sessionId) {
            state.currentSession = null;
            showView("welcome-screen");
        }
    } catch (e) {
        showToast("Failed to kill session", true);
    } });
}

export async function showInfoDirect(name, agentType, sessionId) {
    const { selectLiveSession } = await importOnce('./sessions.js');
    await selectLiveSession(name, agentType, sessionId, serverForSession(name, sessionId));
    const { showInfoModal } = await importOnce('./modals.js');
    showInfoModal();
}

export async function attachDirect(name, agentType, sessionId) {
    try {
        const resp = await serverFetch(serverForSession(name, sessionId), `/api/sessions/live/${encodeURIComponent(name)}/attach`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ agent_type: agentType, session_id: sessionId }),
        });
        const result = await resp.json();
        if (result.error) showToast(result.error, true);
        else showToast("Terminal opened");
    } catch (e) {
        showToast("Failed to open terminal", true);
    }
}

export async function restartDirect(name, agentType, sessionId) {
    const { selectLiveSession } = await importOnce('./sessions.js');
    await selectLiveSession(name, agentType, sessionId, serverForSession(name, sessionId));
    const { restartSession } = await importOnce('./controls.js');
    restartSession();
}

export function toggleAgentSleep(name, agentType, sessionId, action) {
    const server = serverForSession(name, sessionId);
    // Resolves true when the server applied the action.
    const doIt = async () => {
        try {
            const resp = await serverFetch(server, `/api/sessions/live/${encodeURIComponent(sessionId)}/${action}`, { method: 'POST' });
            const data = await resp.json();
            if (!data.ok) { showToast(data.error || 'Failed to toggle sleep', true); return false; }
            const sleeping = !!data.sleeping;
            const s = state.liveSessions.find(s => s.session_id === sessionId && sessionServer(s) === server);
            if (s) {
                s.sleeping = sleeping;
                if (!sleeping) s.archived = false; // waking unarchives (#3030)
            }
            redrawLiveList();
            showToast(sleeping ? `"${name}" is now sleeping` : `"${name}" is awake`);
            return true;
        } catch (e) {
            showToast('Failed to toggle agent sleep', true);
            return false;
        }
    };
    if (action === 'sleep') {
        showConfirmModal('Sleep Agent', `Put "${name}" to sleep?`, doIt);
        return Promise.resolve(false);
    }
    return doIt();
}

/**
 * Archive or unarchive one agent (#3030). Archiving stops a running agent and
 * keeps it out of team wake, Wake All and the main list; unarchiving leaves it
 * sleeping. Resolves true when the server applied it, false on failure or
 * when the archive confirmation is cancelled.
 */
export function toggleAgentArchive(name, agentType, sessionId, action) {
    const server = serverForSession(name, sessionId);
    const doIt = async () => {
        try {
            const resp = await serverFetch(server, `/api/sessions/live/${encodeURIComponent(sessionId)}/${action}`, { method: 'POST' });
            const data = await resp.json();
            if (!resp.ok || !data.ok) { showToast(data.error || `Failed to ${action} agent`, true); return false; }
            const s = state.liveSessions.find(s => s.session_id === sessionId && sessionServer(s) === server);
            if (s) { s.archived = !!data.archived; s.sleeping = !!data.sleeping; }
            redrawLiveList();
            // The agent on screen follows now, not on the next feed tick.
            const cur = state.currentSession;
            if (cur && cur.type === 'live' && cur.session_id === sessionId && sessionServer(cur) === server) {
                if (!!cur.sleeping !== !!data.sleeping) {
                    window.selectLiveSession(cur.name, cur.agent_type, cur.session_id, server);
                } else if (data.sleeping) {
                    applySleepingView(true, { archived: !!data.archived, manageInput: !document.body.classList.contains('popout-mode') });
                }
            }
            showToast(data.archived ? `"${name}" is archived` : `"${name}" is unarchived (sleeping)`);
            return true;
        } catch (e) {
            showToast(`Failed to ${action} agent`, true);
            return false;
        }
    };
    if (action === 'archive') {
        const s = state.liveSessions.find(s => s.session_id === sessionId && sessionServer(s) === server);
        const stops = s && !s.sleeping ? ' It will be stopped.' : '';
        return new Promise(resolve => showConfirmModal('Archive Agent',
            `Archive "${name}"?${stops} Archived agents are not woken with their team or by Wake All.`,
            async () => resolve(await doIt()), () => resolve(false)));
    }
    return doIt();
}
