import { escapeHtml } from './utils.js';

export function showTeamAvailability(team) {
    document.getElementById('team-availability-dialog')?.close();
    document.getElementById('team-availability-dialog')?.remove();
    const dialog = document.createElement('dialog');
    dialog.id = 'team-availability-dialog';
    dialog.className = 'team-availability-dialog';
    dialog.setAttribute('aria-labelledby', 'availability-title');
    dialog.innerHTML = `<header><div><h2 id="availability-title">Agent availability</h2><p>${escapeHtml(team)}</p></div><button type="button" aria-label="Close availability">×</button></header>
        <div class="availability-toolbar"><span>Observed activity and assigned work</span><button type="button">Refresh</button></div>
        <div class="availability-content" aria-live="polite"></div>`;
    document.body.append(dialog);
    const close = dialog.querySelector('header button');
    const refresh = dialog.querySelector('.availability-toolbar button');
    const content = dialog.querySelector('.availability-content');
    close.onclick = () => dialog.close();
    dialog.addEventListener('close', () => dialog.remove());
    dialog.addEventListener('click', event => { if (event.target === dialog) dialog.close(); });
    async function load() {
        refresh.disabled = true;
        content.textContent = 'Checking agents…';
        try {
            const response = await fetch(`/api/board/${encodeURIComponent(team)}/status`, {cache: 'no-store'});
            if (!response.ok) throw new Error(`Availability could not be loaded (${response.status}).`);
            const data = await response.json();
            if (!dialog.isConnected) return;
            const labels = {available:'Free',busy:'Busy',queued:'Queued',needs_input:'Needs input',sleeping:'Sleeping',offline:'Offline',unknown:'Unknown',unavailable:'Unavailable'};
            const tasks = items => items.map(t => `<li><span>${escapeHtml(t.scope)} #${Number(t.id)} · ${escapeHtml(t.status.replaceAll('_',' '))}</span>${escapeHtml(t.title)}</li>`).join('');
            content.innerHTML = `<div class="availability-summary">${Object.entries(labels).filter(([key]) => data.summary[key] || key === 'available').map(([key,label]) => `<div><strong>${Number(data.summary[key] || 0)}</strong><span>${label}</span></div>`).join('')}</div>
                <p class="availability-note">Free means idle with no active or ready assigned tasks. Blocked work is shown below. This snapshot does not reserve an agent.</p>
                <div class="availability-agents">${data.agents.map(a => `<article class="availability-agent"><div class="availability-agent-heading"><div><strong>${escapeHtml(a.name)}</strong><small>${escapeHtml([a.agent_type,a.role].filter(Boolean).join(' · '))}</small></div><span class="availability-badge ${a.available ? 'is-free' : ''}">${escapeHtml(labels[a.availability] || 'Unknown')}</span></div><p>${escapeHtml(a.reason)}</p>${a.tasks.length ? `<ul>${tasks(a.tasks)}</ul>` : '<small>No open assigned tasks</small>'}</article>`).join('') || '<p>No agents found for this team.</p>'}</div>
                ${data.unassigned_tasks.length ? `<section class="availability-unassigned"><h3>Unassigned work</h3><ul>${tasks(data.unassigned_tasks)}</ul></section>` : ''}
                <p class="availability-note">Updated ${escapeHtml(new Date(data.observed_at).toLocaleTimeString())}</p>`;
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
