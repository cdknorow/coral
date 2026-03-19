/* Message Board: project list, messages, subscribers, posting */

import { escapeHtml, escapeAttr } from './utils.js';

let currentProject = null;
let pollTimer = null;
let isPaused = false;
const PAGE_SIZE = 50;
let _allMessages = [];
let _totalMessages = 0;
let _loadedOffset = 0;

// ── API helpers ──────────────────────────────────────────────────────────

async function fetchProjects() {
    const resp = await fetch('/api/board/projects');
    return await resp.json();
}

async function fetchMessages(project, limit = PAGE_SIZE, offset = 0) {
    const resp = await fetch(`/api/board/${encodeURIComponent(project)}/messages/all?limit=${limit}&offset=${offset}`);
    const data = await resp.json();
    // Support both old format (array) and new format ({messages, total})
    if (Array.isArray(data)) {
        return { messages: data, total: data.length, offset: 0 };
    }
    return { messages: data.messages || [], total: data.total || 0, offset: data.offset || 0 };
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
        return `<li class="session-list-item ${active}" onclick="selectBoardProject('${escapeAttr(p.project)}')">
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
    document.getElementById('mb-pause-btn').style.display = '';
    document.getElementById('mb-delete-btn').style.display = '';

    const badge = document.getElementById('messageboard-project-badge');
    badge.textContent = project;
    badge.style.display = '';
    document.getElementById('messageboard-title').textContent = 'Message Board';

    // Ensure dashboard is subscribed as a reader
    subscribeDashboard(project);

    // Load paused state
    loadPausedState(project);

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
    document.getElementById('mb-pause-btn').style.display = 'none';
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
            <li class="session-list-item" onclick="selectBoardProject('${escapeAttr(p.project)}')"
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
        // Load the latest page — calculate offset to get the last PAGE_SIZE messages
        const countResult = await fetchMessages(project, 1, 0);
        _totalMessages = countResult.total;
        const startOffset = Math.max(0, _totalMessages - PAGE_SIZE);
        const result = await fetchMessages(project, PAGE_SIZE, startOffset);
        _allMessages = result.messages;
        _loadedOffset = startOffset;
        renderMessages(_allMessages);
    } catch (e) {
        console.error('Failed to load board messages:', e);
    }
}

async function loadEarlierMessages() {
    if (!currentProject || _loadedOffset <= 0) return;
    try {
        const newOffset = Math.max(0, _loadedOffset - PAGE_SIZE);
        const fetchCount = _loadedOffset - newOffset;
        const result = await fetchMessages(currentProject, fetchCount, newOffset);
        _allMessages = [...result.messages, ..._allMessages];
        _loadedOffset = newOffset;
        renderMessages(_allMessages);
    } catch (e) {
        console.error('Failed to load earlier messages:', e);
    }
}
window.loadEarlierMessages = loadEarlierMessages;

// Agent color palette for bubble accents
const _agentColors = [
    { bg: 'rgba(88, 166, 255, 0.08)', border: 'rgba(88, 166, 255, 0.25)', name: '#58a6ff' },   // blue
    { bg: 'rgba(126, 231, 135, 0.08)', border: 'rgba(126, 231, 135, 0.25)', name: '#7ee787' },  // green
    { bg: 'rgba(210, 153, 237, 0.08)', border: 'rgba(210, 153, 237, 0.25)', name: '#d299ed' },  // purple
    { bg: 'rgba(255, 166, 87, 0.08)', border: 'rgba(255, 166, 87, 0.25)', name: '#ffa657' },    // orange
    { bg: 'rgba(255, 123, 114, 0.08)', border: 'rgba(255, 123, 114, 0.25)', name: '#ff7b72' },  // red
    { bg: 'rgba(121, 192, 255, 0.08)', border: 'rgba(121, 192, 255, 0.25)', name: '#79c0ff' },  // sky
    { bg: 'rgba(238, 190, 95, 0.08)', border: 'rgba(238, 190, 95, 0.25)', name: '#eebe5f' },    // gold
    { bg: 'rgba(150, 210, 200, 0.08)', border: 'rgba(150, 210, 200, 0.25)', name: '#96d2c8' },  // teal
];
const _agentColorMap = {};

function _getAgentColor(name) {
    if (!name) return _agentColors[0];
    if (_agentColorMap[name]) return _agentColorMap[name];
    const idx = Object.keys(_agentColorMap).length % _agentColors.length;
    _agentColorMap[name] = _agentColors[idx];
    return _agentColorMap[name];
}

function _renderMarkdown(content) {
    if (!content) return '';
    if (typeof marked !== 'undefined') {
        try {
            return marked.parse(content);
        } catch (e) {
            console.warn('marked.parse() failed, falling back to escapeHtml:', e);
        }
    }
    return escapeHtml(content);
}

function renderMessages(messages) {
    const container = document.getElementById('mb-messages');
    if (!messages.length) {
        container.innerHTML = '<div class="empty-state">No messages yet</div>';
        return;
    }
    // Save scroll position before replacing content
    const prevScrollTop = container.scrollTop;
    const wasAtBottom = (container.scrollHeight - prevScrollTop - container.clientHeight) < 50;

    // "Load Earlier" button if there are older messages
    let loadEarlierHtml = '';
    if (_loadedOffset > 0) {
        const remaining = _loadedOffset;
        loadEarlierHtml = `<div style="text-align:center;padding:8px 0 12px">
            <button class="btn btn-small" onclick="loadEarlierMessages()" style="font-size:12px;color:var(--text-muted)">
                Load ${Math.min(remaining, PAGE_SIZE)} earlier messages (${remaining} remaining)
            </button>
        </div>`;
    }

    // Message count indicator
    const countHtml = `<div style="text-align:center;font-size:10px;color:var(--text-muted);padding:4px 0 8px">
        Showing ${messages.length} of ${_totalMessages} messages
    </div>`;

    container.innerHTML = loadEarlierHtml + countHtml + messages.map(m => {
        const color = _getAgentColor(m.job_title || 'Unknown');
        return `
        <div class="mb-message" style="background:${color.bg};border:1px solid ${color.border};border-radius:10px;padding:10px 14px;margin-bottom:10px;position:relative">
            <div style="display:flex;justify-content:space-between;align-items:baseline;margin-bottom:6px">
                <span class="mb-agent-name" style="font-weight:600;font-size:13px;color:${color.name}">${escapeHtml(m.job_title || 'Unknown')}</span>
                <div style="display:flex;align-items:center;gap:8px">
                    <span style="font-size:10px;color:var(--text-muted)">${formatTime(m.created_at)}</span>
                    <button class="mb-delete-msg-btn" onclick="deleteBoardMessage(${m.id})" title="Delete message">&times;</button>
                </div>
            </div>
            <div class="mb-message-body" style="font-size:13px;color:var(--text-primary);line-height:1.5">${_renderMarkdown(m.content)}</div>
        </div>`;
    }).join('');
    if (wasAtBottom) {
        container.scrollTop = container.scrollHeight;
    } else {
        container.scrollTop = prevScrollTop;
    }
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
            body: JSON.stringify({ session_id: 'dashboard', job_title: 'Operator' }),
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
        // Fetch new messages since our last known total
        await _pollNewMessages();
    } catch (e) {
        console.error('Failed to post message:', e);
    }
}

// ── Pause / Resume reads ─────────────────────────────────────────────────

async function loadPausedState(project) {
    try {
        const resp = await fetch(`/api/board/${encodeURIComponent(project)}/paused`);
        const data = await resp.json();
        isPaused = !!data.paused;
        updatePauseButton();
    } catch (e) {
        isPaused = false;
        updatePauseButton();
    }
}

function updatePauseButton() {
    const btn = document.getElementById('mb-pause-btn');
    if (!btn) return;
    if (isPaused) {
        btn.textContent = 'Resume Reads';
        btn.classList.add('btn-warning');
    } else {
        btn.textContent = 'Pause Reads';
        btn.classList.remove('btn-warning');
    }
}

export async function toggleBoardPause() {
    if (!currentProject) return;
    const action = isPaused ? 'resume' : 'pause';
    try {
        const resp = await fetch(`/api/board/${encodeURIComponent(currentProject)}/${action}`, {
            method: 'POST',
        });
        const data = await resp.json();
        isPaused = !!data.paused;
        updatePauseButton();
    } catch (e) {
        console.error('Failed to toggle pause:', e);
    }
}

// ── Delete message ───────────────────────────────────────────────────────

export async function deleteBoardMessage(messageId) {
    if (!currentProject) return;
    try {
        const resp = await fetch(`/api/board/${encodeURIComponent(currentProject)}/messages/${messageId}`, {
            method: 'DELETE',
        });
        if (!resp.ok) {
            console.error('Delete message failed:', resp.status);
            return;
        }
        // Remove locally and re-render
        _allMessages = _allMessages.filter(m => m.id !== messageId);
        _totalMessages = Math.max(0, _totalMessages - 1);
        renderMessages(_allMessages);
    } catch (e) {
        console.error('Failed to delete message:', e);
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

async function _pollNewMessages() {
    if (!currentProject || isPaused) return;
    try {
        // Fetch a small batch from the end — if total grew, we have new messages
        const knownEnd = _loadedOffset + _allMessages.length;
        const result = await fetchMessages(currentProject, PAGE_SIZE, knownEnd);
        if (result.total > _totalMessages && result.messages.length > 0) {
            _allMessages = [..._allMessages, ...result.messages];
            _totalMessages = result.total;
            renderMessages(_allMessages);
        } else if (result.total !== _totalMessages) {
            // Messages were deleted — refresh
            _totalMessages = result.total;
        }
    } catch (e) {
        // Silent fail on poll
    }
}

let _pollCount = 0;

function startBoardPoll() {
    stopBoardPoll();
    _pollCount = 0;
    pollTimer = setInterval(() => {
        if (currentProject && !document.hidden) {
            _pollNewMessages();
            // Refresh subscribers less often (every 30s instead of every 10s)
            _pollCount++;
            if (_pollCount % 3 === 0) {
                loadBoardSubscribers(currentProject);
            }
        }
    }, 10000);
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
