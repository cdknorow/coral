/* Git commits tab: loading and rendering commit history for a session */

import { escapeHtml } from './utils.js';

export async function loadSessionCommits(sessionId) {
    const container = document.getElementById("commits-list");
    container.innerHTML = '<div class="empty-notes">Loading commits...</div>';

    try {
        const resp = await fetch(`/api/sessions/history/${encodeURIComponent(sessionId)}/git`);
        const data = await resp.json();
        renderCommits(data.commits || []);
    } catch (e) {
        console.error("Failed to load session commits:", e);
        container.innerHTML = '<div class="empty-notes">Failed to load commits</div>';
    }
}

function formatTimestamp(ts) {
    if (!ts) return "";
    try {
        const d = new Date(ts);
        return d.toLocaleString(undefined, {
            month: "short", day: "numeric",
            hour: "2-digit", minute: "2-digit",
        });
    } catch {
        return ts;
    }
}

function renderCommits(commits) {
    const container = document.getElementById("commits-list");

    if (!commits.length) {
        container.innerHTML = '<div class="empty-notes">No commits recorded during this session</div>';
        return;
    }

    const html = commits.map(c => {
        const shortHash = (c.commit_hash || "").substring(0, 8);
        const subject = escapeHtml(c.commit_subject || "(no message)");
        const ts = formatTimestamp(c.commit_timestamp);
        const branch = escapeHtml(c.branch || "");
        const agent = escapeHtml(c.agent_name || "");

        return `<div class="commit-row">
            <div class="commit-main">
                <code class="commit-hash">${shortHash}</code>
                <span class="commit-subject">${subject}</span>
            </div>
            <div class="commit-meta">
                <span class="commit-branch" title="Branch">${branch}</span>
                <span class="commit-agent" title="Agent">${agent}</span>
                <span class="commit-time">${ts}</span>
            </div>
        </div>`;
    }).join("");

    container.innerHTML = html;
}
