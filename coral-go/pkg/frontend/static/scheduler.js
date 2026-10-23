/* Scheduler: scheduled jobs sidebar list, job detail view, and CRUD operations */

import { state } from './state.js';
import { escapeHtml, escapeAttr, showToast, showView } from './utils.js';
import { registerActions, withId, uuidOf } from './actions.js';
import { pollEvery } from './overload.js';
import { changedFields, NOTHING_TO_SAVE } from './settings_diff.js';
import { ensureBuilt } from './lazy_dom.js';
import { formatDuration as fmtDuration } from './format.js';
import { runFailure, runFailureTag } from './run_failure.js';
import { cronWords, atWords, awayWords, whenWords, resultWords, reasonWords, lastWords, limitWords } from './job_words.js';
import { updateJobsSectionCount } from './live_jobs.js';

let scheduledJobs = [];
let selectedJobId = null;
let editingJobId = null;  // non-null when editing an existing job
let currentJobType = 'workflow'; // 'workflow' or 'prompt'
// What the editor showed for the job it opened (#4423): Save sends only what the person changed since.
let openedJob = null;

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
        list.innerHTML = '<li class="empty-state">No jobs yet. New job schedules one.</li>';
        updateJobsSectionCount();
        return;
    }
    // One list of every job (#6521): its schedule, its last result and its next run in words.
    list.innerHTML = scheduledJobs.map(job => {
        const active = selectedJobId === job.id ? 'active' : '';
        const lastStatus = job.last_run ? job.last_run.status : '';
        const dot = lastStatus === 'completed' ? 'ok'
            : lastStatus === 'running' ? 'run'
            : lastStatus === 'failed' || lastStatus === 'killed' ? 'bad'
            : 'none';
        const dotWords = lastStatus ? `Last run ${lastStatus}` : 'Not run yet'; // the dot is a coloured circle: its words say what it means
        const paused = job.enabled ? '' : `<span class="sched-chip">${job.paused_reason ? 'paused: failing' : 'paused'}</span>`;
        // A job whose scheduling works but whose every run fails otherwise looks identical to a healthy one here.
        const fails = job.consecutive_failures || 0;
        const failBadge = fails >= 2
            ? `<span class="sched-fail-badge" title="The last ${escapeAttr(fails)} runs all failed">${fails} failed</span>`
            : '';
        const meta = [cronWords(job.cron_expr), lastWords(job.last_run), job.enabled && job.next_fire_at ? `next ${awayWords(job.next_fire_at)}` : ''].filter(Boolean).join(' · ');
        const nextTitle = job.enabled && job.next_fire_at ? `Next run ${atWords(job.next_fire_at)} your time` : '';
        return `<li class="session-list-item sched-row ${escapeAttr(active)}" data-on-click="job-select" data-job-id="${Number(job.id)}" title="${escapeAttr(nextTitle)}">
            <span class="sched-row-head"><span class="sched-dot sched-dot-${dot}" role="img" aria-label="${escapeAttr(dotWords)}" title="${escapeAttr(dotWords)}"></span> <span class="sched-row-name">${escapeHtml(job.name)}</span> ${paused} ${failBadge}</span>
            <span class="sched-row-meta">${escapeHtml(meta)}</span>
        </li>`;
    }).join('');
    updateJobsSectionCount();
}

// ── Job detail view ──────────────────────────────────────────────────────

let workflowsById = null; // the workflows a job can run, by id, for the name and the step count

async function loadWorkflowsById() {
    try {
        const resp = await fetch('/api/workflows');
        const data = await resp.json();
        workflowsById = new Map((data.workflows || []).map(w => [Number(w.id), w]));
    } catch { workflowsById = workflowsById || new Map(); }
    return workflowsById;
}

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

    const [runs] = await Promise.all([fetchJobRuns(jobId), job.workflow_id ? loadWorkflowsById() : null]);
    renderJobDetail(job, runs);
    if (window.innerWidth <= 767) document.getElementById('scheduler-detail')?.scrollIntoView({ block: 'start' }); // the list is above it on a phone: show the job that was picked (#6139)
}

function renderJobDetail(job, runs) {
    const container = document.getElementById('scheduler-detail');
    if (!container) return;

    const isWorkflow = !!job.workflow_id;
    const wf = isWorkflow && workflowsById ? workflowsById.get(Number(job.workflow_id)) : null;
    const wfName = wf ? wf.name : (job.workflow_name || `workflow ${job.workflow_id}`);
    const doesHtml = isWorkflow
        ? `Workflow <a href="#" data-on-click="job-workflow" data-workflow-id="${Number(job.workflow_id)}" class="sched-link">${escapeHtml(wfName)}</a>${wf && Array.isArray(wf.steps) ? ` (${wf.steps.length} step${wf.steps.length === 1 ? '' : 's'})` : ''}`
        : `${escapeHtml(AGENT_NAMES[job.agent_type] || job.agent_type || 'Agent')} agent${job.repo_path ? ` in <span class="sched-path">${escapeHtml(job.repo_path)}</span>` : ''}`;

    // Run history: for workflow jobs, show workflow runs; for agent jobs, show scheduled runs
    let runsHtml = '';
    if (isWorkflow && job.workflow_runs && job.workflow_runs.length) {
        runsHtml = renderWorkflowRunsTable(job.workflow_runs);
    } else if (runs.length) {
        runsHtml = renderRunsTable(runs);
    } else {
        runsHtml = '<p class="empty-state">No runs yet</p>';
    }

    // The failure comes first (#6521): a job that fires on schedule and fails every time otherwise looks fine until the run history is opened.
    const fails = job.consecutive_failures || 0;
    const last = job.last_run || null;
    // A run refused for want of a sign-in does not count as a failure (the job keeps its schedule and runs again once signed in), so it has its own banner (#5471).
    const noSignIn = runFailure(last);
    const why = reasonWords(last);
    const when = last && (last.finished_at || last.started_at) ? ` ${whenWords(last.finished_at || last.started_at)}` : '';
    const failBanner = noSignIn
        ? `<div class="sched-fail-banner" role="status">
             <strong>The last run did not start: ${escapeHtml(noSignIn.text)}.</strong>
             ${escapeHtml(noSignIn.hint)} This job keeps its schedule and ${escapeHtml(noSignIn.again)}.
           </div>`
        : fails >= 1
        ? `<div class="sched-fail-banner" role="status">
             <strong>${fails === 1 ? 'The last run failed' : `The last ${fails} runs all failed`}</strong>${escapeHtml(when)}${why ? `: ${escapeHtml(why)}` : ''}. It is still scheduled.
           </div>`
        : '';

    const settingsRows = [
        job.repo_path ? ['Folder', `<span class="sched-path">${escapeHtml(job.repo_path)}</span>`] : null,
        !isWorkflow ? ['Base branch', escapeHtml(job.base_branch || 'main')] : null,
        ['Time limit', escapeHtml(limitWords(job.max_duration_s) || `${job.max_duration_s} seconds`)],
        !isWorkflow ? ['Cleanup worktree', job.cleanup_worktree ? 'Yes' : 'No'] : null,
        job.flags ? ['Flags', `<code>${escapeHtml(job.flags)}</code>`] : null,
    ].filter(Boolean);

    const moreId = `sched-more-${Number(job.id)}`;
    container.innerHTML = `
        ${failBanner}
        <div class="sched-header">
            <h2>${escapeHtml(job.name)}</h2>
            ${job.enabled ? '' : '<span class="sched-chip">paused</span>'}
            <div class="sched-actions">
                <button class="btn btn-small" data-on-click="job-edit" data-job-id="${Number(job.id)}">Edit</button>
                <button class="btn btn-small" data-on-click="job-toggle" data-job-id="${Number(job.id)}">${job.enabled ? 'Pause' : 'Enable'}</button>
                <div class="sched-more">
                    <button class="btn btn-small sched-more-btn" type="button" data-on-click="job-more" aria-expanded="false" aria-controls="${escapeAttr(moreId)}" aria-label="More for ${escapeAttr(job.name)}">More</button>
                    <div class="sched-more-menu" id="${escapeAttr(moreId)}" hidden>
                        <button class="btn btn-small btn-warning sched-delete" type="button" data-on-click="job-delete" data-job-id="${Number(job.id)}" aria-label="Delete ${escapeAttr(job.name)}">Delete</button>
                    </div>
                </div>
            </div>
        </div>
        ${job.description ? `<p class="sched-desc">${escapeHtml(job.description)}</p>` : ''}
        <dl class="sched-facts">
            <dt>Schedule</dt><dd title="${escapeAttr(job.cron_expr)}">${escapeHtml(cronWords(job.cron_expr))}${job.timezone ? ` (${escapeHtml(job.timezone)})` : ''}</dd>
            ${job.enabled
                ? (job.next_fire_at ? `<dt>Next</dt><dd title="${escapeAttr(awayWords(job.next_fire_at))}">${escapeHtml(atWords(job.next_fire_at))} your time</dd>` : '<dt>Next</dt><dd>not scheduled</dd>')
                : `<dt>Paused</dt><dd>${job.paused_reason ? `${escapeHtml(job.paused_reason)}. Fix the cause, then Enable to resume.` : 'Enable it to run on schedule.'}</dd>`}
            <dt>Does</dt><dd>${doesHtml}</dd>
        </dl>
        <details class="disclosure sched-settings">
            <summary>Settings: ${isWorkflow ? 'folder, time limit' : 'folder, branch, time limit'}</summary>
            <dl class="sched-facts">${settingsRows.map(([k, v]) => `<dt>${k}</dt><dd>${v}</dd>`).join('')}</dl>
        </details>
        ${!isWorkflow && job.prompt ? `<details class="disclosure sched-prompt-section">
            <summary>Prompt</summary>
            <pre class="sched-prompt">${escapeHtml(job.prompt)}</pre>
        </details>` : ''}
        <div class="sched-runs-section">
            <h3>Runs</h3>
            ${runsHtml}
        </div>
    `;
}

const AGENT_NAMES = { claude: 'Claude', codex: 'Codex', agy: 'Antigravity', antigravity: 'Antigravity', gemini: 'Antigravity', pi: 'Pi' };

function resultCell(r) {
    const cls = r.status === 'completed' ? 'status-ok' : r.status === 'failed' || r.status === 'killed' ? 'status-err' : r.status === 'running' ? 'status-running' : '';
    return `<span class="sched-status ${cls}">${escapeHtml(resultWords(r))}</span>${runFailureTag(r)}`;
}
const tookOf = (r) => (r.started_at && r.finished_at ? formatDuration(new Date(r.finished_at) - new Date(r.started_at)) : r.started_at ? 'running...' : '-');

// The runs of a job, one table for both kinds (#6521): when, what came of it in words, how long it took.
function renderWorkflowRunsTable(runs) {
    return `<table class="sched-runs-table">
        <thead><tr><th>When</th><th>Result</th><th>Took</th></tr></thead>
        <tbody>${runs.map(r => `<tr data-on-click="job-run" data-run-id="${Number(r.id)}" style="cursor:pointer">
                <td>${escapeHtml(whenWords(r.started_at || r.created_at) || '-')}</td>
                <td>${resultCell(r)}</td>
                <td>${escapeHtml(tookOf(r))}</td>
            </tr>`).join('')}</tbody>
    </table>`;
}

function renderRunsTable(runs) {
    return `<table class="sched-runs-table">
        <thead><tr><th>When</th><th>Result</th><th>Took</th></tr></thead>
        <tbody>${runs.map(r => {
            const sessionLink = r.session_id
                ? ` <a href="#" class="run-session-link" title="Open the conversation" data-on-click="job-run-session" data-session-id="${escapeAttr(r.session_id)}">Open the conversation</a>`
                : '';
            return `<tr>
                <td>${escapeHtml(whenWords(r.scheduled_at || r.started_at) || '-')}</td>
                <td>${resultCell(r)}${sessionLink}</td>
                <td>${escapeHtml(tookOf(r))}</td>
            </tr>`;
        }).join('')}</tbody>
    </table>`;
}

const formatDuration = ms => fmtDuration(ms);

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

// A choice button's on state: the look (active) and what a screen reader hears (aria-pressed), set together (#6348).
const press = (b, on) => { b.classList.toggle('active', on); b.setAttribute('aria-pressed', String(!!on)); };

export function pickSchedulePreset(btn) {
    document.querySelectorAll('.sched-preset').forEach(b => press(b, b === btn));
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
        press(btn, btn.dataset.type === type);
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
            workflows.map(w => `<option value="${Number(w.id)}">${escapeHtml(w.name)}${w.description ? ' — ' + escapeHtml(w.description) : ''}</option>`).join('');
    } catch (e) {
        select.innerHTML = '<option value="">Failed to load workflows</option>';
    }
}

export function showJobModal() {
    ensureBuilt('job-modal'); // built on first open (#4500)
    editingJobId = null;
    openedJob = null;
    document.getElementById('job-modal-title').textContent = 'New job';
    document.getElementById('job-modal-submit').textContent = 'Create Job';
    document.getElementById('job-modal').style.display = 'flex';
    document.getElementById('job-modal-name').value = '';
    document.getElementById('job-modal-description').value = '';
    document.getElementById('job-modal-cron').value = '0 * * * *';
    document.getElementById('job-modal-timezone').value = 'UTC';
    // Reset schedule picker to first preset
    document.querySelectorAll('.sched-preset').forEach((b, i) => press(b, i === 0));
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

export async function editScheduledJob(jobId) {
    // The job as the server has it now, not the list this page last polled (#4423).
    let job = null;
    try {
        const resp = await fetch(`/api/scheduled/jobs/${encodeURIComponent(jobId)}`);
        if (resp.ok) job = await resp.json();
    } catch { /* below */ }
    if (!job || job.error) { showToast('Could not load the job. Try again.', true); return; }

    editingJobId = jobId;
    ensureBuilt('job-modal');
    document.getElementById('job-modal-title').textContent = 'Edit job';
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
    // The job's own type, and its workflow selected (the editor kept whatever the last one left).
    const type = job.job_type || (job.workflow_id ? 'workflow' : 'prompt');
    switchJobType(type);
    if (type === 'workflow') {
        await _loadWorkflowOptions();
        document.getElementById('job-modal-workflow').value = job.workflow_id ? String(job.workflow_id) : '';
    }
    openedJob = jobFormValues();
    validateCronPreview();
}

/** jobFormValues is what the editor's fields hold, as the job's fields (those of the type it shows). */
function jobFormValues() {
    const v = id => document.getElementById(id).value;
    const values = {
        name: v('job-modal-name').trim(),
        description: v('job-modal-description').trim(),
        cron_expr: v('job-modal-cron').trim(),
        timezone: v('job-modal-timezone').trim() || 'UTC',
        max_duration_s: parseInt(v('job-modal-timeout')) || 3600,
        cleanup_worktree: document.getElementById('job-modal-cleanup').checked ? 1 : 0,
        job_type: currentJobType,
    };
    if (currentJobType === 'workflow') {
        const wfId = v('job-modal-workflow');
        values.workflow_id = wfId ? parseInt(wfId) : null;
    } else {
        values.repo_path = v('job-modal-repo').trim();
        values.base_branch = v('job-modal-branch').trim() || 'main';
        values.agent_type = v('job-modal-agent');
        values.prompt = v('job-modal-prompt').trim();
        values.flags = v('job-modal-flags').trim();
    }
    return values;
}

export function hideJobModal() {
    const modal = document.getElementById('job-modal');
    if (modal) modal.style.display = 'none';
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
        document.getElementById('job-modal-cron').toggleAttribute('aria-invalid', !data.valid);
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
        // the field that is missing is marked and has focus; typing clears the mark (#6348)
        const field = document.getElementById(!name ? 'job-modal-name' : 'job-modal-cron');
        const custom = document.querySelector('.sched-preset[data-cron=""]');
        if (name && custom) pickSchedulePreset(custom); // the cron field is behind the Custom choice: choose it, which shows the field
        field.setAttribute('aria-invalid', 'true');
        field.addEventListener('input', () => field.removeAttribute('aria-invalid'), { once: true });
        field.focus();
        return;
    }

    const values = jobFormValues();
    if (currentJobType === 'workflow') {
        if (!values.workflow_id) {
            showToast('Please select a workflow', true);
            return;
        }
    } else if (!values.repo_path || !values.prompt) {
        showToast('Repo path and prompt are required for agent jobs', true);
        return;
    }

    try {
        const isEdit = editingJobId !== null;
        // An edit sends only what the person changed since the editor opened, so a field another client changed
        // meanwhile keeps that change (#4423); a new job sends everything.
        const body = isEdit && openedJob ? changedFields(openedJob, values) : values;
        if (isEdit && !Object.keys(body).length) {
            showToast(NOTHING_TO_SAVE);
            hideJobModal();
            return;
        }
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

function toggleJobMore(btn, open) {
    const menu = document.getElementById(btn.getAttribute('aria-controls'));
    if (!menu) return;
    const to = open === undefined ? menu.hidden : open;
    menu.hidden = !to;
    btn.setAttribute('aria-expanded', String(to));
}

export function initScheduler() {
    // A click elsewhere and Escape close an open More (it is redrawn with the page, never kept)
    document.addEventListener('click', (e) => { for (const b of document.querySelectorAll('.sched-more-btn[aria-expanded="true"]')) if (!b.parentElement.contains(e.target)) toggleJobMore(b, false); });
    document.addEventListener('focusout', (e) => { const b = document.querySelector('.sched-more-btn[aria-expanded="true"]'); if (b && e.relatedTarget && !b.parentElement.contains(e.relatedTarget)) toggleJobMore(b, false); }); // Tab out of More closes it
    document.addEventListener('keydown', (e) => { if (e.key !== 'Escape') return; const b = document.querySelector('.sched-more-btn[aria-expanded="true"]'); if (b) { toggleJobMore(b, false); b.focus(); e.stopPropagation(); } }, true);
    fetchJobs();
    // Refresh jobs every 30s
    pollEvery(fetchJobs, 30000, { whenHidden: true }); // job notifications
}

// The scheduled jobs' markup (#3645): data-on-* names, never code.
// Ids are checked (idOf, uuidOf) before use: a bad one does nothing. Deleting still asks first.
registerActions({
    'job-new': () => showJobModal(),
    'job-select': el => withId(el.dataset.jobId, selectScheduledJob),
    'job-edit': el => withId(el.dataset.jobId, editScheduledJob),
    'job-toggle': el => withId(el.dataset.jobId, toggleScheduledJob),
    'job-delete': el => withId(el.dataset.jobId, deleteScheduledJob),
    'job-more': el => toggleJobMore(el),
    'job-workflow': (el, event) => { event.preventDefault(); withId(el.dataset.workflowId, id => window.selectWorkflow(id)); },
    'job-run': el => withId(el.dataset.runId, id => window.selectWorkflowRun(id)),
    'job-run-session': (el, event) => { event.preventDefault(); const sid = uuidOf(el.dataset.sessionId); if (sid) window.selectHistorySession(sid); },
});
