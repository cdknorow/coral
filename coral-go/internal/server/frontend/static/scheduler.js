/* Scheduler: scheduled jobs sidebar list, job detail view, and CRUD operations */

import { state } from './state.js';
import { escapeHtml, showToast, showView } from './utils.js';

let scheduledJobs = [];
let selectedJobId = null;
let editingJobId = null;  // non-null when editing an existing job
let currentJobType = 'workflow'; // 'workflow' or 'prompt'

// ── API helpers ──────────────────────────────────────────────────────────

async function fetchJobs() {
    try {
        const resp = await fetch('/api/scheduled/jobs');
        const data = await resp.json();
        scheduledJobs = data.jobs || [];
        renderJobsSidebar();
    } catch (e) {
        console.error('Failed to fetch scheduled jobs:', e);
    }
}

async function fetchJobRuns(jobId) {
    try {
        const resp = await fetch(`/api/scheduled/jobs/${jobId}/runs?limit=20`);
        const data = await resp.json();
        return data.runs || [];
    } catch (e) {
        console.error('Failed to fetch runs:', e);
        return [];
    }
}

// ── Sidebar rendering ────────────────────────────────────────────────────

function renderJobsSidebar() {
    const list = document.getElementById('scheduled-jobs-list');
    if (!list) return;
    if (!scheduledJobs.length) {
        list.innerHTML = '<li class="empty-state">No scheduled jobs</li>';
        return;
    }
    list.innerHTML = scheduledJobs.map(job => {
        const active = selectedJobId === job.id ? 'active' : '';
        const status = job.enabled ? '' : ' (paused)';
        const lastStatus = job.last_run ? job.last_run.status : '';
        const dot = lastStatus === 'completed' ? '🟢'
            : lastStatus === 'running' ? '🔵'
            : lastStatus === 'failed' || lastStatus === 'killed' ? '🔴'
            : '⚪';
        const isWorkflow = !!job.workflow_id;
        const typeIcon = isWorkflow
            ? '<span class="material-icons sched-type-icon" style="font-size:13px;color:#d2a8ff" title="Workflow">account_tree</span>'
            : '';
        // A job whose scheduling works but whose every run fails otherwise looks
        // identical to a healthy one here — enabled, with a next fire time.
        const fails = job.consecutive_failures || 0;
        const failBadge = fails >= 2
            ? `<span class="sched-fail-badge" title="The last ${fails} runs all failed">${fails} failed</span>`
            : '';
        return `<li class="session-list-item ${active}" onclick="selectScheduledJob(${job.id})">
            <span class="sched-dot">${dot}</span>
            ${typeIcon}
            <span class="session-name">${escapeHtml(job.name)}${status}</span>
            ${failBadge}
            <span class="sched-cron" style="font-size:10px;color:var(--text-muted);margin-left:auto">${escapeHtml(job.cron_expr)}</span>
        </li>`;
    }).join('');
}

// ── Job detail view ──────────────────────────────────────────────────────

export async function selectScheduledJob(jobId) {
    selectedJobId = jobId;
    renderJobsSidebar();

    const job = scheduledJobs.find(j => j.id === jobId);
    if (!job) return;

    // Hide other views, show scheduler view
    showView("scheduler-view");

    // Deselect live/history sessions
    state.currentSession = null;
    document.querySelectorAll('.session-list-item.active').forEach(el => {
        if (!el.closest('#scheduled-jobs-list')) el.classList.remove('active');
    });

    const runs = await fetchJobRuns(jobId);
    renderJobDetail(job, runs);
}

function renderJobDetail(job, runs) {
    const container = document.getElementById('scheduler-detail');
    if (!container) return;

    const enabledLabel = job.enabled ? 'Enabled' : 'Paused';
    const toggleLabel = job.enabled ? 'Pause' : 'Enable';
    const nextFire = job.next_fire_at ? new Date(job.next_fire_at).toLocaleString() : 'N/A';
    const isWorkflow = !!job.workflow_id;

    // Workflow-specific info or agent info
    let jobTypeHtml = '';
    if (isWorkflow) {
        const wfName = job.workflow_name || `Workflow #${job.workflow_id}`;
        jobTypeHtml = `
            <dt>Type</dt><dd><span class="material-icons" style="font-size:14px;vertical-align:-2px;color:#d2a8ff">account_tree</span> Workflow</dd>
            <dt>Workflow</dt><dd><a href="javascript:void(0)" onclick="selectWorkflow(${job.workflow_id})" style="color:var(--accent)">${escapeHtml(wfName)}</a></dd>`;
    } else {
        jobTypeHtml = `
            <dt>Type</dt><dd>Agent Job</dd>
            <dt>Agent</dt><dd>${escapeHtml(job.agent_type)}</dd>`;
    }

    // Run history: for workflow jobs, show workflow runs; for agent jobs, show scheduled runs
    let runsHtml = '';
    if (isWorkflow && job.workflow_runs && job.workflow_runs.length) {
        runsHtml = renderWorkflowRunsTable(job.workflow_runs);
    } else if (runs.length) {
        runsHtml = renderRunsTable(runs);
    } else {
        runsHtml = '<p class="empty-state">No runs yet</p>';
    }

    // Surface a job that fires on schedule and fails every time. Without this
    // the only evidence is inside the run history table, which nobody opens
    // for a job that appears to be running fine.
    const fails = job.consecutive_failures || 0;
    const lastErr = job.last_run && job.last_run.error_msg ? job.last_run.error_msg : '';
    const failBanner = fails >= 1
        ? `<div class="sched-fail-banner" role="status">
             <strong>${fails === 1 ? 'The last run failed.' : `The last ${fails} runs all failed.`}</strong>
             This job is still firing on schedule; its runs are not completing.
             ${lastErr ? `<div class="sched-fail-error"><code>${escapeHtml(lastErr)}</code></div>` : ''}
           </div>`
        : '';

    container.innerHTML = `
        ${failBanner}
        <div class="sched-header">
            <h2>${escapeHtml(job.name)}</h2>
            <div class="sched-actions">
                <button class="btn btn-small" onclick="editScheduledJob(${job.id})">Edit</button>
                <button class="btn btn-small" onclick="toggleScheduledJob(${job.id})">${toggleLabel}</button>
                <button class="btn btn-small btn-warning" onclick="deleteScheduledJob(${job.id})">Delete</button>
            </div>
        </div>
        ${job.description ? `<p class="sched-desc">${escapeHtml(job.description)}</p>` : ''}
        <dl class="info-grid" style="margin-bottom:16px">
            <dt>Schedule</dt><dd><code>${escapeHtml(job.cron_expr)}</code> (${escapeHtml(job.timezone)})</dd>
            <dt>Status</dt><dd>${enabledLabel}</dd>
            <dt>Next Run</dt><dd>${nextFire}</dd>
            ${jobTypeHtml}
            <dt>Repo</dt><dd style="word-break:break-all">${escapeHtml(job.repo_path)}</dd>
            <dt>Branch</dt><dd>${escapeHtml(job.base_branch || 'main')}</dd>
            <dt>Timeout</dt><dd>${job.max_duration_s}s</dd>
            <dt>Cleanup Worktree</dt><dd>${job.cleanup_worktree ? 'Yes' : 'No'}</dd>
            ${job.flags ? `<dt>Flags</dt><dd><code>${escapeHtml(job.flags)}</code></dd>` : ''}
        </dl>
        ${!isWorkflow && job.prompt ? `<div class="sched-prompt-section">
            <h3>Prompt</h3>
            <pre class="sched-prompt">${escapeHtml(job.prompt)}</pre>
        </div>` : ''}
        <div class="sched-runs-section">
            <h3>Run History</h3>
            ${runsHtml}
        </div>
    `;
}

function renderWorkflowRunsTable(runs) {
    return `<table class="sched-runs-table">
        <thead><tr><th>Run</th><th>Status</th><th>Steps</th><th>Started</th><th>Duration</th></tr></thead>
        <tbody>${runs.map(r => {
            const statusClass = r.status === 'completed' ? 'status-ok'
                : r.status === 'failed' || r.status === 'killed' ? 'status-err'
                : r.status === 'running' ? 'status-running'
                : '';
            const started = r.started_at ? new Date(r.started_at).toLocaleString() : '-';
            const duration = r.started_at && r.finished_at
                ? formatDuration(new Date(r.finished_at) - new Date(r.started_at))
                : r.started_at ? 'running...' : '-';
            const stepsInfo = r.current_step != null ? `${r.current_step + 1}` : '-';
            return `<tr onclick="selectWorkflowRun(${r.id})" style="cursor:pointer">
                <td>#${r.id}</td>
                <td><span class="sched-status ${statusClass}">${r.status}</span></td>
                <td>${stepsInfo}</td>
                <td>${started}</td>
                <td>${duration}</td>
            </tr>`;
        }).join('')}</tbody>
    </table>`;
}

function renderRunsTable(runs) {
    return `<table class="sched-runs-table">
        <thead><tr><th>Scheduled</th><th>Status</th><th>Duration</th><th>Exit</th><th>Session</th></tr></thead>
        <tbody>${runs.map(r => {
            const scheduled = new Date(r.scheduled_at).toLocaleString();
            const duration = r.started_at && r.finished_at
                ? formatDuration(new Date(r.finished_at) - new Date(r.started_at))
                : '-';
            const statusClass = r.status === 'completed' ? 'status-ok'
                : r.status === 'failed' || r.status === 'killed' ? 'status-err'
                : r.status === 'running' ? 'status-running'
                : '';
            const sessionLink = r.session_id
                ? `<a href="javascript:void(0)" class="run-session-link" title="View session history" onclick="selectHistorySession('${r.session_id}')">${r.session_id.substring(0, 8)}</a>`
                : '-';
            return `<tr>
                <td>${scheduled}</td>
                <td><span class="sched-status ${statusClass}">${r.status}</span></td>
                <td>${duration}</td>
                <td>${escapeHtml(r.exit_reason || '-')}</td>
                <td>${sessionLink}</td>
            </tr>`;
        }).join('')}</tbody>
    </table>`;
}

function formatDuration(ms) {
    const s = Math.floor(ms / 1000);
    if (s < 60) return `${s}s`;
    const m = Math.floor(s / 60);
    if (m < 60) return `${m}m ${s % 60}s`;
    const h = Math.floor(m / 60);
    return `${h}h ${m % 60}m`;
}

// ── CRUD operations ──────────────────────────────────────────────────────

export async function toggleScheduledJob(jobId) {
    try {
        await fetch(`/api/scheduled/jobs/${jobId}/toggle`, { method: 'POST' });
        await fetchJobs();
        if (selectedJobId === jobId) selectScheduledJob(jobId);
    } catch (e) {
        showToast('Failed to toggle job', true);
    }
}

export function deleteScheduledJob(jobId) {
    window.showConfirmModal('Delete Job', 'Delete this scheduled job and all its run history?', async () => {
        try {
            await fetch(`/api/scheduled/jobs/${jobId}`, { method: 'DELETE' });
            showToast('Job deleted');
            selectedJobId = null;
            showView("welcome-screen");
            await fetchJobs();
        } catch (e) {
            showToast('Failed to delete job', true);
        }
    });
}

// ── Job create/edit modal ────────────────────────────────────────────────

export function pickSchedulePreset(btn) {
    document.querySelectorAll('.sched-preset').forEach(b => b.classList.remove('active'));
    btn.classList.add('active');
    const cron = btn.dataset.cron;
    const cronInput = document.getElementById('job-modal-cron');
    const customRow = document.getElementById('sched-custom-row');
    if (cron === '') {
        // Custom — show cron input
        if (customRow) customRow.style.display = '';
        if (cronInput) cronInput.focus();
    } else {
        if (customRow) customRow.style.display = 'none';
        if (cronInput) cronInput.value = cron;
    }
    validateCronPreview();
}

export function switchJobType(type) {
    currentJobType = type;
    document.querySelectorAll('.job-type-btn').forEach(btn => {
        btn.classList.toggle('active', btn.dataset.type === type);
    });
    const wfFields = document.getElementById('job-modal-workflow-fields');
    const promptFields = document.getElementById('job-modal-prompt-fields');
    if (wfFields) wfFields.style.display = type === 'workflow' ? '' : 'none';
    if (promptFields) promptFields.style.display = type === 'prompt' ? '' : 'none';
}

async function _loadWorkflowOptions() {
    const select = document.getElementById('job-modal-workflow');
    if (!select) return;
    try {
        const resp = await fetch('/api/workflows');
        const data = await resp.json();
        const workflows = data.workflows || [];
        select.innerHTML = '<option value="">Select a workflow...</option>' +
            workflows.map(w => `<option value="${w.id}">${escapeHtml(w.name)}${w.description ? ' — ' + escapeHtml(w.description) : ''}</option>`).join('');
    } catch (e) {
        select.innerHTML = '<option value="">Failed to load workflows</option>';
    }
}

export function showJobModal() {
    editingJobId = null;
    document.getElementById('job-modal-title').textContent = 'New Scheduled Job';
    document.getElementById('job-modal-submit').textContent = 'Create Job';
    document.getElementById('job-modal').style.display = 'flex';
    document.getElementById('job-modal-name').value = '';
    document.getElementById('job-modal-description').value = '';
    document.getElementById('job-modal-cron').value = '0 * * * *';
    document.getElementById('job-modal-timezone').value = 'UTC';
    // Reset schedule picker to first preset
    document.querySelectorAll('.sched-preset').forEach((b, i) => b.classList.toggle('active', i === 0));
    const customRow = document.getElementById('sched-custom-row');
    if (customRow) customRow.style.display = 'none';
    document.getElementById('job-modal-repo').value = document.getElementById('job-modal-repo').dataset.coralRoot || '';
    document.getElementById('job-modal-branch').value = 'main';
    document.getElementById('job-modal-agent').value = 'claude';
    document.getElementById('job-modal-prompt').value = '';
    document.getElementById('job-modal-timeout').value = '3600';
    document.getElementById('job-modal-cleanup').checked = true;
    switchJobType('workflow');
    _loadWorkflowOptions();
    document.getElementById('job-modal-flags').value = '';
    document.getElementById('cron-preview').innerHTML = '';
    validateCronPreview();
}

export function editScheduledJob(jobId) {
    const job = scheduledJobs.find(j => j.id === jobId);
    if (!job) return;

    editingJobId = jobId;
    document.getElementById('job-modal-title').textContent = 'Edit Scheduled Job';
    document.getElementById('job-modal-submit').textContent = 'Save Changes';
    document.getElementById('job-modal').style.display = 'flex';
    document.getElementById('job-modal-name').value = job.name;
    document.getElementById('job-modal-description').value = job.description || '';
    document.getElementById('job-modal-cron').value = job.cron_expr;
    document.getElementById('job-modal-timezone').value = job.timezone || 'UTC';
    document.getElementById('job-modal-repo').value = job.repo_path;
    document.getElementById('job-modal-branch').value = job.base_branch || 'main';
    document.getElementById('job-modal-agent').value = job.agent_type || 'claude';
    document.getElementById('job-modal-prompt').value = job.prompt;
    document.getElementById('job-modal-timeout').value = String(job.max_duration_s || 3600);
    document.getElementById('job-modal-cleanup').checked = !!job.cleanup_worktree;
    document.getElementById('job-modal-flags').value = job.flags || '';
    document.getElementById('cron-preview').innerHTML = '';
    // Sync flag button active states
    document.getElementById('job-modal-flags').dispatchEvent(new Event('input'));
    validateCronPreview();
}

export function hideJobModal() {
    document.getElementById('job-modal').style.display = 'none';
}

export async function validateCronPreview() {
    const expr = document.getElementById('job-modal-cron').value.trim();
    const tz = document.getElementById('job-modal-timezone').value.trim() || 'UTC';
    const preview = document.getElementById('cron-preview');
    if (!expr) { preview.innerHTML = ''; return; }

    try {
        const resp = await fetch('/api/scheduled/validate-cron', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ cron_expr: expr, timezone: tz }),
        });
        const data = await resp.json();
        if (data.valid) {
            preview.innerHTML = '<span style="color:var(--green)">Valid</span> — Next: ' +
                data.next_fire_times.slice(0, 3).map(t => new Date(t).toLocaleString()).join(', ');
        } else {
            preview.innerHTML = `<span style="color:var(--red)">Invalid:</span> ${escapeHtml(data.error || 'bad expression')}`;
        }
    } catch (e) {
        preview.innerHTML = '<span style="color:var(--red)">Error validating</span>';
    }
}

export async function saveScheduledJob() {
    const name = document.getElementById('job-modal-name').value.trim();
    const cron = document.getElementById('job-modal-cron').value.trim();

    if (!name || !cron) {
        showToast('Name and cron expression are required', true);
        return;
    }

    const body = {
        name: name,
        description: document.getElementById('job-modal-description').value.trim(),
        cron_expr: cron,
        timezone: document.getElementById('job-modal-timezone').value.trim() || 'UTC',
        max_duration_s: parseInt(document.getElementById('job-modal-timeout').value) || 3600,
        cleanup_worktree: document.getElementById('job-modal-cleanup').checked ? 1 : 0,
        job_type: currentJobType,
    };

    if (currentJobType === 'workflow') {
        const wfId = document.getElementById('job-modal-workflow').value;
        if (!wfId) {
            showToast('Please select a workflow', true);
            return;
        }
        body.workflow_id = parseInt(wfId);
    } else {
        body.repo_path = document.getElementById('job-modal-repo').value.trim();
        body.base_branch = document.getElementById('job-modal-branch').value.trim() || 'main';
        body.agent_type = document.getElementById('job-modal-agent').value;
        body.prompt = document.getElementById('job-modal-prompt').value.trim();
        body.flags = document.getElementById('job-modal-flags').value.trim();
        if (!body.repo_path || !body.prompt) {
            showToast('Repo path and prompt are required for agent jobs', true);
            return;
        }
    }

    try {
        const isEdit = editingJobId !== null;
        const url = isEdit ? `/api/scheduled/jobs/${editingJobId}` : '/api/scheduled/jobs';
        const method = isEdit ? 'PUT' : 'POST';

        const resp = await fetch(url, {
            method,
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body),
        });
        const data = await resp.json();
        if (data.error) {
            showToast(data.error, true);
            return;
        }
        showToast(isEdit ? 'Job updated' : 'Job created');
        hideJobModal();
        await fetchJobs();
        selectScheduledJob(data.id);
    } catch (e) {
        showToast('Failed to save job', true);
    }
}

// ── Init ─────────────────────────────────────────────────────────────────

export function initScheduler() {
    fetchJobs();
    // Refresh jobs every 30s
    setInterval(fetchJobs, 30000);
}
