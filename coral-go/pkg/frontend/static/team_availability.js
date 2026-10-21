import { importOnce } from './import_once.js';
import { bindNativeDialog } from './modal_chrome.js';
import { escapeHtml } from './utils.js';
import { state } from './state.js';
import { boardFetch, inTeam, keyLabel, sessionTeamKey } from './server_base.js';
import { platform } from './platform/detect.js';

const availabilityCache = new Map();
// Polling state per team: when the next background refresh is allowed and the
// request in flight, if any. Every window polls on each sidebar render, so one
// slow /status must not stack identical requests behind it.
const availabilityNextAt = new Map();
const availabilityInFlight = new Map();
// Request order per team: a response only replaces the cache when it was
// started after the one that last wrote it, so a slow background poll cannot
// overwrite a newer forced refresh (e.g. right after removing a reminder).
let availabilitySeq = 0;
const availabilityWrittenSeq = new Map();
const AVAILABILITY_MIN_INTERVAL_MS = 5000;

export function getCachedAgentAvailability(session) {
    if (!session) return null;
    // The session id first, on any board: an agent's name repeats across boards (Lead Developer on
    // several teams), so a name match could show another team's state (#3473). Live-session refreshes
    // can briefly omit board_project, which the global id match covers.
    const byId = agents => session.session_id ? agents?.find(a => a.session_id === session.session_id) : undefined;
    const byName = agents => agents?.find(a => a.name === session.name || a.subscriber_id === session.name);
    const snapshot = availabilityCache.get(sessionTeamKey(session));
    const direct = byId(snapshot?.agents);
    if (direct) return direct;
    for (const cached of availabilityCache.values()) {
        const found = byId(cached.agents);
        if (found) return found;
    }
    // No row has this session id: its own team's row by name, else (no team known for it) any board's.
    if (snapshot) return byName(snapshot.agents) || null;
    for (const cached of availabilityCache.values()) {
        const found = byName(cached.agents);
        if (found) return found;
    }
    return null;
}

function fetchTeamAvailability(team) {
    const started = Date.now();
    const seq = ++availabilitySeq;
    const request = (async () => {
        try {
            const response = await boardFetch(team, `/status`, { cache: 'no-store' });
            if (!response.ok) throw new Error(`Availability could not be loaded (${response.status}).`);
            const data = await response.json();
            if (seq > (availabilityWrittenSeq.get(team) || 0)) {
                availabilityWrittenSeq.set(team, seq);
                availabilityCache.set(team, data);
            }
            return data;
        } finally {
            // Measured from completion and backed off when the server is slow,
            // so a /status slower than the interval cannot overlap itself.
            const latency = Date.now() - started;
            availabilityNextAt.set(team, Date.now() + Math.max(AVAILABILITY_MIN_INTERVAL_MS, 2 * latency));
            if (availabilityInFlight.get(team) === request) availabilityInFlight.delete(team);
        }
    })();
    availabilityInFlight.set(team, request);
    return request;
}

/** Background polls skip hidden tabs, a team with a request in flight (its
 *  starter re-renders on the result), and the backoff window; they serve the
 *  cache on error. Native webviews can report hidden permanently, so they
 *  always poll. force (the team view: open, Refresh, after a reminder change)
 *  always sends its own request, since one already in flight may predate the
 *  change the user just made, and throws on error. */
export async function refreshTeamAvailability(team, force = false) {
    if (!team) return null;
    if (!force) {
        if (!platform.isNative && document.hidden) return null;
        if (availabilityInFlight.has(team) || Date.now() < (availabilityNextAt.get(team) || 0)) return null;
    }
    try {
        return await fetchTeamAvailability(team);
    } catch (err) {
        if (force) throw err;
        return availabilityCache.get(team) || null;
    }
}

function availabilitySignature(data) {
    return JSON.stringify((data?.agents || []).map(a => [a.session_id, a.subscriber_id, a.name, a.availability, !!a.reminder, a.reminder_interval_seconds || 0]));
}

/** Background refresh for the sidebar: resolves true only when the fields the
 *  sidebar renders (availability badge, reminder) changed, so a poll with no
 *  change does not trigger another full re-render. */
export async function refreshTeamAvailabilityIfChanged(team) {
    const before = availabilitySignature(availabilityCache.get(team));
    const snapshot = await refreshTeamAvailability(team);
    return !!snapshot && availabilitySignature(snapshot) !== before;
}

export function showTeamAvailability(team, options = {}) {
    const workspaceMode = Boolean(options.workspace);
    document.getElementById('team-availability-dialog')?.close();
    document.getElementById('team-availability-dialog')?.remove();
    document.getElementById('team-availability-workspace')?.remove();
    const dialog = document.createElement(workspaceMode ? 'section' : 'dialog');
    dialog.id = workspaceMode ? 'team-availability-workspace' : 'team-availability-dialog';
    dialog.className = workspaceMode ? 'team-settings-workspace team-view-workspace' : 'team-availability-dialog';
    dialog.setAttribute('aria-labelledby', 'availability-title');
    const actions = workspaceMode
        ? ''  // sidebar panel: the team context owns navigation
        : '<button type="button" data-dialog-close aria-label="Close team view">×</button>';
    dialog.innerHTML = `<header><div><h2 id="availability-title">Team view</h2><p>${escapeHtml(keyLabel(team))}</p></div>${actions}</header>
        <div class="availability-toolbar"><button type="button" class="btn btn-small">Refresh</button></div>
        <div class="availability-content" aria-live="polite"></div>`;
    if (workspaceMode) {
        // Rendered into the sidebar's Team View tab (see team_context.js).
        const host = document.getElementById('agentic-panel-team-view');
        if (!host) return;
        host.replaceChildren(dialog);
    } else document.body.append(dialog);
    const refresh = dialog.querySelector('.availability-toolbar button');
    const content = dialog.querySelector('.availability-content');
    if (!workspaceMode) bindNativeDialog(dialog);
    async function load() {
        const hadFocus = document.activeElement === refresh;   // a disabled button loses the keyboard focus: give it back afterwards
        refresh.disabled = true;
        content.textContent = 'Checking agents…';
        try {
            const data = await refreshTeamAvailability(team, true);
            if (!data) throw new Error('Availability could not be loaded.');
            if (!dialog.isConnected) return;
            const labels = {available:'Free',busy:'Busy',task_idle:'Task assigned · idle',queued:'Queued',waiting:'Waiting',needs_input:'Needs input',sleeping:'Sleeping',offline:'Offline',unknown:'No signal yet',unavailable:'Unavailable'};
            const tasks = items => items.map(t => `<li><span>${escapeHtml(t.scope)} #${Number(t.id)} · ${escapeHtml(t.status.replaceAll('_',' '))}</span>${escapeHtml(t.title)}</li>`).join('');
            const teamAgents = (state.liveSessions || []).filter(s => inTeam(s, team));
            const directory = teamAgents.find(s => s.working_directory)?.working_directory || '';
            const branchSession = teamAgents.find(s => s.branch);
            const branch = branchSession?.branch || '';
            content.innerHTML = `<section class="team-view-details"><div><span>Agents</span><strong>${teamAgents.length || data.agents.length}</strong></div><div><span>Directory</span><strong title="${escapeHtml(directory)}">${escapeHtml(directory || '—')}</strong></div><div><span>Branch</span><strong>${escapeHtml(branch || '—')}</strong></div></section>
                <div class="availability-summary">${Object.entries(labels).filter(([key]) => data.summary[key] || key === 'available').map(([key,label]) => `<div class="availability-summary-${key}"><strong>${Number(data.summary[key] || 0)}</strong><span>${label}</span></div>`).join('')}</div>
                <p class="availability-note">Free means idle, with no active or ready task. A snapshot: it does not reserve an agent.${data.agents.length && !data.agents.some(a => a.tasks.length) ? ' No open assigned tasks.' : ''}</p>
                <div class="availability-agents">${data.agents.map(a => { const every = a.reminder_interval_seconds ? ` every ${Math.round(a.reminder_interval_seconds / 60)} min` : ''; const title = a.reminder ? `Periodic reminder${every}` : ''; return `<article class="availability-agent availability-${escapeHtml(a.availability)}"><div class="availability-agent-heading"><div><strong>${escapeHtml(a.name)}</strong>${a.reminder ? `<span class="agent-reminder-icon" title="${escapeHtml(title)}" aria-label="${escapeHtml(title)}">⏰</span>` : ''}<small>${escapeHtml([a.agent_type, a.role && a.role.trim().toLowerCase() !== String(a.name || '').trim().toLowerCase() ? a.role : ''].filter(Boolean).join(' · '))}</small></div><span class="availability-badge ${a.available ? 'is-free' : ''}">${escapeHtml(labels[a.availability] || 'Unknown')}</span></div><p>${escapeHtml(a.reason)}</p>${a.tasks.length ? `<ul>${tasks(a.tasks)}</ul>` : ''}</article>`; }).join('') || '<p>No agents found for this team.</p>'}</div>
                ${data.unassigned_tasks.length ? `<section class="availability-unassigned"><h3>Unassigned work</h3><ul>${tasks(data.unassigned_tasks)}</ul></section>` : ''}
                <section class="availability-health"><h3>Board health</h3><ul>${(data.health_report || []).map(item => `<li>${escapeHtml(item)}</li>`).join('')}</ul></section>
                <p class="availability-note">Updated ${escapeHtml(new Date(data.observed_at).toLocaleTimeString())}</p>`;
            content.querySelectorAll('.availability-agent').forEach((card, index) => {
                const agent = data.agents[index];
                if (!agent) return;
                if (agent.agent_type === 'terminal' && !agent.reminder) return; // a terminal is not an agent: a reminder to it does nothing (UI audit #5589)
                const button = document.createElement('button');
                button.type = 'button'; button.className = 'btn'; button.textContent = agent.reminder ? 'Edit reminder' : 'Remind agent';
                button.onclick = () => remindAgent(team, agent.subscriber_id || agent.name, agent);
                card.append(button);
                if (agent.reminder) {
                    const stop = document.createElement('button');
                    stop.type = 'button'; stop.className = 'btn availability-stop-reminder'; stop.textContent = 'Remove reminder';
                    stop.onclick = () => stopAgentReminder(team, agent.subscriber_id || agent.name, load);
                    card.append(stop);
                }
            });
        } catch (error) {
            content.textContent = `${error.message} Use Refresh to try again.`;
        } finally {
            refresh.disabled = false;
            if (hadFocus && dialog.isConnected) refresh.focus();
        }
    }
    refresh.onclick = load;
    if (!workspaceMode) dialog.showModal();
    load();
}

/** Open the team: Team view in the center and the team tabs in the sidebar. */
export function showTeamAvailabilityWorkspace(team, options = {}) {
    return importOnce('./team_context.js').then(m => m.enterTeamContext(team, { restore: options.restore }));
}

const remindAgent = async (team, subscriber, existing = null) => {
    const message = window.prompt('Reminder instruction for this agent:');
    if (!message?.trim()) return;
    const defaultMinutes = existing?.reminder_interval_seconds ? Math.round(existing.reminder_interval_seconds / 60) : 5;
    const minutes = Number(window.prompt('Send it every how many minutes?', String(defaultMinutes)));
    if (!Number.isFinite(minutes) || minutes * 60 < 30 || minutes * 60 > 86400) return;
    const response = await boardFetch(team, `/reminder`, { method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({subscriber_id:subscriber,message:message.trim(),interval_seconds:Math.round(minutes*60)}) });
    const data = await response.json().catch(() => ({}));
    window.showToast?.(response.ok ? 'Agent reminder started' : (data.error || 'Could not start reminder'), !response.ok);
};

const stopAgentReminder = async (team, subscriber, onChanged = null) => {
    try {
        const response = await boardFetch(team, `/reminder`, { method:'DELETE', headers:{'Content-Type':'application/json'}, body:JSON.stringify({subscriber_id:subscriber}) });
        const data = await response.json().catch(() => ({}));
        if (!response.ok) {
            window.showToast?.(data.error || 'Could not remove reminder', true);
            return false;
        }
        window.showToast?.('Agent reminder removed');
        // The status endpoint is the source of truth for the icon and action
        // buttons. Refresh the open view immediately so removal is visible
        // without requiring the operator to close and reopen Team view.
        if (typeof onChanged === 'function') await onChanged();
        return true;
    } catch (error) {
        window.showToast?.(error?.message || 'Could not remove reminder', true);
        return false;
    }
};
