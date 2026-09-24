/* Message Board: project list, messages, subscribers, posting */

import { escapeHtml, escapeAttr, showView, renderMarkdown, boardMessageText, getAgentColor, hexToRgba, showToast } from './utils.js';
import { state } from './state.js';
import { loadLiveSessions } from './api.js';
import { platform } from './platform/detect.js';

let currentProject = null;
let pollTimer = null;
let isPaused = false;
let isSleeping = false;
const PAGE_SIZE = 50;
let _allMessages = [];
let _totalMessages = 0;
let _loadedOffset = 0;
let _selectMode = false;
let _selectedIds = new Set();

// ── API helpers ──────────────────────────────────────────────────────────

async function fetchProjects() {
    const resp = await fetch('/api/board/projects');
    return await resp.json();
}

async function fetchMessages(project, limit = PAGE_SIZE, offset = 0) {
    const resp = await fetch(`/api/board/${encodeURIComponent(project)}/messages/all?limit=${limit}&offset=${offset}&format=dashboard`);
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
    showView("messageboard-view");

    // Show board panel
    document.getElementById('mb-project-list').style.display = 'none';
    const board = document.getElementById('mb-board');
    board.style.display = 'flex';
    document.getElementById('mb-subscribers-panel').style.display = 'block';
    const backBtn = document.getElementById('mb-back-btn');
    if (backBtn) backBtn.style.display = '';
    document.getElementById('mb-pause-btn').style.display = '';
    document.getElementById('mb-export-btn').style.display = '';
    document.getElementById('mb-select-btn').style.display = '';
    const sleepBtn = document.getElementById('mb-sleep-btn');
    if (sleepBtn) sleepBtn.style.display = '';
    document.getElementById('mb-delete-btn').style.display = '';

    const badge = document.getElementById('messageboard-project-badge');
    badge.textContent = project;
    badge.style.display = '';
    document.getElementById('messageboard-title').textContent = 'Message Board';

    // Ensure dashboard is subscribed as a reader
    subscribeDashboard(project);

    // Load paused and sleep state
    loadPausedState(project);
    loadSleepState(project);

    loadBoardMessages(project);
    loadBoardSubscribers(project);
    loadBoardProjects();
    startBoardPoll();
}

export function showMessageBoardProjects() {
    currentProject = null;
    stopBoardPoll();
    // Exit select mode if active
    _selectMode = false;
    _selectedIds.clear();

    showView("messageboard-view");

    document.getElementById('mb-project-list').style.display = '';
    document.getElementById('mb-board').style.display = 'none';
    document.getElementById('mb-subscribers-panel').style.display = 'none';
    const backBtn2 = document.getElementById('mb-back-btn');
    if (backBtn2) backBtn2.style.display = 'none';
    document.getElementById('mb-pause-btn').style.display = 'none';
    const sleepBtn2 = document.getElementById('mb-sleep-btn');
    if (sleepBtn2) sleepBtn2.style.display = 'none';
    document.getElementById('mb-delete-btn').style.display = 'none';
    document.getElementById('mb-select-btn').style.display = 'none';
    document.getElementById('messageboard-project-badge').style.display = 'none';

    loadBoardProjectList();
    loadBoardProjects();
}

// ── Project list view ────────────────────────────────────────────────────

async function loadBoardProjectList() {
    const ul = document.getElementById('mb-projects-ul');
    const empty = document.getElementById('mb-projects-empty');
    // Show loading indicator while fetching
    empty.style.display = 'none';
    ul.innerHTML = '<li class="loading-indicator">Loading projects</li>';
    try {
        const projects = await fetchProjects();
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
    const msgsEl = document.getElementById('mb-messages');
    const empty = document.getElementById('mb-messages-empty');
    if (empty) empty.style.display = 'none';
    // Show loading indicator below any existing messages
    let loadingEl = msgsEl?.querySelector('.loading-indicator');
    if (!loadingEl && msgsEl) {
        loadingEl = document.createElement('div');
        loadingEl.className = 'loading-indicator';
        loadingEl.textContent = 'Loading messages';
        msgsEl.appendChild(loadingEl);
    }
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

    container.innerHTML = loadEarlierHtml + countHtml + messages.map((m, i) => {
        const color = getAgentColor(m.job_title || 'Unknown');
        const agent = m.job_title || 'Unknown';
        const prevAgent = i > 0 ? (messages[i - 1].job_title || 'Unknown') : null;
        const sameAsPrev = agent === prevAgent;
        const spacing = sameAsPrev ? 'mb-message-grouped' : 'mb-message-first';
        const isLeader = /orchestrator/i.test(agent) || m.subscriber_id === 'dashboard';
        const alignClass = isLeader ? ' board-msg-left' : ' board-msg-right';
        const selectedClass = _selectMode && _selectedIds.has(m.id) ? ' mb-message-selected' : '';
        const checkbox = _selectMode ? `<input type="checkbox" class="mb-select-checkbox" data-msg-id="${m.id}" ${_selectedIds.has(m.id) ? 'checked' : ''} onclick="toggleMessageSelect(${m.id}, this.checked)">` : '';
        return `
        <div class="mb-message ${spacing}${alignClass}${selectedClass}" style="border-left:3px solid ${hexToRgba(color, 0.55)}; border-bottom:2px solid ${hexToRgba(color, 0.3)}">
            <div class="mb-message-header">
                ${checkbox}
                <span class="mb-agent-name" style="color:${color}">${m.icon ? escapeHtml(m.icon) + ' ' : ''}${escapeHtml(agent)}</span>
                <span class="mb-message-time">${formatTime(m.created_at)}</span>
                <button class="mb-delete-msg-btn" onclick="deleteBoardMessage(${m.id})" title="Delete message">&times;</button>
            </div>
            <div class="mb-message-body">${renderMarkdown(boardMessageText(m.content))}</div>
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

function navigateToAgentSession(sessionName, displayName) {
    if (sessionName) {
        // session_name format is "<agentType>-<uuid>" — extract the UUID
        const dashIdx = sessionName.indexOf('-');
        const sessionId = dashIdx >= 0 ? sessionName.substring(dashIdx + 1) : sessionName;
        // Try live session first, fall back to history
        const live = (state.liveSessions || []).find(s => s.session_id === sessionId);
        if (live) {
            window.selectLiveSession(live.name, live.agent_type, live.session_id);
        } else {
            window.selectHistorySession(sessionId);
        }
        return;
    }
    // Fallback: search by display name
    navigateToAgentHistoryByName(displayName);
}
window.navigateToAgentSession = navigateToAgentSession;

async function navigateToAgentHistoryByName(displayName) {
    const resp = await fetch(`/api/sessions/history?page_size=50`);
    const data = await resp.json();
    const sessions = data.sessions || [];
    const match = sessions.find(s => s.display_name === displayName || s.agent_name === displayName);
    if (match) {
        window.selectHistorySession(match.session_id);
    } else {
        showToast(`No history found for ${displayName}`);
    }
}

async function loadBoardSubscribers(project) {
    try {
        const subs = await fetchSubscribers(project);
        const list = document.getElementById('mb-subscribers-list');
        if (!subs.length) {
            list.innerHTML = '<li style="font-size:12px;color:var(--text-muted)">No subscribers</li>';
            return;
        }
        list.innerHTML = subs.map(s => {
            const displayName = escapeAttr(s.job_title || s.subscriber_id);
            const sessionName = escapeAttr(s.session_name || '');
            const icon = s.icon ? `<span class="agent-icon">${escapeHtml(s.icon)}</span> ` : '';
            return `<li style="padding:6px 0;border-bottom:1px solid var(--border)">
                <div style="font-weight:600;font-size:12px">${icon}<a href="javascript:void(0)" class="subscriber-history-link" onclick="navigateToAgentSession('${sessionName}', '${displayName}')" title="View chat history">${escapeHtml(s.job_title)}</a></div>
                <div style="font-size:10px;color:var(--text-muted)">${escapeHtml(s.subscriber_id)}</div>
            </li>`;
        }).join('');
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
            body: JSON.stringify({ subscriber_id: 'dashboard', job_title: 'Operator' }),
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
            body: JSON.stringify({ subscriber_id: 'dashboard', content }),
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
    const buttons = [
        document.getElementById('mb-pause-btn'),
        document.getElementById('board-chat-pause-btn'),
    ];
    for (const btn of buttons) {
        if (!btn) continue;
        if (isPaused) {
            btn.textContent = 'Resume Board';
            btn.classList.add('mb-action-danger');
        } else {
            btn.textContent = 'Pause Board';
            btn.classList.remove('mb-action-danger');
        }
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

export function deleteMessageBoardProject() {
    if (!currentProject) return;
    const project = currentProject;
    window.showConfirmModal('Delete Board', `Delete project "${project}" and all its messages?`, async () => {
        try {
            await fetch(`/api/board/${encodeURIComponent(project)}`, { method: 'DELETE' });
            showMessageBoardProjects();
        } catch (e) {
            console.error('Failed to delete project:', e);
        }
    });
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
        if (currentProject && (platform.isNative || !document.hidden)) {
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

// ── Sleep / Wake ─────────────────────────────────────────────────────

async function loadSleepState(project) {
    try {
        const resp = await fetch(`/api/sessions/live/team/${encodeURIComponent(project)}/sleep-status`);
        const data = await resp.json();
        isSleeping = !!data.sleeping;
        updateSleepUI();
    } catch (e) {
        isSleeping = false;
        updateSleepUI();
    }
}

function updateSleepUI() {
    const btn = document.getElementById('mb-sleep-btn');
    if (btn) {
        btn.textContent = isSleeping ? 'Wake Team' : 'Sleep Team';
        btn.classList.toggle('btn-warning', isSleeping);
    }
    const banner = document.getElementById('mb-sleep-banner');
    if (banner) {
        banner.style.display = isSleeping ? '' : 'none';
    }
    const input = document.getElementById('mb-post-input');
    const sendBtn = document.querySelector('#mb-board .btn-primary');
    if (input) {
        input.disabled = isSleeping;
        input.placeholder = isSleeping ? 'Team is sleeping...' : 'Post a message as Operator...';
    }
    if (sendBtn) {
        sendBtn.disabled = isSleeping;
        sendBtn.style.opacity = isSleeping ? '0.4' : '';
    }
}

export function toggleBoardSleep() {
    if (!currentProject) return;
    const action = isSleeping ? 'wake' : 'sleep';
    const doIt = async () => {
        try {
            const resp = await fetch(`/api/sessions/live/team/${encodeURIComponent(currentProject)}/${action}`, { method: 'POST' });
            const data = await resp.json();
            isSleeping = !!data.sleeping;
            updateSleepUI();
            loadLiveSessions();
        } catch (e) {
            console.error('Failed to toggle sleep:', e);
        }
    };
    if (!isSleeping) {
        window.showConfirmModal('Sleep Team', `Put all agents on "${currentProject}" to sleep?`, doIt);
    } else {
        doIt();
    }
}

// ── Export ────────────────────────────────────────────────────────────────

export async function showExportBoardModal() {
    if (!currentProject) return;

    // Fetch subscribers to build the agent selection list
    const subs = await fetchSubscribers(currentProject);

    // Build modal HTML
    const agentCheckboxes = subs.map(s => {
        const role = escapeHtml(s.job_title || s.subscriber_id);
        const sid = escapeAttr(s.subscriber_id);
        return `<label style="display:flex;align-items:center;gap:6px;padding:2px 0;cursor:pointer;white-space:nowrap">
            <input type="checkbox" class="export-agent-cb" value="${sid}" data-role="${escapeAttr(s.job_title || s.subscriber_id)}">
            <span style="font-weight:600;font-size:12px">${role}</span>
        </label>`;
    }).join('');

    const modal = document.getElementById('confirm-modal');
    const content = modal.querySelector('.modal-content');
    if (content) content.classList.add('modal-content-wide');
    const title = modal.querySelector('.modal-title') || modal.querySelector('h3');
    const body = modal.querySelector('.modal-body') || modal.querySelector('.confirm-body');
    const actions = modal.querySelector('.modal-actions') || modal.querySelector('.confirm-actions');

    title.textContent = 'Export Board Chat';
    body.innerHTML = `
        <div style="margin-bottom:16px">
            <p style="margin:0 0 8px;font-size:14px">Export <strong>${escapeHtml(currentProject)}</strong> board messages.</p>
            <p style="margin:0 0 12px;font-size:13px;color:var(--text-muted)">Optionally include individual agent chat histories, interleaved by timestamp.</p>
        </div>
        <div style="margin-bottom:16px">
            <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:8px">
                <label style="font-weight:600;font-size:13px">Include agent chats:</label>
                <div style="display:flex;gap:8px">
                    <button class="mb-action-btn" onclick="document.querySelectorAll('.export-agent-cb').forEach(c=>c.checked=true)" style="font-size:11px;padding:2px 8px">All</button>
                    <button class="mb-action-btn" onclick="document.querySelectorAll('.export-agent-cb').forEach(c=>c.checked=false)" style="font-size:11px;padding:2px 8px">None</button>
                </div>
            </div>
            <div style="max-height:200px;overflow-y:auto;border:1px solid var(--border);border-radius:6px;padding:4px 12px">
                ${agentCheckboxes || '<p style="font-size:13px;color:var(--text-muted)">No subscribers</p>'}
            </div>
        </div>
        <div style="margin-bottom:8px">
            <label style="font-weight:600;font-size:13px;display:block;margin-bottom:4px">Format:</label>
            <select id="export-format-select" style="padding:6px 10px;border:1px solid var(--border);border-radius:6px;background:var(--bg-secondary);color:var(--text-primary);font-size:13px">
                <option value="html">HTML (styled, shareable)</option>
                <option value="json">JSON (canonical data)</option>
                <option value="markdown">Markdown</option>
            </select>
        </div>
    `;
    actions.innerHTML = `
        <button class="btn" onclick="closeConfirmModal()">Cancel</button>
        <button class="btn btn-primary" onclick="doExportBoard()">Export</button>
    `;
    modal.style.display = 'flex';
}

export async function doExportBoard() {
    const format = document.getElementById('export-format-select').value;
    const selectedAgents = Array.from(document.querySelectorAll('.export-agent-cb:checked'))
        .map(cb => ({ sessionId: cb.value, role: cb.dataset.role }));

    // Close modal and show progress
    const modal = document.getElementById('confirm-modal');
    const actions = modal.querySelector('.modal-actions') || modal.querySelector('.confirm-actions');
    actions.innerHTML = '<span style="color:var(--text-muted);font-size:13px">Exporting...</span>';

    try {
        // 1. Fetch all board messages
        const boardResp = await fetch(`/api/board/${encodeURIComponent(currentProject)}/messages/all?limit=10000`);
        const boardMessages = await boardResp.json();

        // 2. Fetch subscribers
        const subsResp = await fetch(`/api/board/${encodeURIComponent(currentProject)}/subscribers`);
        const subscribers = await subsResp.json();

        // 3. Build entries from board messages
        const entries = boardMessages.map(m => ({
            timestamp: (m.created_at || '').substring(0, 16),
            role: m.job_title || m.subscriber_id || 'unknown',
            content: m.content || '',
            source: 'board',
        }));

        // 4. Fetch live sessions to get working directories and names
        const liveResp = await fetch('/api/sessions/live');
        const liveSessions = await liveResp.json();

        // 5. Fetch selected agent chat histories and merge
        for (const agent of selectedAgents) {
            try {
                // Find the live session for this subscriber to get name + working_directory
                const live = liveSessions.find(s => s.display_name === agent.role);
                let histData;
                if (live) {
                    // Use live chat endpoint (has working_directory for JSONL resolution)
                    const params = new URLSearchParams({
                        agent_type: live.agent_type || 'claude',
                        session_id: live.session_id,
                        working_directory: live.working_directory || '',
                        after: '0',
                        limit: '10000',
                    });
                    const chatResp = await fetch(`/api/sessions/live/${encodeURIComponent(live.name)}/chat?${params}`);
                    histData = await chatResp.json();
                } else {
                    // Fallback to history endpoint for completed sessions
                    const histResp = await fetch(`/api/sessions/history/${encodeURIComponent(agent.sessionId)}`);
                    histData = await histResp.json();
                }
                if (histData.messages && histData.messages.length) {
                    const parsed = parseAgentHistory(histData.messages, agent.role);
                    entries.push(...parsed);
                }
            } catch (e) {
                console.warn(`Failed to load history for ${agent.sessionId}:`, e);
            }
        }

        // 5. Sort by timestamp
        entries.sort((a, b) => a.timestamp.localeCompare(b.timestamp));

        // 6. Build export data
        const exportData = {
            project: currentProject,
            exported_at: new Date().toISOString(),
            subscribers: subscribers.map(s => ({
                session_id: s.subscriber_id,
                role: s.job_title || s.subscriber_id,
            })),
            messages: entries,
            stats: {
                total: entries.length,
                board: entries.filter(e => e.source === 'board').length,
                agent_chat: entries.filter(e => e.source === 'agent-chat').length,
            },
        };

        // 7. Render and download
        let output, filename, mime;
        if (format === 'json') {
            output = JSON.stringify(exportData, null, 2);
            filename = `${currentProject}-export.json`;
            mime = 'application/json';
        } else if (format === 'markdown') {
            output = renderExportMarkdown(exportData);
            filename = `${currentProject}-export.md`;
            mime = 'text/markdown';
        } else {
            output = renderExportHTML(exportData);
            filename = `${currentProject}-export.html`;
            mime = 'text/html';
        }

        // Trigger download
        const blob = new Blob([output], { type: mime });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = filename;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        URL.revokeObjectURL(url);

        modal.style.display = 'none';
        showToast(`Exported ${entries.length} messages as ${format}`);
    } catch (e) {
        console.error('Export failed:', e);
        actions.innerHTML = `<span style="color:var(--error)">Export failed: ${escapeHtml(e.message)}</span>
            <button class="btn" onclick="closeConfirmModal()">Close</button>`;
    }
}

// ── Agent History Parsers ─────────────────────────────────────────────────
// Modular parsers for different agent JSONL formats. Each returns an array
// of { timestamp, role, content, source } entries.

function parseAgentHistory(messages, agentRole) {
    const entries = [];
    for (const msg of messages) {
        const ts = (msg.timestamp || '').substring(0, 16);
        if (!ts) continue;

        if (msg.type === 'user') {
            // Human/operator message to this agent
            const content = typeof msg.content === 'string' ? msg.content : '';
            if (!content.trim()) continue;
            // Skip system notifications (board read reminders, task notifications)
            if (content.startsWith('You have ') && content.includes('unread message')) continue;
            if (content.startsWith('<task-notification>')) continue;
            entries.push({
                timestamp: ts,
                role: `Operator → ${agentRole}`,
                content: content.trim(),
                source: 'agent-chat',
            });
        } else if (msg.type === 'assistant') {
            // Agent's text response (skip tool-only messages)
            const text = (msg.text || '').trim();
            if (!text) continue;
            entries.push({
                timestamp: ts,
                role: agentRole,
                content: text,
                source: 'agent-chat',
            });
        }
        // Skip tool_result messages — they're tool output, not conversation
    }
    return entries;
}

function agentHue(name) {
    let h = 0;
    for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) & 0xFFFFFF;
    return h % 360;
}

function renderExportMarkdown(d) {
    let out = `# Board Chat Export: ${d.project}\n\n`;
    out += `**Exported**: ${d.exported_at}\n`;
    out += `**Messages**: ${d.stats.total}\n`;
    if (d.stats.agent_chat > 0) {
        out += `**Board**: ${d.stats.board} | **Agent chats**: ${d.stats.agent_chat}\n`;
    }
    if (d.subscribers.length) {
        out += `**Subscribers**: ${d.subscribers.length}\n\n`;
        out += '| Agent | Role |\n|-------|------|\n';
        d.subscribers.forEach(s => { out += `| ${s.subscriber_id} | ${s.role} |\n`; });
    }
    out += '\n---\n\n## Messages\n\n';
    d.messages.forEach(e => {
        const tag = e.source === 'agent-chat' ? ' (agent chat)' : '';
        out += `**[${e.timestamp}] ${e.role}${tag}:**\n\n${e.content}\n\n---\n\n`;
    });
    return out;
}

function renderExportHTML(d) {
    const msgs = d.messages.map(e => {
        const cls = e.source === 'agent-chat' ? 'msg side-chat' : 'msg';
        const color = `hsl(${agentHue(e.role)}, 60%, 45%)`;
        const tag = e.source === 'agent-chat' ? '<span class="msg-tag">AGENT CHAT</span>' : '';
        return `<div class="${cls}">
  <div class="msg-header">
    <span class="msg-role" style="color:${color}">${escapeHtml(e.role)}</span>
    <span class="msg-time">${escapeHtml(e.timestamp)}</span>
    ${tag}
  </div>
  <div class="msg-content">${renderMarkdown(e.content)}</div>
</div>`;
    }).join('\n');

    const subList = d.subscribers.map(s =>
        `<div class="sub-chip"><span class="role">${escapeHtml(s.role)}</span></div>`
    ).join('\n');

    return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Board Chat Export: ${escapeHtml(d.project)}</title>
<style>
  :root { --bg: #0d1117; --surface: #161b22; --border: #30363d; --text: #e6edf3; --muted: #8b949e; --accent: #58a6ff; --side-chat: #1a1a2e; }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif; background: var(--bg); color: var(--text); line-height: 1.6; }
  .container { max-width: 900px; margin: 0 auto; padding: 2rem 1rem; }
  h1 { font-size: 1.8rem; margin-bottom: 0.5rem; }
  .stats { color: var(--muted); margin-bottom: 1.5rem; font-size: 0.9rem; }
  .stats span { margin-right: 1.5rem; }
  .subscribers { margin-bottom: 2rem; }
  .subscribers summary { cursor: pointer; color: var(--accent); font-weight: 600; margin-bottom: 0.5rem; }
  .sub-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 0.5rem; padding: 0.5rem 0; }
  .sub-chip { background: var(--surface); border: 1px solid var(--border); border-radius: 6px; padding: 0.4rem 0.8rem; font-size: 0.85rem; }
  .sub-chip .role { font-weight: 600; }
  .messages { display: flex; flex-direction: column; gap: 0.75rem; }
  .msg { background: var(--surface); border: 1px solid var(--border); border-radius: 8px; padding: 1rem; border-left: 3px solid var(--accent); }
  .msg.side-chat { background: var(--side-chat); border-left-color: #f0883e; }
  .msg-header { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.5rem; flex-wrap: wrap; }
  .msg-role { font-weight: 700; font-size: 0.95rem; }
  .msg-time { color: var(--muted); font-size: 0.8rem; }
  .msg-tag { background: #f0883e33; color: #f0883e; font-size: 0.7rem; padding: 0.15rem 0.5rem; border-radius: 10px; font-weight: 600; }
  .msg-content { word-wrap: break-word; font-size: 0.9rem; }
  .msg-content p { margin: 0.4em 0; }
  .msg-content h1, .msg-content h2, .msg-content h3, .msg-content h4 { margin: 0.8em 0 0.3em; font-size: 1.1em; }
  .msg-content h2 { font-size: 1.05em; }
  .msg-content h3 { font-size: 1em; }
  .msg-content code { background: rgba(255,255,255,0.08); padding: 0.15em 0.4em; border-radius: 4px; font-size: 0.9em; }
  .msg-content pre { background: rgba(0,0,0,0.3); padding: 0.8em; border-radius: 6px; overflow-x: auto; margin: 0.5em 0; }
  .msg-content pre code { background: none; padding: 0; }
  .msg-content ul, .msg-content ol { padding-left: 1.5em; margin: 0.3em 0; }
  .msg-content li { margin: 0.2em 0; }
  .msg-content strong { font-weight: 700; }
  .msg-content blockquote { border-left: 3px solid var(--border); padding-left: 0.8em; color: var(--muted); margin: 0.5em 0; }
  .msg-content table { border-collapse: collapse; margin: 0.5em 0; width: 100%; }
  .msg-content th, .msg-content td { border: 1px solid var(--border); padding: 0.4em 0.6em; text-align: left; font-size: 0.85em; }
  .msg-content th { background: rgba(0,0,0,0.2); font-weight: 600; }
</style>
</head>
<body>
<div class="container">
<h1>${escapeHtml(d.project)}</h1>
<div class="stats">
  <span>Exported: ${escapeHtml(d.exported_at)}</span>
  <span>Messages: ${d.stats.total}</span>
  ${d.stats.agent_chat > 0 ? `<span>Board: ${d.stats.board}</span><span>Agent chats: ${d.stats.agent_chat}</span>` : ''}
</div>
${d.subscribers.length ? `<details class="subscribers"><summary>${d.subscribers.length} Subscribers</summary><div class="sub-grid">${subList}</div></details>` : ''}
<div class="messages">
${msgs}
</div>
</div>
</body>
</html>`;
}

// ── Multi-select export ─────────────────────────────────────────────────

export function toggleSelectMode() {
    _selectMode = !_selectMode;
    _selectedIds.clear();
    const btn = document.getElementById('mb-select-btn');
    if (btn) btn.classList.toggle('active', _selectMode);
    updateSelectBar();
    renderMessages(_allMessages);
}

export function toggleMessageSelect(msgId, checked) {
    if (checked) {
        _selectedIds.add(msgId);
    } else {
        _selectedIds.delete(msgId);
    }
    // Update selected highlight without full re-render
    const msg = document.querySelector(`.mb-select-checkbox[data-msg-id="${msgId}"]`);
    if (msg) {
        msg.closest('.mb-message').classList.toggle('mb-message-selected', checked);
    }
    updateSelectBar();
}

export function selectAllMessages() {
    _allMessages.forEach(m => _selectedIds.add(m.id));
    renderMessages(_allMessages);
    updateSelectBar();
}

export function selectNoneMessages() {
    _selectedIds.clear();
    renderMessages(_allMessages);
    updateSelectBar();
}

export function cancelSelectMode() {
    _selectMode = false;
    _selectedIds.clear();
    const btn = document.getElementById('mb-select-btn');
    if (btn) btn.classList.remove('active');
    updateSelectBar();
    renderMessages(_allMessages);
}

function updateSelectBar() {
    let bar = document.getElementById('mb-select-bar');
    if (!_selectMode) {
        if (bar) bar.style.display = 'none';
        return;
    }
    if (!bar) {
        bar = document.createElement('div');
        bar.id = 'mb-select-bar';
        bar.className = 'mb-select-bar';
        document.getElementById('mb-board').appendChild(bar);
    }
    const count = _selectedIds.size;
    bar.innerHTML = `
        <span class="mb-select-count">${count} selected</span>
        <button class="btn mb-select-action-btn" onclick="selectAllMessages()">Select All</button>
        <button class="btn mb-select-action-btn" onclick="selectNoneMessages()">None</button>
        <button class="btn btn-primary mb-select-action-btn" onclick="exportSelectedAsMarkdown()" ${count === 0 ? 'disabled' : ''}>Export Markdown</button>
        <button class="btn mb-select-action-btn" onclick="cancelSelectMode()">Cancel</button>
    `;
    bar.style.display = '';
}

export async function exportSelectedAsMarkdown() {
    const selected = _allMessages
        .filter(m => _selectedIds.has(m.id))
        .sort((a, b) => (a.created_at || '').localeCompare(b.created_at || ''));

    if (!selected.length) return;

    const now = new Date().toLocaleString();
    let md = `# ${currentProject} — Exported Chat\n\n`;
    md += `**Exported**: ${now} · **Messages**: ${selected.length}\n\n---\n\n`;

    for (const m of selected) {
        const agent = m.job_title || 'Unknown';
        const time = formatTime(m.created_at);
        md += `### ${agent} — ${time}\n${m.content}\n\n`;
    }
    md += `---\n*Exported from Coral*\n`;

    // Copy to clipboard
    try {
        await navigator.clipboard.writeText(md);
        showToast(`Copied ${selected.length} messages to clipboard`);
    } catch {
        // Fallback: just download
    }

    // Also offer download
    const blob = new Blob([md], { type: 'text/markdown' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${currentProject}-selected-export.md`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);

    cancelSelectMode();
}

// ── Init ─────────────────────────────────────────────────────────────────

export function initMessageBoard() {
    loadBoardProjects();
}

export { loadBoardProjects, showMessageBoardProjects as showBoardProjects };
