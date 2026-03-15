/* Message Board: project list, messages, subscribers, posting */

import { escapeHtml } from './utils.js';

let currentProject = null;
let pollTimer = null;

// ── API helpers ──────────────────────────────────────────────────────────

async function fetchProjects() {
    const resp = await fetch('/api/board/projects');
    return await resp.json();
}

async function fetchMessages(project) {
    const resp = await fetch(`/api/board/${encodeURIComponent(project)}/messages/all?limit=200`);
    const data = await resp.json();
    return Array.isArray(data) ? data : [];
}

async function fetchSubscribers(project) {
    const resp = await fetch(`/api/board/${encodeURIComponent(project)}/subscribers`);
    return await resp.json();
}

// ── Sidebar rendering ────────────────────────────────────────────────────

async function loadBoardProjects() {
    try {
        const projects = await fetchProjects();
        renderBoardSidebar(projects);
        const badge = document.getElementById('messageboard-count');
        if (badge) badge.textContent = String(projects.length);
    } catch (e) {
        console.error('Failed to load board projects:', e);
    }
}

function renderBoardSidebar(projects) {
    const list = document.getElementById('messageboard-sidebar-list');
    if (!list) return;
    if (!projects.length) {
        list.innerHTML = '<li class="empty-state">No projects</li>';
        return;
    }
    list.innerHTML = projects.map(p => {
        const active = currentProject === p.project ? 'active' : '';
        return `<li class="session-list-item ${active}" onclick="selectBoardProject('${escapeHtml(p.project)}')">
            <span class="session-name">${escapeHtml(p.project)}</span>
            <span style="font-size:10px;color:var(--text-muted);margin-left:auto">${p.message_count} msgs</span>
        </li>`;
    }).join('');
}

// ── View switching ───────────────────────────────────────────────────────

export function selectBoardProject(project) {
    currentProject = project;

    // Hide other views, show messageboard
    document.getElementById('welcome-screen').style.display = 'none';
    document.getElementById('live-session-view').style.display = 'none';
    document.getElementById('history-session-view').style.display = 'none';
    document.getElementById('scheduler-view').style.display = 'none';
    document.getElementById('messageboard-view').style.display = 'flex';

    // Show board panel
    document.getElementById('mb-project-list').style.display = 'none';
    const board = document.getElementById('mb-board');
    board.style.display = 'flex';
    document.getElementById('mb-subscribers-panel').style.display = 'block';
    document.getElementById('mb-back-btn').style.display = '';
    document.getElementById('mb-delete-btn').style.display = '';

    const badge = document.getElementById('messageboard-project-badge');
    badge.textContent = project;
    badge.style.display = '';
    document.getElementById('messageboard-title').textContent = 'Message Board';

    // Ensure dashboard is subscribed as a reader
    subscribeDashboard(project);

    loadBoardMessages(project);
    loadBoardSubscribers(project);
    loadBoardProjects();
    startBoardPoll();
}

export function showMessageBoardProjects() {
    currentProject = null;
    stopBoardPoll();

    document.getElementById('welcome-screen').style.display = 'none';
    document.getElementById('live-session-view').style.display = 'none';
    document.getElementById('history-session-view').style.display = 'none';
    document.getElementById('scheduler-view').style.display = 'none';
    document.getElementById('messageboard-view').style.display = 'flex';

    document.getElementById('mb-project-list').style.display = '';
    document.getElementById('mb-board').style.display = 'none';
    document.getElementById('mb-subscribers-panel').style.display = 'none';
    document.getElementById('mb-back-btn').style.display = 'none';
    document.getElementById('mb-delete-btn').style.display = 'none';
    document.getElementById('messageboard-project-badge').style.display = 'none';

    loadBoardProjectList();
    loadBoardProjects();
}

// ── Project list view ────────────────────────────────────────────────────

async function loadBoardProjectList() {
    try {
        const projects = await fetchProjects();
        const ul = document.getElementById('mb-projects-ul');
        const empty = document.getElementById('mb-projects-empty');
        if (!projects.length) {
            ul.innerHTML = '';
            empty.style.display = '';
            return;
        }
        empty.style.display = 'none';
        ul.innerHTML = projects.map(p => `
            <li class="session-list-item" onclick="selectBoardProject('${escapeHtml(p.project)}')"
                style="display:flex;justify-content:space-between;align-items:center;padding:12px 16px">
                <div>
                    <strong>${escapeHtml(p.project)}</strong>
                    <div style="font-size:11px;color:var(--text-muted)">${p.subscriber_count} subscriber${p.subscriber_count !== 1 ? 's' : ''}</div>
                </div>
                <span style="font-size:12px;color:var(--text-muted)">${p.message_count} messages</span>
            </li>
        `).join('');
    } catch (e) {
        console.error('Failed to load board project list:', e);
    }
}

// ── Messages ─────────────────────────────────────────────────────────────

async function loadBoardMessages(project) {
    try {
        const messages = await fetchMessages(project);
        renderMessages(messages);
    } catch (e) {
        console.error('Failed to load board messages:', e);
    }
}

function renderMessages(messages) {
    const container = document.getElementById('mb-messages');
    if (!messages.length) {
        container.innerHTML = '<div class="empty-state">No messages yet</div>';
        return;
    }
    container.innerHTML = messages.map(m => `
        <div style="padding:8px 0;border-bottom:1px solid var(--border)">
            <div style="display:flex;justify-content:space-between;align-items:baseline;margin-bottom:4px">
                <span style="font-weight:600;font-size:13px;color:var(--text-primary)">${escapeHtml(m.job_title || 'Unknown')}</span>
                <span style="font-size:10px;color:var(--text-muted)">${escapeHtml(m.session_id)}</span>
            </div>
            <div style="font-size:13px;color:var(--text-primary);white-space:pre-wrap">${escapeHtml(m.content)}</div>
            <div style="font-size:10px;color:var(--text-muted);margin-top:4px">${formatTime(m.created_at)}</div>
        </div>
    `).join('');
    container.scrollTop = container.scrollHeight;
}

function formatTime(iso) {
    try {
        const d = new Date(iso);
        return d.toLocaleString();
    } catch {
        return iso;
    }
}

// ── Subscribers ──────────────────────────────────────────────────────────

async function loadBoardSubscribers(project) {
    try {
        const subs = await fetchSubscribers(project);
        const list = document.getElementById('mb-subscribers-list');
        if (!subs.length) {
            list.innerHTML = '<li style="font-size:12px;color:var(--text-muted)">No subscribers</li>';
            return;
        }
        list.innerHTML = subs.map(s => `
            <li style="padding:6px 0;border-bottom:1px solid var(--border)">
                <div style="font-weight:600;font-size:12px">${escapeHtml(s.job_title)}</div>
                <div style="font-size:10px;color:var(--text-muted)">${escapeHtml(s.session_id)}</div>
            </li>
        `).join('');
    } catch (e) {
        console.error('Failed to load subscribers:', e);
    }
}

// ── Dashboard subscription ───────────────────────────────────────────────

async function subscribeDashboard(project) {
    try {
        await fetch(`/api/board/${encodeURIComponent(project)}/subscribe`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ session_id: 'dashboard', job_title: 'Developer (Dashboard)' }),
        });
    } catch (e) {
        // Best effort
    }
}

// ── Posting ──────────────────────────────────────────────────────────────

export async function postBoardMessage() {
    if (!currentProject) return;
    const input = document.getElementById('mb-post-input');
    const content = input.value.trim();
    if (!content) return;

    try {
        const resp = await fetch(`/api/board/${encodeURIComponent(currentProject)}/messages`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ session_id: 'dashboard', content }),
        });
        if (!resp.ok) {
            console.error('Post failed:', resp.status, await resp.text());
            return;
        }
        input.value = '';
        // Re-fetch and render all messages so the new one appears immediately
        await loadBoardMessages(currentProject);
    } catch (e) {
        console.error('Failed to post message:', e);
    }
}

// ── Delete project ───────────────────────────────────────────────────────

export async function deleteMessageBoardProject() {
    if (!currentProject) return;
    if (!confirm(`Delete project "${currentProject}" and all its messages?`)) return;

    try {
        await fetch(`/api/board/${encodeURIComponent(currentProject)}`, { method: 'DELETE' });
        showMessageBoardProjects();
    } catch (e) {
        console.error('Failed to delete project:', e);
    }
}

// ── Polling ──────────────────────────────────────────────────────────────

function startBoardPoll() {
    stopBoardPoll();
    pollTimer = setInterval(() => {
        if (currentProject) {
            loadBoardMessages(currentProject);
            loadBoardSubscribers(currentProject);
        }
    }, 5000);
}

function stopBoardPoll() {
    if (pollTimer) {
        clearInterval(pollTimer);
        pollTimer = null;
    }
}

// ── Init ─────────────────────────────────────────────────────────────────

export function initMessageBoard() {
    loadBoardProjects();
}

export { loadBoardProjects, showMessageBoardProjects as showBoardProjects };
