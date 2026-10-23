/* Exporting the board: the Export dialog and file, and select-and-export (moved out of message_board.js, #5174; no behaviour change besides where the board's open project, its messages and its redraw come from: the board hands them over once,
 * connectBoard, and the select-mode state lives here). */
import { escapeHtml, escapeAttr, showToast } from './utils.js';
import { keyLabel, splitKey, sessionServer, boardFetch } from './server_base.js';
import { attachDialogFocus } from './dialog_focus.js';
import { fetchAllBoardMessages, fetchAllHistory, fetchAllLiveChat, fetchSubscribers } from './board_fetch.js';
import { parseAgentHistory, renderExportHTML, renderExportMarkdown } from './board_export_render.js';

let _selectMode = false;
let _selectedIds = new Set();

// What the board hands over: the open project, the messages shown, a redraw of them and its time format.
let host = null;
export function connectBoard(h) { host = h; }
/** Select mode is on (the board draws a checkbox on each message). */
export function isSelectMode() { return _selectMode; }
export function isSelected(id) { return _selectedIds.has(id); }
/** A new board starts with nothing selected. */
export function resetSelection() { _selectMode = false; _selectedIds.clear(); }

// ── Export ────────────────────────────────────────────────────────────────

export async function showExportBoardModal() {
    if (!host.project()) return;

    // Fetch subscribers to build the agent selection list
    const subs = await fetchSubscribers(host.project());

    // Build modal HTML
    const agentCheckboxes = subs.map(s => {
        const role = escapeHtml(s.job_title || s.subscriber_id);
        const sid = escapeAttr(s.subscriber_id);
        return `<label style="display:flex;align-items:center;gap:6px;padding:2px 0;cursor:pointer;white-space:nowrap">
            <input type="checkbox" class="export-agent-cb" value="${escapeAttr(sid)}" data-role="${escapeAttr(s.job_title || s.subscriber_id)}">
            <span style="font-weight:600;font-size:0.75rem">${role}</span>
        </label>`;
    }).join('');

    const modal = document.getElementById('mb-export-modal');
    const body = document.getElementById('mb-export-body');
    const actions = document.getElementById('mb-export-actions');

    body.innerHTML = `
        <div style="margin-bottom:16px">
            <p style="margin:0 0 8px;font-size:0.875rem">Export <strong>${escapeHtml(keyLabel(host.project()))}</strong> board messages.</p>
            <p style="margin:0 0 12px;font-size:0.8125rem;color:var(--text-muted)">Optionally include individual agent chat histories, interleaved by timestamp.</p>
            <p id="export-secrets-note" style="margin:0;font-size:0.8125rem;color:var(--text-muted)">The export holds agent output as written and may include secrets the agents printed. It is plain text, not encrypted.</p>
        </div>
        <div style="margin-bottom:16px">
            <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:8px">
                <label style="font-weight:600;font-size:0.8125rem">Include agent chats:</label>
                <div style="display:flex;gap:8px">
                    <button class="mb-action-btn" data-on-click="mb-export-agents" data-checked="1" style="font-size:0.6875rem;padding:2px 8px">All</button>
                    <button class="mb-action-btn" data-on-click="mb-export-agents" data-checked="0" style="font-size:0.6875rem;padding:2px 8px">None</button>
                </div>
            </div>
            <div style="max-height:200px;overflow-y:auto;border:1px solid var(--border);border-radius:6px;padding:4px 12px">
                ${agentCheckboxes || '<p style="font-size:0.8125rem;color:var(--text-muted)">No subscribers</p>'}
            </div>
        </div>
        <div style="margin-bottom:8px">
            <label style="font-weight:600;font-size:0.8125rem;display:block;margin-bottom:4px">Format:</label>
            <select id="export-format-select" style="padding:6px 10px;border:1px solid var(--border);border-radius:6px;background:var(--bg-secondary);color:var(--text-primary);font-size:0.8125rem">
                <option value="html">HTML (styled, shareable)</option>
                <option value="json">JSON (canonical data)</option>
                <option value="markdown">Markdown</option>
            </select>
        </div>
    `;
    actions.innerHTML = `
        <button class="btn" data-on-click="mb-export-close">Cancel</button>
        <button class="btn btn-primary" data-on-click="mb-export-do">Export</button>
    `;
    attachDialogFocus(modal);
    modal.style.display = 'flex';
}

 export function hideExportBoardModal() {
    const modal = document.getElementById('mb-export-modal');
    if (modal) modal.style.display = 'none';
}

export async function doExportBoard() {
    const format = document.getElementById('export-format-select').value;
    const selectedAgents = Array.from(document.querySelectorAll('.export-agent-cb:checked'))
        .map(cb => ({ sessionId: cb.value, role: cb.dataset.role }));

    // Show progress in the export dialog's own footer
    const actions = document.getElementById('mb-export-actions');
    actions.innerHTML = '<span style="color:var(--text-muted);font-size:0.8125rem">Exporting...</span>';

    try {
        // 1. Fetch all board messages (in pages, #3718)
        const boardMessages = await fetchAllBoardMessages(host.project());

        // 2. Fetch subscribers
        const subsResp = await boardFetch(host.project(), `/subscribers`);
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
        const liveRaw = await liveResp.json();
        // The list is {sessions, servers} when remotes exist; only this board's server counts.
        const boardSrv = splitKey(host.project()).server;
        const liveSessions = (Array.isArray(liveRaw) ? liveRaw : (liveRaw.sessions || [])).filter(s => sessionServer(s) === boardSrv);

        // 5. Fetch selected agent chat histories and merge
        for (const agent of selectedAgents) {
            try {
                // Find the live session for this subscriber to get name + working_directory
                const live = liveSessions.find(s => s.display_name === agent.role);
                // Live: the chat endpoint (it has working_directory for JSONL resolution); ended:
                // the history endpoint. Both in pages (#3718).
                const histData = live ? await fetchAllLiveChat(live) : await fetchAllHistory(agent.sessionId);
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
            project: keyLabel(host.project()),
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
            filename = `${keyLabel(host.project())}-export.json`;
            mime = 'application/json';
        } else if (format === 'markdown') {
            output = renderExportMarkdown(exportData);
            filename = `${keyLabel(host.project())}-export.md`;
            mime = 'text/markdown';
        } else {
            output = renderExportHTML(exportData);
            filename = `${keyLabel(host.project())}-export.html`;
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

        hideExportBoardModal();
        showToast(`Exported ${entries.length} messages as ${format}`);
    } catch (e) {
        console.error('Export failed:', e);
        actions.innerHTML = `<span style="color:var(--error-text)">Export failed: ${escapeHtml(e.message)}</span>
            <button class="btn" data-on-click="mb-export-close">Close</button>`;
    }
}

// ── Agent History Parsers ─────────────────────────────────────────────────
// Modular parsers for different agent JSONL formats. Each returns an array
// of { timestamp, role, content, source } entries.





// ── Multi-select export ─────────────────────────────────────────────────

export function toggleSelectMode() {
    _selectMode = !_selectMode;
    _selectedIds.clear();
    const btn = document.getElementById('mb-select-btn');
    if (btn) btn.classList.toggle('active', _selectMode);
    updateSelectBar();
    host.render();
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
    host.messages().forEach(m => _selectedIds.add(m.id));
    host.render();
    updateSelectBar();
}

export function selectNoneMessages() {
    _selectedIds.clear();
    host.render();
    updateSelectBar();
}

export function cancelSelectMode() {
    _selectMode = false;
    _selectedIds.clear();
    const btn = document.getElementById('mb-select-btn');
    if (btn) btn.classList.remove('active');
    updateSelectBar();
    host.render();
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
        <button class="btn mb-select-action-btn" data-on-click="mb-select-all">Select All</button>
        <button class="btn mb-select-action-btn" data-on-click="mb-select-none">None</button>
        <button class="btn btn-primary mb-select-action-btn" data-on-click="mb-export-selected" ${count === 0 ? 'disabled' : ''}>Export Markdown</button>
        <button class="btn mb-select-action-btn" data-on-click="mb-select-cancel">Cancel</button>
    `;
    bar.style.display = '';
}

export async function exportSelectedAsMarkdown() {
    const selected = host.messages()
        .filter(m => _selectedIds.has(m.id))
        .sort((a, b) => (a.created_at || '').localeCompare(b.created_at || ''));

    if (!selected.length) return;

    const now = new Date().toLocaleString();
    let md = `# ${keyLabel(host.project())} — Exported Chat\n\n`;
    md += `**Exported**: ${now} · **Messages**: ${selected.length}\n\n---\n\n`;

    for (const m of selected) {
        const agent = m.job_title || 'Unknown';
        const time = host.formatTime(m.created_at);
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
    a.download = `${keyLabel(host.project())}-selected-export.md`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);

    cancelSelectMode();
}
