/* Jobs: sidebar rendering for active task runs (cron + API-triggered) */

import { selectLiveSession } from './sessions.js';
import { escapeHtml, escapeAttr } from './utils.js';

let activeRuns = [];

export function initLiveJobs() {
    // Data comes via WebSocket — nothing to init
}

export function renderLiveJobs(runs) {
    activeRuns = runs || [];
    const list = document.getElementById('live-jobs-list');
    if (!list) return;

    // Update active jobs count badge
    const countBadge = document.getElementById('active-jobs-count');
    if (countBadge) countBadge.textContent = activeRuns.length || '';

    // Update combined jobs section count
    updateJobsSectionCount();

    if (!activeRuns.length) {
        list.innerHTML = '<li class="empty-state">No active jobs</li>';
        return;
    }

    list.innerHTML = activeRuns.map(run => {
        const name = run.display_name || run.job_name || `Run #${run.id}`;
        const hasSid = !!run.session_id;
        const isPending = run.status === 'pending';
        const triggerBadge = (run.trigger_type || 'cron') === 'api'
            ? '<span class="live-job-badge live-job-badge-api">api</span>'
            : '<span class="live-job-badge live-job-badge-cron">cron</span>';

        // Status dot
        const dotClass = isPending ? 'status-dot-stale' : 'status-dot-working';

        // Elapsed time
        const elapsed = run.started_at ? formatElapsed(run.started_at) : 'pending';

        const clickable = hasSid ? '' : ' style="opacity: 0.5; cursor: default;"';
        const onClick = hasSid
            ? `onclick="selectLiveJobRun('${escapeAttr(run.session_id)}', '${escapeAttr(run.agent_type || 'claude')}')"`
            : '';

        return `<li class="session-list-item"${clickable} ${onClick}>
            <div class="live-job-item">
                <span class="status-dot ${dotClass}"></span>
                <span class="live-job-name">${escapeHtml(name)}</span>
                ${triggerBadge}
                <span class="live-job-elapsed">${elapsed}</span>
            </div>
        </li>`;
    }).join('');
}

export function selectLiveJobRun(sessionId, agentType) {
    // Find the matching live session name from the current live sessions list
    // and delegate to selectLiveSession
    const name = agentType + '-' + sessionId;
    selectLiveSession(name, agentType, sessionId);
}

function formatElapsed(startedAt) {
    try {
        const start = new Date(startedAt);
        const now = new Date();
        const diffMs = now - start;
        if (diffMs < 0) return '0s';
        const totalSec = Math.floor(diffMs / 1000);
        if (totalSec < 60) return `${totalSec}s`;
        const min = Math.floor(totalSec / 60);
        const sec = totalSec % 60;
        if (min < 60) return `${min}m ${sec}s`;
        const hr = Math.floor(min / 60);
        const remMin = min % 60;
        return `${hr}h ${remMin}m`;
    } catch {
        return '';
    }
}

function updateJobsSectionCount() {
    const jobsCount = document.getElementById('jobs-count');
    if (!jobsCount) return;
    const scheduledList = document.getElementById('scheduled-jobs-list');
    const scheduledCount = scheduledList ? scheduledList.querySelectorAll('li:not(.empty-state)').length : 0;
    const total = activeRuns.length + scheduledCount;
    jobsCount.textContent = total;

    // Update scheduled count badge
    const schedBadge = document.getElementById('scheduled-jobs-count');
    if (schedBadge) schedBadge.textContent = scheduledCount || '';
}
