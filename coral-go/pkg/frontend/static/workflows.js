/* Workflows: list, detail, run views, and CRUD */

import { escapeHtml as esc, escapeAttr as escAttr, showView, showToast } from './utils.js';
import { apiFetch, loadLiveSessions } from './api.js';
import { selectLiveSession } from './sessions.js';

let workflows = [];
let selectedWorkflowId = null;
let currentRunPollTimer = null;
let editingWorkflowId = null; // non-null when editing

// ── API helpers ────────────────────────────────────────────────────────

async function fetchWorkflows() {
    try {
        const data = await apiFetch('/api/workflows');
        workflows = data.workflows || [];
        renderWorkflowList();
    } catch (e) {
        console.error('Failed to fetch workflows:', e);
    }
}

async function fetchWorkflowDetail(id) {
    try {
        return await apiFetch(`/api/workflows/${id}`);
    } catch (e) {
        console.error('Failed to fetch workflow:', e);
        return null;
    }
}

async function fetchWorkflowRuns(workflowId, limit) {
    try {
        const data = await apiFetch(`/api/workflows/${workflowId}/runs?limit=${limit || 20}`);
        return data.runs || [];
    } catch (e) {
        console.error('Failed to fetch workflow runs:', e);
        return [];
    }
}

async function fetchRunDetail(runId) {
    try {
        return await apiFetch(`/api/workflows/runs/${runId}`);
    } catch (e) {
        console.error('Failed to fetch run:', e);
        return null;
    }
}

// ── Initialization ─────────────────────────────────────────────────────

export function initWorkflows() {
    fetchWorkflows();
}

export function showWorkflowsTab() {
    showView('workflows-view');
    fetchWorkflows();
}

// ── Build with Agent ──────────────────────────────────────────────────

// Workflows are hub-local (definitions, runs and the builder prompt all live on
// the hub), so these agents always launch on the hub itself, in the hub's coral
// root. They intentionally do not use the multi-server launch picker.

export async function launchWorkflowAgent() {
    const btn = document.getElementById('wf-build-agent-btn');
    if (btn) { btn.disabled = true; btn.innerHTML = '<span class="material-icons" style="font-size:16px">hourglass_top</span> Launching\u2026'; }

    try {
        // Fetch the skill prompt
        const doc = await apiFetch('/api/agent-docs/workflow-builder');
        if (!doc || !doc.content) {
            showToast('Workflow builder skill not found', true);
            return;
        }

        // Get default working directory from body data attribute
        const workingDir = document.body.dataset.coralRoot || '';
        if (!workingDir) {
            showToast('Could not determine working directory', true);
            return;
        }

        const resp = await fetch('/api/sessions/launch', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                working_dir: workingDir,
                agent_type: 'claude',
                display_name: 'Workflow Builder',
                prompt: doc.content,
                capabilities: { allow: ['Read', 'Edit', 'Write', 'Bash', 'Glob', 'Grep'] },
            }),
        });

        if (resp.status === 403) {
            showToast('Demo limit reached — stop existing sessions first', true);
            return;
        }

        const result = await resp.json();
        if (result.error) {
            showToast(result.error, true);
            return;
        }

        showToast(`Launched: ${result.session_name}`);

        // Switch to agents tab and navigate to the new session
        setTimeout(async () => {
            await loadLiveSessions();
            if (window.switchNavTab) window.switchNavTab('agents');
            selectLiveSession(result.session_name, 'claude', result.session_id);
        }, 1500);
    } catch (e) {
        showToast('Failed to launch workflow agent', true);
    } finally {
        if (btn) { btn.disabled = false; btn.innerHTML = '<span class="material-icons" style="font-size:16px">smart_toy</span> Build with Agent'; }
    }
}

export async function editWorkflowWithAgent(workflowId) {
    try {
        // Fetch the workflow details and skill prompt in parallel
        const [wfResp, doc] = await Promise.all([
            apiFetch(`/api/workflows/${workflowId}`),
            apiFetch('/api/agent-docs/workflow-builder'),
        ]);
        if (!doc || !doc.content) { showToast('Workflow builder skill not found', true); return; }
        if (!wfResp) { showToast('Could not load workflow', true); return; }

        const workingDir = document.body.dataset.coralRoot || '';
        if (!workingDir) { showToast('Could not determine working directory', true); return; }

        // Build an edit-specific prompt with the current workflow context
        const wfJSON = JSON.stringify(wfResp, null, 2);
        const editPrompt = `${doc.content}

---

## Current Task: Edit an Existing Workflow

The user wants to modify an existing workflow. Here is the current workflow definition (ID: ${workflowId}):

\`\`\`json
${wfJSON}
\`\`\`

**Instructions:**
1. Start by showing the user a summary of this workflow — its name, description, steps, and what each step does
2. Ask what they'd like to change (add/remove/modify steps, change prompts, add hooks, adjust timeouts, etc.)
3. After confirming the changes, update the workflow using: \`curl -X PUT http://localhost:\${CORAL_PORT}/api/workflows/${workflowId} -H 'Content-Type: application/json' -d '...'\`
4. Offer to trigger a test run and help debug if anything fails
5. To update the workflow, use PUT (not POST) since it already exists`;

        const resp = await fetch('/api/sessions/launch', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                working_dir: wfResp.repo_path || workingDir,
                agent_type: 'claude',
                display_name: `Edit: ${wfResp.name}`,
                prompt: editPrompt,
                capabilities: { allow: ['Read', 'Edit', 'Write', 'Bash', 'Glob', 'Grep'] },
            }),
        });

        if (resp.status === 403) { showToast('Demo limit reached — stop existing sessions first', true); return; }
        const result = await resp.json();
        if (result.error) { showToast(result.error, true); return; }

        showToast(`Launched: Edit ${wfResp.name}`);
        setTimeout(async () => {
            await loadLiveSessions();
            if (window.switchNavTab) window.switchNavTab('agents');
            selectLiveSession(result.session_name, 'claude', result.session_id);
        }, 1500);
    } catch (e) {
        showToast('Failed to launch workflow edit agent', true);
    }
}

// ── Workflow list ──────────────────────────────────────────────────────

function renderWorkflowList() {
    const list = document.getElementById('wf-list');
    const empty = document.getElementById('wf-list-empty');
    const badge = document.getElementById('wf-count-badge');
    if (!list) return;

    if (badge) badge.textContent = workflows.length ? `${workflows.length}` : '';

    if (!workflows.length) {
        empty.style.display = '';
        list.innerHTML = '';
        return;
    }
    empty.style.display = 'none';

    list.innerHTML = workflows.map(wf => {
        const enabledBadge = wf.enabled
            ? '<span class="wf-badge wf-badge-enabled">enabled</span>'
            : '<span class="wf-badge wf-badge-disabled">disabled</span>';
        const stepCount = wf.step_count || (wf.steps ? wf.steps.length : 0);

        let lastRunHtml = '';
        if (wf.last_run) {
            const lr = wf.last_run;
            lastRunHtml = `<span class="wf-last-run-status">
                <span class="wf-status-dot ${esc(lr.status)}"></span> ${esc(lr.status)}
                ${lr.started_at ? '&middot; ' + formatTime(lr.started_at) : ''}
            </span>`;
        } else {
            lastRunHtml = '<span style="color:var(--text-muted)">Never run</span>';
        }

        return `<div class="wf-card" onclick="selectWorkflow(${wf.id})">
            <div class="wf-card-top">
                <span class="wf-card-name">${esc(wf.name)}</span>
                <span class="wf-card-badges">
                    <span class="wf-badge wf-badge-steps">${stepCount} step${stepCount !== 1 ? 's' : ''}</span>
                    ${enabledBadge}
                </span>
            </div>
            ${wf.description ? `<div class="wf-card-desc">${esc(wf.description)}</div>` : ''}
            <div class="wf-card-footer">${lastRunHtml}</div>
        </div>`;
    }).join('');
}

// ── Workflow detail ────────────────────────────────────────────────────

export async function selectWorkflow(id) {
    selectedWorkflowId = id;
    stopRunPolling();

    const wf = await fetchWorkflowDetail(id);
    if (!wf) return;

    // Show detail, hide list
    document.getElementById('wf-list-container').style.display = 'none';
    document.getElementById('wf-run-detail-container').style.display = 'none';
    document.getElementById('wf-detail-container').style.display = '';
    document.getElementById('wf-back-btn').style.display = '';
    document.getElementById('wf-create-btn').style.display = 'none';
    document.getElementById('wf-build-agent-btn').style.display = 'none';
    document.getElementById('wf-title').textContent = wf.name;

    const runs = await fetchWorkflowRuns(id, 10);
    renderWorkflowDetail(wf, runs);
}

function renderWorkflowDetail(wf, runs) {
    const container = document.getElementById('wf-detail');
    if (!container) return;

    const steps = wf.steps || [];
    const stepsHtml = steps.map((s, i) => {
        const stepType = s.type || 'shell';
        return `<div class="wf-step-item">
            <span class="wf-step-index">${i}</span>
            <span class="wf-step-name">${esc(s.name)}</span>
            <span class="wf-step-type">${esc(stepType)}</span>
        </div>`;
    }).join('');

    let runsHtml = '';
    if (runs.length) {
        const rowsHtml = runs.map(r => {
            const statusIcon = statusIconHtml(r.status);
            return `<tr onclick="selectWorkflowRun(${r.id})">
                <td>#${r.id}</td>
                <td>${statusIcon} ${esc(r.status)}</td>
                <td>${esc(r.trigger_type || '')}</td>
                <td>${r.started_at ? formatTime(r.started_at) : '—'}</td>
                <td>${r.finished_at ? formatTime(r.finished_at) : '—'}</td>
            </tr>`;
        }).join('');
        runsHtml = `<table class="wf-runs-table">
            <thead><tr><th>Run</th><th>Status</th><th>Trigger</th><th>Started</th><th>Finished</th></tr></thead>
            <tbody>${rowsHtml}</tbody>
        </table>`;
    } else {
        runsHtml = '<p style="color:var(--text-muted);font-size:13px">No runs yet.</p>';
    }

    container.innerHTML = `
        <div class="wf-detail-header">
            <div>
                <h3 class="wf-detail-title">${esc(wf.name)}</h3>
                ${wf.description ? `<div style="color:var(--text-secondary);font-size:13px;margin-top:2px">${esc(wf.description)}</div>` : ''}
            </div>
            <div class="wf-detail-actions">
                <button class="btn btn-sm btn-primary" onclick="triggerWorkflow(${wf.id})">
                    <span class="material-icons" style="font-size:14px">play_arrow</span> Run
                </button>
                <button class="btn btn-sm" onclick="editWorkflow(${wf.id})">Edit</button>
                <button class="btn btn-sm" onclick="editWorkflowWithAgent(${wf.id})">
                    <span class="material-icons" style="font-size:14px">smart_toy</span> Edit with Agent
                </button>
                <button class="btn btn-sm" style="color:var(--error)" onclick="deleteWorkflow(${wf.id}, '${esc(wf.name)}')">Delete</button>
            </div>
        </div>
        <div class="wf-detail-meta">
            <span><span class="material-icons">folder</span> ${esc(wf.repo_path || 'Not set')}</span>
            <span><span class="material-icons">timer</span> Max ${wf.max_duration_s || 3600}s</span>
            <span><span class="material-icons">schedule</span> Created ${formatTime(wf.created_at)}</span>
            ${wf.enabled ? '' : '<span style="color:var(--warning)"><span class="material-icons">pause_circle</span> Disabled</span>'}
        </div>
        <div class="wf-steps-section">
            <h3>Steps (${steps.length})</h3>
            <div class="wf-step-list">${stepsHtml}</div>
        </div>
        <div class="wf-runs-section">
            <h3>Recent Runs</h3>
            ${runsHtml}
        </div>
    `;
}

// ── Run detail ─────────────────────────────────────────────────────────

export async function selectWorkflowRun(runId) {
    stopRunPolling();

    const run = await fetchRunDetail(runId);
    if (!run) return;

    document.getElementById('wf-list-container').style.display = 'none';
    document.getElementById('wf-detail-container').style.display = 'none';
    document.getElementById('wf-run-detail-container').style.display = '';
    document.getElementById('wf-back-btn').style.display = '';
    document.getElementById('wf-create-btn').style.display = 'none';
    document.getElementById('wf-build-agent-btn').style.display = 'none';

    renderRunDetail(run);

    // Poll if running
    if (run.status === 'running' || run.status === 'pending') {
        currentRunPollTimer = setInterval(async () => {
            const updated = await fetchRunDetail(runId);
            if (!updated) { stopRunPolling(); return; }
            renderRunDetail(updated);
            if (updated.status !== 'running' && updated.status !== 'pending') {
                stopRunPolling();
                // Refresh workflow list data in background
                fetchWorkflows();
            }
        }, 3000);
    }
}

function stopRunPolling() {
    if (currentRunPollTimer) {
        clearInterval(currentRunPollTimer);
        currentRunPollTimer = null;
    }
}

function renderRunDetail(run) {
    const container = document.getElementById('wf-run-detail');
    if (!container) return;

    const steps = run.steps || [];
    const completedCount = steps.filter(s => s.status === 'completed').length;
    const totalSteps = steps.length;
    const progressPct = totalSteps > 0 ? Math.round((completedCount / totalSteps) * 100) : 0;
    const isActive = run.status === 'running' || run.status === 'pending';

    // Overall run duration
    let durationHtml = '';
    if (run.started_at) {
        const endTime = run.finished_at ? new Date(run.finished_at) : new Date();
        const durSec = Math.round((endTime - new Date(run.started_at)) / 1000);
        durationHtml = `<span><span class="material-icons">hourglass_empty</span> ${formatDuration(durSec)}</span>`;
    }

    // Progress bar
    const progressBarHtml = totalSteps > 0 ? `
        <div class="wf-progress">
            <div class="wf-progress-bar">
                <div class="wf-progress-fill ${esc(run.status)}" style="width:${progressPct}%"></div>
            </div>
            <span class="wf-progress-text">${completedCount}/${totalSteps} steps</span>
        </div>` : '';

    // Step timeline
    const stepsHtml = steps.map((s, i) => {
        const icon = statusIconChar(s.status);
        const iconClass = s.status || 'pending';
        const isLast = i === steps.length - 1;

        let timingHtml = '';
        if (s.started_at && s.finished_at) {
            const dur = Math.round((new Date(s.finished_at) - new Date(s.started_at)) / 1000);
            timingHtml = `<span class="wf-run-step-timing">${formatDuration(dur)}</span>`;
        } else if (s.started_at) {
            const elapsed = Math.round((new Date() - new Date(s.started_at)) / 1000);
            timingHtml = `<span class="wf-run-step-timing wf-timing-active">${formatDuration(elapsed)}...</span>`;
        }

        const isFailed = s.status === 'failed' || s.status === 'killed';

        // Exit code badge for shell steps
        let exitCodeHtml = '';
        if (s.type === 'shell' && s.exit_code != null) {
            const ecClass = s.exit_code === 0 ? 'wf-exit-ok' : 'wf-exit-err';
            exitCodeHtml = `<span class="wf-exit-code ${ecClass}">exit ${s.exit_code}</span>`;
        }

        // Output section — auto-expanded for failed steps
        let outputHtml = '';
        if (s.output_tail) {
            const outputId = `wf-step-output-${run.id}-${i}`;
            const autoExpand = isFailed ? ' wf-expanded' : '';
            outputHtml = `
                <div class="wf-run-step-output-toggle" onclick="document.getElementById('${outputId}').classList.toggle('wf-expanded')">
                    <span class="material-icons" style="font-size:14px">terminal</span> Output
                    <span class="material-icons wf-toggle-arrow" style="font-size:14px">expand_more</span>
                </div>
                <div id="${outputId}" class="wf-run-step-output${autoExpand}">${esc(s.output_tail)}</div>`;
        }

        // File list for steps with artifacts
        let filesHtml = '';
        const files = s.files || [];
        if (files.length > 0) {
            const fileItems = files.map(f => `<span class="wf-step-file">${esc(f)}</span>`).join('');
            filesHtml = `<div class="wf-step-files">
                <span class="material-icons" style="font-size:13px;color:var(--text-muted)">folder_open</span>
                ${fileItems}
            </div>`;
        }

        // Session link for agent steps
        let agentHtml = '';
        if (s.type === 'agent' && s.session_id) {
            agentHtml = `<span class="wf-step-agent-link" onclick="event.stopPropagation(); selectLiveSession('${esc(s.session_name || s.session_id)}')">
                <span class="material-icons" style="font-size:13px">open_in_new</span> View agent
            </span>`;
        }

        return `<div class="wf-run-step ${isLast ? '' : 'wf-run-step-connected'}">
            <div class="wf-timeline-node ${iconClass}"></div>
            <div class="wf-run-step-content${isFailed ? ' wf-step-failed' : ''}">
                <div class="wf-run-step-header">
                    <span class="wf-run-step-status ${iconClass}">${icon}</span>
                    <span class="wf-run-step-name">${esc(s.name || 'Step ' + (s.index != null ? s.index : i))}</span>
                    <span class="wf-step-type">${esc(s.type || '')}</span>
                    ${exitCodeHtml}
                    ${agentHtml}
                    ${timingHtml}
                </div>
                ${outputHtml}
                ${filesHtml}
            </div>
        </div>`;
    }).join('');

    const killBtn = isActive
        ? `<button class="btn btn-sm" style="color:var(--error)" onclick="killWorkflowRun(${run.id})">
            <span class="material-icons" style="font-size:14px">stop</span> Kill
          </button>`
        : '';

    const retriggerBtn = !isActive && run.workflow_id
        ? `<button class="btn btn-sm" onclick="triggerWorkflow(${run.workflow_id})">
            <span class="material-icons" style="font-size:14px">replay</span> Re-run
          </button>`
        : '';

    container.innerHTML = `
        <div class="wf-run-header">
            <h3 class="wf-run-title">Run #${run.id} <span style="color:var(--text-secondary);font-weight:400">— ${esc(run.workflow_name || '')}</span></h3>
            <div style="display:flex;gap:6px">${retriggerBtn}${killBtn}</div>
        </div>
        <div class="wf-run-meta">
            <span><span class="wf-status-badge ${esc(run.status)}">${esc(run.status)}</span></span>
            <span><span class="material-icons">bolt</span> ${esc(run.trigger_type || 'api')}</span>
            ${run.started_at ? `<span><span class="material-icons">schedule</span> ${formatTime(run.started_at)}</span>` : ''}
            ${durationHtml}
            ${run.finished_at ? `<span><span class="material-icons">check_circle</span> ${formatTime(run.finished_at)}</span>` : ''}
            ${run.error_msg ? `<span style="color:var(--error)"><span class="material-icons">error</span> ${esc(run.error_msg)}</span>` : ''}
        </div>
        ${progressBarHtml}
        <div class="wf-run-steps wf-timeline">${stepsHtml}</div>
    `;
}

// ── Trigger ────────────────────────────────────────────────────────────

export async function triggerWorkflow(id) {
    try {
        const result = await apiFetch(`/api/workflows/${id}/trigger`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ trigger_type: 'ui' }),
        });
        showToast(`Workflow triggered — run #${result.run_id}`);
        // Navigate to the run detail
        selectWorkflowRun(result.run_id);
    } catch (e) {
        showToast('Failed to trigger workflow: ' + e.message, true);
    }
}

// ── Kill ───────────────────────────────────────────────────────────────

export async function killWorkflowRun(runId) {
    try {
        await apiFetch(`/api/workflows/runs/${runId}/kill`, { method: 'POST' });
        showToast(`Run #${runId} killed`);
        // Refresh
        const updated = await fetchRunDetail(runId);
        if (updated) renderRunDetail(updated);
        stopRunPolling();
    } catch (e) {
        showToast('Failed to kill run: ' + e.message, true);
    }
}

// ── Delete ─────────────────────────────────────────────────────────────

export async function deleteWorkflow(id, name) {
    if (!confirm(`Delete workflow "${name}"? This will also delete all run history.`)) return;
    try {
        await apiFetch(`/api/workflows/${id}`, { method: 'DELETE' });
        showToast(`Deleted workflow "${name}"`);
        workflowsBackToList();
    } catch (e) {
        showToast('Failed to delete: ' + e.message, true);
    }
}

// ── Create / Edit modal ────────────────────────────────────────────────

export function showWorkflowCreateModal() {
    editingWorkflowId = null;
    document.getElementById('wf-modal-title').textContent = 'Create Workflow';
    document.getElementById('wf-modal-save').textContent = 'Create';
    document.getElementById('wf-input-name').value = '';
    document.getElementById('wf-input-desc').value = '';
    document.getElementById('wf-input-repo').value = '';
    document.getElementById('wf-input-max-dur').value = '3600';
    document.getElementById('wf-modal-error').style.display = 'none';
    document.getElementById('wf-steps-editor').innerHTML = '';
    // Add one default step
    workflowAddStep();
    document.getElementById('wf-create-modal').style.display = 'flex';
}

export function hideWorkflowCreateModal() {
    document.getElementById('wf-create-modal').style.display = 'none';
}

export async function editWorkflow(id) {
    const wf = await fetchWorkflowDetail(id);
    if (!wf) return;

    editingWorkflowId = id;
    document.getElementById('wf-modal-title').textContent = 'Edit Workflow';
    document.getElementById('wf-modal-save').textContent = 'Save';
    document.getElementById('wf-input-name').value = wf.name || '';
    document.getElementById('wf-input-desc').value = wf.description || '';
    document.getElementById('wf-input-repo').value = wf.repo_path || '';
    document.getElementById('wf-input-max-dur').value = wf.max_duration_s || 3600;
    document.getElementById('wf-modal-error').style.display = 'none';

    const editor = document.getElementById('wf-steps-editor');
    editor.innerHTML = '';
    const steps = wf.steps || [];
    steps.forEach(s => workflowAddStep(s));

    document.getElementById('wf-create-modal').style.display = 'flex';
}

export function workflowAddStep(step) {
    const editor = document.getElementById('wf-steps-editor');
    const idx = editor.children.length;
    const type = step ? step.type || 'shell' : 'shell';
    const name = step ? step.name || '' : '';
    const content = step ? (type === 'shell' ? step.command || '' : step.prompt || '') : '';

    const div = document.createElement('div');
    div.className = 'wf-step-editor-item';
    div.innerHTML = `
        <div class="wf-step-editor-top">
            <select class="wf-step-type-select" onchange="workflowStepTypeChanged(this)">
                <option value="shell" ${type === 'shell' ? 'selected' : ''}>shell</option>
                <option value="agent" ${type === 'agent' ? 'selected' : ''}>agent</option>
            </select>
            <input type="text" class="wf-step-name-input" placeholder="Step name" value="${escAttr(name)}">
            <button class="wf-step-remove-btn" onclick="this.closest('.wf-step-editor-item').remove()" title="Remove step">&times;</button>
        </div>
        <div class="wf-step-editor-body">
            <textarea class="wf-step-content" placeholder="${type === 'shell' ? 'Shell command...' : 'Agent prompt...'}" rows="2">${esc(content)}</textarea>
        </div>
    `;
    editor.appendChild(div);
}

export function workflowStepTypeChanged(select) {
    const textarea = select.closest('.wf-step-editor-item').querySelector('.wf-step-content');
    textarea.placeholder = select.value === 'shell' ? 'Shell command...' : 'Agent prompt...';
}

export async function saveWorkflow() {
    const name = document.getElementById('wf-input-name').value.trim();
    const desc = document.getElementById('wf-input-desc').value.trim();
    const repo = document.getElementById('wf-input-repo').value.trim();
    const maxDur = parseInt(document.getElementById('wf-input-max-dur').value) || 3600;
    const errEl = document.getElementById('wf-modal-error');

    // Collect steps
    const stepItems = document.querySelectorAll('#wf-steps-editor .wf-step-editor-item');
    const steps = [];
    for (const item of stepItems) {
        const type = item.querySelector('.wf-step-type-select').value;
        const stepName = item.querySelector('.wf-step-name-input').value.trim();
        const content = item.querySelector('.wf-step-content').value.trim();
        if (!stepName) { showError(errEl, 'All steps need a name'); return; }
        if (!content) { showError(errEl, `Step "${stepName}" needs a ${type === 'shell' ? 'command' : 'prompt'}`); return; }

        const step = { name: stepName, type };
        if (type === 'shell') step.command = content;
        else step.prompt = content;
        steps.push(step);
    }

    if (!name) { showError(errEl, 'Name is required'); return; }
    if (!steps.length) { showError(errEl, 'At least one step is required'); return; }

    const body = { name, description: desc, steps, max_duration_s: maxDur };
    if (repo) body.repo_path = repo;

    try {
        if (editingWorkflowId) {
            await apiFetch(`/api/workflows/${editingWorkflowId}`, {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(body),
            });
            showToast(`Updated workflow "${name}"`);
        } else {
            await apiFetch('/api/workflows', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(body),
            });
            showToast(`Created workflow "${name}"`);
        }
        hideWorkflowCreateModal();
        fetchWorkflows();
        if (editingWorkflowId) selectWorkflow(editingWorkflowId);
    } catch (e) {
        let msg = e.message;
        try { const j = JSON.parse(msg.replace(/^\d+:\s*/, '')); msg = j.error || msg; } catch (_) {}
        showError(errEl, msg);
    }
}

// ── Navigation ─────────────────────────────────────────────────────────

export function workflowsBackToList() {
    stopRunPolling();
    selectedWorkflowId = null;
    document.getElementById('wf-list-container').style.display = '';
    document.getElementById('wf-detail-container').style.display = 'none';
    document.getElementById('wf-run-detail-container').style.display = 'none';
    document.getElementById('wf-back-btn').style.display = 'none';
    document.getElementById('wf-create-btn').style.display = '';
    document.getElementById('wf-build-agent-btn').style.display = '';
    document.getElementById('wf-title').textContent = 'Workflows';
    fetchWorkflows();
}

// ── Helpers ────────────────────────────────────────────────────────────

function formatTime(ts) {
    if (!ts) return '';
    try {
        const d = new Date(ts);
        return d.toLocaleString(undefined, {
            month: 'short', day: 'numeric',
            hour: '2-digit', minute: '2-digit',
        });
    } catch (_) {
        return ts.slice(0, 16);
    }
}

function statusIconHtml(status) {
    return `<span class="wf-status-dot ${esc(status)}"></span>`;
}

function statusIconChar(status) {
    switch (status) {
        case 'completed': return '&#10003;';
        case 'running': return '&#9654;';
        case 'failed': return '&#10007;';
        case 'killed': return '!';
        case 'skipped': return '&#8212;';
        default: return '&#8226;';
    }
}

function formatDuration(sec) {
    if (sec < 60) return `${sec}s`;
    const m = Math.floor(sec / 60);
    const s = sec % 60;
    if (m < 60) return s > 0 ? `${m}m ${s}s` : `${m}m`;
    const h = Math.floor(m / 60);
    return `${h}h ${m % 60}m`;
}

function showError(el, msg) {
    el.textContent = msg;
    el.style.display = '';
}
