import { escapeHtml } from './utils.js';

const availabilityCache = new Map();
const availabilityFetchedAt = new Map();

export function getCachedAgentAvailability(session) {
    if (!session) return null;
    const matches = agents => agents?.find(a => a.session_id === session.session_id || a.name === session.name || a.subscriber_id === session.name);
    const snapshot = availabilityCache.get(session.board_project);
    const direct = matches(snapshot?.agents);
    if (direct) return direct;
    // Live-session refreshes can briefly omit board_project. Preserve status
    // indicators by matching the stable session/subscriber identity globally.
    for (const cached of availabilityCache.values()) {
        const found = matches(cached.agents);
        if (found) return found;
    }
    return null;
}

export async function refreshTeamAvailability(team, force = false) {
    if (!team) return null;
    const now = Date.now();
    if (!force && now - (availabilityFetchedAt.get(team) || 0) < 5000) return null;
    availabilityFetchedAt.set(team, now);
    try {
        const response = await fetch(`/api/board/${encodeURIComponent(team)}/status`, { cache: 'no-store' });
        if (!response.ok) throw new Error(`Availability could not be loaded (${response.status}).`);
        const data = await response.json();
        availabilityCache.set(team, data);
        return data;
    } catch {
        return availabilityCache.get(team) || null;
    }
}

export function showTeamAvailability(team) {
    document.getElementById('team-availability-dialog')?.close();
    document.getElementById('team-availability-dialog')?.remove();
    const dialog = document.createElement('dialog');
    dialog.id = 'team-availability-dialog';
    dialog.className = 'team-availability-dialog';
    dialog.setAttribute('aria-labelledby', 'availability-title');
    dialog.innerHTML = `<header><div><h2 id="availability-title">Agent availability</h2><p>${escapeHtml(team)}</p></div><button type="button" aria-label="Close availability">×</button></header>
        <div class="availability-toolbar"><span>Observed activity and assigned work</span><label class="availability-health-toggle"><input type="checkbox"> Board health monitor</label><button type="button">Refresh</button></div>
        <div class="availability-content" aria-live="polite"></div>`;
    document.body.append(dialog);
    const close = dialog.querySelector('header button');
    const refresh = dialog.querySelector('.availability-toolbar button');
    const healthToggle = dialog.querySelector('.availability-health-toggle input');
    const content = dialog.querySelector('.availability-content');
    close.onclick = () => dialog.close();
    dialog.addEventListener('close', () => dialog.remove());
    dialog.addEventListener('click', event => { if (event.target === dialog) dialog.close(); });
    fetch('/api/settings').then(r => r.ok ? r.json() : null).then(data => {
        if (data) healthToggle.checked = data.settings?.board_health_monitor === 'true';
    }).catch(() => {});
    healthToggle.onchange = async () => {
        healthToggle.disabled = true;
        try {
            const response = await fetch('/api/settings', { method:'PUT', headers:{'Content-Type':'application/json'}, body:JSON.stringify({board_health_monitor: healthToggle.checked ? 'true' : 'false'}) });
            if (!response.ok) throw new Error('Could not save monitor setting');
            window.showToast?.(`Board health monitor ${healthToggle.checked ? 'enabled' : 'disabled'}; restart Coral to apply`);
        } catch (error) {
            healthToggle.checked = !healthToggle.checked;
            window.showToast?.(error.message, true);
        } finally { healthToggle.disabled = false; }
    };
    async function load() {
        refresh.disabled = true;
        content.textContent = 'Checking agents…';
        try {
            const data = await refreshTeamAvailability(team, true);
            if (!data) throw new Error('Availability could not be loaded.');
            if (!dialog.isConnected) return;
            const labels = {available:'Free',busy:'Busy',task_idle:'Task assigned · idle',queued:'Queued',waiting:'Waiting',needs_input:'Needs input',sleeping:'Sleeping',offline:'Offline',unknown:'Unknown',unavailable:'Unavailable'};
            const tasks = items => items.map(t => `<li><span>${escapeHtml(t.scope)} #${Number(t.id)} · ${escapeHtml(t.status.replaceAll('_',' '))}</span>${escapeHtml(t.title)}</li>`).join('');
            content.innerHTML = `<div class="availability-summary">${Object.entries(labels).filter(([key]) => data.summary[key] || key === 'available').map(([key,label]) => `<div class="availability-summary-${key}"><strong>${Number(data.summary[key] || 0)}</strong><span>${label}</span></div>`).join('')}</div>
                <p class="availability-note">Free means idle with no active or ready assigned tasks. Blocked work is shown below. This snapshot does not reserve an agent.</p>
                <div class="availability-agents">${data.agents.map(a => { const every = a.reminder_interval_seconds ? ` every ${Math.round(a.reminder_interval_seconds / 60)} min` : ''; const title = a.reminder ? `Periodic reminder${every}` : ''; return `<article class="availability-agent availability-${escapeHtml(a.availability)}"><div class="availability-agent-heading"><div><strong>${escapeHtml(a.name)}</strong>${a.reminder ? `<span class="agent-reminder-icon" title="${escapeHtml(title)}" aria-label="${escapeHtml(title)}">⏰</span>` : ''}<small>${escapeHtml([a.agent_type,a.role].filter(Boolean).join(' · '))}</small></div><span class="availability-badge ${a.available ? 'is-free' : ''}">${escapeHtml(labels[a.availability] || 'Unknown')}</span></div><p>${escapeHtml(a.reason)}</p>${a.tasks.length ? `<ul>${tasks(a.tasks)}</ul>` : '<small>No open assigned tasks</small>'}</article>`; }).join('') || '<p>No agents found for this team.</p>'}</div>
                ${data.unassigned_tasks.length ? `<section class="availability-unassigned"><h3>Unassigned work</h3><ul>${tasks(data.unassigned_tasks)}</ul></section>` : ''}
                <section class="availability-health"><h3>Board health</h3><ul>${(data.health_report || []).map(item => `<li>${escapeHtml(item)}</li>`).join('')}</ul></section>
                <p class="availability-note">Updated ${escapeHtml(new Date(data.observed_at).toLocaleTimeString())}</p>`;
            content.querySelectorAll('.availability-agent').forEach((card, index) => {
                const agent = data.agents[index];
                if (!agent) return;
                const button = document.createElement('button');
                button.type = 'button'; button.className = 'btn'; button.textContent = agent.reminder ? 'Edit reminder' : 'Remind agent';
                button.onclick = () => window.remindAgent(team, agent.subscriber_id || agent.name, agent);
                card.append(button);
                if (agent.reminder) {
                    const stop = document.createElement('button');
                    stop.type = 'button'; stop.className = 'btn availability-stop-reminder'; stop.textContent = 'Remove reminder';
                    stop.onclick = () => window.stopAgentReminder(team, agent.subscriber_id || agent.name);
                    card.append(stop);
                }
            });
        } catch (error) {
            content.textContent = `${error.message} Use Refresh to try again.`;
        } finally {
            refresh.disabled = false;
        }
    }
    refresh.onclick = load;
    dialog.showModal();
    load();
}

window.remindAgent = async (team, subscriber, existing = null) => {
    const message = window.prompt('Reminder instruction for this agent:');
    if (!message?.trim()) return;
    const defaultMinutes = existing?.reminder_interval_seconds ? Math.round(existing.reminder_interval_seconds / 60) : 5;
    const minutes = Number(window.prompt('Send it every how many minutes?', String(defaultMinutes)));
    if (!Number.isFinite(minutes) || minutes * 60 < 30 || minutes * 60 > 86400) return;
    const response = await fetch(`/api/board/${encodeURIComponent(team)}/reminder`, { method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({subscriber_id:subscriber,message:message.trim(),interval_seconds:Math.round(minutes*60)}) });
    const data = await response.json().catch(() => ({}));
    window.showToast?.(response.ok ? 'Agent reminder started' : (data.error || 'Could not start reminder'), !response.ok);
};

window.stopAgentReminder = async (team, subscriber) => {
    const response = await fetch(`/api/board/${encodeURIComponent(team)}/reminder`, { method:'DELETE', headers:{'Content-Type':'application/json'}, body:JSON.stringify({subscriber_id:subscriber}) });
    const data = await response.json().catch(() => ({}));
    window.showToast?.(response.ok ? 'Agent reminder removed' : (data.error || 'Could not remove reminder'), !response.ok);
};
