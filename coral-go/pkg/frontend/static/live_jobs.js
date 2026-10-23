/* Jobs: sidebar rendering for active task runs (cron + API-triggered) */

import { selectLiveSession } from './sessions.js';
import { escapeHtml, escapeAttr } from './utils.js';
import { registerActions, textOf, tokenOf } from './actions.js';

let activeRuns = [];

export function initLiveJobs() {
    // Data comes via WebSocket — nothing to init
}

export function renderLiveJobs(runs) {
    activeRuns = runs || [];
    const list = document.getElementById('live-jobs-list');
    if (!list) return;

    // "Running now" is there only while something runs (#6521): an empty "No active jobs" over jobs that exist read as "no jobs"
    const group = document.getElementById('live-jobs-group');
    if (group) group.hidden = !activeRuns.length;
    const status = document.getElementById('live-jobs-status'); // said once when a run starts or the last one ends, not on every redraw
    if (status) { const said = activeRuns.length ? `${activeRuns.length} job${activeRuns.length === 1 ? '' : 's'} running now` : ''; if (status.textContent !== said) status.textContent = said; }
    updateJobsSectionCount();

    if (!activeRuns.length) {
        list.replaceChildren();
        return;
    }

    list.innerHTML = activeRuns.map(run => {
        // A one-off run (POST /api/tasks/run) has no job of its own: never the
        // internal "__oneshot__" name or a cron badge (#3171).
        const oneOff = run.trigger_type === 'api' || run.job_name === '__oneshot__';
        const name = run.display_name || (oneOff ? `Job run ${run.id}` : run.job_name) || `Job run ${run.id}`;
        const hasSid = !!run.session_id;
        const isPending = run.status === 'pending';
        const triggerBadge = oneOff
            ? '<span class="live-job-badge live-job-badge-api">one-off</span>'
            : '<span class="live-job-badge live-job-badge-cron">cron</span>';

        // Status dot
        const dotClass = isPending ? 'status-dot-stale' : 'status-dot-working';

        // Elapsed time
        const elapsed = run.started_at ? formatElapsed(run.started_at) : 'pending';

        const clickable = hasSid ? '' : ' style="opacity: 0.5; cursor: default;"';
        const onClick = hasSid
            ? `data-on-click="job-live-run" data-session-id="${escapeAttr(run.session_id)}" data-agent-type="${escapeAttr(run.agent_type || 'claude')}"`
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

/** The Jobs count is the jobs (a running job is one of them, not another): recounted whenever the list is drawn. */
export function updateJobsSectionCount() {
    const jobsCount = document.getElementById('jobs-count');
    if (!jobsCount) return;
    const scheduledList = document.getElementById('scheduled-jobs-list');
    jobsCount.textContent = scheduledList ? scheduledList.querySelectorAll('li:not(.empty-state)').length : 0;
}

// A running job's row opens its agent (#3645).
registerActions({
    'job-live-run': el => { const sid = textOf(el.dataset.sessionId), type = tokenOf(el.dataset.agentType); if (sid && type) window.selectLiveJobRun(sid, type); },
});
